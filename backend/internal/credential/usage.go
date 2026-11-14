package credential

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// Usage of a credential per CI and namespace (CRD-06, WP-898): the
// collectors report with every scope run which credential they used for
// which target and namespace, and whether the login worked (never a value
// of the secret). RecordUsage keeps the last success and the last failure
// with its error class per key in credential_usage and raises
// credential.resolution_failed for a refused login or an unusable vault
// reference, at most once per key and hour (interval '1 hour' in the
// upsert of RecordUsage, measured on the server's clock).

// Error classes of a failed use (credential_usage_error_class_check).
const (
	UsageAuthFailed       = "auth_failed"
	UsageTimeout          = "timeout"
	UsageUnreachable      = "unreachable"
	UsageTLS              = "tls"
	UsageVaultUnreachable = "vault_unreachable"
	UsageVaultDenied      = "vault_denied"
	UsageVaultNotFound    = "vault_not_found"
	UsageFieldMissing     = "field_missing"
)

// UsageErrorClasses are the error classes credential_usage stores.
var UsageErrorClasses = map[string]bool{
	UsageAuthFailed: true, UsageTimeout: true, UsageUnreachable: true, UsageTLS: true,
	UsageVaultUnreachable: true, UsageVaultDenied: true, UsageVaultNotFound: true, UsageFieldMissing: true,
}

// usageEventClasses raise credential.resolution_failed (AK4): the target
// refused the login, or the vault reference could not be resolved.
var usageEventClasses = map[string]bool{
	UsageAuthFailed: true, UsageVaultUnreachable: true, UsageVaultDenied: true, UsageVaultNotFound: true,
}

// MaxUsageEntries bounds the usage entries of one run report; the
// collector sends no more, the server ignores the rest.
const MaxUsageEntries = 1000

// UsageReport is one usage entry of a run report: the credential the
// collector used for a target in a namespace, the CI when the bundle named
// it, and whether the use succeeded; Cause is the error class of a failed
// use.
type UsageReport struct {
	Target       string `json:"target"`
	CIID         string `json:"ci_id,omitempty"`
	Namespace    string `json:"namespace"`
	CredentialID string `json:"credential_id"`
	OK           bool   `json:"ok"`
	Cause        string `json:"cause,omitempty"`
}

// UsageBundle is what the reporting collector's bundle held: the
// credential IDs and the per-device targets (format 2, WP-891).
type UsageBundle struct {
	CredentialIDs map[string]bool
	Targets       []BundleTargetCredential
}

// UsageInput is a batch of usage entries of one scope run.
type UsageInput struct {
	OrganizationID string
	// ScopeID and ScopeClientID are the reporting scope and its client
	// (empty: none); a target address resolves to a CI of that client.
	ScopeID       string
	ScopeClientID string
	Bundle        UsageBundle
	// FromProbes marks the entries of the probe ingest (WP-960 AK7): the CI
	// is the probe's (CIID, checked to exist in the organization) instead
	// of a bundle target, and the credential need not be in a bundle of a
	// scope; it must exist in the organization, and a client's credential
	// still records nothing at a CI of another client.
	FromProbes bool
	Entries    []UsageReport
	// At is the time of the use (the run, the collector's clock); zero is
	// now. It sets last_success_at and last_failure_at.
	At time.Time
	// Now is the server's clock the event throttle is measured on; zero is
	// time.Now(). The collector's clock never decides the throttle: a
	// repeated report or a clock ahead or behind cannot skip or block it.
	Now time.Time
}

// UsageOutcome counts what RecordUsage did with a batch.
type UsageOutcome struct {
	Recorded int
	// Unmatched entries had no CI; Rejected entries named a credential
	// outside the bundle (of the reporting scope), of another client than
	// the CI, or carried an invalid namespace or class.
	Unmatched int
	Rejected  int
	Events    int
}

var (
	usageUnmatched = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "reticora", Name: "credential_usage_unmatched_total",
		Help: "Credential usage entries of collector runs dropped because no CI matched their target (CRD-06).",
	})
	usageRejected = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "reticora", Name: "credential_usage_rejected_total",
		Help: "Credential usage entries of collector runs rejected: credential outside the collector's bundle, invalid namespace or error class (CRD-06).",
	})
)

// UsageMetrics are the metrics of the usage recording.
func UsageMetrics() []prometheus.Collector {
	return []prometheus.Collector{usageUnmatched, usageRejected}
}

// usageNamespaces are the namespaces a usage row may name (the protocol
// namespaces of the collector).
var usageNamespaces = map[string]bool{
	"snmp": true, "ssh": true, "redfish": true, "ipmi": true, "wmi": true, "api": true, "nut": true, "nas": true,
	"http": true, "gnmi": true, "acs": true,
}

// usageKey identifies a credential_usage row.
type usageKey struct{ credential, ci, namespace string }

// canonicalAddr returns the canonical form of an address ("" for none or a
// name).
func canonicalAddr(s string) string {
	a, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil {
		return ""
	}
	return a.Unmap().String()
}

// RecordUsage stores the usage entries of a run in tx, the transaction of
// the run (an organization-wide system scope): per credential, CI and
// namespace a success sets last_success_at, a failure last_failure_at and
// last_error_class. The CI comes from the bundle target of the scope, else
// from the CIs with the target as management address in the scope's
// client; an entry without CI is dropped and counted
// (reticora_credential_usage_unmatched_total). A credential that was not
// in the bundle of the reporting scope is rejected, as is a client's
// credential at a CI of another client. auth_failed and the vault classes
// append credential.resolution_failed through the outbox, at most once per
// key and hour (last_event_at), as the last writes before the commit
// (Konvention 20).
func RecordUsage(ctx context.Context, tx pgx.Tx, in *UsageInput) (UsageOutcome, error) {
	var out UsageOutcome
	at := in.At
	if at.IsZero() {
		at = time.Now()
	}
	at = at.UTC().Truncate(time.Microsecond)
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC().Truncate(time.Microsecond)
	entries := in.Entries
	if len(entries) > MaxUsageEntries {
		entries = entries[:MaxUsageEntries]
	}

	// The bundle targets of the reporting scope by address and CI.
	byIP, byCI := map[string]string{}, map[string]bool{}
	ambiguous := map[string]bool{}
	for i := range in.Bundle.Targets {
		t := &in.Bundle.Targets[i]
		if t.ScopeID != in.ScopeID {
			continue
		}
		byCI[t.CIID] = true
		if ip := canonicalAddr(t.ManagementIP); ip != "" {
			if owner, seen := byIP[ip]; seen && owner != t.CIID {
				ambiguous[ip] = true
			}
			byIP[ip] = t.CIID
		}
	}
	for ip := range ambiguous {
		delete(byIP, ip)
	}

	valid := make([]UsageReport, 0, len(entries))
	var lookupIPs, lookupCIs, credIDs []string
	for _, e := range entries {
		switch {
		case !api.IsUUID(e.CredentialID) || (!in.FromProbes && !in.Bundle.CredentialIDs[e.CredentialID]):
			out.Rejected++
			continue
		case !usageNamespaces[e.Namespace]:
			out.Rejected++
			continue
		case !e.OK && !UsageErrorClasses[e.Cause]:
			out.Rejected++
			continue
		}
		if e.CIID != "" && !api.IsUUID(e.CIID) {
			e.CIID = ""
		}
		valid = append(valid, e)
		credIDs = append(credIDs, e.CredentialID)
		if e.CIID != "" && !byCI[e.CIID] {
			lookupCIs = append(lookupCIs, e.CIID)
		}
		if ip := canonicalAddr(e.Target); ip != "" && byIP[ip] == "" {
			lookupIPs = append(lookupIPs, ip)
		}
	}
	usageRejected.Add(float64(out.Rejected))

	// CIs named by the collector outside the scope's targets must exist in
	// the scope's client; addresses resolve to exactly one CI of it.
	knownCIs, err := visibleCIs(ctx, tx, in.OrganizationID, in.ScopeClientID, lookupCIs)
	if err != nil {
		return out, err
	}
	ciByIP, err := cisByAddress(ctx, tx, in.OrganizationID, in.ScopeClientID, lookupIPs)
	if err != nil {
		return out, err
	}
	creds, err := usageCredentials(ctx, tx, in.OrganizationID, credIDs)
	if err != nil {
		return out, err
	}
	ciIDs := make([]string, 0, len(byCI)+len(knownCIs)+len(ciByIP))
	for id := range byCI {
		ciIDs = append(ciIDs, id)
	}
	for id := range knownCIs {
		ciIDs = append(ciIDs, id)
	}
	for _, id := range ciByIP {
		ciIDs = append(ciIDs, id)
	}
	ciClients, err := clientsOfCIs(ctx, tx, in.OrganizationID, ciIDs)
	if err != nil {
		return out, err
	}

	// One entry per key: the last of the batch wins.
	var order []usageKey
	latest := map[usageKey]UsageReport{}
	for _, e := range valid {
		ciID := ""
		ip := canonicalAddr(e.Target)
		switch {
		case e.CIID != "" && (byCI[e.CIID] || knownCIs[e.CIID]):
			ciID = e.CIID
		case ip != "" && byIP[ip] != "":
			ciID = byIP[ip]
		case ip != "" && ciByIP[ip] != "":
			ciID = ciByIP[ip]
		}
		c, ok := creds[e.CredentialID]
		if !ok {
			// Deleted since the bundle was sealed.
			out.Rejected++
			usageRejected.Inc()
			continue
		}
		if ciID == "" {
			out.Unmatched++
			usageUnmatched.Inc()
			continue
		}
		if c.clientID != "" && c.clientID != ciClients[ciID] {
			// A client's credential never records usage at a CI of
			// another client (or an organization-wide CI).
			out.Rejected++
			usageRejected.Inc()
			continue
		}
		k := usageKey{e.CredentialID, ciID, e.Namespace}
		if _, seen := latest[k]; !seen {
			order = append(order, k)
		}
		latest[k] = e
	}

	var raised []*events.Event
	for _, k := range order {
		e := latest[k]
		class := ""
		if !e.OK {
			class = e.Cause
		}
		wantEvent := !e.OK && usageEventClasses[class]
		var emit bool
		// The throttle runs on the server's clock ($9): an event is due
		// when the row had no event within the hour before now (prev,
		// locked) and this statement set last_event_at to now (a
		// concurrent first insert that won the conflict keeps its own).
		err := tx.QueryRow(ctx, `WITH prev AS (
				SELECT last_event_at FROM credential_usage
				WHERE credential_id = $2 AND ci_id = $3 AND namespace = $4 FOR UPDATE
			), up AS (
				INSERT INTO credential_usage (organization_id, credential_id, ci_id, namespace,
					last_success_at, last_failure_at, last_error_class, last_event_at)
				VALUES ($1, $2, $3, $4,
					CASE WHEN $5 THEN $6::timestamptz END, CASE WHEN $5 THEN NULL ELSE $6::timestamptz END,
					NULLIF($7, ''), CASE WHEN $8 THEN $9::timestamptz END)
				ON CONFLICT (credential_id, ci_id, namespace) DO UPDATE SET
					last_success_at = CASE WHEN $5 THEN GREATEST(credential_usage.last_success_at, $6::timestamptz)
						ELSE credential_usage.last_success_at END,
					last_failure_at = CASE WHEN $5 THEN credential_usage.last_failure_at
						ELSE GREATEST(credential_usage.last_failure_at, $6::timestamptz) END,
					last_error_class = CASE WHEN $5 THEN credential_usage.last_error_class ELSE NULLIF($7, '') END,
					last_event_at = CASE WHEN $8 AND (credential_usage.last_event_at IS NULL
							OR credential_usage.last_event_at <= $9::timestamptz - interval '1 hour')
						THEN $9::timestamptz ELSE credential_usage.last_event_at END
				RETURNING last_event_at
			)
			SELECT $8 AND up.last_event_at IS NOT DISTINCT FROM $9::timestamptz
				AND NOT EXISTS (SELECT 1 FROM prev WHERE prev.last_event_at > $9::timestamptz - interval '1 hour')
			FROM up`,
			in.OrganizationID, k.credential, k.ci, k.namespace, e.OK, at, class, wantEvent, now).Scan(&emit)
		if err != nil {
			return out, err
		}
		out.Recorded++
		if emit {
			c := creds[k.credential]
			raised = append(raised, &events.Event{OrgID: in.OrganizationID, Type: events.CredentialResolutionFailed,
				EntityID: k.credential, Data: events.CredentialResolutionFailedData{
					ID: k.credential, ClientID: c.clientID, Kind: c.kind, Reason: class,
					CredentialID: k.credential, CIID: k.ci, Namespace: k.namespace, ErrorClass: class}})
		}
	}
	if len(raised) == 0 {
		return out, nil
	}
	out.Events = len(raised)
	return out, database.BeforeCommit(ctx, func() error {
		_, aerr := events.NewOutbox().AppendMany(ctx, tx, raised)
		return aerr
	})
}

// visibleCIs returns which of ids are CIs of the organization in the
// client (all clients without one).
func visibleCIs(ctx context.Context, tx pgx.Tx, orgID, clientID string, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT id::text FROM ci WHERE organization_id = $1 AND id = ANY ($2::uuid[])
		AND deleted_at IS NULL AND ($3::uuid IS NULL OR client_id = $3)`, orgID, ids, nilIfEmpty(clientID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// cisByAddress returns the CI of each address that exactly one CI of the
// client (all clients without one) carries as management address.
func cisByAddress(ctx context.Context, tx pgx.Tx, orgID, clientID string, ips []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ips) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT host(management_ip), (array_agg(id::text))[1], count(*)
		FROM ci WHERE organization_id = $1 AND management_ip = ANY ($2::inet[]) AND deleted_at IS NULL
			AND ($3::uuid IS NULL OR client_id = $3)
		GROUP BY host(management_ip)`, orgID, ips, nilIfEmpty(clientID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ip, id string
		var n int
		if err := rows.Scan(&ip, &id, &n); err != nil {
			return nil, err
		}
		if n == 1 {
			out[canonicalAddr(ip)] = id
		}
	}
	return out, rows.Err()
}

// clientsOfCIs returns the client of each CI ("" for an
// organization-wide CI).
func clientsOfCIs(ctx context.Context, tx pgx.Tx, orgID string, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT id::text, COALESCE(client_id::text, '') FROM ci
		WHERE organization_id = $1 AND id = ANY ($2::uuid[])`, orgID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, client string
		if err := rows.Scan(&id, &client); err != nil {
			return nil, err
		}
		out[id] = client
	}
	return out, rows.Err()
}

type usageCredential struct{ kind, clientID string }

// usageCredentials returns kind and client of the credentials.
func usageCredentials(ctx context.Context, tx pgx.Tx, orgID string, ids []string) (map[string]usageCredential, error) {
	out := map[string]usageCredential{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT id::text, kind, COALESCE(client_id::text, '') FROM credential
		WHERE organization_id = $1 AND id = ANY ($2::uuid[])`, orgID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var c usageCredential
		if err := rows.Scan(&id, &c.kind, &c.clientID); err != nil {
			return nil, err
		}
		out[id] = c
	}
	return out, rows.Err()
}

// --- reading ------------------------------------------------------------------

// Usage is a credential_usage row as the API returns it.
type Usage struct {
	CredentialID   string     `json:"credential_id"`
	CIID           string     `json:"ci_id"`
	Namespace      string     `json:"namespace"`
	LastSuccessAt  *time.Time `json:"last_success_at"`
	LastFailureAt  *time.Time `json:"last_failure_at"`
	LastErrorClass *string    `json:"last_error_class"`
	ClientID       *string    `json:"client_id"`
	SiteID         *string    `json:"site_id"`
}

// UsageRepository reads the usage of a credential in the caller's scope.
type UsageRepository interface {
	ListUsage(ctx context.Context, orgID, credentialID, ciID string) ([]Usage, error)
}

// ListUsage implements UsageRepository: the rows of the credential (and
// the CI) the scope of ctx sees, by CI and namespace.
func (r *PGRepository) ListUsage(ctx context.Context, orgID, credentialID, ciID string) ([]Usage, error) {
	out := []Usage{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT credential_id::text, ci_id::text, namespace, last_success_at, last_failure_at,
				last_error_class, client_id::text, site_id::text
			FROM credential_usage WHERE organization_id = $1 AND credential_id = $2 AND ($3::uuid IS NULL OR ci_id = $3)
			ORDER BY ci_id, namespace`, orgID, credentialID, nilIfEmpty(ciID))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var u Usage
			if err := rows.Scan(&u.CredentialID, &u.CIID, &u.Namespace, &u.LastSuccessAt, &u.LastFailureAt,
				&u.LastErrorClass, &u.ClientID, &u.SiteID); err != nil {
				return err
			}
			out = append(out, u)
		}
		return rows.Err()
	})
	return out, err
}

// noUsage is the usage store without a database: collectors report no
// usage there (--no-db), so every list is empty.
type noUsage struct{}

func (noUsage) ListUsage(context.Context, string, string, string) ([]Usage, error) {
	return []Usage{}, nil
}

// Usage returns the usage store of the service.
func (s *Service) Usage() UsageRepository {
	if u, ok := s.repo.(UsageRepository); ok {
		return u
	}
	return noUsage{}
}

// listUsage handles GET /api/v1/credentials/{id}/usage (AK5): the usage of
// a credential the caller sees, cursor-paginated, optionally of one CI.
func (h *Handler) listUsage(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	ciID := r.URL.Query().Get("ci_id")
	if ciID != "" && !api.IsUUID(ciID) {
		writeFieldError(w, "ci_id", "ci_id must be a UUID")
		return
	}
	id := chi.URLParam(r, "id")
	if _, err := h.svc.Get(r.Context(), orgID, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "credential not found")
			return
		}
		api.WriteRepoError(w, err)
		return
	}
	list, err := h.svc.Usage().ListUsage(r.Context(), orgID, id, ciID)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.Page(list, page))
}
