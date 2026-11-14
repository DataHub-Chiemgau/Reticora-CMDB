package probe_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
	cprobe "github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Retest (WP-963, PRB-03, PRB-10, PRB-11):
//
//	cd backend && go test -count=1 -p 1 -run 'TestFailover|TestSplitBrain|TestMemberReturn|TestMemberWithoutAlive|TestLivenessIndependent' ./internal/probe/

// liveEnv is organization A with an org-wide collector group of n active
// collectors (priority 30, 20, …), the production path of probe.assign
// (Assigner.Tick with the liveness sweep, the NATS notifier .probes.changed
// on TEST_NATS_URL) and a fake clock: alive messages and ticks take the time
// explicitly. Every collector is modelled like the engine of WP-956: it
// listens on collector.<org>.<id>.probes.changed, fetches its set through
// the distribution (the handler of .probes.request) and reports the
// fetched version as config_version in its next alive message.
type liveEnv struct {
	t          *testing.T
	f          *scopetest.Fixture
	group      string
	collectors []string
	skew       *probe.ClockSkew
	live       *probe.Liveness
	assigner   *probe.Assigner
	dist       *probe.Distributor
	subs       map[string]*nats.Subscription

	// held is the set version a collector applied, sets the set it holds;
	// offline collectors are cut off from the server (no push, no fetch).
	held    map[string]int64
	sets    map[string][]probewire.ProbeSpec
	offline map[string]bool

	mu       sync.Mutex
	returned []string
}

// syncTrigger runs the assignment of a group synchronously (the production
// trigger runs it in a goroutine), so a test sees its result at once.
type syncTrigger struct{ a *probe.Assigner }

func (s syncTrigger) ProbesChanged(ctx context.Context, orgID string, groupIDs []string, raised []probe.CollectorVersion) {
	for _, g := range groupIDs {
		_, _ = s.a.RunGroup(ctx, orgID, g)
	}
}

func newLiveEnv(t *testing.T, mode string, n int) *liveEnv {
	t.Helper()
	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Skip("TEST_NATS_URL not set")
	}
	f := scopetest.Seed(t, "93")
	nc, err := nats.Connect(url, nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	e := &liveEnv{t: t, f: f, subs: map[string]*nats.Subscription{}, held: map[string]int64{},
		sets: map[string][]probewire.ProbeSpec{}, offline: map[string]bool{}}
	e.group = e.scalar(`INSERT INTO collector_group (organization_id, name, mode) VALUES ($1, 'wp963', $2) RETURNING id::text`, f.OrgA, mode)
	for i := range n {
		c := e.scalar(`INSERT INTO collector (organization_id, name, status) VALUES ($1, $2, 'active') RETURNING id::text`, f.OrgA,
			fmt.Sprintf("wp963-%d", i))
		e.exec(`UPDATE collector_group_member SET group_id = $1, priority = $2 WHERE collector_id = $3`, e.group, 30-10*i, c)
		e.collectors = append(e.collectors, c)
		sub, serr := nc.SubscribeSync("collector." + f.OrgA + "." + c + discovery.ProbesChangedSuffix)
		if serr != nil {
			t.Fatal(serr)
		}
		e.subs[c] = sub
	}
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}
	notifier := probe.NewNATSAssignmentNotifier(nc)
	e.live = probe.NewLiveness(f.App).WithNotifier(notifier)
	e.assigner = probe.NewAssigner(f.App).WithNotifier(notifier).WithLiveness(e.live)
	repo := probe.NewPGRepository(f.App)
	repo.SetAssignTrigger(syncTrigger{e.assigner})
	// The hook of the production router (skew.OnMemberReturned(
	// pg.CollectorChanged)), recorded.
	e.skew = probe.NewClockSkew(f.App).OnMemberReturned(func(ctx context.Context, orgID, collector string) {
		e.mu.Lock()
		e.returned = append(e.returned, collector)
		e.mu.Unlock()
		repo.CollectorChanged(ctx, orgID, collector)
	})
	check := discovery.NewHandler(discovery.NewPGRepository(f.App), nil).CheckCollectorChannel
	e.dist = probe.NewDistributor(probe.NewPGRepository(f.App), check)
	return e
}

func (e *liveEnv) scalar(sql string, args ...any) string {
	e.t.Helper()
	var out string
	if err := e.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return out
}

func (e *liveEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.f.Admin.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

// probes creates n enabled icmp probes in the group.
func (e *liveEnv) probes(n int) {
	e.t.Helper()
	e.exec(`INSERT INTO probe (organization_id, client_id, collector_group_id, type, target)
		SELECT $1, $2, $3, 'icmp', '10.' || (i / 65536) || '.' || ((i / 256) % 256) || '.' || (i % 256)
		  FROM generate_series(1, $4::int) AS i`, e.f.OrgA, e.f.Client1, e.group, n)
}

// alive sends an alive message of the collector received at at with the
// set version it applied (config_version) and then lets the collectors
// follow the pushes the alive message caused (a return assigns at once).
// It returns the version sent.
func (e *liveEnv) alive(collector string, at time.Time) int64 {
	e.t.Helper()
	v := e.held[collector]
	if err := e.skew.Observe(context.Background(), e.f.OrgA, collector, &probewire.Alive{CollectorTime: at, ConfigVersion: v,
		QueueState: probewire.QueueOK, ICMPCapability: probewire.ICMPUnprivileged, ICMPCapabilityV6: probewire.ICMPUnprivileged}, at); err != nil {
		e.t.Fatal(err)
	}
	e.sync()
	return v
}

// sync lets every connected collector follow its pushes: it reads
// .probes.changed and fetches the announced set. A stored version above
// the applied one must have been pushed.
func (e *liveEnv) sync() {
	e.t.Helper()
	for _, c := range e.collectors {
		if e.offline[c] {
			continue
		}
		stored := e.version(c)
		pushed := int64(-1)
		for {
			wait := 20 * time.Millisecond
			if stored > e.held[c] && pushed < stored {
				wait = 5 * time.Second
			}
			msg, err := e.subs[c].NextMsg(wait)
			if errors.Is(err, nats.ErrTimeout) {
				break
			}
			if err != nil {
				e.t.Fatal(err)
			}
			var ch probewire.ProbesChanged
			if err := json.Unmarshal(msg.Data, &ch); err != nil {
				e.t.Fatal(err)
			}
			pushed = max(pushed, ch.Version)
		}
		if stored > e.held[c] && pushed < stored {
			e.t.Fatalf("collector %s: version %d stored, no .probes.changed (pushed %d)", c, stored, pushed)
		}
		if pushed > e.held[c] {
			e.fetch(c)
		}
	}
}

// fetch gets every page of the current set of the collector, like the
// engine on .probes.request, and applies it.
func (e *liveEnv) fetch(collector string) {
	e.t.Helper()
	var specs []probewire.ProbeSpec
	var version int64
	for page := 1; page != 0; {
		got := e.dist.Answer(context.Background(), e.f.OrgA, collector,
			[]byte(`{"page":`+strconv.Itoa(page)+`,"version":`+strconv.FormatInt(version, 10)+`}`))
		set, ok := got.(probewire.AssignmentSet)
		if !ok {
			e.t.Fatalf("page %d of %s: %#v", page, collector, got)
		}
		if set.VersionChanged {
			specs, version, page = nil, 0, 1
			continue
		}
		version = set.Version
		specs = append(specs, set.Probes...)
		page = set.NextPage
	}
	e.held[collector], e.sets[collector] = version, specs
}

// reconnect ends the cut-off of the collector: it drops the pushes it
// missed and fetches its set.
func (e *liveEnv) reconnect(collector string) {
	e.t.Helper()
	for {
		if _, err := e.subs[collector].NextMsg(20 * time.Millisecond); err != nil {
			break
		}
	}
	e.offline[collector] = false
	e.fetch(collector)
}

func (e *liveEnv) version(collector string) int64 {
	e.t.Helper()
	v, err := strconv.ParseInt(e.scalar(`SELECT config_version::text FROM collector_group_member WHERE collector_id = $1`, collector), 10, 64)
	if err != nil {
		e.t.Fatal(err)
	}
	return v
}

func (e *liveEnv) status(collector string) string {
	e.t.Helper()
	return e.scalar(`SELECT member_status FROM collector_group_member WHERE collector_id = $1`, collector)
}

// resetReturned forgets the recorded returns (the first alive message of
// every member counts as one).
func (e *liveEnv) resetReturned() {
	e.mu.Lock()
	e.returned = nil
	e.mu.Unlock()
}

// tick is one run of the system job probe.assign at now (Assigner.Tick:
// the liveness sweep, then the assignment of every group); the collectors
// follow the pushes.
func (e *liveEnv) tick(now time.Time) {
	e.t.Helper()
	if _, err := e.assigner.Tick(context.Background(), now); err != nil {
		e.t.Fatal(err)
	}
	e.sync()
}

func (e *liveEnv) spec(collector, probeID string) *probewire.ProbeSpec {
	for i := range e.sets[collector] {
		if e.sets[collector][i].ID == probeID {
			return &e.sets[collector][i]
		}
	}
	return nil
}

// assignments maps every assigned probe of the group to its collector.
func (e *liveEnv) assignments() map[string]string {
	e.t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT a.probe_id::text, a.collector_id::text FROM probe_assignment a
		JOIN probe p ON p.id = a.probe_id WHERE p.collector_group_id = $1`, e.group)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var p, c string
		if err := rows.Scan(&p, &c); err != nil {
			e.t.Fatal(err)
		}
		out[p] = c
	}
	return out
}

func count(m map[string]string, collector string) int {
	n := 0
	for _, c := range m {
		if c == collector {
			n++
		}
	}
	return n
}

// failovers returns the data of the collector_group.failover events of the
// group, oldest first.
func (e *liveEnv) failovers() []map[string]any {
	e.t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT data FROM event_outbox
		WHERE organization_id = $1 AND type = 'collector_group.failover' AND entity_id = $2 ORDER BY sequence`, e.f.OrgA, e.group)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			e.t.Fatal(err)
		}
		var d map[string]any
		if err := json.Unmarshal(raw, &d); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}

// TestFailoverWithin60s covers AK1–AK4: a group of three members with 3,000
// probes; A stops its alive messages. With the default timeout of 30 s, a
// tick of 5 s and alive messages every 10 s the time from A's last alive
// message to the alive message of B that confirms its new assignment set
// is at most 60 s: the sweep in Assigner.Tick sets A down and assigns the
// group, the notifier pushes .probes.changed over NATS, B fetches the set
// and reports its version as config_version. The first check of a probe B
// took over follows at most one interval later in its unchanged phase
// (WP-951). Exactly one collector_group.failover names A and the probes
// that moved; A keeps no assignment.
func TestFailoverWithin60s(t *testing.T) {
	e := newLiveEnv(t, probe.ModeDistributed, 3)
	e.probes(3000)
	a, b := e.collectors[0], e.collectors[1]
	start := time.Now().UTC().Truncate(time.Second)
	for _, c := range e.collectors {
		e.alive(c, start)
	}
	e.tick(start)
	before := e.assignments()
	if len(before) != 3000 {
		t.Fatalf("%d assignments, want 3000", len(before))
	}
	onA := count(before, a)
	if onA == 0 {
		t.Fatal("A has no probes")
	}
	if len(e.sets[a]) != onA {
		t.Fatalf("A holds %d probes, %d assigned", len(e.sets[a]), onA)
	}
	lastA := start.Add(20 * time.Second)
	var versionAfter int64 = -1
	var confirmed time.Time
	for at := start; at.Before(start.Add(2 * time.Minute)); at = at.Add(probe.DefaultAssignTick) {
		if at.Sub(start)%(10*time.Second) == 0 {
			for _, c := range e.collectors {
				if c == a && at.After(lastA) {
					continue
				}
				v := e.alive(c, at)
				if c == b && versionAfter >= 0 && v >= versionAfter && confirmed.IsZero() {
					confirmed = at
				}
			}
		}
		e.tick(at)
		if versionAfter < 0 && e.status(a) == probe.MemberDown {
			versionAfter = e.version(b)
			if d := at.Sub(lastA); d <= probe.DefaultLivenessTimeout {
				t.Errorf("A down %s after its last alive message, want after the timeout", d)
			}
		}
	}
	if confirmed.IsZero() {
		t.Fatal("B never confirmed its new assignment set")
	}
	if d := confirmed.Sub(lastA); d > 60*time.Second {
		t.Errorf("takeover confirmed %s after A's last alive message, want at most 60 s", d)
	}
	after := e.assignments()
	if len(after) != 3000 || count(after, a) != 0 {
		t.Errorf("%d assignments, %d on A; want 3000 and none on the failed member", len(after), count(after, a))
	}
	if len(e.sets[b]) != count(after, b) {
		t.Errorf("B holds %d probes, %d assigned", len(e.sets[b]), count(after, b))
	}
	tookOver := 0
	for p, c := range before {
		if c != a && after[p] != c {
			t.Errorf("probe %s of a remaining member moved %s -> %s", p, c, after[p])
		}
		if c != a || after[p] != b {
			continue
		}
		// AK4: the first check of B in the unchanged phase (WP-951).
		tookOver++
		spec := e.spec(b, p)
		if spec == nil || spec.IntervalMS <= 0 {
			t.Fatalf("probe %s taken over by B missing from its set: %+v", p, spec)
		}
		interval := time.Duration(spec.IntervalMS) * time.Millisecond
		first := cprobe.NextStart(p, spec.IntervalMS, confirmed)
		if first.After(confirmed.Add(interval)) {
			t.Errorf("probe %s: first check of B at %s, more than one interval after the takeover %s", p, first, confirmed)
		}
		if ph := first.UnixMilli() % spec.IntervalMS; ph != cprobe.Phase(p, spec.IntervalMS) {
			t.Errorf("probe %s: first check of B in phase %d, want %d", p, ph, cprobe.Phase(p, spec.IntervalMS))
		}
	}
	if tookOver == 0 {
		t.Error("B took over no probe of A")
	}
	got := e.failovers()
	if len(got) != 1 {
		t.Fatalf("%d collector_group.failover events, want exactly one: %v", len(got), got)
	}
	if got[0]["failed_collector_id"] != a || got[0]["collector_group_id"] != e.group || got[0]["moved_probes"] != float64(onA) {
		t.Errorf("failover data %v, want A and %d moved probes", got[0], onA)
	}
	if e.status(b) != probe.MemberHealthy || e.status(e.collectors[2]) != probe.MemberHealthy {
		t.Error("a member with alive messages is not healthy")
	}
}

// TestMemberWithoutAliveTakesNoProbes: a member that never sent a
// monitoring alive message (not started, without the probe engine) is not
// assignable although its member_status is the default healthy, and the
// sweep leaves it alone; its first alive message assigns the group at once
// (the hook of the router) and it takes its rendezvous share.
func TestMemberWithoutAliveTakesNoProbes(t *testing.T) {
	e := newLiveEnv(t, probe.ModeDistributed, 2)
	e.probes(200)
	a, c := e.collectors[0], e.collectors[1]
	start := time.Now().UTC().Truncate(time.Second)
	e.alive(a, start)
	e.tick(start)
	if n := count(e.assignments(), a); n != 200 {
		t.Fatalf("%d probes on the member with alive messages, want all 200", n)
	}
	later := start.Add(5 * time.Minute)
	e.alive(a, later)
	e.tick(later)
	if s := e.status(c); s != probe.MemberHealthy {
		t.Errorf("silent member never seen is %s after the sweep, want its status unchanged", s)
	}
	if n := count(e.assignments(), c); n != 0 {
		t.Fatalf("member without alive message has %d probes, want none", n)
	}
	e.resetReturned()
	at := later.Add(10 * time.Second)
	e.alive(c, at)
	e.mu.Lock()
	returned := append([]string(nil), e.returned...)
	e.mu.Unlock()
	if len(returned) != 1 || returned[0] != c {
		t.Errorf("member returned hook %v, want once for the new member", returned)
	}
	if n := count(e.assignments(), c); n == 0 || n == 200 {
		t.Errorf("%d probes on the member after its first alive message, want its rendezvous share", n)
	}
	if len(e.sets[c]) != count(e.assignments(), c) {
		t.Errorf("member holds %d probes after the push, %d assigned", len(e.sets[c]), count(e.assignments(), c))
	}
}

// TestLivenessIndependentOfCollectorStatus covers AK1: the monitoring
// liveness does not follow the COL-05 status. A member whose collector is
// offline (heartbeat) but that sends alive messages stays healthy and keeps
// its probes; a member whose collector is active but that is silent goes
// down.
func TestLivenessIndependentOfCollectorStatus(t *testing.T) {
	e := newLiveEnv(t, probe.ModeDistributed, 2)
	e.probes(100)
	a, b := e.collectors[0], e.collectors[1]
	start := time.Now().UTC().Truncate(time.Second)
	e.alive(a, start)
	e.alive(b, start)
	e.tick(start)
	onA := count(e.assignments(), a)
	if onA == 0 {
		t.Fatal("A has no probes")
	}
	e.exec(`UPDATE collector SET status = 'offline' WHERE id = $1`, a)
	at := start.Add(40 * time.Second)
	e.alive(a, at)
	e.tick(at)
	if s := e.status(a); s != probe.MemberHealthy {
		t.Errorf("member of an offline collector with alive messages is %s, want healthy", s)
	}
	if n := count(e.assignments(), a); n != 100 {
		t.Errorf("%d probes on A, want all 100 (B, active but silent, failed over)", n)
	}
	if s := e.status(b); s != probe.MemberDown {
		t.Errorf("silent member of an active collector is %s, want down", s)
	}
}

// TestSplitBrainStaleEpoch covers AK5: A is cut off from the server and
// goes on checking. After the failover its buffered change with the old
// epoch lands in the history only (stale, no event); probe_state comes from
// B alone, and A's assignment set no longer holds the probe.
func TestSplitBrainStaleEpoch(t *testing.T) {
	e := newLiveEnv(t, probe.ModeFailover, 2)
	e.probes(1)
	a, b := e.collectors[0], e.collectors[1]
	id := e.scalar(`SELECT id::text FROM probe WHERE collector_group_id = $1`, e.group)
	start := time.Now().UTC().Truncate(time.Second)
	e.alive(a, start)
	e.alive(b, start)
	e.tick(start)
	var epochA int64
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT epoch FROM probe_assignment WHERE probe_id = $1 AND collector_id = $2`, id, a).
		Scan(&epochA); err != nil {
		t.Fatalf("probe not on A (priority 30): %v", err)
	}
	applier := probe.NewStateApplier(nil, probe.StreamConfig{}, e.f.App)
	if err := applier.Apply(context.Background(), e.f.OrgA, a, &probewire.StateChange{ProbeID: id, Epoch: epochA, Seq: 1,
		From: probewire.StateUnknown, To: probewire.StateUp, Reason: "ok", ObservedAt: start}, start); err != nil {
		t.Fatal(err)
	}
	// A is silent for the server (cut off: no push, no fetch); B goes on.
	e.offline[a] = true
	for at := start.Add(10 * time.Second); at.Before(start.Add(50 * time.Second)); at = at.Add(10 * time.Second) {
		e.alive(b, at)
		e.tick(at)
	}
	if s := e.status(a); s != probe.MemberDown {
		t.Fatalf("A is %s, want down", s)
	}
	var epochB int64
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT epoch FROM probe_assignment WHERE probe_id = $1 AND collector_id = $2`, id, b).
		Scan(&epochB); err != nil || epochB <= epochA {
		t.Fatalf("probe not on B with a higher epoch: %d after %d (%v)", epochB, epochA, err)
	}
	// B takes over; A reconnects and delivers its buffered change.
	at := start.Add(50 * time.Second)
	if err := applier.Apply(context.Background(), e.f.OrgA, b, &probewire.StateChange{ProbeID: id, Epoch: epochB, Seq: 1,
		From: probewire.StateUnknown, To: probewire.StateUp, Reason: "ok", ObservedAt: at}, at); err != nil {
		t.Fatal(err)
	}
	if err := applier.Apply(context.Background(), e.f.OrgA, a, &probewire.StateChange{ProbeID: id, Epoch: epochA, Seq: 2,
		From: probewire.StateUp, To: probewire.StateDown, Reason: "timeout", ObservedAt: start.Add(40 * time.Second)}, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := e.scalar(`SELECT stale::text || ':' || notify::text FROM probe_state_change WHERE probe_id = $1 AND collector_id = $2 AND seq = 2`,
		id, a); got != "true:false" {
		t.Errorf("A's change with the old epoch: stale:notify = %s, want true:false", got)
	}
	if got := e.scalar(`SELECT state || ':' || collector_id::text || ':' || epoch::text FROM probe_state WHERE probe_id = $1`, id); got !=
		"up:"+b+":"+strconv.FormatInt(epochB, 10) {
		t.Errorf("probe_state %s, want up from B with epoch %d", got, epochB)
	}
	if n := e.scalar(`SELECT count(*)::text FROM event_outbox WHERE organization_id = $1 AND type = 'probe.state_changed' AND entity_id = $2
		AND data->>'collector_id' = $3 AND data->>'to' = 'down'`, e.f.OrgA, id, a); n != "0" {
		t.Errorf("%s probe.state_changed of A's stale change, want none", n)
	}
	// A's buffered result with the old epoch: stored as history only
	// (stale_epoch), no effect on probe_state.
	out, err := probe.NewPGIngestStore(e.f.App).WriteResults(context.Background(), e.f.OrgA, []probe.ResultsMessage{{
		CollectorID: a, Results: []probewire.Result{{ProbeID: id, Epoch: epochA, Seq: 3, ObservedAt: start.Add(45 * time.Second),
			Status: probewire.StatusFail}}}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Stale != 1 {
		t.Errorf("ingest of A's buffered result %+v, want it stale", out)
	}
	if got := e.scalar(`SELECT stale_epoch::text FROM probe_result WHERE probe_id = $1 AND collector_id = $2 AND seq = 3`, id, a); got != "true" {
		t.Errorf("A's result with the old epoch: stale_epoch = %s, want true", got)
	}
	if got := e.scalar(`SELECT state || ':' || collector_id::text FROM probe_state WHERE probe_id = $1`, id); got != "up:"+b {
		t.Errorf("probe_state %s after A's stale result, want up from B", got)
	}
	if n := e.scalar(`SELECT count(*)::text FROM probe_assignment WHERE collector_id = $1`, a); n != "0" {
		t.Errorf("A still has %s assignments; its set must no longer hold the probe", n)
	}
	// A reconnects and takes over the new set: the probe is gone from it,
	// A checks it no more.
	if e.spec(a, id) == nil {
		t.Fatal("A did not hold the probe before the cut-off")
	}
	e.reconnect(a)
	if e.spec(a, id) != nil {
		t.Errorf("A's set after the reconnect still holds the probe: %+v", e.sets[a])
	}
	if got := e.failovers(); len(got) != 1 || got[0]["moved_probes"] != float64(1) {
		t.Errorf("failover events %v, want one with one moved probe", got)
	}
}

// TestMemberReturnMinimalMovement covers AK1 and AK6: A goes down and comes
// back. It is assignable only with its second alive message within one
// timeout; the return assigns the group at once through the hook of the
// router (PGRepository.CollectorChanged), without waiting for a tick; in mode
// distributed exactly the probes whose highest hash falls on A move back,
// the others stay; the return writes no failover event.
func TestMemberReturnMinimalMovement(t *testing.T) {
	e := newLiveEnv(t, probe.ModeDistributed, 3)
	e.probes(300)
	a := e.collectors[0]
	start := time.Now().UTC().Truncate(time.Second)
	for _, c := range e.collectors {
		e.alive(c, start)
	}
	e.tick(start)
	e.resetReturned()
	at := start.Add(40 * time.Second)
	for _, c := range e.collectors[1:] {
		e.alive(c, at)
	}
	e.tick(at)
	if s := e.status(a); s != probe.MemberDown {
		t.Fatalf("A is %s, want down", s)
	}
	down := e.assignments()
	if count(down, a) != 0 {
		t.Fatal("A kept probes while down")
	}
	// The first alive message after the outage: still down.
	at = at.Add(10 * time.Second)
	e.alive(a, at)
	if s := e.status(a); s != probe.MemberDown {
		t.Fatalf("A is %s after its first alive message, want still down", s)
	}
	e.tick(at)
	if count(e.assignments(), a) != 0 {
		t.Fatal("A got probes after its first alive message")
	}
	// The second one within the timeout: healthy, the hook fires.
	at = at.Add(10 * time.Second)
	for _, c := range e.collectors {
		e.alive(c, at)
	}
	if s := e.status(a); s != probe.MemberHealthy {
		t.Fatalf("A is %s after its second alive message, want healthy", s)
	}
	e.mu.Lock()
	returned := append([]string(nil), e.returned...)
	e.mu.Unlock()
	if len(returned) != 1 || returned[0] != a {
		t.Errorf("member returned hook %v, want once for A", returned)
	}
	// No tick: the return assigned the group at once (CollectorChanged).
	back := e.assignments()
	members := make([]probe.Candidate, 0, len(e.collectors))
	for _, c := range e.collectors {
		members = append(members, probe.Candidate{CollectorID: c, Weight: 100, Healthy: true})
	}
	var probes []probe.Probe
	for id := range back {
		probes = append(probes, probe.Probe{ID: id, Enabled: true, Type: "icmp", Target: "10.0.0.1"})
	}
	want := map[string]string{}
	for _, x := range probe.Assign(&probe.CollectorGroup{Mode: probe.ModeDistributed}, members, probes) {
		want[x.ProbeID] = x.CollectorID
	}
	moved := 0
	for id, c := range back {
		if c != want[id] {
			t.Errorf("probe %s on %s, want %s", id, c, want[id])
		}
		if c != down[id] {
			moved++
			if c != a {
				t.Errorf("probe %s moved to %s on A's return, want only moves to A", id, c)
			}
		}
	}
	if moved == 0 || moved != count(back, a) {
		t.Errorf("%d probes moved, %d on A; want exactly A's probes", moved, count(back, a))
	}
	if got := e.failovers(); len(got) != 1 {
		t.Errorf("%d failover events, want only the one of the outage", len(got))
	}
}

// TestFailoverModePriority covers AK6 (E-174): in mode failover all probes
// run on the healthy member with the highest priority; it fails, the next
// takes all of them, and the return of the first takes them back.
func TestFailoverModePriority(t *testing.T) {
	e := newLiveEnv(t, probe.ModeFailover, 3)
	e.probes(50)
	a, b, c := e.collectors[0], e.collectors[1], e.collectors[2]
	start := time.Now().UTC().Truncate(time.Second)
	for _, m := range e.collectors {
		e.alive(m, start)
	}
	e.tick(start)
	if n := count(e.assignments(), a); n != 50 {
		t.Fatalf("%d probes on A (priority 30), want 50", n)
	}
	at := start.Add(35 * time.Second)
	e.alive(b, at)
	e.alive(c, at)
	e.tick(at)
	if n := count(e.assignments(), b); n != 50 {
		t.Fatalf("%d probes on B after A failed, want all 50 on the next priority", n)
	}
	got := e.failovers()
	if len(got) != 1 || got[0]["failed_collector_id"] != a || got[0]["moved_probes"] != float64(50) {
		t.Fatalf("failover events %v, want one for A with 50 probes", got)
	}
	// B fails as well: all go to C.
	at = at.Add(35 * time.Second)
	e.alive(c, at)
	e.tick(at)
	if n := count(e.assignments(), c); n != 50 {
		t.Fatalf("%d probes on C, want 50", n)
	}
	// A comes back with two alive messages: all probes return to it.
	for range 2 {
		at = at.Add(10 * time.Second)
		e.alive(a, at)
		e.alive(c, at)
	}
	e.tick(at)
	if n := count(e.assignments(), a); n != 50 {
		t.Errorf("%d probes on A after its return, want all 50", n)
	}
	if got := e.failovers(); len(got) != 2 {
		t.Errorf("%d failover events, want two (A and B)", len(got))
	}
}

// TestFailoverWithoutHealthyMemberKeepsAssignments: when the last healthy
// member goes down nothing moves and no failover event is written (network
// down, WP-964).
func TestFailoverWithoutHealthyMemberKeepsAssignments(t *testing.T) {
	e := newLiveEnv(t, probe.ModeDistributed, 1)
	e.probes(5)
	start := time.Now().UTC().Truncate(time.Second)
	e.alive(e.collectors[0], start)
	e.tick(start)
	failed, err := e.live.SweepOrg(context.Background(), e.f.OrgA, start.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(failed) != 1 || failed[0].TakenOver {
		t.Errorf("sweep %+v, want one member down without takeover", failed)
	}
	if n := count(e.assignments(), e.collectors[0]); n != 5 {
		t.Errorf("%d assignments kept, want 5", n)
	}
	if got := e.failovers(); len(got) != 0 {
		t.Errorf("failover events %v without a remaining member, want none", got)
	}
}
