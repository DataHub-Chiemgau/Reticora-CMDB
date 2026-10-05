package operator

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// testRouter serves the operator routes without a database: the audit
// fails and is logged, the organization listing is not used.
func testRouter(h *Handler) *chi.Mux {
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	return mux
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
		"no operator, with otp":   {[]string{"reticora-admin"}, []string{"otp"}, "", false},
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
	mux := testRouter(NewHandler(Config{BreakGlassToken: testBreakGlass}, nil, issuer, NewAuditor(nil), nil))
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

	disabled := testRouter(NewHandler(Config{}, nil, issuer, NewAuditor(nil), nil))
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
