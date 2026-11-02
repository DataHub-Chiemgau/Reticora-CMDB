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
	// InsertMissing stores the rows of the organization whose feature has
	// no row yet and returns their feature keys; an existing row, enabled
	// or not, is left unchanged.
	InsertMissing(ctx context.Context, orgID string, ents []Entitlement) ([]string, error)
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

// InsertMissing stores the rows whose feature has no row for the
// organization yet and returns their feature keys.
func (r *MemoryRepository) InsertMissing(_ context.Context, orgID string, ents []Entitlement) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	stored := r.items[orgID]
	var added []string
	for _, ent := range ents {
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
		if ent.UpdatedAt.IsZero() {
			ent.UpdatedAt = time.Now().UTC()
		}
		stored = append(stored, ent)
		added = append(added, ent.FeatureKey)
	}
	r.items[orgID] = stored
	return added, nil
}
