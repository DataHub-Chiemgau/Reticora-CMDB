// Package asset provides the Asset domain model, repository, and HTTP handler.
package asset

import "time"

// Asset represents a tracked asset (hardware, software, license, cloud resource).
type Asset struct {
	ID             string         `json:"id"`
	OrganizationID string         `json:"organization_id"`
	ClientID       string         `json:"client_id,omitempty"`
	CIID           string         `json:"ci_id,omitempty"`
	AssetTag       string         `json:"asset_tag"`
	Name           string         `json:"name"`
	Category       string         `json:"category"`
	Status         string         `json:"status"`
	PurchaseDate   string         `json:"purchase_date,omitempty"`
	PurchaseCost   float64        `json:"purchase_cost,omitempty"`
	Currency       string         `json:"currency,omitempty"`
	WarrantyEnd    string         `json:"warranty_end,omitempty"`
	Supplier       string         `json:"supplier,omitempty"`
	InvoiceNumber  string         `json:"invoice_number,omitempty"`
	SerialNumber   string         `json:"serial_number,omitempty"`
	RFIDTag        string         `json:"rfid_tag,omitempty"`
	Barcode        string         `json:"barcode,omitempty"`
	Location       string         `json:"location,omitempty"`
	Notes          string         `json:"notes,omitempty"`
	CustomFields   map[string]any `json:"custom_fields"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

// CreateRequest is the payload for creating an asset.
type CreateRequest struct {
	RFIDTag  string `json:"rfid_tag,omitempty"`
	Barcode  string `json:"barcode,omitempty"`
	ClientID      string         `json:"client_id,omitempty"`
	CIID          string         `json:"ci_id,omitempty"`
	AssetTag      string         `json:"asset_tag"`
	Name          string         `json:"name"`
	Category      string         `json:"category,omitempty"`
	Status        string         `json:"status,omitempty"`
	PurchaseDate  string         `json:"purchase_date,omitempty"`
	PurchaseCost  float64        `json:"purchase_cost,omitempty"`
	Currency      string         `json:"currency,omitempty"`
	WarrantyEnd   string         `json:"warranty_end,omitempty"`
	Supplier      string         `json:"supplier,omitempty"`
	InvoiceNumber string         `json:"invoice_number,omitempty"`
	SerialNumber  string         `json:"serial_number,omitempty"`
	Location      string         `json:"location,omitempty"`
	Notes         string         `json:"notes,omitempty"`
	CustomFields  map[string]any `json:"custom_fields,omitempty"`
}

// UpdateRequest is the payload for updating an asset.
type UpdateRequest struct {
	Name          *string        `json:"name,omitempty"`
	Category      *string        `json:"category,omitempty"`
	Status        *string        `json:"status,omitempty"`
	PurchaseDate  *string        `json:"purchase_date,omitempty"`
	PurchaseCost  *float64       `json:"purchase_cost,omitempty"`
	Currency      *string        `json:"currency,omitempty"`
	WarrantyEnd   *string        `json:"warranty_end,omitempty"`
	Supplier      *string        `json:"supplier,omitempty"`
	InvoiceNumber *string        `json:"invoice_number,omitempty"`
	SerialNumber  *string        `json:"serial_number,omitempty"`
	RFIDTag       *string        `json:"rfid_tag,omitempty"`
	Barcode       *string        `json:"barcode,omitempty"`
	Location      *string        `json:"location,omitempty"`
	Notes         *string        `json:"notes,omitempty"`
	CustomFields  map[string]any `json:"custom_fields,omitempty"`
	CIID          *string        `json:"ci_id,omitempty"`
}

// FilterParams holds filter parameters for listing assets.
type FilterParams struct {
	Status   string
	Category string
	ClientID string
	Search   string
	SortBy   string
	SortDir  string
}
