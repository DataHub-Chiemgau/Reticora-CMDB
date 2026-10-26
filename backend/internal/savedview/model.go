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
	// (role or custom role ids); they may read it, only the owner edits it.
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
	// LacksRelationship requires the absence of the given relationship type.
	LacksRelationship string `json:"lacks_relationship,omitempty"`
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
}

// Presets returns the standard saved views (spec §17 examples). They are
// starting points; users clone and adjust them.
func Presets() []View {
	return []View{
		{
			Name: "Available laptops in Berlin", EntityKind: "asset", Shared: true,
			FilterSpec: map[string]any{
				"ci_type": "client_laptop", "available_only": true, "location_search": "Berlin",
			},
		},
		{
			Name: "Production servers without backup relationship", EntityKind: "ci", Shared: true,
			FilterSpec: map[string]any{
				"ci_type": "physical_server", "lacks_relationship": "backed_up_by",
				"attributes": map[string]any{"environment": "production"},
			},
		},
		{
			Name: "Assets with warranty expiring within 90 days", EntityKind: "asset", Shared: true,
			FilterSpec: map[string]any{"warranty_within_days": 90},
		},
		{
			Name: "Deployed devices without assigned owner", EntityKind: "asset", Shared: true,
			FilterSpec: map[string]any{"lifecycle": "deployed", "has_owner": false},
		},
		{
			Name: "CIs with unresolved discovery conflicts", EntityKind: "ci", Shared: true,
			FilterSpec: map[string]any{"reconciliation_conflict": true},
		},
	}
}
