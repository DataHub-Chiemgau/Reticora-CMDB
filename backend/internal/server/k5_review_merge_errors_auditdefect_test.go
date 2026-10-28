//go:build auditdefect

package server_test

// Defect tests found by WP-554 (t7-review-merge-job, JOB-04, API-03), run
// through the complete server.NewRouter (newAuditT3Router) against
// PostgreSQL:
//
//   - F-371: POST /api/v1/review-items/{id}/resolve {action: merge} of an
//     ambiguous_identity item into a candidate CI that was soft-deleted
//     meanwhile answers 500. resolveCIReview (discovery/review.go) passes
//     the ci.ErrNotFound of ciRepo.Update unchanged; reviews.writeError maps
//     only reviews.ErrNotFound and api.ErrNotFound, so api.WriteRepoError
//     answers "Internal Error".
//   - F-372: the review.merge job of a failed merge carries the raw error of
//     the database in its error field (SQLSTATE, "ERROR:", the column type),
//     readable for every holder of job:read through GET /api/v1/jobs.
//     reviews.Service.runEffect (and ci.Service.MergeAs for the ci.merge
//     job) mirror err.Error() into the job.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=... go test -tags auditdefect -count=1 -v \
//	  -run '^(TestAuditReviewMergeIntoDeletedCandidateIsNotFound|TestAuditReviewMergeJobErrorIsSanitized)$' ./internal/server/

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// TestAuditReviewMergeIntoDeletedCandidateIsNotFound: the merge of an
// ambiguous_identity item into a candidate CI that is deleted (soft delete)
// answers 404 problem+json without internal text, like a merge into a CI the
// caller cannot see; the item stays open and the CI stays deleted.
func TestAuditReviewMergeIntoDeletedCandidateIsNotFound(t *testing.T) {
	r := newAuditT3Router(t, "f6")
	f := r.f
	candidate := f.CI(t, f.OrgA, f.Client1, "k5-merge-deleted-candidate")
	other := f.CI(t, f.OrgA, f.Client1, "k5-merge-other-candidate")
	item := r.str(`INSERT INTO review_item (organization_id, kind, status, payload, candidate_ci_ids)
		VALUES ($1, 'ambiguous_identity', 'open', jsonb_build_object('name', 'k5-merge-deleted', 'management_ip', '10.55.4.18'), ARRAY[$2::uuid, $3::uuid])
		RETURNING id::text`, f.OrgA, candidate, other)
	r.str(`UPDATE ci SET deleted_at = now() WHERE id = $1 RETURNING id::text`, candidate)

	w := r.call(identity.AllPermissions(), http.MethodPost, "/api/v1/review-items/"+item+"/resolve",
		`{"action":"merge","ci_id":"`+candidate+`"}`)
	if w.Code != http.StatusNotFound {
		t.Errorf("merge into the deleted candidate: %d %s, want 404", w.Code, w.Body.String())
	}
	if got := r.str(`SELECT status FROM review_item WHERE id = $1`, item); got != "open" {
		t.Errorf("item after the refused merge: %s, want open", got)
	}
	if got := r.str(`SELECT (deleted_at IS NOT NULL)::text FROM ci WHERE id = $1`, candidate); got != "true" {
		t.Errorf("candidate deleted after the refused merge: %s, want true", got)
	}
}

// TestAuditReviewMergeJobErrorIsSanitized: a merge whose effect the database
// refuses (the item reports the address 10.55.4.317) answers 400 and fails
// its review.merge job; neither the response nor the error of the job that
// GET /api/v1/jobs?kind=review.merge returns carries database internals
// (API-03, as F-047 for responses).
func TestAuditReviewMergeJobErrorIsSanitized(t *testing.T) {
	r := newAuditT3Router(t, "f6")
	f := r.f
	candidate := f.CI(t, f.OrgA, f.Client1, "k5-merge-job-candidate")
	other := f.CI(t, f.OrgA, f.Client1, "k5-merge-job-other")
	item := r.str(`INSERT INTO review_item (organization_id, kind, status, payload, candidate_ci_ids)
		VALUES ($1, 'ambiguous_identity', 'open', jsonb_build_object('name', 'k5-merge-job', 'management_ip', '10.55.4.317'), ARRAY[$2::uuid, $3::uuid])
		RETURNING id::text`, f.OrgA, candidate, other)

	leaks := []string{"SQLSTATE", "ERROR:", "invalid input syntax", "type inet", "update ci:"}
	w := r.call(identity.AllPermissions(), http.MethodPost, "/api/v1/review-items/"+item+"/resolve",
		`{"action":"merge","ci_id":"`+candidate+`"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("merge with the management IP 10.55.4.317: %d %s, want 400", w.Code, w.Body.String())
	}
	for _, leak := range leaks {
		if strings.Contains(w.Body.String(), leak) {
			t.Errorf("response of the failed merge contains %q: %s", leak, w.Body.String())
		}
	}

	jw := r.call([]identity.Permission{identity.PermJobRead}, http.MethodGet, "/api/v1/jobs?kind=review.merge", "")
	if jw.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/jobs?kind=review.merge with job:read: %d %s", jw.Code, jw.Body.String())
	}
	var list struct {
		Data []struct {
			Status string `json:"status"`
			Error  string `json:"error"`
			RefID  string `json:"ref_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(jw.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, j := range list.Data {
		if j.RefID != item {
			continue
		}
		found = true
		if j.Status != "failed" || j.Error == "" {
			t.Errorf("review.merge job of the failed merge: status %q error %q, want failed with an error", j.Status, j.Error)
		}
		for _, leak := range leaks {
			if strings.Contains(j.Error, leak) {
				t.Errorf("error of the review.merge job readable with job:read contains %q: %q; want a sanitized message", leak, j.Error)
			}
		}
	}
	if !found {
		t.Fatalf("no review.merge job for item %s in %s", item, jw.Body.String())
	}
}
