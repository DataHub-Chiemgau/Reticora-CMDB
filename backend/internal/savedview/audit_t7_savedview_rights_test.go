package savedview_test

// Phase-1 acceptance audit, group T7-ui: REGRESSION test for the high test
// gap of WP-188 (VIE-01) "edit/delete rights", on PostgreSQL through the
// production handler. WP-518 adapts it to decision E-86 (transitional rule
// option 1, F-335): only the owner changes or deletes a view; for everybody
// else a shared view, org-wide or by role, is read-only. An org member reads
// the org-shared view but its PATCH and DELETE answer 404 with the database
// row unchanged, a role member reads but can neither PATCH nor DELETE a
// role-shared view, another organization can neither read nor change it,
// the owner's PATCH answers 200 and keeps the sharing, and the owner's DELETE
// answers 204 with a later GET 404.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/ratchet?sslmode=disable \
//     go test -count=1 -v -run '^TestAuditT7SavedViewEditDeleteRights$' ./internal/savedview/

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/savedview"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// auditT7Views wires the saved-view and structured-search handler on the PG
// repositories of a scopetest fixture.
type auditT7Views struct {
	t   *testing.T
	f   *scopetest.Fixture
	mux *chi.Mux
}

func newAuditT7Views(t *testing.T, tag string) *auditT7Views {
	t.Helper()
	f := scopetest.Seed(t, tag)
	mux := chi.NewRouter()
	savedview.NewHandler(savedview.NewPGRepository(f.App)).
		WithQueryEngine(savedview.NewPGQueryEngine(f.App)).
		WithPermissions(grants{"ci:read": true, "asset:read": true}).
		RegisterRoutes(mux)
	return &auditT7Views{t: t, f: f, mux: mux}
}

// call runs one request as an org-wide principal user of org.
func (v *auditT7Views) call(org, user, method, path, body string) (int, map[string]any, string) {
	v.t.Helper()
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: org, UserID: user})
	scope := database.OrgWideScope(org, user)
	ctx = database.ContextWithTenantScope(ctx, &scope)
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: user, OrganizationID: org, Type: identity.PrincipalTypeUser})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	v.mux.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w.Body.String()
}

// create posts a view as owner and returns its id.
func (v *auditT7Views) create(owner, body string) string {
	v.t.Helper()
	code, out, raw := v.call(v.f.OrgA, owner, http.MethodPost, "/api/v1/saved-views", body)
	id, _ := out["id"].(string)
	if code != http.StatusCreated && code != http.StatusOK || id == "" {
		v.t.Fatalf("create view %s: %d %s", body, code, raw)
	}
	return id
}

func (v *auditT7Views) row(id string) (name string, shared bool, exists bool) {
	v.t.Helper()
	err := v.f.Admin.QueryRow(context.Background(), `SELECT name, shared FROM saved_view WHERE id = $1`, id).Scan(&name, &shared)
	if err != nil {
		return "", false, false
	}
	return name, shared, true
}

func TestAuditT7SavedViewEditDeleteRights(t *testing.T) {
	v := newAuditT7Views(t, "04")
	bg := context.Background()
	owner := v.f.AppUser(t, v.f.OrgA, "t7-owner")
	other := v.f.AppUser(t, v.f.OrgA, "t7-other")
	member := v.f.AppUser(t, v.f.OrgA, "t7-member")
	orgB := v.f.AppUser(t, v.f.OrgB, "t7-orgb")
	role := v.f.ID()
	if _, err := v.f.Admin.Exec(bg, `INSERT INTO role (id, organization_id, name) VALUES ($1, $2, 't7-readers')`, role, v.f.OrgA); err != nil {
		t.Fatal(err)
	}
	if _, err := v.f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, v.f.OrgA, member, role); err != nil {
		t.Fatal(err)
	}

	orgShared := v.create(owner, `{"name":"T7 org shared","entity_kind":"ci","shared":true}`)
	roleShared := v.create(owner, `{"name":"T7 role shared","entity_kind":"ci","shared_with_roles":["`+role+`"]}`)

	// Readers: an org member reads the org-shared view, the role member the
	// role-shared one; a non-member does not see the role-shared one and the
	// other organization sees neither.
	for _, tc := range []struct {
		name, org, user, id string
		want                int
	}{
		{"org member reads org-shared", v.f.OrgA, other, orgShared, http.StatusOK},
		{"role member reads role-shared", v.f.OrgA, member, roleShared, http.StatusOK},
		{"non-member does not read role-shared", v.f.OrgA, other, roleShared, http.StatusNotFound},
		{"other organization does not read org-shared", v.f.OrgB, orgB, orgShared, http.StatusNotFound},
	} {
		if code, _, raw := v.call(tc.org, tc.user, http.MethodGet, "/api/v1/saved-views/"+tc.id, ""); code != tc.want {
			t.Errorf("%s: GET = %d, want %d (%s)", tc.name, code, tc.want, raw)
		}
	}

	// Writers refused (E-86 option 1): every shared view only by its owner;
	// never across organizations. The refusal is the 404 of a view the
	// caller cannot see, before any validation of the body.
	for _, tc := range []struct {
		name, org, user, method, id, body string
	}{
		{"org member PATCH org-shared", v.f.OrgA, other, http.MethodPatch, orgShared, `{"name":"hijacked","shared":true}`},
		{"org member PATCH org-shared unsharing", v.f.OrgA, other, http.MethodPatch, orgShared, `{"shared":false}`},
		{"org member PATCH org-shared invalid body", v.f.OrgA, other, http.MethodPatch, orgShared, `{"name":"hijacked","columns":["secret"]}`},
		{"org member DELETE org-shared", v.f.OrgA, other, http.MethodDelete, orgShared, ""},
		{"role member PATCH role-shared", v.f.OrgA, member, http.MethodPatch, roleShared, `{"name":"hijacked","shared_with_roles":["` + role + `"]}`},
		{"role member DELETE role-shared", v.f.OrgA, member, http.MethodDelete, roleShared, ""},
		{"non-member PATCH role-shared", v.f.OrgA, other, http.MethodPatch, roleShared, `{"name":"hijacked"}`},
		{"other organization PATCH org-shared", v.f.OrgB, orgB, http.MethodPatch, orgShared, `{"name":"hijacked","shared":true}`},
		{"other organization DELETE org-shared", v.f.OrgB, orgB, http.MethodDelete, orgShared, ""},
	} {
		beforeName, beforeShared, _ := v.row(tc.id)
		code, _, raw := v.call(tc.org, tc.user, tc.method, "/api/v1/saved-views/"+tc.id, tc.body)
		if code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404 (%s)", tc.name, code, raw)
		}
		if name, shared, exists := v.row(tc.id); !exists || name != beforeName || shared != beforeShared {
			t.Errorf("%s changed the database row: exists=%v name=%q shared=%v", tc.name, exists, name, shared)
		}
	}
	if name, shared, _ := v.row(orgShared); name != "T7 org shared" || !shared {
		t.Errorf("org-shared view after refused writes: name=%q shared=%v", name, shared)
	}

	// The owner edits the view; a PATCH without shared keeps the sharing.
	if code, out, raw := v.call(v.f.OrgA, owner, http.MethodPatch, "/api/v1/saved-views/"+orgShared, `{"name":"T7 org shared edited"}`); code != http.StatusOK || out["shared"] != true {
		t.Errorf("owner PATCH org-shared = %d, want 200 with shared=true (%s)", code, raw)
	}
	if name, shared, exists := v.row(orgShared); !exists || name != "T7 org shared edited" || !shared {
		t.Errorf("owner PATCH org-shared stored exists=%v name=%q shared=%v", exists, name, shared)
	}
	if code, _, raw := v.call(v.f.OrgA, other, http.MethodGet, "/api/v1/saved-views/"+orgShared, ""); code != http.StatusOK {
		t.Errorf("org member reads the edited org-shared view = %d, want 200 (%s)", code, raw)
	}

	// Owner delete: 204, then 404 for everyone.
	if code, _, raw := v.call(v.f.OrgA, owner, http.MethodDelete, "/api/v1/saved-views/"+orgShared, ""); code != http.StatusNoContent {
		t.Fatalf("owner DELETE = %d, want 204 (%s)", code, raw)
	}
	if _, _, exists := v.row(orgShared); exists {
		t.Errorf("the owner's DELETE left the saved_view row")
	}
	for _, user := range []string{owner, other} {
		if code, _, _ := v.call(v.f.OrgA, user, http.MethodGet, "/api/v1/saved-views/"+orgShared, ""); code != http.StatusNotFound {
			t.Errorf("GET after delete as %s = %d, want 404", user, code)
		}
	}
}
