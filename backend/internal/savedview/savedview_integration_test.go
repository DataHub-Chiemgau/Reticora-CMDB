package savedview_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/savedview"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestSavedViewsVIE01AgainstPostgres covers VIE-01 (WP-188) against
// PostgreSQL: a view stores filter AST, sort, columns and the roles it is
// shared with; a private view of somebody else is 404 on GET, a role
// member reads a role-shared view but cannot change it; invalid sort keys
// and columns are 422.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSavedViewsVIE01AgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "bd")
	bg := context.Background()
	owner := f.AppUser(t, f.OrgA, "view-owner")
	member := f.AppUser(t, f.OrgA, "view-member")
	stranger := f.AppUser(t, f.OrgA, "view-stranger")
	role := f.ID()
	if _, err := f.Admin.Exec(bg, `INSERT INTO role (id, organization_id, name) VALUES ($1, $2, 'view-readers')`, role, f.OrgA); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, member, role); err != nil {
		t.Fatal(err)
	}
	mux := chi.NewRouter()
	savedview.NewHandler(savedview.NewPGRepository(f.App)).RegisterRoutes(mux)
	call := func(user, method, path, body string) (int, map[string]any) {
		t.Helper()
		ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
		ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: user, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	code, view := call(owner, http.MethodPost, "/api/v1/saved-views", `{"name": "Große Server", "entity_kind": "ci",
		"filter_spec": {"where": {"field": "attributes.ram_gb", "op": "gte", "value": 64}},
		"sort": [{"field": "name"}, {"field": "updated_at", "dir": "desc"}], "columns": ["name", "status", "attributes.ram_gb"],
		"shared_with_roles": ["`+role+`"]}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, view)
	}
	id, _ := view["id"].(string)
	private := func() string {
		_, v := call(owner, http.MethodPost, "/api/v1/saved-views", `{"name": "Privat", "entity_kind": "ci"}`)
		pid, _ := v["id"].(string)
		return pid
	}()

	code, got := call(owner, http.MethodGet, "/api/v1/saved-views/"+id, "")
	sortKeys, _ := got["sort"].([]any)
	columns, _ := got["columns"].([]any)
	roles, _ := got["shared_with_roles"].([]any)
	if code != http.StatusOK || len(sortKeys) != 2 || sortKeys[0].(map[string]any)["dir"] != "asc" || len(columns) != 3 || len(roles) != 1 {
		t.Errorf("stored view: %d %v", code, got)
	}
	if code, _ = call(member, http.MethodGet, "/api/v1/saved-views/"+id, ""); code != http.StatusOK {
		t.Errorf("role member reads the role-shared view: %d", code)
	}
	if code, _ = call(stranger, http.MethodGet, "/api/v1/saved-views/"+id, ""); code != http.StatusNotFound {
		t.Errorf("stranger reads the role-shared view: %d", code)
	}
	if code, _ = call(member, http.MethodGet, "/api/v1/saved-views/"+private, ""); code != http.StatusNotFound {
		t.Errorf("GET of somebody's private view: %d", code)
	}
	if code, _ = call(member, http.MethodPatch, "/api/v1/saved-views/"+id, `{"name": "gekapert"}`); code != http.StatusNotFound {
		t.Errorf("role member changes the view: %d", code)
	}
	_, list := call(member, http.MethodGet, "/api/v1/saved-views", "")
	if total, _ := list["total"].(float64); total != 1 {
		t.Errorf("member's list: %v", list)
	}
	for body, field := range map[string]string{
		`{"name": "x", "sort": [{"field": "password"}]}`:                                  "sort[0].field",
		`{"name": "x", "sort": [{"field": "name", "dir": "sideways"}]}`:                   "sort[0].dir",
		`{"name": "x", "columns": ["name", "secret"]}`:                                    "columns[1]",
		`{"name": "x", "shared_with_roles": ["admins"]}`:                                  "shared_with_roles[0]",
		`{"name": "x", "filter_spec": {"where": {"field": "x", "op": "eq", "value": 1}}}`: "filter_spec.where",
	} {
		if code, out := call(owner, http.MethodPost, "/api/v1/saved-views", body); code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %v, want 422 on %s", body, code, out, field)
		}
	}
}
