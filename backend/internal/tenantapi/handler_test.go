package tenantapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

func withTenant(r *http.Request, org string) *http.Request {
	ctx := tenant.WithTenant(r.Context(), tenant.TenantInfo{OrganizationID: org})
	return r.WithContext(ctx)
}

func setup() (*Handler, *MemoryRepository, chi.Router) {
	repo := NewMemoryRepository()
	h := NewHandler(repo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	return h, repo, mux
}

func do(mux chi.Router, method, path, body, org string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, bytes.NewBufferString(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if org != "" {
		r = withTenant(r, org)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestClientCRUD(t *testing.T) {
	_, _, mux := setup()

	w := do(mux, "POST", "/api/v1/clients", `{"name":"Acme","slug":"acme"}`, "org-1")
	if w.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var c Client
	json.Unmarshal(w.Body.Bytes(), &c)
	if c.ID == "" || c.Name != "Acme" {
		t.Fatalf("unexpected client: %+v", c)
	}

	// duplicate slug -> 409 naming the field
	if w := do(mux, "POST", "/api/v1/clients", `{"name":"Acme2","slug":"acme"}`, "org-1"); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), `"field":"slug"`) {
		t.Errorf("dup slug: expected 409 for slug, got %d: %s", w.Code, w.Body.String())
	}

	// missing fields -> 400
	if w := do(mux, "POST", "/api/v1/clients", `{"name":"x"}`, "org-1"); w.Code != http.StatusBadRequest {
		t.Errorf("missing slug: expected 400, got %d", w.Code)
	}

	// get
	if w := do(mux, "GET", "/api/v1/clients/"+c.ID, "", "org-1"); w.Code != http.StatusOK {
		t.Errorf("get: expected 200, got %d", w.Code)
	}

	// patch
	if w := do(mux, "PATCH", "/api/v1/clients/"+c.ID, `{"name":"Renamed"}`, "org-1"); w.Code != http.StatusOK {
		t.Errorf("patch: expected 200, got %d", w.Code)
	}

	// delete
	if w := do(mux, "DELETE", "/api/v1/clients/"+c.ID, "", "org-1"); w.Code != http.StatusNoContent {
		t.Errorf("delete: expected 204, got %d", w.Code)
	}
	if w := do(mux, "GET", "/api/v1/clients/"+c.ID, "", "org-1"); w.Code != http.StatusNotFound {
		t.Errorf("get deleted: expected 404, got %d", w.Code)
	}
}

func TestClientUnauthorized(t *testing.T) {
	_, _, mux := setup()
	r := httptest.NewRequest("GET", "/api/v1/clients", nil).WithContext(context.Background())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestTenantIsolation(t *testing.T) {
	_, repo, mux := setup()
	c := &Client{OrganizationID: "org-1", Name: "A", Slug: "a"}
	repo.CreateClient(context.Background(), c)

	// org-2 must not see org-1's client
	if w := do(mux, "GET", "/api/v1/clients/"+c.ID, "", "org-2"); w.Code != http.StatusNotFound {
		t.Errorf("cross-tenant get: expected 404, got %d", w.Code)
	}
	w := do(mux, "GET", "/api/v1/clients", "", "org-2")
	var resp struct {
		Total int `json:"total"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 0 {
		t.Errorf("cross-tenant list: expected 0, got %d", resp.Total)
	}
}

func TestSiteBuildingRoomHierarchy(t *testing.T) {
	_, repo, mux := setup()
	c := &Client{OrganizationID: "org-1", Name: "A", Slug: "a"}
	repo.CreateClient(context.Background(), c)

	// site requires valid client
	if w := do(mux, "POST", "/api/v1/sites", `{"client_id":"nope","name":"S"}`, "org-1"); w.Code != http.StatusBadRequest {
		t.Errorf("site bad client: expected 400, got %d", w.Code)
	}
	w := do(mux, "POST", "/api/v1/sites", `{"client_id":"`+c.ID+`","name":"HQ"}`, "org-1")
	if w.Code != http.StatusCreated {
		t.Fatalf("site create: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var s Site
	json.Unmarshal(w.Body.Bytes(), &s)

	// filter by client_id
	w = do(mux, "GET", "/api/v1/sites?client_id="+c.ID, "", "org-1")
	var sl struct {
		Total int `json:"total"`
	}
	json.Unmarshal(w.Body.Bytes(), &sl)
	if sl.Total != 1 {
		t.Errorf("site filter: expected 1, got %d", sl.Total)
	}

	// building
	w = do(mux, "POST", "/api/v1/buildings", `{"site_id":"`+s.ID+`","name":"B1"}`, "org-1")
	if w.Code != http.StatusCreated {
		t.Fatalf("building create: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var b Building
	json.Unmarshal(w.Body.Bytes(), &b)

	// room
	w = do(mux, "POST", "/api/v1/rooms", `{"building_id":"`+b.ID+`","name":"R1"}`, "org-1")
	if w.Code != http.StatusCreated {
		t.Fatalf("room create: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var rm Room
	json.Unmarshal(w.Body.Bytes(), &rm)
	if rm.RoomType != "general" {
		t.Errorf("expected default room_type general, got %q", rm.RoomType)
	}

	// filter rooms by building
	w = do(mux, "GET", "/api/v1/rooms?building_id="+b.ID, "", "org-1")
	var rl struct {
		Total int `json:"total"`
	}
	json.Unmarshal(w.Body.Bytes(), &rl)
	if rl.Total != 1 {
		t.Errorf("room filter: expected 1, got %d", rl.Total)
	}
}

// TestOrganizationFields covers WP-107 (LOC-01, API-05): GET/PATCH /org with
// IANA time zone, ISO 4217 currency and canonical BCP 47 locale; invalid
// values are rejected with 400 naming the field. The PATCH runs as an
// org-wide holder of user:manage (WP-633).
func TestOrganizationFields(t *testing.T) {
	_, _, mux := setup()
	all := identity.ScopeSet{All: true}
	orgAdmin := identity.Principal{OrganizationID: "org-1", Permissions: []identity.Permission{identity.PermUserManage},
		Scope: &identity.Scope{Clients: all, Sites: all, Teams: all}}
	patch := func(body string) *httptest.ResponseRecorder {
		r := withTenant(httptest.NewRequest("PATCH", "/api/v1/org", bytes.NewBufferString(body)), "org-1")
		r = r.WithContext(identity.WithPrincipal(r.Context(), orgAdmin))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}

	w := do(mux, "GET", "/api/v1/org", "", "org-1")
	var o Organization
	if err := json.Unmarshal(w.Body.Bytes(), &o); w.Code != http.StatusOK || err != nil ||
		o.Timezone != "UTC" || o.Currency != "EUR" || o.Locale != "de-DE" {
		t.Fatalf("get org: %d %s", w.Code, w.Body.String())
	}
	w = patch(`{"timezone":"Europe/Berlin","currency":"CHF","locale":"de-ch","name":"Acme MSP"}`)
	if err := json.Unmarshal(w.Body.Bytes(), &o); w.Code != http.StatusOK || err != nil ||
		o.Timezone != "Europe/Berlin" || o.Currency != "CHF" || o.Locale != "de-CH" || o.Name != "Acme MSP" {
		t.Fatalf("patch org: %d %s", w.Code, w.Body.String())
	}
	for _, tc := range []struct{ body, field string }{
		{`{"timezone":"CET2"}`, "timezone"},
		{`{"timezone":"Mars/Olympus"}`, "timezone"},
		{`{"timezone":"Local"}`, "timezone"},
		{`{"currency":"EURO"}`, "currency"},
		{`{"currency":"eur"}`, "currency"},
		{`{"currency":"ABC"}`, "currency"},
		{`{"locale":"not a locale"}`, "locale"},
		{`{"locale":""}`, "locale"},
		{`{"name":""}`, "name"},
	} {
		w = patch(tc.body)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"field":"`+tc.field+`"`) {
			t.Errorf("%s: %d %s", tc.body, w.Code, w.Body.String())
		}
	}
	// Slug and plan are not part of the PATCH.
	if w = patch(`{"plan":"enterprise"}`); w.Code != http.StatusBadRequest {
		t.Errorf("plan accepted: %d", w.Code)
	}
}

// TestOrganizationAIOptInRequiresOrgWideUserManage covers WP-463 (AI-02)
// and WP-633 (RBA-03, F-359): only a principal holding user:manage for the
// whole organization changes the organization. A principal bound to a
// client, a site or a team, one holding user:manage only in such a scope, a
// client-bound API key or a request without principal gets 403 for every
// field, with or without ai_opt_in, and the organization stays unchanged;
// the check precedes the validation, so an invalid body gets 403 as well.
func TestOrganizationAIOptInRequiresOrgWideUserManage(t *testing.T) {
	_, repo, mux := setup()
	all := identity.ScopeSet{All: true}
	some := identity.ScopeSet{IDs: []string{"c-1"}}
	manage := []identity.Permission{identity.PermUserManage}
	patch := func(body string, p *identity.Principal) *httptest.ResponseRecorder {
		r := withTenant(httptest.NewRequest("PATCH", "/api/v1/org", bytes.NewBufferString(body)), "org-1")
		if p != nil {
			r = r.WithContext(identity.WithPrincipal(r.Context(), *p))
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	optIn := func() bool {
		o, _ := repo.GetOrganization(context.Background(), "org-1")
		return o.AIOptIn
	}
	org := func() string {
		o, _ := repo.GetOrganization(context.Background(), "org-1")
		b, _ := json.Marshal(o)
		return string(b)
	}
	for name, p := range map[string]*identity.Principal{
		"no principal":   nil,
		"client-bound":   {OrganizationID: "org-1", Permissions: manage, Scope: &identity.Scope{Clients: some, Sites: all, Teams: all}},
		"site-bound":     {OrganizationID: "org-1", Permissions: manage, Scope: &identity.Scope{Clients: some, Sites: some, Teams: all}},
		"team-bound":     {OrganizationID: "org-1", Permissions: manage, Scope: &identity.Scope{Clients: all, Sites: all, Teams: some}},
		"client API key": {OrganizationID: "org-1", Permissions: manage, ClientScope: "c-1", Type: identity.PrincipalTypeAPIKey},
		"user:manage narrower": {OrganizationID: "org-1", Permissions: append(manage, identity.PermCIRead),
			Scope:            &identity.Scope{Clients: all, Sites: all, Teams: all},
			PermissionScopes: map[identity.Permission]identity.Scope{identity.PermUserManage: {Clients: some, Sites: all, Teams: all}}},
		"org-wide without user:manage": {OrganizationID: "org-1", Permissions: []identity.Permission{identity.PermCIRead},
			Scope: &identity.Scope{Clients: all, Sites: all, Teams: all}},
	} {
		if w := patch(`{"ai_opt_in":true,"name":"Changed"}`, p); w.Code != http.StatusForbidden || optIn() {
			t.Errorf("%s: %d %s, ai_opt_in %v; want 403 and false", name, w.Code, w.Body.String(), optIn())
		}
		for _, body := range []string{
			`{"name":"Renamed"}`,
			`{"timezone":"Pacific/Auckland"}`,
			`{"currency":"JPY"}`,
			`{"locale":"ja-JP"}`,
			`{"settings":{"probe":"bound"}}`,
			`{"name":""}`,
		} {
			before := org()
			w := patch(body, p)
			if w.Code != http.StatusForbidden || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") {
				t.Errorf("%s: PATCH %s without ai_opt_in: %d %q %s; want 403 problem+json",
					name, body, w.Code, w.Header().Get("Content-Type"), w.Body.String())
			}
			if after := org(); after != before {
				t.Errorf("%s: PATCH %s changed the organization: %s -> %s", name, body, before, after)
			}
		}
	}

	orgAdmin := &identity.Principal{OrganizationID: "org-1", Permissions: manage, Scope: &identity.Scope{Clients: all, Sites: all, Teams: all}}
	var o Organization
	w := patch(`{"ai_opt_in":true}`, orgAdmin)
	if err := json.Unmarshal(w.Body.Bytes(), &o); w.Code != http.StatusOK || err != nil || !o.AIOptIn || !optIn() {
		t.Fatalf("org-wide user:manage: %d %s; want 200 with ai_opt_in true", w.Code, w.Body.String())
	}
	apiKey := &identity.Principal{OrganizationID: "org-1", Permissions: manage, Type: identity.PrincipalTypeAPIKey}
	if w := patch(`{"ai_opt_in":false}`, apiKey); w.Code != http.StatusOK || optIn() {
		t.Errorf("org-wide API key with user:manage: %d %s; want 200 with ai_opt_in false", w.Code, w.Body.String())
	}
	if w := patch(`{"name":"Renamed by the org admin","timezone":"Europe/Vienna"}`, orgAdmin); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"name":"Renamed by the org admin"`) || !strings.Contains(w.Body.String(), `"timezone":"Europe/Vienna"`) {
		t.Errorf("org-wide user:manage PATCH without ai_opt_in: %d %s; want 200 with the new name and time zone", w.Code, w.Body.String())
	}
	if w := patch(`{"currency":"CHF"}`, apiKey); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"currency":"CHF"`) {
		t.Errorf("org-wide API key PATCH without ai_opt_in: %d %s; want 200 with currency CHF", w.Code, w.Body.String())
	}
}

// TestClientAndSiteUniqueness covers WP-107 (LOC-02, LOC-03, TEN-01):
// client names are unique per organization (409 on name), external_ref and
// the optional time zone are stored and cleared; site names are unique per
// client, so two clients may each have a site "HQ".
func TestClientAndSiteUniqueness(t *testing.T) {
	_, _, mux := setup()
	create := func(path, body string) map[string]any {
		t.Helper()
		w := do(mux, "POST", path, body, "org-1")
		if w.Code != http.StatusCreated {
			t.Fatalf("POST %s %s: %d %s", path, body, w.Code, w.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	a := create("/api/v1/clients", `{"name":"Alpha","slug":"alpha","external_ref":"CRM-17","timezone":"Europe/Vienna"}`)
	b := create("/api/v1/clients", `{"name":"Beta","slug":"beta"}`)
	if a["external_ref"] != "CRM-17" || a["timezone"] != "Europe/Vienna" || b["external_ref"] != nil {
		t.Errorf("client fields: %v / %v", a, b)
	}
	if w := do(mux, "POST", "/api/v1/clients", `{"name":"Alpha","slug":"alpha-2"}`, "org-1"); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), `"field":"name"`) {
		t.Errorf("duplicate client name: %d %s", w.Code, w.Body.String())
	}
	if w := do(mux, "PATCH", "/api/v1/clients/"+b["id"].(string), `{"name":"Alpha"}`, "org-1"); w.Code != http.StatusConflict {
		t.Errorf("rename onto an existing name: %d", w.Code)
	}
	if w := do(mux, "POST", "/api/v1/clients", `{"name":"Gamma","slug":"gamma","timezone":"Moon/Base"}`, "org-1"); w.Code != http.StatusBadRequest ||
		!strings.Contains(w.Body.String(), `"field":"timezone"`) {
		t.Errorf("invalid client timezone: %d %s", w.Code, w.Body.String())
	}
	w := do(mux, "PATCH", "/api/v1/clients/"+a["id"].(string), `{"external_ref":"","timezone":""}`, "org-1")
	if !strings.Contains(w.Body.String(), `"external_ref":null`) || !strings.Contains(w.Body.String(), `"timezone":null`) {
		t.Errorf("clearing external_ref/timezone: %s", w.Body.String())
	}
	// Other organizations may reuse the name.
	if w := do(mux, "POST", "/api/v1/clients", `{"name":"Alpha","slug":"alpha"}`, "org-2"); w.Code != http.StatusCreated {
		t.Errorf("same name in another org: %d", w.Code)
	}

	// Sites: "HQ" once per client.
	create("/api/v1/sites", `{"client_id":"`+a["id"].(string)+`","name":"HQ","timezone":"Europe/Berlin"}`)
	hqB := create("/api/v1/sites", `{"client_id":"`+b["id"].(string)+`","name":"HQ"}`)
	if w := do(mux, "POST", "/api/v1/sites", `{"client_id":"`+a["id"].(string)+`","name":"HQ"}`, "org-1"); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), `"field":"name"`) {
		t.Errorf("duplicate site name per client: %d %s", w.Code, w.Body.String())
	}
	other := create("/api/v1/sites", `{"client_id":"`+b["id"].(string)+`","name":"Branch"}`)
	if w := do(mux, "PATCH", "/api/v1/sites/"+other["id"].(string), `{"name":"HQ"}`, "org-1"); w.Code != http.StatusConflict {
		t.Errorf("rename site onto an existing name: %d", w.Code)
	}
	if w := do(mux, "PATCH", "/api/v1/sites/"+hqB["id"].(string), `{"timezone":"Asia/Tokyo"}`, "org-1"); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"timezone":"Asia/Tokyo"`) {
		t.Errorf("site timezone: %d %s", w.Code, w.Body.String())
	}
}
