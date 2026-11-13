package compliance

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Instantiating the shipped rule templates on PostgreSQL (SEC-03,
// WP-1130): an inactive rule with the controls of the template, 404 for an
// unknown key, 409 for a name in use, 422 on ci_type for a type the
// organization lacks.
//
// They run only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).

type templateEnv struct {
	t    *testing.T
	f    *scopetest.Fixture
	repo *PGRepository
	mux  http.Handler
}

func newTemplateEnv(t *testing.T) *templateEnv {
	t.Helper()
	f := scopetest.Seed(t, "3d")
	repo := NewPGRepository(f.App)
	r := chi.NewRouter()
	NewHandler(repo, nil).RegisterRoutes(r)
	return &templateEnv{t: t, f: f, repo: repo, mux: r}
}

func (e *templateEnv) ctx() context.Context {
	scope := database.OrgWideScope(e.f.OrgA, e.f.User)
	return tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope),
		tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: e.f.User})
}

func (e *templateEnv) do(method, path, lang string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, path, nil).WithContext(e.ctx())
	if lang != "" {
		req.Header.Set("Accept-Language", lang)
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

// instantiatePattern is the route of instantiating a template.
const instantiatePattern = "/api/v1/compliance/rule-templates/{key}/instantiate"

// auditedInstantiate sends the POST through the route audit of the server
// (snapshot register: created compliance_rule).
func (e *templateEnv) auditedInstantiate(key, lang string) *httptest.ResponseRecorder {
	e.t.Helper()
	r := chi.NewRouter()
	r.With(middleware.AuditRoute(http.MethodPost, instantiatePattern, audit.NewPGRouteAuditor(e.f.App), middleware.AuditIntent, "")).
		Method(http.MethodPost, instantiatePattern, e.mux)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/compliance/rule-templates/"+key+"/instantiate", nil).WithContext(e.ctx())
	req.Header.Set("Accept-Language", lang)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestInstantiateCreatesInactiveRule(t *testing.T) {
	e := newTemplateEnv(t)
	rec := e.auditedInstantiate("management_ip_set", "en-US")
	if rec.Code != http.StatusCreated {
		t.Fatalf("instantiate: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Active      bool     `json:"active"`
		Severity    string   `json:"severity"`
		CITypeID    string   `json:"ci_type_id"`
		ControlIDs  []string `json:"control_ids"`
		Expression  JSONMap  `json:"expression"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Active || out.Name != "Management IP set on network devices" || out.Severity != "low" || out.CITypeID != "" ||
		out.Expression["field"] != "management_ip" {
		t.Errorf("rule %s", rec.Body)
	}
	if len(out.ControlIDs) != 3 {
		t.Errorf("controls %v, want 3", out.ControlIDs)
	}
	// The stored row (the audit snapshot of the route) names template and
	// version; the rule applies to every network device type (applies_when
	// ci_type_id in switch, router, firewall and access_point).
	var active bool
	var description string
	var typeKeys, controls []string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT r.active, r.description,
			ARRAY(SELECT t.key FROM ci_type t, jsonb_array_elements_text(r.applies_when->'value') v(id)
			      WHERE r.applies_when->>'op' = 'in' AND r.applies_when->>'field' = 'ci_type_id' AND t.id = v.id::uuid ORDER BY 1),
			ARRAY(SELECT c.standard || ':' || c.control_id FROM compliance_rule_control rc JOIN compliance_control c ON c.id = rc.control_id
			      WHERE rc.rule_id = r.id ORDER BY 1)
		FROM compliance_rule r WHERE r.id = $1 AND r.ci_type_id IS NULL`, out.ID).Scan(&active, &description, &typeKeys, &controls); err != nil {
		t.Fatal(err)
	}
	if active || !strings.Contains(description, "[template management_ip_set v1]") {
		t.Errorf("stored rule: active %v, description %q", active, description)
	}
	if want := []string{"access_point", "firewall", "router", "switch"}; !slices.Equal(typeKeys, want) {
		t.Errorf("CI types of applies_when %v, want %v", typeKeys, want)
	}
	if want := []string{"iso27001:5.9", "iso27001:8.9", "nis2:21.2.i"}; !slices.Equal(controls, want) {
		t.Errorf("stored controls %v, want %v", controls, want)
	}
	// The route audit wrote one entry of the instantiation: the template
	// key as resource key, the new rule's id and the template and its
	// version in its values (snapshot created compliance_rule, AK3).
	var changes string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT changes::text FROM audit_log
		WHERE organization_id = $1 AND action = 'compliance.instantiate' AND changes->>'resource_key' = 'management_ip_set'`,
		e.f.OrgA).Scan(&changes); err != nil {
		t.Fatalf("audit entry of the instantiation: %v", err)
	}
	if !strings.Contains(changes, out.ID) || !strings.Contains(changes, "[template management_ip_set v1]") {
		t.Errorf("audit entry %s, want the rule id and the template marker", changes)
	}
	// The catalog in the language of the request.
	var list struct {
		Data []TemplateView `json:"data"`
	}
	rec = e.do(http.MethodGet, "/api/v1/compliance/rule-templates", "de-DE")
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Data) != len(Templates) {
		t.Fatalf("catalog %s %v", rec.Body, err)
	}
	if list.Data[0].Name != "Firmware-Version gepflegt" {
		t.Errorf("German name %q", list.Data[0].Name)
	}
	rec = e.do(http.MethodGet, "/api/v1/compliance/rule-templates", "en")
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || list.Data[0].Name != "Firmware version maintained" {
		t.Errorf("English catalog %s", rec.Body)
	}
	// Every template instantiates.
	for i := range Templates {
		if Templates[i].Key == "management_ip_set" {
			continue
		}
		if rec := e.do(http.MethodPost, "/api/v1/compliance/rule-templates/"+Templates[i].Key+"/instantiate", ""); rec.Code != http.StatusCreated {
			t.Errorf("%s: %d %s", Templates[i].Key, rec.Code, rec.Body)
		}
	}
}

func TestInstantiateUnknownKey(t *testing.T) {
	e := newTemplateEnv(t)
	if rec := e.do(http.MethodPost, "/api/v1/compliance/rule-templates/no_such_template/instantiate", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown key: %d %s", rec.Code, rec.Body)
	}
	// A CI type the organization does not have: 422 on ci_type, no rule.
	tpl := Templates[0]
	tpl.Key, tpl.CITypeKey = "typed", "wp1130-no-such-type"
	if _, err := e.repo.InstantiateTemplate(e.ctx(), e.f.OrgA, &tpl, "de"); !errors.Is(err, ErrTemplateCIType) {
		t.Errorf("unknown CI type: %v, want ErrTemplateCIType", err)
	}
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM compliance_rule WHERE organization_id = $1`, e.f.OrgA).Scan(&n); err != nil || n != 0 {
		t.Errorf("rules after the refusals: %d %v", n, err)
	}
}

func TestInstantiateNameConflict(t *testing.T) {
	e := newTemplateEnv(t)
	path := "/api/v1/compliance/rule-templates/no_open_critical_finding/instantiate"
	if rec := e.do(http.MethodPost, path, ""); rec.Code != http.StatusCreated {
		t.Fatalf("first: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(http.MethodPost, path, ""); rec.Code != http.StatusConflict {
		t.Errorf("second: %d %s, want 409", rec.Code, rec.Body)
	}
	// In the other language the name differs: a second rule.
	if rec := e.do(http.MethodPost, path, "en"); rec.Code != http.StatusCreated {
		t.Errorf("English: %d %s", rec.Code, rec.Body)
	}
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM compliance_rule_control rc JOIN compliance_rule r ON r.id = rc.rule_id
		WHERE r.organization_id = $1`, e.f.OrgA).Scan(&n); err != nil || n != 6 {
		t.Errorf("controls of the two rules: %d %v, want 6 (none of the refused one)", n, err)
	}
}
