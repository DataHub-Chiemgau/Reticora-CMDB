package transport

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// testPKI is a small in-memory CA plus server and client certificates.
type testPKI struct {
	caPEM      []byte
	serverCert tls.Certificate
	clientPEM  []byte
	clientKey  []byte
	serverName string
}

func newTestPKI(t *testing.T) testPKI {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})

	serverCert := issueCert(t, caCert, caKey, 2, "edge-server", x509.ExtKeyUsageServerAuth)
	clientCertPEM, clientKeyPEM := issueCertPEM(t, caCert, caKey, 3, "edge-client", x509.ExtKeyUsageClientAuth)

	return testPKI{
		caPEM:      caPEM,
		serverCert: serverCert,
		clientPEM:  clientCertPEM,
		clientKey:  clientKeyPEM,
		serverName: "edge-server",
	}
}

func issueCert(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, serial int64, cn string, usage x509.ExtKeyUsage) tls.Certificate {
	t.Helper()
	certPEM, keyPEM := issueCertPEM(t, ca, caKey, serial, cn, usage)
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("load issued key pair: %v", err)
	}
	return cert
}

func issueCertPEM(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, serial int64, cn string, usage x509.ExtKeyUsage) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     []string{cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func TestNewMTLSRequiresClientMaterial(t *testing.T) {
	if _, err := NewMTLS(Config{}); err == nil {
		t.Fatal("expected error when client cert/key are missing, got nil")
	}
	if _, err := NewMTLS(Config{ClientCertPEM: []byte("x")}); err == nil {
		t.Fatal("expected error when client key is missing, got nil")
	}
}

func TestNewMTLSRejectsInvalidKeyPair(t *testing.T) {
	if _, err := NewMTLS(Config{
		ClientCertPEM: []byte("not a cert"),
		ClientKeyPEM:  []byte("not a key"),
	}); err == nil {
		t.Fatal("expected error for invalid PEM key pair, got nil")
	}
}

func TestNewMTLSRejectsInvalidRootCAPEM(t *testing.T) {
	pki := newTestPKI(t)
	_, err := NewMTLS(Config{
		ClientCertPEM: pki.clientPEM,
		ClientKeyPEM:  pki.clientKey,
		RootCAsPEM:    []byte("garbage pem"),
	})
	if err == nil {
		t.Fatal("expected error for unparseable root CA PEM, got nil")
	}
}

// newMutualTLSServer starts a TLS server that requires and verifies client certs.
func newMutualTLSServer(t *testing.T, pki testPKI, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{pki.serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    certPool(t, pki.caPEM),
		MinVersion:   tls.VersionTLS13,
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func certPool(t *testing.T, pemBytes []byte) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatal("append CA to pool")
	}
	return pool
}

func TestMTLSRequestSucceedsWithValidClientCert(t *testing.T) {
	pki := newTestPKI(t)
	srv := newMutualTLSServer(t, pki, func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.PeerCertificates) == 0 {
			t.Error("server saw no client certificate")
		}
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "ok")
	})

	tr, err := NewMTLS(Config{
		ServerName:    pki.serverName,
		RootCAsPEM:    pki.caPEM,
		ClientCertPEM: pki.clientPEM,
		ClientKeyPEM:  pki.clientKey,
		Timeout:       5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewMTLS: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := tr.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("body = %q, want ok", body)
	}
}

func TestMTLSRequestFailsWithWrongCA(t *testing.T) {
	pki := newTestPKI(t)
	srv := newMutualTLSServer(t, pki, func(w http.ResponseWriter, r *http.Request) {
		t.Error("request should never reach handler with wrong CA")
	})

	// Client trusts a *different* CA, so server verification must fail.
	other := newTestPKI(t)
	tr, err := NewMTLS(Config{
		ServerName:    pki.serverName,
		RootCAsPEM:    other.caPEM,
		ClientCertPEM: pki.clientPEM,
		ClientKeyPEM:  pki.clientKey,
		Timeout:       5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewMTLS: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Do(req); err == nil {
		t.Fatal("expected TLS verification failure with wrong root CA, got nil")
	}
}

func TestMTLSRequestFailsWithUntrustedClientCert(t *testing.T) {
	pki := newTestPKI(t)
	srv := newMutualTLSServer(t, pki, func(w http.ResponseWriter, r *http.Request) {
		t.Error("request should never reach handler with untrusted client cert")
	})

	// Client cert signed by a different CA; server must reject it.
	other := newTestPKI(t)
	tr, err := NewMTLS(Config{
		ServerName:    pki.serverName,
		RootCAsPEM:    pki.caPEM,
		ClientCertPEM: other.clientPEM,
		ClientKeyPEM:  other.clientKey,
		Timeout:       5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewMTLS: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Do(req); err == nil {
		t.Fatal("expected mutual TLS failure with untrusted client cert, got nil")
	}
}

func TestDialContextOpensTLSConnection(t *testing.T) {
	pki := newTestPKI(t)
	srv := newMutualTLSServer(t, pki, func(w http.ResponseWriter, r *http.Request) {})

	tr, err := NewMTLS(Config{
		ServerName:    pki.serverName,
		RootCAsPEM:    pki.caPEM,
		ClientCertPEM: pki.clientPEM,
		ClientKeyPEM:  pki.clientKey,
		Timeout:       5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewMTLS: %v", err)
	}

	addr := srv.Listener.Addr().String()
	conn, err := tr.DialContext(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()

	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		t.Fatalf("DialContext returned %T, want *tls.Conn", conn)
	}
	if state := tlsConn.ConnectionState(); !state.HandshakeComplete {
		t.Error("TLS handshake not complete")
	}
}

func TestDialContextHonorsCancelledContext(t *testing.T) {
	pki := newTestPKI(t)
	tr, err := NewMTLS(Config{
		ServerName:    pki.serverName,
		RootCAsPEM:    pki.caPEM,
		ClientCertPEM: pki.clientPEM,
		ClientKeyPEM:  pki.clientKey,
		Timeout:       5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewMTLS: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = tr.DialContext(ctx, "tcp", "127.0.0.1:1")
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

func TestHTTPClientExposesConfiguredClient(t *testing.T) {
	pki := newTestPKI(t)
	tr, err := NewMTLS(Config{
		ServerName:    pki.serverName,
		RootCAsPEM:    pki.caPEM,
		ClientCertPEM: pki.clientPEM,
		ClientKeyPEM:  pki.clientKey,
		Timeout:       7 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewMTLS: %v", err)
	}

	hc := tr.HTTPClient()
	if hc == nil {
		t.Fatal("HTTPClient returned nil")
	}
	if hc.Timeout != 7*time.Second {
		t.Errorf("client timeout = %v, want 7s", hc.Timeout)
	}
}

func TestNewMTLSDefaultTimeout(t *testing.T) {
	pki := newTestPKI(t)
	tr, err := NewMTLS(Config{
		ServerName:    pki.serverName,
		ClientCertPEM: pki.clientPEM,
		ClientKeyPEM:  pki.clientKey,
	})
	if err != nil {
		t.Fatalf("NewMTLS: %v", err)
	}
	if got := tr.HTTPClient().Timeout; got != 30*time.Second {
		t.Errorf("default timeout = %v, want 30s", got)
	}
}

func TestMTLSImplementsTransportInterface(t *testing.T) {
	var _ Transport = (*MTLS)(nil)
}
