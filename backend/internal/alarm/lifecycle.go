package alarm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// The lifecycle of an alarm (ALR-01, WP-982): Pend, Raise, Clear,
// Acknowledge and Unacknowledge move an alarm through the states of
// Transitions. Every change writes its alarm_transition and, where E-161
// names one, its event alarm.* in the transaction of the change; the event
// is the last write before the commit (database.BeforeCommit, Konvention
// 20). One event per state change, never one per evaluation (E-161).

// ErrInvalidTransition is returned for a state change the lifecycle does
// not allow (Transitions); nothing is written.
var ErrInvalidTransition = errors.New("alarm: transition not allowed")

// StateNew stands for "no alarm yet" as the source state of Transitions.
const StateNew = ""

// Transitions are the allowed state changes (ALR-01): from a state (StateNew
// for a new alarm) to the states it may move to. Every other change is
// ErrInvalidTransition.
var Transitions = map[string][]string{
	StateNew:          {StatePending, StateOpen},
	StatePending:      {StateOpen, StateResolved},
	StateOpen:         {StateAcknowledged, StateResolved},
	StateAcknowledged: {StateOpen, StateResolved},
	StateResolved:     nil,
}

// CanTransition reports whether the lifecycle allows the change from from
// (StateNew for a new alarm) to to.
func CanTransition(from, to string) bool {
	return slices.Contains(Transitions[from], to)
}

// Reasons of the transitions the lifecycle writes.
const (
	ReasonPending         = "pending"
	ReasonRaised          = "raised"
	ReasonSeverityChanged = "severity_changed"
	ReasonCleared         = "cleared"
	ReasonAcknowledged    = "acknowledged"
	ReasonUnacknowledged  = "unacknowledged"
)

// RaiseInput is one breach or bad check result of a source and series.
type RaiseInput struct {
	SourceKind string
	SourceID   string
	SeriesKey  string
	Labels     map[string]string
	// CIID binds the alarm to a CI; client and site then follow the CI.
	CIID *string
	// ClientID is the client of an alarm without CI.
	ClientID *string
	Severity string
	Summary  string
	Details  map[string]any
	// Actions are the follow-up actions (WP-988): those of the rule; nil
	// is the defaults (notify and webhook on). The alarm keeps them in
	// details.actions, its alarm.* events carry them.
	Actions *Actions
	// ObservedAt is when the source saw the breach; zero is now.
	ObservedAt time.Time
}

// withDetailsActions is in with its actions in the details (a copy).
func withDetailsActions(in *RaiseInput) *RaiseInput {
	out := *in
	out.Details = withActions(in.Details, in.Actions)
	return &out
}

// EventSink takes the events of the lifecycle in the transaction of the
// change.
type EventSink interface {
	Append(ctx context.Context, e *events.Event) error
}

// OutboxSink appends the events to event_outbox as the last write before
// the commit of the context's transaction (Konvention 20).
type OutboxSink struct {
	pool   *pgxpool.Pool
	outbox *events.Outbox
}

// NewOutboxSink returns the outbox sink on pool.
func NewOutboxSink(pool *pgxpool.Pool) *OutboxSink {
	return &OutboxSink{pool: pool, outbox: events.NewOutbox()}
}

// Append implements EventSink.
func (s *OutboxSink) Append(ctx context.Context, e *events.Event) error {
	return database.WithRequestTenant(ctx, s.pool, e.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		return database.BeforeCommit(ctx, func() error {
			_, err := s.outbox.Append(ctx, tx, e)
			return err
		})
	})
}

// MemorySink records the events in memory (--no-db, tests).
type MemorySink struct {
	mu     sync.Mutex
	events []events.Event
}

// Append implements EventSink.
func (m *MemorySink) Append(_ context.Context, e *events.Event) error {
	if !events.IsType(e.Type) {
		return fmt.Errorf("alarm: unknown event type %q", e.Type)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, *e)
	return nil
}

// Reset forgets the recorded events (tests).
func (m *MemorySink) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = nil
}

// Events returns the recorded events, oldest first.
func (m *MemorySink) Events() []events.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.events)
}

// LogSink records the events in memory and logs the opening and the
// resolution of an alarm: the notification of the operation without a
// database (--no-db), which has no outbox and no notify consumer (WP-987).
// With a database the events go through the outbox to notify.
type LogSink struct {
	MemorySink
}

// Append implements EventSink.
func (l *LogSink) Append(ctx context.Context, e *events.Event) error {
	if err := l.MemorySink.Append(ctx, e); err != nil {
		return err
	}
	if e.Type == events.AlarmOpened || e.Type == events.AlarmResolved {
		d, _ := e.Data.(events.AlarmData)
		slog.WarnContext(ctx, "alarm notification (no database)", "event", e.Type, "organization_id", e.OrgID,
			"alarm_id", d.ID, "severity", d.Severity, "summary", d.Summary)
	}
	return nil
}

// Service runs the lifecycle on a store. With a pool every operation runs
// in one transaction of the context's tenant scope and joins the caller's
// transaction (database.WithRequestTenantTx): the evaluator and the check
// consumer call it under database.WithTenant per organization.
type Service struct {
	store Store
	pool  *pgxpool.Pool
	sink  EventSink
	clock func() time.Time
	// maintenance suppresses the alarms of CIs in active maintenance
	// windows (WP-1033); nil suppresses nothing.
	maintenance MaintenanceChecker
	// tickets opens the tickets of the ticket action (WP-1040); nil opens
	// none.
	tickets TicketWriter
	// memMu serializes the operations without a database: the memory
	// store has no row locks.
	memMu sync.Mutex
}

// NewService returns the lifecycle on store; pool may be nil (memory
// store), sink takes the events.
func NewService(store Store, pool *pgxpool.Pool, sink EventSink) *Service {
	return &Service{store: store, pool: pool, sink: sink, clock: now}
}

// NewPGService returns the lifecycle on PostgreSQL with the event outbox.
func NewPGService(pool *pgxpool.Pool) *Service {
	return NewService(NewPGStore(pool), pool, NewOutboxSink(pool))
}

// WithClock replaces the clock (tests).
func (s *Service) WithClock(clock func() time.Time) *Service {
	s.clock = clock
	return s
}

// Store returns the store of the service.
func (s *Service) Store() Store { return s.store }

// InTx runs fn in one transaction of the organization, the caller's when
// the context carries one, so several operations of the service and a
// domain audit entry commit together (WP-983). Without a database fn runs
// directly: every operation of the service serializes itself.
func (s *Service) InTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error {
	if s.pool == nil {
		return fn(ctx)
	}
	return database.WithRequestTenantTx(ctx, s.pool, orgID, fn)
}

// inTx runs fn in one transaction of the organization, the caller's when
// the context carries one.
func (s *Service) inTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error {
	if s.pool == nil {
		s.memMu.Lock()
		defer s.memMu.Unlock()
		return fn(ctx)
	}
	return database.WithRequestTenantTx(ctx, s.pool, orgID, fn)
}

// severityRank orders the severities, lowest first.
func severityRank(sev string) int { return slices.Index(Severities, sev) }

// Pend records a breach that has not lasted long enough yet: a new alarm in
// state pending, or one more occurrence of the alarm of the key. It writes
// no event.
func (s *Service) Pend(ctx context.Context, orgID string, in *RaiseInput) (*Alarm, error) {
	in = withDetailsActions(in)
	var out *Alarm
	err := s.inTx(ctx, orgID, func(ctx context.Context) error {
		a, created, err := s.store.UpsertOpen(ctx, orgID, upsertOf(in, StatePending, s.observed(in)))
		if err != nil {
			return err
		}
		out = a
		if !created {
			return nil
		}
		_, err = s.store.AppendTransition(ctx, orgID, &Transition{AlarmID: a.ID, ToState: StatePending,
			ToSeverity: &a.Severity, At: a.FirstSeenAt, Reason: ReasonPending})
		return err
	})
	return out, err
}

// Raise opens the alarm of the key: a new one, or the pending one. On an
// open or acknowledged alarm it counts the occurrence; a higher severity is
// taken over (an acknowledged alarm goes back to open, its ack lapses), a
// lower one changes nothing.
func (s *Service) Raise(ctx context.Context, orgID string, in *RaiseInput) (*Alarm, error) {
	in = withDetailsActions(in)
	var out *Alarm
	err := s.inTx(ctx, orgID, func(ctx context.Context) error {
		at := s.observed(in)
		// An alarm of a CI in an active maintenance window opens
		// suppressed (WP-1033).
		suppress, err := s.maintenanceOf(ctx, orgID, in, at)
		if err != nil {
			return err
		}
		a, created, err := s.store.UpsertOpen(ctx, orgID, upsertOf(in, StateOpen, at))
		if err != nil {
			return err
		}
		if created {
			if suppress && a.SuppressedReason == nil {
				u := stateOf(a)
				reason := SuppressedMaintenance
				u.SuppressedReason = &reason
				if a, err = s.store.UpdateState(ctx, orgID, a.ID, u); err != nil {
					return err
				}
			}
			out = a
			s.afterOpen(ctx, a)
			return s.record(ctx, a, &Transition{ToState: StateOpen, ToSeverity: &a.Severity, At: a.FirstSeenAt,
				Reason: ReasonRaised}, events.AlarmOpened)
		}
		// UpsertOpen locked the row: its state is current.
		switch {
		case a.State == StatePending:
			sev := a.Severity
			if severityRank(in.Severity) > severityRank(sev) {
				sev = in.Severity
			}
			u := stateOf(a)
			// The observation that opens the alarm, not the first one of
			// the pending phase, is its value (WP-987, AK4).
			u.State, u.Severity, u.OpenedAt, u.Details = StateOpen, sev, &at, in.Details
			if suppress && u.SuppressedReason == nil {
				reason := SuppressedMaintenance
				u.SuppressedReason = &reason
			}
			keepMaintenanceWindow(a, u)
			out, err = s.move(ctx, a, u, &Transition{At: at, Reason: ReasonRaised}, events.AlarmOpened)
			if err == nil {
				s.afterOpen(ctx, out)
			}
			return err
		case (a.State == StateOpen || a.State == StateAcknowledged) && severityRank(in.Severity) > severityRank(a.Severity):
			u := stateOf(a)
			u.State, u.Severity, u.AcknowledgedAt, u.AcknowledgedBy, u.Details = StateOpen, in.Severity, nil, nil, in.Details
			keepMaintenanceWindow(a, u)
			out, err = s.move(ctx, a, u, &Transition{At: at, Reason: ReasonSeverityChanged}, events.AlarmSeverityChanged)
			return err
		}
		out = a
		return nil
	})
	return out, err
}

// Clear resolves the alarm of the key that is not resolved and returns it;
// nil when there is none. Only an alarm that was opened yields
// alarm.resolved; a pending one is resolved silently.
func (s *Service) Clear(ctx context.Context, orgID, dedupKey string, at time.Time, reason string) (*Alarm, error) {
	if at.IsZero() {
		at = s.clock()
	}
	if reason == "" {
		reason = ReasonCleared
	}
	var out *Alarm
	err := s.inTx(ctx, orgID, func(ctx context.Context) error {
		a, err := s.store.LockOpenByKey(ctx, orgID, dedupKey)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		u := stateOf(a)
		u.State, u.ResolvedAt = StateResolved, &at
		eventType := events.AlarmResolved
		if a.State == StatePending {
			eventType = ""
		}
		out, err = s.move(ctx, a, u, &Transition{At: at, Reason: reason}, eventType)
		if err == nil && eventType != "" {
			s.afterResolve(ctx, out)
		}
		return err
	})
	return out, err
}

// Acknowledge acknowledges an open alarm for actor.
func (s *Service) Acknowledge(ctx context.Context, orgID, id, actor, comment string) (*Alarm, error) {
	var out *Alarm
	err := s.inTx(ctx, orgID, func(ctx context.Context) error {
		a, err := s.store.LockForUpdate(ctx, orgID, id)
		if err != nil {
			return err
		}
		at := s.clock()
		u := stateOf(a)
		u.State, u.AcknowledgedAt, u.AcknowledgedBy = StateAcknowledged, &at, optional(actor)
		out, err = s.move(ctx, a, u, &Transition{At: at, ActorID: optional(actor), Reason: ReasonAcknowledged,
			Comment: optional(comment)}, events.AlarmAcknowledged)
		return err
	})
	return out, err
}

// Unacknowledge withdraws the ack of an alarm: it goes back to open. E-161
// has no event for it; the transition and the audit of the route (WP-983)
// record it.
func (s *Service) Unacknowledge(ctx context.Context, orgID, id, actor, comment string) (*Alarm, error) {
	var out *Alarm
	err := s.inTx(ctx, orgID, func(ctx context.Context) error {
		a, err := s.store.LockForUpdate(ctx, orgID, id)
		if err != nil {
			return err
		}
		u := stateOf(a)
		u.State, u.AcknowledgedAt, u.AcknowledgedBy = StateOpen, nil, nil
		out, err = s.move(ctx, a, u, &Transition{At: s.clock(), ActorID: optional(actor), Reason: ReasonUnacknowledged,
			Comment: optional(comment)}, "")
		return err
	})
	return out, err
}

// move checks the change of a to u against Transitions, writes it with the
// transition tr and records eventType (none when empty).
func (s *Service) move(ctx context.Context, a *Alarm, u *StateUpdate, tr *Transition, eventType string) (*Alarm, error) {
	if u.State != a.State && !CanTransition(a.State, u.State) {
		return nil, fmt.Errorf("%w: %s to %s", ErrInvalidTransition, a.State, u.State)
	}
	if u.State == a.State && u.Severity == a.Severity {
		return nil, fmt.Errorf("%w: %s to %s", ErrInvalidTransition, a.State, u.State)
	}
	// Reason and comment are checked before the state is written: the
	// memory store has no rollback.
	if err := validateTransitionText(tr.Reason, tr.Comment); err != nil {
		return nil, err
	}
	updated, err := s.store.UpdateState(ctx, a.OrganizationID, a.ID, u)
	if err != nil {
		return nil, err
	}
	from := a.State
	tr.FromState, tr.ToState = &from, u.State
	if u.Severity != a.Severity {
		fromSev, toSev := a.Severity, u.Severity
		tr.FromSeverity, tr.ToSeverity = &fromSev, &toSev
	}
	return updated, s.record(ctx, updated, tr, eventType)
}

// record appends the transition tr of a and the event eventType (none when
// empty).
func (s *Service) record(ctx context.Context, a *Alarm, tr *Transition, eventType string) error {
	tr.AlarmID = a.ID
	if _, err := s.store.AppendTransition(ctx, a.OrganizationID, tr); err != nil {
		return err
	}
	if eventType == "" || s.sink == nil {
		return nil
	}
	return s.sink.Append(ctx, &events.Event{OrgID: a.OrganizationID, Type: eventType, EntityID: a.ID, Data: eventData(a)})
}

// eventData is the data of an alarm.* event of a. ci_id, client_id and
// site_id are absent when the alarm has none (the null convention of the
// payload frames). Source, summary, value (details.value of a rule alarm),
// suppression and the times serve the notifications (WP-987); the actions
// of details.actions (defaults without) decide notify and webhook (WP-988).
func eventData(a *Alarm) events.AlarmData {
	d := events.AlarmData{ID: a.ID, AlarmID: a.ID, DedupKey: a.DedupKey, CIID: deref(a.CIID),
		ClientID: deref(a.ClientID), SiteID: deref(a.SiteID), Severity: a.Severity, IncidentID: deref(a.IncidentID),
		SourceKind: a.SourceKind, Summary: a.Summary, SuppressedReason: deref(a.SuppressedReason),
		OpenedAt: a.OpenedAt, ResolvedAt: a.ResolvedAt}
	if v, ok := a.Details["value"].(float64); ok {
		d.Value = &v
	}
	actions := actionsOf(a.Details)
	d.Actions = &actions
	return d
}

// observed is the time of a raise: ObservedAt, or now on the service's
// clock, so that the alarm and its transitions share one clock.
func (s *Service) observed(in *RaiseInput) time.Time {
	if in.ObservedAt.IsZero() {
		return s.clock()
	}
	return in.ObservedAt
}

// upsertOf is the store input of a raise in state observed at at.
func upsertOf(in *RaiseInput, state string, at time.Time) *UpsertInput {
	return &UpsertInput{SourceKind: in.SourceKind, SourceID: in.SourceID, SeriesKey: in.SeriesKey, Labels: in.Labels,
		CIID: in.CIID, ClientID: in.ClientID, Severity: in.Severity, State: state, Summary: in.Summary,
		Details: in.Details, ObservedAt: at}
}

// stateOf is the current state of a as a StateUpdate.
func stateOf(a *Alarm) *StateUpdate {
	return &StateUpdate{State: a.State, Severity: a.Severity, SuppressedReason: a.SuppressedReason,
		IncidentID: a.IncidentID, OpenedAt: a.OpenedAt, AcknowledgedAt: a.AcknowledgedAt,
		AcknowledgedBy: a.AcknowledgedBy, ResolvedAt: a.ResolvedAt}
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
