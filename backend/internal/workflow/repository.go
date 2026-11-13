package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

var (
	// ErrNotFound: the workflow definition is unknown or of another
	// organization; both answer 404 alike (TEN-04).
	ErrNotFound = errors.New("workflow definition not found")
	// ErrRunNotFound: the run is unknown or of another organization (404).
	ErrRunNotFound = errors.New("workflow run not found")
	// ErrVersionNotFound: the definition has no such version, or the
	// definition is unknown or of another organization (404, TEN-04).
	ErrVersionNotFound = errors.New("workflow definition version not found")
	// ErrPreconditionFailed: If-Match names another version than the
	// current one of the definition (412, API-07).
	ErrPreconditionFailed = errors.New("the workflow definition has changed since the If-Match version")
	// ErrCINotFound: the CI of a run's context is unknown, of another
	// organization or outside the scope of the trigger; all answer 404
	// alike (TEN-04, WP-1089).
	ErrCINotFound = errors.New("the CI of the workflow run was not found")
	// ErrNoEventOutbox: the repository writes no event outbox (memory,
	// --no-db; WP-1096).
	ErrNoEventOutbox = errors.New("webhook events unavailable")
)

type Repository interface {
	ListDefinitions(ctx context.Context, orgID string, activeOnly bool, page api.PaginationParams) ([]Definition, int, error)
	GetDefinition(ctx context.Context, orgID, id string) (*Definition, error)
	CreateDefinition(ctx context.Context, def *Definition) error
	UpdateDefinition(ctx context.Context, orgID, id string, req *UpdateDefinitionRequest) (*Definition, error)
	DeleteDefinition(ctx context.Context, orgID, id string) error
	// ListVersions lists the versions of a definition, newest first
	// (ErrNotFound for an unknown definition).
	ListVersions(ctx context.Context, orgID, workflowID string, page api.PaginationParams) ([]DefinitionVersion, int, error)
	// GetVersion returns version number version of a definition.
	GetVersion(ctx context.Context, orgID, workflowID string, version int) (*DefinitionVersion, error)
	// GetVersionByID returns the version a run is bound to.
	GetVersionByID(ctx context.Context, orgID, id string) (*DefinitionVersion, error)
	ListRuns(ctx context.Context, orgID, workflowID, status string, page api.PaginationParams) ([]Run, int, error)
	GetRun(ctx context.Context, orgID, id string) (*Run, error)
	// GetRunForJob returns the run organization-wide, whatever the scope
	// of ctx: the worker closes a run whose CI left the scope of its job
	// (WP-1089).
	GetRunForJob(ctx context.Context, orgID, id string) (*Run, error)
	CreateRun(ctx context.Context, run *Run) error
	UpdateRunStatus(ctx context.Context, orgID, id, status string, finished *time.Time, runCtx JSONMap) (*Run, error)
	// RecordAttempt stores attempts, next_attempt_at and last_error of the
	// job attempt that executes the run (WP-1088).
	RecordAttempt(ctx context.Context, orgID, id string, a RunAttempt) error
	AppendStep(ctx context.Context, step *Step) error
	UpdateStep(ctx context.Context, orgID, id, status string, output JSONMap, errText string) (*Step, error)
	// AppendEvent sets step id succeeded with output and writes event to
	// the event outbox in the same transaction (database.BeforeCommit,
	// last before the commit, WP-1096); it returns the event id. A
	// rollback leaves the step and writes no event. A repository without
	// an outbox returns ErrNoEventOutbox.
	AppendEvent(ctx context.Context, orgID, id string, output JSONMap, event *events.Event) (string, error)
	ListSteps(ctx context.Context, orgID, runID string) ([]Step, error)
	// ClaimRun decides whether the job of enqueue may execute the run now
	// (WP-1089 AK2): under the advisory lock of the run's serial key, a
	// pending run is claimed only when it is the oldest pending run of the
	// key (entity_seq NULLS LAST, created_at, id) and no other run of the
	// key is running; it becomes running in the same transaction. A run
	// without a serial key is claimed at once, a running one (a retry of
	// its job) as well. The job of an older enqueue claims nothing.
	ClaimRun(ctx context.Context, orgID, id string, enqueue int) (RunClaim, error)
	// NextSerialRun picks under the advisory lock of key the oldest pending
	// run of key while no run of key is running, raises its enqueues and
	// calls then with it once the transaction has committed
	// (database.AfterCommit, WP-1089 AK3). Without such a run then is not
	// called.
	NextSerialRun(ctx context.Context, orgID, key string, then func(ctx context.Context, run *Run)) error
	// RequeueRun raises the enqueues of a pending run and returns it: its
	// next job is the only one that executes it.
	RequeueRun(ctx context.Context, orgID, id string) (*Run, error)
	// StrandedSerialKeys lists the serial keys with a pending run unchanged
	// for longer than age while no run of the key is running (the system
	// job workflow.serial_sweep, WP-1089 AK4).
	StrandedSerialKeys(ctx context.Context, orgID string, age time.Duration) ([]string, error)
	// RunJobOrigin returns the first job workflow.run of a run with its
	// creator and scope snapshot (TEN-09); nil when there is none.
	RunJobOrigin(ctx context.Context, orgID, runID string) (*jobs.Job, error)
}

type MemoryRepository struct {
	mu          sync.RWMutex
	definitions map[string]*Definition
	versions    map[string]*DefinitionVersion
	runs        map[string]*Run
	steps       map[string]*Step
	next        int
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{definitions: map[string]*Definition{}, versions: map[string]*DefinitionVersion{}, runs: map[string]*Run{}, steps: map[string]*Step{}}
}

// addVersionLocked stores the next version of d from its current trigger,
// conditions and actions and points d at it.
func (r *MemoryRepository) addVersionLocked(d *Definition, createdBy string, at time.Time) {
	v := &DefinitionVersion{
		ID: r.nextID("version"), OrganizationID: d.OrganizationID, WorkflowID: d.ID, Version: d.Version,
		Trigger: d.Trigger, Conditions: d.Conditions, Actions: d.Actions, CreatedBy: createdBy, CreatedAt: at,
	}
	r.versions[v.ID] = v
	d.CurrentVersionID = v.ID
}

// nextID returns ids of UUID form, as the handler accepts only UUID ids
// (API-03); definitions, runs and steps share the counter.
func (r *MemoryRepository) nextID(_ string) string {
	r.next++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", r.next)
}
func (r *MemoryRepository) ListDefinitions(_ context.Context, orgID string, activeOnly bool, page api.PaginationParams) ([]Definition, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []Definition{}
	for _, d := range r.definitions {
		if d.OrganizationID == orgID && (!activeOnly || d.Active) {
			out = append(out, *d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return pageDefs(out, page)
}
func (r *MemoryRepository) GetDefinition(_ context.Context, orgID, id string) (*Definition, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d := r.definitions[id]
	if d == nil || d.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	cp := *d
	return &cp, nil
}
func (r *MemoryRepository) CreateDefinition(ctx context.Context, def *Definition) error {
	if err := requireScheduleScope(ctx, def.Trigger); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	def.ID = r.nextID("workflow")
	now := time.Now().UTC()
	def.CreatedAt = now
	def.UpdatedAt = now
	normalizeDefinition(def)
	def.Version = 1
	r.addVersionLocked(def, createdBy(ctx), now)
	cp := *def
	r.definitions[def.ID] = &cp
	return nil
}
func (r *MemoryRepository) UpdateDefinition(ctx context.Context, orgID, id string, req *UpdateDefinitionRequest) (*Definition, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.definitions[id]
	if d == nil || d.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	if !ifMatches(req.IfMatch, d.Version) {
		return nil, ErrPreconditionFailed
	}
	next := *d
	applyUpdate(&next, req)
	if err := requireScheduleScope(ctx, d.Trigger, next.Trigger); err != nil {
		return nil, err
	}
	if err := validateEffective(d, &next); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if contentChanged(d, &next) {
		next.Version = d.Version + 1
		r.addVersionLocked(&next, req.CreatedBy, now)
	}
	next.UpdatedAt = now
	*d = next
	cp := *d
	return &cp, nil
}
func (r *MemoryRepository) DeleteDefinition(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.definitions[id]
	if d == nil || d.OrganizationID != orgID {
		return ErrNotFound
	}
	delete(r.definitions, id)
	// The versions and runs of the definition go with it (ON DELETE
	// CASCADE).
	for vid, v := range r.versions {
		if v.WorkflowID == id {
			delete(r.versions, vid)
		}
	}
	for rid, run := range r.runs {
		if run.WorkflowID == id {
			delete(r.runs, rid)
		}
	}
	return nil
}
func (r *MemoryRepository) ListVersions(_ context.Context, orgID, workflowID string, page api.PaginationParams) ([]DefinitionVersion, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if d := r.definitions[workflowID]; d == nil || d.OrganizationID != orgID {
		return nil, 0, ErrNotFound
	}
	out := []DefinitionVersion{}
	for _, v := range r.versions {
		if v.OrganizationID == orgID && v.WorkflowID == workflowID {
			out = append(out, *v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	total := len(out)
	start := min(page.Offset, total)
	end := min(start+page.Limit, total)
	return out[start:end], total, nil
}
func (r *MemoryRepository) GetVersion(_ context.Context, orgID, workflowID string, version int) (*DefinitionVersion, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, v := range r.versions {
		if v.OrganizationID == orgID && v.WorkflowID == workflowID && v.Version == version {
			cp := *v
			return &cp, nil
		}
	}
	return nil, ErrVersionNotFound
}
func (r *MemoryRepository) GetVersionByID(_ context.Context, orgID, id string) (*DefinitionVersion, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v := r.versions[id]
	if v == nil || v.OrganizationID != orgID {
		return nil, ErrVersionNotFound
	}
	cp := *v
	return &cp, nil
}
func (r *MemoryRepository) ListRuns(_ context.Context, orgID, workflowID, status string, page api.PaginationParams) ([]Run, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []Run{}
	for _, run := range r.runs {
		if run.OrganizationID == orgID && (workflowID == "" || run.WorkflowID == workflowID) && (status == "" || run.Status == status) {
			cp := *run
			cp.Steps = r.stepsForLocked(orgID, run.ID)
			out = append(out, cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return pageRuns(out, page)
}
func (r *MemoryRepository) GetRun(_ context.Context, orgID, id string) (*Run, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	run := r.runs[id]
	if run == nil || run.OrganizationID != orgID {
		return nil, ErrRunNotFound
	}
	cp := *run
	cp.Steps = r.stepsForLocked(orgID, id)
	return &cp, nil
}

// GetRunForJob implements Repository: the memory repository knows no
// scope.
func (r *MemoryRepository) GetRunForJob(ctx context.Context, orgID, id string) (*Run, error) {
	return r.GetRun(ctx, orgID, id)
}
func (r *MemoryRepository) CreateRun(_ context.Context, run *Run) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// A run is bound to a stored version of its definition (NOT NULL
	// foreign key in PostgreSQL, WFL-02).
	if v := r.versions[run.DefinitionVersionID]; v == nil || v.OrganizationID != run.OrganizationID || v.WorkflowID != run.WorkflowID {
		return fmt.Errorf("workflow run needs a version of its definition")
	}
	run.ID = r.nextID("run")
	run.SerialKey = serialKeyOf(run.CIID)
	now := time.Now().UTC()
	run.CreatedAt = now
	run.UpdatedAt = now
	if run.StartedAt.IsZero() {
		run.StartedAt = now
	}
	cp := *run
	r.runs[run.ID] = &cp
	return nil
}
func (r *MemoryRepository) UpdateRunStatus(_ context.Context, orgID, id, status string, finished *time.Time, runCtx JSONMap) (*Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run := r.runs[id]
	if run == nil || run.OrganizationID != orgID {
		return nil, ErrRunNotFound
	}
	run.Status = status
	run.FinishedAt = finished
	if runCtx != nil {
		run.Context = runCtx
	}
	run.UpdatedAt = time.Now().UTC()
	cp := *run
	cp.Steps = r.stepsForLocked(orgID, id)
	return &cp, nil
}
func (r *MemoryRepository) RecordAttempt(_ context.Context, orgID, id string, a RunAttempt) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	run := r.runs[id]
	if run == nil || run.OrganizationID != orgID {
		return ErrRunNotFound
	}
	run.Attempts, run.NextAttemptAt, run.LastError = a.Attempts, a.NextAttemptAt, a.LastError
	run.UpdatedAt = time.Now().UTC()
	return nil
}
func (r *MemoryRepository) AppendStep(_ context.Context, step *Step) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	step.ID = r.nextID("step")
	now := time.Now().UTC()
	step.CreatedAt = now
	step.UpdatedAt = now
	cp := *step
	r.steps[step.ID] = &cp
	return nil
}
func (r *MemoryRepository) UpdateStep(_ context.Context, orgID, id, status string, output JSONMap, errText string) (*Step, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.steps[id]
	if s == nil || s.OrganizationID != orgID {
		return nil, fmt.Errorf("workflow step not found")
	}
	s.Status = status
	s.Output = output
	s.Error = errText
	s.UpdatedAt = time.Now().UTC()
	cp := *s
	return &cp, nil
}

// AppendEvent implements Repository: the memory repository has no event
// outbox (--no-db); the executor hands the event to the dispatcher there.
func (r *MemoryRepository) AppendEvent(context.Context, string, string, JSONMap, *events.Event) (string, error) {
	return "", ErrNoEventOutbox
}
func (r *MemoryRepository) ListSteps(_ context.Context, orgID, runID string) ([]Step, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.stepsForLocked(orgID, runID), nil
}
func (r *MemoryRepository) ClaimRun(_ context.Context, orgID, id string, enqueue int) (RunClaim, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run := r.runs[id]
	if run == nil || run.OrganizationID != orgID {
		return RunClaim{}, ErrRunNotFound
	}
	if run.Enqueues != enqueue {
		return RunClaim{}, nil
	}
	switch run.Status {
	case StatusRunning:
		return RunClaim{Claimed: true}, nil
	case StatusPending:
	default:
		return RunClaim{}, nil
	}
	if run.SerialKey != "" {
		if r.runningLocked(orgID, run.SerialKey, id) {
			return RunClaim{}, nil
		}
		if first := r.oldestPendingLocked(orgID, run.SerialKey); first == nil || first.ID != id {
			return RunClaim{}, nil
		}
	}
	now := time.Now().UTC()
	run.Status, run.UpdatedAt = StatusRunning, now
	return RunClaim{Claimed: true, Started: true, Waited: now.Sub(run.CreatedAt)}, nil
}
func (r *MemoryRepository) NextSerialRun(ctx context.Context, orgID, key string, then func(ctx context.Context, run *Run)) error {
	r.mu.Lock()
	var next *Run
	if !r.runningLocked(orgID, key, "") {
		if first := r.oldestPendingLocked(orgID, key); first != nil {
			first.Enqueues++
			cp := *first
			next = &cp
		}
	}
	r.mu.Unlock()
	if next != nil {
		then(ctx, next)
	}
	return nil
}
func (r *MemoryRepository) RequeueRun(_ context.Context, orgID, id string) (*Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run := r.runs[id]
	if run == nil || run.OrganizationID != orgID || run.Status != StatusPending {
		return nil, ErrRunNotFound
	}
	run.Enqueues++
	cp := *run
	return &cp, nil
}
func (r *MemoryRepository) StrandedSerialKeys(_ context.Context, orgID string, age time.Duration) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cutoff := time.Now().UTC().Add(-age)
	seen := map[string]bool{}
	out := []string{}
	for _, run := range r.runs {
		if run.OrganizationID != orgID || run.SerialKey == "" || run.Status != StatusPending || seen[run.SerialKey] {
			continue
		}
		seen[run.SerialKey] = true
		// The age counts on the head of the key, the run NextSerialRun
		// picks.
		head := r.oldestPendingLocked(orgID, run.SerialKey)
		if head != nil && head.UpdatedAt.Before(cutoff) && !r.runningLocked(orgID, run.SerialKey, "") {
			out = append(out, run.SerialKey)
		}
	}
	sort.Strings(out)
	return out, nil
}

// RunJobOrigin: the memory repository keeps no jobs.
func (r *MemoryRepository) RunJobOrigin(context.Context, string, string) (*jobs.Job, error) {
	return nil, nil
}

// runningLocked reports whether a run of key other than except is running.
func (r *MemoryRepository) runningLocked(orgID, key, except string) bool {
	for _, run := range r.runs {
		if run.OrganizationID == orgID && run.SerialKey == key && run.ID != except && run.Status == StatusRunning {
			return true
		}
	}
	return false
}

// oldestPendingLocked returns the pending run of key that is executed
// next: entity_seq NULLS LAST, then created_at, then id.
func (r *MemoryRepository) oldestPendingLocked(orgID, key string) *Run {
	var first *Run
	for _, run := range r.runs {
		if run.OrganizationID != orgID || run.SerialKey != key || run.Status != StatusPending {
			continue
		}
		if first == nil || serialBefore(run, first) {
			first = run
		}
	}
	return first
}

// serialBefore orders the pending runs of a serial key (WP-1089 AK2).
func serialBefore(a, b *Run) bool {
	switch {
	case a.EntitySeq != nil && b.EntitySeq == nil:
		return true
	case a.EntitySeq == nil && b.EntitySeq != nil:
		return false
	case a.EntitySeq != nil && *a.EntitySeq != *b.EntitySeq:
		return *a.EntitySeq < *b.EntitySeq
	case !a.CreatedAt.Equal(b.CreatedAt):
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.ID < b.ID
}

// serialKeyOf is the serial key of a run on ciID: "ci:<ci_id>", empty
// without a CI (such runs are executed in parallel).
func serialKeyOf(ciID string) string {
	if ciID == "" {
		return ""
	}
	return "ci:" + ciID
}

func (r *MemoryRepository) stepsForLocked(orgID, runID string) []Step {
	out := []Step{}
	for _, s := range r.steps {
		if s.OrganizationID == orgID && s.RunID == runID {
			out = append(out, *s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StepIndex < out[j].StepIndex })
	return out
}
func pageDefs(items []Definition, page api.PaginationParams) ([]Definition, int, error) {
	total := len(items)
	start := min(page.Offset, total)
	end := min(start+page.Limit, total)
	return items[start:end], total, nil
}
func pageRuns(items []Run, page api.PaginationParams) ([]Run, int, error) {
	total := len(items)
	start := min(page.Offset, total)
	end := min(start+page.Limit, total)
	return items[start:end], total, nil
}

// normalizeDefinition stores empty trigger, conditions and actions as {}
// and [] like the database defaults.
func normalizeDefinition(d *Definition) {
	if d.Trigger == nil {
		d.Trigger = JSONMap{}
	}
	if d.Conditions == nil {
		d.Conditions = []JSONMap{}
	}
	if d.Actions == nil {
		d.Actions = []JSONMap{}
	}
}

// applyUpdate copies the fields the request carries onto d.
func applyUpdate(d *Definition, req *UpdateDefinitionRequest) {
	if req.Name != nil {
		d.Name = *req.Name
	}
	if req.Description != nil {
		d.Description = *req.Description
	}
	if req.Trigger != nil {
		d.Trigger = req.Trigger
	}
	if req.Conditions != nil {
		d.Conditions = req.Conditions
	}
	if req.Actions != nil {
		d.Actions = req.Actions
	}
	if req.Active != nil {
		d.Active = *req.Active
	}
}

// validateEffective checks the trigger filter and conditions a PATCH
// leaves in effect, not only the ones it carries (WP-1087 AK1/AK6): a
// definition migration 000330 deactivated for a non-convertible G1
// condition is not switched on again and does not carry that condition
// into a new version. An inactive definition whose content stays may still
// be renamed or described.
func validateEffective(current, next *Definition) error {
	if !next.Active && !contentChanged(current, next) {
		return nil
	}
	return ValidateDefinition(next.Trigger, next.Conditions)
}

// contentChanged reports whether trigger, conditions or actions of next
// differ from those of current (compared as JSON): only then a new version
// is stored (WFL-02).
func contentChanged(current, next *Definition) bool {
	return !sameJSON(current.Trigger, next.Trigger) || !sameJSON(current.Conditions, next.Conditions) || !sameJSON(current.Actions, next.Actions)
}

func sameJSON(a, b any) bool {
	ra, errA := json.Marshal(a)
	rb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ra, rb)
}

// ifMatches reports whether an If-Match version admits a change of a
// definition at version current: no header and "*" always do (API-07).
func ifMatches(ifMatch *int64, current int) bool {
	return ifMatch == nil || *ifMatch == api.IfMatchAny || *ifMatch == int64(current)
}

// createdBy is the user of the request that stores a version; empty for a
// caller without a user id (API key, system).
func createdBy(ctx context.Context) string {
	if id := tenant.FromContext(ctx).UserID; api.IsUUID(id) {
		return id
	}
	return ""
}
