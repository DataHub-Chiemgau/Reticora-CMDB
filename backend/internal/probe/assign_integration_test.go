package probe

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// Retest (WP-941, PRB-03, PRB-11):
//
//	cd backend && go test -count=1 -p 1 -run 'TestAssignmentEpochIncrements|TestAssignSingleRunnerTwoInstances|TestProbeWriteTriggersAssignAfterCommit|TestAssignmentEpochSurvivesRemoval|TestAssignRunWaitsForProbeDelete' ./internal/probe/

// assignEnv is organization A with an org-wide collector group of three
// active collectors.
type assignEnv struct {
	t          *testing.T
	f          *scopetest.Fixture
	group      string
	collectors []string
}

func newAssignEnv(t *testing.T) *assignEnv {
	t.Helper()
	f := scopetest.Seed(t, "93")
	e := &assignEnv{t: t, f: f}
	e.group = e.scalar(`INSERT INTO collector_group (organization_id, name) VALUES ($1, 'wp941') RETURNING id::text`, f.OrgA)
	for i := range 3 {
		c := e.scalar(`INSERT INTO collector (organization_id, name, status) VALUES ($1, $2, 'active') RETURNING id::text`,
			f.OrgA, fmt.Sprintf("wp941-%d", i))
		e.exec(`UPDATE collector_group_member SET group_id = $1 WHERE collector_id = $2`, e.group, c)
		e.collectors = append(e.collectors, c)
	}
	return e
}

func (e *assignEnv) scalar(sql string, args ...any) string {
	e.t.Helper()
	var out string
	if err := e.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return out
}

func (e *assignEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.f.Admin.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

// probes inserts n icmp probes of client 1 in the group.
func (e *assignEnv) probes(n int) {
	e.t.Helper()
	e.exec(`INSERT INTO probe (organization_id, client_id, collector_group_id, type, target, params)
		SELECT $1, $2, $3, 'icmp', '10.95.' || (g / 250) || '.' || (g % 250 + 1), '{}' FROM generate_series(0, $4 - 1) g`,
		e.f.OrgA, e.f.Client1, e.group, n)
}

// assignments reads collector and epoch per probe.
func (e *assignEnv) assignments() map[string]Assignment {
	e.t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT probe_id::text, collector_id::text, epoch FROM probe_assignment
		WHERE organization_id = $1`, e.f.OrgA)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]Assignment{}
	for rows.Next() {
		var a Assignment
		if err := rows.Scan(&a.ProbeID, &a.CollectorID, &a.Epoch); err != nil {
			e.t.Fatal(err)
		}
		out[a.ProbeID] = a
	}
	return out
}

// versions reads config_version per collector.
func (e *assignEnv) versions() map[string]int64 {
	e.t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT collector_id::text, config_version FROM collector_group_member
		WHERE organization_id = $1`, e.f.OrgA)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id string
		var v int64
		if err := rows.Scan(&id, &v); err != nil {
			e.t.Fatal(err)
		}
		out[id] = v
	}
	return out
}

// recordingNotifier records the notices of changed collectors.
type recordingNotifier struct {
	mu    sync.Mutex
	calls [][]CollectorVersion
}

func (n *recordingNotifier) AssignmentsChanged(_ context.Context, _ string, cs []CollectorVersion) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, cs)
}

func (n *recordingNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.calls)
}

// TestAssignmentEpochIncrements covers AK3 (PRB-11): a run assigns every
// probe once with epoch 1; a run without change writes nothing; when a
// member drops out only its probes move, with epoch + 1, the others keep
// theirs, and config_version rises by one for the former and the new
// collectors of the moved probes, in the same transaction.
func TestAssignmentEpochIncrements(t *testing.T) {
	e := newAssignEnv(t)
	e.probes(60)
	notes := &recordingNotifier{}
	a := NewAssigner(e.f.App).WithNotifier(notes)
	ctx := context.Background()
	if ran, err := a.RunGroup(ctx, e.f.OrgA, e.group); err != nil || !ran {
		t.Fatalf("first run: ran %v, %v", ran, err)
	}
	first := e.assignments()
	if len(first) != 60 {
		t.Fatalf("%d assignments, want 60", len(first))
	}
	used := map[string]bool{}
	for _, as := range first {
		if as.Epoch != 1 {
			t.Fatalf("probe %s: epoch %d, want 1", as.ProbeID, as.Epoch)
		}
		used[as.CollectorID] = true
	}
	v1 := e.versions()
	for _, c := range e.collectors {
		if want := map[bool]int64{true: 1, false: 0}[used[c]]; v1[c] != want {
			t.Errorf("collector %s: config_version %d, want %d", c, v1[c], want)
		}
	}
	if notes.count() != 1 {
		t.Errorf("%d notices after the first run, want 1", notes.count())
	}

	// No change: nothing written, no notice.
	if _, err := a.RunGroup(ctx, e.f.OrgA, e.group); err != nil {
		t.Fatal(err)
	}
	if again := e.assignments(); fmt.Sprint(again) != fmt.Sprint(first) {
		t.Error("a run without change changed assignments")
	}
	if fmt.Sprint(e.versions()) != fmt.Sprint(v1) || notes.count() != 1 {
		t.Error("a run without change raised versions or notified")
	}

	// The third collector goes offline: only its probes move.
	gone := e.collectors[2]
	e.exec(`UPDATE collector SET status = 'offline' WHERE id = $1`, gone)
	if _, err := a.RunGroup(ctx, e.f.OrgA, e.group); err != nil {
		t.Fatal(err)
	}
	second := e.assignments()
	receivers := map[string]bool{}
	for id, was := range first {
		now := second[id]
		switch {
		case was.CollectorID == gone:
			if now.CollectorID == gone || now.Epoch != 2 {
				t.Errorf("probe %s of the gone member: %s epoch %d, want another member with epoch 2", id, now.CollectorID, now.Epoch)
			}
			receivers[now.CollectorID] = true
		case now != was:
			t.Errorf("probe %s of a remaining member changed: %+v -> %+v", id, was, now)
		}
	}
	v2 := e.versions()
	for _, c := range e.collectors {
		want := v1[c]
		if c == gone || receivers[c] {
			want++
		}
		if v2[c] != want {
			t.Errorf("collector %s: config_version %d -> %d, want %d", c, v1[c], v2[c], want)
		}
	}
	// Epochs never go down: a probe that comes back gets epoch + 1 again.
	e.exec(`UPDATE collector SET status = 'active' WHERE id = $1`, gone)
	if _, err := a.RunGroup(ctx, e.f.OrgA, e.group); err != nil {
		t.Fatal(err)
	}
	for id, now := range e.assignments() {
		if now.Epoch < second[id].Epoch {
			t.Errorf("probe %s: epoch %d after %d", id, now.Epoch, second[id].Epoch)
		}
	}
}

// TestAssignSingleRunnerTwoInstances covers AK4: while one instance holds
// the group lock probe_assign:<group>, the run of a second one skips the
// group; the whole run of both instances assigns every probe once.
func TestAssignSingleRunnerTwoInstances(t *testing.T) {
	e := newAssignEnv(t)
	e.probes(20)
	first, second := NewAssigner(e.f.App), NewAssigner(e.f.App)
	held, release := make(chan struct{}), make(chan struct{})
	first.locked = func(string) {
		close(held)
		<-release
	}
	ctx := context.Background()
	done := make(chan error, 1)
	var firstRan bool
	go func() {
		var err error
		firstRan, err = first.RunGroup(ctx, e.f.OrgA, e.group)
		done <- err
	}()
	select {
	case <-held:
	case <-time.After(10 * time.Second):
		t.Fatal("the first instance did not take the group lock")
	}
	ran, err := second.RunGroup(ctx, e.f.OrgA, e.group)
	if err != nil || ran {
		t.Errorf("second instance while the first holds the group: ran %v, %v; want skipped", ran, err)
	}
	// The organization run of the second instance runs every other group
	// (the collectors' own, now empty groups) but not the held one.
	var groups int
	if qerr := e.f.Admin.QueryRow(ctx, `SELECT count(*) FROM collector_group WHERE organization_id = $1`, e.f.OrgA).Scan(&groups); qerr != nil {
		t.Fatal(qerr)
	}
	n, err := second.RunOrg(ctx, e.f.OrgA)
	if err != nil || n != groups-1 {
		t.Errorf("second instance's organization run: %d groups, %v; want %d", n, err, groups-1)
	}
	close(release)
	if err := <-done; err != nil || !firstRan {
		t.Fatalf("first instance: ran %v, %v", firstRan, err)
	}
	as := e.assignments()
	if len(as) != 20 {
		t.Fatalf("%d assignments, want 20", len(as))
	}
	for _, a := range as {
		if a.Epoch != 1 {
			t.Errorf("probe %s: epoch %d, want 1 (one run)", a.ProbeID, a.Epoch)
		}
	}
	// The system job probe.assign runs every 10 s.
	if s := first.Schedule(); s.Kind != AssignKind || s.Spec != "@every 10s" {
		t.Errorf("schedule %s %s", s.Kind, s.Spec)
	}
}

// recordingTrigger records the calls of the assignment after a write.
type recordingTrigger struct {
	mu     sync.Mutex
	groups [][]string
	next   AssignTrigger
}

func (r *recordingTrigger) ProbesChanged(ctx context.Context, orgID string, groups []string, raised []CollectorVersion) {
	r.mu.Lock()
	r.groups = append(r.groups, groups)
	r.mu.Unlock()
	if r.next != nil {
		r.next.ProbesChanged(ctx, orgID, groups, raised)
	}
}

func (r *recordingTrigger) calls() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]string(nil), r.groups...)
}

// TestProbeWriteTriggersAssignAfterCommit covers AK4: a committed probe
// write runs the assignment of its group at once (database.AfterCommit);
// a write that rolls back triggers nothing; a delete raises the version of
// the collector that ran the probe.
func TestProbeWriteTriggersAssignAfterCommit(t *testing.T) {
	e := newAssignEnv(t)
	notes := &recordingNotifier{}
	trigger := &recordingTrigger{next: NewAssigner(e.f.App).WithNotifier(notes)}
	repo := NewPGRepository(e.f.App)
	repo.SetAssignTrigger(trigger)
	scope := database.OrgWideScope(e.f.OrgA, e.f.User)
	ctx := database.ContextWithTenantScope(context.Background(), &scope)
	p := &Probe{Type: "icmp", Target: "10.95.9.1", ClientID: e.f.Client1, CollectorGroupID: e.group, Params: []byte(`{}`), Enabled: true}

	refuse := errors.New("wp941: refused")
	if _, err := repo.Create(ctx, p, func(context.Context, WriteTx, *Probe, *Probe) error { return refuse }); !errors.Is(err, refuse) {
		t.Fatalf("refused create: %v", err)
	}
	if calls := trigger.calls(); len(calls) != 0 {
		t.Fatalf("a rolled back write triggered %v", calls)
	}

	created, err := repo.Create(ctx, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls := trigger.calls(); len(calls) != 1 || len(calls[0]) != 1 || calls[0][0] != e.group {
		t.Fatalf("calls after the create: %v, want the group once", calls)
	}
	var assigned Assignment
	deadline := time.Now().Add(10 * time.Second)
	for {
		if a, ok := e.assignments()[created.ID]; ok {
			assigned = a
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the created probe was not assigned after the commit")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if assigned.Epoch != 1 {
		t.Errorf("epoch %d, want 1", assigned.Epoch)
	}
	waitNotes := func(want int) {
		t.Helper()
		for end := time.Now().Add(10 * time.Second); notes.count() < want; time.Sleep(20 * time.Millisecond) {
			if time.Now().After(end) {
				t.Fatalf("%d notices, want %d", notes.count(), want)
			}
		}
	}
	waitNotes(1)
	before := e.versions()[assigned.CollectorID]
	if err := repo.Delete(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if after := e.versions()[assigned.CollectorID]; after != before+1 {
		t.Errorf("config_version of the former collector %d -> %d after the delete, want + 1", before, after)
	}
	waitNotes(2)
}

// epochOf reads the assignment of probe id (ok false without one).
func (e *assignEnv) epochOf(id string) (Assignment, bool) {
	e.t.Helper()
	a, ok := e.assignments()[id]
	return a, ok
}

// run runs the assignment of group and fails on an error.
func (e *assignEnv) run(a *Assigner, group string) {
	e.t.Helper()
	if _, err := a.RunGroup(context.Background(), e.f.OrgA, group); err != nil {
		e.t.Fatalf("run of group %s: %v", group, err)
	}
}

// TestAssignmentEpochSurvivesRemoval covers AK3 (PRB-11, never going down
// and never issued twice): the epoch of a probe rises across every way its
// assignment row goes (a disable, a move to another collector group and
// back, a member that leaves the group) and comes back; the high-water mark
// in probe_epoch keeps it.
func TestAssignmentEpochSurvivesRemoval(t *testing.T) {
	e := newAssignEnv(t)
	e.probes(1)
	id := e.scalar(`SELECT id::text FROM probe WHERE organization_id = $1 AND collector_group_id = $2`, e.f.OrgA, e.group)
	a := NewAssigner(e.f.App)
	var last int64
	step := func(what string) Assignment {
		t.Helper()
		got, ok := e.epochOf(id)
		if !ok {
			t.Fatalf("%s: no assignment", what)
		}
		if got.Epoch <= last {
			t.Fatalf("%s: epoch %d after %d, want higher", what, got.Epoch, last)
		}
		last = got.Epoch
		return got
	}
	e.run(a, e.group)
	first := step("first run")
	// A move to another member.
	e.exec(`UPDATE collector SET status = 'offline' WHERE id = $1`, first.CollectorID)
	e.run(a, e.group)
	step("move")
	// Disable: the assignment goes; enable: it comes back higher.
	e.exec(`UPDATE probe SET enabled = false WHERE id = $1`, id)
	e.run(a, e.group)
	if _, ok := e.epochOf(id); ok {
		t.Fatal("a disabled probe kept its assignment")
	}
	e.exec(`UPDATE probe SET enabled = true WHERE id = $1`, id)
	e.run(a, e.group)
	step("enable after disable")
	// A move to another collector group and back (the trigger drops the
	// assignment both times).
	other := e.scalar(`INSERT INTO collector_group (organization_id, name) VALUES ($1, 'wp941-other') RETURNING id::text`, e.f.OrgA)
	c := e.scalar(`INSERT INTO collector (organization_id, name, status) VALUES ($1, 'wp941-other', 'active') RETURNING id::text`, e.f.OrgA)
	e.exec(`UPDATE collector_group_member SET group_id = $1 WHERE collector_id = $2`, other, c)
	e.exec(`UPDATE probe SET collector_group_id = $1 WHERE id = $2`, other, id)
	e.run(a, other)
	step("group change")
	e.exec(`UPDATE probe SET collector_group_id = $1 WHERE id = $2`, e.group, id)
	e.run(a, e.group)
	back := step("group change back")
	// The member leaves the group: the trigger drops the assignment.
	e.exec(`UPDATE collector_group_member SET group_id = $1 WHERE collector_id = $2`, other, back.CollectorID)
	e.run(a, e.group)
	step("member left")
	var hw int64
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT epoch FROM probe_epoch WHERE probe_id = $1`, id).Scan(&hw); err != nil || hw != last {
		t.Errorf("probe_epoch %d (%v), want the last epoch %d", hw, err, last)
	}
}

// TestAssignRunWaitsForProbeDelete covers the lock order of AK3 and AK4:
// a probe write locks the probe row first and then drops its assignment
// (cascade or trigger); a run that moves the probe at the same time locks
// the probes of its plan first, waits there, and then drops what the write
// changed: both finish (no 40P01, no 23503 of the run) and the deleted
// probe gets no assignment.
func TestAssignRunWaitsForProbeDelete(t *testing.T) {
	e := newAssignEnv(t)
	e.probes(1)
	id := e.scalar(`SELECT id::text FROM probe WHERE organization_id = $1 AND collector_group_id = $2`, e.f.OrgA, e.group)
	a := NewAssigner(e.f.App)
	e.run(a, e.group)
	first, _ := e.epochOf(id)
	e.exec(`UPDATE collector SET status = 'offline' WHERE id = $1`, first.CollectorID)

	bg := context.Background()
	tx, err := e.f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	// The first step of PGRepository.Delete: the row lock of the probe.
	if _, err := tx.Exec(bg, `SELECT 1 FROM probe WHERE id = $1 FOR UPDATE`, id); err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() {
		_, rerr := a.RunGroup(bg, e.f.OrgA, e.group)
		errc <- rerr
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting bool
		if err := e.f.Admin.QueryRow(bg, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND query LIKE '%probe%' AND pid <> pg_backend_pid())`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the run never waited on the probe row")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The delete drops the assignment by cascade while the run waits.
	if _, err := tx.Exec(bg, `DELETE FROM probe WHERE id = $1`, id); err != nil {
		t.Fatalf("delete while the run waits: %v", err)
	}
	if err := tx.Commit(bg); err != nil {
		t.Fatalf("commit of the delete: %v", err)
	}
	if err := <-errc; err != nil {
		t.Fatalf("run after the delete: %v", err)
	}
	if _, ok := e.epochOf(id); ok {
		t.Error("the deleted probe has an assignment")
	}
}
