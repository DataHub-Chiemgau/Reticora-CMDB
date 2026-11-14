package admp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/admp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// The affected connections of an outage (ADM-08, ALR-09; WP-931): the
// redundancy over a second router, the statement budget for 500 CIs, the
// client scope, the stale flag with WithRefreshStale, the memory store of
// --no-db with the same result and the redundancy on the CI route.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -run 'TestAffectedConnections|TestCIAdmpConnectionsCarryRedundancy' ./internal/admp/

// seedOutage stores the scenario of a router outage through repo:
// connection 1 has a service with a port on router A and one on router B,
// connection 2 a service with a port on router A only; ifA and ifB are
// interfaces of the routers.
func seedOutage(t *testing.T, repo admp.Repository, ctx context.Context, org, account, routerA, ifA, routerB, ifB string,
	now time.Time) {
	t.Helper()
	exp := now.Add(30 * 24 * time.Hour)
	for _, line := range []struct {
		external string
		ports    []admp.ServicePort
	}{
		{"line-1", []admp.ServicePort{
			{PortKey: "11", NetworkInterfaceID: ifA, CIID: routerA, MatchedBy: admp.MatchedByRule},
			{PortKey: "21", NetworkInterfaceID: ifB, CIID: routerB, MatchedBy: admp.MatchedByRule}}},
		{"line-2", []admp.ServicePort{{PortKey: "12", NetworkInterfaceID: ifA, CIID: routerA, MatchedBy: admp.MatchedByRule}}},
	} {
		c := &admp.Connection{OrganizationID: org, AccountID: account, ExternalID: line.external, Carrier: "Netz AG",
			ContractID: "V-" + line.external, FetchedAt: now, ExpiresAt: exp}
		if err := repo.UpsertConnection(ctx, c); err != nil {
			t.Fatal(err)
		}
		s := &admp.Service{OrganizationID: org, AccountID: account, ExternalID: "svc-" + line.external, ConnectionID: c.ID,
			Kind: "internet", FetchedAt: now, ExpiresAt: exp}
		if err := repo.UpsertService(ctx, s); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.ReplaceServicePorts(ctx, org, s.ID, line.ports); err != nil {
			t.Fatal(err)
		}
	}
}

// affectedSummary is an entry without ids: external id, port, redundancy
// with the routers named A and B.
func affectedSummary(rows []admp.AffectedConnection, names map[string]string) []string {
	out := []string{}
	for _, r := range rows {
		via := []string{}
		for _, ci := range r.RedundantVia {
			via = append(via, names[ci])
		}
		out = append(out, fmt.Sprintf("%s/%s/%s/%s/%s/%v/%v", r.ExternalID, r.ContractID, r.ServiceKind, r.PortKey, names[r.CIID],
			r.Redundant, via))
	}
	return out
}

// affectedFull is an entry with every field: ids through names (the
// account, the routers and interfaces), the connection and service ids
// through their external ids, fetched_at as offset from seeded.
func affectedFull(t *testing.T, rows []admp.AffectedConnection, names map[string]string, seeded time.Time) []string {
	t.Helper()
	connections, services := map[string]string{}, map[string]string{}
	out := []string{}
	for _, r := range rows {
		if r.ConnectionID == "" || r.ServiceID == "" {
			t.Fatalf("entry without ids: %+v", r)
		}
		if ext, ok := connections[r.ConnectionID]; ok && ext != r.ExternalID {
			t.Fatalf("connection %s with external ids %s and %s", r.ConnectionID, ext, r.ExternalID)
		}
		connections[r.ConnectionID] = r.ExternalID
		if kind, ok := services[r.ServiceID]; ok && kind != r.ServiceKind {
			t.Fatalf("service %s with kinds %s and %s", r.ServiceID, kind, r.ServiceKind)
		}
		services[r.ServiceID] = r.ServiceKind
		via := []string{}
		for _, ci := range r.RedundantVia {
			via = append(via, names[ci])
		}
		out = append(out, fmt.Sprintf("%s/%s/%s/%s/%s/%s/%s/%s/%s/stale=%v/%s/%v/%v", r.ExternalID, names[r.AccountID], r.Carrier,
			r.ContractID, r.ServiceKind, r.PortKey, names[r.CIID], names[r.NetworkInterfaceID], connections[r.ConnectionID], r.Stale,
			r.FetchedAt.Sub(seeded), r.Redundant, via))
	}
	return out
}

// ageService stores the service of a line again with an older fetched_at
// and marks it stale.
func ageService(t *testing.T, repo admp.Repository, ctx context.Context, org, account, external string, fetchedAt time.Time) {
	t.Helper()
	s, err := repo.ServiceByExternal(ctx, org, account, external)
	if err != nil {
		t.Fatal(err)
	}
	s.FetchedAt = fetchedAt
	if err = repo.UpsertService(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err = repo.MarkStale(ctx, org, admp.KindService, s.ID, "timeout"); err != nil {
		t.Fatal(err)
	}
}

func (e *apiEnv) routerB() (ci, iface string) {
	e.t.Helper()
	ci = e.f.CI(e.t, e.f.OrgA, e.f.Client1, "wp931-router-b")
	iface = e.scalar(`INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, 'ge-0/0/3') RETURNING id::text`,
		e.f.OrgA, ci)
	return ci, iface
}

var outageWant = []string{
	"line-1/V-line-1/internet/11/A/true/[B]",
	"line-2/V-line-2/internet/12/A/false/[]",
}

// TestAffectedConnectionsRouterOutage: router A fails; connection 1 also
// has a port on router B and is redundant via B, connection 2 is not; the
// failure of both routers leaves no redundancy; the interface of A alone
// gives the same as the CI.
func TestAffectedConnectionsRouterOutage(t *testing.T) {
	e := newAPIEnv(t)
	ciB, ifB := e.routerB()
	seedOutage(t, e.cache, e.org(), e.f.OrgA, e.account1, e.ci1, e.iface1, ciB, ifB, e.now)
	impact := admp.NewImpact(e.cache, nil)
	names := map[string]string{e.ci1: "A", ciB: "B"}
	rows, err := impact.AffectedConnections(e.org(), e.f.OrgA, []string{e.ci1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := affectedSummary(rows, names); !reflect.DeepEqual(got, outageWant) {
		t.Errorf("outage of A: %v, want %v", got, outageWant)
	}
	if r := rows[0]; r.Carrier != "Netz AG" || r.AccountID != e.account1 || r.NetworkInterfaceID != e.iface1 || r.Stale ||
		!r.FetchedAt.Equal(e.now) || r.ConnectionID == "" || r.ServiceID == "" {
		t.Errorf("entry %+v", r)
	}
	rows, err = impact.AffectedConnections(e.org(), e.f.OrgA, []string{e.ci1, ciB}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Redundant {
			t.Errorf("outage of A and B: %s redundant via %v", r.ExternalID, r.RedundantVia)
		}
	}
	if len(rows) != 3 {
		t.Errorf("outage of A and B: %d entries, want 3 (two ports of line-1, one of line-2)", len(rows))
	}
	rows, err = impact.AffectedConnections(e.org(), e.f.OrgA, nil, []string{e.iface1})
	if err != nil {
		t.Fatal(err)
	}
	if got := affectedSummary(rows, names); !reflect.DeepEqual(got, outageWant) {
		t.Errorf("outage of the interface of A: %v, want %v", got, outageWant)
	}
	if rows, err = impact.AffectedConnections(e.org(), e.f.OrgA, nil, nil); err != nil || len(rows) != 0 {
		t.Errorf("no CIs: %v %v, want none", rows, err)
	}
}

// TestAffectedConnectionsStatementBudget: 500 affected CIs cost at most 3
// statements besides the tenant frame, as many as 2 CIs.
func TestAffectedConnectionsStatementBudget(t *testing.T) {
	e := newAPIEnv(t)
	ciB, ifB := e.routerB()
	seedOutage(t, e.cache, e.org(), e.f.OrgA, e.account1, e.ci1, e.iface1, ciB, ifB, e.now)
	e.exec(`INSERT INTO ci (organization_id, client_id, ci_type_id, name, status)
		SELECT $1, $2, t.id, 'wp931-budget-' || i, 'active' FROM generate_series(1, 498) AS i
		JOIN ci_type t ON t.key = 'server' AND t.organization_id IS NULL`, e.f.OrgA, e.f.Client1)
	many := strings.Split(e.scalar(`SELECT string_agg(id::text, ',') FROM ci WHERE organization_id = $1 AND name LIKE 'wp931-budget-%'`,
		e.f.OrgA), ",")
	many = append(many, e.ci1, ciB)
	if len(many) != 500 {
		t.Fatalf("%d CIs, want 500", len(many))
	}
	trace := scopetest.NewQueryTrace(t, e.f.App)
	impact := admp.NewImpact(admp.NewPGRepository(trace.Pool), nil)
	count := func(ciIDs []string) (int, int) {
		t.Helper()
		var rows []admp.AffectedConnection
		stmts := trace.Run(t, func() error {
			var err error
			rows, err = impact.AffectedConnections(e.org(), e.f.OrgA, ciIDs, nil)
			return err
		})
		n := 0
		for _, s := range stmts {
			if !frameStatement(s.SQL) {
				n++
			}
		}
		return n, len(rows)
	}
	count([]string{e.ci1}) // warms the connection
	small, _ := count([]string{e.ci1, ciB})
	large, entries := count(many)
	if large > 3 || large != small {
		t.Errorf("statements: %d for 500 CIs, %d for 2; want the same and at most 3", large, small)
	}
	if entries != 3 {
		t.Errorf("%d entries for 500 CIs, want 3", entries)
	}
	t.Logf("affected connections of 500 CIs: %d statements besides the tenant frame", large)
}

// TestAffectedConnectionsClientScope: a principal of client 2 gets no
// entry of client 1's account, a principal of client 1 all of them; a call
// without request scope (a worker) runs organization-wide.
func TestAffectedConnectionsClientScope(t *testing.T) {
	e := newAPIEnv(t)
	ciB, ifB := e.routerB()
	seedOutage(t, e.cache, e.org(), e.f.OrgA, e.account1, e.ci1, e.iface1, ciB, ifB, e.now)
	impact := admp.NewImpact(e.cache, nil)
	ids := []string{e.ci1, e.ci2}
	if rows, err := impact.AffectedConnections(e.f.ClientCtx(e.f.Client2), e.f.OrgA, ids, nil); err != nil || len(rows) != 0 {
		t.Errorf("client 2: %d entries (%v), want none", len(rows), err)
	}
	if rows, err := impact.AffectedConnections(e.f.ClientCtx(e.f.Client1), e.f.OrgA, ids, nil); err != nil || len(rows) != 2 {
		t.Errorf("client 1: %d entries (%v), want 2", len(rows), err)
	}
	if rows, err := impact.AffectedConnections(context.Background(), e.f.OrgA, ids, nil); err != nil || len(rows) != 2 {
		t.Errorf("worker: %d entries (%v), want 2", len(rows), err)
	}
	for _, r := range mustAffected(t, impact, e.org(), e.f.OrgA, ids) {
		raw, _ := json.Marshal(r)
		for _, member := range []string{"holder", "address"} {
			if strings.Contains(string(raw), member) {
				t.Errorf("entry carries %s: %s", member, raw)
			}
		}
	}
}

func mustAffected(t *testing.T, impact *admp.Impact, ctx context.Context, org string, ids []string) []admp.AffectedConnection {
	t.Helper()
	rows, err := impact.AffectedConnections(ctx, org, ids, nil)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// TestAffectedConnectionsStaleFlag: a stale connection or service marks
// its entries stale; WithRefreshStale queues one admp.refresh per stale
// object without changing the result (kind connection for the stale
// connection, kind service for the stale service), a second call in the
// minute none.
func TestAffectedConnectionsStaleFlag(t *testing.T) {
	e := newAPIEnv(t)
	ciB, ifB := e.routerB()
	seedOutage(t, e.cache, e.org(), e.f.OrgA, e.account1, e.ci1, e.iface1, ciB, ifB, e.now)
	line1, err := e.cache.ConnectionByExternal(e.org(), e.f.OrgA, e.account1, "line-1")
	if err != nil {
		t.Fatal(err)
	}
	if err = e.cache.MarkStale(e.org(), e.f.OrgA, admp.KindConnection, line1.ID, "timeout"); err != nil {
		t.Fatal(err)
	}
	// Only the service of line-2 is stale: its refresh is one of the
	// service, not of the connection.
	svc2, err := e.cache.ServiceByExternal(e.org(), e.f.OrgA, e.account1, "svc-line-2")
	if err != nil {
		t.Fatal(err)
	}
	if err = e.cache.MarkStale(e.org(), e.f.OrgA, admp.KindService, svc2.ID, "timeout"); err != nil {
		t.Fatal(err)
	}
	sync := admp.NewSync(&admp.SyncOptions{Accounts: e.accounts, Cache: e.cache, Sources: e.fake, Pool: e.f.App,
		Now: func() time.Time { return e.now }}).WithSubmitter(queueOnly{svc: e.jobs})
	impact := admp.NewImpact(e.cache, sync)
	for range 2 {
		rows, err := impact.AffectedConnections(e.org(), e.f.OrgA, []string{e.ci1}, nil, admp.WithRefreshStale())
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 2 {
			t.Fatalf("%d entries, want 2", len(rows))
		}
		for _, r := range rows {
			if !r.Stale {
				t.Errorf("%s: not stale", r.ExternalID)
			}
		}
	}
	got := e.scalar(`SELECT string_agg(params->>'kind' || ':' || (params->>'external_id'), ',' ORDER BY params->>'kind')
		FROM job WHERE organization_id = $1 AND kind = $2`, e.f.OrgA, admp.RefreshJobKind)
	if got != "connection:line-1,service:svc-line-2" {
		t.Errorf("refresh jobs %q, want connection:line-1,service:svc-line-2 (each once)", got)
	}
}

// TestAffectedConnectionsMemoryParity: the memory store of --no-db gives
// the same entries as PostgreSQL, every field compared (ids through
// names), also with a stale service of an older fetched_at and a stale
// connection.
func TestAffectedConnectionsMemoryParity(t *testing.T) {
	m := newMemoryCache(t)
	const (
		memA, memIfA = "6f0b6f0e-0000-4000-8000-000000931001", "6f0b6f0e-0000-4000-8000-000000931002"
		memB, memIfB = "6f0b6f0e-0000-4000-8000-000000931003", "6f0b6f0e-0000-4000-8000-000000931004"
	)
	now := time.Now().UTC().Truncate(time.Microsecond)
	seedOutage(t, m.repo, m.ctx, m.org, m.accountOrg, memA, memIfA, memB, memIfB, now)
	memImpact := admp.NewImpact(m.repo, nil)
	memNames := map[string]string{memA: "A", memB: "B", memIfA: "ifA", memIfB: "ifB", m.accountOrg: "account"}
	type store struct {
		impact *admp.Impact
		repo   admp.Repository
		ctx    context.Context
		org    string
		acct   string
		cis    [2]string
		ifA    string
		names  map[string]string
		seeded time.Time
	}
	// scenario runs the three calls, ages the service of line-2 and marks
	// line-1 stale, and runs them again.
	scenario := func(st store) [][]string {
		run := func() [][]string {
			var out [][]string
			for _, call := range [][2][]string{{{st.cis[0]}, nil}, {{st.cis[0], st.cis[1]}, nil}, {nil, {st.ifA}}} {
				rows, err := st.impact.AffectedConnections(st.ctx, st.org, call[0], call[1])
				if err != nil {
					t.Fatal(err)
				}
				out = append(out, affectedFull(t, rows, st.names, st.seeded))
			}
			return out
		}
		out := run()
		ageService(t, st.repo, st.ctx, st.org, st.acct, "svc-line-2", st.seeded.Add(-time.Hour))
		line1, err := st.repo.ConnectionByExternal(st.ctx, st.org, st.acct, "line-1")
		if err != nil {
			t.Fatal(err)
		}
		if err = st.repo.MarkStale(st.ctx, st.org, admp.KindConnection, line1.ID, "timeout"); err != nil {
			t.Fatal(err)
		}
		return append(out, run()...)
	}
	mem := scenario(store{impact: memImpact, repo: m.repo, ctx: m.ctx, org: m.org, acct: m.accountOrg, cis: [2]string{memA, memB},
		ifA: memIfA, names: memNames, seeded: now})
	if got := affectedSummaryOf(t, memImpact, m.ctx, m.org, memA, memNames); !reflect.DeepEqual(got, outageWant) {
		t.Errorf("memory: %v, want %v", got, outageWant)
	}
	if want := "line-2/account/Netz AG/V-line-2/internet/12/A/ifA/line-2/stale=true/-1h0m0s/false/[]"; mem[3][1] != want {
		t.Errorf("memory stale entry %q, want %q", mem[3][1], want)
	}
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set; the PostgreSQL side of the parity is skipped")
	}
	e := newAPIEnv(t)
	ciB, ifB := e.routerB()
	seedOutage(t, e.cache, e.org(), e.f.OrgA, e.accountOrg, e.ci1, e.iface1, ciB, ifB, e.now)
	names := map[string]string{e.ci1: "A", ciB: "B", e.iface1: "ifA", ifB: "ifB", e.accountOrg: "account"}
	pg := scenario(store{impact: admp.NewImpact(e.cache, nil), repo: e.cache, ctx: e.org(), org: e.f.OrgA, acct: e.accountOrg,
		cis: [2]string{e.ci1, ciB}, ifA: e.iface1, names: names, seeded: e.now})
	if !reflect.DeepEqual(mem, pg) {
		t.Errorf("memory %v, PostgreSQL %v", mem, pg)
	}
}

// affectedSummaryOf is affectedSummary of the outage of router a.
func affectedSummaryOf(t *testing.T, impact *admp.Impact, ctx context.Context, org, a string, names map[string]string) []string {
	t.Helper()
	rows, err := impact.AffectedConnections(ctx, org, []string{a}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return affectedSummary(rows, names)
}

// TestCIAdmpConnectionsCarryRedundancy: GET /cis/{id}/admp-connections
// carries redundant and redundant_via of AffectedConnections([id]).
func TestCIAdmpConnectionsCarryRedundancy(t *testing.T) {
	e := newAPIEnv(t)
	ciB, ifB := e.routerB()
	seedOutage(t, e.cache, e.org(), e.f.OrgA, e.account1, e.ci1, e.iface1, ciB, ifB, e.now)
	rec := e.get(e.mux, "/api/v1/cis/"+e.ci1+"/admp-connections", "")
	var page struct {
		Data []struct {
			ExternalID   string   `json:"external_id"`
			Redundant    bool     `json:"redundant"`
			RedundantVia []string `json:"redundant_via"`
		} `json:"data"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &page) != nil || len(page.Data) != 2 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	for _, d := range page.Data {
		switch d.ExternalID {
		case "line-1":
			if !d.Redundant || !reflect.DeepEqual(d.RedundantVia, []string{ciB}) {
				t.Errorf("line-1: redundant %v via %v, want true via [%s]", d.Redundant, d.RedundantVia, ciB)
			}
		case "line-2":
			if d.Redundant || d.RedundantVia == nil || len(d.RedundantVia) != 0 {
				t.Errorf("line-2: redundant %v via %v, want false via []", d.Redundant, d.RedundantVia)
			}
		}
	}
	if !strings.Contains(rec.Body.String(), `"redundant_via":[]`) {
		t.Errorf("an empty redundant_via is no array: %s", rec.Body.String())
	}
	rec = e.get(e.mux, "/api/v1/cis/"+ciB+"/admp-connections", "")
	if !strings.Contains(rec.Body.String(), `"redundant_via":["`+e.ci1+`"]`) {
		t.Errorf("router B: %s, want line-1 redundant via A", rec.Body.String())
	}
}
