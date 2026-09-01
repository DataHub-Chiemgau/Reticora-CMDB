// Package consumable implements the Lagerverwaltung for Verbrauchsmaterial
// (spec §6.9): stock levels with min-level alerting and auditable movements.
package consumable

import "time"

// Consumable is a stock item (cable, SFP, toner, ...).
type Consumable struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	ClientID       string    `json:"client_id,omitempty"`
	Name           string    `json:"name"`
	SKU            string    `json:"sku,omitempty"`
	Category       string    `json:"category"`
	Unit           string    `json:"unit"`
	StockLevel     float64   `json:"stock_level"`
	MinLevel       float64   `json:"min_level"`
	Location       string    `json:"location,omitempty"`
	Notes          string    `json:"notes,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// LowStock reports whether the stock level is at or below the minimum.
func (c Consumable) LowStock() bool { return c.StockLevel <= c.MinLevel }

// Movement is one stock in/out event.
type Movement struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	ConsumableID   string    `json:"consumable_id"`
	Direction      string    `json:"direction"` // in | out
	Quantity       float64   `json:"quantity"`
	Reason         string    `json:"reason,omitempty"`
	Reference      string    `json:"reference,omitempty"`
	ActorID        string    `json:"actor_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// CreateConsumableRequest is the payload for creating a consumable.
type CreateConsumableRequest struct {
	ClientID   string  `json:"client_id,omitempty"`
	Name       string  `json:"name"`
	SKU        string  `json:"sku,omitempty"`
	Category   string  `json:"category,omitempty"`
	Unit       string  `json:"unit,omitempty"`
	StockLevel float64 `json:"stock_level,omitempty"`
	MinLevel   float64 `json:"min_level,omitempty"`
	Location   string  `json:"location,omitempty"`
	Notes      string  `json:"notes,omitempty"`
}

// UpdateConsumableRequest is the payload for updating a consumable.
type UpdateConsumableRequest struct {
	ClientID *string  `json:"client_id,omitempty"`
	Name     *string  `json:"name,omitempty"`
	SKU      *string  `json:"sku,omitempty"`
	Category *string  `json:"category,omitempty"`
	Unit     *string  `json:"unit,omitempty"`
	MinLevel *float64 `json:"min_level,omitempty"`
	Location *string  `json:"location,omitempty"`
	Notes    *string  `json:"notes,omitempty"`
}

// CreateMovementRequest records a stock in/out.
type CreateMovementRequest struct {
	Direction string  `json:"direction"`
	Quantity  float64 `json:"quantity"`
	Reason    string  `json:"reason,omitempty"`
	Reference string  `json:"reference,omitempty"`
}

// FilterParams scopes consumable list queries.
type FilterParams struct {
	Category string
	ClientID string
	LowStock bool
	Search   string
}
