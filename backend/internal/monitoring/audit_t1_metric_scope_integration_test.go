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
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -count=1 -v -run 'TestAuditT1MetricReadPathsHonourScope' ./internal/monitoring/

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
