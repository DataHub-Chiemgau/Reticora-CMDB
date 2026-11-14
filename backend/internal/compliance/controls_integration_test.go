package compliance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
)

// The control catalog of ISO 27001, NIS2 and KRITIS and the assignment of
// rules to controls (SEC-04, WP-1128, E-238): the seeded catalog without
// normative text, read-only for the application, the replacement of a
// rule's controls with 422 and 404, the coverage gaps and the composite
// foreign key across organizations.
//
// They run only with TEST_DATABASE_URL (scopetest.Seed).
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestControlCatalog|TestRuleControl|TestCoverageListsControlsWithoutActiveRule' ./internal/compliance/

// controlIDs returns the catalog ids of standard by control_id.
func (e *evalEnv) controlIDs(t *testing.T, standard string) map[string]string {
	t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT control_id, id::text FROM compliance_control WHERE standard = $1`, standard)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var c, id string
		if err := rows.Scan(&c, &id); err != nil {
			t.Fatal(err)
		}
		out[c] = id
	}
	return out
}

// auditedPut sends PUT /api/v1/compliance/rules/{id}/controls behind the
// production route audit with its snapshot (middleware.AuditRoute).
func (e *evalEnv) auditedPut(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	inner := chi.NewRouter()
	NewHandler(e.repo, e.eval).RegisterRoutes(inner)
	const pattern = "/api/v1/compliance/rules/{id}/controls"
	r := chi.NewRouter()
	r.With(middleware.AuditRoute(http.MethodPut, pattern, audit.NewPGRouteAuditor(e.f.App), middleware.AuditIntent, "")).
		Method(http.MethodPut, pattern, inner)
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader([]byte(body))).WithContext(e.ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func (e *evalEnv) call(t *testing.T, ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	NewHandler(e.repo, e.eval).RegisterRoutes(r)
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body))).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// TestControlCatalogSeeded: 93 ISO controls, 10 NIS2 letters, 3 KRITIS
// entries; no field longer than 120 characters (no normative text).
func TestControlCatalogSeeded(t *testing.T) {
	e := newEvalEnv(t, "c7")
	for standard, want := range map[string]int{"iso27001": 93, "nis2": 10, "kritis": 3} {
		if n := e.count(t, `SELECT count(*) FROM compliance_control WHERE standard = $1`, standard); n != want {
			t.Errorf("%s: %d controls, want %d", standard, n, want)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM compliance_control WHERE length(title_de) > 120 OR length(title_en) > 120 OR length(control_id) > 120`); n != 0 {
		t.Errorf("%d controls with a field longer than 120 characters", n)
	}
	iso := e.controlIDs(t, "iso27001")
	for _, c := range []string{"5.1", "5.37", "6.8", "7.14", "8.34"} {
		if iso[c] == "" {
			t.Errorf("ISO control %s missing", c)
		}
	}
	if e.controlIDs(t, "kritis")["8a.1a"] == "" || e.controlIDs(t, "nis2")["21.2.j"] == "" {
		t.Error("KRITIS 8a.1a or NIS2 21.2.j missing")
	}
	rec := e.call(t, e.ctx, http.MethodGet, "/api/v1/compliance/controls?standard=nis2", "")
	var list struct {
		Data []Control `json:"data"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list.Data) != 10 || list.Data[0].ControlID != "21.2.a" {
		t.Errorf("GET controls nis2: %d %s", rec.Code, rec.Body.String())
	}
	for _, q := range []string{"", "?standard=sox"} {
		if rec := e.call(t, e.ctx, http.MethodGet, "/api/v1/compliance/controls"+q, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("GET controls%s: %d, want 400", q, rec.Code)
		}
	}
}

// TestControlCatalogReadOnlyForApp: the application role cannot write the
// catalog (42501).
func TestControlCatalogReadOnlyForApp(t *testing.T) {
	e := newEvalEnv(t, "c7")
	err := database.WithRequestTenant(e.ctx, e.f.App, e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO compliance_control (standard, control_id, title_de, title_en) VALUES ('nis2', 'x', 'x', 'x')`)
		return err
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Errorf("INSERT into the catalog as the app: %v, want 42501", err)
	}
}

// TestRuleControlMapping: PUT replaces the assignment, writes an audit
// entry through the route audit (snapshot register) and GET of the rule
// names the controls.
func TestRuleControlMapping(t *testing.T) {
	e := newEvalEnv(t, "c7")
	rule := e.rule(t, "mapped", JSONMap{"op": "not_empty", "field": "name"})
	iso := e.controlIDs(t, "iso27001")
	body := `{"control_ids":["` + iso["5.15"] + `","` + iso["8.8"] + `"]}`
	rec := e.auditedPut(t, "/api/v1/compliance/rules/"+rule.ID+"/controls", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT controls: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.auditedPut(t, "/api/v1/compliance/rules/"+rule.ID+"/controls", `{"control_ids":["`+iso["8.8"]+`"]}`)
	var got RuleControls
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || !slices.Equal(got.ControlIDs, []string{iso["8.8"]}) {
		t.Fatalf("replaced controls: %d %s", rec.Code, rec.Body.String())
	}
	// The route audit writes an entry per PUT whose values name the
	// controls of the rule before and after (snapshot register, AK4).
	rows, err := e.f.Admin.Query(context.Background(), `SELECT changes->'values' FROM audit_log WHERE organization_id = $1 AND resource_id = $2
		AND action = 'compliance.controls' ORDER BY timestamp`, e.f.OrgA, rule.ID)
	if err != nil {
		t.Fatal(err)
	}
	var entries [][]map[string]any
	for rows.Next() {
		var raw []byte
		var values []map[string]any
		if err = rows.Scan(&raw); err != nil || json.Unmarshal(raw, &values) != nil {
			t.Fatalf("audit entry: %s %v", raw, err)
		}
		entries = append(entries, values)
	}
	rows.Close()
	controlsOf := func(v any) []string {
		out := []string{}
		list, _ := v.([]any)
		for _, c := range list {
			s, _ := c.(string)
			out = append(out, s)
		}
		slices.Sort(out)
		return out
	}
	both := []string{iso["5.15"], iso["8.8"]}
	slices.Sort(both)
	if len(entries) != 2 {
		t.Fatalf("audit entries of the PUTs: %v, want 2", entries)
	}
	last := entries[1]
	if len(last) != 1 || last[0]["field"] != "control_ids" || !slices.Equal(controlsOf(last[0]["old"]), both) ||
		!slices.Equal(controlsOf(last[0]["new"]), []string{iso["8.8"]}) {
		t.Errorf("values of the second PUT: %v, want control_ids from both controls to 8.8", last)
	}
	// A body without control_ids (a misnamed key) is refused and clears
	// nothing; an explicit [] would.
	for _, b := range []string{`{}`, `{"control_ids":null}`, `{"controls":[]}`} {
		if rec = e.call(t, e.ctx, http.MethodPut, "/api/v1/compliance/rules/"+rule.ID+"/controls", b); rec.Code != http.StatusUnprocessableEntity &&
			rec.Code != http.StatusBadRequest {
			t.Errorf("PUT %s: %d %s, want 422 or 400", b, rec.Code, rec.Body.String())
		}
	}
	if ids, rerr := e.repo.RuleControlIDs(e.ctx, e.f.OrgA, rule.ID); rerr != nil || !slices.Equal(ids, []string{iso["8.8"]}) {
		t.Errorf("controls after the refused bodies: %v %v, want 8.8", ids, rerr)
	}
	rec = e.call(t, e.ctx, http.MethodGet, "/api/v1/compliance/rules/"+rule.ID, "")
	var detail struct {
		ID         string   `json:"id"`
		Name       string   `json:"name"`
		ControlIDs []string `json:"control_ids"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &detail) != nil || detail.ID != rule.ID || !slices.Equal(detail.ControlIDs, []string{iso["8.8"]}) {
		t.Errorf("GET rule: %d %s, want control_ids", rec.Code, rec.Body.String())
	}
	rec = e.call(t, e.ctx, http.MethodGet, "/api/v1/compliance/controls?standard=iso27001", "")
	var list struct {
		Data []Control `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	for _, c := range list.Data {
		want := 0
		if c.ControlID == "8.8" {
			want = 1
		}
		if c.RuleCount != want || c.ActiveRuleCount != want {
			t.Errorf("control %s: %d/%d rules, want %d", c.ControlID, c.RuleCount, c.ActiveRuleCount, want)
		}
	}
}

// TestRuleControlMappingRejectsUnknown: an unknown control is 422 with its
// index; the assignment stays.
func TestRuleControlMappingRejectsUnknown(t *testing.T) {
	e := newEvalEnv(t, "c7")
	rule := e.rule(t, "unknown", JSONMap{"op": "not_empty", "field": "name"})
	iso := e.controlIDs(t, "iso27001")
	unknown := e.f.ID()
	rec := e.call(t, e.ctx, http.MethodPut, "/api/v1/compliance/rules/"+rule.ID+"/controls", `{"control_ids":["`+iso["5.1"]+`","`+unknown+`"]}`)
	if rec.Code != http.StatusUnprocessableEntity || !bytes.Contains(rec.Body.Bytes(), []byte(`"field":"control_ids[1]"`)) {
		t.Errorf("unknown control: %d %s, want 422 on control_ids[1]", rec.Code, rec.Body.String())
	}
	if n := e.count(t, `SELECT count(*) FROM compliance_rule_control WHERE rule_id = $1`, rule.ID); n != 0 {
		t.Errorf("assignments after the refusal: %d, want 0", n)
	}
}

// TestRuleControlMappingForeignRule: a rule of another organization is 404.
func TestRuleControlMappingForeignRule(t *testing.T) {
	e := newEvalEnv(t, "c7")
	scopeB := database.OrgWideScope(e.f.OrgB, "")
	ctxB := database.ContextWithTenantScope(context.Background(), &scopeB)
	ruleB := &Rule{OrganizationID: e.f.OrgB, Name: "foreign", Severity: "low", Category: "hygiene", Expression: JSONMap{"op": "not_empty", "field": "name"}, Active: true}
	if err := e.repo.CreateRule(ctxB, ruleB); err != nil {
		t.Fatal(err)
	}
	iso := e.controlIDs(t, "iso27001")
	if rec := e.call(t, e.ctx, http.MethodPut, "/api/v1/compliance/rules/"+ruleB.ID+"/controls", `{"control_ids":["`+iso["5.1"]+`"]}`); rec.Code != http.StatusNotFound {
		t.Errorf("rule of organization B from A: %d %s, want 404", rec.Code, rec.Body.String())
	}
}

// TestCoverageListsControlsWithoutActiveRule: the coverage lists the
// controls without an active rule; a control with an inactive rule only
// stays a gap.
func TestCoverageListsControlsWithoutActiveRule(t *testing.T) {
	e := newEvalEnv(t, "c7")
	active := e.rule(t, "active", JSONMap{"op": "not_empty", "field": "name"})
	inactive := e.rule(t, "inactive", JSONMap{"op": "not_empty", "field": "name"})
	off := false
	if _, err := e.repo.UpdateRule(e.ctx, e.f.OrgA, inactive.ID, &UpdateRuleRequest{Active: &off}); err != nil {
		t.Fatal(err)
	}
	kritis := e.controlIDs(t, "kritis")
	if err := e.repo.ReplaceRuleControls(e.ctx, e.f.OrgA, active.ID, []string{kritis["8a.1"]}); err != nil {
		t.Fatal(err)
	}
	if err := e.repo.ReplaceRuleControls(e.ctx, e.f.OrgA, inactive.ID, []string{kritis["8a.1a"]}); err != nil {
		t.Fatal(err)
	}
	rec := e.call(t, e.ctx, http.MethodGet, "/api/v1/compliance/controls/coverage?standard=kritis", "")
	var list struct {
		Data []Control `json:"data"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil {
		t.Fatalf("coverage: %d %s", rec.Code, rec.Body.String())
	}
	var ids []string
	for _, c := range list.Data {
		ids = append(ids, c.ControlID)
	}
	if !slices.Equal(ids, []string{"8a.1a", "8a.3"}) {
		t.Errorf("gaps %v, want 8a.1a and 8a.3", ids)
	}
}

// TestRuleControlCrossOrgRejected: an assignment of organization A that
// names a rule of organization B fails on the composite foreign key
// (23503).
func TestRuleControlCrossOrgRejected(t *testing.T) {
	e := newEvalEnv(t, "c7")
	scopeB := database.OrgWideScope(e.f.OrgB, "")
	ctxB := database.ContextWithTenantScope(context.Background(), &scopeB)
	ruleB := &Rule{OrganizationID: e.f.OrgB, Name: "cross", Severity: "low", Category: "hygiene", Expression: JSONMap{"op": "not_empty", "field": "name"}, Active: true}
	if err := e.repo.CreateRule(ctxB, ruleB); err != nil {
		t.Fatal(err)
	}
	iso := e.controlIDs(t, "iso27001")
	_, err := e.f.Admin.Exec(context.Background(), `INSERT INTO compliance_rule_control (organization_id, rule_id, control_id) VALUES ($1, $2, $3)`,
		e.f.OrgA, ruleB.ID, iso["5.1"])
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Errorf("cross-organization assignment: %v, want 23503", err)
	}
}
