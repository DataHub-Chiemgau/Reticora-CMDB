// Shared helpers of the collector's tests against a real nats-server: a
// private CA for the mTLS channel, a free port and the nats-server binary.
// natsconn_test.go uses them without a build tag (WP-381),
// lifecycle_auditdefect_test.go with the tag auditdefect.
package collectorcmd

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"strconv"
	"testing"
	"time"
)

// auditPKI is a private CA for one test.
type auditPKI struct {
	key  *ecdsa.PrivateKey
	cert *x509.Certificate
	pem  string
}

func newAuditPKI(t *testing.T) *auditPKI {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "audit collector CA"}, IsCA: true,
		BasicConstraintsValid: true, NotBefore: time.Now().Add(-100 * 24 * time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return &auditPKI{key: key, cert: cert, pem: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}
}

// issue signs pub; it returns the certificate PEM.
func (p *auditPKI) issue(t *testing.T, pub any, subject pkix.Name, notBefore, notAfter time.Time, server bool) string {
	t.Helper()
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: subject, NotBefore: notBefore, NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	if server {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		tmpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.cert, pub, p.key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func auditKeyPEM(t *testing.T, key *ecdsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func auditFreeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// natsServerBin returns the nats-server for the tests of the collector's
// mTLS channel (TEST_NATS_SERVER_BIN, 2.10+ for handshake_first). Without it
// the test is skipped; in the CI (CI=true) it fails, so the tests cannot
// pass there without a real server.
func natsServerBin(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("TEST_NATS_SERVER_BIN")
	if bin != "" {
		return bin
	}
	if ci, _ := strconv.ParseBool(os.Getenv("CI")); ci {
		t.Fatal("TEST_NATS_SERVER_BIN not set in the CI: the collector's mTLS NATS channel is tested against a real nats-server")
	}
	t.Skip("TEST_NATS_SERVER_BIN not set; skipping the test against a real nats-server")
	return ""
}
