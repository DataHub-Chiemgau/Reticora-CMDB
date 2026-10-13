// Package jobs is the generic job model of JOB-01 (REP-01): a persistent
// job per long operation with status, progress, parameters, result and
// error, idempotent creation, retry with backoff and a timeout per kind.
// Specialised job tables (export, discovery) register their jobs here
// through the Tracker adapter and keep their own tables.
package jobs

import (
	"encoding/json"
	"errors"
	"time"
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
}

// Kind configures the jobs of one kind: how often they are tried, how long
// to wait before the next attempt and how long one attempt may run.
type Kind struct {
	Name        string
	MaxAttempts int
	Timeout     time.Duration
	// Backoff returns the delay before attempt n+1 after n failed attempts.
	Backoff func(attempts int) time.Duration
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
	// ErrUnknownKind: no kind of that name is registered.
	ErrUnknownKind = errors.New("unknown job kind")
)
