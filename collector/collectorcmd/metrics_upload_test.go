package collectorcmd

// The collector's metric samples over the edge (WP-426, MON-02; F-046).
//
//   - TestAuditT7MetricSamplesReachTheServerAuthenticated – WP-190 P1
//     (moved from audit_t7_metrics_upload_auditdefect_test.go): with the
//     collector's mutually authenticated edge channel up (as after
//     enrollment), polled metric samples reach the server through an
//     authenticated path. Before, uploadMetricSamples -> postMetricsPayload
//     POSTed /api/v1/monitoring/metrics with only Content-Type,
//     X-Organization-ID and X-Collector-ID; the backend HTTP API requires a
//     bearer token (middleware/auth.go "missing bearer token"; no
//     client-certificate authentication on the HTTP API) and the edge had
//     no metrics subject, so the samples were dropped on every real
//     installation. The fake backend below answers 401 without a bearer,
//     exactly like the real middleware; now the samples go on
//     collector.<org>.<id>.metrics and nothing goes to the HTTP API.
//   - TestMetricSamplesWaitForTheEdge: without the edge no request goes to
//     the HTTP API; the samples wait in a buffer of at most 10 batches (the
//     oldest is dropped and logged as an error with its count) and go,
//     oldest first, at the next edge contact (upload or flusher round).
//   - TestMetricRefusalsAreLoggedAndDropped: a refusal (status >= 400) is
//     the caller's error with status and count, an older refused or
//     oversized batch is logged as an error; neither is kept. A failed
//     request keeps the batch for the next attempt.
//   - TestMetricSamplesAreSentInBoundedBatches: a large sweep goes in
//     batches of at most 1000 samples.
//
// Retest (F-046):
//
//	cd collector && go test -count=1 -v -run '^TestAuditT7MetricSamplesReachTheServerAuthenticated$' ./collectorcmd/

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

func TestAuditT7MetricSamplesReachTheServerAuthenticated(t *testing.T) {
	var (
		mu        sync.Mutex
		accepted  int
		refused   int
		httpPaths []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		httpPaths = append(httpPaths, r.URL.Path)
		// The backend's HTTP API authenticates every /api/v1 request with a
		// bearer token; X-Organization-ID / X-Collector-ID are not credentials.
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || len(r.Header.Get("Authorization")) <= len("Bearer ") {
			refused++
			http.Error(w, `{"title":"Unauthorized","detail":"missing bearer token"}`, http.StatusUnauthorized)
			return
		}
		accepted++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	up, err := newUploader(context.Background(), testConfig(srv.URL, ""))
	if err != nil {
		t.Fatal(err)
	}

	// The enrolled collector holds its mutually authenticated edge channel:
	// the server answers on the collector's subject tree.
	var edgeSuffixes []string
	withEdge(t, func(_ context.Context, suffix string, _ []byte) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		edgeSuffixes = append(edgeSuffixes, suffix)
		return []byte(`{"status":202,"license_status":"active"}`), nil
	})

	samples := []plugins.MetricSample{
		{Name: "sweep_reachable", Value: 1, Labels: map[string]string{"target": "192.0.2.10"}},
		{Name: "ups_battery_charge_percent", Value: 97, Labels: map[string]string{"target": "192.0.2.20"}},
	}
	uploadErr := up.uploadMetricSamples(context.Background(), samples)

	mu.Lock()
	defer mu.Unlock()
	viaEdge := 0
	for _, s := range edgeSuffixes {
		if s != edgeHeartbeatSuffix {
			viaEdge++
		}
	}
	if viaEdge == 0 && accepted == 0 {
		t.Errorf("metric samples did not reach the server over an authenticated path: edge requests %v, HTTP %v accepted=%d refused(401)=%d, uploadMetricSamples error=%v "+
			"(MON-02: collector baseline metrics must be stored; the HTTP upload carries no credential and there is no edge metrics subject)",
			edgeSuffixes, httpPaths, accepted, refused, uploadErr)
	}
	if uploadErr != nil && (viaEdge > 0 || accepted > 0) {
		t.Errorf("samples were delivered but uploadMetricSamples reported %v", uploadErr)
	}
	// The edge subject of the samples is .metrics; no request without a
	// credential reaches the HTTP API.
	if len(edgeSuffixes) != 1 || edgeSuffixes[0] != edgeMetricsSuffix {
		t.Errorf("edge requests %v, want one on %s", edgeSuffixes, edgeMetricsSuffix)
	}
	if len(httpPaths) != 0 {
		t.Errorf("HTTP requests %v, want none", httpPaths)
	}
}

// captureLogs sends the default logger to a buffer for one test.
func captureLogs(t *testing.T) *lockedBuffer {
	t.Helper()
	logs := &lockedBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return logs
}

// logLines returns the log records with the message msg.
func logLines(t *testing.T, logs *lockedBuffer, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		if rec["msg"] == msg {
			out = append(out, rec)
		}
	}
	return out
}

// sample is a sweep sample of target.
func sample(target string) plugins.MetricSample {
	return plugins.MetricSample{Name: "sweep_reachable", Value: 1, Labels: map[string]string{"target": target, "kind": "sweep"}}
}

// targetsOf returns the target labels of a metrics payload.
func targetsOf(t *testing.T, payload []byte) []string {
	t.Helper()
	var metrics []struct {
		Labels map[string]string `json:"labels"`
	}
	if err := json.Unmarshal(payload, &metrics); err != nil {
		t.Fatalf("metrics payload: %v", err)
	}
	out := make([]string, 0, len(metrics))
	for _, m := range metrics {
		out = append(out, m.Labels["target"])
	}
	return out
}

func TestMetricSamplesWaitForTheEdge(t *testing.T) {
	logs := captureLogs(t)
	var httpRequests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpRequests.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	up, err := newUploader(context.Background(), testConfig(srv.URL, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// No edge channel: 12 sweeps, one batch each.
	withEdge(t, func(context.Context, string, []byte) ([]byte, error) { return nil, errNoEdge })
	for i := range 12 {
		if err = up.uploadMetricSamples(ctx, []plugins.MetricSample{sample(fmt.Sprintf("192.0.2.%d", i+1))}); err != nil {
			t.Fatalf("sweep %d without the edge: %v, want the samples held", i+1, err)
		}
	}
	if n := httpRequests.Load(); n != 0 {
		t.Fatalf("F-046: %d requests without a credential reached the HTTP API", n)
	}
	if n := up.metrics.len(); n != maxMetricBatches {
		t.Fatalf("buffered batches = %d, want %d", n, maxMetricBatches)
	}
	dropped := logLines(t, logs, "metric samples dropped: the edge channel took none of the buffered batches")
	if len(dropped) != 2 {
		t.Fatalf("drop log lines = %d, want 2 (the two oldest batches)", len(dropped))
	}
	for _, rec := range dropped {
		if rec["level"] != "ERROR" || rec["count"] != float64(1) {
			t.Errorf("drop logged as %v with count %v, want ERROR with the count", rec["level"], rec["count"])
		}
	}
	if held := logLines(t, logs, "metric samples held until the edge channel takes them"); len(held) != 12 {
		t.Errorf("held log lines = %d, want one per sweep", len(held))
	}

	// The edge is back: the flusher's round sends the buffered batches,
	// oldest first.
	var sent []string
	withEdge(t, func(_ context.Context, suffix string, data []byte) ([]byte, error) {
		if suffix != edgeMetricsSuffix {
			t.Errorf("suffix %q", suffix)
		}
		sent = append(sent, targetsOf(t, data)...)
		return []byte(`{"status":202,"ingested":1,"unassigned":0}`), nil
	})
	up.flushMetrics(ctx)
	want := []string{"192.0.2.3", "192.0.2.4", "192.0.2.5", "192.0.2.6", "192.0.2.7", "192.0.2.8", "192.0.2.9", "192.0.2.10", "192.0.2.11", "192.0.2.12"}
	if strings.Join(sent, ",") != strings.Join(want, ",") {
		t.Errorf("delivered %v, want %v", sent, want)
	}
	if n := up.metrics.len(); n != 0 {
		t.Errorf("%d batches left after the flush", n)
	}

	// The next upload with the edge goes after what is still buffered.
	sent = nil
	withEdge(t, func(context.Context, string, []byte) ([]byte, error) { return nil, errNoEdge })
	if err = up.uploadMetricSamples(ctx, []plugins.MetricSample{sample("192.0.2.50")}); err != nil {
		t.Fatal(err)
	}
	withEdge(t, func(_ context.Context, _ string, data []byte) ([]byte, error) {
		sent = append(sent, targetsOf(t, data)...)
		return []byte(`{"status":202,"ingested":1,"unassigned":0}`), nil
	})
	if err = up.uploadMetricSamples(ctx, []plugins.MetricSample{sample("192.0.2.51")}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(sent, ",") != "192.0.2.50,192.0.2.51" {
		t.Errorf("delivered %v, want the buffered batch first", sent)
	}
	if n := httpRequests.Load(); n != 0 {
		t.Errorf("F-046: %d requests reached the HTTP API", n)
	}
}

func TestMetricRefusalsAreLoggedAndDropped(t *testing.T) {
	logs := captureLogs(t)
	up, err := newUploader(context.Background(), testConfig("http://127.0.0.1:1", ""))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// The server refuses the batch (no monitoring entitlement): the caller
	// gets the status, the batch is not kept.
	withEdge(t, func(context.Context, string, []byte) ([]byte, error) {
		return []byte(`{"status":403,"type":"https://reticora.io/problems/feature-not-entitled","error":"feature \"monitoring\" is not included in the current plan"}`), nil
	})
	err = up.uploadMetricSamples(ctx, []plugins.MetricSample{sample("192.0.2.1"), sample("192.0.2.2")})
	var refused *metricsRefusedError
	if !errors.As(err, &refused) || refused.status != http.StatusForbidden || !strings.Contains(err.Error(), "403") {
		t.Fatalf("refused batch: %v, want the refusal with status 403", err)
	}
	if n := up.metrics.len(); n != 0 {
		t.Errorf("the refused batch is kept (%d batches)", n)
	}

	// A failed request keeps the batch; a later refusal of it, now an older
	// batch, is logged with status and count.
	withEdge(t, func(context.Context, string, []byte) ([]byte, error) { return nil, errors.New("nats: timeout") })
	if err = up.uploadMetricSamples(ctx, []plugins.MetricSample{sample("192.0.2.3")}); err != nil {
		t.Fatalf("failed request: %v, want the batch kept", err)
	}
	if n := up.metrics.len(); n != 1 {
		t.Fatalf("buffered batches after a failed request = %d, want 1", n)
	}
	withEdge(t, func(context.Context, string, []byte) ([]byte, error) {
		return []byte(`{"status":400,"error":"metric name is required"}`), nil
	})
	up.flushMetrics(ctx)
	if n := up.metrics.len(); n != 0 {
		t.Errorf("the refused older batch is kept (%d batches)", n)
	}
	lines := logLines(t, logs, "metric samples refused")
	if len(lines) != 1 || lines[0]["level"] != "ERROR" || lines[0]["status"] != float64(400) || lines[0]["count"] != float64(1) {
		t.Errorf("refusal log %v, want one ERROR with status 400 and count 1", lines)
	}

	// An oversized batch is dropped and reported.
	withEdge(t, func(context.Context, string, []byte) ([]byte, error) { return nil, errEdgeTooLarge })
	if err = up.uploadMetricSamples(ctx, []plugins.MetricSample{sample("192.0.2.4")}); err == nil || !errors.Is(err, errEdgeTooLarge) {
		t.Errorf("oversized batch: %v, want the drop reported", err)
	}
	if n := up.metrics.len(); n != 0 {
		t.Errorf("the oversized batch is kept (%d batches)", n)
	}
}

func TestMetricSamplesAreSentInBoundedBatches(t *testing.T) {
	up, err := newUploader(context.Background(), testConfig("http://127.0.0.1:1", ""))
	if err != nil {
		t.Fatal(err)
	}
	var sizes []int
	withEdge(t, func(_ context.Context, _ string, data []byte) ([]byte, error) {
		sizes = append(sizes, len(targetsOf(t, data)))
		return []byte(`{"status":202}`), nil
	})
	samples := make([]plugins.MetricSample, 2500)
	for i := range samples {
		samples[i] = sample(fmt.Sprintf("10.%d.%d.%d", i/65536, i/256%256, i%256))
	}
	if err = up.uploadMetricSamples(context.Background(), samples); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(sizes) != "[1000 1000 500]" {
		t.Errorf("batch sizes %v, want [1000 1000 500]", sizes)
	}
}
