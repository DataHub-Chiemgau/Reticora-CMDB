package operator

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
	"maps"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/go-chi/chi/v5"
)

const testBreakGlass = "break-glass-token-with-at-least-32-chars"

func testIssuer(t *testing.T) *identity.SessionIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := identity.NewSessionIssuer(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	if err != nil {
		t.Fatal(err)
	}
	return issuer
}

// testRouter serves the operator routes. Without a database the handler
// needs an audit stand-in (withAudit): an Auditor without a pool fails, and
// with it every operator request (F-208).
func testRouter(h *Handler) *chi.Mux {
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	return mux
}

// memoryAudit is operator_audit without a database: it keeps the entries
// and runs a change without a transaction. With fail set every write fails;
// a change already made then stays (no rollback without a database), so
// the atomicity is proven against PostgreSQL (audit_integration_test.go).
type memoryAudit struct {
	mu      sync.Mutex
	entries []AuditEntry
	fail    error
}

func (m *memoryAudit) Record(_ context.Context, e *AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return fmt.Errorf("%w: %w", ErrAuditFailed, m.fail)
	}
	m.entries = append(m.entries, *e)
	return nil
}

func (m *memoryAudit) RecordChange(ctx context.Context, _ string, change func(ctx context.Context) (*AuditEntry, error)) error {
	e, err := change(ctx)
	if err != nil {
		return err
	}
	return m.Record(ctx, e)
}

func (m *memoryAudit) List(context.Context, int, int) ([]AuditEntry, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.entries), len(m.entries), nil
}

func (m *memoryAudit) Verify(context.Context) (VerifyResult, error) {
	return VerifyResult{Valid: true, Checked: len(m.entries)}, nil
}

// actions lists the actions of the recorded entries.
func (m *memoryAudit) actions() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, e := range m.entries {
		out = append(out, e.Action)
	}
	return out
}

// withAudit replaces the audit of h.
func withAudit(h *Handler, a auditLog) *Handler {
	h.audit = a
	return h
}

func get(mux http.Handler, path string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// TestOperatorLoginRequiresGroupAndMFA covers WP-070 (SEC-07): an operator
// session is issued only to members of the operator group who logged in with
// a second factor; it carries the operator flag and no organization.
func TestOperatorLoginRequiresGroupAndMFA(t *testing.T) {
	issuer := testIssuer(t)
	h := NewHandler(Config{}, nil, issuer, NewAuditor(nil), nil)
	for name, tc := range map[string]struct {
		groups []string
		amr    []string
		acr    string
		ok     bool
	}{
		"operator with otp":       {[]string{"operators"}, []string{"pwd", "otp"}, "", true},
		"operator with webauthn":  {[]string{"operators"}, []string{"webauthn"}, "", true},
		"operator with acr 2":     {[]string{"operators"}, nil, "2", true},
		"operator, password only": {[]string{"operators"}, []string{"pwd"}, "1", false},
		"operator, acr 1":         {[]string{"operators"}, nil, "1", false},
		"operator, acr 0 (SSO)":   {[]string{"operators"}, nil, "0", false},
		"operator, no acr":        {[]string{"operators"}, nil, "", false},
		"no operator, with otp":   {[]string{"reticora-admin"}, []string{"otp"}, "", false},
		"no operator, acr 2":      {[]string{"reticora-admin"}, nil, "2", false},
		"full group path, acr 2":  {[]string{"/operators"}, nil, "2", false},
	} {
		t.Run(name, func(t *testing.T) {
			token, expires, err := h.issue(&identity.IDTokenClaims{Subject: "op-1", Name: "Ops", Email: "ops@example.com",
				Groups: tc.groups, AMR: tc.amr, ACR: tc.acr})
			if !tc.ok {
				if !errors.Is(err, errNotOperator) {
					t.Fatalf("issue: %v, want errNotOperator", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			claims, err := issuer.Validate(token)
			if err != nil {
				t.Fatal(err)
			}
			if !claims.Operator || claims.OrganizationID != "" || claims.Subject != "op-1" || len(claims.Permissions) != 0 {
				t.Errorf("operator claims %+v", claims)
			}
			if d := time.Until(expires); d > SessionLifetime || d < SessionLifetime-time.Minute {
				t.Errorf("session lifetime %v, want %v", d, SessionLifetime)
			}
		})
	}
}

// TestOperatorPathAdmitsOperatorsOnly covers WP-070 (SEC-07): /admin admits
// operator sessions and the break-glass token, refuses tenant sessions,
// expired operator sessions, wrong or disabled break-glass tokens and
// requests without credentials; every break-glass use is counted (alarm).
// The tenant API refuses operator sessions.
func TestOperatorPathAdmitsOperatorsOnly(t *testing.T) {
	issuer := testIssuer(t)
	mux := testRouter(withAudit(NewHandler(Config{BreakGlassToken: testBreakGlass}, nil, issuer, nil, nil), &memoryAudit{}))
	now := time.Now().UTC()
	sign := func(c identity.SessionClaims) string {
		t.Helper()
		token, err := issuer.Issue(c)
		if err != nil {
			t.Fatal(err)
		}
		return "Bearer " + token
	}
	operatorToken := sign(identity.SessionClaims{Subject: "op-1", Name: "Ops", Operator: true, IssuedAt: now, ExpiresAt: now.Add(time.Minute)})
	tenantToken := sign(identity.SessionClaims{Subject: "user-1", OrganizationID: "11111111-1111-4111-8111-111111111111",
		Permissions: identity.AllPermissions(), IssuedAt: now, ExpiresAt: now.Add(time.Minute)})
	expired := sign(identity.SessionClaims{Subject: "op-1", Operator: true, IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(-time.Minute)})

	if w := get(mux, "/api/v1/admin/me", map[string]string{"Authorization": operatorToken}); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"kind":"oidc"`) || !strings.Contains(w.Body.String(), `"id":"op-1"`) {
		t.Errorf("operator session: %d %s", w.Code, w.Body.String())
	}
	for name, header := range map[string]map[string]string{
		"no credentials":           nil,
		"tenant session (admin)":   {"Authorization": tenantToken},
		"expired operator session": {"Authorization": expired},
		"wrong break-glass token":  {BreakGlassHeader: testBreakGlass + "x"},
		"API key":                  {"X-API-Key": "rk_live_" + strings.Repeat("a", 52)},
	} {
		if w := get(mux, "/api/v1/admin/me", header); w.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", name, w.Code)
		}
	}

	before := BreakGlassUses()
	if w := get(mux, "/api/v1/admin/me", map[string]string{BreakGlassHeader: testBreakGlass}); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"kind":"break_glass"`) {
		t.Errorf("break-glass: %d %s", w.Code, w.Body.String())
	}
	if BreakGlassUses() != before+1 {
		t.Errorf("break-glass use not counted: %d -> %d", before, BreakGlassUses())
	}

	disabled := testRouter(withAudit(NewHandler(Config{}, nil, issuer, nil, nil), &memoryAudit{}))
	if w := get(disabled, "/api/v1/admin/me", map[string]string{BreakGlassHeader: ""}); w.Code != http.StatusUnauthorized {
		t.Errorf("empty break-glass header without configured token: %d", w.Code)
	}

	// The tenant API refuses operator sessions.
	tenantAPI := middleware.AuthMiddlewareWithVerifier(issuer)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	if w := get(tenantAPI, "/api/v1/cis", map[string]string{"Authorization": operatorToken}); w.Code != http.StatusUnauthorized {
		t.Errorf("operator session on the tenant API: %d, want 401", w.Code)
	}
}

func TestEntryHashCoversEveryField(t *testing.T) {
	base := AuditEntry{Timestamp: time.Unix(1700000000, 0), OperatorID: "op", OperatorKind: KindOIDC, Action: "GET /me",
		Status: 200, Details: map[string]any{"remote": "1.2.3.4"}, PreviousHash: genesisHash}
	want, err := entryHash(&base)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(e *AuditEntry){
		"operator": func(e *AuditEntry) { e.OperatorID = "op2" },
		"kind":     func(e *AuditEntry) { e.OperatorKind = KindBreakGlass },
		"action":   func(e *AuditEntry) { e.Action = "GET /orgs" },
		"target":   func(e *AuditEntry) { e.TargetOrg = "x" },
		"status":   func(e *AuditEntry) { e.Status = 401 },
		"details":  func(e *AuditEntry) { e.Details = map[string]any{"remote": "5.6.7.8"} },
		"previous": func(e *AuditEntry) { e.PreviousHash = "ff" },
		"time":     func(e *AuditEntry) { e.Timestamp = e.Timestamp.Add(time.Microsecond) },
	} {
		e := base
		change(&e)
		if got, _ := entryHash(&e); got == want {
			t.Errorf("changing %s keeps the hash", name)
		}
	}
}

// fakeIdP is an authorization server test double for the operator login:
// discovery, JWKS and a token endpoint that answers every code with an ID
// token carrying the claims of the next login.
type fakeIdP struct {
	t      *testing.T
	server *httptest.Server
	key    *rsa.PrivateKey
	claims map[string]any
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIdP{t: t, key: key}
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"issuer": f.server.URL, "jwks_uri": f.server.URL + "/jwks"})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"keys": []map[string]any{{"kty": "RSA", "kid": "k1", "alg": "RS256", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
	})
	mux.HandleFunc("/protocol/openid-connect/token", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"access_token": "access", "id_token": f.idToken(), "expires_in": 300})
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeIdP) idToken() string {
	f.t.Helper()
	claims := map[string]any{"iss": f.server.URL, "sub": "op-sub", "aud": "reticora-app", "email": "ops@example.com",
		"email_verified": true, "name": "Ops", "iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix()}
	for k, v := range f.claims {
		claims[k] = v
	}
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "k1"})
	payload, err := json.Marshal(claims)
	if err != nil {
		f.t.Fatal(err)
	}
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	hash := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, hash[:])
	if err != nil {
		f.t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// TestOperatorCallbackRequiresGroupAndACR2 covers WP-361 (SEC-07, F-069):
// POST /admin/auth/callback with the ID token the shipped realm issues. A
// member of the realm group operators whose login reached acr 2 (OTP of the
// flow browser-mfa, requested with acr_values=2) gets an operator session
// (200, /admin/me kind oidc); acr 1 (password only), acr 0 (SSO without a
// new login), no acr, and a login without the group are refused with 403
// and a generic text. Break-glass is unchanged.
func TestOperatorCallbackRequiresGroupAndACR2(t *testing.T) {
	idp := newFakeIdP(t)
	issuer := testIssuer(t)
	oidc := identity.NewOIDCProvider(identity.OIDCConfig{IssuerURL: idp.server.URL, ClientID: "reticora-app",
		ClientSecret: "secret", RedirectURL: "https://app.example.com/auth/callback"})
	mux := testRouter(withAudit(NewHandler(Config{BreakGlassToken: testBreakGlass, Group: "operators", MFAACRValues: []string{"2"}},
		oidc, issuer, nil, nil), &memoryAudit{}))
	callback := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/auth/callback",
			strings.NewReader(`{"code":"c","state":"s","code_verifier":"v"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	// Operator with acr 2: 200 and a session /admin accepts.
	idp.claims = map[string]any{"groups": []string{"operators"}, "acr": "2"}
	w := callback()
	if w.Code != http.StatusOK {
		t.Fatalf("operator with acr 2: %d %s, want 200", w.Code, w.Body.String())
	}
	var session struct {
		Token    string   `json:"token"`
		Operator Identity `json:"operator"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil || session.Token == "" ||
		session.Operator.Kind != KindOIDC || session.Operator.ID != "op-sub" {
		t.Fatalf("operator session %s (%v)", w.Body.String(), err)
	}
	if me := get(mux, "/api/v1/admin/me", map[string]string{"Authorization": "Bearer " + session.Token}); me.Code != http.StatusOK ||
		!strings.Contains(me.Body.String(), `"kind":"oidc"`) || !strings.Contains(me.Body.String(), `"id":"op-sub"`) {
		t.Errorf("/admin/me with the operator session: %d %s", me.Code, me.Body.String())
	}

	for name, claims := range map[string]map[string]any{
		"operator, acr 1 (password only)": {"groups": []string{"operators"}, "acr": "1"},
		"operator, acr 0 (SSO)":           {"groups": []string{"operators"}, "acr": "0"},
		"operator, no acr":                {"groups": []string{"operators"}},
		"tenant admin, acr 2":             {"groups": []string{"reticora-admin"}, "acr": "2"},
		"no groups, acr 2":                {"acr": "2"},
	} {
		idp.claims = claims
		w := callback()
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: %d %s, want 403", name, w.Code, w.Body.String())
			continue
		}
		if !strings.Contains(w.Body.String(), errNotOperator.Error()) || strings.Contains(w.Body.String(), "group") {
			t.Errorf("%s: response %s, want the generic refusal", name, w.Body.String())
		}
	}

	if bg := get(mux, "/api/v1/admin/me", map[string]string{BreakGlassHeader: testBreakGlass}); bg.Code != http.StatusOK ||
		!strings.Contains(bg.Body.String(), `"kind":"break_glass"`) {
		t.Errorf("break-glass: %d %s", bg.Code, bg.Body.String())
	}
}

// TestOperatorActionsFailWithoutTheirAuditEntry covers WP-564 (SEC-07,
// F-208) without a database: when the operator_audit entry cannot be
// written (an Auditor without a database, a failing audit), operator
// actions without a data change (login, /admin/me, a refused request)
// answer 500 instead of their response, the login hands out no session,
// and a grant is not carried out.
func TestOperatorActionsFailWithoutTheirAuditEntry(t *testing.T) {
	idp := newFakeIdP(t)
	issuer := testIssuer(t)
	oidc := identity.NewOIDCProvider(identity.OIDCConfig{IssuerURL: idp.server.URL, ClientID: "reticora-app",
		ClientSecret: "secret", RedirectURL: "https://app.example.com/auth/callback"})
	now := time.Now().UTC()
	operatorToken, err := issuer.Issue(identity.SessionClaims{Subject: "op-1", Operator: true, IssuedAt: now, ExpiresAt: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	const org = "11111111-1111-4111-8111-111111111111"

	for name, audit := range map[string]auditLog{
		"auditor without a database": NewAuditor(nil),
		"failing audit":              &memoryAudit{fail: errors.New("insert operator audit: disk full")},
	} {
		t.Run(name, func(t *testing.T) {
			svc := entitlement.NewService(entitlement.NewMemoryRepository(), entitlement.Options{Enforce: true})
			mux := testRouter(withAudit(NewHandler(Config{BreakGlassToken: testBreakGlass, Group: "operators", MFAACRValues: []string{"2"}},
				oidc, issuer, nil, nil), audit).WithEntitlements(svc))
			for what, header := range map[string]map[string]string{
				"operator session": {"Authorization": "Bearer " + operatorToken},
				"break-glass":      {BreakGlassHeader: testBreakGlass},
				"no credentials":   nil,
			} {
				w := get(mux, "/api/v1/admin/me", header)
				if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), `"kind"`) {
					t.Errorf("/admin/me, %s: %d %s, want 500 without the identity", what, w.Code, w.Body.String())
				}
			}
			for login, claims := range map[string]map[string]any{
				"operator, acr 2": {"groups": []string{"operators"}, "acr": "2"},
				"operator, acr 1": {"groups": []string{"operators"}, "acr": "1"},
			} {
				idp.claims = claims
				req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/auth/callback",
					strings.NewReader(`{"code":"c","state":"s","code_verifier":"v"}`))
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, req)
				if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "token") {
					t.Errorf("login %s: %d %s, want 500 without a session", login, w.Code, w.Body.String())
				}
			}
			w := post(mux, "/api/v1/admin/orgs/"+org+"/entitlements", `{"feature_key":"ticketing"}`,
				map[string]string{"Authorization": "Bearer " + operatorToken})
			if w.Code != http.StatusInternalServerError {
				t.Errorf("grant: %d %s, want 500", w.Code, w.Body.String())
			}
		})
	}

	// An Auditor without a database refuses before the change.
	svc := entitlement.NewService(entitlement.NewMemoryRepository(), entitlement.Options{Enforce: true})
	mux := testRouter(NewHandler(Config{BreakGlassToken: testBreakGlass}, nil, issuer, NewAuditor(nil), nil).WithEntitlements(svc))
	if w := post(mux, "/api/v1/admin/orgs/"+org+"/entitlements", `{"feature_key":"ticketing"}`,
		map[string]string{BreakGlassHeader: testBreakGlass}); w.Code != http.StatusInternalServerError {
		t.Errorf("grant without audit: %d %s, want 500", w.Code, w.Body.String())
	}
	if svc.IsEnabled(context.Background(), org, entitlement.FeatureTicketing) {
		t.Error("grant without audit entry took effect")
	}
}

// TestFailedOperatorAuthenticationCountsWithoutItsAuditEntry covers WP-658
// (AUT-10, review of F-387): a failed operator authentication spends its
// unit of the pre-auth budget of the IP even when its operator_audit entry
// cannot be written and the request is answered 500 (F-208). 20 requests
// without credentials from one IP get 500, the 21st 429; before, the 500
// gave the unit back and the failures were never counted. A request with
// the break-glass token keeps its unit only while it is served.
func TestFailedOperatorAuthenticationCountsWithoutItsAuditEntry(t *testing.T) {
	if left := 60 - time.Now().UTC().Unix()%60; left < 5 {
		time.Sleep(time.Duration(left)*time.Second + 100*time.Millisecond)
	}
	issuer := testIssuer(t)
	handler := middleware.PreAuthRateLimiter(middleware.DefaultPreAuthPerMinute, cache.NewMemoryStore())(
		testRouter(withAudit(NewHandler(Config{BreakGlassToken: testBreakGlass}, nil, issuer, nil, nil),
			&memoryAudit{fail: errors.New("insert operator audit: disk full")})))
	serve := func(ip string, header map[string]string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/me", nil)
		req.RemoteAddr = ip + ":40000"
		for k, v := range header {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w.Code
	}
	codes := map[int]int{}
	for range middleware.DefaultPreAuthPerMinute + 1 {
		codes[serve("198.51.100.58", nil)]++
	}
	if want := map[int]int{http.StatusInternalServerError: middleware.DefaultPreAuthPerMinute, http.StatusTooManyRequests: 1}; !maps.Equal(codes, want) {
		t.Errorf("21 operator requests without credentials of one IP, audit failing: %v, want %v (a failed authentication counts)", codes, want)
	}
	codes = map[int]int{}
	for range middleware.DefaultPreAuthPerMinute + 1 {
		codes[serve("198.51.100.59", map[string]string{BreakGlassHeader: testBreakGlass})]++
	}
	if want := map[int]int{http.StatusInternalServerError: middleware.DefaultPreAuthPerMinute + 1}; !maps.Equal(codes, want) {
		t.Errorf("21 operator requests with the break-glass token, audit failing: %v, want %v (an authenticated request spends nothing)", codes, want)
	}
}

// TestRealmOperatorLoginConfiguration covers WP-361 (SEC-07, F-069): both
// shipped realm exports make the personalized operator login possible. The
// group operators carries the realm role operator (which contains
// mfa_required); the reticora-app client has the default scope acr; the
// realm maps acr 1 and 2 to the levels of authentication of the flow
// browser-mfa, whose password subflow is level 1 and whose level 2 is
// reached only through an OTP: holders of mfa_required enter it in
// browser-mfa otp (before level 2), all others in the level-2 subflow.
func TestRealmOperatorLoginConfiguration(t *testing.T) {
	type execution struct {
		Authenticator       string `json:"authenticator"`
		AuthenticatorConfig string `json:"authenticatorConfig"`
		FlowAlias           string `json:"flowAlias"`
		Requirement         string `json:"requirement"`
		Priority            int    `json:"priority"`
	}
	for _, file := range []string{"realm-reticora.json", "realm-reticora-dev.json"} {
		t.Run(file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "keycloak", file))
			if err != nil {
				t.Fatalf("read realm export: %v", err)
			}
			var realm struct {
				BrowserFlow string            `json:"browserFlow"`
				Attributes  map[string]string `json:"attributes"`
				Roles       struct {
					Realm []struct {
						Name       string `json:"name"`
						Composites struct {
							Realm []string `json:"realm"`
						} `json:"composites"`
					} `json:"realm"`
				} `json:"roles"`
				Clients []struct {
					ClientID            string   `json:"clientId"`
					DefaultClientScopes []string `json:"defaultClientScopes"`
				} `json:"clients"`
				Groups []struct {
					Name       string   `json:"name"`
					Path       string   `json:"path"`
					RealmRoles []string `json:"realmRoles"`
				} `json:"groups"`
				Flows []struct {
					Alias      string      `json:"alias"`
					Executions []execution `json:"authenticationExecutions"`
				} `json:"authenticationFlows"`
				Configs []struct {
					Alias  string            `json:"alias"`
					Config map[string]string `json:"config"`
				} `json:"authenticatorConfig"`
			}
			if err := json.Unmarshal(raw, &realm); err != nil {
				t.Fatal(err)
			}

			group := false
			for _, g := range realm.Groups {
				if g.Name == "operators" {
					group = g.Path == "/operators" && slices.Equal(g.RealmRoles, []string{"operator"})
				}
			}
			if !group {
				t.Errorf("group operators with exactly the realm role operator missing: %+v", realm.Groups)
			}
			operatorMFA := false
			for _, r := range realm.Roles.Realm {
				if r.Name == "operator" {
					operatorMFA = slices.Contains(r.Composites.Realm, "mfa_required")
				}
			}
			if !operatorMFA {
				t.Error("realm role operator does not contain mfa_required")
			}
			acrScope := false
			for _, c := range realm.Clients {
				if c.ClientID == "reticora-app" {
					acrScope = slices.Contains(c.DefaultClientScopes, "acr")
				}
			}
			if !acrScope {
				t.Error("client reticora-app lacks the default client scope acr (no acr claim in the ID token)")
			}
			var loaMap map[string]int
			if err := json.Unmarshal([]byte(realm.Attributes["acr.loa.map"]), &loaMap); err != nil || loaMap["1"] != 1 || loaMap["2"] != 2 {
				t.Errorf("acr.loa.map %q, want acr 1 -> level 1 and acr 2 -> level 2", realm.Attributes["acr.loa.map"])
			}

			configs := map[string]map[string]string{}
			for _, c := range realm.Configs {
				configs[c.Alias] = c.Config
			}
			flows := map[string][]execution{}
			for _, f := range realm.Flows {
				ex := slices.Clone(f.Executions)
				slices.SortFunc(ex, func(a, b execution) int { return a.Priority - b.Priority })
				flows[f.Alias] = ex
			}
			if realm.BrowserFlow != "browser-mfa" {
				t.Fatalf("browser flow %q", realm.BrowserFlow)
			}
			// level returns the level of a subflow's LoA condition (first execution).
			level := func(alias string) string {
				ex := flows[alias]
				if len(ex) == 0 || ex[0].Authenticator != "conditional-level-of-authentication" {
					return ""
				}
				return configs[ex[0].AuthenticatorConfig]["loa-condition-level"]
			}
			authenticators := func(alias string) []string {
				var out []string
				for _, e := range flows[alias] {
					if e.Authenticator != "" && !strings.HasPrefix(e.Authenticator, "conditional-") {
						out = append(out, e.Authenticator)
					}
				}
				return out
			}
			var subflows []string
			for _, e := range flows["browser-mfa forms"] {
				if e.Requirement != "CONDITIONAL" || e.FlowAlias == "" {
					t.Errorf("browser-mfa forms: execution %+v, want conditional subflows only", e)
				}
				subflows = append(subflows, e.FlowAlias)
			}
			if len(subflows) != 3 {
				t.Fatalf("browser-mfa forms subflows %v, want password, otp, level 2", subflows)
			}
			password, otp, level2 := subflows[0], subflows[1], subflows[2]
			if level(password) != "1" || !slices.Equal(authenticators(password), []string{"auth-username-password-form"}) {
				t.Errorf("%s: level %q, authenticators %v; want level 1 with the password form", password, level(password), authenticators(password))
			}
			if ex := flows[otp]; len(ex) == 0 || ex[0].Authenticator != "conditional-user-role" ||
				configs[ex[0].AuthenticatorConfig]["condUserRole"] != "mfa_required" || configs[ex[0].AuthenticatorConfig]["negate"] == "true" ||
				!slices.Equal(authenticators(otp), []string{"auth-otp-form"}) {
				t.Errorf("%s: %+v, want OTP for holders of mfa_required", otp, ex)
			}
			if level(level2) != "2" || configs[flows[level2][0].AuthenticatorConfig]["loa-max-age"] != "0" {
				t.Errorf("%s: level %q, want level 2 with max age 0 (every operator login enters the OTP)", level2, level(level2))
			}
			// Level 2 grants access only after an OTP: its own OTP subflow
			// for users without mfa_required, otherwise the OTP of the
			// subflow before it.
			var nested string
			for _, e := range flows[level2] {
				if e.FlowAlias != "" {
					nested = e.FlowAlias
				}
			}
			if ex := flows[nested]; len(ex) == 0 || ex[0].Authenticator != "conditional-user-role" ||
				configs[ex[0].AuthenticatorConfig]["condUserRole"] != "mfa_required" || configs[ex[0].AuthenticatorConfig]["negate"] != "true" ||
				!slices.Equal(authenticators(nested), []string{"auth-otp-form"}) {
				t.Errorf("%s: nested subflow %q %+v, want OTP for users without mfa_required", level2, nested, ex)
			}
			if got := authenticators(level2); !slices.Equal(got, []string{"allow-access-authenticator"}) {
				t.Errorf("%s: authenticators %v, want only allow access after the OTP", level2, got)
			}
		})
	}
}
