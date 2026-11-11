package credential

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// Resolution of the effective credentials (CRD-04, E-219, WP-891): for each
// (CI, namespace) the credential of the most specific level wins, in the
// order probe > CI > device group > subnet > scope > client > org. Within a
// level the narrowest subnet comes first (subnet level only), then the
// smallest priority, then the smallest ID (binding, else credential). The
// levels scope, client and org are those of E-67: a covering scope with
// credential_ids offers exactly those (scope level); a scope without them,
// or no covering scope, offers the credentials of the client (client level)
// and the organization-wide ones (org level). A credential of a client acts
// only on CIs of that client, on every level (AK2).

// Origins of an effective credential, most specific first.
const (
	OriginProbe       = "probe"
	OriginCI          = "ci"
	OriginDeviceGroup = "device_group"
	OriginSubnet      = "subnet"
	OriginScope       = "scope"
	OriginClient      = "client"
	OriginOrg         = "org"
)

// Origins are the levels of the resolution, most specific first.
var Origins = []string{OriginProbe, OriginCI, OriginDeviceGroup, OriginSubnet, OriginScope, OriginClient, OriginOrg}

// originRank orders the levels.
func originRank(origin string) int {
	if i := slices.Index(Origins, origin); i >= 0 {
		return i
	}
	return len(Origins)
}

// AboveScope reports whether origin is a level above the scope (probe, ci,
// device_group, subnet): those go into the targets of a bundle; the scope
// level and below the collector resolves from the scope's credential list.
func AboveScope(origin string) bool { return originRank(origin) < originRank(OriginScope) }

// DeviceGroupSource yields the memberships of the device-group level (CI →
// device groups, PRB-01; E-178). Until WP-935 (p3-probe-schema-def) the
// source is empty (NoDeviceGroups); WP-935 supplies it from
// monitoring_group_member.
type DeviceGroupSource interface {
	// MembershipQuery is a SELECT of the pairs (ci_id uuid, group_id uuid)
	// of the organization $1; the resolution uses it as a CTE of its one
	// statement (AK5).
	MembershipQuery() string
	// Memberships returns the groups of the CIs outside the database (memory
	// store, tests).
	Memberships(ctx context.Context, orgID string, ciIDs []string) (map[string][]string, error)
}

// NoDeviceGroups is the empty membership source (until WP-935).
type NoDeviceGroups struct{}

// MembershipQuery implements DeviceGroupSource: no pairs.
func (NoDeviceGroups) MembershipQuery() string {
	return `SELECT NULL::uuid AS ci_id, NULL::uuid AS group_id WHERE false`
}

// Memberships implements DeviceGroupSource: no groups.
func (NoDeviceGroups) Memberships(context.Context, string, []string) (map[string][]string, error) {
	return map[string][]string{}, nil
}

// deviceGroupBindingColumn is the column of credential_binding that names
// the device group of a binding. The column comes with WP-935
// (device_group_id → monitoring_group); until then no binding has one.
const deviceGroupBindingColumn = "NULL::uuid"

// CredentialRef names a credential without its secret.
type CredentialRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// ResolvedCredential is a candidate of a namespace: the credential, its
// level and the binding (probe, ci, device_group, subnet), scope (scope) or
// client (client) it comes from; org has no source.
type ResolvedCredential struct {
	Credential CredentialRef `json:"credential"`
	Origin     string        `json:"origin"`
	SourceID   string        `json:"source_id,omitempty"`
	// Priority is that of the binding; the levels scope, client and org
	// have none.
	Priority *int `json:"priority,omitempty"`
}

// EffectiveCredential is the winning credential of a namespace and the
// candidates it overrides, in resolution order (never a secret).
type EffectiveCredential struct {
	Namespace string `json:"namespace"`
	ResolvedCredential
	Overridden []ResolvedCredential `json:"overridden"`
}

// candidate is a resolution row: a credential offered to a CI in a
// namespace by a level.
type candidate struct {
	ResolvedCredential
	ciID string
	// scopeID is the covering scope the row was resolved in (empty: none);
	// the subnet level depends on its VRF.
	scopeID      string
	managementIP string
	namespace    string
	// specificity is the prefix length of a subnet (the narrowest wins).
	specificity int
}

func candidateLess(a, b *candidate) bool {
	if ra, rb := originRank(a.Origin), originRank(b.Origin); ra != rb {
		return ra < rb
	}
	if a.specificity != b.specificity {
		return a.specificity > b.specificity
	}
	pa, pb := 0, 0
	if a.Priority != nil {
		pa = *a.Priority
	}
	if b.Priority != nil {
		pb = *b.Priority
	}
	if pa != pb {
		return pa < pb
	}
	if a.SourceID != b.SourceID && a.SourceID != "" && b.SourceID != "" && AboveScope(a.Origin) {
		return a.SourceID < b.SourceID
	}
	return a.Credential.ID < b.Credential.ID
}

// rank resolves the candidates of one CI: per namespace the first in
// resolution order wins, the others are overridden; namespaces in
// alphabetical order.
func rank(cands []candidate) []EffectiveCredential {
	byNS := map[string][]candidate{}
	for i := range cands {
		if cands[i].namespace == "" {
			continue
		}
		byNS[cands[i].namespace] = append(byNS[cands[i].namespace], cands[i])
	}
	namespaces := make([]string, 0, len(byNS))
	for ns := range byNS {
		namespaces = append(namespaces, ns)
	}
	sort.Strings(namespaces)
	out := make([]EffectiveCredential, 0, len(namespaces))
	for _, ns := range namespaces {
		list := byNS[ns]
		sort.SliceStable(list, func(i, j int) bool { return candidateLess(&list[i], &list[j]) })
		e := EffectiveCredential{Namespace: ns, ResolvedCredential: list[0].ResolvedCredential, Overridden: []ResolvedCredential{}}
		for i := 1; i < len(list); i++ {
			e.Overridden = append(e.Overridden, list[i].ResolvedCredential)
		}
		out = append(out, e)
	}
	return out
}

// rankByCI groups the candidates by CI and ranks each group.
func rankByCI(cands []candidate) map[string][]EffectiveCredential {
	return rankBy(cands, func(c *candidate) string { return c.ciID })
}

// rankBy groups the candidates by key and ranks each group.
func rankBy(cands []candidate, key func(*candidate) string) map[string][]EffectiveCredential {
	groups := map[string][]candidate{}
	for i := range cands {
		k := key(&cands[i])
		groups[k] = append(groups[k], cands[i])
	}
	out := make(map[string][]EffectiveCredential, len(groups))
	for k, list := range groups {
		out[k] = rank(list)
	}
	return out
}

// --- resolution in memory ---------------------------------------------------

// ResolveScope is the discovery scope covering a CI.
type ResolveScope struct {
	ID       string
	ClientID string
	// VRFID is the VRF of the scope; empty is the default VRF.
	VRFID         string
	CredentialIDs []string
}

// ResolveCI is a CI as the resolution sees it.
type ResolveCI struct {
	ID       string
	ClientID string
	// ManagementIP is the CI's management address; the zero value has
	// none (no subnet level).
	ManagementIP netip.Addr
	// Scope is the scope covering the CI; nil is none.
	Scope *ResolveScope
}

// ResolveSubnet is a subnet with its VRF (empty: the default VRF).
type ResolveSubnet struct {
	ID     string
	VRFID  string
	Prefix netip.Prefix
}

// ResolveData is what the resolution reads: the credentials (metadata), the
// bindings and the subnets of the organization.
type ResolveData struct {
	OrganizationID string
	Credentials    []Credential
	Bindings       []Binding
	Subnets        []ResolveSubnet
}

// Resolve returns the effective credentials of each CI (CI ID → one entry
// per namespace) from data, the device-group level from groups (nil: none).
// It is the resolution of the memory store and the reference of the
// set-based statement of the PostgreSQL store (ResolveCollector,
// EffectiveCredentials), which applies the same rules.
func Resolve(ctx context.Context, data *ResolveData, groups DeviceGroupSource, cis []ResolveCI) (map[string][]EffectiveCredential, error) {
	if groups == nil {
		groups = NoDeviceGroups{}
	}
	ids := make([]string, 0, len(cis))
	for i := range cis {
		ids = append(ids, cis[i].ID)
	}
	members, err := groups.Memberships(ctx, data.OrganizationID, ids)
	if err != nil {
		return nil, fmt.Errorf("device group memberships: %w", err)
	}
	creds := make(map[string]*Credential, len(data.Credentials))
	for i := range data.Credentials {
		creds[data.Credentials[i].ID] = &data.Credentials[i]
	}
	subnets := make(map[string]*ResolveSubnet, len(data.Subnets))
	for i := range data.Subnets {
		subnets[data.Subnets[i].ID] = &data.Subnets[i]
	}
	var cands []candidate
	for i := range cis {
		ci := &cis[i]
		offer := func(c *Credential, origin, source, namespace string, priority *int, specificity int) {
			// A credential of a client acts only on CIs of that client.
			if c == nil || (c.ClientID != "" && c.ClientID != ci.ClientID) {
				return
			}
			cands = append(cands, candidate{ResolvedCredential: ResolvedCredential{
				Credential: CredentialRef{ID: c.ID, Name: c.Name, Kind: c.Kind}, Origin: origin, SourceID: source, Priority: priority},
				ciID: ci.ID, managementIP: addrString(ci.ManagementIP), namespace: namespace, specificity: specificity})
		}
		vrf := ""
		if ci.Scope != nil {
			vrf = ci.Scope.VRFID
		}
		for j := range data.Bindings {
			b := &data.Bindings[j]
			p := b.Priority
			switch b.TargetKind {
			case TargetCI:
				if b.TargetID == ci.ID {
					offer(creds[b.CredentialID], OriginCI, b.ID, b.Namespace, &p, 0)
				}
			case TargetDeviceGroup:
				if slices.Contains(members[ci.ID], b.TargetID) {
					offer(creds[b.CredentialID], OriginDeviceGroup, b.ID, b.Namespace, &p, 0)
				}
			case TargetSubnet:
				// The subnets of the CI's VRF (that of its scope) that
				// contain its management address.
				s := subnets[b.TargetID]
				if s != nil && ci.ManagementIP.IsValid() && s.VRFID == vrf && s.Prefix.Contains(ci.ManagementIP) {
					offer(creds[b.CredentialID], OriginSubnet, b.ID, b.Namespace, &p, s.Prefix.Bits())
				}
			}
		}
		offerLower(ci, data.Credentials, offer)
	}
	return rankByCI(cands), nil
}

// offerLower offers the levels of E-67: the scope's credential list, or
// without one the credentials of the client and the organization-wide ones.
func offerLower(ci *ResolveCI, creds []Credential, offer func(*Credential, string, string, string, *int, int)) {
	if ci.Scope != nil && len(ci.Scope.CredentialIDs) > 0 {
		for i := range creds {
			c := &creds[i]
			if slices.Contains(ci.Scope.CredentialIDs, c.ID) && (c.ClientID == "" || c.ClientID == ci.Scope.ClientID) {
				offer(c, OriginScope, ci.Scope.ID, KindNamespace[c.Kind], nil, 0)
			}
		}
		return
	}
	client := ci.ClientID
	if ci.Scope != nil {
		client = ci.Scope.ClientID
	}
	for i := range creds {
		c := &creds[i]
		switch c.ClientID {
		case "":
			offer(c, OriginOrg, "", KindNamespace[c.Kind], nil, 0)
		case client:
			offer(c, OriginClient, c.ClientID, KindNamespace[c.Kind], nil, 0)
		}
	}
}

func addrString(a netip.Addr) string {
	if !a.IsValid() {
		return ""
	}
	return a.String()
}

// --- resolution on PostgreSQL -------------------------------------------------

// scopeMatch is the condition that the scope s covers the CI c (as in the
// offline detector, internal/lifecycle/offline.go): a CIDR or range target
// contains the management address, a host target is the address or the
// host name; invalid targets cover nothing.
const scopeMatch = `(
	(s.kind = 'cidr' AND CASE WHEN strpos(s.target, '%') = 0 AND pg_input_is_valid(s.target, 'inet')
		THEN c.management_ip <<= s.target::inet ELSE false END)
	OR (s.kind = 'host' AND strpos(s.target, '%') = 0
		AND (host(c.management_ip) = s.target OR c.hostname_lc = s.target OR c.fqdn_lc = s.target))
	OR (s.kind = 'range' AND CASE WHEN s.target ~ '^[^-%/]+-[^-%/]+$' AND pg_input_is_valid(split_part(s.target, '-', 1), 'inet')
			AND pg_input_is_valid(split_part(s.target, '-', 2), 'inet')
		THEN family(c.management_ip) = family(split_part(s.target, '-', 1)::inet)
			AND host(c.management_ip)::inet BETWEEN split_part(s.target, '-', 1)::inet AND split_part(s.target, '-', 2)::inet
		ELSE false END))`

// collectorCIs are the CIs of the collector's enabled scopes ($2) with a
// management address, one row per covering scope of the collector and CI:
// the collector applies a target only in the pass of its scope, and the
// subnet level depends on the scope's VRF (two scopes may hold the same
// address in different VRFs or clients). A scope of a client covers only
// CIs of that client, a collector of a client ($3) only CIs of its client.
const collectorCIs = `cis AS (
	SELECT c.id, c.client_id, c.management_ip, s.id AS scope_id, s.client_id AS scope_client,
		COALESCE(s.vrf_id, default_vrf_of(s.organization_id)) AS vrf_id, s.credential_ids
	FROM discovery_scope s
	JOIN ci c ON c.organization_id = s.organization_id AND c.deleted_at IS NULL AND c.management_ip IS NOT NULL AND ` + scopeMatch + `
	WHERE s.organization_id = $1 AND s.collector_id = $2 AND s.enabled
	  AND (s.client_id IS NULL OR c.client_id = s.client_id)
	  AND ($3::uuid IS NULL OR c.client_id = $3::uuid))`

// oneCI is the CI $2 with its first covering enabled scope (any collector).
const oneCI = `cis AS (
	SELECT c.id, c.client_id, c.management_ip, sc.id AS scope_id, sc.client_id AS scope_client,
		COALESCE(sc.vrf_id, default_vrf_of(c.organization_id)) AS vrf_id, sc.credential_ids
	FROM ci c
	LEFT JOIN LATERAL (
		SELECT s.id, s.client_id, s.vrf_id, s.credential_ids FROM discovery_scope s
		WHERE s.organization_id = c.organization_id AND s.enabled AND ` + scopeMatch + `
		  AND (s.client_id IS NULL OR c.client_id = s.client_id)
		ORDER BY s.id LIMIT 1) sc ON true
	WHERE c.organization_id = $1 AND c.id = $2::uuid AND c.deleted_at IS NULL)`

// aboveScopeCandidates are the bindings of the levels ci, device_group and
// subnet (probe follows with WP-935): the subnets of the CI's VRF that
// contain its management address, with their prefix length.
const aboveScopeCandidates = `
	SELECT cis.id AS ci_id, cis.scope_id, 'ci'::text AS origin, b.id AS source_id, b.credential_id, b.namespace, b.priority, 0 AS specificity
	FROM cis JOIN credential_binding b ON b.organization_id = $1 AND b.ci_id = cis.id
	UNION ALL
	SELECT cis.id, cis.scope_id, 'device_group', b.id, b.credential_id, b.namespace, b.priority, 0
	FROM members m JOIN cis ON cis.id = m.ci_id
	JOIN credential_binding b ON b.organization_id = $1 AND b.target_kind = 'device_group' AND ` + deviceGroupBindingColumn + ` = m.group_id
	UNION ALL
	SELECT cis.id, cis.scope_id, 'subnet', b.id, b.credential_id, b.namespace, b.priority, masklen(sn.cidr)
	FROM cis JOIN subnet sn ON sn.organization_id = $1 AND sn.vrf_id = cis.vrf_id AND cis.management_ip <<= sn.cidr
	JOIN credential_binding b ON b.organization_id = $1 AND b.subnet_id = sn.id`

// lowerCandidates are the levels of E-67: the scope's list, or without one
// the credentials of the client (of the scope, without a scope of the CI)
// and the organization-wide ones; the namespace follows the kind (Go).
const lowerCandidates = `
	UNION ALL
	SELECT cis.id, cis.scope_id, CASE WHEN cardinality(cis.credential_ids) > 0 THEN 'scope' WHEN cr.client_id IS NOT NULL THEN 'client' ELSE 'org' END,
		CASE WHEN cardinality(cis.credential_ids) > 0 THEN cis.scope_id WHEN cr.client_id IS NOT NULL THEN cr.client_id END,
		cr.id, NULL::text, NULL::integer, 0
	FROM cis JOIN credential cr ON cr.organization_id = $1 AND CASE WHEN cardinality(cis.credential_ids) > 0
		THEN cr.id = ANY (cis.credential_ids) AND (cr.client_id IS NULL OR cr.client_id = cis.scope_client)
		ELSE cr.client_id IS NULL OR cr.client_id = CASE WHEN cis.scope_id IS NULL THEN cis.client_id ELSE cis.scope_client END END`

// resolveSelect reads the candidates with their credential, each with the
// row of cis (CI and scope) it was resolved for; a credential of
// a client acts only on CIs of that client and reaches a collector of a
// client ($3) only if it is that client's (AK2).
const resolveSelect = `
SELECT cand.ci_id::text, COALESCE(cand.scope_id::text, ''), COALESCE(host(cis.management_ip), ''), cand.origin, COALESCE(cand.source_id::text, ''),
	cr.id::text, cr.name, cr.kind, COALESCE(cand.namespace, ''), cand.priority, cand.specificity
FROM cand JOIN cis ON cis.id = cand.ci_id AND cis.scope_id IS NOT DISTINCT FROM cand.scope_id
JOIN credential cr ON cr.organization_id = $1 AND cr.id = cand.credential_id
WHERE (cr.client_id IS NULL OR cr.client_id = cis.client_id)
  AND ($3::uuid IS NULL OR cr.client_id IS NULL OR cr.client_id = $3::uuid)`

func resolveStatement(cis string, lower bool, groups DeviceGroupSource) string {
	var sb strings.Builder
	sb.WriteString("WITH ")
	sb.WriteString(cis)
	sb.WriteString(",\nmembers AS (")
	sb.WriteString(groups.MembershipQuery())
	sb.WriteString("),\ncand AS (")
	sb.WriteString(aboveScopeCandidates)
	if lower {
		sb.WriteString(lowerCandidates)
	}
	sb.WriteString(")")
	sb.WriteString(resolveSelect)
	return sb.String()
}

func queryCandidates(ctx context.Context, tx pgx.Tx, stmt string, args ...any) ([]candidate, error) {
	rows, err := tx.Query(ctx, stmt, args...)
	if err != nil {
		return nil, fmt.Errorf("resolve credentials: %w", err)
	}
	defer rows.Close()
	var out []candidate
	for rows.Next() {
		var c candidate
		var priority *int
		if err = rows.Scan(&c.ciID, &c.scopeID, &c.managementIP, &c.Origin, &c.SourceID, &c.Credential.ID, &c.Credential.Name,
			&c.Credential.Kind, &c.namespace, &priority, &c.specificity); err != nil {
			return nil, fmt.Errorf("scan credential candidate: %w", err)
		}
		c.Priority = priority
		if c.namespace == "" {
			c.namespace = KindNamespace[c.Credential.Kind]
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func groupsOrNone(groups DeviceGroupSource) DeviceGroupSource {
	if groups == nil {
		return NoDeviceGroups{}
	}
	return groups
}

// BundleTargetCredential is an entry of the targets of a bundle (format 2):
// the credential of a CI in a namespace from a level above the scope, as
// resolved in the scope ScopeID of the collector. The collector applies it
// only in the pass of that scope (an address may belong to another device
// in another scope, VRF or client).
type BundleTargetCredential struct {
	CIID         string `json:"ci_id"`
	ScopeID      string `json:"scope_id"`
	ManagementIP string `json:"management_ip"`
	Namespace    string `json:"namespace"`
	CredentialID string `json:"credential_id"`
	Origin       string `json:"origin"`
}

// ResolveCollector returns the targets of the collector's bundle in tx (the
// organization-wide system scope of VerifyBundleTarget): for every CI whose
// management address lies in a target of an enabled scope of the collector,
// the namespaces whose effective credential comes from a level above the
// scope, once per covering scope of the collector. clientID is the collector's client (empty: organization-wide); a
// credential of a client reaches it only for CIs of that client. One
// statement for all CIs (AK5).
func ResolveCollector(ctx context.Context, tx pgx.Tx, orgID, collectorID, clientID string, groups DeviceGroupSource) ([]BundleTargetCredential, error) {
	cands, err := queryCandidates(ctx, tx, resolveStatement(collectorCIs, false, groupsOrNone(groups)), orgID, collectorID, nilIfEmpty(clientID))
	if err != nil {
		return nil, err
	}
	type ciScope struct{ ci, scope, ip string }
	rows := map[string]ciScope{}
	key := func(c *candidate) string { return c.ciID + "|" + c.scopeID }
	for i := range cands {
		rows[key(&cands[i])] = ciScope{ci: cands[i].ciID, scope: cands[i].scopeID, ip: cands[i].managementIP}
	}
	out := []BundleTargetCredential{}
	for k, effective := range rankBy(cands, key) {
		row := rows[k]
		for i := range effective {
			e := &effective[i]
			if !AboveScope(e.Origin) {
				continue
			}
			out = append(out, BundleTargetCredential{CIID: row.ci, ScopeID: row.scope, ManagementIP: row.ip, Namespace: e.Namespace,
				CredentialID: e.Credential.ID, Origin: e.Origin})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CIID != out[j].CIID {
			return out[i].CIID < out[j].CIID
		}
		if out[i].ScopeID != out[j].ScopeID {
			return out[i].ScopeID < out[j].ScopeID
		}
		return out[i].Namespace < out[j].Namespace
	})
	return out, nil
}

// EffectiveResolver resolves the effective credentials of one CI in the
// caller's scope; PGRepository implements it, the memory store resolves
// with Resolve.
type EffectiveResolver interface {
	EffectiveCredentials(ctx context.Context, orgID, ciID string, groups DeviceGroupSource) ([]EffectiveCredential, error)
}

// EffectiveCredentials implements EffectiveResolver: the CI in the caller's
// scope (RLS; an invisible or unknown CI is ErrBindingNotFound), its first
// covering enabled scope and every candidate of its levels.
func (r *PGRepository) EffectiveCredentials(ctx context.Context, orgID, ciID string, groups DeviceGroupSource) ([]EffectiveCredential, error) {
	if !api.IsUUID(ciID) {
		return nil, ErrBindingNotFound
	}
	var out []EffectiveCredential
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ci WHERE id = $1 AND deleted_at IS NULL)`, ciID).Scan(&exists); err != nil {
			return fmt.Errorf("check ci: %w", err)
		}
		if !exists {
			return ErrBindingNotFound
		}
		cands, err := queryCandidates(ctx, tx, resolveStatement(oneCI, true, groupsOrNone(groups)), orgID, ciID, nil)
		if err != nil {
			return err
		}
		out = rank(cands)
		return nil
	})
	return out, err
}

// Effective returns the effective credentials of a CI (GET
// /cis/{id}/credentials/effective): one entry per namespace with the
// overridden candidates, never a secret.
func (b *BindingService) Effective(ctx context.Context, orgID, ciID string) ([]EffectiveCredential, error) {
	if r, ok := b.bindings.(EffectiveResolver); ok {
		return r.EffectiveCredentials(ctx, orgID, ciID, b.groups)
	}
	// Memory store (--no-db): it knows no CI attributes, scopes or
	// subnets; the CI has no client, so the levels are ci and org.
	visible, err := b.bindings.CIVisible(ctx, orgID, ciID)
	if err != nil {
		return nil, err
	}
	if !visible {
		return nil, ErrBindingNotFound
	}
	creds, err := b.creds.repo.List(ctx, orgID)
	if err != nil {
		return nil, err
	}
	bindings, err := b.bindings.ListBindings(ctx, orgID, BindingFilter{TargetKind: TargetCI, TargetID: ciID})
	if err != nil {
		return nil, err
	}
	res, err := Resolve(ctx, &ResolveData{OrganizationID: orgID, Credentials: creds, Bindings: bindings}, b.groups, []ResolveCI{{ID: ciID}})
	if err != nil {
		return nil, err
	}
	if res[ciID] == nil {
		return []EffectiveCredential{}, nil
	}
	return res[ciID], nil
}

// WithDeviceGroups sets the membership source of the device-group level
// (WP-935); without one the level is empty.
func (s *Service) WithDeviceGroups(groups DeviceGroupSource) *Service {
	s.bindings.groups = groupsOrNone(groups)
	return s
}

var (
	_ EffectiveResolver = (*PGRepository)(nil)
	_ DeviceGroupSource = NoDeviceGroups{}
)
