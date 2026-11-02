// Revocation of role assignments (WP-395, AUT-02, API-05, RBA-03; finding
// F-017): DELETE /api/v1/role-assignments/{id} removes a standard
// (role_assignment) or custom role assignment (user_custom_role) of the
// organization. The caller needs role:manage and the grant ceiling of POST
// /role-assignments (WP-394); the audit entry role_assignment.revoked is
// written in the same transaction; an unknown id or one of another
// organization is 404; the user loses the rights at the next refresh.
//
// The tests run through the production router (server.NewRouter over the
// PostgreSQL repositories: route authorization, OpenAPI validation, route
// audit, refresh endpoint) behind the production authentication and tenant
// middleware. Every session is started as the login callback leaves it and
// renewed through POST /api/v1/auth/refresh, so its permissions and scope
// come from the stored assignments.
//
// Retest (needs a migrated database):
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -v -run 'TestRoleAssignmentRevo' ./internal/user/
package user_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
)

// revokeMFA records every call of the MFA enforcer.
type revokeMFA struct {
	mu    sync.Mutex
	calls []string
}

func (m *revokeMFA) RequireMFA(_ context.Context, subject string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "require "+subject)
	return nil
}

func (m *revokeMFA) AccountByEmail(_ context.Context, email string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "lookup "+email)
	return "", nil
}

func (m *revokeMFA) recorded() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.calls)
}

// revokeStack is the production router on a scopetest fixture with an
// org-wide org_admin (admin) of organization A and of organization B.
type revokeStack struct {
	*scopetest.Fixture
	t                   *testing.T
	handler             http.Handler
	issuer              *identity.SessionIssuer
	refresh             *identity.RefreshSessions
	mfa                 *revokeMFA
	admin, foreignAdmin string
	serverType          string
}

func newRevokeStack(t *testing.T, tag string) *revokeStack {
	t.Helper()
	f := scopetest.Seed(t, tag)
	store := cache.NewMemoryStore()
	revocations := identity.NewSessionRevocations(store)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := identity.NewSessionIssuer(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	if err != nil {
		t.Fatal(err)
	}
	issuer = issuer.WithRevocations(revocations)
	refresh := identity.NewRefreshSessions(store, revocations)
	masterKey, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{})
	mfa := &revokeMFA{}
	mux, _, err := server.NewRouter(repos, server.Options{
		Version: "wp-395", Entitlements: entitlements,
		CIService:   ci.NewServiceWithLimits(repos.CI, entitlements).WithFieldResolver(repos.CIType),
		Credentials: credential.NewService(repos.Credential, encryptor),
		OIDC:        identity.NewOIDCProvider(identity.OIDCConfig{}),
		Sessions:    issuer, RefreshSessions: refresh,
		AuditPool: f.App, MFA: mfa,
		RouteMiddleware: []func(http.Handler) http.Handler{middleware.OpenAPIValidation},
	})
	if err != nil {
		t.Fatalf("build router: %v", err)
	}
	s := &revokeStack{Fixture: f, t: t, issuer: issuer, refresh: refresh, mfa: mfa,
		handler: middleware.Chain(middleware.RequestID, middleware.AuthMiddlewareWithVerifier(issuer), middleware.TenantMiddleware)(mux)}
	s.admin = f.AppUser(t, f.OrgA, "revoke-admin")
	s.grant(f.OrgA, s.admin, "org_admin", "")
	s.foreignAdmin = f.AppUser(t, f.OrgB, "revoke-foreign-admin")
	s.grant(f.OrgB, s.foreignAdmin, "org_admin", "")
	s.serverType = s.str(`SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`)
	return s
}

func (s *revokeStack) str(sql string, args ...any) string {
	s.t.Helper()
	var v string
	if err := s.Admin.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		s.t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func (s *revokeStack) count(sql string, args ...any) int {
	s.t.Helper()
	var n int
	if err := s.Admin.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		s.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func (s *revokeStack) role(orgID, name string) string {
	return s.str(`SELECT id::text FROM role WHERE organization_id = $1 AND name = $2`, orgID, name)
}

// grant stores a standard role assignment, in clientID when not empty.
func (s *revokeStack) grant(orgID, userID, roleName, clientID string) string {
	var client any
	if clientID != "" {
		client = clientID
	}
	return s.str(`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id) VALUES ($1, $2, $3, $4) RETURNING id::text`,
		orgID, userID, s.role(orgID, roleName), client)
}

// customRole stores a custom role of organization A with perms.
func (s *revokeStack) customRole(name string, perms string) string {
	id := s.str(`INSERT INTO custom_role (organization_id, name, permissions) VALUES ($1, $2, $3::jsonb) RETURNING id::text`, s.OrgA, name, perms)
	s.t.Cleanup(func() {
		// custom_role has no ON DELETE CASCADE to organization.
		_, _ = s.Admin.Exec(context.Background(), `DELETE FROM user_custom_role WHERE custom_role_id = $1`, id)
		_, _ = s.Admin.Exec(context.Background(), `DELETE FROM custom_role WHERE id = $1`, id)
	})
	return id
}

// site stores a site of clientID in organization A.
func (s *revokeStack) site(clientID, name string) string {
	return s.str(`INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, $3) RETURNING id::text`, s.OrgA, clientID, name)
}

// login starts a session of userID as the login callback leaves it (a
// refresh family) and renews it once, so the token carries the stored
// assignments.
func (s *revokeStack) login(orgID, userID string) (token, cookie string) {
	s.t.Helper()
	cookie, err := s.refresh.Issue(context.Background(), &identity.RefreshGrant{Claims: identity.SessionClaims{Subject: userID, OrganizationID: orgID}})
	if err != nil {
		s.t.Fatalf("start refresh family: %v", err)
	}
	return s.renew(cookie)
}

// renew is the refresh of the SPA: POST /api/v1/auth/refresh with the cookie
// only. It returns the new access token and the rotated cookie.
func (s *revokeStack) renew(cookie string) (token, next string) {
	s.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	req.AddCookie(&http.Cookie{Name: identity.RefreshCookieName, Value: cookie})
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, req)
	var body struct {
		Token string `json:"token"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Token == "" {
		s.t.Fatalf("POST /api/v1/auth/refresh = %d %.200s", w.Code, w.Body.String())
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == identity.RefreshCookieName {
			next = c.Value
		}
	}
	if next == "" {
		s.t.Fatal("refresh rotated no cookie")
	}
	return body.Token, next
}

// holds reports whether the access token grants perm.
func (s *revokeStack) holds(token string, perm identity.Permission) bool {
	s.t.Helper()
	claims, err := s.issuer.Validate(token)
	if err != nil {
		s.t.Fatalf("validate access token: %v", err)
	}
	return slices.Contains(claims.Permissions, perm)
}

func (s *revokeStack) do(token, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, req)
	return w
}

// assign is POST /role-assignments; it requires 201 and returns the id.
func (s *revokeStack) assign(token, body string) string {
	s.t.Helper()
	w := s.do(token, http.MethodPost, "/api/v1/role-assignments", body)
	var a struct {
		ID string `json:"id"`
	}
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &a) != nil || a.ID == "" {
		s.t.Fatalf("POST /role-assignments %s = %d %.200s", body, w.Code, w.Body.String())
	}
	return a.ID
}

func (s *revokeStack) revoke(token, id string) *httptest.ResponseRecorder {
	return s.do(token, http.MethodDelete, "/api/v1/role-assignments/"+id, "")
}

// roles lists the role or custom role ids of the assignments of userID via
// GET /role-assignments?user_id= as the admin of organization A.
func (s *revokeStack) roles(adminToken, userID string) []string {
	s.t.Helper()
	w := s.do(adminToken, http.MethodGet, "/api/v1/role-assignments?user_id="+userID, "")
	var list struct {
		Data []struct {
			RoleID       string `json:"role_id"`
			CustomRoleID string `json:"custom_role_id"`
		} `json:"data"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &list) != nil {
		s.t.Fatalf("GET /role-assignments?user_id=%s = %d %.200s", userID, w.Code, w.Body.String())
	}
	out := make([]string, 0, len(list.Data))
	for _, a := range list.Data {
		out = append(out, a.RoleID+a.CustomRoleID)
	}
	slices.Sort(out)
	return out
}

// stored reports whether the assignment id is still stored in either table.
func (s *revokeStack) stored(id string) bool {
	return s.count(`SELECT (SELECT count(*) FROM role_assignment WHERE id = $1)::int
		+ (SELECT count(*) FROM user_custom_role WHERE id = $1)::int`, id) > 0
}

// audits counts the audit entries of both organizations.
func (s *revokeStack) audits() int {
	return s.count(`SELECT count(*)::int FROM audit_log WHERE organization_id IN ($1, $2)`, s.OrgA, s.OrgB)
}

// revocation is the audit entry role_assignment.revoked of an assignment.
type revocation struct {
	Count                             int
	Actor, ActorType, ResourceType    string
	Table, User, Role, RoleID, Custom string
	ScopeType, ScopeID                string
}

func (s *revokeStack) revocation(id string) revocation {
	s.t.Helper()
	var r revocation
	if err := s.Admin.QueryRow(context.Background(), `
		SELECT count(*) OVER ()::int, COALESCE(actor_id::text, ''), actor_type, resource_type,
		       COALESCE(changes->>'table', ''), COALESCE(changes->>'user_id', ''), COALESCE(changes->>'role', ''),
		       COALESCE(changes->>'role_id', ''), COALESCE(changes->>'custom_role_id', ''),
		       COALESCE(changes->>'scope_type', ''), COALESCE(changes->>'scope_id', '')
		  FROM audit_log WHERE organization_id = $1 AND action = 'role_assignment.revoked' AND resource_id = $2::uuid
		 LIMIT 1`, s.OrgA, id).Scan(&r.Count, &r.Actor, &r.ActorType, &r.ResourceType, &r.Table, &r.User, &r.Role,
		&r.RoleID, &r.Custom, &r.ScopeType, &r.ScopeID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.t.Fatalf("read revocation audit: %v", err)
	}
	return r
}

// problem reports whether w is an RFC 7807 problem with the status code.
func problem(w *httptest.ResponseRecorder, code int) bool {
	return w.Code == code && strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json")
}

// TestRoleAssignmentRevokeTakesEffectAtNextRefresh covers WP-395 (AUT-02,
// API-05; F-017, retest of roles-authz 13 at the API): the org_admin
// assigns engineer to a viewer; the viewer's refreshed session holds
// ci:write and creates a CI. DELETE /role-assignments/{id} answers 204 with
// one audit entry role_assignment.revoked (admin, role_assignment, engineer,
// organization scope) and no route entry besides; GET /role-assignments
// lists the viewer role only; the next refresh carries no ci:write and POST
// /cis is 403. The same for a custom role assignment (user_custom_role):
// its ci:write and ci:delete are gone at the next refresh. A second DELETE is
// 404 and audits nothing.
func TestRoleAssignmentRevokeTakesEffectAtNextRefresh(t *testing.T) {
	s := newRevokeStack(t, "1a")
	adminToken, _ := s.login(s.OrgA, s.admin)
	viewer, engineer := s.role(s.OrgA, "viewer"), s.role(s.OrgA, "engineer")
	createCI := func(token, name string) int {
		return s.do(token, http.MethodPost, "/api/v1/cis",
			`{"name":"`+name+`","ci_type_id":"`+s.serverType+`","status":"active","discovery_source":"manual"}`).Code
	}

	t.Run("standard role", func(t *testing.T) {
		target := s.AppUser(t, s.OrgA, "revoke-target")
		s.grant(s.OrgA, target, "viewer", "")
		id := s.assign(adminToken, `{"user_id":"`+target+`","role_id":"`+engineer+`"}`)
		token, cookie := s.login(s.OrgA, target)
		if !s.holds(token, identity.PermCIWrite) {
			t.Fatal("control: the engineer session holds no ci:write")
		}
		if code := createCI(token, "wp395-before-revoke"); code != http.StatusCreated {
			t.Fatalf("control: POST /cis as engineer = %d, want 201", code)
		}

		audits := s.audits()
		w := s.revoke(adminToken, id)
		if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
			t.Fatalf("DELETE /role-assignments/{id} = %d %.200s, want 204 without body", w.Code, w.Body.String())
		}
		if got := s.roles(adminToken, target); !slices.Equal(got, []string{viewer}) {
			t.Errorf("GET /role-assignments after the revocation = %v, want only viewer %s", got, viewer)
		}
		if s.stored(id) {
			t.Error("the engineer assignment is still stored")
		}
		rec := s.revocation(id)
		if rec.Count != 1 || rec.Actor != s.admin || rec.ActorType != "user" || rec.ResourceType != "role_assignment" ||
			rec.Table != "role_assignment" || rec.User != target || rec.Role != "engineer" || rec.RoleID != engineer ||
			rec.Custom != "" || rec.ScopeType != "organization" || rec.ScopeID != "" {
			t.Errorf("audit role_assignment.revoked = %+v", rec)
		}
		if n := s.audits() - audits; n != 1 {
			t.Errorf("the revocation wrote %d audit entries, want exactly role_assignment.revoked", n)
		}

		token, _ = s.renew(cookie)
		if s.holds(token, identity.PermCIWrite) || !s.holds(token, identity.PermCIRead) {
			t.Errorf("next refresh: ci:write %v, ci:read %v; want only the viewer rights", s.holds(token, identity.PermCIWrite), s.holds(token, identity.PermCIRead))
		}
		if code := createCI(token, "wp395-after-revoke"); code != http.StatusForbidden {
			t.Errorf("POST /cis after the refresh = %d, want 403", code)
		}

		audits = s.audits()
		if w = s.revoke(adminToken, id); !problem(w, http.StatusNotFound) || s.audits() != audits {
			t.Errorf("second DELETE = %d %q, %d audit entries; want 404 problem and none", w.Code, w.Header().Get("Content-Type"), s.audits()-audits)
		}
	})

	t.Run("custom role", func(t *testing.T) {
		custom := s.customRole("revoke-custom", `["ci:read","ci:write","ci:delete"]`)
		target := s.AppUser(t, s.OrgA, "revoke-custom-target")
		s.grant(s.OrgA, target, "viewer", "")
		id := s.assign(adminToken, `{"user_id":"`+target+`","custom_role_id":"`+custom+`"}`)
		if got := s.roles(adminToken, target); !slices.Equal(got, sorted(viewer, custom)) {
			t.Fatalf("control: GET /role-assignments = %v, want viewer and the custom role", got)
		}
		token, cookie := s.login(s.OrgA, target)
		if !s.holds(token, identity.PermCIWrite) || !s.holds(token, identity.PermCIDelete) {
			t.Fatal("control: the custom role session holds no ci:write and ci:delete")
		}

		if w := s.revoke(adminToken, id); w.Code != http.StatusNoContent {
			t.Fatalf("DELETE /role-assignments/{id} of the custom role = %d %.200s, want 204", w.Code, w.Body.String())
		}
		if got := s.roles(adminToken, target); !slices.Equal(got, []string{viewer}) {
			t.Errorf("GET /role-assignments after the revocation = %v, want only viewer", got)
		}
		rec := s.revocation(id)
		if s.stored(id) || rec.Count != 1 || rec.Actor != s.admin || rec.Table != "user_custom_role" || rec.User != target ||
			rec.Role != "revoke-custom" || rec.Custom != custom || rec.RoleID != "" || rec.ScopeType != "organization" {
			t.Errorf("custom role revocation: stored %v, audit %+v", s.stored(id), rec)
		}
		token, _ = s.renew(cookie)
		if s.holds(token, identity.PermCIWrite) || s.holds(token, identity.PermCIDelete) {
			t.Error("next refresh still carries the rights of the revoked custom role")
		}
		if code := createCI(token, "wp395-after-custom-revoke"); code != http.StatusForbidden {
			t.Errorf("POST /cis after the refresh = %d, want 403", code)
		}
	})
}

// TestRoleAssignmentRevokeRefusals covers WP-395 (TEN-04, RBA-01, RBA-03,
// API-05; F-017): an org_admin of organization B revokes no assignment of
// organization A (404 for a standard and a custom role assignment, as for an
// unknown or malformed id); a caller without role:manage (viewer, engineer)
// gets 403; a client-1 delegate (org_admin with scope_client_id = client 1)
// revoking an org-wide assignment gets 403. Every refusal is RFC 7807, keeps
// the assignment and writes no audit entry.
func TestRoleAssignmentRevokeRefusals(t *testing.T) {
	s := newRevokeStack(t, "08")
	target := s.AppUser(t, s.OrgA, "refusal-target")
	standard := s.grant(s.OrgA, target, "engineer", "")
	custom := s.customRole("refusal-custom", `["ci:read"]`)
	customAssignment := s.str(`INSERT INTO user_custom_role (user_id, custom_role_id) VALUES ($1, $2) RETURNING id::text`, target, custom)
	viewerUser := s.AppUser(t, s.OrgA, "refusal-viewer")
	s.grant(s.OrgA, viewerUser, "viewer", "")
	engineerUser := s.AppUser(t, s.OrgA, "refusal-engineer")
	s.grant(s.OrgA, engineerUser, "engineer", "")
	delegate := s.AppUser(t, s.OrgA, "refusal-delegate")
	s.grant(s.OrgA, delegate, "org_admin", s.Client1)

	foreignToken, _ := s.login(s.OrgB, s.foreignAdmin)
	viewerToken, _ := s.login(s.OrgA, viewerUser)
	engineerToken, _ := s.login(s.OrgA, engineerUser)
	delegateToken, _ := s.login(s.OrgA, delegate)
	if !s.holds(delegateToken, identity.PermRoleManage) || !s.holds(engineerToken, identity.PermRoleRead) || s.holds(engineerToken, identity.PermRoleManage) {
		t.Fatal("fixture: the delegate needs role:manage, the engineer role:read without role:manage")
	}

	const (
		notFound = "role assignment not found"
		noRight  = "missing required permission: role:manage"
		ceiling  = "revoking the role assignment requires "
	)
	for _, tc := range []struct {
		name, token, id string
		code            int
		detail          string
	}{
		{"organization B admin, standard role assignment of A", foreignToken, standard, http.StatusNotFound, notFound},
		{"organization B admin, custom role assignment of A", foreignToken, customAssignment, http.StatusNotFound, notFound},
		{"organization A admin, unknown id", "", "c0de1a00-0000-4000-8000-00000000dead", http.StatusNotFound, notFound},
		{"organization A admin, malformed id", "", "not-a-uuid", http.StatusNotFound, notFound},
		{"viewer without role:manage", viewerToken, standard, http.StatusForbidden, noRight},
		{"engineer without role:manage", engineerToken, customAssignment, http.StatusForbidden, noRight},
		{"client-1 delegate, org-wide standard role assignment", delegateToken, standard, http.StatusForbidden, ceiling},
		{"client-1 delegate, org-wide custom role assignment", delegateToken, customAssignment, http.StatusForbidden, ceiling},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := tc.token
			if token == "" {
				token, _ = s.login(s.OrgA, s.admin)
			}
			audits := s.audits()
			w := s.revoke(token, tc.id)
			if !problem(w, tc.code) || !strings.Contains(w.Body.String(), tc.detail) || s.audits() != audits {
				t.Errorf("DELETE /role-assignments/%s = %d %q, %d new audit entries; want %d problem+json (%q) and none; body %.200s",
					tc.id, w.Code, w.Header().Get("Content-Type"), s.audits()-audits, tc.code, tc.detail, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "SQLSTATE") || strings.Contains(w.Body.String(), "pgx") {
				t.Errorf("the answer names database internals: %.200s", w.Body.String())
			}
		})
	}
	if !s.stored(standard) || !s.stored(customAssignment) {
		t.Errorf("after the refusals: standard stored %v, custom stored %v; want both", s.stored(standard), s.stored(customAssignment))
	}
}

// TestRoleAssignmentRevokeGrantCeilingSymmetry covers WP-395 (RBA-03,
// F-017, symmetric to WP-394): revoking uses the grant ceiling of POST
// /role-assignments. The client-1 delegate revokes what it may assign:
// viewer in client 1, viewer at a site of client 1 and a custom role in
// client 1, each assigned by the delegate itself first (201) and revoked
// with 204, the row removed and role_assignment.revoked naming the delegate
// and the scope. It may not revoke viewer org-wide, in client 2 or at a
// site of client 2 (403, row stays, no audit entry), like it may not assign
// them. Revoking org_admin calls the Keycloak MFA binding neither way: the
// account keeps mfa_required (WP-358), and the org_admin who remains keeps
// role:manage.
func TestRoleAssignmentRevokeGrantCeilingSymmetry(t *testing.T) {
	s := newRevokeStack(t, "87")
	viewer := s.role(s.OrgA, "viewer")
	custom := s.customRole("symmetry-custom", `["ci:read","ci:write"]`)
	site1, site2 := s.site(s.Client1, "symmetry-site-1"), s.site(s.Client2, "symmetry-site-2")
	delegate := s.AppUser(t, s.OrgA, "symmetry-delegate")
	s.grant(s.OrgA, delegate, "org_admin", s.Client1)
	delegateToken, _ := s.login(s.OrgA, delegate)
	adminToken, _ := s.login(s.OrgA, s.admin)
	target := s.AppUser(t, s.OrgA, "symmetry-target")
	scoped := func(role, field, scopeType, scopeID string) string {
		return `{"user_id":"` + target + `","` + field + `":"` + role + `","scope_type":"` + scopeType + `","scope_id":"` + scopeID + `"}`
	}

	for _, tc := range []struct{ name, body, scopeType, scopeID string }{
		{"viewer in client 1", scoped(viewer, "role_id", "client", s.Client1), "client", s.Client1},
		{"viewer at a site of client 1", scoped(viewer, "role_id", "site", site1), "site", site1},
		{"custom role in client 1", scoped(custom, "custom_role_id", "client", s.Client1), "client", s.Client1},
	} {
		t.Run("delegate revokes "+tc.name, func(t *testing.T) {
			id := s.assign(delegateToken, tc.body)
			if w := s.revoke(delegateToken, id); w.Code != http.StatusNoContent {
				t.Fatalf("DELETE = %d %.200s, want 204", w.Code, w.Body.String())
			}
			rec := s.revocation(id)
			if s.stored(id) || rec.Count != 1 || rec.Actor != delegate || rec.ScopeType != tc.scopeType || rec.ScopeID != tc.scopeID {
				t.Errorf("stored %v, audit %+v; want the row removed and role_assignment.revoked by the delegate in %s %s",
					s.stored(id), rec, tc.scopeType, tc.scopeID)
			}
		})
	}

	for _, tc := range []struct{ name, body string }{
		{"viewer org-wide", `{"user_id":"` + target + `","role_id":"` + viewer + `"}`},
		{"viewer in client 2", scoped(viewer, "role_id", "client", s.Client2)},
		{"viewer at a site of client 2", scoped(viewer, "role_id", "site", site2)},
		{"custom role org-wide", `{"user_id":"` + target + `","custom_role_id":"` + custom + `"}`},
	} {
		t.Run("delegate may neither assign nor revoke "+tc.name, func(t *testing.T) {
			if w := s.do(delegateToken, http.MethodPost, "/api/v1/role-assignments", tc.body); !problem(w, http.StatusForbidden) {
				t.Errorf("control: POST as delegate = %d, want 403", w.Code)
			}
			id := s.assign(adminToken, tc.body)
			audits := s.audits()
			if w := s.revoke(delegateToken, id); !problem(w, http.StatusForbidden) || !strings.Contains(w.Body.String(), "revoking the role assignment requires ") ||
				!s.stored(id) || s.audits() != audits {
				t.Errorf("DELETE as delegate = %d, stored %v, %d new audit entries; want 403, the row and no entry; body %.200s",
					w.Code, s.stored(id), s.audits()-audits, w.Body.String())
			}
			if w := s.revoke(adminToken, id); w.Code != http.StatusNoContent || s.stored(id) {
				t.Errorf("DELETE as org-wide org_admin = %d, stored %v; want 204 and the row removed", w.Code, s.stored(id))
			}
		})
	}

	t.Run("org_admin revocation keeps MFA", func(t *testing.T) {
		promoted := s.AppUser(t, s.OrgA, "symmetry-promoted")
		id := s.assign(adminToken, `{"user_id":"`+promoted+`","role_id":"`+s.role(s.OrgA, "org_admin")+`"}`)
		bound := s.mfa.recorded()
		if len(bound) != 1 || bound[0] != "require scopetest-87-symmetry-promoted" {
			t.Fatalf("control: MFA calls of the org_admin assignment = %v, want one binding", bound)
		}
		token, cookie := s.login(s.OrgA, promoted)
		if !s.holds(token, identity.PermRoleManage) {
			t.Fatal("control: the promoted org_admin holds no role:manage")
		}
		if w := s.revoke(adminToken, id); w.Code != http.StatusNoContent {
			t.Fatalf("DELETE of the org_admin assignment = %d %.200s, want 204", w.Code, w.Body.String())
		}
		if calls := s.mfa.recorded(); !slices.Equal(calls, bound) {
			t.Errorf("MFA calls after the revocation = %v, want none beyond the binding %v", calls, bound)
		}
		if token, _ = s.renew(cookie); s.holds(token, identity.PermRoleManage) || s.holds(token, identity.PermUserManage) {
			t.Error("next refresh of the revoked org_admin still carries role:manage or user:manage")
		}
		if token, _ = s.login(s.OrgA, s.admin); !s.holds(token, identity.PermRoleManage) {
			t.Error("the remaining org_admin lost role:manage")
		}
	})
}

func sorted(ids ...string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}
