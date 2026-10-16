//go:build auditdefect

// Phase-1 acceptance audit, group T4-collector-load – DEFECT tests (must fail
// until the defects are fixed).
//
// WP-157 (LIF-04 offline detector), WP-159 (TOP-02 edge cleanup), WP-153
// (DIS-09 scope validation), tenant isolation (TEN).
//
// Findings encoded here:
//   - WP-157 P2 "One tenant's zoned IPv6 range scope stops the offline
//     detector for every org sorted after it".
//   - WP-159 P2 "One tenant's zoned IPv6 range scope stops the edge cleanup
//     for every org sorted after it".
//
// NormalizeScopeTarget accepts IPv6 zones (fe80::1%eth0), PostgreSQL inet
// does not; offline.go and cleanup.go cast discovery_scope.target to inet
// unguarded, and Run() returns on the first failing organization. The
// requirement: the API refuses targets the server cannot evaluate (422), and
// each tenant is processed independently, so one organization's bad row
// affects at most that organization.
//
// TestAuditZonedScopeTargetRefused covers the API; TestAuditBadScopeIsolated
// inserts the malformed row directly (as an old row or a direct write would
// exist even after the API is fixed) into the lower-sorted organization and
// checks that the higher organization is still processed.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -tags auditdefect -count=1 -v -p 1 -run 'TestAuditZonedScopeTargetRefused|TestAuditBadScopeIsolated' ./internal/discovery/
package discovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

func TestAuditZonedScopeTargetRefused(t *testing.T) {
	f := scopetest.Seed(t, "d5")
	repo := discovery.NewPGRepository(f.App)
	col := &discovery.Collector{OrganizationID: f.OrgA, Name: "audit-zone-api", Status: discovery.CollectorActive, Config: map[string]any{}}
	if err := repo.RegisterCollector(f.OrgCtx(f.OrgA), col); err != nil {
		t.Fatal(err)
	}
	mux := chi.NewRouter()
	discovery.NewHandler(repo, nil).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	post := func(kind, target string) (int, string) {
		body, _ := json.Marshal(map[string]any{"collector_id": col.ID, "kind": kind, "target": target})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/discovery-scopes", bytes.NewReader(body)).WithContext(ctx))
		return w.Code, w.Body.String()
	}
	if code, body := post("cidr", "10.93.0.0/24"); code != http.StatusCreated {
		t.Fatalf("control: plain cidr scope: %d %s", code, body)
	}
	for _, tc := range []struct{ kind, target string }{
		{"range", "fe80::1%eth0-fe80::5%eth0"},
		{"host", "fe80::1%eth0"},
	} {
		if code, body := post(tc.kind, tc.target); code != http.StatusUnprocessableEntity {
			t.Errorf("POST /api/v1/discovery-scopes kind=%s target=%q = %d, want 422 (PostgreSQL inet cannot evaluate an IPv6 zone): %.200s",
				tc.kind, tc.target, code, body)
		}
	}
}

func TestAuditBadScopeIsolated(t *testing.T) {
	f := scopetest.Seed(t, "d6")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	first, second := f.OrgA, f.OrgB
	if second < first {
		first, second = second, first
	}
	// The lower-sorted organization holds the malformed scope and one
	// ordinary discovered CI with an edge, so its scope join is evaluated.
	col := f.ID()
	exec(`INSERT INTO collector (id, organization_id, name, status) VALUES ($1, $2, 'audit-zone', 'active')`, col, first)
	exec(`INSERT INTO discovery_scope (organization_id, collector_id, name, kind, target, schedule)
		VALUES ($1, $2, 'zoned', 'range', 'fe80::1%eth0-fe80::5%eth0', '24h')`, first, col)
	cand, peer := f.CI(t, first, "", "audit-zone-cand"), f.CI(t, first, "", "audit-zone-peer")
	exec(`UPDATE ci SET management_ip = '10.94.0.9'::inet, discovery_source = 'snmp', is_manual = false, last_seen_at = now() - interval '1 hour' WHERE id = $1`, cand)
	exec(`INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source, verification_state, last_seen_at, last_confirmed_at, confidence)
		VALUES ($1, $2, $3, 'connected_to', 'discovery', 'unverified', now(), now(), 1.0)`, first, cand, peer)

	// The higher-sorted organization: a CI last seen 400 h ago and a
	// discovery edge last confirmed 30 days ago, both overdue.
	stale, speer := f.CI(t, second, "", "audit-zone-stale"), f.CI(t, second, "", "audit-zone-speer")
	exec(`UPDATE ci SET management_ip = '10.94.1.9'::inet, discovery_source = 'snmp', is_manual = false, last_seen_at = now() - interval '400 hours' WHERE id = $1`, stale)
	var staleEdge string
	if err := f.Admin.QueryRow(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source, verification_state, last_seen_at, last_confirmed_at, confidence)
		VALUES ($1, $2, $3, 'connected_to', 'discovery', 'unverified', now() - interval '30 days', now() - interval '30 days', 1.0) RETURNING id::text`,
		second, stale, speer).Scan(&staleEdge); err != nil {
		t.Fatal(err)
	}
	status := func() string {
		var s string
		if err := f.Admin.QueryRow(bg, `SELECT status FROM ci WHERE id = $1`, stale).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	edgeKept := func() bool {
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_relationship WHERE id = $1`, staleEdge).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}

	t.Run("offline detector (WP-157)", func(t *testing.T) {
		_, err := lifecycle.NewOfflineDetector(f.App).WithClock(time.Now).Run(bg)
		if got := status(); got == "active" {
			t.Errorf("tenant isolation violated: the CI of organization %s last seen 400 h ago is still %q after Run(); "+
				"Run stopped at the malformed scope of organization %s: %v", second, got, first, err)
			// Control: the CI is overdue; processed alone, the org is checked.
			if _, cerr := lifecycle.NewOfflineDetector(f.App).WithClock(time.Now).RunOrg(bg, second); cerr != nil || status() == "active" {
				t.Fatalf("control: RunOrg(%s) alone: status=%s err=%v", second, status(), cerr)
			}
		}
	})
	t.Run("edge cleanup (WP-159)", func(t *testing.T) {
		_, err := relationship.NewCleanup(f.App).Run(bg)
		if edgeKept() {
			t.Errorf("tenant isolation violated: the discovery edge of organization %s last confirmed 30 days ago was kept by Run(); "+
				"Run stopped at the malformed scope of organization %s: %v", second, first, err)
			if _, cerr := relationship.NewCleanup(f.App).RunOrg(bg, second); cerr != nil || edgeKept() {
				t.Fatalf("control: RunOrg(%s) alone: kept=%v err=%v", second, edgeKept(), cerr)
			}
		}
	})
}
