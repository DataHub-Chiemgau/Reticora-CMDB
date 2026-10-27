package identity

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
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
		"iss":             f.issuer(),
		"sub":             "user-123",
		"aud":             "reticora-app",
		"email":           "user@example.com",
		"email_verified":  true,
		"name":            "User Example",
		"organization_id": "123e4567-e89b-12d3-a456-426614174000",
		"groups":          []string{"reticora-viewer"},
		"iat":             time.Now().Unix(),
		"exp":             time.Now().Add(5 * time.Minute).Unix(),
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
	}), sessionIssuer).WithAccessResolver(standardRoleResolver{"org_admin": allPermissions()})

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

	// The refresh cookie outlives the access token: an expired access
	// token is renewed with it.
	w := httptest.NewRecorder()
	handler.Refresh(w, refreshRequest(t, handler, token))

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

// statusProvisioner is a UserProvisioner and UserStatusChecker whose users
// are active unless listed as deactivated.
type statusProvisioner struct {
	deactivated map[string]bool
}

func (p statusProvisioner) EnsureUser(_ context.Context, _, oidcSubject, _, _, _ string) (string, bool, error) {
	if p.deactivated[oidcSubject] {
		return "", false, ErrUserInactive
	}
	return oidcSubject, false, nil
}

func (p statusProvisioner) UserActive(_ context.Context, _, userID string) (bool, error) {
	return !p.deactivated[userID], nil
}

// TestCallbackRejectsDeactivatedUser covers WP-042 (TLC-04, AUT-01): a login
// of a deactivated user issues no session.
func TestCallbackRejectsDeactivatedUser(t *testing.T) {
	orgID := "123e4567-e89b-12d3-a456-426614174000"
	fake := newFakeOIDCServer(t)
	fake.mutateClaims = func(claims map[string]any) {
		claims["groups"] = []string{orgID, "reticora-admin"}
	}
	handler := NewHandler(NewOIDCProvider(OIDCConfig{
		IssuerURL:    fake.issuer(),
		ClientID:     "reticora-app",
		ClientSecret: "top-secret",
		RedirectURL:  "https://app.example.com/callback",
	}), testSessionIssuer(t)).WithProvisioning(statusProvisioner{deactivated: map[string]bool{"user-123": true}}, "")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/callback",
		strings.NewReader(`{"code":"auth-code","state":"nonce","code_verifier":"verifier-123"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.Callback(w, req)

	if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), `"token"`) {
		t.Fatalf("login of a deactivated user: status %d body %s, want 403 without token", w.Code, w.Body.String())
	}
}

// mfaFailingProvisioner fails every login like a first login of an
// org_admin whose account cannot be bound to MFA (WP-358).
type mfaFailingProvisioner struct{ statusProvisioner }

func (mfaFailingProvisioner) EnsureUser(context.Context, string, string, string, string, string) (string, bool, error) {
	return "", false, fmt.Errorf("%w: keycloak: role mapping: status 403", ErrMFAUnavailable)
}

// TestCallbackFailsWhenMFACannotBeEnforced covers WP-358 (AUT-09): a login
// that would make an org_admin without MFA answers 503 without session and
// without naming the admin API.
func TestCallbackFailsWhenMFACannotBeEnforced(t *testing.T) {
	orgID := "123e4567-e89b-12d3-a456-426614174000"
	fake := newFakeOIDCServer(t)
	fake.mutateClaims = func(claims map[string]any) {
		claims["groups"] = []string{orgID}
	}
	handler := NewHandler(NewOIDCProvider(OIDCConfig{
		IssuerURL:    fake.issuer(),
		ClientID:     "reticora-app",
		ClientSecret: "top-secret",
		RedirectURL:  "https://app.example.com/callback",
	}), testSessionIssuer(t)).WithProvisioning(mfaFailingProvisioner{}, "")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/callback",
		strings.NewReader(`{"code":"auth-code","state":"nonce","code_verifier":"verifier-123"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.Callback(w, req)

	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), `"token"`) || strings.Contains(w.Body.String(), "keycloak") {
		t.Fatalf("login without enforceable MFA: status %d body %s, want 503 without token and cause", w.Code, w.Body.String())
	}
	if len(w.Result().Cookies()) != 0 {
		t.Errorf("refresh cookie set: %v", w.Result().Cookies())
	}
}

// quotaLimitError is the refusal of the entitlement service when max_users
// is reached (entitlement.LimitExceededError; that package imports this one).
type quotaLimitError struct{}

func (quotaLimitError) Error() string {
	return `entitlement limit for "max_users" exceeded: 1 of 1 in use`
}
func (quotaLimitError) LimitExceeded() bool { return true }
func (quotaLimitError) ProblemType() string { return api.ProblemEntitlementLimit }

// quotaFailingProvisioner fails every login like a first login with an
// invitation while max_users is reached (WP-472).
type quotaFailingProvisioner struct{ statusProvisioner }

func (quotaFailingProvisioner) EnsureUser(context.Context, string, string, string, string, string) (string, bool, error) {
	return "", false, fmt.Errorf("ensure oidc user: %w", quotaLimitError{})
}

// TestCallbackRefusesFirstLoginOverUserQuota covers WP-472 (ENT-03): a
// first login with an invitation at max_users answers 403
// entitlement-limit without session and refresh cookie.
func TestCallbackRefusesFirstLoginOverUserQuota(t *testing.T) {
	orgID := "123e4567-e89b-12d3-a456-426614174000"
	fake := newFakeOIDCServer(t)
	fake.mutateClaims = func(claims map[string]any) {
		claims["groups"] = []string{orgID}
	}
	handler := NewHandler(NewOIDCProvider(OIDCConfig{
		IssuerURL:    fake.issuer(),
		ClientID:     "reticora-app",
		ClientSecret: "top-secret",
		RedirectURL:  "https://app.example.com/callback",
	}), testSessionIssuer(t)).WithProvisioning(quotaFailingProvisioner{}, "")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/callback",
		strings.NewReader(`{"code":"auth-code","state":"nonce","code_verifier":"verifier-123"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.Callback(w, req)

	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), api.ProblemEntitlementLimit) || strings.Contains(w.Body.String(), `"token"`) {
		t.Fatalf("first login over max_users: status %d body %s, want 403 entitlement-limit without token", w.Code, w.Body.String())
	}
	if len(w.Result().Cookies()) != 0 {
		t.Errorf("refresh cookie set: %v", w.Result().Cookies())
	}
}

// dbFailingProvisioner fails every login with the text of a database error.
type dbFailingProvisioner struct{ statusProvisioner }

func (dbFailingProvisioner) EnsureUser(context.Context, string, string, string, string, string) (string, bool, error) {
	return "", false, errors.New(`ERROR: new row violates row-level security policy for table "app_user" (SQLSTATE 42501)`)
}

// TestCallbackHidesProvisioningErrors covers API-03 (WP-408): a login whose
// user provisioning fails answers 502 with a fixed text; the database error
// goes to the log only.
func TestCallbackHidesProvisioningErrors(t *testing.T) {
	orgID := "123e4567-e89b-12d3-a456-426614174000"
	fake := newFakeOIDCServer(t)
	fake.mutateClaims = func(claims map[string]any) {
		claims["groups"] = []string{orgID}
	}
	handler := NewHandler(NewOIDCProvider(OIDCConfig{
		IssuerURL:    fake.issuer(),
		ClientID:     "reticora-app",
		ClientSecret: "top-secret",
		RedirectURL:  "https://app.example.com/callback",
	}), testSessionIssuer(t)).WithProvisioning(dbFailingProvisioner{}, "")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/callback",
		strings.NewReader(`{"code":"auth-code","state":"nonce","code_verifier":"verifier-123"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.Callback(w, req)

	body := w.Body.String()
	if w.Code != http.StatusBadGateway || !strings.Contains(body, "the login could not be completed") {
		t.Fatalf("login with failing provisioning: status %d body %s, want 502 with the fixed text", w.Code, body)
	}
	for _, leak := range []string{"SQLSTATE", "app_user", "row-level security", "provision user", `"token"`} {
		if strings.Contains(body, leak) {
			t.Errorf("login with failing provisioning: body %s contains %q", body, leak)
		}
	}
}

// fakeMFA records the OIDC subjects RequireMFA is called with; err fails
// every binding (an admin API that refuses, or no admin client like
// user.UnavailableMFA).
type fakeMFA struct {
	subjects []string
	err      error
}

func (m *fakeMFA) RequireMFA(_ context.Context, subject string) error {
	m.subjects = append(m.subjects, subject)
	return m.err
}

// loginProvisioner provisions every login as the app_user app-user-1, so
// the session subject differs from the OIDC subject. assigned reports a
// first login that assigned the default role; holds names a standard role
// the user holds in the database (assigned at this or an earlier login);
// err fails the provisioning.
type loginProvisioner struct {
	assigned bool
	holds    string
	err      error
}

func (p loginProvisioner) EnsureUser(context.Context, string, string, string, string, string) (string, bool, error) {
	if p.err != nil {
		return "", false, p.err
	}
	return "app-user-1", p.assigned, nil
}

// mfaLogin runs the login callback of the OIDC subject user-123 with the
// IdP groups through a handler with the enforcer (nil: none), the
// provisioner (nil: none) and the default role. The role a loginProvisioner
// holds is the stored org-wide assignment the resolver returns.
func mfaLogin(t *testing.T, groups []string, mfa *fakeMFA, provisioner UserProvisioner, defaultRole string) *httptest.ResponseRecorder {
	t.Helper()
	fake := newFakeOIDCServer(t)
	fake.mutateClaims = func(claims map[string]any) { claims["groups"] = groups }
	roles := standardRoleResolver{"org_admin": allPermissions(), "viewer": {PermCIRead}}
	resolver := storedRoleResolver{standardRoleResolver: roles}
	if p, ok := provisioner.(loginProvisioner); ok && p.holds != "" {
		resolver.stored = []Grant{OrgWideGrant(roles[p.holds])}
	}
	handler := NewHandler(NewOIDCProvider(OIDCConfig{
		IssuerURL:    fake.issuer(),
		ClientID:     "reticora-app",
		ClientSecret: "top-secret",
		RedirectURL:  "https://app.example.com/callback",
	}), testSessionIssuer(t)).WithAccessResolver(resolver)
	if provisioner != nil {
		handler.WithProvisioning(provisioner, defaultRole)
	}
	if mfa != nil {
		handler.WithMFA(mfa)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/callback",
		strings.NewReader(`{"code":"auth-code","state":"nonce","code_verifier":"verifier-123"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.Callback(w, req)
	return w
}

// hasSession reports whether a login response issued a session: an access
// token and the refresh cookie.
func hasSession(w *httptest.ResponseRecorder) bool {
	for _, c := range w.Result().Cookies() {
		if c.Name == RefreshCookieName && c.Value != "" {
			return strings.Contains(w.Body.String(), `"token"`)
		}
	}
	return false
}

// storedRoleResolver is a standardRoleResolver whose user holds the stored
// grants.
type storedRoleResolver struct {
	standardRoleResolver
	stored []Grant
}

func (r storedRoleResolver) AccessGrants(context.Context, string, string) ([]Grant, error) {
	return r.stored, nil
}

// TestLoginBindsOrgAdminOfGroupAndDefaultRoleToMFA covers WP-367 (AUT-09,
// CH26, E-14, finding F-005) with WP-637 (F-364): a login that grants
// org_admin through the IdP group org_admin (no database assignment needed)
// or a first login that assigns the default role org_admin binds the
// account of the ID token's subject (not the app_user id) exactly once
// before the session is issued. Other groups, a default role the first
// login did not assign (another role assigned before, or a later login,
// which assigns no role) and other default roles bind nothing; a failing
// provisioning (the default role is assigned in the login transaction)
// answers without session and binds nothing; a failing binding answers 503
// without session; a handler without enforcer binds nothing; refresh never
// binds.
func TestLoginBindsOrgAdminOfGroupAndDefaultRoleToMFA(t *testing.T) {
	for _, c := range []struct {
		name        string
		groups      []string
		provisioner UserProvisioner
		defaultRole string
		want        int
	}{
		{"IdP group /org_admin without provisioning or database assignment", []string{"/org_admin"}, nil, "", 1},
		{"IdP group org_admin (name without path)", []string{"org_admin"}, nil, "", 1},
		{"IdP group /org_admin of a provisioned user", []string{"/org_admin"}, loginProvisioner{}, "viewer", 1},
		{"IdP group /reticora-viewer", []string{"/reticora-viewer"}, loginProvisioner{}, "viewer", 0},
		{"default role org_admin at the first login", []string{"/reticora-viewer"}, loginProvisioner{assigned: true, holds: "org_admin"}, "org_admin", 1},
		{"default role org_admin at the first login, no IdP role", nil, loginProvisioner{assigned: true, holds: "org_admin"}, "org_admin", 1},
		{"default role org_admin and IdP group /org_admin", []string{"/org_admin"}, loginProvisioner{assigned: true, holds: "org_admin"}, "org_admin", 1},
		{"default role org_admin not assigned (another role assigned before)", nil, loginProvisioner{holds: "viewer"}, "org_admin", 0},
		{"default role org_admin of an earlier login (a later login assigns nothing)", nil, loginProvisioner{holds: "org_admin"}, "org_admin", 0},
		{"default role viewer", nil, loginProvisioner{assigned: true, holds: "viewer"}, "viewer", 0},
	} {
		mfa := &fakeMFA{}
		w := mfaLogin(t, c.groups, mfa, c.provisioner, c.defaultRole)
		if w.Code != http.StatusOK || !hasSession(w) {
			t.Errorf("%s: status %d, session %v; want 200 with session", c.name, w.Code, hasSession(w))
			continue
		}
		if len(mfa.subjects) != c.want || (c.want == 1 && mfa.subjects[0] != "user-123") {
			t.Errorf("%s: bound %v, want %d binding(s) of the OIDC subject user-123", c.name, mfa.subjects, c.want)
		}
	}

	// The default role is assigned in the login transaction: when that
	// fails, the login fails without session and binds nothing.
	mfa := &fakeMFA{}
	w := mfaLogin(t, []string{"/reticora-viewer"}, mfa, loginProvisioner{err: errors.New("check role assignments: timeout")}, "org_admin")
	if w.Code != http.StatusBadGateway || hasSession(w) || len(w.Result().Cookies()) != 0 || len(mfa.subjects) != 0 {
		t.Errorf("failing provisioning: status %d, session %v, %d cookies, bound %v; want 502 without session and binding",
			w.Code, hasSession(w), len(w.Result().Cookies()), mfa.subjects)
	}

	mfa = &fakeMFA{err: fmt.Errorf("%w: keycloak: role mapping: status 403", ErrMFAUnavailable)}
	w = mfaLogin(t, []string{"/org_admin"}, mfa, loginProvisioner{}, "viewer")
	if w.Code != http.StatusServiceUnavailable || hasSession(w) || len(w.Result().Cookies()) != 0 {
		t.Errorf("failing binding: status %d, session %v, %d cookies; want 503 without session", w.Code, hasSession(w), len(w.Result().Cookies()))
	}
	if strings.Contains(w.Body.String(), "keycloak") {
		t.Errorf("failing binding names the cause: %s", w.Body.String())
	}
	mfa = &fakeMFA{err: errors.New("keycloak: status 500")}
	if w = mfaLogin(t, []string{"/reticora-viewer"}, mfa, loginProvisioner{assigned: true, holds: "org_admin"}, "org_admin"); w.Code != http.StatusServiceUnavailable || hasSession(w) {
		t.Errorf("failing binding of the default role: status %d, session %v; want 503 without session", w.Code, hasSession(w))
	}

	if w = mfaLogin(t, []string{"/org_admin"}, nil, loginProvisioner{assigned: true, holds: "org_admin"}, "org_admin"); w.Code != http.StatusOK || !hasSession(w) {
		t.Errorf("handler without enforcer: status %d, session %v; want 200 with session", w.Code, hasSession(w))
	}

	// Refresh has no ID token: an org_admin of the group /org_admin keeps
	// its rights without a binding.
	sessionIssuer := testSessionIssuer(t)
	mfa = &fakeMFA{}
	handler := NewHandler(nil, sessionIssuer).
		WithAccessResolver(standardRoleResolver{"org_admin": allPermissions()}).
		WithProvisioning(loginProvisioner{assigned: true, holds: "org_admin"}, "org_admin").
		WithMFA(mfa)
	token, err := sessionIssuer.Issue(SessionClaims{
		Subject:        "app-user-1",
		OrganizationID: "123e4567-e89b-12d3-a456-426614174000",
		Groups:         []string{"/org_admin"},
		IssuedAt:       time.Now().UTC(),
		ExpiresAt:      time.Now().UTC().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	handler.Refresh(w, refreshRequest(t, handler, token))
	if w.Code != http.StatusOK || len(mfa.subjects) != 0 {
		t.Errorf("refresh of an org_admin of /org_admin: status %d, bound %v; want 200 without binding", w.Code, mfa.subjects)
	}
}

// TestLoginWithoutAdminClientAdmitsRealmAdminGroup covers WP-367 with
// E-65 option 1 (AUT-09): without Keycloak admin client the enforcer fails
// closed (user.UnavailableMFA). A member of the realm group reticora-admin,
// which carries mfa_required in the realm (WP-357), as the installer
// administrator of a standard installation, signs in with a session and
// without binding; a login through the IdP group org_admin answers 503
// without session. Only the exact realm group name is exempt.
func TestLoginWithoutAdminClientAdmitsRealmAdminGroup(t *testing.T) {
	noAdminClient := errors.New("no Keycloak admin client configured (RETICORA_KEYCLOAK_ADMIN_CLIENT_ID)")
	for _, groups := range [][]string{{"/reticora-admin"}, {"reticora-admin", "/reticora-viewer"}} {
		mfa := &fakeMFA{err: noAdminClient}
		w := mfaLogin(t, groups, mfa, loginProvisioner{}, "viewer")
		if w.Code != http.StatusOK || !hasSession(w) || len(mfa.subjects) != 0 {
			t.Errorf("member of %v: status %d, session %v, bound %v; want 200 with session and no binding",
				groups, w.Code, hasSession(w), mfa.subjects)
		}
	}
	for _, groups := range [][]string{{"/org_admin"}, {"/reticora-admin", "/org_admin"}, {"/Reticora-Admin"}} {
		mfa := &fakeMFA{err: noAdminClient}
		w := mfaLogin(t, groups, mfa, loginProvisioner{}, "viewer")
		if w.Code != http.StatusServiceUnavailable || hasSession(w) || len(w.Result().Cookies()) != 0 {
			t.Errorf("member of %v: status %d, session %v, %d cookies; want 503 without session", groups, w.Code, hasSession(w), len(w.Result().Cookies()))
		}
		if strings.Contains(w.Body.String(), "admin client") {
			t.Errorf("member of %v: the answer names the cause: %s", groups, w.Body.String())
		}
		if len(mfa.subjects) != 1 {
			t.Errorf("member of %v: bound %v, want one attempt", groups, mfa.subjects)
		}
	}
}

// TestRefreshChecksUserStatus covers WP-042 (AUT-02): refresh issues no new
// token for a deactivated user.
func TestRefreshChecksUserStatus(t *testing.T) {
	sessionIssuer := testSessionIssuer(t)
	now := time.Now().UTC()
	refresh := func(provisioner UserProvisioner, subject string) int {
		t.Helper()
		token, err := sessionIssuer.Issue(SessionClaims{
			Subject:        subject,
			OrganizationID: "123e4567-e89b-12d3-a456-426614174000",
			IssuedAt:       now.Add(-time.Minute),
			ExpiresAt:      now.Add(time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		handler := NewHandler(nil, sessionIssuer).WithProvisioning(provisioner, "")
		w := httptest.NewRecorder()
		handler.Refresh(w, refreshRequest(t, handler, token))
		return w.Code
	}
	provisioner := statusProvisioner{deactivated: map[string]bool{"locked": true}}
	if code := refresh(provisioner, "active"); code != http.StatusOK {
		t.Errorf("refresh of an active user: status %d, want 200", code)
	}
	if code := refresh(provisioner, "locked"); code != http.StatusUnauthorized {
		t.Errorf("refresh of a deactivated user: status %d, want 401", code)
	}
}

// TestValidateRejectsRevokedSessions covers WP-042 (AUT-02): after a
// revocation every earlier token of the user is rejected, later tokens and
// tokens of other users stay valid, and an unreadable blacklist fails closed.
func TestValidateRejectsRevokedSessions(t *testing.T) {
	store := cache.NewMemoryStore()
	revocations := NewSessionRevocations(store)
	issuer := testSessionIssuer(t).WithRevocations(revocations)
	issue := func(subject string, at time.Time) string {
		t.Helper()
		token, err := issuer.Issue(SessionClaims{Subject: subject, OrganizationID: "org", IssuedAt: at, ExpiresAt: at.Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	revokedAt := time.Now().UTC()
	before := issue("u1", revokedAt.Add(-time.Minute))
	same := issue("u1", revokedAt)
	after := issue("u1", revokedAt.Add(time.Second))
	other := issue("u2", revokedAt.Add(-time.Minute))
	if err := revocations.RevokeUser(context.Background(), "u1", revokedAt); err != nil {
		t.Fatal(err)
	}
	for name, token := range map[string]string{"before": before, "same instant": same} {
		if _, err := issuer.Validate(token); err != ErrSessionRevoked {
			t.Errorf("token issued %s the revocation: %v, want ErrSessionRevoked", name, err)
		}
	}
	for name, token := range map[string]string{"after": after, "other user": other} {
		if _, err := issuer.Validate(token); err != nil {
			t.Errorf("%s: %v, want valid", name, err)
		}
	}
	if err := store.Set(context.Background(), revocationKey("u2"), "garbage", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := issuer.Validate(other); err == nil {
		t.Error("unreadable revocation entry accepted the token")
	}
}

// recordingProvisioner records the e-mail it is asked to resolve.
type recordingProvisioner struct{ email *string }

func (p recordingProvisioner) EnsureUser(_ context.Context, _, subject, email, _, _ string) (string, bool, error) {
	*p.email = email
	return subject, false, nil
}

// TestCallbackTakesOrganizationFromAttribute covers WP-043 (AUT-01, AUT-09):
// a token without organization attribute is rejected even when a group is
// named like an organization, and an unverified e-mail is not passed on to
// resolve the user.
func TestCallbackTakesOrganizationFromAttribute(t *testing.T) {
	orgID := "123e4567-e89b-12d3-a456-426614174000"
	callback := func(mutate func(map[string]any), provisioner UserProvisioner) *httptest.ResponseRecorder {
		t.Helper()
		fake := newFakeOIDCServer(t)
		fake.mutateClaims = mutate
		handler := NewHandler(NewOIDCProvider(OIDCConfig{
			IssuerURL:    fake.issuer(),
			ClientID:     "reticora-app",
			ClientSecret: "top-secret",
			RedirectURL:  "https://app.example.com/callback",
		}), testSessionIssuer(t)).WithProvisioning(provisioner, "")
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/callback",
			strings.NewReader(`{"code":"auth-code","state":"nonce","code_verifier":"verifier-123"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.Callback(w, req)
		return w
	}

	var email string
	w := callback(func(c map[string]any) {
		delete(c, "organization_id")
		c["groups"] = []string{orgID, "reticora-admin"}
	}, recordingProvisioner{email: &email})
	if w.Code != http.StatusUnauthorized || strings.Contains(w.Body.String(), `"token"`) {
		t.Errorf("login without organization attribute: status %d body %s, want 401", w.Code, w.Body.String())
	}

	w = callback(func(c map[string]any) { c["email_verified"] = false }, recordingProvisioner{email: &email})
	if w.Code != http.StatusOK || email != "" {
		t.Errorf("unverified e-mail: status %d, provisioner got %q, want empty", w.Code, email)
	}
	w = callback(nil, recordingProvisioner{email: &email})
	if w.Code != http.StatusOK || email != "user@example.com" {
		t.Errorf("verified e-mail: status %d, provisioner got %q", w.Code, email)
	}
}

// standardRoleResolver grants the permissions of the listed standard roles
// and holds no role assignments.
type standardRoleResolver map[string][]Permission

func (r standardRoleResolver) AccessGrants(context.Context, string, string) ([]Grant, error) {
	return nil, nil
}

func (r standardRoleResolver) RoleGrants(_ context.Context, _ string, roleNames []string) ([]Grant, error) {
	grants := make([]Grant, 0, len(roleNames))
	for _, name := range roleNames {
		if perms, ok := r[name]; ok {
			grants = append(grants, OrgWideGrant(perms))
		}
	}
	return grants, nil
}

// TestIdPRolesMapToStandardRoles covers WP-046 (RBA-02): IdP roles and groups
// map to standard roles by an exact, defined mapping; names that only
// resemble a role, and client_technician, grant nothing.
func TestIdPRolesMapToStandardRoles(t *testing.T) {
	for _, c := range []struct {
		groups []string
		want   string
	}{
		{[]string{"reticora-admin"}, "org_admin"},
		{[]string{"/org_admin", "ORG_ADMIN"}, "org_admin"},
		{[]string{"reticora-engineer", "viewer"}, "engineer,viewer"},
		{[]string{"engineer"}, "engineer"},
		{[]string{"client_technician"}, ""},
		{[]string{"superadmin", "owners", "reticora-editor", "ci:delete", "readonly"}, ""},
		{[]string{"123e4567-e89b-12d3-a456-426614174000"}, ""},
	} {
		if got := strings.Join(StandardRolesForGroups(c.groups), ","); got != c.want {
			t.Errorf("StandardRolesForGroups(%v) = %q, want %q", c.groups, got, c.want)
		}
	}
}

// TestSessionPermissionsComeFromDatabaseRoles covers WP-046 (RBA-02): the
// session holds the database permissions of the mapped standard role, not a
// permission set derived from the group name.
func TestSessionPermissionsComeFromDatabaseRoles(t *testing.T) {
	sessionIssuer := testSessionIssuer(t)
	resolver := standardRoleResolver{"viewer": {PermCIRead, PermLocationRead}}
	token, err := sessionIssuer.Issue(SessionClaims{
		Subject:        "user-123",
		OrganizationID: "123e4567-e89b-12d3-a456-426614174000",
		Permissions:    []Permission{PermCredentialRead, PermCredentialManage},
		Groups:         []string{"reticora-viewer", "credential-admins"},
		IssuedAt:       time.Now().UTC(),
		ExpiresAt:      time.Now().UTC().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(nil, sessionIssuer).WithAccessResolver(resolver)
	w := httptest.NewRecorder()
	handler.Refresh(w, refreshRequest(t, handler, token))
	if w.Code != http.StatusOK {
		t.Fatalf("refresh: status %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Token string `json:"token"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	claims, err := sessionIssuer.Validate(out.Token)
	if err != nil {
		t.Fatal(err)
	}
	got := map[Permission]bool{}
	for _, p := range claims.Permissions {
		got[p] = true
	}
	if len(got) != 2 || !got[PermCIRead] || !got[PermLocationRead] {
		t.Errorf("session permissions %v, want exactly the viewer role's ci:read and site:read", claims.Permissions)
	}
}
