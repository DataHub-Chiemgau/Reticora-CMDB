package collectorcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// configHarness drives a configCollector with a fake device read, a fake
// edge and a fake clock.
type configHarness struct {
	c      *configCollector
	mu     sync.Mutex
	now    time.Time
	device map[string]string // CI → raw running-config (cisco_ios)
	sent   []probewire.ConfigSnapshot
	reply  func(msg *probewire.ConfigSnapshot) ([]byte, error)
	logs   *bytes.Buffer
}

func newConfigHarness(t *testing.T, dir string) *configHarness {
	t.Helper()
	c, err := newConfigCollector(dir, 24*time.Hour, 4)
	if err != nil {
		t.Fatal(err)
	}
	h := &configHarness{c: c, now: time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC), device: map[string]string{}, logs: &bytes.Buffer{}}
	h.reply = func(*probewire.ConfigSnapshot) ([]byte, error) { return []byte(`{"status":202}`), nil }
	c.logger = slog.New(slog.NewJSONHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c.now = func() time.Time { h.mu.Lock(); defer h.mu.Unlock(); return h.now }
	c.maxPayload = func() int64 { return defaultEdgeMaxPayload }
	c.collect = func(_ context.Context, tgt *configTarget, _ config.Platform) (*config.Snapshot, error) {
		h.mu.Lock()
		raw, ok := h.device[tgt.CIID]
		h.mu.Unlock()
		if !ok {
			return nil, config.ErrUnsupportedPlatform
		}
		return config.Process(config.CiscoIOS, raw)
	}
	c.send = func(_ context.Context, suffix string, data []byte) ([]byte, error) {
		if suffix != edgeConfigSuffix {
			t.Errorf("suffix %q", suffix)
		}
		var msg probewire.ConfigSnapshot
		if err := probewire.Decode(data, &msg); err != nil {
			t.Fatal(err)
		}
		raw, err := h.reply(&msg)
		// Only the states the server accepted count as sent.
		var reply edgeReply
		if err == nil && json.Unmarshal(raw, &reply) == nil && reply.Status >= 200 && reply.Status < 300 {
			h.mu.Lock()
			h.sent = append(h.sent, msg)
			h.mu.Unlock()
		}
		return raw, err
	}
	c.targets = func() []configTarget {
		h.mu.Lock()
		defer h.mu.Unlock()
		var out []configTarget
		for ci := range h.device {
			out = append(out, configTarget{CIID: ci, IP: "192.0.2.1", Creds: map[string]string{"username": "netops", "password": "pw"}})
		}
		return out
	}
	return h
}

func (h *configHarness) set(ci, raw string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.device[ci] = raw
}

func (h *configHarness) advance(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.now = h.now.Add(d)
}

func (h *configHarness) sentMessages() []probewire.ConfigSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]probewire.ConfigSnapshot(nil), h.sent...)
}

// read reads one CI at once (the schedule is covered separately).
func (h *configHarness) read(ci string) {
	h.c.collectOne(context.Background(), &configTarget{CIID: ci, IP: "192.0.2.1"})
	h.c.flush(context.Background())
}

func pendingFiles(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, configPendingName, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

const ciA = "6a1f8c2e-3b4d-4e5f-8a9b-00000000000a"
const ciB = "6a1f8c2e-3b4d-4e5f-8a9b-00000000000b"

func iosConfig(host, at string) string {
	return "Building configuration...\r\n! Last configuration change at " + at + "\r\nhostname " + host +
		"\r\nenable secret 9 $9$CANARYsecret01\r\nsnmp-server community CANARYcomm01 RO\r\nend\r\n"
}

// TestConfigSendsOnlyChangedHash covers WP-1053 AK5: a state goes out only
// when its hash differs from the confirmed one; a volatile line does not
// count as a change.
func TestConfigSendsOnlyChangedHash(t *testing.T) {
	dir := t.TempDir()
	h := newConfigHarness(t, dir)
	h.set(ciA, iosConfig("r1", "08:00"))
	h.read(ciA)
	h.set(ciA, iosConfig("r1", "09:00"))
	h.read(ciA)
	sent := h.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("sent %d states, want 1", len(sent))
	}
	m := sent[0]
	if m.CIID != ciA || m.Source != "ssh" || m.Trigger != "schedule" || m.Platform != "cisco_ios" || m.MaskedCount != 2 ||
		m.ManagementIP != "192.0.2.1" || m.Validate() != nil {
		t.Fatalf("message = %+v", m)
	}
	text, err := m.Text()
	if err != nil || strings.Contains(text, "CANARY") || !strings.Contains(text, "hostname r1") {
		t.Fatalf("content = %q, %v", text, err)
	}
	h.set(ciA, iosConfig("r2", "09:00"))
	h.read(ciA)
	if sent = h.sentMessages(); len(sent) != 2 || sent[1].SHA256 == sent[0].SHA256 {
		t.Fatalf("changed config: %d states sent", len(sent))
	}
	if h.c.counters.Unchanged.Load() != 1 || len(pendingFiles(t, dir)) != 0 {
		t.Fatalf("unchanged = %d, pending = %v", h.c.counters.Unchanged.Load(), pendingFiles(t, dir))
	}
	var doc configStateDoc
	raw, _ := os.ReadFile(filepath.Join(dir, configStateName))
	if err := json.Unmarshal(raw, &doc); err != nil || doc.CIs[ciA].SHA256 != sent[1].SHA256 || doc.CIs[ciA].Platform != "cisco_ios" {
		t.Fatalf("state.json = %s, %v", raw, err)
	}
	// Too large for the edge: no send, counted.
	h.c.maxPayload = func() int64 { return 100 }
	h.set(ciA, iosConfig("r3", "10:00"))
	h.read(ciA)
	if h.c.counters.TooLarge.Load() != 1 || len(h.sentMessages()) != 2 {
		t.Fatalf("too large: counter %d, sent %d", h.c.counters.TooLarge.Load(), len(h.sentMessages()))
	}
}

// TestConfigPendingSurvivesRestart covers WP-1053 AK5: without the edge the
// state waits in pending/ (0600); a restarted collector sends it first.
func TestConfigPendingSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	h := newConfigHarness(t, dir)
	h.reply = func(*probewire.ConfigSnapshot) ([]byte, error) { return nil, errNoEdge }
	h.set(ciA, iosConfig("r1", "08:00"))
	h.read(ciA)
	files := pendingFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("pending = %v", files)
	}
	info, err := os.Stat(files[0])
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("pending mode = %v, %v", info.Mode(), err)
	}
	if b, _ := os.ReadFile(files[0]); bytes.Contains(b, []byte("CANARY")) {
		t.Fatal("pending file holds a secret")
	}

	h2 := newConfigHarness(t, dir)
	h2.set(ciB, iosConfig("r9", "08:00"))
	h2.advance(time.Minute)
	h2.c.flush(context.Background())
	h2.read(ciB)
	sent := h2.sentMessages()
	if len(sent) != 2 || sent[0].CIID != ciA || sent[1].CIID != ciB {
		t.Fatalf("sent after restart = %d, want the pending state of A first", len(sent))
	}
	if len(pendingFiles(t, dir)) != 0 {
		t.Fatal("pending not cleared after the confirmation")
	}
}

// TestConfigNewerReplacesPending covers WP-1053 AK5: a newer state replaces
// the waiting one (config_superseded); only the newest goes out.
func TestConfigNewerReplacesPending(t *testing.T) {
	dir := t.TempDir()
	h := newConfigHarness(t, dir)
	var online atomic.Bool
	h.reply = func(*probewire.ConfigSnapshot) ([]byte, error) {
		if !online.Load() {
			return nil, errNoEdge
		}
		return []byte(`{"status":202}`), nil
	}
	h.set(ciA, iosConfig("r1", "08:00"))
	h.read(ciA)
	h.advance(time.Hour)
	h.set(ciA, iosConfig("r2", "09:00"))
	h.read(ciA)
	if n := len(pendingFiles(t, dir)); n != 1 || h.c.counters.Superseded.Load() != 1 {
		t.Fatalf("pending = %d, superseded = %d", n, h.c.counters.Superseded.Load())
	}
	online.Store(true)
	h.c.flush(context.Background())
	sent := h.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("sent %d, want 1", len(sent))
	}
	if text, _ := sent[0].Text(); !strings.Contains(text, "hostname r2") {
		t.Fatalf("sent the older state: %q", text)
	}
	if !strings.Contains(h.logs.String(), "config_superseded") {
		t.Fatal("superseded state not logged")
	}
}

// TestConfigPausesOn403 covers WP-1053 AK6: 403 feature not entitled
// pauses the reads for an hour; 404 discards the state with a warning.
func TestConfigPausesOn403(t *testing.T) {
	dir := t.TempDir()
	h := newConfigHarness(t, dir)
	var reads atomic.Int32
	collect := h.c.collect
	h.c.collect = func(ctx context.Context, tgt *configTarget, p config.Platform) (*config.Snapshot, error) {
		reads.Add(1)
		return collect(ctx, tgt, p)
	}
	refuse := true
	h.reply = func(*probewire.ConfigSnapshot) ([]byte, error) {
		if refuse {
			return []byte(`{"status":403,"type":"` + problemFeatureNotEntitled + `","error":"feature not entitled"}`), nil
		}
		return []byte(`{"status":202}`), nil
	}
	h.set(ciA, iosConfig("r1", "08:00"))
	h.read(ciA)
	if !h.c.paused(h.c.now()) || len(pendingFiles(t, dir)) != 1 {
		t.Fatalf("paused = %v, pending = %d", h.c.paused(h.c.now()), len(pendingFiles(t, dir)))
	}
	// Within the hour: neither a read nor a send.
	h.c.next[ciA] = h.c.now().Add(-time.Second)
	before := reads.Load()
	h.advance(30 * time.Minute)
	h.c.tick(context.Background())
	if reads.Load() != before || len(h.sentMessages()) != 0 {
		t.Fatalf("read or sent during the pause")
	}
	// After the hour the state goes out.
	refuse = false
	h.advance(31 * time.Minute)
	h.c.tick(context.Background())
	if len(h.sentMessages()) != 1 || len(pendingFiles(t, dir)) != 0 {
		t.Fatalf("after the pause: sent %d, pending %d", len(h.sentMessages()), len(pendingFiles(t, dir)))
	}
	if n := strings.Count(h.logs.String(), "collector.config.not_licensed"); n != 1 {
		t.Fatalf("not licensed logged %d times", n)
	}

	// 404: the CI is unknown or outside the scope; the state is dropped.
	h.reply = func(*probewire.ConfigSnapshot) ([]byte, error) {
		return []byte(`{"status":404,"error":"not found"}`), nil
	}
	h.set(ciB, iosConfig("r5", "08:00"))
	h.read(ciB)
	if len(pendingFiles(t, dir)) != 0 || h.c.counters.Discarded.Load() != 1 {
		t.Fatalf("404: pending %d, discarded %d", len(pendingFiles(t, dir)), h.c.counters.Discarded.Load())
	}
	if !strings.Contains(h.logs.String(), "collector.config.discarded") {
		t.Fatal("404 without warning")
	}
}

// TestConfigSpreadsStartTimes covers WP-1053 AK4: the first start of a CI
// is its hash phase within the interval (deterministic, spread), and at
// most RETICORA_CONFIG_PARALLEL reads run at a time.
func TestConfigSpreadsStartTimes(t *testing.T) {
	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	const n = 1000
	perHour := map[int]int{}
	for i := range n {
		ci := fmt.Sprintf("%08x-4b1e-4c3a-9d2f-%012x", i*2654435761, i)
		s := startOf(ci, 24*time.Hour, now)
		if !s.Equal(startOf(ci, 24*time.Hour, now)) {
			t.Fatal("start not deterministic")
		}
		if s.Before(now) || !s.Before(now.Add(24*time.Hour)) {
			t.Fatalf("start %s outside the interval", s)
		}
		perHour[int(s.Sub(now).Hours())]++
	}
	mean := float64(n) / 24
	for hour := range 24 {
		if c := perHour[hour]; float64(c) > 2*mean || float64(c) < mean/2 {
			t.Fatalf("hour %d has %d starts, mean %.1f", hour, c, mean)
		}
	}

	h := newConfigHarness(t, t.TempDir())
	h.c.parallel = 3
	var active, peak atomic.Int32
	release := make(chan struct{})
	h.c.collect = func(context.Context, *configTarget, config.Platform) (*config.Snapshot, error) {
		cur := active.Add(1)
		for {
			p := peak.Load()
			if cur <= p || peak.CompareAndSwap(p, cur) {
				break
			}
		}
		<-release
		active.Add(-1)
		return config.Process(config.CiscoIOS, "hostname x\n")
	}
	for i := range 10 {
		h.set(fmt.Sprintf("ci-%02d", i), "hostname x\n")
	}
	h.c.tick(context.Background()) // first sight: starts scheduled
	h.advance(24 * time.Hour)
	done := make(chan struct{})
	go func() { h.c.tick(context.Background()); close(done) }()
	time.Sleep(50 * time.Millisecond)
	close(release)
	<-done
	if p := peak.Load(); p != 3 {
		t.Fatalf("peak concurrency = %d, want 3", p)
	}
}

// TestConfigLogsNoContent covers WP-1053 AK6: the log holds CI, platform,
// hash prefix and masked count, never a secret or configuration text.
func TestConfigLogsNoContent(t *testing.T) {
	dir := t.TempDir()
	h := newConfigHarness(t, dir)
	h.set(ciA, iosConfig("edge-canary-host", "08:00"))
	h.read(ciA)
	h.reply = func(*probewire.ConfigSnapshot) ([]byte, error) { return nil, fmt.Errorf("edge down") }
	h.set(ciA, iosConfig("edge-canary-host2", "08:00"))
	h.read(ciA)
	logs := h.logs.String()
	for _, forbidden := range []string{"CANARY", "edge-canary-host", "hostname", "enable secret", "<masked>"} {
		if strings.Contains(logs, forbidden) {
			t.Fatalf("log contains %q:\n%s", forbidden, logs)
		}
	}
	for _, want := range []string{ciA, `"platform":"cisco_ios"`, "sha256_prefix", `"masked_count":2`} {
		if !strings.Contains(logs, want) {
			t.Fatalf("log lacks %q:\n%s", want, logs)
		}
	}
	if got := configIntervalFromEnv("0"); got != 0 {
		t.Fatalf("interval 0 = %v", got)
	}
	if got := configIntervalFromEnv("10m"); got != time.Hour {
		t.Fatalf("interval 10m = %v, want 1h", got)
	}
	if got := configIntervalFromEnv(""); got != 24*time.Hour {
		t.Fatalf("default interval = %v", got)
	}
	if got := configParallelFromEnv(""); got != 4 {
		t.Fatalf("default parallel = %d", got)
	}
}

// TestConfigPermanentRefusalDoesNotBlock covers WP-1053 AK5: a state the
// server refuses for good (400) is discarded with a warning, so that the
// state of the next CI is still delivered; a transient refusal (429)
// keeps the state.
func TestConfigPermanentRefusalDoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	h := newConfigHarness(t, dir)
	h.reply = func(*probewire.ConfigSnapshot) ([]byte, error) { return nil, errNoEdge }
	h.set(ciA, iosConfig("r1", "08:00"))
	h.set(ciB, iosConfig("r2", "08:00"))
	h.read(ciA)
	h.advance(time.Second)
	h.read(ciB)
	if n := len(pendingFiles(t, dir)); n != 2 {
		t.Fatalf("pending %d, want 2", n)
	}
	h.reply = func(msg *probewire.ConfigSnapshot) ([]byte, error) {
		if msg.CIID == ciA {
			return []byte(`{"status":400,"error":"invalid"}`), nil
		}
		return []byte(`{"status":202}`), nil
	}
	h.c.flush(context.Background())
	if sent := h.sentMessages(); len(sent) != 1 || sent[0].CIID != ciB {
		t.Fatalf("sent %+v, want the state of the second CI", sent)
	}
	if len(pendingFiles(t, dir)) != 0 || h.c.counters.Discarded.Load() != 1 {
		t.Fatalf("pending %d, discarded %d", len(pendingFiles(t, dir)), h.c.counters.Discarded.Load())
	}
	if !permanentRefusal(422) || !permanentRefusal(409) || permanentRefusal(429) || permanentRefusal(408) || permanentRefusal(403) || permanentRefusal(503) {
		t.Fatal("permanentRefusal classifies a status wrongly")
	}
}

// TestConfigFailedReadForgetsPlatform covers WP-1053 AK2: a failed read
// with a stored platform clears it, so that the next read detects the
// platform again (a replaced device).
func TestConfigFailedReadForgetsPlatform(t *testing.T) {
	dir := t.TempDir()
	h := newConfigHarness(t, dir)
	h.set(ciA, iosConfig("r1", "08:00"))
	h.read(ciA)
	if got := h.c.state[ciA].Platform; got != string(config.CiscoIOS) {
		t.Fatalf("platform after the first read = %q", got)
	}
	var gotPlatform []config.Platform
	h.c.collect = func(_ context.Context, _ *configTarget, platform config.Platform) (*config.Snapshot, error) {
		gotPlatform = append(gotPlatform, platform)
		if platform == config.CiscoIOS {
			return nil, fmt.Errorf("show running-config: invalid command")
		}
		return config.Process(config.Junos, "set system host-name mx1\r\n")
	}
	h.read(ciA)
	if got := h.c.state[ciA].Platform; got != "" {
		t.Fatalf("platform after the failed read = %q, want empty", got)
	}
	h.read(ciA)
	if len(gotPlatform) != 2 || gotPlatform[1] != "" || h.c.state[ciA].Platform != string(config.Junos) {
		t.Fatalf("platforms passed %v, stored %q", gotPlatform, h.c.state[ciA].Platform)
	}
	// The cleared platform survives a restart.
	c2, err := newConfigCollector(dir, 24*time.Hour, 4)
	if err != nil {
		t.Fatal(err)
	}
	if c2.state[ciA].Platform != string(config.Junos) {
		t.Fatalf("platform after restart = %q", c2.state[ciA].Platform)
	}
}
