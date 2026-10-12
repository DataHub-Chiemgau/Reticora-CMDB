package ci_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
)

// TestAttributeNamespacesOnPostgreSQL covers WP-091 (CI-10, GLO-13) against
// PostgreSQL:
//   - attributes stores _provenance {field: {source, rank, at}}, _overrides
//     {field: {value, by, at, reason}} and, under an override, _observed
//     {field: {value, source, rank, at}}; they follow ci_field_value;
//   - instance attribute values are stored in _instance and read under
//     their names; a PATCH keeps the namespaces;
//   - migration 000099 moves existing instance attribute values.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAttributeNamespacesOnPostgreSQL(t *testing.T) {
	f := scopetest.Seed(t, "83")
	bg := context.Background()
	ctx := f.OrgCtx(f.OrgA)
	cis := ci.NewPGRepository(f.App)
	svc := ci.NewService(cis)
	overrides := override.NewPGRepository(f.App)
	device := f.CI(t, f.OrgA, "", "ns-83")
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci_instance_field_definition (organization_id, ci_id, name, data_type)
		VALUES ($1, $2, 'rack_label', 'text')`, f.OrgA, device); err != nil {
		t.Fatal(err)
	}
	stored := func() map[string]any {
		t.Helper()
		var raw []byte
		if err := f.Admin.QueryRow(bg, `SELECT attributes FROM ci WHERE id = $1`, device).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	ns := func(attrs map[string]any, namespace, field string) map[string]any {
		m, _ := attrs[namespace].(map[string]any)
		entry, _ := m[field].(map[string]any)
		return entry
	}

	// Business and instance attributes.
	if _, err := svc.Update(ctx, f.OrgA, device, ci.UpdateRequest{Attributes: map[string]any{"rack_label": "A-07", "owner": "ops"}}); err != nil {
		t.Fatal(err)
	}
	attrs := stored()
	instance, _ := attrs["_instance"].(map[string]any)
	if attrs["owner"] != "ops" || attrs["rack_label"] != nil || instance["rack_label"] != "A-07" {
		t.Fatalf("stored attributes: %v", attrs)
	}
	item, err := cis.GetByID(ctx, f.OrgA, device)
	if err != nil || item.Attributes["rack_label"] != "A-07" || item.Attributes["_instance"] != nil {
		t.Fatalf("read view: %v, %v", item.Attributes, err)
	}

	// Provenance of a discovered value.
	observed := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	if _, err = overrides.RecordDiscovered(ctx, f.OrgA, device, "model", "R760", "redfish", observed); err != nil {
		t.Fatal(err)
	}
	attrs = stored()
	if p := ns(attrs, "_provenance", "model"); p["source"] != "redfish" || p["rank"] != 90.0 || !strings.HasPrefix(p["at"].(string), "2026-10-12T09:00:00") {
		t.Errorf("_provenance.model: %v", p)
	}
	if attrs["_overrides"] != nil || attrs["_observed"] != nil {
		t.Errorf("namespaces without an override: %v", attrs)
	}

	// An override; the observed value is held back in _observed.
	if _, err = overrides.SetOverride(ctx, f.OrgA, device, "model", "R760-manual", f.User, "ticket 91", true); err != nil {
		t.Fatal(err)
	}
	if _, err = overrides.RecordDiscovered(ctx, f.OrgA, device, "model", "R760xs", "redfish", observed.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	attrs = stored()
	if o := ns(attrs, "_overrides", "model"); o["value"] != "R760-manual" || o["by"] != f.User || o["reason"] != "ticket 91" || o["at"] == nil {
		t.Errorf("_overrides.model: %v", o)
	}
	if o := ns(attrs, "_observed", "model"); o["value"] != "R760xs" || o["source"] != "redfish" || o["rank"] != 90.0 {
		t.Errorf("_observed.model: %v", o)
	}

	// A PATCH of business attributes keeps every namespace.
	if _, err = svc.Update(ctx, f.OrgA, device, ci.UpdateRequest{Attributes: map[string]any{"owner": "noc"}}); err != nil {
		t.Fatal(err)
	}
	attrs = stored()
	if attrs["owner"] != "noc" || ns(attrs, "_overrides", "model") == nil || ns(attrs, "_provenance", "model") == nil ||
		attrs["_instance"].(map[string]any)["rack_label"] != "A-07" {
		t.Errorf("after a PATCH: %v", attrs)
	}

	// Clearing the override removes _overrides and _observed.
	if _, err = overrides.ClearOverride(ctx, f.OrgA, device, "model"); err != nil {
		t.Fatal(err)
	}
	if attrs = stored(); attrs["_overrides"] != nil || attrs["_observed"] != nil || ns(attrs, "_provenance", "model") == nil {
		t.Errorf("after clearing the override: %v", attrs)
	}

	// Migration 000099 moves stored instance values into _instance.
	legacy := f.CI(t, f.OrgA, "", "ns-legacy")
	if _, err = f.Admin.Exec(bg, `INSERT INTO ci_instance_field_definition (organization_id, ci_id, name, data_type)
		VALUES ($1, $2, 'slot', 'text')`, f.OrgA, legacy); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../migrations/000099_ci_namespaces.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	move := text[strings.Index(text, "UPDATE ci SET attributes = (ci.attributes - inst.names)"):]
	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	if _, err = tx.Exec(bg, `UPDATE ci SET attributes = '{"slot": "7", "owner": "ops"}' WHERE id = $1`, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(bg, move); err != nil {
		t.Fatal(err)
	}
	var moved string
	if err = tx.QueryRow(bg, `SELECT attributes::text FROM ci WHERE id = $1`, legacy).Scan(&moved); err != nil ||
		moved != `{"owner": "ops", "_instance": {"slot": "7"}}` {
		t.Errorf("migrated attributes: %s, %v", moved, err)
	}
}
