package citype

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
)

// Repository defines persistence operations for CI types, field definitions
// and CI-instance field definitions.
type Repository interface {
	List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Type, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Type, error)
	Create(ctx context.Context, typ *Type) error
	Update(ctx context.Context, orgID, id string, req UpdateTypeRequest) (*Type, error)
	// Clone copies a type (including its field definitions) under a new key.
	Clone(ctx context.Context, orgID, id string, req CloneRequest) (*Type, error)
	// SetActive deactivates/reactivates a type. Deactivated types keep their
	// CIs readable but cannot be selected for new CIs.
	SetActive(ctx context.Context, orgID, id string, active bool) (*Type, error)
	// Versions returns the clone/version lineage of a type.
	Versions(ctx context.Context, orgID, id string) ([]Type, error)

	ListFields(ctx context.Context, orgID, typeID string) ([]Field, error)
	ListGlobalFields(ctx context.Context, orgID string) ([]Field, error)
	UpsertField(ctx context.Context, orgID, typeID string, req UpsertFieldRequest) (*Field, error)
	DeleteField(ctx context.Context, orgID, typeID, name string) error

	ListInstanceFields(ctx context.Context, orgID, ciID string) ([]InstanceField, error)
	UpsertInstanceField(ctx context.Context, orgID, ciID string, req UpsertInstanceFieldRequest) (*InstanceField, error)
	DeleteInstanceField(ctx context.Context, orgID, ciID, name string) error
}

// MemoryRepository is an in-memory implementation (tests, --no-db).
type MemoryRepository struct {
	mu       sync.RWMutex
	types    map[string]*Type
	fields   map[string][]Field            // typeID -> fields
	globals  map[string][]Field            // orgID -> global fields
	inst     map[string][]InstanceField    // ciID -> instance fields
	seq      int
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
			!strings.Contains(strings.ToLower(t.DisplayName), strings.ToLower(filter.Search)) {
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
	for _, existing := range r.types {
		if existing.OrganizationID == typ.OrganizationID &&
			(existing.Key == typ.Key || existing.Name == typ.Name) && existing.IsActive {
			return fmt.Errorf("a CI type with this key or name already exists")
		}
	}
	typ.ID = r.nextID()
	if typ.Key == "" {
		typ.Key = slugify(typ.Name)
	}
	typ.IsActive = true
	typ.Version = 1
	now := time.Now().UTC()
	typ.CreatedAt = now
	typ.UpdatedAt = now
	stored := *typ
	r.types[typ.ID] = &stored
	flds := append([]Field{}, typ.Fields...)
	r.fields[typ.ID] = flds
	return nil
}

func (r *MemoryRepository) Update(_ context.Context, orgID, id string, req UpdateTypeRequest) (*Type, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.types[id]
	if !ok || (t.OrganizationID != "" && t.OrganizationID != orgID) {
		return nil, fmt.Errorf("not found")
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
		t.DisplayName = *req.DisplayName
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
	if req.LifecycleDefinitionID != nil {
		t.LifecycleDefinitionID = *req.LifecycleDefinitionID
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
			return nil, fmt.Errorf("a CI type with this key or name already exists")
		}
	}
	clone := *src
	clone.ID = r.nextID()
	clone.OrganizationID = orgID
	clone.Name = name
	clone.Key = key
	clone.DisplayName = name
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
	if _, ok := r.types[typeID]; !ok {
		return nil, fmt.Errorf("not found")
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
