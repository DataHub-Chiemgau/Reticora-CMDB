// Package compliance evaluates tenant CIs against compliance rules.
package compliance

import (
	"encoding/json"
	"errors"
	"time"
)

// ErrOrgWideScopeRequired: the evaluation replaces the results of the whole
// organization, so a principal restricted to some clients, sites or teams
// may not run it (TEN-06, E-09).
var ErrOrgWideScopeRequired = errors.New("compliance evaluation requires an org-wide principal")

type JSONMap map[string]any

type Rule struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	CITypeID       string `json:"ci_type_id"`
	Name           string `json:"name"`
	Description    string `json:"description,omitempty"`
	Severity       string `json:"severity"`
	Category       string `json:"category"`
	// Expression is one MET-45 condition node over Inputs: true is pass,
	// false is fail (SEC-03, E-229).
	Expression JSONMap `json:"expression"`
	// AppliesWhen is an optional MET-45 condition: false is not_applicable
	// (in addition to ci_type_id); nil applies always.
	AppliesWhen     JSONMap   `json:"applies_when"`
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
	// StatusChangedAt is when the status of the result last changed
	// (WP-1126).
	StatusChangedAt time.Time `json:"status_changed_at"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
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
	AppliesWhen     JSONMap `json:"applies_when,omitempty"`
	RemediationHint string  `json:"remediation_hint,omitempty"`
	Active          *bool   `json:"active,omitempty"`
}
type UpdateRuleRequest struct {
	CITypeID    *string `json:"ci_type_id,omitempty"`
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Severity    *string `json:"severity,omitempty"`
	Category    *string `json:"category,omitempty"`
	Expression  JSONMap `json:"expression,omitempty"`
	// AppliesWhen: absent keeps the condition, null removes it, an object
	// replaces it.
	AppliesWhen     json.RawMessage `json:"applies_when,omitempty"`
	RemediationHint *string         `json:"remediation_hint,omitempty"`
	Active          *bool           `json:"active,omitempty"`
	// appliesWhen is the decoded AppliesWhen (nil: remove) once
	// setAppliesWhen is true.
	appliesWhen    JSONMap
	setAppliesWhen bool
}

// errAppliesWhenObject refuses an applies_when that is neither an object
// nor null.
var errAppliesWhenObject = ruleFieldError("applies_when", "applies_when must be a condition object or null")

// decodeAppliesWhen reads AppliesWhen of a PATCH: absent changes nothing,
// null removes the condition, an object is checked like expression.
func (r *UpdateRuleRequest) decodeAppliesWhen() error {
	if len(r.AppliesWhen) == 0 {
		return nil
	}
	r.setAppliesWhen = true
	if string(r.AppliesWhen) == "null" {
		r.appliesWhen = nil
		return nil
	}
	var m JSONMap
	if err := json.Unmarshal(r.AppliesWhen, &m); err != nil || m == nil {
		return errAppliesWhenObject
	}
	if err := ValidateCondition("applies_when", m); err != nil {
		return err
	}
	r.appliesWhen = m
	return nil
}
