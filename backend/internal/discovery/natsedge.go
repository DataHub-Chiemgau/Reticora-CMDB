package discovery

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
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
	Status         string
	NotAfter       time.Time
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
	revoked, err := a.ca.IsRevoked(ctx, leaf.SerialNumber.Text(16))
	if err != nil {
		return nil, err
	}
	if revoked {
		return nil, ErrCertificateRevoked
	}
	if len(leaf.Subject.Organization) != 1 || !audit.IsUUID(leaf.Subject.CommonName) || !audit.IsUUID(leaf.Subject.Organization[0]) {
		return nil, errors.New("client certificate names no collector")
	}
	id := &EdgeIdentity{OrganizationID: leaf.Subject.Organization[0], CollectorID: leaf.Subject.CommonName, NotAfter: leaf.NotAfter}
	scope := database.OrgWideScope(id.OrganizationID, "")
	err = database.WithTenant(ctx, a.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status FROM collector WHERE id = $1 AND organization_id = $2`,
			id.CollectorID, id.OrganizationID).Scan(&id.Status)
	})
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

// UserClaims are the NATS permissions of an authorized collector: its own
// subject tree and its own reply inbox, in the collectors' account, valid
// at most as long as its certificate.
func (id *EdgeIdentity) UserClaims(userNkey string, now time.Time) *jwt.UserClaims {
	uc := jwt.NewUserClaims(userNkey)
	uc.Name = id.CollectorID
	uc.Audience = EdgeAccount
	tree := CollectorSubject(id.OrganizationID, id.CollectorID) + ".>"
	uc.Pub.Allow.Add(tree)
	uc.Sub.Allow.Add(tree, CollectorInbox(id.CollectorID)+".>")
	expires := now.Add(24 * time.Hour)
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

// EdgeSecrets are the issuer key of the callout and the password of its
// service login, derived from the master key: the server and the endpoint
// configuration it writes agree without a further secret.
type EdgeSecrets struct {
	IssuerSeed string
	IssuerKey  string
	Password   string
}

// DeriveEdgeSecrets derives the endpoint's secrets from the master key.
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
		Password: hex.EncodeToString(enc.Derive("reticora/nats-edge/auth-password"))}, nil
}

// edgeConf is the generated part of nats-edge.conf: TLS with client
// verification against the collector CA, the accounts and the callout.
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
  SYS {}
}
system_account: SYS
authorization {
  auth_callout {
    issuer: "` + sec.IssuerKey + `"
    auth_users: [ "` + EdgeAuthUser + `" ]
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
	return &EdgeAuthService{authorizer: authorizer, issuer: kp, now: func() time.Time { return time.Now().UTC() }}, nil
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
		resp.Jwt, err = id.UserClaims(req.UserNkey, s.now()).Encode(s.issuer)
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
			slog.Error("authorization callout", "error", rerr)
			return
		}
		if rerr = msg.Respond(answer); rerr != nil {
			slog.Error("authorization callout reply", "error", rerr)
		}
	})
	if err != nil {
		return fmt.Errorf("subscribe %s: %w", EdgeAuthSubject, err)
	}
	<-ctx.Done()
	return sub.Unsubscribe()
}

// ExportEdgePKI writes the files of the collector NATS endpoint into dir:
// the CA bundle it verifies client certificates with (ca.pem), its server
// certificate and key for the host names (server.pem, server-key.pem) and
// its TLS, account and callout configuration (edge.conf, included by
// nats-edge.conf). An existing server certificate is kept until 60 % of its
// lifetime passed; the endpoint picks up new files on reload.
func ExportEdgePKI(ctx context.Context, ca *CA, sec *EdgeSecrets, dir string, hosts []string, now time.Time) error {
	bundle, err := ca.TrustBundle(ctx)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("pki dir: %w", err)
	}
	if werr := writeFileAtomic(filepath.Join(dir, "ca.pem"), []byte(bundle), 0o644); werr != nil {
		return werr
	}
	// The volume is shared only by the server and the endpoint; the
	// endpoint container reads the files with its own user.
	if werr := writeFileAtomic(filepath.Join(dir, "edge.conf"), []byte(edgeConf(sec)), 0o644); werr != nil {
		return werr
	}
	certPath, keyPath := filepath.Join(dir, "server.pem"), filepath.Join(dir, "server-key.pem")
	if existing, rerr := os.ReadFile(certPath); rerr == nil {
		if cert, perr := parsePEMCert(string(existing)); perr == nil && !NeedsRenewal(cert.NotBefore, cert.NotAfter, now) &&
			cert.CheckSignatureFrom(ca.cert) == nil {
			return nil
		}
	}
	certPEM, keyPEM, err := ca.IssueServer(ctx, hosts, EdgeServerCertValidity)
	if err != nil {
		return err
	}
	if werr := writeFileAtomic(keyPath, []byte(keyPEM), 0o644); werr != nil {
		return werr
	}
	return writeFileAtomic(certPath, []byte(certPEM), 0o644)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

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
}

// RunEdge keeps the endpoint's files current (daily) and serves its
// authorization callout until ctx ends.
func RunEdge(ctx context.Context, c *EdgeConfig) {
	export := func() {
		if c.PKIDir == "" {
			return
		}
		if err := ExportEdgePKI(ctx, c.CA, c.Secrets, c.PKIDir, c.Hosts, time.Now().UTC()); err != nil {
			slog.Error("export collector NATS PKI", "dir", c.PKIDir, "error", err)
		}
	}
	export()
	if c.URL != "" {
		go func() {
			if err := serveEdgeAuth(ctx, c); err != nil {
				slog.Error("collector NATS authorization callout", "error", err)
			}
		}()
	}
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			export()
		}
	}
}

func serveEdgeAuth(ctx context.Context, c *EdgeConfig) error {
	svc, err := NewEdgeAuthService(NewEdgeAuthorizer(c.Pool, c.CA), c.Secrets.IssuerSeed)
	if err != nil {
		return err
	}
	certPEM, keyPEM, err := c.CA.IssueInternalClient(ctx, "reticora-edge-auth", EdgeServerCertValidity)
	if err != nil {
		return err
	}
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return err
	}
	roots, err := c.CA.TrustPool(ctx)
	if err != nil {
		return err
	}
	nc, err := nats.Connect(c.URL, nats.Name("reticora-edge-auth"), nats.UserInfo(EdgeAuthUser, c.Secrets.Password),
		nats.Secure(&tls.Config{Certificates: []tls.Certificate{pair}, RootCAs: roots, MinVersion: tls.VersionTLS12}),
		nats.TLSHandshakeFirst(), nats.RetryOnFailedConnect(true), nats.MaxReconnects(-1))
	if err != nil {
		return fmt.Errorf("connect %s: %w", c.URL, err)
	}
	defer nc.Close()
	return svc.Run(ctx, nc)
}
