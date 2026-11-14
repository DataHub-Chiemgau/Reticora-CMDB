package ipam_test

// WP-1064 (IPM-12): the DNS reverse check against PostgreSQL as
// reticora_app. The results reach IPAM over the ingest path
// (probe.PGIngestStore and the result sink of ptr.go).
//
// Retest: cd backend && TEST_DATABASE_URL=… go test -count=1 -p 1 -run 'TestPTR' ./internal/ipam/

import (
	"context"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// ptrEnv is a scanEnv with a scanned subnet of client 1 in the default
// section and a collector of client 1 for the results.
type ptrEnv struct {
	*scanEnv
	section, subnet, collector string
	seq                        uint64
}

func newPTREnv(t *testing.T, dns bool, addrs ...string) *ptrEnv {
	t.Helper()
	e := &ptrEnv{scanEnv: newScanEnv(t)}
	e.section = e.scalar(`SELECT id::text FROM ipam_section WHERE organization_id = $1 AND is_default`, e.f.OrgA)
	e.subnet = e.scanEnv.subnet("10.65.0.0/24", e.f.Client1, "", addrs...)
	if dns {
		e.exec(`UPDATE subnet SET dns_servers = '{10.65.0.53}' WHERE id = $1`, e.subnet)
	}
	e.mustPatch(e.subnet, `{"scan_enabled":true,"collector_group_id":"`+e.g1+`"}`)
	e.collector = e.scalar(`INSERT INTO collector (organization_id, client_id, name, status) VALUES ($1, $2, 'wp1064', 'active')
		RETURNING id::text`, e.f.OrgA, e.f.Client1)
	// A member of the scan group (instead of its own default group): the
	// PTR probes are assigned to it.
	e.exec(`DELETE FROM collector_group_member WHERE organization_id = $1 AND collector_id = $2`, e.f.OrgA, e.collector)
	e.exec(`INSERT INTO collector_group_member (group_id, collector_id, organization_id) VALUES ($1, $2, $3)`, e.g1, e.collector, e.f.OrgA)
	return e
}

func (e *ptrEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.f.Admin.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *ptrEnv) mode(mode string) (int, map[string]any) {
	e.t.Helper()
	return e.call(http.MethodPatch, "/api/v1/ipam-sections/"+e.section, `{"ptr_mode":"`+mode+`"}`)
}

func (e *ptrEnv) mustMode(mode string) {
	e.t.Helper()
	if code, out := e.mode(mode); code != http.StatusOK || out["ptr_mode"] != mode {
		e.t.Fatalf("PATCH ptr_mode %s = %d %v", mode, code, out)
	}
}

func (e *ptrEnv) ptrProbes() int {
	e.t.Helper()
	return e.count(`SELECT count(*) FROM probe WHERE organization_id = $1 AND managed_by = 'ipam_ptr' AND managed_key LIKE $2`,
		e.f.OrgA, ipam.PTRKeyPrefix(e.subnet)+"%")
}

func (e *ptrEnv) probeOf(addr string) string {
	e.t.Helper()
	return e.scalar(`SELECT id::text FROM probe WHERE organization_id = $1 AND managed_by = 'ipam_ptr' AND managed_key = $2`,
		e.f.OrgA, ipam.PTRKey(e.subnet, addr))
}

// assignAll assigns every PTR probe of the subnet to the collector of the
// results with epoch 1, so its results are current (not stale, PRB-11).
func (e *ptrEnv) assignAll() {
	e.t.Helper()
	e.exec(`INSERT INTO probe_assignment (probe_id, organization_id, client_id, collector_id, epoch)
		SELECT p.id, p.organization_id, p.client_id, $2, 1 FROM probe p
		 WHERE p.organization_id = $1 AND p.managed_by = 'ipam_ptr' AND p.managed_key LIKE $3
		ON CONFLICT (probe_id) DO UPDATE SET collector_id = EXCLUDED.collector_id, epoch = EXCLUDED.epoch`,
		e.f.OrgA, e.collector, ipam.PTRKeyPrefix(e.subnet)+"%")
}

// result is a dns result of the PTR probe of addr: a name, NXDOMAIN
// (name "") or a timeout (timeout true); the probe is assigned to the
// collector of the results first.
func (e *ptrEnv) result(addr, name string, timeout bool) probewire.Result {
	e.t.Helper()
	e.assignAll()
	e.seq++
	r := probewire.Result{ProbeID: e.probeOf(addr), Epoch: 1, Seq: e.seq, ObservedAt: time.Now().UTC(), Status: probewire.StatusOK}
	switch {
	case timeout:
		r.Status, r.Code = probewire.StatusFail, probewire.ReasonTimeout
	case name == "":
		r.Status, r.Code = probewire.StatusOK, probewire.WarnAnswerMismatch
		r.Values = map[string]float64{"probe_dns_rcode": 3, "probe_dns_answer_count": 0}
	default:
		r.Values = map[string]float64{"probe_dns_rcode": 0, "probe_dns_answer_count": 1, "probe_dns_ptr": 1}
		r.Labels = map[string]string{"name": name}
	}
	return r
}

func (e *ptrEnv) deliver(results ...probewire.Result) probe.IngestOutcome {
	e.t.Helper()
	out, err := probe.NewPGIngestStore(e.f.App).WriteResults(context.Background(), e.f.OrgA,
		[]probe.ResultsMessage{{CollectorID: e.collector, Results: results}})
	if err != nil {
		e.t.Fatalf("ingest: %v", err)
	}
	return out
}

// ptr is ptr_status, ptr_name and dns_name of the address.
func (e *ptrEnv) ptr(addr string) (status, name, dns string) {
	e.t.Helper()
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT COALESCE(ptr_status, ''), COALESCE(ptr_name, ''), COALESCE(dns_name, '')
		FROM ip_address WHERE organization_id = $1 AND address_host = $2`, e.f.OrgA, addr).Scan(&status, &name, &dns); err != nil {
		e.t.Fatalf("address %s: %v", addr, err)
	}
	return status, name, dns
}

// TestPTRProbesFollowSectionMode: off → no probe, report → one dns probe
// per known address with the reverse name, back to off → removed; an
// invalid mode is 422 ptr_mode.
func TestPTRProbesFollowSectionMode(t *testing.T) {
	e := newPTREnv(t, true, "10.65.0.1", "10.65.0.2")
	if n := e.ptrProbes(); n != 0 {
		t.Fatalf("ptr_mode off: %d PTR probes", n)
	}
	e.mustMode("report")
	if n := e.ptrProbes(); n != 2 {
		t.Fatalf("ptr_mode report: %d PTR probes, want 2", n)
	}
	var typ, target, qname, qtype, interval string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT type, target, params->>'qname', params->>'qtype', params->>'interval_s'
		FROM probe WHERE organization_id = $1 AND managed_key = $2`, e.f.OrgA, ipam.PTRKey(e.subnet, "10.65.0.2")).
		Scan(&typ, &target, &qname, &qtype, &interval); err != nil {
		t.Fatal(err)
	}
	if typ != "dns" || target != "10.65.0.53" || qname != "2.0.65.10.in-addr.arpa" || qtype != "PTR" || interval != "3600" {
		t.Errorf("PTR probe %s %s %s %s %s, want dns to 10.65.0.53 for 2.0.65.10.in-addr.arpa PTR every 3600 s",
			typ, target, qname, qtype, interval)
	}
	code, sec := e.call(http.MethodGet, "/api/v1/ipam-sections/"+e.section, "")
	if code != http.StatusOK || sec["ptr_mode"] != "report" {
		t.Errorf("GET section = %d %v, want ptr_mode report", code, sec)
	}
	if code, out := e.mode("sometimes"); code != http.StatusUnprocessableEntity || out["field"] != "ptr_mode" {
		t.Errorf("invalid ptr_mode = %d %v, want 422 ptr_mode", code, out)
	}
	e.mustMode("off")
	if n := e.ptrProbes(); n != 0 {
		t.Errorf("back to off: %d PTR probes", n)
	}
	if got := ipam.ReverseName(netip.MustParseAddr("2001:db8::1")); got !=
		"1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa" {
		t.Errorf("IPv6 reverse name %s", got)
	}
}

// TestPTRNoProbesWithoutNameserver: a subnet without an effective DNS
// server gets no PTR probes.
func TestPTRNoProbesWithoutNameserver(t *testing.T) {
	e := newPTREnv(t, false, "10.65.0.1")
	e.mustMode("report")
	if n := e.ptrProbes(); n != 0 {
		t.Errorf("%d PTR probes without a DNS server", n)
	}
}

// TestPTRReportListsMismatch: a deviation and a missing name are listed;
// a principal of another client sees none.
func TestPTRReportListsMismatch(t *testing.T) {
	e := newPTREnv(t, true, "10.65.0.1", "10.65.0.2", "10.65.0.3")
	e.exec(`UPDATE ip_address SET dns_name = CASE address_host WHEN '10.65.0.1' THEN 'Host1.example.net.' ELSE 'old.example.net' END
		WHERE subnet_id = $1`, e.subnet)
	e.mustMode("report")
	e.deliver(e.result("10.65.0.1", "host1.example.net", false), e.result("10.65.0.2", "new.example.net", false),
		e.result("10.65.0.3", "", false))
	for addr, want := range map[string]string{"10.65.0.1": "match", "10.65.0.2": "mismatch", "10.65.0.3": "missing"} {
		if status, _, _ := e.ptr(addr); status != want {
			t.Errorf("%s: ptr_status %q, want %s", addr, status, want)
		}
	}
	code, out := e.call(http.MethodGet, "/api/v1/ipam/ptr-mismatches?subnet_id="+e.subnet, "")
	rows, _ := out["data"].([]any)
	if code != http.StatusOK || len(rows) != 1 {
		t.Fatalf("mismatches = %d %v, want one row", code, out)
	}
	row := rows[0].(map[string]any)
	if row["address"] != "10.65.0.2" || row["ptr_name"] != "new.example.net" || row["dns_name"] != "old.example.net" ||
		row["subnet_cidr"] != "10.65.0.0/24" || row["ptr_checked_at"] == nil {
		t.Errorf("row %v", row)
	}
	_, missing := e.call(http.MethodGet, "/api/v1/ipam/ptr-mismatches?status=missing&section_id="+e.section, "")
	if m, _ := missing["data"].([]any); len(m) != 1 || m[0].(map[string]any)["address"] != "10.65.0.3" {
		t.Errorf("missing = %v, want 10.65.0.3", missing)
	}
	if code, _ := e.call(http.MethodGet, "/api/v1/ipam/ptr-mismatches?status=error", ""); code != http.StatusBadRequest {
		t.Errorf("status=error = %d, want 400", code)
	}
	e.scopedClients = []string{e.f.Client2}
	_, other := e.call(http.MethodGet, "/api/v1/ipam/ptr-mismatches", "")
	if o, _ := other["data"].([]any); len(o) != 0 {
		t.Errorf("a principal of client 2 sees %v", o)
	}
	e.scopedClients = []string{e.f.Client1}
	_, own := e.call(http.MethodGet, "/api/v1/ipam/ptr-mismatches", "")
	if o, _ := own["data"].([]any); len(o) != 1 {
		t.Errorf("a principal of client 1 sees %v, want its mismatch", o)
	}
}

// TestPTRAdoptOverwritesUnpinned: adopt takes the PTR name as dns_name.
func TestPTRAdoptOverwritesUnpinned(t *testing.T) {
	e := newPTREnv(t, true, "10.65.0.1")
	e.exec(`UPDATE ip_address SET dns_name = 'old.example.net' WHERE subnet_id = $1`, e.subnet)
	e.mustMode("adopt")
	e.deliver(e.result("10.65.0.1", "new.example.net", false))
	if status, name, dns := e.ptr("10.65.0.1"); status != "match" || name != "new.example.net" || dns != "new.example.net" {
		t.Errorf("adopt: %s %s %s, want match with dns_name new.example.net", status, name, dns)
	}
}

// TestPTRAdoptKeepsPinned: a pinned address keeps dns_name and stays a
// listed mismatch.
func TestPTRAdoptKeepsPinned(t *testing.T) {
	e := newPTREnv(t, true, "10.65.0.1")
	e.exec(`UPDATE ip_address SET dns_name = 'old.example.net', pinned = true WHERE subnet_id = $1`, e.subnet)
	e.mustMode("adopt")
	e.deliver(e.result("10.65.0.1", "new.example.net", false))
	if status, _, dns := e.ptr("10.65.0.1"); status != "mismatch" || dns != "old.example.net" {
		t.Errorf("pinned: %s %s, want mismatch with old.example.net", status, dns)
	}
	_, out := e.call(http.MethodGet, "/api/v1/ipam/ptr-mismatches", "")
	if rows, _ := out["data"].([]any); len(rows) != 1 || rows[0].(map[string]any)["pinned"] != true {
		t.Errorf("list %v, want the pinned mismatch", out)
	}
}

// TestPTRErrorKeepsName: a timeout after a match keeps ptr_name.
func TestPTRErrorKeepsName(t *testing.T) {
	e := newPTREnv(t, true, "10.65.0.1")
	e.exec(`UPDATE ip_address SET dns_name = 'host.example.net' WHERE subnet_id = $1`, e.subnet)
	e.mustMode("report")
	e.deliver(e.result("10.65.0.1", "host.example.net", false))
	e.deliver(e.result("10.65.0.1", "", true))
	if status, name, _ := e.ptr("10.65.0.1"); status != "error" || name != "host.example.net" {
		t.Errorf("after the timeout: %s %s, want error with the name kept", status, name)
	}
}

// TestPTRSinkSkipsRedelivery: the same result twice is evaluated once.
func TestPTRSinkSkipsRedelivery(t *testing.T) {
	e := newPTREnv(t, true, "10.65.0.1")
	e.exec(`UPDATE ip_address SET dns_name = 'old.example.net' WHERE subnet_id = $1`, e.subnet)
	e.mustMode("report")
	r := e.result("10.65.0.1", "new.example.net", false)
	e.deliver(r)
	// dns_name now equals the name: a second evaluation would say match.
	e.exec(`UPDATE ip_address SET dns_name = 'new.example.net' WHERE subnet_id = $1`, e.subnet)
	if out := e.deliver(r); out.Written != 0 {
		t.Fatalf("redelivery wrote %d results", out.Written)
	}
	if status, _, _ := e.ptr("10.65.0.1"); status != "mismatch" {
		t.Errorf("after the redelivery: %s, want mismatch (no second evaluation)", status)
	}
}

// TestPTRSinkStatementBudget: 1,000 results with at most three statements
// of the sink.
func TestPTRSinkStatementBudget(t *testing.T) {
	e := newPTREnv(t, true)
	e.exec(`UPDATE subnet SET cidr = '10.66.0.0/22' WHERE id = $1`, e.subnet)
	e.exec(`INSERT INTO ip_address (organization_id, subnet_id, address, type, origin, dns_name)
		SELECT $1, $2, '10.66.0.0'::inet + n, 'static', 'manual', 'h' || n || '.example.net' FROM generate_series(1, 1000) AS n`,
		e.f.OrgA, e.subnet)
	e.mustMode("report")
	if n := e.ptrProbes(); n != 1000 {
		t.Fatalf("%d PTR probes, want 1000", n)
	}
	e.assignAll()
	// The results of the real PTR probes over the ingest path
	// (PGIngestStore.WriteResults with the sink of ptr.go).
	probeIDs := map[string]string{}
	rows, err := e.f.Admin.Query(context.Background(), `SELECT managed_key, id::text FROM probe
		WHERE organization_id = $1 AND managed_by = 'ipam_ptr' AND managed_key LIKE $2`, e.f.OrgA, ipam.PTRKeyPrefix(e.subnet)+"%")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var key, id string
		if err := rows.Scan(&key, &id); err != nil {
			t.Fatal(err)
		}
		probeIDs[key] = id
	}
	rows.Close()
	results := make([]probewire.Result, 0, 1000)
	now := time.Now().UTC()
	addr := netip.MustParseAddr("10.66.0.0")
	for range 1000 {
		addr = addr.Next()
		name := "h" + strings.TrimPrefix(addr.String(), "10.66.") + ".example.net"
		e.seq++
		results = append(results, probewire.Result{ProbeID: probeIDs[ipam.PTRKey(e.subnet, addr.String())], Epoch: 1, Seq: e.seq,
			ObservedAt: now, Status: probewire.StatusOK,
			Values: map[string]float64{"probe_dns_rcode": 0, "probe_dns_answer_count": 1, "probe_dns_ptr": 1},
			Labels: map[string]string{"name": name}})
	}
	trace := scopetest.NewQueryTrace(t, e.f.App)
	stmts := trace.Run(t, func() error {
		out, err := probe.NewPGIngestStore(trace.Pool).WriteResults(context.Background(), e.f.OrgA,
			[]probe.ResultsMessage{{CollectorID: e.collector, Results: results}})
		if err == nil && out.Written != 1000 {
			t.Errorf("written %d results, want 1000", out.Written)
		}
		return err
	})
	// The statements of the sink: the change source and scope, the update
	// of ip_address and the restore (AK5: at most 3 per batch).
	var sink []string
	for _, st := range stmts {
		if strings.Contains(st.SQL, "ip_address") || strings.Contains(st.SQL, "app.ipam_change_source") {
			sink = append(sink, strings.Join(strings.Fields(st.SQL), " ")[:min(80, len(strings.Join(strings.Fields(st.SQL), " ")))])
		}
	}
	if len(sink) > 3 {
		t.Errorf("%d sink statements for 1000 results, budget 3: %q", len(sink), sink)
	}
	if n := e.count(`SELECT count(*) FROM ip_address WHERE subnet_id = $1 AND ptr_status = 'mismatch'`, e.subnet); n != 1000 {
		t.Errorf("%d mismatches, want 1000 (the names differ in the third octet)", n)
	}
}

// TestPTRHistorySourceScan: the PTR fields are in the history with source
// scan, ptr_checked_at is not.
func TestPTRHistorySourceScan(t *testing.T) {
	e := newPTREnv(t, true, "10.65.0.1")
	e.mustMode("report")
	e.deliver(e.result("10.65.0.1", "host.example.net", false))
	id := e.scalar(`SELECT id::text FROM ip_address WHERE subnet_id = $1`, e.subnet)
	if n := e.count(`SELECT count(*) FROM ipam_change WHERE object_id = $1 AND field_name IN ('ptr_status', 'ptr_name')
		AND source = 'scan'`, id); n != 2 {
		t.Errorf("%d history rows of ptr_status and ptr_name with source scan, want 2", n)
	}
	if n := e.count(`SELECT count(*) FROM ipam_change WHERE object_id = $1 AND field_name = 'ptr_checked_at'`, id); n != 0 {
		t.Errorf("ptr_checked_at in the history")
	}
	// The ingest transaction keeps its own change source afterwards.
	if n := e.count(`SELECT count(*) FROM ipam_change WHERE object_id = $1 AND source <> 'scan' AND changed_at > now() - interval '1 minute'
		AND field_name LIKE 'ptr%'`, id); n != 0 {
		t.Errorf("%d PTR history rows with another source", n)
	}
}

// TestPTRRefusedIsErrorKeepsName: a server that answers REFUSED (5) or
// SERVFAIL (2) carries probe_dns_rcode but no PTR name: error, ptr_name
// kept (AK5), not missing.
func TestPTRRefusedIsErrorKeepsName(t *testing.T) {
	e := newPTREnv(t, true, "10.65.0.1")
	e.exec(`UPDATE ip_address SET dns_name = 'host.example.net' WHERE subnet_id = $1`, e.subnet)
	e.mustMode("report")
	e.deliver(e.result("10.65.0.1", "host.example.net", false))
	for _, rcode := range []float64{5, 2} {
		r := e.result("10.65.0.1", "", false)
		r.Values = map[string]float64{"probe_dns_rcode": rcode, "probe_dns_answer_count": 0}
		e.deliver(r)
		if status, name, _ := e.ptr("10.65.0.1"); status != ipam.PTRStatusError || name != "host.example.net" {
			t.Errorf("rcode %v: %s %s, want error with the name kept", rcode, status, name)
		}
	}
}

// TestPTRStaleResultIgnored: a result of an old epoch or from another
// than the assigned collector is stored stale and reaches no sink: it
// changes neither ptr_name nor dns_name (PRB-11).
func TestPTRStaleResultIgnored(t *testing.T) {
	e := newPTREnv(t, true, "10.65.0.1")
	e.exec(`UPDATE ip_address SET dns_name = 'host.example.net' WHERE subnet_id = $1`, e.subnet)
	e.mustMode("adopt")
	e.deliver(e.result("10.65.0.1", "host.example.net", false))
	old := e.result("10.65.0.1", "evil.example.net", false)
	old.Epoch = 7
	e.deliver(old)
	other := e.scalar(`INSERT INTO collector (organization_id, client_id, name, status) VALUES ($1, $2, 'wp1064-other', 'active')
		RETURNING id::text`, e.f.OrgA, e.f.Client1)
	foreign := e.result("10.65.0.1", "evil.example.net", false)
	if _, err := probe.NewPGIngestStore(e.f.App).WriteResults(context.Background(), e.f.OrgA,
		[]probe.ResultsMessage{{CollectorID: other, Results: []probewire.Result{foreign}}}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if status, name, dns := e.ptr("10.65.0.1"); status != ipam.PTRStatusMatch || name != "host.example.net" || dns != "host.example.net" {
		t.Errorf("after stale results: %s %s %s, want match with host.example.net kept", status, name, dns)
	}
}

// TestPTRProbesFollowSubnetDNSServers: the PTR probes follow the DNS
// servers of the subnet in the PATCH transaction (AK2): no server → none,
// a new server → the target follows, the nameserver set and its servers,
// a section with ptr_mode off → none.
func TestPTRProbesFollowSubnetDNSServers(t *testing.T) {
	e := newPTREnv(t, true, "10.65.0.1")
	e.mustMode("report")
	target := func() string {
		return e.scalar(`SELECT target FROM probe WHERE organization_id = $1 AND managed_key = $2`, e.f.OrgA, ipam.PTRKey(e.subnet, "10.65.0.1"))
	}
	patch := func(body string) {
		t.Helper()
		if code, out := e.call(http.MethodPatch, "/api/v1/subnets/"+e.subnet, body); code != http.StatusOK {
			t.Fatalf("PATCH subnet %s = %d %v", body, code, out)
		}
	}
	patch(`{"dns_servers":["10.65.0.54"]}`)
	if n, got := e.ptrProbes(), target(); n != 1 || got != "10.65.0.54" {
		t.Errorf("new server: %d probes to %s, want 1 to 10.65.0.54", n, got)
	}
	patch(`{"dns_servers":[]}`)
	if n := e.ptrProbes(); n != 0 {
		t.Errorf("without a DNS server: %d PTR probes, want 0", n)
	}
	set := e.mustID("/api/v1/nameserver-sets", `{"name":"wp1064-ns","servers":["10.65.0.60"]}`)
	patch(`{"nameserver_set_id":"` + set + `"}`)
	if n, got := e.ptrProbes(), target(); n != 1 || got != "10.65.0.60" {
		t.Errorf("nameserver set: %d probes to %s, want 1 to 10.65.0.60", n, got)
	}
	if code, out := e.call(http.MethodPatch, "/api/v1/nameserver-sets/"+set, `{"servers":["10.65.0.61"]}`); code != http.StatusOK {
		t.Fatalf("PATCH nameserver set = %d %v", code, out)
	}
	if got := target(); got != "10.65.0.61" {
		t.Errorf("new servers of the set: target %s, want 10.65.0.61", got)
	}
	off := e.mustID("/api/v1/ipam-sections", `{"name":"wp1064-off"}`)
	patch(`{"section_id":"` + off + `"}`)
	if n := e.ptrProbes(); n != 0 {
		t.Errorf("section with ptr_mode off: %d PTR probes, want 0", n)
	}
}
