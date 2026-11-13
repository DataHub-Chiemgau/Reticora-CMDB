package ipam_test

// WP-1062 (IPM-11): the subnet scan as managed ICMP probes against
// PostgreSQL as reticora_app.
//
// Retest: cd backend && TEST_DATABASE_URL=… go test -count=1 -p 1 -run 'TestScan|TestSubnetDeleteRemovesOwnedProbes|TestManagedProbeNotEditable409' ./internal/ipam/

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// scanEnv is organization A with collector groups of client 1 (g1), of
// client 2 (g2) and an org-wide one (gOrg), the IPAM and probe routes on
// PostgreSQL with the scan wired to the probe repository.
type scanEnv struct {
	t             *testing.T
	f             *scopetest.Fixture
	mux           *chi.Mux
	repo          *probe.PGRepository
	ipam          *ipam.PGRepository
	g1, g2, gOrg  string
	scopedClients []string
}

func newScanEnvOn(t *testing.T, f *scopetest.Fixture, pool *pgxpool.Pool) *scanEnv {
	t.Helper()
	e := &scanEnv{t: t, f: f, mux: chi.NewRouter(), repo: probe.NewPGRepository(pool), ipam: ipam.NewPGRepository(pool)}
	inner := chi.NewRouter()
	ipam.NewHandler(e.ipam).WithScanProbes(probe.ScanProbes{Repo: e.repo}).RegisterRoutes(inner)
	probe.NewHandler(e.repo).RegisterRoutes(inner)
	auditor := audit.NewPGRouteAuditor(pool)
	e.mux.With(middleware.AuditRoute(http.MethodPatch, "/api/v1/subnets/{id}/scan", auditor, middleware.AuditIntent, "")).
		Patch("/api/v1/subnets/{id}/scan", inner.ServeHTTP)
	e.mux.NotFound(inner.ServeHTTP)
	e.mux.MethodNotAllowed(inner.ServeHTTP)
	return e
}

func newScanEnv(t *testing.T) *scanEnv {
	t.Helper()
	f := scopetest.Seed(t, "9e")
	e := newScanEnvOn(t, f, f.App)
	e.groups()
	return e
}

func (e *scanEnv) groups() {
	e.g1 = e.scalar(`INSERT INTO collector_group (organization_id, client_id, name) VALUES ($1, $2, 'wp1062-c1') RETURNING id::text`, e.f.OrgA, e.f.Client1)
	e.g2 = e.scalar(`INSERT INTO collector_group (organization_id, client_id, name) VALUES ($1, $2, 'wp1062-c2') RETURNING id::text`, e.f.OrgA, e.f.Client2)
	e.gOrg = e.scalar(`INSERT INTO collector_group (organization_id, name) VALUES ($1, 'wp1062-org') RETURNING id::text`, e.f.OrgA)
}

func (e *scanEnv) scalar(sql string, args ...any) string {
	e.t.Helper()
	var out string
	if err := e.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return out
}

func (e *scanEnv) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// call sends a request of the org-wide principal (or of the clients set in
// scopedClients).
func (e *scanEnv) call(method, path, body string) (int, map[string]any) {
	e.t.Helper()
	scope := database.OrgWideScope(e.f.OrgA, e.f.User)
	if e.scopedClients != nil {
		scope.Clients = database.ScopeIDs(e.scopedClients...)
	}
	ctx := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope),
		tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: e.f.User})
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func (e *scanEnv) mustID(path, body string) string {
	e.t.Helper()
	code, out := e.call(http.MethodPost, path, body)
	if code != http.StatusCreated {
		e.t.Fatalf("POST %s %s = %d %v", path, body, code, out)
	}
	return out["id"].(string)
}

// subnet creates a subnet with n addresses .1 … .n (IPv4) or ::1 … ::n.
func (e *scanEnv) subnet(cidr, client, vrf string, addrs ...string) string {
	e.t.Helper()
	body := `{"cidr":"` + cidr + `"`
	if client != "" {
		body += `,"client_id":"` + client + `"`
	}
	if vrf != "" {
		body += `,"vrf_id":"` + vrf + `"`
	}
	id := e.mustID("/api/v1/subnets", body+"}")
	for _, a := range addrs {
		e.mustID("/api/v1/ips", `{"address":"`+a+`","subnet_id":"`+id+`"}`)
	}
	return id
}

func hosts(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s%d", prefix, i+1)
	}
	return out
}

func (e *scanEnv) patch(subnet, body string) (int, map[string]any) {
	e.t.Helper()
	return e.call(http.MethodPatch, "/api/v1/subnets/"+subnet+"/scan", body)
}

func (e *scanEnv) mustPatch(subnet, body string) map[string]any {
	e.t.Helper()
	code, out := e.patch(subnet, body)
	if code != http.StatusOK {
		e.t.Fatalf("PATCH scan %s = %d %v", body, code, out)
	}
	return out
}

func (e *scanEnv) refused(subnet, body, field string) {
	e.t.Helper()
	code, out := e.patch(subnet, body)
	if code != http.StatusUnprocessableEntity || out["field"] != field {
		e.t.Errorf("PATCH scan %s = %d %v, want 422 %s", body, code, out, field)
	}
}

func (e *scanEnv) managed(subnet string) int {
	e.t.Helper()
	return e.count(`SELECT count(*) FROM probe WHERE organization_id = $1 AND managed_by = 'ipam_scan' AND managed_key LIKE $2`,
		e.f.OrgA, "scan:"+subnet+":%")
}

// assign makes a collector a member of group and assigns it every managed
// probe of subnet; it returns the collector.
func (e *scanEnv) assign(group, subnet string) string {
	e.t.Helper()
	c := e.scalar(`INSERT INTO collector (organization_id, client_id, name, status)
		SELECT $1, g.client_id, $2, 'active' FROM collector_group g WHERE g.id = $3 RETURNING id::text`,
		e.f.OrgA, "wp1062-"+subnet[:8], group)
	// A new collector joins the default group of its client; it moves to
	// group.
	e.scalar(`WITH d AS (DELETE FROM collector_group_member WHERE collector_id = $1 RETURNING 1)
		SELECT count(*)::text FROM d`, c)
	e.scalar(`INSERT INTO collector_group_member (group_id, collector_id, organization_id) VALUES ($1, $2, $3) RETURNING collector_id::text`,
		group, c, e.f.OrgA)
	e.scalar(`WITH a AS (INSERT INTO probe_assignment (probe_id, organization_id, client_id, collector_id, epoch)
		SELECT id, organization_id, client_id, $3, 1 FROM probe
		 WHERE organization_id = $1 AND managed_by = 'ipam_scan' AND managed_key LIKE $2 RETURNING 1)
		SELECT count(*)::text FROM a`, e.f.OrgA, "scan:"+subnet+":%", c)
	return c
}

// version is the config_version of a collector.
func (e *scanEnv) version(collector string) int {
	e.t.Helper()
	return e.count(`SELECT config_version::int FROM collector_group_member WHERE collector_id = $1`, collector)
}

func num(v any) int {
	f, _ := v.(float64)
	return int(f)
}

// TestScanEnableCreatesManagedProbes: a /24 with 10 addresses gives 10
// managed icmp probes in the subnet's collector group with the client of
// the subnet and the defaults 30 s / 10 s / down count 5.
func TestScanEnableCreatesManagedProbes(t *testing.T) {
	e := newScanEnv(t)
	s := e.subnet("10.62.1.0/24", e.f.Client1, "", hosts("10.62.1.", 10)...)
	out := e.mustPatch(s, `{"scan_enabled":true,"collector_group_id":"`+e.g1+`"}`)
	if num(out["targets"]) != 10 || num(out["owned"]) != 10 || num(out["reused"]) != 0 || out["scan_synced_at"] == nil {
		t.Fatalf("scan %v", out)
	}
	if n := e.count(`SELECT count(*) FROM probe WHERE organization_id = $1 AND managed_by = 'ipam_scan' AND type = 'icmp'
		AND collector_group_id = $2 AND client_id = $3 AND ci_id IS NULL AND params = '{}'::jsonb AND enabled`, e.f.OrgA, e.g1, e.f.Client1); n != 10 {
		t.Fatalf("%d managed icmp probes, want 10", n)
	}
	id := e.scalar(`SELECT id::text FROM probe WHERE organization_id = $1 AND managed_key = $2`, e.f.OrgA, "scan:"+s+":10.62.1.7")
	code, eff := e.call(http.MethodGet, "/api/v1/monitoring/probes/"+id+"/effective", "")
	value := func(name string) int { m, _ := eff[name].(map[string]any); return num(m["value"]) }
	if code != http.StatusOK || value("interval_s") != 30 || value("timeout_s") != 10 || value("down_count") != 5 {
		t.Fatalf("effective values %d %v, want 30 s / 10 s / 5", code, eff)
	}
	code, got := e.call(http.MethodGet, "/api/v1/monitoring/probes/"+id, "")
	if code != http.StatusOK || got["managed_by"] != "ipam_scan" || got["target"] != "10.62.1.7" {
		t.Fatalf("probe %d %v, want managed_by ipam_scan", code, got)
	}
	// A second PATCH with the interval changes the probes, keeps their ids.
	out = e.mustPatch(s, `{"scan_interval_s":60}`)
	if num(out["owned"]) != 10 {
		t.Fatalf("after the interval: %v", out)
	}
	if again := e.scalar(`SELECT id::text FROM probe WHERE organization_id = $1 AND managed_key = $2`, e.f.OrgA, "scan:"+s+":10.62.1.7"); again != id ||
		e.count(`SELECT count(*) FROM probe WHERE id = $1 AND params = '{"interval_s":60}'::jsonb`, id) != 1 {
		t.Fatalf("interval not applied in place")
	}
	code, get := e.call(http.MethodGet, "/api/v1/subnets/"+s+"/scan", "")
	if code != http.StatusOK || get["scan_enabled"] != true || num(get["targets"]) != 10 || get["collector_group_id"] != e.g1 {
		t.Fatalf("GET scan %d %v", code, get)
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE organization_id = $1 AND resource_id = $2`, e.f.OrgA, s); n < 2 {
		t.Errorf("%d audit entries of the subnet, want one per PATCH", n)
	}
}

// TestScanReusesExistingIcmpProbe: an auto probe on an address of the
// subnet in the same collector group is used with owned false.
func TestScanReusesExistingIcmpProbe(t *testing.T) {
	e := newScanEnv(t)
	s := e.subnet("10.62.2.0/24", e.f.Client1, "", hosts("10.62.2.", 4)...)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp1062-host")
	auto := e.scalar(`INSERT INTO probe (organization_id, ci_id, collector_group_id, type, target, origin)
		VALUES ($1, $2, $3, 'icmp', '10.62.2.3', 'auto') RETURNING id::text`, e.f.OrgA, ci, e.g1)
	out := e.mustPatch(s, `{"scan_enabled":true,"collector_group_id":"`+e.g1+`"}`)
	if num(out["targets"]) != 4 || num(out["owned"]) != 3 || num(out["reused"]) != 1 {
		t.Fatalf("scan %v, want 4 targets, 3 owned, 1 reused", out)
	}
	if n := e.count(`SELECT count(*) FROM subnet_scan_target WHERE subnet_id = $1 AND probe_id = $2 AND NOT owned AND address = '10.62.2.3'`, s, auto); n != 1 {
		t.Fatal("the auto probe is not the reused target")
	}
	if e.managed(s) != 3 {
		t.Fatalf("%d managed probes, want 3", e.managed(s))
	}
}

// TestScanDisableRemovesOwnedProbes: switching off deletes every target and
// the managed probes; the reused auto probe and a foreign probe stay.
func TestScanDisableRemovesOwnedProbes(t *testing.T) {
	e := newScanEnv(t)
	s := e.subnet("10.62.3.0/24", e.f.Client1, "", hosts("10.62.3.", 3)...)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp1062-host3")
	auto := e.scalar(`INSERT INTO probe (organization_id, ci_id, collector_group_id, type, target, origin)
		VALUES ($1, $2, $3, 'icmp', '10.62.3.2', 'auto') RETURNING id::text`, e.f.OrgA, ci, e.g1)
	foreign := e.scalar(`INSERT INTO probe (organization_id, client_id, collector_group_id, type, target)
		VALUES ($1, $2, $3, 'tcp', '10.62.3.1:22') RETURNING id::text`, e.f.OrgA, e.f.Client1, e.g1)
	e.mustPatch(s, `{"scan_enabled":true,"collector_group_id":"`+e.g1+`"}`)
	out := e.mustPatch(s, `{"scan_enabled":false}`)
	if num(out["targets"]) != 0 || e.managed(s) != 0 ||
		e.count(`SELECT count(*) FROM subnet_scan_target WHERE subnet_id = $1`, s) != 0 {
		t.Fatalf("after switching off: %v, %d managed probes", out, e.managed(s))
	}
	if e.count(`SELECT count(*) FROM probe WHERE id = ANY ($1::uuid[])`, []string{auto, foreign}) != 2 {
		t.Fatal("the reused or a foreign probe was deleted")
	}
}

// TestSubnetDeleteRemovesOwnedProbes: deleting the subnet deletes its
// targets and with them the managed probes; foreign probes stay.
func TestSubnetDeleteRemovesOwnedProbes(t *testing.T) {
	e := newScanEnv(t)
	s := e.subnet("10.62.4.0/24", e.f.Client1, "", hosts("10.62.4.", 3)...)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp1062-host4")
	auto := e.scalar(`INSERT INTO probe (organization_id, ci_id, collector_group_id, type, target, origin)
		VALUES ($1, $2, $3, 'icmp', '10.62.4.1', 'auto') RETURNING id::text`, e.f.OrgA, ci, e.g1)
	e.mustPatch(s, `{"scan_enabled":true,"collector_group_id":"`+e.g1+`"}`)
	if e.managed(s) != 2 {
		t.Fatalf("%d managed probes before the delete, want 2", e.managed(s))
	}
	collector := e.assign(e.g1, s)
	before := e.version(collector)
	ids := e.scalar(`SELECT string_agg(id::text, ',') FROM probe WHERE organization_id = $1 AND managed_key LIKE $2`, e.f.OrgA, "scan:"+s+":%")
	if code, out := e.call(http.MethodDelete, "/api/v1/subnets/"+s, ""); code != http.StatusNoContent {
		t.Fatalf("DELETE subnet = %d %v", code, out)
	}
	if e.managed(s) != 0 || e.count(`SELECT count(*) FROM probe WHERE id = $1`, auto) != 1 {
		t.Fatalf("after the delete: %d managed probes; the auto probe must stay", e.managed(s))
	}
	// The collector of the deleted probes learns it with a new version and
	// config.changed (WP-941 AK3), not only from the delete trigger.
	if after := e.version(collector); after <= before {
		t.Fatalf("config_version of the assigned collector %d after the delete, was %d", after, before)
	}
	if n := e.count(`SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND type = 'config.changed'
		AND entity_id = ANY (string_to_array($2, ',')) AND data->'changed_fields' ? 'deleted'`, e.f.OrgA, ids); n != 2 {
		t.Fatalf("%d config.changed events of deleted probes, want 2", n)
	}
}

// TestScanReuseOnlyOwnClient: with an org-wide collector group the scan of
// a subnet of client 2 does not reuse the probe of client 1 on the same
// address (overlapping private networks); it gets a probe of its own.
func TestScanReuseOnlyOwnClient(t *testing.T) {
	e := newScanEnv(t)
	other := e.scalar(`INSERT INTO probe (organization_id, client_id, collector_group_id, type, target, origin)
		VALUES ($1, $2, $3, 'icmp', '192.168.5.10', 'auto') RETURNING id::text`, e.f.OrgA, e.f.Client1, e.gOrg)
	vrf := e.mustID("/api/v1/vrfs", `{"name":"wp1062-reuse"}`)
	s := e.subnet("192.168.5.0/24", e.f.Client2, vrf, "192.168.5.10")
	out := e.mustPatch(s, `{"scan_enabled":true,"collector_group_id":"`+e.gOrg+`"}`)
	if num(out["owned"]) != 1 || num(out["reused"]) != 0 {
		t.Fatalf("scan %v, want an own probe, nothing reused", out)
	}
	if n := e.count(`SELECT count(*) FROM subnet_scan_target t JOIN probe p ON p.id = t.probe_id
		WHERE t.subnet_id = $1 AND t.owned AND p.client_id = $2 AND p.id <> $3`, s, e.f.Client2, other); n != 1 {
		t.Fatal("the target of client 2 does not use a probe of client 2")
	}
	// The same client reuses it.
	s1 := e.subnet("192.168.5.0/24", e.f.Client1, "", "192.168.5.10")
	if out = e.mustPatch(s1, `{"scan_enabled":true,"collector_group_id":"`+e.gOrg+`"}`); num(out["reused"]) != 1 {
		t.Fatalf("scan of client 1 %v, want the probe of client 1 reused", out)
	}
}

// TestSubnetPatchResyncsScan: a new client or CIDR of a scanned subnet
// (PATCH /subnets/{id}) moves the managed probes to the new client in the
// same transaction; a collector group of another client than the new one
// is 422 client_id, discover_new_hosts on a CIDR wider than /22 is 422
// cidr, and nothing changes.
func TestSubnetPatchResyncsScan(t *testing.T) {
	e := newScanEnv(t)
	s := e.subnet("10.62.20.0/24", e.f.Client1, "", hosts("10.62.20.", 3)...)
	e.mustPatch(s, `{"scan_enabled":true,"collector_group_id":"`+e.gOrg+`"}`)
	if code, out := e.call(http.MethodPatch, "/api/v1/subnets/"+s, `{"client_id":"`+e.f.Client2+`"}`); code != http.StatusOK {
		t.Fatalf("PATCH client = %d %v", code, out)
	}
	if n := e.count(`SELECT count(*) FROM probe WHERE organization_id = $1 AND managed_key LIKE $2 AND client_id = $3`,
		e.f.OrgA, "scan:"+s+":%", e.f.Client2); n != 3 {
		t.Fatalf("%d managed probes of client 2 after the move, want 3", n)
	}
	if n := e.count(`SELECT count(*) FROM event_outbox o JOIN probe p ON p.id::text = o.entity_id
		WHERE o.organization_id = $1 AND o.type = 'config.changed' AND p.managed_key LIKE $2
		  AND o.data->'changed_fields' ? 'client_id'`, e.f.OrgA, "scan:"+s+":%"); n != 3 {
		t.Fatalf("%d config.changed events naming client_id, want 3", n)
	}

	g := e.subnet("10.62.21.0/24", e.f.Client1, "", "10.62.21.1")
	e.mustPatch(g, `{"scan_enabled":true,"collector_group_id":"`+e.g1+`"}`)
	if code, out := e.call(http.MethodPatch, "/api/v1/subnets/"+g, `{"client_id":"`+e.f.Client2+`"}`); code != http.StatusUnprocessableEntity ||
		out["field"] != "client_id" {
		t.Fatalf("PATCH client with a group of client 1 = %d %v, want 422 client_id", code, out)
	}

	d := e.subnet("10.63.0.0/24", e.f.Client1, "")
	e.mustPatch(d, `{"scan_enabled":true,"collector_group_id":"`+e.g1+`","discover_new_hosts":true}`)
	if code, out := e.call(http.MethodPatch, "/api/v1/subnets/"+d, `{"cidr":"10.63.0.0/21"}`); code != http.StatusUnprocessableEntity ||
		out["field"] != "cidr" {
		t.Fatalf("PATCH cidr /21 with discover_new_hosts = %d %v, want 422 cidr", code, out)
	}
	if e.managed(d) != 254 || e.count(`SELECT count(*) FROM subnet WHERE id = $1 AND cidr = '10.63.0.0/24'`, d) != 1 {
		t.Fatal("the refused PATCH changed the subnet or its probes")
	}
	// A narrower CIDR drops the hosts outside it.
	if code, out := e.call(http.MethodPatch, "/api/v1/subnets/"+d, `{"cidr":"10.63.0.0/25"}`); code != http.StatusOK {
		t.Fatalf("PATCH cidr /25 = %d %v", code, out)
	}
	if e.managed(d) != 126 {
		t.Fatalf("%d managed probes after /25, want 126", e.managed(d))
	}
}

// TestSyncScanTargetsMarksSynced covers AK5: SyncScanTargets(ctx, tx,
// orgID, subnetIDs) takes the probe budget lock, synchronizes the targets
// of the scanned subnets and sets scan_synced_at.
func TestSyncScanTargetsMarksSynced(t *testing.T) {
	e := newScanEnv(t)
	s := e.subnet("10.62.22.0/24", e.f.Client1, "", "10.62.22.1")
	e.mustPatch(s, `{"scan_enabled":true,"collector_group_id":"`+e.g1+`"}`)
	e.scalar(`UPDATE subnet SET scan_synced_at = now() - interval '1 day' WHERE id = $1 RETURNING id::text`, s)
	// A new address appears without the scan (the scan follows in WP-1063).
	e.scalar(`INSERT INTO ip_address (organization_id, subnet_id, address, vrf_id)
		SELECT organization_id, id, '10.62.22.2', vrf_id FROM subnet WHERE id = $1 RETURNING id::text`, s)
	scope := database.OrgWideScope(e.f.OrgA, e.f.User)
	ctx := database.ContextWithTenantScope(context.Background(), &scope)
	if err := database.WithRequestTenant(ctx, e.f.App, e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		return e.ipam.SyncScanTargets(ctx, tx, e.f.OrgA, []string{s})
	}); err != nil {
		t.Fatal(err)
	}
	if e.managed(s) != 2 || e.count(`SELECT count(*) FROM subnet WHERE id = $1 AND scan_synced_at > now() - interval '1 hour'`, s) != 1 {
		t.Fatalf("after SyncScanTargets: %d managed probes, scan_synced_at not renewed", e.managed(s))
	}
	if err := database.WithRequestTenant(ctx, e.f.App, e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		return ipam.NewPGRepository(e.f.App).SyncScanTargets(ctx, tx, e.f.OrgA, []string{s})
	}); err == nil {
		t.Fatal("SyncScanTargets without the probe engine succeeded")
	}
}

// TestScanOverlappingRangesTwoClients: 192.168.1.10 in two client VRFs gives
// two probes.
func TestScanOverlappingRangesTwoClients(t *testing.T) {
	e := newScanEnv(t)
	vrf := e.mustID("/api/v1/vrfs", `{"name":"wp1062-c2"}`)
	s1 := e.subnet("192.168.1.0/24", e.f.Client1, "", "192.168.1.10")
	s2 := e.subnet("192.168.1.0/24", e.f.Client2, vrf, "192.168.1.10")
	e.mustPatch(s1, `{"scan_enabled":true,"collector_group_id":"`+e.g1+`"}`)
	e.mustPatch(s2, `{"scan_enabled":true,"collector_group_id":"`+e.g2+`"}`)
	if n := e.count(`SELECT count(DISTINCT client_id) FROM probe WHERE organization_id = $1 AND target = '192.168.1.10' AND managed_by = 'ipam_scan'`,
		e.f.OrgA); n != 2 {
		t.Fatalf("%d probes of distinct clients on 192.168.1.10, want 2", n)
	}
	// The same target in the same client, VRF apart, collides neither.
	vrf2 := e.mustID("/api/v1/vrfs", `{"name":"wp1062-c1b"}`)
	s3 := e.subnet("192.168.1.0/24", e.f.Client1, vrf2, "192.168.1.10")
	e.mustPatch(s3, `{"scan_enabled":true,"collector_group_id":"`+e.g1+`"}`)
	if e.managed(s3) != 1 {
		t.Fatalf("the second subnet of client 1 got %d probes, want 1", e.managed(s3))
	}
}

// TestScanDiscoverTooLarge422: discover_new_hosts on a /21 is 422; a /22
// takes all 1,022 hosts.
func TestScanDiscoverTooLarge422(t *testing.T) {
	e := newScanEnv(t)
	s := e.subnet("10.63.0.0/21", e.f.Client1, "")
	e.refused(s, `{"scan_enabled":true,"collector_group_id":"`+e.g1+`","discover_new_hosts":true}`, "discover_new_hosts")
	small := e.subnet("10.63.16.0/22", e.f.Client1, "", "10.63.16.5")
	out := e.mustPatch(small, `{"scan_enabled":true,"collector_group_id":"`+e.g1+`","discover_new_hosts":true}`)
	if num(out["targets"]) != 1022 || e.managed(small) != 1022 {
		t.Fatalf("/22 with discover: %v, %d probes, want 1022", out, e.managed(small))
	}
}

// TestScanIPv6OnlyKnown: an IPv6 subnet scans its known addresses only;
// discover_new_hosts is 422 there.
func TestScanIPv6OnlyKnown(t *testing.T) {
	e := newScanEnv(t)
	s := e.subnet("2001:db8:62::/64", e.f.Client1, "", "2001:db8:62::1", "2001:db8:62::2")
	e.refused(s, `{"scan_enabled":true,"collector_group_id":"`+e.g1+`","discover_new_hosts":true}`, "discover_new_hosts")
	out := e.mustPatch(s, `{"scan_enabled":true,"collector_group_id":"`+e.g1+`"}`)
	if num(out["targets"]) != 2 || e.count(`SELECT count(*) FROM probe WHERE managed_key = $1`, "scan:"+s+":2001:db8:62::2") != 1 {
		t.Fatalf("IPv6 scan %v", out)
	}
}

// TestScanCollectorGroupOtherClient422: a collector group of another
// client, an unknown one and none while enabled are 422; an org-wide group
// is accepted; a subnet without client takes the group's client.
func TestScanCollectorGroupOtherClient422(t *testing.T) {
	e := newScanEnv(t)
	s := e.subnet("10.62.5.0/24", e.f.Client1, "", "10.62.5.1")
	e.refused(s, `{"scan_enabled":true,"collector_group_id":"`+e.g2+`"}`, "collector_group_id")
	e.refused(s, `{"scan_enabled":true,"collector_group_id":"00000000-0000-4000-8000-000000000001"}`, "collector_group_id")
	e.refused(s, `{"scan_enabled":true}`, "collector_group_id")
	e.mustPatch(s, `{"scan_enabled":true,"collector_group_id":"`+e.gOrg+`"}`)
	orgWide := e.subnet("10.62.6.0/24", "", "", "10.62.6.1")
	e.refused(orgWide, `{"scan_enabled":true,"collector_group_id":"`+e.gOrg+`"}`, "collector_group_id")
	e.mustPatch(orgWide, `{"scan_enabled":true,"collector_group_id":"`+e.g2+`"}`)
	if n := e.count(`SELECT count(*) FROM probe WHERE managed_key = $1 AND client_id = $2`, "scan:"+orgWide+":10.62.6.1", e.f.Client2); n != 1 {
		t.Fatal("the probe of an org-wide subnet does not carry the group's client")
	}
}

// TestScanIntervalBelowTypeMinimum422: an interval outside 10 s to 1 h and
// offline_after_s below the effective interval are 422.
func TestScanIntervalBelowTypeMinimum422(t *testing.T) {
	e := newScanEnv(t)
	s := e.subnet("10.62.7.0/24", e.f.Client1, "", "10.62.7.1")
	on := `"scan_enabled":true,"collector_group_id":"` + e.g1 + `"`
	e.refused(s, `{`+on+`,"scan_interval_s":5}`, "scan_interval_s")
	e.refused(s, `{`+on+`,"scan_interval_s":3601}`, "scan_interval_s")
	e.refused(s, `{`+on+`,"scan_interval_s":600,"offline_after_s":300}`, "offline_after_s")
	e.refused(s, `{`+on+`,"offline_after_s":30}`, "offline_after_s")
	e.refused(s, `{"bogus":1}`, "bogus")
	out := e.mustPatch(s, `{`+on+`,"scan_interval_s":10,"offline_after_s":60}`)
	if num(out["scan_interval_s"]) != 10 || num(out["offline_after_s"]) != 60 {
		t.Fatalf("scan %v", out)
	}
}

// TestManagedProbeNotEditable409: PATCH and DELETE of a managed probe over
// /api/v1/monitoring/probes/{id} are 409 with field managed_by.
func TestManagedProbeNotEditable409(t *testing.T) {
	e := newScanEnv(t)
	s := e.subnet("10.62.8.0/24", e.f.Client1, "", "10.62.8.1")
	e.mustPatch(s, `{"scan_enabled":true,"collector_group_id":"`+e.g1+`"}`)
	id := e.scalar(`SELECT id::text FROM probe WHERE managed_key = $1`, "scan:"+s+":10.62.8.1")
	for _, c := range []struct{ method, body string }{{http.MethodPatch, `{"enabled":false}`}, {http.MethodDelete, ""}} {
		code, out := e.call(c.method, "/api/v1/monitoring/probes/"+id, c.body)
		if code != http.StatusConflict || out["field"] != "managed_by" {
			t.Errorf("%s managed probe = %d %v, want 409 managed_by", c.method, code, out)
		}
	}
	if e.count(`SELECT count(*) FROM probe WHERE id = $1 AND enabled`, id) != 1 {
		t.Fatal("the managed probe changed")
	}
}

// TestScanGateRequiresIpamAndMonitoring: the rule /api/v1/subnets + /scan
// needs ipam and monitoring; without monitoring 403 feature-not-entitled.
func TestScanGateRequiresIpamAndMonitoring(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPatch} {
		got := entitlement.RequiredFeatures(method, "/api/v1/subnets/00000000-0000-4000-8000-000000000001/scan")
		if strings.Join(got, ",") != entitlement.FeatureIPAM+","+entitlement.FeatureMonitoring {
			t.Fatalf("%s: features %v, want ipam and monitoring", method, got)
		}
	}
	const org = "00000000-0000-4000-8000-0000000000aa"
	repo := entitlement.NewMemoryRepository()
	svc := entitlement.NewService(repo, entitlement.Options{Enforce: true})
	if _, err := svc.Grant(context.Background(), &entitlement.Entitlement{OrganizationID: org, FeatureKey: entitlement.FeatureIPAM,
		Plan: entitlement.PlanStandard, Enabled: true, Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	reached := false
	h := svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached = true; w.WriteHeader(http.StatusOK) }))
	serve := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/subnets/00000000-0000-4000-8000-000000000001/scan", strings.NewReader(`{}`))
		req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: org}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	w := serve()
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "feature-not-entitled") || reached {
		t.Fatalf("without monitoring: %d %s", w.Code, w.Body.String())
	}
	if _, err := svc.Grant(context.Background(), &entitlement.Entitlement{OrganizationID: org, FeatureKey: entitlement.FeatureMonitoring,
		Plan: entitlement.PlanStandard, Enabled: true, Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	if w = serve(); w.Code != http.StatusOK || !reached {
		t.Fatalf("with ipam and monitoring: %d %s", w.Code, w.Body.String())
	}
}

// countingBudget admits every change and records the last budget after it.
type countingBudget struct{ after entitlement.ProbeBudget }

func (b *countingBudget) AllowProbeChange(_ context.Context, _ string, _, after entitlement.ProbeBudget) error {
	b.after = after
	return nil
}
func (b *countingBudget) MinProbeIntervalS(context.Context, string) (int64, error) { return 0, nil }
func (b *countingBudget) CheckInterval(context.Context, string, int) error         { return nil }

// scanStatements enables the scan of a fresh subnet of cidr with
// discover_new_hosts and returns all statements of the request and those
// of its scan transaction (from the BEGIN before the subnet update to its
// commit).
func scanStatements(t *testing.T, e *scanEnv, trace *scopetest.QueryTrace, cidr string, want int) (all, scan, sync int) {
	t.Helper()
	s := e.subnet(cidr, e.f.Client1, "")
	if err := trace.Pool.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	stmts := trace.Run(t, func() error {
		code, out := e.patch(s, `{"scan_enabled":true,"collector_group_id":"`+e.g1+`","discover_new_hosts":true}`)
		if code != http.StatusOK || num(out["targets"]) != want {
			return fmt.Errorf("PATCH = %d %v", code, out)
		}
		return nil
	})
	// SyncManaged runs between the read of the targets and their write; its
	// events go with the outbox append at the commit.
	inTx, txLen, found, inSync := false, 0, false, false
	for _, st := range stmts {
		sql := strings.TrimSpace(st.SQL)
		if strings.HasPrefix(sql, "SET ROLE") {
			continue
		}
		all++
		switch {
		case strings.HasPrefix(sql, "WITH cand AS"):
			inSync = true
			continue
		case strings.Contains(sql, "unnest($3::inet[]"):
			inSync = false
		case strings.Contains(sql, "INSERT INTO event_outbox") && found && scan == 0:
			sync++
		}
		if inSync {
			sync++
		}
		switch {
		case strings.HasPrefix(sql, "BEGIN"):
			inTx, txLen = true, 0
		case strings.Contains(sql, "UPDATE subnet SET scan_enabled"):
			found = true
		}
		if inTx {
			txLen++
		}
		if strings.EqualFold(sql, "commit") {
			if found && scan == 0 {
				scan = txLen
			}
			inTx = false
		}
	}
	if scan == 0 {
		for _, st := range stmts {
			t.Logf("%.140s", strings.Join(strings.Fields(st.SQL), " "))
		}
		t.Fatal("no scan transaction")
	}
	return all, scan, sync
}

// TestScanSyncStatementBudget covers AK8: enabling a /22 with
// discover_new_hosts sends as many statements as a /29, and its scan
// transaction (BEGIN to COMMIT, with the budget lock and check, the
// probes, the targets and the events) at most 12; the route audit adds
// its fixed transactions. The budget counts every managed probe.
func TestScanSyncStatementBudget(t *testing.T) {
	f := scopetest.Seed(t, "9e")
	trace := scopetest.NewQueryTrace(t, f.App)
	e := newScanEnvOn(t, f, trace.Pool)
	e.groups()
	budget := &countingBudget{}
	e.repo.SetBudget(budget)
	smallAll, smallScan, smallSync := scanStatements(t, e, trace, "10.64.8.0/29", 6)
	all, scan, sync := scanStatements(t, e, trace, "10.64.0.0/22", 1022)
	t.Logf("/29: %d statements (scan transaction %d, SyncManaged %d); /22: %d statements (scan transaction %d, SyncManaged %d)",
		smallAll, smallScan, smallSync, all, scan, sync)
	if all != smallAll || scan != smallScan || sync != smallSync {
		t.Fatalf("statements depend on the address count: /29 %d/%d/%d, /22 %d/%d/%d", smallAll, smallScan, smallSync, all, scan, sync)
	}
	// AK4 (Nachtrag 2026-11-13): an org-wide principal without CI layers,
	// the plan minimum from the budget service aside.
	if sync > 4 {
		t.Fatalf("SyncManaged sent %d statements, budget 4", sync)
	}
	if scan > 12 {
		t.Fatalf("scan transaction of %d statements, budget 12", scan)
	}
	if budget.after.PerType["icmp"] < 1022+6 {
		t.Fatalf("budget after %+v, want every managed probe counted", budget.after)
	}
}

// TestScanTargetRLSClientScope: a principal of client 1 sees only the
// targets of its subnets; the scan of a subnet of client 2 is 404 for it.
func TestScanTargetRLSClientScope(t *testing.T) {
	e := newScanEnv(t)
	s1 := e.subnet("10.62.9.0/24", e.f.Client1, "", "10.62.9.1", "10.62.9.2")
	s2 := e.subnet("10.62.10.0/24", e.f.Client2, "", "10.62.10.1")
	e.mustPatch(s1, `{"scan_enabled":true,"collector_group_id":"`+e.g1+`"}`)
	e.mustPatch(s2, `{"scan_enabled":true,"collector_group_id":"`+e.g2+`"}`)
	var visible []string
	err := database.WithRequestTenant(e.f.ClientCtx(e.f.Client1), e.f.App, e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT subnet_id::text FROM subnet_scan_target`)
		if err != nil {
			return err
		}
		visible, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range visible {
		if id != s1 {
			t.Fatalf("client 1 sees a target of subnet %s", id)
		}
	}
	if len(visible) != 2 {
		t.Fatalf("client 1 sees %d targets, want 2", len(visible))
	}
	e.scopedClients = []string{e.f.Client1}
	if code, _ := e.call(http.MethodGet, "/api/v1/subnets/"+s2+"/scan", ""); code != http.StatusNotFound {
		t.Fatalf("GET scan of client 2 as client 1 = %d, want 404", code)
	}
	if code, out := e.call(http.MethodGet, "/api/v1/subnets/"+s1+"/scan", ""); code != http.StatusOK || num(out["targets"]) != 2 {
		t.Fatalf("GET own scan = %d %v", code, out)
	}
}
