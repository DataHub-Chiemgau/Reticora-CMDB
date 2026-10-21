package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	scopeOrg     = "123e4567-e89b-12d3-a456-426614174000"
	scopeClientA = "aaaaaaaa-0000-4000-8000-000000000001"
	scopeClientB = "bbbbbbbb-0000-4000-8000-000000000002"
	scopeSiteA   = "cccccccc-0000-4000-8000-000000000003"
	scopeSiteB   = "dddddddd-0000-4000-8000-000000000004"
)

func TestResolveAccessUnitesScopesPerDimension(t *testing.T) {
	all := ScopeSet{All: true}
	tests := []struct {
		name   string
		grants []Grant
		want   Scope
	}{
		{name: "no grant grants nothing", grants: nil, want: Scope{}},
		{
			name:   "org-wide grant",
			grants: []Grant{OrgWideGrant([]Permission{PermCIRead})},
			want:   Scope{Clients: all, Sites: all, Teams: all},
		},
		{
			name:   "client-only grant restricts clients only",
			grants: []Grant{{Permissions: []Permission{PermCIRead}, Clients: []string{scopeClientA}}},
			want:   Scope{Clients: ScopeSet{IDs: []string{scopeClientA}}, Sites: all, Teams: all},
		},
		{
			name: "client grants are united",
			grants: []Grant{
				{Permissions: []Permission{PermCIRead}, Clients: []string{scopeClientB}},
				{Permissions: []Permission{PermCIWrite}, Clients: []string{strings.ToUpper(scopeClientA)}},
			},
			want: Scope{Clients: ScopeSet{IDs: []string{scopeClientA, scopeClientB}}, Sites: all, Teams: all},
		},
		{
			name: "an org-wide grant sets every dimension to all",
			grants: []Grant{
				{Permissions: []Permission{PermCIRead}, Clients: []string{scopeClientA}},
				OrgWideGrant([]Permission{PermCIRead}),
			},
			want: Scope{Clients: all, Sites: all, Teams: all},
		},
		{
			name: "client and site grants restrict both dimensions (fail-closed)",
			grants: []Grant{
				{Permissions: []Permission{PermCIRead}, Clients: []string{scopeClientA}},
				{Permissions: []Permission{PermCIRead}, Sites: []string{scopeSiteA}},
			},
			want: Scope{Clients: ScopeSet{IDs: []string{scopeClientA}}, Sites: ScopeSet{IDs: []string{scopeSiteA}}, Teams: all},
		},
		{
			name:   "site grant is confined to the client of its site (WP-390)",
			grants: []Grant{{Permissions: []Permission{PermCIRead}, Clients: []string{scopeClientA}, Sites: []string{scopeSiteA}}},
			want:   Scope{Clients: ScopeSet{IDs: []string{scopeClientA}}, Sites: ScopeSet{IDs: []string{scopeSiteA}}, Teams: all},
		},
		{
			name: "site grants of two clients unite clients and sites",
			grants: []Grant{
				{Permissions: []Permission{PermCIRead}, Clients: []string{scopeClientA}, Sites: []string{scopeSiteA}},
				{Permissions: []Permission{PermCIRead}, Clients: []string{scopeClientB}, Sites: []string{scopeSiteB}},
			},
			want: Scope{Clients: ScopeSet{IDs: []string{scopeClientA, scopeClientB}}, Sites: ScopeSet{IDs: []string{scopeSiteA, scopeSiteB}}, Teams: all},
		},
		{
			name:   "sites without clients restrict the clients to none (E-08)",
			grants: []Grant{{Permissions: []Permission{PermCIRead}, Sites: []string{scopeSiteA}}},
			want:   Scope{Clients: ScopeSet{}, Sites: ScopeSet{IDs: []string{scopeSiteA}}, Teams: all},
		},
		{
			name: "an org-wide grant sets every dimension to all beside a site grant",
			grants: []Grant{
				{Permissions: []Permission{PermCIRead}, Clients: []string{scopeClientA}, Sites: []string{scopeSiteA}},
				OrgWideGrant([]Permission{PermCIRead}),
			},
			want: Scope{Clients: all, Sites: all, Teams: all},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveAccess(tt.grants).Scope
			if !got.equal(&tt.want) {
				t.Fatalf("scope = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestResolveAccessSiteGrantScopesEveryPermission covers WP-390 (RBA-03,
// TEN-09, F-014): every permission of a site grant is valid in the client and
// site of the grant only, also as the narrower scope beside an org-wide grant;
// a grant with sites but without clients holds its permissions in no client,
// so its legacy client scope is NoScopeID.
func TestResolveAccessSiteGrantScopesEveryPermission(t *testing.T) {
	site := Scope{Clients: ScopeSet{IDs: []string{scopeClientA}}, Sites: ScopeSet{IDs: []string{scopeSiteA}}, Teams: ScopeSet{All: true}}
	siteGrant := Grant{Permissions: []Permission{PermCIRead, PermCIWrite}, Clients: []string{scopeClientA}, Sites: []string{scopeSiteA}}

	access := ResolveAccess([]Grant{siteGrant})
	if !access.Scope.equal(&site) || access.Scope.LegacyClientScope() != scopeClientA {
		t.Fatalf("scope = %+v (legacy %q), want client A at site A", access.Scope, access.Scope.LegacyClientScope())
	}
	p := Principal{Scope: &access.Scope, PermissionScopes: access.PermissionScopes, Permissions: access.Permissions}
	for _, perm := range []Permission{PermCIRead, PermCIWrite} {
		if s, ok := p.ScopeFor(perm); !ok || !s.equal(&site) {
			t.Errorf("%s scope = %+v %v, want client A at site A", perm, s, ok)
		}
	}

	mixed := ResolveAccess([]Grant{OrgWideGrant([]Permission{PermCIRead}), siteGrant})
	if !mixed.Scope.OrgWide() {
		t.Fatalf("union scope beside an org-wide grant = %+v, want org-wide", mixed.Scope)
	}
	if s, ok := mixed.PermissionScopes[PermCIWrite]; !ok || !s.equal(&site) {
		t.Errorf("ci:write scope = %+v %v, want client A at site A", s, ok)
	}
	if _, ok := mixed.PermissionScopes[PermCIRead]; ok {
		t.Errorf("ci:read is org-wide and needs no narrower scope: %+v", mixed.PermissionScopes)
	}

	unmapped := ResolveAccess([]Grant{{Permissions: []Permission{PermCIRead}, Sites: []string{scopeSiteA}}})
	if unmapped.Scope.Clients.All || len(unmapped.Scope.Clients.IDs) != 0 || unmapped.Scope.LegacyClientScope() != NoScopeID {
		t.Fatalf("sites without clients: clients = %+v (legacy %q), want none", unmapped.Scope.Clients, unmapped.Scope.LegacyClientScope())
	}
}

func TestResolveAccessKeepsNarrowerPermissionScopes(t *testing.T) {
	access := ResolveAccess([]Grant{
		OrgWideGrant([]Permission{PermCIRead}),
		{Permissions: []Permission{PermCIWrite, PermCIRead}, Clients: []string{scopeClientA}},
	})
	if !access.Scope.OrgWide() {
		t.Fatalf("union scope must be org-wide, got %+v", access.Scope)
	}
	if len(access.Permissions) != 2 || access.Permissions[0] != PermCIRead || access.Permissions[1] != PermCIWrite {
		t.Fatalf("permissions = %v", access.Permissions)
	}
	p := Principal{Scope: &access.Scope, PermissionScopes: access.PermissionScopes, Permissions: access.Permissions}
	if s, ok := p.ScopeFor(PermCIRead); !ok || !s.OrgWide() {
		t.Fatalf("ci:read must be org-wide, got %+v %v", s, ok)
	}
	if s, ok := p.ScopeFor(PermCIWrite); !ok || s.Clients.All || len(s.Clients.IDs) != 1 || s.Clients.IDs[0] != scopeClientA {
		t.Fatalf("ci:write must be limited to client A, got %+v %v", s, ok)
	}
	if _, ok := p.ScopeFor(PermCIDelete); ok {
		t.Fatal("permission without grant must have no scope")
	}
}

func TestLegacyClientScopeFailsClosed(t *testing.T) {
	if got := (&Scope{Clients: ScopeSet{All: true}}).LegacyClientScope(); got != "" {
		t.Fatalf("org-wide clients = %q, want empty", got)
	}
	if got := (&Scope{}).LegacyClientScope(); got != NoScopeID {
		t.Fatalf("no clients = %q, want %s", got, NoScopeID)
	}
	if got := (&Scope{Clients: ScopeSet{IDs: []string{scopeClientA, scopeClientB}}}).LegacyClientScope(); got != scopeClientA+","+scopeClientB {
		t.Fatalf("client list = %q", got)
	}
}

type fakeAccessResolver struct {
	grants []Grant
	err    error
	calls  int
	// roles maps standard role names to their permissions for RoleGrants.
	roles map[string][]Permission
	// requestedRoles records the role names RoleGrants was asked for.
	requestedRoles []string
}

func (f *fakeAccessResolver) RoleGrants(_ context.Context, _ string, roleNames []string) ([]Grant, error) {
	f.requestedRoles = append(f.requestedRoles, roleNames...)
	grants := make([]Grant, 0, len(roleNames))
	for _, name := range roleNames {
		if perms, ok := f.roles[name]; ok {
			grants = append(grants, OrgWideGrant(perms))
		}
	}
	return grants, f.err
}

func (f *fakeAccessResolver) AccessGrants(_ context.Context, orgID, userID string) ([]Grant, error) {
	f.calls++
	if orgID != scopeOrg || userID != "user-123" {
		return nil, errors.New("unexpected tenant")
	}
	return f.grants, f.err
}

// A session refresh reads the role assignments from the database: a client
// assignment yields a client scope instead of the org-wide scope of the old
// token, and a revoked role disappears.
func TestRefreshReadsRoleAssignmentsFromDatabase(t *testing.T) {
	sessionIssuer := testSessionIssuer(t)
	resolver := &fakeAccessResolver{grants: []Grant{{Permissions: []Permission{PermCIRead}, Clients: []string{scopeClientA}}}}
	handler := NewHandler(nil, sessionIssuer).WithAccessResolver(resolver)

	orgWide := Scope{Clients: ScopeSet{All: true}, Sites: ScopeSet{All: true}, Teams: ScopeSet{All: true}}
	token, err := sessionIssuer.Issue(SessionClaims{
		Subject:        "user-123",
		OrganizationID: scopeOrg,
		Permissions:    []Permission{PermCIRead, PermCIDelete},
		Scope:          &orgWide,
		IssuedAt:       time.Now().UTC(),
		ExpiresAt:      time.Now().UTC().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}

	refreshed := refresh(t, handler, token)
	if resolver.calls != 1 {
		t.Fatalf("resolver calls = %d, want 1", resolver.calls)
	}
	if refreshed.Scope == nil || refreshed.Scope.OrgWide() || refreshed.Scope.Clients.All {
		t.Fatalf("client-only user must not be org-wide after refresh: %+v", refreshed.Scope)
	}
	if refreshed.ClientScope != scopeClientA {
		t.Fatalf("client scope = %q, want %s", refreshed.ClientScope, scopeClientA)
	}
	if len(refreshed.Permissions) != 1 || refreshed.Permissions[0] != PermCIRead {
		t.Fatalf("revoked permission must be dropped, got %v", refreshed.Permissions)
	}
}

func TestRefreshFailsWhenRoleAssignmentsCannotBeRead(t *testing.T) {
	sessionIssuer := testSessionIssuer(t)
	handler := NewHandler(nil, sessionIssuer).WithAccessResolver(&fakeAccessResolver{err: errors.New("db down")})
	token, err := sessionIssuer.Issue(SessionClaims{
		Subject: "user-123", OrganizationID: scopeOrg,
		IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := refreshRequest(t, handler, token)
	w := httptest.NewRecorder()
	handler.Refresh(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "db down") {
		t.Fatal("raw error text must not leak")
	}
}

// At login the IdP groups are an org-wide grant and the database assignments
// are added with their scope.
func TestCallbackCombinesGroupsAndRoleAssignments(t *testing.T) {
	sessionIssuer := testSessionIssuer(t)
	fake := newFakeOIDCServer(t)
	fake.mutateClaims = func(claims map[string]any) {
		claims["organization_id"] = scopeOrg
		claims["groups"] = []string{"reticora-staff"}
	}
	resolver := &fakeAccessResolver{grants: []Grant{{Permissions: []Permission{PermCIWrite}, Clients: []string{scopeClientB}}}}
	handler := NewHandler(NewOIDCProvider(OIDCConfig{
		IssuerURL:    fake.issuer(),
		ClientID:     "reticora-app",
		ClientSecret: "top-secret",
		RedirectURL:  "https://app.example.com/callback",
	}), sessionIssuer).WithAccessResolver(resolver)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/callback",
		strings.NewReader(`{"code":"auth-code","state":"nonce","code_verifier":"verifier-123"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.Callback(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	claims, err := sessionIssuer.Validate(resp.Token)
	if err != nil {
		t.Fatal(err)
	}
	// The group carries no role, so only the client assignment counts.
	if claims.Scope == nil || claims.Scope.Clients.All || len(claims.Scope.Clients.IDs) != 1 || claims.Scope.Clients.IDs[0] != scopeClientB {
		t.Fatalf("scope = %+v, want client B only", claims.Scope)
	}
	if len(claims.Groups) != 1 || claims.Groups[0] != "reticora-staff" {
		t.Fatalf("groups must be kept for refresh, got %v", claims.Groups)
	}
}

func refresh(t *testing.T, handler *Handler, token string) *SessionClaims {
	t.Helper()
	req := refreshRequest(t, handler, token)
	w := httptest.NewRecorder()
	handler.Refresh(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	claims, err := handler.sessions.Validate(resp.Token)
	if err != nil {
		t.Fatal(err)
	}
	return claims
}
