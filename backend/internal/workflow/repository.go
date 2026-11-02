package workflow

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

var (
	// ErrNotFound: the workflow definition is unknown or of another
	// organization; both answer 404 alike (TEN-04).
	ErrNotFound = errors.New("workflow definition not found")
	// ErrRunNotFound: the run is unknown or of another organization (404).
	ErrRunNotFound = errors.New("workflow run not found")
)

type Repository interface {
	ListDefinitions(ctx context.Context, orgID string, activeOnly bool, page api.PaginationParams) ([]Definition, int, error)
	GetDefinition(ctx context.Context, orgID, id string) (*Definition, error)
	CreateDefinition(ctx context.Context, def *Definition) error
	UpdateDefinition(ctx context.Context, orgID, id string, req UpdateDefinitionRequest) (*Definition, error)
	DeleteDefinition(ctx context.Context, orgID, id string) error
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
	runs        map[string]*Run
	steps       map[string]*Step
	next        int
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{definitions: map[string]*Definition{}, runs: map[string]*Run{}, steps: map[string]*Step{}}
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
func (r *MemoryRepository) CreateDefinition(_ context.Context, def *Definition) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	def.ID = r.nextID("workflow")
	now := time.Now().UTC()
	def.CreatedAt = now
	def.UpdatedAt = now
	cp := *def
	r.definitions[def.ID] = &cp
	return nil
}
func (r *MemoryRepository) UpdateDefinition(_ context.Context, orgID, id string, req UpdateDefinitionRequest) (*Definition, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.definitions[id]
	if d == nil || d.OrganizationID != orgID {
		return nil, ErrNotFound
	}
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
	d.UpdatedAt = time.Now().UTC()
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
	return nil
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
