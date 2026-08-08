package collectorcmd

import (
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/buffer"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/keystore"
)

func testConfig(serverURL, spoolDir string) collectorConfig {
	return collectorConfig{
		ServerURL:      serverURL,
		OrganizationID: "org-1",
		CollectorID:    "collector-1",
		SpoolDir:       spoolDir,
	}
}

func TestUploadResults_PlainHTTPSuccess(t *testing.T) {
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Errorf("read body: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	up, err := newUploader(context.Background(), testConfig(srv.URL, ""))
	if err != nil {
		t.Fatalf("newUploader: %v", err)
	}
	if up.mtls {
		t.Error("expected plain HTTP client when no certificate material is configured")
	}

	results := []plugins.Result{{}}
	if err := up.uploadResults(context.Background(), results); err != nil {
		t.Fatalf("uploadResults: %v", err)
	}
	if gotHeaders.Get("Content-Encoding") != "gzip" {
		t.Errorf("expected gzip content encoding, got %q", gotHeaders.Get("Content-Encoding"))
	}
	if gotHeaders.Get("X-Organization-ID") != "org-1" || gotHeaders.Get("X-Collector-ID") != "collector-1" {
		t.Errorf("missing tenant/collector headers: %v", gotHeaders)
	}
}

func TestUploadResults_FailureSpoolsAndFlushDelivers(t *testing.T) {
	spoolDir := t.TempDir()

	// First attempt against an unreachable server spools the batch.
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	downURL := down.URL
	down.Close()

	up, err := newUploader(context.Background(), testConfig(downURL, spoolDir))
	if err != nil {
		t.Fatalf("newUploader: %v", err)
	}
	if err := up.uploadResults(context.Background(), []plugins.Result{{}}); err == nil {
		t.Fatal("expected upload error against unreachable server")
	}
	spool := &buffer.DiskBuffer{Dir: spoolDir}
	if n, _ := spool.Len(context.Background()); n != 1 {
		t.Fatalf("expected 1 spooled message, got %d", n)
	}

	// Once the backend is reachable again, flushSpool delivers and acks it.
	var delivered int
	up2srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delivered++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer up2srv.Close()

	up.cfg.ServerURL = up2srv.URL
	up.flushSpool(context.Background())
	if delivered != 1 {
		t.Fatalf("expected 1 flushed delivery, got %d", delivered)
	}
	if n, _ := spool.Len(context.Background()); n != 0 {
		t.Fatalf("expected empty spool after flush, got %d", n)
	}
}

func TestUploadResults_WithoutSpoolDirReturnsErrorOnly(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	downURL := down.URL
	down.Close()

	up, err := newUploader(context.Background(), testConfig(downURL, ""))
	if err != nil {
		t.Fatalf("newUploader: %v", err)
	}
	if err := up.uploadResults(context.Background(), []plugins.Result{{}}); err == nil {
		t.Fatal("expected upload error against unreachable server")
	}
}

func TestLoadTLSMaterial_FromKeystore(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials.json")
	certPEM, keyPEM := selfSignedCert(t, "collector-keystore")

	store := &keystore.FileStore{Path: credPath}
	if err := store.Save(context.Background(), keystore.Credentials{
		DeviceID:             "collector-1",
		ClientCertificatePEM: string(certPEM),
		ClientPrivateKeyPEM:  string(keyPEM),
	}); err != nil {
		t.Fatalf("save credentials: %v", err)
	}

	cfg := testConfig("", "")
	cfg.CredentialsPath = credPath
	cert, key, _, err := loadTLSMaterial(context.Background(), cfg)
	if err != nil {
		t.Fatalf("loadTLSMaterial: %v", err)
	}
	if string(cert) != string(certPEM) || string(key) != string(keyPEM) {
		t.Error("keystore material not loaded")
	}
}

func TestLoadTLSMaterial_IncompletePairFails(t *testing.T) {
	cfg := testConfig("", "")
	cfg.TLSCertPEM = []byte("cert-only")
	if _, _, _, err := loadTLSMaterial(context.Background(), cfg); err == nil {
		t.Fatal("expected error when only the certificate is provided")
	}
}

func TestNewUploader_UsesMTLSWhenConfigured(t *testing.T) {
	certPEM, keyPEM := selfSignedCert(t, "collector-mtls")

	cfg := testConfig("", "")
	cfg.TLSCertPEM = certPEM
	cfg.TLSKeyPEM = keyPEM

	up, err := newUploader(context.Background(), cfg)
	if err != nil {
		t.Fatalf("newUploader: %v", err)
	}
	if !up.mtls {
		t.Fatal("expected mTLS transport when certificate material is configured")
	}
	transport, ok := up.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", up.client.Transport)
	}
	if transport.TLSClientConfig == nil || len(transport.TLSClientConfig.Certificates) != 1 {
		t.Fatal("expected client certificate in TLS config")
	}
	if transport.TLSClientConfig.MinVersion != tls.VersionTLS13 {
		t.Errorf("expected TLS 1.3 minimum, got %x", transport.TLSClientConfig.MinVersion)
	}
}

func TestNewUploader_InvalidKeyPairFails(t *testing.T) {
	cfg := testConfig("", "")
	cfg.TLSCertPEM = []byte("not a cert")
	cfg.TLSKeyPEM = []byte("not a key")
	if _, err := newUploader(context.Background(), cfg); err == nil {
		t.Fatal("expected error for invalid key pair")
	}
}

// selfSignedCert generates an ephemeral ECDSA key pair for tests.
func selfSignedCert(t *testing.T, cn string) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// Ensure the upload payload round-trips as JSON for plugins.Result.
func TestPostPayload_SendsCompressedJSON(t *testing.T) {
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Errorf("gzip reader: %v", err)
			return
		}
		defer gz.Close()
		raw, err = io.ReadAll(gz)
		if err != nil {
			t.Errorf("read gzip: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	up, err := newUploader(context.Background(), testConfig(srv.URL, ""))
	if err != nil {
		t.Fatalf("newUploader: %v", err)
	}
	payload, _ := json.Marshal([]plugins.Result{{}})
	if err := up.postPayload(context.Background(), payload); err != nil {
		t.Fatalf("postPayload: %v", err)
	}
	var decoded []plugins.Result
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("server received invalid JSON: %v", err)
	}
	if len(decoded) != 1 {
		t.Errorf("expected 1 result, got %d", len(decoded))
	}
}
