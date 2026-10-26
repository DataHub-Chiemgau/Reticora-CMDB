package discovery

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
	platformcrypto "github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

// The collector NATS endpoint (COL-08, PRI-07): collectors connect
// outbound to port 4222 with mutual TLS (client certificate of WP-145).
// The endpoint is a NATS server of its own (deploy: nats-edge) whose
// clients are placed in the account COLLECTORS by the authorization
// callout below; a leaf node bridges the account to the internal NATS.
const (
	// EdgeAccount is the NATS account of the collectors.
	EdgeAccount = "COLLECTORS"
	// EdgeAuthSubject is the subject of the authorization callout.
	EdgeAuthSubject = "$SYS.REQ.USER.AUTH"
	// EdgeServerCertValidity is the lifetime of the endpoint's server
	// certificate.
	EdgeServerCertValidity = 397 * 24 * time.Hour
	// EdgeUserValidity is the lifetime of the user JWTs the callout issues:
	// the endpoint closes a connection when its JWT expires, the collector
	// connects again and passes the callout again, so a connection the cut
	// of a disable missed ends at the latest then (COL-04).
	EdgeUserValidity = 15 * time.Minute
)

// CollectorSubject is the subject tree of one collector: it may publish
// and subscribe below it and nowhere else.
func CollectorSubject(orgID, collectorID string) string {
	return "collector." + orgID + "." + collectorID
}

// CollectorInbox is the reply prefix of a collector (nats.CustomInboxPrefix):
// replies to one collector cannot be read by another.
func CollectorInbox(collectorID string) string {
	return "_INBOX." + collectorID
}

// Edge authorization errors.
var (
	ErrNoClientCertificate = errors.New("no client certificate")
	ErrCertificateRevoked  = errors.New("collector certificate revoked")
)

// EdgeIdentity is an authorized collector connection.
type EdgeIdentity struct {
	OrganizationID string
	CollectorID    string
	ClientID       string
	Status         string
	NotBefore      time.Time
	NotAfter       time.Time
	// PublicKey is the collector's key from its certificate.
	PublicKey any
	// Serial is the serial number of the certificate (hex), Fingerprint its
	// SHA-256 (hex).
	Serial      string
	Fingerprint string
}

// EdgeAuthorizer decides whether a TLS client may connect: its certificate
// chains to the collector CA, is meant for client authentication, is not
// revoked, and names a collector of its organization that is not disabled.
type EdgeAuthorizer struct {
	ca   *CA
	pool *pgxpool.Pool
	now  func() time.Time
}

// NewEdgeAuthorizer creates the authorizer.
func NewEdgeAuthorizer(pool *pgxpool.Pool, ca *CA) *EdgeAuthorizer {
	return &EdgeAuthorizer{ca: ca, pool: pool, now: func() time.Time { return time.Now().UTC() }}
}

// Authorize checks the PEM chain the NATS server received (leaf first).
func (a *EdgeAuthorizer) Authorize(ctx context.Context, certs []string) (*EdgeIdentity, error) {
	if len(certs) == 0 {
		return nil, ErrNoClientCertificate
	}
	leaf, err := parsePEMCert(certs[0])
	if err != nil {
		return nil, err
	}
	roots, err := a.ca.TrustPool(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = leaf.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: a.now(),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return nil, fmt.Errorf("client certificate: %w", err)
	}
	serial := leaf.SerialNumber.Text(16)
	revoked, err := a.ca.IsRevoked(ctx, serial)
	if err != nil {
		return nil, err
	}
	if revoked {
		return nil, ErrCertificateRevoked
	}
	if len(leaf.Subject.Organization) != 1 || !audit.IsUUID(leaf.Subject.CommonName) || !audit.IsUUID(leaf.Subject.Organization[0]) {
		return nil, errors.New("client certificate names no collector")
	}
	sum := sha256.Sum256(leaf.Raw)
	id := &EdgeIdentity{OrganizationID: leaf.Subject.Organization[0], CollectorID: leaf.Subject.CommonName,
		NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter, PublicKey: leaf.PublicKey, Serial: serial, Fingerprint: hex.EncodeToString(sum[:])}
	id.Status, id.ClientID, err = a.collector(ctx, id.OrganizationID, id.CollectorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errors.New("unknown collector")
	}
	if err != nil {
		return nil, err
	}
	if id.Status == CollectorDisabled {
		return nil, ErrCollectorDisabled
	}
	return id, nil
}

// collector returns the status and client of a collector of the
// organization; an unknown one fails with pgx.ErrNoRows.
func (a *EdgeAuthorizer) collector(ctx context.Context, orgID, collectorID string) (status, clientID string, err error) {
	scope := database.OrgWideScope(orgID, "")
	err = database.WithTenant(ctx, a.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status, COALESCE(client_id::text, '') FROM collector WHERE id = $1 AND organization_id = $2`,
			collectorID, orgID).Scan(&status, &clientID)
	})
	return status, clientID, err
}

// revokePredecessors revokes the collector's certificates issued before the
// one id was authorized with: once the collector connects with a renewed
// certificate (the TLS handshake proved its key), the former ones are no
// longer needed (COL-04).
func (a *EdgeAuthorizer) revokePredecessors(ctx context.Context, id *EdgeIdentity) (int64, error) {
	var n int64
	scope := database.OrgWideScope(id.OrganizationID, "")
	err := database.WithTenant(ctx, a.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE collector_certificate p SET revoked_at = now(), revocation_reason = 'superseded'
			FROM collector_certificate n
			WHERE n.serial = $2 AND n.collector_id = $1 AND p.collector_id = $1 AND p.revoked_at IS NULL
			AND p.created_at < n.created_at`, id.CollectorID, id.Serial)
		n = tag.RowsAffected()
		return err
	})
	return n, err
}

// RevokeSupersededCertificates revokes, across organizations, the collector
// certificates whose successor was issued at least grace ago: a collector
// that never connects with its renewed certificate keeps the former one no
// longer (COL-04). It returns the number of revoked certificates.
func RevokeSupersededCertificates(ctx context.Context, pool *pgxpool.Pool, grace time.Duration) (int64, error) {
	const superseded = `p.revoked_at IS NULL AND EXISTS (SELECT 1 FROM collector_certificate n
		WHERE n.collector_id = p.collector_id AND n.created_at > p.created_at AND n.created_at <= now() - make_interval(secs => $1))`
	var orgs []string
	err := database.WithSystem(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT DISTINCT p.organization_id::text FROM collector_certificate p WHERE `+superseded, grace.Seconds())
		if err != nil {
			return err
		}
		orgs, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("superseded collector certificates: %w", err)
	}
	var revoked int64
	for _, orgID := range orgs {
		scope := database.OrgWideScope(orgID, "")
		err = database.WithTenant(ctx, pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
			tag, uerr := tx.Exec(ctx, `UPDATE collector_certificate p SET revoked_at = now(), revocation_reason = 'superseded'
				WHERE organization_id = $2 AND `+superseded, grace.Seconds(), orgID)
			revoked += tag.RowsAffected()
			return uerr
		})
		if err != nil {
			return revoked, fmt.Errorf("revoke superseded collector certificates: %w", err)
		}
	}
	return revoked, nil
}

// UserClaims are the NATS permissions of an authorized collector: its own
// subject tree and its own reply inbox, in the collectors' account, valid
// for EdgeUserValidity and at most as long as its certificate.
func (id *EdgeIdentity) UserClaims(userNkey string, now time.Time) *jwt.UserClaims {
	return id.userClaims(userNkey, now, EdgeUserValidity)
}

func (id *EdgeIdentity) userClaims(userNkey string, now time.Time, validity time.Duration) *jwt.UserClaims {
	uc := jwt.NewUserClaims(userNkey)
	uc.Name = id.CollectorID
	uc.Audience = EdgeAccount
	tree := CollectorSubject(id.OrganizationID, id.CollectorID) + ".>"
	uc.Pub.Allow.Add(tree)
	uc.Sub.Allow.Add(tree, CollectorInbox(id.CollectorID)+".>")
	expires := now.Add(validity)
	if id.NotAfter.Before(expires) {
		expires = id.NotAfter
	}
	uc.Expires = expires.Unix()
	return uc
}

func parsePEMCert(p string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(p))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("client certificate: no PEM certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

// EdgeAuthUser is the login of the callout service at the endpoint.
const EdgeAuthUser = "reticora-edge-auth"

// EdgeSysUser is the login of the edge worker in the endpoint's system
// account: it looks up and cuts the connections of a disabled collector
// (COL-04).
const EdgeSysUser = "reticora-edge-sys"

// EdgeSecrets are the issuer key of the callout and the passwords of its
// service login and of the system login, derived from the derivation root of
// the installation (crypto.LoadDerivationRoot): the server and the endpoint
// configuration it writes agree without a further secret, and a master-key
// rotation leaves them unchanged, so a running endpoint keeps accepting the
// callout of a restarted server (SEC-06, WP-459).
type EdgeSecrets struct {
	IssuerSeed  string
	IssuerKey   string
	Password    string
	SysPassword string
}

// DeriveEdgeSecrets derives the endpoint's secrets from the derivation root
// of enc, which must be open.
func DeriveEdgeSecrets(enc *platformcrypto.EnvelopeEncryptor) (*EdgeSecrets, error) {
	seed, err := nkeys.EncodeSeed(nkeys.PrefixByteAccount, enc.Derive("reticora/nats-edge/issuer"))
	if err != nil {
		return nil, err
	}
	kp, err := nkeys.FromSeed(seed)
	if err != nil {
		return nil, err
	}
	pub, err := kp.PublicKey()
	if err != nil {
		return nil, err
	}
	return &EdgeSecrets{IssuerSeed: string(seed), IssuerKey: pub,
		Password:    hex.EncodeToString(enc.Derive("reticora/nats-edge/auth-password")),
		SysPassword: hex.EncodeToString(enc.Derive("reticora/nats-edge/sys-password"))}, nil
}

// edgeConf is the generated part of nats-edge.conf: TLS with client
// verification against the collector CA, the accounts and the callout. The
// callout's login and the edge worker's login in the system account bypass
// the callout (auth_users); every connection still needs a client
// certificate of the collector CA.
func edgeConf(sec *EdgeSecrets) string {
	return `# Generated by the Reticora server (worker nats_edge); do not edit.
tls {
  cert_file: "pki/server.pem"
  key_file: "pki/server-key.pem"
  ca_file: "pki/ca.pem"
  verify: true
  handshake_first: true
  timeout: 5
}
accounts {
  AUTH { users: [ { user: "` + EdgeAuthUser + `", password: "` + sec.Password + `" } ] }
  ` + EdgeAccount + ` {}
  SYS { users: [ { user: "` + EdgeSysUser + `", password: "` + sec.SysPassword + `" } ] }
}
system_account: SYS
authorization {
  auth_callout {
    issuer: "` + sec.IssuerKey + `"
    auth_users: [ "` + EdgeAuthUser + `", "` + EdgeSysUser + `" ]
    account: AUTH
  }
}
`
}

// EdgeAuthService answers the authorization callout of the collector NATS
// endpoint. issuer is the account key whose public key the endpoint names
// as the callout issuer.
type EdgeAuthService struct {
	authorizer *EdgeAuthorizer
	issuer     nkeys.KeyPair
	now        func() time.Time
	// validity is the lifetime of the user JWTs (EdgeUserValidity).
	validity time.Duration
	// cycles counts the answers that failed (OPS-05); nil counts none.
	cycles *observability.Cycles
}

// NewEdgeAuthService creates the service; issuerSeed is the account seed
// (SA…) of EdgeSecrets.
func NewEdgeAuthService(authorizer *EdgeAuthorizer, issuerSeed string) (*EdgeAuthService, error) {
	kp, err := nkeys.FromSeed([]byte(issuerSeed))
	if err != nil {
		return nil, fmt.Errorf("callout issuer: %w", err)
	}
	if pub, perr := kp.PublicKey(); perr != nil || nkeys.Prefix(pub) != nkeys.PrefixByteAccount {
		return nil, errors.New("callout issuer: the seed must be an account seed (SA…)")
	}
	return &EdgeAuthService{authorizer: authorizer, issuer: kp, now: func() time.Time { return time.Now().UTC() },
		validity: EdgeUserValidity}, nil
}

// Respond answers one authorization request (the JWT the NATS server
// sent) with a signed response: the user JWT or the refusal.
func (s *EdgeAuthService) Respond(ctx context.Context, request []byte) ([]byte, error) {
	req, err := jwt.DecodeAuthorizationRequestClaims(string(request))
	if err != nil {
		return nil, fmt.Errorf("decode authorization request: %w", err)
	}
	resp := jwt.NewAuthorizationResponseClaims(req.UserNkey)
	resp.Audience = req.Server.ID
	// A verified client sends its verified chains (leaf first), otherwise
	// the presented certificates; both are checked again here.
	var certs []string
	if req.TLS != nil {
		certs = req.TLS.Certs
		if len(certs) == 0 && len(req.TLS.VerifiedChains) > 0 {
			certs = req.TLS.VerifiedChains[0]
		}
	}
	id, err := s.authorizer.Authorize(ctx, certs)
	if err == nil {
		// The first connection with a renewed certificate revokes its
		// predecessors; should that fail, RunEdge revokes them later.
		if n, rerr := s.authorizer.revokePredecessors(ctx, id); rerr != nil {
			slog.Error("revoke the predecessors of a renewed collector certificate", "collector_id", id.CollectorID, "error", rerr)
		} else if n > 0 {
			slog.Info("revoked the predecessors of a renewed collector certificate", "collector_id", id.CollectorID, "serial", id.Serial, "revoked", n)
		}
		resp.Jwt, err = id.userClaims(req.UserNkey, s.now(), s.validity).Encode(s.issuer)
	}
	if err != nil {
		slog.Warn("collector refused at the NATS endpoint", "client", req.ClientInformation.Host, "error", err)
		resp.Error = "not authorized"
		resp.Jwt = ""
	}
	token, err := resp.Encode(s.issuer)
	if err != nil {
		return nil, fmt.Errorf("sign authorization response: %w", err)
	}
	return []byte(token), nil
}

// Run serves the callout on nc (connected to the endpoint as its auth
// user) until ctx ends.
func (s *EdgeAuthService) Run(ctx context.Context, nc *nats.Conn) error {
	sub, err := nc.Subscribe(EdgeAuthSubject, func(msg *nats.Msg) {
		answer, rerr := s.Respond(ctx, msg.Data)
		if rerr != nil {
			s.cycles.Fail()
			slog.Error("authorization callout", "error", rerr)
			return
		}
		if rerr = msg.Respond(answer); rerr != nil {
			s.cycles.Fail()
			slog.Error("authorization callout reply", "error", rerr)
		}
	})
	if err != nil {
		return fmt.Errorf("subscribe %s: %w", EdgeAuthSubject, err)
	}
	<-ctx.Done()
	return sub.Unsubscribe()
}

// Modes of the endpoint's files. The volume is shared only by the server
// and the endpoint, which reads the files through its group (K8s: fsGroup
// of nats-edge); the server certificate and the CA bundle are public, the
// key and edge.conf (the passwords of the callout's and the system login)
// are not readable by others (F-104).
const (
	edgePublicMode os.FileMode = 0o644
	edgeSecretMode os.FileMode = 0o640
)

// edgePKILockClass is the class of the advisory lock under which a replica
// exports the endpoint's files, so replicas sharing the PKI volume (k8s
// prod: 3 replicas, ReadWriteMany) export one after the other (F-104). It
// uses the two-argument form, pg_advisory_xact_lock(class, 0), whose lock
// tag never equals the tag of a one-argument lock (the scheduler's leader
// and job locks, the location tree, the operator audit); the class differs
// from those of the audit chain, the event outbox, its publisher and the
// ingest locks (lanes.go).
const edgePKILockClass int32 = 0x52504b49 // "RPKI"

// edgePKILockWait bounds the wait for the export of another replica: a
// replica whose volume hangs does not hold up the start of the others.
const edgePKILockWait = time.Minute

// ExportEdgePKI writes the files of the collector NATS endpoint into dir:
// the CA bundle it verifies client certificates with (ca.pem), its server
// certificate and key for the host names (server.pem, server-key.pem) and
// its TLS, account and callout configuration (edge.conf, included by
// nats-edge.conf). Replicas sharing dir export under an advisory lock, each
// file through a temporary file of its own, from the CA state current under
// the lock (a change of the CA writes the files under the same lock). An
// existing server key pair is kept only while key and certificate match,
// the certificate stems from the edge issuer (the oldest trusted CA: the
// previous one during a roll-over) and 60 % of its lifetime have not
// passed; the endpoint picks up new files on reload.
func ExportEdgePKI(ctx context.Context, ca *CA, sec *EdgeSecrets, dir string, hosts []string, now time.Time) error {
	_, _, err := exportEdgePKI(ctx, ca, sec, dir, hosts, now)
	return err
}

// edgeExport is the outcome of an export: whether a file changed, and the
// server certificate before it (nil without one), which a running endpoint
// serves until it reloads.
type edgeExport struct {
	changed bool
	served  []byte
}

// exportEdgePKI is ExportEdgePKI; it also returns the outcome and the CA
// state it wrote.
func exportEdgePKI(ctx context.Context, ca *CA, sec *EdgeSecrets, dir string, hosts []string, now time.Time) (*edgeExport, *caState, error) {
	// Loading the CA takes a connection of its own: before the lock, so the
	// export holds only the lock's connection. Under the lock the version is
	// checked again on that connection.
	st, err := ca.state(ctx)
	if err != nil {
		return nil, nil, err
	}
	var ex *edgeExport
	err = withEdgePKILock(ctx, ca.pool, func(ctx context.Context, tx pgx.Tx) error {
		cur, serr := ca.stateTx(ctx, tx)
		if serr != nil {
			return serr
		}
		if cur != nil {
			st = cur
		}
		ex, serr = writeEdgePKI(st, sec, dir, hosts, now, ca.now())
		return serr
	})
	return ex, st, err
}

// writeEdgePKI writes the files of ExportEdgePKI for the CA state st; the
// caller holds the lock of the export. now decides whether the server
// certificate is due, a new one is issued at issueAt (the CA's clock).
func writeEdgePKI(st *caState, sec *EdgeSecrets, dir string, hosts []string, now, issueAt time.Time) (*edgeExport, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("pki dir: %w", err)
	}
	ex := &edgeExport{}
	certPath, keyPath := filepath.Join(dir, "server.pem"), filepath.Join(dir, "server-key.pem")
	if served, err := os.ReadFile(certPath); err == nil {
		ex.served = served
	}
	for _, f := range []struct {
		name string
		data []byte
		mode os.FileMode
	}{{"ca.pem", []byte(st.bundle()), edgePublicMode}, {"edge.conf", []byte(edgeConf(sec)), edgeSecretMode}} {
		path := filepath.Join(dir, f.name)
		if old, err := os.ReadFile(path); err != nil || !bytes.Equal(old, f.data) {
			ex.changed = true
		}
		if err := writeFileAtomic(path, f.data, f.mode); err != nil {
			return nil, err
		}
	}
	if keepServerPair(certPath, keyPath, st.edge.cert, now) {
		// A key written by an earlier version may be readable by others.
		return ex, narrowMode(keyPath, edgeSecretMode)
	}
	certPEM, keyPEM, err := issueWithKey(st.edge, "reticora-collector-nats", hosts, EdgeServerCertValidity, x509.ExtKeyUsageServerAuth, issueAt)
	if err != nil {
		return nil, err
	}
	ex.changed = true
	return ex, writeServerPair(certPath, []byte(certPEM), keyPath, []byte(keyPEM))
}

// withEdgePKILock runs export under the advisory lock of the export
// (edgePKILockClass). The lock lives in a transaction of its own (operator
// context: export reads collector_ca on it), which holds one connection of
// pool for as long as export runs: its end releases lock and connection on
// every path, an error and a panic included.
func withEdgePKILock(ctx context.Context, pool *pgxpool.Pool, export func(ctx context.Context, tx pgx.Tx) error) error {
	return database.WithOperator(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		if err := lockEdgePKI(ctx, tx); err != nil {
			return err
		}
		return export(ctx, tx)
	})
}

// lockEdgePKI takes the lock of the export within tx, waiting at most
// edgePKILockWait.
func lockEdgePKI(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout', $1, true)`, fmt.Sprintf("%dms", edgePKILockWait.Milliseconds())); err != nil {
		return fmt.Errorf("edge PKI lock: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1, 0)`, edgePKILockClass); err != nil {
		return fmt.Errorf("edge PKI lock: %w", err)
	}
	return nil
}

// applyEdgeChange writes the endpoint's files for the CA state after a
// change within the change's transaction tx, under the lock of the export
// (the exports of the replicas wait for the commit and then read the new
// state), and has the endpoint reload them: the change commits only once
// the endpoint loaded them (COL-04).
func applyEdgeChange(ctx context.Context, tx pgx.Tx, edge *EdgeConfig, st *caState, now time.Time) error {
	if err := lockEdgePKI(ctx, tx); err != nil {
		return err
	}
	ex, err := writeEdgePKI(st, edge.Secrets, edge.PKIDir, edge.Hosts, now, now)
	if err != nil {
		return err
	}
	if err = reloadEdge(ctx, edge.URL, edge.Secrets.SysPassword, st, ex.served, now); err != nil {
		return fmt.Errorf("reload the collector NATS endpoint: %w", err)
	}
	return nil
}

// restoreEdge exports the endpoint's files again from the committed CA
// state after a change that wrote them did not commit, and has a running
// endpoint reload them.
func restoreEdge(ctx context.Context, ca *CA, edge *EdgeConfig) error {
	now := ca.now()
	ex, st, err := exportEdgePKI(ctx, ca, edge.Secrets, edge.PKIDir, edge.Hosts, now)
	if err != nil {
		return fmt.Errorf("restore the files of the collector NATS endpoint: %w", err)
	}
	if ex.changed && ex.served != nil {
		if err = reloadEdge(ctx, edge.URL, edge.Secrets.SysPassword, st, ex.served, now); err != nil {
			return fmt.Errorf("reload the restored files of the collector NATS endpoint (restart nats-edge): %w", err)
		}
	}
	return nil
}

// keepServerPair reports whether the server key pair in the files stays: the
// key matches the certificate (tls.X509KeyPair), the certificate was issued
// by issuer and is not due for renewal at now.
func keepServerPair(certPath, keyPath string, issuer *x509.Certificate, now time.Time) bool {
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil || issuer == nil {
		return false
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	return err == nil && cert.CheckSignatureFrom(issuer) == nil && !NeedsRenewal(cert.NotBefore, cert.NotAfter, now)
}

// narrowMode removes the permissions beyond mode from the file at path.
func narrowMode(path string, mode os.FileMode) error {
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if perm := st.Mode().Perm(); perm&^mode != 0 {
		if err = os.Chmod(path, perm&mode); err != nil {
			return fmt.Errorf("chmod %s: %w", path, err)
		}
	}
	return nil
}

// writeServerPair replaces the server key pair: key and certificate are
// written to temporary files, checked as a pair (tls.X509KeyPair) and only
// then renamed, the key first. An export interrupted between the two renames
// leaves a pair that does not match; the next export replaces it.
func writeServerPair(certPath string, certPEM []byte, keyPath string, keyPEM []byte) (err error) {
	keyTmp, err := writeTemp(keyPath, keyPEM, edgeSecretMode)
	if err != nil {
		return err
	}
	certTmp, err := writeTemp(certPath, certPEM, edgePublicMode)
	if err != nil {
		_ = os.Remove(keyTmp)
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(keyTmp)
			_ = os.Remove(certTmp)
		}
	}()
	if _, err = tls.LoadX509KeyPair(certTmp, keyTmp); err != nil {
		return fmt.Errorf("edge server key pair: %w", err)
	}
	if err = os.Rename(keyTmp, keyPath); err != nil {
		return fmt.Errorf("write %s: %w", keyPath, err)
	}
	if err = os.Rename(certTmp, certPath); err != nil {
		return fmt.Errorf("write %s: %w", certPath, err)
	}
	return nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := writeTemp(path, data, mode)
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// writeTemp writes data to a new temporary file with a unique name next to
// path, with mode whatever the umask, and returns its name; the caller
// renames or removes it.
func writeTemp(path string, data []byte, mode os.FileMode) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	err = f.Chmod(mode)
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return f.Name(), nil
}

// Requests of the endpoint's system account to reload the configuration:
// every server answers the ping, then each one reloads (nats-server 2.10+).
const (
	edgePingSubject   = "$SYS.REQ.SERVER.PING"
	edgeReloadSubject = "$SYS.REQ.SERVER.%s.RELOAD"
)

// reloadEdge has every server of the endpoint at url reload its
// configuration and files ($SYS.REQ.SERVER.<id>.RELOAD as the edge worker's
// system login, WP-381) and checks that it loaded those of the CA state st:
// afterwards it admits a client certificate of the active CA and serves a
// server certificate of the trust bundle. Until it reloads, the endpoint
// serves served, the server certificate before the export; the reload
// connection accepts exactly that certificate besides the trust bundle,
// since after a retirement its CA is no longer trusted. Its client
// certificate is one of the edge issuer, which the endpoint trusts before
// and after the change.
func reloadEdge(ctx context.Context, url, sysPassword string, st *caState, served []byte, now time.Time) error {
	tlsConf, err := edgeClientTLSFor(st, st.edge, EdgeSysUser, now)
	if err != nil {
		return err
	}
	if served != nil {
		if cert, perr := parsePEMCert(string(served)); perr == nil {
			tlsConf.RootCAs.AddCert(cert)
		}
	}
	nc, err := nats.Connect(url, nats.Name(EdgeSysUser), nats.UserInfo(EdgeSysUser, sysPassword), nats.Secure(tlsConf),
		nats.TLSHandshakeFirst(), nats.Timeout(edgeSysTimeout), nats.NoReconnect())
	if err != nil {
		return fmt.Errorf("connect %s as %s: %w", url, EdgeSysUser, err)
	}
	defer nc.Close()
	servers, err := sysRequestAll(nc, edgePingSubject, nil)
	if err != nil {
		return err
	}
	for _, s := range servers {
		msg, rerr := nc.Request(fmt.Sprintf(edgeReloadSubject, s.Server.ID), nil, edgeReloadTimeout)
		if rerr != nil {
			return fmt.Errorf("reload server %s: %w", s.Server.ID, rerr)
		}
		var answer edgeServerAnswer
		if jerr := json.Unmarshal(msg.Data, &answer); jerr != nil {
			return fmt.Errorf("reload server %s: %w", s.Server.ID, jerr)
		}
		if answer.Error != nil {
			return fmt.Errorf("reload server %s: %s", s.Server.ID, answer.Error.Description)
		}
	}
	probe, err := edgeClientTLSFor(st, st.active, EdgeSysUser, now)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	pc, err := nats.Connect(url, nats.Name(EdgeSysUser), nats.UserInfo(EdgeSysUser, sysPassword), nats.Secure(probe),
		nats.TLSHandshakeFirst(), nats.Timeout(edgeSysTimeout), nats.NoReconnect())
	if err != nil {
		return fmt.Errorf("the endpoint did not load the files of the collector CA: %w", err)
	}
	pc.Close()
	return nil
}

// edgeReloadTimeout bounds the reload of one server.
const edgeReloadTimeout = 10 * time.Second

// EdgeConfig configures the collector NATS endpoint worker.
type EdgeConfig struct {
	Pool    *pgxpool.Pool
	CA      *CA
	Secrets *EdgeSecrets
	// PKIDir receives ca.pem, server.pem, server-key.pem and edge.conf;
	// empty writes nothing.
	PKIDir string
	Hosts  []string
	// URL is where the authorization callout connects; empty serves none.
	URL string
	// Bus is the internal NATS: the worker receives the disables of
	// collectors on it (CollectorDisabledSubject) and cuts their open
	// connections at URL; nil cuts none, the user JWTs still expire. It also
	// receives the changes of the collector CA (CollectorCAChangedSubject).
	Bus *nats.Conn
	// UserValidity is the lifetime of the user JWTs of the callout; 0 is
	// EdgeUserValidity (tests shorten it).
	UserValidity time.Duration
	// Cycles are the cycles of the worker nats_edge (OPS-05): RunEdge
	// counts the failed exports, sweeps, callout answers and cuts into
	// them and adds the checks of the connections of the callout and the
	// cut; nil records none.
	Cycles *observability.Cycles
}

// supersededSweep is how often RunEdge revokes superseded certificates.
const supersededSweep = time.Hour

// The checks of the services of RunEdge in its cycles: the connection of
// each must answer a round trip, and a service that ended fails every
// cycle.
const (
	edgeCalloutCheck = "authorization callout"
	edgeKickCheck    = "cut of disabled collectors"
)

// RunEdge keeps the endpoint's files current (daily and on every change of
// the collector CA, CollectorCAChangedSubject) and has a running endpoint
// reload changed ones, serves its authorization callout, cuts the open
// connections of collectors that are disabled and revokes the certificates
// superseded by a renewal at the latest SupersededGrace after it (hourly)
// until ctx ends.
func RunEdge(ctx context.Context, c *EdgeConfig) {
	export := func() {
		if c.PKIDir == "" {
			return
		}
		now := time.Now().UTC()
		ex, st, err := exportEdgePKI(ctx, c.CA, c.Secrets, c.PKIDir, c.Hosts, now)
		if err != nil {
			c.Cycles.Fail()
			slog.Error("export collector NATS PKI", "dir", c.PKIDir, "error", err)
			return
		}
		// The endpoint reads its files only at its start and on reload: a
		// running one (a server certificate was there) reloads changed files,
		// a renewed server certificate or the trust anchors of a change of
		// the CA.
		if !ex.changed || ex.served == nil || c.URL == "" {
			return
		}
		if rerr := reloadEdge(ctx, c.URL, c.Secrets.SysPassword, st, ex.served, now); rerr != nil {
			slog.Warn("reload of the collector NATS endpoint failed; it loads its files at its next start", "event", "discovery.edge.reload_failed",
				"url", c.URL, "error", rerr)
			return
		}
		slog.Info("collector NATS endpoint reloaded its files", "event", "discovery.edge.reloaded", "url", c.URL)
	}
	sweep := func() {
		if c.Pool == nil {
			return
		}
		if n, err := RevokeSupersededCertificates(ctx, c.Pool, SupersededGrace-supersededSweep); err != nil {
			c.Cycles.Fail()
			slog.Error("revoke superseded collector certificates", "error", err)
		} else if n > 0 {
			slog.Info("revoked superseded collector certificates", "revoked", n)
		}
	}
	export()
	sweep()
	// Announcements of a change of the CA arrive in the subscription's
	// goroutine; one pending reload covers any number of them.
	caChanged := make(chan struct{}, 1)
	if c.Bus != nil {
		go func() {
			if err := watchCAChanges(ctx, c.Bus, c.CA, func() {
				select {
				case caChanged <- struct{}{}:
				default:
				}
			}); err != nil {
				slog.Error("collector CA changes", "error", err)
			}
		}()
	}
	if c.URL != "" {
		go func() {
			if err := serveEdgeAuth(ctx, c); err != nil {
				c.Cycles.Stopped(edgeCalloutCheck, err)
				slog.Error("collector NATS authorization callout", "error", err)
			}
		}()
		if c.Bus != nil {
			go func() {
				if err := serveEdgeKick(ctx, c); err != nil {
					c.Cycles.Stopped(edgeKickCheck, err)
					slog.Error("cut of disabled collectors at the NATS endpoint", "error", err)
				}
			}()
		}
	}
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	sweeps := time.NewTicker(supersededSweep)
	defer sweeps.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			export()
		case <-caChanged:
			// The CA state is read anew (version check), the files follow it.
			if _, err := c.CA.state(ctx); err != nil {
				slog.Error("reload the collector CA", "error", err)
			}
			export()
		case <-sweeps.C:
			sweep()
		}
	}
}

func serveEdgeAuth(ctx context.Context, c *EdgeConfig) error {
	svc, err := NewEdgeAuthService(NewEdgeAuthorizer(c.Pool, c.CA), c.Secrets.IssuerSeed)
	if err != nil {
		return err
	}
	if c.UserValidity > 0 {
		svc.validity = c.UserValidity
	}
	svc.cycles = c.Cycles
	// Every (re)connect presents a client certificate of the CA state current
	// then and verifies the endpoint with its trust bundle: after a
	// retirement the former ones no longer fit the endpoint (COL-04).
	clientCert := func() (tls.Certificate, error) {
		conf, cerr := edgeClientTLS(ctx, c.CA, EdgeAuthUser)
		if cerr != nil {
			return tls.Certificate{}, cerr
		}
		return conf.Certificates[0], nil
	}
	roots := func() (*x509.CertPool, error) { return c.CA.TrustPool(ctx) }
	nc, err := nats.Connect(c.URL, nats.Name(EdgeAuthUser), nats.UserInfo(EdgeAuthUser, c.Secrets.Password),
		nats.ClientTLSConfig(clientCert, roots), nats.TLSHandshakeFirst(), nats.RetryOnFailedConnect(true), nats.MaxReconnects(-1))
	if err != nil {
		return fmt.Errorf("connect %s: %w", c.URL, err)
	}
	defer nc.Close()
	// While the endpoint does not answer, no collector is let in.
	c.Cycles.Check(edgeCalloutCheck, nc.FlushWithContext)
	return svc.Run(ctx, nc)
}

// edgeClientTLS is the TLS configuration of a Reticora service at the
// endpoint for one connection: an internal client certificate (CN = name)
// of the edge issuer, which the endpoint trusts before and after a change
// of the CA, and the trust bundle as roots.
func edgeClientTLS(ctx context.Context, ca *CA, name string) (*tls.Config, error) {
	st, err := ca.state(ctx)
	if err != nil {
		return nil, err
	}
	return edgeClientTLSFor(st, st.edge, name, ca.now())
}

// edgeClientTLSFor is edgeClientTLS for the CA state st with a client
// certificate of issuer.
func edgeClientTLSFor(st *caState, issuer *caCert, name string, now time.Time) (*tls.Config, error) {
	certPEM, keyPEM, err := issueWithKey(issuer, name, nil, EdgeServerCertValidity, x509.ExtKeyUsageClientAuth, now)
	if err != nil {
		return nil, err
	}
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(st.bundle()))
	return &tls.Config{Certificates: []tls.Certificate{pair}, RootCAs: roots, MinVersion: tls.VersionTLS12}, nil
}

// CollectorCAChangedSubject is the subject of the internal NATS on which a
// change of collector_ca (Rollover, RetirePrevious) is announced
// (AnnounceCAChange): every replica reads its CA state anew, its edge worker
// exports the endpoint's files and has the endpoint reload changed ones
// (COL-04). The internal NATS authenticates nobody and the collectors may
// publish only below their own subject tree: the announcement carries no
// state, only the version of collector_ca, a timestamp and a MAC with a key
// derived from the derivation root; a receiver checks MAC and age and reads
// collector_ca itself.
const CollectorCAChangedSubject = "discovery.collector_ca.changed"

// caChangeMaxAge bounds the age of an announcement (replay).
const caChangeMaxAge = 5 * time.Minute

// caChangeAnnouncement is the message on CollectorCAChangedSubject.
type caChangeAnnouncement struct {
	Version string `json:"version"`
	At      string `json:"at"`
	MAC     string `json:"mac"`
}

// caChangeMAC authenticates an announcement of version at the time at.
func (ca *CA) caChangeMAC(version, at string) []byte {
	m := hmac.New(sha256.New, ca.enc.Derive("reticora/collector-ca/changed"))
	m.Write([]byte("reticora-collector-ca-changed\n" + version + "\n" + at))
	return m.Sum(nil)
}

// AnnounceCAChange announces on the internal NATS that collector_ca changed
// (ch committed).
func (ca *CA) AnnounceCAChange(nc *nats.Conn, ch *CAChange) error {
	data, err := ca.caChangeMessage(ch.version, time.Now())
	if err != nil {
		return err
	}
	if perr := nc.Publish(CollectorCAChangedSubject, data); perr != nil {
		return perr
	}
	return nc.Flush()
}

// caChangeMessage is the announcement of version at the time at.
func (ca *CA) caChangeMessage(version string, at time.Time) ([]byte, error) {
	ts := at.UTC().Format(time.RFC3339Nano)
	return json.Marshal(caChangeAnnouncement{Version: version, At: ts, MAC: hex.EncodeToString(ca.caChangeMAC(version, ts))})
}

// verifyCAChange checks an announcement: its MAC and an age of at most
// caChangeMaxAge at now.
func (ca *CA) verifyCAChange(data []byte, now time.Time) error {
	var a caChangeAnnouncement
	if err := json.Unmarshal(data, &a); err != nil {
		return fmt.Errorf("malformed announcement: %w", err)
	}
	mac, err := hex.DecodeString(a.MAC)
	if err != nil || !hmac.Equal(mac, ca.caChangeMAC(a.Version, a.At)) {
		return errors.New("the announcement is not authentic")
	}
	at, err := time.Parse(time.RFC3339Nano, a.At)
	if err != nil || now.Sub(at).Abs() > caChangeMaxAge {
		return fmt.Errorf("the announcement of %q is outside %s", a.At, caChangeMaxAge)
	}
	return nil
}

// watchCAChanges calls changed for every authentic announcement on
// CollectorCAChangedSubject (every replica, no queue group) until ctx ends.
func watchCAChanges(ctx context.Context, nc *nats.Conn, ca *CA, changed func()) error {
	sub, err := nc.Subscribe(CollectorCAChangedSubject, func(msg *nats.Msg) {
		if verr := ca.verifyCAChange(msg.Data, time.Now()); verr != nil {
			slog.Warn("collector CA change announcement ignored", "subject", msg.Subject, "error", verr)
			return
		}
		changed()
	})
	if err != nil {
		return fmt.Errorf("subscribe %s: %w", CollectorCAChangedSubject, err)
	}
	<-ctx.Done()
	return sub.Unsubscribe()
}

// CollectorDisabledSubject is the subject of the internal NATS on which the
// disable of a collector is announced (AnnounceDisabled), by whichever
// replica handled it: an edge worker cuts the collector's open connections
// at the endpoint (COL-04).
const CollectorDisabledSubject = "discovery.collector.disabled"

// edgeKickQueue is the queue group of the edge workers: one of them cuts
// per announcement.
const edgeKickQueue = "reticora-edge-kick"

// Requests of the endpoint's system account: the connections of every
// server, filtered, and the close of one connection of one server.
const (
	edgeConnzSubject = "$SYS.REQ.SERVER.PING.CONNZ"
	edgeKickSubject  = "$SYS.REQ.SERVER.%s.KICK"
)

// edgeSysTimeout bounds the connect and the first answer of a request of
// the system account; further servers answer within edgeSysMoreAnswers.
const (
	edgeSysTimeout     = 3 * time.Second
	edgeSysMoreAnswers = 250 * time.Millisecond
)

// edgeKickRetry is the wait after the first failed cut; it doubles up to
// edgeKickRetryMax.
const (
	edgeKickRetry    = time.Second
	edgeKickRetryMax = 30 * time.Second
)

// collectorDisabledEvent is the message on CollectorDisabledSubject.
type collectorDisabledEvent struct {
	OrganizationID string `json:"organization_id"`
	CollectorID    string `json:"collector_id"`
}

// AnnounceDisabled announces on the internal NATS that a collector was
// disabled; called once the disable is committed.
func AnnounceDisabled(nc *nats.Conn, orgID, collectorID string) error {
	data, err := json.Marshal(collectorDisabledEvent{OrganizationID: orgID, CollectorID: collectorID})
	if err != nil {
		return err
	}
	return nc.Publish(CollectorDisabledSubject, data)
}

// edgeKicker cuts the connections of disabled collectors: it logs in to the
// endpoint's system account (EdgeSysUser, internal client certificate),
// looks the connections up by account and user (the callout names the user
// after the collector) and closes them. The collector connects again and
// the callout refuses it.
type edgeKicker struct {
	url        string
	password   string
	ca         *CA
	authorizer *EdgeAuthorizer
	// validity is the lifetime of the user JWTs: after it every connection
	// opened before the disable has passed the callout again.
	validity time.Duration
	// cycles counts the failed cuts (OPS-05); nil counts none.
	cycles *observability.Cycles
}

// serveEdgeKick cuts the connections of every collector announced on
// CollectorDisabledSubject until ctx ends.
func serveEdgeKick(ctx context.Context, c *EdgeConfig) error {
	k := &edgeKicker{url: c.URL, password: c.Secrets.SysPassword, ca: c.CA, authorizer: NewEdgeAuthorizer(c.Pool, c.CA),
		validity: EdgeUserValidity, cycles: c.Cycles}
	if c.UserValidity > 0 {
		k.validity = c.UserValidity
	}
	sub, err := c.Bus.QueueSubscribe(CollectorDisabledSubject, edgeKickQueue, func(msg *nats.Msg) {
		var ev collectorDisabledEvent
		if jerr := json.Unmarshal(msg.Data, &ev); jerr != nil || !audit.IsUUID(ev.OrganizationID) || !audit.IsUUID(ev.CollectorID) {
			slog.Warn("malformed collector disable announcement", "subject", msg.Subject)
			return
		}
		go k.cutRetrying(ctx, ev)
	})
	if err != nil {
		return fmt.Errorf("subscribe %s: %w", CollectorDisabledSubject, err)
	}
	c.Cycles.Check(edgeKickCheck, c.Bus.FlushWithContext)
	<-ctx.Done()
	return sub.Unsubscribe()
}

// cutRetrying cuts the collector's connections; a failure is logged as
// discovery.collector.kick_failed and repeated until the user JWTs issued
// before the disable have expired (their connections are closed then).
func (k *edgeKicker) cutRetrying(ctx context.Context, ev collectorDisabledEvent) {
	giveUp := time.Now().Add(k.validity)
	wait := edgeKickRetry
	for {
		n, err := k.cut(ctx, ev)
		if err == nil {
			if n > 0 {
				slog.Info("cut the NATS connections of a disabled collector", "event", "discovery.collector.kicked",
					"organization_id", ev.OrganizationID, "collector_id", ev.CollectorID, "connections", n)
			}
			return
		}
		k.cycles.Fail()
		retry := time.Now().Add(wait).Before(giveUp)
		slog.Warn("cut of the NATS connections of a disabled collector failed", "event", "discovery.collector.kick_failed",
			"organization_id", ev.OrganizationID, "collector_id", ev.CollectorID, "retry", retry, "error", err)
		if !retry {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(2*wait, edgeKickRetryMax)
	}
}

// edgeServerAnswer is the answer of one server to a request of the system
// account.
type edgeServerAnswer struct {
	Server struct {
		ID string `json:"id"`
	} `json:"server"`
	Data struct {
		Connections []struct {
			CID     uint64 `json:"cid"`
			Account string `json:"account"`
			User    string `json:"authorized_user"`
		} `json:"connections"`
	} `json:"data"`
	Error *struct {
		Description string `json:"description"`
	} `json:"error"`
}

// cut closes the open connections of the collector, if it is disabled
// (the announcement alone is no proof), and returns their number. It looks
// again after the kicks: a connection the callout admitted just before the
// disable, or one beyond a server's CONNZ page, is cut in the next round.
func (k *edgeKicker) cut(ctx context.Context, ev collectorDisabledEvent) (int, error) {
	status, _, err := k.authorizer.collector(ctx, ev.OrganizationID, ev.CollectorID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && status != CollectorDisabled) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("collector status: %w", err)
	}
	tlsConf, err := edgeClientTLS(ctx, k.ca, EdgeSysUser)
	if err != nil {
		return 0, err
	}
	nc, err := nats.Connect(k.url, nats.Name(EdgeSysUser), nats.UserInfo(EdgeSysUser, k.password), nats.Secure(tlsConf),
		nats.TLSHandshakeFirst(), nats.Timeout(edgeSysTimeout), nats.NoReconnect())
	if err != nil {
		return 0, fmt.Errorf("connect %s as %s: %w", k.url, EdgeSysUser, err)
	}
	defer nc.Close()
	cut := 0
	for range edgeKickRounds {
		n, kerr := kickConnections(nc, ev.CollectorID)
		cut += n
		if kerr != nil || n == 0 {
			return cut, kerr
		}
	}
	return cut, fmt.Errorf("connections of the collector remain after %d rounds", edgeKickRounds)
}

// edgeKickRounds bounds the rounds of one cut.
const edgeKickRounds = 3

// kickConnections looks up the open connections of the collector on every
// server of the endpoint (CONNZ, account COLLECTORS, user = collector id)
// and closes them (KICK); it returns their number.
func kickConnections(nc *nats.Conn, collectorID string) (int, error) {
	query, err := json.Marshal(map[string]any{"acc": EdgeAccount, "user": collectorID, "auth": true})
	if err != nil {
		return 0, err
	}
	answers, err := sysRequestAll(nc, edgeConnzSubject, query)
	if err != nil {
		return 0, err
	}
	kicked := 0
	for _, a := range answers {
		for _, conn := range a.Data.Connections {
			// The filter is checked again: a server that ignored it must
			// not close the connections of other collectors.
			if conn.Account != EdgeAccount || conn.User != collectorID {
				continue
			}
			kick, merr := json.Marshal(map[string]uint64{"cid": conn.CID})
			if merr != nil {
				return kicked, merr
			}
			msg, rerr := nc.Request(fmt.Sprintf(edgeKickSubject, a.Server.ID), kick, edgeSysTimeout)
			if rerr != nil {
				return kicked, fmt.Errorf("kick connection %d: %w", conn.CID, rerr)
			}
			var answer edgeServerAnswer
			if jerr := json.Unmarshal(msg.Data, &answer); jerr != nil {
				return kicked, fmt.Errorf("kick connection %d: %w", conn.CID, jerr)
			}
			if answer.Error != nil {
				return kicked, fmt.Errorf("kick connection %d: %s", conn.CID, answer.Error.Description)
			}
			kicked++
		}
	}
	return kicked, nil
}

// sysRequestAll sends a request of the system account to every server of
// the endpoint and collects their answers: the first within edgeSysTimeout,
// every further one within edgeSysMoreAnswers of the previous one.
func sysRequestAll(nc *nats.Conn, subject string, data []byte) ([]edgeServerAnswer, error) {
	inbox := nats.NewInbox()
	sub, err := nc.SubscribeSync(inbox)
	if err != nil {
		return nil, err
	}
	defer func() { _ = sub.Unsubscribe() }()
	if perr := nc.PublishRequest(subject, inbox, data); perr != nil {
		return nil, perr
	}
	var answers []edgeServerAnswer
	wait := edgeSysTimeout
	for {
		msg, nerr := sub.NextMsg(wait)
		if errors.Is(nerr, nats.ErrTimeout) && len(answers) > 0 {
			return answers, nil
		}
		if nerr != nil {
			return nil, fmt.Errorf("%s: %w", subject, nerr)
		}
		var a edgeServerAnswer
		if jerr := json.Unmarshal(msg.Data, &a); jerr != nil {
			return nil, fmt.Errorf("%s: %w", subject, jerr)
		}
		if a.Error != nil {
			return nil, fmt.Errorf("%s: %s", subject, a.Error.Description)
		}
		answers = append(answers, a)
		wait = edgeSysMoreAnswers
	}
}

// BundleGate verifies collectors that request their credential bundle and
// records the delivery (COL-02).
type BundleGate struct {
	authorizer *EdgeAuthorizer
}

// NewBundleGate creates the gate on the collector CA.
func NewBundleGate(pool *pgxpool.Pool, ca *CA) *BundleGate {
	return &BundleGate{authorizer: NewEdgeAuthorizer(pool, ca)}
}

// VerifyBundleTarget implements credential.CollectorVerifier: the
// certificate must be the collector's own (subject of the request), issued
// by the collector CA, not revoked, for a collector that is not disabled.
func (g *BundleGate) VerifyBundleTarget(ctx context.Context, orgID, collectorID, certPEM string) (context.Context, *credential.BundleTarget, error) {
	id, err := g.authorizer.Authorize(ctx, []string{certPEM})
	if err != nil {
		return nil, nil, err
	}
	if id.OrganizationID != orgID || id.CollectorID != collectorID {
		return nil, nil, errors.New("the certificate belongs to another collector")
	}
	pub, ok := id.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, nil, errors.New("the collector key is not ECDSA")
	}
	scope := database.OrgWideScope(orgID, "")
	target := &credential.BundleTarget{OrganizationID: orgID, CollectorID: collectorID, ClientID: id.ClientID, PublicKey: pub}
	// The bundle holds the union of the effective credentials of the
	// collector's enabled scopes (DIS-09, COL-02, E-67); without an enabled
	// scope it is empty.
	err = database.WithTenant(ctx, g.authorizer.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		perScope, qerr := scopeCredentials(ctx, tx, orgID, collectorID)
		if qerr != nil {
			return qerr
		}
		ids := []string{}
		for _, scoped := range perScope {
			for _, cid := range scoped {
				if !slices.Contains(ids, cid) {
					ids = append(ids, cid)
				}
			}
		}
		slices.Sort(ids)
		target.CredentialIDs = ids
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return database.ContextWithTenantScope(ctx, &scope), target, nil
}

// BundleDelivered implements credential.BundleAuditor.
func (g *BundleGate) BundleDelivered(ctx context.Context, t *credential.BundleTarget, credentialIDs []string) error {
	scope := database.OrgWideScope(t.OrganizationID, "")
	return database.WithTenant(ctx, g.authorizer.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		ids := credentialIDs
		if ids == nil {
			ids = []string{}
		}
		_, err := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{OrganizationID: t.OrganizationID, ActorID: "",
			ActorType: "system", Action: "credential.bundle_delivered", ResourceType: "collector", ResourceID: t.CollectorID,
			Source: "collector", Changes: map[string]interface{}{"credential_ids": ids}})
		return err
	})
}
