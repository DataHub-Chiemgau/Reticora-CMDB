package plugins

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// tlsEndpoint starts an HTTPS endpoint with the self-signed httptest
// certificate (issued for 127.0.0.1).
func tlsEndpoint(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	// Rejected handshakes are expected; keep them out of the test output.
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// otherCA is a CA pool that did not issue the httptest certificate.
func otherCA(t *testing.T) *x509.CertPool {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "other-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return pool
}

// get fetches the endpoint with a client verifying with cfg.
func get(url string, cfg *tls.Config) error {
	resp, err := NewTLSClient(5*time.Second, cfg).Get(url)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

// TestTLSScopeVerification covers the certificate verification of the
// Redfish and WMI plugins (COL-02): the system roots outside every scope, the
// CA bundle or SPKI pin of a TLS scope as exception; whatever fails is an
// untrusted certificate.
func TestTLSScopeVerification(t *testing.T) {
	srv := tlsEndpoint(t)
	cert := srv.Certificate()
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	pin := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	wrong := sha256.Sum256([]byte("other key"))

	cases := []struct {
		name string
		cfg  *tls.Config
		ok   bool
	}{
		{"system roots reject the self-signed certificate", SystemTLSConfig(), false},
		{"CA bundle of the scope", TLSScope{RootCAs: pool}.TLSConfig(), true},
		{"CA bundle of another CA", TLSScope{RootCAs: otherCA(t)}.TLSConfig(), false},
		{"matching SPKI pin", TLSScope{PinsSHA256: [][]byte{pin[:]}}.TLSConfig(), true},
		{"matching pin among several", TLSScope{PinsSHA256: [][]byte{wrong[:], pin[:]}}.TLSConfig(), true},
		{"wrong pin", TLSScope{PinsSHA256: [][]byte{wrong[:]}}.TLSConfig(), false},
		{"CA plus matching pin", TLSScope{RootCAs: pool, PinsSHA256: [][]byte{pin[:]}}.TLSConfig(), true},
		{"CA plus wrong pin", TLSScope{RootCAs: pool, PinsSHA256: [][]byte{wrong[:]}}.TLSConfig(), false},
		{"wrong CA plus matching pin", TLSScope{RootCAs: otherCA(t), PinsSHA256: [][]byte{pin[:]}}.TLSConfig(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := get(srv.URL, tc.cfg)
			if tc.ok && err != nil {
				t.Fatalf("expected a verified connection, got %v", err)
			}
			if !tc.ok && (err == nil || !UntrustedCertificate(err)) {
				t.Fatalf("expected an untrusted certificate, got %v", err)
			}
		})
	}
	if tc := SystemTLSConfig(); tc.InsecureSkipVerify || tc.MinVersion < tls.VersionTLS12 {
		t.Errorf("system verification = %+v", tc)
	}
	// A connection failure is not a certificate failure.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	if err := get("https://"+addr+"/", SystemTLSConfig()); err == nil || UntrustedCertificate(err) {
		t.Errorf("refused connection classified as untrusted certificate: %v", err)
	}
}

// TestSystemRootsAreTrusted: outside every TLS scope a certificate issued by
// a system root is accepted. Go reads the system roots once per process, so
// the check runs in a child process whose SSL_CERT_FILE holds the issuer of
// the endpoint's certificate.
func TestSystemRootsAreTrusted(t *testing.T) {
	if url := os.Getenv("RETICORA_TEST_SYSTEM_ROOT_URL"); url != "" {
		if err := get(url, SystemTLSConfig()); err != nil {
			t.Fatalf("certificate of a system root rejected: %v", err)
		}
		return
	}
	if runtime.GOOS != "linux" {
		t.Skip("SSL_CERT_FILE selects the system roots on Linux only")
	}
	srv := tlsEndpoint(t)
	roots := filepath.Join(t.TempDir(), "roots.pem")
	if err := os.WriteFile(roots, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestSystemRootsAreTrusted$", "-test.count=1")
	cmd.Env = append(os.Environ(), "SSL_CERT_FILE="+roots, "SSL_CERT_DIR="+t.TempDir(), "RETICORA_TEST_SYSTEM_ROOT_URL="+srv.URL)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("system-root check: %v\n%s", err, out)
	}
	// Control: this process's system roots do not know the certificate.
	if err := get(srv.URL, SystemTLSConfig()); err == nil || !UntrustedCertificate(err) {
		t.Fatalf("certificate unknown to the system roots accepted: %v", err)
	}
}

// TestTLSClientsSelectTheScopeOfTheTarget: the first scope containing the
// target address wins; scopes without network or trust anchor are ignored,
// other targets use the fallback.
func TestTLSClientsSelectTheScopeOfTheTarget(t *testing.T) {
	network := func(cidr string) *net.IPNet {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	pin := sha256.Sum256([]byte("pin"))
	built := 0
	var c TLSClients
	c.Add(func(cfg *tls.Config) *http.Client {
		built++
		return NewTLSClient(time.Second, cfg)
	},
		TLSScope{Network: network("10.0.0.0/24"), PinsSHA256: [][]byte{pin[:]}},
		TLSScope{Network: network("10.0.0.0/8"), RootCAs: x509.NewCertPool()},
		TLSScope{Network: network("192.0.2.0/24")}, // no trust anchor
		TLSScope{RootCAs: x509.NewCertPool()},      // no network
		TLSScope{PinsSHA256: [][]byte{pin[:]}},     // no network
	)
	if len(c.scoped) != 2 {
		t.Fatalf("usable scopes = %d, want 2", len(c.scoped))
	}
	fallback := NewTLSClient(time.Second, SystemTLSConfig())
	if got := c.For("10.0.0.5", fallback); got != c.scoped[0].client {
		t.Error("10.0.0.5 must use the first matching scope (/24 pin)")
	}
	if got := c.For("10.1.0.5", fallback); got != c.scoped[1].client {
		t.Error("10.1.0.5 must use the /8 CA scope")
	}
	for _, target := range []string{"192.0.2.5", "198.51.100.1", "host.example"} {
		if got := c.For(target, fallback); got != fallback {
			t.Errorf("%s must use the fallback (system roots)", target)
		}
	}
	if built != 2 {
		t.Errorf("clients built = %d, want 2", built)
	}
}
