package compliance

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// ErrNotFound: the rule is unknown or of another organization; both answer
// 404 alike (TEN-04).
var ErrNotFound = errors.New("compliance rule not found")

type Repository interface {
	ListRules(ctx context.Context, orgID, ciTypeID, category string, activeOnly bool, page api.PaginationParams) ([]Rule, int, error)
	GetRule(ctx context.Context, orgID, id string) (*Rule, error)
	CreateRule(ctx context.Context, rule *Rule) error
	UpdateRule(ctx context.Context, orgID, id string, req UpdateRuleRequest) (*Rule, error)
	DeleteRule(ctx context.Context, orgID, id string) error
	ReplaceResults(ctx context.Context, orgID string, results []Result) error
	ListResults(ctx context.Context, orgID, ciTypeID, status string, page api.PaginationParams) ([]Result, int, error)
}
type MemoryRepository struct {
	mu      sync.RWMutex
	rules   map[string]*Rule
	results map[string]*Result
	next    int
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{rules: map[string]*Rule{}, results: map[string]*Result{}}
}
// nextID returns ids of UUID form, as the handler accepts only UUID ids
// (API-03); rules and results share the counter, so ids stay unique.
func (r *MemoryRepository) nextID(_ string) string {
	r.next++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", r.next)
}
func (r *MemoryRepository) ListRules(_ context.Context, orgID, ciTypeID, category string, activeOnly bool, page api.PaginationParams) ([]Rule, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []Rule{}
	for _, rule := range r.rules {
		if rule.OrganizationID == orgID && (ciTypeID == "" || rule.CITypeID == ciTypeID) && (category == "" || rule.Category == category) && (!activeOnly || rule.Active) {
			out = append(out, *rule)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return pageRules(out, page)
}
func (r *MemoryRepository) GetRule(_ context.Context, orgID, id string) (*Rule, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rule := r.rules[id]
	if rule == nil || rule.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	cp := *rule
	return &cp, nil
}
func (r *MemoryRepository) CreateRule(_ context.Context, rule *Rule) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rule.ID = r.nextID("rule")
	now := time.Now().UTC()
	rule.CreatedAt = now
	rule.UpdatedAt = now
	cp := *rule
	r.rules[rule.ID] = &cp
	return nil
}
func (r *MemoryRepository) UpdateRule(_ context.Context, orgID, id string, req UpdateRuleRequest) (*Rule, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rule := r.rules[id]
	if rule == nil || rule.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	if req.CITypeID != nil {
		rule.CITypeID = *req.CITypeID
	}
	if req.Name != nil {
		rule.Name = *req.Name
	}
	if req.Description != nil {
		rule.Description = *req.Description
	}
	if req.Severity != nil {
		rule.Severity = *req.Severity
	}
	if req.Category != nil {
		rule.Category = *req.Category
	}
	if req.Expression != nil {
		rule.Expression = req.Expression
	}
	if req.RemediationHint != nil {
		rule.RemediationHint = *req.RemediationHint
	}
	if req.Active != nil {
		rule.Active = *req.Active
	}
	rule.UpdatedAt = time.Now().UTC()
	cp := *rule
	return &cp, nil
}
func (r *MemoryRepository) DeleteRule(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rule := r.rules[id]
	if rule == nil || rule.OrganizationID != orgID {
		return ErrNotFound
	}
	delete(r.rules, id)
	return nil
}
func (r *MemoryRepository) ReplaceResults(_ context.Context, orgID string, results []Result) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, res := range r.results {
		if res.OrganizationID == orgID {
			delete(r.results, id)
		}
	}
	for i := range results {
		res := results[i]
		res.ID = r.nextID("result")
		now := time.Now().UTC()
		res.CreatedAt = now
		res.UpdatedAt = now
		if res.EvaluatedAt.IsZero() {
			res.EvaluatedAt = now
		}
		cp := res
		r.results[res.ID] = &cp
		results[i] = res
	}
	return nil
}
func (r *MemoryRepository) ListResults(_ context.Context, orgID, ciTypeID, status string, page api.PaginationParams) ([]Result, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []Result{}
	for _, res := range r.results {
		if res.OrganizationID == orgID && (ciTypeID == "" || res.CITypeID == ciTypeID) && (status == "" || res.Status == status) {
			out = append(out, *res)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EvaluatedAt.After(out[j].EvaluatedAt) })
	return pageResults(out, page)
}
func pageRules(items []Rule, page api.PaginationParams) ([]Rule, int, error) {
	total := len(items)
	start := min(page.Offset, total)
	end := min(start+page.Limit, total)
	return items[start:end], total, nil
}
func pageResults(items []Result, page api.PaginationParams) ([]Result, int, error) {
	total := len(items)
	start := min(page.Offset, total)
	end := min(start+page.Limit, total)
	return items[start:end], total, nil
}
