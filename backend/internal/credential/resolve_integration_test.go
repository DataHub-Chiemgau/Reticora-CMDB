package credential_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// Resolution of the effective credentials on PostgreSQL (CRD-04, E-219,
// WP-891): the targets of a collector's bundle, the client boundary, the
// statement budget and the view GET /cis/{id}/credentials/effective.

func (e *bindingEnv) scalar(sql string, args ...any) string {
	e.t.Helper()
	var out string
	if err := e.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return out
}

func (e *bindingEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.f.Admin.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

// collector inserts a collector (clientID empty: organization-wide) with an
// enabled CIDR scope of the client and returns its id.
func (e *bindingEnv) collector(name, clientID, cidr string) string {
	e.t.Helper()
	var client any
	if clientID != "" {
		client = clientID
	}
	id := e.scalar(`INSERT INTO collector (organization_id, client_id, name, status) VALUES ($1, $2, $3, 'active') RETURNING id::text`,
		e.f.OrgA, client, name)
	e.exec(`INSERT INTO discovery_scope (organization_id, client_id, collector_id, name, kind, target)
		VALUES ($1, $2, $3, $4, 'cidr', $5)`, e.f.OrgA, client, id, name+"-scope", cidr)
	return id
}

func (e *bindingEnv) setIP(ciID, ip string) {
	e.t.Helper()
	e.exec(`UPDATE ci SET management_ip = $2 WHERE id = $1`, ciID, ip)
}

func (e *bindingEnv) mustBind(credentialID, kind, target string, priority int) string {
	e.t.Helper()
	rec := e.bind(credentialID, kind, target, priority)
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("bind %s to %s %s: %d %s", credentialID, kind, target, rec.Code, rec.Body.String())
	}
	return idOf(e.t, rec)
}

// resolveOn runs ResolveCollector as the BundleGate does: in the
// organization-wide system scope.
func (e *bindingEnv) resolveOn(collectorID, clientID string) []credential.BundleTargetCredential {
	e.t.Helper()
	var out []credential.BundleTargetCredential
	scope := database.OrgWideScope(e.f.OrgA, "")
	if err := database.WithTenant(context.Background(), e.f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = credential.ResolveCollector(ctx, tx, e.f.OrgA, collectorID, clientID, credential.NoDeviceGroups{})
		return err
	}); err != nil {
		e.t.Fatalf("resolve collector: %v", err)
	}
	return out
}

// TestResolveClientCredentialsNeverReachForeignCollector covers CRD-04 AK2
// and AK3 (WP-891): an organization-wide collector receives the credential
// of client 1 only for the CI of client 1 and that of client 2 only for the
// CI of client 2; the collector of client 2 receives no credential of
// client 1, neither in its targets nor in the credential ids of its bundle,
// and the sealed bundle carries neither its id nor its secret. Bindings
// above the scope (CI, subnet) become targets with their origin; the
// narrower CI binding overrides the subnet binding.
func TestResolveClientCredentialsNeverReachForeignCollector(t *testing.T) {
	e := newBindingEnv(t)
	ctx := context.Background()
	cred2 := e.createCredential(`{"name":"wp891-c2","kind":"snmp_v2c","client_id":"` + e.f.Client2 + `","secret":{"community":"client2-secret"}}`)
	credSubnet := e.createCredential(`{"name":"wp891-org-snmp","kind":"snmp_v2c","secret":{"community":"org-secret"}}`)
	e.setIP(e.ci1, "10.91.1.10")
	e.setIP(e.ci2, "10.91.2.10")
	// cred1 (client 1) is bound to ci1, cred2 (client 2) to ci2, the
	// org-wide credSubnet to the subnet of client 2 holding ci2.
	ci1Binding := e.mustBind(e.cred1, "ci", e.ci1, 100)
	ci2Binding := e.mustBind(cred2, "ci", e.ci2, 100)
	subnetOf2 := e.subnet(e.f.Client2, "10.91.2.0/24")
	e.mustBind(credSubnet, "subnet", subnetOf2, 100)
	orgCollector := e.collector("wp891-org", "", "10.91.0.0/16")
	c2Collector := e.collector("wp891-c2", e.f.Client2, "10.91.0.0/16")

	byCI := func(list []credential.BundleTargetCredential) map[string]credential.BundleTargetCredential {
		out := map[string]credential.BundleTargetCredential{}
		for _, x := range list {
			out[x.CIID] = x
		}
		return out
	}
	org := byCI(e.resolveOn(orgCollector, ""))
	if got := org[e.ci1]; got.CredentialID != e.cred1 || got.Origin != credential.OriginCI || got.ManagementIP != "10.91.1.10" || got.Namespace != "snmp" {
		t.Errorf("org-wide collector, CI of client 1: %+v, want cred1 from its CI binding %s", got, ci1Binding)
	}
	if got := org[e.ci2]; got.CredentialID != cred2 || got.Origin != credential.OriginCI {
		t.Errorf("org-wide collector, CI of client 2: %+v, want cred2 from its CI binding %s (over the subnet)", got, ci2Binding)
	}
	if len(org) != 2 {
		t.Errorf("org-wide collector: targets %v, want the two CIs", org)
	}

	foreign := e.resolveOn(c2Collector, e.f.Client2)
	for _, x := range foreign {
		if x.CredentialID == e.cred1 || x.CIID == e.ci1 {
			t.Fatalf("the collector of client 2 receives %+v of client 1", x)
		}
	}
	if got := byCI(foreign)[e.ci2]; got.CredentialID != cred2 {
		t.Errorf("collector of client 2: target of its CI %+v, want cred2", got)
	}

	// The bundle: the targets' credentials join the selection; client 1's
	// credential is not in it.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	target := &credential.BundleTarget{OrganizationID: e.f.OrgA, CollectorID: c2Collector, ClientID: e.f.Client2,
		PublicKey: &key.PublicKey, CredentialIDs: []string{}}
	target.AddTargets(foreign)
	if slices.Contains(target.CredentialIDs, e.cred1) || !slices.Contains(target.CredentialIDs, cred2) {
		t.Fatalf("credential ids of the client 2 collector %v: want cred2, never cred1", target.CredentialIDs)
	}
	scope := database.OrgWideScope(e.f.OrgA, "")
	sealed, ids, err := e.svc.Bundle(database.ContextWithTenantScope(ctx, &scope), target)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := credential.OpenBundle(sealed, key)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(opened)
	if slices.Contains(ids, e.cred1) || strings.Contains(string(raw), e.cred1) || strings.Contains(string(raw), `"x"`) {
		t.Fatalf("the bundle of client 2's collector carries client 1's credential: ids %v", ids)
	}
	if len(opened.Targets) != 1 || opened.Targets[0].CredentialID != cred2 || sealed.Version != 2 {
		t.Errorf("bundle targets %+v (version %d), want the CI of client 2 with cred2 in format 2", opened.Targets, sealed.Version)
	}
}

// TestResolveTargetsCarryTheirScope covers CRD-04 AK2 (WP-891): a target
// names the scope of the collector it was resolved in. An organization-wide
// collector with a scope of client 1 and a scope of client 2 over the same
// addresses receives the CI binding of the CI of client 1 only for the scope
// of client 1 (the collector applies it only in that scope's pass, never to
// the device with the same address in the overlapping scope of client 2); a CI covered
// by two scopes of the collector has one entry per scope.
func TestResolveTargetsCarryTheirScope(t *testing.T) {
	e := newBindingEnv(t)
	e.setIP(e.ci1, "10.93.0.5")
	e.mustBind(e.cred1, "ci", e.ci1, 100)
	col := e.scalar(`INSERT INTO collector (organization_id, name, status) VALUES ($1, 'wp891-scopes', 'active') RETURNING id::text`, e.f.OrgA)
	scope := func(name, clientID, cidr string) string {
		return e.scalar(`INSERT INTO discovery_scope (organization_id, client_id, collector_id, name, kind, target)
			VALUES ($1, $2, $3, $4, 'cidr', $5) RETURNING id::text`, e.f.OrgA, clientID, col, name, cidr)
	}
	scope1 := scope("wp891-scope-c1", e.f.Client1, "10.93.0.0/24")
	scope2 := scope("wp891-scope-c2", e.f.Client2, "10.93.0.0/25")
	got := e.resolveOn(col, "")
	if len(got) != 1 || got[0].ScopeID != scope1 || got[0].CIID != e.ci1 || got[0].CredentialID != e.cred1 {
		t.Fatalf("targets %+v, want the CI of client 1 once, in its scope %s (never in the scope %s of client 2)", got, scope1, scope2)
	}
	scope1b := scope("wp891-scope-c1-wide", e.f.Client1, "10.93.0.0/16")
	got = e.resolveOn(col, "")
	var scopes []string
	for _, x := range got {
		if x.CIID != e.ci1 || x.CredentialID != e.cred1 {
			t.Fatalf("unexpected target %+v", x)
		}
		scopes = append(scopes, x.ScopeID)
	}
	slices.Sort(scopes)
	want := []string{scope1, scope1b}
	slices.Sort(want)
	if !slices.Equal(scopes, want) {
		t.Errorf("scopes of the targets %v, want one entry per covering scope %v", scopes, want)
	}
}

// TestResolveStatementBudget covers CRD-04 AK5 (WP-891): the resolution of
// a collector is one set-based statement – the same statements for 1,000
// CIs with bindings as for a handful, and exactly one that reads
// credential_binding.
func TestResolveStatementBudget(t *testing.T) {
	e := newBindingEnv(t)
	credOrg := e.createCredential(`{"name":"wp891-budget","kind":"snmp_v2c","secret":{"community":"b"}}`)
	typeID := e.scalar(`SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`)
	e.exec(`INSERT INTO ci (organization_id, ci_type_id, name, status, management_ip)
		SELECT $1, $2, 'wp891-budget-' || n, 'active', ('10.92.0.0'::inet + n)
		FROM generate_series(1, 1000) AS n`, e.f.OrgA, typeID)
	e.exec(`INSERT INTO credential_binding (organization_id, credential_id, target_kind, ci_id, namespace, priority)
		SELECT $1, $2, 'ci', c.id, 'snmp', 100 FROM ci c WHERE c.organization_id = $1 AND c.name LIKE 'wp891-budget-%'`, e.f.OrgA, credOrg)
	subnet := e.scalar(`INSERT INTO subnet (organization_id, cidr) VALUES ($1, '10.92.0.0/22') RETURNING id::text`, e.f.OrgA)
	e.mustBind(credOrg, "subnet", subnet, 50)
	small := e.collector("wp891-budget-small", "", "10.92.0.0/29")
	large := e.collector("wp891-budget-large", "", "10.92.0.0/22")

	trace := scopetest.NewQueryTrace(t, e.f.App)
	run := func(collectorID string) ([]scopetest.LoggedStatement, int) {
		t.Helper()
		var n int
		stmts := trace.Run(t, func() error {
			scope := database.OrgWideScope(e.f.OrgA, "")
			return database.WithTenant(context.Background(), trace.Pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
				targets, err := credential.ResolveCollector(ctx, tx, e.f.OrgA, collectorID, "", credential.NoDeviceGroups{})
				n = len(targets)
				return err
			})
		})
		return stmts, n
	}
	run(small) // warms the connection (its setup statements are no part of the resolution)
	smallStmts, smallTargets := run(small)
	largeStmts, largeTargets := run(large)
	if smallTargets != 7 || largeTargets != 1000 {
		t.Fatalf("targets: %d for the /29, %d for the /22; want 7 and 1000", smallTargets, largeTargets)
	}
	if len(largeStmts) != len(smallStmts) {
		t.Errorf("statements: %d for 1000 CIs, %d for 7 CIs; want the same (no statement per CI)", len(largeStmts), len(smallStmts))
	}
	reads := 0
	for _, s := range largeStmts {
		if strings.Contains(s.SQL, "credential_binding") {
			reads++
		}
	}
	if reads != 1 {
		t.Errorf("%d statements read credential_binding, want 1", reads)
	}
	t.Logf("resolution of 1000 CIs: %d statements including the tenant frame", len(largeStmts))
}

// TestEffectiveCredentialsWithoutSecret covers CRD-04 AK4 (WP-891): GET
// /cis/{id}/credentials/effective shows per namespace the credential (id,
// name, kind), its origin and source and the overridden candidates, never
// a secret or ciphertext; a CI outside the principal's scope and an unknown
// CI are 404.
func TestEffectiveCredentialsWithoutSecret(t *testing.T) {
	e := newBindingEnv(t)
	credSubnet := e.createCredential(`{"name":"wp891-subnet","kind":"snmp_v2c","client_id":"` + e.f.Client1 + `","secret":{"community":"subnet-secret"}}`)
	e.setIP(e.ci1, "10.89.1.20")
	ciBinding := e.mustBind(e.cred1, "ci", e.ci1, 100)
	subnetBinding := e.mustBind(credSubnet, "subnet", e.subnet1, 100)
	e.collector("wp891-view", "", "10.89.0.0/16")

	rec := e.do(http.MethodGet, "/api/v1/cis/"+e.ci1+"/credentials/effective", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("effective credentials: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, leak := range []string{"secret\"", "ciphertext", "subnet-secret", "community"} {
		if strings.Contains(body, leak) {
			t.Fatalf("the view carries %q: %s", leak, body)
		}
	}
	var view struct {
		CIID string                           `json:"ci_id"`
		Data []credential.EffectiveCredential `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	byNS := map[string]credential.EffectiveCredential{}
	for _, x := range view.Data {
		byNS[x.Namespace] = x
	}
	snmp := byNS["snmp"]
	if view.CIID != e.ci1 || snmp.Credential.ID != e.cred1 || snmp.Origin != credential.OriginCI || snmp.SourceID != ciBinding ||
		snmp.Credential.Name != "wp890-c1" || snmp.Credential.Kind != credential.KindSNMPv2c {
		t.Fatalf("snmp: %+v, want cred1 from the CI binding %s", snmp, ciBinding)
	}
	if len(snmp.Overridden) == 0 || snmp.Overridden[0].Credential.ID != credSubnet || snmp.Overridden[0].Origin != credential.OriginSubnet ||
		snmp.Overridden[0].SourceID != subnetBinding {
		t.Errorf("snmp overridden %+v, want the subnet binding %s first", snmp.Overridden, subnetBinding)
	}
	// The org-wide SSH credential of the fixture: org level (the scope has
	// no list).
	if ssh := byNS["ssh"]; ssh.Credential.ID != e.credOrg || ssh.Origin != credential.OriginOrg {
		t.Errorf("ssh: %+v, want the org-wide credential from the org level", ssh)
	}

	if rec = e.do(http.MethodGet, "/api/v1/cis/"+e.ci1+"/credentials/effective", "", e.f.Client2); rec.Code != http.StatusNotFound {
		t.Errorf("CI of client 1 for a principal of client 2: %d, want 404", rec.Code)
	}
	if rec = e.do(http.MethodGet, "/api/v1/cis/00000000-0000-4000-8000-000000000891/credentials/effective", "", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown CI: %d, want 404", rec.Code)
	}
}
