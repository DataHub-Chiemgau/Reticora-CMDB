package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Schedule triggers of workflows (JOB-02 [P3], WFL-02, WP-1091): a
// definition with trigger {type: "schedule", cron, timezone?} starts one run
// per slot of its cron expression, read in its time zone (without one the
// organization's, without that Europe/Berlin). The system job
// workflow.schedule runs every minute on one instance (the scheduler's
// advisory lock); the start of a slot is idempotent through the unique
// index (organization_id, workflow_id, schedule_slot), so a second leader
// of the same slot stores no second run. Catch-up is limited: per
// definition only the newest missed slot runs, older ones are counted in
// reticora_workflow_schedule_skipped_total.

// ScheduleKind is the system job that starts the runs of the schedule
// triggers.
const ScheduleKind = "workflow.schedule"

// TriggerSchedule is the trigger type and the run trigger of a schedule.
const TriggerSchedule = "schedule"

// defaultScheduleZone is the time zone of a schedule without one of its
// own when the organization has none either.
const defaultScheduleZone = "Europe/Berlin"

// scheduleLookback bounds how far back the scheduler looks for missed
// slots (and counts the skipped ones).
const scheduleLookback = 31 * 24 * time.Hour

// minScheduleInterval is the shortest interval of an @every schedule.
const minScheduleInterval = time.Minute

// scheduleSkipped counts the missed slots the catch-up left out.
var scheduleSkipped = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "reticora_workflow_schedule_skipped_total",
	Help: "Missed slots of workflow schedules that were not run because only the newest missed slot is caught up.",
})

// ValidateSchedule checks the trigger of a schedule definition before it
// is written (WP-1091 AK1): cron is a five-field cron expression or
// "@every <duration>" of at least one minute (422 on trigger.cron),
// timezone an IANA time zone (422 on trigger.timezone). Other triggers
// pass.
func ValidateSchedule(trigger JSONMap) error {
	if str(trigger, "type", "") != TriggerSchedule {
		return nil
	}
	expr, _ := trigger["cron"].(string)
	if _, err := parseSchedule(expr); err != nil {
		return &api.FieldError{Field: "trigger.cron", Status: http.StatusUnprocessableEntity,
			Message: "must be a five-field cron expression or @every with at least one minute"}
	}
	if raw, has := trigger["timezone"]; has && raw != nil {
		zone, ok := raw.(string)
		if _, err := loadZone(zone); !ok || err != nil {
			return &api.FieldError{Field: "trigger.timezone", Status: http.StatusUnprocessableEntity,
				Message: "must be an IANA time zone"}
		}
	}
	return nil
}

var errScheduleInterval = errors.New("schedule interval below one minute")

// errScheduleScope refuses a schedule trigger written by a principal whose
// scope is not organization-wide (422 on trigger.type): the scheduler
// starts the runs without an initiator in the scope of the organization,
// so a principal restricted to a client or site could otherwise act on
// the objects of other clients through a schedule (TEN-04).
var errScheduleScope = &api.FieldError{Field: "trigger.type", Status: http.StatusUnprocessableEntity,
	Message: "a schedule trigger requires an organization-wide scope"}

// requireScheduleScope checks a write of a definition whose stored or new
// trigger is a schedule: the tenant scope of ctx must be organization-wide
// in clients, sites and teams. Without a scope in ctx (the memory
// repository of --no-db) nothing is restricted.
func requireScheduleScope(ctx context.Context, triggers ...JSONMap) error {
	schedule := false
	for _, trigger := range triggers {
		schedule = schedule || str(trigger, "type", "") == TriggerSchedule
	}
	if !schedule {
		return nil
	}
	scope, ok := database.TenantScopeFromContext(ctx)
	if ok && (!scope.Clients.IsAll() || !scope.Sites.IsAll() || !scope.Teams.IsAll()) {
		return errScheduleScope
	}
	return nil
}

// parseSchedule parses the cron expression of a schedule.
func parseSchedule(expr string) (jobs.Spec, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return jobs.Spec{}, errors.New("cron expression is required")
	}
	spec, err := jobs.ParseSpec(expr)
	if err != nil {
		return jobs.Spec{}, err
	}
	if rest, ok := strings.CutPrefix(expr, "@every "); ok {
		if d, derr := time.ParseDuration(strings.TrimSpace(rest)); derr != nil || d < minScheduleInterval {
			return jobs.Spec{}, errScheduleInterval
		}
	}
	return spec, nil
}

// loadZone loads an IANA time zone; "" and "Local" are none.
func loadZone(name string) (*time.Location, error) {
	if name == "" || name == "Local" {
		return nil, fmt.Errorf("invalid time zone %q", name)
	}
	return time.LoadLocation(name)
}

// scheduleZone is the time zone of a schedule: its own, else the
// organization's, else Europe/Berlin.
func scheduleZone(trigger JSONMap, orgZone string) *time.Location {
	for _, name := range []string{str(trigger, "timezone", ""), orgZone, defaultScheduleZone} {
		if loc, err := loadZone(name); err == nil {
			return loc
		}
	}
	return time.UTC
}

// dueSlot returns the newest slot of the schedule expr in (after, now] and
// how many older slots in that interval it leaves out (WP-1091 AK3). A
// cron expression is read on the wall clock of loc, so "0 6 * * *" is 06:00
// local time in summer and winter; a wall time the clock skips does not
// run twice. "@every" slots are multiples of the duration since the epoch.
func dueSlot(expr string, spec jobs.Spec, loc *time.Location, after, now time.Time) (slot time.Time, skipped int, ok bool) {
	count := 0
	if strings.HasPrefix(strings.TrimSpace(expr), "@every") {
		for t := spec.Next(after); !t.IsZero() && !t.After(now); t = spec.Next(t) {
			slot, count = t, count+1
		}
	} else {
		last := after
		w := wallClock(after, loc)
		for {
			w = spec.Next(w)
			if w.IsZero() {
				break
			}
			t := time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), 0, 0, loc)
			if t.After(now) {
				break
			}
			if !t.After(last) {
				continue
			}
			slot, last, count = t, t, count+1
		}
	}
	if count == 0 {
		return time.Time{}, 0, false
	}
	return slot.UTC(), count - 1, true
}

// wallClock is the wall clock of t in loc as a UTC time, the form
// jobs.Spec reads.
func wallClock(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(), l.Nanosecond(), time.UTC)
}

// ScheduleStore is what the scheduler reads and writes beside the
// repository: the active schedule definitions, the time zone of the
// organization, the last slot of a definition and the idempotent start of
// a slot.
type ScheduleStore interface {
	ScheduledDefinitions(ctx context.Context, orgID string) ([]Definition, error)
	OrganizationTimezone(ctx context.Context, orgID string) (string, error)
	// LastScheduleSlot returns the newest slot a run of the definition
	// was started for.
	LastScheduleSlot(ctx context.Context, orgID, workflowID string) (time.Time, bool, error)
	// CreateScheduledRun stores run for slot unless a run of the
	// definition holds the slot already (created false).
	CreateScheduledRun(ctx context.Context, run *Run, slot time.Time) (bool, error)
	// StrandedScheduledRuns lists the pending scheduled runs unchanged for
	// longer than age that have no job workflow.run of their enqueue.
	StrandedScheduledRuns(ctx context.Context, orgID string, age time.Duration) ([]Run, error)
}

// Scheduler starts the runs of the schedule triggers (the system job
// workflow.schedule).
type Scheduler struct {
	exec    *Executor
	store   ScheduleStore
	orgs    func(ctx context.Context) ([]string, error)
	enabled func(ctx context.Context, orgID string) bool
	now     func() time.Time
}

// NewScheduler returns the scheduler of the runs of exec on store in the
// organizations orgs lists that have the workflow feature (enabled).
func NewScheduler(exec *Executor, store ScheduleStore, orgs func(ctx context.Context) ([]string, error),
	enabled func(ctx context.Context, orgID string) bool) *Scheduler {
	return &Scheduler{exec: exec, store: store, orgs: orgs, enabled: enabled, now: time.Now}
}

// Schedule is the registration with the scheduler: every minute, at most
// one run caught up, one minute timeout (WP-1091 AK2).
func (s *Scheduler) Schedule() jobs.Schedule {
	return jobs.Schedule{Kind: ScheduleKind, Spec: "* * * * *", MaxCatchUp: 1, Timeout: time.Minute,
		Run: func(ctx context.Context) error {
			started, err := s.Run(ctx)
			if started > 0 {
				slog.InfoContext(ctx, "scheduled workflow runs started", "runs", started)
			}
			return err
		}}
}

// Run starts the due slot of every active schedule definition of every
// organization with the workflow feature and returns the runs it started.
// Each organization runs on its own; a failing one does not stop the
// others.
func (s *Scheduler) Run(ctx context.Context) (int, error) {
	orgs, err := s.orgs(ctx)
	if err != nil {
		return 0, fmt.Errorf("%s: list organizations: %w", ScheduleKind, err)
	}
	now := s.now().UTC()
	total := 0
	var errs []error
	for _, orgID := range orgs {
		scope := database.OrgWideScope(orgID, "")
		octx := tenant.WithTenant(database.ContextWithTenantScope(ctx, &scope), tenant.TenantInfo{OrganizationID: orgID})
		if s.enabled == nil || !s.enabled(octx, orgID) {
			continue
		}
		n, oerr := s.runOrganization(octx, orgID, now)
		total += n
		if oerr != nil {
			errs = append(errs, fmt.Errorf("%s: organization %s: %w", ScheduleKind, orgID, oerr))
		}
	}
	return total, errors.Join(errs...)
}

func (s *Scheduler) runOrganization(ctx context.Context, orgID string, now time.Time) (int, error) {
	defs, err := s.store.ScheduledDefinitions(ctx, orgID)
	if err != nil || len(defs) == 0 {
		return 0, err
	}
	orgZone, err := s.store.OrganizationTimezone(ctx, orgID)
	if err != nil {
		return 0, err
	}
	started := 0
	var errs []error
	// A scheduled run stored without its job (the instance ended between
	// the insert and the queue, or the queue refused it) is queued again
	// once it waited longer than serialSweepAge; the job key keeps it to
	// one job.
	stranded, err := s.store.StrandedScheduledRuns(ctx, orgID, serialSweepAge)
	if err != nil {
		errs = append(errs, err)
	}
	for i := range stranded {
		if _, xerr := s.exec.Execute(ctx, orgID, nil, &stranded[i]); xerr != nil {
			errs = append(errs, fmt.Errorf("queue stranded scheduled run %s: %w", stranded[i].ID, xerr))
		}
	}
	for i := range defs {
		ok, derr := s.startDue(ctx, orgID, &defs[i], orgZone, now)
		if derr != nil {
			errs = append(errs, derr)
		}
		if ok {
			started++
		}
	}
	return started, errors.Join(errs...)
}

// startDue starts the newest due slot of def, if any, and reports whether
// it stored a run.
func (s *Scheduler) startDue(ctx context.Context, orgID string, def *Definition, orgZone string, now time.Time) (bool, error) {
	expr, _ := def.Trigger["cron"].(string)
	spec, err := parseSchedule(expr)
	if err != nil {
		// Written before the check of WP-1091: never started.
		slog.WarnContext(ctx, "workflow schedule with an invalid cron expression", "workflow_id", def.ID)
		return false, nil
	}
	last, has, err := s.store.LastScheduleSlot(ctx, orgID, def.ID)
	if err != nil {
		return false, err
	}
	// The schedule applies from the version that sets it and from the last
	// change of the definition (an activation among them): a new cron
	// expression or time zone, or a definition switched on again, catches
	// up no slot from before the change.
	v, err := s.exec.repo.GetVersionByID(ctx, orgID, def.CurrentVersionID)
	if err != nil {
		return false, err
	}
	after := v.CreatedAt
	if def.UpdatedAt.After(after) {
		after = def.UpdatedAt
	}
	if has && last.After(after) {
		after = last
	}
	if floor := now.Add(-scheduleLookback); after.Before(floor) {
		after = floor
	}
	slot, skipped, due := dueSlot(expr, spec, scheduleZone(def.Trigger, orgZone), after, now)
	if !due {
		return false, nil
	}
	if skipped > 0 {
		scheduleSkipped.Add(float64(skipped))
	}
	run := &Run{OrganizationID: orgID, WorkflowID: def.ID, DefinitionVersionID: def.CurrentVersionID, Status: StatusPending,
		Trigger: TriggerSchedule, StartedAt: now,
		Context: JSONMap{"schedule": map[string]any{"slot": slot.Format(time.RFC3339), "cron": expr}}}
	created, err := s.store.CreateScheduledRun(ctx, run, slot)
	if err != nil || !created {
		return false, err
	}
	if _, err = s.exec.Execute(ctx, orgID, def, run); err != nil {
		return true, fmt.Errorf("execute scheduled run %s: %w", run.ID, err)
	}
	return true, nil
}

// ScheduledDefinitions implements ScheduleStore: the active definitions
// whose trigger is a schedule.
func (r *PGRepository) ScheduledDefinitions(ctx context.Context, orgID string) ([]Definition, error) {
	out := []Definition{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT "+defCols+" FROM workflow_def WHERE organization_id=$1 AND active AND trigger->>'type' = $2 ORDER BY id", orgID, TriggerSchedule)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDef(rows)
			if err != nil {
				return err
			}
			out = append(out, *d)
		}
		return rows.Err()
	})
	return out, err
}

// OrganizationTimezone implements ScheduleStore.
func (r *PGRepository) OrganizationTimezone(ctx context.Context, orgID string) (string, error) {
	var zone string
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, "SELECT COALESCE(timezone, '') FROM organization WHERE id=$1", orgID).Scan(&zone)
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	})
	return zone, err
}

// LastScheduleSlot implements ScheduleStore.
func (r *PGRepository) LastScheduleSlot(ctx context.Context, orgID, workflowID string) (time.Time, bool, error) {
	var slot *time.Time
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT max(schedule_slot) FROM workflow_run WHERE organization_id=$1 AND workflow_id=$2", orgID, workflowID).Scan(&slot)
	})
	if err != nil || slot == nil {
		return time.Time{}, false, err
	}
	return slot.UTC(), true, nil
}

// CreateScheduledRun implements ScheduleStore with INSERT … ON CONFLICT
// DO NOTHING on uq_workflow_run_schedule_slot (WP-1091 AK3).
func (r *PGRepository) CreateScheduledRun(ctx context.Context, run *Run, slot time.Time) (bool, error) {
	created := false
	err := database.WithRequestTenant(ctx, r.pool, run.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO workflow_run (organization_id, workflow_id, definition_version_id, status, trigger, context, started_at, schedule_slot)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (organization_id, workflow_id, schedule_slot) WHERE schedule_slot IS NOT NULL DO NOTHING
			RETURNING id::text, created_at, updated_at`,
			run.OrganizationID, run.WorkflowID, run.DefinitionVersionID, run.Status, run.Trigger, run.Context, run.StartedAt, slot).
			Scan(&run.ID, &run.CreatedAt, &run.UpdatedAt)
		if err == pgx.ErrNoRows {
			return nil
		}
		created = err == nil
		return err
	})
	return created, err
}

// StrandedScheduledRuns implements ScheduleStore; the age is measured on
// the clock of the database, at most 100 runs per call.
func (r *PGRepository) StrandedScheduledRuns(ctx context.Context, orgID string, age time.Duration) ([]Run, error) {
	out := []Run{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+runCols+` FROM workflow_run WHERE organization_id=$1 AND schedule_slot IS NOT NULL
			AND status='pending' AND updated_at < now() - make_interval(secs => $2)
			AND NOT EXISTS (SELECT 1 FROM job j WHERE j.organization_id=$1 AND j.kind=$3
				AND j.idempotency_key = 'workflow.run:' || workflow_run.id::text || ':' || workflow_run.enqueues::text)
			ORDER BY created_at, id LIMIT 100`, orgID, age.Seconds(), RunJobKind)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			run, err := scanRun(rows)
			if err != nil {
				return err
			}
			out = append(out, *run)
		}
		return rows.Err()
	})
	return out, err
}

// StrandedScheduledRuns implements ScheduleStore: the memory repository
// keeps no jobs, its runs execute in the request.
func (r *MemoryRepository) StrandedScheduledRuns(context.Context, string, time.Duration) ([]Run, error) {
	return nil, nil
}

// ScheduledDefinitions implements ScheduleStore.
func (r *MemoryRepository) ScheduledDefinitions(_ context.Context, orgID string) ([]Definition, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []Definition{}
	for _, d := range r.definitions {
		if d.OrganizationID == orgID && d.Active && str(d.Trigger, "type", "") == TriggerSchedule {
			out = append(out, *d)
		}
	}
	return out, nil
}

// OrganizationTimezone implements ScheduleStore: the memory repository
// knows no organization, the default zone applies.
func (r *MemoryRepository) OrganizationTimezone(context.Context, string) (string, error) {
	return "", nil
}

// LastScheduleSlot implements ScheduleStore from schedule.slot of the
// runs' context.
func (r *MemoryRepository) LastScheduleSlot(_ context.Context, orgID, workflowID string) (time.Time, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var last time.Time
	for _, run := range r.runs {
		if slot, ok := memorySlot(run); ok && run.OrganizationID == orgID && run.WorkflowID == workflowID && slot.After(last) {
			last = slot
		}
	}
	return last, !last.IsZero(), nil
}

// CreateScheduledRun implements ScheduleStore: a run of the definition for
// the same slot already stored is a conflict.
func (r *MemoryRepository) CreateScheduledRun(ctx context.Context, run *Run, slot time.Time) (bool, error) {
	r.mu.RLock()
	for _, other := range r.runs {
		if s, ok := memorySlot(other); ok && other.OrganizationID == run.OrganizationID && other.WorkflowID == run.WorkflowID && s.Equal(slot) {
			r.mu.RUnlock()
			return false, nil
		}
	}
	r.mu.RUnlock()
	if err := r.CreateRun(ctx, run); err != nil {
		return false, err
	}
	return true, nil
}

// memorySlot is the schedule slot of a run in the memory repository.
func memorySlot(run *Run) (time.Time, bool) {
	if run.Trigger != TriggerSchedule {
		return time.Time{}, false
	}
	sched, _ := run.Context["schedule"].(map[string]any)
	raw, _ := sched["slot"].(string)
	t, err := time.Parse(time.RFC3339, raw)
	return t, err == nil
}
