package discovery

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	platformcrypto "github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

// Collector PKI (COL-04).
const (
	// CertValidity is the lifetime of a collector certificate.
	CertValidity = 90 * 24 * time.Hour
	// RenewAfter is the share of the lifetime after which a collector
	// renews its certificate.
	RenewAfter = 0.6
	// CAValidity is the lifetime of the internal CA.
	CAValidity = 10 * 365 * 24 * time.Hour
)

// PKI errors.
var (
	// ErrInvalidCSR: the request is no valid certificate signing request,
	// names another collector or uses a weak key.
	ErrInvalidCSR = errors.New("invalid certificate signing request")
	// ErrRenewalNotDue: the current certificate has not reached 60 % of its
	// lifetime or was renewed just now.
	ErrRenewalNotDue = errors.New("certificate renewal is not due yet")
)

// IssuedCert is a signed collector certificate.
type IssuedCert struct {
	CertificatePEM string    `json:"certificate_pem"`
	CABundlePEM    string    `json:"ca_bundle_pem"`
	Serial         string    `json:"serial"`
	Fingerprint    string    `json:"fingerprint"`
	NotBefore      time.Time `json:"not_before"`
	NotAfter       time.Time `json:"not_after"`
}

// NeedsRenewal reports whether a certificate valid from notBefore to
// notAfter is due for renewal at now (60 % of its lifetime, COL-04).
func NeedsRenewal(notBefore, notAfter, now time.Time) bool {
	life := notAfter.Sub(notBefore)
	return life <= 0 || now.Sub(notBefore) >= time.Duration(float64(life)*RenewAfter)
}

// CA is the internal certificate authority of the collectors. Its key is
// stored in collector_ca, encrypted under the master key; the active CA
// signs, the previous one stays a trust anchor during a roll-over.
type CA struct {
	pool *pgxpool.Pool
	enc  *platformcrypto.EnvelopeEncryptor
	now  func() time.Time

	mu       sync.Mutex
	cert     *x509.Certificate
	certPEM  string
	key      crypto.Signer
	previous []string
}

// NewCA creates the CA on pool; the key is loaded or created on first use.
func NewCA(pool *pgxpool.Pool, enc *platformcrypto.EnvelopeEncryptor) *CA {
	return &CA{pool: pool, enc: enc, now: func() time.Time { return time.Now().UTC() }}
}

// WithClock replaces the clock (tests).
func (ca *CA) WithClock(now func() time.Time) *CA {
	ca.now = now
	return ca
}

// load reads the active and previous CAs, creating the first one.
func (ca *CA) load(ctx context.Context) error {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	if ca.key != nil {
		return nil
	}
	return ca.reload(ctx)
}

func (ca *CA) reload(ctx context.Context) error {
	for attempt := 0; attempt < 2; attempt++ {
		var certPEM string
		var keyCT, keyDEK []byte
		var previous []string
		err := database.WithOperator(ctx, ca.pool, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT cert_pem, key_ciphertext, key_dek, state FROM collector_ca
				WHERE state IN ('active', 'previous') AND not_after > now() ORDER BY created_at DESC`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var p, state string
				var ct, dek []byte
				if serr := rows.Scan(&p, &ct, &dek, &state); serr != nil {
					return serr
				}
				if state == "active" {
					certPEM, keyCT, keyDEK = p, ct, dek
				} else {
					previous = append(previous, p)
				}
			}
			return rows.Err()
		})
		if err != nil {
			return fmt.Errorf("load collector CA: %w", err)
		}
		if certPEM == "" {
			if cerr := ca.create(ctx, false); cerr != nil {
				return cerr
			}
			continue
		}
		if uerr := ca.use(certPEM, keyCT, keyDEK); uerr != nil {
			return uerr
		}
		ca.previous = previous
		return nil
	}
	return errors.New("collector CA could not be created")
}

func (ca *CA) use(certPEM string, keyCT, keyDEK []byte) error {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return errors.New("collector CA: invalid certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("collector CA: %w", err)
	}
	dek, err := ca.enc.UnwrapDEK(keyDEK)
	if err != nil {
		return fmt.Errorf("collector CA: unwrap key: %w", err)
	}
	der, err := platformcrypto.Decrypt(dek, keyCT)
	if err != nil {
		return fmt.Errorf("collector CA: decrypt key: %w", err)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return fmt.Errorf("collector CA: parse key: %w", err)
	}
	signer, ok := parsed.(crypto.Signer)
	if !ok {
		return errors.New("collector CA: key cannot sign")
	}
	ca.cert, ca.certPEM, ca.key = cert, certPEM, signer
	return nil
}

// create generates a CA; with rollover the active one becomes previous.
// A concurrent creation by another replica wins (unique active index).
func (ca *CA) create(ctx context.Context, rollover bool) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	now := ca.now()
	serial, err := randomSerial()
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Reticora Collector CA " + now.Format("2006-01-02") + " " + fmt.Sprintf("%08x", serial.Uint64()&0xffffffff), Organization: []string{"Reticora"}},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(CAValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("create collector CA: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	dek, err := platformcrypto.GenerateDEK()
	if err != nil {
		return err
	}
	ct, err := platformcrypto.Encrypt(dek, keyDER)
	if err != nil {
		return err
	}
	wrapped, err := ca.enc.WrapDEK(dek)
	if err != nil {
		return err
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return database.WithOperator(ctx, ca.pool, func(ctx context.Context, tx pgx.Tx) error {
		if rollover {
			if _, err = tx.Exec(ctx, `UPDATE collector_ca SET state = 'previous' WHERE state = 'active'`); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO collector_ca (cert_pem, key_ciphertext, key_dek, not_after)
			VALUES ($1, $2, $3, $4) ON CONFLICT (state) WHERE state = 'active' DO NOTHING`, certPEM, ct, wrapped, tmpl.NotAfter)
		return err
	})
}

// Rollover replaces the active CA; the former one stays a trust anchor
// until Retire (COL-04: two trust anchors during the transition).
func (ca *CA) Rollover(ctx context.Context) error {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	if err := ca.create(ctx, true); err != nil {
		return err
	}
	ca.key = nil
	return ca.reload(ctx)
}

// RetirePrevious ends the transition: the former CAs are no longer
// trusted.
func (ca *CA) RetirePrevious(ctx context.Context) error {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	if err := database.WithOperator(ctx, ca.pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE collector_ca SET state = 'retired' WHERE state = 'previous'`)
		return err
	}); err != nil {
		return err
	}
	ca.key = nil
	return ca.reload(ctx)
}

// TrustBundle is the PEM of the trusted CAs: the active one first, then the
// previous ones of a roll-over.
func (ca *CA) TrustBundle(ctx context.Context) (string, error) {
	if err := ca.load(ctx); err != nil {
		return "", err
	}
	ca.mu.Lock()
	defer ca.mu.Unlock()
	bundle := ca.certPEM
	for _, p := range ca.previous {
		bundle += p
	}
	return bundle, nil
}

// TrustPool is the certificate pool of the trust bundle (mTLS servers).
func (ca *CA) TrustPool(ctx context.Context) (*x509.CertPool, error) {
	bundle, err := ca.TrustBundle(ctx)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(bundle))
	return pool, nil
}

// Sign issues a 90-day client certificate for the collector from a CSR.
// The subject is set by the CA (CN = collector id, O = organization); a
// CSR naming another collector is refused.
func (ca *CA) Sign(ctx context.Context, csrPEM, orgID, collectorID string) (*IssuedCert, error) {
	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, fmt.Errorf("%w: no PEM certificate request", ErrInvalidCSR)
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCSR, err)
	}
	if err = csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCSR, err)
	}
	if cn := csr.Subject.CommonName; cn != "" && cn != collectorID {
		return nil, fmt.Errorf("%w: common name %q is not the collector id", ErrInvalidCSR, cn)
	}
	if kerr := checkKey(csr.PublicKey); kerr != nil {
		return nil, kerr
	}
	if lerr := ca.load(ctx); lerr != nil {
		return nil, lerr
	}
	ca.mu.Lock()
	defer ca.mu.Unlock()
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := ca.now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: collectorID, Organization: []string{orgID}},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(CertValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, csr.PublicKey, ca.key)
	if err != nil {
		return nil, fmt.Errorf("sign collector certificate: %w", err)
	}
	sum := sha256.Sum256(der)
	bundle := ca.certPEM
	for _, p := range ca.previous {
		bundle += p
	}
	return &IssuedCert{
		CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		CABundlePEM:    bundle,
		Serial:         serial.Text(16),
		Fingerprint:    hex.EncodeToString(sum[:]),
		NotBefore:      tmpl.NotBefore.UTC(),
		NotAfter:       tmpl.NotAfter.UTC(),
	}, nil
}

// CRL is the revocation list of the collector certificates, signed by the
// active CA: every revoked certificate that has not expired, across
// organizations.
func (ca *CA) CRL(ctx context.Context) ([]byte, error) {
	if err := ca.load(ctx); err != nil {
		return nil, err
	}
	var entries []x509.RevocationListEntry
	err := database.WithSystem(ctx, ca.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT serial, revoked_at FROM collector_certificate
			WHERE revoked_at IS NOT NULL AND not_after > now() ORDER BY revoked_at`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var serial string
			var at time.Time
			if serr := rows.Scan(&serial, &at); serr != nil {
				return serr
			}
			n, ok := new(big.Int).SetString(serial, 16)
			if !ok {
				return fmt.Errorf("invalid serial %q", serial)
			}
			entries = append(entries, x509.RevocationListEntry{SerialNumber: n, RevocationTime: at.UTC()})
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("revoked collector certificates: %w", err)
	}
	ca.mu.Lock()
	defer ca.mu.Unlock()
	now := ca.now()
	der, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{
		Number:                    big.NewInt(now.UnixNano()),
		ThisUpdate:                now,
		NextUpdate:                now.Add(time.Hour),
		RevokedCertificateEntries: entries,
	}, ca.cert, ca.key)
	if err != nil {
		return nil, fmt.Errorf("sign revocation list: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der}), nil
}

// IsRevoked reports whether the certificate with the serial (hex) was
// revoked; the NATS endpoint refuses such certificates (WP-146).
func (ca *CA) IsRevoked(ctx context.Context, serial string) (bool, error) {
	var revoked bool
	err := database.WithSystem(ctx, ca.pool, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM collector_certificate WHERE serial = $1 AND revoked_at IS NOT NULL)`,
			serial).Scan(&revoked)
	})
	return revoked, err
}

func checkKey(pub any) error {
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		if k.Curve.Params().BitSize < 256 {
			return fmt.Errorf("%w: ECDSA key below P-256", ErrInvalidCSR)
		}
	case *rsa.PublicKey:
		if k.N.BitLen() < 2048 {
			return fmt.Errorf("%w: RSA key below 2048 bits", ErrInvalidCSR)
		}
	case ed25519.PublicKey:
	default:
		return fmt.Errorf("%w: unsupported key type", ErrInvalidCSR)
	}
	return nil
}

func randomSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
}

// CertIssuer issues the certificate of a collector inside the repository's
// transaction.
type CertIssuer func(ctx context.Context, orgID, collectorID string) (*IssuedCert, error)

// WithCA enables the collector certificates (COL-04).
func (h *Handler) WithCA(ca *CA) *Handler {
	h.ca = ca
	return h
}

func (h *Handler) issuer(csrPEM string) CertIssuer {
	return func(ctx context.Context, orgID, collectorID string) (*IssuedCert, error) {
		return h.ca.Sign(ctx, csrPEM, orgID, collectorID)
	}
}

// PKI handles GET /api/v1/collectors/pki: the trust bundle (active and
// previous CA) and the current revocation list.
func (h *Handler) PKI(w http.ResponseWriter, r *http.Request) {
	if h.ca == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "the collector PKI is not configured")
		return
	}
	bundle, err := h.ca.TrustBundle(r.Context())
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	crl, err := h.ca.CRL(r.Context())
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]string{"ca_bundle_pem": bundle, "crl_pem": string(crl)})
}

// IssueServer issues the TLS server certificate of the collector NATS
// endpoint (COL-08) for the host names: collectors trust it through the CA
// bundle they received at enrollment. The key is generated here; it is
// handed to the NATS server.
func (ca *CA) IssueServer(ctx context.Context, hosts []string, validity time.Duration) (certPEM, keyPEM string, err error) {
	return ca.issueWithKey(ctx, "reticora-collector-nats", hosts, validity, x509.ExtKeyUsageServerAuth)
}

// IssueInternalClient issues a client certificate for a Reticora service
// (CN = name) that connects to the collector NATS endpoint, e.g. its
// authorization service.
func (ca *CA) IssueInternalClient(ctx context.Context, name string, validity time.Duration) (certPEM, keyPEM string, err error) {
	return ca.issueWithKey(ctx, name, nil, validity, x509.ExtKeyUsageClientAuth)
}

func (ca *CA) issueWithKey(ctx context.Context, cn string, hosts []string, validity time.Duration, usage x509.ExtKeyUsage) (certPEM, keyPEM string, err error) {
	if lerr := ca.load(ctx); lerr != nil {
		return "", "", lerr
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	serial, err := randomSerial()
	if err != nil {
		return "", "", err
	}
	ca.mu.Lock()
	defer ca.mu.Unlock()
	now := ca.now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn, Organization: []string{"Reticora"}},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(validity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else if h != "" {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return "", "", fmt.Errorf("issue %s certificate: %w", cn, err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})), nil
}
