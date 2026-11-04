// Package order implements the internal ordering system (spec §6.9,
// STK-05): position-based orders with a state machine, a four-eyes
// approval and a supplier from the supplier master data (WP-286).
package order

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// The states of an internal order (STK-05, WP-286).
const (
	StatusDraft             = "draft"
	StatusSubmitted         = "submitted"
	StatusApproved          = "approved"
	StatusRejected          = "rejected"
	StatusOrdered           = "ordered"
	StatusPartiallyReceived = "partially_received"
	StatusReceived          = "received"
	StatusClosed            = "closed"
	StatusCancelled         = "cancelled"
)

// transitions is the state machine of the routes: from → allowed targets.
// rejected, closed and cancelled are terminal. A cancellation after a
// partial receipt keeps the booked movements; the rest is dropped.
var transitions = map[string][]string{
	StatusDraft:             {StatusSubmitted, StatusCancelled},
	StatusSubmitted:         {StatusApproved, StatusRejected, StatusCancelled},
	StatusApproved:          {StatusOrdered, StatusCancelled},
	StatusOrdered:           {StatusCancelled},
	StatusPartiallyReceived: {StatusCancelled},
	StatusReceived:          {StatusClosed},
}

// receiptTransitions are reached only through the goods receipt (WP-760),
// never through SetStatus.
var receiptTransitions = map[string][]string{
	StatusOrdered:           {StatusPartiallyReceived, StatusReceived},
	StatusPartiallyReceived: {StatusReceived},
}

// CanTransition reports whether a route may move an order from one state to
// the other.
func CanTransition(from, to string) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// CanReceive reports whether the goods receipt may move an order from one
// state to the other (WP-760).
func CanReceive(from, to string) bool {
	for _, s := range receiptTransitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// Errors of the order repositories; the handler maps them onto RFC 7807.
var (
	// ErrNotFound: the order is unknown or outside the caller's scope (404).
	ErrNotFound = fmt.Errorf("order %w", api.ErrNotFound)
	// ErrInvalidTransition: the order is not in a state the transition
	// starts from, or a parallel transition changed it first (409).
	ErrInvalidTransition = errors.New("the order is not in a state that allows this transition")
	// ErrNotDraft: changing, deleting and adding positions need draft (409).
	ErrNotDraft = errors.New("the order can only be changed in status draft")
	// ErrFourEyes: the approver is the requester, or the requester is
	// unknown (403).
	ErrFourEyes = errors.New("four-eyes rule: an order must be approved by a user other than its requester (requested_by)")
)

// fieldError is a 422 on a request field.
func fieldError(field, message string) error {
	return &api.FieldError{Field: field, Message: message, Status: http.StatusUnprocessableEntity}
}

// Order is an internal purchase order.
type Order struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	ClientID       string `json:"client_id,omitempty"`
	OrderNumber    string `json:"order_number"`
	Title          string `json:"title"`
	Status         string `json:"status"`
	RequestedBy    string `json:"requested_by,omitempty"`
	ApprovedBy     string `json:"approved_by,omitempty"`
	// SupplierID references the supplier master data (WP-736).
	SupplierID   string `json:"supplier_id,omitempty"`
	SupplierName string `json:"supplier_name,omitempty"`
	// Supplier is the deprecated alias of supplier_name (until WP-320). On
	// a write it is the name of an existing supplier of the organization.
	Supplier  string    `json:"supplier,omitempty"`
	TotalCost float64   `json:"total_cost,omitempty"`
	Currency  string    `json:"currency,omitempty"`
	Notes     string    `json:"notes,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Items     []Item    `json:"items,omitempty"`
}

// Item is one position of an internal order: a consumable position
// (consumable_id) or an asset position (description and quantity, asset_id
// optional), never both.
type Item struct {
	ID               string    `json:"id"`
	OrganizationID   string    `json:"organization_id"`
	OrderID          string    `json:"order_id"`
	Description      string    `json:"description"`
	Quantity         float64   `json:"quantity"`
	ReceivedQuantity float64   `json:"received_quantity"`
	UnitPrice        float64   `json:"unit_price,omitempty"`
	ConsumableID     string    `json:"consumable_id,omitempty"`
	AssetID          string    `json:"asset_id,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

// validate checks the references of a new position.
func (it *Item) validate() error {
	if it.ConsumableID != "" && it.AssetID != "" {
		return fieldError("asset_id", "a position references either a consumable or an asset, not both")
	}
	return nil
}

// CreateOrderRequest is the payload for creating an order.
type CreateOrderRequest struct {
	ClientID    string `json:"client_id,omitempty"`
	OrderNumber string `json:"order_number,omitempty"`
	Title       string `json:"title"`
	SupplierID  string `json:"supplier_id,omitempty"`
	// Supplier is deprecated: the name of an existing supplier; empty is
	// not set.
	Supplier  string  `json:"supplier,omitempty"`
	TotalCost float64 `json:"total_cost,omitempty"`
	Currency  string  `json:"currency,omitempty"`
	Notes     string  `json:"notes,omitempty"`
}

// UpdateOrderRequest is the payload for updating an order.
type UpdateOrderRequest struct {
	ClientID *string `json:"client_id,omitempty"`
	Title    *string `json:"title,omitempty"`
	// SupplierID sets the supplier; an empty value removes it.
	SupplierID *string `json:"supplier_id,omitempty"`
	// Supplier is deprecated: the name of an existing supplier; empty is
	// not set.
	Supplier  *string  `json:"supplier,omitempty"`
	TotalCost *float64 `json:"total_cost,omitempty"`
	Currency  *string  `json:"currency,omitempty"`
	Notes     *string  `json:"notes,omitempty"`
}

// supplierName returns the trimmed alias name, empty when not set.
func supplierName(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

// CreateItemRequest adds a position to an order.
type CreateItemRequest struct {
	Description  string  `json:"description"`
	Quantity     float64 `json:"quantity"`
	UnitPrice    float64 `json:"unit_price,omitempty"`
	ConsumableID string  `json:"consumable_id,omitempty"`
	AssetID      string  `json:"asset_id,omitempty"`
}

// FilterParams scopes order list queries.
type FilterParams struct {
	Status   string
	ClientID string
	Search   string
}
