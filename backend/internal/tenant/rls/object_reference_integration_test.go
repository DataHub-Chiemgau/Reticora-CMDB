package rls_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant/rls"
)

const (
	refsBegin = "<!-- rls-references-without-organization:begin -->"
	refsEnd   = "<!-- rls-references-without-organization:end -->"
)

// tenantReferenceQuery lists every foreign key between two tenant tables
// (tables of schema public with an organization_id column) whose target
// has no global rows (organization_id NOT NULL; targets such as ci_type
// hold global rows, E-10): "table.columns", constraint, target, its
// definition and whether it carries the organization, i.e. pairs
// organization_id of the referencing table with organization_id of the
// target.
const tenantReferenceQuery = `
SELECT c.conrelid::regclass::text || '.' ||
       (SELECT string_agg(a.attname, ',' ORDER BY k.n)
          FROM unnest(c.conkey) WITH ORDINALITY AS k(attnum, n)
          JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
         WHERE a.attname <> 'organization_id'),
       c.conname, c.confrelid::regclass::text, pg_get_constraintdef(c.oid),
       EXISTS (SELECT 1
                 FROM unnest(c.conkey, c.confkey) AS k(src, dst)
                 JOIN pg_attribute s ON s.attrelid = c.conrelid AND s.attnum = k.src
                 JOIN pg_attribute d ON d.attrelid = c.confrelid AND d.attnum = k.dst
                WHERE s.attname = 'organization_id' AND d.attname = 'organization_id')
  FROM pg_constraint c
  JOIN pg_class t ON t.oid = c.conrelid
  JOIN pg_namespace n ON n.oid = t.relnamespace
 WHERE c.contype = 'f' AND n.nspname = 'public'
   AND EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid = c.conrelid
                 AND a.attname = 'organization_id' AND NOT a.attisdropped)
   AND EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid = c.confrelid
                 AND a.attname = 'organization_id' AND NOT a.attisdropped AND a.attnotnull)
 ORDER BY 1, 2`

// objectReference is one of the twelve references migration 000194 makes
// composite, with the writes the negative test runs against it.
type objectReference struct {
	// def is the expected pg_get_constraintdef of the foreign key named
	// constraint: composite, ON DELETE as before.
	constraint, def string
	// app is the write of the application role; args gives its arguments
	// for the referenced id. A reference into another organization fails
	// with 23503 naming appConstraint (a trigger can report the reference
	// before the foreign key does, under the same name), the own object is
	// accepted.
	app           string
	appConstraint string
	// fk is the write the foreign key alone has to refuse, run with the
	// triggers of the table switched off.
	fk   string
	args func(ref string) []any
	// foreign is the referenced object of organization B, own the one of
	// organization A.
	foreign, own string
}

// TestTenantReferencesCarryOrganization covers WP-838 (SR-03; ABN-03,
// TEN-09; E-137): every foreign key between two tenant tables carries the
// organization, (column, organization_id) REFERENCES … (id,
// organization_id), so the database refuses a reference into another
// organization although it checks foreign keys without row level security.
// The references that do not yet are listed with their reason in
// rls.ReferencesWithoutOrganization; a new reference without the
// organization and an entry that no longer occurs (fixed or dropped) both
// fail, so the list only shrinks (pattern TestKnownGapsList). Targets with
// global rows (organization_id nullable, e.g. ci_type) are left out.
//
// The twelve references of migration 000194 are composite with the former
// constraint names and ON DELETE actions, and location and consumable hold
// UNIQUE (id, organization_id). Negative test as the application role in
// organization A: a write of each of these columns with the id of an
// object of organization B fails with 23503 (foreign_key_violation), the
// same write with the own object succeeds. With the triggers of the table
// switched off, the foreign key alone refuses each of them (location.site_id
// is derived from the parent by location_maintain and can only be written
// this way).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -v -run 'TestTenantReferences|TestReferencesWithoutOrganization' ./internal/tenant/rls/
func TestTenantReferencesCarryOrganization(t *testing.T) {
	f := scopetest.Seed(t, "cb")
	ctx := context.Background()
	requireMigrated(ctx, t, f.Admin)

	refs := seedObjectReferences(t, f)

	type found struct{ constraint, target, def string }
	without := map[string]found{}
	defs := map[string]string{}
	rows, err := f.Admin.Query(ctx, tenantReferenceQuery)
	if err != nil {
		t.Fatalf("tenant reference query: %v", err)
	}
	for rows.Next() {
		var key, constraint, target, def string
		var carries bool
		if err = rows.Scan(&key, &constraint, &target, &def, &carries); err != nil {
			t.Fatal(err)
		}
		defs[constraint] = def
		if !carries {
			without[key] = found{constraint, target, def}
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatalf("tenant reference query: %v", err)
	}

	var unlisted, closed, changed []string
	for _, key := range slices.Sorted(maps.Keys(without)) {
		got := without[key]
		known, ok := rls.ReferencesWithoutOrganization[key]
		switch {
		case !ok:
			unlisted = append(unlisted, fmt.Sprintf("%s (%s): %s", key, got.constraint, got.def))
		case known.Constraint != got.constraint || known.Target != got.target:
			changed = append(changed, fmt.Sprintf("%s: listed as %s on %s, found %s on %s",
				key, known.Constraint, known.Target, got.constraint, got.target))
		}
	}
	for _, key := range slices.Sorted(maps.Keys(rls.ReferencesWithoutOrganization)) {
		if _, ok := without[key]; !ok {
			closed = append(closed, key)
		}
	}
	if len(unlisted) > 0 {
		t.Errorf("SR-03/K19 violated: foreign keys between tenant tables without the organization; reference "+
			"(column, organization_id) REFERENCES … (id, organization_id) (pattern 000136, 000194):\n  %s",
			strings.Join(unlisted, "\n  "))
	}
	if len(closed) > 0 {
		t.Errorf("rls.ReferencesWithoutOrganization lists references that carry the organization or no longer "+
			"exist; remove them and regenerate docs/schema-baseline.md with %s=1:\n  %s",
			updateDocEnv, strings.Join(closed, "\n  "))
	}
	if len(changed) > 0 {
		t.Errorf("rls.ReferencesWithoutOrganization differs from the catalog:\n  %s", strings.Join(changed, "\n  "))
	}

	for _, column := range slices.Sorted(maps.Keys(refs)) {
		r := refs[column]
		if got := defs[r.constraint]; got != r.def {
			t.Errorf("%s: %s is %q, want %q", column, r.constraint, got, r.def)
		}
		if _, listed := rls.ReferencesWithoutOrganization[column]; listed {
			t.Errorf("%s is composite since migration 000194 and must not be listed", column)
		}
	}
	for _, table := range []string{"location", "consumable"} {
		var unique bool
		if err = f.Admin.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM pg_constraint
			                WHERE conrelid = $1::regclass AND contype = 'u'
			                  AND pg_get_constraintdef(oid) = 'UNIQUE (id, organization_id)')`,
			table).Scan(&unique); err != nil {
			t.Fatal(err)
		}
		if !unique {
			t.Errorf("%s has no UNIQUE (id, organization_id)", table)
		}
	}

	scope := database.OrgWideScope(f.OrgA, f.User)
	for _, column := range slices.Sorted(maps.Keys(refs)) {
		r := refs[column]
		t.Run("app/"+column, func(t *testing.T) {
			foreignErr := appWrite(ctx, f, &scope, r.app, r.args(r.foreign))
			var pgErr *pgconn.PgError
			if !errors.As(foreignErr, &pgErr) || pgErr.Code != "23503" || pgErr.ConstraintName != r.appConstraint {
				t.Errorf("write with an object of another organization: got %v, want 23503 on %s", foreignErr, r.appConstraint)
			}
			if ownErr := appWrite(ctx, f, &scope, r.app, r.args(r.own)); ownErr != nil {
				t.Errorf("write with the own object: %v", ownErr)
			}
		})
	}

	// The foreign keys alone: the triggers that check or derive a reference
	// themselves (ci_derive_location, derive_location_site,
	// location_maintain) are off in a transaction that is rolled back.
	tx, err := f.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, table := range []string{"asset", "ci", "ticket", "reservation", "disposal_record", "internal_order_item", "location"} {
		if _, err = tx.Exec(ctx, `ALTER TABLE `+table+` DISABLE TRIGGER USER`); err != nil {
			t.Fatalf("disable triggers of %s: %v", table, err)
		}
	}
	for _, column := range slices.Sorted(maps.Keys(refs)) {
		r := refs[column]
		sp, spErr := tx.Begin(ctx)
		if spErr != nil {
			t.Fatal(spErr)
		}
		_, err = sp.Exec(ctx, r.fk, r.args(r.foreign)...)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23503" || pgErr.ConstraintName != r.constraint {
			t.Errorf("fk/%s: write with an object of another organization without triggers: got %v, want 23503 on %s",
				column, err, r.constraint)
		}
		if err = sp.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

// appWrite runs stmt as the application role in scope and rolls it back.
func appWrite(ctx context.Context, f *scopetest.Fixture, scope *database.TenantScope, stmt string, args []any) error {
	err := database.WithTenant(ctx, f.App, scope, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, stmt, args...); err != nil {
			return err
		}
		return errRollback
	})
	if errors.Is(err, errRollback) {
		return nil
	}
	return err
}

// seedObjectReferences seeds a site, a CI, an asset and a consumable in
// both organizations plus the rows of organization A the writes change,
// and returns the twelve references of migration 000194 by "table.column".
func seedObjectReferences(t *testing.T, f *scopetest.Fixture) map[string]objectReference {
	t.Helper()
	ctx := context.Background()
	exec := func(stmt string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(ctx, stmt, args...); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	clientB := f.ID()
	exec(`INSERT INTO client (id, organization_id, name, slug) VALUES ($1, $2, 'Client B', $3)`,
		clientB, f.OrgB, "scopetest-cb-w838-b")
	siteA, siteB := f.ID(), f.ID()
	exec(`INSERT INTO site (id, organization_id, client_id, name) VALUES ($1, $2, $3, 'w838 A')`, siteA, f.OrgA, f.Client1)
	exec(`INSERT INTO site (id, organization_id, client_id, name) VALUES ($1, $2, $3, 'w838 B')`, siteB, f.OrgB, clientB)
	ciA, ciB := f.CI(t, f.OrgA, "", "w838-a"), f.CI(t, f.OrgB, "", "w838-b")
	assetA, assetB := f.Asset(t, f.OrgA, "", "w838-a"), f.Asset(t, f.OrgB, "", "w838-b")
	consumableA, consumableB := f.ID(), f.ID()
	exec(`INSERT INTO consumable (id, organization_id, name) VALUES ($1, $2, 'w838 A')`, consumableA, f.OrgA)
	exec(`INSERT INTO consumable (id, organization_id, name) VALUES ($1, $2, 'w838 B')`, consumableB, f.OrgB)
	user := f.AppUser(t, f.OrgA, "w838")
	order := f.ID()
	exec(`INSERT INTO internal_order (id, organization_id, order_number, title) VALUES ($1, $2, 'W838', 'w838')`, order, f.OrgA)

	org := f.OrgA
	byID := func(id string) func(string) []any {
		return func(ref string) []any { return []any{ref, id} }
	}
	inOrg := func(ref string) []any { return []any{ref, org} }
	ticket := `INSERT INTO ticket (organization_id, title, reporter_id, %s) VALUES ($2, 'w838', '` + user + `', $1)`
	// With the triggers off no ticket number is assigned (ticket_number_assign, 000214).
	ticketFK := `INSERT INTO ticket (organization_id, title, reporter_id, ticket_number, %s) VALUES ($2, 'w838', '` + user + `', 1, $1)`
	disposal := `INSERT INTO disposal_record (organization_id, method, performed_at, %s) VALUES ($2, 'reuse', now(), $1)`
	// With the triggers off no record hash is set (disposal_record_set_hash, 000245).
	disposalFK := `INSERT INTO disposal_record (organization_id, method, performed_at, record_hash, %s) VALUES ($2, 'reuse', now(), 'w838', $1)`
	orderItem := `INSERT INTO internal_order_item (organization_id, order_id, description, quantity, %s)
		VALUES ($2, '` + order + `', 'w838', 1, $1)`
	// A warehouse below the site; with the triggers off, path is set by hand.
	location := `INSERT INTO location (organization_id, client_id, kind, name, parent_id, site_id%s)
		VALUES ($2, '` + f.Client1 + `', 'warehouse', 'w838', %s, %s%s)`
	app := func(stmt string) string { return fmt.Sprintf(stmt, "", "$1", "$1", "") }
	return map[string]objectReference{
		"asset.ci_id": {
			constraint: "asset_ci_id_fkey",
			def:        "FOREIGN KEY (ci_id, organization_id) REFERENCES ci(id, organization_id)",
			app:        `UPDATE asset SET ci_id = $1 WHERE id = $2`, appConstraint: "asset_ci_id_fkey",
			fk: `UPDATE asset SET ci_id = $1 WHERE id = $2`, args: byID(assetA), foreign: ciB, own: ciA,
		},
		"asset.location_id": {
			constraint: "asset_location_id_fkey",
			def:        "FOREIGN KEY (location_id, organization_id) REFERENCES location(id, organization_id) ON DELETE RESTRICT",
			app:        `UPDATE asset SET location_id = $1 WHERE id = $2`, appConstraint: "asset_location_id_fkey",
			fk: `UPDATE asset SET location_id = $1 WHERE id = $2`, args: byID(assetA), foreign: siteB, own: siteA,
		},
		"ci.location_id": {
			constraint: "ci_location_id_fkey",
			def:        "FOREIGN KEY (location_id, organization_id) REFERENCES location(id, organization_id) ON DELETE RESTRICT",
			app:        `UPDATE ci SET location_id = $1 WHERE id = $2`, appConstraint: "ci_location_id_fkey",
			fk: `UPDATE ci SET location_id = $1 WHERE id = $2`, args: byID(ciA), foreign: siteB, own: siteA,
		},
		"ticket.related_ci_id": {
			constraint: "ticket_related_ci_id_fkey",
			def:        "FOREIGN KEY (related_ci_id, organization_id) REFERENCES ci(id, organization_id)",
			app:        fmt.Sprintf(ticket, "related_ci_id"), appConstraint: "ticket_related_ci_id_fkey",
			fk: fmt.Sprintf(ticketFK, "related_ci_id"), args: inOrg, foreign: ciB, own: ciA,
		},
		"ticket.related_asset_id": {
			constraint: "ticket_related_asset_id_fkey",
			def:        "FOREIGN KEY (related_asset_id, organization_id) REFERENCES asset(id, organization_id)",
			app:        fmt.Sprintf(ticket, "related_asset_id"), appConstraint: "ticket_related_asset_id_fkey",
			fk: fmt.Sprintf(ticketFK, "related_asset_id"), args: inOrg, foreign: assetB, own: assetA,
		},
		"reservation.asset_id": {
			constraint: "reservation_asset_id_fkey",
			def:        "FOREIGN KEY (asset_id, organization_id) REFERENCES asset(id, organization_id) ON DELETE CASCADE",
			app:        `INSERT INTO reservation (asset_id, organization_id) VALUES ($1, $2)`, appConstraint: "reservation_asset_id_fkey",
			fk: `INSERT INTO reservation (asset_id, organization_id) VALUES ($1, $2)`, args: inOrg, foreign: assetB, own: assetA,
		},
		// The disposal references refuse the deletion since 000245 (WP-751).
		"disposal_record.asset_id": {
			constraint: "disposal_record_asset_id_fkey",
			def:        "FOREIGN KEY (asset_id, organization_id) REFERENCES asset(id, organization_id) ON DELETE RESTRICT",
			app:        fmt.Sprintf(disposal, "asset_id"), appConstraint: "disposal_record_asset_id_fkey",
			fk: fmt.Sprintf(disposalFK, "asset_id"), args: inOrg, foreign: assetB, own: assetA,
		},
		"disposal_record.ci_id": {
			constraint: "disposal_record_ci_id_fkey",
			def:        "FOREIGN KEY (ci_id, organization_id) REFERENCES ci(id, organization_id) ON DELETE RESTRICT",
			app:        fmt.Sprintf(disposal, "ci_id"), appConstraint: "disposal_record_ci_id_fkey",
			fk: fmt.Sprintf(disposalFK, "ci_id"), args: inOrg, foreign: ciB, own: ciA,
		},
		"internal_order_item.asset_id": {
			constraint: "internal_order_item_asset_id_fkey",
			def:        "FOREIGN KEY (asset_id, organization_id) REFERENCES asset(id, organization_id) ON DELETE SET NULL (asset_id)",
			app:        fmt.Sprintf(orderItem, "asset_id"), appConstraint: "internal_order_item_asset_id_fkey",
			fk: fmt.Sprintf(orderItem, "asset_id"), args: inOrg, foreign: assetB, own: assetA,
		},
		"internal_order_item.consumable_id": {
			constraint: "internal_order_item_consumable_id_fkey",
			def: "FOREIGN KEY (consumable_id, organization_id) REFERENCES consumable(id, organization_id) " +
				"ON DELETE SET NULL (consumable_id)",
			app: fmt.Sprintf(orderItem, "consumable_id"), appConstraint: "internal_order_item_consumable_id_fkey",
			fk: fmt.Sprintf(orderItem, "consumable_id"), args: inOrg, foreign: consumableB, own: consumableA,
		},
		// location_maintain reports a parent of another organization under
		// the name of the foreign key and derives site_id from the parent;
		// without triggers each foreign key refuses its column alone.
		"location.parent_id": {
			constraint: "location_parent_id_fkey",
			def:        "FOREIGN KEY (parent_id, organization_id) REFERENCES location(id, organization_id) ON DELETE RESTRICT",
			app:        app(location), appConstraint: "location_parent_id_fkey",
			fk:   fmt.Sprintf(location, ", path", "$1", "'"+siteA+"'", ", 'w838'"),
			args: inOrg, foreign: siteB, own: siteA,
		},
		"location.site_id": {
			constraint: "location_site_id_fkey",
			def:        "FOREIGN KEY (site_id, organization_id) REFERENCES location(id, organization_id) ON DELETE RESTRICT",
			app:        app(location), appConstraint: "location_parent_id_fkey",
			fk:   fmt.Sprintf(location, ", path", "'"+siteA+"'", "$1", ", 'w838'"),
			args: inOrg, foreign: siteB, own: siteA,
		},
	}
}

// TestReferencesWithoutOrganizationList validates
// rls.ReferencesWithoutOrganization without a database: every entry names
// its foreign key, its target and a reason, the table is created by a
// migration, and none of the references migration 000194 made composite is
// listed.
func TestReferencesWithoutOrganizationList(t *testing.T) {
	tables := tablesCreatedUpTo(t, math.MaxInt)
	composite := []string{
		"asset.ci_id", "asset.location_id", "ci.location_id", "ticket.related_ci_id", "ticket.related_asset_id",
		"reservation.asset_id", "disposal_record.asset_id", "disposal_record.ci_id", "internal_order_item.asset_id",
		"internal_order_item.consumable_id", "location.site_id", "location.parent_id",
	}
	for _, key := range slices.Sorted(maps.Keys(rls.ReferencesWithoutOrganization)) {
		e := rls.ReferencesWithoutOrganization[key]
		table, columns, ok := strings.Cut(key, ".")
		if !ok || columns == "" {
			t.Errorf("rls.ReferencesWithoutOrganization: key %q is not table.columns", key)
		}
		if e.Constraint == "" || e.Target == "" || e.Reason == "" {
			t.Errorf("rls.ReferencesWithoutOrganization: %s: constraint, target and reason are required", key)
		}
		if !tables[table] || !tables[e.Target] {
			t.Errorf("rls.ReferencesWithoutOrganization: %s: table %q or target %q is not created by a migration",
				key, table, e.Target)
		}
		if slices.Contains(composite, key) {
			t.Errorf("rls.ReferencesWithoutOrganization: %s is composite since migration 000194", key)
		}
	}
}

// TestReferencesWithoutOrganizationDocumented requires the list of the
// references without organization in docs/schema-baseline.md to match
// rls.ReferencesWithoutOrganization. Set RETICORA_UPDATE_SCHEMA_BASELINE=1
// to regenerate it.
func TestReferencesWithoutOrganizationDocumented(t *testing.T) {
	data, err := os.ReadFile(baselineDocPath)
	if err != nil {
		t.Fatalf("read docs/schema-baseline.md: %v", err)
	}
	text := string(data)
	before, rest, ok := strings.Cut(text, refsBegin)
	if !ok {
		t.Fatalf("docs/schema-baseline.md: marker %q missing", refsBegin)
	}
	current, after, ok := strings.Cut(rest, refsEnd)
	if !ok {
		t.Fatalf("docs/schema-baseline.md: marker %q missing", refsEnd)
	}
	lines := []string{"| Verweis | Fremdschlüssel | Ziel | Grund |", "|---|---|---|---|"}
	for _, key := range slices.Sorted(maps.Keys(rls.ReferencesWithoutOrganization)) {
		e := rls.ReferencesWithoutOrganization[key]
		lines = append(lines, fmt.Sprintf("| `%s` | `%s` | `%s` | %s |", key, e.Constraint, e.Target, e.Reason))
	}
	want := "\n\n" + strings.Join(lines, "\n") + "\n\n"
	if os.Getenv(updateDocEnv) == "1" {
		text = before + refsBegin + want + refsEnd + after
		if err = os.WriteFile(baselineDocPath, []byte(text), 0o600); err != nil {
			t.Fatalf("write docs/schema-baseline.md: %v", err)
		}
		return
	}
	if current != want {
		t.Errorf("docs/schema-baseline.md: references without organization differ from "+
			"rls.ReferencesWithoutOrganization; regenerate with %s=1:\ndocumented:%s\nexpected:%s",
			updateDocEnv, current, want)
	}
}

// rest1References are the 59 references migration 000287 makes composite
// (WP-876, SR-03 rest I) by "table.column": constraint name and the
// expected pg_get_constraintdef with the former ON DELETE action (SET NULL
// as SET NULL (column)).
var rest1References = map[string]struct{ constraint, def string }{
	"agent_enrollment_token.site_id":                 {"agent_enrollment_token_site_id_fkey", "FOREIGN KEY (site_id, organization_id) REFERENCES site(id, organization_id) ON DELETE RESTRICT"},
	"api_key.rotated_from":                           {"api_key_rotated_from_fkey", "FOREIGN KEY (rotated_from, organization_id) REFERENCES api_key(id, organization_id) ON DELETE SET NULL (rotated_from)"},
	"asset_location.asset_id":                        {"asset_location_asset_id_fkey", "FOREIGN KEY (asset_id, organization_id) REFERENCES asset(id, organization_id) ON DELETE CASCADE"},
	"building.id":                                    {"building_location_fkey", "FOREIGN KEY (id, organization_id) REFERENCES location(id, organization_id) ON DELETE CASCADE"},
	"building.site_id":                               {"building_site_id_fkey", "FOREIGN KEY (site_id, organization_id) REFERENCES site(id, organization_id) ON DELETE RESTRICT"},
	"cable.source_interface_id":                      {"cable_source_interface_id_fkey", "FOREIGN KEY (source_interface_id, organization_id) REFERENCES network_interface(id, organization_id) ON DELETE SET NULL (source_interface_id)"},
	"cable.target_interface_id":                      {"cable_target_interface_id_fkey", "FOREIGN KEY (target_interface_id, organization_id) REFERENCES network_interface(id, organization_id) ON DELETE SET NULL (target_interface_id)"},
	"ci.room_id":                                     {"ci_room_id_fkey", "FOREIGN KEY (room_id, organization_id) REFERENCES room(id, organization_id)"},
	"ci.site_id":                                     {"ci_site_id_fkey", "FOREIGN KEY (site_id, organization_id) REFERENCES site(id, organization_id)"},
	"ci_change.ci_id":                                {"ci_change_ci_id_fkey", "FOREIGN KEY (ci_id, organization_id) REFERENCES ci(id, organization_id) ON DELETE CASCADE"},
	"ci_contact.ci_id":                               {"ci_contact_ci_id_fkey", "FOREIGN KEY (ci_id, organization_id) REFERENCES ci(id, organization_id) ON DELETE CASCADE"},
	"ci_contact.contact_id":                          {"ci_contact_contact_id_fkey", "FOREIGN KEY (contact_id, organization_id) REFERENCES contact(id, organization_id) ON DELETE CASCADE"},
	"ci_field_value.ci_id":                           {"ci_field_value_ci_id_fkey", "FOREIGN KEY (ci_id, organization_id) REFERENCES ci(id, organization_id) ON DELETE CASCADE"},
	"ci_instance_field_definition.ci_id":             {"ci_instance_field_definition_ci_id_fkey", "FOREIGN KEY (ci_id, organization_id) REFERENCES ci(id, organization_id) ON DELETE CASCADE"},
	"ci_merge.site_id":                               {"ci_merge_site_id_fkey", "FOREIGN KEY (site_id, organization_id) REFERENCES site(id, organization_id) ON DELETE RESTRICT"},
	"ci_relationship.source_ci_id":                   {"ci_relationship_source_ci_id_fkey", "FOREIGN KEY (source_ci_id, organization_id) REFERENCES ci(id, organization_id) ON DELETE CASCADE"},
	"ci_relationship.source_interface_id":            {"ci_relationship_source_interface_id_fkey", "FOREIGN KEY (source_interface_id, organization_id) REFERENCES network_interface(id, organization_id)"},
	"ci_relationship.target_ci_id":                   {"ci_relationship_target_ci_id_fkey", "FOREIGN KEY (target_ci_id, organization_id) REFERENCES ci(id, organization_id) ON DELETE CASCADE"},
	"ci_relationship.target_interface_id":            {"ci_relationship_target_interface_id_fkey", "FOREIGN KEY (target_interface_id, organization_id) REFERENCES network_interface(id, organization_id)"},
	"ci_relationship.verified_by":                    {"ci_relationship_verified_by_fkey", "FOREIGN KEY (verified_by, organization_id) REFERENCES app_user(id, organization_id) ON DELETE SET NULL (verified_by)"},
	"collector.site_id":                              {"collector_site_id_fkey", "FOREIGN KEY (site_id, organization_id) REFERENCES site(id, organization_id) ON DELETE RESTRICT"},
	"collector_certificate.collector_id":             {"collector_certificate_collector_id_fkey", "FOREIGN KEY (collector_id, organization_id) REFERENCES collector(id, organization_id) ON DELETE CASCADE"},
	"collector_enrollment_code.used_by_collector_id": {"collector_enrollment_code_used_by_collector_id_fkey", "FOREIGN KEY (used_by_collector_id, organization_id) REFERENCES collector(id, organization_id) ON DELETE SET NULL (used_by_collector_id)"},
	"contact.pii_subject_id":                         {"contact_pii_subject_id_fkey", "FOREIGN KEY (pii_subject_id, organization_id) REFERENCES pii_subject(surrogate_id, organization_id) ON DELETE SET NULL (pii_subject_id)"},
	"discovery_job.collector_id":                     {"discovery_job_collector_id_fkey", "FOREIGN KEY (collector_id, organization_id) REFERENCES collector(id, organization_id) ON DELETE CASCADE"},
	"discovery_result.job_id":                        {"discovery_result_job_id_fkey", "FOREIGN KEY (job_id, organization_id) REFERENCES discovery_job(id, organization_id) ON DELETE CASCADE"},
	"discovery_result.matched_ci_id":                 {"discovery_result_matched_ci_id_fkey", "FOREIGN KEY (matched_ci_id, organization_id) REFERENCES ci(id, organization_id)"},
	"discovery_scope.collector_id":                   {"discovery_scope_collector_id_fkey", "FOREIGN KEY (collector_id, organization_id) REFERENCES collector(id, organization_id) ON DELETE CASCADE"},
	"discovery_scope.site_id":                        {"discovery_scope_site_id_fkey", "FOREIGN KEY (site_id, organization_id) REFERENCES site(id, organization_id) ON DELETE RESTRICT"},
	"endpoint_agent.ci_id":                           {"endpoint_agent_ci_id_fkey", "FOREIGN KEY (ci_id, organization_id) REFERENCES ci(id, organization_id) ON DELETE SET NULL (ci_id)"},
	"ip_address.interface_id":                        {"ip_address_interface_id_fkey", "FOREIGN KEY (interface_id, organization_id) REFERENCES network_interface(id, organization_id) ON DELETE SET NULL (interface_id)"},
	"ip_address.subnet_id":                           {"ip_address_subnet_id_fkey", "FOREIGN KEY (subnet_id, organization_id) REFERENCES subnet(id, organization_id) ON DELETE SET NULL (subnet_id)"},
	"network_interface.ci_id":                        {"network_interface_ci_id_fkey", "FOREIGN KEY (ci_id, organization_id) REFERENCES ci(id, organization_id) ON DELETE CASCADE"},
	"pii_subject.parent_id":                          {"pii_subject_parent_id_fkey", "FOREIGN KEY (parent_id, organization_id) REFERENCES pii_subject(surrogate_id, organization_id) ON DELETE CASCADE"},
	"rack.id":                                        {"rack_location_fkey", "FOREIGN KEY (id, organization_id) REFERENCES location(id, organization_id) ON DELETE CASCADE"},
	"rack.room_id":                                   {"rack_room_id_fkey", "FOREIGN KEY (room_id, organization_id) REFERENCES room(id, organization_id) ON DELETE RESTRICT"},
	"rack_mount.ci_id":                               {"rack_mount_ci_id_fkey", "FOREIGN KEY (ci_id, organization_id) REFERENCES ci(id, organization_id) ON DELETE CASCADE"},
	"rack_mount.rack_id":                             {"rack_mount_rack_id_fkey", "FOREIGN KEY (rack_id, organization_id) REFERENCES rack(id, organization_id) ON DELETE RESTRICT"},
	"relationship_suppression.source_ci_id":          {"relationship_suppression_source_ci_id_fkey", "FOREIGN KEY (source_ci_id, organization_id) REFERENCES ci(id, organization_id) ON DELETE CASCADE"},
	"relationship_suppression.target_ci_id":          {"relationship_suppression_target_ci_id_fkey", "FOREIGN KEY (target_ci_id, organization_id) REFERENCES ci(id, organization_id) ON DELETE CASCADE"},
	"role_assignment.role_id":                        {"role_assignment_role_id_fkey", "FOREIGN KEY (role_id, organization_id) REFERENCES role(id, organization_id) ON DELETE CASCADE"},
	"role_assignment.scope_site_id":                  {"role_assignment_scope_site_id_fkey", "FOREIGN KEY (scope_site_id, organization_id) REFERENCES site(id, organization_id)"},
	"role_assignment.user_id":                        {"role_assignment_user_id_fkey", "FOREIGN KEY (user_id, organization_id) REFERENCES app_user(id, organization_id) ON DELETE CASCADE"},
	"role_permission.granted_by":                     {"role_permission_granted_by_fkey", "FOREIGN KEY (granted_by, organization_id) REFERENCES app_user(id, organization_id) ON DELETE SET NULL (granted_by)"},
	"role_permission.role_id":                        {"role_permission_role_id_fkey", "FOREIGN KEY (role_id, organization_id) REFERENCES role(id, organization_id) ON DELETE CASCADE"},
	"room.building_id":                               {"room_building_id_fkey", "FOREIGN KEY (building_id, organization_id) REFERENCES building(id, organization_id) ON DELETE RESTRICT"},
	"room.id":                                        {"room_location_fkey", "FOREIGN KEY (id, organization_id) REFERENCES location(id, organization_id) ON DELETE CASCADE"},
	"service_account_role.role_id":                   {"service_account_role_role_id_fkey", "FOREIGN KEY (role_id, organization_id) REFERENCES role(id, organization_id) ON DELETE CASCADE"},
	"service_account_role.scope_site_id":             {"service_account_role_scope_site_id_fkey", "FOREIGN KEY (scope_site_id, organization_id) REFERENCES site(id, organization_id)"},
	"site.id":                                        {"site_location_fkey", "FOREIGN KEY (id, organization_id) REFERENCES location(id, organization_id) ON DELETE CASCADE"},
	"subnet.site_id":                                 {"subnet_site_id_fkey", "FOREIGN KEY (site_id, organization_id) REFERENCES site(id, organization_id) ON DELETE RESTRICT"},
	"team.lead_id":                                   {"team_lead_id_fkey", "FOREIGN KEY (lead_id, organization_id) REFERENCES app_user(id, organization_id)"},
	"team_member.team_id":                            {"team_member_team_id_fkey", "FOREIGN KEY (team_id, organization_id) REFERENCES team(id, organization_id) ON DELETE CASCADE"},
	"team_member.user_id":                            {"team_member_user_id_fkey", "FOREIGN KEY (user_id, organization_id) REFERENCES app_user(id, organization_id) ON DELETE CASCADE"},
	"user_custom_role.custom_role_id":                {"user_custom_role_custom_role_id_fkey", "FOREIGN KEY (custom_role_id, organization_id) REFERENCES custom_role(id, organization_id) ON DELETE CASCADE"},
	"user_custom_role.granted_by":                    {"user_custom_role_granted_by_fkey", "FOREIGN KEY (granted_by, organization_id) REFERENCES app_user(id, organization_id)"},
	"user_custom_role.user_id":                       {"user_custom_role_user_id_fkey", "FOREIGN KEY (user_id, organization_id) REFERENCES app_user(id, organization_id) ON DELETE CASCADE"},
	"user_invitation.role_id":                        {"user_invitation_role_id_fkey", "FOREIGN KEY (role_id, organization_id) REFERENCES role(id, organization_id) ON DELETE CASCADE"},
	"vlan.site_id":                                   {"vlan_site_id_fkey", "FOREIGN KEY (site_id, organization_id) REFERENCES site(id, organization_id) ON DELETE RESTRICT"},
}

// rest1Keys are the unique keys migration 000287 adds where they were
// missing: the targets of the composite references.
var rest1Keys = map[string]string{
	"api_key": "UNIQUE (id, organization_id)", "network_interface": "UNIQUE (id, organization_id)",
	"collector": "UNIQUE (id, organization_id)", "pii_subject": "UNIQUE (surrogate_id, organization_id)",
	"discovery_job": "UNIQUE (id, organization_id)", "subnet": "UNIQUE (id, organization_id)",
	"rack": "UNIQUE (id, organization_id)", "building": "UNIQUE (id, organization_id)",
}

// rest1Probe writes one composite reference: stmt with $1 the referenced
// object and $2 the organization of the row (A), run as the application
// role (refused for the object of organization B with one of appCodes, the
// own object is accepted) and with the triggers of table off (the foreign
// key constraint alone refuses it with 23503).
type rest1Probe struct {
	table, constraint, stmt string
	// fkStmt replaces stmt without triggers where the triggers derive
	// mandatory columns of an insert (room, rack): it changes the
	// reference of the existing row of organization A.
	fkStmt       string
	foreign, own string
	// appCodes are the refusals of the application role: 23503, or 42501
	// where a trigger takes the organization from the referenced row
	// (team_member, user_custom_role) and row level security refuses it.
	appCodes []string
}

// TestTenantReferencesRest1 covers WP-876 (SR-03 rest I; TEN-09, E-137,
// E-167): the 59 references of the core, location, network, rack, role
// and discovery tables are composite with their former constraint names
// and ON DELETE actions (pg_get_constraintdef per column), none of them is
// listed in rls.ReferencesWithoutOrganization, and every target holds
// UNIQUE (<column>, organization_id). The negative test runs one write per
// target table as the application role in organization A: the object of
// organization B is refused, the own one accepted; with the triggers of the
// table off the foreign key alone refuses it (23503 on its constraint).
// Every column of a target is checked by the same composite key, which the
// catalog check proves per column.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -v -run 'TestTenantReferencesRest1' ./internal/tenant/rls/
func TestTenantReferencesRest1(t *testing.T) {
	f := scopetest.Seed(t, "cc")
	ctx := context.Background()
	requireMigrated(ctx, t, f.Admin)

	for _, column := range slices.Sorted(maps.Keys(rest1References)) {
		want := rest1References[column]
		var def string
		if err := f.Admin.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = $1`,
			want.constraint).Scan(&def); err != nil {
			t.Errorf("%s: constraint %s: %v", column, want.constraint, err)
			continue
		}
		if def != want.def {
			t.Errorf("%s: %s is %q, want %q", column, want.constraint, def, want.def)
		}
		if _, listed := rls.ReferencesWithoutOrganization[column]; listed {
			t.Errorf("%s is composite since migration 000287 and must not be listed", column)
		}
	}
	if n := len(rest1References); n != 59 {
		t.Errorf("rest1References has %d columns, want the 59 of WP-876", n)
	}
	for _, table := range slices.Sorted(maps.Keys(rest1Keys)) {
		var unique bool
		if err := f.Admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_constraint
			WHERE conrelid = $1::regclass AND contype IN ('u', 'p') AND pg_get_constraintdef(oid) = $2)`,
			table, rest1Keys[table]).Scan(&unique); err != nil {
			t.Fatal(err)
		}
		if !unique {
			t.Errorf("%s has no %s", table, rest1Keys[table])
		}
	}

	probes := seedRest1(t, f)
	scope := database.OrgWideScope(f.OrgA, f.User)
	for _, name := range slices.Sorted(maps.Keys(probes)) {
		p := probes[name]
		t.Run("app/"+name, func(t *testing.T) {
			err := appWrite(ctx, f, &scope, p.stmt, []any{p.foreign, f.OrgA})
			var pgErr *pgconn.PgError
			codes := p.appCodes
			if codes == nil {
				codes = []string{"23503"}
			}
			if !errors.As(err, &pgErr) || !slices.Contains(codes, pgErr.Code) {
				t.Errorf("write with an object of another organization: got %v, want one of %v", err, codes)
			}
			if ownErr := appWrite(ctx, f, &scope, p.stmt, []any{p.own, f.OrgA}); ownErr != nil {
				t.Errorf("write with the own object: %v", ownErr)
			}
		})
		t.Run("fk/"+name, func(t *testing.T) {
			err := pgx.BeginFunc(ctx, f.Admin, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `ALTER TABLE `+p.table+` DISABLE TRIGGER USER`); err != nil {
					return err
				}
				stmt := p.stmt
				if p.fkStmt != "" {
					stmt = p.fkStmt
				}
				_, err := tx.Exec(ctx, stmt, p.foreign, f.OrgA)
				if err == nil {
					return errRollback
				}
				return err
			})
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23503" || pgErr.ConstraintName != p.constraint {
				t.Errorf("write with an object of another organization without triggers: got %v, want 23503 on %s", err, p.constraint)
			}
		})
	}
}

// seedRest1 seeds the referenced objects in both organizations and returns
// one write per target table of migration 000287.
func seedRest1(t *testing.T, f *scopetest.Fixture) map[string]rest1Probe {
	t.Helper()
	ctx := context.Background()
	one := func(stmt string, args ...any) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(ctx, stmt, args...).Scan(&id); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
		return id
	}
	clientB := one(`INSERT INTO client (organization_id, name, slug) VALUES ($1, 'w876 B', 'scopetest-cc-w876-b') RETURNING id::text`, f.OrgB)
	clients := [2]string{f.Client1, clientB}
	type objects struct {
		site, building, room, rack, ci, iface, contact, collector, job, subnet, role, user, team, customRole, apiKey string
	}
	var o [2]objects
	for i, org := range []string{f.OrgA, f.OrgB} {
		x := &o[i]
		x.site = one(`INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, 'w876') RETURNING id::text`, org, clients[i])
		x.building = one(`INSERT INTO building (organization_id, site_id, name) VALUES ($1, $2, 'w876') RETURNING id::text`, org, x.site)
		x.room = one(`INSERT INTO room (organization_id, building_id, name) VALUES ($1, $2, 'w876') RETURNING id::text`, org, x.building)
		x.rack = one(`INSERT INTO rack (organization_id, room_id, name) VALUES ($1, $2, 'w876') RETURNING id::text`, org, x.room)
		x.ci = f.CI(t, org, "", "w876")
		x.iface = one(`INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, 'w876') RETURNING id::text`, org, x.ci)
		x.contact = one(`INSERT INTO contact (organization_id) VALUES ($1) RETURNING id::text`, org)
		x.collector = one(`INSERT INTO collector (organization_id, name) VALUES ($1, 'w876') RETURNING id::text`, org)
		x.job = one(`INSERT INTO discovery_job (organization_id, collector_id, job_type) VALUES ($1, $2, 'sweep') RETURNING id::text`, org, x.collector)
		x.subnet = one(`INSERT INTO subnet (organization_id, cidr) VALUES ($1, '10.87.6.0/24') RETURNING id::text`, org)
		x.role = one(`SELECT id::text FROM role WHERE organization_id = $1 AND name = 'org_admin'`, org)
		x.user = f.AppUser(t, org, "w876-"+[2]string{"a", "b"}[i])
		x.team = one(`INSERT INTO team (organization_id, name) VALUES ($1, 'w876') RETURNING id::text`, org)
		x.customRole = one(`INSERT INTO custom_role (organization_id, name, permissions) VALUES ($1, 'w876', '[]') RETURNING id::text`, org)
		x.apiKey = one(`INSERT INTO api_key (organization_id, name, key_hash, key_prefix, created_by)
			VALUES ($1, 'w876', md5(random()::text), 'w876', $2) RETURNING id::text`, org, x.user)
	}
	a, b := o[0], o[1]
	user2 := f.AppUser(t, f.OrgA, "w876-2")
	return map[string]rest1Probe{
		"site": {table: "vlan", constraint: "vlan_site_id_fkey", foreign: b.site, own: a.site,
			stmt: `INSERT INTO vlan (organization_id, vlan_id, name, site_id) VALUES ($2, 876, 'w876', $1)`},
		"building": {table: "room", constraint: "room_building_id_fkey", foreign: b.building, own: a.building,
			stmt:   `INSERT INTO room (organization_id, building_id, name) VALUES ($2, $1, 'w876-r')`,
			fkStmt: `UPDATE room SET building_id = $1, name = 'w876-fk' WHERE id = '` + a.room + `' AND organization_id = $2`},
		"room": {table: "rack", constraint: "rack_room_id_fkey", foreign: b.room, own: a.room,
			stmt:   `INSERT INTO rack (organization_id, room_id, name) VALUES ($2, $1, 'w876-k')`,
			fkStmt: `UPDATE rack SET room_id = $1, name = 'w876-fk' WHERE id = '` + a.rack + `' AND organization_id = $2`},
		"rack": {table: "rack_mount", constraint: "rack_mount_rack_id_fkey", foreign: b.rack, own: a.rack,
			stmt: `INSERT INTO rack_mount (organization_id, rack_id, ci_id, position_u) VALUES ($2, $1, '` + a.ci + `', 1)`},
		"ci": {table: "ci_field_value", constraint: "ci_field_value_ci_id_fkey", foreign: b.ci, own: a.ci,
			stmt: `INSERT INTO ci_field_value (organization_id, ci_id, field_name) VALUES ($2, $1, 'w876')`},
		"contact": {table: "ci_contact", constraint: "ci_contact_contact_id_fkey", foreign: b.contact, own: a.contact,
			stmt: `INSERT INTO ci_contact (organization_id, ci_id, contact_id) VALUES ($2, '` + a.ci + `', $1)`},
		"network_interface": {table: "cable", constraint: "cable_source_interface_id_fkey", foreign: b.iface, own: a.iface,
			stmt: `INSERT INTO cable (organization_id, source_interface_id) VALUES ($2, $1)`},
		"collector": {table: "collector_certificate", constraint: "collector_certificate_collector_id_fkey", foreign: b.collector, own: a.collector,
			stmt: `INSERT INTO collector_certificate (organization_id, collector_id, serial, fingerprint, not_before, not_after)
				VALUES ($2, $1, md5(random()::text), md5(random()::text), now(), now() + interval '1 day')`},
		"discovery_job": {table: "discovery_result", constraint: "discovery_result_job_id_fkey", foreign: b.job, own: a.job,
			stmt: `INSERT INTO discovery_result (organization_id, job_id, raw_data) VALUES ($2, $1, '{}')`},
		"subnet": {table: "ip_address", constraint: "ip_address_subnet_id_fkey", foreign: b.subnet, own: a.subnet,
			stmt: `INSERT INTO ip_address (organization_id, address, subnet_id, vrf_id)
				VALUES ($2, '10.87.6.10', $1, (SELECT vrf_id FROM subnet WHERE id = '` + a.subnet + `'))`},
		"role": {table: "role_assignment", constraint: "role_assignment_role_id_fkey", foreign: b.role, own: a.role,
			stmt: `INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($2, '` + user2 + `', $1)`},
		"app_user": {table: "role_assignment", constraint: "role_assignment_user_id_fkey", foreign: b.user, own: user2,
			stmt: `INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($2, $1, '` + a.role + `')`},
		"team": {table: "team_member", constraint: "team_member_team_id_fkey", foreign: b.team, own: a.team,
			stmt:     `INSERT INTO team_member (team_id, user_id, organization_id) VALUES ($1, '` + user2 + `', $2)`,
			appCodes: []string{"23503", "42501"}},
		"custom_role": {table: "user_custom_role", constraint: "user_custom_role_custom_role_id_fkey", foreign: b.customRole, own: a.customRole,
			stmt:     `INSERT INTO user_custom_role (user_id, custom_role_id, organization_id) VALUES ('` + user2 + `', $1, $2)`,
			appCodes: []string{"23503", "42501"}},
		"api_key": {table: "api_key", constraint: "api_key_rotated_from_fkey", foreign: b.apiKey, own: a.apiKey,
			stmt: `INSERT INTO api_key (organization_id, name, key_hash, key_prefix, created_by, rotated_from)
				VALUES ($2, 'w876-r', md5(random()::text), 'w876', '` + user2 + `', $1)`},
	}
}
