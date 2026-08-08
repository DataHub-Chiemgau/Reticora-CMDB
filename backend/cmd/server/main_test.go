package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
)

func testSessionKeyPath(t *testing.T) string {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "session.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoadSessionIssuerFailsWithoutKeyInSecureMode proves that startup fails
// fast instead of silently accepting unsigned tokens when no session key is
// configured and the insecure development mode was not explicitly enabled.
func TestLoadSessionIssuerFailsWithoutKeyInSecureMode(t *testing.T) {
	issuer, err := loadSessionIssuer(&config.Config{})
	if err == nil {
		t.Fatal("expected an error when no session key is configured in secure mode")
	}
	if issuer != nil {
		t.Fatal("expected no issuer in secure mode without a key")
	}
}

// TestLoadSessionIssuerFailsOnUnreadableKeyInSecureMode proves that a key
// file that cannot be read is fatal in secure mode.
func TestLoadSessionIssuerFailsOnUnreadableKeyInSecureMode(t *testing.T) {
	cfg := &config.Config{SessionKeyPath: filepath.Join(t.TempDir(), "missing.pem")}
	if _, err := loadSessionIssuer(cfg); err == nil {
		t.Fatal("expected an error for an unreadable session key in secure mode")
	}
}

// TestLoadSessionIssuerDevModeOptIn proves that the insecure development mode
// is only reachable through the explicit opt-in flag.
func TestLoadSessionIssuerDevModeOptIn(t *testing.T) {
	issuer, err := loadSessionIssuer(&config.Config{AllowInsecureDevAuth: true})
	if err != nil {
		t.Fatalf("expected dev mode to start without a key, got %v", err)
	}
	if issuer != nil {
		t.Fatal("expected nil issuer in insecure dev mode")
	}

	// Even an unreadable key degrades to dev mode only with the opt-in.
	cfg := &config.Config{
		SessionKeyPath:       filepath.Join(t.TempDir(), "missing.pem"),
		AllowInsecureDevAuth: true,
	}
	issuer, err = loadSessionIssuer(cfg)
	if err != nil {
		t.Fatalf("expected dev mode to tolerate an unreadable key, got %v", err)
	}
	if issuer != nil {
		t.Fatal("expected nil issuer in insecure dev mode")
	}
}

// TestLoadSessionIssuerReadsValidKey proves the happy path: a readable RSA
// key produces a verifying issuer.
func TestLoadSessionIssuerReadsValidKey(t *testing.T) {
	cfg := &config.Config{SessionKeyPath: testSessionKeyPath(t)}
	issuer, err := loadSessionIssuer(cfg)
	if err != nil {
		t.Fatalf("expected a valid key to load, got %v", err)
	}
	if issuer == nil {
		t.Fatal("expected a session issuer for a valid key")
	}
}

// TestLoadSessionIssuerRejectsInvalidKey proves that a corrupt key file is
// fatal even in development mode, since it indicates a misconfiguration.
func TestLoadSessionIssuerRejectsInvalidKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.pem")
	if err := os.WriteFile(path, []byte("not a pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{SessionKeyPath: path, AllowInsecureDevAuth: true}
	if _, err := loadSessionIssuer(cfg); err == nil {
		t.Fatal("expected an error for a corrupt session key")
	}
}
