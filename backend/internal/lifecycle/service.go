package lifecycle

import (
	"context"
	"fmt"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Service applies lifecycle definitions to assets and CIs: it resolves the
// guard of the requested transition, validates required fields, applies the
// state, and records the history entry.
type Service struct {
	repo    Repository
	states  StateStore
	changes ChangeRecorder
}

// StateStore combines StateReader and StateWriter for the two entity kinds.
type StateStore interface {
	StateReader
	StateWriter
}

// NewService creates a lifecycle service.
func NewService(repo Repository, states StateStore, changes ChangeRecorder) *Service {
	return &Service{repo: repo, states: states, changes: changes}
}

// DefinitionForKey resolves a definition by key within the tenant scope.
func (s *Service) DefinitionForKey(ctx context.Context, orgID, key string) (*Definition, error) {
	defs, _, err := s.repo.List(ctx, orgID, api.PaginationParams{Limit: api.MaxPageLimit})
	if err != nil {
		return nil, err
	}
	for _, d := range defs {
		if d.Key == key {
			return &d, nil
		}
	}
	return nil, fmt.Errorf("lifecycle definition %q not found", key)
}

// Transition executes a guarded lifecycle transition for an entity.
//
// Required fields are resolved from the transition's target state definition
// and from the transition itself, then validated against the transition
// context (e.g. storage_location, deployment_location, disposal_date).
func (s *Service) Transition(ctx context.Context, orgID, entityType, entityID, definitionKey string, req TransitionRequest) (*TransitionResult, error) {
	if req.ToState == "" {
		return nil, fmt.Errorf("to_state is required")
	}
	def, err := s.DefinitionForKey(ctx, orgID, definitionKey)
	if err != nil {
		return nil, err
	}
	stateByKey := map[string]State{}
	for _, st := range def.States {
		stateByKey[st.Key] = st
	}
	target, ok := stateByKey[req.ToState]
	if !ok {
		return nil, fmt.Errorf("unknown lifecycle state %q", req.ToState)
	}

	current, err := s.states.CurrentState(ctx, orgID, entityType, entityID)
	if err != nil {
		return nil, err
	}

	// Resolve the guard: an explicit transition from the current state wins;
	// otherwise any transition into the target state matches (initial
	// assignment), otherwise the state itself must exist (direct set allowed
	// only into the initial state).
	required := append([]string{}, target.RequiredFields...)
	guardFound := false
	for _, tr := range def.Transitions {
		if tr.ToStateKey != req.ToState {
			continue
		}
		if tr.FromStateKey == "" || tr.FromStateKey == current {
			required = append(required, tr.RequiredFields...)
			guardFound = true
			break
		}
	}
	if !guardFound && current != "" {
		return nil, fmt.Errorf("no lifecycle transition from %q to %q", current, req.ToState)
	}
	if current == "" && !target.IsInitial && !guardFound {
		return nil, fmt.Errorf("initial lifecycle state must be %q", initialKey(def))
	}

	if err := validateRequiredFields(required, req.Context); err != nil {
		return nil, err
	}

	if err := s.states.SetState(ctx, orgID, entityType, entityID, req.ToState); err != nil {
		return nil, err
	}
	if s.changes != nil {
		_ = s.changes.RecordChange(ctx, orgID, entityType, entityID,
			"lifecycle_transition", "lifecycle_state", current, req.ToState, req.Reason)
	}
	return &TransitionResult{
		EntityType: entityType,
		EntityID:   entityID,
		FromState:  current,
		ToState:    req.ToState,
	}, nil
}

func initialKey(def *Definition) string {
	for _, st := range def.States {
		if st.IsInitial {
			return st.Key
		}
	}
	return ""
}

// validateRequiredFields checks that every required context value is present
// and non-empty.
func validateRequiredFields(required []string, ctx map[string]any) error {
	for _, field := range required {
		v, ok := ctx[field]
		if !ok || v == nil || v == "" {
			return fmt.Errorf("lifecycle transition requires field %q", field)
		}
	}
	return nil
}
