package server_test

// WP-554 (JOB-04, F-330, F-331, E-89): the review merge through the real
// router. POST /api/v1/review-items/{id}/resolve {action: merge, ci_id} of an
// ambiguous_identity item runs the discovery effect in the request (E-89,
// option 1) against the PostgreSQL repositories and is mirrored by a job of
// kind review.merge that GET /api/v1/jobs?kind=review.merge shows. Asserted
// here, with the complete server.NewRouter (authorization and OpenAPI
// validation, newAuditT3Router):
// - a principal restricted to another client gets 404 and changes nothing;
// - a principal without review:resolve gets 403 and changes nothing;
// - a merge whose effect fails (the item reports an address the database
//   refuses, 400) leaves the item open and the job failed; the retry once
//   the item carries a valid address answers 200, sets the management IP of
//   the candidate and ends the same job succeeded with attempts 2 and
//   without the error of the failed attempt (F-330);
// - the job is not visible to the principal of the other client.
//
// WP-643 (F-371, F-372, API-03, JOB-04), formerly the defect tests of
// k5_review_merge_errors_auditdefect_test.go:
//   - TestAuditReviewMergeIntoDeletedCandidateIsNotFound: a merge into a
//     candidate CI deleted meanwhile, the accept of observed values on a
//     deleted CI and the reactivation of a CI that is not deleted answer
//     404 problem+json (resolveCIReview maps ci.ErrNotFound); the item stays
//     open, the review.merge job fails with a sanitized error.
//   - TestAuditReviewMergeJobErrorIsSanitized: the error of a failed
//     review.merge job and of a failed export job, readable with job:read,
//     carries no database internals (jobs.ErrorMessage).
//
// WP-567 (CI-05, residual point of WP-643): TestReviewReactivationOfMergedCI
// IsConflict, the reactivation of a CI merged into another answers 409
// instead of 500.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed):
//
//	cd backend && TEST_DATABASE_URL=... go test -count=1 -run 'TestAuditReviewMerge|TestReviewMergeJobThroughRouter|TestReviewReactivation' ./internal/server/

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

func TestReviewMergeJobThroughRouter(t *testing.T) {
	r := newAuditT3Router(t, "f6")
	f := r.f
	candidate := f.CI(t, f.OrgA, f.Client1, "wp554-router-candidate")
	other := f.CI(t, f.OrgA, f.Client1, "wp554-router-other")
	const ip, badIP = "10.55.4.17", "10.55.4.317"
	item := r.str(`INSERT INTO review_item (organization_id, kind, status, payload, candidate_ci_ids)
		VALUES ($1, 'ambiguous_identity', 'open', jsonb_build_object('name', 'wp554-router', 'management_ip', $2::text), ARRAY[$3::uuid, $4::uuid])
		RETURNING id::text`, f.OrgA, badIP, candidate, other)
	path := "/api/v1/review-items/" + item + "/resolve"
	body := `{"action":"merge","ci_id":"` + candidate + `"}`
	managementIP := func() string {
		return r.str(`SELECT coalesce(host(management_ip), '') FROM ci WHERE id = $1`, candidate)
	}
	status := func() string { return r.str(`SELECT status FROM review_item WHERE id = $1`, item) }

	// callClient sends one request as a principal restricted to clientID.
	callClient := func(clientID string, perms []identity.Permission, method, target, payload string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, target, bytes.NewReader([]byte(payload)))
		req.Header.Set("Content-Type", "application/json")
		principal := identity.Principal{Subject: r.other, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
			Scope: &identity.Scope{Clients: identity.ScopeSet{IDs: []string{clientID}}, Sites: identity.ScopeSet{All: true},
				Teams: identity.ScopeSet{All: true}},
			Permissions: perms, Credential: "wp554"}
		ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), principal), principal.Permissions)
		w := httptest.NewRecorder()
		r.router.ServeHTTP(w, req.WithContext(ctx))
		return w
	}
	type jobView struct {
		ID       string `json:"id"`
		Kind     string `json:"kind"`
		Status   string `json:"status"`
		Error    string `json:"error"`
		Attempts int    `json:"attempts"`
		RefTable string `json:"ref_table"`
		RefID    string `json:"ref_id"`
	}
	// mergeJobs lists the review.merge jobs of the item through the router.
	mergeJobs := func(w *httptest.ResponseRecorder) []jobView {
		t.Helper()
		if w.Code != http.StatusOK {
			t.Fatalf("GET /api/v1/jobs?kind=review.merge: %d %s", w.Code, w.Body.String())
		}
		var list struct {
			Data []jobView `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		var out []jobView
		for _, j := range list.Data {
			if j.Kind != "review.merge" {
				t.Errorf("GET /api/v1/jobs?kind=review.merge lists a job of kind %q", j.Kind)
			}
			if j.RefID == item {
				out = append(out, j)
			}
		}
		return out
	}
	jobsOfItem := func() []jobView {
		t.Helper()
		return mergeJobs(r.call(identity.AllPermissions(), http.MethodGet, "/api/v1/jobs?kind=review.merge", ""))
	}
	unchanged := func(after string) {
		t.Helper()
		if s := status(); s != "open" {
			t.Errorf("item after %s: %s, want open", after, s)
		}
		if got := managementIP(); got != "" {
			t.Errorf("management IP of the candidate after %s: %q, want none", after, got)
		}
	}

	// A principal of another client does not see the item: 404.
	if w := callClient(f.Client2, identity.AllPermissions(), http.MethodPost, path, body); w.Code != http.StatusNotFound {
		t.Errorf("resolve merge by a principal of client 2: %d %s, want 404", w.Code, w.Body.String())
	}
	unchanged("the resolve of another client")
	// Without review:resolve: 403.
	if w := r.call(auditT3Without(identity.PermReviewResolve), http.MethodPost, path, body); w.Code != http.StatusForbidden {
		t.Errorf("resolve merge without review:resolve: %d %s, want 403", w.Code, w.Body.String())
	}
	unchanged("the resolve without review:resolve")
	if got := jobsOfItem(); len(got) != 0 {
		t.Errorf("review.merge jobs after the refused resolves: %+v, want none", got)
	}

	// The item reports an address the database refuses: the effect fails
	// (400), the item stays open and the job fails with the error.
	if w := r.call(identity.AllPermissions(), http.MethodPost, path, body); w.Code != http.StatusBadRequest {
		t.Fatalf("resolve merge with the management IP %s: %d %s, want 400", badIP, w.Code, w.Body.String())
	}
	unchanged("the failed merge")
	failed := jobsOfItem()
	if len(failed) != 1 || failed[0].Status != "failed" || failed[0].Error == "" || failed[0].Attempts != 1 || failed[0].RefTable != "review_item" {
		t.Fatalf("review.merge job of the failed merge: %+v, want one failed job with an error", failed)
	}

	// The retry once the item carries a valid address: 200, the candidate
	// takes the management IP of the item, the same job is succeeded.
	r.str(`UPDATE review_item SET payload = payload || jsonb_build_object('management_ip', $2::text) WHERE id = $1 RETURNING id::text`, item, ip)
	w := r.call(identity.AllPermissions(), http.MethodPost, path, body)
	if w.Code != http.StatusOK {
		t.Fatalf("retry of the resolve merge: %d %s, want 200", w.Code, w.Body.String())
	}
	var resolved struct {
		Status     string `json:"status"`
		ResolvedBy string `json:"resolved_by"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resolved); err != nil || resolved.Status != "resolved" || resolved.ResolvedBy != r.user {
		t.Errorf("response of the merge: %s (%v), want resolved by %s", w.Body.String(), err, r.user)
	}
	if s := status(); s != "resolved" {
		t.Errorf("item after the merge: %s, want resolved", s)
	}
	if got := managementIP(); got != ip {
		t.Errorf("management IP of the candidate after the merge: %q, want %s", got, ip)
	}
	done := jobsOfItem()
	if len(done) != 1 || done[0].ID != failed[0].ID || done[0].Status != "succeeded" || done[0].Attempts != 2 || done[0].Error != "" {
		t.Errorf("review.merge job after the retry: %+v, want job %s succeeded with attempts 2 and no error", done, failed[0].ID)
	}

	// The job of an org-wide merge is not visible to the principal of a
	// client.
	if got := mergeJobs(callClient(f.Client1, identity.AllPermissions(), http.MethodGet, "/api/v1/jobs?kind=review.merge", "")); len(got) != 0 {
		t.Errorf("review.merge job visible to a principal of client 1: %+v", got)
	}
}

// reviewMergeLeaks are texts of database internals an API output must not
// carry (API-03, F-047, F-372).
var reviewMergeLeaks = []string{"SQLSTATE", "ERROR:", "invalid input syntax", "type inet", "type uuid", "update ci:", "pgx", "export_job"}

type reviewMergeJobView struct {
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Error  string `json:"error"`
	RefID  string `json:"ref_id"`
}

// reviewMergeJob returns the job of kind with the reference refID that GET
// /api/v1/jobs?kind=<kind> shows a principal with only job:read.
func reviewMergeJob(t *testing.T, r *auditT3Router, kind, refID string) reviewMergeJobView {
	t.Helper()
	jw := r.call([]identity.Permission{identity.PermJobRead}, http.MethodGet, "/api/v1/jobs?kind="+kind+"&limit=100", "")
	if jw.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/jobs?kind=%s with job:read: %d %s", kind, jw.Code, jw.Body.String())
	}
	var list struct {
		Data []reviewMergeJobView `json:"data"`
	}
	if err := json.Unmarshal(jw.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	for _, j := range list.Data {
		if j.RefID == refID {
			return j
		}
	}
	t.Fatalf("no %s job for %s in %s", kind, refID, jw.Body.String())
	return reviewMergeJobView{}
}

// assertNoLeak fails when out carries database internals.
func assertNoLeak(t *testing.T, what, out string) {
	t.Helper()
	for _, leak := range reviewMergeLeaks {
		if strings.Contains(out, leak) {
			t.Errorf("%s contains %q: %q; want a sanitized message", what, leak, out)
		}
	}
}

// TestAuditReviewMergeIntoDeletedCandidateIsNotFound: the merge of an
// ambiguous_identity item into a candidate CI that is deleted (soft delete)
// answers 404 problem+json without internal text, like a merge into a CI the
// caller cannot see; the item stays open, the CI stays deleted and the
// review.merge job fails with a sanitized error. The accept of a
// conflicting_values item whose CI is deleted and the reactivation of a
// resurrected_device item whose CI is not deleted (Restore) answer 404 as
// well (F-371).
func TestAuditReviewMergeIntoDeletedCandidateIsNotFound(t *testing.T) {
	r := newAuditT3Router(t, "f6")
	f := r.f
	candidate := f.CI(t, f.OrgA, f.Client1, "k5-merge-deleted-candidate")
	other := f.CI(t, f.OrgA, f.Client1, "k5-merge-other-candidate")
	item := r.str(`INSERT INTO review_item (organization_id, kind, status, payload, candidate_ci_ids)
		VALUES ($1, 'ambiguous_identity', 'open', jsonb_build_object('name', 'k5-merge-deleted', 'management_ip', '10.55.4.18'), ARRAY[$2::uuid, $3::uuid])
		RETURNING id::text`, f.OrgA, candidate, other)
	r.str(`UPDATE ci SET deleted_at = now() WHERE id = $1 RETURNING id::text`, candidate)

	// notFound resolves item with body and expects 404 problem+json without
	// internals and the item still open.
	notFound := func(what, item, body string) {
		t.Helper()
		w := r.call(identity.AllPermissions(), http.MethodPost, "/api/v1/review-items/"+item+"/resolve", body)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: %d %s, want 404", what, w.Code, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
			t.Errorf("%s: Content-Type %q, want application/problem+json", what, ct)
		}
		assertNoLeak(t, "response of "+what, w.Body.String())
		if got := r.str(`SELECT status FROM review_item WHERE id = $1`, item); got != "open" {
			t.Errorf("item after %s: %s, want open", what, got)
		}
	}

	notFound("merge into the deleted candidate", item, `{"action":"merge","ci_id":"`+candidate+`"}`)
	if got := r.str(`SELECT (deleted_at IS NOT NULL)::text FROM ci WHERE id = $1`, candidate); got != "true" {
		t.Errorf("candidate deleted after the refused merge: %s, want true", got)
	}
	job := reviewMergeJob(t, r, "review.merge", item)
	if job.Status != "failed" || job.Error == "" {
		t.Errorf("review.merge job of the merge into the deleted candidate: status %q error %q, want failed with an error", job.Status, job.Error)
	}
	assertNoLeak(t, "error of the review.merge job", job.Error)

	// accept: the observed serial number of a CI deleted meanwhile.
	accepted := f.CI(t, f.OrgA, f.Client1, "k5-accept-deleted")
	conflict := r.str(`INSERT INTO review_item (organization_id, kind, status, payload, candidate_ci_ids)
		VALUES ($1, 'conflicting_values', 'open', jsonb_build_object('matched_ci_id', $2::text,
			'incoming', jsonb_build_object('serial_number', 'K5-ACCEPT-1')), ARRAY[$2::uuid])
		RETURNING id::text`, f.OrgA, accepted)
	r.str(`UPDATE ci SET deleted_at = now() WHERE id = $1 RETURNING id::text`, accepted)
	notFound("accept on the deleted CI", conflict, `{"action":"accept"}`)

	// Restore: the CI of a resurrected_device item is no longer deleted.
	live := f.CI(t, f.OrgA, f.Client1, "k5-resurrected-live")
	resurrected := r.str(`INSERT INTO review_item (organization_id, kind, status, payload, candidate_ci_ids)
		VALUES ($1, 'resurrected_device', 'open', jsonb_build_object('name', 'k5-resurrected', 'management_ip', '10.55.4.19'), ARRAY[$2::uuid])
		RETURNING id::text`, f.OrgA, live)
	notFound("reactivation of a CI that is not deleted", resurrected, `{"action":"merge","ci_id":"`+live+`"}`)
	job = reviewMergeJob(t, r, "review.merge", resurrected)
	if job.Status != "failed" || job.Error == "" {
		t.Errorf("review.merge job of the refused reactivation: status %q error %q, want failed with an error", job.Status, job.Error)
	}
	assertNoLeak(t, "error of the review.merge job", job.Error)
}

// TestReviewReactivationOfMergedCIIsConflict: merge of a resurrected_device
// item whose CI was merged into another CI meanwhile answers 409
// problem+json (the CI comes back only through the unmerge, CI-12) without
// internals; the item stays open and the CI stays merged and deleted. Before
// WP-567 the ci.ErrMergeConflict of the restore reached the client as 500.
func TestReviewReactivationOfMergedCIIsConflict(t *testing.T) {
	r := newAuditT3Router(t, "f6")
	f := r.f
	survivor := f.CI(t, f.OrgA, f.Client1, "wp567-review-survivor")
	merged := f.CI(t, f.OrgA, f.Client1, "wp567-review-merged")
	r.str(`UPDATE ci SET merged_into_id = $2, deleted_at = now(), status = 'decommissioned' WHERE id = $1 RETURNING id::text`, merged, survivor)
	item := r.str(`INSERT INTO review_item (organization_id, kind, status, payload, candidate_ci_ids)
		VALUES ($1, 'resurrected_device', 'open', jsonb_build_object('name', 'wp567-resurrected', 'management_ip', '10.55.4.20'), ARRAY[$2::uuid])
		RETURNING id::text`, f.OrgA, merged)

	w := r.call(identity.AllPermissions(), http.MethodPost, "/api/v1/review-items/"+item+"/resolve", `{"action":"merge","ci_id":"`+merged+`"}`)
	if w.Code != http.StatusConflict {
		t.Errorf("reactivation of a merged CI: %d %s, want 409", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("Content-Type %q, want application/problem+json", ct)
	}
	assertNoLeak(t, "response of the reactivation of a merged CI", w.Body.String())
	if got := r.str(`SELECT status FROM review_item WHERE id = $1`, item); got != "open" {
		t.Errorf("item after the refused reactivation: %s, want open", got)
	}
	if got := r.str(`SELECT (deleted_at IS NOT NULL AND merged_into_id IS NOT NULL)::text FROM ci WHERE id = $1`, merged); got != "true" {
		t.Errorf("merged CI deleted and merged after the refused reactivation: %s, want true", got)
	}
}

// TestAuditReviewMergeJobErrorIsSanitized: a merge whose effect the database
// refuses (the item reports the address 10.55.4.317) answers 400 and fails
// its review.merge job; neither the response nor the error of the job that
// GET /api/v1/jobs?kind=review.merge returns carries database internals
// (API-03, as F-047 for responses). An export whose rendering the database
// refuses (a client filter that is no UUID) fails its export job and the
// job mirroring it with a sanitized error as well (F-372).
func TestAuditReviewMergeJobErrorIsSanitized(t *testing.T) {
	r := newAuditT3Router(t, "f6")
	f := r.f
	candidate := f.CI(t, f.OrgA, f.Client1, "k5-merge-job-candidate")
	other := f.CI(t, f.OrgA, f.Client1, "k5-merge-job-other")
	item := r.str(`INSERT INTO review_item (organization_id, kind, status, payload, candidate_ci_ids)
		VALUES ($1, 'ambiguous_identity', 'open', jsonb_build_object('name', 'k5-merge-job', 'management_ip', '10.55.4.317'), ARRAY[$2::uuid, $3::uuid])
		RETURNING id::text`, f.OrgA, candidate, other)

	w := r.call(identity.AllPermissions(), http.MethodPost, "/api/v1/review-items/"+item+"/resolve",
		`{"action":"merge","ci_id":"`+candidate+`"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("merge with the management IP 10.55.4.317: %d %s, want 400", w.Code, w.Body.String())
	}
	assertNoLeak(t, "response of the failed merge", w.Body.String())
	job := reviewMergeJob(t, r, "review.merge", item)
	if job.Status != "failed" || job.Error == "" {
		t.Errorf("review.merge job of the failed merge: status %q error %q, want failed with an error", job.Status, job.Error)
	}
	assertNoLeak(t, "error of the review.merge job readable with job:read", job.Error)

	t.Run("export job", func(t *testing.T) {
		bg := context.Background()
		repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
		worker := export.NewJobWorker(repos.ExportJobs, repos.CI, blob.NewFileStore(t.TempDir()))
		// As wired by the router (server.NewRouter).
		worker.Tracker = jobs.NewService(repos.Jobs)
		exportID := r.str(`INSERT INTO export_job (organization_id, initiated_by, format, status, filters, scope_recorded)
			VALUES ($1, $2, 'json', 'pending', jsonb_build_object('client_id', 'k5-not-a-uuid'), true)
			RETURNING id::text`, f.OrgA, r.user)
		// The job mirroring the export, tracked as POST /api/v1/exports
		// tracks it: in the request of its creator.
		scope := database.OrgWideScope(f.OrgA, r.user)
		creator := tenant.WithTenant(database.ContextWithTenantScope(bg, &scope),
			tenant.TenantInfo{OrganizationID: f.OrgA, UserID: r.user})
		if err := worker.Tracker.Track(creator, f.OrgA, "export", "export_job", exportID, r.user); err != nil {
			t.Fatal(err)
		}
		for worker.ProcessOne(bg) {
		}
		var status, message string
		if err := f.Admin.QueryRow(bg, `SELECT status, COALESCE(error_message, '') FROM export_job WHERE id = $1`,
			exportID).Scan(&status, &message); err != nil {
			t.Fatal(err)
		}
		if status != export.JobStatusFailed || message == "" {
			t.Fatalf("export job with the client filter k5-not-a-uuid: status %q error %q, want failed with an error", status, message)
		}
		assertNoLeak(t, "error of the export job", message)
		ew := r.call([]identity.Permission{identity.PermJobRead}, http.MethodGet, "/api/v1/exports/"+exportID, "")
		if ew.Code != http.StatusOK {
			t.Fatalf("GET /api/v1/exports/%s: %d %s", exportID, ew.Code, ew.Body.String())
		}
		assertNoLeak(t, "GET /api/v1/exports/{id}", ew.Body.String())
		mirrored := reviewMergeJob(t, r, "export", exportID)
		if mirrored.Status != "failed" || mirrored.Error == "" {
			t.Errorf("job mirroring the failed export: status %q error %q, want failed with an error", mirrored.Status, mirrored.Error)
		}
		assertNoLeak(t, "error of the export job readable with job:read", mirrored.Error)
	})
}
