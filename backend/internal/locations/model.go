// Package locations implements the canonical location tree (LOC-10, CH28):
// sites, buildings, rooms and racks as well as warehouses, zones, shelves and
// bins are nodes of one tree in table location. The database derives path,
// site and client of every node from its parent and enforces the parent
// matrix and the cycle guard (migration 000062); this package mirrors the
// matrix to reject bad requests early and maps the database errors.
//
// Sites, buildings, rooms and racks keep their specialist tables, which share
// the primary key with their location row. They are created and moved through
// those tables; their triggers keep the location row in step.
package locations

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Kind is the kind of a location node.
type Kind string

// Kinds of the location tree (LOC-10).
const (
	KindSite      Kind = "site"
	KindBuilding  Kind = "building"
	KindRoom      Kind = "room"
	KindRack      Kind = "rack"
	KindWarehouse Kind = "warehouse"
	KindZone      Kind = "zone"
	KindShelf     Kind = "shelf"
	KindBin       Kind = "bin"
)

// parentKinds is the parent matrix of LOC-10. The site is the root and has
// no entry. It must match location_parent_kind in migration 000062.
var parentKinds = map[Kind]Kind{
	KindBuilding:  KindSite,
	KindRoom:      KindBuilding,
	KindRack:      KindRoom,
	KindWarehouse: KindSite,
	KindZone:      KindWarehouse,
	KindShelf:     KindZone,
	KindBin:       KindShelf,
}

// specialistParentColumns names, per kind with a specialist table, the column
// of that table that references the parent.
var specialistParentColumns = map[Kind]string{
	KindSite:     "",
	KindBuilding: "site_id",
	KindRoom:     "building_id",
	KindRack:     "room_id",
}

// Valid reports whether k is a kind of the location tree.
func (k Kind) Valid() bool {
	_, ok := parentKinds[k]
	return ok || k == KindSite
}

// ParentKind returns the only kind a node of kind k may have as parent; ok is
// false for the root kind site and for unknown kinds.
func (k Kind) ParentKind() (parent Kind, ok bool) {
	parent, ok = parentKinds[k]
	return parent, ok
}

// HasSpecialistTable reports whether nodes of kind k live in a specialist
// table of the same name (site, building, room, rack).
func (k Kind) HasSpecialistTable() bool {
	_, ok := specialistParentColumns[k]
	return ok
}

// Location is one node of the tree. ClientID, SiteID and Path are derived by
// the database.
type Location struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	ClientID       string    `json:"client_id"`
	SiteID         string    `json:"site_id"`
	ParentID       string    `json:"parent_id,omitempty"`
	Kind           Kind      `json:"kind"`
	Name           string    `json:"name"`
	Path           string    `json:"path"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// CreateRequest creates a node. ClientID is required for a site and ignored
// otherwise, because every other node inherits the client of its site.
type CreateRequest struct {
	Kind     Kind   `json:"kind"`
	ParentID string `json:"parent_id,omitempty"`
	ClientID string `json:"client_id,omitempty"`
	Name     string `json:"name"`
}

// UpdateRequest renames and/or moves a node; nil fields stay unchanged.
type UpdateRequest struct {
	Name     *string `json:"name,omitempty"`
	ParentID *string `json:"parent_id,omitempty"`
}

// Filter narrows a listing.
type Filter struct {
	ParentID string
	Kind     Kind
	RootOnly bool
	Search   string
}

// Repository reads and writes the location tree.
type Repository interface {
	List(ctx context.Context, orgID string, filter Filter) ([]Location, error)
	Create(ctx context.Context, orgID string, req CreateRequest) (*Location, error)
	Get(ctx context.Context, orgID, id string) (*Location, error)
	Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Location, error)
	Delete(ctx context.Context, orgID, id string) error
}

// Errors of the repository.
var (
	ErrNotFound      = errors.New("location not found")
	ErrInvalidKind   = errors.New("invalid location kind")
	ErrInvalidParent = errors.New("parent location not allowed")
	ErrCycle         = errors.New("location move would create a cycle")
	ErrInvalidInput  = errors.New("invalid location input")
	ErrHasChildren   = errors.New("location has child locations")
	ErrInUse         = errors.New("location is still referenced")
)

// FieldError names the request field a validation error is about, for the
// RFC 7807 response. It wraps one of the repository errors.
type FieldError struct {
	Field   string
	Message string
	Err     error
}

func (e *FieldError) Error() string { return fmt.Sprintf("%s: %s", e.Field, e.Message) }

// Unwrap returns the repository error.
func (e *FieldError) Unwrap() error { return e.Err }

func fieldError(field string, err error, format string, args ...any) error {
	return &FieldError{Field: field, Message: fmt.Sprintf(format, args...), Err: err}
}

// withField attaches field to err unless it already names one or is not a
// validation error.
func withField(field string, err error) error {
	var fe *FieldError
	if err == nil || errors.As(err, &fe) {
		return err
	}
	if errors.Is(err, ErrInvalidParent) || errors.Is(err, ErrCycle) || errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrInvalidKind) {
		return &FieldError{Field: field, Message: err.Error(), Err: err}
	}
	return err
}

// Validate checks the request against the parent matrix as far as possible
// without the database.
// The error is a *FieldError naming the offending field.
func (r CreateRequest) Validate() error {
	if !r.Kind.Valid() {
		return fieldError("kind", ErrInvalidKind, "unknown kind %q (site, building, room, rack, warehouse, zone, shelf or bin)", r.Kind)
	}
	if r.Name == "" {
		return fieldError("name", ErrInvalidInput, "name is required")
	}
	if r.Kind == KindSite {
		if r.ParentID != "" {
			return fieldError("parent_id", ErrInvalidParent, "a site is a root and has no parent")
		}
		if r.ClientID == "" {
			return fieldError("client_id", ErrInvalidInput, "client_id is required for a site")
		}
		return nil
	}
	if r.ParentID == "" {
		parent, _ := r.Kind.ParentKind()
		return fieldError("parent_id", ErrInvalidParent, "a %s needs a parent of kind %s", r.Kind, parent)
	}
	return nil
}
