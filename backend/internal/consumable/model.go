// Package consumable implements the Lagerverwaltung for Verbrauchsmaterial
// (spec §6.9): stock per bin with min-level alerting and auditable movements
// (STK-01 to STK-03, WP-283).
package consumable

import "time"

// Consumable is a stock item (cable, SFP, toner, ...). Its stock lies in
// bins (Stock, table stock_level); StockLevel is the total stock
// (consumable.on_hand_total), the sum of on_hand over all bins whatever the
// caller's scope (E-128 (a), WP-285), kept read-only and deprecated until
// WP-320.
type Consumable struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	ClientID       string    `json:"client_id,omitempty"`
	Name           string    `json:"name"`
	SKU            string    `json:"sku,omitempty"`
	Barcode        string    `json:"barcode,omitempty"`
	SupplierID     string    `json:"supplier_id,omitempty"`
	Category       string    `json:"category"`
	Unit           string    `json:"unit"`
	StockLevel     float64   `json:"stock_level"`
	MinLevel       float64   `json:"min_level"`
	Notes          string    `json:"notes,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// LowStock reports whether the consumable is below its minimum level, the
// one threshold rule of the event stock.below_minimum, the filter low_stock
// and the badge (WP-285): total stock < min_level. A total equal to the
// minimum is not below it, and with min_level 0 nothing ever is.
func (c *Consumable) LowStock() bool { return c.StockLevel < c.MinLevel }

// CreateConsumableRequest is the payload for creating a consumable.
type CreateConsumableRequest struct {
	ClientID   string  `json:"client_id,omitempty"`
	Name       string  `json:"name"`
	SKU        string  `json:"sku,omitempty"`
	Barcode    string  `json:"barcode,omitempty"`
	SupplierID string  `json:"supplier_id,omitempty"`
	Category   string  `json:"category,omitempty"`
	Unit       string  `json:"unit,omitempty"`
	MinLevel   float64 `json:"min_level,omitempty"`
	Notes      string  `json:"notes,omitempty"`
}

// UpdateConsumableRequest is the payload for updating a consumable.
type UpdateConsumableRequest struct {
	ClientID   *string  `json:"client_id,omitempty"`
	Name       *string  `json:"name,omitempty"`
	SKU        *string  `json:"sku,omitempty"`
	Barcode    *string  `json:"barcode,omitempty"`
	SupplierID *string  `json:"supplier_id,omitempty"`
	Category   *string  `json:"category,omitempty"`
	Unit       *string  `json:"unit,omitempty"`
	MinLevel   *float64 `json:"min_level,omitempty"`
	Notes      *string  `json:"notes,omitempty"`
}

// CreateMovementRequest records a stock in/out (transitional path until
// WP-756): in is a receipt into the bin location_id, out a retrieval from
// it; without location_id the only bin of the consumable is used.
type CreateMovementRequest struct {
	Direction  string  `json:"direction"`
	LocationID string  `json:"location_id,omitempty"`
	Quantity   float64 `json:"quantity"`
	Reason     string  `json:"reason,omitempty"`
	Reference  string  `json:"reference,omitempty"`
}

// Stock is the stock of a consumable in one bin (a row of stock_level):
// on_hand never drops below zero, reserved never exceeds on_hand. client_id
// and site_id are those of the bin.
type Stock struct {
	ID             string    `json:"-"`
	OrganizationID string    `json:"-"`
	ConsumableID   string    `json:"consumable_id"`
	LocationID     string    `json:"location_id"`
	ClientID       string    `json:"client_id,omitempty"`
	SiteID         string    `json:"site_id,omitempty"`
	OnHand         float64   `json:"on_hand"`
	Reserved       float64   `json:"reserved"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Booking is one quantity booking on the stock of a consumable per bin
// (ApplyBooking): receipt into ToLocationID, retrieval from FromLocationID,
// correction with exactly one of them (to raises, from lowers) and
// transfer between two different bins, always with Quantity > 0.
type Booking struct {
	OrganizationID string
	ConsumableID   string
	MovementType   string
	FromLocationID string
	ToLocationID   string
	Quantity       float64
}

// FilterParams scopes consumable list queries.
type FilterParams struct {
	Category string
	ClientID string
	LowStock bool
	Search   string
}
