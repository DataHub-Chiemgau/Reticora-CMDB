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
	"errors"
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
	Kind     Kind
	ParentID string
	ClientID string
	Name     string
}

// Errors of the repository.
var (
	ErrNotFound      = errors.New("location not found")
	ErrInvalidKind   = errors.New("invalid location kind")
	ErrInvalidParent = errors.New("parent location not allowed")
	ErrCycle         = errors.New("location move would create a cycle")
	ErrInvalidInput  = errors.New("invalid location input")
)

// Validate checks the request against the parent matrix as far as possible
// without the database.
func (r CreateRequest) Validate() error {
	if !r.Kind.Valid() {
		return ErrInvalidKind
	}
	if r.Name == "" {
		return ErrInvalidInput
	}
	if r.Kind == KindSite {
		if r.ParentID != "" {
			return ErrInvalidParent
		}
		if r.ClientID == "" {
			return ErrInvalidInput
		}
		return nil
	}
	if r.ParentID == "" {
		return ErrInvalidParent
	}
	return nil
}
