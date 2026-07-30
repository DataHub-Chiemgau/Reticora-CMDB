package identity

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCallbackIssuesSession(t *testing.T) {
	sessionIssuer := testSessionIssuer(t)
	orgID := "123e4567-e89b-12d3-a456-426614174000"

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/protocol/openid-connect/token" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got := r.Form.Get("grant_type"); got != "authorization_code" {
			t.Fatalf("unexpected grant_type %q", got)
		}
		if got := r.Form.Get("code"); got != "auth-code" {
			t.Fatalf("unexpected code %q", got)
		}
		if got := r.Form.Get("client_id"); got != "reticora-app" {
			t.Fatalf("unexpected client_id %q", got)
		}
		if got := r.Form.Get("client_secret"); got != "top-secret" {
			t.Fatalf("unexpected client_secret %q", got)
		}
		if got := r.Form.Get("redirect_uri"); got != "https://app.example.com/callback" {
			t.Fatalf("unexpected redirect_uri %q", got)
		}

		idToken := unsignedJWT(t, map[string]any{
			"iss":    server.URL,
			"sub":    "user-123",
			"email":  "user@example.com",
			"name":   "User Example",
			"groups": []string{orgID, "reticora-admin"},
			"exp":    time.Now().Add(5 * time.Minute).Unix(),
		})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access",
			"id_token":      idToken,
			"refresh_token": "refresh",
			"expires_in":    300,
		})
	}))
	defer server.Close()

	handler := NewHandler(NewOIDCProvider(OIDCConfig{
		IssuerURL:    server.URL,
		ClientID:     "reticora-app",
		ClientSecret: "top-secret",
		RedirectURL:  "https://app.example.com/callback",
	}), sessionIssuer)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/callback", strings.NewReader(`{"code":"auth-code","state":"nonce"}`))
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

	header, err := json.Marshal(map[string]string{"alg": "none", "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payloadBytes) + ".sig"
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
	orgID := "123e4567-e89b-12d3-a456-426614174000"

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got := r.Form.Get("code_verifier"); got != "verifier-123" {
			t.Fatalf("unexpected code_verifier %q", got)
		}

		idToken := unsignedJWT(t, map[string]any{
			"iss":    server.URL,
			"sub":    "user-123",
			"groups": []string{orgID},
			"exp":    time.Now().Add(5 * time.Minute).Unix(),
		})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id_token": idToken, "expires_in": 300})
	}))
	defer server.Close()

	handler := NewHandler(NewOIDCProvider(OIDCConfig{
		IssuerURL: server.URL,
		ClientID:  "reticora-app",
	}), sessionIssuer)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/callback",
		strings.NewReader(`{"code":"auth-code","state":"nonce","code_verifier":"verifier-123"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.Callback(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}
