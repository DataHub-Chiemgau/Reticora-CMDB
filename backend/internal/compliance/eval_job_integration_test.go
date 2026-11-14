package compliance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The compliance evaluation as a job and incrementally (SEC-03, WP-1126,
// E-237): all CIs in pages with one upsert each, status_changed_at only on
// a change of status, stale results removed after a complete run, one CI
// after its event, 403 for a restricted principal, the running job
// returned again, and the nightly job per organization.
//
// They run only with TEST_DATABASE_URL (scopetest.Seed).
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestEvaluationJob|TestIncrementalConsumerEvaluatesOneCI|TestEvaluationRequiresOrgWidePrincipal|TestEvaluationReturnsRunningJob|TestEvaluationSubmitsOneJobAtOnce|TestNightlyEvaluation' ./internal/compliance/

type evalEnv struct {
	f    *scopetest.Fixture
	ctx  context.Context
	repo *PGRepository
	eval *Evaluator
}

func newEvalEnv(t *testing.T, tag string) *evalEnv {
	t.Helper()
	f := scopetest.Seed(t, tag)
	scope := database.OrgWideScope(f.OrgA, "")
	ctx := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope), tenant.TenantInfo{OrganizationID: f.OrgA})
	repo := NewPGRepository(f.App)
	return &evalEnv{f: f, ctx: ctx, repo: repo, eval: NewEvaluator(repo, ci.NewPGRepository(f.App))}
}

// rule stores an active rule with expression.
func (e *evalEnv) rule(t *testing.T, name string, expression JSONMap) *Rule {
	t.Helper()
	r := &Rule{OrganizationID: e.f.OrgA, Name: fmt.Sprintf("%s-%d", name, time.Now().UnixNano()), Severity: "low", Category: "hygiene",
		Expression: expression, Active: true}
	if err := e.repo.CreateRule(e.ctx, r); err != nil {
		t.Fatal(err)
	}
	return r
}

// cis inserts n CIs of the organization.
func (e *evalEnv) cis(t *testing.T, n int, prefix string) {
	t.Helper()
	if _, err := e.f.Admin.Exec(context.Background(), `INSERT INTO ci (organization_id, ci_type_id, name, status)
		SELECT $1, (SELECT id FROM ci_type WHERE key = 'server' AND organization_id IS NULL), $2 || g, 'active' FROM generate_series(1, $3) g`,
		e.f.OrgA, prefix, n); err != nil {
		t.Fatal(err)
	}
}

func (e *evalEnv) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestEvaluationJobCoversAllCIs: 1,000 CIs × 3 rules give 3,000 results;
// the progress ends at 1,000 of 1,000.
func TestEvaluationJobCoversAllCIs(t *testing.T) {
	e := newEvalEnv(t, "c6")
	e.cis(t, 1000, "cmp-all-")
	for i := range 3 {
		e.rule(t, fmt.Sprintf("all-%d", i), JSONMap{"op": "not_empty", "field": "name"})
	}
	var pages []Progress
	final, err := e.eval.Run(e.ctx, e.f.OrgA, func(p Progress) error {
		pages = append(pages, p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if final.Processed != 1000 || final.Total != 1000 || len(pages) != 2 {
		t.Errorf("progress %+v after %d pages, want 1000/1000 after 2", final, len(pages))
	}
	if n := e.count(t, `SELECT count(*) FROM compliance_result WHERE organization_id = $1`, e.f.OrgA); n != 3000 {
		t.Errorf("results: %d, want 3000", n)
	}

	// A CI of the first page is deleted while the run goes on: the run
	// pages by cursor, no other CI moves past it, and the other 999 CIs
	// keep their results after the removal at the end.
	deleted := false
	if _, err = e.eval.Run(e.ctx, e.f.OrgA, func(p Progress) error {
		if !deleted {
			deleted = true
			_, derr := e.f.Admin.Exec(context.Background(), `UPDATE ci SET deleted_at = now() WHERE id = (SELECT id FROM ci
				WHERE organization_id = $1 AND deleted_at IS NULL ORDER BY created_at, id LIMIT 1)`, e.f.OrgA)
			return derr
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM compliance_result r JOIN ci c ON c.id = r.ci_id WHERE r.organization_id = $1
		AND c.deleted_at IS NULL`, e.f.OrgA); n != 2997 {
		t.Errorf("results of the CIs left after a deletion during the run: %d, want 2997", n)
	}

	// Through the job the progress stands on the job after each page as
	// processed/total, not only as a percentage.
	svc := NewJobService(jobs.NewPGRepository(e.f.App))
	rec := &recordingJobs{Service: svc}
	j := NewJobs(e.eval).WithJobs(rec).WithQueue(storeQueue{svc})
	job, err := j.Submit(e.ctx, e.f.OrgA, "")
	if err != nil {
		t.Fatal(err)
	}
	if job, err = svc.ClaimJob(e.ctx, e.f.OrgA, job.ID); err != nil || job == nil {
		t.Fatalf("claim the job: %+v %v", job, err)
	}
	if _, err = j.RunJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if len(rec.interim) != 2 || rec.interim[0].Processed != 500 || rec.interim[0].Total != 999 || rec.interim[1].Processed != 999 {
		t.Errorf("interim results on the job: %+v, want 500/999 and 999/999", rec.interim)
	}
}

// recordingJobs is the job service that reads the job row after every
// progress: the interim result stands on the job while it runs.
type recordingJobs struct {
	*jobs.Service
	interim []Progress
}

func (r *recordingJobs) ProgressResult(ctx context.Context, orgID, id string, percent int, interim any) error {
	if err := r.Service.ProgressResult(ctx, orgID, id, percent, interim); err != nil {
		return err
	}
	job, err := r.Get(ctx, orgID, id)
	if err != nil {
		return err
	}
	var p Progress
	if err := json.Unmarshal(job.Result, &p); err != nil || job.Status != jobs.StatusRunning || job.Progress != percent {
		return fmt.Errorf("job while running: %s %d%% %s %v", job.Status, job.Progress, job.Result, err)
	}
	r.interim = append(r.interim, p)
	return nil
}

// TestEvaluationJobUpsertKeepsStatusChangedAt: a second run without change
// keeps status_changed_at; a change of status sets a new one.
func TestEvaluationJobUpsertKeepsStatusChangedAt(t *testing.T) {
	e := newEvalEnv(t, "c6")
	device := e.f.CI(t, e.f.OrgA, "", "cmp-upsert")
	rule := e.rule(t, "status", JSONMap{"op": "eq", "field": "status", "value": "active"})
	read := func() (status string, changed, evaluated time.Time) {
		t.Helper()
		if err := e.f.Admin.QueryRow(context.Background(), `SELECT status, status_changed_at, evaluated_at FROM compliance_result
			WHERE rule_id = $1 AND ci_id = $2`, rule.ID, device).Scan(&status, &changed, &evaluated); err != nil {
			t.Fatal(err)
		}
		return status, changed, evaluated
	}
	if _, err := e.eval.Run(e.ctx, e.f.OrgA, nil); err != nil {
		t.Fatal(err)
	}
	s1, c1, ev1 := read()
	if _, err := e.eval.Run(e.ctx, e.f.OrgA, nil); err != nil {
		t.Fatal(err)
	}
	s2, c2, ev2 := read()
	if s1 != "pass" || s2 != "pass" || !c2.Equal(c1) || !ev2.After(ev1) {
		t.Errorf("unchanged run: %s→%s changed %v→%v evaluated %v→%v, want the same status_changed_at", s1, s2, c1, c2, ev1, ev2)
	}
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE ci SET status = 'maintenance' WHERE id = $1`, device); err != nil {
		t.Fatal(err)
	}
	if _, err := e.eval.Run(e.ctx, e.f.OrgA, nil); err != nil {
		t.Fatal(err)
	}
	if s3, c3, _ := read(); s3 != "fail" || !c3.After(c2) {
		t.Errorf("changed run: %s changed %v (before %v), want fail with a new status_changed_at", s3, c3, c2)
	}
	if n := e.count(t, `SELECT count(*) FROM compliance_result WHERE rule_id = $1 AND ci_id = $2`, rule.ID, device); n != 1 {
		t.Errorf("rows of rule and CI: %d, want 1", n)
	}
}

// TestEvaluationJobRemovesStaleResults: the results of a rule deactivated
// before a complete run are removed by it.
func TestEvaluationJobRemovesStaleResults(t *testing.T) {
	e := newEvalEnv(t, "c6")
	e.f.CI(t, e.f.OrgA, "", "cmp-stale")
	keep := e.rule(t, "keep", JSONMap{"op": "not_empty", "field": "name"})
	drop := e.rule(t, "drop", JSONMap{"op": "not_empty", "field": "name"})
	if _, err := e.eval.Run(e.ctx, e.f.OrgA, nil); err != nil {
		t.Fatal(err)
	}
	off := false
	if _, err := e.repo.UpdateRule(e.ctx, e.f.OrgA, drop.ID, &UpdateRuleRequest{Active: &off}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.eval.Run(e.ctx, e.f.OrgA, nil); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM compliance_result WHERE rule_id = $1`, drop.ID); n != 0 {
		t.Errorf("results of the deactivated rule: %d, want 0", n)
	}
	if n := e.count(t, `SELECT count(*) FROM compliance_result WHERE rule_id = $1`, keep.ID); n == 0 {
		t.Error("the results of the active rule are gone")
	}
}

// TestIncrementalConsumerEvaluatesOneCI: ci.changed evaluates only its CI;
// finding.created flips a rule on finding.open_count.vulnerability.
func TestIncrementalConsumerEvaluatesOneCI(t *testing.T) {
	e := newEvalEnv(t, "c6")
	one := e.f.CI(t, e.f.OrgA, "", "cmp-inc-1")
	other := e.f.CI(t, e.f.OrgA, "", "cmp-inc-2")
	rule := e.rule(t, "vuln", JSONMap{"op": "eq", "field": InputOpenCountPrefix + "vulnerability", "value": 0})
	if _, err := e.eval.Run(e.ctx, e.f.OrgA, nil); err != nil {
		t.Fatal(err)
	}
	evaluated := func(ciID string) (status string, at time.Time) {
		t.Helper()
		if err := e.f.Admin.QueryRow(context.Background(), `SELECT status, evaluated_at FROM compliance_result WHERE rule_id = $1 AND ci_id = $2`,
			rule.ID, ciID).Scan(&status, &at); err != nil {
			t.Fatal(err)
		}
		return status, at
	}
	_, oneBefore := evaluated(one)
	_, otherBefore := evaluated(other)
	consumer := NewIncrementalConsumer(e.eval, func(context.Context, string) bool { return true })
	if err := consumer.Consume(context.Background(), &events.Envelope{ID: "e1", Type: events.CIChanged, OrgID: e.f.OrgA, EntityID: one,
		EntitySeq: 2, Data: json.RawMessage(`{"id":"` + one + `"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, at := evaluated(one); !at.After(oneBefore) {
		t.Errorf("CI of the event: evaluated %v, before %v; want newer", at, oneBefore)
	}
	if _, at := evaluated(other); !at.Equal(otherBefore) {
		t.Errorf("other CI: evaluated %v, before %v; want unchanged", at, otherBefore)
	}
	if _, err := e.f.Admin.Exec(context.Background(), `INSERT INTO security_finding (organization_id, ci_id, kind, severity, title)
		VALUES ($1, $2, 'vulnerability', 'high', 'cmp-inc')`, e.f.OrgA, one); err != nil {
		t.Fatal(err)
	}
	if err := consumer.Consume(context.Background(), &events.Envelope{ID: "e2", Type: events.FindingCreated, OrgID: e.f.OrgA, EntityID: "f",
		EntitySeq: 1, Data: json.RawMessage(`{"id":"f","ci_id":"` + one + `","severity":"high"}`)}); err != nil {
		t.Fatal(err)
	}
	if status, _ := evaluated(one); status != "fail" {
		t.Errorf("rule after the finding: %s, want fail", status)
	}
	// Without the feature nothing is evaluated.
	off := NewIncrementalConsumer(e.eval, func(context.Context, string) bool { return false })
	_, before := evaluated(other)
	if err := off.Consume(context.Background(), &events.Envelope{ID: "e3", Type: events.CIChanged, OrgID: e.f.OrgA, EntityID: other, EntitySeq: 2,
		Data: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, at := evaluated(other); !at.Equal(before) {
		t.Error("an organization without the feature was evaluated")
	}
}

// storeQueue stores the submitted jobs without running them (a queue whose
// worker has not taken the job yet).
type storeQueue struct{ svc *jobs.Service }

func (q storeQueue) Submit(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error) {
	return q.svc.Enqueue(ctx, orgID, kind, params, opts)
}

func (e *evalEnv) router(j *Jobs) chi.Router {
	r := chi.NewRouter()
	NewHandler(e.repo, e.eval).WithJobs(j).RegisterRoutes(r)
	return r
}

func post(r chi.Router, ctx context.Context) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/compliance/evaluations", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// TestEvaluationRequiresOrgWidePrincipal: a principal of client 1 gets 403
// and no job exists.
func TestEvaluationRequiresOrgWidePrincipal(t *testing.T) {
	e := newEvalEnv(t, "c6")
	svc := jobs.NewService(jobs.NewPGRepository(e.f.App))
	svc.Register(EvaluateJobKindConfig)
	j := NewJobs(e.eval).WithJobs(svc).WithQueue(storeQueue{svc})
	client1 := tenant.WithTenant(e.f.ClientCtx(e.f.Client1), tenant.TenantInfo{OrganizationID: e.f.OrgA, ClientID: e.f.Client1})
	if rec := post(e.router(j), client1); rec.Code != http.StatusForbidden {
		t.Errorf("client-1 evaluation: %d %s, want 403", rec.Code, rec.Body.String())
	}
	if n := e.count(t, `SELECT count(*) FROM job WHERE organization_id = $1 AND kind = $2`, e.f.OrgA, EvaluateJobKind); n != 0 {
		t.Errorf("jobs after the refusal: %d, want 0", n)
	}
}

// TestEvaluationReturnsRunningJob: a second POST while the job is queued
// answers 202 with the same job.
func TestEvaluationReturnsRunningJob(t *testing.T) {
	e := newEvalEnv(t, "c6")
	svc := jobs.NewService(jobs.NewPGRepository(e.f.App))
	svc.Register(EvaluateJobKindConfig)
	j := NewJobs(e.eval).WithJobs(svc).WithQueue(storeQueue{svc})
	r := e.router(j)
	ids := make([]string, 2)
	for i := range ids {
		rec := post(r, e.ctx)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("evaluation %d: %d %s, want 202", i, rec.Code, rec.Body.String())
		}
		var job jobs.Job
		if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil || job.ID == "" || job.Kind != EvaluateJobKind {
			t.Fatalf("job %s: %v", rec.Body.String(), err)
		}
		ids[i] = job.ID
	}
	if ids[0] != ids[1] {
		t.Errorf("job ids %v, want the same job twice", ids)
	}
	// Run by the worker: all CIs are evaluated.
	e.f.CI(t, e.f.OrgA, "", "cmp-job")
	e.rule(t, "job", JSONMap{"op": "not_empty", "field": "name"})
	job, err := svc.Get(e.ctx, e.f.OrgA, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	out, err := j.RunJob(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := out.(Progress); p.Processed == 0 || p.Processed != p.Total {
		t.Errorf("job result %+v, want all CIs", out)
	}
}

// TestEvaluationSubmitsOneJobAtOnce: five submits at the same moment (two
// requests, a request and the nightly run) create one job; once it ended,
// the next submit creates a new one (AK1).
func TestEvaluationSubmitsOneJobAtOnce(t *testing.T) {
	e := newEvalEnv(t, "c6")
	svc := NewJobService(jobs.NewPGRepository(e.f.App))
	j := NewJobs(e.eval).WithJobs(svc).WithQueue(storeQueue{svc})
	ids := make(chan string, 5)
	start := make(chan struct{})
	for range 5 {
		go func() {
			<-start
			job, err := j.Submit(e.ctx, e.f.OrgA, "")
			if err != nil {
				t.Error(err)
				ids <- ""
				return
			}
			ids <- job.ID
		}()
	}
	close(start)
	first := <-ids
	for range 4 {
		if id := <-ids; id != first {
			t.Errorf("job ids %s and %s, want one job", first, id)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM job WHERE organization_id = $1 AND kind = $2`, e.f.OrgA, EvaluateJobKind); n != 1 {
		t.Fatalf("jobs: %d, want 1", n)
	}
	if _, err := svc.ClaimJob(e.ctx, e.f.OrgA, first); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Succeed(e.ctx, e.f.OrgA, first, Progress{}); err != nil {
		t.Fatal(err)
	}
	next, err := j.Submit(e.ctx, e.f.OrgA, "")
	if err != nil || next.ID == first {
		t.Errorf("submit after the end: %+v %v, want a new job", next, err)
	}
}

// TestNightlyEvaluationWithoutQueue: with the wiring of the system job
// without a job queue (NewJobService) the nightly run creates the job and
// runs it in the process.
func TestNightlyEvaluationWithoutQueue(t *testing.T) {
	e := newEvalEnv(t, "c6")
	e.rule(t, "nightly-local", JSONMap{"op": "not_empty", "field": "name"})
	e.f.CI(t, e.f.OrgA, "", "cmp-nightly-local")
	svc := NewJobService(jobs.NewPGRepository(e.f.App))
	nightly := NewNightly(NewJobs(e.eval).WithJobs(svc), e.repo, func(context.Context) ([]string, error) { return []string{e.f.OrgA}, nil },
		func(context.Context, string) bool { return true })
	queued, err := nightly.Run(context.Background())
	if err != nil || queued != 1 {
		t.Fatalf("nightly run without queue: %d %v, want 1", queued, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var status string
		if err := e.f.Admin.QueryRow(context.Background(), `SELECT status FROM job WHERE organization_id = $1 AND kind = $2`, e.f.OrgA, EvaluateJobKind).
			Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == string(jobs.StatusSucceeded) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job of the nightly run: %s, want succeeded", status)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n := e.count(t, `SELECT count(*) FROM compliance_result WHERE organization_id = $1`, e.f.OrgA); n == 0 {
		t.Error("no results after the nightly run without queue")
	}
}

// TestNightlyEvaluationSchedulesPerOrganization: the nightly run queues one
// evaluation for the organization with the feature and an active rule,
// none for an organization without the feature.
func TestNightlyEvaluationSchedulesPerOrganization(t *testing.T) {
	e := newEvalEnv(t, "c6")
	e.rule(t, "nightly", JSONMap{"op": "not_empty", "field": "name"})
	svc := jobs.NewService(jobs.NewPGRepository(e.f.App))
	svc.Register(EvaluateJobKindConfig)
	j := NewJobs(e.eval).WithJobs(svc).WithQueue(storeQueue{svc})
	nightly := NewNightly(j, e.repo, func(context.Context) ([]string, error) { return []string{e.f.OrgA, e.f.OrgB}, nil },
		func(_ context.Context, orgID string) bool { return orgID == e.f.OrgA })
	if s := nightly.Schedule(); s.Kind != NightlyKind || s.Spec != "30 2 * * *" || s.MaxCatchUp != 1 {
		t.Errorf("schedule %+v", s)
	}
	queued, err := nightly.Run(context.Background())
	if err != nil || queued != 1 {
		t.Fatalf("nightly run: %d %v, want 1", queued, err)
	}
	if n := e.count(t, `SELECT count(*) FROM job WHERE organization_id = $1 AND kind = $2 AND created_by IS NULL`, e.f.OrgA, EvaluateJobKind); n != 1 {
		t.Errorf("system jobs of A: %d, want 1", n)
	}
	if n := e.count(t, `SELECT count(*) FROM job WHERE organization_id = $1 AND kind = $2`, e.f.OrgB, EvaluateJobKind); n != 0 {
		t.Errorf("jobs of B: %d, want 0", n)
	}
}

// BenchmarkEvaluatePage measures one page of 500 CIs against 50 rules
// (E-237 budget: 100,000 CIs × 50 rules in 15 minutes, WP-1138 measures it
// end to end): the whole page on PostgreSQL, the CI read, the finding facts,
// the evaluation and the upsert of its 25,000 results. Without
// TEST_DATABASE_URL it is skipped; BenchmarkEvaluatePageInMemory measures
// the evaluation alone.
func BenchmarkEvaluatePage(b *testing.B) {
	f := scopetest.Seed(b, "c6")
	scope := database.OrgWideScope(f.OrgA, "")
	ctx := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope), tenant.TenantInfo{OrganizationID: f.OrgA})
	repo := NewPGRepository(f.App)
	cis := ci.NewPGRepository(f.App)
	if _, err := f.Admin.Exec(context.Background(), `INSERT INTO ci (organization_id, ci_type_id, name, status)
		SELECT $1, (SELECT id FROM ci_type WHERE key = 'server' AND organization_id IS NULL), 'bench-' || g, 'active' FROM generate_series(1, 500) g`,
		f.OrgA); err != nil {
		b.Fatal(err)
	}
	rules := make([]Rule, 50)
	for i := range rules {
		rules[i] = Rule{OrganizationID: f.OrgA, Name: fmt.Sprintf("bench-%d", i), Severity: "low", Category: "hygiene", Active: true,
			Expression: JSONMap{"op": "all", "rules": []any{map[string]any{"op": "not_empty", "field": "name"},
				map[string]any{"op": "eq", "field": "status", "value": "active"}}}}
		if err := repo.CreateRule(ctx, &rules[i]); err != nil {
			b.Fatal(err)
		}
	}
	e := NewEvaluator(repo, cis)
	filter := &ci.FilterParams{SortBy: "created_at", SortDir: "asc"}
	b.ResetTimer()
	for range b.N {
		items, _, err := cis.List(ctx, f.OrgA, filter, api.PaginationParams{Limit: evaluationPage})
		if err != nil {
			b.Fatal(err)
		}
		results, err := e.evaluatePage(ctx, f.OrgA, rules, items, time.Now())
		if err != nil {
			b.Fatal(err)
		}
		if err := repo.UpsertResults(ctx, f.OrgA, results); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkEvaluatePageInMemory measures the evaluation of one page of 500
// CIs against 50 rules without the database.
func BenchmarkEvaluatePageInMemory(b *testing.B) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	rules := make([]Rule, 50)
	for i := range rules {
		rules[i] = Rule{ID: fmt.Sprintf("r-%d", i), OrganizationID: "org-1", Active: true,
			Expression: JSONMap{"op": "all", "rules": []any{map[string]any{"op": "not_empty", "field": "name"},
				map[string]any{"op": "eq", "field": "status", "value": "active"}}}}
	}
	items := make([]ci.Item, 500)
	for i := range items {
		items[i] = ci.Item{ID: fmt.Sprintf("ci-%d", i), Name: fmt.Sprintf("srv-%d", i), Status: "active", CITypeID: "t",
			Attributes: map[string]any{"rack": "R1"}}
	}
	e := NewEvaluator(repo, nil)
	b.ResetTimer()
	for range b.N {
		if _, err := e.evaluatePage(ctx, "org-1", rules, items, time.Now()); err != nil {
			b.Fatal(err)
		}
	}
}
