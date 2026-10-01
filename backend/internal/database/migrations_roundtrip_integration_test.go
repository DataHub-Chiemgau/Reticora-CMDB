package database_test

import (
	"bufio"
	"context"
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
)

// The roundtrip test drives the migrations itself, with the semantics of
// golang-migrate (one simple-protocol Exec per file, bookkeeping in
// schema_migrations), so the database stays usable for the migrate CLI. It
// changes the whole schema of TEST_DATABASE_URL and leaves it fully migrated;
// CI therefore runs the packages sequentially (go test -p 1).

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
		m.t.Fatalf("apply %s: %v", filepath.Base(path), err)
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
// iteration is the down 1/up 1 check of the newest migration.
func TestMigrationsRoundtrip(t *testing.T) {
	dsn := testDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(context.Background())

	files := loadMigrations(t)
	latest := len(files)
	m := &migrator{t: t, conn: conn, files: files}
	m.upTo(ctx, latest)
	truncateAll(ctx, t, conn)
	schemaA := schemaDump(ctx, t, conn)

	for m.version(ctx) > 0 {
		m.down(ctx)
	}
	if leftovers := objectsOutsideExtensions(schemaDump(ctx, t, conn)); len(leftovers) > 0 {
		t.Errorf("objects left after migrating down to version 0:\n  %s", strings.Join(leftovers, "\n  "))
	}

	for _, f := range files {
		before := schemaDump(ctx, t, conn)
		m.up(ctx)
		afterUp := schemaDump(ctx, t, conn)
		m.down(ctx)
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
		if diff := diffLines(afterUp, schemaDump(ctx, t, conn)); diff != "" {
			t.Errorf("re-applying %06d_%s after its down gives a different schema (- first up, + second up):\n%s",
				f.version, f.name, diff)
		}
	}

	if diff := diffLines(schemaA, schemaDump(ctx, t, conn)); diff != "" {
		t.Errorf("schema after down to 0 and up differs from the initial schema (- A, + B):\n%s", diff)
	}
}

// truncateAll removes the rows other tests left behind. Migrating down to 0
// drops all data anyway, but rows referencing seeded catalog entries (for
// example CIs of a global system type) would make a down migration fail. The
// final up seeds the catalogs again, so later tests see a fresh schema.
func truncateAll(ctx context.Context, t *testing.T, conn *pgx.Conn) {
	t.Helper()
	rows, err := conn.Query(ctx, `
		SELECT string_agg(format('%I', c.relname), ', ')
		  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') AND c.relname <> 'schema_migrations'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	var tables *string
	for rows.Next() {
		if err := rows.Scan(&tables); err != nil {
			t.Fatalf("list tables: %v", err)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("list tables: %v", err)
	}
	if tables == nil {
		return
	}
	// Replica mode skips the append-only triggers of audit_log and
	// asset_movement; the whole statement runs as one implicit transaction.
	if _, err := conn.Exec(ctx, "SET session_replication_role = replica; TRUNCATE "+*tables+" CASCADE; SET session_replication_role = DEFAULT"); err != nil {
		t.Fatalf("truncate test data: %v", err)
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
