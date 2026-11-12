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
	CreateRun(ctx context.Context, run *Run) error
	UpdateRunStatus(ctx context.Context, orgID, id, status string, finished *time.Time, runCtx JSONMap) (*Run, error)
	AppendStep(ctx context.Context, step *Step) error
	UpdateStep(ctx context.Context, orgID, id, status string, output JSONMap, errText string) (*Step, error)
	ListSteps(ctx context.Context, orgID, runID string) ([]Step, error)
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
func (r *MemoryRepository) UpdateDefinition(_ context.Context, orgID, id string, req *UpdateDefinitionRequest) (*Definition, error) {
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
func (r *MemoryRepository) CreateRun(_ context.Context, run *Run) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// A run is bound to a stored version of its definition (NOT NULL
	// foreign key in PostgreSQL, WFL-02).
	if v := r.versions[run.DefinitionVersionID]; v == nil || v.OrganizationID != run.OrganizationID || v.WorkflowID != run.WorkflowID {
		return fmt.Errorf("workflow run needs a version of its definition")
	}
	run.ID = r.nextID("run")
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
func (r *MemoryRepository) ListSteps(_ context.Context, orgID, runID string) ([]Step, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.stepsForLocked(orgID, runID), nil
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
