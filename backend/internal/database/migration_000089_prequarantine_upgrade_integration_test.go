package database_test

// WP-649 (DB-02; F-378, E-100): decision E-26 changed migration 000056 to
// create migration_quarantine. Databases that applied the earlier 000056
// have no such table, and 000089 is the first migration that writes to it:
// their upgrade stopped there with relation "migration_quarantine" does not
// exist and left version 89 dirty. This test puts a database of its own into
// that state (migrated to 56, table dropped, one stored contains relationship
// that 000089 has to quarantine), migrates it to the newest version and
// expects a clean upgrade with the quarantined row and a migration_quarantine
// that is defined exactly as on a fresh installation: columns, indexes, RLS
// ENABLE and FORCE, policy, owner and grants. The whole schema must match the
// fresh one. The shared database of TEST_DATABASE_URL is not changed; both
// scratch databases are dropped.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=… go test -count=1 -v -run 'TestUpgradeFromPreQuarantine56' ./internal/database/

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestUpgradeFromPreQuarantine56(t *testing.T) {
	dsn := testDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	files := loadMigrations(t)
	latest := len(files)
	version56 := 0
	for _, f := range files {
		if f.name == "rls_enforcement" {
			version56 = f.version
		}
	}
	if version56 != 56 {
		t.Fatalf("migration rls_enforcement is %06d, want 000056", version56)
	}

	fresh := scratchDatabase(ctx, t, dsn, "fresh649")
	(&migrator{t: t, conn: fresh, files: files}).upTo(ctx, latest)

	conn := scratchDatabase(ctx, t, dsn, "prequar649")
	m := &migrator{t: t, conn: conn, files: files}
	m.upTo(ctx, version56)

	// The state before E-26: 000056 applied without migration_quarantine.
	const (
		org    = "c0de0649-0000-4000-8000-00000000000a"
		other  = "c0de0649-0000-4000-8000-00000000000b"
		parent = "c0de0649-0000-4000-8000-0000000001a1"
		child  = "c0de0649-0000-4000-8000-0000000001a2"
	)
	execAll(ctx, t, conn,
		`DROP TABLE migration_quarantine`,
		`INSERT INTO organization (id, name, slug) VALUES ('`+org+`', 'prequar-a', 'prequar-a'), ('`+other+`', 'prequar-b', 'prequar-b')`,
		`INSERT INTO ci (id, organization_id, ci_type_id, name)
		 SELECT v.id, '`+org+`', t.id, v.name
		   FROM ci_type t, (VALUES ('`+parent+`'::uuid, 'chassis'), ('`+child+`'::uuid, 'blade')) v (id, name)
		  WHERE t.key = 'switch' AND t.organization_id IS NULL`,
		`INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type)
		 VALUES ('`+org+`', '`+parent+`', '`+child+`', 'contains')`,
	)

	// migrator.exec stops the test at the first failing migration and names
	// it (on the old 000089: relation "migration_quarantine" does not exist).
	m.upTo(ctx, latest)
	var (
		version int
		dirty   bool
	)
	if err := conn.QueryRow(ctx, `SELECT version, dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	if version != latest || dirty {
		t.Fatalf("after the upgrade: version %d dirty %v, want %d not dirty", version, dirty, latest)
	}

	// 000089 quarantined the stored contains row and set the parent.
	var quarantined int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM migration_quarantine
		WHERE organization_id = $1 AND migration = '000089' AND source_table = 'ci_relationship'
		  AND row_data->>'rel_type' = 'contains'`, org).Scan(&quarantined); err != nil {
		t.Fatalf("read migration_quarantine: %v", err)
	}
	if quarantined != 1 {
		t.Errorf("quarantined contains rows of 000089: %d, want 1", quarantined)
	}
	var gotParent *string
	if err := conn.QueryRow(ctx, `SELECT parent_ci_id::text FROM ci WHERE id = $1`, child).Scan(&gotParent); err != nil {
		t.Fatalf("read parent_ci_id: %v", err)
	}
	if gotParent == nil || *gotParent != parent {
		t.Errorf("parent_ci_id of the blade: %v, want %s", gotParent, parent)
	}

	// Same definition as on a fresh installation.
	wantDump, gotDump := schemaDump(ctx, t, fresh), schemaDump(ctx, t, conn)
	wantTable, gotTable := quarantineLines(wantDump), quarantineLines(gotDump)
	if len(wantTable) == 0 {
		t.Fatal("fresh installation has no migration_quarantine")
	}
	for _, line := range []string{
		"relation migration_quarantine kind=r rls=true force=true",
		"policy migration_quarantine.migration_quarantine_isolation PERMISSIVE ALL to public using " +
			"((organization_id = (current_setting('app.org_id'::text))::uuid)) " +
			"check ((organization_id = (current_setting('app.org_id'::text))::uuid))",
		"grant migration_quarantine SELECT to reticora_app",
		"grant migration_quarantine INSERT to reticora_app",
		"grant migration_quarantine UPDATE to reticora_app",
		"grant migration_quarantine DELETE to reticora_app",
		"grant migration_quarantine_id_seq USAGE to reticora_app",
		"grant migration_quarantine_id_seq SELECT to reticora_app",
	} {
		if !containsLine(wantTable, line) {
			t.Errorf("fresh installation lacks %q", line)
		}
	}
	if diff := diffLines(wantTable, gotTable); diff != "" {
		t.Errorf("migration_quarantine after the upgrade differs from a fresh installation (- fresh, + upgraded):\n%s", diff)
	}
	if diff := diffLines(wantDump, gotDump); diff != "" {
		t.Errorf("schema after the upgrade differs from a fresh installation (- fresh, + upgraded):\n%s", diff)
	}
	if want, got := quarantineACL(ctx, t, fresh), quarantineACL(ctx, t, conn); diffLines(want, got) != "" {
		t.Errorf("owner and privileges of migration_quarantine: %v, want %v as on a fresh installation", got, want)
	}

	// Tenant isolation as the application role: each organization sees and
	// writes only its own entries.
	asApp := func(orgID string, fn func(tx pgx.Tx) error) error {
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err = tx.Exec(ctx, `SET LOCAL ROLE reticora_app`); err != nil {
			t.Fatalf("set role: %v", err)
		}
		if _, err = tx.Exec(ctx, `SELECT set_config('app.org_id', $1, true)`, orgID); err != nil {
			t.Fatalf("set app.org_id: %v", err)
		}
		return fn(tx)
	}
	for _, c := range []struct {
		org  string
		want int
	}{{org, 1}, {other, 0}} {
		var n int
		if err := asApp(c.org, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM migration_quarantine`).Scan(&n)
		}); err != nil {
			t.Fatalf("read as reticora_app for %s: %v", c.org, err)
		}
		if n != c.want {
			t.Errorf("entries visible to %s as reticora_app: %d, want %d", c.org, n, c.want)
		}
	}
	insert := `INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
		VALUES ($1, 'ci', '{}', 'probe', '000000')`
	if err := asApp(org, func(tx pgx.Tx) error { _, err := tx.Exec(ctx, insert, org); return err }); err != nil {
		t.Errorf("insert of an own entry as reticora_app: %v", err)
	}
	err := asApp(org, func(tx pgx.Tx) error { _, err := tx.Exec(ctx, insert, other); return err })
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Errorf("insert of another organization's entry as reticora_app: %v, want an RLS violation (42501)", err)
	}
}

// quarantineLines returns the schema dump lines of migration_quarantine, its
// sequence, indexes, constraints, policy, trigger and grants.
func quarantineLines(dump []string) []string {
	var out []string
	for _, line := range dump {
		if strings.Contains(line, "migration_quarantine") {
			out = append(out, line)
		}
	}
	return out
}

func containsLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

// quarantineACL returns owner and access privileges (with grantor) of
// migration_quarantine and its sequence.
func quarantineACL(ctx context.Context, t *testing.T, conn *pgx.Conn) []string {
	t.Helper()
	rows, err := conn.Query(ctx, `SELECT c.relname || ' owner=' || pg_get_userbyid(c.relowner) ||
		       ' acl=' || COALESCE(array_to_string(ARRAY(SELECT x::text FROM unnest(c.relacl) x ORDER BY 1), ','), '')
		  FROM pg_class c
		 WHERE c.relnamespace = 'public'::regnamespace
		   AND c.relname IN ('migration_quarantine', 'migration_quarantine_id_seq')
		 ORDER BY c.relname`)
	if err != nil {
		t.Fatalf("read privileges of migration_quarantine: %v", err)
	}
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("read privileges of migration_quarantine: %v", err)
	}
	return lines
}
