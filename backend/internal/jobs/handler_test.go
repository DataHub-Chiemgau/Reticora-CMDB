package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

type apiFixture struct {
	svc      *Service
	mux      chi.Router
	reverted []string
}

func newAPIFixture(t *testing.T) *apiFixture {
	t.Helper()
	f := &apiFixture{svc: NewService(NewMemoryRepository())}
	for _, k := range []string{"ci.bulk", "ci.merge", "system.index"} {
		f.svc.Register(Kind{Name: k, MaxAttempts: 1})
	}
	h := NewAPIHandler(f.svc).
		WithKind("ci.bulk", identity.PermCIWrite, nil).
		WithKind("ci.merge", identity.PermCIDelete, func(_ context.Context, job *Job) error {
			if job.RefID == "broken" {
				return errors.New("the snapshot has expired")
			}
			f.reverted = append(f.reverted, job.ID)
			return nil
		})
	f.mux = chi.NewRouter()
	h.RegisterRoutes(f.mux)
	return f
}

// call runs a request as an org-wide user with the given permissions.
func (f *apiFixture) call(method, path, user string, perms ...identity.Permission) *httptest.ResponseRecorder {
	scope := database.OrgWideScope("org-1", user)
	return f.callIn(&scope, method, path, user, perms...)
}

// callIn runs a request as user with the tenant scope (nil: none) and the
// given permissions.
func (f *apiFixture) callIn(scope *database.TenantScope, method, path, user string, perms ...identity.Permission) *httptest.ResponseRecorder {
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1", UserID: user})
	if scope != nil {
		ctx = database.ContextWithTenantScope(ctx, scope)
	}
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: user, OrganizationID: "org-1",
		Permissions: append([]identity.Permission{identity.PermJobRead}, perms...)})
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, httptest.NewRequest(method, path, http.NoBody).WithContext(ctx))
	return w
}

func (f *apiFixture) enqueue(t *testing.T, kind, creator, ref string) *Job {
	t.Helper()
	job, _, err := f.svc.Enqueue(context.Background(), "org-1", kind, map[string]string{"x": "y"}, EnqueueOptions{CreatedBy: creator})
	if err != nil {
		t.Fatal(err)
	}
	if ref != "" {
		job.RefID = ref
	}
	return job
}

func (f *apiFixture) finish(t *testing.T, job *Job, result any) {
	t.Helper()
	if _, err := f.svc.ClaimJob(context.Background(), "org-1", job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Succeed(context.Background(), "org-1", job.ID, result); err != nil {
		t.Fatal(err)
	}
}

// TestJobAPIListGetResult covers API-09 and JOB-03 (WP-182): the list with
// kind and status filters, the detail and the result of a job.
func TestJobAPIListGetResult(t *testing.T) {
	f := newAPIFixture(t)
	done := f.enqueue(t, "ci.bulk", "alice", "")
	f.finish(t, done, map[string]int{"succeeded": 3})
	f.enqueue(t, "ci.bulk", "alice", "")
	f.enqueue(t, "system.index", "", "")

	var list struct {
		Data  []Job `json:"data"`
		Total int   `json:"total"`
	}
	w := f.call("GET", "/api/v1/jobs?kind=ci.bulk&status=queued", "bob")
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != http.StatusOK || list.Total != 1 || list.Data[0].Kind != "ci.bulk" {
		t.Errorf("list: %d %s", w.Code, w.Body.String())
	}
	if w = f.call("GET", "/api/v1/jobs?status=sleeping", "bob"); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown status: %d", w.Code)
	}
	if w = f.call("GET", "/api/v1/jobs/"+done.ID, "bob"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"succeeded"`) {
		t.Errorf("get: %d %s", w.Code, w.Body.String())
	}
	if w = f.call("GET", "/api/v1/jobs/"+done.ID+"/result", "bob"); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"succeeded":3}` {
		t.Errorf("result: %d %s", w.Code, w.Body.String())
	}
	if w = f.call("GET", "/api/v1/jobs/"+list.Data[0].ID+"/result", "bob"); w.Code != http.StatusConflict {
		t.Errorf("result of a queued job: %d", w.Code)
	}
	if w = f.call("GET", "/api/v1/jobs/missing", "bob"); w.Code != http.StatusNotFound {
		t.Errorf("missing job: %d", w.Code)
	}
}

// TestJobAPICancel: the creator or a holder of the kind's permission
// cancels a queued job; others get 403; a finished job is 409.
func TestJobAPICancel(t *testing.T) {
	f := newAPIFixture(t)
	mine := f.enqueue(t, "ci.bulk", "alice", "")
	other := f.enqueue(t, "ci.bulk", "alice", "")
	system := f.enqueue(t, "system.index", "", "")
	if w := f.call("DELETE", "/api/v1/jobs/"+other.ID, "bob"); w.Code != http.StatusForbidden {
		t.Errorf("stranger without permission: %d", w.Code)
	}
	if w := f.call("DELETE", "/api/v1/jobs/"+mine.ID, "alice"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"cancelled"`) {
		t.Errorf("creator: %d %s", w.Code, w.Body.String())
	}
	if w := f.call("DELETE", "/api/v1/jobs/"+other.ID, "bob", identity.PermCIWrite); w.Code != http.StatusOK {
		t.Errorf("holder of ci:write: %d", w.Code)
	}
	if w := f.call("DELETE", "/api/v1/jobs/"+mine.ID, "alice"); w.Code != http.StatusConflict {
		t.Errorf("cancel a cancelled job: %d", w.Code)
	}
	if w := f.call("DELETE", "/api/v1/jobs/"+system.ID, "bob", identity.PermCIWrite); w.Code != http.StatusForbidden {
		t.Errorf("system job: %d", w.Code)
	}
}

// TestJobAPIRevert: only kinds with a defined revert (E-25) and succeeded
// jobs revert; the rest is 409.
func TestJobAPIRevert(t *testing.T) {
	f := newAPIFixture(t)
	merge := f.enqueue(t, "ci.merge", "alice", "")
	f.finish(t, merge, nil)
	bulk := f.enqueue(t, "ci.bulk", "alice", "")
	f.finish(t, bulk, nil)
	queuedMerge := f.enqueue(t, "ci.merge", "alice", "")
	if w := f.call("POST", "/api/v1/jobs/"+bulk.ID+"/revert", "alice"); w.Code != http.StatusConflict {
		t.Errorf("bulk revert: %d", w.Code)
	}
	if w := f.call("POST", "/api/v1/jobs/"+queuedMerge.ID+"/revert", "alice"); w.Code != http.StatusConflict {
		t.Errorf("revert of a queued job: %d", w.Code)
	}
	if w := f.call("POST", "/api/v1/jobs/"+merge.ID+"/revert", "bob"); w.Code != http.StatusForbidden {
		t.Errorf("stranger: %d", w.Code)
	}
	if w := f.call("POST", "/api/v1/jobs/"+merge.ID+"/revert", "bob", identity.PermCIDelete); w.Code != http.StatusOK {
		t.Errorf("revert: %d %s", w.Code, w.Body.String())
	}
	if len(f.reverted) != 1 || f.reverted[0] != merge.ID {
		t.Errorf("reverted %v", f.reverted)
	}
}

// TestJobAPIRevertErrors covers API-03 (WP-408): a reverter's refusal is
// 409 with its domain text; any other error of a reverter is a repository
// error and answers sanitized, without its text.
func TestJobAPIRevertErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		detail string
	}{
		{"refusal", &RevertRefusedError{Reason: errors.New("the merge snapshot has expired")},
			http.StatusConflict, "the merge snapshot has expired"},
		{"database error", fmt.Errorf("unmerge: %w", &pgconn.PgError{Code: "42501",
			Message: `new row violates row-level security policy for table "ci_merge"`}),
			http.StatusForbidden, "the operation is not permitted"},
		{"other error", errors.New("unmerge: failed to connect to `host=10.0.0.5 user=reticora`"),
			http.StatusInternalServerError, "the request could not be completed"},
	} {
		svc := NewService(NewMemoryRepository())
		svc.Register(Kind{Name: "ci.merge", MaxAttempts: 1})
		f := &apiFixture{svc: svc, mux: chi.NewRouter()}
		NewAPIHandler(svc).WithKind("ci.merge", identity.PermCIDelete, func(context.Context, *Job) error {
			return tc.err
		}).RegisterRoutes(f.mux)
		job := f.enqueue(t, "ci.merge", "alice", "")
		f.finish(t, job, nil)

		w := f.call("POST", "/api/v1/jobs/"+job.ID+"/revert", "alice")
		body := w.Body.String()
		if w.Code != tc.status || !strings.Contains(body, tc.detail) {
			t.Errorf("%s: %d %s, want %d with %q", tc.name, w.Code, body, tc.status, tc.detail)
		}
		for _, leak := range []string{"ci_merge", "row-level security", "10.0.0.5", "unmerge"} {
			if strings.Contains(body, leak) {
				t.Errorf("%s: body %s contains %q", tc.name, body, leak)
			}
		}
	}
}

// TestJobAPIHidesJobsOutsideTheScope covers F-163 (TEN-09, WP-404) on the
// handler: a job whose scope snapshot lies outside the caller's scope is
// 404 on every route, before the 403 of mayControl and the 409 of a
// finished or not revertible job, also for a holder of the kind's
// permission; a job without snapshot is the whole organization's, and a
// request without tenant scope sees no job. The memory repository has no
// row level security, so the handler's own check answers here.
func TestJobAPIHidesJobsOutsideTheScope(t *testing.T) {
	f := newAPIFixture(t)
	scoped := func(user, client string) *database.TenantScope {
		s := database.OrgWideScope("org-1", user)
		s.Clients = database.ScopeIDs(client)
		return &s
	}
	client1 := scoped("alice", "c1000000-0000-4000-8000-000000000001")
	client2 := scoped("bob", "c2000000-0000-4000-8000-000000000002")
	orgWide := database.OrgWideScope("org-1", "carol")
	ctx1 := database.ContextWithTenantScope(context.Background(), client1)
	bulk, _, err := f.svc.Enqueue(ctx1, "org-1", "ci.bulk", nil, EnqueueOptions{CreatedBy: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	merge, _, err := f.svc.Enqueue(ctx1, "org-1", "ci.merge", nil, EnqueueOptions{CreatedBy: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	f.finish(t, merge, nil)
	system := f.enqueue(t, "system.index", "", "")
	if bulk.Scope == nil || len(bulk.Scope.Clients) != 1 || bulk.Scope.Sites != nil || bulk.Scope.Teams != nil || system.Scope != nil {
		t.Fatalf("scope snapshots: bulk %+v, system %+v", bulk.Scope, system.Scope)
	}

	all := []identity.Permission{identity.PermCIWrite, identity.PermCIDelete}
	for _, c := range []struct {
		name  string
		scope *database.TenantScope
		user  string
		id    string
	}{
		{"client 2, bulk job of client 1", client2, "bob", bulk.ID},
		{"client 2, merge job of client 1", client2, "bob", merge.ID},
		{"client 1, system job", client1, "alice", system.ID},
		{"no tenant scope, bulk job", nil, "alice", bulk.ID},
	} {
		for _, req := range []struct{ method, path string }{
			{"GET", "/api/v1/jobs/" + c.id},
			{"GET", "/api/v1/jobs/" + c.id + "/result"},
			{"DELETE", "/api/v1/jobs/" + c.id},
			{"POST", "/api/v1/jobs/" + c.id + "/revert"},
		} {
			if w := f.callIn(c.scope, req.method, req.path, c.user, all...); w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), c.id) {
				t.Errorf("%s: %s %s = %d %s, want 404", c.name, req.method, req.path, w.Code, w.Body.String())
			}
		}
	}
	if j, _ := f.svc.Get(context.Background(), "org-1", bulk.ID); j.Status != StatusQueued {
		t.Errorf("the bulk job is %s after the refused cancels, want queued", j.Status)
	}
	for _, c := range []struct {
		name  string
		scope *database.TenantScope
		user  string
		ids   []string
	}{
		{"creator, client 1", client1, "alice", []string{bulk.ID, merge.ID}},
		{"org-wide", &orgWide, "carol", []string{bulk.ID, merge.ID, system.ID}},
	} {
		for _, id := range c.ids {
			if w := f.callIn(c.scope, "GET", "/api/v1/jobs/"+id, c.user); w.Code != http.StatusOK {
				t.Errorf("%s: GET /jobs/%s = %d, want 200", c.name, id, w.Code)
			}
		}
	}
	// Visible jobs keep the order 403 before the cancel, 409 before 403 on
	// a revert the kind does not define.
	if w := f.callIn(&orgWide, "DELETE", "/api/v1/jobs/"+bulk.ID, "carol"); w.Code != http.StatusForbidden {
		t.Errorf("org-wide stranger cancels: %d, want 403", w.Code)
	}
	if w := f.callIn(&orgWide, "POST", "/api/v1/jobs/"+bulk.ID+"/revert", "carol"); w.Code != http.StatusConflict {
		t.Errorf("org-wide stranger reverts a bulk job: %d, want 409", w.Code)
	}
	if w := f.callIn(client1, "DELETE", "/api/v1/jobs/"+bulk.ID, "alice"); w.Code != http.StatusOK {
		t.Errorf("creator cancels: %d %s", w.Code, w.Body.String())
	}
}

// TestJobVisibleTo pins the rule of job_isolation (migration 000154) in
// Job.VisibleTo per dimension: org-wide callers see every snapshot, a
// restricted caller only a snapshot completely in its scope (an empty one
// included), never a dimension that is org-wide in the snapshot; export
// jobs only their creator.
func TestJobVisibleTo(t *testing.T) {
	const c1, c2, s1 = "c1000000-0000-4000-8000-000000000001", "c2000000-0000-4000-8000-000000000002", "51000000-0000-4000-8000-000000000001"
	scope := func(clients, sites []string) *database.TenantScope {
		s := database.OrgWideScope("org-1", "alice")
		if clients != nil {
			s.Clients = database.ScopeIDs(clients...)
		}
		if sites != nil {
			s.Sites = database.ScopeIDs(sites...)
		}
		return &s
	}
	job := func(snapshot *Scope) *Job { return &Job{OrganizationID: "org-1", Kind: "ci.bulk", Scope: snapshot} }
	for _, c := range []struct {
		name   string
		job    *Job
		caller *database.TenantScope
		want   bool
	}{
		{"org-wide caller, no snapshot", job(nil), scope(nil, nil), true},
		{"client caller, no snapshot", job(nil), scope([]string{c1}, nil), false},
		{"client caller, own client", job(&Scope{Clients: []string{c1}}), scope([]string{c1}, nil), true},
		{"client caller, upper-case id", job(&Scope{Clients: []string{strings.ToUpper(c1)}}), scope([]string{c1}, nil), true},
		{"client caller, other client", job(&Scope{Clients: []string{c2}}), scope([]string{c1}, nil), false},
		{"client caller, two clients", job(&Scope{Clients: []string{c1, c2}}), scope([]string{c1}, nil), false},
		{"two clients caller, one client", job(&Scope{Clients: []string{c2}}), scope([]string{c1, c2}, nil), true},
		{"client caller, empty client snapshot", job(&Scope{Clients: []string{}}), scope([]string{c1}, nil), true},
		{"site caller, snapshot org-wide in sites", job(&Scope{Clients: []string{c1}}), scope(nil, []string{s1}), false},
		{"site caller, own site", job(&Scope{Sites: []string{s1}}), scope(nil, []string{s1}), true},
		{"other organization", &Job{OrganizationID: "org-2", Kind: "ci.bulk"}, scope(nil, nil), false},
		{"export job, creator", &Job{OrganizationID: "org-1", RefTable: "export_job", CreatedBy: "alice"}, scope(nil, nil), true},
		{"export job, other user", &Job{OrganizationID: "org-1", RefTable: "export_job", CreatedBy: "bob"}, scope(nil, nil), false},
		{"nil scope", job(nil), nil, false},
	} {
		if got := c.job.VisibleTo(c.caller); got != c.want {
			t.Errorf("%s: VisibleTo = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestJobMetrics covers the metrics of JOB-03: duration and failures per
// kind, backlog and the age of the oldest queued job.
func TestJobMetrics(t *testing.T) {
	f := newAPIFixture(t)
	repo := NewMemoryRepository()
	f.svc = NewService(repo)
	f.svc.Register(Kind{Name: "metric.kind", MaxAttempts: 1})
	before := testutil.CollectAndCount(JobDuration, "reticora_job_duration_seconds")
	ok := f.enqueue(t, "metric.kind", "", "")
	f.finish(t, ok, nil)
	failed := f.enqueue(t, "metric.kind", "", "")
	if _, err := f.svc.ClaimJob(context.Background(), "org-1", failed.ID); err != nil {
		t.Fatal(err)
	}
	failures := testutil.ToFloat64(JobFailures.WithLabelValues("metric.kind"))
	if _, err := f.svc.Fail(context.Background(), "org-1", failed.ID, errors.New("boom")); err != nil {
		t.Fatal(err)
	}
	if got := testutil.ToFloat64(JobFailures.WithLabelValues("metric.kind")); got != failures+1 {
		t.Errorf("failures %v, want %v", got, failures+1)
	}
	if after := testutil.CollectAndCount(JobDuration, "reticora_job_duration_seconds"); after < before+2 {
		t.Errorf("duration series %d, before %d", after, before)
	}
	f.enqueue(t, "metric.kind", "", "")
	f.enqueue(t, "metric.kind", "", "")
	SetBacklogSource(repo)
	defer SetBacklogSource(nil)
	reg := prometheus.NewRegistry()
	reg.MustRegister(newBacklogCollector())
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]float64{}
	for _, fam := range families {
		for _, m := range fam.GetMetric() {
			values[fam.GetName()] = m.GetGauge().GetValue()
		}
	}
	if values["reticora_job_backlog"] != 2 {
		t.Errorf("backlog %v", values)
	}
	if age := values["reticora_job_oldest_queued_age_seconds"]; age < 0 || age > time.Minute.Seconds() {
		t.Errorf("oldest queued age %v", age)
	}
}
