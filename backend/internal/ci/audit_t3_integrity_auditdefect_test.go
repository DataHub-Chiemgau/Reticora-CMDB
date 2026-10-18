//go:build auditdefect

package ci_test

// Phase-1 acceptance audit, group T3-data-ingest: DEFECT tests (they fail
// until the product is fixed).
//
// WP-079 (DB-05, TEN-05, TEN-06) P2 "A client-scoped principal can set
// parent_ci_id to a CI of another client": the FK check bypasses RLS and
// ci/service.go / pg_repository.go do not verify that the parent is visible
// to the principal, so a client-1 principal attaches its CI to a client-2
// chassis (PATCH /api/v1/cis/{id} path: Service.Update with Manual).
// Requirement: a parent invisible to the principal is refused
// (ValidationError on parent_ci_id, 'parent CI not found') and nothing is
// stored; same-client and org-wide parents are accepted.
//
// WP-174 (CI-12; no silent data loss) P2 "Unmerge silently overwrites
// changes made to the survivor after the merge": merge.go restoreRow sets
// every column from the snapshot without a version/updated_at comparison.
// Requirement: an unmerge either keeps the later, unrelated edit of the
// survivor or refuses (conflict) and leaves the survivor unchanged.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_03?sslmode=disable \
//     go test -tags auditdefect -count=1 -v -run 'TestAuditT3ParentMustBeVisibleToScopedPrincipal|TestAuditT3UnmergeKeepsLaterSurvivorEdits' ./internal/ci/

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

func TestAuditT3ParentMustBeVisibleToScopedPrincipal(t *testing.T) {
	f := scopetest.Seed(t, "6d")
	bg := context.Background()
	svc := ci.NewService(ci.NewPGRepository(f.App))
	manual := &ci.ManualChange{Author: f.User}
	parentOf := func(id string) string {
		t.Helper()
		var p string
		if err := f.Admin.QueryRow(bg, `SELECT coalesce(parent_ci_id::text, '') FROM ci WHERE id = $1`, id).Scan(&p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	client1 := f.ClientCtx(f.Client1)

	// Controls: a same-client parent and an org-wide parent are accepted.
	child := f.CI(t, f.OrgA, f.Client1, "audit-t3-child")
	sameClient := f.CI(t, f.OrgA, f.Client1, "audit-t3-chassis-c1")
	if _, err := svc.Update(client1, f.OrgA, child, ci.UpdateRequest{ParentCIID: &sameClient, Manual: manual}); err != nil {
		t.Fatalf("control: same-client parent refused: %v", err)
	}
	orgWide := f.CI(t, f.OrgA, "", "audit-t3-chassis-org")
	if _, err := svc.Update(client1, f.OrgA, child, ci.UpdateRequest{ParentCIID: &orgWide, Manual: manual}); err != nil {
		t.Fatalf("control: org-wide parent refused: %v", err)
	}

	foreign := f.CI(t, f.OrgA, f.Client2, "audit-t3-chassis-c2")
	_, err := svc.Update(client1, f.OrgA, child, ci.UpdateRequest{ParentCIID: &foreign, Manual: manual})
	if err == nil {
		t.Errorf("client-1 principal set parent_ci_id to a client-2 CI without error (TEN-05: an invisible parent must be refused)")
	}
	if got := parentOf(child); got != orgWide {
		t.Errorf("stored parent_ci_id = %s, want unchanged %s (the client-2 chassis %s now has a client-1 child)", got, orgWide, foreign)
	}
}

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
