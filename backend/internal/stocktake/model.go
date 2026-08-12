// Package stocktake provides the Inventory/Stocktake domain.
package stocktake

import (
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
)

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

// DifferenceEntry pairs a deviating scan (missing, surplus, damaged,
// wrong_location) with the asset it refers to. Asset is nil when the scan
// did not resolve to a known asset.
type DifferenceEntry struct {
	Scan  StockScan    `json:"scan"`
	Asset *asset.Asset `json:"asset,omitempty"`
}

// CompleteRequest is the payload for completing a stocktake.
type CompleteRequest struct {
	ApplyCorrections *bool `json:"apply_corrections,omitempty"`
}

// Correction describes the inventory change applied (or skipped) for one
// deviating scan when a stocktake is completed.
type Correction struct {
	AssetID          string `json:"asset_id"`
	AssetTag         string `json:"asset_tag,omitempty"`
	ScanResult       string `json:"scan_result"`
	PreviousStatus   string `json:"previous_status,omitempty"`
	NewStatus        string `json:"new_status,omitempty"`
	PreviousLocation string `json:"previous_location,omitempty"`
	NewLocation      string `json:"new_location,omitempty"`
	Applied          bool   `json:"applied"`
	Detail           string `json:"detail,omitempty"`
}

// Completion is the result of completing a stocktake: the final stocktake
// state plus every inventory correction that was applied.
type Completion struct {
	Stocktake          Stocktake    `json:"stocktake"`
	CorrectionsApplied int          `json:"corrections_applied"`
	Corrections        []Correction `json:"corrections"`
}
