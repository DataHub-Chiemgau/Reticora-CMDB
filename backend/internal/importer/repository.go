// Package importer holds the file import of Phase 2 (IMP-IO-01, E-38,
// E-49). This file defines the stored mapping profiles (WP-774): the model
// with its structural validation, the repository port and the in-memory
// implementation of the --no-db profile. Import runs, parsers and the
// preview follow with WP-301; the check of the mapping targets against
// metamodel, type and field rights with WP-302.
package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Entities a profile can import (import_profile_entity_check).
var Entities = []string{"ci", "asset", "contract", "consumable", "location", "relationship"}

// Formats of the import file (import_profile_format_check, E-49).
var Formats = []string{"csv", "xlsx", "json"}

// Modes of the write decision (import_profile_mode_check): create_missing
// only creates objects whose identity is unknown, update_existing only
// changes known objects, both does either.
var Modes = []string{"create_missing", "update_existing", "both"}

// Transformation operations, the closed list of IMP-IO-01.
const (
	TransformTrim       = "trim"
	TransformDateFormat = "date_format"
	TransformEnumMap    = "enum_map"
	TransformSplit      = "split"
	TransformConstant   = "constant"
)

// Transformations is the closed list of transformation operations.
var Transformations = []string{TransformTrim, TransformDateFormat, TransformEnumMap, TransformSplit, TransformConstant}

// Limits of a profile; they keep a stored mapping within what one import
// file can carry.
const (
	maxNameLength   = 200
	maxColumns      = 1000
	maxTransforms   = 20
	maxIdentityKeys = 10
	maxTextLength   = 1000
)

// targetPattern is the form of a mapping target: a core field of the
// entity (name), an attribute (attributes.<key>) or a relation
// (relations.<type>). Whether the target exists for the entity and type
// and whether the caller may write it is checked when an import runs
// (WP-302).
var targetPattern = regexp.MustCompile(`^((attributes|relations)\.)?[A-Za-z][A-Za-z0-9_-]{0,99}$`)

// Transform is one step of the transformation list of a column. Op selects
// the operation; the other fields are its parameters: Layout for
// date_format, Values for enum_map, Separator for split and Value for
// constant.
type Transform struct {
	Op        string            `json:"op"`
	Layout    string            `json:"layout,omitempty"`
	Values    map[string]string `json:"values,omitempty"`
	Separator string            `json:"separator,omitempty"`
	Value     *string           `json:"value,omitempty"`
}

// ColumnMapping maps one source column to its target with the ordered list
// of transformations applied to the cell value.
type ColumnMapping struct {
	Target     string      `json:"target"`
	Transforms []Transform `json:"transforms"`
}

// Mapping is the stored mapping of a profile: per source column (header of
// the file, or JSON key) a target, and defaults per target for rows whose
// value is empty or missing.
type Mapping struct {
	Columns  map[string]ColumnMapping `json:"columns"`
	Defaults map[string]string        `json:"defaults"`
}

// Profile is an import mapping profile of an organization.
type Profile struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	Name           string    `json:"name"`
	Description    *string   `json:"description"`
	Entity         string    `json:"entity"`
	Format         string    `json:"format"`
	Mode           string    `json:"mode"`
	Sheet          *string   `json:"sheet"`
	Mapping        Mapping   `json:"mapping"`
	IdentityKeys   []string  `json:"identity_keys"`
	CreatedBy      *string   `json:"created_by"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// CreateProfileRequest is the body of POST /api/v1/import-profiles.
type CreateProfileRequest struct {
	Name         string   `json:"name"`
	Description  *string  `json:"description"`
	Entity       string   `json:"entity"`
	Format       string   `json:"format"`
	Mode         string   `json:"mode"`
	Sheet        *string  `json:"sheet"`
	Mapping      *Mapping `json:"mapping"`
	IdentityKeys []string `json:"identity_keys"`
}

// OptionalText is a nullable text field of a PATCH body: Set reports that
// the field was sent, Value nil that it was sent as null (clears it).
type OptionalText struct {
	Set   bool
	Value *string
}

// UnmarshalJSON records that the field was sent.
func (o *OptionalText) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Value = nil
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	o.Value = &s
	return nil
}

// UpdateProfileRequest is the body of PATCH /api/v1/import-profiles/{id}:
// sent fields change, the merged profile is validated as a whole.
type UpdateProfileRequest struct {
	Name         *string      `json:"name"`
	Description  OptionalText `json:"description"`
	Entity       *string      `json:"entity"`
	Format       *string      `json:"format"`
	Mode         *string      `json:"mode"`
	Sheet        OptionalText `json:"sheet"`
	Mapping      *Mapping     `json:"mapping"`
	IdentityKeys *[]string    `json:"identity_keys"`
}

// ProfileListResponse is the page of GET /api/v1/import-profiles.
type ProfileListResponse = api.ListResponse[Profile]

// Errors of the profile repositories.
var (
	// ErrNotFound: the profile is unknown or of another organization; both
	// answer alike with 404 (TEN-04).
	ErrNotFound = errors.New("import profile not found")
	// errNameTaken: the name is already used in the organization (409).
	errNameTaken = &api.ConflictError{Field: "name", Message: "an import profile with this name already exists"}
)

// Repository stores the import profiles of an organization. List returns
// at most one row more than the page limit, sorted by name and id, and the
// number of profiles of the organization.
type Repository interface {
	List(ctx context.Context, orgID string, page api.PaginationParams) ([]Profile, int, error)
	Get(ctx context.Context, orgID, id string) (*Profile, error)
	Create(ctx context.Context, p *Profile) error
	Update(ctx context.Context, orgID, id string, req *UpdateProfileRequest) (*Profile, error)
	Delete(ctx context.Context, orgID, id string) error
}

// invalid is a validation error of a profile field: 422 with the field.
//
// validation: field names and fixed rule texts.
func invalid(field, message string) *api.FieldError {
	return &api.FieldError{Field: field, Message: message, Status: 422}
}

// newProfile builds the profile of a create request; Validate checks it.
func newProfile(orgID, userID string, req *CreateProfileRequest) *Profile {
	p := &Profile{OrganizationID: orgID, Name: req.Name, Description: req.Description, Entity: req.Entity,
		Format: req.Format, Mode: req.Mode, Sheet: req.Sheet, IdentityKeys: req.IdentityKeys}
	if req.Mapping != nil {
		p.Mapping = *req.Mapping
	}
	if api.IsUUID(userID) {
		p.CreatedBy = &userID
	}
	p.normalize()
	return p
}

// apply merges the sent fields of a PATCH into p.
func (req *UpdateProfileRequest) apply(p *Profile) {
	if req.Name != nil {
		p.Name = *req.Name
	}
	if req.Description.Set {
		p.Description = req.Description.Value
	}
	if req.Entity != nil {
		p.Entity = *req.Entity
	}
	if req.Format != nil {
		p.Format = *req.Format
	}
	if req.Mode != nil {
		p.Mode = *req.Mode
	}
	if req.Sheet.Set {
		p.Sheet = req.Sheet.Value
	}
	if req.Mapping != nil {
		p.Mapping = *req.Mapping
	}
	if req.IdentityKeys != nil {
		p.IdentityKeys = *req.IdentityKeys
	}
	p.normalize()
}

// normalize gives the collections of a profile their empty values, so the
// response carries [] and {} instead of null.
func (p *Profile) normalize() {
	p.Name = strings.TrimSpace(p.Name)
	if p.IdentityKeys == nil {
		p.IdentityKeys = []string{}
	}
	if p.Mapping.Columns == nil {
		p.Mapping.Columns = map[string]ColumnMapping{}
	}
	if p.Mapping.Defaults == nil {
		p.Mapping.Defaults = map[string]string{}
	}
	for col, cm := range p.Mapping.Columns {
		if cm.Transforms == nil {
			cm.Transforms = []Transform{}
			p.Mapping.Columns[col] = cm
		}
	}
}

// Validate checks required fields, the enumerations and the structure of
// the mapping (422 with the field). The targets are only checked for their
// form here; metamodel, type and field rights follow when an import runs
// (WP-302).
//
// validation: field names and fixed rule texts.
func (p *Profile) Validate() error {
	switch {
	case p.Name == "":
		return invalid("name", "is required")
	case utf8.RuneCountInString(p.Name) > maxNameLength:
		return invalid("name", "must be at most 200 characters")
	case p.Description != nil && utf8.RuneCountInString(*p.Description) > maxTextLength:
		return invalid("description", "must be at most 1000 characters")
	case !slices.Contains(Entities, p.Entity):
		return invalid("entity", "must be one of ci, asset, contract, consumable, location, relationship")
	case !slices.Contains(Formats, p.Format):
		return invalid("format", "must be one of csv, xlsx, json")
	case !slices.Contains(Modes, p.Mode):
		return invalid("mode", "must be one of create_missing, update_existing, both")
	}
	if p.Sheet != nil {
		if p.Format != "xlsx" {
			return invalid("sheet", "is only allowed for format xlsx")
		}
		if strings.TrimSpace(*p.Sheet) == "" || utf8.RuneCountInString(*p.Sheet) > maxNameLength {
			return invalid("sheet", "must be a sheet name of 1 to 200 characters")
		}
	}
	targets, err := p.Mapping.validate()
	if err != nil {
		return err
	}
	if len(p.IdentityKeys) > maxIdentityKeys {
		return invalid("identity_keys", "must have at most 10 entries")
	}
	seen := map[string]bool{}
	for i, key := range p.IdentityKeys {
		field := fmt.Sprintf("identity_keys[%d]", i)
		if !targets[key] {
			return invalid(field, "must be a target of the mapping")
		}
		if seen[key] {
			return invalid(field, "is duplicated")
		}
		seen[key] = true
	}
	return nil
}

// validate checks the columns, transformations and defaults of a mapping
// and returns the set of column targets.
//
// validation: field names and fixed rule texts.
func (m *Mapping) validate() (map[string]bool, error) {
	if len(m.Columns) == 0 {
		return nil, invalid("mapping.columns", "must map at least one column")
	}
	if len(m.Columns) > maxColumns {
		return nil, invalid("mapping.columns", "must have at most 1000 columns")
	}
	columns := make([]string, 0, len(m.Columns))
	for col := range m.Columns {
		columns = append(columns, col)
	}
	sort.Strings(columns)
	targets := map[string]bool{}
	for _, col := range columns {
		cm := m.Columns[col]
		field := "mapping.columns." + col
		if strings.TrimSpace(col) == "" || utf8.RuneCountInString(col) > maxNameLength {
			return nil, invalid("mapping.columns", "column names must have 1 to 200 characters")
		}
		if !targetPattern.MatchString(cm.Target) {
			return nil, invalid(field+".target", "must be a field, attributes.<key> or relations.<type>")
		}
		if len(cm.Transforms) > maxTransforms {
			return nil, invalid(field+".transforms", "must have at most 20 entries")
		}
		for i := range cm.Transforms {
			if err := cm.Transforms[i].validate(fmt.Sprintf("%s.transforms[%d]", field, i)); err != nil {
				return nil, err
			}
		}
		targets[cm.Target] = true
	}
	for target, value := range m.Defaults {
		if !targetPattern.MatchString(target) {
			return nil, invalid("mapping.defaults", "keys must be a field, attributes.<key> or relations.<type>")
		}
		if utf8.RuneCountInString(value) > maxTextLength {
			return nil, invalid("mapping.defaults."+target, "must be at most 1000 characters")
		}
	}
	return targets, nil
}

// validate checks the operation and its parameters.
//
// validation: field names and fixed rule texts.
func (t *Transform) validate(field string) error {
	switch t.Op {
	case TransformTrim:
		return nil
	case TransformDateFormat:
		if t.Layout == "" || utf8.RuneCountInString(t.Layout) > maxNameLength {
			return invalid(field+".layout", "is required for date_format (at most 200 characters)")
		}
	case TransformEnumMap:
		if len(t.Values) == 0 {
			return invalid(field+".values", "is required for enum_map")
		}
		if len(t.Values) > maxColumns {
			return invalid(field+".values", "must have at most 1000 entries")
		}
	case TransformSplit:
		if t.Separator == "" || utf8.RuneCountInString(t.Separator) > 10 {
			return invalid(field+".separator", "is required for split (at most 10 characters)")
		}
	case TransformConstant:
		if t.Value == nil {
			return invalid(field+".value", "is required for constant")
		}
		if utf8.RuneCountInString(*t.Value) > maxTextLength {
			return invalid(field+".value", "must be at most 1000 characters")
		}
	default:
		return invalid(field+".op", "must be one of trim, date_format, enum_map, split, constant")
	}
	return nil
}

// Sort order of the list and its keyset cursor (API-02).
const (
	profileSort = "name"
	profileDir  = "asc"
)

// profilePosition returns the (name, id) position of a keyset cursor of
// the profile list; a cursor of another order or with a malformed id is
// api.ErrInvalidCursor.
//
// validation: fixed texts wrapping api.ErrInvalidCursor (API-03).
func profilePosition(c *api.Cursor) (name, id string, err error) {
	if !c.Matches(profileSort, profileDir) {
		return "", "", fmt.Errorf("%w: sort order changed", api.ErrInvalidCursor)
	}
	if !api.IsUUID(c.ID) {
		return "", "", fmt.Errorf("%w: malformed position", api.ErrInvalidCursor)
	}
	return c.Value, c.ID, nil
}

// profilePage is the list response of the rows a repository read (at most
// one more than the limit): when more follow, the page ends at the limit
// and next_cursor is the keyset cursor after its last row.
func profilePage(rows []Profile, total int, page api.PaginationParams) ProfileListResponse {
	list := ProfileListResponse{Data: rows, Total: total, Limit: page.Limit, Offset: page.Offset}
	if page.Cursor != nil {
		list.Offset = 0
	}
	if page.Limit > 0 && len(rows) > page.Limit {
		list.Data = rows[:page.Limit]
		last := list.Data[page.Limit-1]
		list.HasMore = true
		list.NextCursor = api.EncodeCursor(api.Cursor{Sort: profileSort, Dir: profileDir, Value: last.Name, ID: last.ID})
	}
	return list
}

// MemoryRepository is the in-memory profile store of the --no-db profile
// and of tests. Like the database it keeps the name unique per
// organization (409).
type MemoryRepository struct {
	mu       sync.RWMutex
	profiles map[string]Profile
	nextID   int
}

// NewMemoryRepository returns an empty in-memory profile store.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{profiles: map[string]Profile{}}
}

// clone returns a copy of p that shares no collection with the store.
func clone(p *Profile) Profile {
	out := *p
	out.IdentityKeys = slices.Clone(p.IdentityKeys)
	raw, err := json.Marshal(p.Mapping)
	if err == nil {
		out.Mapping = Mapping{}
		if json.Unmarshal(raw, &out.Mapping) != nil {
			out.Mapping = p.Mapping
		}
	}
	out.normalize()
	return out
}

// nameTaken reports whether another profile of the organization has name.
func (r *MemoryRepository) nameTaken(orgID, name, exceptID string) bool {
	for id := range r.profiles {
		p := r.profiles[id]
		if id != exceptID && p.OrganizationID == orgID && p.Name == name {
			return true
		}
	}
	return false
}

// List returns the profiles of the organization sorted by name and id.
func (r *MemoryRepository) List(_ context.Context, orgID string, page api.PaginationParams) ([]Profile, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	all := make([]Profile, 0)
	for id := range r.profiles {
		p := r.profiles[id]
		if p.OrganizationID == orgID {
			all = append(all, clone(&p))
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Name != all[j].Name {
			return all[i].Name < all[j].Name
		}
		return all[i].ID < all[j].ID
	})
	start := min(page.Offset, len(all))
	if page.Cursor != nil {
		name, id, err := profilePosition(page.Cursor)
		if err != nil {
			return nil, 0, err
		}
		start = len(all)
		for i := range all {
			if api.KeysetCompare(all[i].Name, all[i].ID, api.Cursor{Value: name, ID: id}, profileDir) {
				start = i
				break
			}
		}
	}
	end := min(start+max(page.Limit, 0)+1, len(all))
	return all[start:end], len(all), nil
}

// Get returns a profile of the organization.
func (r *MemoryRepository) Get(_ context.Context, orgID, id string) (*Profile, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.profiles[id]
	if !ok || p.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	out := clone(&p)
	return &out, nil
}

// Create validates and stores a profile; a taken name is 409.
func (r *MemoryRepository) Create(_ context.Context, p *Profile) error {
	p.normalize()
	if err := p.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.nameTaken(p.OrganizationID, p.Name, "") {
		return errNameTaken
	}
	r.nextID++
	p.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", r.nextID)
	now := time.Now().UTC()
	p.CreatedAt, p.UpdatedAt = now, now
	r.profiles[p.ID] = clone(p)
	return nil
}

// Update merges the sent fields, validates the result and stores it.
func (r *MemoryRepository) Update(_ context.Context, orgID, id string, req *UpdateProfileRequest) (*Profile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.profiles[id]
	if !ok || stored.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	p := clone(&stored)
	req.apply(&p)
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if r.nameTaken(orgID, p.Name, id) {
		return nil, errNameTaken
	}
	p.UpdatedAt = time.Now().UTC()
	r.profiles[id] = clone(&p)
	return &p, nil
}

// Delete removes a profile of the organization.
func (r *MemoryRepository) Delete(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.profiles[id]
	if !ok || p.OrganizationID != orgID {
		return ErrNotFound
	}
	delete(r.profiles, id)
	return nil
}
