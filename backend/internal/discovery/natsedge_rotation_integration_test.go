package discovery_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

// cleanDerivationRoot removes the derivation root of the test database
// before and after the test: the root goes with the master key a test draws.
func cleanDerivationRoot(t *testing.T, f *scopetest.Fixture) {
	t.Helper()
	clean := func() { _, _ = f.Admin.Exec(context.Background(), `DELETE FROM platform_secret`) }
	clean()
	t.Cleanup(clean)
}

// rotationDEKs is the DEK store of the rewrap run (credential).
type rotationDEKs struct{ repo *credential.PGRepository }

func (d rotationDEKs) RotateOrgDEK(ctx context.Context, orgID string, enc *crypto.EnvelopeEncryptor) (int, int, error) {
	res, err := d.repo.RotateOrgDEK(ctx, orgID, enc)
	if err != nil {
		return 0, 0, err
	}
	return res.KeyVersion, res.Rotated, nil
}

func (d rotationDEKs) RewrapOrgDEK(ctx context.Context, orgID string, enc *crypto.EnvelopeEncryptor) (bool, error) {
	return d.repo.RewrapOrgDEK(ctx, orgID, enc)
}

// TestEdgeAuthAfterRotation covers F-158 (SEC-06, WP-459) against
// PostgreSQL and the deployed endpoint (a real nats-server with
// deploy/docker-compose/nats-edge.conf): the keys of the collector NATS
// endpoint come from the derivation root of the installation, not from the
// master key. After a master-key rotation (the new key, the old one
// previous), the rewrap run (rotation.master_key) and a restart of the
// server with the new key alone, the server derives the same keys and
// writes the same edge.conf; its authorization callout logs in to the
// nats-edge that kept running with the files of before, and the enrolled
// collector is admitted. Negative: a tampered root does not open, so the
// server does not start and no new root is drawn; a replaced root (another
// root, sealed with the valid key) derives other keys: nats-edge refuses
// that login and the collector is not admitted while only such a server
// runs.
//
// It needs TEST_NATS_SERVER_BIN.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  TEST_NATS_SERVER_BIN=<path to nats-server 2.10+> go test -count=1 -v -p 1 \
//	  -run 'TestEdgeAuthAfterRotation' ./internal/discovery/
func TestEdgeAuthAfterRotation(t *testing.T) {
	f := scopetest.Seed(t, "fa")
	natsBin := edgeNATSServerBin(t)
	bg := context.Background()
	clean := func() { _, _ = f.Admin.Exec(bg, `DELETE FROM collector_ca`) }
	clean()
	t.Cleanup(clean)
	cleanDerivationRoot(t, f)
	oldKey, _ := crypto.GenerateMasterKey()
	newKey, _ := crypto.GenerateMasterKey()

	// The server before the rotation; its first start draws the root.
	oldEnc, err := crypto.NewEnvelopeEncryptor(oldKey)
	if err != nil {
		t.Fatal(err)
	}
	if created, lerr := crypto.LoadDerivationRoot(bg, f.App, oldEnc); lerr != nil || !created {
		t.Fatalf("first start: created %t, %v", created, lerr)
	}
	oldCA := discovery.NewCA(f.App, oldEnc)
	e := &edgeFixture{f: f, ca: oldCA, repo: discovery.NewPGRepository(f.App)}
	colID, colKey, issued := auditEnroll(t, e, "rotation")
	identity := clientIdentity(t, colKey, issued)
	secrets, err := discovery.DeriveEdgeSecrets(oldEnc)
	if err != nil {
		t.Fatal(err)
	}

	// The endpoint, started once with the files of that server.
	dir := t.TempDir()
	pki := filepath.Join(dir, "pki")
	hosts := []string{"localhost", "127.0.0.1"}
	if err = discovery.ExportEdgePKI(bg, oldCA, secrets, pki, hosts, time.Now()); err != nil {
		t.Fatal(err)
	}
	edgeConf, err := os.ReadFile(filepath.Join(pki, "edge.conf"))
	if err != nil {
		t.Fatal(err)
	}
	conf, err := os.ReadFile("../../../deploy/docker-compose/nats-edge.conf")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "nats-edge.conf"), conf, 0o644); err != nil {
		t.Fatal(err)
	}
	edgePort := auditFreePort(t)
	startNATSServer(t, natsBin, dir, []string{"RETICORA_NATS_EDGE_LEAF_URL=nats://127.0.0.1:1"},
		"-c", "nats-edge.conf", "-p", fmt.Sprint(edgePort))
	edgeURL := fmt.Sprintf("tls://127.0.0.1:%d", edgePort)
	roots, err := oldCA.TrustPool(bg)
	if err != nil {
		t.Fatal(err)
	}
	tlsConf := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{identity}}
	dial := func(opts ...nats.Option) (*nats.Conn, error) {
		return nats.Connect(edgeURL, append([]nats.Option{nats.Secure(tlsConf), nats.TLSHandshakeFirst(), nats.Timeout(3 * time.Second),
			nats.CustomInboxPrefix(discovery.CollectorInbox(colID)), nats.NoReconnect()}, opts...)...)
	}
	// admitted waits until the collector passes the callout.
	admitted := func(what string) {
		t.Helper()
		var derr error
		for range 50 {
			var nc *nats.Conn
			if nc, derr = dial(); derr == nil {
				nc.Close()
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("%s: the collector is not admitted: %v", what, derr)
	}
	// refused waits until the collector is refused (the callout of a
	// stopped server closes its connection shortly after the worker
	// returned) and then requires the refusal for 5 s on end.
	refused := func(what string) {
		t.Helper()
		for range 25 {
			nc, derr := dial()
			if derr != nil {
				break
			}
			nc.Close()
			time.Sleep(200 * time.Millisecond)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if nc, derr := dial(); derr == nil {
				nc.Close()
				t.Fatalf("%s: the collector was admitted", what)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	// login is the callout's login at the endpoint with password (TLS with
	// a certificate of the collector CA, as every connection).
	login := func(password string) error {
		nc, lerr := dial(nats.UserInfo(discovery.EdgeAuthUser, password))
		if lerr == nil {
			nc.Close()
		}
		return lerr
	}
	// runServer runs the edge worker of a server until stop returns.
	runServer := func(ca *discovery.CA, sec *discovery.EdgeSecrets, pkiDir string) (stop func()) {
		ctx, cancel := context.WithCancel(bg)
		done := make(chan struct{})
		go func() {
			defer close(done)
			discovery.RunEdge(ctx, &discovery.EdgeConfig{Pool: f.App, CA: ca, Secrets: sec, PKIDir: pkiDir, Hosts: hosts, URL: edgeURL})
		}()
		return func() {
			cancel()
			<-done
		}
	}

	stop := runServer(oldCA, secrets, pki)
	admitted("before the rotation")
	stop()
	refused("with the server stopped")

	// The rotation: the new key with the old one previous, the restart and
	// the rewrap run; then the restart with the new key alone.
	rotating, err := crypto.NewEnvelopeEncryptor(newKey, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	if created, lerr := crypto.LoadDerivationRoot(bg, f.App, rotating); lerr != nil || created {
		t.Fatalf("restart during the rotation: created %t, %v", created, lerr)
	}
	n, err := jobs.NewRotation(f.App, rotating, rotationDEKs{repo: credential.NewPGRepository(f.App)}, nil).RewrapMasterKey(bg)
	if err != nil || n < 2 {
		t.Fatalf("rewrap run: %d rewrapped (want the CA key and the root), %v", n, err)
	}
	newEnc, err := crypto.NewEnvelopeEncryptor(newKey)
	if err != nil {
		t.Fatal(err)
	}
	if created, lerr := crypto.LoadDerivationRoot(bg, f.App, newEnc); lerr != nil || created {
		t.Fatalf("restart with the new key alone: created %t, %v", created, lerr)
	}
	newSecrets, err := discovery.DeriveEdgeSecrets(newEnc)
	if err != nil {
		t.Fatal(err)
	}
	if newSecrets.IssuerKey != secrets.IssuerKey || newSecrets.IssuerSeed != secrets.IssuerSeed ||
		newSecrets.Password != secrets.Password || newSecrets.SysPassword != secrets.SysPassword {
		t.Errorf("issuer stable: %t, passwords stable: %t", newSecrets.IssuerSeed == secrets.IssuerSeed,
			newSecrets.Password == secrets.Password && newSecrets.SysPassword == secrets.SysPassword)
	}
	if lerr := login(newSecrets.Password); lerr != nil {
		t.Errorf("the restarted server's callout login at the running nats-edge: %v", lerr)
	}
	stop = runServer(discovery.NewCA(f.App, newEnc), newSecrets, pki)
	admitted("after the rotation, the rewrap and the restart with the new key alone")
	stop()
	if got, rerr := os.ReadFile(filepath.Join(pki, "edge.conf")); rerr != nil || !bytes.Equal(got, edgeConf) {
		t.Errorf("the restarted server wrote another edge.conf (%v)", rerr)
	}

	// Negative: a tampered root does not open; the start fails and draws
	// no new root.
	var sealed []byte
	if err = f.Admin.QueryRow(bg, `SELECT sealed FROM platform_secret`).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Admin.Exec(bg, `UPDATE platform_secret SET sealed = set_byte(sealed, length(sealed) - 1, get_byte(sealed, length(sealed) - 1) # 1)`); err != nil {
		t.Fatal(err)
	}
	tampered, _ := crypto.NewEnvelopeEncryptor(newKey)
	if created, lerr := crypto.LoadDerivationRoot(bg, f.App, tampered); !errors.Is(lerr, crypto.ErrDerivationRoot) || created {
		t.Errorf("tampered root: created %t, %v; want ErrDerivationRoot", created, lerr)
	}
	var rows int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM platform_secret WHERE sealed <> $1`, sealed).Scan(&rows); err != nil || rows != 1 {
		t.Errorf("after the tampered root: %d rows other than the stored root (%v), want the tampered one only", rows, err)
	}
	// Negative: a replaced root opens but derives other keys.
	replacer, _ := crypto.NewEnvelopeEncryptor(newKey)
	replaced, err := replacer.NewDerivationRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Admin.Exec(bg, `UPDATE platform_secret SET sealed = $1`, replaced); err != nil {
		t.Fatal(err)
	}
	swapped, _ := crypto.NewEnvelopeEncryptor(newKey)
	if _, err = crypto.LoadDerivationRoot(bg, f.App, swapped); err != nil {
		t.Fatal(err)
	}
	swappedSecrets, err := discovery.DeriveEdgeSecrets(swapped)
	if err != nil {
		t.Fatal(err)
	}
	if swappedSecrets.IssuerKey == secrets.IssuerKey || swappedSecrets.Password == secrets.Password {
		t.Fatal("a replaced root derives the same keys")
	}
	if lerr := login(swappedSecrets.Password); lerr == nil {
		t.Error("nats-edge accepted the login derived from a replaced root")
	}
	// The endpoint keeps its files (no PKI directory).
	stop = runServer(discovery.NewCA(f.App, swapped), swappedSecrets, "")
	refused("with the keys of a replaced root")
	stop()
	if _, err = f.Admin.Exec(bg, `UPDATE platform_secret SET sealed = $1`, sealed); err != nil {
		t.Fatal(err)
	}
}
