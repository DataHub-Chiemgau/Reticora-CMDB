package main

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
)

// keycloakTLSServer serves the two Keycloak endpoints DeleteAccount needs
// (client-credentials token, admin user delete) over TLS with a certificate
// that is not in the system trust store.
func keycloakTLSServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/realms/reticora/protocol/openid-connect/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"admin-token"}`))
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/admin/realms/reticora/users/"):
			if r.Header.Get("Authorization") != "Bearer admin-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestKeycloakAccountsTrustTheOIDCCABundle: the Keycloak admin client
// (signup, invitations, MFA binding) reaches the issuer through the same
// RETICORA_OIDC_CA_CERT_FILE as the OIDC provider. Before WP-646 it used a
// client with the system trust store only, so a TLS origin with a private CA
// failed the signup with "x509: certificate signed by unknown authority".
func TestKeycloakAccountsTrustTheOIDCCABundle(t *testing.T) {
	srv := keycloakTLSServer(t)
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(caFile, certPEM, 0o600); err != nil {
		t.Fatalf("write CA bundle: %v", err)
	}
	cfg := &config.Config{
		OIDCIssuerURL:             srv.URL + "/realms/reticora",
		OIDCCACertFile:            caFile,
		KeycloakAdminClientID:     "reticora-admin",
		KeycloakAdminClientSecret: "secret",
	}
	kc := keycloakAccounts(cfg)
	if kc == nil {
		t.Fatal("keycloakAccounts returned nil")
	}
	if err := kc.DeleteAccount(context.Background(), "user-1"); err != nil {
		t.Fatalf("admin call over TLS with the OIDC CA bundle: %v", err)
	}
}

// TestKeycloakAccountsWithoutCABundleUseTheSystemStore: without the bundle
// the private certificate stays untrusted; verification is not switched off.
func TestKeycloakAccountsWithoutCABundleUseTheSystemStore(t *testing.T) {
	srv := keycloakTLSServer(t)
	cfg := &config.Config{
		OIDCIssuerURL:             srv.URL + "/realms/reticora",
		KeycloakAdminClientID:     "reticora-admin",
		KeycloakAdminClientSecret: "secret",
	}
	kc := keycloakAccounts(cfg)
	if kc == nil {
		t.Fatal("keycloakAccounts returned nil")
	}
	err := kc.DeleteAccount(context.Background(), "user-1")
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("want a certificate verification error, got %v", err)
	}
}

// TestKeycloakAccountsDisabledOnUnreadableCABundle: a broken bundle disables
// the admin client instead of falling back to the system store silently.
func TestKeycloakAccountsDisabledOnUnreadableCABundle(t *testing.T) {
	cfg := &config.Config{
		OIDCIssuerURL:             "https://idp.example/realms/reticora",
		OIDCCACertFile:            filepath.Join(t.TempDir(), "absent.pem"),
		KeycloakAdminClientID:     "reticora-admin",
		KeycloakAdminClientSecret: "secret",
	}
	if kc := keycloakAccounts(cfg); kc != nil {
		t.Fatal("keycloakAccounts must be nil with an unreadable CA bundle")
	}
}
