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

	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// The adoption of the assignment set (PRB-11, WP-956) against a NATS server
// that answers .probes.request like the platform (backend probe.Distributor).

// fakeProbeServer serves the assignment set of one collector.
type fakeProbeServer struct {
	nc      *nats.Conn
	subject string

	mu       sync.Mutex
	version  int64
	probes   []probewire.ProbeSpec
	schema   string // schema_version of the pages ("" = probewire.SchemaVersion)
	requests []probewire.AssignmentRequest
	// afterPage, when set, runs after the page of that number was served
	// (with the lock held).
	afterPage map[int]func(s *fakeProbeServer)
}

func newFakeProbeServer(t *testing.T) *fakeProbeServer {
	t.Helper()
	nc, err := nats.Connect(auditNATSURL(t), nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	s := &fakeProbeServer{nc: nc, subject: fmt.Sprintf("collector.org-%d.coll-1", time.Now().UnixNano()),
		afterPage: map[int]func(*fakeProbeServer){}}
	sub, err := nc.Subscribe(s.subject+".probes.request", s.answer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}
	return s
}

func (s *fakeProbeServer) answer(m *nats.Msg) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var req probewire.AssignmentRequest
	_ = json.Unmarshal(m.Data, &req)
	if req.Page == 0 {
		req.Page = 1
	}
	s.requests = append(s.requests, req)
	var out any
	switch {
	case req.IfNoneMatch != 0 && req.IfNoneMatch == s.version:
		out = probewire.NotModified{NotModified: true, Version: s.version}
	case req.Page > 1 && req.Version != s.version:
		out = probewire.AssignmentSet{Header: probewire.Header{SchemaVersion: probewire.SchemaVersion}, Version: s.version,
			Page: req.Page, NextPage: 1, VersionChanged: true, Probes: []probewire.ProbeSpec{}}
	default:
		pages, err := probewire.Paginate("coll-1", s.version, s.probes)
		if err != nil || req.Page > len(pages) {
			out = map[string]any{"status": 404, "error": "page not found"}
			break
		}
		p := pages[req.Page-1]
		if s.schema != "" {
			p.SchemaVersion = s.schema
		}
		out = p
	}
	raw, _ := json.Marshal(out)
	_ = m.Respond(raw)
	if f := s.afterPage[req.Page]; f != nil {
		delete(s.afterPage, req.Page)
		f(s)
	}
}

func (s *fakeProbeServer) set(version int64, probes []probewire.ProbeSpec) {
	s.mu.Lock()
	s.version, s.probes = version, probes
	s.mu.Unlock()
}

func (s *fakeProbeServer) requestLog() []probewire.AssignmentRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]probewire.AssignmentRequest(nil), s.requests...)
}

// pushChanged sends .probes.changed like the platform's notifier.
func (s *fakeProbeServer) pushChanged(t *testing.T, version int64) {
	t.Helper()
	raw, _ := json.Marshal(probewire.ProbesChanged{Version: version})
	if err := s.nc.Publish(s.subject+".probes.changed", raw); err != nil {
		t.Fatal(err)
	}
	if err := s.nc.Flush(); err != nil {
		t.Fatal(err)
	}
}

// fakeApplier records the sets handed to the engine.
type fakeApplier struct {
	mu   sync.Mutex
	sets []applied
}

type applied struct {
	version int64
	probes  []probewire.ProbeSpec
}

func (f *fakeApplier) Apply(version int64, probes []probewire.ProbeSpec) probe.ApplyStats {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets = append(f.sets, applied{version: version, probes: append([]probewire.ProbeSpec(nil), probes...)})
	return probe.ApplyStats{Added: len(probes)}
}

func (f *fakeApplier) all() []applied {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]applied(nil), f.sets...)
}

func (f *fakeApplier) last() (applied, bool) {
	all := f.all()
	if len(all) == 0 {
		return applied{}, false
	}
	return all[len(all)-1], true
}

func icmpSpecs(n int, epoch int64) []probewire.ProbeSpec {
	out := make([]probewire.ProbeSpec, n)
	for i := range out {
		out[i] = probewire.ProbeSpec{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i), Type: "icmp",
			Target: fmt.Sprintf("192.0.2.%d", i%250+1), IntervalMS: 30000, TimeoutMS: 10000, DownCount: 5, UpCount: 1, Epoch: epoch}
	}
	return out
}

// startSync runs syncProbes for a until the test ends.
func startSync(t *testing.T, s *fakeProbeServer, a *probeAssignment) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); syncProbes(ctx, s.nc, s.subject, a) }()
	t.Cleanup(func() { cancel(); <-done })
}

func newAssignment(t *testing.T, engine probeApplier, dir string) (*probeAssignment, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	a := &probeAssignment{logger: slog.New(slog.NewTextHandler(&syncWriter{w: &buf}, nil))}
	a.attach(engine, dir, nil)
	return a, &buf
}

// syncWriter serializes the writes of the logger for the test's reads.
type syncWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

// TestProbesFetchedOnConnect covers AK1: the session fetches the set at
// once, without waiting for the periodic fetch, and applies it.
func TestProbesFetchedOnConnect(t *testing.T) {
	s := newFakeProbeServer(t)
	s.set(3, icmpSpecs(2, 1))
	f := &fakeApplier{}
	a, _ := newAssignment(t, f, t.TempDir())
	startSync(t, s, a)
	waitFor(t, 5*time.Second, "the set on connect", func() bool { _, ok := f.last(); return ok })
	got, _ := f.last()
	if got.version != 3 || len(got.probes) != 2 {
		t.Fatalf("applied version %d with %d probes, want 3 with 2", got.version, len(got.probes))
	}
	if req := s.requestLog()[0]; req.IfNoneMatch != 0 || req.Page != 1 {
		t.Fatalf("first request %+v, want page 1 without if_none_match", req)
	}
	if a.version() != 3 {
		t.Fatalf("active version %d", a.version())
	}
}

// TestProbesRefetchedOnChangedPush covers AK1: a .probes.changed push
// leads to a fetch with the held version and to the new set.
func TestProbesRefetchedOnChangedPush(t *testing.T) {
	s := newFakeProbeServer(t)
	s.set(1, icmpSpecs(1, 1))
	f := &fakeApplier{}
	a, _ := newAssignment(t, f, t.TempDir())
	startSync(t, s, a)
	waitFor(t, 5*time.Second, "the first set", func() bool { return a.version() == 1 })
	s.set(2, icmpSpecs(3, 1))
	s.pushChanged(t, 2)
	waitFor(t, 5*time.Second, "the set after the push", func() bool { return a.version() == 2 })
	got, _ := f.last()
	if len(got.probes) != 3 {
		t.Fatalf("applied %d probes, want 3", len(got.probes))
	}
	reqs := s.requestLog()
	if reqs[1].IfNoneMatch != 1 {
		t.Fatalf("request after the push %+v, want if_none_match 1", reqs[1])
	}
}

// TestProbesNotModifiedKeepsSet covers AK1: not_modified keeps the active
// set; the periodic fetch sends the held version.
func TestProbesNotModifiedKeepsSet(t *testing.T) {
	old := probesRefresh
	probesRefresh = 50 * time.Millisecond
	t.Cleanup(func() { probesRefresh = old })
	s := newFakeProbeServer(t)
	s.set(5, icmpSpecs(2, 4))
	f := &fakeApplier{}
	a, _ := newAssignment(t, f, t.TempDir())
	startSync(t, s, a)
	waitFor(t, 5*time.Second, "three fetches", func() bool { return len(s.requestLog()) >= 3 })
	if n := len(f.all()); n != 1 {
		t.Fatalf("set applied %d times, want once", n)
	}
	for _, req := range s.requestLog()[1:] {
		if req.IfNoneMatch != 5 {
			t.Fatalf("periodic request %+v, want if_none_match 5", req)
		}
	}
	if a.version() != 5 {
		t.Fatalf("active version %d, want 5", a.version())
	}
}

// TestProbesReadsAllPagesAndRestartsOnVersionChanged covers AK1: the
// collector reads every page (2,000 probes per page) and applies the set
// only after the last one; a version change while it reads discards the
// pages and starts again with page 1.
func TestProbesReadsAllPagesAndRestartsOnVersionChanged(t *testing.T) {
	s := newFakeProbeServer(t)
	s.set(7, icmpSpecs(4500, 1))
	// After page 1 of version 7 the set changes to version 8.
	s.afterPage[1] = func(s *fakeProbeServer) { s.version, s.probes = 8, icmpSpecs(4100, 2) }
	f := &fakeApplier{}
	a, _ := newAssignment(t, f, t.TempDir())
	refreshProbes(context.Background(), s.nc, s.subject, a)
	sets := f.all()
	if len(sets) != 1 {
		t.Fatalf("%d sets applied, want 1 (none of the outdated version)", len(sets))
	}
	if sets[0].version != 8 || len(sets[0].probes) != 4100 || sets[0].probes[4099].Epoch != 2 {
		t.Fatalf("applied version %d with %d probes, want version 8 with 4100", sets[0].version, len(sets[0].probes))
	}
	seen := map[string]bool{}
	for _, p := range sets[0].probes {
		if seen[p.ID] {
			t.Fatalf("probe %s twice in the set", p.ID)
		}
		seen[p.ID] = true
	}
	reqs := s.requestLog()
	// page 1 (v7), page 2 → version_changed, then pages 1–3 of version 8.
	want := []probewire.AssignmentRequest{{Page: 1}, {Page: 2, Version: 7}, {Page: 1}, {Page: 2, Version: 8}, {Page: 3, Version: 8}}
	if len(reqs) != len(want) {
		t.Fatalf("requests %+v, want %+v", reqs, want)
	}
	for i := range want {
		if reqs[i] != want[i] {
			t.Fatalf("request %d = %+v, want %+v", i, reqs[i], want[i])
		}
	}
}

// TestRestartWithoutServerUsesSavedSet covers AK2: the set is saved
// atomically below the spool directory; a start without the server applies
// it with its epochs, and the results of the checks carry them.
func TestRestartWithoutServerUsesSavedSet(t *testing.T) {
	dir := t.TempDir()
	s := newFakeProbeServer(t)
	specs := icmpSpecs(2, 9)
	specs[1].Epoch = 11
	s.set(4, specs)
	first, _ := newAssignment(t, &fakeApplier{}, dir)
	refreshProbes(context.Background(), s.nc, s.subject, first)
	if first.version() != 4 {
		t.Fatalf("version %d after the fetch", first.version())
	}
	if _, err := os.Stat(filepath.Join(dir, probesFile)); err != nil {
		t.Fatalf("set not saved: %v", err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".tmp-probes-*"))
	if len(leftovers) != 0 {
		t.Fatalf("temporary files left: %v", leftovers)
	}

	// Restart: a new engine, no server.
	epochs := sync.Map{}
	run := func(_ context.Context, spec *probewire.ProbeSpec, _ map[string]string) probewire.Result {
		rtt := 1.0
		epochs.Store(spec.ID, spec.Epoch)
		return probewire.Result{ProbeID: spec.ID, Epoch: spec.Epoch, ObservedAt: time.Now().UTC(),
			Status: probewire.StatusOK, RTTMS: &rtt}
	}
	noEdge := func(context.Context, string, []byte) ([]byte, error) { return nil, probe.ErrNoEdge }
	engine, err := probe.NewEngine(&probe.EngineConfig{SpoolDir: dir, Send: noEdge, Run: run})
	if err != nil {
		t.Fatal(err)
	}
	second, logged := newAssignment(t, engine, dir)
	if second.version() != 4 || engine.Probes() != 2 || engine.ConfigVersion() != 4 {
		t.Fatalf("restart: version %d, engine %d probes at %d", second.version(), engine.Probes(), engine.ConfigVersion())
	}
	if !strings.Contains(logged.String(), "collector.probes.restored") {
		t.Errorf("restore not logged: %s", logged.String())
	}
	// One interval of checks, inline: every result carries the saved epoch.
	engine.Scheduler.Go = func(f func()) { f() }
	start := time.Now()
	engine.Scheduler.Advance(start)
	engine.Scheduler.Advance(start.Add(31 * time.Second))
	if err = engine.Outbox.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	msgs, err := engine.Outbox.Results().PeekBatch(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for i := range msgs {
		batch, _, derr := probe.DecodeBundle(&msgs[i])
		if derr != nil {
			t.Fatal(derr)
		}
		for _, r := range batch.Results {
			got[r.ProbeID] = r.Epoch
		}
	}
	for i := range specs {
		seen, _ := epochs.Load(specs[i].ID)
		if got[specs[i].ID] != specs[i].Epoch || seen != specs[i].Epoch {
			t.Fatalf("result of %s carries epoch %d (check saw %v), want %d", specs[i].ID, got[specs[i].ID], seen, specs[i].Epoch)
		}
	}
}

// TestDiffRemovesProbeAndStopsResults covers AK3 and AK4: a probe missing
// from the new set is removed from the scheduler, its running check is
// cancelled and not reported, and it is not started again; a changed epoch
// takes effect with the next check and keeps the counters of the state
// machine.
func TestDiffRemovesProbeAndStopsResults(t *testing.T) {
	dir := t.TempDir()
	specs := icmpSpecs(2, 1)
	for i := range specs {
		specs[i].IntervalMS = 200
		specs[i].TimeoutMS = 100
	}
	keep, gone := specs[0].ID, specs[1].ID
	var mu sync.Mutex
	calls := map[string]int{}
	cancelled := make(chan struct{})
	var blockOnce sync.Once
	block := make(chan struct{})
	run := func(ctx context.Context, spec *probewire.ProbeSpec, _ map[string]string) probewire.Result {
		mu.Lock()
		calls[spec.ID]++
		mu.Unlock()
		if spec.ID == gone {
			blockOnce.Do(func() { close(block) })
			<-ctx.Done() // the check runs until the diff cancels it
			select {
			case <-cancelled:
			default:
				close(cancelled)
			}
			return probewire.Result{ProbeID: spec.ID, Epoch: spec.Epoch, ObservedAt: time.Now().UTC(),
				Status: probewire.StatusFail, Code: probewire.ReasonTimeout}
		}
		rtt := 1.0
		return probewire.Result{ProbeID: spec.ID, Epoch: spec.Epoch, ObservedAt: time.Now().UTC(),
			Status: probewire.StatusOK, RTTMS: &rtt}
	}
	noEdge := func(context.Context, string, []byte) ([]byte, error) { return nil, probe.ErrNoEdge }
	engine, err := probe.NewEngine(&probe.EngineConfig{SpoolDir: dir, Send: noEdge, Run: run})
	if err != nil {
		t.Fatal(err)
	}
	sink := engine.Scheduler.Sink
	var goneAfterDiff atomic.Int64
	var diffDone atomic.Bool
	engine.Scheduler.Sink = func(r probewire.Result) {
		if r.ProbeID == gone && diffDone.Load() {
			goneAfterDiff.Add(1)
		}
		sink(r)
	}
	s := newFakeProbeServer(t)
	s.set(1, specs)
	a, _ := newAssignment(t, engine, dir)
	refreshProbes(context.Background(), s.nc, s.subject, a)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); engine.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case <-block:
	case <-time.After(5 * time.Second):
		t.Fatal("the check of the removed probe did not start")
	}
	waitFor(t, 5*time.Second, "a result of the kept probe", func() bool {
		_, ok := engine.States.Counters(keep)
		return ok
	})
	before, _ := engine.States.Counters(keep)

	// Version 2: the second probe is gone, the first has a new epoch.
	next := []probewire.ProbeSpec{specs[0]}
	next[0].Epoch = 2
	s.set(2, next)
	diffDone.Store(true)
	refreshProbes(context.Background(), s.nc, s.subject, a)
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the running check of the removed probe was not cancelled")
	}
	mu.Lock()
	goneCalls := calls[gone]
	mu.Unlock()
	time.Sleep(600 * time.Millisecond)
	mu.Lock()
	after := calls[gone]
	mu.Unlock()
	if after != goneCalls {
		t.Fatalf("removed probe started %d more times", after-goneCalls)
	}
	if n := goneAfterDiff.Load(); n != 0 {
		t.Fatalf("%d results of the removed probe reported after the diff", n)
	}
	if engine.Probes() != 1 || engine.Scheduler.Len() != 1 || engine.ConfigVersion() != 2 {
		t.Fatalf("engine %d probes, scheduler %d, version %d", engine.Probes(), engine.Scheduler.Len(), engine.ConfigVersion())
	}
	if _, ok := engine.States.Counters(gone); ok {
		t.Error("counters of the removed probe kept")
	}
	// The kept probe reports under its new epoch; its counters stayed.
	waitFor(t, 5*time.Second, "a result under the new epoch", func() bool {
		_ = engine.Outbox.Flush(context.Background())
		msgs, perr := engine.Outbox.Results().PeekBatch(context.Background(), 1000)
		if perr != nil {
			return false
		}
		for i := range msgs {
			batch, _, derr := probe.DecodeBundle(&msgs[i])
			if derr != nil {
				continue
			}
			for _, r := range batch.Results {
				if r.ProbeID == keep && r.Epoch == 2 {
					return true
				}
			}
		}
		return false
	})
	cur, ok := engine.States.Counters(keep)
	if !ok || cur.State != before.State || cur.ConsecutiveOK <= before.ConsecutiveOK {
		t.Fatalf("counters of the kept probe %+v, before %+v: not kept across the epoch change", cur, before)
	}
}

// TestUnknownMajorVersionKeepsOldSet covers AK5: a set of an unknown major
// schema version is refused and logged; the active set, the saved one and
// the engine stay.
func TestUnknownMajorVersionKeepsOldSet(t *testing.T) {
	dir := t.TempDir()
	s := newFakeProbeServer(t)
	s.set(1, icmpSpecs(2, 1))
	f := &fakeApplier{}
	a, logged := newAssignment(t, f, dir)
	refreshProbes(context.Background(), s.nc, s.subject, a)
	saved, err := os.ReadFile(filepath.Join(dir, probesFile))
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.version, s.probes, s.schema = 2, icmpSpecs(5, 1), "2.0"
	s.mu.Unlock()
	refreshProbes(context.Background(), s.nc, s.subject, a)
	if a.version() != 1 || len(f.all()) != 1 {
		t.Fatalf("active version %d after %d applies, want version 1 applied once", a.version(), len(f.all()))
	}
	now, err := os.ReadFile(filepath.Join(dir, probesFile))
	if err != nil || !bytes.Equal(now, saved) {
		t.Fatalf("saved set changed: %v", err)
	}
	if out := logged.String(); !strings.Contains(out, "collector.probes.version_refused") || !strings.Contains(out, "level=ERROR") {
		t.Fatalf("refusal not logged: %s", out)
	}
	// A saved set of an unknown major version is not applied at the start.
	if err := os.WriteFile(filepath.Join(dir, probesFile), bytes.Replace(saved, []byte(`"1.0"`), []byte(`"2.0"`), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	g := &fakeApplier{}
	b, blog := newAssignment(t, g, dir)
	if b.version() != 0 || len(g.all()) != 0 || !strings.Contains(blog.String(), "collector.probes.saved_invalid") {
		t.Fatalf("saved set of version 2.0 applied: version %d, log %s", b.version(), blog.String())
	}
}

// TestAdoptedSetReportsEnvPollDeprecation covers the addendum from WP-976:
// adopting a set with probes while the environment poll runs at its default
// interval logs collector.metrics.env_poll_deprecated once per version.
func TestAdoptedSetReportsEnvPollDeprecation(t *testing.T) {
	s := newFakeProbeServer(t)
	s.set(6, icmpSpecs(2, 1))
	var buf bytes.Buffer
	dep := newEnvPollDeprecation(&collectorConfig{MetricsInterval: defaultMetricsInterval},
		slog.New(slog.NewTextHandler(&syncWriter{w: &buf}, nil)))
	a := &probeAssignment{logger: slog.New(slog.DiscardHandler)}
	a.attach(&fakeApplier{}, t.TempDir(), dep)
	refreshProbes(context.Background(), s.nc, s.subject, a)
	refreshProbes(context.Background(), s.nc, s.subject, a) // not modified: no second notice
	if n := strings.Count(buf.String(), "collector.metrics.env_poll_deprecated"); n != 1 {
		t.Fatalf("%d notices, want 1: %s", n, buf.String())
	}
}

// TestSavedSetOfAnotherCollectorReplaced covers PRB-11: a set saved under
// an earlier identity (a new enrollment with the same spool directory) is
// not offered as if_none_match, although its version matches the server's;
// the collector fetches the set of its own id and replaces the foreign one.
func TestSavedSetOfAnotherCollectorReplaced(t *testing.T) {
	dir := t.TempDir()
	foreign := &probewire.AssignmentSet{Header: probewire.Header{SchemaVersion: probewire.SchemaVersion},
		CollectorID: "coll-earlier", Version: 1, Page: 1, Probes: icmpSpecs(3, 1)}
	if err := saveProbeSet(filepath.Join(dir, probesFile), foreign); err != nil {
		t.Fatal(err)
	}
	s := newFakeProbeServer(t)
	own := icmpSpecs(1, 2)
	own[0].Target = "198.51.100.7"
	s.set(1, own)
	f := &fakeApplier{}
	a, logged := newAssignment(t, f, dir)
	if a.version() != 1 {
		t.Fatalf("saved set not restored: version %d", a.version())
	}
	refreshProbes(context.Background(), s.nc, s.subject, a)
	reqs := s.requestLog()
	if len(reqs) == 0 || reqs[0].IfNoneMatch != 0 {
		t.Fatalf("requests %+v, want a first request without if_none_match", reqs)
	}
	last, ok := f.last()
	if !ok || len(f.all()) != 2 || len(last.probes) != 1 || last.probes[0].Target != "198.51.100.7" {
		t.Fatalf("applied sets %+v, want the restored and then the own set", f.all())
	}
	if !strings.Contains(logged.String(), "collector.probes.foreign_set") {
		t.Errorf("replacement not logged: %s", logged.String())
	}
	saved, err := loadProbeSet(filepath.Join(dir, probesFile))
	if err != nil || saved.CollectorID != "coll-1" {
		t.Fatalf("saved set %+v (%v), want the set of coll-1", saved, err)
	}
	// The own set now answers: the next fetch offers its version.
	refreshProbes(context.Background(), s.nc, s.subject, a)
	if reqs = s.requestLog(); reqs[len(reqs)-1].IfNoneMatch != 1 || len(f.all()) != 2 {
		t.Fatalf("second fetch %+v, applied %d sets", reqs[len(reqs)-1], len(f.all()))
	}
}
