package discovery

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// TestNormalizeEdgeHost covers the names the endpoint's server certificate
// takes from RETICORA_NATS_EDGE_HOSTS (COL-08): IP addresses in their
// canonical form, also in brackets or with a port; DNS names trimmed, in
// lower case, without port and trailing dot; empty entries skipped.
func TestNormalizeEdgeHost(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"192.0.2.10", "192.0.2.10"},
		{" 192.0.2.10 ", "192.0.2.10"},
		{"192.0.2.10:4222", "192.0.2.10"},
		{"::ffff:192.0.2.10", "192.0.2.10"},
		{"2001:0DB8::0001", "2001:db8::1"},
		{"[2001:db8::1]", "2001:db8::1"},
		{"[2001:db8::1]:4222", "2001:db8::1"},
		{"::1", "::1"},
		{"[::1]", "::1"},
		{"Cmdb.Example.COM", "cmdb.example.com"},
		{"cmdb.example.com.", "cmdb.example.com"},
		{"nats-edge:4222", "nats-edge"},
		{"*.Example.com", "*.example.com"},
		// Not a port: the entry stays as it is (lower case) and is compared
		// as written.
		{"edge.example.com:http", "edge.example.com:http"},
		{"", ""},
		{"   ", ""},
	} {
		if got := normalizeEdgeHost(tc.in); got != tc.want {
			t.Errorf("normalizeEdgeHost(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestEdgeServerHostsAlwaysNameTheEndpoint covers contract C1 (COL-08):
// the server certificate always names EdgeServerName besides the configured
// hosts, each name once, also without a configured host.
func TestEdgeServerHostsAlwaysNameTheEndpoint(t *testing.T) {
	if got := edgeServerHosts(nil); !slices.Equal(got, []string{EdgeServerName}) {
		t.Errorf("edgeServerHosts(nil) = %v, want [%s]", got, EdgeServerName)
	}
	got := edgeServerHosts([]string{"localhost", "", "nats-edge", "LOCALHOST", " ", "192.0.2.10:4222", "192.0.2.10", "Reticora-Collector-Nats"})
	if want := []string{"localhost", "nats-edge", "192.0.2.10", EdgeServerName}; !slices.Equal(got, want) {
		t.Errorf("edgeServerHosts = %v, want %v", got, want)
	}
}

// testEdgeCAState is a CA state with one self-signed CA that is active,
// edge issuer and the only trust anchor, as collector_ca holds it after the
// first start; without a database.
func testEdgeCAState(t *testing.T) *caState {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := randomSerial()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Reticora Collector CA test", Organization: []string{"Reticora"}},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(CAValidity), KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true, IsCA: true, MaxPathLenZero: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	ca := &caCert{id: "ca-1", cert: cert, certPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), key: key, createdAt: now}
	return &caState{version: "ca-1:active", active: ca, edge: ca, anchors: []*caCert{ca}}
}

// edgeLeaf reads the server certificate of an exported PKI directory and
// checks that its key matches.
func edgeLeaf(t *testing.T, dir string) (*x509.Certificate, string) {
	t.Helper()
	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, "server.pem"), filepath.Join(dir, "server-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "server.pem"))
	if err != nil {
		t.Fatal(err)
	}
	return leaf, string(raw)
}

// TestEdgeServerCertificateFollowsTheHosts covers the reuse of the
// endpoint's server key pair against the configured hosts (COL-08, contract
// C2) without a database: a certificate is kept while it names every
// configured host and EdgeServerName however they are written (a subset of
// its names is enough), and replaced, with changed set and the former
// certificate as served so that a running endpoint reloads it (RunEdge),
// when a host is missing or a certificate of an earlier version lacks
// EdgeServerName: exactly once, the next export keeps it again. Entries no
// certificate verifies do not reissue it on every export.
func TestEdgeServerCertificateFollowsTheHosts(t *testing.T) {
	st := testEdgeCAState(t)
	sec := &EdgeSecrets{IssuerKey: "AISSUER", Password: "auth", SysPassword: "sys"}
	dir := filepath.Join(t.TempDir(), "pki")
	certPath, keyPath := filepath.Join(dir, "server.pem"), filepath.Join(dir, "server-key.pem")
	roots := x509.NewCertPool()
	roots.AddCert(st.edge.cert)
	export := func(hosts ...string) *edgeExport {
		t.Helper()
		now := time.Now()
		ex, err := writeEdgePKI(st, sec, dir, hosts, now, now)
		if err != nil {
			t.Fatal(err)
		}
		return ex
	}
	verifies := func(leaf *x509.Certificate, name string) bool {
		_, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: name, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
		return err == nil
	}

	hosts := []string{"localhost", "", "192.0.2.10", "2001:db8::1", "Cmdb.Example.com", "nats-edge"}
	if ex := export(hosts...); !ex.changed || ex.served != nil {
		t.Fatalf("first export: changed %v, served %v; want a new pair and none served before", ex.changed, ex.served != nil)
	}
	leaf, first := edgeLeaf(t, dir)
	if leaf.Subject.CommonName != EdgeServerName {
		t.Errorf("common name %q, want %q", leaf.Subject.CommonName, EdgeServerName)
	}
	for _, name := range []string{"localhost", "192.0.2.10", "2001:db8::1", "[2001:db8::1]", "cmdb.example.com", "CMDB.example.com", "nats-edge", EdgeServerName} {
		if !verifies(leaf, name) {
			t.Errorf("the server certificate does not verify for %q (DNS %v, IP %v)", name, leaf.DNSNames, leaf.IPAddresses)
		}
	}

	// Positive: the same hosts, a subset of them and the same hosts written
	// differently keep the pair; nothing changed, no reload.
	for _, same := range [][]string{
		hosts,
		{"localhost"},
		nil,
		{"LOCALHOST.", "192.0.2.10:4222", "[2001:db8::1]", " cmdb.example.com ", "::ffff:192.0.2.10", EdgeServerName},
	} {
		if ex := export(same...); ex.changed {
			t.Errorf("export with %q replaced files of a pair that names every host", same)
		}
		if _, cur := edgeLeaf(t, dir); cur != first {
			t.Fatalf("export with %q replaced the server certificate", same)
		}
	}
	if !keepServerPair(certPath, keyPath, st.edge.cert, hosts, time.Now()) {
		t.Error("keepServerPair refuses a pair that names every host")
	}

	// Negative: a host is missing (the installer added the public IP).
	changed := append(slices.Clone(hosts), "198.51.100.7")
	if keepServerPair(certPath, keyPath, st.edge.cert, changed, time.Now()) {
		t.Fatal("keepServerPair keeps a pair that does not name 198.51.100.7")
	}
	ex := export(changed...)
	if !ex.changed || string(ex.served) != first {
		t.Fatalf("hosts changed: changed %v, served the former certificate %v; want a reload of the new pair", ex.changed, string(ex.served) == first)
	}
	leaf, second := edgeLeaf(t, dir)
	if second == first || !verifies(leaf, "198.51.100.7") || !verifies(leaf, EdgeServerName) || !verifies(leaf, "cmdb.example.com") {
		t.Fatalf("after the host change the certificate (DNS %v, IP %v) does not name the new host and the former ones", leaf.DNSNames, leaf.IPAddresses)
	}
	if ex = export(changed...); ex.changed {
		t.Error("the export after the reissue replaced the pair again")
	}

	// Negative: a certificate of an earlier version names the hosts but not
	// EdgeServerName; it is replaced once, then kept.
	oldCert, oldKey, err := issueWithKey(st.edge, EdgeServerName, []string{"localhost", "nats-edge"}, EdgeServerCertValidity, x509.ExtKeyUsageServerAuth, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = writeServerPair(certPath, []byte(oldCert), keyPath, []byte(oldKey)); err != nil {
		t.Fatal(err)
	}
	if keepServerPair(certPath, keyPath, st.edge.cert, []string{"localhost", "nats-edge"}, time.Now()) {
		t.Fatal("keepServerPair keeps a certificate without " + EdgeServerName)
	}
	if ex = export("localhost", "nats-edge"); !ex.changed || string(ex.served) != oldCert {
		t.Fatalf("certificate without %s: changed %v; want it replaced and reloaded", EdgeServerName, ex.changed)
	}
	leaf, third := edgeLeaf(t, dir)
	if !verifies(leaf, EdgeServerName) || !verifies(leaf, "nats-edge") {
		t.Fatalf("the reissued certificate (DNS %v) does not name %s and nats-edge", leaf.DNSNames, EdgeServerName)
	}
	for range 3 {
		if ex = export("localhost", "nats-edge"); ex.changed {
			t.Fatal("a certificate of an earlier version was reissued more than once")
		}
	}
	if _, cur := edgeLeaf(t, dir); cur != third {
		t.Fatal("the reissued certificate was replaced again")
	}

	// Entries that no certificate verifies (a wildcard, a host with a port
	// that is no number) are compared as names: one reissue, then the pair
	// stays.
	odd := []string{"*.example.com", "edge.example.com:http", "nats-edge"}
	if ex = export(odd...); !ex.changed {
		t.Fatal("new names did not reissue the certificate")
	}
	_, fourth := edgeLeaf(t, dir)
	for range 3 {
		if ex = export(odd...); ex.changed {
			t.Fatalf("hosts %q reissue the certificate on every export", odd)
		}
	}
	if _, cur := edgeLeaf(t, dir); cur != fourth {
		t.Fatal("the certificate for the odd hosts was replaced again")
	}
}
