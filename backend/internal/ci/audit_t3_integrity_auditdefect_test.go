//go:build auditdefect

package ci_test

// Phase-1 acceptance audit, group T3-data-ingest: DEFECT tests (they fail
// until the product is fixed). The parent scope of WP-079 (F-086) is fixed
// by WP-370; its test TestAuditT3ParentMustBeVisibleToScopedPrincipal is in
// ci_scope_integration_test.go.
//
// WP-174 (CI-12; no silent data loss) P2 "Unmerge silently overwrites
// changes made to the survivor after the merge": merge.go restoreRow sets
// every column from the snapshot without a version/updated_at comparison.
// Requirement: an unmerge either keeps the later, unrelated edit of the
// survivor or refuses (conflict) and leaves the survivor unchanged.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_03?sslmode=disable \
//     go test -tags auditdefect -count=1 -v -run 'TestAuditT3UnmergeKeepsLaterSurvivorEdits' ./internal/ci/

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

func TestAuditT3UnmergeKeepsLaterSurvivorEdits(t *testing.T) {
	f := scopetest.Seed(t, "6e")
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	repo := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	svc := ci.NewService(repo)
	survivor := f.CI(t, f.OrgA, f.Client1, "audit-t3-survivor")
	victim := f.CI(t, f.OrgA, f.Client1, "audit-t3-victim")
	if _, err := svc.Merge(ctx, f.OrgA, survivor, victim); err != nil {
		t.Fatalf("merge: %v", err)
	}
	renamed := "audit-t3-survivor-renamed-after-merge"
	if _, err := svc.Update(ctx, f.OrgA, survivor, ci.UpdateRequest{Name: &renamed, Manual: &ci.ManualChange{Author: f.User}}); err != nil {
		t.Fatalf("interim edit of the survivor: %v", err)
	}

	_, unmergeErr := svc.Unmerge(ctx, f.OrgA, victim)
	var name string
	if err := f.Admin.QueryRow(context.Background(), `SELECT name FROM ci WHERE id = $1`, survivor).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != renamed {
		t.Errorf("survivor name after unmerge (err=%v) = %q, want the later edit %q: the unmerge silently discarded a change made after the merge",
			unmergeErr, name, renamed)
	}
}
