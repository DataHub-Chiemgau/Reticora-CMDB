package savedview

// DisplayName is the name of a system view by language (VIE-02).
type DisplayName struct {
	DE string `json:"de"`
	EN string `json:"en"`
}

// Preset is a system view (VIE-02): a code preset with a stable key, a
// display name per language and the filter, sort and columns a clone
// (POST /api/v1/saved-views with from_preset) takes over. Name is the
// English name the existing UI shows. A preset has no row in saved_view and
// cannot be changed; System is always true.
type Preset struct {
	Key         string         `json:"key"`
	Name        string         `json:"name"`
	DisplayName DisplayName    `json:"display_name"`
	EntityKind  string         `json:"entity_kind"`
	FilterSpec  map[string]any `json:"filter_spec"`
	Sort        []SortKey      `json:"sort"`
	Columns     []string       `json:"columns"`
	System      bool           `json:"system"`
}

// WarrantyExpiringDays is the threshold of the system view
// warranty_expiring: warranty_days_left at most this many days (VIE-02; the
// 30 days are an assumption of the plan, WP-307).
const WarrantyExpiringDays = 30

// MonitoringWindowHours is the window of servers_without_monitoring: no
// metric sample within it (VIE-02).
const MonitoringWindowHours = 24

// Presets returns the system views (spec §17, VIE-02) in a fixed order.
// They use the system type keys server and client with their subtypes
// (ci_type_subtree; seed 000021). Users clone and adjust them.
func Presets() []Preset {
	presets := []Preset{
		{
			// The owner of the asset (WP-271) or its current holder.
			Key: "my_devices_asset", Name: "My devices (assets)", EntityKind: "asset",
			DisplayName: DisplayName{DE: "Meine Geräte", EN: "My devices"},
			FilterSpec:  map[string]any{"owned_by_me": true},
			Sort:        []SortKey{{Field: "name", Dir: "asc"}},
			Columns:     []string{"name", "inventory_no", "lifecycle_state", "location_id"},
		},
		{
			// The owner of the CI (WP-280); the contact role owner does not count.
			Key: "my_devices_ci", Name: "My devices (CIs)", EntityKind: "ci",
			DisplayName: DisplayName{DE: "Meine Geräte", EN: "My devices"},
			FilterSpec:  map[string]any{"owned_by_me": true},
			Sort:        []SortKey{{Field: "name", Dir: "asc"}},
			Columns:     []string{"name", "ci_type", "status", "lifecycle_state"},
		},
		{
			// The computed CI attribute of the linked asset (000225, WP-718).
			Key: "warranty_expiring", Name: "Warranty expiring soon", EntityKind: "ci",
			DisplayName: DisplayName{DE: "Garantie läuft bald ab", EN: "Warranty expiring soon"},
			FilterSpec: map[string]any{"where": map[string]any{
				"field": "attributes.warranty_days_left", "op": "lte", "value": WarrantyExpiringDays,
			}},
			Sort:    []SortKey{{Field: "attributes.warranty_days_left", Dir: "asc"}},
			Columns: []string{"name", "ci_type", "attributes.warranty_days_left"},
		},
		{
			// monitored_by: the server is the source (WP-294); monitors: the
			// monitoring system is the source (000055, 000113).
			Key: "servers_without_monitoring", Name: "Servers without monitoring", EntityKind: "ci",
			DisplayName: DisplayName{DE: "Server ohne Monitoring", EN: "Servers without monitoring"},
			FilterSpec: map[string]any{
				"ci_type_subtree":             "server",
				"lacks_outgoing_relationship": "monitored_by",
				"lacks_incoming_relationship": "monitors",
				"no_metric_within_hours":      MonitoringWindowHours,
			},
			Sort:    []SortKey{{Field: "name", Dir: "asc"}},
			Columns: []string{"name", "ci_type", "hostname", "last_seen_at"},
		},
		{
			// No attribute: no migration or seed defines environment, and
			// an undefined attribute is refused (API-02, WP-309).
			Key: "servers_without_backup", Name: "Servers without backup relationship", EntityKind: "ci",
			DisplayName: DisplayName{DE: "Server ohne Backup-Beziehung", EN: "Servers without backup relationship"},
			FilterSpec:  map[string]any{"ci_type_subtree": "server", "lacks_relationship": "backed_up_by"},
			Sort:        []SortKey{{Field: "name", Dir: "asc"}},
			Columns:     []string{"name", "ci_type", "hostname"},
		},
		{
			Key: "available_clients_berlin", Name: "Available clients in Berlin", EntityKind: "asset",
			DisplayName: DisplayName{DE: "Verfügbare Clients in Berlin", EN: "Available clients in Berlin"},
			FilterSpec: map[string]any{
				"ci_type_subtree": "client", "available_only": true, "location_search": "Berlin",
			},
			Sort:    []SortKey{{Field: "name", Dir: "asc"}},
			Columns: []string{"name", "inventory_no", "lifecycle_state", "location_id"},
		},
		{
			Key: "deployed_without_holder", Name: "Deployed devices without assigned owner", EntityKind: "asset",
			DisplayName: DisplayName{DE: "Ausgegebene Geräte ohne Halter", EN: "Deployed devices without assigned owner"},
			FilterSpec:  map[string]any{"lifecycle": "deployed", "has_owner": false},
			Sort:        []SortKey{{Field: "name", Dir: "asc"}},
			Columns:     []string{"name", "inventory_no", "location_id"},
		},
		{
			Key: "discovery_conflicts", Name: "CIs with unresolved discovery conflicts", EntityKind: "ci",
			DisplayName: DisplayName{DE: "CIs mit offenen Discovery-Konflikten", EN: "CIs with unresolved discovery conflicts"},
			FilterSpec:  map[string]any{"reconciliation_conflict": true},
			Sort:        []SortKey{{Field: "name", Dir: "asc"}},
			Columns:     []string{"name", "ci_type", "discovery_source"},
		},
	}
	for i := range presets {
		presets[i].System = true
	}
	return presets
}

// PresetByKey returns the system view of the key; ok is false for an
// unknown key. The preset is a fresh copy the caller may change.
func PresetByKey(key string) (Preset, bool) {
	presets := Presets()
	for i := range presets {
		if presets[i].Key == key {
			return presets[i], true
		}
	}
	return Preset{}, false
}
