package credential_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// Usage of credentials per CI and namespace (CRD-06, WP-898) on
// PostgreSQL: RecordUsage in the organization-wide system transaction of a
// scope run, GET /credentials/{id}/usage through the handler.

// usageEnv is the binding environment with management addresses on the
// CIs: ci1 (client 1) 10.98.1.1, ci2 (client 2) 10.98.2.1.
type usageEnv struct {
	*bindingEnv
	credOther string
}

func newUsageEnv(t *testing.T) *usageEnv {
	t.Helper()
	e := &usageEnv{bindingEnv: newBindingEnv(t)}
	e.credOther = e.createCredential(`{"name":"wp898-other","kind":"ssh_password","secret":{"password":"wp898-secret-other"}}`)
	for ci, ip := range map[string]string{e.ci1: "10.98.1.1", e.ci2: "10.98.2.1"} {
		if _, err := e.f.Admin.Exec(context.Background(), `UPDATE ci SET management_ip = $2 WHERE id = $1`, ci, ip); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

// bundle is a collector bundle with the given credentials and targets.
func usageBundle(targets []credential.BundleTargetCredential, ids ...string) credential.UsageBundle {
	b := credential.UsageBundle{CredentialIDs: map[string]bool{}, Targets: targets}
	for _, id := range ids {
		b.CredentialIDs[id] = true
	}
	return b
}

// record runs RecordUsage in an organization-wide system transaction, as
// the scope run of a collector does.
func (e *usageEnv) record(in *credential.UsageInput) credential.UsageOutcome {
	e.t.Helper()
	in.OrganizationID = e.f.OrgA
	if in.ScopeID == "" {
		in.ScopeID = "6f0b6f0e-0000-4000-8000-000000000898"
	}
	scope := database.OrgWideScope(e.f.OrgA, "")
	var out credential.UsageOutcome
	err := database.WithTenant(context.Background(), e.f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
		var rerr error
		out, rerr = credential.RecordUsage(ctx, tx, in)
		return rerr
	})
	if err != nil {
		e.t.Fatalf("RecordUsage: %v", err)
	}
	return out
}

// usage lists GET /credentials/{id}/usage as an org-wide principal or one
// restricted to clientID.
func (e *usageEnv) usage(credentialID, query, clientID string) (int, []credential.Usage) {
	e.t.Helper()
	rec := e.do(http.MethodGet, "/api/v1/credentials/"+credentialID+"/usage"+query, "", clientID)
	if rec.Code != http.StatusOK {
		return rec.Code, nil
	}
	var page struct {
		Data []credential.Usage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		e.t.Fatal(err)
	}
	return rec.Code, page.Data
}

// resolutionEvents returns the data of the credential.resolution_failed
// events of the credential.
func (e *usageEnv) resolutionEvents(credentialID string) []map[string]any {
	e.t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT data FROM event_outbox
		WHERE organization_id = $1 AND type = $2 AND entity_id = $3 ORDER BY sequence`,
		e.f.OrgA, events.CredentialResolutionFailed, credentialID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			e.t.Fatal(err)
		}
		var data map[string]any
		if err := json.Unmarshal(raw, &data); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, data)
	}
	return out
}

// TestUsageUpsertSuccessAndFailure: a success sets last_success_at, a later
// failure last_failure_at and its class while the success stays; one row
// per credential, CI and namespace. GET lists it (filter ci_id), a malformed
// ci_id is 422, an unknown credential 404; a bundle target names the CI of
// its address in the reporting scope.
func TestUsageUpsertSuccessAndFailure(t *testing.T) {
	e := newUsageEnv(t)
	t0 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	out := e.record(&credential.UsageInput{ScopeClientID: e.f.Client1, Bundle: usageBundle(nil, e.cred1), At: t0,
		Entries: []credential.UsageReport{{Target: "10.98.1.1", Namespace: "snmp", CredentialID: e.cred1, OK: true}}})
	if out.Recorded != 1 || out.Unmatched != 0 || out.Rejected != 0 || out.Events != 0 {
		t.Fatalf("success: outcome %+v, want one recorded", out)
	}
	e.record(&credential.UsageInput{ScopeClientID: e.f.Client1, Bundle: usageBundle(nil, e.cred1), At: t0.Add(time.Minute),
		Entries: []credential.UsageReport{{Target: "10.98.1.1", Namespace: "snmp", CredentialID: e.cred1, Cause: credential.UsageTimeout}}})

	code, rows := e.usage(e.cred1, "", "")
	if code != http.StatusOK || len(rows) != 1 {
		t.Fatalf("GET usage = %d %+v, want one row", code, rows)
	}
	u := rows[0]
	if u.CIID != e.ci1 || u.Namespace != "snmp" || u.LastSuccessAt == nil || !u.LastSuccessAt.Equal(t0) ||
		u.LastFailureAt == nil || !u.LastFailureAt.Equal(t0.Add(time.Minute)) || u.LastErrorClass == nil ||
		*u.LastErrorClass != credential.UsageTimeout || u.ClientID == nil || *u.ClientID != e.f.Client1 {
		t.Errorf("usage row %+v, want ci1/snmp with the success at %v and the timeout a minute later (client 1)", u, t0)
	}
	if e.resolutionEvents(e.cred1) != nil {
		t.Error("a timeout raised credential.resolution_failed")
	}
	if _, rows = e.usage(e.cred1, "?ci_id="+e.ci2, ""); len(rows) != 0 {
		t.Errorf("filter ci_id of another CI lists %+v", rows)
	}
	if code, _ = e.usage(e.cred1, "?ci_id=x", ""); code != http.StatusUnprocessableEntity {
		t.Errorf("malformed ci_id = %d, want 422", code)
	}
	if code, _ = e.usage("6f0b6f0e-0000-4000-8000-00000000dead", "", ""); code != http.StatusNotFound {
		t.Errorf("unknown credential = %d, want 404", code)
	}

	// A bundle target of the reporting scope names the CI of the address,
	// also when the collector does not know it.
	scopeID := "6f0b6f0e-0000-4000-8000-000000000899"
	target := []credential.BundleTargetCredential{{CIID: e.ci2, ScopeID: scopeID, ManagementIP: "192.0.2.98", Namespace: "ssh",
		CredentialID: e.credOrg, Origin: "ci"}}
	out = e.record(&credential.UsageInput{ScopeID: scopeID, Bundle: usageBundle(target, e.credOrg), At: t0,
		Entries: []credential.UsageReport{{Target: "192.0.2.98", Namespace: "ssh", CredentialID: e.credOrg, OK: true}}})
	if out.Recorded != 1 {
		t.Fatalf("bundle target: outcome %+v, want one recorded", out)
	}
	if _, rows = e.usage(e.credOrg, "?ci_id="+e.ci2, ""); len(rows) != 1 || rows[0].Namespace != "ssh" {
		t.Errorf("usage through the bundle target = %+v, want ci2/ssh", rows)
	}
}

// TestUsageEventThrottledPerHour: auth_failed raises one
// credential.resolution_failed per credential, CI and namespace and hour,
// with the CI, the namespace and the class and without a secret.
func TestUsageEventThrottledPerHour(t *testing.T) {
	e := newUsageEnv(t)
	t0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	// The throttle runs on the server's clock (Now), not on the
	// collector's (At).
	fail := func(now time.Time) credential.UsageOutcome {
		return e.record(&credential.UsageInput{Bundle: usageBundle(nil, e.credOrg), At: t0, Now: now,
			Entries: []credential.UsageReport{{Target: "10.98.1.1", Namespace: "ssh", CredentialID: e.credOrg, Cause: credential.UsageAuthFailed}}})
	}
	if out := fail(t0); out.Events != 1 {
		t.Fatalf("first auth_failed: outcome %+v, want one event", out)
	}
	if out := fail(t0.Add(10 * time.Minute)); out.Events != 0 {
		t.Errorf("auth_failed after 10 minutes: outcome %+v, want no event", out)
	}
	if got := len(e.resolutionEvents(e.credOrg)); got != 1 {
		t.Fatalf("events within the hour = %d, want 1", got)
	}
	if out := fail(t0.Add(61 * time.Minute)); out.Events != 1 {
		t.Errorf("auth_failed after 61 minutes: outcome %+v, want one event", out)
	}
	evs := e.resolutionEvents(e.credOrg)
	if len(evs) != 2 {
		t.Fatalf("events = %d, want 2", len(evs))
	}
	d := evs[0]
	if d["id"] != e.credOrg || d["credential_id"] != e.credOrg || d["ci_id"] != e.ci1 || d["namespace"] != "ssh" || d["error_class"] != credential.UsageAuthFailed ||
		d["kind"] != "ssh_password" {
		t.Errorf("event data %v, want credential, ci1, ssh, auth_failed and the kind", d)
	}
	for _, ev := range evs {
		for k := range ev {
			if k == "secret" || k == "password" || k == "community" || strings.Contains(k, "token") {
				t.Errorf("event data carries the secret member %q", k)
			}
		}
	}
	// A vault class raises it too, for another namespace key at once.
	out := e.record(&credential.UsageInput{Bundle: usageBundle(nil, e.cred1), At: t0, Now: t0,
		Entries: []credential.UsageReport{{Target: "10.98.1.1", Namespace: "snmp", CredentialID: e.cred1, Cause: credential.UsageVaultDenied}}})
	if out.Events != 1 {
		t.Errorf("vault_denied: outcome %+v, want one event", out)
	}
}

// TestUsageEventThrottleIgnoresCollectorClock: the same report sent twice
// (a redelivery, the same At) raises one event; a collector clock a year
// ahead neither raises a second event nor blocks the next one after the
// hour on the server's clock.
func TestUsageEventThrottleIgnoresCollectorClock(t *testing.T) {
	e := newUsageEnv(t)
	at := time.Now().Add(-time.Minute)
	batch := func(at, now time.Time) credential.UsageOutcome {
		return e.record(&credential.UsageInput{Bundle: usageBundle(nil, e.credOrg), At: at, Now: now,
			Entries: []credential.UsageReport{{Target: "10.98.1.1", Namespace: "ssh", CredentialID: e.credOrg, Cause: credential.UsageAuthFailed}}})
	}
	if out := batch(at, time.Time{}); out.Events != 1 {
		t.Fatalf("first report: outcome %+v, want one event", out)
	}
	if out := batch(at, time.Time{}); out.Events != 0 || out.Recorded != 1 {
		t.Errorf("same report again: outcome %+v, want recorded without event", out)
	}
	ahead := time.Now().AddDate(1, 0, 0)
	if out := batch(ahead, time.Time{}); out.Events != 0 {
		t.Errorf("collector clock a year ahead: outcome %+v, want no event within the hour", out)
	}
	if out := batch(at, time.Now().Add(61*time.Minute)); out.Events != 1 {
		t.Errorf("after the hour on the server's clock: outcome %+v, want one event", out)
	}
	if got := len(e.resolutionEvents(e.credOrg)); got != 2 {
		t.Errorf("events = %d, want 2", got)
	}
}

// TestUsageRejectsCredentialOfAnotherClient: a credential of client 1
// reported for a CI of client 2 is rejected, even when it is in the bundle.
func TestUsageRejectsCredentialOfAnotherClient(t *testing.T) {
	e := newUsageEnv(t)
	out := e.record(&credential.UsageInput{Bundle: usageBundle(nil, e.cred1), Entries: []credential.UsageReport{
		{Target: "10.98.2.1", Namespace: "ssh", CredentialID: e.cred1, Cause: credential.UsageAuthFailed},
		{Target: "10.98.1.1", Namespace: "ssh", CredentialID: e.cred1, OK: true},
	}})
	if out.Rejected != 1 || out.Recorded != 1 || out.Events != 0 {
		t.Errorf("outcome %+v, want one rejected and one recorded", out)
	}
	if _, rows := e.usage(e.cred1, "", ""); len(rows) != 1 || rows[0].CIID != e.ci1 {
		t.Errorf("usage of the client 1 credential = %+v, want only ci1", rows)
	}
	if got := len(e.resolutionEvents(e.cred1)); got != 0 {
		t.Errorf("events = %d, want none", got)
	}
}

// TestUsageRejectsCredentialOutsideBundle: a credential that was not in the
// reporting collector's bundle, an unknown namespace and an unknown class
// are rejected and store nothing.
func TestUsageRejectsCredentialOutsideBundle(t *testing.T) {
	e := newUsageEnv(t)
	before := testutil.ToFloat64(credential.UsageMetrics()[1])
	out := e.record(&credential.UsageInput{Bundle: usageBundle(nil, e.credOrg), Entries: []credential.UsageReport{
		{Target: "10.98.1.1", Namespace: "ssh", CredentialID: e.credOther, OK: true},
		{Target: "10.98.1.1", Namespace: "telnet", CredentialID: e.credOrg, OK: true},
		{Target: "10.98.1.1", Namespace: "ssh", CredentialID: e.credOrg, Cause: "config_error"},
	}})
	if out.Rejected != 3 || out.Recorded != 0 {
		t.Errorf("outcome %+v, want three rejected", out)
	}
	if got := testutil.ToFloat64(credential.UsageMetrics()[1]) - before; got != 3 {
		t.Errorf("reticora_credential_usage_rejected_total grew by %v, want 3", got)
	}
	if _, rows := e.usage(e.credOther, "", ""); len(rows) != 0 {
		t.Errorf("a credential outside the bundle has usage %+v", rows)
	}
}

// TestUsageClientPrincipal: a principal restricted to client 1 sees only
// the usage at the CIs of client 1; a credential of client 1 is unknown to
// a principal of client 2 (404).
func TestUsageClientPrincipal(t *testing.T) {
	e := newUsageEnv(t)
	e.record(&credential.UsageInput{Bundle: usageBundle(nil, e.credOrg), Entries: []credential.UsageReport{
		{Target: "10.98.1.1", Namespace: "ssh", CredentialID: e.credOrg, OK: true},
		{Target: "10.98.2.1", Namespace: "ssh", CredentialID: e.credOrg, OK: true},
	}})
	if _, rows := e.usage(e.credOrg, "", ""); len(rows) != 2 {
		t.Fatalf("org-wide principal sees %d rows, want 2", len(rows))
	}
	_, rows := e.usage(e.credOrg, "", e.f.Client1)
	if len(rows) != 1 || rows[0].CIID != e.ci1 {
		t.Errorf("client 1 principal sees %+v, want only ci1", rows)
	}
	e.record(&credential.UsageInput{Bundle: usageBundle(nil, e.cred1), Entries: []credential.UsageReport{
		{Target: "10.98.1.1", Namespace: "snmp", CredentialID: e.cred1, OK: true}}})
	if code, _ := e.usage(e.cred1, "", e.f.Client2); code != http.StatusNotFound {
		t.Errorf("client 2 principal on a credential of client 1 = %d, want 404", code)
	}
}

// TestUsageUnmatchedTargetDropped: an address without CI, an address of
// another client than the scope's and a name are dropped and counted
// (reticora_credential_usage_unmatched_total).
func TestUsageUnmatchedTargetDropped(t *testing.T) {
	e := newUsageEnv(t)
	before := testutil.ToFloat64(credential.UsageMetrics()[0])
	out := e.record(&credential.UsageInput{ScopeClientID: e.f.Client1, Bundle: usageBundle(nil, e.credOrg),
		Entries: []credential.UsageReport{
			{Target: "10.98.9.9", Namespace: "ssh", CredentialID: e.credOrg, OK: true},
			{Target: "10.98.2.1", Namespace: "ssh", CredentialID: e.credOrg, OK: true},
			{Target: "host.example", Namespace: "ssh", CredentialID: e.credOrg, OK: true},
		}})
	if out.Unmatched != 3 || out.Recorded != 0 {
		t.Errorf("outcome %+v, want three unmatched", out)
	}
	if got := testutil.ToFloat64(credential.UsageMetrics()[0]) - before; got != 3 {
		t.Errorf("reticora_credential_usage_unmatched_total grew by %v, want 3", got)
	}
	if _, rows := e.usage(e.credOrg, "", ""); len(rows) != 0 {
		t.Errorf("unmatched entries stored %+v", rows)
	}
}
