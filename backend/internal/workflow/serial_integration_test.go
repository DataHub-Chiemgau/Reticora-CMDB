package workflow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Runs of one CI are executed one after the other in the order of their
// events (WFL-02, EVT-01, WP-1089, E-227 option 2): the job workflow.run
// executes its run only as the oldest pending run of its serial key while
// no other run of the key is running; the end of a run queues the next
// one, the system job workflow.serial_sweep queues a run whose wake-up was
// lost.
//
// They run only with TEST_DATABASE_URL (scopetest.Seed) and TEST_NATS_URL.
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestRunsOfOneCIRunStrictlyInEntitySeqOrder|TestRunsOfDifferentCIsRunInParallel|TestWaitingApprovalDoesNotBlockCI|TestSerialSweepRequeuesStrandedRun|TestSerialRunClosedWhenCILeavesJobScope|TestSerialSweepIgnoresFreshHead' ./internal/workflow/

// recordingRuns records when the runs start (status running) and end, the
// most runs active at once, and holds a started run for hold. With
// waitParallel a started run waits up to 10 s for a second one.
type recordingRuns struct {
	Repository
	hold         time.Duration
	waitParallel bool

	mu        sync.Mutex
	started   map[string]bool
	ended     map[string]bool
	order     []string
	active    int
	maxActive int
}

func newRecordingRuns(repo Repository) *recordingRuns {
	return &recordingRuns{Repository: repo, started: map[string]bool{}, ended: map[string]bool{}}
}

func (r *recordingRuns) UpdateRunStatus(ctx context.Context, orgID, id, status string, finished *time.Time, runCtx JSONMap) (*Run, error) {
	out, err := r.Repository.UpdateRunStatus(ctx, orgID, id, status, finished, runCtx)
	if err != nil {
		return out, err
	}
	r.mu.Lock()
	first := false
	switch status {
	case StatusRunning:
		if !r.started[id] {
			r.started[id], first = true, true
			r.order = append(r.order, id)
			r.active++
			r.maxActive = max(r.maxActive, r.active)
		}
	case StatusPending:
	default:
		if r.started[id] && !r.ended[id] {
			r.ended[id] = true
			r.active--
		}
	}
	r.mu.Unlock()
	if first {
		time.Sleep(r.hold)
		deadline := time.Now().Add(10 * time.Second)
		for r.waitParallel && time.Now().Before(deadline) {
			r.mu.Lock()
			n := r.maxActive
			r.mu.Unlock()
			if n >= 2 {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	return out, err
}

func (r *recordingRuns) snapshot() (order []string, maxActive int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.order), r.maxActive
}

// secondWorker starts a second consumer of the job queue of e, another
// worker process on the same durable consumer.
func (e *workerEnv) secondWorker(t *testing.T) {
	t.Helper()
	q := jobs.NewQueue(jobs.NewService(e.jobs), e.js)
	q.Stream, q.SubjectPrefix = e.queue.Stream, e.queue.SubjectPrefix
	q.AckMargin, q.SweepInterval = e.queue.AckMargin, time.Hour
	kind := RunJobKindConfig
	kind.Backoff = func(int) time.Duration { return 50 * time.Millisecond }
	worker := NewWorker(e.exec)
	kind.Failed = worker.JobFailed
	q.Handle(kind, worker.Run)
	ctx, cancel := context.WithCancel(context.Background())
	go q.Run(ctx)
	t.Cleanup(cancel)
}

// startOn stores a run of a definition with actions on the CI ciID with
// the event sequence seq (nil: none), without queueing it.
func (e *workerEnv) startOn(t *testing.T, ciID string, seq *int64, actions ...JSONMap) *Run {
	t.Helper()
	def := &Definition{OrganizationID: e.f.OrgA, Name: fmt.Sprintf("wf-serial-%d", time.Now().UnixNano()), Trigger: JSONMap{"type": "manual"}, Actions: actions, Active: true}
	if err := e.repo.CreateDefinition(e.ctx, def); err != nil {
		t.Fatal(err)
	}
	var run *Run
	var err error
	if seq != nil {
		run, err = e.exec.StartSequenced(e.ctx, e.f.OrgA, def, "manual", JSONMap{"ci_id": ciID}, *seq)
	} else {
		run, err = e.exec.Start(e.ctx, e.f.OrgA, def, "manual", JSONMap{"ci": map[string]any{"id": ciID}})
	}
	if err != nil {
		t.Fatal(err)
	}
	if run.CIID != ciID || run.SerialKey != "ci:"+ciID {
		t.Fatalf("run on the CI: ci_id %q serial_key %q, want %s", run.CIID, run.SerialKey, ciID)
	}
	return run
}

// awaitStatus waits until the run has status want.
func (e *workerEnv) awaitStatus(t *testing.T, runID, want string) *Run {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		run, err := e.repo.GetRun(e.ctx, e.f.OrgA, runID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status == want {
			return run
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s is %s, want %s", runID, run.Status, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestRunsOfOneCIRunStrictlyInEntitySeqOrder: ten runs on one CI arrive
// with their event sequence numbers shuffled; two workers execute them one
// after the other in the order of the numbers, never two at once.
func TestRunsOfOneCIRunStrictlyInEntitySeqOrder(t *testing.T) {
	e := newWorkerEnv(t, 5, true)
	e.secondWorker(t)
	rec := newRecordingRuns(e.repo)
	rec.hold = 30 * time.Millisecond
	e.exec.repo = rec
	ciID := e.f.CI(t, e.f.OrgA, "", "wf-serial-ci")
	seqs := []int64{7, 3, 10, 1, 9, 4, 2, 8, 6, 5}
	bySeq := map[int64]string{}
	runs := make([]*Run, 0, len(seqs))
	for _, seq := range seqs {
		run := e.startOn(t, ciID, &seq, JSONMap{"type": "noop"})
		bySeq[seq] = run.ID
		runs = append(runs, run)
	}
	waitsBefore := serialWaitCount(t)
	for _, run := range runs {
		if _, err := e.exec.Execute(e.ctx, e.f.OrgA, nil, run); err != nil {
			t.Fatal(err)
		}
	}
	for _, run := range runs {
		if done := e.await(t, run.ID); done.Status != StatusSucceeded {
			t.Fatalf("run %s: %s %q, want succeeded", run.ID, done.Status, done.LastError)
		}
	}
	order, maxActive := rec.snapshot()
	want := make([]string, 0, len(seqs))
	for seq := int64(1); seq <= int64(len(seqs)); seq++ {
		want = append(want, bySeq[seq])
	}
	if !slices.Equal(order, want) {
		t.Errorf("execution order %v, want the order of entity_seq %v", order, want)
	}
	if maxActive != 1 {
		t.Errorf("runs of the CI running at once: %d, want 1", maxActive)
	}
	if waits := serialWaitCount(t) - waitsBefore; waits < uint64(len(seqs)) {
		t.Errorf("observations of reticora_workflow_serial_wait_seconds: %d, want at least %d", waits, len(seqs))
	}
}

// backdate sets updated_at of a run age into the past on the clock of the
// database; the update trigger of updated_at is off for that statement.
func (e *workerEnv) backdate(t *testing.T, runID string, age time.Duration) {
	t.Helper()
	ctx := context.Background()
	tx, err := e.f.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE workflow_run SET updated_at = now() - make_interval(secs => $2) WHERE id = $1`, runID, age.Seconds()); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

// serialWaitCount returns the observations of
// reticora_workflow_serial_wait_seconds so far.
func serialWaitCount(t *testing.T) uint64 {
	t.Helper()
	reg := prometheus.NewRegistry()
	RegisterMetrics(reg)
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() == "reticora_workflow_serial_wait_seconds" && len(f.GetMetric()) == 1 {
			return f.GetMetric()[0].GetHistogram().GetSampleCount()
		}
	}
	return 0
}

// TestRunsOfDifferentCIsRunInParallel: the runs of two CIs do not wait for
// each other: both are running at the same time.
func TestRunsOfDifferentCIsRunInParallel(t *testing.T) {
	e := newWorkerEnv(t, 5, true)
	e.secondWorker(t)
	rec := newRecordingRuns(e.repo)
	rec.waitParallel = true
	e.exec.repo = rec
	ci1 := e.f.CI(t, e.f.OrgA, "", "wf-serial-par-1")
	ci2 := e.f.CI(t, e.f.OrgA, "", "wf-serial-par-2")
	r1 := e.startOn(t, ci1, nil, JSONMap{"type": "noop"})
	r2 := e.startOn(t, ci2, nil, JSONMap{"type": "noop"})
	for _, run := range []*Run{r1, r2} {
		if _, err := e.exec.Execute(e.ctx, e.f.OrgA, nil, run); err != nil {
			t.Fatal(err)
		}
	}
	for _, run := range []*Run{r1, r2} {
		if done := e.await(t, run.ID); done.Status != StatusSucceeded {
			t.Fatalf("run %s: %s, want succeeded", run.ID, done.Status)
		}
	}
	if _, maxActive := rec.snapshot(); maxActive != 2 {
		t.Errorf("runs of two CIs running at once: %d, want 2", maxActive)
	}
}

// TestWaitingApprovalDoesNotBlockCI: a run that waits for an approval
// does not hold its CI: a later run of the CI succeeds meanwhile; the
// approved run is queued again and succeeds.
func TestWaitingApprovalDoesNotBlockCI(t *testing.T) {
	e := newWorkerEnv(t, 5, true)
	ciID := e.f.CI(t, e.f.OrgA, "", "wf-serial-approval")
	waiting := e.startOn(t, ciID, nil, JSONMap{"type": "require_approval", "exclude_initiator": false}, JSONMap{"type": "noop"})
	if _, err := e.exec.Execute(e.ctx, e.f.OrgA, nil, waiting); err != nil {
		t.Fatal(err)
	}
	e.awaitStatus(t, waiting.ID, StatusWaitingApproval)
	later := e.startOn(t, ciID, nil, JSONMap{"type": "noop"})
	if _, err := e.exec.Execute(e.ctx, e.f.OrgA, nil, later); err != nil {
		t.Fatal(err)
	}
	if done := e.await(t, later.ID); done.Status != StatusSucceeded {
		t.Fatalf("later run of the CI: %s, want succeeded while the first waits", done.Status)
	}
	approved, err := e.exec.Approve(e.ctx, e.f.OrgA, waiting.ID, "approved", "ok")
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != StatusPending && approved.Status != StatusRunning && approved.Status != StatusSucceeded {
		t.Errorf("approved run: %s, want queued again", approved.Status)
	}
	done := e.await(t, waiting.ID)
	if done.Status != StatusSucceeded || len(done.Steps) != 2 {
		t.Fatalf("approved run: %s steps %+v, want succeeded with 2 steps", done.Status, done.Steps)
	}
	if done.Enqueues != 1 {
		t.Errorf("enqueues of the approved run: %d, want 1", done.Enqueues)
	}
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM job WHERE organization_id = $1 AND idempotency_key = $2 AND created_by = $3`,
		e.f.OrgA, RunJobKey(waiting.ID, 1), e.user).Scan(&n); err != nil || n != 1 {
		t.Errorf("job of the continuation: %d %v, want one by the initiator", n, err)
	}
}

// TestSerialSweepRequeuesStrandedRun: a pending run whose wake-up was lost
// (its predecessor ended without queueing it) is queued by the sweep once
// it waited more than five minutes; a run that waited less, or whose key
// is held by a running run, is not.
func TestSerialSweepRequeuesStrandedRun(t *testing.T) {
	e := newWorkerEnv(t, 5, true)
	bg := context.Background()
	ciID := e.f.CI(t, e.f.OrgA, "", "wf-serial-sweep")
	holder := e.startOn(t, ciID, nil, JSONMap{"type": "noop"})
	if _, err := e.f.Admin.Exec(bg, `UPDATE workflow_run SET status = 'running' WHERE id = $1`, holder.ID); err != nil {
		t.Fatal(err)
	}
	stranded := e.startOn(t, ciID, nil, JSONMap{"type": "noop"})
	if _, err := e.exec.Execute(e.ctx, e.f.OrgA, nil, stranded); err != nil {
		t.Fatal(err)
	}
	// The job of the run ends without effect while the key is held.
	if j := e.awaitJob(t, stranded.ID, jobs.StatusSucceeded); j.Status != jobs.StatusSucceeded {
		t.Fatalf("job of the blocked run: %s, want succeeded without effect", j.Status)
	}
	sweep := NewSerialSweep(e.exec, func(context.Context) ([]string, error) { return []string{e.f.OrgA}, nil })
	e.backdate(t, stranded.ID, 6*time.Minute)
	if n, err := sweep.Run(bg); err != nil || n != 0 {
		t.Fatalf("sweep while the key is held: %d %v, want nothing queued", n, err)
	}
	// The holder ends without queueing its successor (the lost wake-up).
	if _, err := e.f.Admin.Exec(bg, `UPDATE workflow_run SET status = 'succeeded', finished_at = now() WHERE id = $1`, holder.ID); err != nil {
		t.Fatal(err)
	}
	fresh := e.startOn(t, e.f.CI(t, e.f.OrgA, "", "wf-serial-sweep-fresh"), nil, JSONMap{"type": "noop"})
	n, err := sweep.Run(bg)
	if err != nil || n != 1 {
		var st string
		var age float64
		_ = e.f.Admin.QueryRow(bg, `SELECT status || ' ' || COALESCE(serial_key,''), EXTRACT(EPOCH FROM now()-updated_at)::float8 FROM workflow_run WHERE id=$1`, stranded.ID).Scan(&st, &age)
		t.Fatalf("sweep: %d %v, want the stranded run queued (%s, age %f)", n, err, st, age)
	}
	if done := e.await(t, stranded.ID); done.Status != StatusSucceeded || done.Enqueues != 1 {
		t.Fatalf("stranded run: %s enqueues %d, want succeeded after one requeue", done.Status, done.Enqueues)
	}
	if got, _ := e.repo.GetRun(e.ctx, e.f.OrgA, fresh.ID); got == nil || got.Status != StatusPending || got.Enqueues != 0 {
		t.Errorf("run that waited less than five minutes: %+v, want pending and not queued", got)
	}
	if spec := sweep.Schedule(); spec.Kind != SerialSweepKind || spec.Spec != "* * * * *" || spec.MaxCatchUp != 1 {
		t.Errorf("schedule: %s %q catch-up %d, want %s every minute with 1", spec.Kind, spec.Spec, spec.MaxCatchUp, SerialSweepKind)
	}
}

// TestSerialRunClosedWhenCILeavesJobScope: the CI of a run queued by a
// principal of client 1 is moved to client 2 before the job runs; the job
// does not see the run any more. The run is closed failed and frees its
// key, so the next run of the CI (started organization-wide) is executed.
func TestSerialRunClosedWhenCILeavesJobScope(t *testing.T) {
	e := newWorkerEnv(t, 2, true)
	bg := context.Background()
	scope := database.OrgWideScope(e.f.OrgA, e.user)
	scope.Clients = database.ScopeIDs(e.f.Client1)
	client1 := tenant.WithTenant(database.ContextWithTenantScope(bg, &scope),
		tenant.TenantInfo{OrganizationID: e.f.OrgA, ClientID: e.f.Client1, UserID: e.user})
	ciID := e.f.CI(t, e.f.OrgA, e.f.Client1, "wf-serial-moved")
	def := &Definition{OrganizationID: e.f.OrgA, Name: fmt.Sprintf("wf-serial-moved-%d", time.Now().UnixNano()), Trigger: JSONMap{"type": "manual"},
		Actions: []JSONMap{{"type": "noop"}}, Active: true}
	if err := e.repo.CreateDefinition(e.ctx, def); err != nil {
		t.Fatal(err)
	}
	first, err := e.exec.Start(client1, e.f.OrgA, def, "manual", JSONMap{"ci_id": ciID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.f.Admin.Exec(bg, `UPDATE ci SET client_id = $2 WHERE id = $1`, ciID, e.f.Client2); err != nil {
		t.Fatal(err)
	}
	next, err := e.exec.Start(e.ctx, e.f.OrgA, def, "manual", JSONMap{"ci_id": ciID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.exec.Execute(client1, e.f.OrgA, nil, first); err != nil {
		t.Fatal(err)
	}
	if _, err = e.exec.Execute(e.ctx, e.f.OrgA, nil, next); err != nil {
		t.Fatal(err)
	}
	if done := e.await(t, first.ID); done.Status != StatusFailed || done.LastError != runOutOfScopeText {
		t.Fatalf("run whose CI left the job scope: %s %q, want failed with %q", done.Status, done.LastError, runOutOfScopeText)
	}
	if done := e.await(t, next.ID); done.Status != StatusSucceeded {
		t.Fatalf("next run of the CI: %s %q, want succeeded", done.Status, done.LastError)
	}
}

// TestJobFailedClosesRunOutsideJobScope: the job of a run failed for good
// after the CI of the run moved to client 2, outside the client-1 scope of
// the job. Row level security (WP-1093) hides the run from that scope;
// JobFailed closes it in the organization-wide closingContext all the same,
// so it does not stay pending and block its CI.
func TestJobFailedClosesRunOutsideJobScope(t *testing.T) {
	e := newWorkerEnv(t, 2, false)
	bg := context.Background()
	scope := database.OrgWideScope(e.f.OrgA, e.user)
	scope.Clients = database.ScopeIDs(e.f.Client1)
	client1 := tenant.WithTenant(database.ContextWithTenantScope(bg, &scope),
		tenant.TenantInfo{OrganizationID: e.f.OrgA, ClientID: e.f.Client1, UserID: e.user})
	ciID := e.f.CI(t, e.f.OrgA, e.f.Client1, "wf-lost-moved")
	def := &Definition{OrganizationID: e.f.OrgA, Name: fmt.Sprintf("wf-lost-moved-%d", time.Now().UnixNano()), Trigger: JSONMap{"type": "manual"},
		Actions: []JSONMap{{"type": "noop"}}, Active: true}
	if err := e.repo.CreateDefinition(e.ctx, def); err != nil {
		t.Fatal(err)
	}
	run, err := e.exec.Start(client1, e.f.OrgA, def, "manual", JSONMap{"ci_id": ciID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.exec.Execute(client1, e.f.OrgA, nil, run); err != nil {
		t.Fatal(err)
	}
	if _, err = e.f.Admin.Exec(bg, `UPDATE ci SET client_id = $2 WHERE id = $1`, ciID, e.f.Client2); err != nil {
		t.Fatal(err)
	}
	if _, err = e.repo.GetRun(client1, e.f.OrgA, run.ID); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("run in the job scope after the move: %v, want ErrRunNotFound", err)
	}
	NewWorker(e.exec).JobFailed(bg, e.job(t, run.ID))
	got, err := e.repo.GetRunForJob(e.ctx, e.f.OrgA, run.ID)
	if err != nil || got.Status != StatusFailed || got.LastError != runLostText {
		t.Fatalf("run after the failed job: %+v %v, want failed with %q", got, err, runLostText)
	}
}

// TestSerialSweepIgnoresFreshHead: the sweep measures the age on the head
// of a key, the run it would queue. A head queued just now keeps its job,
// whatever a later run of the key waited; once the head itself waited too
// long, the key is stranded.
func TestSerialSweepIgnoresFreshHead(t *testing.T) {
	e := newWorkerEnv(t, 5, false)
	bg := context.Background()
	ciID := e.f.CI(t, e.f.OrgA, "", "wf-serial-fresh-head")
	head := e.startOn(t, ciID, nil, JSONMap{"type": "noop"})
	e.startOn(t, ciID, nil, JSONMap{"type": "noop"})
	tail := e.startOn(t, ciID, nil, JSONMap{"type": "noop"})
	e.backdate(t, tail.ID, 10*time.Minute)
	sweep := NewSerialSweep(e.exec, func(context.Context) ([]string, error) { return []string{e.f.OrgA}, nil })
	if n, err := sweep.Run(bg); err != nil || n != 0 {
		t.Fatalf("sweep with a fresh head: %d %v, want nothing queued", n, err)
	}
	if got, err := e.repo.GetRun(e.ctx, e.f.OrgA, head.ID); err != nil || got.Enqueues != 0 {
		t.Fatalf("head after the sweep: %+v %v, want enqueues 0", got, err)
	}
	e.backdate(t, head.ID, 6*time.Minute)
	if n, err := sweep.Run(bg); err != nil || n != 1 {
		t.Fatalf("sweep with a stale head: %d %v, want the key queued", n, err)
	}
	if got, err := e.repo.GetRun(e.ctx, e.f.OrgA, head.ID); err != nil || got.Enqueues != 1 {
		t.Errorf("head after the second sweep: %+v %v, want enqueues 1", got, err)
	}
}
