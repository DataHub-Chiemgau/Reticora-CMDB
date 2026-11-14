package alarm_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Retest (WP-986, ALR-02, MON-04):
//
//	cd backend && TEST_NATS_URL=nats://127.0.0.1:4222 go test -count=1 -p 1 -run 'TestCheckConsumer' ./internal/alarm/

// checkEnv is organization A of the alarm fixture with a collector group,
// one collector, the state worker without stream and the check consumer on
// a stream of its own.
type checkEnv struct {
	t         *testing.T
	f         *scopetest.Fixture
	js        jetstream.JetStream
	prefix    string
	group     string
	collector string
	applier   *probe.StateApplier
	consumer  *alarm.CheckConsumer
	seq       map[string]uint64
	n         int
}

func newCheckEnv(t *testing.T) *checkEnv {
	t.Helper()
	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Skip("TEST_NATS_URL not set")
	}
	f := scopetest.Seed(t, alarmTag)
	nc, err := nats.Connect(url, nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	suffix := hex.EncodeToString(b)
	e := &checkEnv{t: t, f: f, js: js, prefix: "alarmtest" + suffix + ".", seq: map[string]uint64{}}
	name := "RETICORA_EVENTS_TEST_" + strings.ToUpper(suffix)
	ctx := context.Background()
	if _, err := js.CreateStream(ctx, jetstream.StreamConfig{Name: name, Subjects: []string{e.prefix + ">"}, Storage: jetstream.MemoryStorage}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = js.DeleteStream(context.Background(), name) })
	e.consumer = alarm.NewCheckConsumer(alarm.NewPGService(f.App), alarm.PGCheckReader{Pool: f.App}, js)
	e.consumer.Stream, e.consumer.Durable, e.consumer.Filter = name, "alarm-check-"+suffix, e.prefix+"*."+events.ProbeStateChanged
	e.group = e.scalar(`INSERT INTO collector_group (organization_id, name) VALUES ($1, 'wp986') RETURNING id::text`, f.OrgA)
	e.collector = e.scalar(`INSERT INTO collector (organization_id, name, status) VALUES ($1, 'wp986', 'active') RETURNING id::text`, f.OrgA)
	e.exec(`UPDATE collector_group_member SET group_id = $1, last_alive_at = now() WHERE collector_id = $2`, e.group, e.collector)
	e.applier = probe.NewStateApplier(nil, probe.StreamConfig{}, f.App)
	return e
}

func (e *checkEnv) scalar(sql string, args ...any) string {
	e.t.Helper()
	var out string
	if err := e.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return out
}

func (e *checkEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.f.Admin.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

// probes creates n assigned icmp probes on CIs of client 1.
func (e *checkEnv) probes(n int) []string {
	e.t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `INSERT INTO probe (organization_id, client_id, collector_group_id, type, target)
		SELECT $1, $2, $3, 'icmp', '10.86.' || ((i + $5) / 250) || '.' || ((i + $5) % 250 + 1) FROM generate_series(1, $4::int) AS i
		RETURNING id::text`, e.f.OrgA, e.f.Client1, e.group, n, e.n)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			e.t.Fatal(err)
		}
		ids = append(ids, id)
	}
	e.n += n
	if _, err := probe.NewAssigner(e.f.App).RunGroup(context.Background(), e.f.OrgA, e.group); err != nil {
		e.t.Fatal(err)
	}
	sort.Strings(ids)
	return ids
}

func (e *checkEnv) epoch(id string) int64 {
	e.t.Helper()
	var ep int64
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT epoch FROM probe_assignment WHERE probe_id = $1`, id).Scan(&ep); err != nil {
		e.t.Fatal(err)
	}
	return ep
}

// change applies the next change of the probe as the state worker does.
func (e *checkEnv) change(id string, from, to probewire.State, at time.Time) {
	e.t.Helper()
	e.seq[id]++
	if err := e.applier.Apply(context.Background(), e.f.OrgA, e.collector, &probewire.StateChange{ProbeID: id, Epoch: e.epoch(id),
		Seq: e.seq[id], From: from, To: to, Reason: "timeout", ObservedAt: at}, at.Add(time.Second)); err != nil {
		e.t.Fatal(err)
	}
}

// outbox returns the probe.state_changed envelopes of the probe as the
// publisher sends them, oldest first.
func (e *checkEnv) outbox(id string) []events.Envelope {
	e.t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT id::text, sequence, COALESCE(entity_id, ''), data, occurred_at FROM event_outbox
		WHERE organization_id = $1 AND type = 'probe.state_changed' AND entity_id = $2 ORDER BY sequence`, e.f.OrgA, id)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []events.Envelope
	for rows.Next() {
		env := events.Envelope{Type: events.ProbeStateChanged, OrgID: e.f.OrgA}
		var raw []byte
		if err := rows.Scan(&env.ID, &env.Sequence, &env.EntityID, &raw, &env.OccurredAt); err != nil {
			e.t.Fatal(err)
		}
		env.Data = raw
		out = append(out, env)
	}
	return out
}

func (e *checkEnv) publish(envs ...events.Envelope) {
	e.t.Helper()
	for i := range envs {
		raw, err := json.Marshal(&envs[i])
		if err != nil {
			e.t.Fatal(err)
		}
		if _, err := e.js.Publish(context.Background(), e.prefix+envs[i].OrgID+"."+envs[i].Type, raw); err != nil {
			e.t.Fatal(err)
		}
	}
}

// drain handles until the consumer has nothing pending.
func (e *checkEnv) drain() {
	e.t.Helper()
	for range 200 {
		if err := e.consumer.RunOnce(context.Background()); err != nil {
			e.t.Fatal(err)
		}
		n, err := e.consumer.Backlog(context.Background())
		if err != nil {
			e.t.Fatal(err)
		}
		if n == 0 {
			return
		}
	}
	e.t.Fatal("the events were not drained")
}

// alarmTrail is the state of the check alarms of a probe and their
// transitions, without ids: "state severity [from>to reason, …]".
func (e *checkEnv) alarmTrail(id string) []string {
	e.t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT a.state, a.severity, COALESCE(a.opened_at::text, ''),
			COALESCE(a.resolved_at::text, ''),
			COALESCE((SELECT string_agg(COALESCE(t.from_state, '') || '>' || t.to_state || ' ' || t.reason, ', ' ORDER BY t.at, t.id)
			            FROM alarm_transition t WHERE t.alarm_id = a.id), '')
		  FROM alarm a WHERE a.organization_id = $1 AND a.source_kind = 'check' AND a.source_id = $2 ORDER BY a.first_seen_at`, e.f.OrgA, id)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var state, sev, opened, resolved, trail string
		if err := rows.Scan(&state, &sev, &opened, &resolved, &trail); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s %s [%s]", state, sev, trail))
	}
	return out
}

// TestCheckConsumerDuplicateAndReorder covers AK4: the same changes of a
// probe delivered once in order and delivered twice in reverse order give
// the same alarm state and the same transitions; a progressive history
// with repeated events gives the trail of the one without repetition.
func TestCheckConsumerDuplicateAndReorder(t *testing.T) {
	e := newCheckEnv(t)
	ids := e.probes(4)
	inOrder, shuffled, stepped, steppedDup := ids[0], ids[1], ids[2], ids[3]
	t0 := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	history := func(id string) {
		e.change(id, probewire.StateUnknown, probewire.StateDown, t0)
		e.change(id, probewire.StateDown, probewire.StateUp, t0.Add(time.Minute))
		e.change(id, probewire.StateUp, probewire.StateDegraded, t0.Add(2*time.Minute))
	}
	history(inOrder)
	history(shuffled)
	e.publish(e.outbox(inOrder)...)
	envs := e.outbox(shuffled)
	slices.Reverse(envs)
	e.publish(append(envs, envs...)...)
	e.drain()
	a, b := e.alarmTrail(inOrder), e.alarmTrail(shuffled)
	if len(a) != 1 || !strings.HasPrefix(a[0], "open warning") || !slices.Equal(a, b) {
		t.Errorf("in order %v, twice reversed %v; want the same open warning alarm", a, b)
	}
	// Progressive: each change delivered after it was applied; the second
	// probe gets every event twice and an older one again.
	steps := []struct{ from, to probewire.State }{{probewire.StateUnknown, probewire.StateDown}, {probewire.StateDown, probewire.StateUp},
		{probewire.StateUp, probewire.StateDown}}
	for i, s := range steps {
		at := t0.Add(time.Duration(i) * time.Minute)
		e.change(stepped, s.from, s.to, at)
		e.change(steppedDup, s.from, s.to, at)
		one := e.outbox(stepped)
		e.publish(one[len(one)-1])
		dup := e.outbox(steppedDup)
		e.publish(dup[len(dup)-1], dup[len(dup)-1], dup[0])
		e.drain()
	}
	a, b = e.alarmTrail(stepped), e.alarmTrail(steppedDup)
	if len(a) != 2 || !strings.HasPrefix(a[0], "resolved critical") || !strings.HasPrefix(a[1], "open critical") || !slices.Equal(a, b) {
		t.Errorf("progressive %v, with repetitions %v; want a resolved and an open critical alarm, equal", a, b)
	}
}

// TestCheckConsumerBurstAfterReconnect covers AK2 and AK4: a buffered burst
// of 5,000 changes (500 probes, ten or nine changes each) arrives at once;
// every probe ends with the alarm of its final state.
func TestCheckConsumerBurstAfterReconnect(t *testing.T) {
	e := newCheckEnv(t)
	ids := e.probes(500)
	t0 := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Millisecond)
	changes := 0
	for i, id := range ids {
		n := 10
		if i%2 == 1 {
			n = 9 // ends down
		}
		from := probewire.StateUnknown
		for k := 1; k <= n; k++ {
			to := probewire.StateDown
			if k%2 == 0 {
				to = probewire.StateUp
			}
			e.change(id, from, to, t0.Add(time.Duration(k)*time.Minute))
			from = to
			changes++
		}
	}
	if changes < 4750 {
		t.Fatalf("%d changes", changes)
	}
	var all []events.Envelope
	for _, id := range ids {
		all = append(all, e.outbox(id)...)
	}
	e.publish(all...)
	e.drain()
	for i, id := range ids {
		trail := e.alarmTrail(id)
		open := 0
		for _, a := range trail {
			if strings.HasPrefix(a, "open") {
				open++
			}
		}
		wantOpen := 0
		if i%2 == 1 {
			wantOpen = 1
		}
		if open != wantOpen {
			t.Fatalf("probe %d (%s): alarms %v, want %d open", i, id, trail, wantOpen)
		}
	}
}

// TestCheckConsumerNetdownOpensNothing covers AK2: the group is unreachable
// and every probe unknown with reason collector_unreachable: no new alarm,
// an alarm open before stays open.
func TestCheckConsumerNetdownOpensNothing(t *testing.T) {
	e := newCheckEnv(t)
	ids := e.probes(20)
	t0 := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	e.change(ids[0], probewire.StateUnknown, probewire.StateDown, t0)
	e.publish(e.outbox(ids[0])...)
	e.drain()
	// Network down (WP-964): every probe unknown/collector_unreachable.
	e.exec(`UPDATE collector_group SET status = 'unreachable', status_changed_at = now() WHERE id = $1`, e.group)
	for _, id := range ids {
		e.seq[id]++
		if err := e.applier.Apply(context.Background(), e.f.OrgA, e.collector, &probewire.StateChange{ProbeID: id, Epoch: e.epoch(id),
			Seq: e.seq[id], From: probewire.StateUp, To: probewire.StateUnknown, Reason: probewire.ReasonCollectorUnreachable,
			ObservedAt: t0.Add(time.Minute)}, t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		envs := e.outbox(id)
		e.publish(envs[len(envs)-1])
	}
	e.drain()
	if n := e.scalar(`SELECT count(*)::text FROM alarm WHERE organization_id = $1 AND source_kind = 'check' AND source_id = ANY ($2::uuid[])`,
		e.f.OrgA, ids); n != "1" {
		t.Errorf("%s check alarms, want only the one open before the network down", n)
	}
	if trail := e.alarmTrail(ids[0]); len(trail) != 1 || !strings.HasPrefix(trail[0], "open critical") {
		t.Errorf("alarm open before the network down: %v, want it still open", trail)
	}
}

// TestCheckConsumerLatency covers AK6: 500 changes per second over 60 s
// (10 s with -short); p95 from the receipt of a change to the commit of its
// alarm is at most 10 s.
func TestCheckConsumerLatency(t *testing.T) {
	e := newCheckEnv(t)
	ids := e.probes(500)
	duration := 60 * time.Second
	if testing.Short() {
		duration = 10 * time.Second
	}
	var mu sync.Mutex
	var lags []time.Duration
	e.consumer.WithLagObserver(func(d time.Duration) {
		mu.Lock()
		lags = append(lags, d)
		mu.Unlock()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.consumer.Run(ctx)
	}()
	epochs := map[string]int64{}
	for _, id := range ids {
		epochs[id] = e.epoch(id)
	}
	epochList := make([]int64, len(ids))
	for i, id := range ids {
		epochList[i] = epochs[id]
	}
	start := time.Now()
	sent := 0
	for tick := 0; time.Since(start) < duration; tick++ {
		next := start.Add(time.Duration(tick+1) * time.Second)
		to := "down"
		if tick%2 == 1 {
			to = "up"
		}
		// 500 changes in one statement, as the state worker writes them.
		e.exec(`WITH u AS (SELECT * FROM unnest($2::uuid[], $3::bigint[]) AS u (probe_id, epoch)),
			s AS (INSERT INTO probe_state AS s (probe_id, organization_id, state, reason, since, last_seq, epoch, collector_id)
				SELECT u.probe_id, $1, $4, 'timeout', clock_timestamp(), $5, u.epoch, $6 FROM u
				ON CONFLICT (probe_id) DO UPDATE SET state = EXCLUDED.state, since = EXCLUDED.since, last_seq = EXCLUDED.last_seq
				RETURNING s.probe_id)
			INSERT INTO probe_state_change (organization_id, probe_id, from_state, to_state, reason, observed_at, received_at,
				epoch, seq, collector_id, late, notify)
			SELECT $1, u.probe_id, 'unknown', $4, 'timeout', clock_timestamp(), clock_timestamp(), u.epoch, $5, $6, false, true FROM u`,
			e.f.OrgA, ids, epochList, to, tick+1, e.collector)
		envs := make([]events.Envelope, len(ids))
		for i, id := range ids {
			data, _ := json.Marshal(map[string]any{"id": id, "probe_id": id, "to": to, "late": false, "notify": true})
			envs[i] = events.Envelope{ID: fmt.Sprintf("lat-%d-%d", tick, i), Type: events.ProbeStateChanged, OrgID: e.f.OrgA, EntityID: id,
				Data: data, OccurredAt: time.Now()}
		}
		e.publish(envs...)
		sent += len(envs)
		time.Sleep(time.Until(next))
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		mu.Lock()
		n := len(lags)
		mu.Unlock()
		if n >= sent || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(lags) < sent*95/100 {
		t.Fatalf("%d of %d changes handled", len(lags), sent)
	}
	slices.Sort(lags)
	p95 := lags[len(lags)*95/100]
	t.Logf("%d changes, p95 %s, max %s", len(lags), p95, lags[len(lags)-1])
	if p95 > 10*time.Second {
		t.Errorf("p95 %s from the receipt to the alarm, want at most 10 s", p95)
	}
}

// pausingReader reads like PGCheckReader and then waits for release once,
// with the transaction (and its lock) still open.
type pausingReader struct {
	inner   alarm.PGCheckReader
	read    chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *pausingReader) ReadCheck(ctx context.Context, orgID, probeID string) (*probe.CheckView, error) {
	v, err := r.inner.ReadCheck(ctx, orgID, probeID)
	r.once.Do(func() {
		close(r.read)
		<-r.release
	})
	return v, err
}

// TestCheckConsumerTwoInstancesSerializePerProbe: two instances reconcile
// the same probe at once (a redelivery or the replicas of the worker).
// Instance 1 has read down and not committed its raise yet when the probe
// turns up and instance 2 takes the up event; instance 2 waits for
// instance 1 (alarm_check:<probe_id>) and then resolves the alarm, so it
// ends in the probe's current state instead of staying open.
func TestCheckConsumerTwoInstancesSerializePerProbe(t *testing.T) {
	e := newCheckEnv(t)
	id := e.probes(1)[0]
	t0 := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Millisecond)
	e.change(id, probewire.StateUnknown, probewire.StateDown, t0)
	envs := e.outbox(id)
	down := envs[len(envs)-1]
	reader := &pausingReader{inner: alarm.PGCheckReader{Pool: e.f.App}, read: make(chan struct{}), release: make(chan struct{})}
	first := alarm.NewCheckConsumer(alarm.NewPGService(e.f.App), reader, nil)
	second := alarm.NewCheckConsumer(alarm.NewPGService(e.f.App), alarm.PGCheckReader{Pool: e.f.App}, nil)
	ctx := context.Background()
	done1 := make(chan error, 1)
	go func() { done1 <- first.Handle(ctx, &down) }()
	<-reader.read
	e.change(id, probewire.StateDown, probewire.StateUp, t0.Add(time.Minute))
	envs = e.outbox(id)
	up := envs[len(envs)-1]
	done2 := make(chan error, 1)
	go func() { done2 <- second.Handle(ctx, &up) }()
	select {
	case err := <-done2:
		t.Errorf("instance 2 reconciled while instance 1 held the probe (err %v); want it to wait", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(reader.release)
	if err := <-done1; err != nil {
		t.Fatal(err)
	}
	if err := <-done2; err != nil {
		t.Fatal(err)
	}
	trail := e.alarmTrail(id)
	if len(trail) != 1 || !strings.HasPrefix(trail[0], "resolved ") {
		t.Errorf("alarm %v, want one alarm resolved (the probe is up)", trail)
	}
}
