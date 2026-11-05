// Package stocktake provides the Inventory/Stocktake domain.
package stocktake

import (
	"time"
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

// Scan results (scan_result_check, 000265): the server sets found when the
// object is in the snapshot of the stocktake, else unexpected; missing
// occurs in rows of earlier releases only (WP-287).
const (
	ScanFound      = "found"
	ScanMissing    = "missing"
	ScanUnexpected = "unexpected"
)

// StockScan represents a single scan event during a stocktake: an asset or
// the counted quantity of a consumable at the place found (WP-287).
// ClientID and SiteID are those of the stocktake (stock_scan_derive_scope).
// CIID, LocationFound and LegacyScanResult are read fields of the rows of
// earlier releases.
type StockScan struct {
	ID               string    `json:"id"`
	OrganizationID   string    `json:"organization_id"`
	StocktakeID      string    `json:"stocktake_id"`
	AssetID          string    `json:"asset_id,omitempty"`
	ConsumableID     string    `json:"consumable_id,omitempty"`
	Quantity         *float64  `json:"quantity,omitempty"`
	LocationID       string    `json:"location_id,omitempty"`
	ClientID         string    `json:"client_id,omitempty"`
	SiteID           string    `json:"site_id,omitempty"`
	CIID             string    `json:"ci_id,omitempty"`
	ScannedBy        string    `json:"scanned_by"`
	ScanMethod       string    `json:"scan_method"`
	ScanResult       string    `json:"scan_result"`
	LegacyScanResult string    `json:"legacy_scan_result,omitempty"`
	Code             string    `json:"code,omitempty"`
	LocationFound    string    `json:"location_found,omitempty"`
	Notes            string    `json:"notes,omitempty"`
	ScannedAt        time.Time `json:"scanned_at"`
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

// ScanRequest is the payload for recording a scan (WP-287): exactly one of
// AssetID and ConsumableID (then with Quantity > 0) at the place found
// LocationID. scan_result, ci_id and location_found are no input any more
// (400, unknown fields); Code is stored, not resolved.
type ScanRequest struct {
	AssetID      string   `json:"asset_id,omitempty"`
	ConsumableID string   `json:"consumable_id,omitempty"`
	Quantity     *float64 `json:"quantity,omitempty"`
	LocationID   string   `json:"location_id,omitempty"`
	ScanMethod   string   `json:"scan_method,omitempty"`
	Code         string   `json:"code,omitempty"`
	Notes        string   `json:"notes,omitempty"`
}

// Scan methods (scan_method_check, 000012).
var scanMethods = []string{"manual", "barcode", "rfid", "qrcode"}

// FilterParams holds filter parameters for listing stocktakes.
type FilterParams struct {
	Status  string
	Scope   string
	Search  string
	SortBy  string
	SortDir string
}

// Difference is one object of the difference list of a stocktake
// (WP-287): missing (in the snapshot, not scanned; counted quantity 0),
// unexpected (not in the snapshot; a consumable in a bin without snapshot
// row) or found with a deviation (an asset whose last scan shows another
// location than the snapshot, a consumable whose scanned quantities do not
// add up to the expected quantity). Asset and Consumable embed the object
// when the caller sees it; the handler projects the asset through
// permission.Project.
type Difference struct {
	ItemKind           string  `json:"item_kind"`
	AssetID            string  `json:"asset_id,omitempty"`
	ConsumableID       string  `json:"consumable_id,omitempty"`
	Classification     string  `json:"classification"`
	ExpectedLocationID string  `json:"expected_location_id,omitempty"`
	FoundLocationID    string  `json:"found_location_id,omitempty"`
	ExpectedQuantity   float64 `json:"expected_quantity"`
	CountedQuantity    float64 `json:"counted_quantity"`
	Asset              any     `json:"asset,omitempty"`
	Consumable         *Supply `json:"consumable,omitempty"`
	key                string
}

// DifferenceAsset is the embedded asset of a difference. SiteID and
// AssetTypeID only address the field permissions of the projection.
type DifferenceAsset struct {
	ID             string `json:"id"`
	ClientID       string `json:"client_id,omitempty"`
	Name           string `json:"name"`
	InventoryNo    string `json:"inventory_no,omitempty"`
	SerialNumber   string `json:"serial_number,omitempty"`
	LifecycleState string `json:"lifecycle_state,omitempty"`
	LocationID     string `json:"location_id,omitempty"`
	SiteID         string `json:"-"`
	AssetTypeID    string `json:"-"`
}

// Supply is the embedded consumable of a difference. Field permissions do
// not know the object kind consumable, so it carries the identity only.
type Supply struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	SKU  string `json:"sku,omitempty"`
	Unit string `json:"unit,omitempty"`
}

// CompleteRequest is the payload for completing a stocktake.
// ApplyCorrections is ignored (WP-287): the completion fixes the result in
// the snapshot and books no correction until WP-761.
type CompleteRequest struct {
	ApplyCorrections *bool `json:"apply_corrections,omitempty"`
}

// Correction is a deprecated member of Completion: the completion applies
// no correction any more (WP-287), the list stays empty.
type Correction struct {
	AssetID    string `json:"asset_id"`
	ScanResult string `json:"scan_result"`
	Applied    bool   `json:"applied"`
}

// Completion is the result of completing a stocktake: the completed
// stocktake with its totals. CorrectionsApplied is 0 and Corrections empty
// (WP-287); the result per object is in GET …/items and …/differences.
type Completion struct {
	Stocktake          Stocktake    `json:"stocktake"`
	CorrectionsApplied int          `json:"corrections_applied"`
	Corrections        []Correction `json:"corrections"`
}
