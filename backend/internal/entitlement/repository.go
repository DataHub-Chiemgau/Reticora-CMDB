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
	Upsert(ctx context.Context, ent *Entitlement) (Entitlement, error)
	// InsertMissing stores the rows of the organization whose feature has
	// no row yet and returns their feature keys; an existing row, enabled
	// or not, is left unchanged.
	InsertMissing(ctx context.Context, orgID string, ents []Entitlement) ([]string, error)
}

// PlanReader reads the leading plan of an organization (organization.plan)
// and the time of its last change (WP-880). PGRepository and
// MemoryRepository implement it.
type PlanReader interface {
	OrganizationPlan(ctx context.Context, orgID string) (Plan, *time.Time, error)
}

// MemoryRepository is an in-memory entitlement store used by tests and the
// explicit --no-db development mode.
type MemoryRepository struct {
	mu    sync.RWMutex
	items map[string][]Entitlement
}

// NewMemoryRepository creates an empty in-memory entitlement repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{items: make(map[string][]Entitlement)}
}

// OrganizationPlan returns the plan of the cmdb_core row (the memory store
// keeps no organization), essential without one.
func (r *MemoryRepository) OrganizationPlan(_ context.Context, orgID string) (Plan, *time.Time, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	items := r.items[orgID]
	for i := range items {
		if items[i].FeatureKey == FeatureCMDBCore && items[i].Plan != "" {
			return normalizePlan(items[i].Plan), nil, nil
		}
	}
	return PlanEssential, nil, nil
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
func (r *MemoryRepository) Upsert(_ context.Context, in *Entitlement) (Entitlement, error) {
	ent := *in
	r.mu.Lock()
	defer r.mu.Unlock()

	if ent.UpdatedAt.IsZero() {
		ent.UpdatedAt = time.Now().UTC()
	}

	if ent.Origin == "" {
		ent.Origin = DefaultOrigin(ent.FeatureKey, ent.Plan)
	}
	stored := r.items[ent.OrganizationID]
	for i := range stored {
		if stored[i].FeatureKey == ent.FeatureKey {
			// The origin of an existing row stays (WP-880).
			if stored[i].Origin != "" {
				ent.Origin = stored[i].Origin
			}
			stored[i] = ent
			r.items[ent.OrganizationID] = stored
			return ent, nil
		}
	}

	r.items[ent.OrganizationID] = append(stored, ent)
	return ent, nil
}

// InsertMissing stores the rows whose feature has no row for the
// organization yet and returns their feature keys.
func (r *MemoryRepository) InsertMissing(_ context.Context, orgID string, ents []Entitlement) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	stored := r.items[orgID]
	var added []string
	for i := range ents {
		ent := ents[i]
		exists := false
		for i := range stored {
			if stored[i].FeatureKey == ent.FeatureKey {
				exists = true
				break
			}
		}
		if exists {
			continue
		}
		ent.OrganizationID = orgID
		if ent.Origin == "" {
			ent.Origin = DefaultOrigin(ent.FeatureKey, ent.Plan)
		}
		if ent.UpdatedAt.IsZero() {
			ent.UpdatedAt = time.Now().UTC()
		}
		stored = append(stored, ent)
		added = append(added, ent.FeatureKey)
	}
	r.items[orgID] = stored
	return added, nil
}
