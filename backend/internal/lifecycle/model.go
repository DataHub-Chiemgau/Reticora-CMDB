// Package lifecycle implements the configurable lifecycle engine (spec §8):
// per-type lifecycle definitions with states and guarded transitions, kept
// strictly separate from technical health. Transition guards declare required
// fields that the API validates before applying a state change.
package lifecycle

import (
	"encoding/json"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
)

// Definition is a named lifecycle model assignable to CI types / asset
// categories.
type Definition struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id,omitempty"`
	Key            string `json:"key"`
	Name           string `json:"name"`
	AppliesTo      string `json:"applies_to"` // asset | ci
	IsSystem       bool   `json:"is_system"`
	Description    string `json:"description,omitempty"`
	// Version rises with every change; GET and PATCH send it as ETag, a
	// PATCH with an older If-Match is 412 (API-07, WP-723).
	Version     int64        `json:"version"`
	States      []State      `json:"states,omitempty"`
	Transitions []Transition `json:"transitions,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
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
	ID             string   `json:"id"`
	DefinitionID   string   `json:"definition_id"`
	FromStateID    string   `json:"from_state_id,omitempty"`
	FromStateKey   string   `json:"from_state_key,omitempty"`
	ToStateID      string   `json:"to_state_id"`
	ToStateKey     string   `json:"to_state_key,omitempty"`
	Key            string   `json:"key"`
	Label          string   `json:"label"`
	RequiredFields []string `json:"required_fields"`
	// Guard holds the rules of the transition context (LCY-02); it is
	// stored in lifecycle_transition.validation, which stays readable
	// as validation (API-01). WP-724 evaluates it.
	Guard      *Guard         `json:"guard,omitempty"`
	Validation map[string]any `json:"validation,omitempty"`
}

// Guard is the guard of a transition: per context field a rule of the
// rule AST (api/rules.schema.json, rules.Parse).
type Guard struct {
	Fields map[string]rules.Rule `json:"fields"`
}

// GuardSpec is the guard of a transition in a request; every rule is
// checked with rules.Parse (422 with its path).
type GuardSpec struct {
	Fields map[string]json.RawMessage `json:"fields"`
}

// CreateDefinitionRequest is the payload for creating a lifecycle definition.
type CreateDefinitionRequest struct {
	Key         string           `json:"key"`
	Name        string           `json:"name"`
	AppliesTo   string           `json:"applies_to,omitempty"`
	Description string           `json:"description,omitempty"`
	States      []StateSpec      `json:"states"`
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
	FromStateKey   string     `json:"from_state_key"` // empty = any state (creation/initial)
	ToStateKey     string     `json:"to_state_key"`
	Key            string     `json:"key"`
	Label          string     `json:"label,omitempty"`
	RequiredFields []string   `json:"required_fields,omitempty"`
	Guard          *GuardSpec `json:"guard,omitempty"`
}

// UpdateDefinitionRequest is the payload of PATCH
// /api/v1/lifecycle-models/{id}: name, description, states (matched by key,
// a kept state keeps its id) and transitions (replaced). key and
// applies_to cannot change (422 for another value).
type UpdateDefinitionRequest struct {
	Key         *string           `json:"key,omitempty"`
	AppliesTo   *string           `json:"applies_to,omitempty"`
	Name        *string           `json:"name,omitempty"`
	Description *string           `json:"description,omitempty"`
	States      *[]StateSpec      `json:"states,omitempty"`
	Transitions *[]TransitionSpec `json:"transitions,omitempty"`
}

// ModelRef names a lifecycle model.
type ModelRef struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

// TransitionOptions are the transitions the effective lifecycle model of
// an entity allows from its current state (GET
// /api/v1/{cis|assets}/{id}/lifecycle-transitions).
type TransitionOptions struct {
	Model        ModelRef           `json:"model"`
	CurrentState string             `json:"current_state"`
	Transitions  []TransitionOption `json:"transitions"`
}

// TransitionOption is one allowed transition: its target state, the
// required fields of target state and transition, and its guard.
type TransitionOption struct {
	ToState        string   `json:"to_state"`
	Key            string   `json:"key"`
	Label          string   `json:"label"`
	RequiredFields []string `json:"required_fields"`
	Guard          *Guard   `json:"guard,omitempty"`
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

// guardValidation is the content of lifecycle_transition.validation for a
// guard: {"fields": {...}}; nil without a guard.
func guardValidation(g *Guard) map[string]any {
	if g == nil {
		return nil
	}
	raw, err := json.Marshal(g)
	if err != nil {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

// guardFromValidation reads the guard back from
// lifecycle_transition.validation; a value without fields or with a rule
// rules.Parse refuses is no guard.
func guardFromValidation(validation []byte) *Guard {
	var stored GuardSpec
	if len(validation) == 0 || json.Unmarshal(validation, &stored) != nil || stored.Fields == nil {
		return nil
	}
	g := &Guard{Fields: map[string]rules.Rule{}}
	for field, raw := range stored.Fields {
		rule, err := rules.Parse(raw)
		if err != nil {
			return nil
		}
		g.Fields[field] = rule
	}
	return g
}

// guardSpecOf returns a stored guard as a request spec (a PATCH without
// transitions keeps the guards of the kept transitions).
func guardSpecOf(g *Guard) *GuardSpec {
	spec := &GuardSpec{Fields: map[string]json.RawMessage{}}
	for field := range g.Fields {
		raw, err := json.Marshal(g.Fields[field])
		if err != nil {
			continue
		}
		spec.Fields[field] = raw
	}
	return spec
}
