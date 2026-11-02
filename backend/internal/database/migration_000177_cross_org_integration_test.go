package database_test

// WP-639 (TEN-09, TEN-06, MGT-03; F-366): migration 000177 gives the foreign
// keys of the modules desk, key and training the organization. Rows that
// already reference another organization must stop the migration with their
// list instead of being changed or deleted silently. This test migrates a
// database of its own to the version before 000177, seeds such rows next to a
// valid one, applies 000177 and expects the abort with the list; after the
// rows are corrected by hand the migration applies and a new cross-org
// reference fails. The shared database of TEST_DATABASE_URL is not changed;
// the scratch database is dropped.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=… go test -count=1 -v -run 'TestMigration000177ReportsCrossOrgRows' ./internal/database/

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestMigration000177ReportsCrossOrgRows(t *testing.T) {
	dsn := testDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	files := loadMigrations(t)
	version := 0
	for _, f := range files {
		if f.name == "module_org_references" {
			version = f.version
		}
	}
	if version == 0 {
		t.Fatal("migration module_org_references not found")
	}
	conn := scratchDatabase(ctx, t, dsn, "orgref177")
	m := &migrator{t: t, conn: conn, files: files}
	m.upTo(ctx, version-1)

	const (
		orgA      = "c0de0177-0000-4000-8000-00000000000a"
		orgB      = "c0de0177-0000-4000-8000-00000000000b"
		userA     = "c0de0177-0000-4000-8000-0000000001a0"
		userB     = "c0de0177-0000-4000-8000-0000000001b0"
		keyA      = "c0de0177-0000-4000-8000-0000000002a0"
		trainingB = "c0de0177-0000-4000-8000-0000000003b0"
		deskA     = "c0de0177-0000-4000-8000-0000000004a0"
		handover  = "c0de0177-0000-4000-8000-0000000005a1" // key of A to the user of B
		valid     = "c0de0177-0000-4000-8000-0000000005a2" // key of A to the user of A
		assigned  = "c0de0177-0000-4000-8000-0000000006a1" // A's assignment to B's course
		booking   = "c0de0177-0000-4000-8000-0000000007a1" // A's desk booked for the user of B
	)
	execAll(ctx, t, conn,
		`INSERT INTO organization (id, name, slug) VALUES ('`+orgA+`', 'orgref-a', 'orgref-a'), ('`+orgB+`', 'orgref-b', 'orgref-b')`,
		`INSERT INTO app_user (id, organization_id, oidc_subject, email, display_name) VALUES
			('`+userA+`', '`+orgA+`', 'orgref-a', 'orgref-a@example.invalid', 'A'),
			('`+userB+`', '`+orgB+`', 'orgref-b', 'orgref-b@example.invalid', 'B')`,
		`INSERT INTO key_item (id, organization_id, name) VALUES ('`+keyA+`', '`+orgA+`', 'key A')`,
		`INSERT INTO training (id, organization_id, title) VALUES ('`+trainingB+`', '`+orgB+`', 'course B')`,
		`INSERT INTO desk (id, organization_id, name) VALUES ('`+deskA+`', '`+orgA+`', 'desk A')`,
		`INSERT INTO key_assignment (id, organization_id, key_item_id, assigned_to) VALUES
			('`+handover+`', '`+orgA+`', '`+keyA+`', '`+userB+`'),
			('`+valid+`', '`+orgA+`', '`+keyA+`', '`+userA+`')`,
		`INSERT INTO training_assignment (id, organization_id, training_id, user_id) VALUES ('`+assigned+`', '`+orgA+`', '`+trainingB+`', '`+userA+`')`,
		`INSERT INTO desk_booking (id, organization_id, desk_id, user_id, starts_at, ends_at) VALUES
			('`+booking+`', '`+orgA+`', '`+deskA+`', '`+userB+`', now(), now() + interval '1 hour')`,
	)

	// The migration stops and lists every offending row, not the valid one.
	m.setVersion(ctx, version, true)
	up, err := os.ReadFile(files[version-1].up)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(ctx, string(up))
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "P0001" {
		t.Fatalf("migration %06d with cross-org rows: %v, want raise_exception P0001", version, err)
	}
	for _, want := range []string{
		"key_assignment " + handover + " assigned_to",
		"training_assignment " + assigned + " training_id",
		"desk_booking " + booking + " user_id",
	} {
		if !strings.Contains(pgErr.Message, want) {
			t.Errorf("abort message %q does not list %q", pgErr.Message, want)
		}
	}
	if strings.Contains(pgErr.Message, valid) {
		t.Errorf("abort message %q lists the valid handover %s", pgErr.Message, valid)
	}
	m.setVersion(ctx, version-1, false)

	// Nothing was changed or deleted: the rows are there, the foreign keys
	// still name the referenced row only.
	var n int
	if err = conn.QueryRow(ctx, `SELECT (SELECT count(*) FROM key_assignment) + (SELECT count(*) FROM training_assignment) + (SELECT count(*) FROM desk_booking)`).Scan(&n); err != nil || n != 4 {
		t.Errorf("module rows after the abort: %d err=%v, want all 4", n, err)
	}
	fkey := func() string {
		t.Helper()
		var def string
		if scanErr := conn.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'key_assignment_assigned_to_fkey'`).Scan(&def); scanErr != nil {
			t.Fatalf("key_assignment_assigned_to_fkey: %v", scanErr)
		}
		return def
	}
	if def := fkey(); def != "FOREIGN KEY (assigned_to) REFERENCES app_user(id)" {
		t.Errorf("key_assignment_assigned_to_fkey after the abort: %s, want unchanged", def)
	}

	// Corrected by hand, the migration applies; a new cross-org reference
	// fails like an unknown id.
	execAll(ctx, t, conn,
		`DELETE FROM key_assignment WHERE id = '`+handover+`'`,
		`DELETE FROM training_assignment WHERE id = '`+assigned+`'`,
		`DELETE FROM desk_booking WHERE id = '`+booking+`'`,
	)
	m.up(ctx)
	if def := fkey(); def != "FOREIGN KEY (assigned_to, organization_id) REFERENCES app_user(id, organization_id)" {
		t.Errorf("key_assignment_assigned_to_fkey after the migration: %s", def)
	}
	_, err = conn.Exec(ctx, `INSERT INTO key_assignment (organization_id, key_item_id, assigned_to) VALUES ('`+orgA+`', '`+keyA+`', '`+userB+`')`)
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Errorf("handover to the user of organization B after the migration: %v, want foreign_key_violation 23503", err)
	}
}
