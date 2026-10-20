//go:build auditdefect

// Phase-1 acceptance audit, group T4-collector-load – DEFECT tests of the
// collector PKI, the edge and the credential bundle on PostgreSQL (must fail
// until the defects are fixed).
//
// WP-145 (COL-04), WP-146 (COL-08), WP-147 (COL-02), WP-153 (DIS-09).
//
// Findings encoded here:
//   - TestAuditDisabledCollectorConnectionCut – WP-145 P1 "Disabled collector
//     keeps its open NATS connection and can keep ingesting" (real
//     nats-server with deploy/docker-compose/nats-edge.conf; needs
//     TEST_NATS_SERVER_BIN).
//   - TestAuditCARolloverKeepsEnrolledCollectorTrust – WP-145 P2 "after a
//     roll-over enrolled collectors no longer trust the edge server
//     certificate". COL-04: roll-over with two trust anchors and no outage.
//   - TestAuditConcurrentEdgePKIExportConsistent – WP-146 P2 "Concurrent
//     edge-PKI export by several server replicas leaves a mismatched or
//     missing server key/certificate" (k8s prod overlay: 3 replicas, shared
//     PVC).
//   - TestAuditBundleCredentialSelection – WP-153 P1 "A scope without
//     credential_ids empties the collector's credential bundle" and WP-147
//     P2 "Collectors without enabled scopes receive all client and org-wide
//     credentials" (bundle = only scope-relevant credentials).
//
// The shared helpers (auditEdge, auditEnroll, auditFreePort) are in
// edge_helpers_integration_test.go without a build tag (WP-374).
// TestAuditCertificateRenewalBoundToCollector (F-012) is fixed and lives in
// renew_edge_integration_test.go (WP-375).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  TEST_NATS_SERVER_BIN=<path to nats-server 2.10+> go test -tags auditdefect -count=1 -v -p 1 \
//	  -run 'TestAuditDisabledCollectorConnectionCut|TestAuditCARolloverKeepsEnrolledCollectorTrust|TestAuditConcurrentEdgePKIExportConsistent|TestAuditBundleCredentialSelection' ./internal/discovery/
package discovery_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
)

func TestAuditCARolloverKeepsEnrolledCollectorTrust(t *testing.T) {
	e, enc := auditEdge(t, "d1")
	bg := context.Background()
	secrets, err := discovery.DeriveEdgeSecrets(enc)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	_, _, enrolled := auditEnroll(t, e, "audit-roll-old")
	if err = discovery.ExportEdgePKI(bg, e.ca, secrets, dir, []string{"localhost"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	verifyServer := func(bundlePEM string) error {
		raw, rerr := os.ReadFile(filepath.Join(dir, "server.pem"))
		if rerr != nil {
			return rerr
		}
		roots := x509.NewCertPool()
		roots.AppendCertsFromPEM([]byte(bundlePEM))
		_, verr := parseCert(t, string(raw)).Verify(x509.VerifyOptions{Roots: roots, DNSName: "localhost",
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
		return verr
	}
	if verr := verifyServer(enrolled.CABundlePEM); verr != nil {
		t.Fatalf("control before roll-over: %v", verr)
	}
	if err = e.ca.Rollover(bg); err != nil {
		t.Fatal(err)
	}
	if err = discovery.ExportEdgePKI(bg, e.ca, secrets, dir, []string{"localhost"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	_, _, fresh := auditEnroll(t, e, "audit-roll-new")
	if verr := verifyServer(fresh.CABundlePEM); verr != nil {
		t.Errorf("a collector enrolled after the roll-over does not trust the edge: %v", verr)
	}
	if verr := verifyServer(enrolled.CABundlePEM); verr != nil {
		t.Errorf("COL-04 violated: after the CA roll-over a collector enrolled before it no longer trusts the edge server certificate (outage at the next nats-edge restart): %v", verr)
	}
}

func TestAuditConcurrentEdgePKIExportConsistent(t *testing.T) {
	e, enc := auditEdge(t, "d2")
	bg := context.Background()
	secrets, err := discovery.DeriveEdgeSecrets(enc)
	if err != nil {
		t.Fatal(err)
	}
	// Two server replicas sharing the database and the PKI volume.
	replicaA, replicaB := e.ca, discovery.NewCA(e.f.App, enc)
	if _, err = replicaA.TrustBundle(bg); err != nil {
		t.Fatal(err)
	}
	if _, err = replicaB.TrustBundle(bg); err != nil {
		t.Fatal(err)
	}
	const runs = 60
	var mismatched, errored int
	var firstMismatch, firstError string
	var widestMode os.FileMode
	for i := range runs {
		dir := filepath.Join(t.TempDir(), fmt.Sprintf("pki-%d", i))
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for j, ca := range []*discovery.CA{replicaA, replicaB} {
			wg.Go(func() {
				errs[j] = discovery.ExportEdgePKI(bg, ca, secrets, dir, []string{"localhost", "127.0.0.1"}, time.Now())
			})
		}
		wg.Wait()
		if errs[0] != nil || errs[1] != nil {
			errored++
			if firstError == "" {
				firstError = fmt.Sprint(errs)
			}
		}
		if _, lerr := tls.LoadX509KeyPair(filepath.Join(dir, "server.pem"), filepath.Join(dir, "server-key.pem")); lerr != nil {
			mismatched++
			if firstMismatch == "" {
				firstMismatch = lerr.Error()
			}
		}
		if st, serr := os.Stat(filepath.Join(dir, "server-key.pem")); serr == nil && st.Mode().Perm() > widestMode {
			widestMode = st.Mode().Perm()
		}
	}
	if mismatched > 0 {
		t.Errorf("%d of %d concurrent two-replica exports left a server key/certificate pair nats-edge cannot load (first: %s)", mismatched, runs, firstMismatch)
	}
	if errored > 0 {
		t.Errorf("%d of %d concurrent two-replica exports failed (first: %s)", errored, runs, firstError)
	}
	if widestMode&0o007 != 0 {
		t.Errorf("server-key.pem is readable by others: mode %o (want at most 0640)", widestMode)
	}
}

func TestAuditDisabledCollectorConnectionCut(t *testing.T) {
	bin := os.Getenv("TEST_NATS_SERVER_BIN")
	if bin == "" {
		t.Fatal("TEST_NATS_SERVER_BIN is required: the test runs deploy/docker-compose/nats-edge.conf with a real nats-server")
	}
	e, enc := auditEdge(t, "d3")
	f := e.f
	bg := context.Background()
	secrets, err := discovery.DeriveEdgeSecrets(enc)
	if err != nil {
		t.Fatal(err)
	}
	confDir := t.TempDir()
	if err = discovery.ExportEdgePKI(bg, e.ca, secrets, filepath.Join(confDir, "pki"), []string{"localhost", "127.0.0.1"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	colID, colCert := e.enroll(t, "audit-disable")
	conf, err := os.ReadFile("../../../deploy/docker-compose/nats-edge.conf")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(confDir, "nats-edge.conf"), conf, 0o600); err != nil {
		t.Fatal(err)
	}
	port := auditFreePort(t)
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-c", "nats-edge.conf", "-p", fmt.Sprint(port))
	cmd.Dir = confDir
	cmd.Env = append(os.Environ(), "RETICORA_NATS_EDGE_LEAF_URL=nats://127.0.0.1:1")
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = cmd.Wait() }()
	url := fmt.Sprintf("tls://127.0.0.1:%d", port)
	go discovery.RunEdge(ctx, &discovery.EdgeConfig{Pool: f.App, CA: e.ca, Secrets: secrets, URL: url})
	roots, _ := e.ca.TrustPool(bg)
	var nc *nats.Conn
	for range 50 {
		nc, err = nats.Connect(url, nats.Secure(&tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{colCert}}),
			nats.TLSHandshakeFirst(), nats.Timeout(3*time.Second), nats.CustomInboxPrefix(discovery.CollectorInbox(colID)), nats.NoReconnect())
		if err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("control: the active collector cannot connect: %v", err)
	}
	defer nc.Close()
	own := discovery.CollectorSubject(f.OrgA, colID) + ".ingest"
	sub, err := nc.SubscribeSync(own)
	if err != nil {
		t.Fatal(err)
	}
	if err = nc.Publish(own, []byte("before-disable")); err != nil {
		t.Fatal(err)
	}
	if _, err = sub.NextMsg(3 * time.Second); err != nil {
		t.Fatalf("control: own subject before disable: %v", err)
	}
	violation := make(chan error, 4)
	nc.SetErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, verr error) { violation <- verr })

	disabled := discovery.CollectorDisabled
	if _, err = e.repo.UpdateCollector(f.OrgCtx(f.OrgA), f.OrgA, colID, &discovery.CollectorPatch{Status: &disabled}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for nc.IsConnected() && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if !nc.IsConnected() {
		return // the connection was cut
	}
	_ = nc.Publish(own, []byte("after-disable"))
	_ = nc.Flush()
	msg, nerr := sub.NextMsg(2 * time.Second)
	select {
	case verr := <-violation:
		if strings.Contains(strings.ToLower(verr.Error()), "permissions violation") {
			return
		}
	default:
	}
	if nerr == nil && msg != nil {
		t.Errorf("COL-04 violated: 5 s after disable the collector is still connected (status %v) and its publish on %s was delivered", nc.Status(), own)
	}
}

func TestAuditBundleCredentialSelection(t *testing.T) {
	e, enc := auditEdge(t, "d4")
	f := e.f
	bg := context.Background()
	org := f.OrgCtx(f.OrgA)
	gate := discovery.NewBundleGate(f.App, e.ca)
	csvc := credential.NewService(credential.NewPGRepository(f.App), enc)
	orgCred, err := csvc.Create(org, credential.CreateRequest{OrganizationID: f.OrgA, Name: "audit-org-snmp", Kind: credential.KindSNMPv2c,
		Secret: map[string]any{"community": "org-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	scopeCred, err := csvc.Create(org, credential.CreateRequest{OrganizationID: f.OrgA, Name: "audit-scope-ssh", Kind: credential.KindSSHPassword,
		Secret: map[string]any{"username": "scope", "password": "scope-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	off := false
	cases := []struct {
		name   string
		scopes []discovery.ScopeInput
		want   []string
		reason string
	}{
		{name: "no scopes", want: []string{},
			reason: "WP-147: a collector without enabled scopes gets no credentials"},
		{name: "only a disabled scope", scopes: []discovery.ScopeInput{{Kind: "cidr", Target: "10.231.1.0/24", CredentialIDs: []string{scopeCred.ID}, Enabled: &off}}, want: []string{},
			reason: "WP-147: a collector without enabled scopes gets no credentials"},
		{name: "one enabled scope without credential_ids (UI form / accepted suggestion)", scopes: []discovery.ScopeInput{{Kind: "cidr", Target: "10.231.2.0/24"}}, want: []string{orgCred.ID},
			reason: "WP-153: a credential-less scope keeps the collector's client/org credentials"},
		{name: "one enabled scope with explicit credential_ids (control)", scopes: []discovery.ScopeInput{{Kind: "cidr", Target: "10.231.3.0/24", CredentialIDs: []string{scopeCred.ID}}}, want: []string{scopeCred.ID},
			reason: "DIS-09: explicit credential_ids select exactly those"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			colID, colCert := e.enroll(t, fmt.Sprintf("audit-bundle-%d", i))
			certPEM := string(pemOf(colCert.Certificate[0]))
			for _, in := range tc.scopes {
				in.CollectorID = colID
				if cerr := e.repo.CreateScope(org, discovery.NewScope(f.OrgA, &in)); cerr != nil {
					t.Fatalf("create scope: %v", cerr)
				}
			}
			tctx, target, verr := gate.VerifyBundleTarget(bg, f.OrgA, colID, certPEM)
			if verr != nil {
				t.Fatalf("bundle target: %v", verr)
			}
			_, ids, berr := csvc.Bundle(tctx, target)
			if berr != nil {
				t.Fatalf("bundle: %v", berr)
			}
			got := slices.Clone(ids)
			if got == nil {
				got = []string{}
			}
			slices.Sort(got)
			want := slices.Clone(tc.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("%s: bundle delivered %v, want %v (org credential %s, scope credential %s)", tc.reason, got, want, orgCred.ID, scopeCred.ID)
			}
		})
	}
}
