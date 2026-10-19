//go:build auditdefect

// Phase-1 acceptance audit, group T2-auth-authz: defect tests of the
// api-keys resource against PostgreSQL. Each test encodes a requirement the
// product currently violates and fails on an assertion until the finding is
// fixed.
//
//   - WP-067 (AUT-04, API-05, RBA-03), finding P1 "API key rotation/revocation
//     by a non-owner: privilege escalation to the owner's rights and
//     takeover/kill of foreign keys"; gap (critical) "Authorization of rotate
//     and revoke across users and scopes".
//   - WP-069 (RBA-08, RBA-03, API-05), finding P1 "Service-account binding
//     and role assignment are not authorized against the caller's rights and
//     scope (escalation)": binding part (TestAuditT2KeyBindingNeverBroadens
//     CreatorScope) and handler part of the role assignment
//     (TestAuditT2ServiceAccountRolesWithinCallerRights; the route part,
//     user:manage instead of role:manage, is in
//     internal/server/t2_auth_chain_auditdefect_test.go); gap (critical)
//     "Authorization of SA binding and SA role assignment".
//
// Principals are resolved from their stored role assignments exactly as at
// login (permission.PGRepository.AccessGrants + identity.ResolveAccess), and
// the request scope is built by middleware.TenantScopeFor.
//
// Retest (needs a migrated database):
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_02?sslmode=disable go test -tags auditdefect -count=1 -v -run 'TestAuditT2' ./internal/identity/
package identity_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

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
	saRole  string
	saRepo  *user.PGServiceAccounts
	resolve func(orgID, userID string) identity.Principal
	exec    func(sql string, args ...any)
}

func newT2KeyFixture(t *testing.T) *t2KeyFixture {
	t.Helper()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Fatal("TEST_DATABASE_URL is required for this audit test")
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
	adminRole, scopedRole, orgBRole, saRole := f.ID(), f.ID(), f.ID(), f.ID()
	exec(`INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 't2-key-admin', '["ci:read","ci:delete","apikey:manage","user:manage","user:read","role:manage"]')`, adminRole, f.OrgA)
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, adminID, adminRole)
	exec(`INSERT INTO role (id, organization_id, name, permissions, scope) VALUES ($1, $2, 't2-key-scoped', '["ci:read","apikey:manage"]', 'client')`, scopedRole, f.OrgA)
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id) VALUES ($1, $2, $3, $4)`, f.OrgA, scopedID, scopedRole, f.Client1)
	exec(`INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 't2-key-orgb', '["ci:read","ci:delete","apikey:manage","user:manage","role:manage"]')`, orgBRole, f.OrgB)
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgB, orgBID, orgBRole)
	exec(`INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 't2-key-sa', '["ci:read"]')`, saRole, f.OrgA)

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
	identity.NewAPIKeyHandler(keys).RegisterRoutes(mux)
	user.NewServiceAccountHandler(saRepo).RegisterRoutes(mux)
	return &t2KeyFixture{
		f: f, mux: mux, saRole: saRole, saRepo: saRepo, resolve: resolve, exec: exec,
		service: identity.NewAPIKeyServiceWithStore(keys).WithOwnerAccess(perms).
			WithServiceAccountAccess(user.ServiceAccountAccess{Repo: saRepo}),
		admin:  resolve(f.OrgA, adminID),
		scoped: resolve(f.OrgA, scopedID),
		orgB:   resolve(f.OrgB, orgBID),
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

// TestAuditT2ServiceAccountRolesWithinCallerRights: service accounts are on a
// par with users (RBA-08); whoever assigns their roles may grant only rights
// they hold, in their own scope (RBA-03). A client-1 scoped caller holding
// user:manage and role:manage (so the route check passes even after the
// route fix) must not give a service account an org-wide role with
// apikey:manage and webhook:manage, nor that role limited to client 1. The
// org-wide administrator assigning a role within its rights is the control.
func TestAuditT2ServiceAccountRolesWithinCallerRights(t *testing.T) {
	k := newT2KeyFixture(t)
	bg := context.Background()
	callerID := k.f.AppUser(t, k.f.OrgA, "t2-sa-role-scoped")
	callerRole, powerful := k.f.ID(), k.f.ID()
	k.exec(`INSERT INTO role (id, organization_id, name, permissions, scope) VALUES ($1, $2, 't2-sa-role-scoped', '["user:read","user:manage","role:manage"]', 'client')`, callerRole, k.f.OrgA)
	k.exec(`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id) VALUES ($1, $2, $3, $4)`, k.f.OrgA, callerID, callerRole, k.f.Client1)
	k.exec(`INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 't2-sa-powerful', '["ci:read","user:manage","role:manage","apikey:manage","webhook:manage"]')`, powerful, k.f.OrgA)
	caller := k.resolve(k.f.OrgA, callerID)
	resetScope := database.OrgWideScope(k.f.OrgA, "")
	resetCtx := database.ContextWithTenantScope(bg, &resetScope)

	w := k.call(caller, http.MethodPost, "/api/v1/service-accounts", `{"name":"t2-sa-minted-by-scoped"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create service account as the scoped caller: %d %s", w.Code, w.Body.String())
	}
	var sa user.ServiceAccount
	if err := json.Unmarshal(w.Body.Bytes(), &sa); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, body string }{
		{"org-wide powerful role", `{"roles":[{"role_id":"` + powerful + `"}]}`},
		{"powerful role limited to client 1", `{"roles":[{"role_id":"` + powerful + `","scope_client_id":"` + k.f.Client1 + `"}]}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			rw := k.call(caller, http.MethodPut, "/api/v1/service-accounts/"+sa.ID+"/roles", c.body)
			grants, err := k.saRepo.ServiceAccountGrants(bg, k.f.OrgA, sa.ID)
			if err != nil {
				t.Fatalf("service account grants: %v", err)
			}
			if rw.Code != http.StatusForbidden || len(grants) != 0 {
				var granted []string
				for _, g := range grants {
					granted = append(granted, permStrings(g.Permissions)...)
				}
				t.Errorf("client-1 scoped caller {user:read,user:manage,role:manage} assigns the %s to a service account: %d, account now grants %v; want 403 and no grants (never grant more than the caller holds)",
					c.name, rw.Code, granted)
			}
			// Reset for the next case.
			if _, err := k.saRepo.SetServiceAccountRoles(resetCtx, k.f.OrgA, sa.ID, nil); err != nil {
				t.Fatalf("reset roles: %v", err)
			}
		})
	}

	// Control: the org-wide admin assigns a role within its rights.
	w = k.call(k.admin, http.MethodPost, "/api/v1/service-accounts", `{"name":"t2-sa-by-admin"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("control: create service account: %d %s", w.Code, w.Body.String())
	}
	var adminSA user.ServiceAccount
	_ = json.Unmarshal(w.Body.Bytes(), &adminSA)
	if w := k.call(k.admin, http.MethodPut, "/api/v1/service-accounts/"+adminSA.ID+"/roles", `{"roles":[{"role_id":"`+k.saRole+`"}]}`); w.Code != http.StatusOK {
		t.Errorf("control: org-wide admin assigns {ci:read} to a service account: %d %s, want 200", w.Code, w.Body.String())
	}
}
