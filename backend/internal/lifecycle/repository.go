package lifecycle

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository defines persistence for lifecycle definitions.
type Repository interface {
	List(ctx context.Context, orgID string, page api.PaginationParams) ([]Definition, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Definition, error)
	// GetByKey resolves a definition by key including its states and
	// transitions. Tenant-scoped definitions take precedence over global
	// system definitions with the same key.
	GetByKey(ctx context.Context, orgID, key string) (*Definition, error)
	Create(ctx context.Context, def *Definition) error
	Delete(ctx context.Context, orgID, id string) error
}

// StateReader resolves the current lifecycle state of an entity.
type StateReader interface {
	CurrentState(ctx context.Context, orgID, entityType, entityID string) (string, error)
}

// StateWriter applies a lifecycle state to an entity.
type StateWriter interface {
	SetState(ctx context.Context, orgID, entityType, entityID, state string) error
}

// ChangeRecorder records a lifecycle transition into the history trail.
type ChangeRecorder interface {
	RecordChange(ctx context.Context, orgID, entityType, entityID, changeType, field string, oldVal, newVal any, comment string) error
}

// MemoryRepository is an in-memory implementation (tests, --no-db).
type MemoryRepository struct {
	mu   sync.RWMutex
	defs map[string]*Definition
	seq  int
}

// NewMemoryRepository creates an empty in-memory lifecycle repository seeded
// with the default physical asset lifecycle (spec §8).
func NewMemoryRepository() *MemoryRepository {
	r := &MemoryRepository{defs: map[string]*Definition{}}
	r.SeedDefinition(DefaultPhysicalAssetLifecycle())
	return r
}

// SeedDefinition registers a definition directly (tests, --no-db defaults).
func (r *MemoryRepository) SeedDefinition(d Definition) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d.ID == "" {
		r.seq++
		d.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", r.seq)
	}
	now := time.Now().UTC()
	d.CreatedAt = now
	d.UpdatedAt = now
	stored := d
	r.defs[d.ID] = &stored
}

func (r *MemoryRepository) List(_ context.Context, orgID string, _ api.PaginationParams) ([]Definition, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Definition
	for _, d := range r.defs {
		if d.OrganizationID != "" && d.OrganizationID != orgID {
			continue
		}
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, len(out), nil
}

func (r *MemoryRepository) GetByID(_ context.Context, orgID, id string) (*Definition, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.defs[id]
	if !ok || (d.OrganizationID != "" && d.OrganizationID != orgID) {
		return nil, fmt.Errorf("not found")
	}
	out := *d
	return &out, nil
}

func (r *MemoryRepository) GetByKey(_ context.Context, orgID, key string) (*Definition, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var system *Definition
	for _, d := range r.defs {
		if d.Key != key {
			continue
		}
		if d.OrganizationID == orgID && orgID != "" {
			out := *d
			return &out, nil
		}
		if d.OrganizationID == "" {
			system = d
		}
	}
	if system != nil {
		out := *system
		return &out, nil
	}
	return nil, fmt.Errorf("not found")
}

func (r *MemoryRepository) Create(_ context.Context, def *Definition) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.defs {
		if existing.OrganizationID == def.OrganizationID && existing.Key == def.Key {
			return fmt.Errorf("a lifecycle definition with this key already exists")
		}
	}
	r.seq++
	def.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", r.seq)
	now := time.Now().UTC()
	def.CreatedAt = now
	def.UpdatedAt = now
	stored := *def
	r.defs[def.ID] = &stored
	return nil
}

func (r *MemoryRepository) Delete(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.defs[id]
	if !ok || (d.OrganizationID != "" && d.OrganizationID != orgID) {
		return fmt.Errorf("not found")
	}
	if d.IsSystem {
		return fmt.Errorf("system lifecycle definitions cannot be deleted")
	}
	delete(r.defs, id)
	return nil
}

// DefaultPhysicalAssetLifecycle returns the default physical asset lifecycle
// (spec §8): Ordered → Received → In Stock → Reserved → Preparing → Deployed
// → Repair → In Stock → Retired → Disposed.
func DefaultPhysicalAssetLifecycle() Definition {
	states := []State{
		{Key: "ordered", Label: "Ordered", IsInitial: true, SortOrder: 10},
		{Key: "received", Label: "Received", SortOrder: 20},
		{Key: "in_stock", Label: "In Stock", SortOrder: 30, RequiredFields: []string{"storage_location"}},
		{Key: "reserved", Label: "Reserved", SortOrder: 40},
		{Key: "preparing", Label: "Preparing", SortOrder: 50},
		{Key: "deployed", Label: "Deployed", SortOrder: 60, RequiredFields: []string{"deployment_location"}},
		{Key: "repair", Label: "Repair", SortOrder: 70},
		{Key: "retired", Label: "Retired", SortOrder: 80},
		{Key: "disposed", Label: "Disposed", IsTerminal: true, SortOrder: 90, RequiredFields: []string{"disposal_date", "disposal_record"}},
	}
	pairs := [][2]string{
		{"ordered", "received"}, {"received", "in_stock"}, {"in_stock", "reserved"},
		{"reserved", "preparing"}, {"reserved", "in_stock"}, {"preparing", "deployed"},
		{"deployed", "repair"}, {"deployed", "in_stock"}, {"repair", "in_stock"},
		{"repair", "disposed"}, {"in_stock", "retired"}, {"deployed", "retired"},
		{"retired", "disposed"}, {"retired", "in_stock"}, {"in_stock", "deployed"},
	}
	var transitions []Transition
	for _, p := range pairs {
		transitions = append(transitions, Transition{
			Key:          p[0] + "_to_" + p[1],
			FromStateKey: p[0],
			ToStateKey:   p[1],
		})
	}
	return Definition{
		Key:         "physical_asset",
		Name:        "Physical Asset Lifecycle",
		AppliesTo:   "asset",
		IsSystem:    true,
		Description: "Default lifecycle for serialized physical assets",
		States:      states,
		Transitions: transitions,
	}
}
