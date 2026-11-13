package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/form"
)

// Package limits of the workflow engine (WFL-08, WP-1112): plan defaults
// of Pro and Enterprise, 403 entitlement-limit above the active
// definitions and the form definitions, an override per organization, and
// runs above the hourly limit that wait instead of failing with one
// warning per hour.
//
// They run only with TEST_DATABASE_URL (scopetest.Seed) and TEST_NATS_URL.
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestWorkflowQuota|TestWorkflowDefinitionActivationOverLimit403|TestWorkflowInactiveDefinitionNotCounted|TestFormDefinitionOverLimit403|TestWorkflowRunsOverHourlyLimitAreDelayed|TestWorkflowRunQuotaHoldsUnderParallelClaims|TestSerialSweepSkipsQuotaDelayedHead|TestWorkflowRunQuotaWarningOncePerHour' ./internal/workflow/

// TestWorkflowQuotaPlanDefaults: Pro and Enterprise carry the workflow
// quotas, Essential has no workflows and no workflow quota.
func TestWorkflowQuotaPlanDefaults(t *testing.T) {
	for plan, want := range map[entitlement.Plan][3]int64{entitlement.PlanPro: {50, 2000, 50}, entitlement.PlanEnterprise: {500, 20000, 500}} {
		d := entitlement.PlanQuotaDefaults(plan)
		if d[entitlement.LimitMaxWorkflowDefinitions] != want[0] || d[entitlement.LimitMaxWorkflowRunsPerHour] != want[1] ||
			d[entitlement.LimitMaxFormDefinitions] != want[2] {
			t.Errorf("%s: %v, want %v", plan, d, want)
		}
	}
	for _, plan := range []entitlement.Plan{entitlement.PlanEssential, entitlement.PlanStandard} {
		if _, has := entitlement.PlanQuotaDefaults(plan)[entitlement.LimitMaxWorkflowDefinitions]; has {
			t.Errorf("%s has a workflow quota without workflows", plan)
		}
	}
	for _, q := range []string{entitlement.LimitMaxWorkflowDefinitions, entitlement.LimitMaxWorkflowRunsPerHour, entitlement.LimitMaxFormDefinitions} {
		if f, _ := entitlement.QuotaFeatureOf(q); f != entitlement.FeatureWorkflows {
			t.Errorf("%s on %q, want %s", q, f, entitlement.FeatureWorkflows)
		}
	}
}

// quotaEnv is the workflow and form API of organization A with the
// workflows feature of plan pro and the limits limits, enforced.
type quotaEnv struct {
	*workerEnv
	svc    *entitlement.Service
	router chi.Router
}

func newQuotaEnv(t *testing.T, limits map[string]int64, run bool) *quotaEnv {
	t.Helper()
	w := newWorkerEnv(t, 5, run)
	svc := entitlement.NewService(entitlement.NewPGRepository(w.f.App), entitlement.Options{Enforce: true})
	if _, err := svc.Grant(w.ctx, &entitlement.Entitlement{OrganizationID: w.f.OrgA, FeatureKey: entitlement.FeatureWorkflows,
		Plan: entitlement.PlanPro, Enabled: true, Limits: limits, Source: "manual", Origin: entitlement.OriginManual}); err != nil {
		t.Fatal(err)
	}
	quotas := NewQuotas(svc, w.repo, form.NewPGRepository(w.f.App))
	w.exec.WithRunQuota(quotas)
	router := chi.NewRouter()
	NewHandler(w.repo, w.exec).WithQuota(quotas).RegisterRoutes(router)
	form.NewHandler(form.NewPGRepository(w.f.App)).WithQuota(quotas).RegisterRoutes(router)
	return &quotaEnv{workerEnv: w, svc: svc, router: router}
}

func (e *quotaEnv) call(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body))).WithContext(e.ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

func (e *quotaEnv) createWorkflow(t *testing.T, active bool) *httptest.ResponseRecorder {
	t.Helper()
	return e.call(t, http.MethodPost, "/api/v1/workflows", fmt.Sprintf(`{"name":"wf-quota-%d","trigger":{"type":"manual"},"actions":[{"type":"noop"}],"active":%v}`,
		time.Now().UnixNano(), active))
}

// limited reports a 403 entitlement-limit answer.
func limited(rec *httptest.ResponseRecorder) bool {
	return rec.Code == http.StatusForbidden && bytes.Contains(rec.Body.Bytes(), []byte(api.ProblemEntitlementLimit))
}

// TestWorkflowDefinitionActivationOverLimit403: with max_workflow_definitions
// 1 a second active definition is refused, and so is switching an inactive
// one on.
func TestWorkflowDefinitionActivationOverLimit403(t *testing.T) {
	e := newQuotaEnv(t, map[string]int64{entitlement.LimitMaxWorkflowDefinitions: 1}, false)
	if rec := e.createWorkflow(t, true); rec.Code != http.StatusCreated {
		t.Fatalf("first active definition: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.createWorkflow(t, true); !limited(rec) {
		t.Errorf("second active definition: %d %s, want 403 entitlement-limit", rec.Code, rec.Body.String())
	}
	rec := e.createWorkflow(t, false)
	if rec.Code != http.StatusCreated {
		t.Fatalf("inactive definition: %d %s", rec.Code, rec.Body.String())
	}
	var def Definition
	if err := json.Unmarshal(rec.Body.Bytes(), &def); err != nil {
		t.Fatal(err)
	}
	if rec = e.call(t, http.MethodPatch, "/api/v1/workflows/"+def.ID, `{"active":true}`); !limited(rec) {
		t.Errorf("switching on over the limit: %d %s, want 403 entitlement-limit", rec.Code, rec.Body.String())
	}
	if got, _ := e.repo.GetDefinition(e.ctx, e.f.OrgA, def.ID); got == nil || got.Active {
		t.Errorf("definition after the refused switch-on: %+v, want inactive", got)
	}
}

// TestWorkflowInactiveDefinitionNotCounted: inactive definitions do not
// count against max_workflow_definitions.
func TestWorkflowInactiveDefinitionNotCounted(t *testing.T) {
	e := newQuotaEnv(t, map[string]int64{entitlement.LimitMaxWorkflowDefinitions: 1}, false)
	for range 3 {
		if rec := e.createWorkflow(t, false); rec.Code != http.StatusCreated {
			t.Fatalf("inactive definition: %d %s", rec.Code, rec.Body.String())
		}
	}
	if rec := e.createWorkflow(t, true); rec.Code != http.StatusCreated {
		t.Errorf("first active definition after three inactive ones: %d %s", rec.Code, rec.Body.String())
	}
}

// TestFormDefinitionOverLimit403: max_form_definitions 1 refuses the second
// form definition.
func TestFormDefinitionOverLimit403(t *testing.T) {
	e := newQuotaEnv(t, map[string]int64{entitlement.LimitMaxFormDefinitions: 1}, false)
	body := func() string {
		return fmt.Sprintf(`{"name":"form-quota-%d","schema":{"type":"object"}}`, time.Now().UnixNano())
	}
	if rec := e.call(t, http.MethodPost, "/api/v1/forms", body()); rec.Code != http.StatusCreated {
		t.Fatalf("first form: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.call(t, http.MethodPost, "/api/v1/forms", body()); !limited(rec) {
		t.Errorf("second form: %d %s, want 403 entitlement-limit", rec.Code, rec.Body.String())
	}
}

// TestWorkflowQuotaOverriddenPerOrganization: a value stored on the
// workflows row overrides the plan default; without one the default of
// the plan applies.
func TestWorkflowQuotaOverriddenPerOrganization(t *testing.T) {
	e := newQuotaEnv(t, map[string]int64{entitlement.LimitMaxWorkflowRunsPerHour: 7}, false)
	if q, err := e.svc.Quota(e.ctx, e.f.OrgA, entitlement.LimitMaxWorkflowRunsPerHour); err != nil || q == nil || *q != 7 {
		t.Errorf("stored runs per hour: %v %v, want 7", q, err)
	}
	if q, err := e.svc.Quota(e.ctx, e.f.OrgA, entitlement.LimitMaxWorkflowDefinitions); err != nil || q == nil || *q != 50 {
		t.Errorf("default active definitions of pro: %v %v, want 50", q, err)
	}
	if q, err := NewQuotas(e.svc, e.repo, nil).RunsPerHour(e.ctx, e.f.OrgA); err != nil || q == nil || *q != 7 {
		t.Errorf("RunsPerHour: %v %v, want 7", q, err)
	}
}

// TestWorkflowRunsOverHourlyLimitAreDelayed: limit 3, five runs: three run,
// two stay pending with next_attempt_at; none fails. Once the started runs
// left the hour, the delayed runs are executed.
func TestWorkflowRunsOverHourlyLimitAreDelayed(t *testing.T) {
	e := newQuotaEnv(t, map[string]int64{entitlement.LimitMaxWorkflowRunsPerHour: 3}, true)
	runs := make([]*Run, 5)
	for i := range runs {
		runs[i] = e.start(t, e.ctx, JSONMap{"type": "noop"})
		if runs[i].StartedAt.IsZero() == false {
			t.Fatalf("queued run %d has started_at %v before its claim", i, runs[i].StartedAt)
		}
		if _, err := e.exec.Execute(e.ctx, e.f.OrgA, nil, runs[i]); err != nil {
			t.Fatal(err)
		}
		// One after the other, so the order of the claims is the order
		// of the runs.
		if i < 3 {
			e.await(t, runs[i].ID)
		}
	}
	deadline := time.Now().Add(15 * time.Second)
	for _, r := range runs[3:] {
		for {
			got, err := e.repo.GetRun(e.ctx, e.f.OrgA, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status == StatusPending && got.NextAttemptAt != nil {
				if got.NextAttemptAt.Before(time.Now().Add(50 * time.Minute)) {
					t.Errorf("delayed run %s until %v, want about an hour", r.ID, got.NextAttemptAt)
				}
				break
			}
			if got.Status != StatusPending || time.Now().After(deadline) {
				t.Fatalf("run %s: %s next %v, want pending and delayed", r.ID, got.Status, got.NextAttemptAt)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	var started int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM workflow_run WHERE organization_id = $1 AND started_at IS NOT NULL`,
		e.f.OrgA).Scan(&started); err != nil || started != 3 {
		t.Errorf("started runs: %d %v, want 3", started, err)
	}
	// The hour has passed for the three: the delayed runs are executed.
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE workflow_run SET started_at = started_at - interval '2 hours'
		WHERE organization_id = $1 AND started_at IS NOT NULL`, e.f.OrgA); err != nil {
		t.Fatal(err)
	}
	for _, r := range runs[3:] {
		next, err := e.repo.RequeueRun(e.ctx, e.f.OrgA, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		e.exec.requeue(e.ctx, e.f.OrgA, next)
		if done := e.await(t, r.ID); done.Status != StatusSucceeded {
			t.Errorf("delayed run %s after the hour: %s %q, want succeeded", r.ID, done.Status, done.LastError)
		}
	}
}

// TestWorkflowRunQuotaWarningOncePerHour: two delayed runs in one hour
// write exactly one entitlement.warning with quota, limit and
// delayed_until.
func TestWorkflowRunQuotaWarningOncePerHour(t *testing.T) {
	e := newQuotaEnv(t, map[string]int64{entitlement.LimitMaxWorkflowRunsPerHour: 1}, true)
	first := e.start(t, e.ctx, JSONMap{"type": "noop"})
	if _, err := e.exec.Execute(e.ctx, e.f.OrgA, nil, first); err != nil {
		t.Fatal(err)
	}
	e.await(t, first.ID)
	for range 2 {
		r := e.start(t, e.ctx, JSONMap{"type": "noop"})
		if _, err := e.exec.Execute(e.ctx, e.f.OrgA, nil, r); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(15 * time.Second)
		for {
			got, err := e.repo.GetRun(e.ctx, e.f.OrgA, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.NextAttemptAt != nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("run %s not delayed: %s", r.ID, got.Status)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	rows, err := e.f.Admin.Query(context.Background(), `SELECT data FROM event_outbox WHERE organization_id = $1 AND type = 'entitlement.warning'`, e.f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var warnings []map[string]any
	for rows.Next() {
		var d map[string]any
		if err := rows.Scan(&d); err != nil {
			t.Fatal(err)
		}
		warnings = append(warnings, d)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings: %v, want one", warnings)
	}
	w := warnings[0]
	if w["feature"] != entitlement.FeatureWorkflows || w["quota"] != entitlement.LimitMaxWorkflowRunsPerHour || w["limit"] != 1.0 || w["delayed_until"] == nil {
		t.Errorf("warning %v", w)
	}
}

// TestWorkflowRunQuotaHoldsUnderParallelClaims: limit 3, five pending runs
// claimed by five workers at once: the count and the claim happen under
// the quota lock of the organization, so exactly three runs start and two
// are delayed (WP-1112 AK3).
func TestWorkflowRunQuotaHoldsUnderParallelClaims(t *testing.T) {
	e := newQuotaEnv(t, map[string]int64{entitlement.LimitMaxWorkflowRunsPerHour: 3}, false)
	runs := make([]*Run, 5)
	for i := range runs {
		runs[i] = e.start(t, e.ctx, JSONMap{"type": "noop"})
	}
	start := make(chan struct{})
	claims := make(chan RunClaim, len(runs))
	errs := make(chan error, len(runs))
	for _, r := range runs {
		go func(r *Run) {
			<-start
			c, err := e.repo.ClaimRunWithinQuota(e.ctx, e.f.OrgA, r.ID, r.Enqueues, 3)
			claims <- c
			errs <- err
		}(r)
	}
	close(start)
	claimed, delayed := 0, 0
	for range runs {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		c := <-claims
		if c.Claimed {
			claimed++
		}
		if c.Delayed {
			delayed++
			if c.Run == nil || c.Until.Before(time.Now().Add(50*time.Minute)) {
				t.Errorf("delayed claim %+v, want the run until about an hour ahead", c)
			}
		}
	}
	var started int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM workflow_run WHERE organization_id = $1 AND started_at IS NOT NULL`,
		e.f.OrgA).Scan(&started); err != nil {
		t.Fatal(err)
	}
	if claimed != 3 || delayed != 2 || started != 3 {
		t.Errorf("parallel claims: %d claimed, %d delayed, %d started; want 3, 2, 3", claimed, delayed, started)
	}
}

// TestSerialSweepSkipsQuotaDelayedHead: a run on a CI delayed by the hourly
// limit is the head of its serial key with next_attempt_at ahead; the
// serial sweep does not take it for stranded, its enqueues stay and the
// delay is counted once.
func TestSerialSweepSkipsQuotaDelayedHead(t *testing.T) {
	e := newQuotaEnv(t, map[string]int64{entitlement.LimitMaxWorkflowRunsPerHour: 1}, false)
	first := e.start(t, e.ctx, JSONMap{"type": "noop"})
	if c, err := e.exec.claimRun(e.ctx, e.f.OrgA, first, first.Enqueues); err != nil || !c.Claimed {
		t.Fatalf("first claim: %+v %v", c, err)
	}
	ciID := e.f.CI(t, e.f.OrgA, "", "wf-quota-serial")
	head := e.startOn(t, ciID, nil, JSONMap{"type": "noop"})
	before := testutil.ToFloat64(runsDelayed)
	c, err := e.exec.claimRun(e.ctx, e.f.OrgA, head, head.Enqueues)
	if err != nil || !c.Delayed {
		t.Fatalf("claim at the limit: %+v %v, want delayed", c, err)
	}
	e.backdate(t, head.ID, 10*time.Minute)
	keys, err := e.repo.StrandedSerialKeys(e.ctx, e.f.OrgA, serialSweepAge)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(keys, head.SerialKey) {
		t.Errorf("stranded keys %v contain the delayed head %s", keys, head.SerialKey)
	}
	if _, err = NewSerialSweep(e.exec, func(context.Context) ([]string, error) { return []string{e.f.OrgA}, nil }).Run(e.ctx); err != nil {
		t.Fatal(err)
	}
	got, err := e.repo.GetRun(e.ctx, e.f.OrgA, head.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enqueues != c.Run.Enqueues || got.Status != StatusPending {
		t.Errorf("delayed head after the sweep: %s enqueues %d, want pending with %d", got.Status, got.Enqueues, c.Run.Enqueues)
	}
	if d := testutil.ToFloat64(runsDelayed) - before; d != 1 {
		t.Errorf("reticora_workflow_runs_delayed_total rose by %v, want 1", d)
	}
}
