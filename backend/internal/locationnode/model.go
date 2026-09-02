// Package locationnode implements the generalized location model (spec §7):
// a single self-referential hierarchy covering deployment locations
// (site/building/floor/room/rack), logistical locations
// (warehouse/zone/shelf/bin), and custom nodes (desk, vehicle, customer,
// logical). The existing site/building/room/rack tables keep working; nodes
// can reference them via back-references so visualization features keep
// functioning on one model.
package locationnode

import "time"

// Node types of the generalized location hierarchy (spec §7).
var NodeTypes = map[string]bool{
	"site": true, "building": true, "floor": true, "room": true,
	"warehouse": true, "zone": true, "shelf": true, "bin": true,
	"rack": true, "desk": true, "vehicle": true, "customer": true,
	"logical": true, "custom": true,
}

// Node is one location node in the hierarchy.
type Node struct {
	ID             string         `json:"id"`
	OrganizationID string         `json:"organization_id"`
	ClientID       string         `json:"client_id,omitempty"`
	ParentID       string         `json:"parent_id,omitempty"`
	NodeType       string         `json:"node_type"`
	Name           string         `json:"name"`
	SiteID         string         `json:"site_id,omitempty"`
	BuildingID     string         `json:"building_id,omitempty"`
	RoomID         string         `json:"room_id,omitempty"`
	RackID         string         `json:"rack_id,omitempty"`
	Barcode        string         `json:"barcode,omitempty"`
	Attributes     map[string]any `json:"attributes"`
	SortOrder      int            `json:"sort_order"`
	Children       []Node         `json:"children,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

// CreateRequest is the payload for creating a location node.
type CreateRequest struct {
	ClientID   string         `json:"client_id,omitempty"`
	ParentID   string         `json:"parent_id,omitempty"`
	NodeType   string         `json:"node_type"`
	Name       string         `json:"name"`
	SiteID     string         `json:"site_id,omitempty"`
	BuildingID string         `json:"building_id,omitempty"`
	RoomID     string         `json:"room_id,omitempty"`
	RackID     string         `json:"rack_id,omitempty"`
	Barcode    string         `json:"barcode,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
	SortOrder  int            `json:"sort_order,omitempty"`
}

// UpdateRequest is the payload for updating a location node.
type UpdateRequest struct {
	Name       *string        `json:"name,omitempty"`
	ParentID   *string        `json:"parent_id,omitempty"`
	Barcode    *string        `json:"barcode,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
	SortOrder  *int           `json:"sort_order,omitempty"`
}

// FilterParams holds query filters for listing nodes.
type FilterParams struct {
	ParentID string
	NodeType string
	RootOnly bool
	Search   string
}
