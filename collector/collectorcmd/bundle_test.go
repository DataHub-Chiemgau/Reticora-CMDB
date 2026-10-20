package collectorcmd

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
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
		if got := collectorBundle.resolve(c.ids); !maps.Equal(got, c.want) {
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
	if err := store.Save(ctx, keystore.Credentials{DeviceID: b.CollectorID, ClientPrivateKeyPEM: key}); err != nil {
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
