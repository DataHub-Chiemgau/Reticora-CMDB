package ticket

import (
	"errors"
	"fmt"
	"slices"
)

// Ticket states (TKT-01, WP-290).
const (
	StatusNew        = "new"
	StatusOpen       = "open"
	StatusInProgress = "in_progress"
	StatusWaiting    = "waiting"
	StatusResolved   = "resolved"
	StatusClosed     = "closed"
)

// Statuses lists every ticket state in the order of the workflow.
var Statuses = []string{StatusNew, StatusOpen, StatusInProgress, StatusWaiting, StatusResolved, StatusClosed}

// Priorities lists every ticket priority (000013; matches sla.priority).
var Priorities = []string{"low", "medium", "high", "critical"}

// transitions is the state machine: the states each state may change to.
// The same state is no transition; resolved and closed reopen to open.
var transitions = map[string][]string{
	StatusNew:        {StatusOpen, StatusInProgress, StatusClosed},
	StatusOpen:       {StatusInProgress, StatusWaiting, StatusResolved, StatusClosed},
	StatusInProgress: {StatusOpen, StatusWaiting, StatusResolved},
	StatusWaiting:    {StatusInProgress, StatusResolved},
	StatusResolved:   {StatusClosed, StatusOpen},
	StatusClosed:     {StatusOpen},
}

// ErrInvalidTransition: the requested status change is not allowed by the
// state machine, or a ticket is created in a state other than new or open.
// The handler answers 409.
var ErrInvalidTransition = errors.New("invalid ticket status transition")

// ValidStatus reports whether status is a ticket state.
func ValidStatus(status string) bool { return slices.Contains(Statuses, status) }

// ValidPriority reports whether priority is a ticket priority.
func ValidPriority(priority string) bool { return slices.Contains(Priorities, priority) }

// CanTransition reports whether a ticket may change from one state to
// another.
func CanTransition(from, to string) bool { return slices.Contains(transitions[from], to) }

// TransitionError is a refused status change; it matches
// ErrInvalidTransition (errors.Is). From is empty for the creation of a
// ticket.
type TransitionError struct {
	From, To string
}

// validation: the refused states, both checked against Statuses by
// writeTicketError before the text reaches a response.
func (e *TransitionError) Error() string {
	if e.From == "" {
		return fmt.Sprintf("a ticket is created as %s or %s, not %s", StatusNew, StatusOpen, e.To)
	}
	return fmt.Sprintf("a ticket cannot change from %s to %s", e.From, e.To)
}

// Is makes errors.Is(err, ErrInvalidTransition) hold.
func (e *TransitionError) Is(target error) bool { return target == ErrInvalidTransition }

// CheckTransition returns a *TransitionError when from → to is no
// transition of the state machine.
func CheckTransition(from, to string) error {
	if !CanTransition(from, to) {
		return &TransitionError{From: from, To: to}
	}
	return nil
}

// CheckInitialStatus returns a *TransitionError unless status is a state a
// ticket may be created in: new (REST) or open (workflow).
func CheckInitialStatus(status string) error {
	if status != StatusNew && status != StatusOpen {
		return &TransitionError{To: status}
	}
	return nil
}

// IsReopen reports whether from → to reopens a resolved or closed ticket.
func IsReopen(from, to string) bool {
	return to == StatusOpen && (from == StatusResolved || from == StatusClosed)
}
