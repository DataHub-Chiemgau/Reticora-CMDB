package form

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository defines persistence for form definitions and submissions.
type Repository interface {
	ListDefinitions(ctx context.Context, orgID, clientID string, activeOnly bool, page api.PaginationParams) ([]Definition, int, error)
	GetDefinition(ctx context.Context, orgID, id string) (*Definition, error)
	CreateDefinition(ctx context.Context, def *Definition) error
	UpdateDefinition(ctx context.Context, orgID, id string, req UpdateDefinitionRequest) (*Definition, error)
	DeleteDefinition(ctx context.Context, orgID, id string) error
	ListSubmissions(ctx context.Context, orgID string, filter SubmissionFilter, page api.PaginationParams) ([]Submission, int, error)
	GetSubmission(ctx context.Context, orgID, id string) (*Submission, error)
	CreateSubmission(ctx context.Context, sub *Submission) error
}

type MemoryRepository struct {
	mu          sync.RWMutex
	definitions map[string]*Definition
	submissions map[string]*Submission
	next        int
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{definitions: map[string]*Definition{}, submissions: map[string]*Submission{}}
}

func (r *MemoryRepository) ListDefinitions(_ context.Context, orgID, clientID string, activeOnly bool, page api.PaginationParams) ([]Definition, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []Definition{}
	for _, def := range r.definitions {
		if def.OrganizationID != orgID || (clientID != "" && def.ClientID != clientID) || (activeOnly && !def.Active) {
			continue
		}
		out = append(out, *def)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return pageDefinitions(out, page)
}
func (r *MemoryRepository) GetDefinition(_ context.Context, orgID, id string) (*Definition, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def := r.definitions[id]
	if def == nil || def.OrganizationID != orgID {
		return nil, fmt.Errorf("form definition not found")
	}
	cp := *def
	return &cp, nil
}
func (r *MemoryRepository) CreateDefinition(_ context.Context, def *Definition) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	def.ID = fmt.Sprintf("form-%d", r.next)
	now := time.Now().UTC()
	def.CreatedAt = now
	def.UpdatedAt = now
	if def.Schema == nil {
		def.Schema = JSONMap{}
	}
	if def.UIHints == nil {
		def.UIHints = JSONMap{}
	}
	cp := *def
	r.definitions[def.ID] = &cp
	return nil
}
func (r *MemoryRepository) UpdateDefinition(_ context.Context, orgID, id string, req UpdateDefinitionRequest) (*Definition, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	def := r.definitions[id]
	if def == nil || def.OrganizationID != orgID {
		return nil, fmt.Errorf("form definition not found")
	}
	if req.ClientID != nil {
		def.ClientID = *req.ClientID
	}
	if req.Name != nil {
		def.Name = *req.Name
	}
	if req.Description != nil {
		def.Description = *req.Description
	}
	if req.Schema != nil {
		def.Schema = req.Schema
	}
	if req.UIHints != nil {
		def.UIHints = req.UIHints
	}
	if req.Active != nil {
		def.Active = *req.Active
	}
	def.UpdatedAt = time.Now().UTC()
	cp := *def
	return &cp, nil
}
func (r *MemoryRepository) DeleteDefinition(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	def := r.definitions[id]
	if def == nil || def.OrganizationID != orgID {
		return fmt.Errorf("form definition not found")
	}
	delete(r.definitions, id)
	return nil
}
func (r *MemoryRepository) ListSubmissions(_ context.Context, orgID string, filter SubmissionFilter, page api.PaginationParams) ([]Submission, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []Submission{}
	for _, sub := range r.submissions {
		if sub.OrganizationID != orgID || (filter.FormID != "" && sub.FormID != filter.FormID) || (filter.Status != "" && sub.Status != filter.Status) || (filter.TicketID != "" && sub.TicketID != filter.TicketID) || (filter.CIID != "" && sub.CIID != filter.CIID) {
			continue
		}
		out = append(out, *sub)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return pageSubmissions(out, page)
}
func (r *MemoryRepository) GetSubmission(_ context.Context, orgID, id string) (*Submission, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sub := r.submissions[id]
	if sub == nil || sub.OrganizationID != orgID {
		return nil, fmt.Errorf("form submission not found")
	}
	cp := *sub
	return &cp, nil
}
func (r *MemoryRepository) CreateSubmission(_ context.Context, sub *Submission) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	def := r.definitions[sub.FormID]
	if def == nil || def.OrganizationID != sub.OrganizationID {
		return fmt.Errorf("form definition not found")
	}
	r.next++
	sub.ID = fmt.Sprintf("submission-%d", r.next)
	now := time.Now().UTC()
	sub.CreatedAt = now
	sub.UpdatedAt = now
	if sub.Values == nil {
		sub.Values = JSONMap{}
	}
	if sub.Status == "" {
		sub.Status = "submitted"
	}
	cp := *sub
	r.submissions[sub.ID] = &cp
	return nil
}

func pageDefinitions(items []Definition, page api.PaginationParams) ([]Definition, int, error) {
	total := len(items)
	start := min(page.Offset, total)
	end := min(start+page.Limit, total)
	return items[start:end], total, nil
}
func pageSubmissions(items []Submission, page api.PaginationParams) ([]Submission, int, error) {
	total := len(items)
	start := min(page.Offset, total)
	end := min(start+page.Limit, total)
	return items[start:end], total, nil
}
