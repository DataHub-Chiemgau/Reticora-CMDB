package jobs

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
)

// Scheduler of the system jobs (JOB-02, OPS-03): every replica runs one,
// one of them leads. The leader holds a session advisory lock; it alone
// looks for due slots and runs them, each kind additionally under an
// advisory lock of its own, so a run of a former leader that lost its
// connection never overlaps a run of the new one. The slot a kind last ran
// for is stored, so a new leader catches up on missed slots — at most
// MaxCatchUp per kind, the newest ones.

// Schedule defaults.
const (
	DefaultTickInterval = 15 * time.Second
	DefaultMaxCatchUp   = 1
	schedulerLockKey    = "reticora.scheduler"
)

// Schedule is one registered system job.
type Schedule struct {
	Kind string
	// Spec is a cron expression or "@every <duration>" (ParseSpec).
	Spec string
	Run  func(ctx context.Context) error
	// MaxCatchUp bounds how many missed slots run after a pause.
	MaxCatchUp int
	// Timeout bounds one run; 0 means DefaultTimeout.
	Timeout time.Duration

	spec Spec
}

// Lock is a held advisory lock.
type Lock interface {
	// Alive reports whether the lock is still held (its connection works).
	Alive(ctx context.Context) bool
	Release()
}

// Locker hands out advisory locks; TryLock returns nil when another
// holder has the key.
type Locker interface {
	TryLock(ctx context.Context, key string) (Lock, error)
}

// RunState is the stored state of one kind.
type RunState struct {
	LastDue time.Time
	Error   string
}

// StateStore persists the slot each kind last ran for.
type StateStore interface {
	// LastDue returns the slot the kind last ran for; ok is false for a
	// kind that never ran.
	LastDue(ctx context.Context, kind string) (time.Time, bool, error)
	// Start records that the kind runs for the slot.
	Start(ctx context.Context, kind string, due, at time.Time, runner string) error
	// Finish records the end of the run for the slot.
	Finish(ctx context.Context, kind string, due, at time.Time, runErr error) error
}

// Scheduler runs the registered schedules on the leading replica.
type Scheduler struct {
	locker Locker
	state  StateStore
	// Runner names this replica in the state.
	Runner string
	// Interval is how often the scheduler looks for due slots.
	Interval time.Duration
	now      func() time.Time

	mu        sync.Mutex
	schedules []*Schedule
	leader    Lock
}

// NewScheduler creates a scheduler on a locker and a state store.
func NewScheduler(locker Locker, state StateStore) *Scheduler {
	host, _ := os.Hostname()
	return &Scheduler{
		locker: locker, state: state, Interval: DefaultTickInterval,
		Runner: host + "/" + strconv.Itoa(os.Getpid()),
		now:    func() time.Time { return time.Now().UTC() },
	}
}

// WithClock replaces the clock (tests).
func (s *Scheduler) WithClock(now func() time.Time) *Scheduler {
	s.now = now
	return s
}

// Register adds a schedule; an invalid spec or a duplicate kind is an
// error.
func (s *Scheduler) Register(in *Schedule) error {
	sch := *in
	spec, err := ParseSpec(sch.Spec)
	if err != nil {
		return fmt.Errorf("schedule %s: %w", sch.Kind, err)
	}
	if sch.Kind == "" || sch.Run == nil {
		return fmt.Errorf("schedule %q needs a kind and a run function", sch.Kind)
	}
	if sch.MaxCatchUp < 1 {
		sch.MaxCatchUp = DefaultMaxCatchUp
	}
	if sch.Timeout <= 0 {
		sch.Timeout = DefaultTimeout
	}
	sch.spec = spec
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.schedules {
		if existing.Kind == sch.Kind {
			return fmt.Errorf("schedule %s registered twice", sch.Kind)
		}
	}
	s.schedules = append(s.schedules, &sch)
	return nil
}

// Kinds lists the registered kinds.
func (s *Scheduler) Kinds() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.schedules))
	for i, sch := range s.schedules {
		out[i] = sch.Kind
	}
	return out
}

// IsLeader reports whether this replica leads.
func (s *Scheduler) IsLeader() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leader != nil
}

// Run ticks until ctx ends and then gives up the leadership.
func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.Interval)
	defer ticker.Stop()
	defer s.resign()
	for {
		s.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Tick takes or checks the leadership and, as leader, runs the due slots.
// It returns how many runs it made.
func (s *Scheduler) Tick(ctx context.Context) int {
	if !s.lead(ctx) {
		return 0
	}
	s.mu.Lock()
	schedules := append([]*Schedule(nil), s.schedules...)
	s.mu.Unlock()
	runs, failures := 0, 0
	for _, sch := range schedules {
		n, err := s.runDue(ctx, sch)
		runs += n
		if err != nil {
			failures++
			slog.Error("scheduled job", "kind", sch.Kind, "error", err)
		}
	}
	observability.ObserveWorkerRun("scheduler", failures)
	return runs
}

// lead keeps or takes the leader lock.
func (s *Scheduler) lead(ctx context.Context) bool {
	s.mu.Lock()
	leader := s.leader
	s.mu.Unlock()
	if leader != nil {
		if leader.Alive(ctx) {
			return true
		}
		slog.Warn("scheduler lost its leader lock", "runner", s.Runner)
		s.resign()
	}
	lock, err := s.locker.TryLock(ctx, schedulerLockKey)
	if err != nil {
		slog.Warn("scheduler leader lock", "error", err)
		return false
	}
	if lock == nil {
		return false
	}
	slog.Info("scheduler leads", "runner", s.Runner)
	s.mu.Lock()
	s.leader = lock
	s.mu.Unlock()
	return true
}

func (s *Scheduler) resign() {
	s.mu.Lock()
	leader := s.leader
	s.leader = nil
	s.mu.Unlock()
	if leader != nil {
		leader.Release()
	}
}

// runDue runs the due slots of one schedule.
func (s *Scheduler) runDue(ctx context.Context, sch *Schedule) (int, error) {
	now := s.now()
	last, ok, err := s.state.LastDue(ctx, sch.Kind)
	if err != nil {
		return 0, err
	}
	if !ok {
		// A new kind starts with its next slot.
		return 0, s.state.Start(ctx, sch.Kind, now, now, s.Runner)
	}
	slots, skipped := sch.spec.Slots(last, now, sch.MaxCatchUp)
	if len(slots) == 0 {
		return 0, nil
	}
	if skipped > 0 {
		slog.Warn("scheduler skips missed slots", "kind", sch.Kind, "skipped", skipped, "catch_up", len(slots))
	}
	lock, err := s.locker.TryLock(ctx, schedulerLockKey+"."+sch.Kind)
	if err != nil {
		return 0, err
	}
	if lock == nil {
		// A run of a former leader still holds the kind.
		return 0, nil
	}
	defer lock.Release()
	runs := 0
	for _, due := range slots {
		if startErr := s.state.Start(ctx, sch.Kind, due, s.now(), s.Runner); startErr != nil {
			return runs, startErr
		}
		runCtx, cancel := context.WithTimeout(context.WithValue(ctx, slotKey{}, due), sch.Timeout)
		runErr := sch.Run(runCtx)
		cancel()
		runs++
		if finishErr := s.state.Finish(ctx, sch.Kind, due, s.now(), runErr); finishErr != nil {
			return runs, finishErr
		}
		if runErr != nil {
			return runs, runErr
		}
	}
	return runs, nil
}

type slotKey struct{}

// SlotFromContext returns the slot a scheduled run is for.
func SlotFromContext(ctx context.Context) (time.Time, bool) {
	t, ok := ctx.Value(slotKey{}).(time.Time)
	return t, ok
}

// MemoryLocker is a process-local Locker (tests and --no-db mode).
type MemoryLocker struct {
	mu   sync.Mutex
	held map[string]bool
}

// NewMemoryLocker creates an empty memory locker.
func NewMemoryLocker() *MemoryLocker { return &MemoryLocker{held: map[string]bool{}} }

// TryLock implements Locker.
func (l *MemoryLocker) TryLock(_ context.Context, key string) (Lock, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held[key] {
		return nil, nil
	}
	l.held[key] = true
	return &memoryLock{l: l, key: key}, nil
}

type memoryLock struct {
	l    *MemoryLocker
	key  string
	once sync.Once
}

func (m *memoryLock) Alive(context.Context) bool { return true }
func (m *memoryLock) Release() {
	m.once.Do(func() {
		m.l.mu.Lock()
		defer m.l.mu.Unlock()
		delete(m.l.held, m.key)
	})
}

// MemoryStateStore keeps the state in memory.
type MemoryStateStore struct {
	mu    sync.Mutex
	state map[string]RunState
}

// NewMemoryStateStore creates an empty memory state store.
func NewMemoryStateStore() *MemoryStateStore {
	return &MemoryStateStore{state: map[string]RunState{}}
}

// LastDue implements StateStore.
func (m *MemoryStateStore) LastDue(_ context.Context, kind string) (time.Time, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.state[kind]
	return st.LastDue, ok, nil
}

// Start implements StateStore.
func (m *MemoryStateStore) Start(_ context.Context, kind string, due, _ time.Time, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state[kind] = RunState{LastDue: due}
	return nil
}

// Finish implements StateStore.
func (m *MemoryStateStore) Finish(_ context.Context, kind string, due, _ time.Time, runErr error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := RunState{LastDue: due}
	if runErr != nil {
		st.Error = runErr.Error()
	}
	m.state[kind] = st
	return nil
}

// PGLocker takes session advisory locks on dedicated connections of the
// pool; a lock ends with Release or with its connection.
type PGLocker struct{ pool *pgxpool.Pool }

// NewPGLocker creates a locker on pool.
func NewPGLocker(pool *pgxpool.Pool) *PGLocker { return &PGLocker{pool: pool} }

// lockKey maps a key to the bigint of pg_try_advisory_lock.
func lockKey(key string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return int64(h.Sum64()) //nolint:gosec // a hash, its sign does not matter
}

// TryLock implements Locker.
func (l *PGLocker) TryLock(ctx context.Context, key string) (Lock, error) {
	conn, err := l.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire lock connection: %w", err)
	}
	var ok bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, lockKey(key)).Scan(&ok); err != nil {
		conn.Release()
		return nil, fmt.Errorf("advisory lock %s: %w", key, err)
	}
	if !ok {
		conn.Release()
		return nil, nil
	}
	return &pgLock{conn: conn, key: lockKey(key)}, nil
}

type pgLock struct {
	mu   sync.Mutex
	conn *pgxpool.Conn
	key  int64
}

func (p *pgLock) Alive(ctx context.Context) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn == nil {
		return false
	}
	// The lock lives as long as the session that took it and is only
	// given up by Release: a working connection still holds it.
	_, err := p.conn.Exec(ctx, `SELECT 1`)
	return err == nil
}

func (p *pgLock) Release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn == nil {
		return
	}
	// background cleanup: the caller's context may be done
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := p.conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, p.key); err != nil {
		// The session may be gone; closing the connection ends the lock.
		_ = p.conn.Conn().Close(ctx)
	}
	p.conn.Release()
	p.conn = nil
}

// PGStateStore stores the scheduler state in scheduler_run (operator path).
type PGStateStore struct{ pool *pgxpool.Pool }

// NewPGStateStore creates a state store on pool.
func NewPGStateStore(pool *pgxpool.Pool) *PGStateStore { return &PGStateStore{pool: pool} }

// LastDue implements StateStore.
func (p *PGStateStore) LastDue(ctx context.Context, kind string) (time.Time, bool, error) {
	var due time.Time
	err := database.WithOperator(ctx, p.pool, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT last_due_at FROM scheduler_run WHERE kind = $1`, kind).Scan(&due)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("read scheduler state: %w", err)
	}
	return due.UTC(), true, nil
}

// Start implements StateStore.
func (p *PGStateStore) Start(ctx context.Context, kind string, due, at time.Time, runner string) error {
	return database.WithOperator(ctx, p.pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO scheduler_run (kind, last_due_at, last_started_at, runner, updated_at)
			VALUES ($1, $2, $3, $4, now())
			ON CONFLICT (kind) DO UPDATE SET last_due_at = EXCLUDED.last_due_at, last_started_at = EXCLUDED.last_started_at,
				last_finished_at = NULL, last_error = NULL, runner = EXCLUDED.runner, updated_at = now()`,
			kind, due.UTC(), at.UTC(), runner)
		if err != nil {
			return fmt.Errorf("record scheduler start: %w", err)
		}
		return nil
	})
}

// Finish implements StateStore.
func (p *PGStateStore) Finish(ctx context.Context, kind string, due, at time.Time, runErr error) error {
	var msg *string
	if runErr != nil {
		m := runErr.Error()
		msg = &m
	}
	return database.WithOperator(ctx, p.pool, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE scheduler_run SET last_finished_at = $3, last_error = $4, updated_at = now()
			WHERE kind = $1 AND last_due_at = $2`, kind, due.UTC(), at.UTC(), msg)
		if err != nil {
			return fmt.Errorf("record scheduler finish: %w", err)
		}
		return nil
	})
}
