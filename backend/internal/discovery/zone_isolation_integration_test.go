// Scope targets without IPv6 zone and the per-organization isolation of the
// offline detector and the edge cleanup (WP-383; F-108).
//
//   - TestAuditZonedScopeTargetRefused – WP-153 (DIS-09, API-05; moved from
//     zone_isolation_auditdefect_test.go): POST /api/v1/discovery-scopes
//     refuses a host or range target with an IPv6 zone (fe80::1%eth0) with
//     422; PostgreSQL inet cannot evaluate a zone. A plain CIDR is the
//     control.
//   - TestAuditBadScopeIsolated – WP-157 (REC-09, LCY-05), WP-159 (TOP-02;
//     moved from zone_isolation_auditdefect_test.go): a zoned range scope
//     inserted directly into the lower-sorted organization (as an old row
//     or a direct write exists even after the API is fixed) neither fails
//     that organization's run nor stops Run for the organization sorted
//     after it: its CI last seen 400 h ago becomes unknown, its discovery
//     edge last confirmed 30 days ago is removed.
//
// Retest (F-108):
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -v -p 1 -run 'TestAuditZonedScopeTargetRefused|TestAuditBadScopeIsolated' ./internal/discovery/
package discovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	f := scopetest.Seed(t, "7d")
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
	f := scopetest.Seed(t, "7f")
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
	// Run may report other organizations of the shared database, never
	// these two.
	ours := func(err error) bool {
		return err != nil && (strings.Contains(err.Error(), first) || strings.Contains(err.Error(), second))
	}

	t.Run("offline detector (WP-157)", func(t *testing.T) {
		_, err := lifecycle.NewOfflineDetector(f.App).WithClock(time.Now).Run(bg)
		if got := status(); got != "unknown" {
			t.Errorf("tenant isolation violated: the CI of organization %s last seen 400 h ago is %q after Run(), want unknown; "+
				"Run stopped at the malformed scope of organization %s: %v", second, got, first, err)
			// Control: the CI is overdue; processed alone, the org is checked.
			if _, cerr := lifecycle.NewOfflineDetector(f.App).WithClock(time.Now).RunOrg(bg, second); cerr != nil || status() == "active" {
				t.Fatalf("control: RunOrg(%s) alone: status=%s err=%v", second, status(), cerr)
			}
		}
		if ours(err) {
			t.Errorf("Run() reports one of the fixture organizations: %v", err)
		}
		if _, oerr := lifecycle.NewOfflineDetector(f.App).WithClock(time.Now).RunOrg(bg, first); oerr != nil {
			t.Errorf("the zoned scope still fails the run of organization %s: %v", first, oerr)
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
		if ours(err) {
			t.Errorf("Run() reports one of the fixture organizations: %v", err)
		}
		if _, oerr := relationship.NewCleanup(f.App).RunOrg(bg, first); oerr != nil {
			t.Errorf("the zoned scope still fails the cleanup of organization %s: %v", first, oerr)
		}
	})
}
