package database_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// The roundtrip test drives the migrations itself, with the semantics of
// golang-migrate (one simple-protocol Exec per file, bookkeeping in
// schema_migrations), so its database stays usable for the migrate CLI. It
// works in databases of its own on the server of TEST_DATABASE_URL and drops
// them at the end; the database of TEST_DATABASE_URL is not changed (F-193).

const migrationsDir = "../../migrations"

// schemaBaselinePath is docs/schema-baseline.md seen from this package.
const schemaBaselinePath = "../../../docs/schema-baseline.md"

// updateBaselineEnv regenerates docs/schema-baseline.md from the migrated
// schema instead of comparing against it.
const updateBaselineEnv = "RETICORA_UPDATE_SCHEMA_BASELINE"

type migrationFiles struct {
	version  int
	name     string
	up, down string
}

func loadMigrations(t *testing.T) []migrationFiles {
	t.Helper()
	ups, err := filepath.Glob(filepath.Join(migrationsDir, "*.up.sql"))
	if err != nil || len(ups) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	var out []migrationFiles
	for _, up := range ups {
		base := strings.TrimSuffix(filepath.Base(up), ".up.sql")
		num, name, _ := strings.Cut(base, "_")
		v, err := strconv.Atoi(num)
		if err != nil {
			t.Fatalf("migration %s: invalid version: %v", up, err)
		}
		out = append(out, migrationFiles{version: v, name: name, up: up, down: strings.TrimSuffix(up, ".up.sql") + ".down.sql"})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i, m := range out {
		if m.version != i+1 {
			t.Fatalf("migration versions must be consecutive from 1: found %06d at position %d", m.version, i+1)
		}
	}
	return out
}

type migrator struct {
	t     *testing.T
	conn  *pgx.Conn
	files []migrationFiles
}

func (m *migrator) version(ctx context.Context) int {
	m.t.Helper()
	if _, err := m.conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version bigint NOT NULL PRIMARY KEY, dirty boolean NOT NULL)`); err != nil {
		m.t.Fatalf("ensure schema_migrations: %v", err)
	}
	var (
		v     int64
		dirty bool
	)
	err := m.conn.QueryRow(ctx, `SELECT version, dirty FROM schema_migrations LIMIT 1`).Scan(&v, &dirty)
	if err == pgx.ErrNoRows {
		return 0
	}
	if err != nil {
		m.t.Fatalf("read schema_migrations: %v", err)
	}
	if dirty {
		m.t.Fatalf("database is dirty at version %d; fix it manually before running the roundtrip", v)
	}
	return int(v)
}

func (m *migrator) setVersion(ctx context.Context, v int, dirty bool) {
	m.t.Helper()
	if _, err := m.conn.Exec(ctx, `TRUNCATE schema_migrations`); err != nil {
		m.t.Fatalf("reset schema_migrations: %v", err)
	}
	if v == 0 && !dirty {
		return
	}
	if _, err := m.conn.Exec(ctx, `INSERT INTO schema_migrations (version, dirty) VALUES ($1, $2)`, v, dirty); err != nil {
		m.t.Fatalf("write schema_migrations: %v", err)
	}
}

func (m *migrator) exec(ctx context.Context, path string, target int) {
	m.t.Helper()
	sqlText, err := os.ReadFile(path)
	if err != nil {
		m.t.Fatalf("read %s: %v", path, err)
	}
	m.setVersion(ctx, target, true)
	// Without arguments pgx uses the simple protocol, so the whole file runs
	// as one implicit transaction like with golang-migrate.
	if _, err := m.conn.Exec(ctx, string(sqlText)); err != nil {
		m.t.Fatalf("apply %s: %v (database left dirty at version %d)", filepath.Base(path), err, target)
	}
	m.setVersion(ctx, target, false)
}

func (m *migrator) up(ctx context.Context) {
	v := m.version(ctx)
	m.exec(ctx, m.files[v].up, v+1)
}

func (m *migrator) down(ctx context.Context) {
	v := m.version(ctx)
	m.exec(ctx, m.files[v-1].down, v-1)
}

func (m *migrator) upTo(ctx context.Context, target int) {
	for m.version(ctx) < target {
		m.up(ctx)
	}
}

// TestMigrationsRoundtrip verifies DB-02: up → schema A → down to 0 → up →
// schema B with A = B, and every down migration restores the schema of the
// previous version exactly. Each migration is checked in isolation on the way
// up: schema before up N must equal the schema after up N + down N, and
// re-applying N must give the schema after the first up N again. The last
// iteration is the down 1/up 1 check of the newest migration. Migrations
// whose down or up also writes rows have a data check (migrationDataChecks)
// around their down and the following up.
//
// The roundtrip runs in a fresh database of its own, so the shared test
// database stays untouched. A second database of the same cluster, migrated
// like another installation (staging next to production), holds grants to
// the cluster-wide role reticora_app throughout (F-193, F-287): down to 0 and
// up must pass without a dirty version, the rows the migrations seed must be
// complete again, the members of reticora_app must stay, and the second
// database must be unchanged and still work with SET ROLE reticora_app.
func TestMigrationsRoundtrip(t *testing.T) {
	dsn := testDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	files := loadMigrations(t)
	latest := len(files)

	peer := scratchDatabase(ctx, t, dsn, "peer")
	(&migrator{t: t, conn: peer, files: files}).upTo(ctx, latest)
	peerSchema := schemaDump(ctx, t, peer)
	members := appRoleMembers(ctx, t, peer)

	conn := scratchDatabase(ctx, t, dsn, "roundtrip")
	m := &migrator{t: t, conn: conn, files: files}
	m.upTo(ctx, latest)
	seedA := seedRows(ctx, t, conn)
	schemaA := schemaDump(ctx, t, conn)

	for m.version(ctx) > 0 {
		m.down(ctx)
	}
	if leftovers := objectsOutsideExtensions(schemaDump(ctx, t, conn)); len(leftovers) > 0 {
		t.Errorf("objects left after migrating down to version 0:\n  %s", strings.Join(leftovers, "\n  "))
	}
	// 000056 down keeps reticora_app only for other databases: nothing in
	// this one (privileges, default privileges) may still depend on it.
	if deps := appRoleDependencies(ctx, t, conn); len(deps) > 0 {
		t.Errorf("objects of this database still depend on %s at version 0:\n  %s", database.DefaultAppRole, strings.Join(deps, "\n  "))
	}

	for _, f := range files {
		before := schemaDump(ctx, t, conn)
		m.up(ctx)
		afterUp := schemaDump(ctx, t, conn)
		check, hasCheck := migrationDataChecks[f.name]
		if hasCheck {
			check.seed(ctx, t, conn)
		}
		m.down(ctx)
		if hasCheck {
			check.afterDown(ctx, t, conn)
		}
		diff, unused := withoutKnownDeviations(f.version, diffLines(before, schemaDump(ctx, t, conn)))
		if diff != "" {
			t.Errorf("down %06d_%s does not restore the schema of version %06d (- before up, + after down):\n%s",
				f.version, f.name, f.version-1, diff)
		}
		if len(unused) > 0 {
			t.Errorf("known down deviation of %06d_%s no longer occurs, remove it from knownDownDeviations: %s",
				f.version, f.name, strings.Join(unused, ", "))
		}
		m.up(ctx)
		if hasCheck {
			check.afterUp(ctx, t, conn)
		}
		if diff := diffLines(afterUp, schemaDump(ctx, t, conn)); diff != "" {
			t.Errorf("re-applying %06d_%s after its down gives a different schema (- first up, + second up):\n%s",
				f.version, f.name, diff)
		}
	}

	if diff := diffLines(schemaA, schemaDump(ctx, t, conn)); diff != "" {
		t.Errorf("schema after down to 0 and up differs from the initial schema (- A, + B):\n%s", diff)
	}
	if v := m.version(ctx); v != latest {
		t.Errorf("version after down to 0 and up: %d, want %d", v, latest)
	}
	seedB := seedRows(ctx, t, conn)
	if diff := diffLines(seedA, seedB); diff != "" {
		t.Errorf("rows after down to 0 and up differ from the freshly migrated database (- before, + after):\n%s", diff)
	}
	t.Logf("rows after down to 0 and up: %s", seedSummary(seedB))

	if diff := diffLines(members, appRoleMembers(ctx, t, peer)); diff != "" {
		t.Errorf("members of %s changed by the roundtrip (- before, + after):\n%s", database.DefaultAppRole, diff)
		restoreAppRoleMembers(ctx, t, peer, members)
	}
	if diff := diffLines(peerSchema, schemaDump(ctx, t, peer)); diff != "" {
		t.Errorf("roundtrip in another database changed the second database (- before, + after):\n%s", diff)
	}
	peerUsableAsAppRole(ctx, t, peer)
}

// scratchDatabase creates an empty database on the server of dsn and returns
// a connection to it as the user of dsn. The database is dropped when the
// test ends.
func scratchDatabase(ctx context.Context, t *testing.T, dsn, purpose string) *pgx.Conn {
	t.Helper()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = admin.Close(context.Background()) }()
	name := fmt.Sprintf("reticora_wp427_%s_%d", purpose, time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+name+" TEMPLATE template0"); err != nil {
		t.Fatalf("create database %s: %v", name, err)
	}
	t.Cleanup(func() {
		drop, cerr := pgx.Connect(context.Background(), dsn)
		if cerr != nil {
			t.Errorf("drop database %s: %v", name, cerr)
			return
		}
		defer func() { _ = drop.Close(context.Background()) }()
		if _, cerr = drop.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); cerr != nil {
			t.Errorf("drop database %s: %v", name, cerr)
		}
	})
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	cfg.Database = name
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect %s: %v", name, err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// seedRows describes the rows a migrated database holds before any test
// writes: the row count of every table, the permission catalog, the global CI
// types and the organizations (F-193: a failed rollback had removed most of
// them).
func seedRows(ctx context.Context, t *testing.T, conn *pgx.Conn) []string {
	t.Helper()
	var counts string
	if err := conn.QueryRow(ctx, `
		SELECT string_agg(format('SELECT %L || count(*) FROM %I', 'rows ' || c.relname || ' ', c.relname), ' UNION ALL ')
		  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') AND c.relname <> 'schema_migrations'`).Scan(&counts); err != nil {
		t.Fatalf("list tables: %v", err)
	}
	var lines []string
	for _, q := range []string{
		counts,
		`SELECT 'permission ' || key FROM permission`,
		`SELECT 'global ci_type ' || key FROM ci_type WHERE organization_id IS NULL`,
		`SELECT 'organization ' || id || ' ' || slug FROM organization`,
	} {
		rows, err := conn.Query(ctx, q)
		if err != nil {
			t.Fatalf("seed rows: %v\n%s", err, q)
		}
		got, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatalf("seed rows: %v\n%s", err, q)
		}
		lines = append(lines, got...)
	}
	sort.Strings(lines)
	return lines
}

// seedSummary counts the permissions and global CI types of seedRows and
// names the organizations.
func seedSummary(lines []string) string {
	var permissions, ciTypes int
	var orgs []string
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "permission "):
			permissions++
		case strings.HasPrefix(l, "global ci_type "):
			ciTypes++
		case strings.HasPrefix(l, "organization "):
			orgs = append(orgs, strings.TrimPrefix(l, "organization "))
		}
	}
	return fmt.Sprintf("%d permissions, %d global CI types, organizations %v", permissions, ciTypes, orgs)
}

// appRoleMembers lists the members of reticora_app, a role of the whole
// cluster, as the GRANT statements that restore them.
func appRoleMembers(ctx context.Context, t *testing.T, conn *pgx.Conn) []string {
	t.Helper()
	rows, err := conn.Query(ctx, `
		SELECT format('GRANT %I TO %I WITH ADMIN %s, INHERIT %s, SET %s',
		              r.rolname, pg_get_userbyid(m.member), m.admin_option::text, m.inherit_option::text, m.set_option::text)
		  FROM pg_auth_members m JOIN pg_roles r ON r.oid = m.roleid
		 WHERE r.rolname = $1 ORDER BY 1`, database.DefaultAppRole)
	if err != nil {
		t.Fatalf("list members of %s: %v", database.DefaultAppRole, err)
	}
	grants, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("list members of %s: %v", database.DefaultAppRole, err)
	}
	return grants
}

// appRoleDependencies lists the objects of the current database that depend
// on reticora_app (pg_shdepend: privileges, default privileges, ownership).
func appRoleDependencies(ctx context.Context, t *testing.T, conn *pgx.Conn) []string {
	t.Helper()
	rows, err := conn.Query(ctx, `
		SELECT d.deptype::text || ' ' || pg_describe_object(d.classid, d.objid, d.objsubid)
		  FROM pg_shdepend d
		  JOIN pg_database db ON db.oid = d.dbid AND db.datname = current_database()
		  JOIN pg_roles r ON r.oid = d.refobjid AND d.refclassid = 'pg_authid'::regclass
		 WHERE r.rolname = $1 ORDER BY 1`, database.DefaultAppRole)
	if err != nil {
		t.Fatalf("list dependencies on %s: %v", database.DefaultAppRole, err)
	}
	deps, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("list dependencies on %s: %v", database.DefaultAppRole, err)
	}
	return deps
}

// restoreAppRoleMembers grants reticora_app again to its former members, so
// a failed roundtrip does not take the runtime login of the later test
// packages (TEST_APP_DATABASE_URL, WP-349) with it.
func restoreAppRoleMembers(ctx context.Context, t *testing.T, conn *pgx.Conn, grants []string) {
	t.Helper()
	for _, grant := range grants {
		if _, err := conn.Exec(ctx, grant); err != nil {
			t.Errorf("restore members of %s: %s: %v", database.DefaultAppRole, grant, err)
		}
	}
}

// peerUsableAsAppRole checks that the second database still works for the
// application role (F-287): SET ROLE reticora_app succeeds and reads a
// tenant table under its policy and a global catalog table.
func peerUsableAsAppRole(ctx context.Context, t *testing.T, peer *pgx.Conn) {
	t.Helper()
	var org, slug string
	var permissions int
	if err := peer.QueryRow(ctx, `SELECT id::text, slug, (SELECT count(*) FROM permission) FROM organization ORDER BY id LIMIT 1`).
		Scan(&org, &slug, &permissions); err != nil {
		t.Fatalf("second database: read organization: %v", err)
	}
	tx, err := peer.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE `+database.DefaultAppRole); err != nil {
		t.Fatalf("second database: SET ROLE %s: %v", database.DefaultAppRole, err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.org_id', $1, true)`, org); err != nil {
		t.Fatalf("second database: set app.org_id: %v", err)
	}
	var gotSlug string
	var gotPermissions int
	if err := tx.QueryRow(ctx, `SELECT slug, (SELECT count(*) FROM permission) FROM organization`).Scan(&gotSlug, &gotPermissions); err != nil {
		t.Fatalf("second database: SELECT as %s: %v", database.DefaultAppRole, err)
	}
	if gotSlug != slug || gotPermissions != permissions {
		t.Errorf("second database as %s: organization %q, %d permissions; want %q, %d",
			database.DefaultAppRole, gotSlug, gotPermissions, slug, permissions)
	}
}

// knownDownDeviations lists, per migration version, the schema objects whose
// down migration knowingly does not restore the previous state. Every entry is
// the prefix of a schema dump line ("relation asset ", "policy t.p "); diff
// lines of these objects are tolerated in the down check of that migration.
// An entry that no longer matches fails the test so the list only shrinks.
// The final up → down to 0 → up comparison is not affected by this list.
var knownDownDeviations = map[int][]string{
	// 000056 down keeps FORCE ROW LEVEL SECURITY on purpose (see the comment in
	// the migration): reverting it would weaken tenant isolation.
	56: {"relation alert_rule ", "relation webhook_dead_letter "},
}

// dataCheck seeds rows after the up of a migration (seed) and checks what
// its down (afterDown) and the up that follows (afterUp) do with them; the
// schema dumps do not see rows. afterDown removes the objects the schema
// comparison would see, afterUp removes the rows.
type dataCheck struct {
	seed, afterDown, afterUp func(ctx context.Context, t *testing.T, conn *pgx.Conn)
}

// migrationDataChecks are the data checks by migration name.
var migrationDataChecks = map[string]dataCheck{
	"attr_text_leakproof": {seedAttrText, attrTextDownQueuesJobs, attrTextUpQueuesRebuilds},
	"attr_index_per_key":  {seedAttrIndexPerKey, attrIndexPerKeyDownQueuesJobs, attrIndexPerKeyUpQueuesKeyJobs},
	"ci_change_aud03":     {seedCIChangeAUD03, ciChangeAUD03Down, ciChangeAUD03Up},
	// The backfill of 000171 (WP-509) writes rows; its down keeps them.
	"notification_default_channels": {seedDefaultChannels, defaultChannelsDown, defaultChannelsUp},
}

// The fixture of attr_text_leakproof (000162, WP-435): an organization with
// a searchable type definition (index over reticora_attr_text), a unique
// global definition (index still over ->>, with a queued rebuild job), a
// definition without index and a searchable system definition.
const (
	rt162Org      = "c0de0162-0000-4000-8000-000000000001"
	rt162Type     = "c0de0162-0000-4000-8000-000000000002"
	rt162Indexed  = "c0de0162-0000-4000-8000-000000000011"
	rt162Unique   = "c0de0162-0000-4000-8000-000000000012"
	rt162Plain    = "c0de0162-0000-4000-8000-000000000013"
	rt162System   = "c0de0162-0000-4000-8000-000000000014"
	rt162IndexA   = "idx_attr_c0de0162000040008000000000000011"
	rt162IndexB   = "idx_attr_c0de0162000040008000000000000012_u"
	rt162Function = "public.reticora_attr_text(jsonb, text)"
)

func execAll(ctx context.Context, t *testing.T, conn *pgx.Conn, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := conn.Exec(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func seedAttrText(ctx context.Context, t *testing.T, conn *pgx.Conn) {
	t.Helper()
	execAll(ctx, t, conn,
		`INSERT INTO organization (id, name, slug) VALUES ('`+rt162Org+`', 'roundtrip 000162', 'roundtrip-000162')`,
		`INSERT INTO ci_type (id, organization_id, key, name) VALUES ('`+rt162Type+`', '`+rt162Org+`', 'rt162-type', 'rt162-type')`,
		`INSERT INTO attribute_definition (id, organization_id, ci_type_id, scope, key, data_type, searchable, is_unique, unique_scope) VALUES
			('`+rt162Indexed+`', '`+rt162Org+`', '`+rt162Type+`', 'type', 'rt162_a', 'text', true, false, 'org'),
			('`+rt162Unique+`', '`+rt162Org+`', NULL, 'global', 'rt162_b', 'text', false, true, 'client'),
			('`+rt162Plain+`', '`+rt162Org+`', '`+rt162Type+`', 'type', 'rt162_c', 'text', false, false, 'org'),
			('`+rt162System+`', NULL, NULL, 'global', 'rt162_d', 'text', true, false, 'org')`,
		`CREATE INDEX `+rt162IndexA+` ON ci (organization_id, reticora_attr_text(attributes, 'rt162_a'))
			WHERE deleted_at IS NULL AND reticora_attr_text(attributes, 'rt162_a') IS NOT NULL
			  AND organization_id = '`+rt162Org+`'::uuid AND ci_type_id = '`+rt162Type+`'::uuid`,
		`CREATE UNIQUE INDEX `+rt162IndexB+` ON ci (organization_id, client_id, (attributes ->> 'rt162_b')) NULLS NOT DISTINCT
			WHERE deleted_at IS NULL AND (attributes ->> 'rt162_b') IS NOT NULL AND organization_id = '`+rt162Org+`'::uuid`,
		`INSERT INTO job (organization_id, kind, params, max_attempts, timeout_seconds)
			VALUES ('`+rt162Org+`', 'attribute.index.rebuild', '{"definition_id": "`+rt162Unique+`"}', 3, 3600)`)
}

// queuedJobs lists the definition ids of the jobs of kind in status of the
// fixture organization, with their attempts and timeout.
func queuedJobs(ctx context.Context, t *testing.T, conn *pgx.Conn, kind, status string) []string {
	t.Helper()
	return orgJobs(ctx, t, conn, rt162Org, kind, status)
}

// orgJobs is queuedJobs for the organization org.
func orgJobs(ctx context.Context, t *testing.T, conn *pgx.Conn, org, kind, status string) []string {
	t.Helper()
	rows, err := conn.Query(ctx, `
		SELECT (params ->> 'definition_id') || ' ' || max_attempts || ' ' || timeout_seconds || ' ' || (finished_at IS NOT NULL)
		  FROM job WHERE organization_id = $1 AND kind = $2 AND status = $3 ORDER BY 1`, org, kind, status)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	jobs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	return jobs
}

// attrTextDownQueuesJobs: the down drops the function with the indexes
// built over it, keeps the indexes over ->>, cancels the queued rebuild jobs
// and queues one attribute.index job per searchable or unique definition
// of an organization, which the DDL job of the previous version builds
// over ->> again.
func attrTextDownQueuesJobs(ctx context.Context, t *testing.T, conn *pgx.Conn) {
	t.Helper()
	var function, indexA, indexB *string
	if err := conn.QueryRow(ctx, `SELECT to_regprocedure($1)::text, to_regclass($2)::text, to_regclass($3)::text`,
		rt162Function, rt162IndexA, rt162IndexB).Scan(&function, &indexA, &indexB); err != nil {
		t.Fatal(err)
	}
	if function != nil || indexA != nil || indexB == nil {
		t.Errorf("down 000162: function %v, index over the function %v, index over ->> %v; want the function and its index gone, the other index kept",
			function, indexA, indexB)
	}
	if got, want := queuedJobs(ctx, t, conn, "attribute.index", "queued"),
		[]string{rt162Indexed + " 3 3600 false", rt162Unique + " 3 3600 false"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("down 000162 queued attribute.index jobs %v, want %v (searchable or unique definitions of an organization)", got, want)
	}
	if got := queuedJobs(ctx, t, conn, "attribute.index.rebuild", "cancelled"); strings.Join(got, ",") != rt162Unique+" 3 3600 true" {
		t.Errorf("down 000162: cancelled rebuild jobs %v, want the queued one", got)
	}
	execAll(ctx, t, conn, `DROP INDEX `+rt162IndexB, `DELETE FROM job WHERE organization_id = '`+rt162Org+`'`)
}

// attrTextUpQueuesRebuilds: the up creates the leakproof function and
// queues one attribute.index.rebuild job per searchable or unique
// definition of an organization.
func attrTextUpQueuesRebuilds(ctx context.Context, t *testing.T, conn *pgx.Conn) {
	t.Helper()
	var leakproof bool
	if err := conn.QueryRow(ctx, `SELECT proleakproof FROM pg_proc WHERE oid = to_regprocedure($1)`, rt162Function).Scan(&leakproof); err != nil || !leakproof {
		t.Errorf("up 000162: reticora_attr_text leakproof %v, %v", leakproof, err)
	}
	if got, want := queuedJobs(ctx, t, conn, "attribute.index.rebuild", "queued"),
		[]string{rt162Indexed + " 3 3600 false", rt162Unique + " 3 3600 false"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("up 000162 queued rebuild jobs %v, want %v (searchable or unique definitions of an organization)", got, want)
	}
	execAll(ctx, t, conn,
		`DELETE FROM job WHERE organization_id = '`+rt162Org+`'`,
		`DELETE FROM attribute_definition WHERE id IN ('`+rt162Indexed+`', '`+rt162Unique+`', '`+rt162Plain+`', '`+rt162System+`')`,
		`DELETE FROM ci_type WHERE id = '`+rt162Type+`'`,
		`DELETE FROM organization WHERE id = '`+rt162Org+`'`)
}

// The fixture of attr_index_per_key (000164, WP-607): an organization with
// a searchable global definition the index of its key serves (index of the
// key built, its attribute.index.key job queued), a unique global and a
// searchable type definition of the same key with their own indexes, a
// definition without index and a searchable system definition; besides the
// index of the key one with a hashed name.
const (
	rt164Org      = "c0de0164-0000-4000-8000-000000000001"
	rt164Type     = "c0de0164-0000-4000-8000-000000000002"
	rt164Shared   = "c0de0164-0000-4000-8000-000000000011"
	rt164Unique   = "c0de0164-0000-4000-8000-000000000012"
	rt164Typed    = "c0de0164-0000-4000-8000-000000000013"
	rt164Plain    = "c0de0164-0000-4000-8000-000000000014"
	rt164System   = "c0de0164-0000-4000-8000-000000000015"
	rt164KeyIndex = "idx_attr_key_rt164_a"
	rt164Hashed   = "idx_attr_keyh_0123456789abcdef0123456789abcdef"
	rt164IndexU   = "idx_attr_c0de0164000040008000000000000012_u"
	rt164IndexT   = "idx_attr_c0de0164000040008000000000000013"
)

func seedAttrIndexPerKey(ctx context.Context, t *testing.T, conn *pgx.Conn) {
	t.Helper()
	execAll(ctx, t, conn,
		`INSERT INTO organization (id, name, slug) VALUES ('`+rt164Org+`', 'roundtrip 000164', 'roundtrip-000164')`,
		`INSERT INTO ci_type (id, organization_id, key, name) VALUES ('`+rt164Type+`', '`+rt164Org+`', 'rt164-type', 'rt164-type')`,
		`INSERT INTO attribute_definition (id, organization_id, ci_type_id, scope, key, data_type, searchable, is_unique, unique_scope) VALUES
			('`+rt164Shared+`', '`+rt164Org+`', NULL, 'global', 'rt164_a', 'text', true, false, 'org'),
			('`+rt164Unique+`', '`+rt164Org+`', NULL, 'global', 'rt164_b', 'text', true, true, 'org'),
			('`+rt164Typed+`', '`+rt164Org+`', '`+rt164Type+`', 'type', 'rt164_a', 'text', true, false, 'org'),
			('`+rt164Plain+`', '`+rt164Org+`', NULL, 'global', 'rt164_c', 'text', false, false, 'org'),
			('`+rt164System+`', NULL, NULL, 'global', 'rt164_d', 'text', true, false, 'org')`,
		`CREATE INDEX `+rt164KeyIndex+` ON ci (organization_id, reticora_attr_text(attributes, 'rt164_a'))
			WHERE deleted_at IS NULL AND reticora_attr_text(attributes, 'rt164_a') IS NOT NULL`,
		`CREATE INDEX `+rt164Hashed+` ON ci (organization_id, reticora_attr_text(attributes, 'RT164 x'))
			WHERE deleted_at IS NULL AND reticora_attr_text(attributes, 'RT164 x') IS NOT NULL`,
		`CREATE UNIQUE INDEX `+rt164IndexU+` ON ci (organization_id, reticora_attr_text(attributes, 'rt164_b'))
			WHERE deleted_at IS NULL AND reticora_attr_text(attributes, 'rt164_b') IS NOT NULL AND organization_id = '`+rt164Org+`'::uuid`,
		`CREATE INDEX `+rt164IndexT+` ON ci (organization_id, reticora_attr_text(attributes, 'rt164_a'))
			WHERE deleted_at IS NULL AND reticora_attr_text(attributes, 'rt164_a') IS NOT NULL
			  AND organization_id = '`+rt164Org+`'::uuid AND ci_type_id = '`+rt164Type+`'::uuid`,
		`INSERT INTO job (organization_id, kind, params, max_attempts, timeout_seconds)
			VALUES ('`+rt164Org+`', 'attribute.index.key', '{"definition_id": "`+rt164Shared+`"}', 3, 3600)`)
}

// attrIndexPerKeyDownQueuesJobs: the down drops the indexes of keys (the
// previous version does not know them), keeps the definitions' own
// indexes, cancels the queued attribute.index.key jobs and queues one
// attribute.index job per definition an index of its key served, which the
// DDL job of the previous version builds as the definition's own index.
func attrIndexPerKeyDownQueuesJobs(ctx context.Context, t *testing.T, conn *pgx.Conn) {
	t.Helper()
	var keyIndex, hashed, unique, typed bool
	if err := conn.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL, to_regclass($2) IS NOT NULL, to_regclass($3) IS NOT NULL, to_regclass($4) IS NOT NULL`,
		rt164KeyIndex, rt164Hashed, rt164IndexU, rt164IndexT).Scan(&keyIndex, &hashed, &unique, &typed); err != nil {
		t.Fatal(err)
	}
	if keyIndex || hashed || !unique || !typed {
		t.Errorf("down 000164: indexes of keys present %v %v, own indexes present %v %v; want the indexes of keys gone, the own ones kept",
			keyIndex, hashed, unique, typed)
	}
	if got, want := orgJobs(ctx, t, conn, rt164Org, "attribute.index", "queued"), rt164Shared+" 3 3600 false"; strings.Join(got, ",") != want {
		t.Errorf("down 000164 queued attribute.index jobs %v, want %s (definitions an index of their key served)", got, want)
	}
	if got := orgJobs(ctx, t, conn, rt164Org, "attribute.index.key", "cancelled"); strings.Join(got, ",") != rt164Shared+" 3 3600 true" {
		t.Errorf("down 000164: cancelled key jobs %v, want the queued one", got)
	}
	execAll(ctx, t, conn, `DROP INDEX `+rt164IndexU+`, `+rt164IndexT, `DELETE FROM job WHERE organization_id = '`+rt164Org+`'`)
}

// attrIndexPerKeyUpQueuesKeyJobs: the up queues one attribute.index.key job
// per searchable definition of an organization that is not unique and has
// no CI type, none for the others.
func attrIndexPerKeyUpQueuesKeyJobs(ctx context.Context, t *testing.T, conn *pgx.Conn) {
	t.Helper()
	if got, want := orgJobs(ctx, t, conn, rt164Org, "attribute.index.key", "queued"), rt164Shared+" 3 3600 false"; strings.Join(got, ",") != want {
		t.Errorf("up 000164 queued key jobs %v, want %s (searchable, not unique definitions without CI type of an organization)", got, want)
	}
	execAll(ctx, t, conn,
		`DELETE FROM job WHERE organization_id = '`+rt164Org+`'`,
		`DELETE FROM attribute_definition WHERE id IN ('`+rt164Shared+`', '`+rt164Unique+`', '`+rt164Typed+`', '`+rt164Plain+`', '`+rt164System+`')`,
		`DELETE FROM ci_type WHERE id = '`+rt164Type+`'`,
		`DELETE FROM organization WHERE id = '`+rt164Org+`'`)
}

// The fixture of ci_change_aud03 (000167, WP-487): ci_change rows of the
// legacy writers, one per case of the change_kind mapping; two rows of one
// write (same CI and created_at) are a status change and a name. The rows
// carry a client the scope trigger would clear: the backfill keeps it.
const (
	rt167Org    = "c0de0167-0000-4000-8000-000000000001"
	rt167Type   = "c0de0167-0000-4000-8000-000000000002"
	rt167CI     = "c0de0167-0000-4000-8000-000000000003"
	rt167Client = "c0de0167-0000-4000-8000-000000000004"
)

// rt167Rows are the fixture rows: id suffix, change_type, field, old and
// new value, created_at offset in seconds and the change_kind the backfill
// gives them.
var rt167Rows = []struct{ id, changeType, field, oldValue, newValue, at, kind string }{
	{"11", "create", "", "null", `{"name": "rt167"}`, "1", "created"},
	{"12", "type_changed", "ci_type_id", `"a"`, `"b"`, "2", "type_changed"},
	{"13", "status_change", "status", `"active"`, `"maintenance"`, "3", "status_changed"},
	{"14", "status_change", "status", `"maintenance"`, `"active"`, "4", "updated"},
	{"15", "update", "name", `"rt167"`, `"rt167b"`, "4", "updated"},
	{"16", "update", "deleted_at", `"deleted"`, "null", "5", "restored"},
	{"17", "reconciliation_decision", "deleted_at", `"deleted"`, "null", "6", "restored"},
	{"18", "reconciliation_decision", "merged_ci", "null", `{"victim_id": "v"}`, "7", "merged"},
	{"19", "reconciliation_decision", "unmerged_ci", "null", `{"merge_id": "m"}`, "8", "restored"},
	{"1a", "delete", "merged_into_id", `{}`, `{"merged_into_id": "s"}`, "9", "merged"},
	{"1b", "delete", "", `{}`, `{}`, "10", "deleted"},
	{"1c", "relationship_change", "relationships", `{"id": "r"}`, "", "11", "relationship_removed"},
	{"1d", "relationship_change", "relationships", "", `{"id": "r"}`, "12", "relationship_added"},
	{"1e", "movement", "rack_mount", "null", `{"rack_id": "r"}`, "13", "updated"},
	{"1f", "override", "model", `"a"`, `"b"`, "14", "updated"},
}

func seedCIChangeAUD03(ctx context.Context, t *testing.T, conn *pgx.Conn) {
	t.Helper()
	execAll(ctx, t, conn,
		`INSERT INTO organization (id, name, slug) VALUES ('`+rt167Org+`', 'roundtrip 000167', 'roundtrip-000167')`,
		`INSERT INTO ci_type (id, organization_id, key, name) VALUES ('`+rt167Type+`', '`+rt167Org+`', 'rt167-type', 'rt167-type')`,
		`INSERT INTO ci (id, organization_id, ci_type_id, name, status) VALUES ('`+rt167CI+`', '`+rt167Org+`', '`+rt167Type+`', 'rt167', 'active')`)
	for _, r := range rt167Rows {
		execAll(ctx, t, conn, `INSERT INTO ci_change (id, organization_id, ci_id, change_type, field_name, old_value, new_value, created_at)
			VALUES ('c0de0167-0000-4000-8000-0000000000`+r.id+`', '`+rt167Org+`', '`+rt167CI+`', '`+r.changeType+`',
				NULLIF('`+r.field+`', ''), NULLIF('`+r.oldValue+`', '')::jsonb, NULLIF('`+r.newValue+`', '')::jsonb,
				'2026-01-01T00:00:00Z'::timestamptz + interval '`+r.at+` seconds')`)
	}
	execAll(ctx, t, conn,
		`ALTER TABLE ci_change DISABLE TRIGGER ci_change_scope`,
		`UPDATE ci_change SET client_id = '`+rt167Client+`' WHERE ci_id = '`+rt167CI+`'`,
		`ALTER TABLE ci_change ENABLE TRIGGER ci_change_scope`)
}

// ciChangeAUD03Rows lists id suffix, change_type and client of the fixture
// rows, with change_kind when withKind is set.
func ciChangeAUD03Rows(ctx context.Context, t *testing.T, conn *pgx.Conn, withKind bool) []string {
	t.Helper()
	kind := `''`
	if withKind {
		kind = `change_kind`
	}
	rows, err := conn.Query(ctx, `SELECT right(id::text, 2) || ' ' || change_type || ' ' || `+kind+` || ' ' || COALESCE(client_id::text, '')
		FROM ci_change WHERE ci_id = $1 ORDER BY id`, rt167CI)
	if err != nil {
		t.Fatalf("list ci_change: %v", err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("list ci_change: %v", err)
	}
	return got
}

// ciChangeAUD03Down: the down renames type_changed back to type_change and
// keeps every row and its client.
func ciChangeAUD03Down(ctx context.Context, t *testing.T, conn *pgx.Conn) {
	t.Helper()
	var want []string
	for _, r := range rt167Rows {
		changeType := r.changeType
		if changeType == "type_changed" {
			changeType = "type_change"
		}
		want = append(want, r.id+" "+changeType+"  "+rt167Client)
	}
	if got := ciChangeAUD03Rows(ctx, t, conn, false); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("down 000167 ci_change rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// ciChangeAUD03Up: the up renames type_change to type_changed and gives
// each row the change_kind of its change (backfill); the clients stay.
func ciChangeAUD03Up(ctx context.Context, t *testing.T, conn *pgx.Conn) {
	t.Helper()
	var want []string
	for _, r := range rt167Rows {
		want = append(want, r.id+" "+r.changeType+" "+r.kind+" "+rt167Client)
	}
	if got := ciChangeAUD03Rows(ctx, t, conn, true); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("up 000167 ci_change rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	execAll(ctx, t, conn,
		`DELETE FROM ci_change WHERE ci_id = '`+rt167CI+`'`,
		`DELETE FROM ci WHERE id = '`+rt167CI+`'`,
		`DELETE FROM ci_type WHERE id = '`+rt167Type+`'`,
		`DELETE FROM organization WHERE id = '`+rt167Org+`'`)
}

// The fixture of notification_default_channels (000171, WP-509, F-157):
// organizations as they exist before the backfill. rt171Bare has no
// channel; rt171Disabled a disabled in-app channel of its own name;
// rt171Named a webhook channel named "In-App"; rt171Both also an e-mail
// channel named "In-App (Standard)".
const (
	rt171Bare     = "c0de0171-0000-4000-8000-000000000001"
	rt171Disabled = "c0de0171-0000-4000-8000-000000000002"
	rt171Named    = "c0de0171-0000-4000-8000-000000000003"
	rt171Both     = "c0de0171-0000-4000-8000-000000000004"
	// The organization of migration 000054.
	rt171Default = "00000000-0000-0000-0000-000000000001"
)

func seedDefaultChannels(ctx context.Context, t *testing.T, conn *pgx.Conn) {
	t.Helper()
	for i, org := range []string{rt171Bare, rt171Disabled, rt171Named, rt171Both} {
		execAll(ctx, t, conn, fmt.Sprintf(`INSERT INTO organization (id, name, slug) VALUES ('%s', 'roundtrip 000171 %d', 'roundtrip-000171-%d')`,
			org, i, i))
	}
	execAll(ctx, t, conn,
		`INSERT INTO notification_channel (organization_id, name, kind, provider, enabled)
			VALUES ('`+rt171Disabled+`', 'Mitteilungen', 'inapp', 'inapp', false),
			       ('`+rt171Named+`', 'In-App', 'webhook', 'webhook', true),
			       ('`+rt171Both+`', 'In-App', 'webhook', 'webhook', true),
			       ('`+rt171Both+`', 'In-App (Standard)', 'email', 'central_smtp', true)`)
}

// defaultChannelsRows lists the channels of the fixture organizations and
// of the default organization as org kind/provider/name/enabled; a unique
// name suffix is cut to "…".
func defaultChannelsRows(ctx context.Context, t *testing.T, conn *pgx.Conn) []string {
	t.Helper()
	rows, err := conn.Query(ctx, `SELECT CASE WHEN organization_id = $2 THEN 'default' ELSE right(organization_id::text, 1) END || ' ' || kind || '/' || provider || '/' ||
			regexp_replace(name, '^In-App \(Standard [0-9a-f-]{36}\)$', 'In-App (Standard …)') || '/' || enabled
		FROM notification_channel WHERE organization_id = ANY($1::uuid[]) ORDER BY 1`,
		[]string{rt171Bare, rt171Disabled, rt171Named, rt171Both, rt171Default}, rt171Default)
	if err != nil {
		t.Fatalf("list notification_channel: %v", err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("list notification_channel: %v", err)
	}
	return got
}

// defaultChannelsDown: the down removes nothing; the fixture is unchanged
// and the default organization keeps the channel of the first up.
func defaultChannelsDown(ctx context.Context, t *testing.T, conn *pgx.Conn) {
	t.Helper()
	want := []string{
		"2 inapp/inapp/Mitteilungen/false",
		"3 webhook/webhook/In-App/true",
		"4 email/central_smtp/In-App (Standard)/true",
		"4 webhook/webhook/In-App/true",
		"default inapp/inapp/In-App/true",
	}
	if got := defaultChannelsRows(ctx, t, conn); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("down 000171 notification channels:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// defaultChannelsUp: the up gives every organization without an in-app
// channel exactly one, enabled, under the first free name; a disabled
// in-app channel stays the only one; no e-mail channel is created; the
// default organization gets no second one.
func defaultChannelsUp(ctx context.Context, t *testing.T, conn *pgx.Conn) {
	t.Helper()
	want := []string{
		"1 inapp/inapp/In-App/true",
		"2 inapp/inapp/Mitteilungen/false",
		"3 inapp/inapp/In-App (Standard)/true",
		"3 webhook/webhook/In-App/true",
		"4 email/central_smtp/In-App (Standard)/true",
		"4 inapp/inapp/In-App (Standard …)/true",
		"4 webhook/webhook/In-App/true",
		"default inapp/inapp/In-App/true",
	}
	if got := defaultChannelsRows(ctx, t, conn); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("up 000171 notification channels:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	execAll(ctx, t, conn, `DELETE FROM organization WHERE id IN ('`+rt171Bare+`', '`+rt171Disabled+`', '`+rt171Named+`', '`+rt171Both+`')`)
}

// TestAttrTextMigrationNeedsSuperuser covers the guard of migration 000162
// (WP-435, E-73): only a superuser can declare reticora_attr_text
// LEAKPROOF; for any other role the migration stops at its first statement
// with insufficient_privilege and a message that names decision E-73 and
// its option B. It runs the up file as reticora_owner in a transaction that
// is rolled back, so the schema is not changed.
func TestAttrTextMigrationNeedsSuperuser(t *testing.T) {
	dsn := testDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(context.Background())
	up, err := os.ReadFile(filepath.Join(migrationsDir, "000162_attr_text_leakproof.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, `SET LOCAL ROLE `+database.DefaultOwnerRole); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, string(up))
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" || !strings.Contains(pgErr.Message, "E-73") ||
		!strings.Contains(pgErr.Message, "option B") || !strings.Contains(pgErr.Message, "superuser") {
		t.Fatalf("migration 000162 as %s: %v; want insufficient_privilege naming E-73 option B", database.DefaultOwnerRole, err)
	}
}

// withoutKnownDeviations removes the diff lines covered by
// knownDownDeviations[version] and returns the remaining diff together with
// the entries that matched no line.
func withoutKnownDeviations(version int, diff string) (rest string, unused []string) {
	prefixes := knownDownDeviations[version]
	used := make([]bool, len(prefixes))
	var kept []string
	for _, line := range strings.Split(diff, "\n") {
		if line == "" {
			continue
		}
		known := false
		for i, p := range prefixes {
			if strings.HasPrefix(line[2:], p) {
				used[i], known = true, true
			}
		}
		if !known {
			kept = append(kept, line)
		}
	}
	for i, p := range prefixes {
		if !used[i] {
			unused = append(unused, strings.TrimSpace(p))
		}
	}
	return strings.Join(kept, "\n"), unused
}

// TestSchemaBaselineMatchesMigratedSchema verifies that docs/schema-baseline.md
// lists exactly the tables of the migrated schema with their RLS status and
// policies. Set RETICORA_UPDATE_SCHEMA_BASELINE=1 to regenerate the table.
func TestSchemaBaselineMatchesMigratedSchema(t *testing.T) {
	dsn := testDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(context.Background())

	files := loadMigrations(t)
	m := &migrator{t: t, conn: conn, files: files}
	m.upTo(ctx, len(files))

	want := baselineRows(ctx, t, conn)

	if os.Getenv(updateBaselineEnv) == "1" {
		writeBaselineTable(t, want, files[len(files)-1])
		return
	}

	got := readBaselineTable(t)
	if diff := diffLines(got, want); diff != "" {
		t.Errorf("docs/schema-baseline.md is out of date with the migrations (- documented, + migrated); "+
			"regenerate it with %s=1 make test-db or the same variable on this test:\n%s", updateBaselineEnv, diff)
	}
	if v := readBaselineVersion(t); v != files[len(files)-1].version {
		t.Errorf("docs/schema-baseline.md describes migration %06d, newest is %06d", v, files[len(files)-1].version)
	}
}

// schemaDump returns a normalized, sorted description of everything the
// migrations create in schema public plus the installed extensions.
func schemaDump(ctx context.Context, t *testing.T, conn *pgx.Conn) []string {
	t.Helper()
	queries := []string{
		// Extensions.
		`SELECT 'extension ' || extname FROM pg_extension`,
		// Relations with their row level security flags.
		`SELECT 'relation ' || c.relname || ' kind=' || c.relkind::text ||
		        ' rls=' || c.relrowsecurity || ' force=' || c.relforcerowsecurity
		   FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		  WHERE n.nspname = 'public' AND c.relkind IN ('r','p','v','m','S','f')
		    AND c.relname <> 'schema_migrations'`,
		// Columns (order-insensitive: attnum is not part of the comparison).
		`SELECT 'column ' || c.relname || '.' || a.attname || ' ' || format_type(a.atttypid, a.atttypmod) ||
		        CASE WHEN a.attnotnull THEN ' not null' ELSE '' END ||
		        COALESCE(' default ' || pg_get_expr(d.adbin, d.adrelid), '') ||
		        CASE WHEN a.attidentity <> '' THEN ' identity ' || a.attidentity::text ELSE '' END ||
		        CASE WHEN a.attgenerated <> '' THEN ' generated ' || a.attgenerated::text ELSE '' END
		   FROM pg_attribute a
		   JOIN pg_class c ON c.oid = a.attrelid
		   JOIN pg_namespace n ON n.oid = c.relnamespace
		   LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		  WHERE n.nspname = 'public' AND c.relkind IN ('r','p','v','m','f')
		    AND a.attnum > 0 AND NOT a.attisdropped AND c.relname <> 'schema_migrations'`,
		// Constraints.
		`SELECT 'constraint ' || cl.relname || '.' || co.conname || ' ' || pg_get_constraintdef(co.oid)
		   FROM pg_constraint co
		   JOIN pg_class cl ON cl.oid = co.conrelid
		   JOIN pg_namespace n ON n.oid = cl.relnamespace
		  WHERE n.nspname = 'public' AND cl.relname <> 'schema_migrations'`,
		// Indexes.
		`SELECT 'index ' || pg_get_indexdef(i.indexrelid)
		   FROM pg_index i
		   JOIN pg_class c ON c.oid = i.indrelid
		   JOIN pg_namespace n ON n.oid = c.relnamespace
		  WHERE n.nspname = 'public' AND c.relname <> 'schema_migrations'`,
		// Policies.
		`SELECT 'policy ' || tablename || '.' || policyname || ' ' || permissive || ' ' || cmd ||
		        ' to ' || array_to_string(roles, ',') ||
		        ' using (' || COALESCE(qual, '') || ') check (' || COALESCE(with_check, '') || ')'
		   FROM pg_policies WHERE schemaname = 'public'`,
		// Triggers.
		`SELECT 'trigger ' || pg_get_triggerdef(tg.oid)
		   FROM pg_trigger tg
		   JOIN pg_class c ON c.oid = tg.tgrelid
		   JOIN pg_namespace n ON n.oid = c.relnamespace
		  WHERE n.nspname = 'public' AND NOT tg.tgisinternal`,
		// Functions and procedures (definition as hash to keep diffs short).
		`SELECT 'function ' || p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ') md5=' ||
		        md5(pg_get_functiondef(p.oid))
		   FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		  WHERE n.nspname = 'public' AND p.prokind IN ('f','p')
		    AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = p.oid AND d.deptype = 'e')`,
		// Views.
		`SELECT 'view ' || c.relname || ' ' || pg_get_viewdef(c.oid)
		   FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		  WHERE n.nspname = 'public' AND c.relkind IN ('v','m')`,
		// Enum and domain types.
		`SELECT 'type ' || t.typname || ' ' || t.typtype::text || ' ' ||
		        COALESCE((SELECT string_agg(e.enumlabel, ',' ORDER BY e.enumsortorder) FROM pg_enum e WHERE e.enumtypid = t.oid),
		                 format_type(t.typbasetype, t.typtypmod))
		   FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace
		  WHERE n.nspname = 'public' AND t.typtype IN ('e','d')
		    AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = t.oid AND d.deptype = 'e')`,
		// Privileges of the application role created by the migrations.
		`SELECT 'grant ' || c.relname || ' ' || a.privilege_type || ' to reticora_app'
		   FROM pg_class c
		   JOIN pg_namespace n ON n.oid = c.relnamespace
		   CROSS JOIN LATERAL aclexplode(c.relacl) a
		   JOIN pg_roles r ON r.oid = a.grantee
		  WHERE n.nspname = 'public' AND r.rolname = 'reticora_app'`,
		`SELECT 'grant schema ' || a.privilege_type || ' to reticora_app'
		   FROM pg_namespace n
		   CROSS JOIN LATERAL aclexplode(n.nspacl) a
		   JOIN pg_roles r ON r.oid = a.grantee
		  WHERE n.nspname = 'public' AND r.rolname = 'reticora_app'`,
	}
	var lines []string
	for _, q := range queries {
		rows, err := conn.Query(ctx, q)
		if err != nil {
			t.Fatalf("schema dump query failed: %v\n%s", err, q)
		}
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				rows.Close()
				t.Fatalf("schema dump scan: %v", err)
			}
			lines = append(lines, normalizeDumpLine(line))
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatalf("schema dump rows: %v", err)
		}
	}
	sort.Strings(lines)
	return lines
}

// TimescaleDB numbers its internal objects per creation; the numbers carry no
// schema information and differ after every roundtrip.
var timescaleCounters = regexp.MustCompile(`(_materialized_hypertable_|_hyper_|_partial_view_|_direct_view_|_compressed_hypertable_|cagg_watermark\(|continuous_agg_invalidation_trigger\('|_ts_meta_)(\d+)`)

func normalizeDumpLine(line string) string {
	line = strings.Join(strings.Fields(line), " ")
	return timescaleCounters.ReplaceAllString(line, "${1}N")
}

func objectsOutsideExtensions(dump []string) []string {
	var out []string
	for _, line := range dump {
		if !strings.HasPrefix(line, "extension ") {
			out = append(out, line)
		}
	}
	return out
}

// diffLines lists the lines only in want (-) or only in got (+).
func diffLines(want, got []string) string {
	count := map[string]int{}
	for _, l := range want {
		count[l]++
	}
	for _, l := range got {
		count[l]--
	}
	var out []string
	for l, n := range count {
		switch {
		case n > 0:
			out = append(out, "- "+l)
		case n < 0:
			out = append(out, "+ "+l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i][2:] < out[j][2:] || (out[i][2:] == out[j][2:] && out[i] < out[j]) })
	return strings.Join(out, "\n")
}

// baselineRows describes every table of schema public as one Markdown table
// row: name, RLS enabled, RLS forced and the policies with their commands.
func baselineRows(ctx context.Context, t *testing.T, conn *pgx.Conn) []string {
	t.Helper()
	rows, err := conn.Query(ctx, `
		SELECT c.relname,
		       c.relrowsecurity,
		       c.relforcerowsecurity,
		       COALESCE((SELECT string_agg(p.policyname || ' (' || p.cmd || ')', ', ' ORDER BY p.policyname)
		                   FROM pg_policies p
		                  WHERE p.schemaname = 'public' AND p.tablename = c.relname), '')
		  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'public' AND c.relkind IN ('r','p') AND c.relname <> 'schema_migrations'
		 ORDER BY c.relname`)
	if err != nil {
		t.Fatalf("query tables: %v", err)
	}
	defer rows.Close()
	yesNo := map[bool]string{true: "ja", false: "nein"}
	var out []string
	for rows.Next() {
		var (
			name, policies string
			rls, force     bool
		)
		if err := rows.Scan(&name, &rls, &force, &policies); err != nil {
			t.Fatalf("scan table: %v", err)
		}
		if policies == "" {
			policies = "–"
		}
		out = append(out, fmt.Sprintf("| `%s` | %s | %s | %s |", name, yesNo[rls], yesNo[force], policies))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("tables: %v", err)
	}
	return out
}

const (
	baselineBegin = "<!-- schema-baseline:tables:begin -->"
	baselineEnd   = "<!-- schema-baseline:tables:end -->"
	tableHeader   = "| Tabelle | RLS | FORCE RLS | Policies (Kommando) |"
	tableRule     = "|---|---|---|---|"
)

var baselineVersionLine = regexp.MustCompile(`^Stand: Migration (\d{6})_`)

func readBaselineTable(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(schemaBaselinePath)
	if err != nil {
		t.Fatalf("read docs/schema-baseline.md: %v", err)
	}
	_, rest, ok := strings.Cut(string(data), baselineBegin)
	if !ok {
		t.Fatalf("docs/schema-baseline.md: marker %q missing", baselineBegin)
	}
	section, _, ok := strings.Cut(rest, baselineEnd)
	if !ok {
		t.Fatalf("docs/schema-baseline.md: marker %q missing", baselineEnd)
	}
	var out []string
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "|") && line != tableHeader && line != tableRule {
			out = append(out, line)
		}
	}
	return out
}

func readBaselineVersion(t *testing.T) int {
	t.Helper()
	f, err := os.Open(schemaBaselinePath)
	if err != nil {
		t.Fatalf("open docs/schema-baseline.md: %v", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if m := baselineVersionLine.FindStringSubmatch(scanner.Text()); m != nil {
			v, _ := strconv.Atoi(m[1])
			return v
		}
	}
	t.Fatal("docs/schema-baseline.md: line \"Stand: Migration nnnnnn_…\" missing")
	return 0
}

func writeBaselineTable(t *testing.T, rows []string, newest migrationFiles) {
	t.Helper()
	data, err := os.ReadFile(schemaBaselinePath)
	if err != nil {
		t.Fatalf("read docs/schema-baseline.md: %v", err)
	}
	text := string(data)
	before, rest, ok := strings.Cut(text, baselineBegin)
	if !ok {
		t.Fatalf("docs/schema-baseline.md: marker %q missing", baselineBegin)
	}
	_, after, ok := strings.Cut(rest, baselineEnd)
	if !ok {
		t.Fatalf("docs/schema-baseline.md: marker %q missing", baselineEnd)
	}
	table := strings.Join(append([]string{tableHeader, tableRule}, rows...), "\n")
	text = before + baselineBegin + "\n\n" + table + "\n\n" + baselineEnd + after
	text = regexp.MustCompile(`(?m)^Stand: Migration \d{6}_\S+`).
		ReplaceAllString(text, fmt.Sprintf("Stand: Migration %06d_%s", newest.version, newest.name))
	if err := os.WriteFile(schemaBaselinePath, []byte(text), 0o600); err != nil {
		t.Fatalf("write docs/schema-baseline.md: %v", err)
	}
	t.Logf("docs/schema-baseline.md regenerated with %d tables", len(rows))
}
