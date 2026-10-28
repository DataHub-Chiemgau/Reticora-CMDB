package database_test

// WP-560 (MGT-01, TEN-06; F-187, E-72 option 1): migration 000066 clears
// document storage keys outside the organization prefix. It reported them
// only as a count (NOTICE), so operators could not tell which documents lost
// their content. This test runs 65 → 66 in a database of its own with keys of
// every kind and expects one migration_quarantine row per cleared key (source
// table document, migration 000066, row_data with id, organization_id and
// the old key), visible to the document's organization only; conforming keys
// stay and are not reported. The down migration removes exactly the 000066
// report. A database that applied 000056 before E-26 has no
// migration_quarantine: 000066 creates it (E-100), and its down runs without
// the table. The shared database of TEST_DATABASE_URL is not changed; the
// scratch databases are dropped.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=… go test -count=1 -v -run 'TestMigration000066' ./internal/database/

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	rp066OrgA = "c0de0560-0000-4000-8000-00000000000a"
	rp066OrgB = "c0de0560-0000-4000-8000-00000000000b"
	rp066User = "c0de0560-0000-4000-8000-0000000000c1"

	rp066Foreign   = "c0de0560-0000-4000-8000-000000000d01" // key of organization B
	rp066Traversal = "c0de0560-0000-4000-8000-000000000d02" // own prefix with ..
	rp066OtherDoc  = "c0de0560-0000-4000-8000-000000000d03" // legacy key of another document
	rp066Free      = "c0de0560-0000-4000-8000-000000000d04" // freely chosen key
	rp066Legacy    = "c0de0560-0000-4000-8000-000000000d05" // legacy key of this document
	rp066Org       = "c0de0560-0000-4000-8000-000000000d06" // server key below org/<org>/
	rp066Empty     = "c0de0560-0000-4000-8000-000000000d07" // no content
	rp066BForeign  = "c0de0560-0000-4000-8000-000000000d08" // organization B, key of A
)

// rp066Keys are the storage keys before 000066, by document.
var rp066Keys = map[string]string{
	rp066Foreign:   "org/" + rp066OrgB + "/documents/" + rp066BForeign + "/1",
	rp066Traversal: "org/" + rp066OrgA + "/../" + rp066OrgB + "/documents/x/1",
	rp066OtherDoc:  "documents/" + rp066OrgA + "/" + rp066Legacy + "/1",
	rp066Free:      "k/handbuch.pdf",
	rp066Legacy:    "documents/" + rp066OrgA + "/" + rp066Legacy + "/1",
	rp066Org:       "org/" + rp066OrgA + "/documents/" + rp066Org + "/1",
	rp066Empty:     "",
	rp066BForeign:  "org/" + rp066OrgA + "/documents/" + rp066Org + "/1",
}

// rp066Cleared are the documents whose key 000066 clears and reports.
var rp066Cleared = []string{rp066Foreign, rp066Traversal, rp066OtherDoc, rp066Free, rp066BForeign}

func rp066OrgOf(doc string) string {
	if doc == rp066BForeign {
		return rp066OrgB
	}
	return rp066OrgA
}

// seedDocuments066 inserts the organizations and documents at version 65.
func seedDocuments066(ctx context.Context, t *testing.T, conn *pgx.Conn) {
	t.Helper()
	execAll(ctx, t, conn,
		`INSERT INTO organization (id, name, slug) VALUES ('`+rp066OrgA+`', 'wp560 a', 'wp560-a'), ('`+rp066OrgB+`', 'wp560 b', 'wp560-b')`,
		`INSERT INTO app_user (id, organization_id, email, display_name) VALUES ('`+rp066User+`', '`+rp066OrgA+`', 'wp560@example.com', 'wp560')`,
	)
	for doc, key := range rp066Keys {
		if _, err := conn.Exec(ctx, `INSERT INTO document (id, organization_id, title, file_name, storage_key, uploaded_by)
			VALUES ($1, $2, 'wp560 document', 'handbuch.pdf', $3, $4)`, doc, rp066OrgOf(doc), key, rp066User); err != nil {
			t.Fatalf("seed document %s: %v", doc, err)
		}
	}
}

// report066 returns the 000066 report as "<column organization_id> <row_data>"
// lines, sorted.
func report066(ctx context.Context, t *testing.T, conn *pgx.Conn) []string {
	t.Helper()
	rows, err := conn.Query(ctx, `SELECT organization_id::text || ' ' || source_table || ' id=' || (row_data->>'id')
		       || ' organization_id=' || (row_data->>'organization_id') || ' storage_key=' || (row_data->>'storage_key')
		       || ' keys=' || (SELECT string_agg(k, ',' ORDER BY k) FROM jsonb_object_keys(row_data) k)
		       || ' reason=' || (reason <> '')::text
		  FROM migration_quarantine WHERE migration = '000066' ORDER BY 1`)
	if err != nil {
		t.Fatalf("read the 000066 report: %v", err)
	}
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("read the 000066 report: %v", err)
	}
	return lines
}

func want066Report() []string {
	var want []string
	for _, doc := range rp066Cleared {
		org := rp066OrgOf(doc)
		want = append(want, org+" document id="+doc+" organization_id="+org+" storage_key="+rp066Keys[doc]+
			" keys=id,organization_id,storage_key reason=true")
	}
	slices.Sort(want)
	return want
}

// storageKeys066 returns the current key of every seeded document.
func storageKeys066(ctx context.Context, t *testing.T, conn *pgx.Conn) map[string]string {
	t.Helper()
	rows, err := conn.Query(ctx, `SELECT id::text, storage_key FROM document WHERE organization_id IN ($1, $2)`, rp066OrgA, rp066OrgB)
	if err != nil {
		t.Fatalf("read storage keys: %v", err)
	}
	defer rows.Close()
	keys := map[string]string{}
	for rows.Next() {
		var id, key string
		if err := rows.Scan(&id, &key); err != nil {
			t.Fatalf("read storage keys: %v", err)
		}
		keys[id] = key
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read storage keys: %v", err)
	}
	return keys
}

func checkKeysAfter066(ctx context.Context, t *testing.T, conn *pgx.Conn) {
	t.Helper()
	got := storageKeys066(ctx, t, conn)
	for doc, before := range rp066Keys {
		want := before
		if slices.Contains(rp066Cleared, doc) {
			want = ""
		}
		if got[doc] != want {
			t.Errorf("storage key of %s after 000066: %q, want %q", doc, got[doc], want)
		}
	}
}

// visibleReport066 counts the 000066 report rows the application role sees
// for orgID.
func visibleReport066(ctx context.Context, t *testing.T, conn *pgx.Conn, orgID string) int {
	t.Helper()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	execTx := func(sql string, args ...any) {
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	execTx(`SET LOCAL ROLE reticora_app`)
	execTx(`SELECT set_config('app.org_id', $1, true)`, orgID)
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM migration_quarantine WHERE migration = '000066'`).Scan(&n); err != nil {
		t.Fatalf("read the report as reticora_app for %s: %v", orgID, err)
	}
	return n
}

func TestMigration000066ReportsClearedStorageKeys(t *testing.T) {
	dsn := testDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	conn := scratchDatabase(ctx, t, dsn, "report066")
	m := &migrator{t: t, conn: conn, files: loadMigrations(t)}
	m.upTo(ctx, 65)
	seedDocuments066(ctx, t, conn)
	// An entry of another migration, which the down of 000066 keeps.
	execAll(ctx, t, conn, `INSERT INTO migration_quarantine (organization_id, source_table, row_data, reason, migration)
		VALUES ('`+rp066OrgA+`', 'ci_relationship', '{}', 'wp560 probe', '000056')`)

	m.up(ctx)
	if diff := diffLines(want066Report(), report066(ctx, t, conn)); diff != "" {
		t.Errorf("report of 000066 (- want, + got):\n%s", diff)
	}
	checkKeysAfter066(ctx, t, conn)
	for org, want := range map[string]int{rp066OrgA: 4, rp066OrgB: 1} {
		if got := visibleReport066(ctx, t, conn, org); got != want {
			t.Errorf("report rows visible to %s as reticora_app: %d, want %d", org, got, want)
		}
	}

	m.down(ctx)
	if got := report066(ctx, t, conn); len(got) != 0 {
		t.Errorf("report of 000066 after its down: %v, want none", got)
	}
	var kept int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM migration_quarantine WHERE migration = '000056' AND reason = 'wp560 probe'`).Scan(&kept); err != nil || kept != 1 {
		t.Errorf("entry of 000056 after the down of 000066: %d (%v), want 1", kept, err)
	}
	// The down does not restore keys that may point at another tenant.
	checkKeysAfter066(ctx, t, conn)
}

func TestMigration000066WithoutMigrationQuarantine(t *testing.T) {
	dsn := testDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	conn := scratchDatabase(ctx, t, dsn, "report066_prequar")
	m := &migrator{t: t, conn: conn, files: loadMigrations(t)}
	m.upTo(ctx, 65)
	// The state of a database that applied 000056 before E-26.
	execAll(ctx, t, conn, `DROP TABLE migration_quarantine`)
	seedDocuments066(ctx, t, conn)

	// migrator.exec stops the test if 000066 fails.
	m.up(ctx)
	if diff := diffLines(want066Report(), report066(ctx, t, conn)); diff != "" {
		t.Errorf("report of 000066 (- want, + got):\n%s", diff)
	}
	checkKeysAfter066(ctx, t, conn)
	var rls, force bool
	if err := conn.QueryRow(ctx, `SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = 'public.migration_quarantine'::regclass`).Scan(&rls, &force); err != nil {
		t.Fatalf("read migration_quarantine: %v", err)
	}
	if !rls || !force {
		t.Errorf("migration_quarantine created by 000066: RLS %v, FORCE %v, want both", rls, force)
	}
	for org, want := range map[string]int{rp066OrgA: 4, rp066OrgB: 1} {
		if got := visibleReport066(ctx, t, conn, org); got != want {
			t.Errorf("report rows visible to %s as reticora_app: %d, want %d", org, got, want)
		}
	}

	// A database that applied 000066 before WP-560 has no table at version
	// 66 either: its down must run without it.
	execAll(ctx, t, conn, `DROP TABLE migration_quarantine`)
	m.down(ctx)
	var constraints int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conname = 'document_storage_key_org_prefix'`).Scan(&constraints); err != nil || constraints != 0 {
		t.Errorf("document_storage_key_org_prefix after the down of 000066: %d (%v), want dropped", constraints, err)
	}
	if v := m.version(ctx); v != 65 {
		t.Errorf("version after the down of 000066: %d, want 65", v)
	}
}
