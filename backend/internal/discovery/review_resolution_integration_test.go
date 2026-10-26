package discovery_test

// The resolution merge of a probable_duplicate item is the CI-12 merge
// (WP-479, CI-12, CH29, F-096): the references of the duplicate move to the
// chosen CI, the duplicate becomes a merge victim (merged_into_id, ci_merge
// snapshot, 301 tombstone, unmerge within 30 days) and the merge is a job of
// kind ci.merge. A refused merge leaves the item open.
//
// The tests run only against PostgreSQL (TEST_DATABASE_URL, see
// scopetest.Seed):
//   cd backend && TEST_DATABASE_URL=... go test -count=1 -run 'TestAuditT3ProbableDuplicateMergeIsCIMerge|TestProbableDuplicateMerge' ./internal/discovery/

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// reviewMergeEnv is a discovery handler whose review merges run the CI-12
// merge of a ci.Service with jobs, and the CI routes of that service.
type reviewMergeEnv struct {
	f    *scopetest.Fixture
	ctx  context.Context
	mux  *chi.Mux
	jobs *jobs.Service
}

func newReviewMergeEnv(t *testing.T, withMerger bool) *reviewMergeEnv {
	t.Helper()
	f := scopetest.Seed(t, "f5")
	cis := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	jobSvc := jobs.NewService(jobs.NewPGRepository(f.App))
	ciSvc := ci.NewService(cis).WithJobs(jobSvc)
	h := discovery.NewHandler(discovery.NewPGRepository(f.App), cis).WithReviewJobs(jobSvc)
	if withMerger {
		h.WithMerger(ciSvc)
	}
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	ci.NewHandler(ciSvc).RegisterRoutes(mux)
	return &reviewMergeEnv{f: f, mux: mux, jobs: jobSvc,
		ctx: tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})}
}

// openDuplicate opens a probable_duplicate item about dup with the
// candidates and returns its id.
func (e *reviewMergeEnv) openDuplicate(t *testing.T, dup string, candidates ...string) string {
	t.Helper()
	var itemID string
	if err := e.f.Admin.QueryRow(context.Background(), `INSERT INTO review_item (organization_id, kind, status, payload, candidate_ci_ids)
		VALUES ($1, 'probable_duplicate', 'open', jsonb_build_object('ci_id', $2::text), $3::uuid[]) RETURNING id::text`,
		e.f.OrgA, dup, candidates).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	return itemID
}

func (e *reviewMergeEnv) do(method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(e.ctx))
	return w
}

func (e *reviewMergeEnv) resolveMerge(itemID, ciID string) *httptest.ResponseRecorder {
	return e.do(http.MethodPost, "/api/v1/review-items/"+itemID+"/resolve", `{"action":"merge","ci_id":"`+ciID+`"}`)
}

// assertUntouched checks that the item is open and the duplicate neither
// deleted nor merged.
func (e *reviewMergeEnv) assertUntouched(t *testing.T, itemID, dup string) {
	t.Helper()
	var status string
	var deleted bool
	var mergedInto string
	var merges int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT
		(SELECT status FROM review_item WHERE id = $1),
		(SELECT deleted_at IS NOT NULL FROM ci WHERE id = $2),
		(SELECT coalesce(merged_into_id::text, '') FROM ci WHERE id = $2),
		(SELECT count(*) FROM ci_merge WHERE victim_id = $2)`, itemID, dup).Scan(&status, &deleted, &mergedInto, &merges); err != nil {
		t.Fatal(err)
	}
	if status != "open" || deleted || mergedInto != "" || merges != 0 {
		t.Errorf("after a refused merge: item %s, duplicate deleted=%v merged_into_id=%q ci_merge rows=%d; want open and untouched",
			status, deleted, mergedInto, merges)
	}
}

func TestAuditT3ProbableDuplicateMergeIsCIMerge(t *testing.T) {
	e := newReviewMergeEnv(t, true)
	f := e.f
	bg := context.Background()
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
	itemID := e.openDuplicate(t, dup, existing)
	if w := e.resolveMerge(itemID, existing); w.Code != http.StatusOK {
		t.Fatalf("resolve merge: %d %s", w.Code, w.Body.String())
	}
	var mergedInto, mergeID, status string
	var deleted, snapshot bool
	var merges, relsOnSurvivor, nicsOnSurvivor int
	if err := f.Admin.QueryRow(bg, `SELECT
		(SELECT coalesce(merged_into_id::text, '') FROM ci WHERE id = $1),
		(SELECT deleted_at IS NOT NULL FROM ci WHERE id = $1),
		(SELECT count(*) FROM ci_merge WHERE victim_id = $1),
		(SELECT coalesce(max(id::text), '') FROM ci_merge WHERE victim_id = $1),
		(SELECT coalesce(bool_and(snapshot ? 'victim' AND snapshot_until > now()), false) FROM ci_merge WHERE victim_id = $1),
		(SELECT count(*) FROM ci_relationship WHERE source_ci_id = $2 AND target_ci_id = $3),
		(SELECT count(*) FROM network_interface WHERE ci_id = $2 AND name = 'eth9'),
		(SELECT status FROM review_item WHERE id = $4)`, dup, existing, peer, itemID).
		Scan(&mergedInto, &deleted, &merges, &mergeID, &snapshot, &relsOnSurvivor, &nicsOnSurvivor, &status); err != nil {
		t.Fatal(err)
	}
	if mergedInto != existing || !deleted || merges != 1 || !snapshot {
		t.Errorf("duplicate after review merge: merged_into_id=%q deleted=%v ci_merge rows=%d snapshot=%v; want %s, true, 1 and true (CI-12 merge with tombstone and snapshot, not a soft delete)",
			mergedInto, deleted, merges, snapshot, existing)
	}
	if relsOnSurvivor != 1 || nicsOnSurvivor != 1 {
		t.Errorf("references on the survivor after review merge: relationships=%d interfaces=%d; want 1 and 1 (they stay on the deleted duplicate)",
			relsOnSurvivor, nicsOnSurvivor)
	}
	if status != "resolved" {
		t.Errorf("review item after merge: %s, want resolved", status)
	}

	// The merge is a job of kind ci.merge with the merge row as reference.
	list, _, err := e.jobs.List(e.ctx, f.OrgA, jobs.Filter{Kind: ci.MergeJobKind}, api.PaginationParams{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var job *jobs.Job
	for i := range list {
		if list[i].RefTable == "ci_merge" && list[i].RefID == mergeID {
			job = &list[i]
		}
	}
	if job == nil || job.Status != jobs.StatusSucceeded {
		t.Errorf("ci.merge job of merge %s: %+v (jobs %+v)", mergeID, job, list)
	}

	// GET on the duplicate points to the survivor (tombstone).
	w := e.do(http.MethodGet, "/api/v1/cis/"+dup, "")
	if w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "/api/v1/cis/"+existing {
		t.Errorf("GET duplicate: %d Location=%q, want 301 to %s", w.Code, w.Header().Get("Location"), existing)
	}

	// The unmerge restores the duplicate and its references.
	if w = e.do(http.MethodPost, "/api/v1/cis/"+dup+"/unmerge", ""); w.Code != http.StatusOK {
		t.Fatalf("unmerge: %d %s", w.Code, w.Body.String())
	}
	var relsOnDup, nicsOnDup int
	if err := f.Admin.QueryRow(bg, `SELECT
		(SELECT coalesce(merged_into_id::text, '') FROM ci WHERE id = $1),
		(SELECT deleted_at IS NOT NULL FROM ci WHERE id = $1),
		(SELECT count(*) FROM ci_relationship WHERE source_ci_id = $1 AND target_ci_id = $2),
		(SELECT count(*) FROM network_interface WHERE ci_id = $1 AND name = 'eth9')`, dup, peer).
		Scan(&mergedInto, &deleted, &relsOnDup, &nicsOnDup); err != nil {
		t.Fatal(err)
	}
	if mergedInto != "" || deleted || relsOnDup != 1 || nicsOnDup != 1 {
		t.Errorf("duplicate after unmerge: merged_into_id=%q deleted=%v relationships=%d interfaces=%d; want restored",
			mergedInto, deleted, relsOnDup, nicsOnDup)
	}
	if w = e.do(http.MethodGet, "/api/v1/cis/"+dup, ""); w.Code != http.StatusOK {
		t.Errorf("GET duplicate after unmerge: %d", w.Code)
	}
}

// TestProbableDuplicateMergeRefusals: a CI that is no candidate is 400, a
// merge of a CI another merge holds or has merged is 409; each leaves the
// item open and the duplicate untouched.
func TestProbableDuplicateMergeRefusals(t *testing.T) {
	e := newReviewMergeEnv(t, true)
	f := e.f
	bg := context.Background()
	existing := f.CI(t, f.OrgA, f.Client1, "review-merge-existing")
	other := f.CI(t, f.OrgA, f.Client1, "review-merge-other")

	t.Run("not a candidate", func(t *testing.T) {
		dup := f.CI(t, f.OrgA, f.Client1, "review-merge-dup-400")
		itemID := e.openDuplicate(t, dup, existing)
		if w := e.resolveMerge(itemID, other); w.Code != http.StatusBadRequest {
			t.Errorf("merge into a CI that is no candidate: %d %s, want 400", w.Code, w.Body.String())
		}
		e.assertUntouched(t, itemID, dup)
	})

	t.Run("parallel merge holds the duplicate", func(t *testing.T) {
		dup := f.CI(t, f.OrgA, f.Client1, "review-merge-dup-locked")
		itemID := e.openDuplicate(t, dup, existing)
		tx, err := f.Admin.Begin(bg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(bg, `SELECT id FROM ci WHERE id = $1 FOR UPDATE`, dup); err != nil {
			_ = tx.Rollback(bg)
			t.Fatal(err)
		}
		// A merge that waited for the lock would block until the deadline.
		reqCtx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
		w := httptest.NewRecorder()
		e.mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/review-items/"+itemID+"/resolve",
			strings.NewReader(`{"action":"merge","ci_id":"`+existing+`"}`)).WithContext(reqCtx))
		cancel()
		_ = tx.Rollback(bg)
		if w.Code != http.StatusConflict {
			t.Errorf("merge while another merge holds the duplicate: %d %s, want 409", w.Code, w.Body.String())
		}
		e.assertUntouched(t, itemID, dup)
	})

	t.Run("duplicate merged elsewhere", func(t *testing.T) {
		dup := f.CI(t, f.OrgA, f.Client1, "review-merge-dup-merged")
		itemID := e.openDuplicate(t, dup, existing)
		if w := e.do(http.MethodPost, "/api/v1/cis/"+other+"/merge", `{"victim_id":"`+dup+`"}`); w.Code != http.StatusOK {
			t.Fatalf("CI-12 merge: %d %s", w.Code, w.Body.String())
		}
		if w := e.resolveMerge(itemID, existing); w.Code != http.StatusConflict {
			t.Errorf("merge of a duplicate merged into another CI: %d %s, want 409", w.Code, w.Body.String())
		}
		var status, mergedInto string
		if err := f.Admin.QueryRow(bg, `SELECT (SELECT status FROM review_item WHERE id = $1),
			(SELECT coalesce(merged_into_id::text, '') FROM ci WHERE id = $2)`, itemID, dup).Scan(&status, &mergedInto); err != nil {
			t.Fatal(err)
		}
		if status != "open" || mergedInto != other {
			t.Errorf("after the refused merge: item %s, duplicate merged into %q; want open and %s", status, mergedInto, other)
		}
	})
}

// TestProbableDuplicateMergeWithoutMerger: without the CI-12 merge the
// resolution merge is 501 and deletes nothing.
func TestProbableDuplicateMergeWithoutMerger(t *testing.T) {
	e := newReviewMergeEnv(t, false)
	f := e.f
	existing := f.CI(t, f.OrgA, f.Client1, "review-nomerger-existing")
	dup := f.CI(t, f.OrgA, f.Client1, "review-nomerger-dup")
	itemID := e.openDuplicate(t, dup, existing)
	if w := e.resolveMerge(itemID, existing); w.Code != http.StatusNotImplemented {
		t.Errorf("merge without merger: %d %s, want 501", w.Code, w.Body.String())
	}
	e.assertUntouched(t, itemID, dup)
}
