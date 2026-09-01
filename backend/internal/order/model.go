// Package order implements the internal ordering system (spec §6.9):
// position-based orders with an approval workflow status.
package order

import "time"

// Order is an internal purchase order.
type Order struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	ClientID       string    `json:"client_id,omitempty"`
	OrderNumber    string    `json:"order_number"`
	Title          string    `json:"title"`
	Status         string    `json:"status"`
	RequestedBy    string    `json:"requested_by,omitempty"`
	ApprovedBy     string    `json:"approved_by,omitempty"`
	Supplier       string    `json:"supplier,omitempty"`
	TotalCost      float64   `json:"total_cost,omitempty"`
	Currency       string    `json:"currency,omitempty"`
	Notes          string    `json:"notes,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	Items          []Item    `json:"items,omitempty"`
}

// Item is one position of an internal order.
type Item struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	OrderID        string    `json:"order_id"`
	Description    string    `json:"description"`
	Quantity       float64   `json:"quantity"`
	UnitPrice      float64   `json:"unit_price,omitempty"`
	ConsumableID   string    `json:"consumable_id,omitempty"`
	AssetID        string    `json:"asset_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// CreateOrderRequest is the payload for creating an order.
type CreateOrderRequest struct {
	ClientID    string  `json:"client_id,omitempty"`
	OrderNumber string  `json:"order_number,omitempty"`
	Title       string  `json:"title"`
	Supplier    string  `json:"supplier,omitempty"`
	TotalCost   float64 `json:"total_cost,omitempty"`
	Currency    string  `json:"currency,omitempty"`
	Notes       string  `json:"notes,omitempty"`
}

// UpdateOrderRequest is the payload for updating an order.
type UpdateOrderRequest struct {
	ClientID   *string  `json:"client_id,omitempty"`
	Title      *string  `json:"title,omitempty"`
	Supplier   *string  `json:"supplier,omitempty"`
	TotalCost  *float64 `json:"total_cost,omitempty"`
	Currency   *string  `json:"currency,omitempty"`
	Notes      *string  `json:"notes,omitempty"`
}

// CreateItemRequest adds a position to an order.
type CreateItemRequest struct {
	Description   string  `json:"description"`
	Quantity      float64 `json:"quantity"`
	UnitPrice     float64 `json:"unit_price,omitempty"`
	ConsumableID  string  `json:"consumable_id,omitempty"`
	AssetID       string  `json:"asset_id,omitempty"`
}

// FilterParams scopes order list queries.
type FilterParams struct {
	Status   string
	ClientID string
	Search   string
}
