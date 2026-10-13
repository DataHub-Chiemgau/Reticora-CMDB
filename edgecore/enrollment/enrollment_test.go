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

// Ensure the test helper transport satisfies the shared interface.
var _ transport.Transport = httpTransport{}

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
