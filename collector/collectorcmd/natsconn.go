package collectorcmd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/keystore"
)

// natsEndpointPort is the collector NATS endpoint of the platform (COL-08):
// the collector connects outbound to it with mutual TLS (PRI-07).
const natsEndpointPort = "4222"

// natsReconnectWait is the pause between two connection attempts (tests
// shorten it).
var natsReconnectWait = 5 * time.Second

// natsEnrollPoll is how often a collector started without enrolled
// credentials looks for its credentials file (tests shorten it).
var natsEnrollPoll = 30 * time.Second

// natsFailureRepeat is how often a connection failure that persists while
// the collector is not connected is logged again, at ERROR (tests shorten
// it).
var natsFailureRepeat = 10 * time.Minute

// natsEndpoint is RETICORA_NATS_URL or, by default, the host of the server
// URL on port 4222 with TLS.
func natsEndpoint(serverURL string) string {
	if v := os.Getenv("RETICORA_NATS_URL"); v != "" {
		return v
	}
	u, err := url.Parse(serverURL)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return "tls://" + net.JoinHostPort(u.Hostname(), natsEndpointPort)
}

// collectorSubject is the subject tree the endpoint grants the collector.
func collectorSubject(creds *keystore.Credentials) (string, error) {
	block, err := leafCertificate(creds.ClientCertificatePEM)
	if err != nil {
		return "", err
	}
	if len(block.Subject.Organization) != 1 {
		return "", errors.New("collector certificate names no organization")
	}
	return "collector." + block.Subject.Organization[0] + "." + block.Subject.CommonName, nil
}

func leafCertificate(certPEM string) (*x509.Certificate, error) {
	rest := []byte(certPEM)
	for {
		var block *pem.Block
		if block, rest = pem.Decode(rest); block == nil {
			return nil, errors.New("no collector certificate")
		}
		if block.Type == "CERTIFICATE" {
			return x509.ParseCertificate(block.Bytes)
		}
	}
}

// natsOptions are the connection options of the collector: its client
// certificate and key from the keystore, the collector CA of the
// enrollment as trust anchor with the name rule of edgeTLSConfig, the TLS
// handshake first and its own reply inbox (other collectors cannot read its
// replies). endpoint is the URL natsEndpoint returned, serverName the
// override RETICORA_NATS_TLS_SERVER_NAME (empty: none). The first
// connection is retried by connectNATS, not by nats.go
// (RetryOnFailedConnect): before its first connection nats.go reports no
// refusal (F-258).
func natsOptions(creds *keystore.Credentials, endpoint, serverName string) ([]nats.Option, error) {
	pair, err := tls.X509KeyPair([]byte(creds.ClientCertificatePEM), []byte(creds.ClientPrivateKeyPEM))
	if err != nil {
		return nil, fmt.Errorf("collector certificate: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(creds.CertificateAuthority)) {
		return nil, errors.New("collector credentials carry no CA bundle")
	}
	return []nats.Option{
		nats.Name("reticora-collector-" + creds.DeviceID),
		nats.Secure(edgeTLSConfig(pair, roots, endpoint, serverName)),
		nats.TLSHandshakeFirst(),
		nats.CustomInboxPrefix("_INBOX." + creds.DeviceID),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(natsReconnectWait),
		// The user JWTs of the auth callout expire after 15 min (WP-381), so
		// the collector authenticates anew every 15 min. On the same
		// authentication error twice (callout briefly unreachable) nats.go
		// would close the connection for good; a disabled collector is just
		// refused by the callout every ReconnectWait, which is cheap.
		nats.IgnoreAuthErrorAbort(),
	}, nil
}

// The names an edge server certificate may be valid for besides the host
// the collector dials (contract C1): the fixed name every nats-edge
// certificate carries (its CN, backend discovery.writeEdgePKI), and
// "nats-edge", which every certificate of the compose stack carries, so an
// updated collector also works with a server whose certificate predates the
// fixed name.
const (
	edgeFixedServerName   = "reticora-collector-nats"
	edgeComposeServerName = "nats-edge"
)

// edgeTLSConfig is the TLS configuration of the collector's edge
// connection. Go's own verification binds the server certificate to the
// host the collector dials: an installation reached by an IP address, an
// internal name, a NAT address or a DNS alias that the edge certificate
// does not list then fails for good although the server is genuine. The
// configuration therefore replaces Go's check with verifyEdgeServer, which
// verifies the full chain itself and accepts the certificate when it is
// valid for the dialled host, the fixed edge name or "nats-edge"; with
// serverName (RETICORA_NATS_TLS_SERVER_NAME) for exactly that name.
//
// Why the server stays authenticated: the only trust anchor is the private
// collector CA bundle the collector received at enrollment, never the
// system roots. That CA issues server authentication only to the
// platform's own edge endpoint: CA.IssueServer and the edge export
// (backend/internal/discovery/ca.go, natsedge.go writeEdgePKI) issue the
// nats-edge certificate with ExtKeyUsageServerAuth and the CN
// reticora-collector-nats, while the collector certificates (CA.Sign) and
// the internal service certificates (CA.IssueInternalClient) carry
// ExtKeyUsageClientAuth only. A certificate that chains to this CA and
// names ServerAuth is therefore the platform's edge certificate whichever
// host the collector dialled, and no collector certificate can act as the
// server. The chain, the validity period and the usage are verified on
// every handshake (VerifyConnection runs for every connection, resumptions
// included); InsecureSkipVerify only switches off Go's host binding, which
// verifyEdgeServer replaces with the names above.
func edgeTLSConfig(pair tls.Certificate, roots *x509.CertPool, endpoint, serverName string) *tls.Config {
	hosts := endpointHosts(endpoint)
	return &tls.Config{
		Certificates: []tls.Certificate{pair},
		RootCAs:      roots,
		MinVersion:   tls.VersionTLS12,
		// With the override nats.go keeps the name as SNI; otherwise it sets
		// the dialled host.
		ServerName:         serverName,
		InsecureSkipVerify: true, //nolint:gosec // verifyEdgeServer verifies the full chain against the collector CA (see above)
		VerifyConnection: func(cs tls.ConnectionState) error {
			return verifyEdgeServer(cs.PeerCertificates, roots, edgeServerNames(cs.ServerName, hosts, serverName), time.Now())
		},
	}
}

// endpointHosts returns the hosts of the endpoint URLs (RETICORA_NATS_URL
// may list several, separated by commas).
func endpointHosts(endpoint string) []string {
	var hosts []string
	for _, raw := range strings.Split(endpoint, ",") {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err == nil && u.Hostname() != "" && !slices.Contains(hosts, u.Hostname()) {
			hosts = append(hosts, u.Hostname())
		}
	}
	return hosts
}

// edgeServerNames are the names of which the edge certificate must name
// one: the override alone, otherwise the dialled host (the TLS server name
// nats.go set; empty for an IP address, which the endpoint hosts name), the
// hosts of the endpoint and the fixed edge names.
func edgeServerNames(dialled string, hosts []string, override string) []string {
	if override != "" {
		return []string{override}
	}
	candidates := make([]string, 0, len(hosts)+3)
	candidates = append(candidates, dialled)
	candidates = append(candidates, hosts...)
	candidates = append(candidates, edgeFixedServerName, edgeComposeServerName)
	names := make([]string, 0, len(candidates))
	for _, n := range candidates {
		if n != "" && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	return names
}

// verifyEdgeServer verifies the edge server's certificate chain against the
// collector CA bundle roots: valid at now, issued for server authentication
// (named explicitly in the leaf, not just allowed by a missing extension)
// and valid for one of names. A name mismatch wraps the x509.HostnameError
// of the first name, the dialled host or the override.
func verifyEdgeServer(certs []*x509.Certificate, roots *x509.CertPool, names []string, now time.Time) error {
	if len(certs) == 0 {
		return errors.New("the edge server presented no certificate")
	}
	leaf := certs[0]
	intermediates := x509.NewCertPool()
	for _, c := range certs[1:] {
		intermediates.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, CurrentTime: now,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return &tls.CertificateVerificationError{UnverifiedCertificates: certs, Err: err}
	}
	if !slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageServerAuth) {
		return &tls.CertificateVerificationError{UnverifiedCertificates: certs, Err: x509.CertificateInvalidError{
			Cert: leaf, Reason: x509.IncompatibleUsage, Detail: "the edge certificate is not issued for server authentication"}}
	}
	var first error
	for _, name := range names {
		err := leaf.VerifyHostname(name)
		if err == nil {
			return nil
		}
		if first == nil {
			first = err
		}
	}
	if first == nil {
		return errors.New("no server name to verify the edge certificate for")
	}
	return fmt.Errorf("the edge certificate is valid for none of %s: %w", strings.Join(names, ", "), first)
}

// runNATS keeps the collector's mTLS connection to the NATS endpoint open
// while ctx lasts. A collector started before its enrollment waits for its
// credentials file (waitForEnrollment); once it appears, the enrolled
// identity is applied and handed to onEnrolled (nil: none), so the uploader
// and the heartbeat use it without a restart. After a certificate renewal
// runNATS closes the session and starts a new one with the renewed
// credentials (COL-04).
func runNATS(ctx context.Context, cfg collectorConfig, onEnrolled func(collectorConfig)) {
	_, waited := waitForEnrollment(ctx, cfg.CredentialsPath)
	if ctx.Err() != nil {
		return
	}
	if waited {
		// The identity loadCollectorConfig could not apply at the start.
		if err := applyEnrolledIdentity(ctx, &cfg, cfg.ServerURLFromEnv); err != nil {
			slog.Warn("enrolled identity not applied", "path", cfg.CredentialsPath, "error", err)
		}
		slog.Info("collector enrolled; the NATS endpoint is used now", "event", "collector.enroll.detected",
			"credentials_path", cfg.CredentialsPath, "nats_url", natsEndpoint(cfg.ServerURL))
		if onEnrolled != nil {
			onEnrolled(cfg)
		}
	}
	for runNATSSession(ctx, cfg) && ctx.Err() == nil {
		slog.Info("collector NATS session restarts with the renewed certificate")
	}
}

// waitForEnrollment loads the credentials at path. While there are none
// (the service was started before `reticora-collector enroll`) or they
// cannot be read, it looks again every natsEnrollPoll and logs each
// distinct cause once, until they load or ctx ends (nil). waited reports
// whether they appeared only after the first attempt.
func waitForEnrollment(ctx context.Context, path string) (creds *keystore.Credentials, waited bool) {
	store := &keystore.FileStore{Path: path}
	logged := ""
	for {
		c, err := store.Load(ctx)
		if err == nil {
			return c, waited
		}
		if ctx.Err() != nil {
			return nil, waited
		}
		if cause := err.Error(); cause != logged {
			logged = cause
			if errors.Is(err, keystore.ErrNotFound) {
				slog.Warn("collector not enrolled: nothing is delivered until it is; the credentials file is checked every "+natsEnrollPoll.String(),
					"event", "collector.enroll.pending", "credentials_path", path,
					"hint", "run the enroll command of the Discovery page: reticora-collector enroll --server <url> --code <code>")
			} else {
				slog.Error("collector credentials unreadable; the NATS endpoint is not used", "event", "collector.enroll.pending",
					"credentials_path", path, "error", err)
			}
		}
		waited = true
		select {
		case <-ctx.Done():
			return nil, waited
		case <-time.After(natsEnrollPoll):
		}
	}
}

// runNATSSession runs one session with the credentials in the keystore:
// the TLS configuration, the scope channel (edge upload), the scopes, the
// scope suggestions, the credential bundle and the renewal check. It
// reports whether the certificate was renewed; the session is then over.
func runNATSSession(ctx context.Context, cfg collectorConfig) (renewed bool) {
	endpoint := natsEndpoint(cfg.ServerURL)
	creds, err := (&keystore.FileStore{Path: cfg.CredentialsPath}).Load(ctx)
	if err != nil || endpoint == "" {
		slog.Error("collector NATS endpoint not used", "reason", "no credentials or no endpoint (RETICORA_SERVER_URL, RETICORA_NATS_URL)",
			"server_url", cfg.ServerURL, "error", err)
		return false
	}
	opts, err := natsOptions(creds, endpoint, cfg.NATSServerName)
	if err != nil {
		slog.Error("collector NATS endpoint", "error", err)
		return false
	}
	nc := connectNATS(ctx, endpoint, opts)
	if nc == nil {
		return false
	}
	defer nc.Close()
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	if subject, serr := collectorSubject(creds); serr == nil {
		scopeChannel.Store(&scopeChannelState{nc: nc, subject: subject})
		defer scopeChannel.Store(nil)
		// The collector's discovery scopes and, once, its local and its
		// configured subnets as scope suggestions (DIS-09).
		wg.Add(2)
		go func() { defer wg.Done(); syncScopes(sctx, nc, subject) }()
		go func() { defer wg.Done(); reportSubnets(sctx, nc, subject, scopeSuggestions(&cfg, systemInterfaces)) }()
		// The server's tests of the vault connections this collector
		// resolves (WP-895, WP-896).
		if vsub, verr := serveVaultTests(sctx, nc, subject); verr != nil {
			slog.Warn("vault connection tests not served", "error", verr)
		} else {
			defer func() { _ = vsub.Unsubscribe() }()
		}
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		renew := func(rctx context.Context) (bool, error) {
			return renewCertificate(rctx, cfg.CredentialsPath, sessionRequest(nc), nil)
		}
		if runRenewal(sctx, renew, time.After) {
			renewed = true
			cancel()
		}
	}()
	// The credentials of the collector's scopes, encrypted for its key
	// (COL-02).
	syncBundle(sctx, nc, creds)
	cancel()
	wg.Wait()
	return renewed
}

// connectNATS opens the connection to the endpoint and retries every
// natsReconnectWait until it succeeds or ctx ends (nil); afterwards nats.go
// reconnects on its own.
func connectNATS(ctx context.Context, endpoint string, opts []nats.Option) *nats.Conn {
	log := &natsConnLog{url: endpoint}
	if hosts := endpointHosts(endpoint); len(hosts) > 0 {
		log.host = hosts[0]
	}
	opts = append(opts, log.options()...)
	for {
		nc, err := nats.Connect(endpoint, opts...)
		if err == nil {
			return nc
		}
		log.failed("collector NATS endpoint unreachable", err)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(natsReconnectWait):
		}
	}
}

// natsConnLog logs the state of the collector's connection to the endpoint
// (F-258): the success only once the endpoint accepted the connection
// (ConnectHandler, ReconnectHandler), a refusal of the endpoint as
// "collector NATS endpoint refused" with its cause. Every failure carries
// the event collector.nats.unreachable and, for the known causes, a hint
// (natsFailureHint). The same failure repeated on every attempt is logged
// once and then, while the collector stays unconnected, again at ERROR at
// most every natsFailureRepeat with the time it has been failing since and
// the number of attempts; a different failure is logged at once.
type natsConnLog struct {
	url  string
	host string // the host the collector dials, for the hints
	now  func() time.Time

	mu         sync.Mutex
	last       string
	lastLogged time.Time
	since      time.Time
	attempts   int
}

func (l *natsConnLog) options() []nats.Option {
	return []nats.Option{
		nats.ConnectHandler(func(*nats.Conn) { l.connected(false) }),
		nats.ReconnectHandler(func(*nats.Conn) { l.connected(true) }),
		// A reconnect did not reach the endpoint or failed in the TLS
		// handshake.
		nats.ReconnectErrHandler(func(_ *nats.Conn, err error) { l.failed("collector NATS endpoint unreachable", err) }),
		// The endpoint refused a reconnect (authorization callout).
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) { l.failed("collector NATS endpoint error", err) }),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) { l.failed("collector NATS endpoint disconnected", err) }),
	}
}

func (l *natsConnLog) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

func (l *natsConnLog) connected(reconnect bool) {
	l.mu.Lock()
	l.last, l.lastLogged, l.since, l.attempts = "", time.Time{}, time.Time{}, 0
	l.mu.Unlock()
	// A new edge outage warns again about the HTTP path (postPayload).
	httpUnauthenticatedLogged.Store(false)
	slog.Info("collector NATS endpoint connected over mTLS", "url", l.url, "reconnect", reconnect)
}

// failed logs err under msg, or as a refusal when the endpoint refused the
// collector: an authorization error or a TLS alert of the endpoint. A
// refusal and a certificate the collector cannot accept are errors from
// the first attempt, an unreachable endpoint a warning until it repeats.
func (l *natsConnLog) failed(msg string, err error) {
	if err == nil {
		return
	}
	level := slog.LevelWarn
	refusal := natsRefusal(err)
	if refusal {
		msg, level = "collector NATS endpoint refused", slog.LevelError
	}
	hint, certificate := natsFailureHint(err, l.host, refusal)
	if certificate {
		level = slog.LevelError
	}
	now := l.clock()
	key := msg + ": " + err.Error()
	l.mu.Lock()
	if l.since.IsZero() {
		l.since = now
	}
	l.attempts++
	repeated := l.last == key
	if repeated && now.Sub(l.lastLogged) < natsFailureRepeat {
		l.mu.Unlock()
		return
	}
	l.last, l.lastLogged = key, now
	since, attempts := l.since, l.attempts
	l.mu.Unlock()
	attrs := []any{"event", "collector.nats.unreachable", "url", l.url, "error", err}
	if hint != "" {
		attrs = append(attrs, "hint", hint)
	}
	if repeated {
		level = slog.LevelError
		attrs = append(attrs, "failing_since", since.UTC().Format(time.RFC3339), "attempts", attempts)
	}
	slog.Log(context.Background(), level, msg, attrs...)
}

// natsRefusal reports whether the endpoint refused the collector: an
// authorization error or a TLS alert the endpoint sent.
func natsRefusal(err error) bool {
	var opErr *net.OpError
	return errors.Is(err, nats.ErrAuthorization) || errors.Is(err, nats.ErrAuthExpired) || errors.Is(err, nats.ErrAuthRevoked) ||
		errors.Is(err, nats.ErrAccountAuthExpired) || (errors.As(err, &opErr) && opErr.Op == "remote error")
}

// natsFailureHint names what to do about a connection failure; certificate
// reports a server certificate the collector does not accept.
func natsFailureHint(err error, host string, refusal bool) (hint string, certificate bool) {
	var (
		hostErr  x509.HostnameError
		authErr  x509.UnknownAuthorityError
		certErr  x509.CertificateInvalidError
		dnsErr   *net.DNSError
		netErr   net.Error
		opErr    *net.OpError
		badAlert = errors.As(err, &opErr) && opErr.Op == "remote error"
	)
	switch {
	case errors.As(err, &hostErr):
		return fmt.Sprintf("the edge certificate does not name %s; update the server (RETICORA_NATS_EDGE_HOSTS / re-run install-cloud.sh) "+
			"or set RETICORA_NATS_TLS_SERVER_NAME", hostErr.Host), true
	case errors.As(err, &authErr):
		return "the edge certificate is not signed by the collector CA of this enrollment: the collector was enrolled with another " +
			"server or CA; re-enroll it (reticora-collector enroll --server <url> --code <code>), " +
			"then restart it (systemctl restart reticora-collector)", true
	case errors.As(err, &certErr):
		return "the edge certificate is not accepted (" + certErr.Error() + "); check the clock of the collector host " +
			"and the edge certificate of the server", true
	case badAlert:
		return "the endpoint refused the collector certificate: the collector is disabled, its certificate revoked or it was " +
			"enrolled with another installation; re-enroll it, then restart it (systemctl restart reticora-collector)", false
	case refusal:
		return "the endpoint refused the collector (authorization): the collector is disabled or unknown, " +
			"or the authorization service of the server is unavailable", false
	case errors.As(err, &dnsErr):
		return fmt.Sprintf("the name %s does not resolve on the collector host; check DNS, RETICORA_SERVER_URL and RETICORA_NATS_URL", dnsErr.Name), false
	case errors.Is(err, nats.ErrNoServers), errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.EHOSTUNREACH),
		errors.Is(err, syscall.ENETUNREACH), errors.Is(err, nats.ErrTimeout), errors.As(err, &netErr) && netErr.Timeout():
		return fmt.Sprintf("%s:%s/TCP is not reachable (connection refused or timed out): allow 4222/TCP outbound in the customer "+
			"network and inbound on the cloud host (host firewall, security group)", host, natsEndpointPort), false
	}
	return "", false
}
