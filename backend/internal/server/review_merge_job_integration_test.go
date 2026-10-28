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
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed):
//
//	cd backend && TEST_DATABASE_URL=... go test -count=1 -run TestReviewMergeJobThroughRouter ./internal/server/

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
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
