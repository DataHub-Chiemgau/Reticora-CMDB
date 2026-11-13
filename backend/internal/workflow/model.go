// Package workflow provides workflow definitions, runs, and deterministic execution.
package workflow

import "time"

type JSONMap map[string]any

type Definition struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	Name           string    `json:"name"`
	Description    string    `json:"description,omitempty"`
	Trigger        JSONMap   `json:"trigger"`
	Conditions     []JSONMap `json:"conditions"`
	Actions        []JSONMap `json:"actions"`
	Active         bool      `json:"active"`
	// Version is the number of the current version of trigger, conditions
	// and actions (WFL-02, E-228); it is the ETag of the definition.
	Version int `json:"version"`
	// CurrentVersionID names the stored version the fields above copy.
	CurrentVersionID string    `json:"current_version_id"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// DefinitionVersion is one immutable revision of the trigger, conditions
// and actions of a definition (WFL-02, E-228 option 1). A run executes the
// version it started with.
type DefinitionVersion struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	WorkflowID     string    `json:"workflow_id"`
	Version        int       `json:"version"`
	Trigger        JSONMap   `json:"trigger"`
	Conditions     []JSONMap `json:"conditions"`
	Actions        []JSONMap `json:"actions"`
	CreatedBy      string    `json:"created_by,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type Run struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	WorkflowID     string `json:"workflow_id"`
	// DefinitionVersionID is the version of the definition the run
	// executes (WFL-02); a later change of the definition does not touch it.
	DefinitionVersionID string     `json:"definition_version_id"`
	Status              string     `json:"status"`
	Trigger             string     `json:"trigger"`
	Context             JSONMap    `json:"context"`
	StartedAt           time.Time  `json:"started_at"`
	FinishedAt          *time.Time `json:"finished_at,omitempty"`
	// Attempts counts the attempts of the job workflow.run that executed
	// the run (WFL-02, WP-1088); 0 for a run executed in the request.
	Attempts int `json:"attempts"`
	// NextAttemptAt is when the job retries the run after a failed
	// attempt; absent otherwise.
	NextAttemptAt *time.Time `json:"next_attempt_at,omitempty"`
	// LastError is the sanitized error of the last failed attempt.
	LastError string `json:"last_error,omitempty"`
	// InitiatedBy is the user who started the run; the worker executes
	// the run as this user (submitted_by, reporter). Empty without one.
	InitiatedBy string `json:"initiated_by,omitempty"`
	// CIID is the CI the run refers to (ci_id or ci.id of the context,
	// read in the scope of the trigger, WP-1089); empty without one.
	CIID string `json:"ci_id,omitempty"`
	// SerialKey is "ci:<ci_id>" for a run with a CI: the runs of one key
	// are executed one after the other (EVT-01, E-227). Empty otherwise.
	SerialKey string `json:"-"`
	// EntitySeq is the sequence number of the event of the object that
	// started the run (WP-1090); the runs of a key are executed in this
	// order. Nil without one.
	EntitySeq *int64 `json:"-"`
	// Enqueues counts how often the run was queued as the job
	// workflow.run; only the job of the last enqueue executes it.
	Enqueues int `json:"-"`
	// TriggerEventID is the event that started the run (WP-1090): one run
	// per definition version and event. Empty for other triggers.
	TriggerEventID string `json:"-"`
	// TriggerDepth counts the runs started by events of runs before this
	// one (loop protection, at most triggerMaxDepth).
	TriggerDepth int       `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Steps        []Step    `json:"steps,omitempty"`
}

// RunClaim is the decision of Repository.ClaimRun about a job of a run
// (WP-1089 AK2).
type RunClaim struct {
	// Claimed reports that the job executes the run now: it is the job of
	// the last enqueue, and the run is running already (a retry) or the
	// oldest pending run of its serial key while no other run of the key
	// is running. Otherwise the job ends without effect.
	Claimed bool
	// Started reports that the claim moved the run from pending to
	// running; Waited is then the time since the run was created.
	Started bool
	Waited  time.Duration
}

// RunAttempt is the state of the job attempt a worker records on a run.
type RunAttempt struct {
	Attempts      int
	NextAttemptAt *time.Time
	LastError     string
}

type Step struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	RunID          string    `json:"run_id"`
	StepIndex      int       `json:"step_index"`
	ActionType     string    `json:"action_type"`
	Status         string    `json:"status"`
	Input          JSONMap   `json:"input"`
	Output         JSONMap   `json:"output"`
	Error          string    `json:"error,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type CreateDefinitionRequest struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Trigger     JSONMap   `json:"trigger"`
	Conditions  []JSONMap `json:"conditions,omitempty"`
	Actions     []JSONMap `json:"actions"`
	Active      *bool     `json:"active,omitempty"`
}
type UpdateDefinitionRequest struct {
	Name        *string   `json:"name,omitempty"`
	Description *string   `json:"description,omitempty"`
	Trigger     JSONMap   `json:"trigger,omitempty"`
	Conditions  []JSONMap `json:"conditions,omitempty"`
	Actions     []JSONMap `json:"actions,omitempty"`
	Active      *bool     `json:"active,omitempty"`
	// IfMatch is the version of the If-Match header (API-07); nil without
	// the header, api.IfMatchAny for "*".
	IfMatch *int64 `json:"-"`
	// CreatedBy is the user a new version names (empty: none).
	CreatedBy string `json:"-"`
}

type TriggerRequest struct {
	Trigger string  `json:"trigger"`
	Context JSONMap `json:"context"`
}
type ApprovalRequest struct {
	Decision string `json:"decision"`
	Comment  string `json:"comment,omitempty"`
}

// RunFilter narrows GET /api/v1/workflow-runs (WP-1092 AK3): empty fields
// do not filter; From and To bound created_at (inclusive, exclusive).
type RunFilter struct {
	WorkflowID string
	CIID       string
	Status     string
	Trigger    string
	From       *time.Time
	To         *time.Time
}

const (
	StatusPending         = "pending"
	StatusRunning         = "running"
	StatusWaitingApproval = "waiting_approval"
	StatusSucceeded       = "succeeded"
	StatusFailed          = "failed"
	StatusCancelled       = "cancelled"
	// StatusWaitingInput is the state of a run that waits for a form
	// (request_form, WP-1102); it is cancellable like a waiting approval.
	StatusWaitingInput = "waiting_input"
)

// runStatuses are the states a run can have; the status filter of the run
// list accepts only these (422 otherwise).
var runStatuses = []string{StatusPending, StatusRunning, StatusWaitingApproval, StatusWaitingInput, StatusSucceeded, StatusFailed, StatusCancelled}

// endedStatus reports whether status ends a run: it is changed only by
// RetryRun (WP-1092).
func endedStatus(status string) bool {
	return status == StatusSucceeded || status == StatusFailed || status == StatusCancelled
}

// approvalRejectedText is the error of an approval step that was rejected.
const approvalRejectedText = "approval rejected"

// cancellableStatuses are the states from which POST
// /api/v1/workflow-runs/{id}/cancel ends a run (WP-1092 AK1): it waits for
// its job, an approval or a form. A running run and an ended one are 409.
var cancellableStatuses = []string{StatusPending, StatusWaitingApproval, StatusWaitingInput}
