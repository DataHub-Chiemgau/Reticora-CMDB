package citype

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
)

// ErrConflict: the in-memory repository refuses a key or name that is taken
// (409; PostgreSQL reports the unique violation, 23505).
var ErrConflict = errors.New("a CI type with this key or name already exists")

// ErrTypeReadOnly: system CI types (organization_id NULL) are read-only for
// tenants and cannot be deactivated or activated (E-10, MET-05; 403, like
// ErrAttributeReadOnly). An organization replaces a system type by cloning.
var ErrTypeReadOnly = errors.New("system ci types are read-only")

// The invariant of MET-05: an active type has no deactivated parent.
// validation: fixed texts.
const (
	typeDeactivated      = "the CI type is deactivated"
	typeHasActiveSubtype = "the ci type has active subtypes; deactivate them first"
)

// errParentInactive refuses a deactivated parent_type_id on create and
// update (422).
func errParentInactive() error {
	return &api.FieldError{Field: "parent_type_id", Message: typeDeactivated, Status: http.StatusUnprocessableEntity}
}

// errParentInactiveConflict refuses activating or cloning a type whose
// parent is deactivated (409).
func errParentInactiveConflict() error {
	return &api.ConflictError{Field: "parent_type_id", Message: "the parent ci type is deactivated"}
}

// errActiveSubtypes refuses deactivating a type with active subtypes (409).
func errActiveSubtypes() error {
	return &api.ConflictError{Field: "is_active", Message: typeHasActiveSubtype}
}

// Repository defines persistence operations for CI types, field definitions
// and CI-instance field definitions.
type Repository interface {
	List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Type, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Type, error)
	Create(ctx context.Context, typ *Type) error
	Update(ctx context.Context, orgID, id string, req UpdateTypeRequest) (*Type, error)
	// Clone copies a type (including its field definitions, its parent and,
	// in PostgreSQL, its attribute set assignments with their positions)
	// under a new key; a deactivated parent is a conflict (409).
	Clone(ctx context.Context, orgID, id string, req CloneRequest) (*Type, error)
	// SetActive deactivates/reactivates a type. Deactivated types keep their
	// CIs readable but cannot be selected for new CIs (MET-05). A system
	// type is ErrTypeReadOnly; deactivating a type with active subtypes and
	// activating one under a deactivated parent are conflicts (409), an
	// unknown type is "not found".
	SetActive(ctx context.Context, orgID, id string, active bool) (*Type, error)
	// Versions returns the clone/version lineage of a type.
	Versions(ctx context.Context, orgID, id string) ([]Type, error)
	// TypeChain returns the type chain root → id (MET-06), each type with
	// its own field definitions (PostgreSQL fills ID and Fields only).
	TypeChain(ctx context.Context, orgID, id string) ([]Type, error)
	// TypeSubtree returns the type id and every descendant the
	// organization sees, by depth (MET-06).
	TypeSubtree(ctx context.Context, orgID, id string) ([]Type, error)

	ListFields(ctx context.Context, orgID, typeID string) ([]Field, error)
	ListGlobalFields(ctx context.Context, orgID string) ([]Field, error)
	UpsertField(ctx context.Context, orgID, typeID string, req UpsertFieldRequest) (*Field, error)
	DeleteField(ctx context.Context, orgID, typeID, name string) error

	ListInstanceFields(ctx context.Context, orgID, ciID string) ([]InstanceField, error)
	// InstanceFieldNames lists the names of the instance attributes of a CI;
	// discovery must not write them (MET-14).
	InstanceFieldNames(ctx context.Context, orgID, ciID string) ([]string, error)
	UpsertInstanceField(ctx context.Context, orgID, ciID string, req UpsertInstanceFieldRequest) (*InstanceField, error)
	DeleteInstanceField(ctx context.Context, orgID, ciID, name string) error

	// ResolveFields merges the global, type and instance attribute scopes into
	// the effective field definitions of a CI, used for server-side validation.
	ResolveFields(ctx context.Context, orgID, ciTypeID, ciID string) ([]fieldmeta.FieldDefinition, error)
}

// MemoryRepository is an in-memory implementation (tests, --no-db).
type MemoryRepository struct {
	mu      sync.RWMutex
	types   map[string]*Type
	fields  map[string][]Field         // typeID -> fields
	globals map[string][]Field         // orgID -> global fields
	inst    map[string][]InstanceField // ciID -> instance fields
	seq     int
}

// NewMemoryRepository creates an empty in-memory CI type repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		types:   map[string]*Type{},
		fields:  map[string][]Field{},
		globals: map[string][]Field{},
		inst:    map[string][]InstanceField{},
	}
}

func (r *MemoryRepository) nextID() string {
	r.seq++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", r.seq)
}

// SeedType registers a type directly (tests, --no-db defaults).
func (r *MemoryRepository) SeedType(t Type) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t.ID == "" {
		t.ID = r.nextID()
	}
	if t.Key == "" {
		t.Key = t.Name
	}
	now := time.Now().UTC()
	t.CreatedAt = now
	t.UpdatedAt = now
	stored := t
	r.types[t.ID] = &stored
	if len(t.Fields) > 0 {
		r.fields[t.ID] = append([]Field{}, t.Fields...)
	}
}

func (r *MemoryRepository) List(_ context.Context, orgID string, filter FilterParams, _ api.PaginationParams) ([]Type, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Type
	for _, t := range r.types {
		if t.OrganizationID != "" && t.OrganizationID != orgID {
			continue
		}
		if !filter.IncludeInactive && !t.IsActive {
			continue
		}
		if filter.Category != "" && t.Category != filter.Category {
			continue
		}
		if filter.Search != "" &&
			!strings.Contains(strings.ToLower(t.Name), strings.ToLower(filter.Search)) &&
			!t.DisplayName.Contains(filter.Search) {
			continue
		}
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, len(out), nil
}

func (r *MemoryRepository) GetByID(_ context.Context, orgID, id string) (*Type, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.types[id]
	if !ok || (t.OrganizationID != "" && t.OrganizationID != orgID) {
		return nil, fmt.Errorf("not found")
	}
	out := *t
	out.Fields = append([]Field{}, r.fields[id]...)
	return &out, nil
}

func (r *MemoryRepository) Create(_ context.Context, typ *Type) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkParentLocked(typ, typ.ParentTypeID); err != nil {
		return err
	}
	for _, existing := range r.types {
		if existing.OrganizationID == typ.OrganizationID &&
			(existing.Key == typ.Key || existing.Name == typ.Name) && existing.IsActive {
			return ErrConflict
		}
	}
	typ.ID = r.nextID()
	if typ.Key == "" {
		typ.Key = slugify(typ.Name)
	}
	typ.IsActive = true
	typ.Version = 1
	typ.mirrorLifecycle()
	now := time.Now().UTC()
	typ.CreatedAt = now
	typ.UpdatedAt = now
	stored := *typ
	r.types[typ.ID] = &stored
	// As in PostgreSQL (upsertFieldTx), a new definition is inheritable.
	flds := append([]Field{}, typ.Fields...)
	for i := range flds {
		flds[i].Inheritable = true
	}
	r.fields[typ.ID] = flds
	if err := r.checkSubtreeCompositionLocked(stored.OrganizationID, typ.ID); err != nil {
		delete(r.types, typ.ID)
		delete(r.fields, typ.ID)
		return err
	}
	return nil
}

func (r *MemoryRepository) Update(_ context.Context, orgID, id string, req UpdateTypeRequest) (*Type, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.types[id]
	if !ok || (t.OrganizationID != "" && t.OrganizationID != orgID) {
		return nil, fmt.Errorf("not found")
	}
	if _, err := req.LifecycleModel(); err != nil {
		return nil, err
	}
	if req.ParentTypeID != nil && *req.ParentTypeID != t.ParentTypeID {
		if err := r.checkParentLocked(t, *req.ParentTypeID); err != nil {
			return nil, err
		}
		previous := t.ParentTypeID
		t.ParentTypeID = *req.ParentTypeID
		if err := r.checkSubtreeCompositionLocked(orgID, id); err != nil {
			t.ParentTypeID = previous
			return nil, err
		}
	}
	applyTypeUpdate(t, req)
	t.UpdatedAt = time.Now().UTC()
	out := *t
	out.Fields = append([]Field{}, r.fields[id]...)
	return &out, nil
}

func applyTypeUpdate(t *Type, req UpdateTypeRequest) {
	if req.Name != nil {
		t.Name = *req.Name
	}
	if req.DisplayName != nil {
		t.DisplayName = req.DisplayName.WithFallback(t.Name)
	}
	if req.IsObservable != nil {
		t.IsObservable = *req.IsObservable
	}
	if req.Icon != nil {
		t.Icon = *req.Icon
	}
	if req.Description != nil {
		t.Description = *req.Description
	}
	if req.Category != nil {
		t.Category = *req.Category
	}
	if req.IsLogical != nil {
		t.IsLogical = *req.IsLogical
	}
	if model, err := req.LifecycleModel(); err == nil && model != nil {
		t.LifecycleModelID = *model
		t.mirrorLifecycle()
	}
	if req.Capabilities != nil {
		t.Capabilities = req.Capabilities
	}
	if req.AllowedRelationshipTypes != nil {
		t.AllowedRelationshipTypes = req.AllowedRelationshipTypes
	}
	if req.UISchema != nil {
		t.UISchema = req.UISchema
	}
	if req.ComplianceRules != nil {
		t.ComplianceRules = req.ComplianceRules
	}
	if req.DiscoveryMappings != nil {
		t.DiscoveryMappings = req.DiscoveryMappings
	}
}

func (r *MemoryRepository) Clone(_ context.Context, orgID, id string, req CloneRequest) (*Type, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	src, ok := r.types[id]
	if !ok || (src.OrganizationID != "" && src.OrganizationID != orgID) {
		return nil, fmt.Errorf("not found")
	}
	if parent, has := r.types[src.ParentTypeID]; has && !parent.IsActive {
		return nil, errParentInactiveConflict()
	}
	name := req.Name
	if name == "" {
		name = src.Name + "_copy"
	}
	key := req.Key
	if key == "" {
		key = slugify(name)
	}
	for _, existing := range r.types {
		if existing.OrganizationID == orgID && (existing.Key == key || existing.Name == name) && existing.IsActive {
			return nil, ErrConflict
		}
	}
	clone := *src
	clone.ID = r.nextID()
	clone.OrganizationID = orgID
	clone.Name = name
	clone.Key = key
	clone.DisplayName = LocalizedText{}.WithFallback(name)
	clone.IsBuiltin = false
	clone.IsSystem = false
	clone.IsActive = true
	// A clone of an existing type continues the lineage at the next version.
	clone.Version = src.Version + 1
	clone.ClonedFromID = src.ID
	clone.CreatedAt = time.Now().UTC()
	clone.UpdatedAt = clone.CreatedAt
	r.types[clone.ID] = &clone
	r.fields[clone.ID] = append([]Field{}, r.fields[id]...)
	out := clone
	out.Fields = append([]Field{}, r.fields[clone.ID]...)
	return &out, nil
}

func (r *MemoryRepository) SetActive(_ context.Context, orgID, id string, active bool) (*Type, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.types[id]
	if !ok || (t.OrganizationID != "" && t.OrganizationID != orgID) {
		return nil, fmt.Errorf("not found")
	}
	if t.OrganizationID == "" {
		return nil, ErrTypeReadOnly
	}
	if active {
		if parent, has := r.types[t.ParentTypeID]; has && !parent.IsActive {
			return nil, errParentInactiveConflict()
		}
	} else {
		for _, child := range r.types {
			if child.ParentTypeID == id && child.IsActive {
				return nil, errActiveSubtypes()
			}
		}
	}
	t.IsActive = active
	t.UpdatedAt = time.Now().UTC()
	out := *t
	return &out, nil
}

func (r *MemoryRepository) Versions(_ context.Context, orgID, id string) ([]Type, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	root, ok := r.types[id]
	if !ok || (root.OrganizationID != "" && root.OrganizationID != orgID) {
		return nil, fmt.Errorf("not found")
	}
	rootID := root.ClonedFromID
	if rootID == "" {
		rootID = root.ID
	}
	var out []Type
	for _, t := range r.types {
		if t.ID == rootID || t.ClonedFromID == rootID {
			out = append(out, *t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// visibleLocked returns type id when orgID sees it (its own or a system
// type).
func (r *MemoryRepository) visibleLocked(orgID, id string) (*Type, bool) {
	t, ok := r.types[id]
	if !ok || (t.OrganizationID != "" && t.OrganizationID != orgID) {
		return nil, false
	}
	return t, true
}

// chainLocked is the chain root → id of the types orgID sees.
func (r *MemoryRepository) chainLocked(orgID, id string) []*Type {
	var up []*Type
	for cur := id; cur != "" && len(up) <= 64; {
		t, ok := r.visibleLocked(orgID, cur)
		if !ok {
			break
		}
		up = append(up, t)
		cur = t.ParentTypeID
	}
	slices.Reverse(up)
	return up
}

// subtreeLocked is id and its visible descendants, by depth; height is
// the number of levels (1 for a leaf).
func (r *MemoryRepository) subtreeLocked(orgID, id string) (out []*Type, height int) {
	root, ok := r.visibleLocked(orgID, id)
	if !ok {
		return nil, 0
	}
	level := []*Type{root}
	for len(level) > 0 && height <= 64 {
		height++
		out = append(out, level...)
		var next []*Type
		for _, parent := range level {
			for _, t := range r.types {
				if t.ParentTypeID == parent.ID && (t.OrganizationID == "" || t.OrganizationID == orgID) {
					next = append(next, t)
				}
			}
		}
		sort.Slice(next, func(i, j int) bool { return next[i].Name < next[j].Name })
		level = next
	}
	return out, height
}

// checkParentLocked applies the rules of trg_ci_type_parent_guard to the
// new parent of t (t.ID empty for a new type).
func (r *MemoryRepository) checkParentLocked(t *Type, parentID string) error {
	if parentID == "" {
		return nil
	}
	parent, ok := r.types[parentID]
	switch {
	case !ok:
		return errParentUnknown()
	case t.OrganizationID == "" && parent.OrganizationID != "":
		return errParentSystem()
	case parent.OrganizationID != "" && parent.OrganizationID != t.OrganizationID:
		return errParentUnknown()
	}
	if !parent.IsActive {
		return errParentInactive()
	}
	chain := r.chainLocked(t.OrganizationID, parentID)
	height := 1
	if t.ID != "" {
		for _, a := range chain {
			if a.ID == t.ID {
				return errParentCycle()
			}
		}
		_, height = r.subtreeLocked(t.OrganizationID, t.ID)
	}
	if len(chain)+height > MaxTypeChainDepth {
		return errParentDepth()
	}
	return nil
}

// chainTypesLocked copies a chain with the field definitions of each type.
func (r *MemoryRepository) chainTypesLocked(chain []*Type) []Type {
	out := make([]Type, 0, len(chain))
	for _, t := range chain {
		c := *t
		c.Fields = append([]Field{}, r.fields[t.ID]...)
		sort.SliceStable(c.Fields, func(i, j int) bool { return c.Fields[i].SortOrder < c.Fields[j].SortOrder })
		out = append(out, c)
	}
	return out
}

// checkSubtreeCompositionLocked composes system → organization → chain
// for id and each descendant (MET-16); a conflict is a 422 on
// parent_type_id naming the key.
func (r *MemoryRepository) checkSubtreeCompositionLocked(orgID, id string) error {
	var system, org []fieldmeta.FieldDefinition
	globals := r.globals[orgID]
	for i := range globals {
		if globals[i].IsSystem {
			system = append(system, globals[i].Definition())
		} else {
			org = append(org, globals[i].Definition())
		}
	}
	subtree, _ := r.subtreeLocked(orgID, id)
	for _, t := range subtree {
		typed := chainDefinitions(r.chainTypesLocked(r.chainLocked(orgID, t.ID)))
		if _, err := Compose(system, org, typed); err != nil {
			var conflict *CompositionError
			code := ""
			if errors.As(err, &conflict) {
				code = conflict.Code
			}
			return errParentComposition(err.Error(), code)
		}
	}
	return nil
}

// TypeChain implements Repository: the chain root → id; an unknown type is
// "not found".
func (r *MemoryRepository) TypeChain(_ context.Context, orgID, id string) ([]Type, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if _, ok := r.visibleLocked(orgID, id); !ok {
		return nil, fmt.Errorf("not found")
	}
	return r.chainTypesLocked(r.chainLocked(orgID, id)), nil
}

// ListSetFields implements FieldLister: the in-memory repository keeps no
// attribute sets (WP-250 stores them in PostgreSQL only), so there is no
// set layer.
func (r *MemoryRepository) ListSetFields(context.Context, string, string) ([][]Field, error) {
	return nil, nil
}

// TypeSubtree implements Repository: id and its visible descendants by
// depth; an unknown type is "not found".
func (r *MemoryRepository) TypeSubtree(_ context.Context, orgID, id string) ([]Type, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	subtree, _ := r.subtreeLocked(orgID, id)
	if len(subtree) == 0 {
		return nil, fmt.Errorf("not found")
	}
	out := make([]Type, 0, len(subtree))
	for _, t := range subtree {
		out = append(out, *t)
	}
	return out, nil
}

func (r *MemoryRepository) ListFields(_ context.Context, orgID, typeID string) ([]Field, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.types[typeID]
	if !ok || (t.OrganizationID != "" && t.OrganizationID != orgID) {
		return nil, fmt.Errorf("not found")
	}
	out := append([]Field{}, r.fields[typeID]...)
	sort.Slice(out, func(i, j int) bool { return out[i].SortOrder < out[j].SortOrder })
	return out, nil
}

func (r *MemoryRepository) ListGlobalFields(_ context.Context, orgID string) ([]Field, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := append([]Field{}, r.globals[orgID]...)
	sort.Slice(out, func(i, j int) bool { return out[i].SortOrder < out[j].SortOrder })
	return out, nil
}

func (r *MemoryRepository) UpsertField(_ context.Context, orgID, typeID string, req UpsertFieldRequest) (*Field, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	def := fieldFromUpsert(req)
	if err := fieldmeta.ValidateDefinition(def); err != nil {
		return nil, err
	}
	f := fieldFromDefinition(def)
	f.Scope = req.Scope
	if f.Scope == "" {
		f.Scope = "type"
	}
	if f.Scope == "global" {
		list := r.globals[orgID]
		for i, existing := range list {
			if existing.Name == f.Name {
				f.ID = existing.ID
				list[i] = f
				r.globals[orgID] = list
				return &f, nil
			}
		}
		f.ID = r.nextID()
		r.globals[orgID] = append(list, f)
		return &f, nil
	}
	// As in PostgreSQL (checkFieldType): a type of another organization is
	// unknown, a system type is read-only.
	t, ok := r.types[typeID]
	if !ok || (t.OrganizationID != "" && t.OrganizationID != orgID) {
		return nil, fmt.Errorf("not found")
	}
	if t.OrganizationID == "" || t.IsSystem {
		return nil, errSystemTypeAttribute()
	}
	list := r.fields[typeID]
	for i, existing := range list {
		if existing.Name == f.Name {
			f.ID = existing.ID
			list[i] = f
			r.fields[typeID] = list
			return &f, nil
		}
	}
	f.ID = r.nextID()
	f.CITypeID = typeID
	f.Inheritable = true
	r.fields[typeID] = append(list, f)
	return &f, nil
}

func (r *MemoryRepository) DeleteField(_ context.Context, orgID, typeID, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := r.fields[typeID]
	for i, existing := range list {
		if existing.Name == name {
			r.fields[typeID] = append(list[:i], list[i+1:]...)
			return nil
		}
	}
	// Fall back to the org-global definitions.
	globals := r.globals[orgID]
	for i, existing := range globals {
		if existing.Name == name {
			r.globals[orgID] = append(globals[:i], globals[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("not found")
}

func (r *MemoryRepository) ListInstanceFields(_ context.Context, orgID, ciID string) ([]InstanceField, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := append([]InstanceField{}, r.inst[ciID]...)
	sort.Slice(out, func(i, j int) bool { return out[i].SortOrder < out[j].SortOrder })
	return out, nil
}

// InstanceFieldNames lists the names of the instance attributes of a CI.
func (r *MemoryRepository) InstanceFieldNames(ctx context.Context, orgID, ciID string) ([]string, error) {
	fields, err := r.ListInstanceFields(ctx, orgID, ciID)
	return fieldNames(fields), err
}

func (r *MemoryRepository) UpsertInstanceField(_ context.Context, orgID, ciID string, req UpsertInstanceFieldRequest) (*InstanceField, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := fieldmeta.ValidateDefinition(instanceFieldFromUpsert(req)); err != nil {
		return nil, err
	}
	f := InstanceField{
		OrganizationID:  orgID,
		CIID:            ciID,
		Name:            req.Name,
		Label:           req.Label,
		Description:     req.Description,
		DataType:        req.DataType,
		Required:        req.Required,
		DefaultValue:    req.DefaultValue,
		EnumValues:      req.EnumValues,
		UIGroup:         req.UIGroup,
		SortOrder:       req.SortOrder,
		Validation:      req.Validation,
		Conditional:     req.Conditional,
		ReferenceTarget: req.ReferenceTarget,
	}
	now := time.Now().UTC()
	list := r.inst[ciID]
	for i, existing := range list {
		if existing.Name == f.Name {
			f.ID = existing.ID
			f.CreatedAt = existing.CreatedAt
			f.UpdatedAt = now
			list[i] = f
			r.inst[ciID] = list
			return &f, nil
		}
	}
	f.ID = r.nextID()
	f.CreatedAt = now
	f.UpdatedAt = now
	r.inst[ciID] = append(list, f)
	return &f, nil
}

func (r *MemoryRepository) DeleteInstanceField(_ context.Context, orgID, ciID, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := r.inst[ciID]
	for i, existing := range list {
		if existing.Name == name {
			r.inst[ciID] = append(list[:i], list[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("not found")
}

func fieldFromUpsert(req UpsertFieldRequest) fieldmeta.FieldDefinition {
	return fieldmeta.FieldDefinition{
		Name:            req.Name,
		Label:           req.Label,
		Description:     req.Description,
		DataType:        req.DataType,
		Required:        req.Required,
		DefaultValue:    req.DefaultValue,
		EnumValues:      req.EnumValues,
		UIGroup:         req.UIGroup,
		SortOrder:       req.SortOrder,
		Validation:      req.Validation,
		Conditional:     req.Conditional,
		ReferenceTarget: req.ReferenceTarget,
	}
}

func instanceFieldFromUpsert(req UpsertInstanceFieldRequest) fieldmeta.FieldDefinition {
	return fieldmeta.FieldDefinition{
		Name:            req.Name,
		Label:           req.Label,
		Description:     req.Description,
		DataType:        req.DataType,
		Required:        req.Required,
		DefaultValue:    req.DefaultValue,
		EnumValues:      req.EnumValues,
		UIGroup:         req.UIGroup,
		SortOrder:       req.SortOrder,
		Validation:      req.Validation,
		Conditional:     req.Conditional,
		ReferenceTarget: req.ReferenceTarget,
	}
}

func fieldFromDefinition(def fieldmeta.FieldDefinition) Field {
	return Field{
		Name:            def.Name,
		Label:           def.Label,
		Description:     def.Description,
		DataType:        def.DataType,
		Required:        def.Required,
		DefaultValue:    def.DefaultValue,
		EnumValues:      def.EnumValues,
		UIGroup:         def.UIGroup,
		SortOrder:       def.SortOrder,
		Validation:      def.Validation,
		Conditional:     def.Conditional,
		ReferenceTarget: def.ReferenceTarget,
	}
}

func slugify(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ', r == '-', r == '/', r == '_':
			b.WriteRune('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func fieldNames(fields []InstanceField) []string {
	out := make([]string, 0, len(fields))
	for i := range fields {
		out = append(out, fields[i].Name)
	}
	return out
}
