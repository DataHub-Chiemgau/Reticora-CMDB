package identity

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
)

// refreshRequest builds POST /auth/refresh with a refresh cookie for the
// session of the access token, as the callback would have set it.
func refreshRequest(t *testing.T, h *Handler, accessToken string) *http.Request {
	t.Helper()
	claims, err := h.sessions.Validate(accessToken)
	if err != nil {
		t.Fatal(err)
	}
	cookie, err := h.refresh.Issue(context.Background(), &RefreshGrant{Claims: withoutTokenTimes(claims)})
	if err != nil {
		t.Fatal(err)
	}
	return cookieRequest(http.MethodPost, "/api/v1/auth/refresh", cookie)
}

func cookieRequest(method, path, refreshToken string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	if refreshToken != "" {
		req.AddCookie(&http.Cookie{Name: RefreshCookieName, Value: refreshToken})
	}
	return req
}

// refreshCookie returns the refresh cookie a response sets.
func refreshCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == RefreshCookieName {
			return c
		}
	}
	t.Fatalf("response sets no %s cookie", RefreshCookieName)
	return nil
}

// jwtPart decodes the header (0) or payload (1) of a JWT into a map.
func jwtPart(t *testing.T, token string, i int) map[string]any {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[i])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err = json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// loginHandler returns a handler wired to a fake IdP and the token of a
// completed callback together with the refresh cookie it set.
func loginHandler(t *testing.T, issuer *SessionIssuer, refresh *RefreshSessions) (handler *Handler, accessToken string, cookie *http.Cookie) {
	t.Helper()
	fake := newFakeOIDCServer(t)
	fake.mutateClaims = func(claims map[string]any) {
		claims["groups"] = []string{"reticora-viewer"}
		claims["name"] = "Ada Admin"
		claims["email"] = "ada@example.com"
	}
	handler = NewHandler(fake.provider("reticora-app"), issuer).
		WithAccessResolver(standardRoleResolver{"viewer": {PermCIRead}})
	if refresh != nil {
		handler.WithRefreshSessions(refresh)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/callback",
		strings.NewReader(`{"code":"auth-code","state":"nonce","code_verifier":"verifier-123"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.Callback(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("callback: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return handler, resp.Token, refreshCookie(t, w)
}

func doRefresh(h *Handler, refreshToken string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.Refresh(w, cookieRequest(http.MethodPost, "/api/v1/auth/refresh", refreshToken))
	return w
}

// TestSessionTokenFollowsAUT02 covers WP-066 (AUT-02, SEC-06): the access
// token lives 15 minutes, carries the claims of AUT-02 with NumericDate
// times and the key id of the signing key in its header; the refresh token
// is an HttpOnly, Secure, SameSite=Strict cookie for the auth endpoints only.
func TestSessionTokenFollowsAUT02(t *testing.T) {
	issuer := testSessionIssuer(t)
	_, token, cookie := loginHandler(t, issuer, nil)

	header := jwtPart(t, token, 0)
	if header["alg"] != "RS256" || header["kid"] != issuer.KeyID() || issuer.KeyID() == "" {
		t.Errorf("JWT header %v, want RS256 with kid %q", header, issuer.KeyID())
	}
	claims := jwtPart(t, token, 1)
	for _, k := range []string{"sub", "org", "scopes", "cls", "sts", "tms", "name", "email", "jti", "iat", "exp"} {
		if _, ok := claims[k]; !ok {
			t.Errorf("claim %q missing in %v", k, claims)
		}
	}
	for _, k := range []string{"org_id", "permissions", "scope", "client_scope"} {
		if _, ok := claims[k]; ok {
			t.Errorf("claim %q of the old format still present", k)
		}
	}
	if claims["name"] != "Ada Admin" || claims["email"] != "ada@example.com" {
		t.Errorf("name/email claims: %v / %v", claims["name"], claims["email"])
	}
	iat, _ := claims["iat"].(float64)
	exp, _ := claims["exp"].(float64)
	if exp-iat != (15 * time.Minute).Seconds() {
		t.Errorf("token lifetime %v s, want 900 s", exp-iat)
	}

	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode ||
		cookie.Path != "/api/v1/auth" || cookie.MaxAge != int(RefreshTokenLifetime.Seconds()) || cookie.Value == "" {
		t.Errorf("refresh cookie %+v", cookie)
	}
}

// TestSessionClaimsRoundTrip: the scope dimensions survive encoding (null =
// all, list = these, empty list = none), as do permission scopes.
func TestSessionClaimsRoundTrip(t *testing.T) {
	scope := Scope{Clients: ScopeSet{IDs: []string{"c1", "c2"}}, Sites: ScopeSet{All: true}, Teams: ScopeSet{}}
	in := SessionClaims{
		Subject: "u", OrganizationID: "o", Permissions: []Permission{PermCIRead},
		Scope: &scope, ClientScope: scope.LegacyClientScope(),
		PermissionScopes: map[Permission]Scope{PermCIRead: {Clients: ScopeSet{IDs: []string{"c1"}}, Sites: ScopeSet{All: true}, Teams: ScopeSet{All: true}}},
		Groups:           []string{"g"}, Name: "n", Email: "e", ID: "j",
		IssuedAt: time.Unix(1700000000, 0).UTC(), ExpiresAt: time.Unix(1700000900, 0).UTC(),
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"cls":["c1","c2"],"sts":null,"tms":[]`) {
		t.Errorf("scope encoding: %s", raw)
	}
	var out SessionClaims
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round trip:\n got %+v\nwant %+v", out, in)
	}

	// A token without scope (development tokens) keeps its client scope.
	raw, _ = json.Marshal(SessionClaims{Subject: "u", OrganizationID: "o", ClientScope: "c9"})
	if err = json.Unmarshal(raw, &out); err != nil || out.Scope != nil || out.ClientScope != "c9" {
		t.Errorf("token without scope: %s -> %+v, %v", raw, out, err)
	}
}

// TestRefreshRotatesAndDetectsReuse covers WP-066 (AUT-02): every refresh
// uses up its cookie and sets a new one; presenting a used cookie again
// revokes the whole session, so the stolen and the legitimate successor
// both stop working.
func TestRefreshRotatesAndDetectsReuse(t *testing.T) {
	issuer := testSessionIssuer(t)
	handler, _, first := loginHandler(t, issuer, nil)

	w := doRefresh(handler, first.Value)
	if w.Code != http.StatusOK {
		t.Fatalf("first refresh: %d %s", w.Code, w.Body.String())
	}
	second := refreshCookie(t, w)
	if second.Value == first.Value {
		t.Fatal("refresh cookie was not rotated")
	}
	var resp struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	claims, err := issuer.Validate(resp.Token)
	if err != nil || claims.Name != "Ada Admin" || len(claims.Permissions) != 1 {
		t.Fatalf("refreshed token: %+v, %v", claims, err)
	}

	if w = doRefresh(handler, first.Value); w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "already used") {
		t.Fatalf("reused cookie: %d %s, want 401", w.Code, w.Body.String())
	}
	if c := refreshCookie(t, w); c.MaxAge >= 0 {
		t.Errorf("reuse must clear the cookie: %+v", c)
	}
	if w = doRefresh(handler, second.Value); w.Code != http.StatusUnauthorized {
		t.Fatalf("successor after reuse: %d, want 401 (family revoked)", w.Code)
	}
	if w = doRefresh(handler, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("refresh without cookie: %d, want 401", w.Code)
	}
	if w = doRefresh(handler, "unknown"); w.Code != http.StatusUnauthorized {
		t.Fatalf("unknown cookie: %d, want 401", w.Code)
	}
}

// TestLogoutRevokesSession covers WP-066 (AUT-02): logout revokes the
// refresh family and blacklists the access token until it expires.
func TestLogoutRevokesSession(t *testing.T) {
	store := cache.NewMemoryStore()
	revocations := NewSessionRevocations(store)
	issuer := testSessionIssuer(t).WithRevocations(revocations)
	handler, token, cookie := loginHandler(t, issuer, NewRefreshSessions(store, revocations))
	if _, err := issuer.Validate(token); err != nil {
		t.Fatal(err)
	}

	req := cookieRequest(http.MethodPost, "/api/v1/auth/logout", cookie.Value)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.Logout(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("logout: %d %s", w.Code, w.Body.String())
	}
	if c := refreshCookie(t, w); c.MaxAge >= 0 || c.Value != "" {
		t.Errorf("logout must clear the cookie: %+v", c)
	}
	if _, err := issuer.Validate(token); !errors.Is(err, ErrSessionRevoked) {
		t.Errorf("access token after logout: %v, want revoked", err)
	}
	if w = doRefresh(handler, cookie.Value); w.Code != http.StatusUnauthorized {
		t.Errorf("refresh after logout: %d, want 401", w.Code)
	}

	// Logging out without a session is harmless.
	w = httptest.NewRecorder()
	handler.Logout(w, cookieRequest(http.MethodPost, "/api/v1/auth/logout", ""))
	if w.Code != http.StatusNoContent {
		t.Errorf("logout without session: %d", w.Code)
	}
}

// TestDeactivationEndsRefresh: a user revocation (deactivation) rejects
// every refresh token issued before it at once.
func TestDeactivationEndsRefresh(t *testing.T) {
	store := cache.NewMemoryStore()
	revocations := NewSessionRevocations(store)
	issuer := testSessionIssuer(t).WithRevocations(revocations)
	handler, token, cookie := loginHandler(t, issuer, NewRefreshSessions(store, revocations))
	claims, err := issuer.Validate(token)
	if err != nil {
		t.Fatal(err)
	}
	if err = revocations.RevokeUser(context.Background(), claims.Subject, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if w := doRefresh(handler, cookie.Value); w.Code != http.StatusUnauthorized {
		t.Errorf("refresh after deactivation: %d, want 401", w.Code)
	}
}

// TestSigningKeyRotationOverlap covers WP-066 (SEC-06 in part): after a key
// rotation tokens of the previous key stay valid during the overlap, new
// tokens carry the new kid, and a token of an unknown key is rejected.
func TestSigningKeyRotationOverlap(t *testing.T) {
	oldKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	oldPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(oldKey)})
	oldIssuer := mustSessionIssuer(t, oldPEM)
	claims := SessionClaims{Subject: "u", OrganizationID: "o", ExpiresAt: time.Now().Add(time.Minute)}
	oldToken, err := oldIssuer.Issue(claims)
	if err != nil {
		t.Fatal(err)
	}

	newIssuer := testSessionIssuer(t)
	if _, err = newIssuer.Validate(oldToken); err == nil || !strings.Contains(err.Error(), "unknown JWT key id") {
		t.Fatalf("token of an unknown key: %v", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&oldKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if err = newIssuer.WithPreviousKeys(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})); err != nil {
		t.Fatal(err)
	}
	if _, err = newIssuer.Validate(oldToken); err != nil {
		t.Fatalf("token of the previous key during the overlap: %v", err)
	}
	newToken, err := newIssuer.Issue(claims)
	if err != nil {
		t.Fatal(err)
	}
	if kid := jwtPart(t, newToken, 0)["kid"]; kid != newIssuer.KeyID() || kid == oldIssuer.KeyID() {
		t.Errorf("new token kid %v, want %s", kid, newIssuer.KeyID())
	}
	// A token without kid (issued before key ids) verifies with the
	// current key only.
	legacy := unsignedJWT(t, map[string]any{"sub": "u"})
	if _, err = newIssuer.Validate(legacy); err == nil || strings.Contains(err.Error(), "key id") {
		t.Errorf("unsigned legacy token: %v, want a signature error", err)
	}
}
