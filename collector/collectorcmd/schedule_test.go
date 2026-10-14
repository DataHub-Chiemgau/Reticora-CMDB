package collectorcmd

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// TestIntervalsHaveDefaultsAndMinimums covers WP-154 (DIS-10): sweep 24 h,
// polls 15 min, organization intervals per protocol, polls never below
// 5 min.
func TestIntervalsHaveDefaultsAndMinimums(t *testing.T) {
	var none *discoveryIntervals
	if none.pollInterval("snmp") != 15*time.Minute {
		t.Error("default poll interval")
	}
	iv := &discoveryIntervals{Sweep: "12h", Poll: "1m", Protocols: map[string]string{"redfish": "30m", "ssh": "10s"}}
	for proto, want := range map[string]time.Duration{"snmp": 5 * time.Minute, "redfish": 30 * time.Minute, "ssh": 5 * time.Minute} {
		if got := iv.pollInterval(proto); got != want {
			t.Errorf("%s: %s, want %s", proto, got, want)
		}
	}
	if s := parseSchedule("2m", 24*time.Hour); s.every != 24*time.Hour {
		t.Errorf("a scope schedule below 5 min falls back: %+v", s)
	}
	t.Setenv("RETICORA_DISCOVERY_INTERVAL", "1m")
	t.Setenv("RETICORA_SWEEP_INTERVAL", "")
	cfg := loadCollectorConfig()
	if cfg.DiscoveryInterval != 5*time.Minute || cfg.SweepInterval != 24*time.Hour || cfg.SpoolFlushInterval != 30*time.Second {
		t.Errorf("env intervals: poll %s sweep %s flush %s", cfg.DiscoveryInterval, cfg.SweepInterval, cfg.SpoolFlushInterval)
	}
}

func TestSchedulerRunsSweepAndPollsSeparately(t *testing.T) {
	t.Cleanup(func() { collectorScopes = &scopeCache{} })
	collectorScopes.set([]collectorScope{{ID: "s-1", Kind: "cidr", Target: "10.0.0.0/30", Schedule: "24h", Plugins: []string{"sweep", "snmp", "redfish"}}},
		&discoveryIntervals{Poll: "15m", Protocols: map[string]string{"redfish": "1h"}})
	pass := discoveryPasses(&collectorConfig{})[0]
	sched := newScheduler()
	t0 := time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)
	steps := []struct {
		at   time.Duration
		want string
	}{
		{0, "sweep,snmp,redfish"},
		{time.Minute, ""},
		{15 * time.Minute, "snmp"},
		{30 * time.Minute, "snmp"},
		{time.Hour, "snmp,redfish"},
		{24 * time.Hour, "sweep,snmp,redfish"},
	}
	for _, step := range steps {
		if got := strings.Join(pass.dueProtocols(sched, t0.Add(step.at)), ","); got != step.want {
			t.Errorf("+%s: due %q, want %q", step.at, got, step.want)
		}
	}
	if next := pass.nextRun(sched); !next.Equal(t0.Add(48 * time.Hour)) {
		t.Errorf("next sweep %s", next)
	}
}

func TestCronSchedule(t *testing.T) {
	at := time.Date(2026, 10, 13, 10, 7, 0, 0, time.UTC) // Tuesday
	for expr, want := range map[string]string{
		"0 2 * * *":         "2026-10-14T02:00:00Z",
		"*/15 6-18 * * 1-5": "2026-10-13T10:15:00Z",
		"30 4 1 * *":        "2026-11-01T04:30:00Z",
		"0 0 * * 7":         "2026-10-18T00:00:00Z",
		"0 12 13 10 *":      "2026-10-13T12:00:00Z",
		"0 9 13 10 *":       "2027-10-13T09:00:00Z",
	} {
		c, err := parseCron(expr)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		if got := c.next(at).Format(time.RFC3339); got != want {
			t.Errorf("%s: next %s, want %s", expr, got, want)
		}
	}
	for _, bad := range []string{"* * * *", "60 * * * *", "* 24 * * *", "*/0 * * * *", "5-1 * * * *", "a * * * *"} {
		if _, err := parseCron(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if s := parseSchedule("0 2 * * *", time.Hour); s.cron == nil {
		t.Error("cron schedule not recognised")
	}
}

// recordingPlugin answers the probe for the listed hosts and records the
// targets it was given.
type recordingPlugin struct {
	name  string
	alive []string
	mu    sync.Mutex
	calls [][]string
}

func (p *recordingPlugin) Name() string { return p.name }
func (p *recordingPlugin) Probe(_ context.Context, targets []string, _ map[string]string) ([]plugins.Result, error) {
	p.mu.Lock()
	p.calls = append(p.calls, slices.Clone(targets))
	p.mu.Unlock()
	var hits []plugins.Result
	for _, t := range targets {
		if slices.Contains(p.alive, t) {
			hits = append(hits, plugins.Result{CIType: "generic_device", Name: t, IP: t})
		}
	}
	return hits, nil
}
func (p *recordingPlugin) Collect(_ context.Context, target string, _ map[string]string) (*plugins.DeviceRecord, error) {
	return plugins.CollectRecord(p.name, &plugins.Result{CIType: "generic_device", Name: target, IP: target}, nil, time.Now())
}

// TestPollsTargetTheSweptHosts: a due poll probes the hosts the last
// sweep of the scope found, not the whole range.
func TestPollsTargetTheSweptHosts(t *testing.T) {
	t.Cleanup(func() { collectorScopes = &scopeCache{} })
	var uploads, metricUploads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/monitoring/metrics") {
			metricUploads.Add(1)
		} else {
			uploads.Add(1)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	up, err := newUploader(context.Background(), testConfig(srv.URL, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	sweep := &recordingPlugin{name: "sweep", alive: []string{"10.0.0.1", "10.0.0.5"}}
	snmp := &recordingPlugin{name: "snmp", alive: []string{"10.0.0.5"}}
	for name, plug := range map[string]plugins.Plugin{"sweep": sweep, "snmp": snmp} {
		previous := pluginRegistry[name]
		pluginRegistry[name] = plug
		defer func(name string, p plugins.Plugin) { pluginRegistry[name] = p }(name, previous)
	}
	scopeID := "6f0b6f0e-0000-4000-8000-000000000001"
	collectorScopes.set([]collectorScope{{ID: scopeID, Kind: "cidr", Target: "10.0.0.0/29", Schedule: "24h", Plugins: []string{"snmp", "sweep"}}}, nil)

	cfg := testConfig(srv.URL, "")
	sched, hosts := newScheduler(), &hostCache{}
	t0 := time.Now()
	if n := runDueDiscovery(context.Background(), &cfg, up, sched, hosts, t0); n != 3 {
		t.Fatalf("first run: %d records", n)
	}
	if len(sweep.calls) != 1 || len(sweep.calls[0]) != 6 || strings.Join(snmp.calls[0], ",") != "10.0.0.1,10.0.0.5" {
		t.Fatalf("sweep %v, snmp %v", sweep.calls, snmp.calls)
	}
	if n := runDueDiscovery(context.Background(), &cfg, up, sched, hosts, t0.Add(time.Minute)); n != 0 || len(snmp.calls) != 1 {
		t.Fatal("nothing is due a minute later")
	}
	runDueDiscovery(context.Background(), &cfg, up, sched, hosts, t0.Add(15*time.Minute))
	if len(sweep.calls) != 1 || len(snmp.calls) != 2 || strings.Join(snmp.calls[1], ",") != "10.0.0.1,10.0.0.5" {
		t.Errorf("poll after 15 min: sweep %d calls, snmp %v", len(sweep.calls), snmp.calls)
	}
	if uploads.Load() != 2 {
		t.Errorf("uploads = %d", uploads.Load())
	}
	// The sweep reports the reachability of its hosts once (MON-02).
	if metricUploads.Load() != 1 {
		t.Errorf("metric uploads = %d", metricUploads.Load())
	}
}

// TestSpoolFlushDeliversTenThousandRecords covers NFR-03: the flusher runs
// on its own, independent of discovery, and delivers 10,000 spooled
// records well within 5 minutes once the backend is reachable.
func TestSpoolFlushDeliversTenThousandRecords(t *testing.T) {
	spoolDir := t.TempDir()
	var reachable atomic.Bool
	var received atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !reachable.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var batch plugins.DeviceRecordBatch
		if err = json.NewDecoder(gz).Decode(&batch); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		received.Add(int64(len(batch.Records)))
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	cfg := testConfig(srv.URL, spoolDir)
	up, err := newUploader(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const batches, perBatch = 20, 500
	for b := range batches {
		records := make([]plugins.DeviceRecord, perBatch)
		for i := range records {
			records[i] = plugins.RecordFromResult("sweep", &plugins.Result{CIType: "generic_device", Name: fmt.Sprintf("h-%d-%d", b, i),
				IP: fmt.Sprintf("10.%d.%d.%d", b, i/250, i%250+1)}, time.Now())
		}
		// The unavailable backend makes the uploader spool the batch.
		_ = up.uploadRecords(ctx, records)
	}
	if n, _ := up.spool.Len(ctx); n != batches {
		t.Fatalf("spooled %d batches", n)
	}

	reachable.Store(true)
	flushCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	start := time.Now()
	go runSpoolFlusher(flushCtx, up, time.Hour) // delivers at start and on request
	up.requestFlush()
	deadline := start.Add(2 * time.Minute)
	left := func() int { n, _ := up.spool.Len(ctx); return n }
	for (received.Load() < batches*perBatch || left() > 0) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	elapsed := time.Since(start)
	if received.Load() != batches*perBatch {
		t.Fatalf("delivered %d of %d records in %s", received.Load(), batches*perBatch, elapsed)
	}
	if elapsed > 5*time.Minute {
		t.Errorf("flushing 10,000 records took %s", elapsed)
	}
	if n := left(); n != 0 {
		t.Errorf("%d batches left in the spool", n)
	}
	t.Logf("10,000 spooled records delivered in %s", elapsed)
}
