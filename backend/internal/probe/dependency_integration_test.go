package probe_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Retest (WP-965, PRB-03, PRB-05):
//
//	cd backend && go test -count=1 -p 1 -run 'TestAnchorDown|TestAnchorOfAnotherGroup|TestParentDown|TestParentReturn|TestDependencyDepth|TestDependencyCache|TestParentAndChildDown' ./internal/probe/

// depEnv is organization A with an org-wide collector group of one active
// collector and the state worker without stream.
type depEnv struct {
	t         *testing.T
	f         *scopetest.Fixture
	group     string
	collector string
	applier   *probe.StateApplier
	seq       map[string]uint64
	n         int
}

func newDepEnv(t *testing.T) *depEnv {
	t.Helper()
	f := scopetest.Seed(t, "93")
	e := &depEnv{t: t, f: f, seq: map[string]uint64{}}
	e.group = e.scalar(`INSERT INTO collector_group (organization_id, name) VALUES ($1, 'wp965') RETURNING id::text`, f.OrgA)
	e.collector = e.scalar(`INSERT INTO collector (organization_id, name, status) VALUES ($1, 'wp965', 'active') RETURNING id::text`, f.OrgA)
	e.exec(`UPDATE collector_group_member SET group_id = $1, last_alive_at = now() WHERE collector_id = $2`, e.group, e.collector)
	e.applier = probe.NewStateApplier(nil, probe.StreamConfig{}, f.App)
	return e
}

func (e *depEnv) scalar(sql string, args ...any) string {
	e.t.Helper()
	var out string
	if err := e.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return out
}

func (e *depEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.f.Admin.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

// probe creates an icmp probe with the parent ("" for none).
func (e *depEnv) probe(parent string) string {
	e.t.Helper()
	e.n++
	var p any
	if parent != "" {
		p = parent
	}
	return e.scalar(`INSERT INTO probe (organization_id, client_id, collector_group_id, type, target, parent_probe_id)
		VALUES ($1, $2, $3, 'icmp', $4, $5) RETURNING id::text`, e.f.OrgA, e.f.Client1, e.group,
		fmt.Sprintf("10.65.%d.%d", e.n/250, e.n%250+1), p)
}

// assign assigns the group and returns the epoch of every probe.
func (e *depEnv) assign() {
	e.t.Helper()
	if _, err := probe.NewAssigner(e.f.App).RunOrg(context.Background(), e.f.OrgA); err != nil {
		e.t.Fatal(err)
	}
}

func (e *depEnv) epoch(id string) int64 {
	e.t.Helper()
	var ep int64
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT epoch FROM probe_assignment WHERE probe_id = $1`, id).Scan(&ep); err != nil {
		e.t.Fatal(err)
	}
	return ep
}

// change applies the next change of the probe from the collector.
func (e *depEnv) change(id string, from, to probewire.State, at time.Time) {
	e.t.Helper()
	e.seq[id]++
	reason := "ok"
	if to == probewire.StateDown {
		reason = probewire.ReasonTimeout
	}
	if err := e.applier.Apply(context.Background(), e.f.OrgA, e.collector, &probewire.StateChange{ProbeID: id, Epoch: e.epoch(id),
		Seq: e.seq[id], From: from, To: to, Reason: reason, ObservedAt: at}, at); err != nil {
		e.t.Fatal(err)
	}
}

func (e *depEnv) state(id string) string {
	e.t.Helper()
	return e.scalar(`SELECT state || '/' || reason FROM probe_state WHERE probe_id = $1`, id)
}

// notified counts the probe.state_changed events of the probes with notify
// true.
func (e *depEnv) notified(ids []string) int {
	e.t.Helper()
	n := e.scalar(`SELECT count(*)::text FROM event_outbox WHERE organization_id = $1 AND type = 'probe.state_changed'
		AND entity_id = ANY ($2::text[]) AND (data->>'notify')::boolean`, e.f.OrgA, ids)
	var out int
	_, _ = fmt.Sscan(n, &out)
	return out
}

// lastEvent returns to/reason/notify/suppressed_by of the youngest
// probe.state_changed of the probe.
func (e *depEnv) lastEvent(id string) string {
	e.t.Helper()
	return e.scalar(`SELECT (data->>'to') || '/' || (data->>'reason') || '/' || (data->>'notify') || '/' || COALESCE(data->>'suppressed_by', '')
		FROM event_outbox WHERE organization_id = $1 AND type = 'probe.state_changed' AND entity_id = $2 ORDER BY sequence DESC LIMIT 1`,
		e.f.OrgA, id)
}

// TestAnchorDownSuppressesChildren covers AK1, AK2 and AK5: the anchor of
// the group is down; 500 probes of the group fall 150 s later: all become
// unknown/parent_down, no probe.state_changed with notify true, the events
// name the anchor in suppressed_by. Counter-check: a probe down before the
// anchor becomes unknown/parent_down with the anchor's change.
func TestAnchorDownSuppressesChildren(t *testing.T) {
	e := newDepEnv(t)
	anchor := e.probe("")
	early := e.probe("")
	children := make([]string, 0, 500)
	for range 500 {
		children = append(children, e.probe(""))
	}
	e.exec(`UPDATE collector_group SET anchor_probe_id = $1 WHERE id = $2`, anchor, e.group)
	e.assign()
	t0 := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Millisecond)
	e.change(early, probewire.StateUnknown, probewire.StateDown, t0.Add(-time.Minute))
	if s := e.state(early); s != "down/timeout" {
		t.Fatalf("early child %s, want down", s)
	}
	e.change(anchor, probewire.StateUnknown, probewire.StateDown, t0)
	if s := e.state(anchor); s != "down/timeout" {
		t.Errorf("the anchor itself is %s, want down (exempt)", s)
	}
	if s := e.state(early); s != "unknown/parent_down" {
		t.Errorf("child down before the anchor: %s, want unknown/parent_down", s)
	}
	if ev := e.lastEvent(early); ev != "unknown/parent_down/false/"+anchor {
		t.Errorf("rewrite event of the early child %s", ev)
	}
	if n := e.scalar(`SELECT count(*)::text FROM probe_state_change WHERE probe_id = $1 AND collector_id IS NULL
		AND to_state = 'unknown' AND reason = 'parent_down' AND NOT notify`, early); n != "1" {
		t.Errorf("%s server history rows of the rewrite, want 1", n)
	}
	for _, c := range children {
		e.change(c, probewire.StateUnknown, probewire.StateDown, t0.Add(150*time.Second))
	}
	if n := e.notified(children); n != 0 {
		t.Errorf("%d probe.state_changed with notify true behind the down anchor, want 0", n)
	}
	if n := e.scalar(`SELECT count(*)::text FROM probe_state WHERE probe_id = ANY ($1::uuid[]) AND state = 'unknown' AND reason = 'parent_down'`,
		children); n != "500" {
		t.Errorf("%s of 500 children unknown/parent_down", n)
	}
	if n := e.scalar(`SELECT count(*)::text FROM probe_state_change WHERE probe_id = ANY ($1::uuid[]) AND to_state = 'unknown'
		AND reason = 'parent_down' AND NOT notify AND collector_id IS NOT NULL`, children); n != "500" {
		t.Errorf("%s history rows of the children with notify false, want 500", n)
	}
	if ev := e.lastEvent(children[0]); ev != "unknown/parent_down/false/"+anchor {
		t.Errorf("child event %s, want unknown/parent_down/false/<anchor>", ev)
	}
}

// TestParentDownRewritesDownChildren covers AK1 and AK2: a parent that goes
// down rewrites its down descendants (transitively) to
// unknown/parent_down; an up child stays up; a later down of a child behind
// the down parent is applied as unknown at once.
func TestParentDownRewritesDownChildren(t *testing.T) {
	e := newDepEnv(t)
	parent := e.probe("")
	child := e.probe(parent)
	grandchild := e.probe(child)
	sibling := e.probe(parent)
	late := e.probe(parent)
	e.assign()
	t0 := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Millisecond)
	e.change(sibling, probewire.StateUnknown, probewire.StateUp, t0)
	e.change(child, probewire.StateUnknown, probewire.StateDown, t0)
	// The child is down: the grandchild behind it is suppressed by it.
	e.change(grandchild, probewire.StateUnknown, probewire.StateDown, t0.Add(time.Second))
	if s := e.state(grandchild); s != "unknown/parent_down" {
		t.Fatalf("grandchild behind the down child: %s", s)
	}
	if n := e.notified([]string{child}); n != 1 {
		t.Errorf("the child's own down notified %d times, want 1", n)
	}
	e.change(parent, probewire.StateUnknown, probewire.StateDown, t0.Add(time.Minute))
	if s := e.state(child); s != "unknown/parent_down" {
		t.Errorf("down child after the parent's down: %s, want unknown/parent_down", s)
	}
	if ev := e.lastEvent(child); ev != "unknown/parent_down/false/"+parent {
		t.Errorf("child rewrite event %s", ev)
	}
	if s := e.state(sibling); s != "up/ok" {
		t.Errorf("up sibling %s, want unchanged", s)
	}
	e.change(late, probewire.StateUnknown, probewire.StateDown, t0.Add(2*time.Minute))
	if s := e.state(late); s != "unknown/parent_down" {
		t.Errorf("down after the parent: %s, want unknown/parent_down", s)
	}
	if n := e.notified([]string{grandchild, late}); n != 0 {
		t.Errorf("%d notifications of suppressed probes, want 0", n)
	}
}

// TestParentReturnChildResync covers AK3: the parent returns, the child
// stays unknown/parent_down; a failed result leaves it, an ok result after
// the return makes it up with reason resync (notify false); a child with a
// change of the collector is evaluated by the change.
func TestParentReturnChildResync(t *testing.T) {
	e := newDepEnv(t)
	parent := e.probe("")
	child := e.probe(parent)
	other := e.probe(parent)
	e.assign()
	t0 := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Millisecond)
	e.change(parent, probewire.StateUnknown, probewire.StateDown, t0)
	e.change(child, probewire.StateUnknown, probewire.StateDown, t0.Add(time.Second))
	e.change(other, probewire.StateUnknown, probewire.StateDown, t0.Add(time.Second))
	store := probe.NewPGIngestStore(e.f.App)
	result := func(id string, seq uint64, status probewire.Status, at time.Time) {
		t.Helper()
		if _, err := store.WriteResults(context.Background(), e.f.OrgA, []probe.ResultsMessage{{CollectorID: e.collector,
			Results: []probewire.Result{{ProbeID: id, Epoch: e.epoch(id), Seq: seq, ObservedAt: at, Status: status}}}}); err != nil {
			t.Fatal(err)
		}
	}
	// An ok result while the parent is still down changes nothing.
	result(child, 1, probewire.StatusOK, t0.Add(30*time.Second))
	if s := e.state(child); s != "unknown/parent_down" {
		t.Fatalf("child %s while the parent is down", s)
	}
	e.change(parent, probewire.StateDown, probewire.StateUp, t0.Add(time.Minute))
	if s := e.state(child); s != "unknown/parent_down" {
		t.Errorf("child %s right after the parent's return, want still unknown/parent_down", s)
	}
	result(child, 2, probewire.StatusFail, t0.Add(70*time.Second))
	if s := e.state(child); s != "unknown/parent_down" {
		t.Errorf("child %s after a failed result, want unknown until the next change", s)
	}
	result(child, 3, probewire.StatusOK, t0.Add(100*time.Second))
	if s := e.state(child); s != "up/"+probe.ReasonResync {
		t.Errorf("child %s after an ok result, want up/resync", s)
	}
	if ev := e.lastEvent(child); ev != "up/resync/false/" {
		t.Errorf("resync event %s, want up/resync/false", ev)
	}
	if n := e.scalar(`SELECT count(*)::text FROM probe_state_change WHERE probe_id = $1 AND reason = 'resync' AND collector_id IS NULL
		AND NOT notify`, child); n != "1" {
		t.Errorf("%s resync history rows, want 1", n)
	}
	// The other child: its next change decides.
	e.change(other, probewire.StateDown, probewire.StateUp, t0.Add(90*time.Second))
	if s := e.state(other); s != "up/ok" {
		t.Errorf("other child %s after its change, want up", s)
	}
}

// TestDependencyDepthLimit covers AK4: a chain of ten levels below a down
// root: the probe on level 8 is suppressed, the one on level 9 counts as
// without parent there (counted) and goes down with notification.
func TestDependencyDepthLimit(t *testing.T) {
	e := newDepEnv(t)
	chain := func() []string {
		ids := []string{e.probe("")}
		for i := 1; i <= 9; i++ {
			ids = append(ids, e.probe(ids[i-1]))
		}
		return ids
	}
	ids, deep := chain(), chain()
	e.assign()
	t0 := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Millisecond)
	e.change(ids[0], probewire.StateUnknown, probewire.StateDown, t0)
	e.change(ids[8], probewire.StateUnknown, probewire.StateDown, t0.Add(time.Second))
	if s := e.state(ids[8]); s != "unknown/parent_down" {
		t.Errorf("level 8: %s, want unknown/parent_down (root within 8 levels)", s)
	}
	// A chain whose only blocking probe lies beyond the limit: the root is
	// down, the levels in between have no state; level 9 goes down with
	// notification and the cut is counted.
	e.change(deep[0], probewire.StateUnknown, probewire.StateDown, t0)
	before := testutil.ToFloat64(probe.DependencyDepthExceeded())
	e.change(deep[9], probewire.StateUnknown, probewire.StateDown, t0.Add(time.Second))
	if s := e.state(deep[9]); s != "down/timeout" {
		t.Errorf("level 9 with the down root beyond the limit: %s, want down", s)
	}
	if n := e.notified([]string{deep[9]}); n != 1 {
		t.Errorf("level 9 notified %d times, want once", n)
	}
	if testutil.ToFloat64(probe.DependencyDepthExceeded()) <= before {
		t.Error("the cut chain was not counted")
	}
}

// TestAnchorOfAnotherGroupSuppressesChildren covers AK2 and AK5 with an
// anchor probe of another group (WP-947 only requires a probe of the
// organization): when it falls, the down probes of the group it anchors
// become unknown/parent_down; later downs of the group are suppressed.
func TestAnchorOfAnotherGroupSuppressesChildren(t *testing.T) {
	e := newDepEnv(t)
	other := e.scalar(`INSERT INTO collector_group (organization_id, name) VALUES ($1, 'wp965-anchor') RETURNING id::text`, e.f.OrgA)
	second := e.scalar(`INSERT INTO collector (organization_id, name, status) VALUES ($1, 'wp965-anchor', 'active') RETURNING id::text`, e.f.OrgA)
	e.exec(`UPDATE collector_group_member SET group_id = $1, last_alive_at = now() WHERE collector_id = $2`, other, second)
	anchor := e.probe("")
	e.exec(`UPDATE probe SET collector_group_id = $1 WHERE id = $2`, other, anchor)
	early := e.probe("")
	later := e.probe("")
	e.exec(`UPDATE collector_group SET anchor_probe_id = $1 WHERE id = $2`, anchor, e.group)
	e.assign()
	t0 := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Millisecond)
	e.change(early, probewire.StateUnknown, probewire.StateDown, t0)
	e.seq[anchor]++
	if err := e.applier.Apply(context.Background(), e.f.OrgA, second, &probewire.StateChange{ProbeID: anchor, Epoch: e.epoch(anchor),
		Seq: e.seq[anchor], From: probewire.StateUnknown, To: probewire.StateDown, Reason: probewire.ReasonTimeout,
		ObservedAt: t0.Add(time.Second)}, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if s := e.state(anchor); s != "down/timeout" {
		t.Fatalf("anchor of the other group: %s, want down", s)
	}
	if s := e.state(early); s != "unknown/parent_down" {
		t.Errorf("down probe of the anchored group: %s, want unknown/parent_down", s)
	}
	if ev := e.lastEvent(early); ev != "unknown/parent_down/false/"+anchor {
		t.Errorf("rewrite event %s, want suppressed by the anchor of the other group", ev)
	}
	e.change(later, probewire.StateUnknown, probewire.StateDown, t0.Add(2*time.Second))
	if s := e.state(later); s != "unknown/parent_down" {
		t.Errorf("later down of the anchored group: %s, want unknown/parent_down", s)
	}
}

// TestDependencyCacheSeesChangedAnchorAndParent covers AK4: the chain and
// the anchor are cached per lane, but a change of anchor_probe_id or
// parent_probe_id written by another process (here straight in the
// database) decides the next down at once, without waiting for
// DependencyCacheTTL.
func TestDependencyCacheSeesChangedAnchorAndParent(t *testing.T) {
	e := newDepEnv(t)
	oldAnchor := e.probe("")
	newAnchor := e.probe("")
	child := e.probe("")
	parent := e.probe("")
	orphan := e.probe(parent)
	e.exec(`UPDATE collector_group SET anchor_probe_id = $1 WHERE id = $2`, oldAnchor, e.group)
	e.assign()
	t0 := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Millisecond)
	e.change(oldAnchor, probewire.StateUnknown, probewire.StateDown, t0)
	// Fill the cache of the child's lane and the orphan's lane.
	e.change(child, probewire.StateUnknown, probewire.StateUp, t0)
	e.change(child, probewire.StateUp, probewire.StateDown, t0.Add(time.Second))
	if s := e.state(child); s != "unknown/parent_down" {
		t.Fatalf("child behind the down anchor: %s", s)
	}
	e.change(orphan, probewire.StateUnknown, probewire.StateUp, t0)
	e.change(parent, probewire.StateUnknown, probewire.StateDown, t0.Add(time.Second))
	if s := e.state(orphan); s != "up/ok" {
		t.Fatalf("up child of the down parent: %s, want up", s)
	}
	stale := testutil.ToFloat64(probe.DependencyCacheStale())
	// The anchor moves to an up probe, the orphan loses its parent.
	e.exec(`UPDATE collector_group SET anchor_probe_id = $1 WHERE id = $2`, newAnchor, e.group)
	e.exec(`UPDATE probe SET parent_probe_id = NULL WHERE id = $1`, orphan)
	e.change(newAnchor, probewire.StateUnknown, probewire.StateUp, t0.Add(2*time.Second))
	e.change(child, probewire.StateUnknown, probewire.StateUp, t0.Add(3*time.Second))
	e.change(child, probewire.StateUp, probewire.StateDown, t0.Add(4*time.Second))
	if s := e.state(child); s != "down/timeout" {
		t.Errorf("child after the anchor moved to an up probe: %s, want down (the old anchor no longer applies)", s)
	}
	e.change(orphan, probewire.StateUp, probewire.StateDown, t0.Add(4*time.Second))
	if s := e.state(orphan); s != "down/timeout" {
		t.Errorf("probe whose parent was removed: %s, want down", s)
	}
	if got := testutil.ToFloat64(probe.DependencyCacheStale()); got <= stale {
		t.Errorf("stale cache entries counted %v, want more than %v", got, stale)
	}
	// And the other way: a parent set on a probe whose cache knew none.
	free := e.probe("")
	grand := e.probe(free)
	e.assign()
	e.change(free, probewire.StateUnknown, probewire.StateUp, t0)
	e.change(grand, probewire.StateUnknown, probewire.StateUp, t0)
	e.exec(`UPDATE probe SET parent_probe_id = $1 WHERE id = $2`, parent, free)
	e.change(grand, probewire.StateUp, probewire.StateDown, t0.Add(5*time.Second))
	if s := e.state(grand); s != "unknown/parent_down" {
		t.Errorf("grandchild of the down parent through a new parent link: %s, want unknown/parent_down", s)
	}
}

// TestParentAndChildDownConcurrently covers AK1/AK2 across the lanes: the
// down of a parent and of its child arrive at once in two transactions.
// Whatever order their commits take, the child never ends down behind the
// down parent: either its down is suppressed, or the parent's change
// rewrites it (as for a child down before its parent). A deadlock of the
// two is repeated inside apply, no error reaches the caller.
func TestParentAndChildDownConcurrently(t *testing.T) {
	e := newDepEnv(t)
	const rounds = 10
	type pair struct{ parent, child string }
	pairs := make([]pair, rounds)
	for i := range pairs {
		pairs[i].parent = e.probe("")
		pairs[i].child = e.probe(pairs[i].parent)
	}
	e.assign()
	t0 := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Millisecond)
	for _, p := range pairs {
		e.change(p.parent, probewire.StateUnknown, probewire.StateUp, t0)
		e.change(p.child, probewire.StateUnknown, probewire.StateUp, t0)
	}
	for _, p := range pairs {
		start := make(chan struct{})
		errs := make(chan error, 2)
		for _, id := range []string{p.parent, p.child} {
			e.seq[id]++
			c := &probewire.StateChange{ProbeID: id, Epoch: e.epoch(id), Seq: e.seq[id], From: probewire.StateUp,
				To: probewire.StateDown, Reason: probewire.ReasonTimeout, ObservedAt: t0.Add(time.Minute)}
			go func() {
				<-start
				errs <- e.applier.Apply(context.Background(), e.f.OrgA, e.collector, c, t0.Add(time.Minute))
			}()
		}
		close(start)
		for range 2 {
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
		}
		if s := e.state(p.child); s != "unknown/parent_down" {
			t.Errorf("child of a parent that fell at the same time: %s, want unknown/parent_down", s)
		}
	}
}
