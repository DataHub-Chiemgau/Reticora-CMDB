package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

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

// call runs a request as user with the given permissions.
func (f *apiFixture) call(method, path, user string, perms ...identity.Permission) *httptest.ResponseRecorder {
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1", UserID: user})
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
