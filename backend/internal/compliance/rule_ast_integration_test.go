package compliance_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/compliance"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/security"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Compliance rules as MET-45 conditions on PostgreSQL (SEC-03, CMP-02,
// WP-1125, E-229 option 1): the API refuses an invalid AST and a field that
// is no input with 422 on the path, the data migration converts the G1
// expressions and deactivates matches, and the finding inputs of a page of
// CIs are read in one query.
//
// They run only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestCreateRuleRejectsInvalidAST|TestCreateRuleRejectsUnknownInput|TestLegacyRuleMigration|TestFindingFactsSingleQuery' ./internal/compliance/

// astEnv is the compliance API of organization A on PostgreSQL.
type astEnv struct {
	f      *scopetest.Fixture
	repo   *compliance.PGRepository
	router chi.Router
	ctx    context.Context
}

func newASTEnv(t *testing.T) *astEnv {
	t.Helper()
	f := scopetest.Seed(t, "3b")
	repo := compliance.NewPGRepository(f.App)
	router := chi.NewRouter()
	compliance.NewHandler(repo, compliance.NewEvaluator(repo, ci.NewPGRepository(f.App))).RegisterRoutes(router)
	scope := database.OrgWideScope(f.OrgA, "")
	ctx := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope), tenant.TenantInfo{OrganizationID: f.OrgA})
	return &astEnv{f: f, repo: repo, router: router, ctx: ctx}
}

func (e *astEnv) do(t *testing.T, method, path string, body any) (int, api.FieldProblem, string) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(e.ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	var p api.FieldProblem
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	return rec.Code, p, rec.Body.String()
}

func ruleBody(expression any) map[string]any {
	return map[string]any{"name": "wp1125-rule", "severity": "high", "category": "ISO27001", "expression": expression}
}

// TestCreateRuleRejectsInvalidAST: an invalid AST is 422 with the path of
// the parser error, on POST and PATCH; applies_when alike.
func TestCreateRuleRejectsInvalidAST(t *testing.T) {
	e := newASTEnv(t)
	for _, c := range []struct {
		body  map[string]any
		field string
	}{
		{ruleBody(map[string]any{"op": "all", "rules": []any{map[string]any{"op": "eq", "field": "name", "value": "a"}, map[string]any{"op": "eq", "value": "b"}}}), "expression.rules[1].field"},
		{ruleBody(map[string]any{"op": "matches", "field": "name", "value": "^sw"}), "expression.op"},
		{ruleBody(map[string]any{"field": "name", "op": "exists"}), "expression.op"},
		{ruleBody(map[string]any{"op": "pattern", "pattern": "^a"}), "expression.op"},
		{func() map[string]any {
			b := ruleBody(map[string]any{"op": "eq", "field": "name", "value": "a"})
			b["applies_when"] = map[string]any{"op": "in", "field": "status", "value": []any{}}
			return b
		}(), "applies_when.value"},
	} {
		code, p, raw := e.do(t, http.MethodPost, "/api/v1/compliance/rules", c.body)
		if code != http.StatusUnprocessableEntity || p.Field != c.field {
			t.Errorf("POST %v: %d %s, want 422 on %s", c.body["expression"], code, raw, c.field)
		}
		for _, m := range []string{"SQLSTATE", "compliance_rule"} {
			if strings.Contains(raw, m) {
				t.Errorf("answer discloses %q: %s", m, raw)
			}
		}
	}
	code, _, raw := e.do(t, http.MethodPost, "/api/v1/compliance/rules", map[string]any{"name": "wp1125-ok", "severity": "high", "category": "ISO27001",
		"expression":   map[string]any{"op": "eq", "field": "status", "value": "active"},
		"applies_when": map[string]any{"op": "eq", "field": "lifecycle_state", "value": "production"}})
	if code != http.StatusCreated {
		t.Fatalf("valid rule: %d %s", code, raw)
	}
	var rule compliance.Rule
	if err := json.Unmarshal([]byte(raw), &rule); err != nil || rule.AppliesWhen["field"] != "lifecycle_state" {
		t.Fatalf("created rule: %+v %v", rule, err)
	}
	if code, p, raw := e.do(t, http.MethodPatch, "/api/v1/compliance/rules/"+rule.ID, map[string]any{"expression": map[string]any{"op": "gt", "field": "attributes.n", "value": "x"}}); code != http.StatusUnprocessableEntity || p.Field != "expression.value" {
		t.Errorf("PATCH invalid expression: %d %s, want 422 on expression.value", code, raw)
	}
	if code, _, raw := e.do(t, http.MethodPatch, "/api/v1/compliance/rules/"+rule.ID, map[string]any{"applies_when": nil}); code != http.StatusOK || strings.Contains(raw, `"applies_when":{`) {
		t.Errorf("PATCH applies_when null: %d %s, want the condition removed", code, raw)
	}
}

// TestCreateRuleRejectsUnknownInput: a field that is no input of Inputs is
// 422 on its path.
func TestCreateRuleRejectsUnknownInput(t *testing.T) {
	e := newASTEnv(t)
	for _, c := range []struct {
		expression map[string]any
		field      string
	}{
		{map[string]any{"op": "eq", "field": "hostname", "value": "a"}, "expression.field"},
		{map[string]any{"op": "any", "rules": []any{map[string]any{"op": "eq", "field": "name", "value": "a"}, map[string]any{"op": "gt", "field": "finding.open_count.compliance_violation", "value": 0}}}, "expression.rules[1].field"},
		{map[string]any{"op": "not", "rules": []any{map[string]any{"op": "empty", "field": "attributes."}}}, "expression.rules[0].field"},
	} {
		if code, p, raw := e.do(t, http.MethodPost, "/api/v1/compliance/rules", ruleBody(c.expression)); code != http.StatusUnprocessableEntity || p.Field != c.field {
			t.Errorf("POST %v: %d %s, want 422 on %s", c.expression, code, raw, c.field)
		}
	}
}

// migrationBlock returns the SQL between the markers of the migration
// compliance_rule_ast.
func migrationBlock(t *testing.T, marker string) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*_compliance_rule_ast.up.sql"))
	if err != nil || len(files) != 1 {
		t.Fatalf("compliance_rule_ast migration: %v %v", files, err)
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(b), "-- "+marker+":begin")
	block, _, ok2 := strings.Cut(rest, "-- "+marker+":end")
	if !ok || !ok2 {
		t.Fatalf("markers %s missing in %s", marker, files[0])
	}
	return block
}

// TestLegacyRuleMigration: G1 rules with exists, eq and matches stored
// before the data migration become not_empty and eq; the matches rule is
// inactive and keeps its old expression in the description. Rules already
// in MET-45 form (all, lt) stay unchanged and active, and a second run of
// the block changes nothing.
func TestLegacyRuleMigration(t *testing.T) {
	e := newASTEnv(t)
	bg := context.Background()
	tx, err := e.f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	ids := map[string]string{}
	for name, expr := range map[string]string{
		"legacy-exists":  `{"field":"serial_number","op":"exists"}`,
		"legacy-eq":      `{"field":"attributes.encrypted","op":"eq","value":true}`,
		"legacy-noop":    `{"field":"status","value":"active"}`,
		"legacy-matches": `{"field":"name","op":"matches","value":"^sw-"}`,
		// Already MET-45 (written after 000332, seen again on up after down).
		"met45-all": `{"op":"all","rules":[{"op":"eq","field":"status","value":"active"},{"op":"not_empty","field":"serial_number"}]}`,
		"met45-lt":  `{"op":"lt","field":"finding.max_open_severity_rank","value":3}`,
	} {
		var id string
		if err = tx.QueryRow(bg, `INSERT INTO compliance_rule (organization_id, name, description, severity, category, expression, active)
			VALUES ($1, $2, 'G1 rule', 'high', 'ISO27001', $3::jsonb, true) RETURNING id::text`, e.f.OrgA, name, expr).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}
	if _, err = tx.Exec(bg, migrationBlock(t, "compliance-rule-ast")); err != nil {
		t.Fatalf("data migration: %v", err)
	}
	read := func(name string) (expr map[string]any, active bool, description string) {
		t.Helper()
		var raw []byte
		if qerr := tx.QueryRow(bg, `SELECT expression, active, description FROM compliance_rule WHERE id = $1`, ids[name]).Scan(&raw, &active, &description); qerr != nil {
			t.Fatal(qerr)
		}
		if jerr := json.Unmarshal(raw, &expr); jerr != nil {
			t.Fatal(jerr)
		}
		return expr, active, description
	}
	for name, want := range map[string]map[string]any{
		"legacy-exists": {"op": "not_empty", "field": "serial_number"},
		"legacy-eq":     {"op": "eq", "field": "attributes.encrypted", "value": true},
		"legacy-noop":   {"op": "eq", "field": "status", "value": "active"},
		"met45-all": {"op": "all", "rules": []any{map[string]any{"op": "eq", "field": "status", "value": "active"},
			map[string]any{"op": "not_empty", "field": "serial_number"}}},
		"met45-lt": {"op": "lt", "field": "finding.max_open_severity_rank", "value": 3},
	} {
		expr, active, description := read(name)
		if description != "G1 rule" {
			t.Errorf("%s: description %q changed", name, description)
		}
		raw, _ := json.Marshal(expr)
		wantRaw, _ := json.Marshal(want)
		if !active || !bytes.Equal(raw, wantRaw) {
			t.Errorf("%s: %s active %v, want %s active", name, raw, active, wantRaw)
		}
		if verr := compliance.ValidateCondition("expression", expr); verr != nil {
			t.Errorf("%s converted to no valid MET-45 condition: %v", name, verr)
		}
	}
	expr, active, description := read("legacy-matches")
	if active || expr["op"] != "matches" || !strings.Contains(description, `"op": "matches"`) || !strings.HasPrefix(description, "G1 rule") {
		t.Errorf("matches rule: %v active %v description %q, want inactive with the legacy expression appended", expr, active, description)
	}
	// A second run (up after the down migration) changes nothing: the
	// deactivated rule gets no second note, the MET-45 rules stay active.
	if _, err = tx.Exec(bg, migrationBlock(t, "compliance-rule-ast")); err != nil {
		t.Fatalf("second data migration: %v", err)
	}
	if _, again, second := read("legacy-matches"); again || second != description {
		t.Errorf("second run changed the matches rule: active %v description %q, want %q", again, second, description)
	}
	for _, name := range []string{"met45-all", "met45-lt", "legacy-eq"} {
		if _, active, _ := read(name); !active {
			t.Errorf("second run deactivated %s", name)
		}
	}
	var functions int
	if err := tx.QueryRow(bg, `SELECT count(*) FROM pg_proc WHERE proname = 'reticora_cmp_legacy_condition'`).Scan(&functions); err != nil || functions != 0 {
		t.Errorf("helper function left behind: %d %v", functions, err)
	}
}

// queryCounter counts the statements that read security_finding.
type queryCounter struct{ n atomic.Int32 }

func (c *queryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "FROM security_finding") {
		c.n.Add(1)
	}
	return ctx
}
func (c *queryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// TestFindingFactsSingleQuery: the finding inputs of 500 CIs are one query;
// the evaluation reads them once per page of 500 CIs (WP-1126).
func TestFindingFactsSingleQuery(t *testing.T) {
	e := newASTEnv(t)
	bg := context.Background()
	var typeID string
	if err := e.f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`).Scan(&typeID); err != nil {
		t.Fatal(err)
	}
	rows, err := e.f.Admin.Query(bg, `INSERT INTO ci (organization_id, ci_type_id, name, status)
		SELECT $1, $2, 'wp1125-' || g, 'active' FROM generate_series(1, 500) g RETURNING id::text`, e.f.OrgA, typeID)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(ids) != 500 {
		t.Fatalf("CIs: %d %v", len(ids), err)
	}
	findings := security.NewPGRepository(e.f.App)
	for i, f := range []*security.Finding{
		{CIID: ids[0], Kind: "vulnerability", Severity: "critical", Status: "open", Reference: "CVE-2026-0001"},
		{CIID: ids[0], Kind: "vulnerability", Severity: "low", Status: "open", Reference: "CVE-2026-0002"},
		{CIID: ids[1], Kind: "missing_patch", Severity: "medium", Status: "open", Reference: "KB1"},
	} {
		f.OrganizationID, f.Title, f.Source = e.f.OrgA, f.Reference, "manual"
		if cerr := findings.Create(e.ctx, f); cerr != nil {
			t.Fatalf("finding %d: %v", i, cerr)
		}
	}

	cfg := e.f.App.Config()
	counter := &queryCounter{}
	cfg.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(bg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repo := compliance.NewPGRepository(pool)
	facts, err := repo.FindingFacts(e.ctx, e.f.OrgA, ids)
	if err != nil {
		t.Fatal(err)
	}
	if n := counter.n.Load(); n != 1 {
		t.Errorf("queries on security_finding for 500 CIs: %d, want 1", n)
	}
	if f := facts[ids[0]]; f.MaxSeverityRank != 4 || f.OpenCount["vulnerability"] != 2 {
		t.Errorf("facts of CI 0: %+v, want critical and 2 vulnerabilities", f)
	}
	if f := facts[ids[1]]; f.MaxSeverityRank != 2 || f.OpenCount["missing_patch"] != 1 {
		t.Errorf("facts of CI 1: %+v", f)
	}
	if _, ok := facts[ids[2]]; ok {
		t.Error("CI without findings has facts")
	}

	if err = repo.CreateRule(e.ctx, &compliance.Rule{OrganizationID: e.f.OrgA, Name: "wp1125-no-critical", Severity: "high", Category: "ISO27001", Active: true,
		Expression: compliance.JSONMap{"op": "lt", "field": "finding.max_open_severity_rank", "value": 4}}); err != nil {
		t.Fatal(err)
	}
	counter.n.Store(0)
	resp, err := compliance.NewEvaluator(repo, ci.NewPGRepository(pool)).Evaluate(e.ctx, e.f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	// One page of 500 CIs since WP-1126.
	if n := counter.n.Load(); n != 1 {
		t.Errorf("queries on security_finding while evaluating 500 CIs: %d, want 1 (one per page of 500)", n)
	}
	if resp.Overall.Failed != 1 || resp.Overall.Passed != 499 {
		t.Errorf("score: %+v, want 1 failed (CI 0) and 499 passed", resp.Overall)
	}
}
