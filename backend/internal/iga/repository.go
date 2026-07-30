package iga

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

type Repository interface {
	ListConnectors(orgID string, page api.PaginationParams) ([]ConnectorConfig, int, error)
	GetConnector(orgID, id string) (*ConnectorConfig, error)
	CreateConnector(c *ConnectorConfig) error
	UpdateConnector(orgID, id string, req UpdateConnectorRequest) (*ConnectorConfig, error)
	DeleteConnector(orgID, id string) error
	MarkConnectorSynced(orgID, id string, at time.Time) error

	ListTasks(orgID string, status string, page api.PaginationParams) ([]ProvisioningTask, int, error)
	GetTask(orgID, id string) (*ProvisioningTask, error)
	CreateTask(t *ProvisioningTask) error
	UpdateTask(t *ProvisioningTask) error
	DueTasks(orgID string, now time.Time, limit int) ([]ProvisioningTask, error)

	ListPolicies(orgID, event string, activeOnly bool, page api.PaginationParams) ([]LifecyclePolicy, int, error)
	CreatePolicy(p *LifecyclePolicy) error

	ListAccessRequests(orgID, status string, page api.PaginationParams) ([]AccessRequest, int, error)
	CreateAccessRequest(req *AccessRequest) error
	DecideAccessRequest(orgID, id, status, actorID, comment string) (*AccessRequest, error)

	ListReviews(orgID, status string, page api.PaginationParams) ([]AccessReview, int, error)
	CreateReview(r *AccessReview, items []AccessReviewItem) error
	ListReviewItems(orgID, reviewID string, page api.PaginationParams) ([]AccessReviewItem, int, error)
	DecideReviewItem(orgID, id, decision, actorID string) (*AccessReviewItem, error)

	ListDrift(orgID, status string, page api.PaginationParams) ([]DriftFinding, int, error)
	CreateDrift(f *DriftFinding) error
	UpdateDrift(f *DriftFinding) error
	GetDrift(orgID, id string) (*DriftFinding, error)
}

type MemoryRepository struct {
	mu          sync.RWMutex
	connectors  map[string]*ConnectorConfig
	tasks       map[string]*ProvisioningTask
	policies    map[string]*LifecyclePolicy
	requests    map[string]*AccessRequest
	reviews     map[string]*AccessReview
	reviewItems map[string]*AccessReviewItem
	drift       map[string]*DriftFinding
	next        int
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{connectors: map[string]*ConnectorConfig{}, tasks: map[string]*ProvisioningTask{}, policies: map[string]*LifecyclePolicy{}, requests: map[string]*AccessRequest{}, reviews: map[string]*AccessReview{}, reviewItems: map[string]*AccessReviewItem{}, drift: map[string]*DriftFinding{}}
}
func (r *MemoryRepository) id(prefix string) string {
	r.next++
	return fmt.Sprintf("%s-%d", prefix, r.next)
}

func page[T any](items []T, p api.PaginationParams) ([]T, int, error) {
	total := len(items)
	start := min(p.Offset, total)
	end := min(start+p.Limit, total)
	return items[start:end], total, nil
}

func (r *MemoryRepository) ListConnectors(orgID string, p api.PaginationParams) ([]ConnectorConfig, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []ConnectorConfig{}
	for _, v := range r.connectors {
		if v.OrganizationID == orgID {
			out = append(out, *v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return page(out, p)
}
func (r *MemoryRepository) GetConnector(orgID, id string) (*ConnectorConfig, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v := r.connectors[id]
	if v == nil || v.OrganizationID != orgID {
		return nil, fmt.Errorf("connector not found")
	}
	cp := *v
	return &cp, nil
}
func (r *MemoryRepository) CreateConnector(c *ConnectorConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c.ID = r.id("conn")
	now := time.Now().UTC()
	c.CreatedAt = now
	c.UpdatedAt = now
	if c.Status == "" {
		c.Status = "active"
	}
	if c.Capabilities == (ConnectorCapabilities{}) {
		c.Capabilities = DefaultCapabilities(c.Type)
	}
	cp := *c
	r.connectors[c.ID] = &cp
	return nil
}
func (r *MemoryRepository) UpdateConnector(orgID, id string, req UpdateConnectorRequest) (*ConnectorConfig, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.connectors[id]
	if c == nil || c.OrganizationID != orgID {
		return nil, fmt.Errorf("connector not found")
	}
	if req.Name != nil {
		c.Name = *req.Name
	}
	if req.BaseURL != nil {
		c.BaseURL = *req.BaseURL
	}
	if req.CredentialID != nil {
		c.CredentialID = *req.CredentialID
	}
	if req.CollectorID != nil {
		c.CollectorID = *req.CollectorID
	}
	if req.Capabilities != nil {
		c.Capabilities = *req.Capabilities
	}
	if req.Config != nil {
		c.Config = req.Config
	}
	if req.Status != nil {
		c.Status = *req.Status
	}
	c.UpdatedAt = time.Now().UTC()
	cp := *c
	return &cp, nil
}
func (r *MemoryRepository) DeleteConnector(orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.connectors[id]
	if c == nil || c.OrganizationID != orgID {
		return fmt.Errorf("connector not found")
	}
	delete(r.connectors, id)
	return nil
}
func (r *MemoryRepository) MarkConnectorSynced(orgID, id string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.connectors[id]
	if c == nil || c.OrganizationID != orgID {
		return fmt.Errorf("connector not found")
	}
	c.LastSyncAt = &at
	c.UpdatedAt = at
	return nil
}

func (r *MemoryRepository) ListTasks(orgID, status string, p api.PaginationParams) ([]ProvisioningTask, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []ProvisioningTask{}
	for _, v := range r.tasks {
		if v.OrganizationID == orgID && (status == "" || v.Status == status) {
			out = append(out, *v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return page(out, p)
}
func (r *MemoryRepository) GetTask(orgID, id string) (*ProvisioningTask, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v := r.tasks[id]
	if v == nil || v.OrganizationID != orgID {
		return nil, fmt.Errorf("task not found")
	}
	cp := *v
	return &cp, nil
}
func (r *MemoryRepository) CreateTask(t *ProvisioningTask) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	t.ID = r.id("task")
	now := time.Now().UTC()
	t.CreatedAt = now
	t.UpdatedAt = now
	if t.Status == "" {
		t.Status = TaskStatusPending
	}
	if t.MaxAttempts == 0 {
		t.MaxAttempts = 3
	}
	if t.NextRunAt.IsZero() {
		t.NextRunAt = now
	}
	if t.Payload == nil {
		t.Payload = JSONMap{}
	}
	cp := *t
	r.tasks[t.ID] = &cp
	return nil
}
func (r *MemoryRepository) UpdateTask(t *ProvisioningTask) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tasks[t.ID] == nil {
		return fmt.Errorf("task not found")
	}
	t.UpdatedAt = time.Now().UTC()
	cp := *t
	r.tasks[t.ID] = &cp
	return nil
}
func (r *MemoryRepository) DueTasks(orgID string, now time.Time, limit int) ([]ProvisioningTask, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []ProvisioningTask{}
	for _, v := range r.tasks {
		if v.OrganizationID == orgID && v.Status == TaskStatusPending && !v.NextRunAt.After(now) {
			out = append(out, *v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NextRunAt.Before(out[j].NextRunAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *MemoryRepository) ListPolicies(orgID, event string, activeOnly bool, p api.PaginationParams) ([]LifecyclePolicy, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []LifecyclePolicy{}
	for _, v := range r.policies {
		if v.OrganizationID == orgID && (event == "" || v.Event == event) && (!activeOnly || v.Active) {
			out = append(out, *v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Priority > out[j].Priority })
	return page(out, p)
}
func (r *MemoryRepository) CreatePolicy(p *LifecyclePolicy) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p.ID = r.id("jml")
	now := time.Now().UTC()
	p.CreatedAt = now
	p.UpdatedAt = now
	cp := *p
	r.policies[p.ID] = &cp
	return nil
}

func (r *MemoryRepository) ListAccessRequests(orgID, status string, p api.PaginationParams) ([]AccessRequest, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []AccessRequest{}
	for _, v := range r.requests {
		if v.OrganizationID == orgID && (status == "" || v.Status == status) {
			out = append(out, *v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return page(out, p)
}
func (r *MemoryRepository) CreateAccessRequest(a *AccessRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	a.ID = r.id("ar")
	now := time.Now().UTC()
	a.CreatedAt = now
	a.UpdatedAt = now
	if a.Status == "" {
		a.Status = "pending"
	}
	cp := *a
	r.requests[a.ID] = &cp
	return nil
}
func (r *MemoryRepository) DecideAccessRequest(orgID, id, status, actor, comment string) (*AccessRequest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.requests[id]
	if a == nil || a.OrganizationID != orgID {
		return nil, fmt.Errorf("access request not found")
	}
	now := time.Now().UTC()
	a.Status = status
	a.DecisionBy = actor
	a.DecisionComment = comment
	a.DecidedAt = &now
	a.UpdatedAt = now
	cp := *a
	return &cp, nil
}

func (r *MemoryRepository) ListReviews(orgID, status string, p api.PaginationParams) ([]AccessReview, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []AccessReview{}
	for _, v := range r.reviews {
		if v.OrganizationID == orgID && (status == "" || v.Status == status) {
			out = append(out, *v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return page(out, p)
}
func (r *MemoryRepository) CreateReview(rv *AccessReview, items []AccessReviewItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rv.ID = r.id("review")
	now := time.Now().UTC()
	rv.CreatedAt = now
	rv.UpdatedAt = now
	if rv.Status == "" {
		rv.Status = "active"
	}
	cp := *rv
	r.reviews[rv.ID] = &cp
	for i := range items {
		items[i].ID = r.id("review-item")
		items[i].OrganizationID = rv.OrganizationID
		items[i].ReviewID = rv.ID
		items[i].CreatedAt = now
		items[i].UpdatedAt = now
		it := items[i]
		r.reviewItems[it.ID] = &it
	}
	return nil
}
func (r *MemoryRepository) ListReviewItems(orgID, reviewID string, p api.PaginationParams) ([]AccessReviewItem, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []AccessReviewItem{}
	for _, v := range r.reviewItems {
		if v.OrganizationID == orgID && v.ReviewID == reviewID {
			out = append(out, *v)
		}
	}
	return page(out, p)
}
func (r *MemoryRepository) DecideReviewItem(orgID, id, decision, actor string) (*AccessReviewItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	it := r.reviewItems[id]
	if it == nil || it.OrganizationID != orgID {
		return nil, fmt.Errorf("review item not found")
	}
	now := time.Now().UTC()
	it.Decision = decision
	it.DecisionBy = actor
	it.DecidedAt = &now
	it.UpdatedAt = now
	cp := *it
	return &cp, nil
}

func (r *MemoryRepository) ListDrift(orgID, status string, p api.PaginationParams) ([]DriftFinding, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []DriftFinding{}
	for _, v := range r.drift {
		if v.OrganizationID == orgID && (status == "" || v.Status == status) {
			out = append(out, *v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return page(out, p)
}
func (r *MemoryRepository) CreateDrift(f *DriftFinding) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	f.ID = r.id("drift")
	now := time.Now().UTC()
	f.CreatedAt = now
	f.UpdatedAt = now
	if f.Status == "" {
		f.Status = "open"
	}
	if f.Severity == "" {
		f.Severity = "medium"
	}
	cp := *f
	r.drift[f.ID] = &cp
	return nil
}
func (r *MemoryRepository) UpdateDrift(f *DriftFinding) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.drift[f.ID] == nil {
		return fmt.Errorf("drift not found")
	}
	f.UpdatedAt = time.Now().UTC()
	cp := *f
	r.drift[f.ID] = &cp
	return nil
}
func (r *MemoryRepository) GetDrift(orgID, id string) (*DriftFinding, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v := r.drift[id]
	if v == nil || v.OrganizationID != orgID {
		return nil, fmt.Errorf("drift not found")
	}
	cp := *v
	return &cp, nil
}

func matchConditions(conditions JSONMap, attrs JSONMap) bool {
	for k, want := range conditions {
		if strings.TrimSpace(fmt.Sprint(want)) == "" {
			continue
		}
		if fmt.Sprint(attrs[k]) != fmt.Sprint(want) {
			return false
		}
	}
	return true
}
