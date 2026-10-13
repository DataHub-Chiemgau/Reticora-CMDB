package jobs_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
)

// canceled returns a context that is already done: Run then only resigns.
func canceled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// TestSchedulerReplicasAgainstPostgres covers JOB-02 and OPS-03 (WP-131)
// with two scheduler instances on PostgreSQL advisory locks: exactly one
// leads, every slot runs once and never two runs overlap; when the leader
// stops — or its database session is killed — the other instance takes
// over and catches up on the missed slots; the state is visible only on
// the operator path.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSchedulerReplicasAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "94")
	bg := context.Background()
	kind := fmt.Sprintf("wp131.tick.%d", time.Now().UnixNano())

	var mu sync.Mutex
	slots := map[time.Time]int{}
	runners := map[string]int{}
	var active, overlaps int32
	replica := func(name string) (*jobs.Scheduler, context.CancelFunc) {
		s := jobs.NewScheduler(jobs.NewPGLocker(f.App), jobs.NewPGStateStore(f.App))
		s.Runner, s.Interval = name, 20*time.Millisecond
		if err := s.Register(&jobs.Schedule{Kind: kind, Spec: "@every 150ms", MaxCatchUp: 3, Run: func(ctx context.Context) error {
			if atomic.AddInt32(&active, 1) > 1 {
				atomic.AddInt32(&overlaps, 1)
			}
			defer atomic.AddInt32(&active, -1)
			slot, _ := jobs.SlotFromContext(ctx)
			mu.Lock()
			slots[slot]++
			runners[name]++
			mu.Unlock()
			time.Sleep(10 * time.Millisecond)
			return nil
		}}); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(bg)
		go s.Run(ctx)
		t.Cleanup(cancel)
		return s, cancel
	}
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	count := func(name string) int {
		mu.Lock()
		defer mu.Unlock()
		return runners[name]
	}

	a, stopA := replica("a")
	waitFor("a to lead", a.IsLeader)
	b, stopB := replica("b")
	time.Sleep(time.Second)
	if b.IsLeader() || count("b") != 0 || count("a") < 4 {
		t.Errorf("two replicas: b leads %v, runs a=%d b=%d", b.IsLeader(), count("a"), count("b"))
	}

	// The leader stops: b takes over.
	stopA()
	waitFor("b to lead", b.IsLeader)
	waitFor("b to run", func() bool { return count("b") >= 2 })

	// b's database session is killed: its lock is gone and one of b and a
	// new replica c leads; the slots keep running once each.
	c, stopC := replica("c")
	if _, err := f.Admin.Exec(bg, `SELECT pg_terminate_backend(pid) FROM pg_locks
		WHERE locktype = 'advisory' AND granted AND pid <> pg_backend_pid()
		  AND pid IN (SELECT pid FROM pg_stat_activity WHERE datname = current_database())`); err != nil {
		t.Fatal(err)
	}
	ran := count("b") + count("c")
	waitFor("the slots to go on", func() bool { return count("b")+count("c") >= ran+3 })
	if b.IsLeader() && c.IsLeader() {
		t.Error("two leaders")
	}

	// Every replica stops for more slots than MaxCatchUp; a new replica
	// catches up on the three newest.
	stopB()
	stopC()
	waitFor("b and c to resign", func() bool { return !b.IsLeader() && !c.IsLeader() })
	time.Sleep(time.Second)
	d := jobs.NewScheduler(jobs.NewPGLocker(f.App), jobs.NewPGStateStore(f.App))
	d.Runner = "d"
	if err := d.Register(&jobs.Schedule{Kind: kind, Spec: "@every 150ms", MaxCatchUp: 3, Run: func(ctx context.Context) error {
		slot, _ := jobs.SlotFromContext(ctx)
		mu.Lock()
		slots[slot]++
		mu.Unlock()
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	if n := d.Tick(bg); n != 3 {
		t.Errorf("catch-up after the pause: %d runs, want 3", n)
	}
	t.Cleanup(func() { d.Run(canceled()) })

	if overlaps != 0 {
		t.Errorf("%d overlapping runs", overlaps)
	}
	mu.Lock()
	for slot, n := range slots {
		if n != 1 {
			t.Errorf("slot %s ran %d times", slot.Format(time.RFC3339Nano), n)
		}
	}
	if len(slots) < 12 {
		t.Errorf("only %d slots ran", len(slots))
	}
	mu.Unlock()

	// The state is reachable only on the operator path.
	var visible int
	if err := f.App.QueryRow(bg, `SELECT count(*) FROM scheduler_run WHERE kind = $1`, kind).Scan(&visible); err != nil || visible != 0 {
		t.Errorf("scheduler_run outside the operator path: %d rows (%v)", visible, err)
	}
}
