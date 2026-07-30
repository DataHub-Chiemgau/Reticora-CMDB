package workflow

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

type Repository interface {
	ListDefinitions(orgID string, activeOnly bool, page api.PaginationParams) ([]Definition, int, error)
	GetDefinition(orgID, id string) (*Definition, error)
	CreateDefinition(def *Definition) error
	UpdateDefinition(orgID, id string, req UpdateDefinitionRequest) (*Definition, error)
	DeleteDefinition(orgID, id string) error
	ListRuns(orgID, workflowID, status string, page api.PaginationParams) ([]Run, int, error)
	GetRun(orgID, id string) (*Run, error)
	CreateRun(run *Run) error
	UpdateRunStatus(orgID, id, status string, finished *time.Time, ctx JSONMap) (*Run, error)
	AppendStep(step *Step) error
	UpdateStep(orgID, id, status string, output JSONMap, errText string) (*Step, error)
	ListSteps(orgID, runID string) ([]Step, error)
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
func (r *MemoryRepository) nextID(prefix string) string {
	r.next++
	return fmt.Sprintf("%s-%d", prefix, r.next)
}
func (r *MemoryRepository) ListDefinitions(orgID string, activeOnly bool, page api.PaginationParams) ([]Definition, int, error) {
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
func (r *MemoryRepository) GetDefinition(orgID, id string) (*Definition, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d := r.definitions[id]
	if d == nil || d.OrganizationID != orgID {
		return nil, fmt.Errorf("workflow definition not found")
	}
	cp := *d
	return &cp, nil
}
func (r *MemoryRepository) CreateDefinition(def *Definition) error {
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
func (r *MemoryRepository) UpdateDefinition(orgID, id string, req UpdateDefinitionRequest) (*Definition, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.definitions[id]
	if d == nil || d.OrganizationID != orgID {
		return nil, fmt.Errorf("workflow definition not found")
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
func (r *MemoryRepository) DeleteDefinition(orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.definitions[id]
	if d == nil || d.OrganizationID != orgID {
		return fmt.Errorf("workflow definition not found")
	}
	delete(r.definitions, id)
	return nil
}
func (r *MemoryRepository) ListRuns(orgID, workflowID, status string, page api.PaginationParams) ([]Run, int, error) {
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
func (r *MemoryRepository) GetRun(orgID, id string) (*Run, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	run := r.runs[id]
	if run == nil || run.OrganizationID != orgID {
		return nil, fmt.Errorf("workflow run not found")
	}
	cp := *run
	cp.Steps = r.stepsForLocked(orgID, id)
	return &cp, nil
}
func (r *MemoryRepository) CreateRun(run *Run) error {
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
func (r *MemoryRepository) UpdateRunStatus(orgID, id, status string, finished *time.Time, ctx JSONMap) (*Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run := r.runs[id]
	if run == nil || run.OrganizationID != orgID {
		return nil, fmt.Errorf("workflow run not found")
	}
	run.Status = status
	run.FinishedAt = finished
	if ctx != nil {
		run.Context = ctx
	}
	run.UpdatedAt = time.Now().UTC()
	cp := *run
	cp.Steps = r.stepsForLocked(orgID, id)
	return &cp, nil
}
func (r *MemoryRepository) AppendStep(step *Step) error {
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
func (r *MemoryRepository) UpdateStep(orgID, id, status string, output JSONMap, errText string) (*Step, error) {
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
func (r *MemoryRepository) ListSteps(orgID, runID string) ([]Step, error) {
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
