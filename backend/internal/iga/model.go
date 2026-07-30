// Package iga implements identity governance and active provisioning.
package iga

import "time"

const (
	ConnectorTypeSCIM  = "scim"
	ConnectorTypeRelay = "relay"

	TaskStatusPending   = "pending"
	TaskStatusRunning   = "running"
	TaskStatusSucceeded = "succeeded"
	TaskStatusFailed    = "failed"
	TaskStatusCancelled = "cancelled"

	TaskActionCreateAccount     = "create_account"
	TaskActionUpdateAccount     = "update_account"
	TaskActionDisableAccount    = "disable_account"
	TaskActionDeleteAccount     = "delete_account"
	TaskActionAddGroupMember    = "add_group_member"
	TaskActionRemoveGroupMember = "remove_group_member"

	ReviewDecisionApprove = "approve"
	ReviewDecisionRevoke  = "revoke"
)

type JSONMap map[string]any

type ConnectorCapabilities struct {
	CreateAccount   bool `json:"create_account"`
	UpdateAccount   bool `json:"update_account"`
	DisableAccount  bool `json:"disable_account"`
	DeleteAccount   bool `json:"delete_account"`
	ReadAccounts    bool `json:"read_accounts"`
	GroupMembership bool `json:"group_membership"`
}

type ConnectorConfig struct {
	ID             string                `json:"id"`
	OrganizationID string                `json:"organization_id"`
	Name           string                `json:"name"`
	Type           string                `json:"type"`
	BaseURL        string                `json:"base_url,omitempty"`
	CredentialID   string                `json:"credential_id,omitempty"`
	CollectorID    string                `json:"collector_id,omitempty"`
	Capabilities   ConnectorCapabilities `json:"capabilities"`
	Config         JSONMap               `json:"config,omitempty"`
	Status         string                `json:"status"`
	LastSyncAt     *time.Time            `json:"last_sync_at,omitempty"`
	CreatedAt      time.Time             `json:"created_at"`
	UpdatedAt      time.Time             `json:"updated_at"`
}

type CreateConnectorRequest struct {
	Name         string                `json:"name"`
	Type         string                `json:"type"`
	BaseURL      string                `json:"base_url,omitempty"`
	CredentialID string                `json:"credential_id,omitempty"`
	Secret       JSONMap               `json:"secret,omitempty"`
	CollectorID  string                `json:"collector_id,omitempty"`
	Capabilities ConnectorCapabilities `json:"capabilities,omitempty"`
	Config       JSONMap               `json:"config,omitempty"`
}

type UpdateConnectorRequest struct {
	Name         *string                `json:"name,omitempty"`
	BaseURL      *string                `json:"base_url,omitempty"`
	CredentialID *string                `json:"credential_id,omitempty"`
	Secret       JSONMap                `json:"secret,omitempty"`
	CollectorID  *string                `json:"collector_id,omitempty"`
	Capabilities *ConnectorCapabilities `json:"capabilities,omitempty"`
	Config       JSONMap                `json:"config,omitempty"`
	Status       *string                `json:"status,omitempty"`
}

type ProvisioningTask struct {
	ID             string     `json:"id"`
	OrganizationID string     `json:"organization_id"`
	ConnectorID    string     `json:"connector_id"`
	UserID         string     `json:"user_id,omitempty"`
	ExternalID     string     `json:"external_id,omitempty"`
	Action         string     `json:"action"`
	Status         string     `json:"status"`
	Payload        JSONMap    `json:"payload"`
	Result         JSONMap    `json:"result,omitempty"`
	Error          string     `json:"error,omitempty"`
	Attempts       int        `json:"attempts"`
	MaxAttempts    int        `json:"max_attempts"`
	NextRunAt      time.Time  `json:"next_run_at"`
	LastRunAt      *time.Time `json:"last_run_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type CreateTaskRequest struct {
	ConnectorID string  `json:"connector_id"`
	UserID      string  `json:"user_id,omitempty"`
	ExternalID  string  `json:"external_id,omitempty"`
	Action      string  `json:"action"`
	Payload     JSONMap `json:"payload,omitempty"`
}

type LifecyclePolicy struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	Name           string    `json:"name"`
	Event          string    `json:"event"`
	Priority       int       `json:"priority"`
	Active         bool      `json:"active"`
	Conditions     JSONMap   `json:"conditions,omitempty"`
	Actions        []JSONMap `json:"actions"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type CreateLifecyclePolicyRequest struct {
	Name       string    `json:"name"`
	Event      string    `json:"event"`
	Priority   int       `json:"priority"`
	Active     *bool     `json:"active,omitempty"`
	Conditions JSONMap   `json:"conditions,omitempty"`
	Actions    []JSONMap `json:"actions"`
}

type IdentityChange struct {
	Event      string  `json:"event"`
	UserID     string  `json:"user_id"`
	Attributes JSONMap `json:"attributes,omitempty"`
}

type AccessRequest struct {
	ID              string     `json:"id"`
	OrganizationID  string     `json:"organization_id"`
	RequesterID     string     `json:"requester_id"`
	SubjectUserID   string     `json:"subject_user_id"`
	ConnectorID     string     `json:"connector_id,omitempty"`
	Entitlement     string     `json:"entitlement"`
	Reason          string     `json:"reason,omitempty"`
	Status          string     `json:"status"`
	WorkflowRunID   string     `json:"workflow_run_id,omitempty"`
	DecisionBy      string     `json:"decision_by,omitempty"`
	DecisionComment string     `json:"decision_comment,omitempty"`
	DecidedAt       *time.Time `json:"decided_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type CreateAccessRequest struct {
	SubjectUserID string `json:"subject_user_id"`
	ConnectorID   string `json:"connector_id,omitempty"`
	Entitlement   string `json:"entitlement"`
	Reason        string `json:"reason,omitempty"`
}

type DecisionRequest struct {
	Decision string `json:"decision"`
	Comment  string `json:"comment,omitempty"`
}

type AccessReview struct {
	ID             string     `json:"id"`
	OrganizationID string     `json:"organization_id"`
	Name           string     `json:"name"`
	Description    string     `json:"description,omitempty"`
	Status         string     `json:"status"`
	DueAt          *time.Time `json:"due_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type AccessReviewItem struct {
	ID             string     `json:"id"`
	OrganizationID string     `json:"organization_id"`
	ReviewID       string     `json:"review_id"`
	UserID         string     `json:"user_id"`
	ConnectorID    string     `json:"connector_id,omitempty"`
	Entitlement    string     `json:"entitlement"`
	Decision       string     `json:"decision,omitempty"`
	DecisionBy     string     `json:"decision_by,omitempty"`
	DecidedAt      *time.Time `json:"decided_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type CreateReviewRequest struct {
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	DueAt       *time.Time         `json:"due_at,omitempty"`
	Items       []AccessReviewItem `json:"items,omitempty"`
}

type DriftFinding struct {
	ID                string    `json:"id"`
	OrganizationID    string    `json:"organization_id"`
	ConnectorID       string    `json:"connector_id"`
	ExternalID        string    `json:"external_id"`
	UserID            string    `json:"user_id,omitempty"`
	DriftType         string    `json:"drift_type"`
	Severity          string    `json:"severity"`
	Expected          JSONMap   `json:"expected,omitempty"`
	Observed          JSONMap   `json:"observed,omitempty"`
	Status            string    `json:"status"`
	RemediationTaskID string    `json:"remediation_task_id,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type ReconcileRequest struct {
	ConnectorID string `json:"connector_id"`
}
