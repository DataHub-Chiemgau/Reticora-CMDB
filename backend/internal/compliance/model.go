// Package compliance evaluates tenant CIs against compliance rules.
package compliance

import "time"

type JSONMap map[string]any

type Rule struct {
	ID              string    `json:"id"`
	OrganizationID  string    `json:"organization_id"`
	CITypeID        string    `json:"ci_type_id"`
	Name            string    `json:"name"`
	Description     string    `json:"description,omitempty"`
	Severity        string    `json:"severity"`
	Category        string    `json:"category"`
	Expression      JSONMap   `json:"expression"`
	RemediationHint string    `json:"remediation_hint,omitempty"`
	Active          bool      `json:"active"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}
type Result struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	RuleID         string    `json:"rule_id"`
	CIID           string    `json:"ci_id"`
	CITypeID       string    `json:"ci_type_id"`
	Status         string    `json:"status"`
	Details        string    `json:"details,omitempty"`
	EvaluatedAt    time.Time `json:"evaluated_at"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}
type Score struct {
	CITypeID      string  `json:"ci_type_id,omitempty"`
	Passed        int     `json:"passed"`
	Failed        int     `json:"failed"`
	NotApplicable int     `json:"not_applicable"`
	Score         float64 `json:"score"`
}
type EvaluationResponse struct {
	Overall  Score    `json:"overall"`
	ByCIType []Score  `json:"by_ci_type"`
	Results  []Result `json:"results"`
}
type CreateRuleRequest struct {
	CITypeID        string  `json:"ci_type_id"`
	Name            string  `json:"name"`
	Description     string  `json:"description,omitempty"`
	Severity        string  `json:"severity"`
	Category        string  `json:"category"`
	Expression      JSONMap `json:"expression"`
	RemediationHint string  `json:"remediation_hint,omitempty"`
	Active          *bool   `json:"active,omitempty"`
}
type UpdateRuleRequest struct {
	CITypeID        *string `json:"ci_type_id,omitempty"`
	Name            *string `json:"name,omitempty"`
	Description     *string `json:"description,omitempty"`
	Severity        *string `json:"severity,omitempty"`
	Category        *string `json:"category,omitempty"`
	Expression      JSONMap `json:"expression,omitempty"`
	RemediationHint *string `json:"remediation_hint,omitempty"`
	Active          *bool   `json:"active,omitempty"`
}
