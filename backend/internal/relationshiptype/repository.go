package relationshiptype

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository defines persistence for relationship types.
type Repository interface {
	List(ctx context.Context, orgID string, page api.PaginationParams) ([]Type, int, error)
	GetByKey(ctx context.Context, orgID, key string) (*Type, error)
	Create(ctx context.Context, typ *Type) error
	Update(ctx context.Context, orgID, key string, req UpsertRequest) (*Type, error)
	Delete(ctx context.Context, orgID, key string) error
	// Exists reports whether a key resolves to a visible type.
	Exists(ctx context.Context, orgID, key string) (bool, error)
}

// MemoryRepository is an in-memory implementation seeded with the catalogue.
type MemoryRepository struct {
	mu    sync.RWMutex
	types map[string]*Type // orgID/key -> type; "" org for global catalogue
	seq   int
}

// NewMemoryRepository creates a memory repository seeded with the system
// catalogue so relationship validation works in tests and --no-db mode.
func NewMemoryRepository() *MemoryRepository {
	r := &MemoryRepository{types: map[string]*Type{}}
	for _, t := range Catalogue() {
		stored := t
		r.seq++
		stored.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", r.seq)
		now := time.Now().UTC()
		stored.CreatedAt = now
		stored.UpdatedAt = now
		r.types["/"+stored.Key] = &stored
	}
	return r
}

func mapKey(orgID, key string) string { return orgID + "/" + key }

func (r *MemoryRepository) List(_ context.Context, orgID string, _ api.PaginationParams) ([]Type, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []Type{}
	// Global catalogue first, then tenant-specific types shadow/extend it.
	seen := map[string]bool{}
	for _, t := range r.types {
		if t.OrganizationID == "" {
			out = append(out, *t)
			seen[t.Key] = true
		}
	}
	for _, t := range r.types {
		if t.OrganizationID == orgID && !seen[t.Key] {
			out = append(out, *t)
		} else if t.OrganizationID == orgID && seen[t.Key] {
			// Tenant shadow of a system type replaces the catalogue entry.
			for i, existing := range out {
				if existing.Key == t.Key {
					out[i] = *t
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, len(out), nil
}

func (r *MemoryRepository) GetByKey(_ context.Context, orgID, key string) (*Type, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if t, ok := r.types[mapKey(orgID, key)]; ok {
		out := *t
		return &out, nil
	}
	if t, ok := r.types[mapKey("", key)]; ok {
		out := *t
		return &out, nil
	}
	return nil, fmt.Errorf("not found")
}

func (r *MemoryRepository) Create(_ context.Context, typ *Type) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.types[mapKey(typ.OrganizationID, typ.Key)]; exists {
		return fmt.Errorf("a relationship type with this key already exists")
	}
	r.seq++
	typ.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", r.seq)
	now := time.Now().UTC()
	typ.CreatedAt = now
	typ.UpdatedAt = now
	stored := *typ
	r.types[mapKey(typ.OrganizationID, typ.Key)] = &stored
	return nil
}

func (r *MemoryRepository) Update(_ context.Context, orgID, key string, req UpsertRequest) (*Type, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.types[mapKey(orgID, key)]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	if req.ForwardLabel != "" {
		t.ForwardLabel = req.ForwardLabel
	}
	if req.ReverseLabel != "" {
		t.ReverseLabel = req.ReverseLabel
	}
	if req.SourceCITypes != nil {
		t.SourceCITypes = req.SourceCITypes
	}
	if req.TargetCITypes != nil {
		t.TargetCITypes = req.TargetCITypes
	}
	if req.Direction != "" {
		t.Direction = req.Direction
	}
	if req.Cardinality != "" {
		t.Cardinality = req.Cardinality
	}
	if req.Category != "" {
		t.Category = req.Category
	}
	if req.ImpactParticipation != nil {
		t.ImpactParticipation = *req.ImpactParticipation
	}
	if req.Description != "" {
		t.Description = req.Description
	}
	t.UpdatedAt = time.Now().UTC()
	out := *t
	return &out, nil
}

func (r *MemoryRepository) Delete(_ context.Context, orgID, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.types[mapKey(orgID, key)]
	if !ok {
		return fmt.Errorf("not found")
	}
	if t.IsSystem {
		return fmt.Errorf("system relationship types cannot be deleted")
	}
	delete(r.types, mapKey(orgID, key))
	return nil
}

func (r *MemoryRepository) Exists(ctx context.Context, orgID, key string) (bool, error) {
	_, err := r.GetByKey(ctx, orgID, key)
	return err == nil, nil
}
