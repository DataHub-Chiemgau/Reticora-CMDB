package collectorcmd

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/keystore"
)

// TestNATSEndpointAndOptions covers WP-146 (COL-08, PRI-07): the collector
// connects outbound to the server's host on 4222 with TLS, with its
// enrolled certificate, the collector CA as trust anchor and its own inbox.
func TestNATSEndpointAndOptions(t *testing.T) {
	t.Setenv("RETICORA_NATS_URL", "")
	if got := natsEndpoint("https://cmdb.example.com/api"); got != "tls://cmdb.example.com:4222" {
		t.Fatalf("endpoint = %q", got)
	}
	t.Setenv("RETICORA_NATS_URL", "tls://nats.example.com:4222")
	if got := natsEndpoint("https://cmdb.example.com"); got != "tls://nats.example.com:4222" {
		t.Fatalf("configured endpoint = %q", got)
	}

	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ca"}, IsCA: true,
		BasicConstraintsValid: true, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageCertSign}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	caCert, _ := x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "col-1", Organization: []string{"org-1"}},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	creds := &keystore.Credentials{
		DeviceID:             "col-1",
		ClientCertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		ClientPrivateKeyPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})),
		CertificateAuthority: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})),
	}
	opts, err := natsOptions(creds)
	if err != nil || len(opts) == 0 {
		t.Fatalf("options: %v", err)
	}
	if subject, serr := collectorSubject(creds); serr != nil || subject != "collector.org-1.col-1" {
		t.Fatalf("subject = %q, %v", subject, serr)
	}
	creds.CertificateAuthority = ""
	if _, err = natsOptions(creds); err == nil {
		t.Fatal("options without the collector CA")
	}
}
