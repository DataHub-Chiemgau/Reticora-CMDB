package identity_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

// TestCallbackScopeFromRoleAssignments covers TEN-04 and RBA-03 (WP-009,
// evidence WP-536): the OIDC callback with a fake IdP, the user provisioning
// of user.PGRepository and permission.NewPGRepository as access resolver, as
// router.go wires them, derives the session scope from the role assignments
// in PostgreSQL. A user assigned to client 1 only receives a session token
// whose wire claims read cls=[C1] and sts=null (all sites of that client),
// never an org-wide scope; the login adds no assignment. A subject without
// an assignment to the organization (no account, no invitation) receives no
// session: no access token and no refresh cookie, and no app_user is
// created.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCallbackScopeFromRoleAssignments(t *testing.T) {
	f := scopetest.Seed(t, "61")
	bg := context.Background()
	idp := newScopeIdP(t, f.OrgA)

	// The client-1 user exists (created by an administrator, as in the
	// token chain test) and holds the engineer role in client 1 only.
	clientUser := f.AppUser(t, f.OrgA, "cb-c1")
	if _, err := f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id)
		SELECT $1, $2, id, $3 FROM role WHERE organization_id = $1 AND name = 'engineer'`, f.OrgA, clientUser, f.Client1); err != nil {
		t.Fatalf("assign engineer in client 1: %v", err)
	}
	assignments := func(userID string) string {
		t.Helper()
		var got string
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(string_agg(ro.name || ':' || COALESCE(ra.scope_client_id::text, 'org'), ',' ORDER BY ro.name), '')
			FROM role_assignment ra JOIN role ro ON ro.id = ra.role_id WHERE ra.user_id = $1`, userID).Scan(&got); err != nil {
			t.Fatalf("read role assignments: %v", err)
		}
		return got
	}
	before := assignments(clientUser)

	issuer := newIssuer(t)
	// As router.go: provisioning with the configured default role (the
	// configuration default viewer) and the database as access resolver.
	handler := identity.NewHandler(idp.provider(), issuer).
		WithProvisioning(user.NewPGRepository(f.App), "viewer").
		WithAccessResolver(permission.NewPGRepository(f.App))

	t.Run("client-1 assignment", func(t *testing.T) {
		rec := idp.login(handler, "scopetest-61-cb-c1", "cb-c1@scopetest.invalid")
		if rec.Code != http.StatusOK {
			t.Fatalf("callback: %d %s, want 200", rec.Code, rec.Body.String())
		}
		var out struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Token == "" {
			t.Fatalf("callback response: %v %s", err, rec.Body.String())
		}
		if !hasRefreshCookie(rec) {
			t.Error("callback set no refresh cookie")
		}
		claims, err := issuer.Validate(out.Token)
		if err != nil {
			t.Fatalf("validate session token: %v", err)
		}
		if claims.Subject != clientUser || claims.OrganizationID != f.OrgA {
			t.Errorf("session subject/org %s/%s, want %s/%s", claims.Subject, claims.OrganizationID, clientUser, f.OrgA)
		}
		if claims.Scope == nil || claims.Scope.Clients.All || len(claims.Scope.Clients.IDs) != 1 || claims.Scope.Clients.IDs[0] != f.Client1 {
			t.Errorf("session client scope %+v, want exactly client 1", claims.Scope)
		}
		if len(claims.Permissions) == 0 {
			t.Error("session carries no permission of the engineer role")
		}
		// The wire format of the token (AUT-02, TEN-04): cls lists client
		// 1, sts is null (every site of the client).
		wire := tokenPayload(t, out.Token)
		if got := string(wire["cls"]); got != `["`+f.Client1+`"]` {
			t.Errorf("token cls = %s, want [%q]", got, f.Client1)
		}
		if got, ok := wire["sts"]; !ok || string(got) != "null" {
			t.Errorf("token sts = %s (present %v), want null", got, ok)
		}
		// The login keeps the assignment: no default role is added.
		if after := assignments(clientUser); after != before {
			t.Errorf("role assignments after login %q, want unchanged %q", after, before)
		}
	})

	t.Run("no assignment to the organization", func(t *testing.T) {
		const subject = "scopetest-61-cb-unassigned"
		rec := idp.login(handler, subject, "cb-unassigned@scopetest.invalid")
		if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), `"token"`) {
			t.Fatalf("callback without assignment: %d %s, want no session", rec.Code, rec.Body.String())
		}
		if rec.Code != http.StatusForbidden {
			t.Errorf("callback without assignment: %d %s, want 403", rec.Code, rec.Body.String())
		}
		if hasRefreshCookie(rec) {
			t.Error("callback without assignment set a refresh cookie")
		}
		var users int
		if err := f.Admin.QueryRow(bg, `SELECT COUNT(*) FROM app_user WHERE oidc_subject = $1 OR email = 'cb-unassigned@scopetest.invalid'`, subject).Scan(&users); err != nil {
			t.Fatalf("count users: %v", err)
		}
		if users != 0 {
			t.Errorf("callback without assignment created %d app_user rows, want 0", users)
		}
	})
}

// TestAuditCallbackWithoutAssignmentIssuesNoSession covers TEN-04 and RBA-03
// (WP-637, F-364, found by WP-536): a user of the organization without any
// role assignment logs in through the OIDC callback with the production
// wiring (user.PGRepository with the default provisioning role, the
// configuration default viewer; permission.NewPGRepository). The user held
// engineer in client 1 and an administrator removed it. The login assigns
// no role (the default role belongs to the first login only) and issues no
// session: 403 problem+json with the fixed text, no access token, no
// refresh cookie, and the refusal is audited as user.login_refused without
// an entry of a role grant.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAuditCallbackWithoutAssignmentIssuesNoSession(t *testing.T) {
	f := scopetest.Seed(t, "61")
	bg := context.Background()
	idp := newScopeIdP(t, f.OrgA)
	f.AppUser(t, f.OrgA, "cb-admin")
	member := f.AppUser(t, f.OrgA, "cb-revoked")
	// The user held engineer in client 1; an administrator removed it.
	if _, err := f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id)
		SELECT $1, $2, id, $3 FROM role WHERE organization_id = $1 AND name = 'engineer'`, f.OrgA, member, f.Client1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `DELETE FROM role_assignment WHERE user_id = $1`, member); err != nil {
		t.Fatal(err)
	}
	issuer := newIssuer(t)
	handler := identity.NewHandler(idp.provider(), issuer).
		WithProvisioning(user.NewPGRepository(f.App), "viewer").
		WithAccessResolver(permission.NewPGRepository(f.App))

	rec := idp.login(handler, "scopetest-61-cb-revoked", "cb-revoked@scopetest.invalid")
	if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), `"token"`) || hasRefreshCookie(rec) {
		body := rec.Body.String()
		if i := strings.Index(body, `"token":"`); i >= 0 {
			wire := tokenPayload(t, strings.SplitN(body[i+9:], `"`, 2)[0])
			t.Errorf("token cls=%s sts=%s scopes=%s", wire["cls"], wire["sts"], wire["scopes"])
		}
		t.Errorf("callback of a user without role assignment: %d, want no session", rec.Code)
	}
	if rec.Code != http.StatusForbidden || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/problem+json") ||
		!strings.Contains(rec.Body.String(), identity.ErrNoRoleAssignment.Error()) {
		t.Errorf("callback of a user without role assignment: %d %s %s, want 403 problem+json with %q",
			rec.Code, rec.Header().Get("Content-Type"), rec.Body.String(), identity.ErrNoRoleAssignment.Error())
	}
	var granted int
	if err := f.Admin.QueryRow(bg, `SELECT COUNT(*) FROM role_assignment WHERE user_id = $1`, member).Scan(&granted); err != nil {
		t.Fatal(err)
	}
	if granted != 0 {
		t.Errorf("the login assigned %d role(s) to a user without assignment, want 0", granted)
	}
	var refused, roleEntries int
	if err := f.Admin.QueryRow(bg, `SELECT
			count(*) FILTER (WHERE action = $2 AND resource_type = 'user' AND actor_id::text = $1 AND changes->>'reason' = 'no_role_assignment'),
			count(*) FILTER (WHERE action LIKE 'role_assignment.%')
		FROM audit_log WHERE organization_id = $3 AND (resource_id::text = $1 OR actor_id::text = $1 OR changes::text LIKE '%' || $1 || '%')`,
		member, user.ActionLoginRefused, f.OrgA).Scan(&refused, &roleEntries); err != nil {
		t.Fatalf("read audit entries: %v", err)
	}
	if refused != 1 || roleEntries != 0 {
		t.Errorf("audit: %d user.login_refused entries, %d role assignment entries; want 1 and 0", refused, roleEntries)
	}
}

// TestCallbackDefaultRoleOnlyAtFirstLogin covers TEN-04 and RBA-03 (WP-637,
// F-364) with the production wiring: the first login of a new user (an
// account an administrator created by e-mail, linked by the login) assigns
// the default role viewer org-wide and gets a session with its rights; a
// later login keeps exactly that assignment, and after an administrator
// revoked it the next login gets no session and no role. A user holding
// only a custom role gets a session with the custom role's rights and no
// default role.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCallbackDefaultRoleOnlyAtFirstLogin(t *testing.T) {
	f := scopetest.Seed(t, "62")
	bg := context.Background()
	idp := newScopeIdP(t, f.OrgA)
	f.AppUser(t, f.OrgA, "cb-admin")
	issuer := newIssuer(t)
	handler := identity.NewHandler(idp.provider(), issuer).
		WithProvisioning(user.NewPGRepository(f.App), "viewer").
		WithAccessResolver(permission.NewPGRepository(f.App))
	assignments := func(userID string) string {
		t.Helper()
		var got string
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(string_agg(ro.name || ':' || COALESCE(ra.scope_client_id::text, 'org'), ',' ORDER BY ro.name), '')
			FROM role_assignment ra JOIN role ro ON ro.id = ra.role_id WHERE ra.user_id = $1`, userID).Scan(&got); err != nil {
			t.Fatalf("read role assignments: %v", err)
		}
		return got
	}
	session := func(rec *httptest.ResponseRecorder) *identity.SessionClaims {
		t.Helper()
		var out struct {
			Token string `json:"token"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Token == "" || !hasRefreshCookie(rec) {
			t.Fatalf("callback: %d %s, want 200 with token and refresh cookie", rec.Code, rec.Body.String())
		}
		claims, err := issuer.Validate(out.Token)
		if err != nil {
			t.Fatalf("validate session token: %v", err)
		}
		return claims
	}

	t.Run("first login of a new user", func(t *testing.T) {
		var fresh string
		if err := f.Admin.QueryRow(bg, `INSERT INTO app_user (organization_id, email, display_name)
			VALUES ($1, 'cb-fresh@scopetest.invalid', 'Fresh') RETURNING id::text`, f.OrgA).Scan(&fresh); err != nil {
			t.Fatalf("seed account: %v", err)
		}
		claims := session(idp.login(handler, "scopetest-62-cb-fresh", "cb-fresh@scopetest.invalid"))
		if claims.Subject != fresh {
			t.Errorf("session subject %s, want %s", claims.Subject, fresh)
		}
		if got := assignments(fresh); got != "viewer:org" {
			t.Errorf("role assignments after the first login %q, want viewer:org", got)
		}
		if claims.Scope == nil || !claims.Scope.Clients.All || !slices.Contains(claims.Permissions, identity.PermCIRead) || slices.Contains(claims.Permissions, identity.PermCIWrite) {
			t.Errorf("first login session: scope %+v permissions %v, want the org-wide viewer rights", claims.Scope, claims.Permissions)
		}
		// A later login keeps exactly the assignment.
		session(idp.login(handler, "scopetest-62-cb-fresh", "cb-fresh@scopetest.invalid"))
		if got := assignments(fresh); got != "viewer:org" {
			t.Errorf("role assignments after the second login %q, want viewer:org", got)
		}
		// An administrator revokes it: the next login gets no session and
		// no role back.
		if _, err := f.Admin.Exec(bg, `DELETE FROM role_assignment WHERE user_id = $1`, fresh); err != nil {
			t.Fatal(err)
		}
		rec := idp.login(handler, "scopetest-62-cb-fresh", "cb-fresh@scopetest.invalid")
		if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), `"token"`) || hasRefreshCookie(rec) {
			t.Errorf("login after the revocation: %d %s, want 403 without session", rec.Code, rec.Body.String())
		}
		if got := assignments(fresh); got != "" {
			t.Errorf("login after the revocation assigned %q, want nothing", got)
		}
	})

	t.Run("only a custom role", func(t *testing.T) {
		member := f.AppUser(t, f.OrgA, "cb-custom")
		var customRole string
		if err := f.Admin.QueryRow(bg, `INSERT INTO custom_role (organization_id, name, permissions)
			VALUES ($1, 'cb-62-custom', '["ticket:read"]') RETURNING id::text`, f.OrgA).Scan(&customRole); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			// custom_role has no ON DELETE CASCADE to organization.
			_, _ = f.Admin.Exec(context.Background(), `DELETE FROM user_custom_role WHERE custom_role_id = $1`, customRole)
			_, _ = f.Admin.Exec(context.Background(), `DELETE FROM custom_role WHERE id = $1`, customRole)
		})
		if _, err := f.Admin.Exec(bg, `INSERT INTO user_custom_role (user_id, custom_role_id) VALUES ($1, $2)`, member, customRole); err != nil {
			t.Fatal(err)
		}
		claims := session(idp.login(handler, "scopetest-62-cb-custom", "cb-custom@scopetest.invalid"))
		if !slices.Contains(claims.Permissions, identity.PermTicketRead) || slices.Contains(claims.Permissions, identity.PermCIRead) {
			t.Errorf("custom role session permissions %v, want ticket:read without the viewer rights", claims.Permissions)
		}
		if got := assignments(member); got != "" {
			t.Errorf("login of a custom role user assigned %q, want no default role", got)
		}
	})
}

// scopeIdP is a fake OIDC provider (discovery document, JWKS, token
// endpoint) whose token endpoint answers each authorization code with an ID
// token for the subject registered under that code.
type scopeIdP struct {
	t      *testing.T
	server *httptest.Server
	key    *rsa.PrivateKey
	org    string
	codes  map[string][2]string // code -> subject, e-mail
	// claims are further claims of a subject's ID token (WP-653: locale).
	claims map[string]map[string]any
}

func newScopeIdP(t *testing.T, org string) *scopeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &scopeIdP{t: t, key: key, org: org, codes: map[string][2]string{}, claims: map[string]map[string]any{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeIdPJSON(w, map[string]any{"issuer": idp.server.URL, "jwks_uri": idp.server.URL + "/jwks"})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		writeIdPJSON(w, map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "kid": "scope-idp", "alg": "RS256", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		who, ok := idp.codes[r.Form.Get("code")]
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		writeIdPJSON(w, map[string]any{"access_token": "access", "id_token": idp.idToken(who[0], who[1]),
			"refresh_token": "refresh", "expires_in": 300})
	})
	idp.server = httptest.NewServer(mux)
	t.Cleanup(idp.server.Close)
	return idp
}

func (p *scopeIdP) provider() *identity.OIDCProvider {
	return identity.NewOIDCProvider(identity.OIDCConfig{IssuerURL: p.server.URL, ClientID: "reticora-app",
		ClientSecret: "secret", RedirectURL: "https://app.example.invalid/callback"})
}

// idToken signs an ID token of the organization without IdP role groups:
// the grants come from the database only.
func (p *scopeIdP) idToken(subject, email string) string {
	p.t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "scope-idp"})
	if err != nil {
		p.t.Fatal(err)
	}
	claims := map[string]any{
		"iss": p.server.URL, "sub": subject, "aud": "reticora-app", "email": email, "email_verified": true,
		"name": subject, "organization_id": p.org, "groups": []string{},
		"iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix(),
	}
	for k, v := range p.claims[subject] {
		claims[k] = v
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		p.t.Fatal(err)
	}
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, digest[:])
	if err != nil {
		p.t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// login runs POST /api/v1/auth/callback for the subject.
func (p *scopeIdP) login(h *identity.Handler, subject, email string) *httptest.ResponseRecorder {
	p.t.Helper()
	code := "code-" + subject
	p.codes[code] = [2]string{subject, email}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/callback",
		strings.NewReader(`{"code":"`+code+`","state":"state","code_verifier":"verifier"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.Callback(rec, req)
	return rec
}

func writeIdPJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func hasRefreshCookie(rec *httptest.ResponseRecorder) bool {
	for _, c := range rec.Result().Cookies() {
		if c.Name == identity.RefreshCookieName && c.Value != "" {
			return true
		}
	}
	return false
}

// tokenPayload decodes the claims of a JWT without verifying it (the caller
// verified it with the issuer) as raw JSON values per claim.
func tokenPayload(t *testing.T, token string) map[string]json.RawMessage {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts", len(parts))
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode token payload: %v", err)
	}
	var claims map[string]json.RawMessage
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("parse token payload: %v", err)
	}
	return claims
}
