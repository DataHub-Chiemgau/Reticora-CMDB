package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Schedule triggers on PostgreSQL (JOB-02 [P3], WFL-02, WP-1091): the start
// of a slot is idempotent through uq_workflow_run_schedule_slot, cron and
// time zone are checked when a definition is written, and an organization
// without the feature workflows gets no scheduled run.
//
// They run only with TEST_DATABASE_URL (scopetest.Seed).
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestTwoLeadersSameSlotOneRun|TestInvalidCron422|TestInvalidTimeZone422|TestNoScheduleRunWithoutFeature|TestScheduleTriggerRequiresOrgWideScope|TestScheduleChangeCatchesUpNoEarlierSlot|TestScheduledRunWithoutJobIsQueuedAgain' ./internal/workflow/

var pageAll = api.PaginationParams{Limit: 100}

type scheduleEnv struct {
	f      *scopetest.Fixture
	repo   *PGRepository
	exec   *Executor
	router chi.Router
	ctx    context.Context
}

func newScheduleEnv(t *testing.T) *scheduleEnv {
	t.Helper()
	f := scopetest.Seed(t, "3f")
	user := f.AppUser(t, f.OrgA, "wf-schedule")
	scope := database.OrgWideScope(f.OrgA, user)
	e := &scheduleEnv{f: f, repo: NewPGRepository(f.App),
		ctx: tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})}
	e.exec = NewExecutor(e.repo, nil, nil, nil, nil)
	e.router = chi.NewRouter()
	NewHandler(e.repo, e.exec).RegisterRoutes(e.router)
	return e
}

// scheduler returns a scheduler of organization A at now; enabled tells
// whether A has the feature workflows.
func (e *scheduleEnv) scheduler(now time.Time, enabled bool) *Scheduler {
	s := NewScheduler(e.exec, e.repo, func(context.Context) ([]string, error) { return []string{e.f.OrgA}, nil },
		func(context.Context, string) bool { return enabled })
	s.now = func() time.Time { return now }
	return s
}

// definition stores an active schedule definition with a noop action.
func (e *scheduleEnv) definition(t *testing.T, trigger JSONMap) *Definition {
	t.Helper()
	def := &Definition{OrganizationID: e.f.OrgA, Name: fmt.Sprintf("wf-schedule-%d", time.Now().UnixNano()), Trigger: trigger,
		Actions: []JSONMap{{"type": "noop"}}, Active: true}
	if err := e.repo.CreateDefinition(e.ctx, def); err != nil {
		t.Fatal(err)
	}
	return def
}

func (e *scheduleEnv) runsOf(t *testing.T, workflowID string) (n int, slots []time.Time) {
	t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT schedule_slot FROM workflow_run WHERE workflow_id = $1 AND schedule_slot IS NOT NULL ORDER BY schedule_slot`, workflowID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s time.Time
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		slots = append(slots, s.UTC())
	}
	return len(slots), slots
}

func (e *scheduleEnv) do(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(e.ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

func fieldOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Errors []struct {
			Field string `json:"field"`
		} `json:"errors"`
		Field string `json:"field"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if body.Field != "" {
		return body.Field
	}
	if len(body.Errors) > 0 {
		return body.Errors[0].Field
	}
	return ""
}

// TestTwoLeadersSameSlotOneRun: two scheduler leaders of the same slot (a
// change of the leader within the minute) store one run; the run carries
// trigger schedule and the slot in its context. A new version of the
// definition within the slot starts no second run, and the unique index
// refuses a second run of the slot.
func TestTwoLeadersSameSlotOneRun(t *testing.T) {
	e := newScheduleEnv(t)
	def := e.definition(t, JSONMap{"type": "schedule", "cron": "0 6 * * *", "timezone": "UTC"})
	now := time.Now().UTC().Add(25 * time.Hour)
	var wg sync.WaitGroup
	started := make([]int, 2)
	for i := range started {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := e.scheduler(now, true).Run(context.Background())
			if err != nil {
				t.Errorf("leader %d: %v", i, err)
			}
			started[i] = n
		}()
	}
	wg.Wait()
	if started[0]+started[1] != 1 {
		t.Errorf("runs started by the two leaders: %v, want one in total", started)
	}
	n, slots := e.runsOf(t, def.ID)
	if n != 1 || slots[0].Hour() != 6 || slots[0].Minute() != 0 || slots[0].After(now) {
		t.Fatalf("scheduled runs: %v, want one at 06:00 UTC before now", slots)
	}
	runs, _, err := e.repo.ListRuns(e.ctx, e.f.OrgA, &RunFilter{WorkflowID: def.ID}, pageAll)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs: %+v %v", runs, err)
	}
	run := runs[0]
	sched, _ := run.Context["schedule"].(map[string]any)
	if run.Trigger != TriggerSchedule || sched["slot"] != slots[0].Format(time.RFC3339) || sched["cron"] != "0 6 * * *" || run.Status != StatusSucceeded {
		t.Errorf("run: trigger %q status %q context %v, want schedule with slot and cron", run.Trigger, run.Status, run.Context)
	}

	// A new version within the slot: no second run.
	if _, err = e.repo.UpdateDefinition(e.ctx, e.f.OrgA, def.ID, &UpdateDefinitionRequest{Actions: []JSONMap{{"type": "noop"}, {"type": "noop"}}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.repo.GetDefinition(e.ctx, e.f.OrgA, def.ID); got != nil {
		def = got
	}
	if again, rerr := e.scheduler(now, true).Run(context.Background()); rerr != nil || again != 0 {
		t.Errorf("after a new version in the slot: %d %v, want no run", again, rerr)
	}
	dup := &Run{OrganizationID: e.f.OrgA, WorkflowID: def.ID, DefinitionVersionID: def.CurrentVersionID, Status: StatusPending,
		Trigger: TriggerSchedule, Context: JSONMap{}, StartedAt: now}
	if created, err := e.repo.CreateScheduledRun(e.ctx, dup, slots[0]); err != nil || created {
		t.Errorf("second run of the slot: created %v %v, want refused", created, err)
	}
	if n, _ = e.runsOf(t, def.ID); n != 1 {
		t.Errorf("scheduled runs: %d, want 1", n)
	}
	// Manual starts of a schedule definition stay possible.
	if _, err := e.exec.Trigger(e.ctx, e.f.OrgA, def, "manual", JSONMap{}); err != nil {
		t.Errorf("manual start of a schedule definition: %v", err)
	}
}

// TestInvalidCron422: a schedule trigger without a valid cron expression,
// or with an interval below one minute, is 422 on trigger.cron, on create
// and on PATCH.
func TestInvalidCron422(t *testing.T) {
	e := newScheduleEnv(t)
	for _, cron := range []any{"", "0 6 * *", "70 * * * *", "@every 10s", 7} {
		rec := e.do(t, http.MethodPost, "/api/v1/workflows", map[string]any{"name": "wf-cron", "actions": []any{map[string]any{"type": "noop"}},
			"trigger": map[string]any{"type": "schedule", "cron": cron}})
		if rec.Code != http.StatusUnprocessableEntity || fieldOf(t, rec) != "trigger.cron" {
			t.Errorf("create with cron %v: %d %s, want 422 on trigger.cron", cron, rec.Code, rec.Body.String())
		}
	}
	def := e.definition(t, JSONMap{"type": "manual"})
	rec := e.do(t, http.MethodPatch, "/api/v1/workflows/"+def.ID, map[string]any{"trigger": map[string]any{"type": "schedule", "cron": "* * *"}})
	if rec.Code != http.StatusUnprocessableEntity || fieldOf(t, rec) != "trigger.cron" {
		t.Errorf("PATCH with an invalid cron: %d %s, want 422 on trigger.cron", rec.Code, rec.Body.String())
	}
	rec = e.do(t, http.MethodPost, "/api/v1/workflows", map[string]any{"name": "wf-cron-ok", "actions": []any{map[string]any{"type": "noop"}},
		"trigger": map[string]any{"type": "schedule", "cron": "*/5 * * * *"}})
	if rec.Code != http.StatusCreated {
		t.Errorf("create with a valid cron: %d %s, want 201", rec.Code, rec.Body.String())
	}
}

// TestInvalidTimeZone422: an unknown time zone is 422 on trigger.timezone.
func TestInvalidTimeZone422(t *testing.T) {
	e := newScheduleEnv(t)
	for _, zone := range []any{"Mars/Olympus", "Local", "", 3} {
		rec := e.do(t, http.MethodPost, "/api/v1/workflows", map[string]any{"name": "wf-zone", "actions": []any{map[string]any{"type": "noop"}},
			"trigger": map[string]any{"type": "schedule", "cron": "0 6 * * *", "timezone": zone}})
		if rec.Code != http.StatusUnprocessableEntity || fieldOf(t, rec) != "trigger.timezone" {
			t.Errorf("create with time zone %v: %d %s, want 422 on trigger.timezone", zone, rec.Code, rec.Body.String())
		}
	}
	rec := e.do(t, http.MethodPost, "/api/v1/workflows", map[string]any{"name": "wf-zone-ok", "actions": []any{map[string]any{"type": "noop"}},
		"trigger": map[string]any{"type": "schedule", "cron": "0 6 * * *", "timezone": "America/New_York"}})
	if rec.Code != http.StatusCreated {
		t.Errorf("create with a valid time zone: %d %s, want 201", rec.Code, rec.Body.String())
	}
}

// TestNoScheduleRunWithoutFeature: an organization without the feature
// workflows gets no scheduled run, with it the due slot runs.
func TestNoScheduleRunWithoutFeature(t *testing.T) {
	e := newScheduleEnv(t)
	def := e.definition(t, JSONMap{"type": "schedule", "cron": "*/5 * * * *"})
	now := time.Now().UTC().Add(time.Hour)
	if n, err := e.scheduler(now, false).Run(context.Background()); err != nil || n != 0 {
		t.Errorf("without the feature: %d %v, want no run", n, err)
	}
	if n, _ := e.runsOf(t, def.ID); n != 0 {
		t.Fatalf("scheduled runs without the feature: %d, want 0", n)
	}
	if n, err := e.scheduler(now, true).Run(context.Background()); err != nil || n != 1 {
		t.Errorf("with the feature: %d %v, want one run", n, err)
	}
	inactive := e.definition(t, JSONMap{"type": "schedule", "cron": "*/5 * * * *"})
	off := false
	if _, err := e.repo.UpdateDefinition(e.ctx, e.f.OrgA, inactive.ID, &UpdateDefinitionRequest{Active: &off}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.scheduler(now.Add(10*time.Minute), true).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.runsOf(t, inactive.ID); n != 0 {
		t.Errorf("runs of an inactive schedule: %d, want 0", n)
	}
}

// TestScheduleTriggerRequiresOrgWideScope: a principal restricted to a
// client writes no schedule trigger and changes no schedule definition
// (422 on trigger.type): the scheduler runs the actions organization-wide,
// so a schedule of client 1 could otherwise write to a CI of client 2. A
// manual definition it may still write.
func TestScheduleTriggerRequiresOrgWideScope(t *testing.T) {
	e := newScheduleEnv(t)
	scope := database.OrgWideScope(e.f.OrgA, e.f.User)
	scope.Clients = database.ScopeIDs(e.f.Client1)
	client1 := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope),
		tenant.TenantInfo{OrganizationID: e.f.OrgA, ClientID: e.f.Client1, UserID: e.f.User})
	ci2 := e.f.CI(t, e.f.OrgA, e.f.Client2, "wf-schedule-ci-client2")
	isScopeRefusal := func(err error) bool {
		var fe *api.FieldError
		return errors.As(err, &fe) && fe.Field == "trigger.type" && fe.Status == http.StatusUnprocessableEntity
	}
	scheduled := &Definition{OrganizationID: e.f.OrgA, Name: fmt.Sprintf("wf-schedule-c1-%d", time.Now().UnixNano()),
		Trigger: JSONMap{"type": "schedule", "cron": "* * * * *"}, Active: true,
		Actions: []JSONMap{{"type": "set_ci_field", "ci_id": ci2, "field": "status", "value": "maintenance"}}}
	if err := e.repo.CreateDefinition(client1, scheduled); !isScopeRefusal(err) {
		t.Fatalf("schedule of client 1: %v, want 422 on trigger.type", err)
	}
	manual := &Definition{OrganizationID: e.f.OrgA, Name: fmt.Sprintf("wf-schedule-c1m-%d", time.Now().UnixNano()),
		Trigger: JSONMap{"type": "manual"}, Actions: []JSONMap{{"type": "noop"}}, Active: true}
	if err := e.repo.CreateDefinition(client1, manual); err != nil {
		t.Fatalf("manual definition of client 1: %v", err)
	}
	toSchedule := JSONMap{"type": "schedule", "cron": "* * * * *"}
	if _, err := e.repo.UpdateDefinition(client1, e.f.OrgA, manual.ID, &UpdateDefinitionRequest{Trigger: toSchedule}); !isScopeRefusal(err) {
		t.Errorf("PATCH to a schedule by client 1: %v, want 422 on trigger.type", err)
	}
	orgDef := e.definition(t, JSONMap{"type": "schedule", "cron": "0 6 * * *"})
	actions := []JSONMap{{"type": "set_ci_field", "ci_id": ci2, "field": "status", "value": "maintenance"}}
	if _, err := e.repo.UpdateDefinition(client1, e.f.OrgA, orgDef.ID, &UpdateDefinitionRequest{Actions: actions}); !isScopeRefusal(err) {
		t.Errorf("PATCH of the actions of a schedule by client 1: %v, want 422 on trigger.type", err)
	}
	if _, err := e.scheduler(time.Now().UTC().Add(25*time.Hour), true).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT status FROM ci WHERE id = $1`, ci2).Scan(&status); err != nil || status != "active" {
		t.Errorf("CI of client 2: %q %v, want unchanged active", status, err)
	}
}

// TestScheduleChangeCatchesUpNoEarlierSlot: a definition whose last run
// held the slot of its old cron expression gets a new one; a slot of the
// new expression from before the change is not caught up.
func TestScheduleChangeCatchesUpNoEarlierSlot(t *testing.T) {
	e := newScheduleEnv(t)
	now := time.Now().UTC()
	oldSlot := now.Add(-3 * time.Hour).Truncate(time.Hour)
	newSlot := now.Add(-time.Hour).Truncate(time.Hour)
	def := e.definition(t, JSONMap{"type": "schedule", "cron": fmt.Sprintf("0 %d * * *", oldSlot.Hour()), "timezone": "UTC"})
	run := &Run{OrganizationID: e.f.OrgA, WorkflowID: def.ID, DefinitionVersionID: def.CurrentVersionID, Status: StatusSucceeded,
		Trigger: TriggerSchedule, Context: JSONMap{}, StartedAt: oldSlot}
	if created, err := e.repo.CreateScheduledRun(e.ctx, run, oldSlot); err != nil || !created {
		t.Fatalf("run of the old slot: %v %v", created, err)
	}
	trigger := JSONMap{"type": "schedule", "cron": fmt.Sprintf("0 %d * * *", newSlot.Hour()), "timezone": "UTC"}
	if _, err := e.repo.UpdateDefinition(e.ctx, e.f.OrgA, def.ID, &UpdateDefinitionRequest{Trigger: trigger}); err != nil {
		t.Fatal(err)
	}
	if n, err := e.scheduler(now.Add(time.Minute), true).Run(context.Background()); err != nil || n != 0 {
		t.Errorf("run after the change: %d %v, want no run for the slot %s before the change", n, err, newSlot)
	}
	if n, slots := e.runsOf(t, def.ID); n != 1 {
		t.Errorf("scheduled runs %v, want only the one of the old slot", slots)
	}
}

// TestScheduledRunWithoutJobIsQueuedAgain: a scheduled run stored without
// its job (the instance ended before the queue) is queued by the next
// scheduler run once it waited longer than five minutes, with the key of
// its first job and only once.
func TestScheduledRunWithoutJobIsQueuedAgain(t *testing.T) {
	e := newWorkerEnv(t, 5, false)
	def := &Definition{OrganizationID: e.f.OrgA, Name: fmt.Sprintf("wf-schedule-stranded-%d", time.Now().UnixNano()),
		Trigger: JSONMap{"type": "schedule", "cron": "0 0 1 1 *"}, Actions: []JSONMap{{"type": "noop"}}, Active: true}
	if err := e.repo.CreateDefinition(e.ctx, def); err != nil {
		t.Fatal(err)
	}
	slot := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Minute)
	run := &Run{OrganizationID: e.f.OrgA, WorkflowID: def.ID, DefinitionVersionID: def.CurrentVersionID, Status: StatusPending,
		Trigger: TriggerSchedule, Context: JSONMap{}, StartedAt: slot}
	if created, err := e.repo.CreateScheduledRun(e.ctx, run, slot); err != nil || !created {
		t.Fatalf("scheduled run: %v %v", created, err)
	}
	s := NewScheduler(e.exec, e.repo, func(context.Context) ([]string, error) { return []string{e.f.OrgA}, nil },
		func(context.Context, string) bool { return true })
	countJobs := func() int {
		var n int
		if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM job WHERE organization_id = $1 AND idempotency_key = $2`,
			e.f.OrgA, RunJobKey(run.ID, 0)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if _, err := s.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := countJobs(); n != 0 {
		t.Fatalf("jobs of a run that waited less than five minutes: %d, want 0", n)
	}
	e.backdate(t, run.ID, 6*time.Minute)
	for range 2 {
		if _, err := s.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if n := countJobs(); n != 1 {
		t.Errorf("jobs of the stranded run: %d, want 1", n)
	}
}
