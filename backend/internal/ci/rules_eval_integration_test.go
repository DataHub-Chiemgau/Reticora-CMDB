package ci_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestCatalogRulesOnWriteAndDiscovery covers MET-10 (WP-257) against
// PostgreSQL: the active catalog rules (metamodel_rule, WP-716) of a CI
// are resolved over the type chain, the sets of its types and its
// template versions (citype.ResolveRules, in that order) and evaluated
// with the attribute rules:
//   - a required rule of the parent type is inherited by the subtype and
//     applies while its condition holds: POST /cis is 422 with field,
//     rule, code required and the rule's message;
//   - a validation rule of a set of the subtype refuses a value (code
//     pattern), also though a visible rule of the parent type would hide
//     the field: the subtype's attribute definition makes it required, so
//     visible does not hide it;
//   - a validation rule of an assigned template version applies on POST
//     (the template list of the request) and on PATCH (the stored
//     ci_template), 422 with code range.max;
//   - PATCH /cis/{id} is 422 when it breaks a rule; bulk set_status
//     reports the CI that breaks a rule with code 422;
//   - discovery (InvalidObserved, ObservedRules) drops an observed value
//     a validation rule refuses, for a new and for a stored CI; required
//     rules do not apply to observations.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCatalogRulesOnWriteAndDiscovery(t *testing.T) {
	f := scopetest.Seed(t, "e5")
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: f.User, OrganizationID: f.OrgA,
		Type: identity.PrincipalTypeUser, Permissions: []identity.Permission{identity.PermCIRead, identity.PermCIWrite}})

	types := citype.NewPGRepository(f.App)
	repo := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox())
	svc := ci.NewService(repo).WithFieldResolver(types)
	mux := chi.NewRouter()
	// The field policy source of the server router (WP-265).
	mux.Use(fieldPolicySource())
	ci.NewHandler(svc).RegisterRoutes(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	// refused asserts a 422 whose violations include the one of field with
	// rule and code, and returns it.
	refused := func(step string, w *httptest.ResponseRecorder, field, rule, code string) ci.Violation {
		t.Helper()
		var body struct {
			Violations []ci.Violation `json:"violations"`
		}
		if w.Code != http.StatusUnprocessableEntity || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("%s: %d %s, want 422", step, w.Code, w.Body.String())
		}
		for _, v := range body.Violations {
			if v.Field == field && v.Rule == rule && v.Code == code {
				return v
			}
		}
		t.Fatalf("%s: %s, want the violation of %s by rule %q with code %q", step, w.Body.String(), field, rule, code)
		return ci.Violation{}
	}
	created := func(step string, w *httptest.ResponseRecorder) string {
		t.Helper()
		var item ci.Item
		if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &item) != nil {
			t.Fatalf("%s: %d %s, want 201", step, w.Code, w.Body.String())
		}
		return item.ID
	}

	// The type chain parent → child, a set of the child and a template of
	// the child.
	parent := &citype.Type{OrganizationID: f.OrgA, Key: "wp257-parent", Name: "wp257-parent", IsObservable: true}
	if err := types.Create(ctx, parent); err != nil {
		t.Fatalf("parent type: %v", err)
	}
	child := &citype.Type{OrganizationID: f.OrgA, Key: "wp257-child", Name: "wp257-child", IsObservable: true, ParentTypeID: parent.ID}
	if err := types.Create(ctx, child); err != nil {
		t.Fatalf("child type: %v", err)
	}
	serial := &citype.AttributeDefinition{CITypeID: child.ID, Key: "wp257_serial", DataType: "text", Required: true}
	if err := types.CreateAttributeDefinition(ctx, f.OrgA, serial); err != nil {
		t.Fatalf("definition: %v", err)
	}
	set := &citype.AttributeSet{Key: "wp257-set", DisplayName: citype.LocalizedText{"en": "WP-257 set"}, IsActive: true}
	if err := types.CreateAttributeSet(ctx, f.OrgA, set); err != nil {
		t.Fatalf("set: %v", err)
	}
	if _, err := types.SetTypeAttributeSets(ctx, f.OrgA, child.ID, []citype.AttributeSetAssignment{{AttributeSetID: set.ID}}); err != nil {
		t.Fatalf("assign set: %v", err)
	}
	tpl := citype.Template{Key: "wp257-template", CITypeID: child.ID, IsActive: true}
	if err := tpl.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := types.CreateTemplate(ctx, f.OrgA, &tpl); err != nil {
		t.Fatalf("template: %v", err)
	}
	version, err := types.GetTemplateVersion(ctx, f.OrgA, tpl.ID, 1)
	if err != nil {
		t.Fatalf("template version: %v", err)
	}
	rule := func(in citype.RuleInput) {
		t.Helper()
		if _, rerr := types.CreateRule(ctx, f.OrgA, &in); rerr != nil {
			t.Fatalf("rule %s: %v", in.Key, rerr)
		}
	}
	physical := json.RawMessage(`{"op":"eq","field":"wp257_kind","value":"physical"}`)
	rule(citype.RuleInput{Key: "wp257-owner", AttributeKey: "wp257_owner", Kind: citype.RuleKindRequired, CITypeID: parent.ID,
		Condition: physical, Message: json.RawMessage(`{"de":"Verantwortlicher fehlt","en":"owner missing"}`)})
	rule(citype.RuleInput{Key: "wp257-serial-visible", AttributeKey: "wp257_serial", Kind: citype.RuleKindVisible, CITypeID: parent.ID,
		Condition: json.RawMessage(`{"op":"eq","field":"wp257_kind","value":"never"}`)})
	rule(citype.RuleInput{Key: "wp257-serial-form", AttributeKey: "wp257_serial", Kind: citype.RuleKindValidation, AttributeSetID: set.ID,
		AST: json.RawMessage(`{"op":"pattern","pattern":"^SN-[0-9]+$"}`)})
	rule(citype.RuleInput{Key: "wp257-rack", AttributeKey: "wp257_rack", Kind: citype.RuleKindValidation, TemplateVersionID: version.ID,
		AST: json.RawMessage(`{"op":"range","max":42}`)})
	// An inactive rule does not apply.
	off := false
	rule(citype.RuleInput{Key: "wp257-inactive", AttributeKey: "wp257_owner", Kind: citype.RuleKindRequired, CITypeID: child.ID, IsActive: &off})
	if _, err = types.PublishTemplateVersion(ctx, f.OrgA, tpl.ID, 1); err != nil {
		t.Fatalf("publish: %v", err)
	}

	body := func(name, attrs string, templates ...string) string {
		out := `{"ci_type_id":"` + child.ID + `","name":"` + name + `","client_id":"` + f.Client1 + `","attributes":` + attrs
		if len(templates) > 0 {
			out += `,"templates":[{"template_id":"` + strings.Join(templates, `"},{"template_id":"`) + `"}]`
		}
		return out + "}"
	}

	// The rule of the parent type is inherited and applies while its
	// condition holds; the violation carries rule, code and message.
	v := refused("POST without the owner", call(http.MethodPost, "/api/v1/cis",
		body("wp257-a", `{"wp257_kind":"physical","wp257_serial":"SN-1"}`)), "wp257_owner", "wp257-owner", "required")
	if v.Message == nil || (*v.Message)["de"] != "Verantwortlicher fehlt" || (*v.Message)["en"] != "owner missing" {
		t.Errorf("violation message %v, want the rule's message", v.Message)
	}
	// The set rule applies; the visible rule of the parent type does not
	// hide the field the subtype requires.
	refused("POST with an invalid serial", call(http.MethodPost, "/api/v1/cis",
		body("wp257-a", `{"wp257_kind":"physical","wp257_owner":"ops","wp257_serial":"bad"}`)), "wp257_serial", "wp257-serial-form", "pattern")
	w := call(http.MethodPost, "/api/v1/cis", body("wp257-a", `{"wp257_kind":"physical","wp257_owner":"ops"}`))
	refused("POST without the required serial", w, "wp257_serial", "", "")
	plain := created("POST valid", call(http.MethodPost, "/api/v1/cis", body("wp257-a", `{"wp257_kind":"physical","wp257_owner":"ops","wp257_serial":"SN-1"}`)))
	// The rule of the template version applies to the template list of
	// the request.
	refused("POST with the template and a rack out of range", call(http.MethodPost, "/api/v1/cis",
		body("wp257-b", `{"wp257_kind":"virtual","wp257_serial":"SN-2","wp257_rack":99}`, tpl.ID)), "wp257_rack", "wp257-rack", "range.max")
	templated := created("POST with the template", call(http.MethodPost, "/api/v1/cis",
		body("wp257-b", `{"wp257_kind":"virtual","wp257_serial":"SN-2","wp257_rack":10}`, tpl.ID)))

	// The resolution order: type chain, sets, template versions.
	resolved, err := types.ResolveRules(ctx, f.OrgA, child.ID, templated)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for i := range resolved {
		keys = append(keys, resolved[i].Key)
	}
	if got := strings.Join(keys, ","); got != "wp257-owner,wp257-serial-visible,wp257-serial-form,wp257-rack" {
		t.Errorf("ResolveRules = %s, want the type chain, the set and the template version in order", got)
	}

	// PATCH: the stored template assignment and the type rule apply.
	refused("PATCH a rack out of range", call(http.MethodPatch, "/api/v1/cis/"+templated, `{"attributes":{"wp257_rack":50}}`),
		"wp257_rack", "wp257-rack", "range.max")
	refused("PATCH removing the owner", call(http.MethodPatch, "/api/v1/cis/"+plain, `{"attributes":{"wp257_owner":""}}`),
		"wp257_owner", "wp257-owner", "required")
	if w = call(http.MethodPatch, "/api/v1/cis/"+plain, `{"attributes":{"wp257_kind":"virtual","wp257_owner":""}}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH with a condition that no longer holds: %d %s", w.Code, w.Body.String())
	}

	// Discovery drops the values a validation rule refuses, for a new CI
	// (once per type) and for a stored one; required does not apply.
	observed := map[string]any{"wp257_kind": "physical", "wp257_serial": "bad", "wp257_rack": 99}
	if got, oerr := svc.InvalidObserved(ctx, f.OrgA, child.ID, "", observed); oerr != nil || strings.Join(got, ",") != "wp257_serial" {
		t.Errorf("InvalidObserved of a new CI = %v, %v; want wp257_serial", got, oerr)
	}
	check, err := svc.ObservedRules(ctx, f.OrgA, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := check(observed); strings.Join(got, ",") != "wp257_serial" {
		t.Errorf("ObservedRules = %v, want wp257_serial", got)
	}
	if got, oerr := svc.InvalidObserved(ctx, f.OrgA, child.ID, templated, observed); oerr != nil || strings.Join(got, ",") != "wp257_rack,wp257_serial" {
		t.Errorf("InvalidObserved of the templated CI = %v, %v; want wp257_rack,wp257_serial", got, oerr)
	}

	// Bulk set_status reports the CI that breaks a rule with code 422.
	rule(citype.RuleInput{Key: "wp257-contact", AttributeKey: "wp257_contact", Kind: citype.RuleKindRequired, CITypeID: child.ID})
	w = call(http.MethodPost, "/api/v1/cis/bulk", `{"action":"set_status","ids":["`+plain+`"],"status":"maintenance"}`)
	var result ci.BulkResult
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatalf("bulk set_status: %d %s", w.Code, w.Body.String())
	}
	if result.Failed != 1 || len(result.Items) != 1 || result.Items[0].Code != http.StatusUnprocessableEntity ||
		!strings.Contains(result.Items[0].Error, "wp257_contact") {
		t.Errorf("bulk set_status: %+v, want the CI failed with 422 naming wp257_contact", result)
	}
	var status string
	if err = f.Admin.QueryRow(context.Background(), `SELECT status FROM ci WHERE id = $1`, plain).Scan(&status); err != nil || status == "maintenance" {
		t.Errorf("status after the refused bulk change: %q (%v)", status, err)
	}
}
