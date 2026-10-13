package enrollment

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
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
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: commonName}}, key)
	if err != nil {
		return "", fmt.Errorf("enrollment: create CSR: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})), nil
}

// NeedsRenewal reports whether the certificate has reached 60 % of its
// lifetime at now.
func NeedsRenewal(certPEM string, now time.Time) (bool, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return false, errors.New("enrollment: no certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false, fmt.Errorf("enrollment: parse certificate: %w", err)
	}
	life := cert.NotAfter.Sub(cert.NotBefore)
	return life <= 0 || now.Sub(cert.NotBefore) >= time.Duration(float64(life)*RenewAfter), nil
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
	// Token authenticates renewals (the collector's API credential).
	Token string
	now   func() time.Time
}

func (c *CollectorClient) clock() time.Time {
	if c.now != nil {
		return c.now()
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
	if perr := c.post(ctx, "/api/v1/collectors/enroll", "", map[string]string{
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

// Renew replaces the certificate (and key) of creds when 60 % of its
// lifetime passed; it reports whether it renewed.
func (c *CollectorClient) Renew(ctx context.Context, creds *keystore.Credentials) (bool, error) {
	due, err := NeedsRenewal(creds.ClientCertificatePEM, c.clock())
	if err != nil || !due {
		return false, err
	}
	key, keyPEM, err := GenerateKey()
	if err != nil {
		return false, err
	}
	csr, err := CreateCSR(key, creds.DeviceID)
	if err != nil {
		return false, err
	}
	var out issuedCert
	if perr := c.post(ctx, "/api/v1/collectors/"+creds.DeviceID+"/certificate", c.Token, map[string]string{"csr": csr}, &out); perr != nil {
		return false, perr
	}
	creds.ClientCertificatePEM, creds.ClientPrivateKeyPEM = out.CertificatePEM, keyPEM
	if out.CABundlePEM != "" {
		creds.CertificateAuthority = out.CABundlePEM
	}
	creds.UpdatedAt = c.clock()
	return true, nil
}

func (c *CollectorClient) post(ctx context.Context, path, token string, body, out any) error {
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
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
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
