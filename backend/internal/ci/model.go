// Package ci provides the CI domain model, repository, and HTTP handler for Reticora CMDB.
package ci

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Item represents a Configuration Item instance.
type Item struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	ClientID       string `json:"client_id,omitempty"`
	// LocationID is the location of the CI in the canonical location tree
	// (DB-05). SiteID and RoomID are derived from it by the database and are
	// read-only.
	LocationID string `json:"location_id,omitempty"`
	SiteID     string `json:"site_id,omitempty"`
	RoomID     string `json:"room_id,omitempty"`
	// AssetID links the CI to its asset (DB-05, AST-04). The asset is then
	// canonical for serial number, location, physical lifecycle and
	// inventory data: LocationID mirrors the asset's location and Asset
	// shows the asset's data read-only. SerialNumber stays the observed
	// value used for identity resolution.
	AssetID string      `json:"asset_id,omitempty"`
	Asset   *AssetFacts `json:"asset,omitempty"`
	// ParentCIID is the CI this one is physically part of (chassis → blade,
	// AST-05); the relationship contains is projected from it (DB-05).
	ParentCIID string `json:"parent_ci_id,omitempty"`
	// MergedIntoID is the CI this one was merged into (CI-01, CI-12);
	// only the merge sets it.
	MergedIntoID string `json:"merged_into_id,omitempty"`
	// LifecycleState is the software lifecycle; only CI types with a
	// lifecycle model have one, and it changes through lifecycle
	// transitions (DB-05).
	LifecycleState string `json:"lifecycle_state,omitempty"`
	// Health is computed from the monitoring state and read-only (DB-05):
	// ok, warning, critical or unknown.
	Health          string         `json:"health"`
	HealthUpdatedAt *time.Time     `json:"health_updated_at,omitempty"`
	CITypeID        string         `json:"ci_type_id"`
	Name            string         `json:"name"`
	Status          string         `json:"status"` // active|inactive|maintenance|decommissioned|unknown
	Manufacturer    string         `json:"manufacturer,omitempty"`
	Model           string         `json:"model,omitempty"`
	SerialNumber    string         `json:"serial_number,omitempty"`
	HardwareUUID    string         `json:"hardware_uuid,omitempty"`
	ManagementIP    string         `json:"management_ip,omitempty"`
	PrimaryMAC      string         `json:"primary_mac,omitempty"`
	Hostname        string         `json:"hostname,omitempty"`
	FQDN            string         `json:"fqdn,omitempty"`
	OSName          string         `json:"os_name,omitempty"`
	OSVersion       string         `json:"os_version,omitempty"`
	FirmwareVersion string         `json:"firmware_version,omitempty"`
	SysObjectID     string         `json:"sys_object_id,omitempty"`
	Attributes      map[string]any `json:"attributes"`
	DiscoverySource string         `json:"discovery_source,omitempty"` // snmp|ssh|redfish|ipmi|wmi|api|agent|sweep|manual
	FirstSeenAt     *time.Time     `json:"first_seen_at,omitempty"`
	LastSeenAt      *time.Time     `json:"last_seen_at,omitempty"`
	IsManual        bool           `json:"is_manual"`
	// Version is the CI's ETag; it rises only with writes of rank >= 92
	// (manual, import, workflow), not with observed updates (API-07).
	Version int64 `json:"version"`
	// SchemaVersion is the version of the materialized schema of the CI
	// type the CI was last written against (MET-16); the database sets it.
	SchemaVersion int        `json:"schema_version"`
	DeletedAt     *time.Time `json:"deleted_at,omitempty"`
	CreatedAt     string     `json:"created_at"`
	UpdatedAt     string     `json:"updated_at"`
	// ChosenType marks a create through the CI service (POST /cis): the
	// writer chose the type, which must be an active system type or an
	// active type of the organization, read in the writing transaction
	// (CI-01, TEN-09). Discovery and the agent create CIs of the types they
	// resolved without it; a type of another organization is refused on
	// every path by the trigger ci_type_organization (migration 000153).
	ChosenType bool `json:"-"`
}

// AssetFacts are the canonical data of the asset a CI is linked to, shown
// read-only on the CI (DB-05).
type AssetFacts struct {
	ID             string `json:"id"`
	AssetTag       string `json:"asset_tag"`
	Name           string `json:"name"`
	SerialNumber   string `json:"serial_number,omitempty"`
	LocationID     string `json:"location_id,omitempty"`
	LifecycleState string `json:"lifecycle_state,omitempty"`
	Status         string `json:"status"`
	Barcode        string `json:"barcode,omitempty"`
	RFIDTag        string `json:"rfid_tag,omitempty"`
	PurchaseDate   string `json:"purchase_date,omitempty"`
	WarrantyEnd    string `json:"warranty_end,omitempty"`
	Supplier       string `json:"supplier,omitempty"`
	InvoiceNumber  string `json:"invoice_number,omitempty"`
}

// CreateRequest is the payload for creating a CI.
type CreateRequest struct {
	CITypeID        string         `json:"ci_type_id"`
	ClientID        string         `json:"client_id,omitempty"`
	LocationID      string         `json:"location_id,omitempty"`
	AssetID         string         `json:"asset_id,omitempty"`
	ParentCIID      string         `json:"parent_ci_id,omitempty"`
	Name            string         `json:"name"`
	Status          string         `json:"status,omitempty"`
	Manufacturer    string         `json:"manufacturer,omitempty"`
	Model           string         `json:"model,omitempty"`
	SerialNumber    string         `json:"serial_number,omitempty"`
	HardwareUUID    string         `json:"hardware_uuid,omitempty"`
	ManagementIP    string         `json:"management_ip,omitempty"`
	PrimaryMAC      string         `json:"primary_mac,omitempty"`
	Hostname        string         `json:"hostname,omitempty"`
	FQDN            string         `json:"fqdn,omitempty"`
	OSName          string         `json:"os_name,omitempty"`
	OSVersion       string         `json:"os_version,omitempty"`
	FirmwareVersion string         `json:"firmware_version,omitempty"`
	SysObjectID     string         `json:"sys_object_id,omitempty"`
	Attributes      map[string]any `json:"attributes,omitempty"`
	DiscoverySource string         `json:"discovery_source,omitempty"`
}

// UpdateRequest is the payload for updating a CI.
type UpdateRequest struct {
	Name       *string `json:"name,omitempty"`
	Status     *string `json:"status,omitempty"`
	ClientID   *string `json:"client_id,omitempty"`
	LocationID *string `json:"location_id,omitempty"`
	// AssetID links ("" unlinks) the CI to an asset.
	AssetID *string `json:"asset_id,omitempty"`
	// ParentCIID sets ("" clears) the containing CI.
	ParentCIID      *string        `json:"parent_ci_id,omitempty"`
	Manufacturer    *string        `json:"manufacturer,omitempty"`
	Model           *string        `json:"model,omitempty"`
	SerialNumber    *string        `json:"serial_number,omitempty"`
	HardwareUUID    *string        `json:"hardware_uuid,omitempty"`
	ManagementIP    *string        `json:"management_ip,omitempty"`
	PrimaryMAC      *string        `json:"primary_mac,omitempty"`
	Hostname        *string        `json:"hostname,omitempty"`
	FQDN            *string        `json:"fqdn,omitempty"`
	OSName          *string        `json:"os_name,omitempty"`
	OSVersion       *string        `json:"os_version,omitempty"`
	FirmwareVersion *string        `json:"firmware_version,omitempty"`
	SysObjectID     *string        `json:"sys_object_id,omitempty"`
	Attributes      map[string]any `json:"attributes,omitempty"`
	DiscoverySource *string        `json:"discovery_source,omitempty"`
	LastSeenAt      *string        `json:"last_seen_at,omitempty"`
	// ChangeReason is stored with the overrides a manual change creates.
	ChangeReason string `json:"change_reason,omitempty"`
	// IfMatch is the version the writer last read (If-Match); nil when the
	// request carried none, api.IfMatchAny for "*". Checked for manual
	// changes only.
	IfMatch *int64 `json:"-"`
	// Authoritative marks an automated write of rank >= 92 (workflow,
	// import), which raises the version like a manual change does.
	Authoritative bool `json:"-"`
	// Manual marks a change made by a person through the API (not by
	// discovery or automation): every changed field gets a protected
	// override in the same transaction (OVR-01). Set by the API paths only
	// (PATCH, bulk status change).
	Manual *ManualChange `json:"-"`
	// Nulls lists the members the JSON body set to null (RFC 7396, CI-04).
	// UnmarshalJSON collects them: a nullable field is cleared (its pointer
	// is set to "", its override holds null), any other member is refused by
	// the service with 422 on the field.
	Nulls []string `json:"-"`
}

// nullableFields are the members of UpdateRequest a JSON null clears (RFC
// 7396): the typed columns and the references that "" already clears.
// name and status are required; null on them, on attributes,
// discovery_source, last_seen_at and change_reason is refused.
var nullableFields = map[string]bool{
	"client_id": true, "location_id": true, "asset_id": true, "parent_ci_id": true,
	"manufacturer": true, "model": true, "serial_number": true, "hardware_uuid": true,
	"management_ip": true, "primary_mac": true, "hostname": true, "fqdn": true,
	"os_name": true, "os_version": true, "firmware_version": true, "sys_object_id": true,
}

// UnmarshalJSON decodes a merge patch presence-aware (RFC 7396, CI-04): an
// absent member stays nil (unchanged), a null member is recorded in Nulls
// and a nullable one is set to "" (cleared). Unknown members are refused
// as by api.ReadJSON.
func (r *UpdateRequest) UnmarshalJSON(data []byte) error {
	type plain UpdateRequest
	var req plain
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return err
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return err
	}
	*r = UpdateRequest(req)
	r.Nulls = nil
	for name, raw := range members {
		if !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue
		}
		r.Nulls = append(r.Nulls, name)
		if nullableFields[name] {
			*r.field(name) = new(string)
		}
	}
	slices.Sort(r.Nulls)
	return nil
}

// field is the pointer field of a nullable member.
func (r *UpdateRequest) field(name string) **string {
	return map[string]**string{
		"client_id": &r.ClientID, "location_id": &r.LocationID, "asset_id": &r.AssetID,
		"parent_ci_id": &r.ParentCIID, "manufacturer": &r.Manufacturer, "model": &r.Model,
		"serial_number": &r.SerialNumber, "hardware_uuid": &r.HardwareUUID,
		"management_ip": &r.ManagementIP, "primary_mac": &r.PrimaryMAC, "hostname": &r.Hostname,
		"fqdn": &r.FQDN, "os_name": &r.OSName, "os_version": &r.OSVersion,
		"firmware_version": &r.FirmwareVersion, "sys_object_id": &r.SysObjectID,
	}[name]
}

// isNull reports whether the request set the member to null.
func (r *UpdateRequest) isNull(name string) bool {
	return slices.Contains(r.Nulls, name)
}

// nullViolations refuses null on the members that cannot be cleared: the
// required fields name and status and the members outside nullableFields.
func (r *UpdateRequest) nullViolations() error {
	var violations []Violation
	for _, name := range r.Nulls {
		if !nullableFields[name] {
			violations = append(violations, Violation{Field: name, Detail: "must not be null"})
		}
	}
	if len(violations) > 0 {
		return &ValidationError{Violations: violations}
	}
	return nil
}

// ManualChange identifies the author of a manual CI change.
type ManualChange struct {
	Author string
}

// DefaultChangeReason is the override reason of a manual change without one.
const DefaultChangeReason = "manual change"

// FilterParams holds query filter parameters for listing CIs.
type FilterParams struct {
	Status   string
	TypeID   string
	ClientID string
	SiteID   string
	RoomID   string
	Search   string
	SortBy   string
	SortDir  string
	// Filters are the filter[field][op] conditions of API-02.
	Filters []api.Filter
}

// Change represents one persisted CI change history row.
type Change struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	CIID           string    `json:"ci_id"`
	ActorID        string    `json:"actor_id,omitempty"`
	ChangeType     string    `json:"change_type"`
	FieldName      string    `json:"field_name,omitempty"`
	OldValue       any       `json:"old_value,omitempty"`
	NewValue       any       `json:"new_value,omitempty"`
	Comment        string    `json:"comment,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// ErrNotFound is the error of the repositories for a CI that does not exist,
// is deleted or is not visible to the principal. Only it is answered with
// 404; its text stays "not found" for the callers that compare it.
var ErrNotFound = errors.New("not found")

// Errors of the optimistic concurrency check (API-07, API-03).
var (
	// ErrPreconditionRequired: the organization requires If-Match and the
	// request carried none (412).
	ErrPreconditionRequired = errors.New("If-Match is required for CI changes")
	// ErrPreconditionFailed: the If-Match version is unknown (412).
	ErrPreconditionFailed = errors.New("If-Match does not name a version of the CI")
)

// ConflictError reports fields of a PATCH that were changed by a manual or
// other rank >= 92 write since the If-Match version (409).
type ConflictError struct {
	Fields  []string
	Current int64
}

// validation: a fixed rule text with the names of the changed fields.
func (e *ConflictError) Error() string {
	return fmt.Sprintf("fields changed since the If-Match version: %s", strings.Join(e.Fields, ", "))
}
