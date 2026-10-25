package collectorcmd

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

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
	var o nats.Options
	for _, opt := range opts {
		if err = opt(&o); err != nil {
			t.Fatal(err)
		}
	}
	// It keeps reconnecting, also after repeated authentication refusals
	// (WP-381, TestCollectorNATSReconnectsAfterAuthRefusals). The first
	// connection is retried by connectNATS, which logs every refusal; with
	// RetryOnFailedConnect nats.go would report none before it (WP-414,
	// F-258, TestCollectorNATSLogsRefusalNotSuccess).
	if o.MaxReconnect != -1 || o.RetryOnFailedConnect || !o.IgnoreAuthErrorAbort {
		t.Fatalf("reconnect options: max reconnects %d, retry on failed connect %v, ignore auth error abort %v",
			o.MaxReconnect, o.RetryOnFailedConnect, o.IgnoreAuthErrorAbort)
	}
	if subject, serr := collectorSubject(creds); serr != nil || subject != "collector.org-1.col-1" {
		t.Fatalf("subject = %q, %v", subject, serr)
	}
	creds.CertificateAuthority = ""
	if _, err = natsOptions(creds); err == nil {
		t.Fatal("options without the collector CA")
	}
}

// TestCollectorNATSReconnectsAfterAuthRefusals covers the follow-up to
// WP-381 (COL-08): the user JWTs of the auth callout expire after 15 min
// (EdgeUserValidity), so the collector authenticates anew on the endpoint
// every 15 min. If the endpoint refuses it meanwhile (callout unreachable or
// collector disabled: "Authorization Violation" on every attempt), nats.go
// by default closes the connection for good on the same authentication
// error twice, and the collector has no edge channel until it is restarted.
// The connection built from natsOptions must keep reconnecting and be back
// once the endpoint accepts the collector again. The endpoint is a real
// nats-server with mTLS whose authorization (verify_and_map) is switched by
// a configuration reload: accept, refuse, accept.
func TestCollectorNATSReconnectsAfterAuthRefusals(t *testing.T) {
	bin := natsServerBin(t)
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ca := newAuditPKI(t)
	now := time.Now()
	serverKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	write("ca.pem", ca.pem)
	write("server.pem", ca.issue(t, &serverKey.PublicKey, pkix.Name{CommonName: "127.0.0.1"}, now.Add(-time.Hour), now.Add(24*time.Hour), true))
	write("server-key.pem", auditKeyPEM(t, serverKey))
	const orgID, colID = "6f0b6f0e-0000-4000-8000-0000000000c1", "6f0b6f0e-0000-4000-8000-0000000000c2"
	colKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	creds := &keystore.Credentials{
		DeviceID:             colID,
		ClientCertificatePEM: ca.issue(t, &colKey.PublicKey, pkix.Name{CommonName: colID, Organization: []string{orgID}}, now.Add(-time.Hour), now.Add(24*time.Hour), false),
		ClientPrivateKeyPEM:  auditKeyPEM(t, colKey),
		CertificateAuthority: ca.pem,
	}
	port := auditFreeTCPPort(t)
	// admit writes the server configuration that admits only the
	// certificate subject user.
	admit := func(user string) {
		write("nats.conf", fmt.Sprintf(`listen: "127.0.0.1:%d"
tls {
  cert_file: "server.pem"
  key_file: "server-key.pem"
  ca_file: "ca.pem"
  verify_and_map: true
  handshake_first: true
  timeout: 5
}
authorization {
  users = [ { user: %q } ]
}
`, port, user))
	}
	collectorUser := "CN=" + colID + ",O=" + orgID
	admit(collectorUser)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := exec.CommandContext(ctx, bin, "-c", "nats.conf")
	srv.Dir = dir
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = srv.Wait() }()
	reload := func(user string) {
		admit(user)
		if err := srv.Process.Signal(syscall.SIGHUP); err != nil {
			t.Fatal(err)
		}
	}

	opts, err := natsOptions(creds)
	if err != nil {
		t.Fatal(err)
	}
	var refusals atomic.Int32
	closed := make(chan struct{})
	// Shorter waits than in production (5 s plus up to 1 s TLS jitter) keep
	// the test fast; giving up on repeated refusals does not depend on them.
	opts = append(opts,
		nats.ReconnectWait(50*time.Millisecond),
		nats.ReconnectJitter(0, 0),
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			if errors.Is(err, nats.ErrAuthorization) {
				refusals.Add(1)
			}
		}),
		nats.ClosedHandler(func(*nats.Conn) { close(closed) }),
	)
	// The first connection waits for the nats-server to listen (in the
	// collector connectNATS retries it).
	var nc *nats.Conn
	for range 50 {
		if nc, err = nats.Connect(fmt.Sprintf("tls://127.0.0.1:%d", port), opts...); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	isClosed := func() bool {
		select {
		case <-closed:
			return true
		default:
			return false
		}
	}
	waitFor := func(cond func() bool) bool {
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if cond() || isClosed() {
				return cond()
			}
		}
		return false
	}
	if !waitFor(nc.IsConnected) {
		t.Fatalf("control: the collector did not connect over mTLS (status %v, last error %v)", nc.Status(), nc.LastError())
	}

	// The endpoint refuses the collector on every attempt.
	reload("CN=someone-else")
	const wantRefusals = 5
	waitFor(func() bool { return refusals.Load() >= wantRefusals })
	if isClosed() {
		t.Fatalf("the collector's NATS connection was closed for good after %d authentication refusals (last error %v): "+
			"once the endpoint accepts it again it stays without its edge channel until restarted", refusals.Load(), nc.LastError())
	}
	if n := refusals.Load(); n < wantRefusals || nc.IsConnected() {
		t.Fatalf("control: the endpoint refused the collector %d times in 10 s (connected %v)", n, nc.IsConnected())
	}

	// The endpoint accepts it again: the same connection is back.
	reload(collectorUser)
	if !waitFor(nc.IsConnected) {
		t.Fatalf("the collector did not reconnect once the endpoint accepted it again (status %v, closed %v, last error %v)",
			nc.Status(), isClosed(), nc.LastError())
	}
	if err = nc.FlushTimeout(5 * time.Second); err != nil {
		t.Fatalf("round trip after the reconnect: %v", err)
	}
}

// TestCollectorNATSLogsRefusalNotSuccess covers WP-414 (DIS-08, F-258):
// with RetryOnFailedConnect nats.Connect returns without an error also when
// the endpoint refuses the collector, and runNATS logged "connected over
// mTLS" unconditionally. Against a real nats-server with mTLS that refuses
// the collector's certificate (verify_and_map, like the callout refuses a
// disabled collector), runNATS logs no success and one "collector NATS
// endpoint refused" with the cause over several refused attempts; once the
// endpoint accepts it, "connected" follows; refused again after a
// configuration reload, one further "refused" and no success; accepted
// again, "connected" once more.
func TestCollectorNATSLogsRefusalNotSuccess(t *testing.T) {
	bin := natsServerBin(t)
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ca := newAuditPKI(t)
	now := time.Now()
	serverKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	write("ca.pem", ca.pem)
	write("server.pem", ca.issue(t, &serverKey.PublicKey, pkix.Name{CommonName: "127.0.0.1"}, now.Add(-time.Hour), now.Add(24*time.Hour), true))
	write("server-key.pem", auditKeyPEM(t, serverKey))
	const orgID, colID = "6f0b6f0e-0000-4000-8000-0000000000c3", "6f0b6f0e-0000-4000-8000-0000000000c4"
	colKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	credPath := filepath.Join(dir, "credentials.json")
	if err := (&keystore.FileStore{Path: credPath}).Save(context.Background(), keystore.Credentials{
		DeviceID:             colID,
		ClientCertificatePEM: ca.issue(t, &colKey.PublicKey, pkix.Name{CommonName: colID, Organization: []string{orgID}}, now.Add(-time.Hour), now.Add(24*time.Hour), false),
		ClientPrivateKeyPEM:  auditKeyPEM(t, colKey),
		CertificateAuthority: ca.pem,
	}); err != nil {
		t.Fatal(err)
	}
	port := auditFreeTCPPort(t)
	admit := func(user string) {
		write("nats.conf", fmt.Sprintf(`listen: "127.0.0.1:%d"
tls {
  cert_file: "server.pem"
  key_file: "server-key.pem"
  ca_file: "ca.pem"
  verify_and_map: true
  handshake_first: true
  timeout: 5
}
authorization {
  users = [ { user: %q } ]
}
`, port, user))
	}
	collectorUser := "CN=" + colID + ",O=" + orgID
	// The endpoint refuses the collector from the start.
	admit("CN=someone-else")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := exec.CommandContext(ctx, bin, "-c", "nats.conf")
	srv.Dir = dir
	serverLog := &lockedBuffer{}
	srv.Stdout, srv.Stderr = serverLog, serverLog
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = srv.Wait() }()
	reload := func(user string) {
		admit(user)
		if err := srv.Process.Signal(syscall.SIGHUP); err != nil {
			t.Fatal(err)
		}
	}
	refusedByServer := func() int { return strings.Count(serverLog.String(), "authentication error") }

	logs := &lockedBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	wait := natsReconnectWait
	natsReconnectWait = 50 * time.Millisecond
	t.Cleanup(func() { natsReconnectWait = wait })
	t.Setenv("RETICORA_NATS_URL", fmt.Sprintf("tls://127.0.0.1:%d", port))
	runCtx, stopRun := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runNATS(runCtx, collectorConfig{ServerURL: "https://cmdb.example.com", CredentialsPath: credPath})
	}()
	defer func() { stopRun(); <-done }()

	waitFor := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if cond() {
				return
			}
		}
		t.Fatalf("timeout waiting for %s; collector log:\n%s\nserver log tail:\n%s", what, logs.String(), auditTail(serverLog.String(), 5))
	}
	const connected, refused = `"msg":"collector NATS endpoint connected over mTLS"`, `"msg":"collector NATS endpoint refused"`
	count := func(from int, msg string) int { return strings.Count(logs.String()[from:], msg) }
	refusedLine := func(from int) string {
		for _, line := range strings.Split(logs.String()[from:], "\n") {
			if strings.Contains(line, refused) {
				return line
			}
		}
		return ""
	}

	// Refused from the start (a disabled collector restarted): no success,
	// one refusal with the cause.
	waitFor("three refused connection attempts", func() bool { return refusedByServer() >= 3 })
	if n := count(0, connected); n != 0 {
		t.Errorf("F-258: the collector logged %d successful connections while the endpoint refused it:\n%s", n, logs.String())
	}
	if n := count(0, refused); n != 1 || !strings.Contains(strings.ToLower(refusedLine(0)), "authorization violation") {
		t.Errorf("F-258: want one %s with the cause (authorization violation) over %d refused attempts, got %d:\n%s",
			refused, refusedByServer(), n, logs.String())
	}

	// Accepted: the success is logged.
	reload(collectorUser)
	waitFor("the connection after the endpoint accepted the collector", func() bool { return count(0, connected) == 1 })

	// Refused again (configuration reload, the server drops the
	// connection): no success, one further refusal.
	offset, attempts := len(logs.String()), refusedByServer()
	reload("CN=someone-else")
	waitFor("three further refused connection attempts", func() bool { return refusedByServer() >= attempts+3 })
	if n := count(offset, connected); n != 0 {
		t.Errorf("F-258: the collector logged %d successful connections after the endpoint refused it again:\n%s", n, logs.String()[offset:])
	}
	if n := count(offset, refused); n != 1 || !strings.Contains(strings.ToLower(refusedLine(offset)), "authorization violation") {
		t.Errorf("F-258: want one %s with the cause after the endpoint refused the collector again (%d attempts), got %d:\n%s",
			refused, refusedByServer()-attempts, n, logs.String()[offset:])
	}

	// Accepted again: connected once more.
	reload(collectorUser)
	waitFor("the reconnect after the endpoint accepted the collector again", func() bool { return count(offset, connected) == 1 })
}
