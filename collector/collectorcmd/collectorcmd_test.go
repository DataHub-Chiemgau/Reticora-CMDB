package collectorcmd

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
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
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/snmp"
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
	if _, err := up.postPayload(context.Background(), payload); err != nil {
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

// TestParseTLSScopes covers the format shared by RETICORA_REDFISH_TLS_SCOPES
// and RETICORA_WMI_TLS_SCOPES (COL-02): CIDR, CA bundle and SPKI pins; every
// malformed entry rejects the whole value.
func TestParseTLSScopes(t *testing.T) {
	pin := strings.Repeat("ab", 32)
	caPEM, _ := selfSignedCert(t, "winrm-ca")
	files := map[string][]byte{"/etc/reticora/winrm-ca.pem": caPEM, "/etc/reticora/empty.pem": []byte("no certificate")}
	readFile := func(path string) ([]byte, error) {
		if data, ok := files[path]; ok {
			return data, nil
		}
		return nil, os.ErrNotExist
	}
	scopes, err := parseTLSScopes("10.0.20.5/32=pin:"+pin+"; ", readFile)
	if err != nil || len(scopes) != 1 || len(scopes[0].PinsSHA256) != 1 || scopes[0].Network.String() != "10.0.20.5/32" {
		t.Fatalf("scopes = %+v, err = %v", scopes, err)
	}
	// CA bundle plus a pin written with colons, and a second network.
	colonPin := strings.TrimSuffix(strings.Repeat("cd:", 32), ":")
	scopes, err = parseTLSScopes("10.0.10.0/24=ca:/etc/reticora/winrm-ca.pem,pin:"+colonPin+";192.0.2.0/28=pin:"+pin, readFile)
	if err != nil || len(scopes) != 2 || scopes[0].RootCAs == nil || len(scopes[0].PinsSHA256) != 1 ||
		hex.EncodeToString(scopes[0].PinsSHA256[0]) != strings.Repeat("cd", 32) || scopes[1].Network.String() != "192.0.2.0/28" || scopes[1].RootCAs != nil {
		t.Fatalf("scopes = %+v, err = %v", scopes, err)
	}
	if scopes, err := parseTLSScopes("", readFile); err != nil || len(scopes) != 0 {
		t.Fatalf("empty value = %+v, %v", scopes, err)
	}
	for _, bad := range []string{
		"10.0.0.0/24",                             // no rule
		"10.0.0.0/24=",                            // empty rule
		"10.0.0.0/33=pin:" + pin,                  // bad CIDR
		"10.0.0.0/24=pin:abcd",                    // short pin
		"10.0.0.0/24=ca:/missing.pem",             // unreadable CA
		"10.0.0.0/24=ca:/etc/reticora/empty.pem",  // CA bundle without certificate
		"10.0.0.0/24=insecure:true",               // unknown rule
		"10.0.0.0/24=pin:" + pin + ";10.0.1.0/24", // one bad entry rejects all
	} {
		if _, err := parseTLSScopes(bad, readFile); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

// TestTLSScopesFromEnv: RETICORA_WMI_TLS_SCOPES configures the exceptions of
// the WMI plugin only, RETICORA_REDFISH_TLS_SCOPES those of Redfish,
// RETICORA_NAS_TLS_SCOPES those of NAS; an invalid value configures no
// exception (fail closed).
func TestTLSScopesFromEnv(t *testing.T) {
	pin := strings.Repeat("ef", 32)
	t.Setenv("RETICORA_WMI_TLS_SCOPES", "10.0.40.0/24=pin:"+pin)
	t.Setenv("RETICORA_REDFISH_TLS_SCOPES", "")
	t.Setenv("RETICORA_NAS_TLS_SCOPES", "")
	cfg := loadCollectorConfig()
	if len(cfg.WMITLSScopes) != 1 || cfg.WMITLSScopes[0].Network.String() != "10.0.40.0/24" ||
		len(cfg.WMITLSScopes[0].PinsSHA256) != 1 || hex.EncodeToString(cfg.WMITLSScopes[0].PinsSHA256[0]) != pin {
		t.Fatalf("WMI TLS scopes = %+v", cfg.WMITLSScopes)
	}
	if len(cfg.RedfishTLSScopes) != 0 || len(cfg.NASTLSScopes) != 0 {
		t.Fatalf("the WMI exception must not apply to Redfish or NAS: %+v, %+v", cfg.RedfishTLSScopes, cfg.NASTLSScopes)
	}
	// WP-379: RETICORA_NAS_TLS_SCOPES configures only the NAS plugin.
	t.Setenv("RETICORA_WMI_TLS_SCOPES", "")
	t.Setenv("RETICORA_NAS_TLS_SCOPES", "10.0.60.5/32=pin:"+pin)
	cfg = loadCollectorConfig()
	if len(cfg.NASTLSScopes) != 1 || cfg.NASTLSScopes[0].Network.String() != "10.0.60.5/32" ||
		len(cfg.NASTLSScopes[0].PinsSHA256) != 1 || hex.EncodeToString(cfg.NASTLSScopes[0].PinsSHA256[0]) != pin {
		t.Fatalf("NAS TLS scopes = %+v", cfg.NASTLSScopes)
	}
	if len(cfg.RedfishTLSScopes) != 0 || len(cfg.WMITLSScopes) != 0 {
		t.Fatalf("the NAS exception must not apply to Redfish or WMI: %+v, %+v", cfg.RedfishTLSScopes, cfg.WMITLSScopes)
	}
	t.Setenv("RETICORA_NAS_TLS_SCOPES", "10.0.60.0/24=insecure:true")
	if cfg = loadCollectorConfig(); cfg.NASTLSScopes != nil {
		t.Fatalf("an invalid RETICORA_NAS_TLS_SCOPES must configure no exception: %+v", cfg.NASTLSScopes)
	}
	t.Setenv("RETICORA_NAS_TLS_SCOPES", "")
	t.Setenv("RETICORA_WMI_TLS_SCOPES", "10.0.40.0/24=insecure:true")
	t.Setenv("RETICORA_REDFISH_TLS_SCOPES", "10.0.50.0/24=pin:"+pin)
	cfg = loadCollectorConfig()
	if cfg.WMITLSScopes != nil {
		t.Fatalf("an invalid RETICORA_WMI_TLS_SCOPES must configure no exception: %+v", cfg.WMITLSScopes)
	}
	if len(cfg.RedfishTLSScopes) != 1 || cfg.RedfishTLSScopes[0].Network.String() != "10.0.50.0/24" {
		t.Fatalf("Redfish TLS scopes = %+v", cfg.RedfishTLSScopes)
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

// Retest F-260 (COL-06): with auto-updates on, the start warns when cosign
// is not on PATH, because every update would be refused; the verifier runs
// the cosign it looked for.
func TestUpdateCheckerWarnsWithoutCosign(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	cfg := collectorConfig{UpdateURL: "https://updates.example.com", UpdateChannel: update.ChannelStable}

	logs := captureLogs(t)
	c := newUpdateChecker(&cfg)
	warnings := logLines(t, logs, "auto-updates are enabled, but cosign is not on PATH: every update is refused until it is installed")
	if len(warnings) != 1 || warnings[0]["level"] != "WARN" || warnings[0]["event"] != "collector.update.cosign_missing" {
		t.Errorf("start without cosign on PATH: warnings %v, log %s", warnings, logs.String())
	}
	if v, ok := c.Verifier.(update.CosignVerifier); !ok || v.Binary != "cosign" {
		t.Errorf("verifier = %#v, want CosignVerifier running cosign", c.Verifier)
	}

	if err := os.WriteFile(filepath.Join(dir, "cosign"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	logs = captureLogs(t)
	newUpdateChecker(&cfg)
	if strings.Contains(logs.String(), "collector.update.cosign_missing") {
		t.Errorf("warning although cosign is on PATH: %s", logs.String())
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

// fakeEnrollServer redeems one code like POST /api/v1/collectors/enroll:
// it signs the CSR with a test CA (CN = collector id, O = organization).
func fakeEnrollServer(t *testing.T, code, orgID, collectorID string) *httptest.Server {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test collector CA"}, IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	caCert, _ := x509.ParseCertificate(caDER)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Code, Name, Version, CSR string }
		if r.URL.Path != "/api/v1/collectors/enroll" || json.NewDecoder(r.Body).Decode(&req) != nil || req.Code != code {
			http.Error(w, "invalid or expired enrollment code", http.StatusUnauthorized)
			return
		}
		block, _ := pem.Decode([]byte(req.CSR))
		csr, perr := x509.ParseCertificateRequest(block.Bytes)
		if perr != nil {
			http.Error(w, "bad csr", http.StatusBadRequest)
			return
		}
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: collectorID, Organization: []string{orgID}},
			NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(90 * 24 * time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
		der, _ := x509.CreateCertificate(rand.Reader, tmpl, caCert, csr.PublicKey, caKey)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": collectorID, "certificate": map[string]any{
			"certificate_pem": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
			"ca_bundle_pem":   string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})),
		}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestEnrollCommand covers WP-156 (INS-06): `enroll --server --code`
// stores identity and server URL in the one credentials file; the
// collector then starts without further settings.
func TestEnrollCommand(t *testing.T) {
	orgID, colID := "6f0b6f0e-0000-4000-8000-0000000000aa", "6f0b6f0e-0000-4000-8000-0000000000bb"
	srv := fakeEnrollServer(t, "ABCD-EFGH", orgID, colID)
	path := filepath.Join(t.TempDir(), "credentials.json")
	t.Setenv("RETICORA_CREDENTIALS_PATH", path)
	t.Setenv("RETICORA_SERVER_URL", "")
	t.Setenv("RETICORA_ORGANIZATION_ID", "")
	t.Setenv("RETICORA_COLLECTOR_ID", "")

	var stdout, stderr bytes.Buffer
	handled, code := runCommand(context.Background(), []string{"enroll", "--server", srv.URL + "/", "--code", "ABCD-EFGH", "--name", "site-a"}, &stdout, &stderr)
	if !handled || code != 0 {
		t.Fatalf("enroll exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), colID) || !strings.Contains(stdout.String(), path) {
		t.Errorf("output = %s", stdout.String())
	}
	cfg := loadCollectorConfig()
	if cfg.ServerURL != srv.URL || cfg.CollectorID != colID || cfg.OrganizationID != orgID || cfg.CredentialsPath != path {
		t.Errorf("config after enroll: server %q collector %q org %q path %q", cfg.ServerURL, cfg.CollectorID, cfg.OrganizationID, cfg.CredentialsPath)
	}
	certPEM, keyPEM, _, err := loadTLSMaterial(context.Background(), cfg)
	if err != nil || len(certPEM) == 0 || len(keyPEM) == 0 {
		t.Errorf("TLS material from the credentials file: %v", err)
	}
	// The environment still wins.
	t.Setenv("RETICORA_SERVER_URL", "https://other.example.com")
	if cfg = loadCollectorConfig(); cfg.ServerURL != "https://other.example.com" {
		t.Errorf("RETICORA_SERVER_URL ignored: %s", cfg.ServerURL)
	}

	// Argument and code errors.
	for _, args := range [][]string{{"enroll", "--code", "X"}, {"enroll", "--server", srv.URL}, {"enroll", "--bogus"}, {"frobnicate"}} {
		stderr.Reset()
		if _, code = runCommand(context.Background(), args, &stdout, &stderr); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
	}
	stderr.Reset()
	if _, code = runCommand(context.Background(), []string{"enroll", "--server", srv.URL, "--code", "WRONG", "--credentials", filepath.Join(t.TempDir(), "c.json")}, &stdout, &stderr); code != 1 ||
		!strings.Contains(stderr.String(), "invalid or expired") {
		t.Errorf("wrong code: exit %d, %s", code, stderr.String())
	}
	stdout.Reset()
	if handled, code = runCommand(context.Background(), []string{"version"}, &stdout, &stderr); !handled || code != 0 || !strings.Contains(stdout.String(), "reticora-collector") {
		t.Errorf("version: %v %d %q", handled, code, stdout.String())
	}
	if handled, _ = runCommand(context.Background(), nil, &stdout, &stderr); handled {
		t.Error("no arguments starts the collector")
	}
}

func TestReachabilitySamplesReportLostHosts(t *testing.T) {
	samples := reachabilitySamples([]string{"10.0.0.1", "10.0.0.2"}, []string{"10.0.0.2", "10.0.0.3"})
	got := map[string]float64{}
	for _, s := range samples {
		if s.Name != metricSweepReachable || s.Labels["kind"] != "sweep" {
			t.Fatalf("unexpected sample %+v", s)
		}
		got[s.Labels["target"]] = s.Value
	}
	want := map[string]float64{"10.0.0.1": 0, "10.0.0.2": 1, "10.0.0.3": 1}
	if len(got) != len(want) {
		t.Fatalf("samples = %v, want %v", got, want)
	}
	for host, v := range want {
		if got[host] != v {
			t.Fatalf("sample of %s = %v, want %v", host, got[host], v)
		}
	}
}

// ingestBatchName decodes a gzip ingest request and returns the name of its
// first record.
func ingestBatchName(t *testing.T, r *http.Request) string {
	t.Helper()
	gz, err := gzip.NewReader(r.Body)
	if err != nil {
		t.Errorf("gzip: %v", err)
		return ""
	}
	var batch plugins.DeviceRecordBatch
	if err := json.NewDecoder(gz).Decode(&batch); err != nil || len(batch.Records) == 0 {
		t.Errorf("decode batch: %v", err)
		return ""
	}
	return batch.Records[0].System.Name
}

// spoolBatches spools one discovery batch per name, oldest first.
func spoolBatches(t *testing.T, up *uploader, names ...string) {
	t.Helper()
	base := time.Now().Add(-time.Hour)
	for i, name := range names {
		payload, err := json.Marshal(plugins.DeviceRecordBatch{Records: testRecords(name)})
		if err != nil {
			t.Fatal(err)
		}
		if err := up.spoolResults(context.Background(), payload, base.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("spool %s: %v", name, err)
		}
	}
}

// quarantined counts the files in the spool's quarantine directory.
func quarantined(t *testing.T, spoolDir string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(spoolDir, buffer.QuarantineDir))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

// TestSpoolPoisonBatchIsQuarantined covers F-085 (COL-05): a batch the
// server refuses for good (400) is quarantined and counted as lost, and the
// delivery continues with the next batch (202). A fresh batch refused for
// good is not spooled but quarantined and counted as well.
func TestSpoolPoisonBatchIsQuarantined(t *testing.T) {
	spoolDir := t.TempDir()
	var mu sync.Mutex
	var delivered []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := ingestBatchName(t, r)
		if strings.HasPrefix(name, "poison") {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"title":"Bad Request","status":400,"detail":"records[0]: invalid"}`))
			return
		}
		mu.Lock()
		delivered = append(delivered, name)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	ctx := context.Background()
	up, err := newUploader(ctx, testConfig(srv.URL, spoolDir))
	if err != nil {
		t.Fatal(err)
	}
	spoolBatches(t, up, "poison-1", "good-2")
	up.flushSpool(ctx)

	if len(delivered) != 1 || delivered[0] != "good-2" {
		t.Fatalf("delivered = %v, want the batch behind the poison batch", delivered)
	}
	if n, _ := up.spool.Len(ctx); n != 0 {
		t.Fatalf("spool still holds %d batches", n)
	}
	if q := quarantined(t, spoolDir); q != 1 {
		t.Fatalf("quarantine holds %d files, want the poison batch", q)
	}
	stats, err := up.spool.Stats(ctx)
	if err != nil || stats.DroppedMessages != 1 || stats.DroppedBytes == 0 {
		t.Fatalf("loss after the poison batch: %+v, %v; want 1 message counted", stats, err)
	}

	// A fresh batch refused for good: an error for the scope run, no spool
	// entry, quarantined and counted.
	if err := up.uploadRecords(ctx, testRecords("poison-3")); !errors.Is(err, errUploadRejected) {
		t.Fatalf("upload of a poison batch: %v, want errUploadRejected", err)
	}
	if n, _ := up.spool.Len(ctx); n != 0 {
		t.Fatalf("poison batch spooled: %d", n)
	}
	if q := quarantined(t, spoolDir); q != 2 {
		t.Fatalf("quarantine holds %d files, want 2", q)
	}
	if stats, _ = up.spool.Stats(ctx); stats.DroppedMessages != 2 {
		t.Fatalf("dropped messages = %d, want 2", stats.DroppedMessages)
	}
}

// TestSpoolTransientFailureKeepsBatches covers COL-05: 503, 408, 429, 401
// (the HTTP path before the mTLS edge is up) and network errors are transient; every batch stays in the spool in its
// order and nothing is counted as lost.
func TestSpoolTransientFailureKeepsBatches(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusUnauthorized, 0} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			spoolDir := t.TempDir()
			var requests atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(status)
			}))
			url := srv.URL
			if status == 0 {
				srv.Close() // network error
			} else {
				defer srv.Close()
			}

			ctx := context.Background()
			up, err := newUploader(ctx, testConfig(url, spoolDir))
			if err != nil {
				t.Fatal(err)
			}
			spoolBatches(t, up, "first", "second")
			up.flushSpool(ctx)

			if n, _ := up.spool.Len(ctx); n != 2 {
				t.Fatalf("spool holds %d batches, want both", n)
			}
			if status != 0 && requests.Load() != 1 {
				t.Fatalf("%d requests, want the delivery to stop at the first batch", requests.Load())
			}
			stats, _ := up.spool.Stats(ctx)
			if stats.DroppedMessages != 0 || quarantined(t, spoolDir) != 0 {
				t.Fatalf("transient failure counted as loss: %+v", stats)
			}
			// A fresh batch queues behind them, not quarantined.
			if err := up.uploadRecords(ctx, testRecords("third")); err != nil {
				t.Fatalf("queueing behind the backlog: %v", err)
			}
			if n, _ := up.spool.Len(ctx); n != 3 {
				t.Fatalf("spool holds %d batches, want 3", n)
			}
		})
	}
}

// TestRecordsRejectedInAcceptedBatchAreCounted: a 202 that names rejected
// records (WP-481) acknowledges the batch and counts the rejected records
// as lost.
func TestRecordsRejectedInAcceptedBatchAreCounted(t *testing.T) {
	spoolDir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"received":3,"accepted":1,"rejected":2,"errors":[{"record_id":"a","status":403},{"record_id":"b","status":422}]}`))
	}))
	defer srv.Close()

	ctx := context.Background()
	up, err := newUploader(ctx, testConfig(srv.URL, spoolDir))
	if err != nil {
		t.Fatal(err)
	}
	spoolBatches(t, up, "spooled")
	up.flushSpool(ctx)
	if n, _ := up.spool.Len(ctx); n != 0 {
		t.Fatalf("accepted batch not acknowledged: %d", n)
	}
	if stats, _ := up.spool.Stats(ctx); stats.DroppedMessages != 2 {
		t.Fatalf("dropped messages = %d, want the 2 rejected records", stats.DroppedMessages)
	}
	if err := up.uploadRecords(ctx, testRecords("fresh")); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if stats, _ := up.spool.Stats(ctx); stats.DroppedMessages != 4 {
		t.Fatalf("dropped messages = %d, want 4", stats.DroppedMessages)
	}
}

// TestSpooledAgentTelemetryLossIsCounted covers F-233: spooled agent
// telemetry the backend rejects (403) or that is unreadable is dropped and
// counted as lost before it is acknowledged; 429 is transient.
func TestSpooledAgentTelemetryLossIsCounted(t *testing.T) {
	spoolDir := t.TempDir()
	backend := &relayBackend{status: http.StatusForbidden}
	srv := httptest.NewServer(backend)
	defer srv.Close()

	ctx := context.Background()
	up, err := newUploader(ctx, testConfig(srv.URL, spoolDir))
	if err != nil {
		t.Fatal(err)
	}
	if err := up.spoolAgentTelemetry(ctx, []byte(`{"token":"agent-token","telemetry":{"hostname":"h1"}}`)); err != nil {
		t.Fatal(err)
	}
	up.flushSpool(ctx)
	if n, _ := up.spool.Len(ctx); n != 0 {
		t.Fatalf("rejected telemetry still spooled: %d", n)
	}
	stats, _ := up.spool.Stats(ctx)
	if stats.DroppedMessages != 1 {
		t.Fatalf("dropped_messages = %d after a 403, want 1", stats.DroppedMessages)
	}
	backend.mu.Lock()
	requests := len(backend.requests)
	backend.mu.Unlock()
	if requests != 1 {
		t.Fatalf("telemetry requests = %d, want 1", requests)
	}

	if err := up.spoolAgentTelemetry(ctx, []byte(`not json`)); err != nil {
		t.Fatal(err)
	}
	up.flushSpool(ctx)
	if stats, _ = up.spool.Stats(ctx); stats.DroppedMessages != 2 {
		t.Fatalf("dropped_messages = %d after unreadable telemetry, want 2", stats.DroppedMessages)
	}

	// 429 is transient: the telemetry stays for the next flush.
	backend.mu.Lock()
	backend.status = http.StatusTooManyRequests
	backend.mu.Unlock()
	if err := up.spoolAgentTelemetry(ctx, []byte(`{"token":"agent-token","telemetry":{"hostname":"h2"}}`)); err != nil {
		t.Fatal(err)
	}
	up.flushSpool(ctx)
	if n, _ := up.spool.Len(ctx); n != 1 {
		t.Fatalf("telemetry after 429 spooled: %d, want 1", n)
	}
	if stats, _ = up.spool.Stats(ctx); stats.DroppedMessages != 2 {
		t.Fatalf("dropped_messages = %d after a 429, want still 2", stats.DroppedMessages)
	}
}

// TestDroppedSamplesAreReportedInTheHeartbeat covers E-79 (option 1) and
// F-234: metric samples and traps dropped on upload failures are counted
// (dropped_samples) and reported with the heartbeat; an accepted heartbeat
// acknowledges them.
func TestDroppedSamplesAreReportedInTheHeartbeat(t *testing.T) {
	withEdge(t, func(_ context.Context, suffix string, _ []byte) ([]byte, error) {
		if suffix == edgeMetricsSuffix {
			return []byte(`{"status":400,"error":"invalid metrics"}`), nil
		}
		return nil, errNoEdge
	})
	var mu sync.Mutex
	var bodies []heartbeatBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body heartbeatBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode heartbeat: %v", err)
		}
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	ctx := context.Background()
	cfg := testConfig(srv.URL, "")
	up, err := newUploader(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := up.uploadMetricSamples(ctx, []plugins.MetricSample{sample("10.0.0.1"), sample("10.0.0.2")}); err == nil {
		t.Fatal("refused samples reported as delivered")
	}
	if err := up.uploadTrapEvent(ctx, snmp.TrapEvent{SourceIP: "10.0.0.3", TrapOID: "1.3.6.1.6.3.1.1.5.3", Received: time.Now()}); err == nil {
		t.Fatal("refused trap reported as delivered")
	}

	if _, ok := up.heartbeat(ctx, cfg); !ok {
		t.Fatal("heartbeat not accepted")
	}
	if _, ok := up.heartbeat(ctx, cfg); !ok {
		t.Fatal("heartbeat not accepted")
	}
	if len(bodies) != 2 || bodies[0].Spool == nil || bodies[0].Spool.DroppedSamples != 3 {
		t.Fatalf("first heartbeat spool report = %+v, want dropped_samples=3", bodies[0].Spool)
	}
	if bodies[1].Spool != nil {
		t.Fatalf("second heartbeat reports %+v, want the samples acknowledged", bodies[1].Spool)
	}
}

// TestHeartbeatAcknowledgesReportedLossOnly covers WP-482/483: the
// persisted loss totals shrink only after the server accepted the
// heartbeat (204), and only by the reported amounts; a loss recorded while
// the heartbeat is under way goes with the next one.
func TestHeartbeatAcknowledgesReportedLossOnly(t *testing.T) {
	spoolDir := t.TempDir()
	var status atomic.Int64
	status.Store(http.StatusServiceUnavailable)
	var up *uploader
	var mu sync.Mutex
	var reports []*spoolReport
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body heartbeatBody
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		reports = append(reports, body.Spool)
		mu.Unlock()
		if status.Load() == http.StatusNoContent {
			// A loss recorded while the heartbeat is under way.
			if err := up.spool.RecordLoss(r.Context(), buffer.Loss{Messages: 1, Bytes: 7, Reason: buffer.LossRejected}); err != nil {
				t.Errorf("record loss: %v", err)
			}
		}
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()

	ctx := context.Background()
	cfg := testConfig(srv.URL, spoolDir)
	var err error
	if up, err = newUploader(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if err = up.spool.RecordLoss(ctx, buffer.Loss{Messages: 3, Bytes: 300, Reason: buffer.LossRejected}); err != nil {
		t.Fatal(err)
	}

	if _, ok := up.heartbeat(ctx, cfg); ok {
		t.Fatal("503 heartbeat counted as accepted")
	}
	if stats, _ := up.spool.Stats(ctx); stats.DroppedMessages != 3 || stats.DroppedBytes != 300 {
		t.Fatalf("loss after a failed heartbeat = %d/%d, want 3/300 kept", stats.DroppedMessages, stats.DroppedBytes)
	}

	status.Store(http.StatusNoContent)
	if _, ok := up.heartbeat(ctx, cfg); !ok {
		t.Fatal("heartbeat not accepted")
	}
	// A new instance on the same spool sees the persisted state.
	stats, err := (&buffer.DiskBuffer{Dir: spoolDir}).Stats(ctx)
	if err != nil || stats.DroppedMessages != 1 || stats.DroppedBytes != 7 {
		t.Fatalf("loss after the accepted heartbeat = %+v, %v; want only the new 1/7", stats, err)
	}
	if len(reports) != 2 || reports[0].DroppedMessages != 3 || reports[1].DroppedMessages != 3 {
		t.Fatalf("reports = %+v, want 3 lost messages in both", reports)
	}
}

// TestBackpressurePausesCollectionUntilTheSpoolDrains covers COL-05 and
// NFR-04: while the spool is saturated the discovery loop scans nothing;
// once the flush has drained the spool, collection resumes.
func TestBackpressurePausesCollectionUntilTheSpoolDrains(t *testing.T) {
	spoolDir := t.TempDir()
	var reachable atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !reachable.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	ctx := context.Background()
	cfg := testConfig(srv.URL, spoolDir)
	up, err := newUploader(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	spoolBatches(t, up, "backlog")
	stats, _ := up.spool.Stats(ctx)
	up.spool.MaxBytes = stats.Bytes // saturated
	if !up.backpressure(ctx) {
		t.Fatal("saturated spool applies no backpressure")
	}

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
		t.Fatalf("collection ran %d times on a saturated spool", plugin.runs.Load())
	}

	// The backend is back: the flush drains the spool, collection resumes.
	reachable.Store(true)
	up.flushSpool(ctx)
	if n, _ := up.spool.Len(ctx); n != 0 || up.backpressure(ctx) {
		t.Fatalf("spool not drained: %d messages, backpressure %v", n, up.backpressure(ctx))
	}
	loopCtx, cancel = context.WithTimeout(ctx, 60*time.Millisecond)
	runDiscoveryLoop(loopCtx, loopCfg, up)
	cancel()
	if plugin.runs.Load() == 0 {
		t.Fatal("collection did not resume after the spool drained")
	}
	if stats, _ = up.spool.Stats(ctx); stats.DroppedMessages != 0 {
		t.Fatalf("backpressure lost data: %+v", stats)
	}
}
