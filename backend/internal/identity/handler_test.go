package identity

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeOIDCServer is a minimal authorization server test double exposing the
// discovery document, JWKS and token endpoints, signing ID tokens with a
// generated RSA key.
type fakeOIDCServer struct {
	t          *testing.T
	server     *httptest.Server
	privateKey *rsa.PrivateKey
	keyID      string

	// tokenRequest, when non-nil, is invoked with the parsed token request
	// form before the token response is written.
	tokenRequest func(form url.Values)
	// mutateClaims, when non-nil, can adjust the ID token claims.
	mutateClaims func(claims map[string]any)
	// idTokenOverride, when non-nil, replaces the signed ID token in the
	// token response (used to simulate a compromised/competing signer).
	idTokenOverride func() string
}

func newFakeOIDCServer(t *testing.T) *fakeOIDCServer {
	t.Helper()

	fake := &fakeOIDCServer{t: t, keyID: "test-key"}
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fake.privateKey = privateKey

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeTestJSON(w, map[string]any{
			"issuer":   fake.issuer(),
			"jwks_uri": fake.issuer() + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		pub := &privateKey.PublicKey
		writeTestJSON(w, map[string]any{
			"keys": []map[string]any{{
				"kty": "RSA",
				"kid": fake.keyID,
				"alg": "RS256",
				"use": "sig",
				"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			}},
		})
	})
	mux.HandleFunc("/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if fake.tokenRequest != nil {
			fake.tokenRequest(r.Form)
		}
		idToken := fake.idToken(nil)
		if fake.idTokenOverride != nil {
			idToken = fake.idTokenOverride()
		}
		writeTestJSON(w, map[string]any{
			"access_token":  "access",
			"id_token":      idToken,
			"refresh_token": "refresh",
			"expires_in":    300,
		})
	})

	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeOIDCServer) issuer() string { return f.server.URL }

func (f *fakeOIDCServer) provider(clientID string) *OIDCProvider {
	return NewOIDCProvider(OIDCConfig{IssuerURL: f.issuer(), ClientID: clientID})
}

// idToken returns a signed ID token with the given claim overrides applied.
func (f *fakeOIDCServer) idToken(overrides map[string]any) string {
	f.t.Helper()

	claims := map[string]any{
		"iss":    f.issuer(),
		"sub":    "user-123",
		"aud":    "reticora-app",
		"email":  "user@example.com",
		"name":   "User Example",
		"groups": []string{"123e4567-e89b-12d3-a456-426614174000"},
		"iat":    time.Now().Unix(),
		"exp":    time.Now().Add(5 * time.Minute).Unix(),
	}
	if f.mutateClaims != nil {
		f.mutateClaims(claims)
	}
	for key, value := range overrides {
		claims[key] = value
	}
	return signJWT(f.t, f.privateKey, f.keyID, claims)
}

func signJWT(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()

	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	hash := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func writeTestJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func TestCallbackIssuesSession(t *testing.T) {
	sessionIssuer := testSessionIssuer(t)
	orgID := "123e4567-e89b-12d3-a456-426614174000"

	fake := newFakeOIDCServer(t)
	fake.mutateClaims = func(claims map[string]any) {
		claims["groups"] = []string{orgID, "reticora-admin"}
	}
	fake.tokenRequest = func(form url.Values) {
		if got := form.Get("grant_type"); got != "authorization_code" {
			t.Errorf("unexpected grant_type %q", got)
		}
		if got := form.Get("code"); got != "auth-code" {
			t.Errorf("unexpected code %q", got)
		}
		if got := form.Get("client_id"); got != "reticora-app" {
			t.Errorf("unexpected client_id %q", got)
		}
		if got := form.Get("client_secret"); got != "top-secret" {
			t.Errorf("unexpected client_secret %q", got)
		}
		if got := form.Get("redirect_uri"); got != "https://app.example.com/callback" {
			t.Errorf("unexpected redirect_uri %q", got)
		}
	}

	handler := NewHandler(NewOIDCProvider(OIDCConfig{
		IssuerURL:    fake.issuer(),
		ClientID:     "reticora-app",
		ClientSecret: "top-secret",
		RedirectURL:  "https://app.example.com/callback",
	}), sessionIssuer)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/callback",
		strings.NewReader(`{"code":"auth-code","state":"nonce","code_verifier":"verifier-123"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.Callback(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
		User      struct {
			OrgID       string       `json:"org_id"`
			Permissions []Permission `json:"permissions"`
		} `json:"user"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Token == "" {
		t.Fatal("expected token in response")
	}
	if resp.User.OrgID != orgID {
		t.Fatalf("expected org %s, got %s", orgID, resp.User.OrgID)
	}
	if len(resp.User.Permissions) != len(allPermissions()) {
		t.Fatalf("expected %d permissions, got %d", len(allPermissions()), len(resp.User.Permissions))
	}
	if _, err := time.Parse(time.RFC3339, resp.ExpiresAt); err != nil {
		t.Fatalf("invalid expires_at: %v", err)
	}

	claims, err := sessionIssuer.Validate(resp.Token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != "user-123" {
		t.Fatalf("expected subject user-123, got %s", claims.Subject)
	}
	if claims.OrganizationID != orgID {
		t.Fatalf("expected org %s, got %s", orgID, claims.OrganizationID)
	}
}

func TestRefreshAllowsRecentlyExpiredToken(t *testing.T) {
	sessionIssuer := testSessionIssuer(t)
	handler := NewHandler(nil, sessionIssuer)

	expiredClaims := SessionClaims{
		Subject:        "user-123",
		OrganizationID: "123e4567-e89b-12d3-a456-426614174000",
		Permissions:    []Permission{PermCIRead},
		IssuedAt:       time.Now().UTC().Add(-2 * time.Hour),
		ExpiresAt:      time.Now().UTC().Add(-30 * time.Minute),
	}
	token, err := sessionIssuer.Issue(expiredClaims)
	if err != nil {
		t.Fatal(err)
	}

	body := url.Values{}
	body.Set("token", token)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", strings.NewReader(`{"token":"`+token+`"}`))
	req.Header.Set("Content-Type", "application/json")
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
	refreshed, err := sessionIssuer.Validate(resp.Token)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Subject != expiredClaims.Subject {
		t.Fatalf("expected subject %s, got %s", expiredClaims.Subject, refreshed.Subject)
	}
	if !refreshed.ExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("expected refreshed expiry in future, got %s", refreshed.ExpiresAt)
	}
}

func testSessionIssuer(t *testing.T) *SessionIssuer {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	return mustSessionIssuer(t, pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	}))
}

func mustSessionIssuer(t *testing.T, privateKeyPEM []byte) *SessionIssuer {
	t.Helper()

	issuer, err := NewSessionIssuer(privateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return issuer
}

func unsignedJWT(t *testing.T, payload map[string]any) string {
	t.Helper()

	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payloadBytes) + ".sig"
}

// TestCallbackRejectsMissingPKCEVerifier proves that an authorization code
// exchange without the PKCE verifier is refused before the provider is
// contacted, closing the code-injection path of the legacy login flow.
func TestCallbackRejectsMissingPKCEVerifier(t *testing.T) {
	sessionIssuer := testSessionIssuer(t)
	fake := newFakeOIDCServer(t)

	tokenCalled := false
	fake.tokenRequest = func(url.Values) { tokenCalled = true }

	handler := NewHandler(fake.provider("reticora-app"), sessionIssuer)

	for _, body := range []string{
		`{"code":"auth-code","state":"nonce"}`,
		`{"code":"auth-code","state":"nonce","code_verifier":"  "}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/callback", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		handler.Callback(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %s, got %d: %s", body, w.Code, w.Body.String())
		}
		if tokenCalled {
			t.Fatal("the token endpoint must not be contacted without a code verifier")
		}
	}
}

// TestCallbackRejectsMissingState proves that state is mandatory on the
// callback exchange.
func TestCallbackRejectsMissingState(t *testing.T) {
	sessionIssuer := testSessionIssuer(t)
	fake := newFakeOIDCServer(t)
	handler := NewHandler(fake.provider("reticora-app"), sessionIssuer)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/callback",
		strings.NewReader(`{"code":"auth-code","code_verifier":"verifier-123"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.Callback(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCallbackRejectsForgedIDToken proves that a token endpoint returning an
// ID token signed by an unknown key fails the exchange with 401 and does not
// issue a session.
func TestCallbackRejectsForgedIDToken(t *testing.T) {
	sessionIssuer := testSessionIssuer(t)
	fake := newFakeOIDCServer(t)

	attackerKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fake.idTokenOverride = func() string {
		return signJWT(t, attackerKey, fake.keyID, map[string]any{
			"iss":    fake.issuer(),
			"sub":    "attacker",
			"aud":    "reticora-app",
			"groups": []string{"123e4567-e89b-12d3-a456-426614174000"},
			"exp":    time.Now().Add(5 * time.Minute).Unix(),
		})
	}

	handler := NewHandler(fake.provider("reticora-app"), sessionIssuer)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/callback",
		strings.NewReader(`{"code":"auth-code","state":"nonce","code_verifier":"verifier-123"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.Callback(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for a forged ID token, got %d: %s", w.Code, w.Body.String())
	}
}

func TestConfigExposesPublicOIDCParameters(t *testing.T) {
	handler := NewHandler(NewOIDCProvider(OIDCConfig{
		IssuerURL:    "https://idp.example.com/realms/reticora",
		ClientID:     "reticora-app",
		ClientSecret: "top-secret",
		RedirectURL:  "https://app.example.com/auth/callback",
	}), nil)

	w := httptest.NewRecorder()
	handler.Config(w, httptest.NewRequest(http.MethodGet, "/api/v1/auth/config", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "top-secret") {
		t.Fatal("the client secret must never be exposed")
	}

	var cfg PublicConfig
	if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.ClientID != "reticora-app" {
		t.Fatalf("unexpected client_id %q", cfg.ClientID)
	}
	if cfg.AuthorizationEndpoint != "https://idp.example.com/realms/reticora/protocol/openid-connect/auth" {
		t.Fatalf("unexpected authorization endpoint %q", cfg.AuthorizationEndpoint)
	}
	if !cfg.PKCERequired {
		t.Fatal("expected PKCE to be required")
	}
}

func TestCallbackForwardsPKCEVerifier(t *testing.T) {
	sessionIssuer := testSessionIssuer(t)

	fake := newFakeOIDCServer(t)
	fake.tokenRequest = func(form url.Values) {
		if got := form.Get("code_verifier"); got != "verifier-123" {
			t.Errorf("unexpected code_verifier %q", got)
		}
	}

	handler := NewHandler(fake.provider("reticora-app"), sessionIssuer)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/callback",
		strings.NewReader(`{"code":"auth-code","state":"nonce","code_verifier":"verifier-123"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.Callback(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}
