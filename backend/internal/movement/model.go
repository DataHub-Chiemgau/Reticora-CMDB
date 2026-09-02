// Package movement implements the unified, append-only inventory movement
// ledger (spec §9) plus quantity-based inventory items (spec §6). Every
// inventory movement is an auditable transaction; history is never
// overwritten (enforced by a database trigger and by the absence of update
// operations in this package).
package movement

import "time"

// Movement types (spec §9).
var MovementTypes = map[string]bool{
	"receipt":             true,
	"warehouse_transfer":  true,
	"bin_transfer":        true,
	"assignment":          true,
	"return":              true,
	"deployment":          true,
	"retrieval":           true,
	"reservation":         true,
	"reservation_release": true,
	"repair_transfer":     true,
	"disposal":            true,
	"correction":          true,
}

// Movement is one auditable inventory transaction.
type Movement struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	ItemKind       string    `json:"item_kind"` // asset | quantity_item
	AssetID        string    `json:"asset_id,omitempty"`
	QuantityItemID string    `json:"quantity_item_id,omitempty"`
	MovementType   string    `json:"movement_type"`
	FromLocationID string    `json:"from_location_id,omitempty"`
	ToLocationID   string    `json:"to_location_id,omitempty"`
	Quantity       *float64  `json:"quantity,omitempty"`
	ActorID        string    `json:"actor_id,omitempty"`
	Reason         string    `json:"reason,omitempty"`
	TicketID       string    `json:"ticket_id,omitempty"`
	OrderID        string    `json:"order_id,omitempty"`
	WorkflowRunID  string    `json:"workflow_run_id,omitempty"`
	DocumentID     string    `json:"document_id,omitempty"`
	Notes          string    `json:"notes,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// CreateMovementRequest is the payload for recording a movement.
type CreateMovementRequest struct {
	ItemKind       string   `json:"item_kind,omitempty"`
	AssetID        string   `json:"asset_id,omitempty"`
	QuantityItemID string   `json:"quantity_item_id,omitempty"`
	MovementType   string   `json:"movement_type"`
	FromLocationID string   `json:"from_location_id,omitempty"`
	ToLocationID   string   `json:"to_location_id,omitempty"`
	Quantity       *float64 `json:"quantity,omitempty"`
	Reason         string   `json:"reason,omitempty"`
	TicketID       string   `json:"ticket_id,omitempty"`
	OrderID        string   `json:"order_id,omitempty"`
	WorkflowRunID  string   `json:"workflow_run_id,omitempty"`
	DocumentID     string   `json:"document_id,omitempty"`
	Notes          string   `json:"notes,omitempty"`
}

// MovementFilter holds query filters for listing movements.
type MovementFilter struct {
	AssetID        string
	QuantityItemID string
	MovementType   string
	LocationID     string
}

// QuantityItem is a quantity-tracked inventory item (cables, adapters,
// consumables) without serialized CI identity (spec §6).
type QuantityItem struct {
	ID             string         `json:"id"`
	OrganizationID string         `json:"organization_id"`
	ClientID       string         `json:"client_id,omitempty"`
	SKU            string         `json:"sku,omitempty"`
	Name           string         `json:"name"`
	Category       string         `json:"category"`
	Unit           string         `json:"unit"`
	StockLevel     float64        `json:"stock_level"`
	MinLevel       float64        `json:"min_level"`
	LocationID     string         `json:"location_id,omitempty"`
	Notes          string         `json:"notes,omitempty"`
	Attributes     map[string]any `json:"attributes"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

// CreateItemRequest is the payload for creating a quantity item.
type CreateItemRequest struct {
	ClientID   string         `json:"client_id,omitempty"`
	SKU        string         `json:"sku,omitempty"`
	Name       string         `json:"name"`
	Category   string         `json:"category,omitempty"`
	Unit       string         `json:"unit,omitempty"`
	StockLevel float64        `json:"stock_level,omitempty"`
	MinLevel   float64        `json:"min_level,omitempty"`
	LocationID string         `json:"location_id,omitempty"`
	Notes      string         `json:"notes,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// UpdateItemRequest is the payload for updating a quantity item.
type UpdateItemRequest struct {
	Name       *string        `json:"name,omitempty"`
	Category   *string        `json:"category,omitempty"`
	Unit       *string        `json:"unit,omitempty"`
	MinLevel   *float64       `json:"min_level,omitempty"`
	LocationID *string        `json:"location_id,omitempty"`
	Notes      *string        `json:"notes,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
}
