// Package stocktake provides the Inventory/Stocktake domain.
package stocktake

import (
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
)

// Stocktake represents a planned or active inventory count.
type Stocktake struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	Title          string `json:"title"`
	Description    string `json:"description,omitempty"`
	Status         string `json:"status"`
	Scope          string `json:"scope"`
	// LocationID is the area of the stocktake, a node of the location tree
	// whose subtree (bins included) is counted (WP-848). ClientID and
	// SiteID are derived from it by the database; all three are empty for
	// an org-wide stocktake of an earlier release.
	LocationID    string    `json:"location_id,omitempty"`
	ClientID      string    `json:"client_id,omitempty"`
	SiteID        string    `json:"site_id,omitempty"`
	StartedBy     string    `json:"started_by,omitempty"`
	StartedAt     string    `json:"started_at,omitempty"`
	CompletedAt   string    `json:"completed_at,omitempty"`
	DueDate       string    `json:"due_date,omitempty"`
	TotalExpected int       `json:"total_expected"`
	TotalScanned  int       `json:"total_scanned"`
	TotalMissing  int       `json:"total_missing"`
	TotalSurplus  int       `json:"total_surplus"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
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

// CreateRequest is the payload for creating a stocktake. Status and the
// totals are not writable (WP-848): a stocktake starts planned, start and
// cancel are its transitions, start sets total_expected from the snapshot.
type CreateRequest struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	// Scope is deprecated (WP-848); LocationID names the area.
	Scope      string `json:"scope,omitempty"`
	LocationID string `json:"location_id,omitempty"`
	DueDate    string `json:"due_date,omitempty"`
}

// UpdateRequest is the payload for updating a stocktake. LocationID may
// change only while the stocktake is planned; an empty value removes the
// area.
type UpdateRequest struct {
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
	DueDate     *string `json:"due_date,omitempty"`
	LocationID  *string `json:"location_id,omitempty"`
}

// Item kinds of the snapshot (stocktake_item.item_kind).
const (
	ItemKindAsset      = "asset"
	ItemKindConsumable = "consumable"
)

// Item is one row of the expected stock that POST …/start freezes
// (stocktake_item, WP-848): an asset in the area (expected quantity 1) or
// the stock of a consumable in a bin of the area (expected quantity
// on_hand at the start). CountedQuantity and Classification are set when
// the stocktake is completed (WP-287).
type Item struct {
	ID               string    `json:"id"`
	StocktakeID      string    `json:"stocktake_id"`
	ItemKind         string    `json:"item_kind"`
	AssetID          string    `json:"asset_id,omitempty"`
	ConsumableID     string    `json:"consumable_id,omitempty"`
	LocationID       string    `json:"location_id"`
	ExpectedQuantity float64   `json:"expected_quantity"`
	CountedQuantity  *float64  `json:"counted_quantity,omitempty"`
	Classification   string    `json:"classification,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
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
