package rls_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant/rls"
)

const (
	backendDir      = "../../.."
	migrationsDir   = "../../../migrations"
	planPath        = "../../../../docs/plan/implementierungsplan.md"
	baselineDocPath = "../../../../docs/schema-baseline.md"
	gapsBegin       = "<!-- rls-known-gaps:begin -->"
	gapsEnd         = "<!-- rls-known-gaps:end -->"
	updateDocEnv    = "RETICORA_UPDATE_SCHEMA_BASELINE"
)

// catalogPolicy is one row of pg_policies that applies to the application.
type catalogPolicy struct {
	name       string
	permissive bool
	cmd        string
	using      string // empty when the policy has no USING clause
	check      string // empty when the policy has no WITH CHECK clause
}

// catalogTable describes a table of schema public with its RLS flags, scope
// columns and the policies that apply to PUBLIC or reticora_app.
type catalogTable struct {
	name                    string
	rls, force              bool
	org, client, site, team bool
	// appWrite: reticora_app holds INSERT, UPDATE or DELETE on the table.
	appWrite bool
	// appAccess: reticora_app holds any privilege on the table.
	appAccess bool
	// appDelete: reticora_app holds DELETE or TRUNCATE on the table.
	appDelete bool
	// truncateGuard: the table carries the enabled TRUNCATE guard
	// (database.TruncateGuardTrigger).
	truncateGuard bool
	// barrierView: the table's view of rls.ViewProtectedTables passes
	// rls.RuleViewBarrier.
	barrierView bool
	// refs are the location and CI reference columns (rls.RuleLocatedScope);
	// treeNode: the id references location (site, building, room, rack).
	refs     []string
	treeNode bool
	// clientDim, siteDim: the table carries the dimension (client_id,
	// source_client_id or target_client_id; likewise site, for site its id).
	clientDim, siteDim bool
	policies           []catalogPolicy
}

// lacksLocatedScope reports whether rls.RuleLocatedScope applies to the
// table and it lacks the client or the site dimension.
func (tbl *catalogTable) lacksLocatedScope() bool {
	return tbl.org && (len(tbl.refs) > 0 || tbl.treeNode) && (!tbl.clientDim || !tbl.siteDim)
}

var commands = []string{"SELECT", "INSERT", "UPDATE", "DELETE"}

// sides lists which policy expressions PostgreSQL evaluates for a command.
var sides = map[string][]string{
	"SELECT": {"using"},
	"INSERT": {"check"},
	"UPDATE": {"using", "check"},
	"DELETE": {"using"},
}

func (p *catalogPolicy) appliesTo(cmd string) bool { return p.cmd == "ALL" || p.cmd == cmd }

// expr returns the expression evaluated for the given side. Without an explicit
// WITH CHECK PostgreSQL reuses USING for ALL and UPDATE policies.
func (p *catalogPolicy) expr(side string) string {
	if side == "check" && p.check == "" && p.cmd != "INSERT" {
		return p.using
	}
	if side == "check" {
		return p.check
	}
	return p.using
}

func usesGUC(expr, guc string) bool { return strings.Contains(expr, "'"+guc+"'") }

var globalRows = regexp.MustCompile(`\borganization_id IS NULL\b`)

// TestRLSCatalog checks every tenant table against the rules of
// rls.KnownGaps (TEN-03, TEN-05, TEN-09) and requires the known gap list to be
// exact: unlisted violations and listed gaps that no longer occur both fail.
// Likewise every entry of rls.DerivedScopeTables must name an existing test
// and a table that rls.RuleLocatedScope reports (TEN-02, WP-393).
func TestRLSCatalog(t *testing.T) {
	dsn := testDatabaseURL(t)
	ctx := context.Background()

	pool, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatalf("maintenance pool: %v", err)
	}
	defer pool.Close()

	requireMigrated(ctx, t, pool)
	tables := loadCatalog(ctx, t, pool)
	if len(tables) == 0 {
		t.Fatal("schema public has no tables; run the migrations first")
	}

	actual := map[gapKey]bool{}
	for _, tbl := range tables {
		for _, r := range violations(tbl) {
			actual[gapKey{tbl.name, r}] = true
		}
	}

	known := map[gapKey]rls.Gap{}
	for _, g := range rls.KnownGaps {
		known[gapKey{g.Table, g.Rule}] = g
	}

	var unlisted, closed []string
	for k := range actual {
		if _, ok := known[k]; !ok {
			unlisted = append(unlisted, fmt.Sprintf("%s: %s", k.table, k.rule))
		}
	}
	for k, g := range known {
		if !actual[k] {
			closed = append(closed, fmt.Sprintf("%s: %s (%s)", k.table, k.rule, g.WP))
		}
	}
	sort.Strings(unlisted)
	sort.Strings(closed)
	if len(unlisted) > 0 {
		t.Errorf("RLS rule violations without a known gap; fix the policies (new tables must meet every rule "+
			"and must not be added to rls.KnownGaps; %s: add client_id and site_id or document the derived "+
			"scope in rls.DerivedScopeTables):\n  %s", rls.RuleLocatedScope, strings.Join(unlisted, "\n  "))
	}
	if len(closed) > 0 {
		t.Errorf("known RLS gaps are closed; remove them from rls.KnownGaps and docs/schema-baseline.md:\n  %s",
			strings.Join(closed, "\n  "))
	}

	for _, name := range slices.Sorted(maps.Keys(rls.DerivedScopeTables)) {
		d := rls.DerivedScopeTables[name]
		if tbl, ok := tables[name]; !ok || !tbl.lacksLocatedScope() {
			t.Errorf("rls.DerivedScopeTables: %s: rule %s does not report the table (it has no location or CI "+
				"reference, or carries client_id and site_id); remove the entry", name, rls.RuleLocatedScope)
		}
		if d.Path == "" || d.Reason == "" {
			t.Errorf("rls.DerivedScopeTables: %s: derivation path and reason are required", name)
		}
		if err := testExists(d.TestFile, d.TestName); err != nil {
			t.Errorf("rls.DerivedScopeTables: %s: test of the derivation: %v", name, err)
		}
	}
}

// testExists checks that file (relative to backend/) is a Go test file that
// declares the test function name.
func testExists(file, name string) error {
	if !strings.HasSuffix(file, "_test.go") || !regexp.MustCompile(`^Test\w+$`).MatchString(name) {
		return fmt.Errorf("%q in %q is not a Go test", name, file)
	}
	data, err := os.ReadFile(filepath.Join(backendDir, filepath.FromSlash(file)))
	if err != nil {
		return err
	}
	if !regexp.MustCompile(`(?m)^func ` + name + `\(t \*testing\.T\) \{`).Match(data) {
		return fmt.Errorf("%s does not declare %s", file, name)
	}
	return nil
}

// TestKnownGapsList validates rls.KnownGaps without a database: no duplicates,
// known rules, existing work packages and only tables that existed at
// rls.KnownGapsBaselineMigration.
func TestKnownGapsList(t *testing.T) {
	rules := map[rls.Rule]bool{
		rls.RuleRLSEnabled: true, rls.RuleRLSForced: true, rls.RuleCommands: true,
		rls.RuleUsing: true, rls.RuleWithCheck: true, rls.RuleOrgPredicate: true,
		rls.RuleSystemWrite: true, rls.RuleGlobalRows: true, rls.RuleClientScope: true,
		rls.RuleSiteScope: true, rls.RuleTeamScope: true, rls.RuleOrgColumn: true,
		rls.RuleReadOnlyCatalog: true, rls.RuleViewBarrier: true, rls.RuleOperatorOnly: true,
		rls.RuleLocatedScope: true, rls.RuleSystemOnly: true,
	}
	plan, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read implementation plan: %v", err)
	}
	baselineTables := tablesCreatedUpTo(t, rls.KnownGapsBaselineMigration)

	seen := map[gapKey]bool{}
	for _, g := range rls.KnownGaps {
		k := gapKey{g.Table, g.Rule}
		if seen[k] {
			t.Errorf("duplicate known gap %s: %s", g.Table, g.Rule)
		}
		seen[k] = true
		if !rules[g.Rule] {
			t.Errorf("known gap %s: unknown rule %q", g.Table, g.Rule)
		}
		if !regexp.MustCompile(`^WP-\d{3}$`).MatchString(g.WP) ||
			!strings.Contains(string(plan), "\n### "+g.WP+" ") {
			t.Errorf("known gap %s: %s: work package %q is not in docs/plan/implementierungsplan.md", g.Table, g.Rule, g.WP)
		}
		if !baselineTables[g.Table] {
			t.Errorf("known gap %s: %s: table is not created by a migration up to %06d; new tables must meet every rule",
				g.Table, g.Rule, rls.KnownGapsBaselineMigration)
		}
	}
}

// TestKnownGapsDocumented requires the gap table in docs/schema-baseline.md to
// match rls.KnownGaps. Set RETICORA_UPDATE_SCHEMA_BASELINE=1 to regenerate it.
func TestKnownGapsDocumented(t *testing.T) {
	data, err := os.ReadFile(baselineDocPath)
	if err != nil {
		t.Fatalf("read docs/schema-baseline.md: %v", err)
	}
	text := string(data)
	before, rest, ok := strings.Cut(text, gapsBegin)
	if !ok {
		t.Fatalf("docs/schema-baseline.md: marker %q missing", gapsBegin)
	}
	current, after, ok := strings.Cut(rest, gapsEnd)
	if !ok {
		t.Fatalf("docs/schema-baseline.md: marker %q missing", gapsEnd)
	}
	want := "\n\n" + gapsTable() + "\n\n"
	if os.Getenv(updateDocEnv) == "1" {
		text = before + gapsBegin + want + gapsEnd + after
		if err := os.WriteFile(baselineDocPath, []byte(text), 0o600); err != nil {
			t.Fatalf("write docs/schema-baseline.md: %v", err)
		}
		return
	}
	if current != want {
		t.Errorf("docs/schema-baseline.md: known RLS gaps differ from rls.KnownGaps; regenerate with %s=1:\n"+
			"documented:%s\nexpected:%s", updateDocEnv, current, want)
	}
}

type gapKey struct {
	table string
	rule  rls.Rule
}

func gapsTable() string {
	lines := []string{"| Tabelle | Regel | Zuständiges WP |", "|---|---|---|"}
	for _, g := range rls.KnownGaps {
		lines = append(lines, fmt.Sprintf("| `%s` | `%s` | %s |", g.Table, g.Rule, g.WP))
	}
	return strings.Join(lines, "\n")
}

// violations evaluates every catalog rule for one table.
func violations(tbl *catalogTable) []rls.Rule {
	var out []rls.Rule
	if _, derived := rls.DerivedScopeTables[tbl.name]; tbl.lacksLocatedScope() && !derived {
		out = append(out, rls.RuleLocatedScope)
	}
	if _, ok := rls.ViewProtectedTables[tbl.name]; ok {
		// Documented exception (ADR 0001): no direct privilege for the
		// application, access only through the security-barrier view.
		if tbl.appAccess || !tbl.barrierView {
			out = append(out, rls.RuleViewBarrier)
		}
		return out
	}
	if slices.Contains(rls.GlobalCatalogTables, tbl.name) {
		// Documented exception (E-10): a global catalog without
		// organization_id must be read-only for the application.
		if tbl.appWrite {
			return []rls.Rule{rls.RuleReadOnlyCatalog}
		}
		return nil
	}
	if slices.Contains(rls.OperatorTables, tbl.name) {
		// Documented exception (SEC-07): organization-spanning operator
		// table, reachable only in the operator context.
		operatorOnly := tbl.rls && tbl.force && len(tbl.policies) > 0
		for i := range tbl.policies {
			p := &tbl.policies[i]
			if !usesGUC(p.using, rls.OperatorGUC) || (p.cmd != "SELECT" && p.cmd != "DELETE" && !usesGUC(p.expr("check"), rls.OperatorGUC)) {
				operatorOnly = false
			}
		}
		if !operatorOnly {
			return []rls.Rule{rls.RuleOperatorOnly}
		}
		return nil
	}
	if slices.Contains(rls.SystemTables, tbl.name) {
		// Documented exception (SEC-06, WP-459): a table of the
		// installation, reachable only with the system flag, whose rows
		// are never deleted.
		systemOnly := tbl.rls && tbl.force && tbl.truncateGuard && !tbl.appDelete && len(tbl.policies) > 0
		for i := range tbl.policies {
			p := &tbl.policies[i]
			if p.appliesTo("DELETE") || (p.cmd != "INSERT" && !usesGUC(p.using, rls.SystemGUC)) ||
				(p.cmd != "SELECT" && !usesGUC(p.expr("check"), rls.SystemGUC)) {
				systemOnly = false
			}
		}
		if !systemOnly {
			return []rls.Rule{rls.RuleSystemOnly}
		}
		return nil
	}
	if !tbl.org && tbl.name != "organization" {
		return []rls.Rule{rls.RuleOrgColumn}
	}
	if !tbl.rls {
		out = append(out, rls.RuleRLSEnabled)
	}
	if !tbl.force {
		out = append(out, rls.RuleRLSForced)
	}
	for _, cmd := range commands {
		if len(tbl.applicable(cmd, true)) == 0 {
			out = append(out, rls.RuleCommands)
			break
		}
	}
	if tbl.violatesClauses(func(p *catalogPolicy) bool {
		return p.cmd != "INSERT" && p.using == ""
	}) {
		out = append(out, rls.RuleUsing)
	}
	if tbl.violatesClauses(func(p *catalogPolicy) bool {
		return p.cmd != "SELECT" && p.cmd != "DELETE" && p.check == ""
	}) {
		out = append(out, rls.RuleWithCheck)
	}
	if !tbl.enforces(rls.OrgGUC) {
		out = append(out, rls.RuleOrgPredicate)
	}
	if tbl.writable(func(expr string) bool { return usesGUC(expr, rls.SystemGUC) }) {
		out = append(out, rls.RuleSystemWrite)
	}
	if tbl.writable(globalRows.MatchString) {
		out = append(out, rls.RuleGlobalRows)
	}
	if tbl.client && !tbl.enforces(rls.ClientScopeGUC) {
		out = append(out, rls.RuleClientScope)
	}
	if tbl.site && !tbl.enforces(rls.SiteScopeGUC) {
		out = append(out, rls.RuleSiteScope)
	}
	if tbl.team && !tbl.enforces(rls.TeamScopeGUC) {
		out = append(out, rls.RuleTeamScope)
	}
	return out
}

func (tbl *catalogTable) applicable(cmd string, permissive bool) []*catalogPolicy {
	var out []*catalogPolicy
	for i := range tbl.policies {
		p := &tbl.policies[i]
		if p.permissive == permissive && p.appliesTo(cmd) {
			out = append(out, p)
		}
	}
	return out
}

func (tbl *catalogTable) violatesClauses(missing func(*catalogPolicy) bool) bool {
	for i := range tbl.policies {
		if missing(&tbl.policies[i]) {
			return true
		}
	}
	return false
}

// enforces reports whether every evaluated expression of every command
// references guc: either all permissive policies or one restrictive policy
// must use it. Commands without permissive policies are left to RuleCommands.
func (tbl *catalogTable) enforces(guc string) bool {
	for _, cmd := range commands {
		permissive := tbl.applicable(cmd, true)
		if len(permissive) == 0 {
			continue
		}
		restrictive := tbl.applicable(cmd, false)
		for _, side := range sides[cmd] {
			all := true
			for _, p := range permissive {
				all = all && usesGUC(p.expr(side), guc)
			}
			some := false
			for _, p := range restrictive {
				some = some || usesGUC(p.expr(side), guc)
			}
			if !all && !some {
				return false
			}
		}
	}
	return true
}

// writable reports whether a permissive policy for INSERT, UPDATE or DELETE
// evaluates an expression matching match.
func (tbl *catalogTable) writable(match func(string) bool) bool {
	for _, cmd := range commands[1:] {
		for _, p := range tbl.applicable(cmd, true) {
			for _, side := range sides[cmd] {
				if match(p.expr(side)) {
					return true
				}
			}
		}
	}
	return false
}

func requireMigrated(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var version int
	var dirty bool
	if err := pool.QueryRow(ctx, `SELECT version, dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	if dirty || version < rls.KnownGapsBaselineMigration {
		t.Fatalf("database is at migration %d (dirty=%t); migrate up before the catalog test", version, dirty)
	}
}

func loadCatalog(ctx context.Context, t *testing.T, pool *pgxpool.Pool) map[string]*catalogTable {
	t.Helper()
	tables := map[string]*catalogTable{}
	rows, err := pool.Query(ctx, `
		SELECT c.relname, c.relrowsecurity, c.relforcerowsecurity,
		       bool_or(a.attname = 'organization_id'), bool_or(a.attname = 'client_id'),
		       bool_or(a.attname = 'site_id'), bool_or(a.attname = 'team_id'),
		       has_table_privilege('reticora_app', c.oid, 'INSERT, UPDATE, DELETE'),
		       has_table_privilege('reticora_app', c.oid, 'SELECT, INSERT, UPDATE, DELETE'),
		       has_table_privilege('reticora_app', c.oid, 'DELETE, TRUNCATE'),
		       EXISTS (SELECT 1 FROM pg_trigger tg
		                WHERE tg.tgrelid = c.oid AND tg.tgname = '`+database.TruncateGuardTrigger+`'
		                  AND tg.tgfoid = to_regprocedure('public.`+database.TruncateGuardTrigger+`()')
		                  AND tg.tgtype & 1 = 0 AND tg.tgtype & 2 = 2 AND tg.tgtype & 32 = 32 -- statement, BEFORE, TRUNCATE
		                  AND tg.tgenabled IN ('O', 'A')),
		       COALESCE(array_agg(a.attname::text ORDER BY a.attnum)
		                FILTER (WHERE a.attname ~ '^(\w+_)?(location|room|rack|ci)_id$'), '{}'),
		       EXISTS (SELECT 1 FROM pg_constraint k
		                 JOIN pg_attribute ka ON ka.attrelid = k.conrelid AND ka.attnum = ANY (k.conkey)
		                WHERE k.conrelid = c.oid AND k.contype = 'f' AND k.confrelid = to_regclass('public.location')
		                  AND ka.attname = 'id'),
		       bool_or(a.attname IN ('client_id', 'source_client_id', 'target_client_id')),
		       bool_or(a.attname IN ('site_id', 'source_site_id', 'target_site_id') OR (c.relname = 'site' AND a.attname = 'id'))
		  FROM pg_class c
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		  JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
		 WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') AND NOT c.relispartition
		   AND c.relname <> 'schema_migrations'
		 GROUP BY c.oid, c.relname, c.relrowsecurity, c.relforcerowsecurity`)
	if err != nil {
		t.Fatalf("query tables: %v", err)
	}
	for rows.Next() {
		tbl := &catalogTable{}
		if scanErr := rows.Scan(&tbl.name, &tbl.rls, &tbl.force, &tbl.org, &tbl.client, &tbl.site, &tbl.team, &tbl.appWrite, &tbl.appAccess,
			&tbl.appDelete, &tbl.truncateGuard, &tbl.refs, &tbl.treeNode, &tbl.clientDim, &tbl.siteDim); scanErr != nil {
			t.Fatalf("scan table: %v", scanErr)
		}
		tables[tbl.name] = tbl
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		t.Fatalf("query tables: %v", err)
	}

	for table, view := range rls.ViewProtectedTables {
		tbl, ok := tables[table]
		if !ok {
			continue
		}
		// The view must be a security barrier with the organization
		// predicate and grant the application SELECT and INSERT only.
		if err = pool.QueryRow(ctx, `
			SELECT COALESCE('security_barrier=true' = ANY (v.reloptions), false)
			       AND position($2 IN pg_get_viewdef(v.oid)) > 0
			       AND has_table_privilege('reticora_app', v.oid, 'SELECT')
			       AND has_table_privilege('reticora_app', v.oid, 'INSERT')
			       AND NOT has_table_privilege('reticora_app', v.oid, 'UPDATE, DELETE')
			  FROM pg_class v
			  JOIN pg_namespace n ON n.oid = v.relnamespace
			 WHERE n.nspname = 'public' AND v.relkind = 'v' AND v.relname = $1`, view, rls.OrgGUC).Scan(&tbl.barrierView); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("inspect view %s: %v", view, err)
		}
	}

	rows, err = pool.Query(ctx, `
		SELECT tablename, policyname, permissive = 'PERMISSIVE', cmd,
		       COALESCE(qual, ''), COALESCE(with_check, '')
		  FROM pg_policies
		 WHERE schemaname = 'public'
		   AND (roles && ARRAY['public', 'reticora_app']::name[])`)
	if err != nil {
		t.Fatalf("query policies: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var table string
		var p catalogPolicy
		if err := rows.Scan(&table, &p.name, &p.permissive, &p.cmd, &p.using, &p.check); err != nil {
			t.Fatalf("scan policy: %v", err)
		}
		if tbl, ok := tables[table]; ok {
			tbl.policies = append(tbl.policies, p)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("query policies: %v", err)
	}
	return tables
}

var createTable = regexp.MustCompile(`(?i)\bCREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(?:public\.)?"?(\w+)"?`)

// tablesCreatedUpTo returns the tables created by the up migrations with a
// version up to maxVersion.
func tablesCreatedUpTo(t *testing.T, maxVersion int) map[string]bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(migrationsDir, "*.up.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("list migrations: %v", err)
	}
	out := map[string]bool{}
	for _, f := range files {
		v, err := strconv.Atoi(strings.SplitN(filepath.Base(f), "_", 2)[0])
		if err != nil {
			t.Fatalf("migration %s: version: %v", f, err)
		}
		if v > maxVersion {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range createTable.FindAllStringSubmatch(string(data), -1) {
			out[strings.ToLower(m[1])] = true
		}
	}
	return out
}
