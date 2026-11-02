package collectorcmd

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/buffer"
)

// withEdge replaces the edge requester for one test.
func withEdge(t *testing.T, fn func(ctx context.Context, suffix string, data []byte) ([]byte, error)) {
	t.Helper()
	previous := edgeRequest
	edgeRequest = fn
	t.Cleanup(func() { edgeRequest = previous })
}

// TestUploadPrefersTheEdge covers the collector side of COL-08: with the
// edge channel the records go there and not over HTTP; without it, or when
// no server answers the subject, the HTTP ingest takes them.
func TestUploadPrefersTheEdge(t *testing.T) {
	var httpUploads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpUploads.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	up, err := newUploader(context.Background(), testConfig(srv.URL, ""))
	if err != nil {
		t.Fatal(err)
	}
	rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: "edge-srv", IP: "192.0.2.10"}, time.Now().Add(-time.Minute))

	var edgeSubjects []string
	withEdge(t, func(_ context.Context, suffix string, _ []byte) ([]byte, error) {
		edgeSubjects = append(edgeSubjects, suffix)
		return []byte(`{"status":202,"license_status":"active"}`), nil
	})
	if err = up.uploadRecords(context.Background(), []plugins.DeviceRecord{rec}); err != nil {
		t.Fatal(err)
	}
	if len(edgeSubjects) != 1 || edgeSubjects[0] != edgeIngestSuffix || httpUploads.Load() != 0 {
		t.Errorf("edge %v, http %d", edgeSubjects, httpUploads.Load())
	}

	withEdge(t, func(context.Context, string, []byte) ([]byte, error) { return nil, errNoEdge })
	if err = up.uploadRecords(context.Background(), []plugins.DeviceRecord{rec}); err != nil {
		t.Fatal(err)
	}
	if httpUploads.Load() != 1 {
		t.Errorf("fallback to HTTP: %d uploads", httpUploads.Load())
	}
}

// TestEdgeRefusals: an expired license pauses the collector; a refusal for
// good (4xx other than 408/429) is errUploadRejected (the caller
// quarantines the batch), 5xx is transient (the caller spools it); an
// accepted batch names the rejected records (WP-481).
func TestEdgeRefusals(t *testing.T) {
	up, err := newUploader(context.Background(), testConfig("http://127.0.0.1:1", ""))
	if err != nil {
		t.Fatal(err)
	}
	withEdge(t, func(context.Context, string, []byte) ([]byte, error) {
		return []byte(`{"status":403,"type":"` + problemLicenseExpired + `"}`), nil
	})
	if _, err = up.postViaEdge(context.Background(), []byte(`{}`)); !errors.Is(err, errLicenseExpired) || !up.paused.Load() {
		t.Fatalf("expired license: %v, paused %v", err, up.paused.Load())
	}
	withEdge(t, func(context.Context, string, []byte) ([]byte, error) {
		return []byte(`{"status":202,"license_status":"active","result":{"accepted":3,"rejected":2}}`), nil
	})
	lost, err := up.postViaEdge(context.Background(), []byte(`{}`))
	if err != nil || up.paused.Load() || lost != 2 {
		t.Fatalf("renewed license: %v, paused %v, lost %d (want 2)", err, up.paused.Load(), lost)
	}
	withEdge(t, func(context.Context, string, []byte) ([]byte, error) {
		return []byte(`{"status":422,"error":"records[0].source missing"}`), nil
	})
	if _, err = up.postViaEdge(context.Background(), []byte(`{}`)); !errors.Is(err, errUploadRejected) {
		t.Fatalf("refused batch: %v, want errUploadRejected", err)
	}
	for _, status := range []int{408, 429, 503} {
		withEdge(t, func(context.Context, string, []byte) ([]byte, error) {
			return []byte(fmt.Sprintf(`{"status":%d,"error":"busy"}`, status)), nil
		})
		if _, err = up.postViaEdge(context.Background(), []byte(`{}`)); err == nil || errors.Is(err, errUploadRejected) {
			t.Fatalf("status %d: %v, want a transient error", status, err)
		}
	}
}

// limitedEdge is an edge fake with a maximum message size: like
// edgeRequest it refuses a larger payload with errEdgeTooLarge and takes
// the others; it records the system names of the records it took and the
// payload sizes, in delivery order.
type limitedEdge struct {
	limit int

	mu      sync.Mutex
	records []string
	sizes   []int
}

func (e *limitedEdge) request(_ context.Context, suffix string, data []byte) ([]byte, error) {
	if suffix != edgeIngestSuffix {
		return []byte(`{"status":204}`), nil
	}
	if len(data) > e.limit {
		return nil, errEdgeTooLarge
	}
	var batch plugins.DeviceRecordBatch
	if err := json.Unmarshal(data, &batch); err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range batch.Records {
		e.records = append(e.records, batch.Records[i].System.Name)
	}
	e.sizes = append(e.sizes, len(data))
	return []byte(`{"status":202,"license_status":"active"}`), nil
}

func (e *limitedEdge) took() (records []string, sizes []int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.records), slices.Clone(e.sizes)
}

// unauthenticatedAPI answers every request with 401, as the HTTP API does
// for a collector (it authenticates none, F-046).
func unauthenticatedAPI(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, `{"title":"Unauthorized","detail":"missing bearer token"}`, http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// withRecordLimit sets the size of the records payloads for one test, as
// the limit of a connected edge.
func withRecordLimit(t *testing.T, limit int) {
	t.Helper()
	previous := recordPayloadLimit
	recordPayloadLimit = func() (int, bool) { return limit, true }
	t.Cleanup(func() { recordPayloadLimit = previous })
}

// namedRecords are n sweep records named <prefix>-000 and on.
func namedRecords(prefix string, n int) []plugins.DeviceRecord {
	out := make([]plugins.DeviceRecord, n)
	for i := range out {
		out[i] = plugins.RecordFromResult("sweep", &plugins.Result{CIType: "server", Name: fmt.Sprintf("%s-%03d", prefix, i),
			IP: fmt.Sprintf("192.0.2.%d", i%250+1)}, time.Now().Add(-time.Minute))
	}
	return out
}

func namesOf(records []plugins.DeviceRecord) []string {
	out := make([]string, len(records))
	for i := range records {
		out[i] = records[i].System.Name
	}
	return out
}

// TestOversizedRecordRunIsSplitForTheEdge: a discovery run larger than the
// edge's message size goes in payloads below it, in its order; nothing
// reaches the HTTP path (it answers 401 like the real API) and the spool
// stays empty. Before, the run went as one payload, errEdgeTooLarge sent it
// to the HTTP path, the 401 spooled it, and the spool was blocked for good.
func TestOversizedRecordRunIsSplitForTheEdge(t *testing.T) {
	const limit = 4096
	edge := &limitedEdge{limit: limit}
	withEdge(t, edge.request)
	withRecordLimit(t, limit)
	srv, httpCalls := unauthenticatedAPI(t)
	ctx := context.Background()
	up, err := newUploader(ctx, testConfig(srv.URL, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	records := namedRecords("run", 100)
	if whole, _ := json.Marshal(plugins.DeviceRecordBatch{Records: records}); len(whole) <= 3*limit {
		t.Fatalf("control: the run is %d bytes, not much larger than the edge limit", len(whole))
	}
	if err = up.uploadRecords(ctx, records); err != nil {
		t.Fatalf("upload: %v", err)
	}
	took, sizes := edge.took()
	if !slices.Equal(took, namesOf(records)) {
		t.Errorf("the edge took %d records %v, want all 100 in their order", len(took), took)
	}
	if len(sizes) < 4 || slices.Max(sizes) > limit {
		t.Errorf("payload sizes %v, want several, each at most %d bytes", sizes, limit)
	}
	if n := httpCalls.Load(); n != 0 {
		t.Errorf("%d requests to the HTTP path, want none", n)
	}
	if n, _ := up.spool.Len(ctx); n != 0 {
		t.Errorf("spool holds %d batches, want none", n)
	}
}

// TestSpooledOversizedBatchIsSplit: a spooled batch larger than the edge
// takes (spooled before the split existed, or for an edge with a smaller
// limit) is replaced by its parts, which the flush delivers in order ahead
// of the later batches; the batch is acknowledged, nothing is lost and
// nothing goes to the HTTP path. Also when the known limit lets it pass
// (the edge's limit is smaller), it is halved until it fits, and the
// records keep their order. Also when the spool's free space is smaller
// than the batch: the parts take its space, so the split neither waits
// for room (spool_full) nor drops the batch, even one older than the
// minimum retention (size_limit), and the batches behind it flow.
func TestSpooledOversizedBatchIsSplit(t *testing.T) {
	for _, tc := range []struct {
		name       string
		knownLimit int
		nearCap    bool
		age        time.Duration
	}{
		{"known edge limit", 4096, false, 0},
		{"edge limit smaller than known", defaultRecordPayloadLimit, false, 0},
		{"spool near its size limit", 4096, true, 0},
		{"spool near its size limit, batch older than the retention", defaultRecordPayloadLimit, true, 25 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			edge := &limitedEdge{limit: 4096}
			withEdge(t, edge.request)
			withRecordLimit(t, tc.knownLimit)
			srv, httpCalls := unauthenticatedAPI(t)
			ctx := context.Background()
			spoolDir := t.TempDir()
			up, err := newUploader(ctx, testConfig(srv.URL, spoolDir))
			if err != nil {
				t.Fatal(err)
			}
			old, later := namedRecords("old", 60), namedRecords("later", 2)
			base := time.Now().Add(-time.Hour)
			for i, recs := range [][]plugins.DeviceRecord{old, later} {
				payload, _ := json.Marshal(plugins.DeviceRecordBatch{Records: recs})
				if err = up.spoolResults(ctx, payload, base.Add(time.Duration(i)*time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			if tc.age > 0 {
				backdateSpool(t, spoolDir, tc.age)
			}
			if tc.nearCap {
				stats, _ := up.spool.Stats(ctx)
				up.spool.MaxBytes = stats.Bytes + 2000
				if msgs, _ := up.spool.PeekBatch(ctx, 1); len(msgs) != 1 || len(msgs[0].Payload) <= 2000 {
					t.Fatalf("control: the head batch must not fit the free space of the spool")
				}
			}
			up.flushSpool(ctx)

			took, sizes := edge.took()
			if want := append(namesOf(old), namesOf(later)...); !slices.Equal(took, want) {
				t.Errorf("delivered %v, want the 60 old records in order, then the later batch", took)
			}
			if len(sizes) == 0 || slices.Max(sizes) > 4096 {
				t.Errorf("payload sizes %v", sizes)
			}
			if n, _ := up.spool.Len(ctx); n != 0 {
				t.Errorf("spool holds %d batches after the flush, want none", n)
			}
			stats, _ := up.spool.Stats(ctx)
			if stats.DroppedMessages != 0 || quarantined(t, spoolDir) != 0 || httpCalls.Load() != 0 {
				t.Errorf("loss %+v, quarantine %d, HTTP calls %d; want none", stats, quarantined(t, spoolDir), httpCalls.Load())
			}
			if split := logLines(t, logs, "spooled batch split for the edge's maximum message size"); len(split) == 0 || split[0]["event"] != "collector.spool.split" {
				t.Errorf("split log %v", split)
			}
		})
	}
}

// TestOversizedSingleRecordIsQuarantined: a record that does not fit an
// edge message of its own is quarantined and counted as lost, the run
// reports it, and it blocks neither the run's other records nor the spool.
func TestOversizedSingleRecordIsQuarantined(t *testing.T) {
	logs := captureLogs(t)
	const limit = 4096
	edge := &limitedEdge{limit: limit}
	withEdge(t, edge.request)
	withRecordLimit(t, limit)
	srv, httpCalls := unauthenticatedAPI(t)
	ctx := context.Background()
	spoolDir := t.TempDir()
	up, err := newUploader(ctx, testConfig(srv.URL, spoolDir))
	if err != nil {
		t.Fatal(err)
	}
	records := namedRecords("r", 3)
	records[1].System.Description = strings.Repeat("x", 2*limit)
	err = up.uploadRecords(ctx, records)
	if !errors.Is(err, errRecordTooLarge) {
		t.Fatalf("upload: %v, want errRecordTooLarge", err)
	}
	if took, _ := edge.took(); !slices.Equal(took, []string{"r-000", "r-002"}) {
		t.Errorf("the edge took %v, want the two records that fit", took)
	}
	if q := quarantined(t, spoolDir); q != 1 {
		t.Errorf("quarantine holds %d files, want the oversized record", q)
	}
	if lines := logLines(t, logs, "device record larger than the maximum message size of the edge; quarantined and counted as lost"); len(lines) != 1 ||
		lines[0]["event"] != "collector.record.oversized" || lines[0]["level"] != "ERROR" {
		t.Errorf("oversized log %v", lines)
	}

	// A spooled batch of one such record is quarantined; the batch behind
	// it is delivered.
	big, _ := json.Marshal(plugins.DeviceRecordBatch{Records: records[1:2]})
	small, _ := json.Marshal(plugins.DeviceRecordBatch{Records: namedRecords("behind", 1)})
	base := time.Now().Add(-time.Hour)
	if err = up.spoolResults(ctx, big, base); err != nil {
		t.Fatal(err)
	}
	if err = up.spoolResults(ctx, small, base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	up.flushSpool(ctx)
	if took, _ := edge.took(); !slices.Equal(took, []string{"r-000", "r-002", "behind-000"}) {
		t.Errorf("the edge took %v, want the batch behind the oversized one", took)
	}
	if n, _ := up.spool.Len(ctx); n != 0 || quarantined(t, spoolDir) != 2 {
		t.Errorf("spool %d, quarantine %d; want 0 and 2", n, quarantined(t, spoolDir))
	}
	if stats, _ := up.spool.Stats(ctx); stats.DroppedMessages != 2 {
		t.Errorf("dropped messages = %d, want 2", stats.DroppedMessages)
	}
	if n := httpCalls.Load(); n != 0 {
		t.Errorf("%d requests to the HTTP path", n)
	}
}

// TestRecordAbovePackingSizeWaitsForTheEdge: while the edge's limit is
// unknown (no connection), a record larger than the packing size (512 KiB)
// but within the NATS default (1 MiB) is spooled in a payload of its own,
// not quarantined; the connected edge then takes it with the run's other
// records, in their order. A record the edge refuses even alone is
// quarantined only then, and blocks nothing behind it.
func TestRecordAbovePackingSizeWaitsForTheEdge(t *testing.T) {
	previous := scopeChannel.Load()
	scopeChannel.Store(nil)
	t.Cleanup(func() { scopeChannel.Store(previous) })
	if limit, known := recordPayloadLimit(); known || limit != defaultRecordPayloadLimit {
		t.Fatalf("control: limit %d known %v without an edge connection", limit, known)
	}
	withEdge(t, func(context.Context, string, []byte) ([]byte, error) { return nil, errNoEdge })
	srv, _ := unauthenticatedAPI(t)
	ctx := context.Background()
	spoolDir := t.TempDir()
	up, err := newUploader(ctx, testConfig(srv.URL, spoolDir))
	if err != nil {
		t.Fatal(err)
	}
	records := namedRecords("run", 3)
	records[1].System.Description = strings.Repeat("x", 700<<10)
	huge := namedRecords("huge", 1)
	huge[0].System.Description = strings.Repeat("y", 3<<19)
	base := time.Now().Add(-time.Hour)
	if err = up.uploadRecordsAt(ctx, records, base); err == nil || errors.Is(err, errRecordTooLarge) {
		t.Fatalf("upload without the edge: %v, want a transient failure, not errRecordTooLarge", err)
	}
	if err = up.uploadRecordsAt(ctx, huge, base.Add(time.Second)); errors.Is(err, errRecordTooLarge) {
		t.Fatalf("upload of a 1.5 MiB record without the edge: %v, want it queued behind the backlog", err)
	}
	if err = up.uploadRecordsAt(ctx, namedRecords("later", 1), base.Add(2*time.Second)); err != nil {
		t.Fatalf("queueing behind the backlog: %v", err)
	}
	if n, _ := up.spool.Len(ctx); n != 5 || quarantined(t, spoolDir) != 0 {
		t.Fatalf("spool %d, quarantine %d; want 5 payloads (3 of the run, the huge record, the later run) and none quarantined",
			n, quarantined(t, spoolDir))
	}

	edge := &limitedEdge{limit: 1 << 20}
	withEdge(t, edge.request)
	up.flushSpool(ctx)
	if took, _ := edge.took(); !slices.Equal(took, []string{"run-000", "run-001", "run-002", "later-000"}) {
		t.Errorf("the edge took %v, want the run in its order, then the later run", took)
	}
	if n, _ := up.spool.Len(ctx); n != 0 || quarantined(t, spoolDir) != 1 {
		t.Errorf("spool %d, quarantine %d; want 0 and the record the edge refused", n, quarantined(t, spoolDir))
	}
	if stats, _ := up.spool.Stats(ctx); stats.DroppedMessages != 1 {
		t.Errorf("dropped messages = %d, want 1", stats.DroppedMessages)
	}
}

// TestHTTPFallback401KeepsBatchesForTheEdge: without the edge the HTTP
// path answers 401 like the real API: the batch is spooled and reported
// as failed, not as uploaded, the cause is logged once per edge outage
// (collector.upload.unauthenticated), and the next flush with the edge
// delivers the spool there.
func TestHTTPFallback401KeepsBatchesForTheEdge(t *testing.T) {
	logs := captureLogs(t)
	t.Setenv("RETICORA_NATS_URL", "")
	httpUnauthenticatedLogged.Store(false)
	srv, httpCalls := unauthenticatedAPI(t)
	ctx := context.Background()
	up, err := newUploader(ctx, testConfig(srv.URL, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	withEdge(t, func(context.Context, string, []byte) ([]byte, error) { return nil, errNoEdge })
	if _, runErr := uploadDiscovered(ctx, up, namedRecords("first", 1), time.Now()); runErr == nil {
		t.Fatal("a batch the HTTP path refused is reported as uploaded")
	}
	if lines := logLines(t, logs, "discovery records uploaded"); len(lines) != 0 {
		t.Errorf("logged as uploaded: %v", lines)
	}
	if err = up.uploadRecords(ctx, namedRecords("second", 1)); err != nil {
		t.Fatalf("queueing behind the backlog: %v", err)
	}
	up.flushSpool(ctx)
	if n, _ := up.spool.Len(ctx); n != 2 || httpCalls.Load() < 2 {
		t.Fatalf("spool %d, HTTP calls %d; want both batches spooled after the 401s", n, httpCalls.Load())
	}
	const unauthenticated = "HTTP ingest answered 401: the HTTP API authenticates no collector; records wait in the spool until the mTLS edge is connected"
	if lines := logLines(t, logs, unauthenticated); len(lines) != 1 || lines[0]["event"] != "collector.upload.unauthenticated" ||
		lines[0]["nats_url"] != "tls://127.0.0.1:4222" {
		t.Errorf("unauthenticated log %v, want one per edge outage with the NATS endpoint", lines)
	}

	edge := &limitedEdge{limit: 1 << 20}
	withEdge(t, edge.request)
	before := httpCalls.Load()
	up.flushSpool(ctx)
	if took, _ := edge.took(); !slices.Equal(took, []string{"first-000", "second-000"}) {
		t.Errorf("the edge took %v, want both spooled batches in order", took)
	}
	if n, _ := up.spool.Len(ctx); n != 0 || httpCalls.Load() != before {
		t.Errorf("spool %d, further HTTP calls %d", n, httpCalls.Load()-before)
	}

	// The next outage (after the edge was connected) warns again.
	(&natsConnLog{url: "tls://127.0.0.1:4222"}).connected(true)
	withEdge(t, func(context.Context, string, []byte) ([]byte, error) { return nil, errNoEdge })
	_ = up.uploadRecords(ctx, namedRecords("third", 1))
	if n := len(logLines(t, logs, unauthenticated)); n != 2 {
		t.Errorf("unauthenticated warnings = %d, want 2 (one per edge outage)", n)
	}
}

// TestSpoolFullInASplitRunCountsEveryPayload: when the spool fills up
// while the payloads of a run are spooled (no edge, the HTTP path answers
// 401), the payloads after the refused one are counted as lost too: each
// payload is either in the spool or counted (NFR-04). Before, only the
// refused payload was counted.
func TestSpoolFullInASplitRunCountsEveryPayload(t *testing.T) {
	const limit = 4096
	withRecordLimit(t, limit)
	withEdge(t, func(context.Context, string, []byte) ([]byte, error) { return nil, errNoEdge })
	srv, _ := unauthenticatedAPI(t)
	ctx := context.Background()
	cfg := testConfig(srv.URL, t.TempDir())
	cfg.SpoolMaxBytes = 12000
	up, err := newUploader(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	records := namedRecords("run", 100)
	raws, err := marshalRecords(records)
	if err != nil {
		t.Fatal(err)
	}
	payloads, _ := splitRecords(raws, limit, true)
	if err = up.uploadRecords(ctx, records); !errors.Is(err, buffer.ErrSpoolFull) {
		t.Fatalf("upload: %v, want the full spool reported", err)
	}
	spooled, _ := up.spool.Len(ctx)
	stats, _ := up.spool.Stats(ctx)
	if spooled == 0 || spooled > len(payloads)-2 {
		t.Fatalf("control: %d of %d payloads spooled, want the spool full with at least two payloads left", spooled, len(payloads))
	}
	if total := spooled + int(stats.DroppedMessages); total != len(payloads) || stats.DroppedBytes == 0 {
		t.Errorf("%d payloads spooled and %d counted as lost (%d bytes), want all %d payloads accounted for",
			spooled, stats.DroppedMessages, stats.DroppedBytes, len(payloads))
	}
}

// TestHeartbeatOverTheEdge: the heartbeat takes the edge and reads the
// license state from the reply; without the edge it is not handled there.
func TestHeartbeatOverTheEdge(t *testing.T) {
	withEdge(t, func(_ context.Context, suffix string, _ []byte) ([]byte, error) {
		if suffix != edgeHeartbeatSuffix {
			t.Errorf("subject suffix %q", suffix)
		}
		return []byte(`{"status":204,"license_status":"active","collector_status":"active"}`), nil
	})
	if license, ok, handled := heartbeatViaEdge(context.Background(), []byte(`{}`)); !handled || !ok || license != "active" {
		t.Errorf("edge heartbeat: %q %v %v", license, ok, handled)
	}
	withEdge(t, func(context.Context, string, []byte) ([]byte, error) { return nil, errNoEdge })
	if _, _, handled := heartbeatViaEdge(context.Background(), []byte(`{}`)); handled {
		t.Error("handled without the edge")
	}
}
