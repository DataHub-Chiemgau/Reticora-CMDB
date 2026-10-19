package rls_test

// Metrics channel of the CI detail view through the complete PostgreSQL
// router (WP-344, F-052, TEN-06, MON-01). GET /api/v1/cis/{id}/metrics
// answers like GET /api/v1/cis/{id}: 404 for a CI outside the principal's
// client/site scope, of another organization, unknown or with a malformed
// id, with or without name filter, and the body never names its series.
// Visible CIs keep their series.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -count=1 -run '^TestCIMetricsEndpointScope$' ./internal/tenant/rls/

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
)

func TestCIMetricsEndpointScope(t *testing.T) {
	w := newT1World(t, "db")
	f := w.f
	bg := context.Background()
	t.Cleanup(func() {
		// metric_sample has no foreign key to the organization.
		_, _ = f.Admin.Exec(bg, `DELETE FROM metric_sample WHERE organization_id = ANY($1::uuid[])`, []string{f.OrgA, f.OrgB})
	})
	site1 := w.siteOfRoom(f.Room(t, f.Client1, "t1-metrics-s1"))
	site2 := w.siteOfRoom(f.Room(t, f.Client2, "t1-metrics-s2"))
	own := f.CI(t, f.OrgA, f.Client1, "t1-metrics-c1")
	foreign := f.CI(t, f.OrgA, f.Client2, "t1-metrics-c2")
	for ci, site := range map[string]string{own: site1, foreign: site2} {
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET location_id = $2 WHERE id = $1`, ci, site); err != nil {
			t.Fatalf("place CI: %v", err)
		}
	}
	at := time.Now().UTC().Add(-time.Hour)
	if err := monitoring.NewPGMetricStore(f.App).Ingest(f.OrgCtx(f.OrgA), []monitoring.Metric{
		{OrgID: f.OrgA, CIID: own, Name: "t1_own_metric", Value: 1, Timestamp: at},
		{OrgID: f.OrgA, CIID: foreign, Name: "t1_secret_metric", Value: 7, Timestamp: at},
	}); err != nil {
		t.Fatal(err)
	}
	series := map[string]string{own: "t1_own_metric", foreign: "t1_secret_metric"}

	cases := []struct {
		p       *channelPrincipal
		visible map[string]bool
	}{
		{w.orgA, map[string]bool{own: true, foreign: true}},
		{w.as("client1", only(f.Client1), all()), map[string]bool{own: true}},
		{w.as("site1", all(), only(site1)), map[string]bool{own: true}},
		{w.as("c1+s1", only(f.Client1), only(site1)), map[string]bool{own: true}},
		{w.orgB, map[string]bool{}},
	}
	for _, c := range cases {
		for _, ci := range []string{own, foreign} {
			detail, _ := w.do(c.p, http.MethodGet, "/api/v1/cis/"+ci, nil)
			for _, path := range []string{"/api/v1/cis/" + ci + "/metrics", "/api/v1/cis/" + ci + "/metrics?name=" + series[ci]} {
				code, body := w.do(c.p, http.MethodGet, path, nil)
				if c.visible[ci] {
					if code != http.StatusOK || !strings.Contains(body, series[ci]) {
						t.Errorf("%s GET %s: %d %s, want 200 with the series", c.p.kind, path, code, trim(body))
					}
					continue
				}
				if detail != http.StatusNotFound {
					t.Fatalf("setup: GET /cis/{id} of an invisible CI as %s: %d, want 404", c.p.kind, detail)
				}
				if code != http.StatusNotFound || strings.Contains(body, series[ci]) {
					t.Errorf("TEN-06: %s GET %s: %d %s, want 404 without the series name", c.p.kind, path, code, trim(body))
				}
			}
		}
	}
	// Unknown and malformed ids name no CI either.
	for _, id := range []string{"00000000-0000-4000-8000-000000000044", "not-a-uuid"} {
		if code, body := w.do(w.orgA, http.MethodGet, "/api/v1/cis/"+id+"/metrics", nil); code != http.StatusNotFound {
			t.Errorf("GET /cis/%s/metrics: %d %s, want 404", id, code, trim(body))
		}
	}
}
