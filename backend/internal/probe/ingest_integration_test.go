package probe_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// The worker probe_ingest on PostgreSQL with TimescaleDB and the NATS
// server with JetStream of TEST_NATS_URL (NFR-11, PRB-07, CRD-06, WP-960).
// The NATS server is the external one of TEST_NATS_URL, as for every other
// JetStream integration test of the backend and in the CI (ci.yml sets
// it), not an embedded one: the backend does not depend on nats-server.
// Without the variable the tests skip. Each test runs on a stream and
// subject root of its own (natsJetStream), the
// writes as the restricted runtime login (scopetest App pool) through
// probe_result_v.
//
// Retest:
//
//	cd backend && TEST_NATS_URL=nats://localhost:4222 go test -count=1 -p 1 -timeout 30m -run 'TestProbeIngest' ./internal/probe/
//
// TestProbeIngestRejectsForeignCollector covers the client boundary of the
// sending collector.

// ingestEnv is organization A with an active collector and probes of
// client 1 assigned to it in epoch 1, and organization B with one probe.
type ingestEnv struct {
	*schemaEnv
	js         jetstream.JetStream
	cfg        probe.StreamConfig
	collector  string
	collectorB string
	probes     []string
	probeB     string
	ingester   *probe.Ingester
}

func newIngestEnv(t *testing.T, probes int) *ingestEnv {
	t.Helper()
	_, js, _, cfg := natsJetStream(t)
	e := &ingestEnv{schemaEnv: newSchemaEnv(t), js: js, cfg: cfg}
	f := e.f
	clean := func() {
		if _, err := f.Admin.Exec(context.Background(), `DELETE FROM probe_result WHERE organization_id IN ($1, $2)`,
			f.OrgA, f.OrgB); err != nil {
			t.Errorf("clean probe_result: %v", err)
		}
	}
	clean()
	t.Cleanup(clean)
	e.collector = e.scalar(`INSERT INTO collector (organization_id, name, status) VALUES ($1, 'wp960-a', 'active') RETURNING id::text`, f.OrgA)
	e.collectorB = e.scalar(`INSERT INTO collector (organization_id, name, status) VALUES ($1, 'wp960-b', 'active') RETURNING id::text`, f.OrgB)
	// The collectors join the group the probes of the tests run in; the
	// group of their own is removed, so the org-wide group of schemaEnv is
	// the only one (the probe helpers pick it).
	for _, c := range []struct{ org, id string }{{f.OrgA, e.collector}, {f.OrgB, e.collectorB}} {
		own := e.scalar(`SELECT group_id::text FROM collector_group_member WHERE collector_id = $1`, c.id)
		e.mustExec(`UPDATE collector_group_member SET group_id = (SELECT g.id FROM collector_group g
			WHERE g.organization_id = $1 AND g.name = 'wp935-probes') WHERE collector_id = $2`, c.org, c.id)
		e.mustExec(`DELETE FROM collector_group WHERE id = $1`, own)
	}
	rows, err := f.Admin.Query(context.Background(), `INSERT INTO probe (organization_id, client_id, collector_group_id, type, target)
		SELECT $1, $2, (SELECT g.id FROM collector_group g WHERE g.organization_id = $1 AND g.client_id IS NULL ORDER BY g.id LIMIT 1),
			'icmp', '10.96.' || (g / 250) || '.' || (g % 250 + 1)
		FROM generate_series(0, $3 - 1) g RETURNING id::text`, f.OrgA, f.Client1, probes)
	if err != nil {
		t.Fatal(err)
	}
	e.probes, err = pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	e.mustExec(`INSERT INTO probe_assignment (probe_id, organization_id, client_id, collector_id, epoch)
		SELECT id, organization_id, client_id, $2, 1 FROM probe WHERE organization_id = $1`, f.OrgA, e.collector)
	clientB := e.scalar(`INSERT INTO client (organization_id, name, slug) VALUES ($1, 'wp960 b', 'wp960-b') RETURNING id::text`, f.OrgB)
	e.probeB = e.probeOfClient(f.OrgB, clientB, "10.96.200.1:22")
	e.mustExec(`INSERT INTO probe_assignment (probe_id, organization_id, client_id, collector_id, epoch)
		VALUES ($1, $2, $3, $4, 1)`, e.probeB, f.OrgB, clientB, e.collectorB)
	e.ingester = probe.NewIngester(js, cfg, probe.NewPGIngestStore(f.App))
	return e
}

// results returns n results of the probes of A round robin, seq from..,
// observed one millisecond apart from base.
func (e *ingestEnv) results(base time.Time, from uint64, n int) []probewire.Result {
	out := make([]probewire.Result, n)
	for i := range n {
		rtt := 0.5 + float64(i%7)
		out[i] = probewire.Result{ProbeID: e.probes[i%len(e.probes)], Epoch: 1, Seq: from + uint64(i), //nolint:gosec // small
			ObservedAt: base.Add(time.Duration(from+uint64(i)) * time.Millisecond), Status: probewire.StatusOK, RTTMS: &rtt} //nolint:gosec // small
	}
	return out
}

// publish stores a result batch of the collector of org in the stream
// (without deduplication ID: a repeated publish is a second delivery the
// database must absorb), retrying while the stream is full.
func (e *ingestEnv) publish(ctx context.Context, org, collector string, rs []probewire.Result) {
	e.t.Helper()
	data, err := probewire.Encode(&probewire.ResultBatch{CollectorID: collector, Results: rs})
	if err != nil {
		e.t.Fatal(err)
	}
	for attempt := 0; ; attempt++ {
		_, err := e.js.Publish(ctx, e.cfg.ResultsSubject(org, collector), data)
		if err == nil {
			return
		}
		if attempt > 600 || ctx.Err() != nil {
			e.t.Fatalf("publish: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// rowCount counts the rows of probe_result of the organization.
func (e *ingestEnv) rowCount(org string) int {
	e.t.Helper()
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM probe_result WHERE organization_id = $1`, org).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// drain runs the worker until the consumer has nothing left (pending and
// unacknowledged) and want rows of A are written, or the deadline passes.
func (e *ingestEnv) drain(ctx context.Context, want int) {
	e.t.Helper()
	for {
		if err := e.ingester.RunOnce(ctx); err != nil && ctx.Err() == nil {
			e.t.Logf("fetch: %v", err)
		}
		backlog, err := e.ingester.Backlog(ctx)
		if err == nil && backlog == 0 && e.rowCount(e.f.OrgA) >= want {
			return
		}
		if ctx.Err() != nil {
			e.t.Fatalf("drain: %v (backlog %d, rows %d, want %d)", ctx.Err(), backlog, e.rowCount(e.f.OrgA), want)
		}
	}
}

// runIngest writes total results of A in batches of probewire.MaxBatchResults
// while the worker runs, every dup-th batch published twice, and returns
// the time from the first publish to the last row.
func (e *ingestEnv) runIngest(total, dup int) time.Duration {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	start := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for n, from := 0, uint64(1); n*probewire.MaxBatchResults < total; n, from = n+1, from+probewire.MaxBatchResults {
			size := min(probewire.MaxBatchResults, total-n*probewire.MaxBatchResults)
			rs := e.results(base, from, size)
			e.publish(ctx, e.f.OrgA, e.collector, rs)
			if dup > 0 && n%dup == dup-1 {
				e.publish(ctx, e.f.OrgA, e.collector, rs)
			}
		}
	}()
	// Publish and write concurrently: the stream of the test is smaller
	// than the million results.
	for {
		select {
		case <-done:
			e.drain(ctx, total)
			return time.Since(start)
		default:
		}
		if err := e.ingester.RunOnce(ctx); err != nil && ctx.Err() == nil {
			e.t.Logf("fetch: %v", err)
		}
		if ctx.Err() != nil {
			e.t.Fatal(ctx.Err())
		}
	}
}

// TestProbeIngestIdempotentUnderRedelivery covers PRB-07 and NFR-11: one
// million results, every tenth batch delivered twice, give exactly one
// million rows (ON CONFLICT DO NOTHING on the idempotency key), and the
// written-rows counter does not count the repetitions.
func TestProbeIngestIdempotentUnderRedelivery(t *testing.T) {
	total := 1_000_000
	if testing.Short() {
		total = 100_000
	}
	e := newIngestEnv(t, 100)
	rowsBefore := testutil.ToFloat64(probe.IngestRows())
	took := e.runIngest(total, 10)
	if n := e.rowCount(e.f.OrgA); n != total {
		t.Fatalf("%d rows, want exactly %d", n, total)
	}
	if got := testutil.ToFloat64(probe.IngestRows()) - rowsBefore; int(got) != total {
		t.Errorf("rows counter +%v, want +%d", got, total)
	}
	var stale int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM probe_result WHERE organization_id = $1 AND stale_epoch`,
		e.f.OrgA).Scan(&stale); err != nil || stale != 0 {
		t.Errorf("stale rows %d, %v; want none (epoch 1 is current)", stale, err)
	}
	t.Logf("%d results (every 10th batch twice) in %s: %.0f rows/s", total, took, float64(total)/took.Seconds())
}

// TestProbeIngestThroughput covers NFR-11 (resync peak): a backlog of
// 200,000 results in the stream is written at 5,000 rows/s or more.
func TestProbeIngestThroughput(t *testing.T) {
	const total = 200_000
	e := newIngestEnv(t, 500)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	for from := uint64(1); from <= total; from += probewire.MaxBatchResults {
		e.publish(ctx, e.f.OrgA, e.collector, e.results(base, from, probewire.MaxBatchResults))
	}
	start := time.Now()
	e.drain(ctx, total)
	took := time.Since(start)
	rate := float64(e.rowCount(e.f.OrgA)) / took.Seconds()
	t.Logf("%d rows in %s: %.0f rows/s", total, took, rate)
	if rate < 5000 {
		t.Errorf("throughput %.0f rows/s, want at least 5000", rate)
	}
}

// TestProbeIngestIsolatesOrganizations: one fetch with batches of A and B
// writes each organization's rows in its own transaction; A sees none of
// B's rows through probe_result_v; a batch on A's subject naming the probe
// of B, or a deleted probe of A, writes nothing and is counted as
// unknown_probe.
func TestProbeIngestIsolatesOrganizations(t *testing.T) {
	e := newIngestEnv(t, 3)
	f := e.f
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	now := time.Now().UTC().Add(-time.Minute)
	e.publish(ctx, f.OrgA, e.collector, e.results(now, 1, 6))
	e.publish(ctx, f.OrgB, e.collectorB, []probewire.Result{
		{ProbeID: e.probeB, Epoch: 1, Seq: 1, ObservedAt: now, Status: probewire.StatusFail, Code: probewire.ReasonTimeout},
		{ProbeID: e.probeB, Epoch: 1, Seq: 2, ObservedAt: now.Add(time.Second), Status: probewire.StatusOK},
	})
	// The probe of B smuggled into a batch of A, and a probe of A deleted
	// before its results arrive.
	deleted := e.probes[2]
	e.mustExec(`DELETE FROM probe WHERE id = $1`, deleted)
	unknownBefore := testutil.ToFloat64(probe.IngestDiscarded(probe.DiscardUnknownProbe))
	e.publish(ctx, f.OrgA, e.collector, []probewire.Result{
		{ProbeID: e.probeB, Epoch: 1, Seq: 100, ObservedAt: now, Status: probewire.StatusOK},
		{ProbeID: deleted, Epoch: 1, Seq: 101, ObservedAt: now, Status: probewire.StatusOK},
	})
	e.drain(ctx, 4)

	if n := e.rowCount(f.OrgA); n != 4 {
		t.Errorf("organization A has %d rows, want 4 (6 results, 2 of the deleted probe)", n)
	}
	if n := e.rowCount(f.OrgB); n != 2 {
		t.Errorf("organization B has %d rows, want 2", n)
	}
	if got := testutil.ToFloat64(probe.IngestDiscarded(probe.DiscardUnknownProbe)) - unknownBefore; got != 4 {
		t.Errorf("unknown_probe +%v, want +4 (2 of the deleted probe in the first batch, B's probe and the deleted one)", got)
	}
	var seen int
	if err := e.as(f.OrgCtx(f.OrgA), f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM probe_result_v WHERE probe_id = $1 OR organization_id = $2`,
			e.probeB, f.OrgB).Scan(&seen)
	}); err != nil || seen != 0 {
		t.Errorf("organization A sees %d rows of B, %v", seen, err)
	}
	if n := e.count(f.OrgCtx(f.OrgB), f.OrgB, "probe_result_v"); n != 2 {
		t.Errorf("organization B sees %d rows, want 2", n)
	}
}

// TestProbeIngestStaleEpochStored covers PRB-11: a result with another
// epoch than probe_assignment, and one of a probe without assignment, is
// stored with stale_epoch = true; a result of the current epoch without.
func TestProbeIngestStaleEpochStored(t *testing.T) {
	e := newIngestEnv(t, 2)
	f := e.f
	e.mustExec(`UPDATE probe_assignment SET epoch = 2 WHERE probe_id = $1`, e.probes[0])
	e.mustExec(`DELETE FROM probe_assignment WHERE probe_id = $1`, e.probes[1])
	now := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	out, err := probe.NewPGIngestStore(f.App).WriteResults(context.Background(), f.OrgA, []probe.ResultsMessage{{
		CollectorID: e.collector, Results: []probewire.Result{
			{ProbeID: e.probes[0], Epoch: 1, Seq: 1, ObservedAt: now, Status: probewire.StatusOK},
			{ProbeID: e.probes[0], Epoch: 2, Seq: 2, ObservedAt: now, Status: probewire.StatusOK},
			{ProbeID: e.probes[1], Epoch: 1, Seq: 3, ObservedAt: now, Status: probewire.StatusOK},
		}}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Written != 3 || out.Stale != 2 {
		t.Errorf("outcome %+v, want 3 written, 2 stale", out)
	}
	rows, err := f.Admin.Query(context.Background(), `SELECT seq, epoch, stale_epoch FROM probe_result
		WHERE organization_id = $1 ORDER BY seq`, f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		seq, epoch int64
		stale      bool
	}
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		return x, r.Scan(&x.seq, &x.epoch, &x.stale)
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []row{{1, 1, true}, {2, 2, false}, {3, 1, true}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("rows %v, want %v", got, want)
	}
}

// TestProbeIngestCursorAdvancesInSameTransaction covers AK3: the results
// cursor of the collector moves to the highest seq in the transaction of
// the rows (never back); while another transaction holds the cursor row,
// the rows are not visible, and a write that ends there (context canceled)
// leaves neither rows nor a moved cursor, and the message is not
// acknowledged.
func TestProbeIngestCursorAdvancesInSameTransaction(t *testing.T) {
	e := newIngestEnv(t, 2)
	f := e.f
	bg := context.Background()
	store := probe.NewPGIngestStore(f.App)
	now := time.Now().UTC().Add(-time.Minute)
	msg := func(from uint64, n int) []probe.ResultsMessage {
		return []probe.ResultsMessage{{CollectorID: e.collector, Results: e.results(now, from, n)}}
	}
	cursor := func() int64 {
		t.Helper()
		var seq int64
		err := f.Admin.QueryRow(bg, `SELECT last_seq FROM probe_ingest_cursor WHERE collector_id = $1 AND stream = $2`,
			e.collector, probe.StreamResults).Scan(&seq)
		if errors.Is(err, pgx.ErrNoRows) {
			return -1
		}
		if err != nil {
			t.Fatal(err)
		}
		return seq
	}
	if _, err := store.WriteResults(bg, f.OrgA, msg(1, 10)); err != nil {
		t.Fatal(err)
	}
	if got := cursor(); got != 10 {
		t.Fatalf("cursor %d after seq 1..10, want 10", got)
	}
	// An older repetition does not move the cursor back.
	if _, err := store.WriteResults(bg, f.OrgA, msg(3, 2)); err != nil {
		t.Fatal(err)
	}
	if got := cursor(); got != 10 {
		t.Errorf("cursor %d after the repetition of seq 3..4, want 10", got)
	}

	// Hold the cursor row: the write blocks at its cursor update with its
	// rows not yet visible; canceling it rolls both back.
	lock, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Rollback(bg) }()
	if _, err := lock.Exec(bg, `SELECT 1 FROM probe_ingest_cursor WHERE collector_id = $1 AND stream = $2 FOR UPDATE`,
		e.collector, probe.StreamResults); err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithCancel(bg)
	errc := make(chan error, 1)
	go func() {
		_, werr := store.WriteResults(wctx, f.OrgA, msg(11, 5))
		errc <- werr
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting bool
		if err := f.Admin.QueryRow(bg, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND query LIKE '%probe_ingest_cursor%' AND pid <> pg_backend_pid())`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the write never reached the cursor update")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n := e.rowCount(f.OrgA); n != 10 {
		t.Errorf("%d rows visible while the cursor update waits, want 10 (the new rows are in its transaction)", n)
	}
	cancel()
	if err := <-errc; err == nil {
		t.Fatal("canceled write succeeded")
	}
	_ = lock.Rollback(bg)
	if n := e.rowCount(f.OrgA); n != 10 {
		t.Errorf("%d rows after the canceled write, want 10", n)
	}
	if got := cursor(); got != 10 {
		t.Errorf("cursor %d after the canceled write, want 10", got)
	}

	// Through the worker: the message is acknowledged after the commit,
	// with rows and cursor together.
	ctx, stop := context.WithTimeout(bg, time.Minute)
	defer stop()
	e.publish(ctx, f.OrgA, e.collector, e.results(now, 11, 5))
	e.drain(ctx, 15)
	if got := cursor(); got != 15 {
		t.Errorf("cursor %d after the worker wrote seq 11..15, want 15", got)
	}
}

// TestProbeIngestRecordsCredentialUsage covers CRD-06 (AK7): results with
// the labels credential_id and credential_namespace record the use at the
// CI of the probe in the transaction of the rows: a success sets
// last_success_at, an auth_failed last_failure_at and last_error_class,
// each at the time it was observed, the latest of each per key; a result
// without labels, or a failure whose code says nothing about the
// credential, records nothing. No secret is involved.
func TestProbeIngestRecordsCredentialUsage(t *testing.T) {
	e := newIngestEnv(t, 1)
	f := e.f
	bg := context.Background()
	ciID := f.CI(t, f.OrgA, f.Client1, "wp960-ci")
	probeID := e.probeOnCI(f.OrgA, ciID, "10.96.250.1")
	e.mustExec(`INSERT INTO probe_assignment (probe_id, organization_id, collector_id, epoch)
		SELECT id, organization_id, $2, 1 FROM probe WHERE id = $1`, probeID, e.collector)
	cred := e.scalar(`INSERT INTO credential (organization_id, client_id, name, kind, ciphertext)
		VALUES ($1, $2, 'wp960-snmp', 'snmp_v2c', '\x00'::bytea) RETURNING id::text`, f.OrgA, f.Client1)
	labels := map[string]string{probewire.LabelCredentialID: cred, probewire.LabelCredentialNamespace: "snmp"}
	base := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	okEarly, okLate, failAt := base, base.Add(2*time.Minute), base.Add(time.Minute)
	_, err := probe.NewPGIngestStore(f.App).WriteResults(bg, f.OrgA, []probe.ResultsMessage{{
		CollectorID: e.collector, Results: []probewire.Result{
			{ProbeID: probeID, Epoch: 1, Seq: 1, ObservedAt: okEarly, Status: probewire.StatusOK, Labels: labels},
			{ProbeID: probeID, Epoch: 1, Seq: 2, ObservedAt: failAt, Status: probewire.StatusFail,
				Code: probewire.ReasonAuthFailed, Labels: labels},
			{ProbeID: probeID, Epoch: 1, Seq: 3, ObservedAt: okLate, Status: probewire.StatusOK, Labels: labels},
			// Says nothing about the credential.
			{ProbeID: probeID, Epoch: 1, Seq: 4, ObservedAt: okLate.Add(time.Minute), Status: probewire.StatusFail,
				Code: probewire.ReasonConfig, Labels: labels},
			// Without labels.
			{ProbeID: probeID, Epoch: 1, Seq: 5, ObservedAt: okLate.Add(2 * time.Minute), Status: probewire.StatusFail,
				Code: probewire.ReasonAuthFailed},
		}}})
	if err != nil {
		t.Fatal(err)
	}
	var success, failure *time.Time
	var class *string
	var n int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) OVER (), last_success_at, last_failure_at, last_error_class
		FROM credential_usage WHERE credential_id = $1`, cred).Scan(&n, &success, &failure, &class); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d usage rows, want 1 (credential, CI of the probe, snmp)", n)
	}
	if success == nil || !success.Equal(okLate) {
		t.Errorf("last_success_at %v, want %v", success, okLate)
	}
	if failure == nil || !failure.Equal(failAt) {
		t.Errorf("last_failure_at %v, want %v", failure, failAt)
	}
	if class == nil || *class != "auth_failed" {
		t.Errorf("last_error_class %v, want auth_failed", class)
	}
	var ci string
	if err := f.Admin.QueryRow(bg, `SELECT ci_id::text FROM credential_usage WHERE credential_id = $1`, cred).Scan(&ci); err != nil || ci != ciID {
		t.Errorf("usage at CI %q, %v; want the probe's CI %s", ci, err, ciID)
	}
}

// TestProbeIngestRejectsForeignCollector covers PRB-11 and the client
// boundary: a collector bound to client 2 that reports a result for a
// probe of client 1 with its current epoch writes no row and no credential
// use (foreign_collector); an org-wide collector that is not the assigned
// one is stored with stale_epoch = true and records no credential use
// either; the assigned collector's result is current.
func TestProbeIngestRejectsForeignCollector(t *testing.T) {
	e := newIngestEnv(t, 1)
	f := e.f
	bg := context.Background()
	ciID := f.CI(t, f.OrgA, f.Client1, "wp960-foreign-ci")
	probeID := e.probeOnCI(f.OrgA, ciID, "10.96.251.1")
	e.mustExec(`INSERT INTO probe_assignment (probe_id, organization_id, collector_id, epoch)
		SELECT id, organization_id, $2, 1 FROM probe WHERE id = $1`, probeID, e.collector)
	foreign := e.scalar(`INSERT INTO collector (organization_id, client_id, name, status) VALUES ($1, $2, 'wp960-c2', 'active')
		RETURNING id::text`, f.OrgA, f.Client2)
	other := e.scalar(`INSERT INTO collector (organization_id, name, status) VALUES ($1, 'wp960-other', 'active') RETURNING id::text`, f.OrgA)
	cred := e.scalar(`INSERT INTO credential (organization_id, client_id, name, kind, ciphertext)
		VALUES ($1, $2, 'wp960-foreign', 'snmp_v2c', '\x00'::bytea) RETURNING id::text`, f.OrgA, f.Client1)
	labels := map[string]string{probewire.LabelCredentialID: cred, probewire.LabelCredentialNamespace: "snmp"}
	now := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	result := func(seq uint64) probewire.Result {
		return probewire.Result{ProbeID: probeID, Epoch: 1, Seq: seq, ObservedAt: now, Status: probewire.StatusFail,
			Code: probewire.ReasonAuthFailed, Labels: labels}
	}
	out, err := probe.NewPGIngestStore(f.App).WriteResults(bg, f.OrgA, []probe.ResultsMessage{
		{CollectorID: foreign, Results: []probewire.Result{result(1)}},
		{CollectorID: other, Results: []probewire.Result{result(2)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.ForeignCollector != 1 || out.Written != 1 || out.Stale != 1 {
		t.Errorf("outcome %+v, want 1 foreign, 1 written, 1 stale", out)
	}
	var fromForeign, current int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FILTER (WHERE collector_id = $2), count(*) FILTER (WHERE NOT stale_epoch)
		FROM probe_result WHERE organization_id = $1 AND probe_id = $3`, f.OrgA, foreign, probeID).Scan(&fromForeign, &current); err != nil {
		t.Fatal(err)
	}
	if fromForeign != 0 || current != 0 {
		t.Errorf("%d rows of the foreign collector, %d current rows; want none", fromForeign, current)
	}
	var usage int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM credential_usage WHERE credential_id = $1`, cred).Scan(&usage); err != nil {
		t.Fatal(err)
	}
	if usage != 0 {
		t.Errorf("%d credential_usage rows from collectors other than the assigned one, want 0", usage)
	}
	// The assigned collector: current, with its credential use.
	if _, err := probe.NewPGIngestStore(f.App).WriteResults(bg, f.OrgA, []probe.ResultsMessage{
		{CollectorID: e.collector, Results: []probewire.Result{result(3)}}}); err != nil {
		t.Fatal(err)
	}
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM credential_usage WHERE credential_id = $1`, cred).Scan(&usage); err != nil || usage != 1 {
		t.Errorf("%d credential_usage rows after the assigned collector (%v), want 1", usage, err)
	}
}
