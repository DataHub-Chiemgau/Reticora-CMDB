// Package asset provides the Asset domain model, repository, and HTTP handler.
package asset

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
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
	// AssignedUserID or AssignedTeamID is the current holder and
	// AssignmentDueAt the due date of its assignment (holder cache, AST-02,
	// WP-745); at most one holder, a due date only with one. Read-only:
	// they change through POST /api/v1/assets/{id}/assign|return|transfer
	// (WP-275). The holder is not the owner (OwnerUserID, OwnerTeamID).
	AssignedUserID  string     `json:"assigned_user_id,omitempty"`
	AssignedTeamID  string     `json:"assigned_team_id,omitempty"`
	AssignmentDueAt *time.Time `json:"assignment_due_at,omitempty"`
	// LifecycleState is the state of the effective asset lifecycle model
	// (E-120); it changes only through POST
	// /api/v1/assets/{id}/lifecycle-transitions.
	LifecycleState string         `json:"lifecycle_state,omitempty"`
	Notes          string         `json:"notes,omitempty"`
	CustomFields   map[string]any `json:"custom_fields"`
	// Tags are the keys of the catalog tags of the asset (asset_tag_link,
	// WP-755), sorted, [] without tags; written by PUT
	// /api/v1/assets/{id}/tags only.
	Tags      []string  `json:"tags"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// AssetTypeID and SiteID are the type and scope of the asset for its
	// field permissions (RBA-07). Not part of the API.
	AssetTypeID string `json:"-"`
	SiteID      string `json:"-"`
	// Version counts the changes of the asset (API-07, WP-740): the
	// trigger asset_version_bump raises it on every change of a column,
	// also by the writers outside this package; it is the ETag.
	Version int64 `json:"version"`
	// PrimaryCI is the read-only technical identity of the primary CI,
	// set by GET /api/v1/assets/{id} only (WP-740).
	PrimaryCI *CIRef `json:"primary_ci,omitempty"`
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
	// LocationID, LifecycleState (E-120) and the holder cache (WP-745) are
	// read-only: a PATCH that sends them is 422 naming the canonical write
	// path.
	LocationID      ReadOnly `json:"location_id"`
	LifecycleState  ReadOnly `json:"lifecycle_state"`
	AssignedUserID  ReadOnly `json:"assigned_user_id"`
	AssignedTeamID  ReadOnly `json:"assigned_team_id"`
	AssignmentDueAt ReadOnly `json:"assignment_due_at"`
	// Check runs on the locked current asset before the write (the field
	// permissions of the handler); nil passes.
	Check func(current *Asset) error `json:"-"`
	// IfMatch is the version of the If-Match header (api.IfMatchAny for
	// "*"); nil without the header (API-07, WP-740).
	IfMatch *int64 `json:"-"`
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

// FilterParams holds filter parameters for listing assets. Status,
// Category, ClientID, Search, SortBy and SortDir are the former single
// query parameters, kept as aliases; Filters are the filter[field][op]
// conditions of API-02 on the fields of ListFields (WP-740).
type FilterParams struct {
	Status   string
	Category string
	ClientID string
	Search   string
	SortBy   string
	SortDir  string
	Filters  []api.Filter
}

// requestedFields are the trail field names (entity_change.field_name) a
// PATCH writes: the sent members, name included, and custom_fields for
// any custom field.
func (req *UpdateRequest) requestedFields() []string {
	out := sentPaths(reflect.ValueOf(req).Elem(), func(f reflect.Value) bool {
		return f.Kind() == reflect.Pointer && !f.IsNil()
	})
	if req.Name != nil {
		out = append(out, "name")
	}
	if len(req.CustomFields) > 0 {
		out = append(out, customFieldsMember)
	}
	sort.Strings(out)
	return out
}

// customFieldsMember is the DTO member and trail field of the custom
// fields.
const customFieldsMember = "custom_fields"

// Errors of the optimistic concurrency check (API-07, API-03, WP-740).
var (
	// ErrPreconditionRequired: the organization requires If-Match
	// (settings.require_if_match) and the request carried none (412).
	ErrPreconditionRequired = errors.New("If-Match is required for asset changes")
	// ErrPreconditionFailed: the If-Match version is no version of the
	// asset (412).
	ErrPreconditionFailed = errors.New("If-Match does not name a version of the asset")
	// ErrDeleteBlocked: the asset has stock movements or a disposal
	// record, both append-only; its end is a lifecycle state (retired,
	// disposed) instead of a delete (E-121, 409).
	ErrDeleteBlocked = errors.New("the asset has stock movements or a disposal record; end its lifecycle instead")
)

// ConflictError reports fields of a PATCH that changed since the If-Match
// version (409): the fields of the trail rows of the later versions, or
// every field of the request when a later version has no trail row (a
// writer outside the asset package).
type ConflictError struct {
	Fields  []string
	Current int64
}

// validation: a fixed rule text with the names of the changed fields.
func (e *ConflictError) Error() string {
	return fmt.Sprintf("fields changed since the If-Match version: %s", strings.Join(e.Fields, ", "))
}
