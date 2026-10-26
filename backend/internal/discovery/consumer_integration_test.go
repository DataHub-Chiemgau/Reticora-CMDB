package discovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestIngestRecordLedger covers WP-085 (REC-01) against PostgreSQL: the
// ingest_record ledger keeps processed record_ids per organization under
// RLS, a repeated record_id is not processed again within 48 hours, an older
// entry no longer counts and is removed; records of a batch are processed in
// observed_at order. With TEST_NATS_URL the same records also run through the
// ingest.> stream and its consumer.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestIngestRecordLedger(t *testing.T) {
	f := scopetest.Seed(t, "77")
	bg := context.Background()
	repo := discovery.NewPGRepository(f.App)
	cis := ci.NewPGRepository(f.App)
	handler := discovery.NewHandler(repo, cis)
	mux := chi.NewRouter()
	handler.RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})

	record := func(serial, model string, observedAt time.Time) plugins.DeviceRecord {
		return plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: "ledger-" + serial, IP: "192.0.2.77",
			Serial: serial, Model: model}, observedAt)
	}
	post := func(records ...plugins.DeviceRecord) discovery.BulkIngestResponse {
		t.Helper()
		body, err := json.Marshal(plugins.DeviceRecordBatch{Records: records})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/ingest", bytes.NewReader(body)).WithContext(ctx)
		req.Header.Set("X-Collector-ID", "col-77")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusAccepted {
			t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
		}
		var resp discovery.BulkIngestResponse
		if err = json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}
	model := func(serial string) string {
		t.Helper()
		var m string
		if err := f.Admin.QueryRow(bg, `SELECT model FROM ci WHERE organization_id = $1 AND serial_number = $2`, f.OrgA, serial).Scan(&m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	t0 := time.Now().UTC().Add(-time.Hour)
	first := record("LEDGER-1", "m1", t0)
	if resp := post(first); resp.Created != 1 {
		t.Fatalf("first: %+v", resp)
	}
	var collector string
	if err := f.Admin.QueryRow(bg, `SELECT collector_id FROM ingest_record WHERE organization_id = $1 AND record_id = $2`,
		f.OrgA, first.RecordID).Scan(&collector); err != nil || collector != "col-77" {
		t.Fatalf("ledger row: %q, %v", collector, err)
	}
	if resp := post(first); resp.Duplicates != 1 || resp.Updated != 0 {
		t.Errorf("repeated record_id: %+v", resp)
	}

	// The ledger is per organization and under RLS.
	var visible int
	scopeB := database.OrgWideScope(f.OrgB, f.User)
	if err := database.WithTenant(bg, f.App, &scopeB, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM ingest_record WHERE record_id = $1`, first.RecordID).Scan(&visible)
	}); err != nil || visible != 0 {
		t.Errorf("ledger row visible to another organization: %d, %v", visible, err)
	}

	// After 48 hours the record_id counts again, and the old entry is removed.
	if _, err := f.Admin.Exec(bg, `UPDATE ingest_record SET processed_at = now() - interval '49 hours' WHERE record_id = $1`, first.RecordID); err != nil {
		t.Fatal(err)
	}
	stale := record("LEDGER-2", "x", t0)
	post(stale)
	var left int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ingest_record WHERE record_id = $1 AND processed_at < now() - interval '48 hours'`,
		first.RecordID).Scan(&left); err != nil || left != 0 {
		t.Errorf("expired ledger rows left: %d, %v", left, err)
	}
	if resp := post(first); resp.Duplicates != 0 {
		t.Errorf("record_id after 48 hours: %+v", resp)
	}

	// Order by observed_at within a batch.
	if resp := post(record("LEDGER-1", "m3", t0.Add(2*time.Minute)), record("LEDGER-1", "m2", t0.Add(time.Minute))); resp.Updated != 2 {
		t.Errorf("batch: %+v", resp)
	}
	if m := model("LEDGER-1"); m != "m3" {
		t.Errorf("model after the batch: %s, want m3", m)
	}

	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Log("TEST_NATS_URL not set; skipping the ingest.> stream")
		return
	}
	queueCtx, cancel := context.WithTimeout(bg, 10*time.Second)
	queue, err := discovery.NewNATSIngestQueue(queueCtx, url, 4)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	queued := discovery.NewHandler(repo, cis)
	queue.Attach(queued)
	qmux := chi.NewRouter()
	queued.RegisterRoutes(qmux)
	mux = qmux
	runCtx, stop := context.WithCancel(bg)
	defer stop()
	go queue.Run(runCtx)

	streamed := record("LEDGER-3", "s1", t0)
	if resp := post(streamed, streamed, record("LEDGER-3", "s2", t0.Add(time.Minute))); resp.Queued != 3 || resp.Created != 0 {
		t.Fatalf("queued ingest: %+v", resp)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		var n int
		var m string
		_ = f.Admin.QueryRow(bg, `SELECT count(*), COALESCE(max(model), '') FROM ci WHERE organization_id = $1 AND serial_number = 'LEDGER-3'`,
			f.OrgA).Scan(&n, &m)
		if n == 1 && m == "s2" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stream not consumed: %d CIs, model %q", n, m)
		}
		time.Sleep(100 * time.Millisecond)
	}
	var processed int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM ingest_record WHERE organization_id = $1 AND record_id = $2`,
		f.OrgA, streamed.RecordID).Scan(&processed); err != nil || processed != 1 {
		t.Errorf("streamed record in the ledger: %d, %v", processed, err)
	}
}

// TestIngestLanes covers WP-334 (NFR-03, E-56, OPS-05) on NATS JetStream and
// PostgreSQL:
//   - upgrade: the unfiltered consumer of the release before the lanes, with
//     three of its messages pending, is restricted to ingest.*.* in place and
//     its messages are processed;
//   - a consumer per lane (filter ingest.*.<lane>.*, MaxAckPending 100); the
//     records are published on the subject of their lane, over HTTP and over
//     the collector edge;
//   - backlog per lane in LaneBacklog and reticora_ingest_lane_backlog while
//     nothing consumes, none after the consumers ran;
//   - order per device: the records of one serial number are processed in
//     observed_at order across requests and within one;
//   - fewer lanes: a server with one lane drains lane 3; a drain lane the
//     operator removes ends without a worker error; a server of an earlier
//     release that resets the former consumer's filter is corrected;
//   - Run stops at once with 16 lanes.
//
// It needs TEST_NATS_URL (a stream of its own: the test deletes
// RETICORA_INGEST) and TEST_DATABASE_URL.
func TestIngestLanes(t *testing.T) {
	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Skip("TEST_NATS_URL not set; skipping the ingest lanes on NATS")
	}
	f := scopetest.Seed(t, "f2")
	bg := context.Background()
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	// The stream as the release before the lanes left it.
	if err = js.DeleteStream(bg, discovery.IngestStream); err != nil && !errors.Is(err, jetstream.ErrStreamNotFound) {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = js.DeleteStream(context.Background(), discovery.IngestStream) })
	stream, err := js.CreateOrUpdateStream(bg, jetstream.StreamConfig{Name: discovery.IngestStream, Subjects: []string{"ingest.>"},
		Retention: jetstream.WorkQueuePolicy, Storage: jetstream.FileStorage, MaxAge: discovery.RecordDedupeWindow})
	if err != nil {
		t.Fatal(err)
	}
	oldConsumer := jetstream.ConsumerConfig{Durable: discovery.IngestConsumer, AckPolicy: jetstream.AckExplicitPolicy,
		AckWait: 2 * time.Minute, MaxDeliver: 5, MaxAckPending: 100}
	if _, err = stream.CreateOrUpdateConsumer(bg, oldConsumer); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().UTC().Add(-time.Hour)
	message := func(serial string, at time.Time) []byte {
		raw, merr := json.Marshal(&discovery.IngestMessage{OrganizationID: f.OrgA, CollectorID: "col-77",
			Scope:    &discovery.MessageScope{UserID: f.User},
			Item:     discovery.IngestItem{CITypeName: "server", Name: serial, SerialNumber: serial, Source: "snmp"},
			RecordID: plugins.NewRecordID(), ObservedAt: at})
		if merr != nil {
			t.Fatal(merr)
		}
		return raw
	}
	for i := range 3 {
		if _, err = js.Publish(bg, "ingest."+f.OrgA+".col-77", message(fmt.Sprintf("LANES-OLD-%d", i), t0)); err != nil {
			t.Fatal(err)
		}
	}

	open := func(lanes int) *discovery.NATSIngestQueue {
		t.Helper()
		qctx, cancel := context.WithTimeout(bg, 10*time.Second)
		defer cancel()
		q, qerr := discovery.NewNATSIngestQueue(qctx, url, lanes)
		if qerr != nil {
			t.Fatal(qerr)
		}
		t.Cleanup(q.Close)
		return q
	}
	consumerConfig := func(name string) jetstream.ConsumerConfig {
		t.Helper()
		c, cerr := stream.Consumer(bg, name)
		if cerr != nil {
			t.Fatalf("consumer %s: %v", name, cerr)
		}
		return c.CachedInfo().Config
	}
	queue4 := open(4)
	if cfg := consumerConfig(discovery.IngestConsumer); cfg.FilterSubject != "ingest.*.*" {
		t.Errorf("former consumer filter %q, want ingest.*.*", cfg.FilterSubject)
	}
	for lane := range 4 {
		name := fmt.Sprintf("%s%d", discovery.IngestLaneConsumerPrefix, lane)
		if cfg := consumerConfig(name); cfg.FilterSubject != fmt.Sprintf("ingest.*.%d.*", lane) || cfg.MaxAckPending != 100 {
			t.Errorf("%s: filter %q, MaxAckPending %d", name, cfg.FilterSubject, cfg.MaxAckPending)
		}
	}

	h4 := discovery.NewHandler(discovery.NewPGRepository(f.App), ci.NewPGRepository(f.App))
	queue4.Attach(h4)
	mux := chi.NewRouter()
	h4.RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	post := func(records ...plugins.DeviceRecord) {
		t.Helper()
		body, merr := json.Marshal(plugins.DeviceRecordBatch{Records: records})
		if merr != nil {
			t.Fatal(merr)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/ingest", bytes.NewReader(body)).WithContext(ctx)
		req.Header.Set("X-Collector-ID", "col-f2")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusAccepted {
			t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
		}
	}
	ips := map[string]string{}
	device := func(serial, model string, at time.Time) plugins.DeviceRecord {
		if ips[serial] == "" {
			ips[serial] = fmt.Sprintf("10.77.0.%d", len(ips)+1)
		}
		return plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: serial, Serial: serial, Model: model,
			IP: ips[serial]}, at)
	}
	want := map[string]uint64{"ingest." + f.OrgA + ".col-77": 3}
	perLane := map[string]int{"legacy": 3}
	var records []plugins.DeviceRecord
	for i := range 40 {
		serial := fmt.Sprintf("LANES-%02d", i)
		records = append(records, device(serial, "m", t0))
		lane := discovery.IngestLane(&discovery.IngestItem{SerialNumber: serial}, 4)
		want[discovery.LaneSubject(f.OrgA, lane, "col-f2")]++
		perLane[strconv.Itoa(lane)]++
	}
	post(records...)
	// The edge takes the records of a known collector only (COL-04).
	edgeCol := &discovery.Collector{OrganizationID: f.OrgA, Name: "lanes-edge", Status: discovery.CollectorActive, Config: map[string]any{}}
	if err = discovery.NewPGRepository(f.App).RegisterCollector(ctx, edgeCol); err != nil {
		t.Fatal(err)
	}
	edge, _ := json.Marshal(plugins.DeviceRecordBatch{Records: []plugins.DeviceRecord{device("LANES-EDGE", "m", t0)}})
	if reply := h4.EdgeIngest(ctx, f.OrgA, edgeCol.ID, edge); reply.Status != http.StatusAccepted || reply.Result.Queued != 1 {
		t.Fatalf("edge ingest: %+v", reply)
	}
	edgeLane := discovery.IngestLane(&discovery.IngestItem{SerialNumber: "LANES-EDGE"}, 4)
	want[discovery.LaneSubject(f.OrgA, edgeLane, edgeCol.ID)]++
	perLane[strconv.Itoa(edgeLane)]++

	// Before anything consumes: the subjects and the backlog per lane.
	info, err := stream.Info(bg, jetstream.WithSubjectFilter("ingest."+f.OrgA+".>"))
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(info.State.Subjects, want) {
		t.Errorf("messages per subject %v, want %v", info.State.Subjects, want)
	}
	backlog, err := queue4.LaneBacklog(bg)
	if err != nil {
		t.Fatal(err)
	}
	if backlog.Lanes != 4 || backlog.Total != 44 || !maps.Equal(backlog.Backlog, map[string]int{"legacy": 3, "0": perLane["0"],
		"1": perLane["1"], "2": perLane["2"], "3": perLane["3"]}) {
		t.Errorf("backlog %+v, want 44 as %v", backlog, perLane)
	}
	reg := prometheus.NewRegistry()
	discovery.RegisterMetrics(reg)
	if detail, derr := queue4.ReadinessDetail(bg); !errors.Is(derr, discovery.ErrNoBacklogSample) {
		t.Errorf("/readyz detail before the first sample: %+v, %v; want ErrNoBacklogSample", detail, derr)
	}
	if total, berr := queue4.Backlog(bg); berr != nil || total != 44 {
		t.Errorf("Backlog = %d, %v", total, berr)
	}
	if got := gaugeValue(t, reg, "reticora_ingest_lane_backlog", "legacy"); got != 3 {
		t.Errorf("reticora_ingest_lane_backlog{lane=legacy} = %v, want 3", got)
	}
	readinessFromSample(t, url, queue4, backlog)

	countLike := func(pattern string) int {
		t.Helper()
		var n int
		if cerr := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci WHERE organization_id = $1 AND serial_number LIKE $2`, f.OrgA,
			pattern).Scan(&n); cerr != nil {
			t.Fatal(cerr)
		}
		return n
	}
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	run := func(q *discovery.NATSIngestQueue) func() {
		runCtx, stop := context.WithCancel(bg)
		done := make(chan struct{})
		go func() {
			q.Run(runCtx)
			close(done)
		}()
		return func() {
			t.Helper()
			stop()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("Run did not stop")
			}
		}
	}
	stop4 := run(queue4)
	waitFor("44 CIs", func() bool { return countLike("LANES-%") == 44 })
	waitFor("an empty backlog", func() bool {
		b, berr := queue4.LaneBacklog(bg)
		return berr == nil && b.Total == 0
	})

	// Order per device, across requests and within one.
	post(device("LANES-ORDER", "m1", t0))
	waitFor("LANES-ORDER", func() bool { return countLike("LANES-ORDER") == 1 })
	post(device("LANES-ORDER", "m3", t0.Add(2*time.Minute)), device("LANES-ORDER", "m2", t0.Add(time.Minute)))
	var models []string
	waitFor("model m3", func() bool {
		models = nil
		rows, qerr := f.Admin.Query(bg, `SELECT ch.new_value #>> '{}' FROM ci_change ch JOIN ci c ON c.id = ch.ci_id
			WHERE c.organization_id = $1 AND c.serial_number = 'LANES-ORDER' AND ch.field_name = 'model' ORDER BY ch.created_at`, f.OrgA)
		if qerr != nil {
			t.Fatal(qerr)
		}
		defer rows.Close()
		for rows.Next() {
			var m string
			if serr := rows.Scan(&m); serr != nil {
				t.Fatal(serr)
			}
			models = append(models, m)
		}
		return len(models) >= 2
	})
	if !slices.Equal(models, []string{"m2", "m3"}) {
		t.Errorf("model history %v, want [m2 m3]", models)
	}
	stop4()

	// One lane: lane 3 is drained.
	queue1 := open(1)
	h1 := discovery.NewHandler(discovery.NewPGRepository(f.App), ci.NewPGRepository(f.App))
	queue1.Attach(h1)
	if _, err = js.Publish(bg, "ingest."+f.OrgA+".3.col-77", message("LANES-DRAIN", t0)); err != nil {
		t.Fatal(err)
	}
	lane3 := discovery.IngestLaneConsumerPrefix + "3"
	if consumers := discovery.QueueConsumers(queue1); !slices.Equal(consumers, []string{discovery.IngestConsumer,
		discovery.IngestLaneConsumerPrefix + "0", discovery.IngestLaneConsumerPrefix + "1", discovery.IngestLaneConsumerPrefix + "2", lane3}) {
		t.Errorf("consumers with one lane: %v", consumers)
	}
	errorsBefore := testutil.ToFloat64(observability.WorkerErrors.WithLabelValues("ingest"))
	stop1 := run(queue1)
	waitFor("the drained record", func() bool { return countLike("LANES-DRAIN") == 1 })
	if err = stream.DeleteConsumer(bg, lane3); err != nil {
		t.Fatal(err)
	}
	waitFor("the removed drain lane to end", func() bool { return !slices.Contains(discovery.QueueConsumers(queue1), lane3) })
	if after := testutil.ToFloat64(observability.WorkerErrors.WithLabelValues("ingest")); after != errorsBefore {
		t.Errorf("worker errors %v -> %v after removing a drain lane", errorsBefore, after)
	}

	// A server of an earlier release asserts its unfiltered consumer again.
	// NATS 2.10 accepts that as an update; the next check restores the
	// filter. A server that refuses it leaves nothing to restore.
	if _, err = stream.CreateOrUpdateConsumer(bg, oldConsumer); err != nil {
		t.Logf("NATS refuses the unfiltered consumer next to the lanes: %v", err)
	} else {
		discovery.RefreshQueue(bg, queue1)
		if cfg := consumerConfig(discovery.IngestConsumer); cfg.FilterSubject != "ingest.*.*" {
			t.Errorf("former consumer filter after the check %q, want ingest.*.*", cfg.FilterSubject)
		}
	}
	stop1()

	// Sixteen lanes and the former consumer stop together.
	queue16 := open(16)
	queue16.Attach(discovery.NewHandler(discovery.NewPGRepository(f.App), ci.NewPGRepository(f.App)))
	stop16 := run(queue16)
	time.Sleep(1500 * time.Millisecond)
	stop16()
}

// gaugeValue returns the value of a gauge with the label lane.
func gaugeValue(t *testing.T, reg *prometheus.Registry, name, lane string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range families {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "lane" && l.GetValue() == lane {
					return m.GetGauge().GetValue()
				}
			}
		}
	}
	return -1
}

// readinessFromSample: the /readyz detail of the queue is the backlog its
// sampler read last (holistic review of WP-334); no request asks NATS. A
// second connection watches the JetStream API requests of the consumers.
func readinessFromSample(t *testing.T, url string, q *discovery.NATSIngestQueue, want *discovery.IngestBacklog) {
	t.Helper()
	bg := context.Background()
	watch, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Close()
	sub, err := watch.SubscribeSync("$JS.API.CONSUMER.>")
	if err != nil {
		t.Fatal(err)
	}
	if err = watch.Flush(); err != nil {
		t.Fatal(err)
	}
	requests := func() int {
		t.Helper()
		time.Sleep(200 * time.Millisecond)
		if ferr := watch.Flush(); ferr != nil {
			t.Fatal(ferr)
		}
		n, _, perr := sub.Pending()
		if perr != nil {
			t.Fatal(perr)
		}
		return n
	}
	for range 20 {
		detail, derr := q.ReadinessDetail(bg)
		b, ok := detail.(*discovery.IngestBacklog)
		if derr != nil || !ok || b.Total != want.Total || !maps.Equal(b.Backlog, want.Backlog) || b.SampledAt.IsZero() ||
			time.Since(b.SampledAt) > time.Minute {
			t.Fatalf("/readyz detail %+v (%v), want the sample of %d with its time", detail, derr, want.Total)
		}
	}
	if n := requests(); n != 0 {
		t.Errorf("20 /readyz details sent %d JetStream consumer requests, want none", n)
	}
	// The watch sees the requests of a listing.
	if _, err = q.LaneBacklog(bg); err != nil {
		t.Fatal(err)
	}
	if n := requests(); n == 0 {
		t.Error("the watch saw no JetStream request of a consumer listing")
	}
}

// TestPoisonRecordDoesNotTakeValidRecordsDown covers WP-481 (REC-01,
// NFR-03, F-090; moved from the audit's defect test
// TestAuditT3PoisonRecordDoesNotTakeValidRecordsDown) against PostgreSQL
// with RLS and four lanes: a record of client 2 sent by a principal
// restricted to client 1 can never succeed. On the queue path it is
// terminated at its first delivery while the valid records of the same lane
// and group are processed and acknowledged; at the last delivery none of
// them is given up. The HTTP ingest without a queue processes the valid
// records and answers 202 with the accepted and rejected records and the
// error of the poison record by its record_id; a request with only the
// poison record keeps 403.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestPoisonRecordDoesNotTakeValidRecordsDown(t *testing.T) {
	f := scopetest.Seed(t, "6f")
	bg := context.Background()
	h := discovery.NewHandler(discovery.NewPGRepository(f.App), ci.NewPGRepository(f.App)).WithIngestLanes(4)
	ti := tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User}
	ciRows := func(serial string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci WHERE organization_id = $1 AND serial_number = $2 AND deleted_at IS NULL`, f.OrgA, serial).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	ipSeq := 10
	record := func(serial, client string, at time.Time) plugins.DeviceRecord {
		ipSeq++
		r := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: serial, Serial: serial, IP: fmt.Sprintf("192.0.2.%d", ipSeq)}, at)
		r.ClientID = client
		return r
	}

	t.Run("queue path", func(t *testing.T) {
		t0 := time.Now().UTC().Add(-time.Hour)
		recs := []plugins.DeviceRecord{record("AUDIT-T3-POISON-Q", f.Client2, t0)}
		for i := 0; i < 8; i++ {
			recs = append(recs, record(fmt.Sprintf("AUDIT-T3-GOOD-Q-%d", i), f.Client1, t0.Add(time.Duration(i+1)*time.Second)))
		}
		lane := func(r *plugins.DeviceRecord) int {
			item := discovery.ItemFromRecord(r)
			return discovery.IngestLane(&item, 4)
		}
		poisonLane, sharing := lane(&recs[0]), 0
		for i := 1; i < len(recs); i++ {
			if lane(&recs[i]) == poisonLane {
				sharing++
			}
		}
		if sharing == 0 {
			t.Fatalf("precondition: no valid record shares lane %d with the poison record", poisonLane)
		}
		deadBefore := discovery.IngestDeadLetters()
		// First delivery: every valid record is processed and acknowledged,
		// only the poison record is terminated.
		for i, result := range discovery.ConsumeRecords(f.ClientCtx(f.Client1), h, f.OrgA, recs...) {
			serial := recs[i].Identity.SerialNumber
			if i == 0 {
				if n := ciRows(serial); n != 0 || result != "term" {
					t.Errorf("out-of-scope poison record at delivery 1: %s with %d CI rows, want term and none", result, n)
				}
				continue
			}
			if result != "ack" || ciRows(serial) != 1 {
				t.Errorf("valid record %s (lane %d, poison lane %d) at delivery 1: %s with %d CI rows, want ack and 1 row",
					serial, lane(&recs[i]), poisonLane, result, ciRows(serial))
			}
		}
		if n := discovery.IngestDeadLetters() - deadBefore; n != 1 {
			t.Errorf("dead letters grew by %d, want 1 (the poison record)", n)
		}
		// Last delivery: no valid record is dead-lettered.
		for i, result := range discovery.ConsumeRecordsDelivered(f.ClientCtx(f.Client1), h, f.OrgA, discovery.IngestMaxDeliver, recs...) {
			if i == 0 {
				continue
			}
			serial := recs[i].Identity.SerialNumber
			if result != "ack" || ciRows(serial) != 1 {
				t.Errorf("valid record %s at the last delivery: %s with %d CI rows, want it acknowledged and persisted (never dead-lettered)",
					serial, result, ciRows(serial))
			}
		}
	})

	t.Run("frame", func(t *testing.T) {
		// A record of an unknown type outside the sender's scope fails the
		// review the frame opens for it: the records run one by one.
		t0 := time.Now().UTC().Add(-30 * time.Minute)
		poison := record("AUDIT-T3-POISON-F", f.Client2, t0)
		poison.Classification.CIType = "audit-t3-unknown-type"
		recs := []plugins.DeviceRecord{poison, record("AUDIT-T3-GOOD-F-0", f.Client1, t0.Add(time.Second)),
			record("AUDIT-T3-GOOD-F-1", f.Client1, t0.Add(2*time.Second))}
		results := discovery.ConsumeRecords(f.ClientCtx(f.Client1), h, f.OrgA, recs...)
		if want := []string{"term", "ack", "ack"}; !slices.Equal(results, want) {
			t.Errorf("deliveries %v, want %v", results, want)
		}
		for _, r := range recs[1:] {
			if n := ciRows(r.Identity.SerialNumber); n != 1 {
				t.Errorf("valid record %s: %d CI rows, want 1", r.Identity.SerialNumber, n)
			}
		}
	})

	t.Run("http path", func(t *testing.T) {
		mux := chi.NewRouter()
		discovery.NewHandler(discovery.NewPGRepository(f.App), ci.NewPGRepository(f.App)).RegisterRoutes(mux)
		post := func(records []plugins.DeviceRecord) *httptest.ResponseRecorder {
			t.Helper()
			body, err := json.Marshal(plugins.DeviceRecordBatch{Records: records})
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/discovery/ingest", bytes.NewReader(body)).
				WithContext(tenant.WithTenant(f.ClientCtx(f.Client1), ti)))
			return w
		}
		at := time.Now().UTC().Add(-time.Minute)
		// Control: the same kind of batch without the poison record is accepted.
		if w := post([]plugins.DeviceRecord{record("AUDIT-T3-CONTROL-H", f.Client1, at)}); w.Code != http.StatusAccepted || ciRows("AUDIT-T3-CONTROL-H") != 1 {
			t.Fatalf("control batch without poison: %d %s", w.Code, w.Body.String())
		}
		records := []plugins.DeviceRecord{record("AUDIT-T3-POISON-H", f.Client2, at)}
		for i := 0; i < 2; i++ {
			records = append(records, record(fmt.Sprintf("AUDIT-T3-GOOD-H-%d", i), f.Client1, at.Add(time.Duration(i+1)*time.Second)))
		}
		w := post(records)
		for _, r := range records[1:] {
			if n := ciRows(r.Identity.SerialNumber); n != 1 {
				t.Errorf("HTTP ingest with one out-of-scope record answered %d; valid record %s has %d CI rows, want 1 (the valid records must not be rejected with it)",
					w.Code, r.Identity.SerialNumber, n)
			}
		}
		if n := ciRows("AUDIT-T3-POISON-H"); n != 0 {
			t.Errorf("out-of-scope poison record created %d CI rows", n)
		}
		var resp discovery.BulkIngestResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); w.Code != http.StatusAccepted || err != nil {
			t.Fatalf("HTTP ingest with one out-of-scope record: %d %s, want 202", w.Code, w.Body.String())
		}
		if resp.Received != 3 || resp.Accepted != 2 || resp.Rejected != 1 || resp.Created != 2 || len(resp.Errors) != 1 ||
			resp.Errors[0].RecordID != records[0].RecordID || resp.Errors[0].Status != http.StatusForbidden || resp.Errors[0].Retryable {
			t.Errorf("answer %s, want 2 accepted and the poison record %s rejected with 403, not retryable", w.Body.String(), records[0].RecordID)
		}
		if strings.Contains(w.Body.String(), "row-level") || strings.Contains(w.Body.String(), "SQLSTATE") {
			t.Errorf("answer discloses internals: %s", w.Body.String())
		}
		// Only the poison record: rejected as a whole, as before.
		if w = post([]plugins.DeviceRecord{record("AUDIT-T3-POISON-H2", f.Client2, at)}); w.Code != http.StatusForbidden {
			t.Errorf("HTTP ingest of only an out-of-scope record: %d %s, want 403", w.Code, w.Body.String())
		}
	})
}
