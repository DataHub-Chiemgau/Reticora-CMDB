// Package movement implements the unified, append-only inventory movement
// ledger (spec §9) plus the deprecated alias inventory/items of quantity
// items on consumable (spec §6, E-40, WP-757). Every
// inventory movement is an auditable transaction; history is never
// overwritten (enforced by a database trigger and by the absence of update
// operations in this package).
package movement

import (
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
)

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

// AssetMovementTypes are the movements POST /api/v1/stock-movements books
// for an asset, without quantity: only transfer, the move of a serial
// asset to to_location_id (E-142, WP-756). The other physical types arise
// in their domain paths, which call the service directly.
var AssetMovementTypes = map[string]bool{"transfer": true}

// ConsumableMovementTypes are the quantity bookings POST
// /api/v1/stock-movements takes for a consumable, with quantity > 0, on
// its stock per bin (consumable.ApplyBooking, E-38, WP-756).
var ConsumableMovementTypes = consumable.BookingTypes

// Problem types of the movement service (RFC 7807).
const (
	// ProblemMovementTypeReserved: the movement type arises only in its
	// domain path (422).
	ProblemMovementTypeReserved = "https://reticora.io/problems/movement-type-reserved"
	// ProblemInsufficientStock: the movement would drive the stock of a
	// bin below zero (409).
	ProblemInsufficientStock = consumable.ProblemInsufficientStock
)

// Movement is one auditable inventory transaction, a row of the ledger
// stock_movement (WP-744). client_id and site_id are derived by the
// database from the moved object and the locations at booking time.
type Movement struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	ClientID       string `json:"client_id,omitempty"`
	SiteID         string `json:"site_id,omitempty"`
	ItemKind       string `json:"item_kind"` // asset | consumable
	AssetID        string `json:"asset_id,omitempty"`
	ConsumableID   string `json:"consumable_id,omitempty"`
	// QuantityItemID is the deprecated alias of ConsumableID (E-40,
	// WP-757): quantity items were merged into consumable with the same
	// ids. normalizeMovement moves it to ConsumableID, so a recorded or
	// listed movement never carries it.
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

	// DueAt is the due date of an assignment (CHECK
	// stock_movement_due_at_assignment, WP-745); nil for every other type.
	DueAt *time.Time `json:"due_at,omitempty"`

	// onlyBin books a consumable receipt or retrieval without location
	// into or from the only bin of the consumable (POST
	// /api/v1/consumables/{id}/movements, WP-756).
	onlyBin bool
	// itemAlias marks a movement named by quantity_item_id: an invisible
	// article is ErrItemNotFound instead of consumable.ErrNotFound.
	itemAlias bool
}

// ConsumableMovement is a consumable movement in the form of the
// deprecated GET /api/v1/consumables/{id}/movements (E-38, WP-756): the
// ledger row plus direction (in when it books only into to_location_id,
// else out), location_id (the bin of that direction) and reference (the
// notes).
type ConsumableMovement struct {
	Movement
	Direction  string `json:"direction"`
	LocationID string `json:"location_id,omitempty"`
	Reference  string `json:"reference,omitempty"`
}

// LegacyConsumableMovement returns m in the form of the deprecated path.
func LegacyConsumableMovement(m *Movement) ConsumableMovement {
	out := ConsumableMovement{Movement: *m, Direction: "out", LocationID: m.FromLocationID, Reference: m.Notes}
	if m.FromLocationID == "" {
		out.Direction, out.LocationID = "in", m.ToLocationID
	}
	return out
}

// CreateMovementRequest is the payload for recording a movement of
// exactly one asset or consumable; quantity_item_id is the deprecated alias
// of consumable_id (WP-757).
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
// occurred_at inclusively. QuantityItemID is the deprecated alias of
// ConsumableID (WP-757).
type MovementFilter struct {
	AssetID        string
	ConsumableID   string
	QuantityItemID string
	MovementType   string
	LocationID     string
	OccurredFrom   *time.Time
	OccurredTo     *time.Time
}

// QuantityItem is the answer form of the deprecated alias inventory/items
// on consumable (E-40, E-38, WP-757): StockLevel is the sum of on_hand over
// the bins the caller sees, LocationID the bin when the article lies in
// exactly one, else empty; Attributes is always empty (consumable keeps
// none).
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

	// actorID is the user of a receipt booked by CreateItem.
	actorID string
}

// CreateItemRequest is the payload for creating an article through the
// alias: a stock_level > 0 is booked as a receipt into the bin
// location_id; attributes have no target and must be empty.
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

// UpdateItemRequest is the payload for updating an article through the
// alias. LocationID and Attributes are refused (422): stock moves by a
// transfer through POST /api/v1/stock-movements, consumable keeps no
// attributes.
type UpdateItemRequest struct {
	Name       *string        `json:"name,omitempty"`
	Category   *string        `json:"category,omitempty"`
	Unit       *string        `json:"unit,omitempty"`
	MinLevel   *float64       `json:"min_level,omitempty"`
	LocationID *string        `json:"location_id,omitempty"`
	Notes      *string        `json:"notes,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// Conversion converts one unit of an article into a serialized asset (POST
// /api/v1/inventory/items/{id}/convert, WP-757): the retrieval of one unit
// from the bin LocationID (the only bin of the article when empty) and the
// asset in that bin arise in one transaction. AssetID and Movement are set
// on success.
type Conversion struct {
	OrganizationID string
	ItemID         string
	LocationID     string
	AssetTag       string
	SerialNumber   string
	ActorID        string

	AssetID  string
	Movement *Movement
}
