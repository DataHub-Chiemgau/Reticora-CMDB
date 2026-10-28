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

// scopeIdP is a fake OIDC provider (discovery document, JWKS, token
// endpoint) whose token endpoint answers each authorization code with an ID
// token for the subject registered under that code.
type scopeIdP struct {
	t      *testing.T
	server *httptest.Server
	key    *rsa.PrivateKey
	org    string
	codes  map[string][2]string // code -> subject, e-mail
}

func newScopeIdP(t *testing.T, org string) *scopeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &scopeIdP{t: t, key: key, org: org, codes: map[string][2]string{}}
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
	payload, err := json.Marshal(map[string]any{
		"iss": p.server.URL, "sub": subject, "aud": "reticora-app", "email": email, "email_verified": true,
		"name": subject, "organization_id": p.org, "groups": []string{},
		"iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix(),
	})
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
