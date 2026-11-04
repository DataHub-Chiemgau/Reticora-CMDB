// Package movement implements the unified, append-only inventory movement
// ledger (spec §9) plus quantity-based inventory items (spec §6). Every
// inventory movement is an auditable transaction; history is never
// overwritten (enforced by a database trigger and by the absence of update
// operations in this package).
package movement

import "time"

// MovementTypes are the movement types of the ledger stock_movement
// (STK-04, stock_movement_movement_type_check).
var MovementTypes = map[string]bool{
	"receipt": true, "transfer": true, "assignment": true, "return": true,
	"deployment": true, "retrieval": true, "reservation": true, "release": true,
	"repair": true, "disposal": true, "correction": true,
}

// LegacyMovementTypes maps the former type names of asset_movement to the
// canonical types, like migration 000242 does for the existing rows
// (WP-744). The service accepts them from internal callers; the API
// refuses them (422, WP-274).
var LegacyMovementTypes = map[string]string{
	"warehouse_transfer":  "transfer",
	"bin_transfer":        "transfer",
	"reservation_release": "release",
	"repair_transfer":     "repair",
}

// ReservedMovementTypes arise only in their domain paths (assignment,
// reservation, disposal), which call the service directly; POST
// /api/v1/stock-movements refuses them with ProblemMovementTypeReserved
// (E-38, WP-274).
var ReservedMovementTypes = map[string]bool{
	"assignment": true, "return": true, "reservation": true, "release": true, "disposal": true,
}

// AssetMovementTypes are the physical movements POST
// /api/v1/stock-movements books for an asset, without quantity.
var AssetMovementTypes = map[string]bool{
	"receipt": true, "transfer": true, "deployment": true, "retrieval": true, "repair": true, "correction": true,
}

// QuantityMovementTypes are the quantity bookings POST
// /api/v1/stock-movements takes for a quantity item, with quantity > 0
// (until the merge into consumable, E-40).
var QuantityMovementTypes = map[string]bool{
	"receipt": true, "retrieval": true, "correction": true, "transfer": true,
}

// Problem types of the movement service (RFC 7807).
const (
	// ProblemMovementTypeReserved: the movement type arises only in its
	// domain path (422).
	ProblemMovementTypeReserved = "https://reticora.io/problems/movement-type-reserved"
	// ProblemInsufficientStock: the movement would drive the stock of a
	// quantity item below zero (409).
	ProblemInsufficientStock = "https://reticora.io/problems/insufficient-stock"
)

// stockDelta is the change of the stock level of a quantity item by a
// movement of the given type and quantity: receipt and correction add,
// transfer keeps the level, every other type takes away (WP-274).
func stockDelta(movementType string, quantity float64) float64 {
	switch movementType {
	case "receipt", "correction":
		return quantity
	case "transfer":
		return 0
	default:
		return -quantity
	}
}

// Movement is one auditable inventory transaction, a row of the ledger
// stock_movement (WP-744). client_id and site_id are derived by the
// database from the moved object and the locations at booking time.
type Movement struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	ClientID       string    `json:"client_id,omitempty"`
	SiteID         string    `json:"site_id,omitempty"`
	ItemKind       string    `json:"item_kind"` // asset | consumable | quantity_item
	AssetID        string    `json:"asset_id,omitempty"`
	ConsumableID   string    `json:"consumable_id,omitempty"`
	QuantityItemID string    `json:"quantity_item_id,omitempty"`
	MovementType   string    `json:"movement_type"`
	FromLocationID string    `json:"from_location_id,omitempty"`
	ToLocationID   string    `json:"to_location_id,omitempty"`
	Quantity       *float64  `json:"quantity,omitempty"`
	ActorID        string    `json:"actor_id,omitempty"`
	AssigneeUserID string    `json:"assignee_user_id,omitempty"`
	AssigneeTeamID string    `json:"assignee_team_id,omitempty"`
	Reason         string    `json:"reason,omitempty"`
	TicketID       string    `json:"ticket_id,omitempty"`
	OrderID        string    `json:"order_id,omitempty"`
	WorkflowRunID  string    `json:"workflow_run_id,omitempty"`
	DocumentID     string    `json:"document_id,omitempty"`
	Notes          string    `json:"notes,omitempty"`
	SignatureHash  string    `json:"signature_hash,omitempty"`
	OccurredAt     time.Time `json:"occurred_at"`
	CreatedAt      time.Time `json:"created_at"`
}

// CreateMovementRequest is the payload for recording a movement.
// consumable_id is only read to refuse it (consumables book through POST
// /api/v1/consumables/{id}/movements until WP-283).
type CreateMovementRequest struct {
	ItemKind       string   `json:"item_kind,omitempty"`
	AssetID        string   `json:"asset_id,omitempty"`
	ConsumableID   string   `json:"consumable_id,omitempty"`
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

// MovementFilter holds query filters for listing movements. LocationID
// matches the source or the target; OccurredFrom and OccurredTo bound
// occurred_at inclusively.
type MovementFilter struct {
	AssetID        string
	ConsumableID   string
	QuantityItemID string
	MovementType   string
	LocationID     string
	OccurredFrom   *time.Time
	OccurredTo     *time.Time
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
