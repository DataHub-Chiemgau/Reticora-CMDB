// Package compliance evaluates tenant CIs against compliance rules.
package compliance

import (
	"context"
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

// Standards of the control catalog.
var Standards = []string{"iso27001", "nis2", "kritis"}

// maxRuleControls bounds the controls of one rule.
const maxRuleControls = 200

// Control is one control of the catalog with the rules of the organization
// assigned to it.
type Control struct {
	ID              string `json:"id"`
	Standard        string `json:"standard"`
	ControlID       string `json:"control_id"`
	TitleDE         string `json:"title_de"`
	TitleEN         string `json:"title_en"`
	SortOrder       int    `json:"sort_order"`
	RuleCount       int    `json:"rule_count"`
	ActiveRuleCount int    `json:"active_rule_count"`
}

// RuleControls is the assignment of a rule (PUT
// /api/v1/compliance/rules/{id}/controls).
type RuleControls struct {
	RuleID     string   `json:"rule_id"`
	ControlIDs []string `json:"control_ids"`
}

// RuleWithControls is a rule with the controls assigned to it (GET
// /api/v1/compliance/rules/{id}).
type RuleWithControls struct {
	*Rule
	ControlIDs []string `json:"control_ids"`
}

// ErrControlsUnavailable: the repository keeps no control catalog (the
// memory profile).
var ErrControlsUnavailable = errors.New("the control catalog needs PostgreSQL")

// ControlStore reads the catalog and assigns rules to controls.
type ControlStore interface {
	// ListControls lists the controls of standard with the counts of the
	// organization's rules; withoutActiveRule keeps only the controls
	// without an active rule (coverage gaps).
	ListControls(ctx context.Context, orgID, standard string, withoutActiveRule bool) ([]Control, error)
	// RuleControlIDs returns the controls assigned to a rule.
	RuleControlIDs(ctx context.Context, orgID, ruleID string) ([]string, error)
	// ReplaceRuleControls replaces the assignment of a rule: ErrNotFound
	// for a rule unknown or of another organization, a 422 field error
	// controls[i] for an unknown control.
	ReplaceRuleControls(ctx context.Context, orgID, ruleID string, controlIDs []string) error
}
