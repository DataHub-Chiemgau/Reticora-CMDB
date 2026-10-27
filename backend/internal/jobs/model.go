// Package jobs is the generic job model of JOB-01 (REP-01): a persistent
// job per long operation with status, progress, parameters, result and
// error, idempotent creation, retry with backoff and a timeout per kind.
// Specialised job tables (export, discovery) register their jobs here
// through the Tracker adapter and keep their own tables.
package jobs

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// Status of a job.
type Status string

// Job states of JOB-01.
const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// Terminal reports whether no further transition leaves the status.
func (s Status) Terminal() bool {
	return s == StatusSucceeded || s == StatusFailed || s == StatusCancelled
}

// transitions are the allowed status changes: a queued job starts or is
// cancelled; a running job ends, is cancelled or goes back to the queue
// for a retry.
var transitions = map[Status][]Status{
	StatusQueued:  {StatusRunning, StatusCancelled},
	StatusRunning: {StatusSucceeded, StatusFailed, StatusCancelled, StatusQueued},
}

// CanTransition reports whether a job may change from one status to another.
func CanTransition(from, to Status) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// Job is one row of the job table.
type Job struct {
	ID             string          `json:"id"`
	OrganizationID string          `json:"organization_id,omitempty"`
	Kind           string          `json:"kind"`
	Status         Status          `json:"status"`
	Progress       int             `json:"progress"`
	Params         json.RawMessage `json:"params"`
	Result         json.RawMessage `json:"result,omitempty"`
	Error          string          `json:"error,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
	// CreatedBy is a surrogate of the creating user, not a reference.
	CreatedBy      string     `json:"created_by,omitempty"`
	Attempts       int        `json:"attempts"`
	MaxAttempts    int        `json:"max_attempts"`
	RunAfter       time.Time  `json:"run_after"`
	TimeoutSeconds int        `json:"timeout_seconds,omitempty"`
	RefTable       string     `json:"ref_table,omitempty"`
	RefID          string     `json:"ref_id,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	// RevertedAt and RevertedBy record the revert of a succeeded job
	// (POST /jobs/{id}/revert, F-329); RevertedBy is a surrogate of the
	// user like CreatedBy. Both are empty for a job that was not reverted.
	RevertedAt *time.Time `json:"reverted_at,omitempty"`
	RevertedBy string     `json:"reverted_by,omitempty"`
	// Scope is the scope snapshot of the job; nil for a job without one.
	Scope *Scope `json:"-"`
	// Cancellable reports whether DELETE /jobs/{id} would cancel the job
	// now (Service.Cancellable); the job API sets it, it is not stored.
	Cancellable bool `json:"cancellable"`
}

// Scope is the scope snapshot of a job (TEN-09, F-163): the client, site
// and team scope its creator had when queueing it, or for a tracked job the
// snapshot of the row it mirrors (export: the export job). A nil dimension
// is the whole organization, an empty one no id. A job without snapshot
// (system jobs, jobs from before migration 000154) belongs to the whole
// organization.
type Scope struct {
	Clients []string
	Sites   []string
	Teams   []string
}

// ScopeOf returns the snapshot of a tenant scope.
func ScopeOf(s *database.TenantScope) *Scope {
	set := func(d database.ScopeSet) []string {
		if d.IsAll() {
			return nil
		}
		if ids := d.IDs(); ids != nil {
			return ids
		}
		return []string{}
	}
	return &Scope{Clients: set(s.Clients), Sites: set(s.Sites), Teams: set(s.Teams)}
}

// VisibleTo reports whether a principal with the tenant scope s sees the
// job, the rule of job_isolation (migration 000154) and of the export
// creator rule (ownExport): the job belongs to the organization of s, in
// every dimension s is org-wide or the job's snapshot lies completely in
// s, and an export job only to its creator. A job without snapshot is
// visible to org-wide principals only (E-09).
func (j *Job) VisibleTo(s *database.TenantScope) bool {
	if s == nil || !strings.EqualFold(j.OrganizationID, s.OrgID) {
		return false
	}
	if j.RefTable == "export_job" && (s.UserID == "" || !strings.EqualFold(j.CreatedBy, s.UserID)) {
		return false
	}
	snapshot := j.Scope
	if snapshot == nil {
		snapshot = &Scope{}
	}
	within := func(ids []string, set database.ScopeSet) bool {
		if set.IsAll() {
			return true
		}
		if ids == nil {
			return false
		}
		allowed := set.IDs()
		for _, id := range ids {
			if !slices.ContainsFunc(allowed, func(a string) bool { return strings.EqualFold(a, id) }) {
				return false
			}
		}
		return true
	}
	return within(snapshot.Clients, s.Clients) && within(snapshot.Sites, s.Sites) && within(snapshot.Teams, s.Teams)
}

// Kind configures the jobs of one kind: how often they are tried, how long
// to wait before the next attempt and how long one attempt may run.
type Kind struct {
	Name        string
	MaxAttempts int
	Timeout     time.Duration
	// Backoff returns the delay before attempt n+1 after n failed attempts.
	Backoff func(attempts int) time.Duration
	// Cancellable: a running job of the kind can be cancelled, its worker
	// stops at its next checkpoint (JOB-03, F-162). A running job of any
	// other kind runs to its end and a cancel is refused
	// (ErrNotCancellable); a queued job of every kind can be cancelled.
	Cancellable bool
}

// ExponentialBackoff doubles base per failed attempt up to limit.
func ExponentialBackoff(base, limit time.Duration) func(int) time.Duration {
	return func(attempts int) time.Duration {
		d := base
		for i := 1; i < attempts && d < limit; i++ {
			d *= 2
		}
		return min(d, limit)
	}
}

// Filter narrows a job list.
type Filter struct {
	Kind   string
	Status Status
}

// Errors of the job model.
var (
	ErrNotFound = errors.New("job not found")
	// ErrTransition: the job is not in a status that allows the change.
	ErrTransition = errors.New("job status does not allow this change")
	// ErrNotCancellable: the job runs and its kind cannot be cancelled
	// while running (Kind.Cancellable).
	ErrNotCancellable = errors.New("the job kind cannot be cancelled while running")
	// ErrUnknownKind: no kind of that name is registered.
	ErrUnknownKind = errors.New("unknown job kind")
	// ErrAlreadyReverted: the job's revert is recorded already (F-329).
	ErrAlreadyReverted = errors.New("the job is already reverted")
)
