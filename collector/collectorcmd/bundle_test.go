package collectorcmd

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	cryptorand "crypto/rand"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/buffer"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/keystore"
)

func loadGoldenBundle(t *testing.T) (*sealedBundle, string) {
	t.Helper()
	raw, err := os.ReadFile("testdata/bundle.json")
	if err != nil {
		t.Fatal(err)
	}
	var b sealedBundle
	if err = json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile("testdata/bundle-key.pem")
	if err != nil {
		t.Fatal(err)
	}
	return &b, string(key)
}

// TestCredentialBundle covers WP-147 (COL-02) on the collector side: a
// bundle sealed by the server (testdata, produced by
// backend/internal/credential.SealBundle) opens with the collector's key
// only; its credentials are kept in memory by ID, and a pass resolves only
// the IDs of its scope, each under the namespace of its kind (WP-378);
// local settings apply only to the pass without server scopes; the bundle
// does not open for another collector id or key.
func TestCredentialBundle(t *testing.T) {
	b, key := loadGoldenBundle(t)
	if strings.Contains(string(b.Ciphertext), "golden") {
		t.Fatal("the bundle carries plaintext")
	}
	content, err := openBundle(b, key)
	if err != nil {
		t.Fatal(err)
	}
	collectorBundle.set(content)
	t.Cleanup(func() { collectorBundle.set(nil) })
	for _, c := range []struct {
		ids  []string
		want map[string]string
	}{
		{[]string{"c1", "c2"}, map[string]string{"snmp.community": "golden-community", "ssh.username": "golden-user", "ssh.password": "golden-pass"}},
		// c2 and c3 are both SSH: the first of the list, not a mix.
		{[]string{"c3", "c2"}, map[string]string{"ssh.username": "other"}},
		{[]string{"c1", "c-unknown"}, map[string]string{"snmp.community": "golden-community"}},
		{nil, map[string]string{}},
	} {
		if got, _ := collectorBundle.resolve(c.ids); !maps.Equal(got, c.want) {
			t.Errorf("resolve(%v) = %v, want %v", c.ids, got, c.want)
		}
	}

	cfg := collectorConfig{Credentials: map[string]string{"ssh.username": "local-user"}}
	if got := credentialsFor(&cfg, nil); !maps.Equal(got, cfg.Credentials) {
		t.Errorf("credentials of the pass without server scopes = %v, want only the local ones", got)
	}
	if got := credentialsFor(&cfg, &collectorScope{CredentialIDs: []string{"c2"}}); !maps.Equal(got,
		map[string]string{"ssh.username": "golden-user", "ssh.password": "golden-pass"}) {
		t.Errorf("credentials of a scope pass = %v, want only its bundle credential", got)
	}

	// Bound to the collector and its key.
	other := *b
	other.CollectorID = "col-other"
	if _, err = openBundle(&other, key); err == nil {
		t.Fatal("the bundle opened for another collector id")
	}
	tampered := *b
	tampered.Ciphertext = append([]byte(nil), b.Ciphertext...)
	tampered.Ciphertext[0] ^= 0xff
	if _, err = openBundle(&tampered, key); err == nil {
		t.Fatal("a modified bundle opened")
	}
	if _, err = openBundle(b, "not a key"); err == nil {
		t.Fatal("the bundle opened without the collector key")
	}
}

// lockedBuffer is a log sink safe for concurrent writers.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// fileViews returns the content of a file under the collector's data
// directory as it would be read back: raw, and for spool messages also the
// decoded payload (base64 in the message JSON, gzip if compressed).
func fileViews(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	views := []string{string(raw)}
	var msg buffer.Message
	if json.Unmarshal(raw, &msg) == nil && len(msg.Payload) > 0 {
		views = append(views, string(msg.Payload))
		if gz, gerr := gzip.NewReader(bytes.NewReader(msg.Payload)); gerr == nil {
			if plain, rerr := io.ReadAll(gz); rerr == nil {
				views = append(views, string(plain))
			}
		}
	}
	return views
}

// TestBundleVaultRefKeepsNamespaceFree covers the bundle of WP-894 on the
// collector side: a vault reference (source vault_ref, no secret) of a kind
// does not take its namespace, so a stored credential of the same namespace
// later in the scope's list still applies; the stored login of the
// connection decodes as an ordinary entry. Reading the vault is WP-895.
func TestBundleVaultRefKeepsNamespaceFree(t *testing.T) {
	var content bundleContent
	if err := json.Unmarshal([]byte(`{"credentials":[
		{"id":"ref","name":"ref","kind":"snmp_v2c","source":"vault_ref",
		 "vault_ref":{"connection_id":"conn","kind":"openbao_kv2","address":"https://vault.example.com","mount":"secret",
		  "path":"network/sw1","field_map":{"community":"snmp_ro"},"auth_method":"approle","login_id":"login"}},
		{"id":"login","name":"login","kind":"vault_approle","source":"stored","secret":{"role_id":"r","secret_id":"s"}},
		{"id":"stored","name":"stored","kind":"snmp_v2c","source":"stored","secret":{"community":"stored-community"}},
		{"id":"legacy","name":"legacy","kind":"ssh_password","secret":{"username":"u","password":"p"}}
	]}`), &content); err != nil {
		t.Fatal(err)
	}
	collectorBundle.set(&content)
	t.Cleanup(func() { collectorBundle.set(nil) })
	for _, c := range []struct {
		ids  []string
		want map[string]string
	}{
		{[]string{"ref", "stored"}, map[string]string{"snmp.community": "stored-community"}},
		{[]string{"stored", "ref"}, map[string]string{"snmp.community": "stored-community"}},
		{[]string{"ref"}, map[string]string{}},
		{[]string{"ref", "legacy"}, map[string]string{"ssh.username": "u", "ssh.password": "p"}},
	} {
		if got, _ := collectorBundle.resolve(c.ids); !maps.Equal(got, c.want) {
			t.Errorf("resolve(%v) = %v, want %v", c.ids, got, c.want)
		}
	}
}

// TestBundlePlaintextOnlyInMemory covers WP-147 (COL-02; WP-378): the
// collector opens its bundle with the key from its keystore and runs a
// discovery pass whose plugins receive the secrets, while the ingest is
// unreachable so the records go to the spool. No file under the
// collector's data directory (keystore, spool, temp) and no log line
// carries a secret of the bundle.
func TestBundlePlaintextOnlyInMemory(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	tmpDir := filepath.Join(dataDir, "tmp")
	if err := os.Mkdir(tmpDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tmpDir)
	logs := &lockedBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	// The keystore holds the collector key the bundle is sealed for.
	b, key := loadGoldenBundle(t)
	store := &keystore.FileStore{Path: filepath.Join(dataDir, "credentials.json")}
	if err := store.Save(ctx, &keystore.Credentials{DeviceID: b.CollectorID, ClientPrivateKeyPEM: key}); err != nil {
		t.Fatal(err)
	}
	identity, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	content, err := openBundle(b, identity.ClientPrivateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	var secrets []string
	for _, cred := range content.Credentials {
		if cred.ID == "c1" || cred.ID == "c2" { // the credentials of the scope below
			for _, v := range cred.Secret {
				if s, ok := v.(string); ok {
					secrets = append(secrets, s)
				}
			}
		}
	}
	if len(secrets) != 3 {
		t.Fatalf("secrets of the golden bundle = %v", secrets)
	}

	marker := "spool-marker-host"
	record := func(protocol string) func(context.Context, string) (*plugins.DeviceRecord, error) {
		return func(_ context.Context, target string) (*plugins.DeviceRecord, error) {
			return plugins.CollectRecord(protocol, &plugins.Result{CIType: "server", Name: marker, IP: target}, nil, time.Now())
		}
	}
	snmpSpy := &auditSpy{name: "snmp", alive: answerAll, collect: record("snmp")}
	sshSpy := &auditSpy{name: "ssh", alive: answerAll, collect: record("ssh")}
	auditUsePlugins(t, snmpSpy, sshSpy)
	collectorBundle.set(content) // as syncBundle does after fetchBundle
	collectorScopes.set([]collectorScope{{ID: "6f0b6f0e-0000-4000-8000-0000000000d1", Kind: "host", Target: "192.0.2.80", Schedule: "24h",
		CredentialIDs: []string{"c1", "c2"}, Plugins: []string{"snmp", "ssh"}}}, nil)
	ingest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(ingest.Close)
	spoolDir := filepath.Join(dataDir, "spool")
	up, err := newUploader(ctx, testConfig(ingest.URL, spoolDir))
	if err != nil {
		t.Fatal(err)
	}
	if n := runDueDiscovery(ctx, &collectorConfig{}, up, newScheduler(), &hostCache{}, time.Now()); n != 2 {
		t.Fatalf("records = %d, want 2", n)
	}

	// Controls: the plugins received the secrets, the records are spooled.
	if seen := snmpSpy.seen("192.0.2.80"); len(seen) == 0 || seen[0]["community"] != "golden-community" {
		t.Fatalf("snmp plugin received %v", seen)
	}
	if seen := sshSpy.seen("192.0.2.80"); len(seen) == 0 || seen[0]["password"] != "golden-pass" {
		t.Fatalf("ssh plugin received %v", seen)
	}
	spooled, files := false, 0
	err = filepath.WalkDir(dataDir, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil || d.IsDir() {
			return werr
		}
		files++
		for _, view := range fileViews(t, path) {
			if strings.HasPrefix(path, spoolDir) && strings.Contains(view, marker) {
				spooled = true
			}
			for _, secret := range secrets {
				if strings.Contains(view, secret) {
					t.Errorf("COL-02 violated: %s carries the bundle secret %q", path, secret)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !spooled {
		t.Fatalf("control: no spooled record under %s (%d files in the data directory)", spoolDir, files)
	}
	out := logs.String()
	if !strings.Contains(out, "discovery results spooled") {
		t.Fatalf("control: the log was not captured:\n%s", out)
	}
	for _, secret := range secrets {
		if strings.Contains(out, secret) {
			t.Errorf("COL-02 violated: the log carries the bundle secret %q", secret)
		}
	}
}

// deviceBundle is a format 2 bundle: the scope credentials scope-snmp and
// scope-ssh and the target credentials of two devices (CRD-04, WP-892).
func deviceBundle() *bundleContent {
	return &bundleContent{
		Credentials: []bundleCredential{
			{ID: "scope-snmp", Kind: "snmp_v2c", Secret: map[string]any{"community": "scope-community"}},
			{ID: "scope-ssh", Kind: "ssh_password", Secret: map[string]any{"username": "scope", "password": "scope-pass"}},
			{ID: "dev-snmp", Kind: "snmp_v3", Secret: map[string]any{"username": "dev-v3", "auth_password": "dev-auth"}},
			{ID: "dev-ssh", Kind: "ssh_key", Secret: map[string]any{"username": "dev", "private_key": "dev-key"}},
		},
		Targets: []bundleTarget{
			{CIID: "ci-1", ScopeID: deviceScopeID, ManagementIP: "192.0.2.91", Namespace: "snmp", CredentialID: "dev-snmp", Origin: "ci"},
			{CIID: "ci-2", ScopeID: deviceScopeID, ManagementIP: "192.0.2.92", Namespace: "ssh", CredentialID: "dev-ssh", Origin: "subnet"},
			// A target whose credential is not in the bundle: the scope's.
			{CIID: "ci-3", ScopeID: deviceScopeID, ManagementIP: "192.0.2.93", Namespace: "snmp", CredentialID: "missing", Origin: "ci"},
		},
	}
}

const deviceScopeID = "6f0b6f0e-0000-4000-8000-0000000000e1"

var deviceScope = &collectorScope{ID: deviceScopeID, Kind: "cidr", Target: "192.0.2.0/24",
	CredentialIDs: []string{"scope-snmp", "scope-ssh"}}

// TestCredentialsForTargetOverridesScope covers CRD-04 AK1/AK2 (WP-892): a
// device with a target credential gets it in place of the scope's for its
// namespace, by management address and by CI id; the scope's other keys of
// that namespace do not mix in; the pass without server scopes uses only
// the local settings.
func TestCredentialsForTargetOverridesScope(t *testing.T) {
	collectorBundle.set(deviceBundle())
	t.Cleanup(func() { collectorBundle.set(nil) })
	cfg := collectorConfig{Credentials: map[string]string{"snmp.community": "local"}}
	want := map[string]string{"snmp.username": "dev-v3", "snmp.auth_password": "dev-auth",
		"ssh.username": "scope", "ssh.password": "scope-pass"}
	if got, _ := credentialsForTarget(&cfg, deviceScope, "192.0.2.91", ""); !maps.Equal(got, want) {
		t.Errorf("by address: %v, want %v", got, want)
	}
	if got, _ := credentialsForTarget(&cfg, deviceScope, "", "ci-1"); !maps.Equal(got, want) {
		t.Errorf("by CI id: %v, want %v", got, want)
	}
	if got, _ := credentialsForTarget(&cfg, nil, "192.0.2.91", "ci-1"); !maps.Equal(got, cfg.Credentials) {
		t.Errorf("pass without server scopes: %v, want only the local settings", got)
	}
	// The scope's list stays as it is (credentialsFor unchanged).
	if got := credentialsFor(&cfg, deviceScope); got["snmp.community"] != "scope-community" {
		t.Errorf("credentialsFor = %v, want the scope's", got)
	}
}

// TestCredentialsForTargetNamespaceFallback covers CRD-04 AK2 (WP-892):
// namespaces without a target entry fall back to the scope's list; an SSH
// target credential does not displace the scope's SNMP credential; a
// device without entries and one whose target credential is missing from
// the bundle resolve per scope.
func TestCredentialsForTargetNamespaceFallback(t *testing.T) {
	collectorBundle.set(deviceBundle())
	t.Cleanup(func() { collectorBundle.set(nil) })
	cfg := collectorConfig{}
	scopeOnly := map[string]string{"snmp.community": "scope-community", "ssh.username": "scope", "ssh.password": "scope-pass"}
	want := map[string]string{"snmp.community": "scope-community", "ssh.username": "dev", "ssh.private_key": "dev-key"}
	if got, _ := credentialsForTarget(&cfg, deviceScope, "192.0.2.92", ""); !maps.Equal(got, want) {
		t.Errorf("SSH target: %v, want %v", got, want)
	}
	for _, ip := range []string{"192.0.2.10", "192.0.2.93"} {
		if got, _ := credentialsForTarget(&cfg, deviceScope, ip, ""); !maps.Equal(got, scopeOnly) {
			t.Errorf("%s: %v, want the scope's %v", ip, got, scopeOnly)
		}
		if key := collectorBundle.targetKey(deviceScope, ip, ""); key != "" {
			t.Errorf("%s: resolution key %q, want none", ip, key)
		}
	}
	if a, b := collectorBundle.targetKey(deviceScope, "192.0.2.91", ""), collectorBundle.targetKey(deviceScope, "192.0.2.92", ""); a == "" || a == b {
		t.Errorf("resolution keys %q and %q: want two different ones", a, b)
	}
}

// TestBundleFormat1HasNoTargets covers CRD-04 AK1 (WP-892): a format 1
// bundle (the golden bundle) gives empty target indexes, every device
// resolves per scope; a format 2 bundle replaces the indexes in one step,
// and an address named for two CIs is ambiguous and resolves per scope.
func TestBundleFormat1HasNoTargets(t *testing.T) {
	b, key := loadGoldenBundle(t)
	content, err := openBundle(b, key)
	if err != nil {
		t.Fatal(err)
	}
	if b.Version != 1 || content.Targets != nil {
		t.Fatalf("golden bundle: version %d, targets %v; want format 1 without targets", b.Version, content.Targets)
	}
	collectorBundle.set(deviceBundle())
	t.Cleanup(func() { collectorBundle.set(nil) })
	collectorBundle.set(content)
	collectorBundle.mu.RLock()
	scopes := len(collectorBundle.scopes)
	collectorBundle.mu.RUnlock()
	if scopes != 0 {
		t.Fatalf("after a format 1 bundle the indexes hold targets of %d scopes, want none", scopes)
	}
	scope := &collectorScope{CredentialIDs: []string{"c1"}}
	if got, _ := credentialsForTarget(&collectorConfig{}, scope, "192.0.2.91", "ci-1"); !maps.Equal(got, credentialsFor(&collectorConfig{}, scope)) {
		t.Errorf("format 1: %v, want the scope's %v", got, credentialsFor(&collectorConfig{}, scope))
	}

	dup := deviceBundle()
	dup.Targets = append(dup.Targets, bundleTarget{CIID: "ci-9", ScopeID: deviceScopeID, ManagementIP: "192.0.2.91", Namespace: "snmp", CredentialID: "dev-snmp", Origin: "ci"})
	collectorBundle.set(dup)
	if key := collectorBundle.targetKey(deviceScope, "192.0.2.91", ""); key != "" {
		t.Errorf("an address of two CIs resolves to %q, want per scope", key)
	}
	if key := collectorBundle.targetKey(deviceScope, "", "ci-9"); key == "" {
		t.Error("the CI id index lost ci-9")
	}
}

// TestTargetCredentialsStayInTheirScope covers CRD-04 AK2 (WP-892): a
// target applies only in the pass of the scope the server resolved it in.
// Two scopes of different clients (and VRFs) hold the same address; the
// device of client X has a CI binding, the one of client Y none: the pass
// of client Y's scope uses its scope credentials, never client X's
// community, by address and by CI id alike. A target without a scope
// applies nowhere.
func TestTargetCredentialsStayInTheirScope(t *testing.T) {
	scopeX := &collectorScope{ID: "6f0b6f0e-0000-4000-8000-0000000000a1", Kind: "cidr", Target: "10.0.0.0/24", ClientID: "client-x",
		VRFID: "vrf-x", CredentialIDs: []string{"x-scope"}}
	scopeY := &collectorScope{ID: "6f0b6f0e-0000-4000-8000-0000000000a2", Kind: "cidr", Target: "10.0.0.0/24", ClientID: "client-y",
		VRFID: "vrf-y", CredentialIDs: []string{"y-scope"}}
	collectorBundle.set(&bundleContent{
		Credentials: []bundleCredential{
			{ID: "x-scope", Kind: "snmp_v2c", Secret: map[string]any{"community": "x-scope-community"}},
			{ID: "y-scope", Kind: "snmp_v2c", Secret: map[string]any{"community": "y-scope-community"}},
			{ID: "x-device", Kind: "snmp_v2c", Secret: map[string]any{"community": "x-device-community"}},
		},
		Targets: []bundleTarget{
			{CIID: "ci-x", ScopeID: scopeX.ID, ManagementIP: "10.0.0.5", Namespace: "snmp", CredentialID: "x-device", Origin: "ci"},
			{CIID: "ci-unscoped", ManagementIP: "10.0.0.6", Namespace: "snmp", CredentialID: "x-device", Origin: "ci"},
		},
	})
	t.Cleanup(func() { collectorBundle.set(nil) })
	cfg := &collectorConfig{}
	if got, _ := credentialsForTarget(cfg, scopeX, "10.0.0.5", ""); got["snmp.community"] != "x-device-community" {
		t.Errorf("pass of scope X: %v, want the target credential of client X's device", got)
	}
	for _, c := range []struct{ ip, ci string }{{"10.0.0.5", ""}, {"10.0.0.5", "ci-x"}, {"10.0.0.6", "ci-unscoped"}} {
		if got, _ := credentialsForTarget(cfg, scopeY, c.ip, c.ci); !maps.Equal(got, map[string]string{"snmp.community": "y-scope-community"}) {
			t.Errorf("pass of scope Y, %s/%q: %v, want only scope Y's community", c.ip, c.ci, got)
		}
		if key := collectorBundle.targetKey(scopeY, c.ip, c.ci); key != "" {
			t.Errorf("pass of scope Y, %s/%q: resolution key %q, want none", c.ip, c.ci, key)
		}
	}
	if got, _ := credentialsForTarget(cfg, scopeX, "10.0.0.6", ""); got["snmp.community"] != "x-scope-community" {
		t.Errorf("a target without a scope: %v, want the scope's credentials", got)
	}
}

// TestHostScopeByNameUsesTargetCredential covers CRD-04 (WP-892): a host
// scope given by name covers the CI the server matched by host name; its
// targets apply to the name of the probe and to the address of the
// collection alike. With two CIs (an ambiguous name) the scope's
// credentials apply.
func TestHostScopeByNameUsesTargetCredential(t *testing.T) {
	scope := &collectorScope{ID: "6f0b6f0e-0000-4000-8000-0000000000b1", Kind: "host", Target: "printer.example",
		CredentialIDs: []string{"scope-snmp"}}
	content := &bundleContent{
		Credentials: []bundleCredential{
			{ID: "scope-snmp", Kind: "snmp_v2c", Secret: map[string]any{"community": "scope-community"}},
			{ID: "dev-snmp", Kind: "snmp_v2c", Secret: map[string]any{"community": "device-community"}},
		},
		Targets: []bundleTarget{{CIID: "ci-printer", ScopeID: scope.ID, ManagementIP: "192.0.2.150", Namespace: "snmp",
			CredentialID: "dev-snmp", Origin: "ci"}},
	}
	collectorBundle.set(content)
	t.Cleanup(func() { collectorBundle.set(nil) })
	for _, target := range []string{"printer.example", "192.0.2.150", "192.0.2.151"} {
		if got, _ := credentialsForTarget(&collectorConfig{}, scope, target, ""); got["snmp.community"] != "device-community" {
			t.Errorf("%s: %v, want the device's community", target, got)
		}
	}
	if a, b := collectorBundle.targetKey(scope, "printer.example", ""), collectorBundle.targetKey(scope, "192.0.2.150", ""); a == "" || a != b {
		t.Errorf("resolution keys of name %q and address %q: want the same, non-empty", a, b)
	}
	content.Targets = append(content.Targets, bundleTarget{CIID: "ci-other", ScopeID: scope.ID, ManagementIP: "192.0.2.151",
		Namespace: "snmp", CredentialID: "dev-snmp", Origin: "ci"})
	collectorBundle.set(content)
	if got, _ := credentialsForTarget(&collectorConfig{}, scope, "printer.example", ""); got["snmp.community"] != "scope-community" {
		t.Errorf("ambiguous name: %v, want the scope's community", got)
	}
}

// TestBundleCacheKeepsResolvedReferences covers WP-895 (AK3): a new
// bundle with the same reference and the same login keeps the values
// resolved in memory until the resolver reads it again; a changed
// reference or a changed login drops them.
func TestBundleCacheKeepsResolvedReferences(t *testing.T) {
	useVaultResolver(t)
	_, srv := newTestVault(t)
	conn := vaultConn(srv)
	content := func(path, secretID string) *bundleContent {
		return &bundleContent{Credentials: []bundleCredential{
			vaultLogin(secretID),
			vaultRefCred("ref", "snmp_v2c", conn, path, map[string]string{"community": "snmp_ro"}),
		}}
	}
	collectorBundle.set(content("network/sw1", testSecretID))
	if out := collectorVault.resolveBundle(context.Background(), collectorBundle); out["ref"] != "ok" {
		t.Fatalf("resolution = %v", out)
	}
	collectorBundle.set(content("network/sw1", testSecretID))
	if got, _ := collectorBundle.resolve([]string{"ref"}); got["snmp.community"] != testValue {
		t.Fatalf("the same reference lost its values: %v", got)
	}
	collectorBundle.set(content("network/sw2", testSecretID))
	if got, _ := collectorBundle.resolve([]string{"ref"}); len(got) != 0 {
		t.Fatalf("a changed path kept the values: %v", got)
	}
	collectorBundle.set(content("network/sw1", testSecretID))
	collectorVault.resolveBundle(context.Background(), collectorBundle)
	collectorBundle.set(content("network/sw1", "rotated-secret-id"))
	if got, _ := collectorBundle.resolve([]string{"ref"}); len(got) != 0 {
		t.Fatalf("a changed login kept the values: %v", got)
	}
}

// bundleServer answers the bundle requests of the golden collector on a
// NATS server like the platform and counts them; it returns the
// collector's credentials (certificate of its subject tree), the
// connection and the subject tree.
func bundleServer(t *testing.T) (creds *keystore.Credentials, nc *nats.Conn, subject string, fetches func() int) {
	t.Helper()
	b, key := loadGoldenBundle(t)
	org := fmt.Sprintf("org-%d", time.Now().UnixNano())
	pub, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := newAuditPKI(t).issue(t, &pub.PublicKey, pkix.Name{Organization: []string{org}, CommonName: b.CollectorID},
		time.Now().Add(-time.Hour), time.Now().Add(time.Hour), false)
	creds = &keystore.Credentials{DeviceID: b.CollectorID, ClientCertificatePEM: cert, ClientPrivateKeyPEM: key}
	if nc, err = nats.Connect(auditNATSURL(t), nats.Timeout(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	subject = "collector." + org + "." + b.CollectorID
	raw, err := json.Marshal(map[string]any{"bundle": b})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	count := 0
	sub, err := nc.Subscribe(subject+".credentials.request", func(m *nats.Msg) {
		mu.Lock()
		count++
		mu.Unlock()
		_ = m.Respond(raw)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err = nc.Flush(); err != nil {
		t.Fatal(err)
	}
	return creds, nc, subject, func() int {
		mu.Lock()
		defer mu.Unlock()
		return count
	}
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, within time.Duration, what string, cond func() bool) time.Duration {
	t.Helper()
	start := time.Now()
	for !cond() {
		if time.Since(start) > within {
			t.Fatalf("%s not within %v", what, within)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return time.Since(start)
}

// TestBundleChangedTriggersReload covers AK2 of WP-897: the collector
// subscribes bundle.changed in its session; two notices within the random
// delay lead to one fetch of the bundle, a later notice to another.
func TestBundleChangedTriggersReload(t *testing.T) {
	t.Cleanup(func() { collectorBundle.set(nil) })
	jitter := bundleReloadJitter
	bundleReloadJitter = 300 * time.Millisecond
	t.Cleanup(func() { bundleReloadJitter = jitter })
	creds, nc, subject, fetches := bundleServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); syncBundle(ctx, nc, creds) }()
	t.Cleanup(func() { cancel(); <-done })
	waitFor(t, 5*time.Second, "the first fetch of the session", func() bool { return fetches() == 1 })
	// The subscription is set up before the first fetch.
	for range 2 {
		if err := nc.Publish(subject+".bundle.changed", []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "the reload", func() bool { return fetches() >= 2 })
	time.Sleep(2 * bundleReloadJitter)
	if n := fetches(); n != 2 {
		t.Fatalf("fetches after two notices = %d, want 2 (one reload)", n)
	}
	if err := nc.Publish(subject+".bundle.changed", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "the reload of a later notice", func() bool { return fetches() == 3 })
	if got, _ := collectorBundle.resolve([]string{"c1"}); got["snmp.community"] != "golden-community" {
		t.Fatalf("the reloaded bundle is not in memory: %v", got)
	}
}

// TestBundleChangedWithinSixtySeconds covers AK2 of WP-897: from the notice
// to the new bundle in memory it takes at most the reload delay (0–20 s)
// plus one bundle request (bounded by bundleFetchTimeout), below 60 s;
// measured with the longest delay drawn, the reload lands within it.
func TestBundleChangedWithinSixtySeconds(t *testing.T) {
	if bundleReloadJitter+bundleFetchTimeout >= 60*time.Second {
		t.Fatalf("reload delay %v plus fetch timeout %v reach 60 s", bundleReloadJitter, bundleFetchTimeout)
	}
	for range 100 {
		if d := bundleReloadDelay(); d < 0 || d > bundleReloadJitter {
			t.Fatalf("reload delay %v outside 0–%v", d, bundleReloadJitter)
		}
	}
	t.Cleanup(func() { collectorBundle.set(nil) })
	jitter, delay := bundleReloadJitter, bundleReloadDelay
	bundleReloadJitter = 500 * time.Millisecond
	bundleReloadDelay = func() time.Duration { return bundleReloadJitter } // the longest delay
	t.Cleanup(func() { bundleReloadJitter, bundleReloadDelay = jitter, delay })
	creds, nc, subject, fetches := bundleServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); syncBundle(ctx, nc, creds) }()
	t.Cleanup(func() { cancel(); <-done })
	waitFor(t, 5*time.Second, "the first fetch", func() bool { return fetches() == 1 })
	collectorBundle.set(nil)
	if err := nc.Publish(subject+".bundle.changed", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	took := waitFor(t, bundleReloadJitter+5*time.Second, "the new bundle in memory", func() bool {
		got, _ := collectorBundle.resolve([]string{"c1"})
		return got["snmp.community"] == "golden-community"
	})
	if took < bundleReloadJitter-50*time.Millisecond {
		t.Fatalf("the reload came after %v, before its delay of %v", took, bundleReloadJitter)
	}
}
