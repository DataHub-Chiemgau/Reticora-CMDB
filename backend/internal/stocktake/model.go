// Package stocktake provides the Inventory/Stocktake domain.
package stocktake

import "time"

// Stocktake represents a planned or active inventory count.
type Stocktake struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	Title          string    `json:"title"`
	Description    string    `json:"description,omitempty"`
	Status         string    `json:"status"`
	Scope          string    `json:"scope"`
	StartedBy      string    `json:"started_by,omitempty"`
	StartedAt      string    `json:"started_at,omitempty"`
	CompletedAt    string    `json:"completed_at,omitempty"`
	DueDate        string    `json:"due_date,omitempty"`
	TotalExpected  int       `json:"total_expected"`
	TotalScanned   int       `json:"total_scanned"`
	TotalMissing   int       `json:"total_missing"`
	TotalSurplus   int       `json:"total_surplus"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// StockScan represents a single scan event during a stocktake.
type StockScan struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	StocktakeID    string    `json:"stocktake_id"`
	AssetID        string    `json:"asset_id,omitempty"`
	CIID           string    `json:"ci_id,omitempty"`
	ScannedBy      string    `json:"scanned_by"`
	ScanMethod     string    `json:"scan_method"`
	ScanResult     string    `json:"scan_result"`
	LocationFound  string    `json:"location_found,omitempty"`
	Notes          string    `json:"notes,omitempty"`
	ScannedAt      time.Time `json:"scanned_at"`
}

// CreateRequest is the payload for creating a stocktake.
type CreateRequest struct {
	Title         string `json:"title"`
	Description   string `json:"description,omitempty"`
	Scope         string `json:"scope,omitempty"`
	DueDate       string `json:"due_date,omitempty"`
	TotalExpected int    `json:"total_expected,omitempty"`
}

// UpdateRequest is the payload for updating a stocktake.
type UpdateRequest struct {
	Title         *string `json:"title,omitempty"`
	Description   *string `json:"description,omitempty"`
	Status        *string `json:"status,omitempty"`
	DueDate       *string `json:"due_date,omitempty"`
	TotalExpected *int    `json:"total_expected,omitempty"`
}

// ScanRequest is the payload for recording a scan.
type ScanRequest struct {
	AssetID       string `json:"asset_id,omitempty"`
	CIID          string `json:"ci_id,omitempty"`
	ScanMethod    string `json:"scan_method,omitempty"`
	ScanResult    string `json:"scan_result"`
	LocationFound string `json:"location_found,omitempty"`
	Notes         string `json:"notes,omitempty"`
}

// FilterParams holds filter parameters for listing stocktakes.
type FilterParams struct {
	Status  string
	Scope   string
	Search  string
	SortBy  string
	SortDir string
}
