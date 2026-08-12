package privacy

import (
	"context"
	"sync"
	"time"
)

// MemoryRepository is an in-memory retention-policy store for tests and the
// --no-db development mode.
type MemoryRepository struct {
	mu       sync.RWMutex
	policies map[string]*RetentionPolicy
}

// NewMemoryRepository creates an empty in-memory repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{policies: make(map[string]*RetentionPolicy)}
}

func (r *MemoryRepository) GetPolicy(_ context.Context, orgID string) (*RetentionPolicy, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	policy, ok := r.policies[orgID]
	if !ok {
		return nil, ErrNoPolicy
	}
	cp := *policy
	return &cp, nil
}

func (r *MemoryRepository) UpsertPolicy(_ context.Context, policy *RetentionPolicy) (*RetentionPolicy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.policies[policy.OrganizationID]
	now := time.Now().UTC()
	if !ok {
		existing = &RetentionPolicy{OrganizationID: policy.OrganizationID}
		existing.ID = "policy-" + policy.OrganizationID
		existing.CreatedAt = now
		r.policies[policy.OrganizationID] = existing
	}
	existing.RetentionDays = policy.RetentionDays
	existing.Mode = policy.Mode
	existing.UpdatedAt = now
	cp := *existing
	return &cp, nil
}
