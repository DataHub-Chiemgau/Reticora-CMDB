// Package savedview implements saved views and the structured filter DSL
// (spec §17). Views store a filter spec compiled to SQL by the query engine
// or evaluated in memory; presets cover the standard inventory questions.
package savedview

import "time"

// View is a saved, optionally shared filter specification.
type View struct {
	ID             string         `json:"id"`
	OrganizationID string         `json:"organization_id"`
	OwnerID        string         `json:"owner_id,omitempty"`
	Name           string         `json:"name"`
	EntityKind     string         `json:"entity_kind"` // ci | asset
	FilterSpec     map[string]any `json:"filter_spec"`
	// Sort and Columns are the presentation of the view (VIE-01).
	Sort    []SortKey `json:"sort"`
	Columns []string  `json:"columns"`
	Shared  bool      `json:"shared"`
	// SharedWithRoles shares the view with the holders of these roles
	// (role or custom role ids); they may read it. Only the owner or a
	// holder of saved_view:manage changes or deletes it (VIE-02).
	SharedWithRoles []string  `json:"shared_with_roles"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// SortKey is one sort column of a view.
type SortKey struct {
	Field string `json:"field"`
	Dir   string `json:"dir"`
}

// UpsertRequest is the payload for creating or updating a saved view. A
// PATCH changes only the fields it sends: Shared is a pointer so that a
// request without it keeps the sharing (F-166); the other fields are unset
// when empty (strings) or nil (lists, filter_spec).
type UpsertRequest struct {
	Name            string         `json:"name"`
	EntityKind      string         `json:"entity_kind"`
	FilterSpec      map[string]any `json:"filter_spec"`
	Sort            []SortKey      `json:"sort,omitempty"`
	Columns         []string       `json:"columns,omitempty"`
	Shared          *bool          `json:"shared,omitempty"`
	SharedWithRoles []string       `json:"shared_with_roles,omitempty"`
	// FromPreset clones a system view (POST only, VIE-02): the server takes
	// entity_kind, filter_spec, sort and columns of the preset of this key
	// (Presets); name and sharing come from the request.
	FromPreset string `json:"from_preset,omitempty"`
}

// FilterSpec is the structured filter DSL (spec §17). Every field is
// optional; set fields are AND-ed.
type FilterSpec struct {
	// EntityKind selects the queried table: "ci" (default) or "asset".
	EntityKind      string `json:"entity_kind,omitempty"`
	CIType          string `json:"ci_type,omitempty"`
	AssetCategory   string `json:"asset_category,omitempty"`
	Lifecycle       string `json:"lifecycle,omitempty"`
	Status          string `json:"status,omitempty"`
	ClientID        string `json:"client_id,omitempty"`
	LocationID      string `json:"location_id,omitempty"`
	LocationSubtree string `json:"location_subtree,omitempty"`
	// LocationSearch matches locations by name and includes everything below
	// them, so "Berlin" selects the Berlin site and every rack inside it. It
	// is the name-based counterpart to LocationSubtree, which needs an ID.
	LocationSearch     string `json:"location_search,omitempty"`
	WarehouseOnly      bool   `json:"warehouse_only,omitempty"`
	AvailableOnly      bool   `json:"available_only,omitempty"`
	Reserved           *bool  `json:"reserved,omitempty"`
	WarrantyWithinDays int    `json:"warranty_within_days,omitempty"`
	HasOwner           *bool  `json:"has_owner,omitempty"`
	// HasRelationship requires at least one relationship of the given type.
	HasRelationship string `json:"has_relationship,omitempty"`
	// LacksRelationship requires the absence of the given relationship type,
	// in either direction.
	LacksRelationship string `json:"lacks_relationship,omitempty"`
	// LacksOutgoingRelationship requires that the CI is the source of no
	// relationship of the type, LacksIncomingRelationship that it is the
	// target of none (VIE-02, CIs only).
	LacksOutgoingRelationship string `json:"lacks_outgoing_relationship,omitempty"`
	LacksIncomingRelationship string `json:"lacks_incoming_relationship,omitempty"`
	// NoMetricWithinHours requires that no metric sample of the CI lies in
	// the last n hours (metric_sample_v; VIE-02, CIs only), 1 to
	// MaxMetricWindowHours.
	NoMetricWithinHours int `json:"no_metric_within_hours,omitempty"`
	// OwnedByMe selects what the calling user owns (VIE-02): the owner user
	// or a team of the user as owner team; for an asset also the current
	// holder (holder cache, user or team). The user is app.user_id of the
	// request transaction, never a value of the request.
	OwnedByMe bool `json:"owned_by_me,omitempty"`
	// CITypeSubtree is a type key or name including all its subtypes
	// (ci_type.parent_type_id, ci_type_subtree; VIE-02); for an asset
	// through asset_type_id or the linked CI like ci_type.
	CITypeSubtree string `json:"ci_type_subtree,omitempty"`
	// UpstreamOf / DownstreamOf scope to graph dependencies of a CI.
	UpstreamOf             string   `json:"upstream_of,omitempty"`
	DownstreamOf           string   `json:"downstream_of,omitempty"`
	DiscoverySource        string   `json:"discovery_source,omitempty"`
	ReconciliationConflict bool     `json:"reconciliation_conflict,omitempty"`
	Tags                   []string `json:"tags,omitempty"`
	// Attributes matches dynamic JSONB attributes (both type- and
	// instance-level values share ci.attributes).
	Attributes map[string]any `json:"attributes,omitempty"`
	Search     string         `json:"search,omitempty"`
	// Where is the filter AST with and, or and not (SRC-03); it is AND-ed
	// with the fields above.
	Where *Expr `json:"where,omitempty"`
	// Sort and Columns present the result of a run, they select no rows:
	// the client sends those of the saved view it runs (VIE-01, F-336).
	// Sort orders the rows (default: by name), Columns adds the values of
	// these fields to each row (QueryResult.Columns). A saved view keeps
	// them in its own sort and columns, never in its filter_spec.
	Sort    []SortKey `json:"sort,omitempty"`
	Columns []string  `json:"columns,omitempty"`
}
