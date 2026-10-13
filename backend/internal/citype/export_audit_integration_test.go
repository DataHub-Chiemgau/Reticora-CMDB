package citype_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestMetamodelExportAndAudit covers MET-54 and AUD-07 (WP-134) against
// PostgreSQL: every metamodel change through the API — type create,
// update, deactivate, activate, clone, field upsert and delete, attribute
// definition create, change and delete — writes an audit entry in its
// transaction with before/after, actor, request and correlation id; the
// export holds the organization's and the system's types with their
// definitions and the global definitions, and nothing of another
// organization.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestMetamodelExportAndAudit(t *testing.T) {
	f := scopetest.Seed(t, "96")
	bg := context.Background()
	repo := citype.NewPGRepository(f.App)
	user := f.AppUser(t, f.OrgA, "met-admin")
	mux := chi.NewRouter()
	citype.NewHandler(repo).RegisterRoutes(mux)
	n := 0
	call := func(org, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		n++
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		scope := database.OrgWideScope(org, user)
		ctx := tenant.WithTenant(database.ContextWithTenantScope(req.Context(), &scope), tenant.TenantInfo{OrganizationID: org, UserID: user})
		ctx = audit.WithRequestInfo(ctx, audit.RequestInfo{RequestID: "req-" + string(rune('a'+n)), CorrelationID: "met-op", Source: audit.SourceUI})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(ctx))
		return w
	}
	decode := func(w *httptest.ResponseRecorder) map[string]any {
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}

	w := call(f.OrgA, http.MethodPost, "/api/v1/ci-types", `{"key":"wp134-probe","name":"WP-134 Probe",
		"fields":[{"name":"probe_class","data_type":"text"}]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create type: %d %s", w.Code, w.Body.String())
	}
	typeID := decode(w)["id"].(string)
	steps := []struct{ method, path, body string }{
		{http.MethodPatch, "/api/v1/ci-types/" + typeID, `{"name":"WP-134 Probe 2"}`},
		{http.MethodPost, "/api/v1/ci-types/" + typeID + "/deactivate", ""},
		{http.MethodPost, "/api/v1/ci-types/" + typeID + "/activate", ""},
		{http.MethodPut, "/api/v1/ci-types/" + typeID + "/fields", `{"name":"probe_range","data_type":"integer"}`},
		{http.MethodPut, "/api/v1/ci-types/" + typeID + "/fields", `{"name":"probe_range","data_type":"integer","required":true}`},
		{http.MethodDelete, "/api/v1/ci-types/" + typeID + "/fields/probe_range", ""},
	}
	for _, s := range steps {
		if w = call(f.OrgA, s.method, s.path, s.body); w.Code >= 300 {
			t.Fatalf("%s %s: %d %s", s.method, s.path, w.Code, w.Body.String())
		}
	}
	w = call(f.OrgA, http.MethodPost, "/api/v1/ci-types/"+typeID+"/clone", `{"key":"wp134-probe-v2","name":"WP-134 Probe v2"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("clone: %d %s", w.Code, w.Body.String())
	}
	cloneID := decode(w)["id"].(string)
	w = call(f.OrgA, http.MethodPost, "/api/v1/attribute-definitions", `{"key":"wp134_site_code","data_type":"text"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("attribute definition: %d %s", w.Code, w.Body.String())
	}
	defID := decode(w)["id"].(string)
	if w = call(f.OrgA, http.MethodPatch, "/api/v1/attribute-definitions/"+defID, `{"required":true}`); w.Code != http.StatusOK {
		t.Fatalf("patch attribute definition: %d %s", w.Code, w.Body.String())
	}

	// Export before the deletion of the global definition; another
	// organization's type stays out.
	if w = call(f.OrgB, http.MethodPost, "/api/v1/ci-types", `{"key":"wp134-foreign","name":"Foreign"}`); w.Code != http.StatusCreated {
		t.Fatalf("org B type: %d", w.Code)
	}
	w = call(f.OrgA, http.MethodGet, "/api/v1/ci-types/export", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Disposition"), "metamodel-") {
		t.Fatalf("export: %d %s", w.Code, w.Header())
	}
	var export citype.MetamodelExport
	if err := json.Unmarshal(w.Body.Bytes(), &export); err != nil {
		t.Fatal(err)
	}
	keys := map[string]*citype.Type{}
	system := 0
	for i := range export.CITypes {
		keys[export.CITypes[i].Key] = &export.CITypes[i]
		if export.CITypes[i].OrganizationID == "" {
			system++
		}
	}
	probe := keys["wp134-probe"]
	if export.FormatVersion != 1 || export.OrganizationID != f.OrgA || probe == nil || keys["wp134-probe-v2"] == nil ||
		keys["wp134-foreign"] != nil || system == 0 {
		t.Fatalf("export types: version %d, org %s, %d system, keys %v", export.FormatVersion, export.OrganizationID, system, len(keys))
	}
	fieldNames := []string{}
	for _, fld := range probe.Fields {
		fieldNames = append(fieldNames, fld.Name)
	}
	if !strings.Contains(strings.Join(fieldNames, ","), "probe_class") || strings.Contains(strings.Join(fieldNames, ","), "probe_range") {
		t.Errorf("probe fields in the export: %v", fieldNames)
	}
	globals := map[string]bool{}
	for _, d := range export.AttributeDefinitions {
		globals[d.Key] = true
		if d.Scope != "global" {
			t.Errorf("non-global definition in the global list: %+v", d)
		}
	}
	if !globals["wp134_site_code"] {
		t.Errorf("global definitions: %v", globals)
	}
	if w = call(f.OrgA, http.MethodDelete, "/api/v1/attribute-definitions/"+defID, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete attribute definition: %d %s", w.Code, w.Body.String())
	}

	// The audit trail of the organization.
	rows, err := f.Admin.Query(bg, `SELECT action, COALESCE(resource_id::text, ''), COALESCE(actor_id::text, ''), source,
		COALESCE(request_id, ''), COALESCE(correlation_id, ''), changes
		FROM audit_log WHERE organization_id = $1 AND resource_type IN ('ci_type', 'attribute_definition') ORDER BY seq`, f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type entry struct {
		action, resource, actor, source, request, correlation string
		changes                                               map[string]any
	}
	var got []entry
	for rows.Next() {
		var e entry
		if err = rows.Scan(&e.action, &e.resource, &e.actor, &e.source, &e.request, &e.correlation, &e.changes); err != nil {
			t.Fatal(err)
		}
		got = append(got, e)
	}
	wantActions := []string{"ci_type.created", "ci_type.updated", "ci_type.deactivated", "ci_type.activated",
		"attribute_definition.created", "attribute_definition.updated", "attribute_definition.deleted", "ci_type.cloned",
		"attribute_definition.created", "attribute_definition.updated", "attribute_definition.deleted"}
	actions := make([]string, len(got))
	for i, e := range got {
		actions[i] = e.action
		if e.actor != user || e.source != audit.SourceUI || e.correlation != "met-op" || !strings.HasPrefix(e.request, "req-") {
			t.Errorf("%s: actor %s, source %s, request %s, correlation %s", e.action, e.actor, e.source, e.request, e.correlation)
		}
	}
	if strings.Join(actions, " ") != strings.Join(wantActions, " ") {
		t.Fatalf("audit actions:\n got %v\nwant %v", actions, wantActions)
	}
	if got[0].resource != typeID || got[0].changes["after"] == nil || got[0].changes["before"] != nil {
		t.Errorf("type created: %+v", got[0])
	}
	before, _ := got[1].changes["before"].(map[string]any)
	after, _ := got[1].changes["after"].(map[string]any)
	if before["name"] != "WP-134 Probe" || after["name"] != "WP-134 Probe 2" {
		t.Errorf("type updated: before %v, after %v", before["name"], after["name"])
	}
	if b, _ := got[5].changes["before"].(map[string]any); b["required"] != false {
		t.Errorf("field updated: %+v", got[5].changes)
	}
	if got[6].changes["after"] != nil || got[6].changes["before"] == nil {
		t.Errorf("field deleted: %+v", got[6].changes)
	}
	if got[7].resource != cloneID {
		t.Errorf("clone: %+v", got[7])
	}
	if got[10].resource != defID {
		t.Errorf("definition deleted: %+v", got[10])
	}
	if result, err := audit.Verify(bg, f.App, f.OrgA); err != nil || !result.Intact {
		t.Errorf("audit chain: %+v %v", result, err)
	}
}
