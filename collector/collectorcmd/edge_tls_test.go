package collectorcmd

// The edge server certificate, the connection log and the start before the
// enrollment (contract C1/C4 of the collector-ingest fix; COL-08, PRI-07,
// INS-06):
//
//   - TestEdgeCertificateNameRule: real certificates against a TLS endpoint
//     that speaks the NATS handshake, dialled with nats.go and the options
//     of natsOptions: a certificate of the collector CA with ServerAuth is
//     accepted when it names the dialled host, reticora-collector-nats or
//     nats-edge, also when the collector dials an IP address the
//     certificate does not name; a certificate for another name, of
//     another CA or without ServerAuth is refused; RETICORA_NATS_TLS_SERVER_NAME
//     replaces the rule by exactly that name.
//   - TestNATSConnLogRepeatsPersistingFailures: a failure that persists is
//     logged once, then again as an error at most every 10 minutes with the
//     time since it started; every line carries collector.nats.unreachable
//     and the hint for a name mismatch, an unknown CA and an unreachable
//     port.
//   - TestCollectorStartedBeforeItsEnrollment: without a credentials file
//     runNATS waits (logged once) and starts the session as soon as the
//     file exists, with the identity of the certificate.
//
// Retest: cd collector && go test -count=1 -v -run 'TestEdgeCertificateNameRule|TestNATSConnLog|TestCollectorStartedBeforeItsEnrollment' ./collectorcmd/

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/keystore"
)

// edgeServerPair issues a server certificate by ca for the DNS names and
// IP addresses with the extended key usages; it returns the TLS pair.
func edgeServerPair(t *testing.T, ca *auditPKI, dns []string, ips []net.IP, usages []x509.ExtKeyUsage) tls.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "reticora-collector-nats", Organization: []string{"Reticora"}},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: usages, DNSNames: dns, IPAddresses: ips}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), []byte(auditKeyPEM(t, key)))
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

// collectorCreds are enrolled credentials of a collector of ca.
func collectorCreds(t *testing.T, ca *auditPKI, orgID, colID string) *keystore.Credentials {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	return &keystore.Credentials{
		DeviceID: colID,
		ClientCertificatePEM: ca.issue(t, &key.PublicKey, pkix.Name{CommonName: colID, Organization: []string{orgID}},
			time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour), false),
		ClientPrivateKeyPEM:  auditKeyPEM(t, key),
		CertificateAuthority: ca.pem,
	}
}

// fakeNATSEndpoint is a TLS endpoint (handshake first, client certificate
// of the collector CA required) that answers the NATS handshake: INFO,
// then PONG to every PING. It returns its port.
func fakeNATSEndpoint(t *testing.T, server tls.Certificate, clientCA *auditPKI) int {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(clientCA.cert)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{server}, MinVersion: tls.VersionTLS12,
		ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
				if conn.(*tls.Conn).Handshake() != nil {
					return
				}
				if _, werr := fmt.Fprint(conn, `INFO {"server_id":"fake","version":"2.10.0","proto":1,"max_payload":1048576,"tls_required":true,"headers":true}`+"\r\n"); werr != nil {
					return
				}
				lines := bufio.NewScanner(conn)
				for lines.Scan() {
					if strings.HasPrefix(lines.Text(), "PING") {
						if _, werr := fmt.Fprint(conn, "PONG\r\n"); werr != nil {
							return
						}
					}
				}
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// dialEdge connects like the collector (natsOptions) to url.
func dialEdge(t *testing.T, creds *keystore.Credentials, url, serverName string) error {
	t.Helper()
	opts, err := natsOptions(creds, url, serverName)
	if err != nil {
		t.Fatal(err)
	}
	nc, err := nats.Connect(url, append(opts, nats.Timeout(5*time.Second))...)
	if err == nil {
		nc.Close()
	}
	return err
}

func TestEdgeCertificateNameRule(t *testing.T) {
	ca, other := newAuditPKI(t), newAuditPKI(t)
	creds := collectorCreds(t, ca, "6f0b6f0e-0000-4000-8000-0000000000d1", "6f0b6f0e-0000-4000-8000-0000000000d2")
	serverAuth := []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	loopback := []net.IP{net.ParseIP("127.0.0.1")}
	var hostErr x509.HostnameError
	var authErr x509.UnknownAuthorityError
	var usageErr x509.CertificateInvalidError
	for _, tc := range []struct {
		name       string
		server     tls.Certificate
		host       string // dialled host
		serverName string // RETICORA_NATS_TLS_SERVER_NAME
		check      func(error) bool
		want       string
	}{
		// Installations without a TLS domain: the certificate names
		// localhost and nats-edge, the collector dials the IP (C1).
		{"nats-edge only, dialled by IP", edgeServerPair(t, ca, []string{"localhost", "nats-edge", "localhost"}, nil, serverAuth),
			"127.0.0.1", "", func(err error) bool { return err == nil }, "accepted"},
		{"fixed edge name only, dialled by IP", edgeServerPair(t, ca, []string{"reticora-collector-nats"}, nil, serverAuth),
			"127.0.0.1", "", func(err error) bool { return err == nil }, "accepted"},
		{"the dialled IP", edgeServerPair(t, ca, nil, loopback, serverAuth),
			"127.0.0.1", "", func(err error) bool { return err == nil }, "accepted"},
		{"the dialled name", edgeServerPair(t, ca, []string{"localhost"}, nil, serverAuth),
			"localhost", "", func(err error) bool { return err == nil }, "accepted"},
		{"an unrelated name", edgeServerPair(t, ca, []string{"evil.example"}, nil, serverAuth),
			"127.0.0.1", "", func(err error) bool { return errors.As(err, &hostErr) && hostErr.Host == "127.0.0.1" }, "a HostnameError for 127.0.0.1"},
		{"another CA", edgeServerPair(t, other, []string{"nats-edge", "reticora-collector-nats"}, loopback, serverAuth),
			"127.0.0.1", "", func(err error) bool { return errors.As(err, &authErr) }, "an UnknownAuthorityError"},
		{"a collector certificate (ClientAuth only)", edgeServerPair(t, ca, []string{"nats-edge", "reticora-collector-nats"}, loopback,
			[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}),
			"127.0.0.1", "", func(err error) bool { return errors.As(err, &usageErr) && usageErr.Reason == x509.IncompatibleUsage }, "an IncompatibleUsage error"},
		{"no extended key usage", edgeServerPair(t, ca, []string{"nats-edge"}, loopback, nil),
			"127.0.0.1", "", func(err error) bool { return errors.As(err, &usageErr) && usageErr.Reason == x509.IncompatibleUsage }, "an IncompatibleUsage error"},
		// The override replaces the rule by exactly its name.
		{"override honoured", edgeServerPair(t, ca, []string{"edge.example.net"}, nil, serverAuth),
			"127.0.0.1", "edge.example.net", func(err error) bool { return err == nil }, "accepted"},
		{"override replaces the fixed names", edgeServerPair(t, ca, []string{"nats-edge", "reticora-collector-nats"}, loopback, serverAuth),
			"127.0.0.1", "edge.example.net", func(err error) bool { return errors.As(err, &hostErr) && hostErr.Host == "edge.example.net" },
			"a HostnameError for the override"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			port := fakeNATSEndpoint(t, tc.server, ca)
			err := dialEdge(t, creds, fmt.Sprintf("tls://%s:%d", tc.host, port), tc.serverName)
			if !tc.check(err) {
				t.Fatalf("dial %s: %v, want %s", tc.host, err, tc.want)
			}
		})
	}

	// A connection whose certificate chains to the CA but presents nothing
	// is refused before any name is considered.
	if err := verifyEdgeServer(nil, x509.NewCertPool(), []string{"nats-edge"}, time.Now()); err == nil {
		t.Error("no certificate accepted")
	}
	if got := strings.Join(edgeServerNames("", endpointHosts("tls://192.0.2.10:4222,tls://[2001:db8::1]:4222"), ""), ","); got != "192.0.2.10,2001:db8::1,reticora-collector-nats,nats-edge" {
		t.Errorf("names = %s", got)
	}
}

func TestNATSConnLogRepeatsPersistingFailures(t *testing.T) {
	logs := captureLogs(t)
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	l := &natsConnLog{url: "tls://192.0.2.10:4222", host: "192.0.2.10", now: func() time.Time { return now }}
	ca := newAuditPKI(t)
	mismatch := fmt.Errorf("%w: %w", nats.ErrTLS, fmt.Errorf("the edge certificate is valid for none of 192.0.2.10: %w",
		x509.HostnameError{Certificate: ca.cert, Host: "192.0.2.10"}))
	const msg = "collector NATS endpoint unreachable"
	lines := func() []map[string]any { return logLines(t, logs, msg) }

	l.failed(msg, mismatch)
	now = now.Add(5 * time.Minute)
	l.failed(msg, mismatch)
	if got := lines(); len(got) != 1 || got[0]["level"] != "ERROR" || got[0]["event"] != "collector.nats.unreachable" ||
		!strings.Contains(fmt.Sprint(got[0]["hint"]), "does not name 192.0.2.10") ||
		!strings.Contains(fmt.Sprint(got[0]["hint"]), "RETICORA_NATS_TLS_SERVER_NAME") {
		t.Fatalf("name mismatch: %v, want one ERROR with the hint", got)
	}
	now = now.Add(5 * time.Minute)
	l.failed(msg, mismatch)
	got := lines()
	if len(got) != 2 || got[1]["level"] != "ERROR" || got[1]["failing_since"] != "2026-11-01T10:00:00Z" || got[1]["attempts"] != float64(3) {
		t.Fatalf("repeat after 10 minutes: %v", got)
	}
	now = now.Add(time.Minute)
	l.failed(msg, mismatch)
	if n := len(lines()); n != 2 {
		t.Fatalf("%d lines, want no repeat within 10 minutes", n)
	}

	// Another failure is logged at once; an unreachable port at WARN first.
	timeout := &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}
	l.failed(msg, timeout)
	if got = lines(); len(got) != 3 || got[2]["level"] != "WARN" || !strings.Contains(fmt.Sprint(got[2]["hint"]), "192.0.2.10:4222/TCP is not reachable") {
		t.Fatalf("timeout: %v", got)
	}
	l.failed(msg, nats.ErrNoServers)
	if got = lines(); len(got) != 4 || !strings.Contains(fmt.Sprint(got[3]["hint"]), "allow 4222/TCP outbound") {
		t.Fatalf("connection refused: %v", got)
	}
	l.failed(msg, fmt.Errorf("%w: %w", nats.ErrTLS, x509.UnknownAuthorityError{}))
	if got = lines(); len(got) != 5 || got[4]["level"] != "ERROR" || !strings.Contains(fmt.Sprint(got[4]["hint"]), "re-enroll") {
		t.Fatalf("unknown authority: %v", got)
	}

	// Connected: the next outage starts anew.
	l.connected(false)
	l.failed(msg, timeout)
	if got = lines(); len(got) != 6 || got[5]["failing_since"] != nil {
		t.Fatalf("after a connection: %v", got)
	}
}

func TestCollectorStartedBeforeItsEnrollment(t *testing.T) {
	logs := captureLogs(t)
	poll, wait := natsEnrollPoll, natsReconnectWait
	natsEnrollPoll, natsReconnectWait = 20*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { natsEnrollPoll, natsReconnectWait = poll, wait })
	// Nothing listens on the endpoint: the session's attempts show it runs.
	t.Setenv("RETICORA_NATS_URL", fmt.Sprintf("tls://127.0.0.1:%d", auditFreeTCPPort(t)))
	path := filepath.Join(t.TempDir(), "credentials.json")
	ctx, cancel := context.WithCancel(context.Background())
	adopted := make(chan collectorConfig, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runNATS(ctx, collectorConfig{ServerURL: "http://localhost:8080", CredentialsPath: path}, func(c collectorConfig) { adopted <- c })
	}()
	defer func() { cancel(); <-done }()

	time.Sleep(200 * time.Millisecond)
	if n := len(logLines(t, logs, "collector not enrolled: nothing is delivered until it is; the credentials file is checked every 20ms")); n != 1 {
		t.Fatalf("%d not-enrolled warnings over ~10 checks, want 1:\n%s", n, logs.String())
	}
	select {
	case <-done:
		t.Fatal("runNATS gave up without the credentials file")
	default:
	}

	const orgID, colID = "6f0b6f0e-0000-4000-8000-0000000000e1", "6f0b6f0e-0000-4000-8000-0000000000e2"
	creds := collectorCreds(t, newAuditPKI(t), orgID, colID)
	creds.Metadata = map[string]string{"server_url": "https://cmdb.example.com"}
	if err := (&keystore.FileStore{Path: path}).Save(context.Background(), creds); err != nil {
		t.Fatal(err)
	}
	select {
	case cfg := <-adopted:
		if cfg.OrganizationID != orgID || cfg.CollectorID != colID || cfg.ServerURL != "https://cmdb.example.com" {
			t.Errorf("adopted identity: org %q collector %q server %q", cfg.OrganizationID, cfg.CollectorID, cfg.ServerURL)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the enrolled identity was not handed over:\n%s", logs.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(logLines(t, logs, "collector NATS endpoint unreachable")) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("no connection attempt after the enrollment:\n%s", logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
