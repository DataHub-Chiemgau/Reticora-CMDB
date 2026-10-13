package collectorcmd

import (
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/buffer"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/keystore"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/update"
)

func testConfig(serverURL, spoolDir string) collectorConfig {
	return collectorConfig{
		ServerURL:      serverURL,
		OrganizationID: "org-1",
		CollectorID:    "collector-1",
		SpoolDir:       spoolDir,
	}
}

func TestUploadRecords_PlainHTTPSuccess(t *testing.T) {
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Errorf("read body: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	up, err := newUploader(context.Background(), testConfig(srv.URL, ""))
	if err != nil {
		t.Fatalf("newUploader: %v", err)
	}
	if up.mtls {
		t.Error("expected plain HTTP client when no certificate material is configured")
	}

	if err := up.uploadRecords(context.Background(), testRecords("r")); err != nil {
		t.Fatalf("uploadRecords: %v", err)
	}
	if gotHeaders.Get("Content-Encoding") != "gzip" {
		t.Errorf("expected gzip content encoding, got %q", gotHeaders.Get("Content-Encoding"))
	}
	if gotHeaders.Get("X-Organization-ID") != "org-1" || gotHeaders.Get("X-Collector-ID") != "collector-1" {
		t.Errorf("missing tenant/collector headers: %v", gotHeaders)
	}
}

func TestUploadRecords_FailureSpoolsAndFlushDelivers(t *testing.T) {
	spoolDir := t.TempDir()

	// First attempt against an unreachable server spools the batch.
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	downURL := down.URL
	down.Close()

	up, err := newUploader(context.Background(), testConfig(downURL, spoolDir))
	if err != nil {
		t.Fatalf("newUploader: %v", err)
	}
	if err := up.uploadRecords(context.Background(), testRecords("r")); err == nil {
		t.Fatal("expected upload error against unreachable server")
	}
	spool := &buffer.DiskBuffer{Dir: spoolDir}
	if n, _ := spool.Len(context.Background()); n != 1 {
		t.Fatalf("expected 1 spooled message, got %d", n)
	}

	// Once the backend is reachable again, flushSpool delivers and acks it.
	var delivered int
	up2srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delivered++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer up2srv.Close()

	up.cfg.ServerURL = up2srv.URL
	up.flushSpool(context.Background())
	if delivered != 1 {
		t.Fatalf("expected 1 flushed delivery, got %d", delivered)
	}
	if n, _ := spool.Len(context.Background()); n != 0 {
		t.Fatalf("expected empty spool after flush, got %d", n)
	}
}

func TestUploadRecords_WithoutSpoolDirReturnsErrorOnly(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	downURL := down.URL
	down.Close()

	up, err := newUploader(context.Background(), testConfig(downURL, ""))
	if err != nil {
		t.Fatalf("newUploader: %v", err)
	}
	if err := up.uploadRecords(context.Background(), testRecords("r")); err == nil {
		t.Fatal("expected upload error against unreachable server")
	}
}

func TestLoadTLSMaterial_FromKeystore(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials.json")
	certPEM, keyPEM := selfSignedCert(t, "collector-keystore")

	store := &keystore.FileStore{Path: credPath}
	if err := store.Save(context.Background(), keystore.Credentials{
		DeviceID:             "collector-1",
		ClientCertificatePEM: string(certPEM),
		ClientPrivateKeyPEM:  string(keyPEM),
	}); err != nil {
		t.Fatalf("save credentials: %v", err)
	}

	cfg := testConfig("", "")
	cfg.CredentialsPath = credPath
	cert, key, _, err := loadTLSMaterial(context.Background(), cfg)
	if err != nil {
		t.Fatalf("loadTLSMaterial: %v", err)
	}
	if string(cert) != string(certPEM) || string(key) != string(keyPEM) {
		t.Error("keystore material not loaded")
	}
}

func TestLoadTLSMaterial_IncompletePairFails(t *testing.T) {
	cfg := testConfig("", "")
	cfg.TLSCertPEM = []byte("cert-only")
	if _, _, _, err := loadTLSMaterial(context.Background(), cfg); err == nil {
		t.Fatal("expected error when only the certificate is provided")
	}
}

func TestNewUploader_UsesMTLSWhenConfigured(t *testing.T) {
	certPEM, keyPEM := selfSignedCert(t, "collector-mtls")

	cfg := testConfig("", "")
	cfg.TLSCertPEM = certPEM
	cfg.TLSKeyPEM = keyPEM

	up, err := newUploader(context.Background(), cfg)
	if err != nil {
		t.Fatalf("newUploader: %v", err)
	}
	if !up.mtls {
		t.Fatal("expected mTLS transport when certificate material is configured")
	}
	transport, ok := up.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", up.client.Transport)
	}
	if transport.TLSClientConfig == nil || len(transport.TLSClientConfig.Certificates) != 1 {
		t.Fatal("expected client certificate in TLS config")
	}
	if transport.TLSClientConfig.MinVersion != tls.VersionTLS13 {
		t.Errorf("expected TLS 1.3 minimum, got %x", transport.TLSClientConfig.MinVersion)
	}
}

func TestNewUploader_InvalidKeyPairFails(t *testing.T) {
	cfg := testConfig("", "")
	cfg.TLSCertPEM = []byte("not a cert")
	cfg.TLSKeyPEM = []byte("not a key")
	if _, err := newUploader(context.Background(), cfg); err == nil {
		t.Fatal("expected error for invalid key pair")
	}
}

// selfSignedCert generates an ephemeral ECDSA key pair for tests.
func selfSignedCert(t *testing.T, cn string) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// Ensure the upload payload round-trips as a DeviceRecord batch.
func TestPostPayload_SendsCompressedJSON(t *testing.T) {
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Errorf("gzip reader: %v", err)
			return
		}
		defer gz.Close()
		raw, err = io.ReadAll(gz)
		if err != nil {
			t.Errorf("read gzip: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	up, err := newUploader(context.Background(), testConfig(srv.URL, ""))
	if err != nil {
		t.Fatalf("newUploader: %v", err)
	}
	payload, _ := json.Marshal(plugins.DeviceRecordBatch{Records: testRecords("r")})
	if err := up.postPayload(context.Background(), payload); err != nil {
		t.Fatalf("postPayload: %v", err)
	}
	var decoded plugins.DeviceRecordBatch
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("server received invalid JSON: %v", err)
	}
	if len(decoded.Records) != 1 {
		t.Errorf("expected 1 record, got %d", len(decoded.Records))
	}
}

func TestCredentialsFromEnvNamespacesPerProtocol(t *testing.T) {
	env := map[string]string{
		"RETICORA_SSH_USERNAME":   "root",
		"RETICORA_SSH_PASSWORD":   "ssh-secret",
		"RETICORA_SNMP_COMMUNITY": "public",
	}
	creds := credentialsFromEnv(func(k string) string { return env[k] })
	if creds["ssh.username"] != "root" || creds["snmp.community"] != "public" {
		t.Fatalf("credentials = %v", creds)
	}
	for key := range creds {
		if strings.HasPrefix(key, "redfish.") || !strings.Contains(key, ".") {
			t.Fatalf("unexpected credential key %q: SSH settings must not become Redfish or shared credentials", key)
		}
	}
}

func TestParseRedfishTLSScopes(t *testing.T) {
	pin := strings.Repeat("ab", 32)
	files := map[string][]byte{}
	readFile := func(path string) ([]byte, error) {
		if data, ok := files[path]; ok {
			return data, nil
		}
		return nil, os.ErrNotExist
	}
	scopes, err := parseRedfishTLSScopes("10.0.20.5/32=pin:"+pin+"; ", readFile)
	if err != nil || len(scopes) != 1 || len(scopes[0].PinsSHA256) != 1 || scopes[0].Network.String() != "10.0.20.5/32" {
		t.Fatalf("scopes = %+v, err = %v", scopes, err)
	}
	if scopes, err := parseRedfishTLSScopes("", readFile); err != nil || len(scopes) != 0 {
		t.Fatalf("empty value = %+v, %v", scopes, err)
	}
	for _, bad := range []string{
		"10.0.0.0/24",                 // no rule
		"10.0.0.0/33=pin:" + pin,      // bad CIDR
		"10.0.0.0/24=pin:abcd",        // short pin
		"10.0.0.0/24=ca:/missing.pem", // unreadable CA
		"10.0.0.0/24=insecure:true",   // unknown rule
	} {
		if _, err := parseRedfishTLSScopes(bad, readFile); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

// COL-05: new results queue behind the spool backlog, so the backend receives
// batches in source-time order; one flush delivers more than 16 batches.
func TestUploadRecords_BacklogFirstAndUnboundedFlush(t *testing.T) {
	spoolDir := t.TempDir()
	var order []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Errorf("gzip: %v", err)
			return
		}
		var batch plugins.DeviceRecordBatch
		if err := json.NewDecoder(gz).Decode(&batch); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		order = append(order, batch.Records[0].System.Name)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	up, err := newUploader(context.Background(), testConfig(srv.URL, spoolDir))
	if err != nil {
		t.Fatalf("newUploader: %v", err)
	}
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 20; i++ {
		payload, _ := json.Marshal(plugins.DeviceRecordBatch{Records: testRecords(fmt.Sprintf("old-%02d", i))})
		if err := up.spoolResults(context.Background(), payload, base.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("spool: %v", err)
		}
	}

	if err := up.uploadRecords(context.Background(), testRecords("new")); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if len(order) != 21 || order[0] != "old-00" || order[19] != "old-19" || order[20] != "new" {
		t.Fatalf("delivery order = %v, want 20 backlog batches then the new one", order)
	}
	if n, _ := up.spool.Len(context.Background()); n != 0 {
		t.Fatalf("spool not drained: %d", n)
	}
}

// NFR-04/OPS-06: a saturated spool pauses collection and the heartbeat reports
// backlog, backpressure and losses to the server.
func TestHeartbeatReportsSpoolBackpressureAndLoss(t *testing.T) {
	spoolDir := t.TempDir()
	var body heartbeatBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode heartbeat: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	cfg := testConfig(srv.URL, spoolDir)
	up, err := newUploader(context.Background(), cfg)
	if err != nil {
		t.Fatalf("newUploader: %v", err)
	}
	if err := up.spoolResults(context.Background(), []byte(`[{"Name":"a"}]`), time.Now()); err != nil {
		t.Fatalf("spool: %v", err)
	}
	stats, _ := up.spool.Stats(context.Background())
	up.spool.MaxBytes = stats.Bytes // spool is full now
	if !up.backpressure(context.Background()) {
		t.Fatal("full spool must apply backpressure")
	}
	if err := up.spoolResults(context.Background(), []byte(`[{"Name":"b"}]`), time.Now()); !errors.Is(err, buffer.ErrSpoolFull) {
		t.Fatalf("expected ErrSpoolFull, got %v", err)
	}

	postHeartbeat(context.Background(), up.client, cfg, up.spoolReport(context.Background()))
	if body.Spool == nil || body.Spool.Messages != 1 || !body.Spool.Backpressure || body.Spool.DroppedMessages != 1 {
		t.Fatalf("heartbeat spool report = %+v", body.Spool)
	}
}

// countingPlugin counts discovery runs.
type countingPlugin struct{ runs atomic.Int64 }

func (p *countingPlugin) Name() string { return "counting" }
func (p *countingPlugin) Probe(context.Context, []string, map[string]string) ([]plugins.Result, error) {
	p.runs.Add(1)
	return nil, nil
}
func (p *countingPlugin) Collect(context.Context, string, map[string]string) (*plugins.DeviceRecord, error) {
	return nil, nil
}

// TestLicenseExpiryPausesDiscovery covers WP-072 (ENT-07, CH21): an ingest
// refused with the license-expired problem pauses the collector; the batch
// is not spooled, paused cycles neither scan nor flush the spool, and the
// heartbeat's license status resumes discovery.
func TestLicenseExpiryPausesDiscovery(t *testing.T) {
	var license atomic.Value
	license.Store("expired")
	var ingests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/heartbeat") {
			w.Header().Set(LicenseStatusHeader, license.Load().(string))
			w.WriteHeader(http.StatusNoContent)
			return
		}
		ingests.Add(1)
		if license.Load() == "expired" {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"type":"` + problemLicenseExpired + `","title":"Forbidden","status":403}`))
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	cfg := testConfig(srv.URL, t.TempDir())
	up, err := newUploader(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = up.uploadRecords(ctx, testRecords("a")); !errors.Is(err, errLicenseExpired) {
		t.Fatalf("upload after expiry: %v, want errLicenseExpired", err)
	}
	if !up.paused.Load() {
		t.Fatal("collector not paused after a license-expired refusal")
	}
	if n, _ := up.spool.Len(ctx); n != 0 {
		t.Fatalf("refused batch spooled: %d messages", n)
	}
	if err = up.uploadRecords(ctx, testRecords("b")); !errors.Is(err, errLicenseExpired) || ingests.Load() != 1 {
		t.Fatalf("paused upload: %v after %d ingests, want no further request", err, ingests.Load())
	}

	// Paused cycles do not scan.
	plugin := &countingPlugin{}
	pluginRegistry["counting"] = plugin
	defer delete(pluginRegistry, "counting")
	loopCfg := cfg
	loopCfg.Protocols = []string{"counting"}
	loopCfg.DiscoveryInterval = 10 * time.Millisecond
	loopCtx, cancel := context.WithTimeout(ctx, 60*time.Millisecond)
	runDiscoveryLoop(loopCtx, loopCfg, up)
	cancel()
	if plugin.runs.Load() != 0 {
		t.Fatalf("paused collector scanned %d times", plugin.runs.Load())
	}

	// The heartbeat keeps reporting the status; a renewed license resumes.
	if status, ok := postHeartbeat(ctx, up.client, cfg, nil); status != "expired" || !ok {
		t.Fatalf("heartbeat license status %q, accepted %v", status, ok)
	}
	license.Store("active")
	status, _ := postHeartbeat(ctx, up.client, cfg, nil)
	up.setLicenseStatus(status)
	if up.paused.Load() {
		t.Fatal("collector still paused after the license was renewed")
	}
	if err = up.uploadRecords(ctx, testRecords("c")); err != nil {
		t.Fatalf("upload after renewal: %v", err)
	}
	loopCtx, cancel = context.WithTimeout(ctx, 30*time.Millisecond)
	runDiscoveryLoop(loopCtx, loopCfg, up)
	cancel()
	if plugin.runs.Load() == 0 {
		t.Fatal("resumed collector did not scan")
	}
}

// testRecords returns one valid DeviceRecord named name.
func testRecords(name string) []plugins.DeviceRecord {
	return []plugins.DeviceRecord{plugins.RecordFromResult("sweep", &plugins.Result{CIType: "server", Name: name, IP: "192.0.2.10"}, time.Now())}
}

// fakeUpdater offers release once and records what was applied.
type fakeUpdater struct {
	release  *update.ReleaseInfo
	applyErr error
	checks   atomic.Int32
	applied  chan string
}

func (f *fakeUpdater) Check(context.Context, string) (*update.ReleaseInfo, error) {
	f.checks.Add(1)
	return f.release, nil
}

func (f *fakeUpdater) Apply(_ context.Context, rel update.ReleaseInfo) error {
	if f.applyErr != nil {
		return f.applyErr
	}
	f.applied <- rel.Version
	return nil
}

func TestUpdateLoopWaitsForAWorkingStartAndRestartsIntoTheUpdate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u := &fakeUpdater{release: &update.ReleaseInfo{Version: "v9.0.0", Channel: update.ChannelStable}, applied: make(chan string, 1)}
	confirmed, restart := make(chan struct{}), make(chan string, 1)
	go runUpdateLoop(ctx, time.Hour, u, confirmed, restart)

	time.Sleep(30 * time.Millisecond)
	if u.checks.Load() != 0 {
		t.Fatal("the updater must not run before the running binary confirmed a successful heartbeat")
	}
	close(confirmed)
	select {
	case v := <-restart:
		if v != "v9.0.0" || <-u.applied != "v9.0.0" {
			t.Fatalf("restart for %q", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no restart after the update was installed")
	}
}

func TestRefusedUpdateKeepsTheCollectorRunning(t *testing.T) {
	u := &fakeUpdater{release: &update.ReleaseInfo{Version: "v9.0.0"}, applyErr: update.ErrUnsigned}
	if v := checkAndApply(context.Background(), u); v != "" {
		t.Fatalf("a refused update reported %q", v)
	}
}

func TestStartupUpdateConfirmsAndRollsBack(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "reticora-collector")
	for _, f := range []struct{ name, content string }{
		{execPath, "new"}, {execPath + ".old", "old"},
		{filepath.Join(dir, ".update-pending"), `{"version":"v9.0.0","attempts":0}`},
	} {
		if err := os.WriteFile(f.name, []byte(f.content), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// A start that reaches a heartbeat confirms the update.
	confirm, err := startupUpdate(execPath)
	if err != nil {
		t.Fatal(err)
	}
	confirm()
	if _, serr := os.Stat(filepath.Join(dir, ".update-pending")); !os.IsNotExist(serr) {
		t.Fatalf("the confirmed update is still pending: %v", serr)
	}

	// Starts that never reach a heartbeat roll it back.
	if err = os.WriteFile(filepath.Join(dir, ".update-pending"), []byte(`{"version":"v9.0.1","attempts":0}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for range update.MaxStartAttempts {
		if _, err = startupUpdate(execPath); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = startupUpdate(execPath); !errors.Is(err, update.ErrRolledBack) {
		t.Fatalf("err = %v, want ErrRolledBack", err)
	}
	if got, _ := os.ReadFile(execPath); string(got) != "old" {
		t.Fatalf("binary after rollback = %q", got)
	}
}

func TestUpdateChannelFromEnv(t *testing.T) {
	for value, want := range map[string]struct {
		ch string
		ok bool
	}{"": {"stable", true}, "Beta": {"beta", true}, " stable ": {"stable", true}, "nightly": {"nightly", false}} {
		if ch, ok := updateChannelFromEnv(value); ch != want.ch || ok != want.ok {
			t.Errorf("updateChannelFromEnv(%q) = %q, %v", value, ch, ok)
		}
	}
	t.Setenv("RETICORA_UPDATE_URL", "https://updates.example.com")
	t.Setenv("RETICORA_UPDATE_CHANNEL", "nightly")
	if cfg := loadCollectorConfig(); cfg.UpdateURL != "" {
		t.Error("an unknown channel must disable auto-updates")
	}
	t.Setenv("RETICORA_UPDATE_CHANNEL", "beta")
	if cfg := loadCollectorConfig(); cfg.UpdateURL == "" || cfg.UpdateChannel != "beta" || cfg.UpdateInterval != 6*time.Hour {
		t.Errorf("update config = %q %q %s", cfg.UpdateURL, cfg.UpdateChannel, cfg.UpdateInterval)
	}
}

func TestSweepConcurrencyIsCappedAt256(t *testing.T) {
	for value, want := range map[string]int{"": 256, "64": 64, "1000": 256, "-3": 256, "x": 256} {
		if got := sweepConcurrencyFromEnv(value); got != want {
			t.Errorf("sweepConcurrencyFromEnv(%q) = %d, want %d", value, got, want)
		}
	}
	cfg := collectorConfig{SweepConcurrency: 32}
	if p := newSweep(&cfg); p.Concurrency != 32 || p.Pinger == nil || p.Neighbors == nil {
		t.Errorf("sweep plugin = %+v", p)
	}
}

// probeCollectPlugin answers the probe for two targets and collects a full
// record for one of them.
type probeCollectPlugin struct {
	collected sync.Map
}

func (p *probeCollectPlugin) Name() string { return "snmp" }
func (p *probeCollectPlugin) Probe(_ context.Context, targets []string, _ map[string]string) ([]plugins.Result, error) {
	var hits []plugins.Result
	for _, t := range targets {
		hits = append(hits, plugins.Result{CIType: "network.device", Name: t, IP: t, MAC: "00:11:22:33:44:01"})
	}
	return hits, nil
}
func (p *probeCollectPlugin) Collect(_ context.Context, target string, _ map[string]string) (*plugins.DeviceRecord, error) {
	p.collected.Store(target, true)
	if target == "192.0.2.2" {
		return nil, errors.New("authentication failed")
	}
	return plugins.CollectRecord("snmp", &plugins.Result{
		CIType: "network.device", Name: "core-sw-1", IP: target, Serial: "FOC1",
		Attributes: map[string]any{"sysObjectID": "1.3.6.1.4.1.9.1.2494", "sysDescr": "Cisco IOS XE, C9300-48P"},
		Interfaces: []plugins.NetworkInterface{{Name: "Gi1/0/1", IfIndex: 1, OperUp: true, AdminUp: true}},
	}, nil, time.Now())
}

// TestDiscoveryProbesThenCollects covers WP-150 (DIS-03, DIS-04): the
// cycle calls Collect for every probed target and uploads its DeviceRecord;
// a failed Collect falls back to the probe result.
func TestDiscoveryProbesThenCollects(t *testing.T) {
	plug := &probeCollectPlugin{}
	hits, err := plug.Probe(context.Background(), []string{"192.0.2.1", "192.0.2.2"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	records, failed := collectHits(context.Background(), "snmp", plug, hits, nil)
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2", len(records))
	}
	// The failed collection is reported with target and protocol (DIS-08).
	if len(failed) != 1 || failed[0].Target != "192.0.2.2" || failed[0].Protocol != "snmp" || failed[0].Message != "authentication failed" {
		t.Errorf("failed targets = %+v", failed)
	}
	for _, target := range []string{"192.0.2.1", "192.0.2.2"} {
		if _, ok := plug.collected.Load(target); !ok {
			t.Errorf("Collect was not called for %s", target)
		}
	}
	full, fallback := records[0], records[1]
	if full.Classification.CIType != "switch" || full.Classification.Profile != "cisco-catalyst-9000-snmp" ||
		full.Identity.SerialNumber != "FOC1" || len(full.Interfaces) != 1 {
		t.Errorf("collected record = %+v", full)
	}
	if full.Identity.PrimaryMAC != "00:11:22:33:44:01" {
		t.Errorf("the probe MAC must complete the record, got %q", full.Identity.PrimaryMAC)
	}
	// Without evidence the probe hit is an unclassified generic_device (DIS-02).
	if fallback.Classification.CIType != "generic_device" || fallback.Attributes["classified_by"] != "fallback" ||
		fallback.TargetIP != "192.0.2.2" || fallback.Source != "snmp" {
		t.Errorf("fallback record = %+v", fallback)
	}
	for _, rec := range records {
		if v := rec.Validate(); len(v) > 0 {
			t.Errorf("record %s breaks the contract: %v", rec.TargetIP, v)
		}
	}
}

// TestEveryPluginImplementsProbeCollect checks the registry: each of the
// eight protocol plugins is registered under its own name.
func TestEveryPluginImplementsProbeCollect(t *testing.T) {
	want := []string{"ipmi", "nas", "power", "redfish", "snmp", "ssh", "sweep", "wmi"}
	for _, name := range want {
		plug, ok := pluginRegistry[name]
		if !ok {
			t.Errorf("plugin %s missing", name)
			continue
		}
		if plug.Name() != name {
			t.Errorf("plugin registered as %s is named %s", name, plug.Name())
		}
	}
	if len(pluginRegistry) != len(want) {
		t.Errorf("registry has %d plugins, want %d", len(pluginRegistry), len(want))
	}
}

// TestDiscoveryFollowsTheServerScopes covers WP-153 (DIS-09): with a scope
// list from the server the cycle discovers each scope with its plugins and
// tags the records with scope, client, site and VRF; without one the
// configured subnets apply.
func TestDiscoveryFollowsTheServerScopes(t *testing.T) {
	t.Cleanup(func() { collectorScopes = &scopeCache{} })
	cfg := collectorConfig{ScanSubnets: []string{"192.0.2.0/30"}, Protocols: []string{"sweep"}}
	if passes := discoveryPasses(&cfg); len(passes) != 1 || passes[0].scope != nil || len(passes[0].targets) != 2 {
		t.Fatalf("fallback passes = %+v", passes)
	}

	collectorScopes.set([]collectorScope{
		{ID: "s-1", Kind: "cidr", Target: "10.1.0.0/29", ClientID: "c-1", SiteID: "site-1", VRFID: "vrf-1", Plugins: []string{"sweep", "snmp"}},
		{ID: "s-2", Kind: "range", Target: "10.2.0.250-10.2.1.2", Plugins: []string{"ssh"}},
		{ID: "s-3", Kind: "host", Target: "fw.example.com", Plugins: []string{"snmp"}},
	}, nil)
	passes := discoveryPasses(&cfg)
	if len(passes) != 3 {
		t.Fatalf("passes = %+v", passes)
	}
	if len(passes[0].targets) != 6 || strings.Join(passes[0].protocols, ",") != "sweep,snmp" {
		t.Errorf("cidr pass = %+v", passes[0])
	}
	if strings.Join(passes[1].targets, ",") != "10.2.0.250,10.2.0.251,10.2.0.252,10.2.0.253,10.2.0.254,10.2.0.255,10.2.1.0,10.2.1.1,10.2.1.2" {
		t.Errorf("range targets = %v", passes[1].targets)
	}
	if strings.Join(passes[2].targets, ",") != "fw.example.com" {
		t.Errorf("host targets = %v", passes[2].targets)
	}

	recs := []plugins.DeviceRecord{plugins.RecordFromResult("sweep", &plugins.Result{CIType: "generic_device", Name: "x", IP: "10.1.0.1"}, time.Now())}
	tagRecords(recs, passes[0].scope)
	if recs[0].ScopeID != "s-1" || recs[0].ClientID != "c-1" || recs[0].SiteID != "site-1" || recs[0].VRFID != "vrf-1" {
		t.Errorf("tagged record = %+v", recs[0])
	}

	// An empty list from the server means: nothing to discover.
	collectorScopes.set([]collectorScope{}, nil)
	if passes = discoveryPasses(&cfg); len(passes) != 0 {
		t.Errorf("passes without scopes = %+v", passes)
	}
}

type fakeAddr string

func (a fakeAddr) Network() string { return "ip+net" }
func (a fakeAddr) String() string  { return string(a) }

func TestLocalSubnetsForScopeSuggestions(t *testing.T) {
	addrs := func() ([]net.Addr, error) {
		return []net.Addr{fakeAddr("127.0.0.1/8"), fakeAddr("192.168.10.23/24"), fakeAddr("192.168.10.24/24"),
			fakeAddr("169.254.1.2/16"), fakeAddr("fe80::1/64"), fakeAddr("10.8.0.1/32"), fakeAddr("172.16.4.9/22")}, nil
	}
	if got := strings.Join(localSubnets(addrs), ","); got != "192.168.10.0/24,172.16.4.0/22" {
		t.Errorf("local subnets = %s", got)
	}
}
