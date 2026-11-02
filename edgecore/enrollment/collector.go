package enrollment

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/keystore"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/transport"
)

// RenewAfter is the share of the certificate lifetime after which a
// collector renews its certificate (COL-04).
const RenewAfter = 0.6

// GenerateKey creates the collector's private key (ECDSA P-256) locally; it
// never leaves the collector.
func GenerateKey() (key *ecdsa.PrivateKey, keyPEM string, err error) {
	key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, "", fmt.Errorf("enrollment: generate key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, "", fmt.Errorf("enrollment: encode key: %w", err)
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), nil
}

// CreateCSR builds the certificate signing request of the key; commonName
// is the collector id, or empty before the collector has one.
func CreateCSR(key *ecdsa.PrivateKey, commonName string) (string, error) {
	return createCSR(key, &pkix.Name{CommonName: commonName})
}

func createCSR(key *ecdsa.PrivateKey, subject *pkix.Name) (string, error) {
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: *subject}, key)
	if err != nil {
		return "", fmt.Errorf("enrollment: create CSR: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})), nil
}

// NeedsRenewal reports whether the certificate has reached 60 % of its
// lifetime at now.
func NeedsRenewal(certPEM string, now time.Time) (bool, error) {
	cert, err := parseCertificate(certPEM)
	if err != nil {
		return false, err
	}
	life := cert.NotAfter.Sub(cert.NotBefore)
	return life <= 0 || now.Sub(cert.NotBefore) >= time.Duration(float64(life)*RenewAfter), nil
}

func parseCertificate(certPEM string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return nil, errors.New("enrollment: no certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("enrollment: parse certificate: %w", err)
	}
	return cert, nil
}

// RequestFunc sends a request on a subject of the collector's mTLS NATS
// channel and returns the reply.
type RequestFunc func(ctx context.Context, subject string, data []byte) ([]byte, error)

// renewSuffix is appended to the collector's subject tree
// (collector.<org>.<id>) for renewal requests; renewalSignaturePrefix
// starts the message the collector signs with its current key. Both are
// the contract with the server (backend/internal/discovery/renew_edge.go,
// docs/betrieb/collector-ca.md).
const (
	renewSuffix            = ".certificate.renew"
	renewalSignaturePrefix = "reticora-collector-certificate-renewal"
)

// renewalRequest is what the collector sends on its renewal subject.
type renewalRequest struct {
	CSR            string `json:"csr"`
	CertificatePEM string `json:"certificate_pem"`
	Timestamp      string `json:"timestamp"`
	Signature      []byte `json:"signature"`
}

// issuedCert is the certificate part of the server's answers.
type issuedCert struct {
	CertificatePEM string    `json:"certificate_pem"`
	CABundlePEM    string    `json:"ca_bundle_pem"`
	NotAfter       time.Time `json:"not_after"`
}

// CollectorClient enrolls a collector with the enrollment code
// (RETICORA_ENROLL_TOKEN) at the cloud URL (RETICORA_CLOUD_URL) and renews
// its certificate (COL-04). The private key is generated here and stored
// only in the local keystore; the server receives the CSR.
type CollectorClient struct {
	BaseURL   string
	Transport transport.Transport
	// Request carries renewals over the collector's NATS channel; there is
	// no renewal over the API (F-012).
	Request RequestFunc
	// Now is the clock of the renewal check and of the request timestamp;
	// nil is the system clock.
	Now func() time.Time
}

func (c *CollectorClient) clock() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now().UTC()
}

// Enroll redeems the code and returns the collector's credentials: id,
// certificate, CA bundle and the local private key.
func (c *CollectorClient) Enroll(ctx context.Context, code, name, version string) (*keystore.Credentials, error) {
	key, keyPEM, err := GenerateKey()
	if err != nil {
		return nil, err
	}
	csr, err := CreateCSR(key, "")
	if err != nil {
		return nil, err
	}
	var out struct {
		ID          string      `json:"id"`
		Certificate *issuedCert `json:"certificate"`
	}
	if perr := c.post(ctx, EnrollPath, map[string]string{
		"code": code, "name": name, "version": version, "csr": csr,
	}, &out); perr != nil {
		return nil, perr
	}
	if out.ID == "" || out.Certificate == nil || out.Certificate.CertificatePEM == "" {
		return nil, errors.New("enrollment: the server issued no certificate")
	}
	return &keystore.Credentials{
		DeviceID:             out.ID,
		ClientCertificatePEM: out.Certificate.CertificatePEM,
		ClientPrivateKeyPEM:  keyPEM,
		CertificateAuthority: out.Certificate.CABundlePEM,
		UpdatedAt:            c.clock(),
	}, nil
}

// Renew replaces the certificate, key and CA bundle of creds when 60 % of
// the certificate's lifetime passed; it reports whether it renewed. The
// request goes over c.Request on collector.<org>.<id>.certificate.renew:
// the CSR of a fresh key (CN = collector id, O = organization), the current
// certificate, a timestamp and a signature with the current key over the
// CSR, the timestamp and the subject. creds stays unchanged unless the
// server issued a certificate of the CA bundle for the fresh key.
func (c *CollectorClient) Renew(ctx context.Context, creds *keystore.Credentials) (bool, error) {
	now := c.clock()
	due, err := NeedsRenewal(creds.ClientCertificatePEM, now)
	if err != nil || !due {
		return false, err
	}
	if c.Request == nil {
		return false, errors.New("enrollment: no channel for the renewal")
	}
	current, err := parseCertificate(creds.ClientCertificatePEM)
	if err != nil {
		return false, err
	}
	if len(current.Subject.Organization) != 1 {
		return false, errors.New("enrollment: the certificate names no organization")
	}
	orgID := current.Subject.Organization[0]
	signer, err := parseSigner(creds.ClientPrivateKeyPEM)
	if err != nil {
		return false, err
	}
	key, keyPEM, err := GenerateKey()
	if err != nil {
		return false, err
	}
	csr, err := createCSR(key, &pkix.Name{CommonName: creds.DeviceID, Organization: []string{orgID}})
	if err != nil {
		return false, err
	}
	subject := "collector." + orgID + "." + creds.DeviceID + renewSuffix
	timestamp := now.UTC().Format(time.RFC3339)
	sig, err := signRenewal(signer, []byte(renewalSignaturePrefix+"\n"+subject+"\n"+timestamp+"\n"+csr))
	if err != nil {
		return false, fmt.Errorf("enrollment: sign the renewal: %w", err)
	}
	raw, err := json.Marshal(renewalRequest{CSR: csr, CertificatePEM: creds.ClientCertificatePEM, Timestamp: timestamp, Signature: sig})
	if err != nil {
		return false, fmt.Errorf("enrollment: marshal request: %w", err)
	}
	reply, err := c.Request(ctx, subject, raw)
	if err != nil {
		return false, fmt.Errorf("enrollment: renewal request: %w", err)
	}
	var out struct {
		issuedCert
		Error string `json:"error"`
	}
	if err = json.Unmarshal(reply, &out); err != nil {
		return false, fmt.Errorf("enrollment: decode renewal reply: %w", err)
	}
	if out.Error != "" {
		return false, fmt.Errorf("enrollment: renewal refused: %s", out.Error)
	}
	caBundle := creds.CertificateAuthority
	if out.CABundlePEM != "" {
		caBundle = out.CABundlePEM
	}
	if ierr := checkIssued(out.CertificatePEM, keyPEM, caBundle, creds.DeviceID, orgID, now); ierr != nil {
		return false, ierr
	}
	creds.ClientCertificatePEM, creds.ClientPrivateKeyPEM, creds.CertificateAuthority = out.CertificatePEM, keyPEM, caBundle
	creds.UpdatedAt = now
	return true, nil
}

// checkIssued refuses a renewed certificate the collector could not use:
// not for the fresh key, not for the collector, or not of the CA bundle.
func checkIssued(certPEM, keyPEM, caBundle, collectorID, orgID string, now time.Time) error {
	if _, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM)); err != nil {
		return fmt.Errorf("enrollment: the renewed certificate is not for the new key: %w", err)
	}
	cert, err := parseCertificate(certPEM)
	if err != nil {
		return err
	}
	if cert.Subject.CommonName != collectorID || len(cert.Subject.Organization) != 1 || cert.Subject.Organization[0] != orgID {
		return errors.New("enrollment: the renewed certificate names another collector")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(caBundle)) {
		return errors.New("enrollment: no CA bundle for the renewed certificate")
	}
	if _, err = cert.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return fmt.Errorf("enrollment: the renewed certificate: %w", err)
	}
	return nil
}

func parseSigner(keyPEM string) (crypto.Signer, error) {
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		return nil, errors.New("enrollment: no private key")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if signer, ok := key.(crypto.Signer); ok {
			return signer, nil
		}
	}
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, errors.New("enrollment: unsupported private key")
}

// signRenewal signs msg as the server verifies it: ECDSA keys its SHA-256
// (ASN.1 DER), RSA keys its SHA-256 (PKCS #1 v1.5), Ed25519 keys the
// message.
func signRenewal(key crypto.Signer, msg []byte) ([]byte, error) {
	if _, ok := key.(ed25519.PrivateKey); ok {
		return key.Sign(rand.Reader, msg, crypto.Hash(0))
	}
	digest := sha256.Sum256(msg)
	return key.Sign(rand.Reader, digest[:], crypto.SHA256)
}

func (c *CollectorClient) post(ctx context.Context, path string, body, out any) error {
	if c.Transport == nil {
		return errors.New("enrollment: transport is required")
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("enrollment: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("enrollment: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Transport.Do(req)
	if err != nil {
		return fmt.Errorf("enrollment: send request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("enrollment: unexpected status %s: %s", resp.Status, strings.TrimSpace(string(payload)))
	}
	if err = json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("enrollment: decode response: %w", err)
	}
	return nil
}
