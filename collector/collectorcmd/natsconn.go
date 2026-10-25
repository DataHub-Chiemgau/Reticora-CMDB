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
	"sync"
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
// enrollment as trust anchor, the TLS handshake first and its own reply
// inbox (other collectors cannot read its replies). The first connection
// is retried by connectNATS, not by nats.go (RetryOnFailedConnect): before
// its first connection nats.go reports no refusal (F-258).
func natsOptions(creds *keystore.Credentials) ([]nats.Option, error) {
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
		nats.Secure(&tls.Config{Certificates: []tls.Certificate{pair}, RootCAs: roots, MinVersion: tls.VersionTLS12}),
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

// runNATS keeps the collector's mTLS connection to the NATS endpoint open
// while ctx lasts; without enrolled credentials it stays off. After a
// certificate renewal it closes the session and starts a new one with the
// renewed credentials (COL-04).
func runNATS(ctx context.Context, cfg collectorConfig) {
	for runNATSSession(ctx, cfg) && ctx.Err() == nil {
		slog.Info("collector NATS session restarts with the renewed certificate")
	}
}

// runNATSSession runs one session with the credentials in the keystore:
// the TLS configuration, the scope channel (edge upload), the scopes, the
// local subnets, the credential bundle and the renewal check. It reports
// whether the certificate was renewed; the session is then over.
func runNATSSession(ctx context.Context, cfg collectorConfig) (renewed bool) {
	endpoint := natsEndpoint(cfg.ServerURL)
	creds, err := (&keystore.FileStore{Path: cfg.CredentialsPath}).Load(ctx)
	if err != nil || endpoint == "" {
		slog.Info("collector NATS endpoint not used", "reason", "not enrolled or no endpoint", "error", err)
		return false
	}
	opts, err := natsOptions(creds)
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
		// The collector's discovery scopes and, once, its local subnets as
		// scope suggestions (DIS-09).
		wg.Add(2)
		go func() { defer wg.Done(); syncScopes(sctx, nc, subject) }()
		go func() { defer wg.Done(); reportSubnets(sctx, nc, subject, localSubnets(net.InterfaceAddrs)) }()
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
// "collector NATS endpoint refused" with its cause. The same failure
// repeated on every attempt is logged once until the collector is
// connected again.
type natsConnLog struct {
	url  string
	mu   sync.Mutex
	last string
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

func (l *natsConnLog) connected(reconnect bool) {
	l.mu.Lock()
	l.last = ""
	l.mu.Unlock()
	slog.Info("collector NATS endpoint connected over mTLS", "url", l.url, "reconnect", reconnect)
}

// failed logs err under msg, or as a refusal when the endpoint refused the
// collector: an authorization error or a TLS alert of the endpoint.
func (l *natsConnLog) failed(msg string, err error) {
	if err == nil {
		return
	}
	level := slog.LevelWarn
	var opErr *net.OpError
	if errors.Is(err, nats.ErrAuthorization) || errors.Is(err, nats.ErrAuthExpired) || errors.Is(err, nats.ErrAuthRevoked) ||
		errors.Is(err, nats.ErrAccountAuthExpired) || (errors.As(err, &opErr) && opErr.Op == "remote error") {
		msg, level = "collector NATS endpoint refused", slog.LevelError
	}
	l.mu.Lock()
	repeated := l.last == msg+": "+err.Error()
	l.last = msg + ": " + err.Error()
	l.mu.Unlock()
	if !repeated {
		slog.Log(context.Background(), level, msg, "url", l.url, "error", err)
	}
}
