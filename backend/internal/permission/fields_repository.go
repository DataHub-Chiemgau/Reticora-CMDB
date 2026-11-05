package permission

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// Level is the access level of a field, an action or an object (E-43).
type Level string

// The levels in ascending order.
const (
	LevelNone  Level = "none"
	LevelRead  Level = "read"
	LevelWrite Level = "write"
)

// rank orders the levels; an unknown level ranks as none (fail-closed).
func (l Level) rank() int {
	switch l {
	case LevelRead:
		return 1
	case LevelWrite:
		return 2
	default:
		return 0
	}
}

// Valid reports whether l is none, read or write.
func (l Level) Valid() bool { return l == LevelNone || l == LevelRead || l == LevelWrite }

// Readable reports whether the level allows reading.
func (l Level) Readable() bool { return l.rank() >= 1 }

// Editable reports whether the level allows writing.
func (l Level) Editable() bool { return l.rank() >= 2 }

func minLevel(a, b Level) Level {
	if a.rank() <= b.rank() {
		return a
	}
	return b
}

func maxLevel(a, b Level) Level {
	if a.rank() >= b.rank() {
		return a
	}
	return b
}

// ErrUnknownReference refuses a CI type, relationship type, action or
// object that does not exist or is not visible to the caller (RLS).
var ErrUnknownReference = errors.New("unknown or invisible reference")

// FieldGrant is a row of field_grant: the level of a role for an object
// kind, optionally of a CI type, and for one target (field, relationship
// type or action) or the whole kind.
type FieldGrant struct {
	ID                 string     `json:"id"`
	OrganizationID     string     `json:"organization_id"`
	RoleID             *string    `json:"role_id"`
	CustomRoleID       *string    `json:"custom_role_id"`
	ObjectKind         ObjectKind `json:"object_kind"`
	CITypeID           *string    `json:"ci_type_id"`
	FieldKey           *string    `json:"field_key"`
	RelationshipTypeID *string    `json:"relationship_type_id"`
	// RelationshipType is the key of the relationship type
	// (relationship_type.key = ci_relationship.rel_type); read only.
	RelationshipType string    `json:"relationship_type,omitempty"`
	Action           *string   `json:"action"`
	Level            Level     `json:"level"`
	CreatedBy        *string   `json:"created_by"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// ObjectGrant is a row of object_grant: the level of a role, custom role or
// team for one object (E-43).
type ObjectGrant struct {
	ID                  string     `json:"id"`
	OrganizationID      string     `json:"organization_id"`
	GranteeRoleID       *string    `json:"grantee_role_id"`
	GranteeCustomRoleID *string    `json:"grantee_custom_role_id"`
	GranteeTeamID       *string    `json:"grantee_team_id"`
	ObjectKind          ObjectKind `json:"object_kind"`
	ObjectID            string     `json:"object_id"`
	Level               Level      `json:"level"`
	CreatedBy           *string    `json:"created_by"`
	CreatedAt           time.Time  `json:"created_at"`
}

// SensitiveField is a row of field_sensitivity.
type SensitiveField struct {
	ID             string     `json:"id"`
	OrganizationID string     `json:"organization_id"`
	ObjectKind     ObjectKind `json:"object_kind"`
	CITypeID       *string    `json:"ci_type_id"`
	FieldKey       string     `json:"field_key"`
	CreatedBy      *string    `json:"created_by"`
	CreatedAt      time.Time  `json:"created_at"`
}

// Snapshot holds every field permission row of an organization; the
// resolver caches it (fieldgrants:<org>).
type Snapshot struct {
	FieldGrants  []FieldGrant     `json:"field_grants"`
	ObjectGrants []ObjectGrant    `json:"object_grants"`
	Sensitive    []SensitiveField `json:"sensitive"`
}

// Empty reports whether the organization has no field permission at all.
func (s *Snapshot) Empty() bool {
	return len(s.FieldGrants) == 0 && len(s.ObjectGrants) == 0 && len(s.Sensitive) == 0
}

// FieldGrantFilter narrows ListFieldGrants; empty fields do not filter.
type FieldGrantFilter struct {
	RoleID       string
	CustomRoleID string
	ObjectKind   ObjectKind
	CITypeID     string
}

// ObjectGrantFilter narrows ListObjectGrants; empty fields do not filter.
type ObjectGrantFilter struct {
	ObjectKind          ObjectKind
	ObjectID            string
	GranteeRoleID       string
	GranteeCustomRoleID string
	GranteeTeamID       string
}

// SensitiveFieldFilter narrows ListSensitiveFields; empty fields do not
// filter.
type SensitiveFieldFilter struct {
	ObjectKind ObjectKind
	CITypeID   string
}

// ExportedFieldGrant is a field grant with keys instead of IDs (WP-726).
type ExportedFieldGrant struct {
	Role             string     `json:"role"`
	RoleKind         string     `json:"role_kind"`
	ObjectKind       ObjectKind `json:"object_kind"`
	CIType           string     `json:"ci_type,omitempty"`
	FieldKey         string     `json:"field_key,omitempty"`
	RelationshipType string     `json:"relationship_type,omitempty"`
	Action           string     `json:"action,omitempty"`
	Level            Level      `json:"level"`
}

// ExportedSensitiveField is a sensitivity mark with keys instead of IDs.
type ExportedSensitiveField struct {
	ObjectKind ObjectKind `json:"object_kind"`
	CIType     string     `json:"ci_type,omitempty"`
	FieldKey   string     `json:"field_key"`
}

// FieldGrantExport is the field permission configuration of an
// organization in a stable order, without object grants.
type FieldGrantExport struct {
	FieldGrants []ExportedFieldGrant     `json:"field_grants"`
	Sensitivity []ExportedSensitiveField `json:"field_sensitivity"`
}

// Role kinds of ExportedFieldGrant.RoleKind.
const (
	RoleKindRole       = "role"
	RoleKindCustomRole = "custom_role"
)

// FieldGrantRepository stores field_grant, object_grant and
// field_sensitivity (RBA-07). CRUD and lists run in the request's tenant
// transaction; Snapshot, TeamIDs and TypeChain read org-wide for the
// resolver. Get, update and delete of a missing or invisible row answer
// api.ErrNotFound. Lists page by (created_at, id) and return the cursor of
// the next page, empty on the last one.
type FieldGrantRepository interface {
	CreateFieldGrant(ctx context.Context, orgID string, g *FieldGrant) (FieldGrant, error)
	GetFieldGrant(ctx context.Context, orgID, id string) (FieldGrant, error)
	UpdateFieldGrantLevel(ctx context.Context, orgID, id string, level Level) (FieldGrant, error)
	DeleteFieldGrant(ctx context.Context, orgID, id string) error
	// A nil filter lists every row.
	ListFieldGrants(ctx context.Context, orgID string, f *FieldGrantFilter, page api.PaginationParams) ([]FieldGrant, string, error)

	CreateObjectGrant(ctx context.Context, orgID string, g *ObjectGrant) (ObjectGrant, error)
	GetObjectGrant(ctx context.Context, orgID, id string) (ObjectGrant, error)
	DeleteObjectGrant(ctx context.Context, orgID, id string) error
	ListObjectGrants(ctx context.Context, orgID string, f *ObjectGrantFilter, page api.PaginationParams) ([]ObjectGrant, string, error)

	CreateSensitiveField(ctx context.Context, orgID string, s *SensitiveField) (SensitiveField, error)
	GetSensitiveField(ctx context.Context, orgID, id string) (SensitiveField, error)
	DeleteSensitiveField(ctx context.Context, orgID, id string) error
	ListSensitiveFields(ctx context.Context, orgID string, f *SensitiveFieldFilter, page api.PaginationParams) ([]SensitiveField, string, error)

	// Snapshot returns every row of the organization (org-wide).
	Snapshot(ctx context.Context, orgID string) (Snapshot, error)
	// TeamIDs returns the teams the user is a member of (org-wide).
	TeamIDs(ctx context.Context, orgID, userID string) ([]string, error)
	// TypeChain maps every visible CI type with a parent to its parent
	// (ci_type.parent_type_id, org-wide).
	TypeChain(ctx context.Context, orgID string) (map[string]string, error)
	// Export returns grants and sensitivity with keys instead of IDs in the
	// request's tenant transaction (joins the snapshot transaction of the
	// v2 export, WP-262).
	Export(ctx context.Context, orgID string) (FieldGrantExport, error)
}

// fieldError is a 422 field error with a fixed rule text.
func fieldError(field, message string) error {
	return &api.FieldError{Field: field, Message: message, Status: http.StatusUnprocessableEntity}
}

// checkFieldGrant validates a field grant before it is stored: exactly one
// role, a known kind and level, a CI type only for ci and asset, at most one
// target, a relationship type only for ci and relationship, an action only
// with none or write, and a field key of the kind (ParseFieldKey).
func checkFieldGrant(g *FieldGrant) error {
	switch {
	case (g.RoleID == nil) == (g.CustomRoleID == nil):
		return fieldError("role_id", "exactly one of role_id and custom_role_id is required")
	case !g.ObjectKind.Valid():
		return fieldError("object_kind", "unknown object kind")
	case !g.Level.Valid():
		return fieldError("level", "level must be none, read or write")
	case g.CITypeID != nil && !g.ObjectKind.typed():
		return fieldError("ci_type_id", "a CI type applies to ci and asset only")
	case countSet(g.FieldKey, g.RelationshipTypeID, g.Action) > 1:
		return fieldError("field_key", "at most one of field_key, relationship_type_id and action is allowed")
	case g.RelationshipTypeID != nil && g.ObjectKind != KindCI && g.ObjectKind != KindRelationship:
		return fieldError("relationship_type_id", "a relationship type applies to ci and relationship only")
	case g.Action != nil && g.Level == LevelRead:
		return fieldError("level", "an action grant is none or write")
	}
	if g.FieldKey != nil {
		if _, err := ParseFieldKey(g.ObjectKind, *g.FieldKey); err != nil {
			return err
		}
	}
	return nil
}

// checkObjectGrant validates an object grant: exactly one grantee, a known
// kind and level and an object id.
func checkObjectGrant(g *ObjectGrant) error {
	switch {
	case countSet(g.GranteeRoleID, g.GranteeCustomRoleID, g.GranteeTeamID) != 1:
		return fieldError("grantee_role_id", "exactly one of grantee_role_id, grantee_custom_role_id and grantee_team_id is required")
	case !g.ObjectKind.Valid():
		return fieldError("object_kind", "unknown object kind")
	case !g.Level.Valid():
		return fieldError("level", "level must be none, read or write")
	case !api.IsUUID(g.ObjectID):
		return fieldError("object_id", "object_id must be a UUID")
	}
	return nil
}

// checkSensitiveField validates a sensitivity mark.
func checkSensitiveField(s *SensitiveField) error {
	switch {
	case !s.ObjectKind.Valid():
		return fieldError("object_kind", "unknown object kind")
	case s.CITypeID != nil && !s.ObjectKind.typed():
		return fieldError("ci_type_id", "a CI type applies to ci and asset only")
	}
	_, err := ParseFieldKey(s.ObjectKind, s.FieldKey)
	return err
}

func countSet(values ...*string) int {
	n := 0
	for _, v := range values {
		if v != nil {
			n++
		}
	}
	return n
}

// cursorTime is the sort value of the list cursors: created_at in UTC with
// fixed microseconds, so string order is time order.
const cursorTime = "2006-01-02T15:04:05.000000Z"

func cursorValue(t time.Time) string { return t.UTC().Format(cursorTime) }

// nextCursor returns the cursor after the last of limit rows when more
// exist.
func nextCursor(more bool, created time.Time, id string) string {
	if !more {
		return ""
	}
	return api.EncodeCursor(api.Cursor{Sort: "created_at", Dir: "asc", Value: cursorValue(created), ID: id})
}

// checkCursor refuses a cursor of another list order.
func checkCursor(page api.PaginationParams) error {
	if page.CursorError != nil {
		return page.CursorError
	}
	if page.Cursor != nil && (page.Cursor.Sort != "created_at" || page.Cursor.Dir != "asc") {
		return fmt.Errorf("%w: not issued for this list", api.ErrInvalidCursor)
	}
	return nil
}

func pageLimit(page api.PaginationParams) int {
	if page.Limit <= 0 {
		return api.DefaultPageLimit
	}
	return min(page.Limit, api.MaxPageLimit)
}

// MemoryFieldGrants is the in-memory FieldGrantRepository for tests and
// --no-db. Attribute and instance keys are checked for syntax only; CI
// types, relationship types and objects are not checked. The type chain
// and the team memberships are set with SetTypeParent and AddTeamMember.
type MemoryFieldGrants struct {
	mu        sync.RWMutex
	field     map[string][]FieldGrant
	object    map[string][]ObjectGrant
	sensitive map[string][]SensitiveField
	parents   map[string]map[string]string
	teams     map[string]map[string][]string
	// relationshipTypes maps relationship type ids to their key.
	relationshipTypes map[string]string
	now               func() time.Time
}

// NewMemoryFieldGrants creates an empty memory repository.
func NewMemoryFieldGrants() *MemoryFieldGrants {
	return &MemoryFieldGrants{
		field:             map[string][]FieldGrant{},
		object:            map[string][]ObjectGrant{},
		sensitive:         map[string][]SensitiveField{},
		parents:           map[string]map[string]string{},
		teams:             map[string]map[string][]string{},
		relationshipTypes: map[string]string{},
		now:               func() time.Time { return time.Now().UTC() },
	}
}

var _ FieldGrantRepository = (*MemoryFieldGrants)(nil)

// SetTypeParent records parent as the parent type of typeID.
func (m *MemoryFieldGrants) SetTypeParent(orgID, typeID, parent string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.parents[orgID] == nil {
		m.parents[orgID] = map[string]string{}
	}
	m.parents[orgID][typeID] = parent
}

// AddTeamMember records userID as a member of teamID.
func (m *MemoryFieldGrants) AddTeamMember(orgID, teamID, userID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.teams[orgID] == nil {
		m.teams[orgID] = map[string][]string{}
	}
	if !slices.Contains(m.teams[orgID][userID], teamID) {
		m.teams[orgID][userID] = append(m.teams[orgID][userID], teamID)
	}
}

// SetRelationshipType records the key of a relationship type id.
func (m *MemoryFieldGrants) SetRelationshipType(id, key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.relationshipTypes[id] = key
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// errDuplicate mirrors the unique violation of the tables (409).
var errDuplicate = &api.FieldError{Field: "id", Message: "an equal entry exists", Status: http.StatusConflict}

func sameString(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (m *MemoryFieldGrants) CreateFieldGrant(_ context.Context, orgID string, g *FieldGrant) (FieldGrant, error) {
	if err := checkFieldGrant(g); err != nil {
		return FieldGrant{}, err
	}
	if g.Action != nil && !inCatalogue(*g.Action) {
		return FieldGrant{}, ErrUnknownReference
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.field[orgID] {
		e := &m.field[orgID][i]
		if sameString(e.RoleID, g.RoleID) && sameString(e.CustomRoleID, g.CustomRoleID) && e.ObjectKind == g.ObjectKind &&
			sameString(e.CITypeID, g.CITypeID) && sameString(e.FieldKey, g.FieldKey) &&
			sameString(e.RelationshipTypeID, g.RelationshipTypeID) && sameString(e.Action, g.Action) {
			return FieldGrant{}, errDuplicate
		}
	}
	item := *g
	item.ID, item.OrganizationID = newID(), orgID
	item.CreatedAt = m.now()
	item.UpdatedAt = item.CreatedAt
	if item.RelationshipTypeID != nil {
		item.RelationshipType = m.relationshipTypes[*item.RelationshipTypeID]
	}
	m.field[orgID] = append(m.field[orgID], item)
	return item, nil
}

func (m *MemoryFieldGrants) GetFieldGrant(_ context.Context, orgID, id string) (FieldGrant, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := range m.field[orgID] {
		if m.field[orgID][i].ID == id {
			return m.field[orgID][i], nil
		}
	}
	return FieldGrant{}, api.ErrNotFound
}

func (m *MemoryFieldGrants) UpdateFieldGrantLevel(_ context.Context, orgID, id string, level Level) (FieldGrant, error) {
	if !level.Valid() {
		return FieldGrant{}, fieldError("level", "level must be none, read or write")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.field[orgID] {
		g := &m.field[orgID][i]
		if g.ID != id {
			continue
		}
		if g.Action != nil && level == LevelRead {
			return FieldGrant{}, fieldError("level", "an action grant is none or write")
		}
		g.Level, g.UpdatedAt = level, m.now()
		return *g, nil
	}
	return FieldGrant{}, api.ErrNotFound
}

func (m *MemoryFieldGrants) DeleteFieldGrant(_ context.Context, orgID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := len(m.field[orgID])
	m.field[orgID] = slices.DeleteFunc(m.field[orgID], func(g FieldGrant) bool { return g.ID == id })
	if len(m.field[orgID]) == n {
		return api.ErrNotFound
	}
	return nil
}

// memoryPage pages items sorted by (created_at, id) after the cursor.
func memoryPage[T any](items []T, page api.PaginationParams, key func(*T) (time.Time, string)) (rows []T, next string, err error) {
	if err := checkCursor(page); err != nil {
		return nil, "", err
	}
	sorted := slices.Clone(items)
	slices.SortFunc(sorted, func(a, b T) int {
		ta, ia := key(&a)
		tb, ib := key(&b)
		if c := ta.Compare(tb); c != 0 {
			return c
		}
		return cmp.Compare(ia, ib)
	})
	out := make([]T, 0)
	skip := page.Offset
	limit := pageLimit(page)
	for i := range sorted {
		created, id := key(&sorted[i])
		if page.Cursor != nil && !api.KeysetCompare(cursorValue(created), id, *page.Cursor, "asc") {
			continue
		}
		if page.Cursor == nil && skip > 0 {
			skip--
			continue
		}
		if len(out) == limit {
			last := &out[len(out)-1]
			lc, lid := key(last)
			return out, nextCursor(true, lc, lid), nil
		}
		out = append(out, sorted[i])
	}
	return out, "", nil
}

func (m *MemoryFieldGrants) ListFieldGrants(_ context.Context, orgID string, f *FieldGrantFilter, page api.PaginationParams) ([]FieldGrant, string, error) {
	if f == nil {
		f = &FieldGrantFilter{}
	}
	m.mu.RLock()
	items := slices.DeleteFunc(slices.Clone(m.field[orgID]), func(g FieldGrant) bool {
		return (f.RoleID != "" && !sameString(g.RoleID, &f.RoleID)) ||
			(f.CustomRoleID != "" && !sameString(g.CustomRoleID, &f.CustomRoleID)) ||
			(f.ObjectKind != "" && g.ObjectKind != f.ObjectKind) ||
			(f.CITypeID != "" && !sameString(g.CITypeID, &f.CITypeID))
	})
	m.mu.RUnlock()
	return memoryPage(items, page, func(g *FieldGrant) (time.Time, string) { return g.CreatedAt, g.ID })
}

func (m *MemoryFieldGrants) CreateObjectGrant(_ context.Context, orgID string, g *ObjectGrant) (ObjectGrant, error) {
	if err := checkObjectGrant(g); err != nil {
		return ObjectGrant{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.object[orgID] {
		e := &m.object[orgID][i]
		if sameString(e.GranteeRoleID, g.GranteeRoleID) && sameString(e.GranteeCustomRoleID, g.GranteeCustomRoleID) &&
			sameString(e.GranteeTeamID, g.GranteeTeamID) && e.ObjectKind == g.ObjectKind && e.ObjectID == g.ObjectID {
			return ObjectGrant{}, errDuplicate
		}
	}
	item := *g
	item.ID, item.OrganizationID, item.CreatedAt = newID(), orgID, m.now()
	m.object[orgID] = append(m.object[orgID], item)
	return item, nil
}

func (m *MemoryFieldGrants) GetObjectGrant(_ context.Context, orgID, id string) (ObjectGrant, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := range m.object[orgID] {
		if m.object[orgID][i].ID == id {
			return m.object[orgID][i], nil
		}
	}
	return ObjectGrant{}, api.ErrNotFound
}

func (m *MemoryFieldGrants) DeleteObjectGrant(_ context.Context, orgID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := len(m.object[orgID])
	m.object[orgID] = slices.DeleteFunc(m.object[orgID], func(g ObjectGrant) bool { return g.ID == id })
	if len(m.object[orgID]) == n {
		return api.ErrNotFound
	}
	return nil
}

func (m *MemoryFieldGrants) ListObjectGrants(_ context.Context, orgID string, f *ObjectGrantFilter, page api.PaginationParams) ([]ObjectGrant, string, error) {
	if f == nil {
		f = &ObjectGrantFilter{}
	}
	m.mu.RLock()
	items := slices.DeleteFunc(slices.Clone(m.object[orgID]), func(g ObjectGrant) bool {
		return (f.ObjectKind != "" && g.ObjectKind != f.ObjectKind) ||
			(f.ObjectID != "" && g.ObjectID != f.ObjectID) ||
			(f.GranteeRoleID != "" && !sameString(g.GranteeRoleID, &f.GranteeRoleID)) ||
			(f.GranteeCustomRoleID != "" && !sameString(g.GranteeCustomRoleID, &f.GranteeCustomRoleID)) ||
			(f.GranteeTeamID != "" && !sameString(g.GranteeTeamID, &f.GranteeTeamID))
	})
	m.mu.RUnlock()
	return memoryPage(items, page, func(g *ObjectGrant) (time.Time, string) { return g.CreatedAt, g.ID })
}

func (m *MemoryFieldGrants) CreateSensitiveField(_ context.Context, orgID string, s *SensitiveField) (SensitiveField, error) {
	if err := checkSensitiveField(s); err != nil {
		return SensitiveField{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.sensitive[orgID] {
		e := &m.sensitive[orgID][i]
		if e.ObjectKind == s.ObjectKind && sameString(e.CITypeID, s.CITypeID) && e.FieldKey == s.FieldKey {
			return SensitiveField{}, errDuplicate
		}
	}
	item := *s
	item.ID, item.OrganizationID, item.CreatedAt = newID(), orgID, m.now()
	m.sensitive[orgID] = append(m.sensitive[orgID], item)
	return item, nil
}

func (m *MemoryFieldGrants) GetSensitiveField(_ context.Context, orgID, id string) (SensitiveField, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := range m.sensitive[orgID] {
		if m.sensitive[orgID][i].ID == id {
			return m.sensitive[orgID][i], nil
		}
	}
	return SensitiveField{}, api.ErrNotFound
}

func (m *MemoryFieldGrants) DeleteSensitiveField(_ context.Context, orgID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := len(m.sensitive[orgID])
	m.sensitive[orgID] = slices.DeleteFunc(m.sensitive[orgID], func(s SensitiveField) bool { return s.ID == id })
	if len(m.sensitive[orgID]) == n {
		return api.ErrNotFound
	}
	return nil
}

func (m *MemoryFieldGrants) ListSensitiveFields(_ context.Context, orgID string, f *SensitiveFieldFilter, page api.PaginationParams) ([]SensitiveField, string, error) {
	if f == nil {
		f = &SensitiveFieldFilter{}
	}
	m.mu.RLock()
	items := slices.DeleteFunc(slices.Clone(m.sensitive[orgID]), func(s SensitiveField) bool {
		return (f.ObjectKind != "" && s.ObjectKind != f.ObjectKind) ||
			(f.CITypeID != "" && !sameString(s.CITypeID, &f.CITypeID))
	})
	m.mu.RUnlock()
	return memoryPage(items, page, func(s *SensitiveField) (time.Time, string) { return s.CreatedAt, s.ID })
}

func (m *MemoryFieldGrants) Snapshot(_ context.Context, orgID string) (Snapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return Snapshot{
		FieldGrants:  slices.Clone(m.field[orgID]),
		ObjectGrants: slices.Clone(m.object[orgID]),
		Sensitive:    slices.Clone(m.sensitive[orgID]),
	}, nil
}

func (m *MemoryFieldGrants) TeamIDs(_ context.Context, orgID, userID string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	teams := slices.Clone(m.teams[orgID][userID])
	slices.Sort(teams)
	return teams, nil
}

func (m *MemoryFieldGrants) TypeChain(_ context.Context, orgID string) (map[string]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]string, len(m.parents[orgID]))
	for k, v := range m.parents[orgID] {
		out[k] = v
	}
	return out, nil
}

// Export names roles, types and relationship types by their IDs: the
// memory repository holds no names.
func (m *MemoryFieldGrants) Export(_ context.Context, orgID string) (FieldGrantExport, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := FieldGrantExport{FieldGrants: []ExportedFieldGrant{}, Sensitivity: []ExportedSensitiveField{}}
	for i := range m.field[orgID] {
		g := &m.field[orgID][i]
		e := ExportedFieldGrant{ObjectKind: g.ObjectKind, Level: g.Level, RoleKind: RoleKindRole,
			CIType: deref(g.CITypeID), FieldKey: deref(g.FieldKey), RelationshipType: g.RelationshipType, Action: deref(g.Action)}
		if g.RoleID != nil {
			e.Role = *g.RoleID
		} else {
			e.Role, e.RoleKind = deref(g.CustomRoleID), RoleKindCustomRole
		}
		out.FieldGrants = append(out.FieldGrants, e)
	}
	for i := range m.sensitive[orgID] {
		s := &m.sensitive[orgID][i]
		out.Sensitivity = append(out.Sensitivity, ExportedSensitiveField{ObjectKind: s.ObjectKind, CIType: deref(s.CITypeID), FieldKey: s.FieldKey})
	}
	sortExport(&out)
	return out, nil
}

// inCatalogue reports whether key is a key of Catalogue.
func inCatalogue(key string) bool {
	return slices.ContainsFunc(Catalogue, func(p Permission) bool { return p.Key == key })
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// sortExport orders the export stably by its keys.
func sortExport(e *FieldGrantExport) {
	slices.SortFunc(e.FieldGrants, func(a, b ExportedFieldGrant) int {
		return cmp.Or(cmp.Compare(a.RoleKind, b.RoleKind), cmp.Compare(a.Role, b.Role), cmp.Compare(a.ObjectKind, b.ObjectKind),
			cmp.Compare(a.CIType, b.CIType), cmp.Compare(a.FieldKey, b.FieldKey), cmp.Compare(a.RelationshipType, b.RelationshipType),
			cmp.Compare(a.Action, b.Action))
	})
	slices.SortFunc(e.Sensitivity, func(a, b ExportedSensitiveField) int {
		return cmp.Or(cmp.Compare(a.ObjectKind, b.ObjectKind), cmp.Compare(a.CIType, b.CIType), cmp.Compare(a.FieldKey, b.FieldKey))
	})
}

// permissionOf is the object permission <kind>:<action>.
func permissionOf(kind ObjectKind, action string) identity.Permission {
	return identity.Permission(string(kind) + ":" + action)
}
