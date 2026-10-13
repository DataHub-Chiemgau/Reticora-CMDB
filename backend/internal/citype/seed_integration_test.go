package citype

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// physicalSeeds and logicalSeeds are the system types of MET-02 and MET-03.
var (
	physicalSeeds = []string{"switch", "router", "firewall", "access_point", "server", "hypervisor", "vm", "client",
		"pdu", "ups", "nas", "storage_array", "printer", "ip_phone", "camera", "generic_device", "patch_panel"}
	logicalSeeds = []string{"business_service", "application", "database", "cluster", "cloud_resource", "software"}
)

// diffKeys returns the keys of want missing in got and the keys of got not
// in want.
func diffKeys(want, got []string) (missing, extra []string) {
	for _, k := range want {
		if !slices.Contains(got, k) {
			missing = append(missing, k)
		}
	}
	for _, k := range got {
		if !slices.Contains(want, k) {
			extra = append(extra, k)
		}
	}
	return missing, extra
}

// TestSystemTypeSeedsAndFields covers WP-115 (MET-01 – MET-03) against
// PostgreSQL: the system types are exactly the 17 physical and six logical
// seeds (missing and surplus keys are listed), physical types are
// observable and logical ones are not, display names are de/en maps, the
// excluded types (network, vlan, contract) exist neither as seeds nor as
// templates, and new types get the defaults.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSystemTypeSeedsAndFields(t *testing.T) {
	f := scopetest.Seed(t, "88")
	ctx := context.Background()

	rows, err := f.Admin.Query(ctx, `SELECT key, is_logical, is_observable, display_name, schema_version
		FROM ci_type WHERE organization_id IS NULL AND is_system ORDER BY key`)
	if err != nil {
		t.Fatal(err)
	}
	var physical, logical []string
	for rows.Next() {
		var (
			key                string
			isLogical, observe bool
			raw                []byte
			schemaVersion      int
		)
		if err = rows.Scan(&key, &isLogical, &observe, &raw, &schemaVersion); err != nil {
			t.Fatal(err)
		}
		var name LocalizedText
		if err = json.Unmarshal(raw, &name); err != nil || name["de"] == "" || name["en"] == "" {
			t.Errorf("%s: display_name %s is not a de/en map", key, raw)
		}
		if observe == isLogical {
			t.Errorf("%s: is_observable %v for is_logical %v", key, observe, isLogical)
		}
		if schemaVersion != 1 {
			t.Errorf("%s: schema_version %d", key, schemaVersion)
		}
		if isLogical {
			logical = append(logical, key)
		} else {
			physical = append(physical, key)
		}
	}
	rows.Close()
	if missing, extra := diffKeys(physicalSeeds, physical); len(missing)+len(extra) > 0 {
		t.Errorf("physical seeds (MET-02): missing %v, surplus %v", missing, extra)
	}
	if missing, extra := diffKeys(logicalSeeds, logical); len(missing)+len(extra) > 0 {
		t.Errorf("logical seeds (MET-03): missing %v, surplus %v", missing, extra)
	}

	var templateKeys []string
	for i := range Templates() {
		tpl := Templates()[i]
		templateKeys = append(templateKeys, tpl.Key)
		if tpl.IsObservable == tpl.IsLogical || tpl.DisplayName["de"] == "" || tpl.DisplayName["en"] == "" {
			t.Errorf("template %s: observable %v logical %v name %v", tpl.Key, tpl.IsObservable, tpl.IsLogical, tpl.DisplayName)
		}
		if tpl.Key == "vm" && tpl.IsLogical {
			t.Error("template vm is logical; it is a physical seed type (MET-02)")
		}
	}
	sort.Strings(templateKeys)
	for _, excluded := range ExcludedTypeKeys {
		if slices.Contains(physical, excluded) || slices.Contains(logical, excluded) || slices.Contains(templateKeys, excluded) {
			t.Errorf("excluded type %s is offered (MET-03, DB-05)", excluded)
		}
	}

	// Defaults of a new type: the repository derives is_observable and the
	// missing language; the trigger covers inserts that leave both out.
	repo := NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	logicalType := &Type{OrganizationID: f.OrgA, Key: "wp115-service", Name: "wp115-service",
		DisplayName: LocalizedText{"en": "Payroll"}, IsLogical: true, IsObservable: observableDefault(nil, true)}
	if err = repo.Create(orgCtx, logicalType); err != nil {
		t.Fatalf("create logical type: %v", err)
	}
	got, err := repo.GetByID(orgCtx, f.OrgA, logicalType.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.IsObservable || got.DisplayName["de"] != "Payroll" || got.DisplayName["en"] != "Payroll" || got.SchemaVersion != 1 {
		t.Errorf("logical type = observable %v name %v schema %d", got.IsObservable, got.DisplayName, got.SchemaVersion)
	}

	var (
		observe bool
		raw     []byte
	)
	if err = f.Admin.QueryRow(ctx, `INSERT INTO ci_type (organization_id, key, name) VALUES ($1, 'wp115-device', 'Device')
		RETURNING is_observable, display_name`, f.OrgA).Scan(&observe, &raw); err != nil {
		t.Fatal(err)
	}
	if !observe || string(raw) != `{"de": "Device", "en": "Device"}` {
		t.Errorf("trigger defaults: observable %v display_name %s", observe, raw)
	}
	for _, bad := range []string{`{"fr": "Machine"}`, `{}`, `{"de": ""}`, `"Device"`} {
		_, err = f.Admin.Exec(ctx, `UPDATE ci_type SET display_name = $2::jsonb WHERE organization_id = $1 AND key = 'wp115-device'`, f.OrgA, bad)
		if bad == `{}` {
			// An emptied label is reset to the name by the trigger.
			if err != nil {
				t.Errorf("display_name {}: %v", err)
			}
			continue
		}
		if err == nil {
			t.Errorf("display_name %s accepted", bad)
		}
	}

	// Updates keep a partial label complete and switch observation.
	observeOff := false
	updated, err := repo.Update(orgCtx, f.OrgA, logicalType.ID, UpdateTypeRequest{
		DisplayName: LocalizedText{"de": "Lohnabrechnung", "en": "Payroll"}, IsObservable: &observeOff})
	if err != nil {
		t.Fatal(err)
	}
	if updated.DisplayName["de"] != "Lohnabrechnung" || updated.IsObservable {
		t.Errorf("updated = %v observable %v", updated.DisplayName, updated.IsObservable)
	}
	found, _, err := repo.List(orgCtx, f.OrgA, FilterParams{Search: "lohnab"}, api.PaginationParams{Limit: 50})
	if err != nil || len(found) != 1 || found[0].ID != logicalType.ID {
		t.Errorf("search by German label: %d results (%v)", len(found), err)
	}
}
