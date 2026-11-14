package probe_test

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Retest (WP-961, PRB-05, PRB-07, PRB-11):
//
//	cd backend && TEST_NATS_URL=nats://127.0.0.1:4222 go test -count=1 -p 1 -run 'TestStateApply' ./internal/probe/

// stateEnv is organization A with two active collectors in an org-wide
// group and a stream of its own.
type stateEnv struct {
	t          *testing.T
	f          *scopetest.Fixture
	js         jetstream.JetStream
	cfg        probe.StreamConfig
	group      string
	collectors []string
	applier    *probe.StateApplier
}

func newStateEnv(t *testing.T) *stateEnv {
	t.Helper()
	_, js, _, cfg := natsJetStream(t)
	f := scopetest.Seed(t, "93")
	e := &stateEnv{t: t, f: f, js: js, cfg: cfg}
	e.group = e.scalar(`INSERT INTO collector_group (organization_id, name, mode) VALUES ($1, 'wp961', 'failover') RETURNING id::text`, f.OrgA)
	for i := range 2 {
		c := e.scalar(`INSERT INTO collector (organization_id, name, status) VALUES ($1, $2, 'active') RETURNING id::text`, f.OrgA,
			fmt.Sprintf("wp961-%d", i))
		e.exec(`UPDATE collector_group_member SET group_id = $1, priority = $2, last_alive_at = now() WHERE collector_id = $3`, e.group, 10-i, c)
		e.collectors = append(e.collectors, c)
	}
	e.applier = probe.NewStateApplier(js, cfg, f.App)
	return e
}

func (e *stateEnv) scalar(sql string, args ...any) string {
	e.t.Helper()
	var out string
	if err := e.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return out
}

func (e *stateEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.f.Admin.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

// probe creates an assigned icmp probe and returns it with its epoch and
// collector.
func (e *stateEnv) probe(target string) (id string, epoch int64, collector string) {
	e.t.Helper()
	id = e.scalar(`INSERT INTO probe (organization_id, client_id, collector_group_id, type, target) VALUES ($1, $2, $3, 'icmp', $4)
		RETURNING id::text`, e.f.OrgA, e.f.Client1, e.group, target)
	if _, err := probe.NewAssigner(e.f.App).RunOrg(context.Background(), e.f.OrgA); err != nil {
		e.t.Fatal(err)
	}
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT epoch, collector_id::text FROM probe_assignment WHERE probe_id = $1`, id).
		Scan(&epoch, &collector); err != nil {
		e.t.Fatal(err)
	}
	return id, epoch, collector
}

// publish stores a state change of the collector in the stream.
func (e *stateEnv) publish(collector string, c probewire.StateChange) {
	e.t.Helper()
	raw, err := probewire.Encode(&c)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.js.Publish(context.Background(), e.cfg.StateSubject(e.f.OrgA, collector), raw); err != nil {
		e.t.Fatal(err)
	}
}

// drain applies until the consumer has nothing pending.
func (e *stateEnv) drain() {
	e.t.Helper()
	for range 20 {
		if err := e.applier.RunOnce(context.Background()); err != nil {
			e.t.Fatal(err)
		}
		n, err := e.applier.Backlog(context.Background())
		if err != nil {
			e.t.Fatal(err)
		}
		if n == 0 {
			return
		}
	}
	e.t.Fatal("the state changes were not drained")
}

func change(probeID string, epoch int64, seq uint64, from, to probewire.State, at time.Time) probewire.StateChange {
	return probewire.StateChange{ProbeID: probeID, Epoch: epoch, Seq: seq, From: from, To: to, Reason: "timeout", ObservedAt: at}
}

// history reads seq/to/stale/notify of the changes of the probe.
func (e *stateEnv) history(id string) []string {
	e.t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT seq, to_state, stale, notify, late FROM probe_state_change WHERE probe_id = $1`, id)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var seq int64
		var to string
		var stale, notify, late bool
		if err := rows.Scan(&seq, &to, &stale, &notify, &late); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%d:%s:stale=%v:notify=%v:late=%v", seq, to, stale, notify, late))
	}
	sort.Strings(out)
	return out
}

func (e *stateEnv) state(id string) string {
	e.t.Helper()
	var s string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT state || ':' || last_seq || ':' || epoch || ':' || collector_id FROM probe_state
		WHERE probe_id = $1`, id).Scan(&s); err != nil {
		return ""
	}
	return s
}

func (e *stateEnv) events(id string) int {
	e.t.Helper()
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM event_outbox WHERE type = 'probe.state_changed' AND entity_id = $1`,
		id).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestStateApplyIdempotentAndOrdered(t *testing.T) {
	e := newStateEnv(t)
	now := time.Now()
	seqs := func(id string, epoch int64) []probewire.StateChange {
		return []probewire.StateChange{
			change(id, epoch, 1, probewire.StateUnknown, probewire.StateUp, now.Add(-3*time.Second)),
			change(id, epoch, 2, probewire.StateUp, probewire.StateDown, now.Add(-2*time.Second)),
			change(id, epoch, 3, probewire.StateDown, probewire.StateUp, now.Add(-time.Second)),
		}
	}
	a, epochA, colA := e.probe("10.61.0.1")
	for range 2 {
		for _, c := range seqs(a, epochA) {
			e.publish(colA, c)
		}
	}
	e.drain()
	b, epochB, colB := e.probe("10.61.0.2")
	list := seqs(b, epochB)
	slices.Reverse(list)
	for range 2 {
		for _, c := range list {
			e.publish(colB, c)
		}
	}
	e.drain()
	// The whole rows (seq, to, stale, notify, late) agree (AK6).
	ha, hb := e.history(a), e.history(b)
	if len(ha) != 3 || !slices.Equal(ha, hb) {
		t.Errorf("history in order %v, reversed %v; want the same three changes", ha, hb)
	}
	sa, sb := e.state(a), e.state(b)
	if !strings.HasPrefix(sa, "up:3:") || !strings.HasPrefix(sb, "up:3:") {
		t.Errorf("state in order %s, reversed %s; want up at seq 3", sa, sb)
	}
	// One event per applied change: all three in order, only seq 3 reversed
	// (the older ones arrived after it and are history only).
	if na, nb := e.events(a), e.events(b); na != 3 || nb != 1 {
		t.Errorf("events in order %d, reversed %d; want 3 and 1", na, nb)
	}
}

func TestStateApplyStaleEpochOnlyHistory(t *testing.T) {
	e := newStateEnv(t)
	id, epoch, collector := e.probe("10.61.1.1")
	e.publish(collector, change(id, epoch-1, 7, probewire.StateUp, probewire.StateDown, time.Now()))
	e.drain()
	if h := e.history(id); len(h) != 1 || !strings.Contains(h[0], "stale=true:notify=false") {
		t.Errorf("history %v, want one stale change without notify", h)
	}
	if s := e.state(id); s != "" {
		t.Errorf("state %q, want none", s)
	}
	if n := e.events(id); n != 0 {
		t.Errorf("%d events, want none", n)
	}
	// An epoch above the assignment is refused.
	e.publish(collector, change(id, epoch+1, 1, probewire.StateUp, probewire.StateDown, time.Now()))
	e.drain()
	if h := e.history(id); len(h) != 1 {
		t.Errorf("history %v, want the future epoch refused", h)
	}
}

func TestStateApplyCollectorSwitchNewEpoch(t *testing.T) {
	e := newStateEnv(t)
	id, epoch, first := e.probe("10.61.2.1")
	e.publish(first, change(id, epoch, 900, probewire.StateUnknown, probewire.StateUp, time.Now().Add(-time.Second)))
	e.drain()
	other := e.collectors[0]
	if other == first {
		other = e.collectors[1]
	}
	e.exec(`UPDATE probe_assignment SET collector_id = $2, epoch = $3 WHERE probe_id = $1`, id, other, epoch+1)
	e.publish(other, change(id, epoch+1, 1, probewire.StateUp, probewire.StateDown, time.Now()))
	e.drain()
	if s := e.state(id); s != fmt.Sprintf("down:1:%d:%s", epoch+1, other) {
		t.Errorf("state %s, want down at seq 1 of the new epoch of the new collector", s)
	}
	if n := e.events(id); n != 2 {
		t.Errorf("%d events, want 2", n)
	}
}

func TestStateApplyLateNotify(t *testing.T) {
	e := newStateEnv(t)
	old := time.Now().Add(-10 * time.Minute)
	a, epochA, colA := e.probe("10.61.3.1")
	e.publish(colA, change(a, epochA, 1, probewire.StateUp, probewire.StateDown, old))
	e.publish(colA, change(a, epochA, 2, probewire.StateDown, probewire.StateUp, old.Add(time.Second)))
	e.drain()
	h := e.history(a)
	if len(h) != 2 || h[0] != "1:down:stale=false:notify=false:late=true" || h[1] != "2:up:stale=false:notify=true:late=true" {
		t.Errorf("history %v; want the late change with a successor without notify, the last one with", h)
	}
}

func TestStateApplyLatencyP95(t *testing.T) {
	e := newStateEnv(t)
	id, epoch, collector := e.probe("10.61.4.1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Background load: 353 results per second through the ingest of
	// WP-960 on the same stream (ten ticks per second, three of 36 and
	// seven of 35 results).
	ingester := probe.NewIngester(e.js, e.cfg, probe.NewPGIngestStore(e.f.App)).WithStates(e.applier)
	go ingester.Run(ctx)
	go func() {
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		var seq uint64 = 1 << 20
		for n := 0; ; n++ {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			batch := probewire.ResultBatch{CollectorID: collector}
			size := 35
			if n%10 < 3 {
				size = 36
			}
			for range size {
				seq++
				batch.Results = append(batch.Results, probewire.Result{ProbeID: id, Epoch: epoch, Seq: seq, ObservedAt: time.Now().UTC(),
					Status: probewire.StatusOK})
			}
			raw, _ := probewire.Encode(&batch)
			_, _ = e.js.Publish(ctx, e.cfg.ResultsSubject(e.f.OrgA, collector), raw)
		}
	}()
	var lat []time.Duration
	states := []probewire.State{probewire.StateUp, probewire.StateDown}
	for i := range 30 {
		at := time.Now()
		e.publish(collector, change(id, epoch, uint64(i+1), states[(i+1)%2], states[i%2], at))
		want := fmt.Sprintf(":%d:", i+1)
		for !strings.Contains(e.state(id), want) {
			if time.Since(at) > 10*time.Second {
				t.Fatalf("change %d not visible after 10 s (state %s)", i+1, e.state(id))
			}
			time.Sleep(20 * time.Millisecond)
		}
		lat = append(lat, time.Since(at))
	}
	slices.Sort(lat)
	p95 := lat[len(lat)*95/100]
	if p95 >= 5*time.Second {
		t.Errorf("p95 %v, want < 5 s", p95)
	}
	t.Logf("p95 from observed_at to probe_state: %v", p95)
}

// TestStateApplyFirstChangeRace: two instances apply the first two changes
// of a probe at the same time, before a probe_state row exists. The lock
// probe_state:<probe> serializes them: the state ends at the younger
// change, never at the older one, and the history is the same as in order
// (AK2, AK6).
func TestStateApplyFirstChangeRace(t *testing.T) {
	e := newStateEnv(t)
	other := probe.NewStateApplier(e.js, e.cfg, e.f.App)
	now := time.Now()
	for i := range 10 {
		id, epoch, collector := e.probe(fmt.Sprintf("10.61.5.%d", i+1))
		first := change(id, epoch, 1, probewire.StateUnknown, probewire.StateUp, now.Add(-2*time.Second))
		second := change(id, epoch, 2, probewire.StateUp, probewire.StateDown, now.Add(-time.Second))
		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			errs[0] = other.Apply(context.Background(), e.f.OrgA, collector, &second, now)
		}()
		go func() {
			defer wg.Done()
			errs[1] = e.applier.Apply(context.Background(), e.f.OrgA, collector, &first, now)
		}()
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		if s := e.state(id); !strings.HasPrefix(s, "down:2:") {
			t.Errorf("probe %d: state %s, want down at seq 2", i, s)
		}
		want := []string{"1:up:stale=false:notify=true:late=false", "2:down:stale=false:notify=true:late=false"}
		if h := e.history(id); !slices.Equal(h, want) {
			t.Errorf("probe %d: history %v, want %v", i, h, want)
		}
		if n := e.events(id); n < 1 || n > 2 {
			t.Errorf("probe %d: %d events, want the younger change published", i, n)
		}
	}
}
