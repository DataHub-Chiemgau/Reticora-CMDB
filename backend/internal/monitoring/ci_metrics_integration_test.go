package monitoring_test

// GET /api/v1/cis/{id}/metrics through the real handler and PGMetricStore
// (WP-402, F-167, MON-02). A principal restricted to client 1 asking for a
// client-2 CI with sweep_reachable samples gets 404 and no series, like for
// an unknown CI; its own CI keeps its points, and an org-wide principal
// keeps the series of the client-2 CI. A malformed id answers 400 before
// any SQL.
//
// Retest (F-167):
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -count=1 -run 'TestCIMetricsRouteScopeAgainstPostgres' ./internal/monitoring/

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

func TestCIMetricsRouteScopeAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "f9")
	bg := context.Background()
	t.Cleanup(func() {
		// metric_sample has no foreign key to the organization.
		if _, err := f.Admin.Exec(bg, `DELETE FROM metric_sample WHERE organization_id = ANY($1::uuid[])`, []string{f.OrgA, f.OrgB}); err != nil {
			t.Errorf("cleanup metric samples: %v", err)
		}
	})
	own := f.CI(t, f.OrgA, f.Client1, "f167-own")
	foreign := f.CI(t, f.OrgA, f.Client2, "f167-foreign")
	unknown := "00000000-0000-4000-8000-000000000402"

	store := monitoring.NewPGMetricStore(f.App)
	at := time.Now().UTC().Add(-time.Hour)
	if err := store.Ingest(f.OrgCtx(f.OrgA), []monitoring.Metric{
		{OrgID: f.OrgA, CIID: own, Name: "sweep_reachable", Value: 1, Timestamp: at},
		{OrgID: f.OrgA, CIID: foreign, Name: "sweep_reachable", Value: 1, Timestamp: at},
		{OrgID: f.OrgA, CIID: foreign, Name: "sweep_reachable", Value: 0, Timestamp: at.Add(5 * time.Minute)},
	}); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	router := chi.NewRouter()
	monitoring.NewHandler(store).RegisterRoutes(router)
	principal := func(orgID string, clients ...string) context.Context {
		scope := database.OrgWideScope(orgID, f.User)
		if len(clients) > 0 {
			scope.Clients = database.ScopeIDs(clients...)
		}
		return tenant.WithTenant(database.ContextWithTenantScope(bg, &scope), tenant.TenantInfo{OrganizationID: orgID, UserID: f.User})
	}
	org, client1, orgB := principal(f.OrgA), principal(f.OrgA, f.Client1), principal(f.OrgB)
	get := func(ctx context.Context, path string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	paths := func(ciID string) []string {
		return []string{"/api/v1/cis/" + ciID + "/metrics", "/api/v1/cis/" + ciID + "/metrics?name=sweep_reachable"}
	}
	// wantPoints asserts 200 with the sweep_reachable series of n points.
	wantPoints := func(who, path string, code int, body string, n int) {
		t.Helper()
		var resp struct {
			Series []monitoring.CISeries `json:"series"`
		}
		if code != http.StatusOK {
			t.Errorf("%s GET %s: %d %s, want 200", who, path, code, body)
			return
		}
		if err := json.Unmarshal([]byte(body), &resp); err != nil {
			t.Fatalf("%s GET %s: decode %q: %v", who, path, body, err)
		}
		if len(resp.Series) != 1 || resp.Series[0].Name != "sweep_reachable" || len(resp.Series[0].Points) != n {
			t.Errorf("%s GET %s: series %+v, want sweep_reachable with %d points", who, path, resp.Series, n)
		}
	}
	// wantNotFound asserts 404 that names no series, the same answer as for
	// an unknown CI.
	wantNotFound := func(who, path string, code int, body string) {
		t.Helper()
		if code != http.StatusNotFound || strings.Contains(body, `"series"`) || strings.Contains(body, "sweep_reachable") {
			t.Errorf("F-167: %s GET %s: %d %s, want 404 without series", who, path, code, body)
		}
	}

	for _, path := range paths(foreign) {
		code, body := get(client1, path)
		wantNotFound("client 1", path, code, body)
		code, body = get(orgB, path)
		wantNotFound("organization B", path, code, body)
		code, body = get(org, path)
		wantPoints("org-wide", path, code, body, 2)
	}
	for _, path := range paths(own) {
		code, body := get(client1, path)
		wantPoints("client 1", path, code, body, 1)
	}
	for _, path := range paths(unknown) {
		for who, ctx := range map[string]context.Context{"org-wide": org, "client 1": client1} {
			code, body := get(ctx, path)
			wantNotFound(who, path, code, body)
		}
	}
	// A malformed id is a bad request and never reaches SQL (API-03).
	for _, path := range paths("not-a-uuid") {
		code, body := get(org, path)
		if code != http.StatusBadRequest || !strings.Contains(body, "must be a UUID") || strings.Contains(body, "SQLSTATE") {
			t.Errorf("GET %s: %d %s, want 400 for the malformed id", path, code, body)
		}
	}
}
