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
	// ErrNotReceivable: the goods receipt needs status ordered or
	// partially_received (409, WP-760).
	ErrNotReceivable = errors.New("goods can only be received for an order in status ordered or partially_received")
)

// msgTargetBin is the 422 message for a target_location_id that names no bin the
// order may use. An unknown id, a location outside the scope and a bin of
// another client than the order get the same answer, so the check reveals
// nothing about locations the order cannot use.
const msgTargetBin = "target_location_id does not name a location visible for the order's client"

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
	Supplier string `json:"supplier,omitempty"`
	// TargetLocationID is the bin the goods receipt books into (WP-847):
	// a location of kind bin within the scope and, for an order with a
	// client, of that client.
	TargetLocationID string `json:"target_location_id,omitempty"`
	// ApprovalRunID is the workflow run of the trigger order_submitted that
	// decides on the approval (WP-1108); while it is open, approve and
	// reject are 409.
	ApprovalRunID string    `json:"approval_run_id,omitempty"`
	TotalCost     float64   `json:"total_cost,omitempty"`
	Currency      string    `json:"currency,omitempty"`
	Notes         string    `json:"notes,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	Items         []Item    `json:"items,omitempty"`
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
	Supplier         string  `json:"supplier,omitempty"`
	TargetLocationID string  `json:"target_location_id,omitempty"`
	TotalCost        float64 `json:"total_cost,omitempty"`
	Currency         string  `json:"currency,omitempty"`
	Notes            string  `json:"notes,omitempty"`
}

// UpdateOrderRequest is the payload for updating an order.
type UpdateOrderRequest struct {
	ClientID *string `json:"client_id,omitempty"`
	Title    *string `json:"title,omitempty"`
	// SupplierID sets the supplier; an empty value removes it.
	SupplierID *string `json:"supplier_id,omitempty"`
	// Supplier is deprecated: the name of an existing supplier; empty is
	// not set.
	Supplier *string `json:"supplier,omitempty"`
	// TargetLocationID sets the target bin; an empty value removes it.
	TargetLocationID *string  `json:"target_location_id,omitempty"`
	TotalCost        *float64 `json:"total_cost,omitempty"`
	Currency         *string  `json:"currency,omitempty"`
	Notes            *string  `json:"notes,omitempty"`
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

// ReceiveItemRequest is one position of a goods receipt (WP-760): the
// position item_id of the order receives quantity (> 0) into the bin
// location_id, by default the target bin of the order. An asset position
// names the received assets in asset_ids, as many as quantity.
type ReceiveItemRequest struct {
	ItemID     string   `json:"item_id"`
	Quantity   float64  `json:"quantity"`
	LocationID string   `json:"location_id,omitempty"`
	AssetIDs   []string `json:"asset_ids,omitempty"`
}

// ReceiveOrderRequest is the payload of POST /api/v1/orders/{id}/receive.
type ReceiveOrderRequest struct {
	Items []ReceiveItemRequest `json:"items"`
}

// itemField is the field name of position i of a goods receipt.
func itemField(i int, field string) string {
	return fmt.Sprintf("items[%d].%s", i, field)
}

// validate checks the form of a goods receipt: at least one position, each
// with item_id and a positive quantity, and no asset named twice. The
// references are checked by the repository.
func (req *ReceiveOrderRequest) validate() error {
	if len(req.Items) == 0 {
		return fieldError("items", "at least one position is required")
	}
	assets := map[string]bool{}
	for i := range req.Items {
		line := &req.Items[i]
		line.ItemID = strings.TrimSpace(line.ItemID)
		line.LocationID = strings.TrimSpace(line.LocationID)
		if line.ItemID == "" {
			return fieldError(itemField(i, "item_id"), "item_id is required")
		}
		if line.Quantity <= 0 {
			return fieldError(itemField(i, "quantity"), "quantity must be greater than 0")
		}
		for j, id := range line.AssetIDs {
			id = strings.ToLower(strings.TrimSpace(id))
			if assets[id] {
				return fieldError(itemField(i, "asset_ids"), "an asset is named more than once in the goods receipt")
			}
			assets[id] = true
			line.AssetIDs[j] = id
		}
	}
	return nil
}

// Receipt is a goods receipt of the order OrderID by ActorID (WP-760).
type Receipt struct {
	OrganizationID string
	OrderID        string
	ActorID        string
	Items          []ReceiveItemRequest
}

// FilterParams scopes order list queries.
type FilterParams struct {
	Status   string
	ClientID string
	Search   string
}
