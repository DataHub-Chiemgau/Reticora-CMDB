package ci_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// errRollback ends a tenant transaction of a test without committing it.
var errRollback = errors.New("rollback")

// TestMigration99InstanceAttributesReadView covers WP-478 (CI-10, E-23):
// the data part of migration 000099 on legacy rows, with the read view of
// the CI repository. Instance attribute values on the top level of
// attributes move into _instance (an existing _instance keeps its other
// keys); a key without an instance definition and the CIs of another
// organization stay where they are. The repository reads the instance
// attributes under their names and shows none of the four namespaces
// (CI-10). The statement runs as the runtime role in a tenant transaction
// of organization A, which is rolled back.
func TestMigration99InstanceAttributesReadView(t *testing.T) {
	f := scopetest.Seed(t, "83")
	bg := context.Background()
	cis := ci.NewPGRepository(f.App)
	legacy := f.CI(t, f.OrgA, "", "ns99-legacy")
	partial := f.CI(t, f.OrgA, "", "ns99-partial")
	plain := f.CI(t, f.OrgA, "", "ns99-plain")
	foreign := f.CI(t, f.OrgB, "", "ns99-foreign")
	for _, def := range []struct{ org, ci, name string }{
		{f.OrgA, legacy, "slot"}, {f.OrgA, legacy, "rack_label"}, {f.OrgA, partial, "slot"}, {f.OrgB, foreign, "slot"},
	} {
		if _, err := f.Admin.Exec(bg, `INSERT INTO ci_instance_field_definition (organization_id, ci_id, name, data_type)
			VALUES ($1, $2, $3, 'text')`, def.org, def.ci, def.name); err != nil {
			t.Fatal(err)
		}
	}
	legacyAttrs := map[string]string{
		legacy:  `{"slot": "7", "rack_label": "A-1", "owner": "ops"}`,
		partial: `{"slot": "9", "_instance": {"note": "kept"}}`,
		plain:   `{"slot": "1"}`,
		foreign: `{"slot": "3"}`,
	}
	for id, attrs := range legacyAttrs {
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET attributes = $2::jsonb WHERE id = $1`, id, attrs); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile("../../migrations/000099_ci_namespaces.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	move := text[strings.Index(text, "UPDATE ci SET attributes = (ci.attributes - inst.names)"):]

	err = database.WithRequestTenant(f.OrgCtx(f.OrgA), f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		if _, execErr := tx.Exec(ctx, move); execErr != nil {
			t.Fatal(execErr)
		}
		for id, want := range map[string]string{
			legacy:  `{"owner": "ops", "_instance": {"slot": "7", "rack_label": "A-1"}}`,
			partial: `{"_instance": {"note": "kept", "slot": "9"}}`,
			plain:   `{"slot": "1"}`,
		} {
			var stored string
			if scanErr := tx.QueryRow(ctx, `SELECT attributes::text FROM ci WHERE id = $1`, id).Scan(&stored); scanErr != nil || stored != want {
				t.Errorf("stored attributes of %s = %s, %v; want %s", id, stored, scanErr, want)
			}
		}
		// The read view of the repository in the same transaction.
		ctx = database.ContextWithTx(ctx, tx)
		for id, want := range map[string]map[string]any{
			legacy:  {"slot": "7", "rack_label": "A-1", "owner": "ops"},
			partial: {"note": "kept", "slot": "9"},
			plain:   {"slot": "1"},
		} {
			item, getErr := cis.GetByID(ctx, f.OrgA, id)
			if getErr != nil {
				t.Fatalf("read %s: %v", id, getErr)
			}
			if !reflect.DeepEqual(item.Attributes, want) {
				t.Errorf("read view of %s = %v, want %v", id, item.Attributes, want)
			}
			for _, namespace := range ci.ReservedNamespaces {
				if _, ok := item.Attributes[namespace]; ok {
					t.Errorf("read view of %s shows the namespace %s: %v", id, namespace, item.Attributes)
				}
			}
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("tenant transaction: %v", err)
	}
	// Row-level security kept the statement to organization A.
	var stored string
	if err := f.Admin.QueryRow(bg, `SELECT attributes::text FROM ci WHERE id = $1`, foreign).Scan(&stored); err != nil || stored != `{"slot": "3"}` {
		t.Errorf("attributes of the other organization's CI = %s, %v; want unchanged", stored, err)
	}
}

// TestMigration166QuarantinesReservedFieldValues covers WP-478 (GLO-13,
// E-23; F-092): migration 000166 moves ci_field_value rows whose field_name
// is one of the four namespaces into migration_quarantine (one row each,
// with the whole row as row_data), leaves the other rows alone and adds the
// CHECK constraint ci_field_value_field_name_not_reserved, which then
// refuses such a row, also for the runtime role. The down migration drops
// only the constraint. The migration files run in a transaction of the
// maintenance pool, which is rolled back.
func TestMigration166QuarantinesReservedFieldValues(t *testing.T) {
	f := scopetest.Seed(t, "83")
	bg := context.Background()
	device := f.CI(t, f.OrgA, "", "ns166")
	reserved := []string{"_instance", "_observed", "_overrides", "_provenance"}

	// The migrated schema refuses a reserved field name for the runtime role.
	err := database.WithRequestTenant(f.OrgCtx(f.OrgA), f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO ci_field_value (organization_id, ci_id, field_name, override_value, override_at)
			VALUES ($1, $2, '_instance', '"x"', now())`, f.OrgA, device)
		return err
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "ci_field_value_field_name_not_reserved" {
		t.Errorf("runtime role writes a reserved field name: %v, want the CHECK constraint", err)
	}

	read := func(file string) string {
		t.Helper()
		raw, readErr := os.ReadFile("../../migrations/" + file)
		if readErr != nil {
			t.Fatal(readErr)
		}
		return string(raw)
	}
	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	if _, err = tx.Exec(bg, read("000166_ci_field_value_reserved.down.sql")); err != nil {
		t.Fatalf("down migration: %v", err)
	}
	// Legacy rows: the four namespaces as field names next to a normal field.
	for _, name := range append([]string{"owner"}, reserved...) {
		if _, err = tx.Exec(bg, `INSERT INTO ci_field_value (organization_id, ci_id, field_name, override_value, override_reason, override_at, protected)
			VALUES ($1, $2, $3, '"x"', 'legacy', now(), true)`, f.OrgA, device, name); err != nil {
			t.Fatalf("legacy row %s: %v", name, err)
		}
	}
	var quarantinedBefore int
	if err = tx.QueryRow(bg, `SELECT count(*) FROM migration_quarantine WHERE migration = '000166'`).Scan(&quarantinedBefore); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(bg, read("000166_ci_field_value_reserved.up.sql")); err != nil {
		t.Fatalf("up migration: %v", err)
	}

	var quarantined, total int
	var names, reason string
	if err = tx.QueryRow(bg, `SELECT count(*), string_agg(row_data ->> 'field_name', ',' ORDER BY row_data ->> 'field_name'),
			min(reason), (SELECT count(*) FROM migration_quarantine WHERE migration = '000166')
		FROM migration_quarantine
		WHERE migration = '000166' AND source_table = 'ci_field_value' AND organization_id = $1
		  AND row_data ->> 'ci_id' = $2 AND row_data ->> 'override_reason' = 'legacy'`, f.OrgA, device).
		Scan(&quarantined, &names, &reason, &total); err != nil {
		t.Fatal(err)
	}
	if quarantined != 4 || names != strings.Join(reserved, ",") || total-quarantinedBefore != 4 || !strings.Contains(reason, "GLO-13") {
		t.Errorf("quarantined %d rows (%s, %q), %d new in all; want the 4 reserved rows", quarantined, names, reason, total-quarantinedBefore)
	}
	var remaining string
	if err = tx.QueryRow(bg, `SELECT string_agg(field_name, ',') FROM ci_field_value WHERE ci_id = $1`, device).Scan(&remaining); err != nil || remaining != "owner" {
		t.Errorf("field values after the migration: %q, %v; want owner only", remaining, err)
	}
	// The projection follows: _overrides names only the remaining field.
	var overrides string
	if err = tx.QueryRow(bg, `SELECT (SELECT string_agg(k, ',') FROM jsonb_object_keys(attributes -> '_overrides') AS k) FROM ci WHERE id = $1`, device).Scan(&overrides); err != nil || overrides != "owner" {
		t.Errorf("attributes._overrides keys after the migration: %q, %v; want owner", overrides, err)
	}
	for _, name := range reserved {
		if _, err = tx.Exec(bg, `SAVEPOINT reserved`); err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(bg, `INSERT INTO ci_field_value (organization_id, ci_id, field_name, discovered_value, discovered_at)
			VALUES ($1, $2, $3, '"x"', now())`, f.OrgA, device, name)
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "ci_field_value_field_name_not_reserved" {
			t.Errorf("insert of %s after the migration: %v, want the CHECK constraint", name, err)
		}
		if _, err = tx.Exec(bg, `ROLLBACK TO SAVEPOINT reserved`); err != nil {
			t.Fatal(err)
		}
	}
	// A name that only starts with an underscore stays a field name.
	if _, err = tx.Exec(bg, `INSERT INTO ci_field_value (organization_id, ci_id, field_name, discovered_value, discovered_at)
		VALUES ($1, $2, '_instance_note', '"x"', now())`, f.OrgA, device); err != nil {
		t.Errorf("insert of _instance_note: %v", err)
	}
	// The down migration drops the constraint; the quarantine stays.
	if _, err = tx.Exec(bg, read("000166_ci_field_value_reserved.down.sql")); err != nil {
		t.Fatalf("down migration: %v", err)
	}
	if err = tx.QueryRow(bg, `SELECT count(*) FROM migration_quarantine WHERE migration = '000166'`).Scan(&total); err != nil || total-quarantinedBefore != 4 {
		t.Errorf("quarantine after the down migration: %d new rows, %v; want 4", total-quarantinedBefore, err)
	}
}
