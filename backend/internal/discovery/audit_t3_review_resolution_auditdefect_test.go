//go:build auditdefect

package discovery_test

// Phase-1 acceptance audit, group T3-data-ingest: DEFECT tests (they fail
// until the product is fixed). The resolution 'new' of WP-087 (F-022) is
// fixed by WP-369; its test TestAuditT3ResurrectedNewKeepsClientAndIdentity
// is in resurrect_test.go.
//
// WP-173 (CI-12, CH29) P2 "Review resolution 'merge' of probable_duplicate
// soft-deletes the duplicate instead of a CI-12 merge": resolveCIReview only
// calls ciRepo.Delete(dup). Requirement: references move to the survivor and
// the duplicate becomes a merge victim (merged_into_id, ci_merge snapshot,
// 301 tombstone).
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_03?sslmode=disable \
//     go test -tags auditdefect -count=1 -v -run 'TestAuditT3ProbableDuplicateMergeIsCIMerge' ./internal/discovery/

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

func TestAuditT3ProbableDuplicateMergeIsCIMerge(t *testing.T) {
	f := scopetest.Seed(t, "f5")
	bg := context.Background()
	cis := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	mux := chi.NewRouter()
	discovery.NewHandler(discovery.NewPGRepository(f.App), cis).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	existing := f.CI(t, f.OrgA, f.Client1, "audit-t3-existing")
	dup := f.CI(t, f.OrgA, f.Client1, "audit-t3-dup")
	peer := f.CI(t, f.OrgA, f.Client1, "audit-t3-peer")
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES ($1, $2, $3, 'depends_on')`,
		f.OrgA, dup, peer); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, 'eth9')`, f.OrgA, dup); err != nil {
		t.Fatal(err)
	}
	var itemID string
	if err := f.Admin.QueryRow(bg, `INSERT INTO review_item (organization_id, kind, status, payload, candidate_ci_ids)
		VALUES ($1, 'probable_duplicate', 'open', jsonb_build_object('ci_id', $2::text), ARRAY[$3::uuid]) RETURNING id::text`,
		f.OrgA, dup, existing).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/review-items/"+itemID+"/resolve",
		strings.NewReader(`{"action":"merge","ci_id":"`+existing+`"}`)).WithContext(ctx))
	if w.Code != http.StatusOK {
		t.Fatalf("resolve merge: %d %s", w.Code, w.Body.String())
	}
	var mergedInto string
	var merges, relsOnSurvivor, nicsOnSurvivor int
	if err := f.Admin.QueryRow(bg, `SELECT
		(SELECT coalesce(merged_into_id::text, '') FROM ci WHERE id = $1),
		(SELECT count(*) FROM ci_merge WHERE victim_id = $1),
		(SELECT count(*) FROM ci_relationship WHERE source_ci_id = $2 AND target_ci_id = $3),
		(SELECT count(*) FROM network_interface WHERE ci_id = $2 AND name = 'eth9')`, dup, existing, peer).
		Scan(&mergedInto, &merges, &relsOnSurvivor, &nicsOnSurvivor); err != nil {
		t.Fatal(err)
	}
	if mergedInto != existing || merges != 1 {
		t.Errorf("duplicate after review merge: merged_into_id=%q ci_merge rows=%d; want %s and 1 (CI-12 merge with tombstone and snapshot, not a soft delete)",
			mergedInto, merges, existing)
	}
	if relsOnSurvivor != 1 || nicsOnSurvivor != 1 {
		t.Errorf("references on the survivor after review merge: relationships=%d interfaces=%d; want 1 and 1 (they stay on the deleted duplicate)",
			relsOnSurvivor, nicsOnSurvivor)
	}
}
