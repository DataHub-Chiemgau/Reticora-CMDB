package citype_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

type rulePage struct {
	Data       []citype.MetamodelRule `json:"data"`
	Total      int                    `json:"total"`
	NextCursor string                 `json:"next_cursor"`
}

func (a *setAPI) createRule(org, body string) citype.MetamodelRule {
	a.t.Helper()
	var rule citype.MetamodelRule
	a.must(http.StatusCreated, org, http.MethodPost, "/api/v1/rules", body, &rule)
	return rule
}

func (a *setAPI) templateVersion(org, templateID string, version int) citype.TemplateVersion {
	a.t.Helper()
	var v citype.TemplateVersion
	a.must(http.StatusOK, org, http.MethodGet, versionPath(templateID, version), "", &v)
	return v
}

// isNull reports whether a decoded JSON field is absent or null.
func isNull(raw []byte) bool {
	return len(raw) == 0 || string(raw) == "null"
}

func ruleKeys(p rulePage) []string {
	keys := make([]string, 0, len(p.Data))
	for _, r := range p.Data {
		keys = append(keys, r.Key)
	}
	return keys
}

// TestRuleCatalogAgainstPostgres covers API-06 and AUD-07 (WP-716, MET-45):
// CRUD of catalog rules on a CI type, an attribute set and a template
// version with the filters and cursor pagination of GET /api/v1/rules;
// key and target immutable (422), a taken key 409, version rises with each
// change. An invalid condition or ast is 422 with the parser's path, ast is
// required exactly for a validation, exactly one target is required, and a
// visible rule on a statically required attribute is 422. A system rule
// is readable but read-only (403). Organization B sees nothing of
// organization A and cannot bind to its targets (422). A new template
// version copies the rules of the previous one with their keys; rules of a
// published version are immutable (409). Every change is audited as
// metamodel_rule.* with before and after, actor and request id.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRuleCatalogAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "c9")
	a := newSetAPI(t, f)
	bg := context.Background()

	typ := a.createType(f.OrgA, "wp716-type")
	foreignType := a.createType(f.OrgB, "wp716-foreign")
	a.definition(f.OrgA, `{"ci_type_id":"`+typ.ID+`","key":"wp716_serial","data_type":"text","required":true}`)
	a.definition(f.OrgA, `{"ci_type_id":"`+typ.ID+`","key":"wp716_note","data_type":"text"}`)
	set := a.createSet(f.OrgA, "wp716-set")
	tpl := a.createTemplate(f.OrgA, typ.ID, "wp716-tpl")
	v1 := a.templateVersion(f.OrgA, tpl.ID, 1)

	// Create on each target.
	var typeRules []citype.MetamodelRule
	for i := 0; i < 3; i++ {
		typeRules = append(typeRules, a.createRule(f.OrgA, `{"key":"wp716-type-`+strconv.Itoa(i)+`","attribute_key":"wp716_note",
			"kind":"required","ci_type_id":"`+typ.ID+`","condition":{"op":"eq","field":"status","value":"active"},
			"message":{"de":"Pflicht","en":"Required"}}`))
	}
	rule := typeRules[0]
	if rule.OrganizationID != f.OrgA || rule.IsSystem || !rule.IsActive || rule.Version != 1 || rule.CITypeID != typ.ID ||
		rule.Kind != citype.RuleKindRequired || !isNull(rule.AST) || rule.Message["en"] != "Required" ||
		!strings.Contains(string(rule.Condition), `"field":"status"`) {
		t.Fatalf("created rule: %+v (condition %s)", rule, rule.Condition)
	}
	setRule := a.createRule(f.OrgA, `{"key":"wp716-set","attribute_key":"wp716_vlan","kind":"readonly","attribute_set_id":"`+set.ID+`"}`)
	tplRule := a.createRule(f.OrgA, `{"key":"wp716-tpl","attribute_key":"wp716_note","kind":"validation","template_version_id":"`+v1.ID+`",
		"condition":{"op":"any","rules":[{"op":"empty","field":"wp716_serial"}]},"ast":{"op":"enum","values":["a","b"]}}`)
	if setRule.AttributeSetID != set.ID || !isNull(setRule.Condition) || tplRule.TemplateVersionID != v1.ID ||
		!strings.Contains(string(tplRule.AST), `"enum"`) {
		t.Fatalf("set rule %+v, template rule %+v", setRule, tplRule)
	}
	// A template rule key is unique per version, not per organization.
	a.createRule(f.OrgA, `{"key":"wp716-type-1","attribute_key":"wp716_note","kind":"readonly","template_version_id":"`+v1.ID+`"}`)

	// Refusals of the request.
	for _, c := range []struct {
		name, body string
		status     int
		field      string
	}{
		{"taken key", `{"key":"wp716-type-1","attribute_key":"x","kind":"readonly","attribute_set_id":"` + set.ID + `"}`, http.StatusConflict, "key"},
		{"no target", `{"key":"wp716-x","attribute_key":"x","kind":"readonly"}`, http.StatusUnprocessableEntity, "ci_type_id"},
		{"two targets", `{"key":"wp716-x","attribute_key":"x","kind":"readonly","ci_type_id":"` + typ.ID + `","attribute_set_id":"` + set.ID + `"}`,
			http.StatusUnprocessableEntity, "ci_type_id"},
		{"unknown kind", `{"key":"wp716-x","attribute_key":"x","kind":"hidden","ci_type_id":"` + typ.ID + `"}`, http.StatusUnprocessableEntity, "kind"},
		{"validation without ast", `{"key":"wp716-x","attribute_key":"x","kind":"validation","ci_type_id":"` + typ.ID + `"}`,
			http.StatusUnprocessableEntity, "ast"},
		{"ast without validation", `{"key":"wp716-x","attribute_key":"x","kind":"required","ci_type_id":"` + typ.ID + `","ast":{"op":"required"}}`,
			http.StatusUnprocessableEntity, "ast"},
		{"invalid condition", `{"key":"wp716-x","attribute_key":"x","kind":"readonly","ci_type_id":"` + typ.ID +
			`","condition":{"op":"all","rules":[{"op":"eq"}]}}`, http.StatusUnprocessableEntity, "condition.rules[0].field"},
		{"value rule as condition", `{"key":"wp716-x","attribute_key":"x","kind":"readonly","ci_type_id":"` + typ.ID +
			`","condition":{"op":"required"}}`, http.StatusUnprocessableEntity, "condition.op"},
		{"invalid ast", `{"key":"wp716-x","attribute_key":"x","kind":"validation","ci_type_id":"` + typ.ID +
			`","ast":{"op":"all","rules":[{"op":"pattern","pattern":""}]}}`, http.StatusUnprocessableEntity, "ast.rules[0].pattern"},
		{"invalid message", `{"key":"wp716-x","attribute_key":"x","kind":"readonly","ci_type_id":"` + typ.ID + `","message":{"fr":"x"}}`,
			http.StatusUnprocessableEntity, "message"},
		{"visible on a required attribute", `{"key":"wp716-x","attribute_key":"wp716_serial","kind":"visible","ci_type_id":"` + typ.ID + `"}`,
			http.StatusUnprocessableEntity, "attribute_key"},
		{"unknown type", `{"key":"wp716-x","attribute_key":"x","kind":"readonly","ci_type_id":"` + f.ID() + `"}`,
			http.StatusUnprocessableEntity, "ci_type_id"},
	} {
		w := a.call(f.OrgA, http.MethodPost, "/api/v1/rules", c.body)
		if p := problemOf(w); w.Code != c.status || p.Field != c.field {
			t.Errorf("%s: %d %s, want %d on %s", c.name, w.Code, w.Body.String(), c.status, c.field)
		}
	}
	// visible on an optional attribute is allowed.
	visible := a.createRule(f.OrgA, `{"key":"wp716-visible","attribute_key":"wp716_note","kind":"visible","ci_type_id":"`+typ.ID+`",
		"condition":{"op":"not_empty","field":"wp716_serial"}}`)

	// List with filters and cursor.
	seen := map[string]bool{}
	path := "/api/v1/rules?limit=2&kind=required&ci_type_id=" + typ.ID
	for pages := 0; path != ""; pages++ {
		if pages > 10 {
			t.Fatal("pagination does not end")
		}
		var page rulePage
		a.must(http.StatusOK, f.OrgA, http.MethodGet, path, "", &page)
		if len(page.Data) > 2 || page.Total != 3 {
			t.Fatalf("page of %d rules (total %d), limit 2", len(page.Data), page.Total)
		}
		for _, r := range page.Data {
			if seen[r.ID] || r.CITypeID != typ.ID || r.Kind != citype.RuleKindRequired {
				t.Errorf("listed rule %+v (seen %v)", r, seen[r.ID])
			}
			seen[r.ID] = true
		}
		path = ""
		if page.NextCursor != "" {
			path = "/api/v1/rules?limit=2&kind=required&ci_type_id=" + typ.ID + "&cursor=" + page.NextCursor
		}
	}
	if len(seen) != 3 {
		t.Errorf("listed %d type rules, want 3", len(seen))
	}
	for query, want := range map[string]string{
		"attribute_set_id=" + set.ID:   "wp716-set",
		"template_version_id=" + v1.ID: "wp716-type-1 wp716-tpl",
		"kind=visible":                 "wp716-visible",
		"attribute_key=wp716_vlan":     "wp716-set",
	} {
		var page rulePage
		a.must(http.StatusOK, f.OrgA, http.MethodGet, "/api/v1/rules?"+query, "", &page)
		keys := ruleKeys(page)
		if strings.Join(keys, " ") != want {
			t.Errorf("rules with %s: %v, want %s", query, keys, want)
		}
	}
	a.must(http.StatusBadRequest, f.OrgA, http.MethodGet, "/api/v1/rules?cursor=forged", "", nil)

	// Get, patch and immutable fields.
	var got citype.MetamodelRule
	a.must(http.StatusOK, f.OrgA, http.MethodGet, "/api/v1/rules/"+rule.ID, "", &got)
	if got.Key != rule.Key || got.Version != 1 {
		t.Errorf("get: %+v", got)
	}
	a.must(http.StatusNotFound, f.OrgA, http.MethodGet, "/api/v1/rules/"+f.ID(), "", nil)
	a.must(http.StatusNotFound, f.OrgA, http.MethodGet, "/api/v1/rules/not-a-uuid", "", nil)
	var updated citype.MetamodelRule
	a.must(http.StatusOK, f.OrgA, http.MethodPatch, "/api/v1/rules/"+rule.ID,
		`{"kind":"validation","ast":{"op":"pattern","pattern":"^[A-Z]+$"},"condition":null,"message":null}`, &updated)
	if updated.Version != 2 || updated.Kind != citype.RuleKindValidation || !isNull(updated.Condition) || updated.Message != nil ||
		!strings.Contains(string(updated.AST), "pattern") {
		t.Errorf("patched rule: %+v", updated)
	}
	a.must(http.StatusOK, f.OrgA, http.MethodPatch, "/api/v1/rules/"+rule.ID, `{"kind":"readonly","is_active":false}`, &updated)
	if updated.Version != 3 || !isNull(updated.AST) || updated.IsActive {
		t.Errorf("kind changed back: %+v", updated)
	}
	for field, body := range map[string]string{
		"key":                 `{"key":"other"}`,
		"ci_type_id":          `{"ci_type_id":"` + typ.ID + `"}`,
		"template_version_id": `{"template_version_id":"` + v1.ID + `"}`,
		"ast":                 `{"kind":"validation"}`,
		"condition.op":        `{"condition":{"op":"nope"}}`,
	} {
		w := a.call(f.OrgA, http.MethodPatch, "/api/v1/rules/"+rule.ID, body)
		if p := problemOf(w); w.Code != http.StatusUnprocessableEntity || p.Field != field {
			t.Errorf("patch %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	w := a.call(f.OrgA, http.MethodPatch, "/api/v1/rules/"+visible.ID, `{"attribute_key":"wp716_serial"}`)
	if p := problemOf(w); w.Code != http.StatusUnprocessableEntity || p.Field != "attribute_key" {
		t.Errorf("visible moved onto a required attribute: %d %s", w.Code, w.Body.String())
	}

	// System rules are readable and read-only.
	sysType := systemTypeID(t, f, "server")
	var sysID string
	if err := f.Admin.QueryRow(bg, `INSERT INTO metamodel_rule (organization_id, key, attribute_key, kind, ci_type_id)
		VALUES (NULL, 'wp716-system', 'serial_number', 'readonly', $1) RETURNING id::text`, sysType).Scan(&sysID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.Admin.Exec(bg, `DELETE FROM metamodel_rule WHERE id = $1`, sysID) })
	var sys citype.MetamodelRule
	a.must(http.StatusOK, f.OrgB, http.MethodGet, "/api/v1/rules/"+sysID, "", &sys)
	if !sys.IsSystem || sys.OrganizationID != "" {
		t.Errorf("system rule: %+v", sys)
	}
	a.must(http.StatusForbidden, f.OrgA, http.MethodPatch, "/api/v1/rules/"+sysID, `{"is_active":false}`, nil)
	a.must(http.StatusForbidden, f.OrgA, http.MethodDelete, "/api/v1/rules/"+sysID, "", nil)
	// An organization rule may target a system type.
	onSystem := a.createRule(f.OrgA, `{"key":"wp716-on-system","attribute_key":"wp716_note","kind":"readonly","ci_type_id":"`+sysType+`"}`)

	// Tenant separation: org B sees nothing of org A and cannot bind to it.
	var pageB rulePage
	a.must(http.StatusOK, f.OrgB, http.MethodGet, "/api/v1/rules?limit=200", "", &pageB)
	for _, r := range pageB.Data {
		if r.OrganizationID == f.OrgA {
			t.Errorf("org B lists rule %s of org A", r.Key)
		}
	}
	a.must(http.StatusNotFound, f.OrgB, http.MethodGet, "/api/v1/rules/"+rule.ID, "", nil)
	a.must(http.StatusNotFound, f.OrgB, http.MethodPatch, "/api/v1/rules/"+rule.ID, `{"is_active":true}`, nil)
	a.must(http.StatusNotFound, f.OrgB, http.MethodDelete, "/api/v1/rules/"+rule.ID, "", nil)
	for field, id := range map[string]string{"ci_type_id": typ.ID, "attribute_set_id": set.ID, "template_version_id": v1.ID} {
		w = a.call(f.OrgB, http.MethodPost, "/api/v1/rules", `{"key":"wp716-b","attribute_key":"x","kind":"readonly","`+field+`":"`+id+`"}`)
		if p := problemOf(w); w.Code != http.StatusUnprocessableEntity || p.Field != field {
			t.Errorf("org B binds to %s of org A: %d %s", field, w.Code, w.Body.String())
		}
	}
	a.createRule(f.OrgB, `{"key":"wp716-type-0","attribute_key":"x","kind":"readonly","ci_type_id":"`+foreignType.ID+`"}`)

	// Template versions: publishing v1 freezes its rules; v2 copies them.
	a.must(http.StatusOK, f.OrgA, http.MethodPost, versionPath(tpl.ID, 1)+"/publish", `{}`, nil)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/rules", `{"key":"wp716-late","attribute_key":"x","kind":"readonly","template_version_id":"` + v1.ID + `"}`},
		{http.MethodPatch, "/api/v1/rules/" + tplRule.ID, `{"is_active":false}`},
		{http.MethodDelete, "/api/v1/rules/" + tplRule.ID, ""},
	} {
		if w = a.call(f.OrgA, c.method, c.path, c.body); w.Code != http.StatusConflict {
			t.Errorf("%s %s on a published version: %d %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
	var v2 citype.TemplateVersion
	a.must(http.StatusCreated, f.OrgA, http.MethodPost, "/api/v1/templates/"+tpl.ID+"/versions", `{}`, &v2)
	var copied rulePage
	a.must(http.StatusOK, f.OrgA, http.MethodGet, "/api/v1/rules?template_version_id="+v2.ID, "", &copied)
	keys := ruleKeys(copied)
	slices.Sort(keys)
	if strings.Join(keys, " ") != "wp716-tpl wp716-type-1" {
		t.Fatalf("rules of version 2: %v", keys)
	}
	for _, r := range copied.Data {
		if r.ID == tplRule.ID || r.OrganizationID != f.OrgA || r.Version != 1 {
			t.Errorf("copied rule: %+v", r)
		}
		if r.Key == tplRule.Key && (string(r.AST) != string(tplRule.AST) || string(r.Condition) != string(tplRule.Condition)) {
			t.Errorf("copied rule %s differs: %+v", r.Key, r)
		}
	}
	for _, r := range copied.Data {
		if r.Key == tplRule.Key {
			a.must(http.StatusOK, f.OrgA, http.MethodPatch, "/api/v1/rules/"+r.ID, `{"is_active":false}`, nil)
		}
	}
	active, err := a.repo.ActiveRules(a.ctx(f.OrgA), f.OrgA, citype.RuleTargets{
		CITypeIDs: []string{typ.ID, sysType}, AttributeSetIDs: []string{set.ID}, TemplateVersionIDs: []string{v2.ID}})
	if err != nil {
		t.Fatal(err)
	}
	var activeKeys []string
	for _, r := range active {
		activeKeys = append(activeKeys, r.Key)
	}
	// rule (wp716-type-0) and the copy of wp716-tpl are inactive.
	if strings.Join(activeKeys, " ") != "wp716-on-system wp716-set wp716-system wp716-type-1 wp716-type-1 wp716-type-2 wp716-visible" {
		t.Errorf("active rules: %v", activeKeys)
	}

	// Delete.
	a.must(http.StatusNoContent, f.OrgA, http.MethodDelete, "/api/v1/rules/"+setRule.ID, "", nil)
	a.must(http.StatusNotFound, f.OrgA, http.MethodGet, "/api/v1/rules/"+setRule.ID, "", nil)
	a.must(http.StatusNoContent, f.OrgA, http.MethodDelete, "/api/v1/rules/"+onSystem.ID, "", nil)

	// Audit: metamodel_rule.* with before and after, actor and request id;
	// the copies of the new version have no entry of their own.
	rows, err := f.Admin.Query(bg, `SELECT action, COALESCE(resource_id::text, ''), COALESCE(actor_id::text, ''),
		COALESCE(request_id, ''), changes FROM audit_log
		WHERE organization_id = $1 AND resource_type = 'metamodel_rule' ORDER BY seq`, f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	counts := map[string]int{}
	byResource := map[string][]map[string]any{}
	for rows.Next() {
		var action, resource, actor, request string
		var changes map[string]any
		if err = rows.Scan(&action, &resource, &actor, &request, &changes); err != nil {
			t.Fatal(err)
		}
		counts[action]++
		if actor != a.user || !strings.HasPrefix(request, "wp250-") {
			t.Errorf("%s: actor %s, request %s", action, actor, request)
		}
		changes["action"] = action
		byResource[resource] = append(byResource[resource], changes)
	}
	// 3 type rules, set, template, template key twin, visible, on-system.
	if counts["metamodel_rule.created"] != 8 || counts["metamodel_rule.updated"] != 3 || counts["metamodel_rule.deleted"] != 2 {
		t.Errorf("audit counts: %v", counts)
	}
	if entries := byResource[rule.ID]; len(entries) != 3 || entries[0]["before"] != nil || entries[0]["after"] == nil {
		t.Errorf("audit of %s: %+v", rule.ID, entries)
	} else {
		before, _ := entries[1]["before"].(map[string]any)
		after, _ := entries[1]["after"].(map[string]any)
		if before["kind"] != "required" || after["kind"] != "validation" || after["version"] != float64(2) {
			t.Errorf("rule updated: before %+v after %+v", before, after)
		}
	}
	if entries := byResource[setRule.ID]; len(entries) != 2 || entries[1]["after"] != nil || entries[1]["before"] == nil {
		t.Errorf("audit of the deleted set rule: %+v", entries)
	}
	if result, verr := audit.Verify(bg, f.App, f.OrgA); verr != nil || !result.Intact {
		t.Errorf("audit chain: %+v %v", result, verr)
	}
}

// TestRuleBindingTriggerAgainstPostgres covers the binding trigger of
// migration 000207 (WP-716, AUD-07): past the application check, a rule of
// organization B on a target of organization A is refused with 23503, and
// rows of A stay invisible to B under row level security.
func TestRuleBindingTriggerAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "c9")
	a := newSetAPI(t, f)
	typ := a.createType(f.OrgA, "wp716-bind")
	a.createRule(f.OrgA, `{"key":"wp716-bind","attribute_key":"x","kind":"readonly","ci_type_id":"`+typ.ID+`"}`)
	tx, err := f.App.Begin(f.OrgCtx(f.OrgB))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	ctx := context.Background()
	if _, err = tx.Exec(ctx, `SELECT set_config('app.org_id', $1, true)`, f.OrgB); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM metamodel_rule WHERE ci_type_id = $1`, typ.ID).Scan(&n); err != nil || n != 0 {
		t.Errorf("org B sees %d rules of org A: %v", n, err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO metamodel_rule (organization_id, key, attribute_key, kind, ci_type_id)
		VALUES ($1, 'wp716-steal', 'x', 'readonly', $2)`, f.OrgB, typ.ID)
	if err == nil || !strings.Contains(err.Error(), "23503") {
		t.Errorf("org B rule on a type of org A: %v", err)
	}
}

// TestRuleCatalogWithoutDatabase covers WP-716: in memory mode the rule
// catalog answers 503 like the attribute definitions.
func TestRuleCatalogWithoutDatabase(t *testing.T) {
	mux := chi.NewRouter()
	citype.NewHandler(citype.NewMemoryRepository()).RegisterRoutes(mux)
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "c0de0000-0000-4000-8000-000000000001"})
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/rules", ""},
		{http.MethodPost, "/api/v1/rules", `{"key":"x","attribute_key":"x","kind":"readonly"}`},
		{http.MethodGet, "/api/v1/rules/c0de0000-0000-4000-8000-000000000002", ""},
		{http.MethodPatch, "/api/v1/rules/c0de0000-0000-4000-8000-000000000002", `{}`},
		{http.MethodDelete, "/api/v1/rules/c0de0000-0000-4000-8000-000000000002", ""},
	} {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s in memory mode: %d %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
}
