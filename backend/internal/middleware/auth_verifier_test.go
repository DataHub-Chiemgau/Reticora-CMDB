package middleware

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

func newTestIssuer(t *testing.T) *identity.SessionIssuer {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})

	issuer, err := identity.NewSessionIssuer(keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return issuer
}

func TestAuthMiddlewareWithVerifierAcceptsSignedToken(t *testing.T) {
	issuer := newTestIssuer(t)
	now := time.Now().UTC()
	token, err := issuer.Issue(identity.SessionClaims{
		Subject:        "user-1",
		OrganizationID: "org-1",
		IssuedAt:       now,
		ExpiresAt:      now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}

	handler := AuthMiddlewareWithVerifier(issuer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := ClaimsFromContext(r.Context())
		if !ok || claims.Organization() != "org-1" || claims.Subject != "user-1" {
			t.Errorf("unexpected claims: %+v (ok=%v)", claims, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cis", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAuthMiddlewareWithVerifierRejectsUnsignedAndExpiredTokens(t *testing.T) {
	issuer := newTestIssuer(t)
	handler := AuthMiddlewareWithVerifier(issuer)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	// A token that was never signed by the issuer must be rejected even though
	// its claims are well-formed.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/cis", nil)
	req.Header.Set("Authorization", "Bearer "+testToken(t, Claims{OrganizationID: "org-1"}))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for an unsigned token, got %d", w.Code)
	}

	expired := time.Now().UTC().Add(-2 * time.Hour)
	token, err := issuer.Issue(identity.SessionClaims{
		Subject:        "user-1",
		OrganizationID: "org-1",
		IssuedAt:       expired,
		ExpiresAt:      expired.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/cis", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for an expired token, got %d", w.Code)
	}
}

func TestAuthMiddlewareWithVerifierSkipsPublicAuthRoutes(t *testing.T) {
	handler := AuthMiddlewareWithVerifier(newTestIssuer(t))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/auth/config", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected the public OIDC config route to stay reachable, got %d", w.Code)
	}
}
