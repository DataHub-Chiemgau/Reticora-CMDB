package rls_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant/rls"
)

const (
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
	policies                []catalogPolicy
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
			"and must not be added to rls.KnownGaps):\n  %s", strings.Join(unlisted, "\n  "))
	}
	if len(closed) > 0 {
		t.Errorf("known RLS gaps are closed; remove them from rls.KnownGaps and docs/schema-baseline.md:\n  %s",
			strings.Join(closed, "\n  "))
	}
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
	if !tbl.org && tbl.name != "organization" {
		return []rls.Rule{rls.RuleOrgColumn}
	}
	var out []rls.Rule
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
		       bool_or(a.attname = 'site_id'), bool_or(a.attname = 'team_id')
		  FROM pg_class c
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		  JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
		 WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') AND NOT c.relispartition
		   AND c.relname <> 'schema_migrations'
		 GROUP BY c.relname, c.relrowsecurity, c.relforcerowsecurity`)
	if err != nil {
		t.Fatalf("query tables: %v", err)
	}
	for rows.Next() {
		tbl := &catalogTable{}
		if scanErr := rows.Scan(&tbl.name, &tbl.rls, &tbl.force, &tbl.org, &tbl.client, &tbl.site, &tbl.team); scanErr != nil {
			t.Fatalf("scan table: %v", scanErr)
		}
		tables[tbl.name] = tbl
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		t.Fatalf("query tables: %v", err)
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
