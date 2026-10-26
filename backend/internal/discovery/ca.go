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
	"strings"
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
	// ErrNoPreviousCA: RetirePrevious found no previous CA to retire.
	ErrNoPreviousCA = errors.New("there is no previous collector CA to retire")
)

// PreviousCAInUseError is the refusal of RetirePrevious while active
// collectors still hold a certificate of a previous CA: their CA bundle
// lacks the active CA until they renew (COL-04, WP-414).
type PreviousCAInUseError struct {
	// Collectors is the number of those collectors.
	Collectors int
}

func (e *PreviousCAInUseError) Error() string {
	return fmt.Sprintf("%d active collectors still hold a certificate of a previous collector CA; they receive the new CA bundle with their next renewal", e.Collectors)
}

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
// signs, the previous ones stay trust anchors during a roll-over, and the
// oldest trusted CA issues the edge server certificate until the previous
// ones are retired (COL-04). Every replica keeps the CA state in memory and
// reads it anew once collector_ca changed: before every signature, export
// and callout it compares the version of the table, and the workers reload
// it on the announcement of a change (CollectorCAChangedSubject).
type CA struct {
	pool *pgxpool.Pool
	enc  *platformcrypto.EnvelopeEncryptor
	now  func() time.Time

	mu sync.Mutex
	st *caState
}

// caState is one reading of the trusted CAs of collector_ca (state active
// or previous, not expired); it is not changed after the reading.
type caState struct {
	// version identifies the reading (caVersionSQL).
	version string
	// active signs the collector certificates.
	active *caCert
	// edge issues the endpoint's server certificate and the internal client
	// certificates: the oldest trusted CA. Every enrolled collector trusts
	// it, and the endpoint trusts it before and after a roll-over or a
	// retirement.
	edge *caCert
	// anchors are the trusted CAs, the active one first, then the previous
	// ones, the newest first.
	anchors []*caCert
}

// caCert is one CA of collector_ca; key is set for the active CA and the
// edge issuer.
type caCert struct {
	id        string
	cert      *x509.Certificate
	certPEM   string
	key       crypto.Signer
	createdAt time.Time
}

// bundle is the PEM of the trust anchors.
func (s *caState) bundle() string {
	var b strings.Builder
	for _, a := range s.anchors {
		b.WriteString(a.certPEM)
	}
	return b.String()
}

// caVersionSQL is the version of the trusted CAs: it changes with every
// roll-over and retirement (and with the expiry of a CA).
const caVersionSQL = `SELECT COALESCE(string_agg(id::text || ':' || state, ',' ORDER BY created_at, id), '')
	FROM collector_ca WHERE state IN ('active', 'previous') AND not_after > now()`

// caLockClass is the class of the advisory lock of collector_ca: a change
// (Rollover, RetirePrevious) holds it exclusively, the issuance of a
// collector certificate shared (lockCAShared). No certificate is signed
// while a change is in progress, and every certificate of a former CA was
// issued in a transaction that committed before the change created the new
// CA (RetirePrevious counts them by collector_certificate.created_at). Like
// edgePKILockClass it uses the two-argument form and differs from the other
// classes (lanes.go).
const caLockClass int32 = 0x52434153 // "RCAS"

// caLockWait bounds the wait of a change for the issuances in progress and
// for the exports of the replicas.
const caLockWait = time.Minute

// NewCA creates the CA on pool; the key is loaded or created on first use.
func NewCA(pool *pgxpool.Pool, enc *platformcrypto.EnvelopeEncryptor) *CA {
	return &CA{pool: pool, enc: enc, now: func() time.Time { return time.Now().UTC() }}
}

// WithClock replaces the clock (tests).
func (ca *CA) WithClock(now func() time.Time) *CA {
	ca.now = now
	return ca
}

// state returns the CA state: the one in memory while collector_ca has not
// changed since it was read (version check), otherwise the table is read
// anew; the first CA is created on first use.
func (ca *CA) state(ctx context.Context) (*caState, error) {
	for attempt := 0; attempt < 2; attempt++ {
		var st *caState
		err := database.WithOperator(ctx, ca.pool, func(ctx context.Context, tx pgx.Tx) error {
			var serr error
			st, serr = ca.stateTx(ctx, tx)
			return serr
		})
		if err != nil {
			return nil, fmt.Errorf("load collector CA: %w", err)
		}
		if st != nil {
			return st, nil
		}
		if cerr := ca.create(ctx); cerr != nil {
			return nil, cerr
		}
	}
	return nil, errors.New("collector CA could not be created")
}

// stateTx is state within tx (operator context) without creating a CA: nil
// when there is no active one.
func (ca *CA) stateTx(ctx context.Context, tx pgx.Tx) (*caState, error) {
	var version string
	if err := tx.QueryRow(ctx, caVersionSQL).Scan(&version); err != nil {
		return nil, err
	}
	ca.mu.Lock()
	cur := ca.st
	ca.mu.Unlock()
	if cur != nil && cur.version == version {
		return cur, nil
	}
	st, err := readCAState(ctx, tx, ca.enc)
	if err != nil || st == nil {
		return nil, err
	}
	ca.mu.Lock()
	ca.st = st
	ca.mu.Unlock()
	return st, nil
}

// readCAState reads the trusted CAs within tx (operator context); nil when
// there is no active CA.
func readCAState(ctx context.Context, tx pgx.Tx, enc *platformcrypto.EnvelopeEncryptor) (*caState, error) {
	rows, err := tx.Query(ctx, `SELECT id::text, cert_pem, key_ciphertext, key_dek, state, created_at FROM collector_ca
		WHERE state IN ('active', 'previous') AND not_after > now() ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	type row struct {
		ca          *caCert
		keyCT, dek  []byte
		state       string
		versionPart string
	}
	var read []row
	for rows.Next() {
		var r row
		r.ca = &caCert{}
		if err = rows.Scan(&r.ca.id, &r.ca.certPEM, &r.keyCT, &r.dek, &r.state, &r.ca.createdAt); err != nil {
			rows.Close()
			return nil, err
		}
		r.versionPart = r.ca.id + ":" + r.state
		read = append(read, r)
	}
	rows.Close()
	if rerr := rows.Err(); rerr != nil {
		return nil, rerr
	}
	st := &caState{}
	parts := make([]string, 0, len(read))
	var previous []*caCert
	for i := range read {
		r := &read[i]
		parts = append(parts, r.versionPart)
		if r.ca.cert, err = parseCACert(r.ca.certPEM); err != nil {
			return nil, err
		}
		// The oldest CA issues the edge server certificate, the active one
		// signs: only their keys are decrypted.
		if i == 0 || r.state == "active" {
			if r.ca.key, err = decryptCAKey(enc, r.keyCT, r.dek); err != nil {
				return nil, err
			}
		}
		if i == 0 {
			st.edge = r.ca
		}
		if r.state == "active" {
			st.active = r.ca
		} else {
			previous = append([]*caCert{r.ca}, previous...)
		}
	}
	if st.active == nil {
		return nil, nil
	}
	st.version = strings.Join(parts, ",")
	st.anchors = append([]*caCert{st.active}, previous...)
	return st, nil
}

func parseCACert(certPEM string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return nil, errors.New("collector CA: invalid certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("collector CA: %w", err)
	}
	return cert, nil
}

func decryptCAKey(enc *platformcrypto.EnvelopeEncryptor, keyCT, keyDEK []byte) (crypto.Signer, error) {
	dek, err := enc.UnwrapDEK(keyDEK)
	if err != nil {
		return nil, fmt.Errorf("collector CA: unwrap key: %w", err)
	}
	der, err := platformcrypto.Decrypt(dek, keyCT)
	if err != nil {
		return nil, fmt.Errorf("collector CA: decrypt key: %w", err)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("collector CA: parse key: %w", err)
	}
	signer, ok := parsed.(crypto.Signer)
	if !ok {
		return nil, errors.New("collector CA: key cannot sign")
	}
	return signer, nil
}

// newCA generates a CA: its certificate and its key, encrypted with a data
// key of its own wrapped by the master key.
func (ca *CA) newCA() (certPEM string, keyCT, keyDEK []byte, notAfter time.Time, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", nil, nil, time.Time{}, err
	}
	now := ca.now()
	serial, err := randomSerial()
	if err != nil {
		return "", nil, nil, time.Time{}, err
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
		return "", nil, nil, time.Time{}, fmt.Errorf("create collector CA: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", nil, nil, time.Time{}, err
	}
	dek, err := platformcrypto.GenerateDEK()
	if err != nil {
		return "", nil, nil, time.Time{}, err
	}
	if keyCT, err = platformcrypto.Encrypt(dek, keyDER); err != nil {
		return "", nil, nil, time.Time{}, err
	}
	if keyDEK, err = ca.enc.WrapDEK(dek); err != nil {
		return "", nil, nil, time.Time{}, err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), keyCT, keyDEK, tmpl.NotAfter, nil
}

// create generates the first CA. A concurrent creation by another replica
// wins (unique active index).
func (ca *CA) create(ctx context.Context) error {
	certPEM, keyCT, keyDEK, notAfter, err := ca.newCA()
	if err != nil {
		return err
	}
	return database.WithOperator(ctx, ca.pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO collector_ca (cert_pem, key_ciphertext, key_dek, not_after)
			VALUES ($1, $2, $3, $4) ON CONFLICT (state) WHERE state = 'active' DO NOTHING`, certPEM, keyCT, keyDEK, notAfter)
		return err
	})
}

// CAInfo describes a CA of collector_ca.
type CAInfo struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	// Fingerprint is the SHA-256 of the certificate (hex).
	Fingerprint string    `json:"fingerprint"`
	CreatedAt   time.Time `json:"created_at"`
	NotAfter    time.Time `json:"not_after"`
}

func (c *caCert) info() CAInfo {
	sum := sha256.Sum256(c.cert.Raw)
	return CAInfo{ID: c.id, Subject: c.cert.Subject.CommonName, Fingerprint: hex.EncodeToString(sum[:]),
		CreatedAt: c.createdAt.UTC(), NotAfter: c.cert.NotAfter.UTC()}
}

// CAStatus is the state of the collector CA (server collector-ca status).
type CAStatus struct {
	// Active signs, Previous are the further trust anchors, EdgeIssuer
	// issues the edge server certificate.
	Active     CAInfo   `json:"active"`
	Previous   []CAInfo `json:"previous"`
	EdgeIssuer CAInfo   `json:"edge_issuer"`
	// Collectors is the number of active collectors with a certificate of a
	// previous CA: RetirePrevious refuses while it is not 0.
	Collectors int `json:"collectors_on_previous"`
}

// CAChange is a roll-over or a retirement of the collector CA.
type CAChange struct {
	// Active, Previous and EdgeIssuer are the state after the change.
	Active     CAInfo   `json:"active"`
	Previous   []CAInfo `json:"previous"`
	EdgeIssuer CAInfo   `json:"edge_issuer"`
	// Retired are the CAs RetirePrevious retired; Overridden is the number
	// of active collectors with a certificate of one of them that
	// CAChangeOptions.Force overrode.
	Retired    []CAInfo `json:"retired,omitempty"`
	Overridden int      `json:"overridden_collectors"`
	// EdgeReloaded: the endpoint loaded the files of the new state before
	// the change committed (CAChangeOptions.Edge).
	EdgeReloaded bool `json:"edge_reloaded"`
	version      string
}

// CAChangeOptions configures Rollover and RetirePrevious.
type CAChangeOptions struct {
	// Force retires the previous CAs although active collectors still hold
	// a certificate of one of them (RetirePrevious).
	Force bool
	// Edge is the collector NATS endpoint (PKIDir, URL, Hosts, Secrets): the
	// change writes its files under the lock of the export and has it reload
	// them before the change commits, so it trusts the new CA before the
	// first signature of it (roll-over) and serves the server certificate of
	// the new CA (retirement). nil leaves the files to the edge workers.
	Edge *EdgeConfig
	// Audit records the change within its transaction (operator context).
	Audit func(ctx context.Context, tx pgx.Tx, ch *CAChange) error
}

// Status returns the state of the collector CA and the number of active
// collectors that still hold a certificate of a previous CA.
func (ca *CA) Status(ctx context.Context) (*CAStatus, error) {
	st, err := ca.state(ctx)
	if err != nil {
		return nil, err
	}
	out := &CAStatus{Active: st.active.info(), EdgeIssuer: st.edge.info(), Previous: []CAInfo{}}
	for _, a := range st.anchors[1:] {
		out.Previous = append(out.Previous, a.info())
	}
	if len(st.anchors) > 1 {
		if out.Collectors, err = ca.previousCACollectors(ctx, st.active.createdAt); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// previousCACollectors counts the collectors that hold a certificate of a
// former CA: an unrevoked, unexpired certificate issued before the active
// CA was created (activeAt, caLockClass). A disabled collector holds none
// (disabling revokes its certificates); a renewed one holds its predecessor
// until it connects with the successor (callout) or SupersededGrace passed.
func (ca *CA) previousCACollectors(ctx context.Context, activeAt time.Time) (int, error) {
	var n int
	err := database.WithSystem(ctx, ca.pool, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(DISTINCT collector_id) FROM collector_certificate
			WHERE revoked_at IS NULL AND not_after > now() AND created_at < $1`, activeAt).Scan(&n)
	})
	if err != nil {
		return 0, fmt.Errorf("collectors on a previous collector CA: %w", err)
	}
	return n, nil
}

// Rollover replaces the active CA (COL-04): a new CA signs from then on; the
// former one stays a trust anchor and, as the oldest one, keeps issuing the
// edge server certificate until RetirePrevious, so the collectors enrolled
// before still trust the endpoint and are trusted by it.
func (ca *CA) Rollover(ctx context.Context, opts CAChangeOptions) (*CAChange, error) {
	certPEM, keyCT, keyDEK, notAfter, err := ca.newCA()
	if err != nil {
		return nil, err
	}
	return ca.change(ctx, opts, func(ctx context.Context, tx pgx.Tx, _ *caState, _ *CAChange) error {
		if _, err := tx.Exec(ctx, `UPDATE collector_ca SET state = 'previous' WHERE state = 'active'`); err != nil {
			return err
		}
		// Taken under the exclusive lock: every certificate a former CA
		// signed was issued in a transaction that began before.
		_, err := tx.Exec(ctx, `INSERT INTO collector_ca (cert_pem, key_ciphertext, key_dek, not_after, created_at)
			VALUES ($1, $2, $3, $4, clock_timestamp())`, certPEM, keyCT, keyDEK, notAfter)
		return err
	})
}

// RetirePrevious ends the transition: the former CAs are no longer trusted
// and the edge server certificate is issued by the active CA. It refuses
// with PreviousCAInUseError while active collectors still hold a
// certificate of a former CA, unless opts.Force (CAChange.Overridden).
func (ca *CA) RetirePrevious(ctx context.Context, opts CAChangeOptions) (*CAChange, error) {
	return ca.change(ctx, opts, func(ctx context.Context, tx pgx.Tx, before *caState, ch *CAChange) error {
		if before == nil || len(before.anchors) < 2 {
			return ErrNoPreviousCA
		}
		n, err := ca.previousCACollectors(ctx, before.active.createdAt)
		if err != nil {
			return err
		}
		if n > 0 && !opts.Force {
			return &PreviousCAInUseError{Collectors: n}
		}
		ch.Overridden = n
		for _, a := range before.anchors[1:] {
			ch.Retired = append(ch.Retired, a.info())
		}
		_, err = tx.Exec(ctx, `UPDATE collector_ca SET state = 'retired' WHERE state = 'previous'`)
		return err
	})
}

// change runs a change of collector_ca in one operator transaction under
// the exclusive CA lock: mutate changes the table, then, with opts.Edge,
// the endpoint's files are written for the new state and reloaded, and
// opts.Audit records the change. A failure rolls the change back; files it
// wrote are exported again from the committed state.
func (ca *CA) change(ctx context.Context, opts CAChangeOptions,
	mutate func(ctx context.Context, tx pgx.Tx, before *caState, ch *CAChange) error) (*CAChange, error) {
	if e := opts.Edge; e != nil && (e.PKIDir == "" || e.URL == "" || e.Secrets == nil) {
		return nil, errors.New("collector CA change: the endpoint needs its PKI directory, URL and secrets")
	}
	ch := &CAChange{}
	var after *caState
	wrote := false
	err := database.WithOperator(ctx, ca.pool, func(ctx context.Context, tx pgx.Tx) error {
		if err := lockCA(ctx, tx); err != nil {
			return err
		}
		before, err := readCAState(ctx, tx, ca.enc)
		if err != nil {
			return err
		}
		if merr := mutate(ctx, tx, before, ch); merr != nil {
			return merr
		}
		if after, err = readCAState(ctx, tx, ca.enc); err != nil {
			return err
		}
		if after == nil {
			return errors.New("collector CA change left no active CA")
		}
		ch.version, ch.Active, ch.EdgeIssuer, ch.Previous = after.version, after.active.info(), after.edge.info(), []CAInfo{}
		for _, a := range after.anchors[1:] {
			ch.Previous = append(ch.Previous, a.info())
		}
		if opts.Edge != nil {
			wrote = true
			if aerr := applyEdgeChange(ctx, tx, opts.Edge, after, ca.now()); aerr != nil {
				return aerr
			}
			ch.EdgeReloaded = true
		}
		if opts.Audit != nil {
			return opts.Audit(ctx, tx, ch)
		}
		return nil
	})
	if err != nil {
		if wrote {
			err = errors.Join(err, restoreEdge(ctx, ca, opts.Edge))
		}
		return nil, err
	}
	ca.mu.Lock()
	ca.st = after
	ca.mu.Unlock()
	return ch, nil
}

// lockCA takes the CA lock exclusively within tx (change of collector_ca).
func lockCA(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout', $1, true)`, fmt.Sprintf("%dms", caLockWait.Milliseconds())); err != nil {
		return fmt.Errorf("collector CA lock: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1, 0)`, caLockClass); err != nil {
		return fmt.Errorf("collector CA lock: %w", err)
	}
	return nil
}

// lockCAShared takes the CA lock shared within tx, the transaction that
// issues and records a collector certificate: it waits for a change of the
// CA in progress, and a change waits for it.
func lockCAShared(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1, 0)`, caLockClass); err != nil {
		return fmt.Errorf("collector CA lock: %w", err)
	}
	return nil
}

// TrustBundle is the PEM of the trusted CAs: the active one first, then the
// previous ones of a roll-over.
func (ca *CA) TrustBundle(ctx context.Context) (string, error) {
	st, err := ca.state(ctx)
	if err != nil {
		return "", err
	}
	return st.bundle(), nil
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

// Sign issues a 90-day client certificate for the collector from a CSR with
// the active CA. The subject is set by the CA (CN = collector id, O =
// organization); a CSR naming another collector is refused.
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
	st, err := ca.state(ctx)
	if err != nil {
		return nil, err
	}
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
	der, err := x509.CreateCertificate(rand.Reader, tmpl, st.active.cert, csr.PublicKey, st.active.key)
	if err != nil {
		return nil, fmt.Errorf("sign collector certificate: %w", err)
	}
	sum := sha256.Sum256(der)
	return &IssuedCert{
		CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		CABundlePEM:    st.bundle(),
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
	st, err := ca.state(ctx)
	if err != nil {
		return nil, err
	}
	var entries []x509.RevocationListEntry
	err = database.WithSystem(ctx, ca.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, qerr := tx.Query(ctx, `SELECT serial, revoked_at FROM collector_certificate
			WHERE revoked_at IS NOT NULL AND not_after > now() ORDER BY revoked_at`)
		if qerr != nil {
			return qerr
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
	now := ca.now()
	der, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{
		Number:                    big.NewInt(now.UnixNano()),
		ThisUpdate:                now,
		NextUpdate:                now.Add(time.Hour),
		RevokedCertificateEntries: entries,
	}, st.active.cert, st.active.key)
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
// endpoint (COL-08) for the host names, by the edge issuer (the oldest
// trusted CA): every collector trusts it through the CA bundle it received
// at enrollment or renewal, also during a roll-over. The key is generated
// here; it is handed to the NATS server.
func (ca *CA) IssueServer(ctx context.Context, hosts []string, validity time.Duration) (certPEM, keyPEM string, err error) {
	st, err := ca.state(ctx)
	if err != nil {
		return "", "", err
	}
	return issueWithKey(st.edge, "reticora-collector-nats", hosts, validity, x509.ExtKeyUsageServerAuth, ca.now())
}

// IssueInternalClient issues a client certificate for a Reticora service
// (CN = name) that connects to the collector NATS endpoint, e.g. its
// authorization service, by the edge issuer: the endpoint trusts it before
// and after a roll-over or a retirement.
func (ca *CA) IssueInternalClient(ctx context.Context, name string, validity time.Duration) (certPEM, keyPEM string, err error) {
	st, err := ca.state(ctx)
	if err != nil {
		return "", "", err
	}
	return issueWithKey(st.edge, name, nil, validity, x509.ExtKeyUsageClientAuth, ca.now())
}

// issueWithKey issues a certificate with a new key by issuer.
func issueWithKey(issuer *caCert, cn string, hosts []string, validity time.Duration, usage x509.ExtKeyUsage, now time.Time) (certPEM, keyPEM string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	serial, err := randomSerial()
	if err != nil {
		return "", "", err
	}
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
	der, err := x509.CreateCertificate(rand.Reader, tmpl, issuer.cert, &key.PublicKey, issuer.key)
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
