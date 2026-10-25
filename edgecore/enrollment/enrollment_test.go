package enrollment

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
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
	"reflect"
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
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return &testCA{cert: cert, key: key, pem: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}
}

// sign issues a 90-day client certificate for the key of the CSR to the
// collector cn of organization org, like the server's collector CA.
func (ca *testCA) sign(t *testing.T, csrPEM, cn, org string) string {
	t.Helper()
	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil {
		t.Fatal("no CSR")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || csr.CheckSignature() != nil {
		t.Fatalf("invalid CSR: %v", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(now.UnixNano()), Subject: pkix.Name{CommonName: cn, Organization: []string{org}},
		NotBefore: now, NotAfter: now.Add(90 * 24 * time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, csr.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// verifyRenewalSignature checks a renewal signature as the server does
// (backend/internal/discovery/renew_edge.go): ECDSA and RSA over the
// SHA-256 of the message, Ed25519 over the message.
func verifyRenewalSignature(pub any, msg, sig []byte) bool {
	digest := sha256.Sum256(msg)
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		return ecdsa.VerifyASN1(k, digest[:], sig)
	case *rsa.PublicKey:
		return rsa.VerifyPKCS1v15(k, crypto.SHA256, digest[:], sig) == nil
	case ed25519.PublicKey:
		return ed25519.Verify(k, msg, sig)
	}
	return false
}

func certKeyMatches(t *testing.T, certPEM, keyPEM string) bool {
	t.Helper()
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	return err == nil && len(pair.Certificate) == 1
}

// TestCollectorEnrollAndRenew covers WP-145 (COL-04) on the collector side:
// the key is generated locally and only a CSR is sent; the stored
// credentials pair the issued certificate with the local key; renewal waits
// for 60 % of the lifetime and then goes over the request function of the
// collector's NATS channel (WP-414, F-026), not over HTTP: on
// collector.<org>.<id>.certificate.renew the CSR of a fresh key with
// CN = collector id and O = organization, the current certificate, the
// timestamp and a signature with the current key over (CSR, timestamp,
// subject) as the server verifies it (WP-375). A refused renewal or a
// certificate the collector could not use leaves the credentials as they
// were.
func TestCollectorEnrollAndRenew(t *testing.T) {
	const orgID, colID = "org-1", "col-id-1"
	ca := newTestCA(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), "PRIVATE KEY") {
			t.Errorf("the request carries a private key: %s", raw)
		}
		var body map[string]string
		_ = json.Unmarshal(raw, &body)
		if r.URL.Path != EnrollPath {
			t.Errorf("unexpected HTTP request %s %s (renewals go over the NATS channel)", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if body["code"] != "code-1" || body["name"] != "col-1" {
			t.Errorf("enroll body %v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": colID, "status": "active",
			"certificate": map[string]any{"certificate_pem": ca.sign(t, body["csr"], colID, orgID), "ca_bundle_pem": ca.pem}})
	}))
	defer srv.Close()

	now := time.Now().UTC().Truncate(time.Second)
	var renewals []renewalRequest
	// answer is the server's reply to the next renewal request.
	answer := func(req renewalRequest) any {
		return map[string]any{"certificate_pem": ca.sign(t, req.CSR, colID, orgID), "ca_bundle_pem": ca.pem}
	}
	client := &CollectorClient{BaseURL: srv.URL, Transport: httpTransport{client: srv.Client()}}
	client.Request = func(_ context.Context, subject string, data []byte) ([]byte, error) {
		if strings.Contains(string(data), "PRIVATE KEY") {
			t.Errorf("the renewal request carries a private key: %s", data)
		}
		var req renewalRequest
		if err := json.Unmarshal(data, &req); err != nil {
			t.Fatalf("renewal request: %v", err)
		}
		renewals = append(renewals, req)
		if subject != "collector."+orgID+"."+colID+".certificate.renew" {
			t.Errorf("renewal subject %q", subject)
		}
		block, _ := pem.Decode([]byte(req.CSR))
		if block == nil {
			t.Fatal("renewal without a CSR")
		}
		csr, err := x509.ParseCertificateRequest(block.Bytes)
		if err != nil || csr.CheckSignature() != nil || csr.Subject.CommonName != colID ||
			len(csr.Subject.Organization) != 1 || csr.Subject.Organization[0] != orgID {
			t.Errorf("renewal CSR does not name the collector and its organization: %v", err)
		}
		if req.Timestamp != now.Format(time.RFC3339) {
			t.Errorf("renewal timestamp %q, want %q", req.Timestamp, now.Format(time.RFC3339))
		}
		current, err := parseCertificate(req.CertificatePEM)
		if err != nil {
			t.Fatal(err)
		}
		msg := []byte("reticora-collector-certificate-renewal\n" + subject + "\n" + req.Timestamp + "\n" + req.CSR)
		if !verifyRenewalSignature(current.PublicKey, msg, req.Signature) {
			t.Error("the renewal is not signed with the key of the presented certificate")
		}
		return json.Marshal(answer(req))
	}
	creds, err := client.Enroll(context.Background(), "code-1", "col-1", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if creds.DeviceID != colID || creds.CertificateAuthority != ca.pem ||
		!certKeyMatches(t, creds.ClientCertificatePEM, creds.ClientPrivateKeyPEM) {
		t.Fatalf("credentials do not pair the certificate with the local key: %+v", creds.DeviceID)
	}

	// Not due before 60 % of the lifetime.
	renewed, err := client.Renew(context.Background(), creds)
	if err != nil || renewed || len(renewals) != 0 {
		t.Fatalf("early renewal = %v, %v (%d requests)", renewed, err, len(renewals))
	}
	client.Now = func() time.Time { return now.Add(53 * 24 * time.Hour) }
	if renewed, err = client.Renew(context.Background(), creds); err != nil || renewed || len(renewals) != 0 {
		t.Fatalf("renewal after 53 of 90 days = %v, %v (%d requests)", renewed, err, len(renewals))
	}

	// Due after 55 of 90 days.
	now = now.Add(55 * 24 * time.Hour)
	client.Now = func() time.Time { return now }
	before := *creds
	if renewed, err = client.Renew(context.Background(), creds); err != nil || !renewed || len(renewals) != 1 {
		t.Fatalf("renewal = %v, %v (%d requests)", renewed, err, len(renewals))
	}
	if renewals[0].CertificatePEM != before.ClientCertificatePEM {
		t.Error("the renewal request does not carry the current certificate")
	}
	if creds.ClientPrivateKeyPEM == before.ClientPrivateKeyPEM || creds.ClientCertificatePEM == before.ClientCertificatePEM ||
		!certKeyMatches(t, creds.ClientCertificatePEM, creds.ClientPrivateKeyPEM) || creds.CertificateAuthority != ca.pem {
		t.Fatal("renewal did not pair a fresh key with the new certificate")
	}
	if !creds.UpdatedAt.Equal(now) {
		t.Errorf("UpdatedAt = %v, want %v", creds.UpdatedAt, now)
	}

	// Refused, unanswered or unusable renewals leave the credentials as
	// they were.
	refusals := map[string]func(req renewalRequest) any{
		"refused": func(renewalRequest) any { return map[string]string{"error": "renewal_not_due"} },
		"certificate for another key": func(renewalRequest) any {
			other, _, _ := GenerateKey()
			otherCSR, _ := CreateCSR(other, colID)
			return map[string]any{"certificate_pem": ca.sign(t, otherCSR, colID, orgID), "ca_bundle_pem": ca.pem}
		},
		"certificate of another collector": func(req renewalRequest) any {
			return map[string]any{"certificate_pem": ca.sign(t, req.CSR, "col-id-2", orgID), "ca_bundle_pem": ca.pem}
		},
		"certificate of another CA": func(req renewalRequest) any {
			return map[string]any{"certificate_pem": newTestCA(t).sign(t, req.CSR, colID, orgID)}
		},
	}
	for name, reply := range refusals {
		answer = reply
		renewed := *creds
		ok, rerr := client.Renew(context.Background(), &renewed)
		if changed := !reflect.DeepEqual(renewed, *creds); ok || rerr == nil || changed {
			t.Errorf("%s: renewal = %v, %v; credentials changed %v", name, ok, rerr, changed)
		}
		if name == "refused" && (rerr == nil || !strings.Contains(rerr.Error(), "renewal_not_due")) {
			t.Errorf("the refusal does not name its cause: %v", rerr)
		}
	}
	client.Request = func(context.Context, string, []byte) ([]byte, error) { return nil, errors.New("nats: timeout") }
	unchanged := *creds
	if ok, rerr := client.Renew(context.Background(), &unchanged); ok || rerr == nil || !reflect.DeepEqual(unchanged, *creds) {
		t.Errorf("unanswered renewal = %v, %v", ok, rerr)
	}
}

// TestRenewalSignature: the signature of a renewal verifies as the server
// checks it, for every key type the server accepts (WP-375).
func TestRenewalSignature(t *testing.T) {
	ecKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	_, edKey, _ := ed25519.GenerateKey(rand.Reader)
	msg := []byte("reticora-collector-certificate-renewal\ncollector.o.c.certificate.renew\n2026-10-25T00:00:00Z\ncsr")
	for _, key := range []crypto.Signer{ecKey, rsaKey, edKey} {
		der, merr := x509.MarshalPKCS8PrivateKey(key)
		if merr != nil {
			t.Fatal(merr)
		}
		signer, perr := parseSigner(string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})))
		if perr != nil {
			t.Fatalf("%T: %v", key, perr)
		}
		sig, serr := signRenewal(signer, msg)
		if serr != nil || !verifyRenewalSignature(key.Public(), msg, sig) {
			t.Errorf("%T: the signature does not verify (%v)", key, serr)
		}
	}
	ecDER, _ := x509.MarshalECPrivateKey(ecKey)
	if _, err = parseSigner(string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: ecDER}))); err != nil {
		t.Errorf("SEC 1 key: %v", err)
	}
}
