package identity

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNewHTTPClientWithCAEmptyPathReturnsNil(t *testing.T) {
	client, err := NewHTTPClientWithCA("  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client != nil {
		t.Fatalf("expected nil client for an empty path, got %#v", client)
	}
}

func TestNewHTTPClientWithCAMissingFile(t *testing.T) {
	if _, err := NewHTTPClientWithCA(filepath.Join(t.TempDir(), "absent.pem")); err == nil {
		t.Fatal("expected an error for a missing CA bundle")
	}
}

func TestNewHTTPClientWithCAInvalidBundle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.pem")
	if err := os.WriteFile(path, []byte("not a certificate"), 0o600); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	if _, err := NewHTTPClientWithCA(path); err == nil {
		t.Fatal("expected an error for a bundle without certificates")
	}
}

func TestNewHTTPClientWithCATrustsSelfSignedServer(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, encodeCertPEM(t, server), 0o600); err != nil {
		t.Fatalf("write bundle: %v", err)
	}

	client, err := NewHTTPClientWithCA(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client == nil {
		t.Fatal("expected a client")
	}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request to the self-signed server failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("unexpected status %d", resp.StatusCode)
	}

	// Without the bundle the same request must still be rejected.
	if _, err := (&http.Client{}).Get(server.URL); err == nil {
		t.Fatal("expected the default client to reject the self-signed certificate")
	}
}

func encodeCertPEM(t *testing.T, server *httptest.Server) []byte {
	t.Helper()
	cert := server.Certificate()
	if cert == nil {
		t.Fatal("test server has no certificate")
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
}
