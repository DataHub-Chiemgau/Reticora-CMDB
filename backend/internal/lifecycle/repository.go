package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// ErrConflict: the in-memory repository refuses a key that is taken (409;
// PostgreSQL reports the unique violation, 23505).
var ErrConflict = errors.New("a lifecycle definition with this key already exists")

// ErrSystemModel: system lifecycle models (organization_id NULL, is_system)
// are read-only; PATCH and DELETE are 403 (WP-723).
var ErrSystemModel = errors.New("system lifecycle models are read-only")

// ErrVersionMismatch: the If-Match version of a PATCH is not the current
// version of the model (412, API-07).
var ErrVersionMismatch = errors.New("the lifecycle model was changed since the If-Match version")

// ModelInUseConflict refuses the delete of a lifecycle model that a CI type
// binds (lifecycle_model_id) or that is the effective model of CIs or
// assets with a lifecycle state (409 with the counts, WP-723).
type ModelInUseConflict struct {
	CITypes int `json:"ci_types"`
	CIs     int `json:"cis"`
	Assets  int `json:"assets"`
}

// validation: a fixed text.
func (e *ModelInUseConflict) Error() string {
	return "the lifecycle model is in use by CI types, CIs or assets"
}

// StateCount is the number of entities (CIs and assets) in one lifecycle
// state of a model.
type StateCount struct {
	State string `json:"state"`
	Count int    `json:"count"`
}

// StatesInUseConflict refuses a PATCH that removes states in which entities
// of the model still stand (409 with the counts per state, WP-723).
type StatesInUseConflict struct {
	Counts []StateCount
}

// validation: a fixed text.
func (e *StatesInUseConflict) Error() string {
	return "entities stand in lifecycle states the change removes; move them to another state first"
}

// Repository defines persistence for lifecycle definitions.
type Repository interface {
	List(ctx context.Context, orgID string, page api.PaginationParams) ([]Definition, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Definition, error)
	Create(ctx context.Context, def *Definition) error
	// Update stores name, description, states (matched by key; a kept
	// state keeps its id) and transitions (replaced) of def.ID and raises
	// the version; def is filled with the stored model. expectedVersion 0
	// accepts any version, another one that differs is ErrVersionMismatch.
	// An unknown or foreign model is api.ErrNotFound, a system model
	// ErrSystemModel, a removed state with entities *StatesInUseConflict.
	Update(ctx context.Context, def *Definition, expectedVersion int64) error
	// Delete removes an organization model: unknown or foreign is
	// api.ErrNotFound, a system model ErrSystemModel, a used model
	// *ModelInUseConflict.
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
// with the two system models: the physical asset lifecycle (spec §8) and the
// software lifecycle (LCY-02), as in PostgreSQL (000055, 000201).
func NewMemoryRepository() *MemoryRepository {
	r := &MemoryRepository{defs: map[string]*Definition{}}
	r.SeedDefinition(DefaultPhysicalAssetLifecycle())
	r.SeedDefinition(DefaultSoftwareLifecycle())
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
	if d.Version == 0 {
		d.Version = 1
	}
	stored := d
	r.defs[d.ID] = &stored
}

func (r *MemoryRepository) List(_ context.Context, orgID string, page api.PaginationParams) ([]Definition, int, error) {
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
	if page.Limit <= 0 {
		return out, len(out), nil
	}
	return api.Page(out, page).Data, len(out), nil
}

func (r *MemoryRepository) GetByID(_ context.Context, orgID, id string) (*Definition, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.defs[id]
	if !ok || (d.OrganizationID != "" && d.OrganizationID != orgID) {
		return nil, api.ErrNotFound
	}
	out := *d
	return &out, nil
}

func (r *MemoryRepository) Create(_ context.Context, def *Definition) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.defs {
		if existing.OrganizationID == def.OrganizationID && existing.Key == def.Key {
			return ErrConflict
		}
	}
	r.seq++
	def.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", r.seq)
	now := time.Now().UTC()
	def.CreatedAt = now
	def.UpdatedAt = now
	def.Version = 1
	if def.AppliesTo == "" {
		def.AppliesTo = "asset"
	}
	stored := *def
	r.defs[def.ID] = &stored
	return nil
}

// Update implements Repository. The memory repository knows no entities,
// so no state counts as used.
func (r *MemoryRepository) Update(_ context.Context, def *Definition, expectedVersion int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.defs[def.ID]
	if !ok || (d.OrganizationID != "" && d.OrganizationID != def.OrganizationID) {
		return api.ErrNotFound
	}
	if d.IsSystem || d.OrganizationID == "" {
		return ErrSystemModel
	}
	if expectedVersion != api.IfMatchAny && expectedVersion != d.Version {
		return ErrVersionMismatch
	}
	ids := map[string]string{}
	for _, st := range d.States {
		ids[st.Key] = st.ID
	}
	states := make([]State, 0, len(def.States))
	for _, st := range def.States {
		st.DefinitionID = d.ID
		st.ID = ids[st.Key]
		if st.ID == "" {
			r.seq++
			st.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", r.seq)
		}
		if st.RequiredFields == nil {
			st.RequiredFields = []string{}
		}
		states = append(states, st)
		ids[st.Key] = st.ID
	}
	transitions := make([]Transition, 0, len(def.Transitions))
	for i := range def.Transitions {
		tr := def.Transitions[i]
		r.seq++
		tr.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", r.seq)
		tr.DefinitionID = d.ID
		tr.FromStateID = ids[tr.FromStateKey]
		tr.ToStateID = ids[tr.ToStateKey]
		if tr.Label == "" {
			tr.Label = tr.Key
		}
		if tr.RequiredFields == nil {
			tr.RequiredFields = []string{}
		}
		tr.Validation = guardValidation(tr.Guard)
		transitions = append(transitions, tr)
	}
	updated := *d
	updated.Name = def.Name
	updated.Description = def.Description
	updated.States = states
	updated.Transitions = transitions
	updated.Version = d.Version + 1
	updated.UpdatedAt = time.Now().UTC()
	r.defs[d.ID] = &updated
	*def = updated
	return nil
}

// Delete implements Repository. The memory repository knows no CI types or
// entities, so no model counts as used.
func (r *MemoryRepository) Delete(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.defs[id]
	if !ok || (d.OrganizationID != "" && d.OrganizationID != orgID) {
		return api.ErrNotFound
	}
	if d.IsSystem || d.OrganizationID == "" {
		return ErrSystemModel
	}
	delete(r.defs, id)
	return nil
}

// lookupKey returns the id of the model key the organization sees, its own
// before a system model of the same key.
func (r *MemoryRepository) lookupKey(orgID, key string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	found := ""
	for _, d := range r.defs {
		if d.Key != key || (d.OrganizationID != "" && d.OrganizationID != orgID) {
			continue
		}
		if d.OrganizationID != "" {
			return d.ID, true
		}
		found = d.ID
	}
	return found, found != ""
}

// SoftwareLifecycleID is the fixed id of the system model software
// (migration 000201).
const SoftwareLifecycleID = "00000000-0000-0000-0000-000000000102"

// DefaultSoftwareLifecycle returns the system lifecycle model of software
// and applications (LCY-02, applies to CIs; bound to the system types
// application and software, E-116): evaluation → approved → production ⇄
// deprecated → eol, and approved → eol.
func DefaultSoftwareLifecycle() Definition {
	states := []State{
		{Key: "evaluation", Label: "Evaluation", IsInitial: true, SortOrder: 10},
		{Key: "approved", Label: "Approved", SortOrder: 20},
		{Key: "production", Label: "Production", SortOrder: 30},
		{Key: "deprecated", Label: "Deprecated", SortOrder: 40},
		{Key: "eol", Label: "End of Life", IsTerminal: true, SortOrder: 50},
	}
	pairs := [][2]string{
		{"evaluation", "approved"}, {"approved", "production"}, {"production", "deprecated"},
		{"deprecated", "production"}, {"deprecated", "eol"}, {"approved", "eol"},
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
		ID:          SoftwareLifecycleID,
		Key:         "software",
		Name:        "Software Lifecycle",
		AppliesTo:   "ci",
		IsSystem:    true,
		Description: "Default lifecycle for software and applications",
		States:      states,
		Transitions: transitions,
	}
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
		ID:          physicalAssetModelID,
		Key:         "physical_asset",
		Name:        "Physical Asset Lifecycle",
		AppliesTo:   "asset",
		IsSystem:    true,
		Description: "Default lifecycle for serialized physical assets",
		States:      states,
		Transitions: transitions,
	}
}
