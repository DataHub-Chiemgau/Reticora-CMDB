package probe_test

// Tests of the probe edge subjects (WP-959, PRB-05, PRB-07, NFR-11). The
// validation, entitlement and alive cases call the edge directly; the cases
// with the stream, the dispatcher and the handler pool run against the NATS
// server with JetStream of TEST_NATS_URL (docker compose; skipped without
// it), each on a stream and subject root of its own:
//
//	cd backend && TEST_NATS_URL=nats://localhost:4222 go test -count=1 -run 'ProbeEdge' -bench ProbeEdge ./internal/probe/

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

const (
	testOrg       = "6f0b6f0e-0000-4000-8000-0000000000aa"
	testCollector = "6f0b6f0e-0000-4000-8000-0000000000bb"
)

// reply is the answer of a probe subject as the collector reads it.
type reply struct {
	Status     int        `json:"status"`
	Type       string     `json:"type"`
	Error      string     `json:"error"`
	Seq        *uint64    `json:"seq"`
	Duplicate  bool       `json:"duplicate"`
	ServerTime *time.Time `json:"server_time"`
}

func asReply(t testing.TB, v any) reply {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var r reply
	if err = json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func encode(t testing.TB, m probewire.Message) []byte {
	t.Helper()
	b, err := probewire.Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func stateChange(seq uint64) *probewire.StateChange {
	return &probewire.StateChange{
		ProbeID: "p-1", Epoch: 3, Seq: seq, From: probewire.StateUp, To: probewire.StateDown,
		Reason: probewire.ReasonTimeout, ObservedAt: time.Now().UTC(),
		LastResult: &probewire.Result{ProbeID: "p-1", Epoch: 3, Seq: seq, ObservedAt: time.Now().UTC(),
			Status: probewire.StatusFail, Code: probewire.ReasonTimeout},
	}
}

func resultBatch(collectorID string, from uint64, n int) *probewire.ResultBatch {
	b := &probewire.ResultBatch{CollectorID: collectorID}
	now := time.Now().UTC()
	for i := range n {
		rtt := 1.5
		b.Results = append(b.Results, probewire.Result{ProbeID: "p-" + string(rune('a'+i%26)), Epoch: 3,
			Seq: from + uint64(i), ObservedAt: now, Status: probewire.StatusOK, RTTMS: &rtt})
	}
	return b
}

func alive() *probewire.Alive {
	return &probewire.Alive{CollectorTime: time.Now().UTC(), ConfigVersion: 7, Probes: 10, ResultsPerS: 0.3,
		QueueState: probewire.QueueOK, ICMPCapability: probewire.ICMPRaw}
}

// TestProbeEdgeValidatesSizeVersionAndCollector: a message over 256 KiB,
// with an unknown major schema version, with a collector_id other than the
// authenticated one or with an invalid field is refused before anything is
// stored (AK2); the edge has no stream here, so a publish would panic.
func TestProbeEdgeValidatesSizeVersionAndCollector(t *testing.T) {
	edge := probe.NewEdge(nil, probe.DefaultStream(0))
	ctx := context.Background()
	handlers := map[string]func(context.Context, string, string, []byte) any{
		"state": edge.ProbeState, "results": edge.ProbeResults, "alive": edge.ProbeAlive,
	}
	valid := map[string][]byte{
		"state":   encode(t, stateChange(1)),
		"results": encode(t, resultBatch(testCollector, 1, 3)),
		"alive":   encode(t, alive()),
	}
	for name, handle := range handlers {
		big := make([]byte, probe.MaxMessageBytes+1)
		if r := asReply(t, handle(ctx, testOrg, testCollector, big)); r.Status != http.StatusRequestEntityTooLarge {
			t.Errorf("%s over 256 KiB: %+v, want 413", name, r)
		}
		other := strings.Replace(string(valid[name]), `"schema_version":"1.0"`, `"schema_version":"2.0"`, 1)
		if r := asReply(t, handle(ctx, testOrg, testCollector, []byte(other))); r.Status != http.StatusBadRequest ||
			!strings.Contains(r.Error, "schema_version") {
			t.Errorf("%s with schema 2.0: %+v, want 400", name, r)
		}
		if r := asReply(t, handle(ctx, testOrg, testCollector, []byte(`not json`))); r.Status != http.StatusBadRequest {
			t.Errorf("%s not JSON: %+v, want 400", name, r)
		}
		var m map[string]any
		_ = json.Unmarshal(valid[name], &m)
		m["collector_id"] = "6f0b6f0e-0000-4000-8000-0000000000cc"
		foreign, _ := json.Marshal(m)
		if r := asReply(t, handle(ctx, testOrg, testCollector, foreign)); r.Status != http.StatusUnprocessableEntity ||
			r.Error != "collector_id: invalid" {
			t.Errorf("%s of another collector: %+v, want 422 collector_id", name, r)
		}
	}
	// Invalid fields: 422 with the field, nothing published.
	bad := stateChange(1)
	bad.To = "sideways"
	if r := asReply(t, edge.ProbeState(ctx, testOrg, testCollector, encode(t, bad))); r.Status != http.StatusUnprocessableEntity || r.Error != "to: invalid" {
		t.Errorf("state with invalid to: %+v", r)
	}
	if r := asReply(t, edge.ProbeResults(ctx, testOrg, testCollector, encode(t, resultBatch(testCollector, 1, 0)))); r.Status != http.StatusUnprocessableEntity || r.Error != "results: invalid" {
		t.Errorf("empty batch: %+v", r)
	}
	if r := asReply(t, edge.ProbeResults(ctx, testOrg, testCollector, encode(t, resultBatch(testCollector, 1, probewire.MaxBatchResults+1)))); r.Status != http.StatusUnprocessableEntity {
		t.Errorf("batch over %d results: %+v", probewire.MaxBatchResults, r)
	}
	batch := resultBatch(testCollector, 1, 2)
	batch.Results[1].Status = "maybe"
	if r := asReply(t, edge.ProbeResults(ctx, testOrg, testCollector, encode(t, batch))); r.Status != http.StatusUnprocessableEntity || r.Error != "results[1]: invalid" {
		t.Errorf("batch with an invalid result: %+v", r)
	}
	a := alive()
	a.ResultsPerS = -1
	if r := asReply(t, edge.ProbeAlive(ctx, testOrg, testCollector, encode(t, a))); r.Status != http.StatusUnprocessableEntity {
		t.Errorf("alive with negative load: %+v", r)
	}
}

type features map[string]entitlement.Status

func (f features) Status(_ context.Context, orgID, _ string) (entitlement.Entitlement, entitlement.Status, error) {
	if s, ok := f[orgID]; ok {
		return entitlement.Entitlement{}, s, nil
	}
	return entitlement.Entitlement{}, entitlement.StatusNotEntitled, nil
}

// TestProbeEdgeRefusesWithoutMonitoringFeature: without the feature
// monitoring all three subjects answer 403 feature-not-entitled (AK4), the
// collector pauses its uplink on it (WP-955); nothing is validated or
// stored, and the alive hooks do not run.
func TestProbeEdgeRefusesWithoutMonitoringFeature(t *testing.T) {
	edge := probe.NewEdge(nil, probe.DefaultStream(0)).WithFeatures(features{})
	edge.OnAlive(func(string, string, probewire.Alive, time.Time) { t.Error("alive hook ran without monitoring") })
	ctx := context.Background()
	for name, call := range map[string]any{
		"state":   edge.ProbeState(ctx, testOrg, testCollector, encode(t, stateChange(1))),
		"results": edge.ProbeResults(ctx, testOrg, testCollector, encode(t, resultBatch(testCollector, 1, 1))),
		"alive":   edge.ProbeAlive(ctx, testOrg, testCollector, encode(t, alive())),
	} {
		r := asReply(t, call)
		if r.Status != http.StatusForbidden || r.Type != api.ProblemFeatureNotEntitled {
			t.Errorf("%s without monitoring: %+v, want 403 %s", name, r, api.ProblemFeatureNotEntitled)
		}
	}
	entitled := probe.NewEdge(nil, probe.DefaultStream(0)).WithFeatures(features{testOrg: entitlement.StatusActive})
	if r := asReply(t, entitled.ProbeAlive(ctx, testOrg, testCollector, encode(t, alive()))); r.Status != http.StatusOK {
		t.Errorf("alive with monitoring: %+v, want 200", r)
	}
}

// TestProbeEdgeAliveReturnsServerTime: .probe.alive answers with the server
// time (AliveReply) and hands the message with its receipt time to the
// hooks of WP-962 and WP-963 (AK5).
func TestProbeEdgeAliveReturnsServerTime(t *testing.T) {
	edge := probe.NewEdge(nil, probe.DefaultStream(0))
	var got struct {
		org, collector string
		alive          probewire.Alive
		received       time.Time
	}
	calls := 0
	edge.OnAlive(func(orgID, collectorID string, a probewire.Alive, receivedAt time.Time) {
		calls++
		got.org, got.collector, got.alive, got.received = orgID, collectorID, a, receivedAt
	})
	sent := alive()
	before := time.Now().UTC()
	r := asReply(t, edge.ProbeAlive(context.Background(), testOrg, testCollector, encode(t, sent)))
	after := time.Now().UTC()
	if r.Status != http.StatusOK || r.ServerTime == nil {
		t.Fatalf("alive: %+v, want 200 with server_time", r)
	}
	if r.ServerTime.Before(before) || r.ServerTime.After(after) {
		t.Errorf("server_time %s outside [%s, %s]", r.ServerTime, before, after)
	}
	if r.Seq != nil {
		t.Errorf("alive answered a seq: %d", *r.Seq)
	}
	if calls != 1 || got.org != testOrg || got.collector != testCollector || got.alive.ConfigVersion != sent.ConfigVersion ||
		got.received.Before(before) || got.received.After(*r.ServerTime) {
		t.Errorf("hook: %d calls, %+v", calls, got)
	}
	missing := alive()
	missing.CollectorTime = time.Time{}
	if r = asReply(t, edge.ProbeAlive(context.Background(), testOrg, testCollector, encode(t, missing))); r.Status != http.StatusUnprocessableEntity {
		t.Errorf("alive without collector_time: %+v", r)
	}
}

// natsJetStream connects to TEST_NATS_URL and creates a probe stream of
// its own (removed afterwards).
func natsJetStream(t testing.TB) (*nats.Conn, jetstream.JetStream, jetstream.Stream, probe.StreamConfig) {
	t.Helper()
	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Skip("TEST_NATS_URL not set")
	}
	nc, err := nats.Connect(url, nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatalf("connect %s: %v", url, err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	suffix := randomHex(t)
	cfg := probe.StreamConfig{Name: "RETICORA_PROBE_TEST_" + strings.ToUpper(suffix), SubjectPrefix: "probetest" + suffix, MaxBytes: 64 << 20}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := probe.EnsureStream(ctx, js, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = js.DeleteStream(context.Background(), cfg.Name) })
	return nc, js, stream, cfg
}

func randomHex(t testing.TB) string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func randomUUID(t testing.TB) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func streamMsgs(t testing.TB, stream jetstream.Stream) uint64 {
	t.Helper()
	info, err := stream.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return info.State.Msgs
}

// TestProbeEdgeRepliesAfterPubAck: a state change and a result batch are in
// the stream under probe.state.<org>.<collector> and
// probe.results.<org>.<collector> with the deduplication ID
// <collector>:<kind>:<epoch>:<seq_from>-<seq_to> when the edge answers 202 with
// the highest seq; a stream that does not acknowledge yields 503 and no
// acknowledgement for the collector (AK2, AK3, PRB-07).
func TestProbeEdgeRepliesAfterPubAck(t *testing.T) {
	_, js, stream, cfg := natsJetStream(t)
	edge := probe.NewEdge(js, cfg)
	ctx := context.Background()

	r := asReply(t, edge.ProbeState(ctx, testOrg, testCollector, encode(t, stateChange(41))))
	if r.Status != http.StatusAccepted || r.Seq == nil || *r.Seq != 41 || r.Duplicate {
		t.Fatalf("state: %+v, want 202 seq 41", r)
	}
	msg, err := stream.GetLastMsgForSubject(ctx, cfg.StateSubject(testOrg, testCollector))
	if err != nil {
		t.Fatalf("state not in the stream when acknowledged: %v", err)
	}
	if id := msg.Header.Get(jetstream.MsgIDHeader); id != testCollector+":state:3:41-41" {
		t.Errorf("state Nats-Msg-Id %q", id)
	}
	var stored probewire.StateChange
	if err = probewire.Decode(msg.Data, &stored); err != nil || stored.Seq != 41 || stored.To != probewire.StateDown {
		t.Errorf("stored state: %+v, %v", stored, err)
	}

	batch := resultBatch(testCollector, 100, 5)
	batch.Results[2].Epoch = 4
	r = asReply(t, edge.ProbeResults(ctx, testOrg, testCollector, encode(t, batch)))
	if r.Status != http.StatusAccepted || r.Seq == nil || *r.Seq != 104 {
		t.Fatalf("results: %+v, want 202 seq 104", r)
	}
	msg, err = stream.GetLastMsgForSubject(ctx, cfg.ResultsSubject(testOrg, testCollector))
	if err != nil {
		t.Fatalf("results not in the stream when acknowledged: %v", err)
	}
	if id := msg.Header.Get(jetstream.MsgIDHeader); id != testCollector+":results:4:100-104" {
		t.Errorf("results Nats-Msg-Id %q", id)
	}
	if n := streamMsgs(t, stream); n != 2 {
		t.Errorf("stream holds %d messages, want 2", n)
	}

	info, err := stream.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := info.Config
	if c.Retention != jetstream.WorkQueuePolicy || c.Storage != jetstream.FileStorage || c.Duplicates != 2*time.Minute ||
		c.MaxAge != 24*time.Hour || c.MaxBytes != cfg.MaxBytes {
		t.Errorf("stream configuration %+v", c)
	}

	// Without the stream there is no PubAck: 503, the collector keeps the
	// message in its spool and sends it again.
	gone := probe.NewEdge(js, probe.StreamConfig{Name: cfg.Name + "_MISSING", SubjectPrefix: "probemissing" + randomHex(t)})
	if r = asReply(t, gone.ProbeState(ctx, testOrg, testCollector, encode(t, stateChange(42)))); r.Status != http.StatusServiceUnavailable || r.Seq != nil {
		t.Errorf("state without stream: %+v, want 503 without seq", r)
	}
	if strings.Contains(r.Error, "nats") || strings.Contains(r.Error, "stream not found") {
		t.Errorf("internal error text reached the collector: %q", r.Error)
	}
}

// TestProbeEdgeDuplicateIsDeduplicated: a repeated transmission of the same
// state change or batch (same deduplication ID) is acknowledged again with
// its seq but leaves one message in the stream (PRB-07).
func TestProbeEdgeDuplicateIsDeduplicated(t *testing.T) {
	_, js, stream, cfg := natsJetStream(t)
	edge := probe.NewEdge(js, cfg)
	ctx := context.Background()
	state := encode(t, stateChange(7))
	batch := encode(t, resultBatch(testCollector, 8, 10))
	for i := range 2 {
		r := asReply(t, edge.ProbeState(ctx, testOrg, testCollector, state))
		if r.Status != http.StatusAccepted || r.Seq == nil || *r.Seq != 7 || r.Duplicate != (i == 1) {
			t.Errorf("state transmission %d: %+v", i+1, r)
		}
		r = asReply(t, edge.ProbeResults(ctx, testOrg, testCollector, batch))
		if r.Status != http.StatusAccepted || r.Seq == nil || *r.Seq != 17 || r.Duplicate != (i == 1) {
			t.Errorf("results transmission %d: %+v", i+1, r)
		}
	}
	if n := streamMsgs(t, stream); n != 2 {
		t.Errorf("stream holds %d messages after a double transmission, want 2", n)
	}
}

// TestProbeEdgeStateAndResultsWithSameSeqAreKeptApart: JetStream
// deduplicates over the whole stream, and the collector counts state and
// results separately (WP-952 AK4); a state change and a one-result batch
// with the same epoch and seq are two messages, neither is a duplicate
// (PRB-07: nothing acknowledged is lost).
func TestProbeEdgeStateAndResultsWithSameSeqAreKeptApart(t *testing.T) {
	_, js, stream, cfg := natsJetStream(t)
	edge := probe.NewEdge(js, cfg)
	ctx := context.Background()
	if r := asReply(t, edge.ProbeState(ctx, testOrg, testCollector, encode(t, stateChange(5)))); r.Status != http.StatusAccepted || r.Duplicate {
		t.Errorf("state seq 5: %+v", r)
	}
	if r := asReply(t, edge.ProbeResults(ctx, testOrg, testCollector, encode(t, resultBatch(testCollector, 5, 1)))); r.Status != http.StatusAccepted || r.Duplicate {
		t.Errorf("results seq 5: %+v, want 202 without duplicate", r)
	}
	if n := streamMsgs(t, stream); n != 2 {
		t.Errorf("stream holds %d messages, want 2", n)
	}
}

// edgeServer runs the discovery edge dispatcher with the probe edge on nc.
func edgeServer(t testing.TB, nc *nats.Conn, repo *discovery.MemoryRepository, edge *probe.Edge) {
	t.Helper()
	h := discovery.NewHandler(repo, ci.NewMemoryRepository()).WithEdgeProbes(edge)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.ServeCollectorEdge(ctx, nc) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
}

// request sends a request to the edge and waits until the subscriptions of
// the dispatcher are active.
func request(t testing.TB, nc *nats.Conn, subject string, data []byte) reply {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		msg, err := nc.Request(subject, data, 10*time.Second)
		if errors.Is(err, nats.ErrNoResponders) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		if err != nil {
			t.Fatalf("request %s: %v", subject, err)
		}
		var r reply
		if err = json.Unmarshal(msg.Data, &r); err != nil {
			t.Fatalf("reply %s: %v", msg.Data, err)
		}
		return r
	}
}

func subject(orgID, collectorID, suffix string) string {
	return "collector." + orgID + "." + collectorID + suffix
}

// TestProbeEdgeRefusesUnknownOrDisabledCollector: the dispatcher refuses
// the probe messages of an unknown (404) or disabled (403) collector before
// the probe edge sees them, so nothing reaches the stream (AK1, COL-04);
// an active collector is served.
func TestProbeEdgeRefusesUnknownOrDisabledCollector(t *testing.T) {
	nc, js, stream, cfg := natsJetStream(t)
	repo := discovery.NewMemoryRepository()
	orgID := randomUUID(t)
	ctx := context.Background()
	active := &discovery.Collector{OrganizationID: orgID, Name: "active", Status: discovery.CollectorActive}
	disabled := &discovery.Collector{OrganizationID: orgID, Name: "disabled", Status: discovery.CollectorActive}
	for _, c := range []*discovery.Collector{active, disabled} {
		if err := repo.RegisterCollector(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	off := discovery.CollectorDisabled
	if _, err := repo.UpdateCollector(ctx, orgID, disabled.ID, &discovery.CollectorPatch{Status: &off}); err != nil {
		t.Fatal(err)
	}
	alives := 0
	var mu sync.Mutex
	edge := probe.NewEdge(js, cfg).OnAlive(func(string, string, probewire.Alive, time.Time) {
		mu.Lock()
		alives++
		mu.Unlock()
	})
	edgeServer(t, nc, repo, edge)

	unknown := randomUUID(t)
	for _, tc := range []struct {
		suffix string
		data   []byte
	}{
		{discovery.ProbeStateSuffix, encode(t, stateChange(1))},
		{discovery.ProbeResultsSuffix, encode(t, resultBatch("", 2, 3))},
		{discovery.ProbeAliveSuffix, encode(t, alive())},
	} {
		if r := request(t, nc, subject(orgID, unknown, tc.suffix), tc.data); r.Status != http.StatusNotFound {
			t.Errorf("%s of an unknown collector: %+v, want 404", tc.suffix, r)
		}
		if r := request(t, nc, subject(orgID, disabled.ID, tc.suffix), tc.data); r.Status != http.StatusForbidden {
			t.Errorf("%s of a disabled collector: %+v, want 403", tc.suffix, r)
		}
		// Another organization's collector under this organization.
		if r := request(t, nc, subject(randomUUID(t), active.ID, tc.suffix), tc.data); r.Status != http.StatusNotFound {
			t.Errorf("%s under another organization: %+v, want 404", tc.suffix, r)
		}
	}
	if n := streamMsgs(t, stream); n != 0 {
		t.Fatalf("refused collectors stored %d messages", n)
	}
	if alives != 0 {
		t.Fatalf("alive hook ran %d times for refused collectors", alives)
	}
	if r := request(t, nc, subject(orgID, active.ID, discovery.ProbeStateSuffix), encode(t, stateChange(1))); r.Status != http.StatusAccepted {
		t.Errorf("state of the active collector: %+v, want 202", r)
	}
	if r := request(t, nc, subject(orgID, active.ID, discovery.ProbeAliveSuffix), encode(t, alive())); r.Status != http.StatusOK || r.ServerTime == nil {
		t.Errorf("alive of the active collector: %+v, want 200", r)
	}
	if n := streamMsgs(t, stream); n != 1 {
		t.Errorf("stream holds %d messages, want 1", n)
	}
}

// TestProbeEdgeParallelHandlers: the probe subjects run on a pool of at
// least DefaultEdgeWorkers handlers per instance (AK6): as many alive
// messages as there are handlers are in their hook at the same time; with
// the serial callbacks of a plain subscription the barrier would never
// open.
func TestProbeEdgeParallelHandlers(t *testing.T) {
	nc, js, _, cfg := natsJetStream(t)
	repo := discovery.NewMemoryRepository()
	orgID := randomUUID(t)
	col := &discovery.Collector{OrganizationID: orgID, Name: "parallel", Status: discovery.CollectorActive}
	if err := repo.RegisterCollector(context.Background(), col); err != nil {
		t.Fatal(err)
	}
	workers := probe.DefaultEdgeWorkers
	var inside, peak atomic.Int32
	var armed atomic.Bool
	barrier := make(chan struct{})
	var once sync.Once
	edge := probe.NewEdge(js, cfg).WithWorkers(1).OnAlive(func(string, string, probewire.Alive, time.Time) {
		if !armed.Load() {
			return
		}
		n := inside.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		if int(n) == workers {
			once.Do(func() { close(barrier) })
		}
		select {
		case <-barrier:
		case <-time.After(5 * time.Second):
		}
		inside.Add(-1)
	})
	if edge.Workers() != workers {
		t.Fatalf("workers %d, want at least %d", edge.Workers(), workers)
	}
	edgeServer(t, nc, repo, edge)
	request(t, nc, subject(orgID, col.ID, discovery.ProbeAliveSuffix), encode(t, alive())) // subscriptions active
	armed.Store(true)

	start := time.Now()
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			if r := request(t, nc, subject(orgID, col.ID, discovery.ProbeAliveSuffix), encode(t, alive())); r.Status != http.StatusOK {
				t.Errorf("alive: %+v", r)
			}
		})
	}
	wg.Wait()
	if p := peak.Load(); int(p) < workers {
		t.Errorf("at most %d handlers ran at the same time, want %d", p, workers)
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Errorf("parallel requests took %s: the handlers ran one after another", d)
	}
}

// TestProbeEdgeBurstBeyondPoolIsAnswered: a burst of requests far beyond
// the handler pool, as at a resync of many collectors (NFR-11), waits in
// the buffer of the pool while every handler is busy; nats.go would drop
// what a full buffer cannot take (slow consumer), so every request must
// be answered.
func TestProbeEdgeBurstBeyondPoolIsAnswered(t *testing.T) {
	const burst = 2000
	nc, js, _, cfg := natsJetStream(t)
	repo := discovery.NewMemoryRepository()
	orgID := randomUUID(t)
	col := &discovery.Collector{OrganizationID: orgID, Name: "burst", Status: discovery.CollectorActive}
	if err := repo.RegisterCollector(context.Background(), col); err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	var armed atomic.Bool
	edge := probe.NewEdge(js, cfg).OnAlive(func(string, string, probewire.Alive, time.Time) {
		if armed.Load() {
			<-gate
		}
	})
	edgeServer(t, nc, repo, edge)
	subj := subject(orgID, col.ID, discovery.ProbeAliveSuffix)
	request(t, nc, subj, encode(t, alive())) // subscriptions active

	client, err := nats.Connect(nc.ConnectedUrl(), nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	inbox := nats.NewInbox()
	replies, err := client.SubscribeSync(inbox + ".*")
	if err != nil {
		t.Fatal(err)
	}
	if err = replies.SetPendingLimits(-1, -1); err != nil {
		t.Fatal(err)
	}
	armed.Store(true)
	data := encode(t, alive())
	for i := range burst {
		if err = client.PublishRequest(subj, inbox+"."+strconv.Itoa(i), data); err != nil {
			t.Fatal(err)
		}
	}
	if err = client.Flush(); err != nil {
		t.Fatal(err)
	}
	// Every handler blocks until the burst reached the edge instance.
	time.Sleep(500 * time.Millisecond)
	close(gate)
	for i := range burst {
		msg, err := replies.NextMsg(10 * time.Second)
		if err != nil {
			t.Fatalf("%d of %d requests answered: %v", i, burst, err)
		}
		var r reply
		if err = json.Unmarshal(msg.Data, &r); err != nil || r.Status != http.StatusOK {
			t.Fatalf("reply %s: %v", msg.Data, err)
		}
	}
}

// TestProbeEdgeResultsThroughput checks AK6 in every test run, the
// benchmark below only with -bench: a fixed number of result batches of
// 177 results, sent one after another through the dispatcher into the
// stream, must arrive at a rate of at least 2,000 batches per minute.
func TestProbeEdgeResultsThroughput(t *testing.T) {
	const batches = 100
	nc, js, _, cfg := natsJetStream(t)
	repo := discovery.NewMemoryRepository()
	orgID := randomUUID(t)
	col := &discovery.Collector{OrganizationID: orgID, Name: "throughput", Status: discovery.CollectorActive}
	if err := repo.RegisterCollector(context.Background(), col); err != nil {
		t.Fatal(err)
	}
	edgeServer(t, nc, repo, probe.NewEdge(js, cfg))
	subj := subject(orgID, col.ID, discovery.ProbeResultsSuffix)
	request(t, nc, subj, encode(t, resultBatch(col.ID, 1, 1))) // subscriptions active
	start := time.Now()
	for i := range batches {
		if r := request(t, nc, subj, encode(t, resultBatch(col.ID, uint64(1000+i*177), 177))); r.Status != http.StatusAccepted {
			t.Fatalf("batch %d: %+v", i, r)
		}
	}
	elapsed := time.Since(start)
	if perMin := float64(batches) / elapsed.Minutes(); perMin < 2000 {
		t.Errorf("%.0f result batches/min, want at least 2,000 (AK6)", perMin)
	}
}

// BenchmarkProbeEdgeResults sends result batches of 177 results (the mean
// batch of E-173) through the dispatcher into the stream and reports the
// batches per minute; AK6 requires at least 2,000 per instance.
func BenchmarkProbeEdgeResults(b *testing.B) {
	nc, js, stream, cfg := natsJetStream(b)
	repo := discovery.NewMemoryRepository()
	orgID := randomUUID(b)
	col := &discovery.Collector{OrganizationID: orgID, Name: "bench", Status: discovery.CollectorActive}
	if err := repo.RegisterCollector(context.Background(), col); err != nil {
		b.Fatal(err)
	}
	// A consumer drains the work queue as probe-ingest does (WP-960), so
	// the stream stays below its byte limit however long the run.
	consumer, err := stream.CreateOrUpdateConsumer(context.Background(), jetstream.ConsumerConfig{
		Durable: "bench", AckPolicy: jetstream.AckExplicitPolicy,
	})
	if err != nil {
		b.Fatal(err)
	}
	consuming, err := consumer.Consume(func(m jetstream.Msg) { _ = m.Ack() })
	if err != nil {
		b.Fatal(err)
	}
	defer consuming.Stop()
	edgeServer(b, nc, repo, probe.NewEdge(js, cfg))
	subj := subject(orgID, col.ID, discovery.ProbeResultsSuffix)
	request(b, nc, subj, encode(b, resultBatch(col.ID, 1, 1)))
	var seq atomic.Uint64
	seq.Store(1 << 20)
	b.ResetTimer()
	start := time.Now()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			from := seq.Add(177) - 177
			data, err := probewire.Encode(resultBatch(col.ID, from, 177))
			if err != nil {
				b.Error(err)
				return
			}
			msg, err := nc.Request(subj, data, 10*time.Second)
			if err != nil {
				b.Error(err)
				return
			}
			var r reply
			if err = json.Unmarshal(msg.Data, &r); err != nil || r.Status != http.StatusAccepted {
				b.Errorf("reply %s", msg.Data)
				return
			}
		}
	})
	elapsed := time.Since(start)
	perMin := float64(b.N) / elapsed.Minutes()
	b.ReportMetric(perMin, "batches/min")
	if b.N >= 100 && perMin < 2000 {
		b.Errorf("%.0f result batches/min, want at least 2,000 (AK6)", perMin)
	}
}
