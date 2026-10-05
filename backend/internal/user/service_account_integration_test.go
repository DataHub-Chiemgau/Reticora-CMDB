package user_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/go-chi/chi/v5"
)

// TestServiceAccounts covers WP-069 (RBA-08, API-05): service accounts are
// managed through /service-accounts with role assignments and scopes; an API
// key bound to an account acts with the account's rights within its scope and
// names the account as subject; the webhook filter lets an account read only
// objects in its scope; a deactivated account grants nothing; an account with
// bound keys cannot be deleted; every change is audited.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestServiceAccounts(t *testing.T) {
	f := scopetest.Seed(t, "62")
	bg := context.Background()
	admin := f.AppUser(t, f.OrgA, "sa-admin")
	repo := user.NewPGServiceAccounts(f.App)
	keys := identity.NewPGAPIKeyStore(f.App)

	mux := chi.NewRouter()
	user.NewServiceAccountHandler(repo).RegisterRoutes(mux)
	identity.NewAPIKeyHandler(keys).RegisterRoutes(mux)
	principal := identity.Principal{Subject: admin, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
		Permissions: []identity.Permission{identity.PermUserManage, identity.PermAPIKeyManage, identity.PermCIRead, identity.PermCIWrite}}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		scope := database.OrgWideScope(f.OrgA, admin)
		ctx := identity.WithPrincipal(req.Context(), principal)
		ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: f.OrgA, UserID: admin})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(database.ContextWithTenantScope(ctx, &scope)))
		return w
	}

	w := call(http.MethodPost, "/api/v1/service-accounts", `{"name":"ci-exporter","description":"nightly export"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var sa user.ServiceAccount
	if err := json.Unmarshal(w.Body.Bytes(), &sa); err != nil {
		t.Fatal(err)
	}
	if w = call(http.MethodPost, "/api/v1/service-accounts", `{"name":"ci-exporter"}`); w.Code != http.StatusConflict {
		t.Errorf("duplicate name: %d, want 409", w.Code)
	}

	// The account reads and writes CIs, but only in client 1.
	roleID := f.ID()
	if _, err := f.Admin.Exec(bg, `INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 'sa-role', '["ci:read","ci:write"]')`, roleID, f.OrgA); err != nil {
		t.Fatal(err)
	}
	w = call(http.MethodPut, "/api/v1/service-accounts/"+sa.ID+"/roles", `{"roles":[{"role_id":"`+roleID+`","scope_client_id":"`+f.Client1+`"}]}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), f.Client1) {
		t.Fatalf("set roles: %d %s", w.Code, w.Body.String())
	}
	if w = call(http.MethodPut, "/api/v1/service-accounts/"+sa.ID+"/roles", `{"roles":[{"role_id":"`+f.ID()+`"}]}`); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown role: %d, want 422", w.Code)
	}
	if w = call(http.MethodGet, "/api/v1/service-accounts/"+sa.ID, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), roleID) {
		t.Errorf("get: %d %s (roles must be unchanged after the failed replacement)", w.Code, w.Body.String())
	}

	// Webhook filter: objects of client 1 and org-wide objects, not client 2.
	access := user.ServiceAccountAccess{Repo: repo}
	for _, tc := range []struct {
		permission, client string
		want               bool
	}{
		{"ci:read", f.Client1, true},
		{"ci:read", "", true},
		{"ci:read", f.Client2, false},
		{"asset:read", f.Client1, false},
	} {
		if ok, err := access.CanReadEvent(bg, f.OrgA, sa.ID, tc.permission, tc.client, ""); err != nil || ok != tc.want {
			t.Errorf("CanReadEvent(%s, %q) = %v, %v; want %v", tc.permission, tc.client, ok, err, tc.want)
		}
	}

	// A key bound to the account acts as the account.
	w = call(http.MethodPost, "/api/v1/api-keys", `{"name":"exporter-key","permissions":["ci:read","ci:write"],"service_account_id":"`+sa.ID+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create bound key: %d %s", w.Code, w.Body.String())
	}
	var key struct {
		Key              string `json:"key"`
		ServiceAccountID string `json:"service_account_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &key); err != nil || key.ServiceAccountID != sa.ID {
		t.Fatalf("bound key %s, %v", w.Body.String(), err)
	}
	service := identity.NewAPIKeyServiceWithStore(keys).WithOwnerAccess(permission.NewPGRepository(f.App)).
		WithServiceAccountAccess(access)
	info, err := service.Validate(bg, key.Key)
	if err != nil {
		t.Fatal(err)
	}
	if info.OwnerID != sa.ID || len(info.Scopes) != 2 || info.Scope == nil || info.Scope.Clients.All ||
		len(info.Scope.Clients.IDs) != 1 || info.Scope.Clients.IDs[0] != f.Client1 {
		t.Errorf("bound key info %+v scope %+v, want the account's rights in client 1", info, info.Scope)
	}

	// Deactivation removes every right of the account.
	if w = call(http.MethodPatch, "/api/v1/service-accounts/"+sa.ID, `{"is_active":false}`); w.Code != http.StatusOK {
		t.Fatalf("deactivate: %d %s", w.Code, w.Body.String())
	}
	if info, err = service.Validate(bg, key.Key); err != nil || len(info.Scopes) != 0 {
		t.Errorf("key of a deactivated account: %+v, %v; want no permissions", info, err)
	}
	if ok, _ := access.CanReadEvent(bg, f.OrgA, sa.ID, "ci:read", f.Client1, ""); ok {
		t.Error("deactivated account still reads events")
	}

	// Bound accounts cannot be deleted; unbound ones can.
	if w = call(http.MethodDelete, "/api/v1/service-accounts/"+sa.ID, ""); w.Code != http.StatusConflict {
		t.Errorf("delete bound account: %d, want 409", w.Code)
	}
	w = call(http.MethodPost, "/api/v1/service-accounts", `{"name":"spare"}`)
	var spare user.ServiceAccount
	_ = json.Unmarshal(w.Body.Bytes(), &spare)
	if w = call(http.MethodDelete, "/api/v1/service-accounts/"+spare.ID, ""); w.Code != http.StatusNoContent {
		t.Errorf("delete unbound account: %d %s", w.Code, w.Body.String())
	}
	if w = call(http.MethodGet, "/api/v1/service-accounts/"+f.ID(), ""); w.Code != http.StatusNotFound {
		t.Errorf("unknown account: %d, want 404", w.Code)
	}
	if w = call(http.MethodGet, "/api/v1/service-accounts", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "ci-exporter") {
		t.Errorf("list: %d %s", w.Code, w.Body.String())
	}

	var actions []string
	rows, err := f.Admin.Query(bg, `SELECT action FROM audit_log WHERE organization_id = $1 AND resource_type = 'service_account' AND actor_id = $2 ORDER BY timestamp`, f.OrgA, admin)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a string
		if err = rows.Scan(&a); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, a)
	}
	rows.Close()
	want := "service_account.created,service_account.roles_changed,service_account.updated,service_account.created,service_account.deleted"
	if strings.Join(actions, ",") != want {
		t.Errorf("audit %v, want %s", actions, want)
	}
}
