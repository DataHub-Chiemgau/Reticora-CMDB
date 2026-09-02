package entitlement

import (
	"context"
	"sync"
	"time"
)

// Repository defines persistence operations for entitlements.
type Repository interface {
	// List returns every entitlement row stored for the organization.
	List(ctx context.Context, orgID string) ([]Entitlement, error)
	// Upsert stores an entitlement, replacing an existing row for the same
	// (organization, feature) pair.
	Upsert(ctx context.Context, ent Entitlement) (Entitlement, error)
	// OrganizationPlan returns the base plan stored on the organization
	// record, or "" when the organization has no plan set.
	OrganizationPlan(ctx context.Context, orgID string) (Plan, error)
}

// MemoryRepository is an in-memory entitlement store used by tests and the
// explicit --no-db development mode.
type MemoryRepository struct {
	mu    sync.RWMutex
	items map[string][]Entitlement
	plans map[string]Plan
}

// NewMemoryRepository creates an empty in-memory entitlement repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{items: make(map[string][]Entitlement), plans: make(map[string]Plan)}
}

// SetOrganizationPlan stores the base plan for an organization (tests/dev mode).
func (r *MemoryRepository) SetOrganizationPlan(orgID string, plan Plan) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.plans[orgID] = plan
}

// OrganizationPlan returns the stored base plan, or "" when unset.
func (r *MemoryRepository) OrganizationPlan(_ context.Context, orgID string) (Plan, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.plans[orgID], nil
}

// List returns the entitlements stored for the organization.
func (r *MemoryRepository) List(_ context.Context, orgID string) ([]Entitlement, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	stored := r.items[orgID]
	out := make([]Entitlement, len(stored))
	copy(out, stored)
	return out, nil
}

// Upsert stores or replaces a single entitlement.
func (r *MemoryRepository) Upsert(_ context.Context, ent Entitlement) (Entitlement, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if ent.UpdatedAt.IsZero() {
		ent.UpdatedAt = time.Now().UTC()
	}

	stored := r.items[ent.OrganizationID]
	for i := range stored {
		if stored[i].FeatureKey == ent.FeatureKey {
			stored[i] = ent
			r.items[ent.OrganizationID] = stored
			return ent, nil
		}
	}

	r.items[ent.OrganizationID] = append(stored, ent)
	return ent, nil
}
