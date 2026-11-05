package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Service applies lifecycle models to assets and CIs (LCY-02, WP-724): in
// one transaction it locks the current state, checks the edge, the
// required fields and the guard of the transition and the system checks,
// sets the state and records history, audit and the event
// lifecycle.changed. Any error rolls everything back.
type Service struct {
	repo    Repository
	states  StateStore
	changes ChangeRecorder
	guards  GuardChecker
}

// StateStore combines StateReader and StateWriter for the two entity kinds
// with the transaction of a transition and its audit and event.
type StateStore interface {
	StateReader
	StateWriter
	// RunInTx runs fn in one transaction of orgID; the calls of the
	// service with the context fn gets join it (NFR-03). A transaction in
	// ctx is joined. Inside it CurrentState locks the entity row.
	RunInTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error
	// RecordTransition writes the audit entry and the event
	// lifecycle.changed of a transition in the transaction of ctx.
	RecordTransition(ctx context.Context, orgID string, rec *TransitionRecord) error
}

// TransitionRecord is a transition the service applied, for its audit
// entry and its event.
type TransitionRecord struct {
	EntityType string
	EntityID   string
	ModelID    string
	ModelKey   string
	From       string
	To         string
	Reason     string
}

// TransitionCheck is what a GuardChecker sees: the entity, its model, the
// locked current state, the target and the request context.
type TransitionCheck struct {
	OrgID      string
	EntityType string
	EntityID   string
	Model      *Definition
	From       string
	To         string
	Context    map[string]any
}

// ErrNoLifecycleModel: the entity's type has no lifecycle model, so it has
// no lifecycle state (DB-05).
var ErrNoLifecycleModel = errors.New("the type has no lifecycle model")

// errEntityNotFound: the entity is unknown or outside the caller's scope.
var errEntityNotFound = errors.New("not found")

// Problem types of a refused transition.
const (
	ProblemLifecycleGuard    = "urn:reticora:problem:lifecycle-guard"
	ProblemInvalidTransition = "urn:reticora:problem:invalid-transition"
	ProblemConflict          = "urn:reticora:problem:conflict"
)

// GuardViolation is one failed required field or guard rule of a
// transition: the context field, the rule code and its message.
type GuardViolation struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// GuardError refuses a transition whose required fields or guard rules the
// context fails (422 lifecycle-guard with the violations), or an asset
// that fails a system guard of LCY-03 (WP-279): then Guard is its
// guard_key and Missing names the missing references (location_id,
// assignee, disposal_record, reservation).
type GuardError struct {
	Violations []GuardViolation
	Guard      string
	Missing    []string
	detail     string
}

// validation: a fixed text, or the text of the failed system guard with
// its key and the target state.
func (e *GuardError) Error() string {
	if e.detail != "" {
		return e.detail
	}
	return "the transition context fails the guard of the lifecycle transition"
}

// InvalidTransitionError refuses a transition the model has no edge for,
// or into an unknown state (422 invalid-transition).
type InvalidTransitionError struct {
	From, To string
	reason   string
}

// validation: the rule text of the edge check with the state keys of the
// model and the request.
func (e *InvalidTransitionError) Error() string { return e.reason }

// StateConflictError refuses a transition whose from_state is not the
// current state (409 conflict with current_state).
type StateConflictError struct {
	CurrentState string
}

// validation: a fixed text.
func (e *StateConflictError) Error() string {
	return "the lifecycle state changed: from_state is not the current state"
}

// NewService creates a lifecycle service; changes records the history
// entry of a transition (NewHistoryRecorder), nil records none.
func NewService(repo Repository, states StateStore, changes ChangeRecorder) *Service {
	return &Service{repo: repo, states: states, changes: changes}
}

// WithGuards sets the checker of the system guards of LCY-03 (WP-279). They
// run in the transaction of every transition of an asset into a guarded
// state (SystemGuardFor), after the guard of the model. Without a checker
// such a transition fails closed (422 lifecycle-guard, cannot be checked).
func (s *Service) WithGuards(checker GuardChecker) *Service {
	s.guards = checker
	return s
}

// Transition executes a lifecycle transition of an entity with the model
// modelID (EntityResolver.LifecycleModelFor) in one transaction: the
// current state is read with a row lock, the request's from_state must
// match it (StateConflictError), the model needs an edge from it or from
// any state, or the target is the initial state of an entity without a
// state (InvalidTransitionError); the required fields of target and edge
// and the guard rules are evaluated against the context (GuardError);
// then the system guard of an asset's target state runs (WP-279), the state is set and history, audit and
// event are recorded. A model that cannot be loaded is ErrNoLifecycleModel.
func (s *Service) Transition(ctx context.Context, orgID, entityType, entityID, modelID string, req TransitionRequest) (*TransitionResult, error) {
	if req.ToState == "" {
		return nil, &api.FieldError{Field: "to_state", Message: "to_state is required", Status: http.StatusUnprocessableEntity}
	}
	var result *TransitionResult
	err := s.states.RunInTx(ctx, orgID, func(ctx context.Context) error {
		def, err := s.repo.GetByID(ctx, orgID, modelID)
		if errors.Is(err, api.ErrNotFound) || (err != nil && err.Error() == "not found") {
			return ErrNoLifecycleModel
		}
		if err != nil {
			return err
		}
		current, err := s.states.CurrentState(ctx, orgID, entityType, entityID)
		if err != nil {
			return err
		}
		if req.FromState != nil && *req.FromState != current {
			return &StateConflictError{CurrentState: current}
		}
		target, edge, err := allowedTransition(def, current, req.ToState)
		if err != nil {
			return err
		}
		if violations := guardViolations(target, edge, req.Context); len(violations) > 0 {
			return &GuardError{Violations: violations}
		}
		if guardErr := s.checkSystemGuard(ctx, &TransitionCheck{OrgID: orgID, EntityType: entityType,
			EntityID: entityID, Model: def, From: current, To: req.ToState, Context: req.Context}); guardErr != nil {
			return guardErr
		}
		if setErr := s.states.SetState(ctx, orgID, entityType, entityID, req.ToState); setErr != nil {
			return setErr
		}
		if s.changes != nil {
			var old any
			if current != "" {
				old = current
			}
			if histErr := s.changes.RecordChange(ctx, orgID, entityType, entityID,
				"lifecycle_transition", "lifecycle_state", old, req.ToState, req.Reason); histErr != nil {
				return fmt.Errorf("record lifecycle history: %w", histErr)
			}
		}
		// The change set of a bulk and the marks of its revert (WP-783),
		// before audit and event.
		if bulkErr := recordBulkTransition(ctx, orgID, entityType, entityID, current, req.ToState); bulkErr != nil {
			return bulkErr
		}
		if recErr := s.states.RecordTransition(ctx, orgID, &TransitionRecord{EntityType: entityType, EntityID: entityID,
			ModelID: def.ID, ModelKey: def.Key, From: current, To: req.ToState, Reason: req.Reason}); recErr != nil {
			return recErr
		}
		result = &TransitionResult{EntityType: entityType, EntityID: entityID, FromState: current, ToState: req.ToState}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// The change set of a lifecycle transition of a bulk (BLK-02, WP-783):
// under a bulk job (jobs.WithChangeSet) the transition records in its
// transaction one row op set, field lifecycle_state, with the state before
// (NULL without one) and after; the revert of the bulk passes the rows it
// reverts (WithRevertMarks), which the transaction back marks reverted.
// Only a transaction of the database (PGStateStore) records; the memory
// store keeps no change set.

// ChangeFieldLifecycleState is the field name of the change set row of a
// transition.
const ChangeFieldLifecycleState = "lifecycle_state"

type revertMarksKey struct{}

// WithRevertMarks returns ctx whose transition records marks (a revert of
// a bulk transition) in its transaction.
func WithRevertMarks(ctx context.Context, marks []jobs.ChangeMark) context.Context {
	return context.WithValue(ctx, revertMarksKey{}, marks)
}

func revertMarksFrom(ctx context.Context) []jobs.ChangeMark {
	marks, _ := ctx.Value(revertMarksKey{}).([]jobs.ChangeMark)
	return marks
}

// recordBulkTransition writes the change set row of a transition from
// current to to of a bulk and the marks of a revert in the transaction of
// ctx; nothing outside a bulk, a revert or a database transaction.
func recordBulkTransition(ctx context.Context, orgID, entityType, entityID, current, to string) error {
	jobID, capture := jobs.ChangeSetFrom(ctx)
	marks := revertMarksFrom(ctx)
	if (!capture && len(marks) == 0) || !database.InTx(ctx) {
		return nil
	}
	table := map[string]string{"ci": "ci", "asset": "asset"}[entityType]
	if table == "" {
		return nil
	}
	// The transaction of the transition is joined (RunInTx); no pool is
	// needed.
	return database.WithRequestTenant(ctx, nil, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := jobs.MarkChangeTx(ctx, tx, orgID, marks); err != nil {
			return err
		}
		if !capture {
			return nil
		}
		change := jobs.Change{JobID: jobID, EntityType: entityType, EntityID: entityID, Op: jobs.OpSet,
			FieldName: ChangeFieldLifecycleState}
		var err error
		if current != "" {
			if change.OldValue, err = json.Marshal(current); err != nil {
				return fmt.Errorf("encode bulk change set: %w", err)
			}
		}
		if change.NewValue, err = json.Marshal(to); err != nil {
			return fmt.Errorf("encode bulk change set: %w", err)
		}
		if err = tx.QueryRow(ctx, "SELECT COALESCE(client_id::text, ''), COALESCE(site_id::text, '') FROM "+table+
			" WHERE id = $1 AND organization_id = $2", entityID, orgID).Scan(&change.ClientID, &change.SiteID); err != nil {
			return fmt.Errorf("read the entity of the bulk change set: %w", err)
		}
		return jobs.RecordChangeTx(ctx, tx, orgID, []jobs.Change{change})
	})
}

// checkSystemGuard evaluates the system guard of the target state of an
// asset (LCY-03, WP-279) whatever its model; CIs have none. Without a
// checker a guarded transition is refused (fail-closed).
func (s *Service) checkSystemGuard(ctx context.Context, check *TransitionCheck) error {
	if check.EntityType != "asset" {
		return nil
	}
	guard, ok := SystemGuardFor(check.To)
	if !ok {
		return nil
	}
	if s.guards == nil {
		return uncheckableGuardError(guard, check.To)
	}
	return s.guards.CheckGuard(ctx, guard, check)
}

// allowedTransition returns the target state and the edge of a transition
// from current to to: an edge from current wins over one without a from
// state; without an edge only an entity without a state may enter the
// initial state (edge nil).
func allowedTransition(def *Definition, current, to string) (*State, *Transition, error) {
	var target *State
	for i := range def.States {
		if def.States[i].Key == to {
			target = &def.States[i]
		}
	}
	if target == nil {
		return nil, nil, &InvalidTransitionError{From: current, To: to,
			reason: fmt.Sprintf("unknown lifecycle state %q", to)}
	}
	var edge *Transition
	for i := range def.Transitions {
		tr := &def.Transitions[i]
		if tr.ToStateKey != to {
			continue
		}
		if tr.FromStateKey == current && current != "" {
			edge = tr
			break
		}
		if tr.FromStateKey == "" && edge == nil {
			edge = tr
		}
	}
	switch {
	case edge != nil:
		return target, edge, nil
	case current == "" && target.IsInitial:
		return target, nil, nil
	case current == "":
		return nil, nil, &InvalidTransitionError{From: current, To: to,
			reason: fmt.Sprintf("initial lifecycle state must be %q", initialKey(def))}
	}
	return nil, nil, &InvalidTransitionError{From: current, To: to,
		reason: fmt.Sprintf("no lifecycle transition from %q to %q", current, to)}
}

// guardViolations evaluates the required fields of target and edge (the
// short form of {op: required}) and every rule of the edge's guard
// (rules.Evaluate) against the transition context.
func guardViolations(target *State, edge *Transition, values map[string]any) []GuardViolation {
	required := slices.Clone(target.RequiredFields)
	var guard *Guard
	if edge != nil {
		required = append(required, edge.RequiredFields...)
		guard = edge.Guard
	}
	var out []GuardViolation
	seen := map[string]bool{}
	requiredRule := rules.Rule{Op: rules.OpRequired}
	for _, field := range required {
		if seen[field] {
			continue
		}
		seen[field] = true
		value, present := values[field]
		for _, v := range rules.Evaluate(&requiredRule, value, present) {
			out = append(out, GuardViolation{Field: field, Code: v.Code,
				Message: fmt.Sprintf("lifecycle transition requires field %q", field)})
		}
	}
	if guard == nil {
		return out
	}
	fields := make([]string, 0, len(guard.Fields))
	for field := range guard.Fields {
		fields = append(fields, field)
	}
	slices.Sort(fields)
	for _, field := range fields {
		rule := guard.Fields[field]
		value, present := values[field]
		for _, v := range rules.Evaluate(&rule, value, present) {
			if v.Code == rules.CodeRequired && seen[field] {
				continue
			}
			out = append(out, GuardViolation{Field: field, Code: v.Code, Message: v.Message})
		}
	}
	return out
}

func initialKey(def *Definition) string {
	for _, st := range def.States {
		if st.IsInitial {
			return st.Key
		}
	}
	return ""
}

// HistoryRecorder is the ChangeRecorder of the transition service on the
// entity change trail (history.Repository.Record): one entity_change row
// with the acting user, in the transaction of the transition when the
// repository joins it (PostgreSQL).
type HistoryRecorder struct {
	repo history.Repository
}

// NewHistoryRecorder records lifecycle transitions into repo.
func NewHistoryRecorder(repo history.Repository) *HistoryRecorder {
	return &HistoryRecorder{repo: repo}
}

// RecordChange implements ChangeRecorder.
func (h *HistoryRecorder) RecordChange(ctx context.Context, orgID, entityType, entityID, changeType, field string, oldVal, newVal any, comment string) error {
	actor := tenant.FromContext(ctx).UserID
	if !audit.IsUUID(actor) {
		actor = ""
	}
	return h.repo.Record(ctx, &history.Change{OrganizationID: orgID, EntityType: entityType, EntityID: entityID,
		ActorID: actor, ChangeType: changeType, FieldName: field, OldValue: oldVal, NewValue: newVal, Comment: comment})
}
