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
	InitiatedBy string    `json:"initiated_by,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Steps       []Step    `json:"steps,omitempty"`
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

const (
	StatusPending         = "pending"
	StatusRunning         = "running"
	StatusWaitingApproval = "waiting_approval"
	StatusSucceeded       = "succeeded"
	StatusFailed          = "failed"
	StatusCancelled       = "cancelled"
)
