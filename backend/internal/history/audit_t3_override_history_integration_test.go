package history_test

// Phase-1 acceptance audit, group T3-data-ingest: REGRESSION test (passes
// today; closes an evidence gap).
//
// WP-171 (CI-06, AUD-03; criterion 1 "... Audit, location_change und
// Overrides chronologisch") high test gap "No repo test that the unified
// history includes overrides": TestUnifiedCIHistory covers ci_change,
// entity_change and audit of create/update only. Asserted here with the real
// override.PGRepository: SetOverride and ClearOverride appear in the CI's
// unified history as audit entries ci.override_set / ci.override_cleared,
// newest first (cleared before set), and a principal restricted to client 2
// reads none of the client-1 CI's history (not found, WP-475).
// (The location_change part is TestAuditT3HistoryIncludesLocationChange in
// history_integration_test.go.)
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_03?sslmode=disable \
//     go test -count=1 -v -run 'TestAuditT3HistoryIncludesOverrides' ./internal/history/

import (
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

func TestAuditT3HistoryIncludesOverrides(t *testing.T) {
	f := scopetest.Seed(t, "d5")
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	ciID := f.CI(t, f.OrgA, f.Client1, "audit-t3-hist-ovr")
	ovr := override.NewPGRepository(f.App)
	if _, err := ovr.SetOverride(ctx, f.OrgA, ciID, "model", "manual-model", f.User, "audit", true); err != nil {
		t.Fatalf("set override: %v", err)
	}
	if _, err := ovr.ClearOverride(ctx, f.OrgA, ciID, "model"); err != nil {
		t.Fatalf("clear override: %v", err)
	}
	repo := history.NewPGRepository(f.App)
	all, total, err := repo.List(ctx, f.OrgA, "ci", ciID, api.PaginationParams{Limit: 100}, true)
	if err != nil {
		t.Fatal(err)
	}
	if total != len(all) {
		t.Errorf("total %d, entries %d", total, len(all))
	}
	pos := map[string]int{}
	for i, c := range all {
		if i > 0 && c.CreatedAt.After(all[i-1].CreatedAt) {
			t.Errorf("not newest first at %d", i)
		}
		if c.Source == history.SourceAudit {
			if _, seen := pos[c.ChangeType]; !seen {
				pos[c.ChangeType] = i
			}
		}
	}
	set, okSet := pos["ci.override_set"]
	cleared, okCleared := pos["ci.override_cleared"]
	if !okSet || !okCleared {
		t.Fatalf("unified CI history lacks the override entries: audit change types %v", pos)
	}
	if cleared > set {
		t.Errorf("ci.override_cleared at %d after ci.override_set at %d, want newest first", cleared, set)
	}

	client2 := tenant.WithTenant(f.ClientCtx(f.Client2), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	other, otherTotal, err := repo.List(client2, f.OrgA, "ci", ciID, api.PaginationParams{Limit: 100}, true)
	if !errors.Is(err, api.ErrNotFound) || otherTotal != 0 || len(other) != 0 {
		t.Errorf("client-2 principal reads %d history entries of a client-1 CI (%v), want 0 and api.ErrNotFound", otherTotal, err)
	}
}
