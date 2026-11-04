// Package asset provides the Asset domain model, repository, and HTTP handler.
package asset

import (
	"reflect"
	"sort"
	"strings"
	"time"
)

// Asset represents a tracked asset (hardware, software, license, cloud resource).
type Asset struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	ClientID       string `json:"client_id,omitempty"`
	// CIID is the primary CI (API name primary_ci_id over the column
	// asset.ci_id, E-122).
	CIID string `json:"primary_ci_id,omitempty"`
	// AssetTag is the inventory number (column inventory_no, AST-01); the
	// Go names AssetTag, PurchaseCost and WarrantyEnd stay (WP-739).
	AssetTag     string `json:"inventory_no"`
	Name         string `json:"name"`
	Category     string `json:"category"`
	Status       string `json:"status"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model,omitempty"`
	PurchaseDate string `json:"purchase_date,omitempty"`
	// PurchaseCost is the purchase price as a decimal string (NUMERIC(14,2),
	// no float rounding; empty without a price).
	PurchaseCost string `json:"purchase_price,omitempty"`
	Currency     string `json:"currency,omitempty"`
	WarrantyEnd  string `json:"warranty_until,omitempty"`
	CostCenter   string `json:"cost_center,omitempty"`
	// Supplier is the deprecated free text of the supplier, read from the
	// mirror of supplier.name (E-41); not part of the API. SupplierID
	// names the supplier record (WP-271, WP-739).
	Supplier      string `json:"-"`
	SupplierID    string `json:"supplier_id,omitempty"`
	ContractID    string `json:"contract_id,omitempty"`
	OwnerUserID   string `json:"owner_user_id,omitempty"`
	OwnerTeamID   string `json:"owner_team_id,omitempty"`
	InvoiceNumber string `json:"invoice_number,omitempty"`
	SerialNumber  string `json:"serial_number,omitempty"`
	RFIDTag       string `json:"rfid_tag,omitempty"`
	Barcode       string `json:"barcode,omitempty"`
	Location      string `json:"location,omitempty"`
	// LocationID is the asset's node in the canonical location tree (LOC-10).
	// It changes only through stock movements and rack placement and is
	// read-only in the API; internal callers (the inventory conversion) set
	// it on create.
	LocationID string `json:"location_id,omitempty"`
	// LifecycleState is the state of the effective asset lifecycle model
	// (E-120); it changes only through POST
	// /api/v1/assets/{id}/lifecycle-transitions.
	LifecycleState string         `json:"lifecycle_state,omitempty"`
	Notes          string         `json:"notes,omitempty"`
	CustomFields   map[string]any `json:"custom_fields"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	// AssetTypeID and SiteID are the type and scope of the asset for its
	// field permissions (RBA-07). Not part of the API.
	AssetTypeID string `json:"-"`
	SiteID      string `json:"-"`
}

// CreateRequest is the payload for creating an asset. A missing currency is
// the currency of the organization (WP-107); the location and the lifecycle
// state are not writable here.
type CreateRequest struct {
	RFIDTag       string         `json:"rfid_tag,omitempty"`
	Barcode       string         `json:"barcode,omitempty"`
	ClientID      string         `json:"client_id,omitempty"`
	CIID          string         `json:"primary_ci_id,omitempty"`
	AssetTag      string         `json:"inventory_no"`
	Name          string         `json:"name"`
	Category      string         `json:"category,omitempty"`
	Status        string         `json:"status,omitempty"`
	Manufacturer  string         `json:"manufacturer,omitempty"`
	Model         string         `json:"model,omitempty"`
	PurchaseDate  string         `json:"purchase_date,omitempty"`
	PurchaseCost  string         `json:"purchase_price,omitempty"`
	Currency      string         `json:"currency,omitempty"`
	WarrantyEnd   string         `json:"warranty_until,omitempty"`
	CostCenter    string         `json:"cost_center,omitempty"`
	SupplierID    string         `json:"supplier_id,omitempty"`
	ContractID    string         `json:"contract_id,omitempty"`
	OwnerUserID   string         `json:"owner_user_id,omitempty"`
	OwnerTeamID   string         `json:"owner_team_id,omitempty"`
	InvoiceNumber string         `json:"invoice_number,omitempty"`
	SerialNumber  string         `json:"serial_number,omitempty"`
	Location      string         `json:"location,omitempty"`
	Notes         string         `json:"notes,omitempty"`
	CustomFields  map[string]any `json:"custom_fields,omitempty"`
}

// SentFields are the DTO paths of the fields the request sets (a non-empty
// value): the field permissions check them (WP-271). name and client_id
// are structural; a custom field is custom_fields.<key>.
func (req *CreateRequest) SentFields() []string {
	out := sentPaths(reflect.ValueOf(req).Elem(), func(f reflect.Value) bool { return !f.IsZero() })
	return append(out, customFieldPaths(req.CustomFields)...)
}

// ReadOnly is a member of a PATCH body the API serves read-only: Set
// records that it was sent, whatever its value.
type ReadOnly struct{ Set bool }

// UnmarshalJSON records that the member was sent.
func (o *ReadOnly) UnmarshalJSON([]byte) error {
	o.Set = true
	return nil
}

// UpdateRequest is the payload for updating an asset.
type UpdateRequest struct {
	Name *string `json:"name,omitempty"`
	// AssetTag is the inventory number (AST-01, WP-739); it must not be
	// empty.
	AssetTag      *string        `json:"inventory_no,omitempty"`
	Category      *string        `json:"category,omitempty"`
	Status        *string        `json:"status,omitempty"`
	Manufacturer  *string        `json:"manufacturer,omitempty"`
	Model         *string        `json:"model,omitempty"`
	PurchaseDate  *string        `json:"purchase_date,omitempty"`
	PurchaseCost  *string        `json:"purchase_price,omitempty"`
	Currency      *string        `json:"currency,omitempty"`
	WarrantyEnd   *string        `json:"warranty_until,omitempty"`
	CostCenter    *string        `json:"cost_center,omitempty"`
	SupplierID    *string        `json:"supplier_id,omitempty"`
	ContractID    *string        `json:"contract_id,omitempty"`
	OwnerUserID   *string        `json:"owner_user_id,omitempty"`
	OwnerTeamID   *string        `json:"owner_team_id,omitempty"`
	InvoiceNumber *string        `json:"invoice_number,omitempty"`
	SerialNumber  *string        `json:"serial_number,omitempty"`
	RFIDTag       *string        `json:"rfid_tag,omitempty"`
	Barcode       *string        `json:"barcode,omitempty"`
	Location      *string        `json:"location,omitempty"`
	Notes         *string        `json:"notes,omitempty"`
	CustomFields  map[string]any `json:"custom_fields,omitempty"`
	CIID          *string        `json:"primary_ci_id,omitempty"`
	// LocationID and LifecycleState are read-only (E-120): a PATCH that
	// sends them is 422 naming the canonical write path.
	LocationID     ReadOnly `json:"location_id"`
	LifecycleState ReadOnly `json:"lifecycle_state"`
	// Check runs on the locked current asset before the write (the field
	// permissions of the handler); nil passes.
	Check func(current *Asset) error `json:"-"`
}

// SentFields are the DTO paths of the fields the request sends; a custom
// field is custom_fields.<key>.
func (req *UpdateRequest) SentFields() []string {
	out := sentPaths(reflect.ValueOf(req).Elem(), func(f reflect.Value) bool {
		return f.Kind() == reflect.Pointer && !f.IsNil()
	})
	return append(out, customFieldPaths(req.CustomFields)...)
}

// sentPaths returns the JSON names of the scalar members of v that sent
// reports as set, sorted; maps, functions, read-only markers and the
// structural members are left out.
func sentPaths(v reflect.Value, sent func(reflect.Value) bool) []string {
	var out []string
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" || structuralRequestFields[name] {
			continue
		}
		switch f.Type.Kind() {
		case reflect.Map, reflect.Func, reflect.Struct:
			continue
		}
		if sent(v.Field(i)) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// structuralRequestFields are request members without field permission:
// the name and the client scope.
var structuralRequestFields = map[string]bool{"name": true, "client_id": true}

// customFieldPaths returns custom_fields.<key> for each custom field,
// sorted.
func customFieldPaths(fields map[string]any) []string {
	out := make([]string, 0, len(fields))
	for k := range fields {
		out = append(out, customFieldsPrefix+k)
	}
	sort.Strings(out)
	return out
}

// customFieldsPrefix is the DTO path prefix of a custom field; its field
// key is attributes.<key> (permission.ParseFieldKey).
const customFieldsPrefix = "custom_fields."

// LinkCIRequest is the body of POST /api/v1/assets/{id}/cis (AST-04,
// WP-272): the CI to link and, when sent, whether it is the primary CI
// (true makes it the primary CI, false clears the primary CI when it is
// this one; absent leaves the primary CI as it is).
type LinkCIRequest struct {
	CIID    string `json:"ci_id"`
	Primary *bool  `json:"primary,omitempty"`
}

// CILink is a CI linked to an asset (ci.asset_id) and whether it is the
// asset's primary CI (asset.ci_id, API name primary_ci_id).
type CILink struct {
	AssetID string `json:"asset_id"`
	CIID    string `json:"ci_id"`
	Primary bool   `json:"primary"`
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
