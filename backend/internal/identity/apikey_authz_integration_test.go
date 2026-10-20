package identity_test

// API key authorization against PostgreSQL (WP-354, AUT-04, API-05,
// RBA-08; findings F-006, F-007): rotating and revoking a key needs its
// owner or a caller holding the key's rights in the holder's scope, and a
// key bound to a service account never grants more than its creator holds.
// The fixture is also used by the remaining defect test
// TestAuditT2ServiceAccountRolesWithinCallerRights (WP-355).
//
// Principals are resolved from their stored role assignments exactly as at
// login (permission.PGRepository.AccessGrants + identity.ResolveAccess), and
// the request scope is built by middleware.TenantScopeFor.
//
// Retest (needs a migrated database):
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -v -run '^TestAuditT2APIKeyRotateRevokeOnlyByEntitledCaller$|^TestAuditT2KeyBindingNeverBroadensCreatorScope$' ./internal/identity/

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/go-chi/chi/v5"
)

// t2KeyFixture is organization A with an org-wide key administrator and a
// client-1 scoped user holding apikey:manage through a custom role.
type t2KeyFixture struct {
	f       *scopetest.Fixture
	mux     *chi.Mux
	service *identity.APIKeyService
	admin   identity.Principal
	scoped  identity.Principal
	orgB    identity.Principal
	// orgAdmin holds the built-in org_admin role and owns no key.
	orgAdmin identity.Principal
	saRole   string
	saRepo   *user.PGServiceAccounts
	resolve  func(orgID, userID string) identity.Principal
	exec     func(sql string, args ...any)
}

func newT2KeyFixture(t *testing.T) *t2KeyFixture {
	t.Helper()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	f := scopetest.Seed(t, "ea")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	adminID := f.AppUser(t, f.OrgA, "t2-key-admin")
	scopedID := f.AppUser(t, f.OrgA, "t2-key-scoped")
	orgBID := f.AppUser(t, f.OrgB, "t2-key-orgb")
	orgAdminID := f.AppUser(t, f.OrgA, "t2-key-org-admin")
	adminRole, scopedRole, orgBRole, saRole := f.ID(), f.ID(), f.ID(), f.ID()
	exec(`INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 't2-key-admin', '["ci:read","ci:delete","apikey:manage","user:manage","user:read","role:manage"]')`, adminRole, f.OrgA)
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, adminID, adminRole)
	exec(`INSERT INTO role (id, organization_id, name, permissions, scope) VALUES ($1, $2, 't2-key-scoped', '["ci:read","apikey:manage"]', 'client')`, scopedRole, f.OrgA)
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id) VALUES ($1, $2, $3, $4)`, f.OrgA, scopedID, scopedRole, f.Client1)
	exec(`INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 't2-key-orgb', '["ci:read","ci:delete","apikey:manage","user:manage","role:manage"]')`, orgBRole, f.OrgB)
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgB, orgBID, orgBRole)
	exec(`INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 't2-key-sa', '["ci:read"]')`, saRole, f.OrgA)
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id)
		SELECT $1, $2, id FROM role WHERE organization_id = $1 AND name = 'org_admin'`, f.OrgA, orgAdminID)

	perms := permission.NewPGRepository(f.App)
	resolve := func(orgID, userID string) identity.Principal {
		t.Helper()
		grants, err := perms.AccessGrants(bg, orgID, userID)
		if err != nil {
			t.Fatalf("access grants of %s: %v", userID, err)
		}
		access := identity.ResolveAccess(grants)
		scope := access.Scope
		return identity.Principal{Subject: userID, OrganizationID: orgID, Type: identity.PrincipalTypeUser,
			Permissions: access.Permissions, Scope: &scope, PermissionScopes: access.PermissionScopes, ClientScope: scope.LegacyClientScope()}
	}

	keys := identity.NewPGAPIKeyStore(f.App)
	saRepo := user.NewPGServiceAccounts(f.App)
	mux := chi.NewRouter()
	// Wired like server.apiKeyHandler and the API key service of cmd/server.
	identity.NewAPIKeyHandler(keys).WithOwnerAccess(perms).
		WithServiceAccountAccess(user.ServiceAccountAccess{Repo: saRepo}).RegisterRoutes(mux)
	user.NewServiceAccountHandler(saRepo).RegisterRoutes(mux)
	return &t2KeyFixture{
		f: f, mux: mux, saRole: saRole, saRepo: saRepo, resolve: resolve, exec: exec,
		service: identity.NewAPIKeyServiceWithStore(keys).WithOwnerAccess(perms).
			WithServiceAccountAccess(user.ServiceAccountAccess{Repo: saRepo}),
		admin:    resolve(f.OrgA, adminID),
		scoped:   resolve(f.OrgA, scopedID),
		orgB:     resolve(f.OrgB, orgBID),
		orgAdmin: resolve(f.OrgA, orgAdminID),
	}
}

// call serves a request as p with the tenant context the middleware builds.
func (k *t2KeyFixture) call(p identity.Principal, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	scope := middleware.TenantScopeFor(&p)
	ctx := identity.WithPrincipal(req.Context(), p)
	ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: p.OrganizationID, UserID: p.Subject, ClientID: p.ClientScope})
	ctx = database.ContextWithTenantScope(ctx, &scope)
	w := httptest.NewRecorder()
	k.mux.ServeHTTP(w, req.WithContext(ctx))
	return w
}

func (k *t2KeyFixture) createKey(t *testing.T, p identity.Principal, body string) (id, plaintext string) {
	t.Helper()
	w := k.call(p, http.MethodPost, "/api/v1/api-keys", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create key %s: %d %s", body, w.Code, w.Body.String())
	}
	var created struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	return created.ID, created.Key
}

// TestAuditT2APIKeyRotateRevokeOnlyByEntitledCaller: only the owner (or a
// caller holding every key permission in at least the owner's scope) may
// rotate or revoke a key (AUT-04 owner intersection, RBA-03). A client-scoped
// apikey:manage holder must neither obtain the plaintext of an org-wide admin
// key holding user:manage/role:manage nor end other users' keys. Tenant
// isolation and the owner's own rotation are controls.
func TestAuditT2APIKeyRotateRevokeOnlyByEntitledCaller(t *testing.T) {
	k := newT2KeyFixture(t)
	bg := context.Background()
	adminKeyID, adminKey := k.createKey(t, k.admin, `{"name":"t2-admin-key","permissions":["ci:read","ci:delete","user:manage","role:manage"]}`)
	readKeyID, readKey := k.createKey(t, k.admin, `{"name":"t2-admin-read","permissions":["ci:read"]}`)

	t.Run("non-owner rotate", func(t *testing.T) {
		w := k.call(k.scoped, http.MethodPost, "/api/v1/api-keys/"+adminKeyID+"/rotate", `{"overlap_seconds":0}`)
		if w.Code != http.StatusForbidden {
			var rotated struct {
				Key struct {
					Key string `json:"key"`
				} `json:"key"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &rotated)
			detail := ""
			if info, err := k.service.Validate(bg, rotated.Key.Key); err == nil {
				detail = " - the returned successor key carries " + strings.Join(permStrings(info.Scopes), ",")
			}
			t.Errorf("client-scoped {ci:read,apikey:manage} user rotates the org-wide admin key: %d, want 403%s", w.Code, detail)
		}
		if _, err := k.service.Validate(bg, adminKey); err != nil {
			t.Errorf("the admin's key after the foreign rotation attempt: %v, want still valid", err)
		}
	})
	t.Run("non-owner revoke", func(t *testing.T) {
		if w := k.call(k.scoped, http.MethodDelete, "/api/v1/api-keys/"+readKeyID, ""); w.Code != http.StatusForbidden {
			t.Errorf("client-scoped user revokes the org-wide admin's key: %d, want 403", w.Code)
		}
		if _, err := k.service.Validate(bg, readKey); err != nil {
			t.Errorf("the admin's key after the foreign revoke attempt: %v, want still valid", err)
		}
	})
	t.Run("controls", func(t *testing.T) {
		// Fresh keys: the subtests above may have ended the earlier ones.
		controlID, controlKey := k.createKey(t, k.admin, `{"name":"t2-admin-control","permissions":["ci:read"]}`)
		if w := k.call(k.orgB, http.MethodPost, "/api/v1/api-keys/"+controlID+"/rotate", ""); w.Code != http.StatusNotFound {
			t.Errorf("org B admin rotates an org A key: %d, want 404", w.Code)
		}
		if w := k.call(k.orgB, http.MethodDelete, "/api/v1/api-keys/"+controlID, ""); w.Code != http.StatusNotFound {
			t.Errorf("org B admin revokes an org A key: %d, want 404", w.Code)
		}
		if _, err := k.service.Validate(bg, controlKey); err != nil {
			t.Errorf("org A key after the org B attempts: %v", err)
		}
		ownID, _ := k.createKey(t, k.scoped, `{"name":"t2-scoped-own","permissions":["ci:read"]}`)
		if w := k.call(k.scoped, http.MethodPost, "/api/v1/api-keys/"+ownID+"/rotate", ""); w.Code != http.StatusCreated {
			t.Errorf("owner rotates its own key: %d %s, want 201", w.Code, w.Body.String())
		}
		if w := k.call(k.admin, http.MethodPost, "/api/v1/api-keys/"+controlID+"/rotate", ""); w.Code != http.StatusCreated {
			t.Errorf("owner (admin) rotates its own key: %d %s, want 201", w.Code, w.Body.String())
		}
	})
}

func permStrings(perms []identity.Permission) []string {
	out := make([]string, len(perms))
	for i, p := range perms {
		out[i] = string(p)
	}
	return out
}

// TestAuditT2KeyBindingNeverBroadensCreatorScope: a key bound to a service
// account (RBA-08) must never give its creator more than the creator holds:
// a client-1 scoped creator binding a key to an org-wide service account is
// refused (403) or the bound key stays within client 1. An org-wide admin may
// bind (control).
func TestAuditT2KeyBindingNeverBroadensCreatorScope(t *testing.T) {
	k := newT2KeyFixture(t)
	bg := context.Background()
	w := k.call(k.admin, http.MethodPost, "/api/v1/service-accounts", `{"name":"t2-sa-orgwide"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create service account: %d %s", w.Code, w.Body.String())
	}
	var sa user.ServiceAccount
	if err := json.Unmarshal(w.Body.Bytes(), &sa); err != nil {
		t.Fatal(err)
	}
	if rw := k.call(k.admin, http.MethodPut, "/api/v1/service-accounts/"+sa.ID+"/roles", `{"roles":[{"role_id":"`+k.saRole+`"}]}`); rw.Code != http.StatusOK {
		t.Fatalf("assign org-wide role to the service account: %d %s", rw.Code, rw.Body.String())
	}

	w = k.call(k.scoped, http.MethodPost, "/api/v1/api-keys", `{"name":"t2-bound-by-scoped","permissions":["ci:read"],"service_account_id":"`+sa.ID+`"}`)
	switch w.Code {
	case http.StatusForbidden:
		// refused: requirement met
	case http.StatusCreated:
		var bound struct {
			Key string `json:"key"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &bound)
		info, err := k.service.Validate(bg, bound.Key)
		if err != nil {
			t.Fatalf("validate bound key: %v", err)
		}
		all, ids := effectiveClients(info, identity.PermCIRead)
		if all || len(ids) != 1 || ids[0] != k.f.Client1 {
			t.Errorf("client-1 scoped creator bound a key to an org-wide service account: 201, key scope for ci:read org-wide=%v clients=%v; want 403 or a key limited to client 1 (%s)", all, ids, k.f.Client1)
		}
	default:
		t.Errorf("bind by client-scoped creator: %d %s, want 403 (or 201 within the creator's scope)", w.Code, w.Body.String())
	}

	// Control: the org-wide admin binds a key to the same account.
	_, adminBound := k.createKey(t, k.admin, `{"name":"t2-bound-by-admin","permissions":["ci:read"],"service_account_id":"`+sa.ID+`"}`)
	if info, err := k.service.Validate(bg, adminBound); err != nil {
		t.Errorf("control: admin-bound key: %v", err)
	} else if all, _ := effectiveClients(info, identity.PermCIRead); !all {
		t.Errorf("control: admin-bound key to an org-wide account is not org-wide")
	}
}

// effectiveClients is the client dimension a key acts with for perm.
func effectiveClients(info *identity.APIKeyInfo, perm identity.Permission) (bool, []string) {
	if s, ok := info.PermissionScopes[perm]; ok {
		return s.Clients.All, s.Clients.IDs
	}
	if info.Scope == nil {
		// Without a resolved scope the middleware restricts the key by its
		// client scope only (middleware.TenantScopeFor).
		if strings.TrimSpace(info.ClientScope) == "" {
			return true, nil
		}
		return false, strings.Split(info.ClientScope, ",")
	}
	return info.Scope.Clients.All, info.Scope.Clients.IDs
}

// createServiceAccount creates an account as the org-wide admin and assigns
// it the roles body (empty: none).
func (k *t2KeyFixture) createServiceAccount(t *testing.T, name, roles string) string {
	t.Helper()
	w := k.call(k.admin, http.MethodPost, "/api/v1/service-accounts", `{"name":"`+name+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create service account %s: %d %s", name, w.Code, w.Body.String())
	}
	var sa user.ServiceAccount
	if err := json.Unmarshal(w.Body.Bytes(), &sa); err != nil {
		t.Fatal(err)
	}
	if roles != "" {
		if rw := k.call(k.admin, http.MethodPut, "/api/v1/service-accounts/"+sa.ID+"/roles", roles); rw.Code != http.StatusOK {
			t.Fatalf("assign roles to %s: %d %s", name, rw.Code, rw.Body.String())
		}
	}
	return sa.ID
}

// TestAPIKeyRotateRevokeAuthorization covers the rotate and revoke cases of
// WP-354 (AUT-04, API-05): a non-owner without the key's rights in the
// holder's scope gets 403 problem+json and the key stays unchanged; the
// owner rotates with overlap; an org_admin revokes a foreign key; keys bound
// to a service account are checked against the account's scope; a key of
// another organization is 404.
func TestAPIKeyRotateRevokeAuthorization(t *testing.T) {
	k := newT2KeyFixture(t)
	bg := context.Background()
	adminKeyID, adminKey := k.createKey(t, k.admin, `{"name":"t2-admin-key","permissions":["ci:read","ci:delete","user:manage","role:manage"]}`)

	for _, c := range []struct{ name, method, path, body string }{
		{"rotate", http.MethodPost, "/api/v1/api-keys/" + adminKeyID + "/rotate", `{"overlap_seconds":0}`},
		{"revoke", http.MethodDelete, "/api/v1/api-keys/" + adminKeyID, ""},
	} {
		w := k.call(k.scoped, c.method, c.path, c.body)
		if w.Code != http.StatusForbidden || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") {
			t.Errorf("client-scoped {ci:read,apikey:manage} user: %s of the admin key: %d %s (%s), want 403 problem+json",
				c.name, w.Code, w.Body.String(), w.Header().Get("Content-Type"))
		}
		info, err := k.service.Validate(bg, adminKey)
		if err != nil || len(info.Scopes) != 4 {
			t.Errorf("admin key after the refused %s: %+v, %v; want unchanged and valid", c.name, info, err)
		}
	}
	var successors int
	if err := k.f.Admin.QueryRow(bg, `SELECT count(*) FROM api_key WHERE rotated_from = $1`, adminKeyID).Scan(&successors); err != nil || successors != 0 {
		t.Errorf("successors of the admin key after the refused rotation: %d, %v; want 0", successors, err)
	}

	// The owner rotates its own key with overlap: both keys stay valid.
	ownID, ownKey := k.createKey(t, k.scoped, `{"name":"t2-scoped-own","permissions":["ci:read"]}`)
	w := k.call(k.scoped, http.MethodPost, "/api/v1/api-keys/"+ownID+"/rotate", `{"overlap_seconds":3600}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("owner rotates its own key: %d %s, want 201", w.Code, w.Body.String())
	}
	var rotated struct {
		Key struct {
			Key string `json:"key"`
		} `json:"key"`
		Previous struct {
			ExpiresAt *time.Time `json:"expires_at"`
		} `json:"previous"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rotated); err != nil {
		t.Fatal(err)
	}
	if rotated.Previous.ExpiresAt == nil || time.Until(*rotated.Previous.ExpiresAt) < 50*time.Minute {
		t.Errorf("previous key expiry %v, want about one hour of overlap", rotated.Previous.ExpiresAt)
	}
	for name, key := range map[string]string{"successor": rotated.Key.Key, "old key during the overlap": ownKey} {
		if _, err := k.service.Validate(bg, key); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	// An org_admin holds every key permission org-wide and revokes a
	// foreign key.
	if w = k.call(k.orgAdmin, http.MethodDelete, "/api/v1/api-keys/"+adminKeyID, ""); w.Code != http.StatusNoContent {
		t.Errorf("org_admin revokes the admin's key: %d %s, want 204", w.Code, w.Body.String())
	}
	if _, err := k.service.Validate(bg, adminKey); err == nil {
		t.Error("key revoked by the org_admin is still valid")
	}

	// Keys bound to service accounts: the account's scope is the ceiling.
	clientSA := k.createServiceAccount(t, "t2-sa-client1", `{"roles":[{"role_id":"`+k.saRole+`","scope_client_id":"`+k.f.Client1+`"}]}`)
	orgSA := k.createServiceAccount(t, "t2-sa-orgwide-rot", `{"roles":[{"role_id":"`+k.saRole+`"}]}`)
	clientBoundID, _ := k.createKey(t, k.admin, `{"name":"t2-bound-client1","permissions":["ci:read"],"service_account_id":"`+clientSA+`"}`)
	orgBoundID, orgBoundKey := k.createKey(t, k.admin, `{"name":"t2-bound-orgwide","permissions":["ci:read"],"service_account_id":"`+orgSA+`"}`)
	if w = k.call(k.scoped, http.MethodPost, "/api/v1/api-keys/"+orgBoundID+"/rotate", ""); w.Code != http.StatusForbidden {
		t.Errorf("client-scoped user rotates a key of an org-wide service account: %d %s, want 403", w.Code, w.Body.String())
	}
	if _, err := k.service.Validate(bg, orgBoundKey); err != nil {
		t.Errorf("org-wide bound key after the refused rotation: %v", err)
	}
	if w = k.call(k.scoped, http.MethodPost, "/api/v1/api-keys/"+clientBoundID+"/rotate", ""); w.Code != http.StatusCreated {
		t.Errorf("client-scoped user rotates a key of a client-1 service account: %d %s, want 201", w.Code, w.Body.String())
	}
	// A deactivated account's rights are unknown: they are required org-wide.
	if w = k.call(k.admin, http.MethodPatch, "/api/v1/service-accounts/"+clientSA, `{"is_active":false}`); w.Code != http.StatusOK {
		t.Fatalf("deactivate service account: %d %s", w.Code, w.Body.String())
	}
	inactiveBoundID, _ := k.createKey(t, k.admin, `{"name":"t2-bound-inactive","permissions":["ci:read"],"service_account_id":"`+clientSA+`"}`)
	if w = k.call(k.scoped, http.MethodPost, "/api/v1/api-keys/"+inactiveBoundID+"/rotate", ""); w.Code != http.StatusForbidden {
		t.Errorf("client-scoped user rotates a key of a deactivated service account: %d %s, want 403", w.Code, w.Body.String())
	}

	// Another organization's key is 404 for rotate and revoke.
	controlID, controlKey := k.createKey(t, k.admin, `{"name":"t2-admin-control","permissions":["ci:read"]}`)
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/api-keys/" + controlID + "/rotate"},
		{http.MethodDelete, "/api/v1/api-keys/" + controlID},
	} {
		if w = k.call(k.orgB, c.method, c.path, ""); w.Code != http.StatusNotFound {
			t.Errorf("org B %s %s: %d, want 404", c.method, c.path, w.Code)
		}
	}
	if _, err := k.service.Validate(bg, controlKey); err != nil {
		t.Errorf("org A key after the org B attempts: %v", err)
	}
}

// TestAPIKeyBindingAuthorization covers the binding cases of WP-354 (RBA-08):
// the service account must exist in the organization (404), and the key's
// rights through the account must lie within the creator's scope (403);
// within it the key is created and acts only in that scope.
func TestAPIKeyBindingAuthorization(t *testing.T) {
	k := newT2KeyFixture(t)
	bg := context.Background()
	orgSA := k.createServiceAccount(t, "t2-sa-bind-orgwide", `{"roles":[{"role_id":"`+k.saRole+`"}]}`)
	clientSA := k.createServiceAccount(t, "t2-sa-bind-client1", `{"roles":[{"role_id":"`+k.saRole+`","scope_client_id":"`+k.f.Client1+`"}]}`)
	inactiveSA := k.createServiceAccount(t, "t2-sa-bind-inactive", `{"roles":[{"role_id":"`+k.saRole+`","scope_client_id":"`+k.f.Client1+`"}]}`)
	if w := k.call(k.admin, http.MethodPatch, "/api/v1/service-accounts/"+inactiveSA, `{"is_active":false}`); w.Code != http.StatusOK {
		t.Fatalf("deactivate: %d %s", w.Code, w.Body.String())
	}
	if w := k.call(k.orgB, http.MethodPost, "/api/v1/service-accounts", `{"name":"t2-sa-bind-orgb"}`); w.Code != http.StatusCreated {
		t.Fatalf("create org B account: %d %s", w.Code, w.Body.String())
	}
	var orgBSA string
	if err := k.f.Admin.QueryRow(bg, `SELECT id::text FROM service_account WHERE organization_id = $1 AND name = 't2-sa-bind-orgb'`, k.f.OrgB).Scan(&orgBSA); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name string
		p    identity.Principal
		sa   string
		want int
	}{
		{"client-1 creator, org-wide account", k.scoped, orgSA, http.StatusForbidden},
		{"client-1 creator, deactivated client-1 account", k.scoped, inactiveSA, http.StatusForbidden},
		{"unknown account", k.admin, k.f.ID(), http.StatusNotFound},
		{"account of another organization", k.admin, orgBSA, http.StatusNotFound},
		{"malformed account id", k.admin, "not-a-uuid", http.StatusNotFound},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := k.call(c.p, http.MethodPost, "/api/v1/api-keys", `{"name":"t2-bind-refused","permissions":["ci:read"],"service_account_id":"`+c.sa+`"}`)
			if w.Code != c.want || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") {
				t.Errorf("bind: %d %s (%s), want %d problem+json", w.Code, w.Body.String(), w.Header().Get("Content-Type"), c.want)
			}
		})
	}
	var refused int
	if err := k.f.Admin.QueryRow(bg, `SELECT count(*) FROM api_key WHERE name = 't2-bind-refused'`).Scan(&refused); err != nil || refused != 0 {
		t.Errorf("keys created by refused bindings: %d, %v; want 0", refused, err)
	}

	// Within the creator's scope the binding succeeds and the key acts only
	// in client 1.
	_, bound := k.createKey(t, k.scoped, `{"name":"t2-bound-client1","permissions":["ci:read"],"service_account_id":"`+clientSA+`"}`)
	info, err := k.service.Validate(bg, bound)
	if err != nil {
		t.Fatalf("validate bound key: %v", err)
	}
	if all, ids := effectiveClients(info, identity.PermCIRead); all || len(ids) != 1 || ids[0] != k.f.Client1 || info.OwnerID != clientSA {
		t.Errorf("key bound by a client-1 creator: owner %s org-wide=%v clients=%v, want the account and client 1 only", info.OwnerID, all, ids)
	}
}

// TestAPIKeyHandlerFailClosedWithoutResolvers: without the owner and
// service-account resolvers only the owner rotates and revokes, and no key
// is bound to a service account (WP-354).
func TestAPIKeyHandlerFailClosedWithoutResolvers(t *testing.T) {
	store := identity.NewMemoryAPIKeyStore()
	mux := chi.NewRouter()
	identity.NewAPIKeyHandler(store).RegisterRoutes(mux)
	all := identity.AllPermissions()
	owner := identity.Principal{Subject: "owner", OrganizationID: "org", Type: identity.PrincipalTypeUser, Permissions: all}
	admin := identity.Principal{Subject: "admin", OrganizationID: "org", Type: identity.PrincipalTypeUser, Permissions: all}
	call := func(p identity.Principal, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(identity.WithPrincipal(req.Context(), p)))
		return w
	}
	w := call(owner, http.MethodPost, "/api/v1/api-keys", `{"name":"k","permissions":["ci:read"]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/api-keys/" + created.ID + "/rotate"},
		{http.MethodDelete, "/api/v1/api-keys/" + created.ID},
	} {
		if w = call(admin, c.method, c.path, ""); w.Code != http.StatusForbidden {
			t.Errorf("non-owner %s without resolver: %d, want 403", c.method, w.Code)
		}
	}
	if w = call(owner, http.MethodPost, "/api/v1/api-keys", `{"name":"b","permissions":["ci:read"],"service_account_id":"00000000-0000-0000-0000-0000000000aa"}`); w.Code != http.StatusForbidden {
		t.Errorf("binding without resolver: %d, want 403", w.Code)
	}
	if w = call(owner, http.MethodPost, "/api/v1/api-keys/"+created.ID+"/rotate", ""); w.Code != http.StatusCreated {
		t.Errorf("owner rotates without resolver: %d %s, want 201", w.Code, w.Body.String())
	}
	if w = call(owner, http.MethodDelete, "/api/v1/api-keys/"+created.ID, ""); w.Code != http.StatusNoContent {
		t.Errorf("owner revokes without resolver: %d %s, want 204", w.Code, w.Body.String())
	}
}
