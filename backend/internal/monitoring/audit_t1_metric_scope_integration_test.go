package monitoring_test

// Phase-1 acceptance audit, group T1-tenancy: REGRESSION test (passes).
//
// WP-039 / WP-040 (TEC-06, MON-01; TEN-05, TEN-06) gap "No test covers all
// metric read paths under client/site scope" and "Scope limitation of the
// metric views and aggregate is not proven by any repo test": the existing
// tests cover raw samples of a client-1 principal only. This test runs the
// raw, bucketed (5 min) and hourly (continuous aggregate metric_sample_1h)
// read paths of PGMetricStore.Query for an org-wide, a client-1 and a site-1
// principal, with and without a CI filter.
//
// WP-344 (F-052, MON-01, TEN-06): MetricNames lists the series of a CI only
// when the CI is visible under the caller's scope (as metricWhere), CIVisible
// decides the 404 of GET /cis/{id}/metrics, and Ingest refuses samples for a
// CI outside the scope or of another organization without writing any.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -count=1 -v -run 'TestAuditT1MetricReadPathsHonourScope|TestMetricNamesHonourScope|TestIngestRefusesInvisibleCI' ./internal/monitoring/

import (
	"context"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
)

// t1MetricFixture seeds three CIs with samples: own (client 1, site 1),
// sibling (client 1, a second site of client 1) and foreign (client 2,
// site 2). The samples lie two days back in one hour, and the hourly
// aggregate is refreshed for that window.
type t1MetricFixture struct {
	f                      *scopetest.Fixture
	store                  *monitoring.PGMetricStore
	own, sibling, foreign  string
	site1                  string
	from, to               time.Time
	ownValue, foreignValue float64
}

func seedT1Metrics(t *testing.T, tag string) *t1MetricFixture {
	t.Helper()
	f := scopetest.Seed(t, tag)
	bg := context.Background()
	t.Cleanup(func() {
		// metric_sample has no foreign key to the organization.
		if _, err := f.Admin.Exec(bg, `DELETE FROM metric_sample WHERE organization_id = ANY($1::uuid[])`, []string{f.OrgA, f.OrgB}); err != nil {
			t.Errorf("cleanup metric samples: %v", err)
		}
	})
	site := func(client, name string) string {
		t.Helper()
		room := f.Room(t, client, name)
		var id string
		if err := f.Admin.QueryRow(bg, `SELECT b.site_id::text FROM room r JOIN building b ON b.id = r.building_id WHERE r.id = $1`, room).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	place := func(ciID, siteID string) {
		t.Helper()
		var got string
		if err := f.Admin.QueryRow(bg, `UPDATE ci SET location_id = $2 WHERE id = $1 RETURNING COALESCE(site_id::text, '')`, ciID, siteID).Scan(&got); err != nil || got != siteID {
			t.Fatalf("place CI at site: site_id %q, %v; want %s", got, err, siteID)
		}
	}
	m := &t1MetricFixture{f: f, store: monitoring.NewPGMetricStore(f.App), ownValue: 11, foreignValue: 99}
	m.site1 = site(f.Client1, "t1-metric-s1")
	site1b, site2 := site(f.Client1, "t1-metric-s1b"), site(f.Client2, "t1-metric-s2")
	m.own = f.CI(t, f.OrgA, f.Client1, "t1-metric-own")
	m.sibling = f.CI(t, f.OrgA, f.Client1, "t1-metric-sibling")
	m.foreign = f.CI(t, f.OrgA, f.Client2, "t1-metric-foreign")
	place(m.own, m.site1)
	place(m.sibling, site1b)
	place(m.foreign, site2)

	m.from = time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	m.to = m.from.Add(time.Hour - time.Second)
	if err := m.store.Ingest(f.OrgCtx(f.OrgA), []monitoring.Metric{
		{OrgID: f.OrgA, CIID: m.own, Name: "t1_cpu", Value: m.ownValue, Timestamp: m.from.Add(10 * time.Minute)},
		{OrgID: f.OrgA, CIID: m.sibling, Name: "t1_cpu", Value: 33, Timestamp: m.from.Add(11 * time.Minute)},
		{OrgID: f.OrgA, CIID: m.foreign, Name: "t1_cpu", Value: m.foreignValue, Timestamp: m.from.Add(12 * time.Minute)},
		{OrgID: f.OrgA, CIID: m.foreign, Name: "t1_secret_series", Value: 1, Timestamp: m.from.Add(13 * time.Minute)},
	}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if _, err := f.Admin.Exec(bg, `CALL refresh_continuous_aggregate('metric_sample_1h', $1::timestamptz, $2::timestamptz)`,
		m.from.Add(-time.Hour), m.from.Add(2*time.Hour)); err != nil {
		t.Fatalf("refresh aggregate: %v", err)
	}
	return m
}

// scopes returns the request contexts of the org-wide, client-1 and
// site-1 principals of organization A.
func (m *t1MetricFixture) scopes() map[string]context.Context {
	site := database.OrgWideScope(m.f.OrgA, m.f.User)
	site.Sites = database.ScopeIDs(m.site1)
	return map[string]context.Context{
		"org":     m.f.OrgCtx(m.f.OrgA),
		"client1": m.f.ClientCtx(m.f.Client1),
		"site1":   database.ContextWithTenantScope(context.Background(), &site),
	}
}

func TestAuditT1MetricReadPathsHonourScope(t *testing.T) {
	m := seedT1Metrics(t, "d8")
	// visible[principal][ci]
	visible := map[string]map[string]bool{
		"org":     {m.own: true, m.sibling: true, m.foreign: true},
		"client1": {m.own: true, m.sibling: true, m.foreign: false},
		"site1":   {m.own: true, m.sibling: false, m.foreign: false},
	}
	steps := map[string]time.Duration{"raw": 0, "bucketed-5m": 5 * time.Minute, "hourly-cagg": time.Hour}
	for kind, ctx := range m.scopes() {
		for path, step := range steps {
			for _, ciID := range []string{m.own, m.sibling, m.foreign} {
				points, err := m.store.Query(ctx, monitoring.MetricQuery{OrgID: m.f.OrgA, CIID: ciID, Name: "t1_cpu", From: m.from, To: m.to, Step: step})
				if err != nil {
					t.Fatalf("%s %s query: %v", kind, path, err)
				}
				if got, want := len(points) > 0, visible[kind][ciID]; got != want {
					t.Errorf("%s %s CI %s: points present %v, want %v (%+v)", kind, path, ciID, got, want, points)
				}
			}
			// Without a CI filter the foreign value must never contribute.
			points, err := m.store.Query(ctx, monitoring.MetricQuery{OrgID: m.f.OrgA, Name: "t1_cpu", From: m.from, To: m.to, Step: step})
			if err != nil {
				t.Fatalf("%s %s unfiltered query: %v", kind, path, err)
			}
			for _, p := range points {
				if kind != "org" && (p.Value == m.foreignValue || (step > 0 && p.Value > 33)) {
					t.Errorf("%s %s unfiltered: point %+v includes the client-2 sample", kind, path, p)
				}
			}
			if len(points) == 0 {
				t.Errorf("%s %s unfiltered: no points, want the own samples", kind, path)
			}
		}
	}
	// Another organization reads nothing of organization A.
	for path, step := range steps {
		points, err := m.store.Query(m.f.OrgCtx(m.f.OrgB), monitoring.MetricQuery{OrgID: m.f.OrgB, CIID: m.own, Name: "t1_cpu", From: m.from, To: m.to, Step: step})
		if err != nil || len(points) != 0 {
			t.Errorf("org B %s reads an org A CI: %+v, %v", path, points, err)
		}
	}
}

func TestMetricNamesHonourScope(t *testing.T) {
	m := seedT1Metrics(t, "d9")
	since := m.from.Add(-time.Hour)
	// visible[principal][ci]
	visible := map[string]map[string]bool{
		"org":     {m.own: true, m.sibling: true, m.foreign: true},
		"client1": {m.own: true, m.sibling: true, m.foreign: false},
		"site1":   {m.own: true, m.sibling: false, m.foreign: false},
	}
	for kind, ctx := range m.scopes() {
		for _, ciID := range []string{m.own, m.sibling, m.foreign} {
			names, err := m.store.MetricNames(ctx, m.f.OrgA, ciID, since)
			if err != nil {
				t.Fatalf("%s MetricNames: %v", kind, err)
			}
			want := 0
			if visible[kind][ciID] {
				want = 1
				if ciID == m.foreign {
					want = 2
				}
			}
			if len(names) != want {
				t.Errorf("TEN-06: %s MetricNames(CI %s) = %v, want %d names", kind, ciID, names, want)
			}
			ok, err := m.store.CIVisible(ctx, m.f.OrgA, ciID)
			if err != nil || ok != visible[kind][ciID] {
				t.Errorf("%s CIVisible(CI %s) = %v, %v; want %v", kind, ciID, ok, err, visible[kind][ciID])
			}
		}
	}
	// Another organization lists nothing of organization A; unknown and
	// malformed ids are no visible CI.
	if names, err := m.store.MetricNames(m.f.OrgCtx(m.f.OrgB), m.f.OrgB, m.foreign, since); err != nil || len(names) != 0 {
		t.Errorf("org B MetricNames(org A CI) = %v, %v; want none", names, err)
	}
	for _, id := range []string{"00000000-0000-4000-8000-000000000344", "not-a-uuid"} {
		if ok, err := m.store.CIVisible(m.f.OrgCtx(m.f.OrgA), m.f.OrgA, id); err != nil || ok {
			t.Errorf("CIVisible(%q) = %v, %v; want false", id, ok, err)
		}
	}
	if ok, err := m.store.CIVisible(m.f.OrgCtx(m.f.OrgB), m.f.OrgB, m.own); err != nil || ok {
		t.Errorf("org B CIVisible(org A CI) = %v, %v; want false", ok, err)
	}
}

// TestIngestRefusesInvisibleCI is the open proof of WP-040: a sample for a
// CI the caller cannot see is refused and the whole batch is rolled back.
func TestIngestRefusesInvisibleCI(t *testing.T) {
	m := seedT1Metrics(t, "6a")
	at := m.from.Add(20 * time.Minute)
	count := func(ciID string) int {
		t.Helper()
		var n int
		if err := m.f.Admin.QueryRow(context.Background(),
			`SELECT count(*) FROM metric_sample WHERE ci_id = $1 AND metric_name = 't1_probe'`, ciID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, c := range []struct {
		kind  string
		ctx   context.Context
		orgID string
		ciID  string
	}{
		{"client1 -> client-2 CI", m.f.ClientCtx(m.f.Client1), m.f.OrgA, m.foreign},
		{"site1 -> sibling-site CI", m.scopes()["site1"], m.f.OrgA, m.sibling},
		{"org B -> org A CI", m.f.OrgCtx(m.f.OrgB), m.f.OrgB, m.own},
		{"org -> unknown CI", m.f.OrgCtx(m.f.OrgA), m.f.OrgA, "00000000-0000-4000-8000-000000000344"},
	} {
		// The first, visible sample must not survive the refused batch.
		batch := []monitoring.Metric{{OrgID: c.orgID, CIID: c.ciID, Name: "t1_probe", Value: 5, Timestamp: at}}
		if c.orgID == m.f.OrgA {
			batch = append([]monitoring.Metric{{OrgID: c.orgID, CIID: m.own, Name: "t1_probe", Value: 4, Timestamp: at}}, batch...)
		}
		if err := m.store.Ingest(c.ctx, batch); err == nil {
			t.Errorf("WP-040: %s: Ingest accepted the sample, want an error", c.kind)
		}
		if n := count(c.ciID); n != 0 {
			t.Errorf("WP-040: %s: %d probe samples stored for the CI, want 0", c.kind, n)
		}
		if n := count(m.own); n != 0 {
			t.Errorf("WP-040: %s: %d probe samples of the refused batch stored, want 0", c.kind, n)
		}
	}
	// Positive control: the visible CI accepts the probe.
	if err := m.store.Ingest(m.f.ClientCtx(m.f.Client1), []monitoring.Metric{{OrgID: m.f.OrgA, CIID: m.own, Name: "t1_probe", Value: 4, Timestamp: at}}); err != nil {
		t.Fatalf("client1 ingest for its own CI: %v", err)
	}
	if n := count(m.own); n != 1 {
		t.Errorf("own probe samples = %d, want 1", n)
	}
}
