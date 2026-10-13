package enrollment

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/transport"
)

// httpTransport adapts a plain *http.Client to the transport.Transport
// interface so tests can hit httptest servers without mTLS material.
type httpTransport struct{ client *http.Client }

func (h httpTransport) Do(req *http.Request) (*http.Response, error) { return h.client.Do(req) }

func (h httpTransport) DialContext(ctx context.Context, network, addr string) (conn net.Conn, err error) {
	d := &net.Dialer{}
	return d.DialContext(ctx, network, addr)
}

func (h httpTransport) HTTPClient() *http.Client { return h.client }

func testClient(t *testing.T, handler http.HandlerFunc) (*HTTPClient, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &HTTPClient{
		BaseURL:   srv.URL,
		Transport: httpTransport{client: srv.Client()},
	}, srv
}

func TestEnrollSuccess(t *testing.T) {
	expires := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	var gotReq EnrollmentRequest
	var gotContentType string
	var gotMethod, gotPath string

	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(EnrollmentResponse{
			DeviceID:             "edge-01",
			ClientCertificatePEM: "cert-pem",
			ClientPrivateKeyPEM:  "key-pem",
			CertificateAuthority: "ca-pem",
			AssignedLabels:       map[string]string{"role": "collector"},
			ExpiresAt:            expires,
		})
	})

	resp, err := client.Enroll(context.Background(), EnrollmentRequest{
		DeviceID:        "edge-01",
		EnrollmentToken: "tok",
		Hostname:        "pi-01",
		Site:            "munich",
		Labels:          map[string]string{"env": "prod"},
		Capabilities:    []string{"metrics"},
		CSRPEM:          "csr",
	})
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/v1/enrollment" {
		t.Errorf("path = %q, want default endpoint /api/v1/enrollment", gotPath)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotReq.DeviceID != "edge-01" || gotReq.EnrollmentToken != "tok" || gotReq.Site != "munich" {
		t.Errorf("server received wrong request body: %+v", gotReq)
	}
	if gotReq.Labels["env"] != "prod" || len(gotReq.Capabilities) != 1 {
		t.Errorf("labels/capabilities not transmitted: %+v", gotReq)
	}

	if resp.DeviceID != "edge-01" || resp.ClientCertificatePEM != "cert-pem" || resp.ClientPrivateKeyPEM != "key-pem" {
		t.Errorf("unexpected response: %+v", resp)
	}
	if resp.CertificateAuthority != "ca-pem" {
		t.Errorf("CertificateAuthority = %q, want ca-pem", resp.CertificateAuthority)
	}
	if resp.AssignedLabels["role"] != "collector" {
		t.Errorf("AssignedLabels[role] = %q, want collector", resp.AssignedLabels["role"])
	}
	if !resp.ExpiresAt.Equal(expires) {
		t.Errorf("ExpiresAt = %v, want %v", resp.ExpiresAt, expires)
	}
}

func TestEnrollCustomEndpointAndBaseURLTrailingSlash(t *testing.T) {
	var gotPath string
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewEncoder(w).Encode(EnrollmentResponse{DeviceID: "d"})
	})
	client.Endpoint = "/custom/enroll"
	client.BaseURL += "/" // trailing slash must not produce "//custom/enroll"

	if _, err := client.Enroll(context.Background(), EnrollmentRequest{DeviceID: "d"}); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if gotPath != "/custom/enroll" {
		t.Errorf("path = %q, want /custom/enroll", gotPath)
	}
}

func TestEnrollServerErrorIncludesStatusAndBody(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "token expired", http.StatusUnauthorized)
	})

	_, err := client.Enroll(context.Background(), EnrollmentRequest{DeviceID: "d"})
	if err == nil {
		t.Fatal("expected error on 401, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should include status, got: %v", err)
	}
	if !strings.Contains(err.Error(), "token expired") {
		t.Errorf("error should include server body, got: %v", err)
	}
}

func TestEnrollInvalidJSONResponse(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "{not json")
	})

	_, err := client.Enroll(context.Background(), EnrollmentRequest{DeviceID: "d"})
	if err == nil {
		t.Fatal("expected decode error, got nil")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Errorf("error should mention decode failure, got: %v", err)
	}
}

func TestEnrollNilTransport(t *testing.T) {
	client := &HTTPClient{BaseURL: "https://example.invalid"}

	_, err := client.Enroll(context.Background(), EnrollmentRequest{DeviceID: "d"})
	if err == nil {
		t.Fatal("expected error for nil transport, got nil")
	}
	if !strings.Contains(err.Error(), "transport is required") {
		t.Errorf("error should mention missing transport, got: %v", err)
	}
}

type failingTransport struct{ err error }

func (f failingTransport) Do(req *http.Request) (*http.Response, error) { return nil, f.err }
func (f failingTransport) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return nil, f.err
}
func (f failingTransport) HTTPClient() *http.Client { return nil }

func TestEnrollTransportFailure(t *testing.T) {
	client := &HTTPClient{
		BaseURL:   "https://control-plane.invalid",
		Transport: failingTransport{err: errors.New("connection refused")},
	}

	_, err := client.Enroll(context.Background(), EnrollmentRequest{DeviceID: "d"})
	if err == nil {
		t.Fatal("expected error for transport failure, got nil")
	}
	if !strings.Contains(err.Error(), "send request") {
		t.Errorf("error should wrap send failure, got: %v", err)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error should preserve underlying cause, got: %v", err)
	}
}

func TestEnrollRespectsContextCancellation(t *testing.T) {
	// Server blocks until the client goes away.
	unblock := make(chan struct{})
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		<-unblock
	})
	t.Cleanup(func() { close(unblock) })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := client.Enroll(ctx, EnrollmentRequest{DeviceID: "d"})
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error should wrap context.DeadlineExceeded, got: %v", err)
	}
}

func TestEnrollRetryOnTransientFailure(t *testing.T) {
	// The client itself does not retry; verify it surfaces a transient 503
	// accurately so a caller's retry loop can react, and that a subsequent
	// attempt against the recovered server succeeds.
	var attempts int
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, "try again later")
			return
		}
		json.NewEncoder(w).Encode(EnrollmentResponse{DeviceID: "d"})
	})

	ctx := context.Background()
	_, err := client.Enroll(ctx, EnrollmentRequest{DeviceID: "d"})
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("first attempt should surface 503, got: %v", err)
	}

	resp, err := client.Enroll(ctx, EnrollmentRequest{DeviceID: "d"})
	if err != nil {
		t.Fatalf("retry after transient failure: %v", err)
	}
	if resp.DeviceID != "d" {
		t.Errorf("DeviceID = %q, want d", resp.DeviceID)
	}
	if attempts != 2 {
		t.Errorf("server saw %d attempts, want 2", attempts)
	}
}

func TestHTTPClientImplementsClientInterface(t *testing.T) {
	var _ Client = (*HTTPClient)(nil)
}

// Ensure the test helper transport satisfies the shared interface.
var _ transport.Transport = httpTransport{}
var _ transport.Transport = failingTransport{}

// testCA signs CSRs like the server's collector CA (COL-04).
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  string
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return &testCA{cert: cert, key: key, pem: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}
}

func (ca *testCA) sign(t *testing.T, csrPEM, cn string) string {
	t.Helper()
	block, _ := pem.Decode([]byte(csrPEM))
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || csr.CheckSignature() != nil {
		t.Fatalf("invalid CSR: %v", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(now.UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: now, NotAfter: now.Add(90 * 24 * time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, csr.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func certKeyMatches(t *testing.T, certPEM, keyPEM string) bool {
	t.Helper()
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	return err == nil && len(pair.Certificate) == 1
}

// TestCollectorEnrollAndRenew covers WP-145 (COL-04) on the collector side:
// the key is generated locally and only a CSR is sent; the stored
// credentials pair the issued certificate with the local key; renewal waits
// for 60 % of the lifetime and then sends a CSR with CN = collector id over
// the authenticated channel.
func TestCollectorEnrollAndRenew(t *testing.T) {
	ca := newTestCA(t)
	var renewals int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), "PRIVATE KEY") {
			t.Errorf("the request carries a private key: %s", raw)
		}
		var body map[string]string
		_ = json.Unmarshal(raw, &body)
		switch r.URL.Path {
		case "/api/v1/collectors/enroll":
			if body["code"] != "code-1" || body["name"] != "col-1" {
				t.Errorf("enroll body %v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "col-id-1", "status": "active",
				"certificate": map[string]any{"certificate_pem": ca.sign(t, body["csr"], "col-id-1"), "ca_bundle_pem": ca.pem}})
		case "/api/v1/collectors/col-id-1/certificate":
			renewals++
			if r.Header.Get("Authorization") != "Bearer api-key" {
				t.Errorf("renewal without the collector credential")
			}
			block, _ := pem.Decode([]byte(body["csr"]))
			csr, _ := x509.ParseCertificateRequest(block.Bytes)
			if csr == nil || csr.Subject.CommonName != "col-id-1" {
				t.Errorf("renewal CSR does not name the collector")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"certificate_pem": ca.sign(t, body["csr"], "col-id-1"), "ca_bundle_pem": ca.pem})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := &CollectorClient{BaseURL: srv.URL, Transport: httpTransport{client: srv.Client()}, Token: "api-key"}
	creds, err := client.Enroll(context.Background(), "code-1", "col-1", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if creds.DeviceID != "col-id-1" || creds.CertificateAuthority != ca.pem ||
		!certKeyMatches(t, creds.ClientCertificatePEM, creds.ClientPrivateKeyPEM) {
		t.Fatalf("credentials do not pair the certificate with the local key: %+v", creds.DeviceID)
	}

	// Not due before 60 % of the lifetime.
	renewed, err := client.Renew(context.Background(), creds)
	if err != nil || renewed || renewals != 0 {
		t.Fatalf("early renewal = %v, %v (%d requests)", renewed, err, renewals)
	}
	if due, _ := NeedsRenewal(creds.ClientCertificatePEM, time.Now().Add(53*24*time.Hour)); due {
		t.Fatal("due after 53 of 90 days")
	}
	client.now = func() time.Time { return time.Now().Add(55 * 24 * time.Hour) }
	oldKey := creds.ClientPrivateKeyPEM
	if renewed, err = client.Renew(context.Background(), creds); err != nil || !renewed || renewals != 1 {
		t.Fatalf("renewal = %v, %v", renewed, err)
	}
	if creds.ClientPrivateKeyPEM == oldKey || !certKeyMatches(t, creds.ClientCertificatePEM, creds.ClientPrivateKeyPEM) {
		t.Fatal("renewal did not pair a fresh key with the new certificate")
	}
}
