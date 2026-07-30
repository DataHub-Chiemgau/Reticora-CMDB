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
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type Run struct {
	ID             string     `json:"id"`
	OrganizationID string     `json:"organization_id"`
	WorkflowID     string     `json:"workflow_id"`
	Status         string     `json:"status"`
	Trigger        string     `json:"trigger"`
	Context        JSONMap    `json:"context"`
	StartedAt      time.Time  `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	Steps          []Step     `json:"steps,omitempty"`
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
