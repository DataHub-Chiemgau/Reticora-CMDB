// Package lifecycle implements the configurable lifecycle engine (spec §8):
// per-type lifecycle definitions with states and guarded transitions, kept
// strictly separate from technical health. Transition guards declare required
// fields that the API validates before applying a state change.
package lifecycle

import "time"

// Definition is a named lifecycle model assignable to CI types / asset
// categories.
type Definition struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id,omitempty"`
	Key            string    `json:"key"`
	Name           string    `json:"name"`
	AppliesTo      string    `json:"applies_to"` // asset | ci | both
	IsSystem       bool      `json:"is_system"`
	Description    string    `json:"description,omitempty"`
	States         []State   `json:"states,omitempty"`
	Transitions    []Transition `json:"transitions,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// State is one lifecycle state within a definition.
type State struct {
	ID             string   `json:"id"`
	DefinitionID   string   `json:"definition_id"`
	Key            string   `json:"key"`
	Label          string   `json:"label"`
	IsInitial      bool     `json:"is_initial"`
	IsTerminal     bool     `json:"is_terminal"`
	SortOrder      int      `json:"sort_order"`
	RequiredFields []string `json:"required_fields"`
}

// Transition is a guarded edge between two states.
type Transition struct {
	ID             string         `json:"id"`
	DefinitionID   string         `json:"definition_id"`
	FromStateID    string         `json:"from_state_id,omitempty"`
	FromStateKey   string         `json:"from_state_key,omitempty"`
	ToStateID      string         `json:"to_state_id"`
	ToStateKey     string         `json:"to_state_key,omitempty"`
	Key            string         `json:"key"`
	Label          string         `json:"label"`
	RequiredFields []string       `json:"required_fields"`
	Validation     map[string]any `json:"validation,omitempty"`
}

// CreateDefinitionRequest is the payload for creating a lifecycle definition.
type CreateDefinitionRequest struct {
	Key         string       `json:"key"`
	Name        string       `json:"name"`
	AppliesTo   string       `json:"applies_to,omitempty"`
	Description string       `json:"description,omitempty"`
	States      []StateSpec  `json:"states"`
	Transitions []TransitionSpec `json:"transitions,omitempty"`
}

// StateSpec defines a state inside a create/update request.
type StateSpec struct {
	Key            string   `json:"key"`
	Label          string   `json:"label"`
	IsInitial      bool     `json:"is_initial,omitempty"`
	IsTerminal     bool     `json:"is_terminal,omitempty"`
	SortOrder      int      `json:"sort_order,omitempty"`
	RequiredFields []string `json:"required_fields,omitempty"`
}

// TransitionSpec defines a transition inside a create/update request.
type TransitionSpec struct {
	FromStateKey   string   `json:"from_state_key"` // empty = any state (creation/initial)
	ToStateKey     string   `json:"to_state_key"`
	Key            string   `json:"key"`
	Label          string   `json:"label,omitempty"`
	RequiredFields []string `json:"required_fields,omitempty"`
}

// TransitionRequest is the payload for executing a lifecycle transition on an
// asset or CI.
type TransitionRequest struct {
	// ToState is the target state key. The service resolves the guard from the
	// definition assigned to the entity's type.
	ToState string `json:"to_state"`
	// Context carries values consulted by required-field validation
	// (e.g. storage_location, deployment_location, disposal_date).
	Context map[string]any `json:"context,omitempty"`
	// Reason is stored in the history/audit trail.
	Reason string `json:"reason,omitempty"`
}

// TransitionResult reports the outcome of a lifecycle transition.
type TransitionResult struct {
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
	FromState  string `json:"from_state,omitempty"`
	ToState    string `json:"to_state"`
}
