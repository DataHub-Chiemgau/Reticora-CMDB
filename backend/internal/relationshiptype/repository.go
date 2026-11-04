package relationshiptype

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Errors of the repositories.
var (
	ErrNotFound = errors.New("not found")
	// ErrInvalid: the type violates the REL-01 contract (400).
	ErrInvalid = errors.New("invalid relationship type")
	// ErrConflict: the key is taken in the organization or by a system type
	// (409, REL-06: an organization type no longer shadows a system type).
	ErrConflict = errors.New("a relationship type with this key already exists")
	// ErrInUse: a relationship or a suppression of the organization uses the
	// type, so it cannot be deleted (409, REL-06).
	ErrInUse = errors.New("the relationship type is used by relationships or suppressions")
)

// Repository defines persistence for relationship types. A key resolves to
// one type: the organization's row before the system row (REL-06).
type Repository interface {
	List(ctx context.Context, orgID string, page api.PaginationParams) ([]Type, int, error)
	// ListAll returns every type the organization sees, one per key, without
	// a page bound (the impact rules, IMP-10).
	ListAll(ctx context.Context, orgID string) ([]Type, error)
	GetByKey(ctx context.Context, orgID, key string) (*Type, error)
	Create(ctx context.Context, typ *Type) error
	Update(ctx context.Context, orgID, key string, req *UpsertRequest) (*Type, error)
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
	catalogue := Catalogue()
	for i := range catalogue {
		stored := catalogue[i]
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

func (r *MemoryRepository) List(ctx context.Context, orgID string, _ api.PaginationParams) ([]Type, int, error) {
	out, err := r.ListAll(ctx, orgID)
	return out, len(out), err
}

// ListAll returns the system catalogue and the organization's types by
// key; an organization row replaces the system row of its key.
func (r *MemoryRepository) ListAll(_ context.Context, orgID string) ([]Type, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	byKey := map[string]Type{}
	for _, t := range r.types {
		if t.OrganizationID == "" {
			if _, taken := byKey[t.Key]; !taken {
				byKey[t.Key] = *t
			}
		} else if t.OrganizationID == orgID {
			byKey[t.Key] = *t
		}
	}
	out := make([]Type, 0, len(byKey))
	for key := range byKey {
		out = append(out, byKey[key])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
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
	return nil, ErrNotFound
}

func (r *MemoryRepository) Create(_ context.Context, typ *Type) error {
	if typ.Cardinality == "" {
		typ.Cardinality = CardinalityManyToMany
	}
	if err := typ.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// The key of a system type is taken as well (REL-06).
	if _, exists := r.types[mapKey(typ.OrganizationID, typ.Key)]; exists {
		return ErrConflict
	}
	if _, system := r.types[mapKey("", typ.Key)]; system && typ.OrganizationID != "" {
		return ErrConflict
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

func (r *MemoryRepository) Update(_ context.Context, orgID, key string, req *UpsertRequest) (*Type, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.types[mapKey(orgID, key)]
	if !ok {
		return nil, ErrNotFound
	}
	next := *t
	req.apply(&next)
	if err := next.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	*t = next
	t.UpdatedAt = time.Now().UTC()
	out := *t
	return &out, nil
}

func (r *MemoryRepository) Delete(_ context.Context, orgID, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// The memory repository knows no relationships, so it does not check
	// the use of the type (ErrInUse).
	t, ok := r.types[mapKey(orgID, key)]
	if !ok || t.IsSystem || orgID == "" {
		return ErrNotFound
	}
	delete(r.types, mapKey(orgID, key))
	return nil
}

func (r *MemoryRepository) Exists(ctx context.Context, orgID, key string) (bool, error) {
	_, err := r.GetByKey(ctx, orgID, key)
	return err == nil, nil
}
