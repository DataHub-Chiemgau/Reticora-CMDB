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
