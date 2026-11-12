package alarm

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// Store is the port of the alarm instances (ALR-01). Every method runs in
// the transaction the context carries (database.ContextWithTx, Konvention
// 20), so a service writes alarm, transition and outbox event in one unit;
// without one it opens a transaction of the context's tenant scope.
type Store interface {
	// Get returns the alarm, or ErrNotFound.
	Get(ctx context.Context, orgID, id string) (*Alarm, error)
	// List returns one page of alarms newest first on (first_seen_at, id).
	List(ctx context.Context, orgID string, f *ListFilter) (Page, error)
	// UpsertOpen inserts a new alarm for the key of in, or, when an alarm
	// of that key is not resolved, increments its occurrence_count and sets
	// last_seen_at (INSERT … ON CONFLICT … DO UPDATE). created reports a
	// new row.
	UpsertOpen(ctx context.Context, orgID string, in *UpsertInput) (a *Alarm, created bool, err error)
	// LockForUpdate reads the alarm and locks its row until the end of the
	// transaction (SELECT … FOR UPDATE), or returns ErrNotFound.
	LockForUpdate(ctx context.Context, orgID, id string) (*Alarm, error)
	// LockOpenByKey reads the alarm of dedupKey that is not resolved and
	// locks its row like LockForUpdate, or returns ErrNotFound.
	LockOpenByKey(ctx context.Context, orgID, dedupKey string) (*Alarm, error)
	// UpdateState writes the state of the alarm and returns it, or
	// ErrNotFound.
	UpdateState(ctx context.Context, orgID, id string, u *StateUpdate) (*Alarm, error)
	// AppendTransition appends an entry to the state log of an alarm;
	// organization, client and site follow the alarm.
	AppendTransition(ctx context.Context, orgID string, tr *Transition) (*Transition, error)
	// Transitions returns the state log of an alarm, oldest first.
	Transitions(ctx context.Context, orgID, alarmID string) ([]Transition, error)

	// Incidents (ALR-03, WP-1027). Every change appends to
	// incident_transition in the same transaction.

	// UpsertIncident opens an incident (in.ID empty) or updates cause,
	// confidence, reasons and affected counts of one that is not resolved;
	// created reports a new incident.
	UpsertIncident(ctx context.Context, orgID string, in *IncidentInput) (inc *Incident, created bool, err error)
	// GetIncident returns the incident, or ErrIncidentNotFound.
	GetIncident(ctx context.Context, orgID, id string) (*Incident, error)
	// ListIncidents returns one page of incidents newest first on
	// (opened_at, id).
	ListIncidents(ctx context.Context, orgID string, f *IncidentFilter) (IncidentPage, error)
	// AttachAlarms assigns alarms to an incident that is not resolved: as
	// symptoms (suppressed with reason symptom unless suppressed already)
	// or one alarm as its root (the automatic root: ErrManualRoot when the
	// root was set by hand). An alarm of another incident is
	// ErrAlarmInOtherIncident (also for SetRoot and the root of a merge).
	AttachAlarms(ctx context.Context, orgID, incidentID string, alarmIDs []string, role string) (*Incident, error)
	// SetRoot makes the alarm the root of the incident (root CI from the
	// alarm). manual marks a root set by hand; an automatic change
	// (manual false) of such an incident is ErrManualRoot.
	SetRoot(ctx context.Context, orgID, incidentID, alarmID string, manual bool) (*Incident, error)
	// MergeIncidents moves every alarm of absorb to keep and resolves absorb
	// (merged_into; absorb loses its root alarm). The root of keep stays
	// when it was set by hand, otherwise rootAlarmID becomes the root
	// (empty: the root stays). It needs the organization-wide scope
	// (ErrMergeScopeRestricted).
	MergeIncidents(ctx context.Context, orgID, keepID, absorbID, rootAlarmID string) (*Incident, error)
	// TransitionIncident moves an incident to state to (acknowledge with
	// actor, un-acknowledge, resolve).
	TransitionIncident(ctx context.Context, orgID, id, to string, actorID *string) (*Incident, error)
	// IncidentTransitions returns the log of an incident, oldest first.
	IncidentTransitions(ctx context.Context, orgID, incidentID string) ([]IncidentTransition, error)
}

// ValidateUpsert checks the input of UpsertOpen against the value sets of
// the database, so both stores refuse the same input.
func ValidateUpsert(in *UpsertInput) error {
	switch {
	case !slices.Contains(SourceKinds, in.SourceKind):
		return fmt.Errorf("alarm: unknown source kind %q", in.SourceKind)
	case in.SourceID == "":
		return fmt.Errorf("alarm: source id is required")
	case !slices.Contains(Severities, in.Severity):
		return fmt.Errorf("alarm: unknown severity %q", in.Severity)
	case in.State != StatePending && in.State != StateOpen:
		return fmt.Errorf("alarm: a new alarm is pending or open, not %q", in.State)
	case len([]rune(in.Summary)) > SummaryMaxLen:
		return fmt.Errorf("alarm: summary longer than %d characters", SummaryMaxLen)
	}
	return nil
}

// validateState checks a StateUpdate against the value sets of the
// database.
func validateState(u *StateUpdate) error {
	switch {
	case !slices.Contains(States, u.State):
		return fmt.Errorf("alarm: unknown state %q", u.State)
	case !slices.Contains(Severities, u.Severity):
		return fmt.Errorf("alarm: unknown severity %q", u.Severity)
	case u.SuppressedReason != nil && !slices.Contains(SuppressedReasons, *u.SuppressedReason):
		return fmt.Errorf("alarm: unknown suppression reason %q", *u.SuppressedReason)
	case (u.State == StateResolved) != (u.ResolvedAt != nil):
		return fmt.Errorf("alarm: resolved_at is set exactly in state resolved")
	}
	return nil
}

// ErrInvalidTransitionInput is returned for a transition whose values the
// database refuses (state, severity, reason or comment too long); nothing
// is written.
var ErrInvalidTransitionInput = errors.New("alarm: invalid transition input")

// Lengths of the free text of a transition (characters), as the database
// checks them.
const (
	ReasonMaxLen  = 200
	CommentMaxLen = 1000
)

// ValidateTransition checks a transition against the value sets and
// lengths of alarm_transition, so both stores refuse the same input.
func ValidateTransition(tr *Transition) error {
	switch {
	case !slices.Contains(States, tr.ToState):
		return fmt.Errorf("%w: unknown state %q", ErrInvalidTransitionInput, tr.ToState)
	case tr.FromState != nil && !slices.Contains(States, *tr.FromState):
		return fmt.Errorf("%w: unknown state %q", ErrInvalidTransitionInput, *tr.FromState)
	case tr.FromSeverity != nil && !slices.Contains(Severities, *tr.FromSeverity),
		tr.ToSeverity != nil && !slices.Contains(Severities, *tr.ToSeverity):
		return fmt.Errorf("%w: unknown severity", ErrInvalidTransitionInput)
	}
	return validateTransitionText(tr.Reason, tr.Comment)
}

// validateTransitionText checks reason and comment of a transition.
func validateTransitionText(reason string, comment *string) error {
	if len([]rune(reason)) > ReasonMaxLen {
		return fmt.Errorf("%w: reason longer than %d characters", ErrInvalidTransitionInput, ReasonMaxLen)
	}
	if comment != nil && len([]rune(*comment)) > CommentMaxLen {
		return fmt.Errorf("%w: comment longer than %d characters", ErrInvalidTransitionInput, CommentMaxLen)
	}
	return nil
}

// limitOf returns the page size of a filter.
func limitOf(f *ListFilter) int {
	if f.Limit <= 0 {
		return DefaultLimit
	}
	return f.Limit
}
