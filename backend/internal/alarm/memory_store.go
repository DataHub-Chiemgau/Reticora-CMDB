package alarm

import (
	"context"
	"crypto/rand"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"
)

// MemoryStore keeps the alarms in memory (no database, tests). It follows
// the PostgreSQL store: one alarm per key that is not resolved, the state
// log only grows. It knows no client or site scope.
type MemoryStore struct {
	mu          sync.Mutex
	alarms      map[string]*Alarm
	transitions []Transition
	clock       func() time.Time
	// incidents and incidentTransitions are the incidents (WP-1027).
	incidents           map[string]*Incident
	incidentTransitions []IncidentTransition
	// settings are the alarm settings by organization (WP-1030).
	settings map[string]Settings
}

// NewMemoryStore returns an empty memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{alarms: map[string]*Alarm{}, incidents: map[string]*Incident{}, clock: now}
}

var _ Store = (*MemoryStore)(nil)

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// clone returns a deep copy of an alarm.
func clone(a *Alarm) *Alarm {
	c := *a
	c.Labels = maps.Clone(a.Labels)
	c.Details = maps.Clone(a.Details)
	return &c
}

// Get implements Store.
func (m *MemoryStore) Get(_ context.Context, orgID, id string) (*Alarm, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.alarms[id]
	if !ok || a.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	return clone(a), nil
}

// LockForUpdate implements Store; the memory store has no row locks.
func (m *MemoryStore) LockForUpdate(ctx context.Context, orgID, id string) (*Alarm, error) {
	return m.Get(ctx, orgID, id)
}

// LockOpenByKey implements Store.
func (m *MemoryStore) LockOpenByKey(_ context.Context, orgID, dedupKey string) (*Alarm, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.alarms {
		if a.OrganizationID == orgID && a.DedupKey == dedupKey && a.State != StateResolved {
			return clone(a), nil
		}
	}
	return nil, ErrNotFound
}

// matches reports whether a passes the filter (without cursor and limit).
func matches(a *Alarm, f *ListFilter) bool {
	eq := func(p *string, v string) bool { return v == "" || (p != nil && *p == v) }
	switch {
	case len(f.States) > 0 && !slices.Contains(f.States, a.State),
		len(f.Severities) > 0 && !slices.Contains(f.Severities, a.Severity),
		len(f.SourceKinds) > 0 && !slices.Contains(f.SourceKinds, a.SourceKind),
		f.SourceID != "" && !strings.EqualFold(a.SourceID, f.SourceID),
		!eq(a.CIID, f.CIID), !eq(a.ClientID, f.ClientID), !eq(a.SiteID, f.SiteID), !eq(a.IncidentID, f.IncidentID),
		f.ExcludeID != "" && a.ID == f.ExcludeID,
		f.Suppressed != nil && *f.Suppressed != (a.SuppressedReason != nil),
		f.To != nil && !a.FirstSeenAt.Before(*f.To),
		f.From != nil && a.ResolvedAt != nil && a.ResolvedAt.Before(*f.From),
		f.OpenedFrom != nil && (a.OpenedAt == nil || a.OpenedAt.Before(*f.OpenedFrom)),
		f.OpenedTo != nil && (a.OpenedAt == nil || !a.OpenedAt.Before(*f.OpenedTo)):
		return false
	}
	return true
}

// List implements Store.
func (m *MemoryStore) List(_ context.Context, orgID string, f *ListFilter) (Page, error) {
	m.mu.Lock()
	var items []Alarm
	for _, a := range m.alarms {
		if a.OrganizationID == orgID && matches(a, f) {
			items = append(items, *clone(a))
		}
	}
	m.mu.Unlock()
	slices.SortFunc(items, func(a, b Alarm) int {
		if c := b.SortAt().Compare(a.SortAt()); c != 0 {
			return c
		}
		return strings.Compare(b.ID, a.ID)
	})
	if f.After != nil {
		after := f.After
		items = slices.DeleteFunc(items, func(a Alarm) bool {
			c := a.SortAt().Compare(after.At)
			return c > 0 || (c == 0 && a.ID >= after.ID)
		})
	}
	limit := limitOf(f)
	if len(items) > limit+1 {
		items = items[:limit+1]
	}
	return paged(items, limit), nil
}

// Count implements Store.
func (m *MemoryStore) Count(_ context.Context, orgID string, f *ListFilter, limit int) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, a := range m.alarms {
		if a.OrganizationID == orgID && matches(a, f) {
			n++
			if limit > 0 && n >= limit {
				break
			}
		}
	}
	return n, nil
}

// UpsertOpen implements Store.
func (m *MemoryStore) UpsertOpen(_ context.Context, orgID string, in *UpsertInput) (*Alarm, bool, error) {
	if err := ValidateUpsert(in); err != nil {
		return nil, false, err
	}
	at := in.ObservedAt
	if at.IsZero() {
		at = m.clock()
	}
	key := DedupKey(in.SourceKind, in.SourceID, in.SeriesKey)
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.alarms {
		if a.OrganizationID == orgID && a.DedupKey == key && a.State != StateResolved {
			a.OccurrenceCount++
			if at.After(a.LastSeenAt) {
				a.LastSeenAt = at
			}
			return clone(a), false, nil
		}
	}
	a := &Alarm{
		ID: newID(), OrganizationID: orgID, CIID: in.CIID,
		SourceKind: in.SourceKind, SourceID: in.SourceID, SeriesKey: in.SeriesKey,
		Labels: maps.Clone(in.Labels), DedupKey: key,
		Severity: in.Severity, State: in.State, OccurrenceCount: 1,
		FirstSeenAt: at, LastSeenAt: at, Summary: in.Summary, Details: maps.Clone(in.Details), UpdatedAt: m.clock(),
	}
	if in.CIID == nil {
		a.ClientID = in.ClientID
	}
	if a.Labels == nil {
		a.Labels = map[string]string{}
	}
	if a.Details == nil {
		a.Details = map[string]any{}
	}
	if in.State == StateOpen {
		opened := at
		a.OpenedAt = &opened
	}
	m.alarms[a.ID] = a
	return clone(a), true, nil
}

// UpdateState implements Store.
func (m *MemoryStore) UpdateState(_ context.Context, orgID, id string, u *StateUpdate) (*Alarm, error) {
	if err := validateState(u); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.alarms[id]
	if !ok || a.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	if u.State != StateResolved && a.State == StateResolved {
		for _, o := range m.alarms {
			if o.ID != a.ID && o.OrganizationID == orgID && o.DedupKey == a.DedupKey && o.State != StateResolved {
				return nil, fmt.Errorf("alarm: another alarm of key %s is not resolved", a.DedupKey)
			}
		}
	}
	a.State, a.Severity, a.SuppressedReason, a.IncidentID = u.State, u.Severity, u.SuppressedReason, u.IncidentID
	a.OpenedAt, a.AcknowledgedAt, a.AcknowledgedBy, a.ResolvedAt = u.OpenedAt, u.AcknowledgedAt, u.AcknowledgedBy, u.ResolvedAt
	if u.Details != nil {
		a.Details = maps.Clone(u.Details)
	}
	a.UpdatedAt = m.clock()
	return clone(a), nil
}

// AppendTransition implements Store.
func (m *MemoryStore) AppendTransition(_ context.Context, orgID string, in *Transition) (*Transition, error) {
	if err := ValidateTransition(in); err != nil {
		return nil, err
	}
	tr := *in
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.alarms[tr.AlarmID]
	if !ok || a.OrganizationID != orgID {
		return nil, fmt.Errorf("alarm: append transition: %w", ErrNotFound)
	}
	tr.ID, tr.OrganizationID, tr.ClientID, tr.SiteID = newID(), orgID, a.ClientID, a.SiteID
	if tr.At.IsZero() {
		tr.At = m.clock()
	}
	m.transitions = append(m.transitions, tr)
	return &tr, nil
}

// Transitions implements Store.
func (m *MemoryStore) Transitions(ctx context.Context, orgID, alarmID string) ([]Transition, error) {
	return m.TransitionsUpTo(ctx, orgID, alarmID, 0)
}

// TransitionsUpTo implements Store.
func (m *MemoryStore) TransitionsUpTo(_ context.Context, orgID, alarmID string, limit int) ([]Transition, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Transition
	for i := range m.transitions {
		if t := &m.transitions[i]; t.OrganizationID == orgID && t.AlarmID == alarmID {
			out = append(out, *t)
		}
	}
	slices.SortStableFunc(out, func(a, b Transition) int {
		if c := a.At.Compare(b.At); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// PurgeResolved removes the resolved alarms of the organization resolved
// before cutoff, at most limit, with their transitions, like the retention
// does in PostgreSQL (Retention), and returns their number.
func (m *MemoryStore) PurgeResolved(orgID string, cutoff time.Time, limit int) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	removed := map[string]bool{}
	for id, a := range m.alarms {
		if len(removed) >= limit {
			break
		}
		if a.OrganizationID == orgID && a.State == StateResolved && a.ResolvedAt != nil && a.ResolvedAt.Before(cutoff) {
			removed[id] = true
			delete(m.alarms, id)
		}
	}
	m.transitions = slices.DeleteFunc(m.transitions, func(t Transition) bool { return removed[t.AlarmID] })
	return len(removed)
}

// PurgeResolvedIncidents removes the resolved incidents of the organization
// resolved before cutoff, at most limit, with their transitions; their
// alarms lose the reference (ON DELETE SET NULL). It returns their number.
func (m *MemoryStore) PurgeResolvedIncidents(orgID string, cutoff time.Time, limit int) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	removed := map[string]bool{}
	for id, inc := range m.incidents {
		if len(removed) >= limit {
			break
		}
		if inc.OrganizationID == orgID && inc.State == IncidentResolved && inc.ResolvedAt != nil && inc.ResolvedAt.Before(cutoff) {
			removed[id] = true
			delete(m.incidents, id)
		}
	}
	for _, a := range m.alarms {
		if a.IncidentID != nil && removed[*a.IncidentID] {
			a.IncidentID = nil
		}
	}
	m.incidentTransitions = slices.DeleteFunc(m.incidentTransitions, func(t IncidentTransition) bool { return removed[t.IncidentID] })
	return len(removed)
}

// cloneIncident returns a deep copy of an incident.
func cloneIncident(inc *Incident) *Incident {
	c := *inc
	c.Reasons = slices.Clone(inc.Reasons)
	c.AffectedCounts = maps.Clone(inc.AffectedCounts)
	return &c
}

// incidentOf returns the incident of the organization (lock held).
func (m *MemoryStore) incidentOf(orgID, id string) (*Incident, error) {
	inc, ok := m.incidents[id]
	if !ok || inc.OrganizationID != orgID {
		return nil, ErrIncidentNotFound
	}
	return inc, nil
}

// appendIncidentTransition appends to the log of inc (lock held).
func (m *MemoryStore) appendIncidentTransition(inc *Incident, from *string, to string, at time.Time, actor *string,
	reason string, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	m.incidentTransitions = append(m.incidentTransitions, IncidentTransition{
		ID: newID(), OrganizationID: inc.OrganizationID, ClientID: inc.ClientID, SiteID: inc.SiteID, IncidentID: inc.ID,
		FromState: from, ToState: to, At: at, ActorID: actor, Reason: reason, Details: details})
}

// UpsertIncident implements Store.
func (m *MemoryStore) UpsertIncident(_ context.Context, orgID string, in *IncidentInput) (*Incident, bool, error) {
	if err := ValidateIncident(in); err != nil {
		return nil, false, err
	}
	at := in.At
	if at.IsZero() {
		at = m.clock()
	}
	reasons := slices.Clone(in.Reasons)
	if reasons == nil {
		reasons = []string{}
	}
	counts := maps.Clone(in.AffectedCounts)
	if counts == nil {
		counts = map[string]int{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if in.ID == "" {
		inc := &Incident{ID: newID(), OrganizationID: orgID, ClientID: in.ClientID, SiteID: in.SiteID, RootCIID: in.RootCIID,
			CauseClass: causeOrUnknown(in.CauseClass), Confidence: in.Confidence, Reasons: reasons, State: IncidentOpen,
			OpenedAt: at, AffectedCounts: counts, UpdatedAt: m.clock()}
		m.incidents[inc.ID] = inc
		m.appendIncidentTransition(inc, nil, IncidentOpen, at, nil, IncidentReasonOpened,
			map[string]any{"cause_class": inc.CauseClass, "confidence": inc.Confidence})
		return cloneIncident(inc), true, nil
	}
	inc, err := m.incidentOf(orgID, in.ID)
	if err != nil {
		return nil, false, err
	}
	if inc.State == IncidentResolved {
		return nil, false, ErrIncidentResolved
	}
	inc.CauseClass, inc.Confidence, inc.Reasons, inc.AffectedCounts = causeOrUnknown(in.CauseClass), in.Confidence, reasons, counts
	inc.UpdatedAt = m.clock()
	state := inc.State
	m.appendIncidentTransition(inc, &state, inc.State, at, nil, IncidentReasonUpdated,
		map[string]any{"cause_class": inc.CauseClass, "confidence": inc.Confidence})
	return cloneIncident(inc), false, nil
}

// GetIncident implements Store.
func (m *MemoryStore) GetIncident(_ context.Context, orgID, id string) (*Incident, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inc, err := m.incidentOf(orgID, id)
	if err != nil {
		return nil, err
	}
	return cloneIncident(inc), nil
}

// CountIncidents implements Store.
func (m *MemoryStore) CountIncidents(_ context.Context, orgID string, f *IncidentFilter, limit int) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, inc := range m.incidents {
		if incidentMatches(inc, orgID, f) {
			n++
		}
	}
	if limit > 0 && n > limit {
		n = limit
	}
	return n, nil
}

// incidentMatches reports whether inc passes the filter (without cursor
// and limit); the caller holds m.mu.
func incidentMatches(inc *Incident, orgID string, f *IncidentFilter) bool {
	switch {
	case inc.OrganizationID != orgID,
		len(f.States) > 0 && !slices.Contains(f.States, inc.State),
		len(f.CauseClasses) > 0 && !slices.Contains(f.CauseClasses, inc.CauseClass),
		f.ClientID != "" && (inc.ClientID == nil || *inc.ClientID != f.ClientID),
		f.To != nil && !inc.OpenedAt.Before(*f.To),
		f.From != nil && inc.ResolvedAt != nil && inc.ResolvedAt.Before(*f.From),
		f.OpenedFrom != nil && inc.OpenedAt.Before(*f.OpenedFrom),
		f.OpenedTo != nil && !inc.OpenedAt.Before(*f.OpenedTo):
		return false
	}
	return true
}

// ListIncidents implements Store.
func (m *MemoryStore) ListIncidents(_ context.Context, orgID string, f *IncidentFilter) (IncidentPage, error) {
	m.mu.Lock()
	var items []Incident
	for _, inc := range m.incidents {
		if incidentMatches(inc, orgID, f) {
			items = append(items, *cloneIncident(inc))
		}
	}
	m.mu.Unlock()
	slices.SortFunc(items, func(a, b Incident) int {
		if c := b.OpenedAt.Compare(a.OpenedAt); c != 0 {
			return c
		}
		return strings.Compare(b.ID, a.ID)
	})
	if f.After != nil {
		after := f.After
		items = slices.DeleteFunc(items, func(inc Incident) bool {
			c := inc.OpenedAt.Compare(after.OpenedAt)
			return c > 0 || (c == 0 && inc.ID >= after.ID)
		})
	}
	limit := incidentLimitOf(f)
	if len(items) > limit+1 {
		items = items[:limit+1]
	}
	return pagedIncidents(items, limit), nil
}

// attachable reports whether a may be attached to incidentID: it belongs to
// no incident or to that one.
func attachable(a *Alarm, incidentID string) bool {
	return a.IncidentID == nil || *a.IncidentID == incidentID
}

// attach assigns the alarm to an incident as root or symptom (lock held).
func attach(a *Alarm, incidentID string, root bool) {
	id := incidentID
	a.IncidentID = &id
	switch {
	case root && a.SuppressedReason != nil && *a.SuppressedReason == SuppressedSymptom:
		a.SuppressedReason = nil
	case !root && a.SuppressedReason == nil:
		reason := SuppressedSymptom
		a.SuppressedReason = &reason
	}
}

// setRoot makes a (attached already) the root of inc (lock held).
func (m *MemoryStore) setRoot(inc *Incident, a *Alarm, manual bool, at time.Time, actor *string, reason string) {
	if inc.RootAlarmID != nil && *inc.RootAlarmID != a.ID {
		if prev, ok := m.alarms[*inc.RootAlarmID]; ok && prev.IncidentID != nil && *prev.IncidentID == inc.ID && prev.SuppressedReason == nil {
			symptom := SuppressedSymptom
			prev.SuppressedReason = &symptom
		}
	}
	from := deref(inc.RootAlarmID)
	id := a.ID
	inc.RootAlarmID, inc.RootCIID, inc.ManualRoot = &id, a.CIID, inc.ManualRoot || manual
	inc.UpdatedAt = m.clock()
	state := inc.State
	details := map[string]any{"from_alarm_id": from, "to_alarm_id": a.ID, "manual": manual}
	if reason != "" {
		details["reason"] = reason
	}
	m.appendIncidentTransition(inc, &state, inc.State, at, actor, IncidentReasonRootChanged, details)
}

// AttachAlarms implements Store.
func (m *MemoryStore) AttachAlarms(_ context.Context, orgID, incidentID string, alarmIDs []string, role string) (*Incident, error) {
	ids := uniqueIDs(alarmIDs)
	if err := validateAttach(ids, role); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	inc, err := m.incidentOf(orgID, incidentID)
	if err != nil {
		return nil, err
	}
	if cerr := checkAttach(inc, role); cerr != nil {
		return nil, cerr
	}
	targets := make([]*Alarm, 0, len(ids))
	for _, id := range ids {
		a, ok := m.alarms[id]
		if !ok || a.OrganizationID != orgID {
			return nil, fmt.Errorf("alarm: attach: %w", ErrNotFound)
		}
		if !attachable(a, incidentID) {
			return nil, fmt.Errorf("alarm: attach: %w", ErrAlarmInOtherIncident)
		}
		targets = append(targets, a)
	}
	at := m.clock()
	for _, a := range targets {
		attach(a, incidentID, role == RoleRoot)
	}
	if role == RoleRoot {
		m.setRoot(inc, targets[0], false, at, nil, "")
	} else {
		state := inc.State
		m.appendIncidentTransition(inc, &state, inc.State, at, nil, IncidentReasonAlarmsAttached,
			map[string]any{"alarm_ids": ids, "role": role})
	}
	return cloneIncident(inc), nil
}

// SetRoot implements Store.
func (m *MemoryStore) SetRoot(_ context.Context, orgID, incidentID, alarmID string, manual bool) (*Incident, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inc, err := m.incidentOf(orgID, incidentID)
	if err != nil {
		return nil, err
	}
	if cerr := checkRoot(inc, manual); cerr != nil {
		return nil, cerr
	}
	a, ok := m.alarms[alarmID]
	if !ok || a.OrganizationID != orgID {
		return nil, fmt.Errorf("alarm: set root: %w", ErrNotFound)
	}
	if !attachable(a, incidentID) {
		return nil, fmt.Errorf("alarm: attach: %w", ErrAlarmInOtherIncident)
	}
	attach(a, incidentID, true)
	m.setRoot(inc, a, manual, m.clock(), nil, "")
	return cloneIncident(inc), nil
}

// SetRootManual implements Store.
func (m *MemoryStore) SetRootManual(_ context.Context, orgID, incidentID, alarmID string, actor *string, reason string) (*Incident, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inc, err := m.incidentOf(orgID, incidentID)
	if err != nil {
		return nil, err
	}
	if cerr := checkRoot(inc, true); cerr != nil {
		return nil, cerr
	}
	a, ok := m.alarms[alarmID]
	if !ok || a.OrganizationID != orgID || a.IncidentID == nil || *a.IncidentID != incidentID {
		return nil, ErrAlarmNotMember
	}
	attach(a, incidentID, true)
	m.setRoot(inc, a, true, m.clock(), actor, reason)
	return cloneIncident(inc), nil
}

// CINames implements Store: the memory store knows no CIs.
func (m *MemoryStore) CINames(_ context.Context, _ string, _ []string) (map[string]string, error) {
	return map[string]string{}, nil
}

// MergeIncidents implements Store.
func (m *MemoryStore) MergeIncidents(_ context.Context, orgID, keepID, absorbID, rootAlarmID string) (*Incident, error) {
	if keepID == absorbID {
		return nil, fmt.Errorf("alarm: merge an incident into itself")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	keep, err := m.incidentOf(orgID, keepID)
	if err != nil {
		return nil, err
	}
	absorb, err := m.incidentOf(orgID, absorbID)
	if err != nil {
		return nil, err
	}
	if keep.State == IncidentResolved || absorb.State == IncidentResolved {
		return nil, ErrIncidentResolved
	}
	if keep.ManualRoot || (keep.RootAlarmID != nil && *keep.RootAlarmID == rootAlarmID) {
		rootAlarmID = ""
	}
	var root *Alarm
	if rootAlarmID != "" {
		a, ok := m.alarms[rootAlarmID]
		if !ok || a.OrganizationID != orgID {
			return nil, fmt.Errorf("alarm: merge root: %w", ErrNotFound)
		}
		// The alarms of absorb move to keep first.
		if !attachable(a, keepID) && !attachable(a, absorbID) {
			return nil, fmt.Errorf("alarm: attach: %w", ErrAlarmInOtherIncident)
		}
		root = a
	}
	at := m.clock()
	moved := 0
	for _, a := range m.alarms {
		if a.OrganizationID == orgID && a.IncidentID != nil && *a.IncidentID == absorbID {
			attach(a, keepID, a.ID == rootAlarmID)
			moved++
		}
	}
	absorbFrom, absorbRoot := absorb.State, deref(absorb.RootAlarmID)
	absorb.State, absorb.ResolvedAt, absorb.RootAlarmID, absorb.UpdatedAt = IncidentResolved, &at, nil, m.clock()
	m.appendIncidentTransition(absorb, &absorbFrom, IncidentResolved, at, nil, IncidentReasonMergedInto,
		map[string]any{"into": keepID, "alarms": moved, "root_alarm_id": absorbRoot})
	keepState := keep.State
	m.appendIncidentTransition(keep, &keepState, keep.State, at, nil, IncidentReasonAbsorbed,
		map[string]any{"absorbed": absorbID, "alarms": moved})
	if root != nil {
		attach(root, keepID, true)
		m.setRoot(keep, root, false, at, nil, "")
	}
	return cloneIncident(keep), nil
}

// TransitionIncident implements Store.
func (m *MemoryStore) TransitionIncident(_ context.Context, orgID, id, to string, actorID *string) (*Incident, error) {
	return m.transitionIncident(orgID, id, to, actorID, nil)
}

// AcknowledgeIncident implements Store.
func (m *MemoryStore) AcknowledgeIncident(_ context.Context, orgID, id string, actorID *string, comment string) (*Incident, error) {
	return m.transitionIncident(orgID, id, IncidentAcknowledged, actorID, commentDetails(comment))
}

func (m *MemoryStore) transitionIncident(orgID, id, to string, actorID *string, details map[string]any) (*Incident, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inc, err := m.incidentOf(orgID, id)
	if err != nil {
		return nil, err
	}
	if !incidentTransitionAllowed(inc.State, to) {
		return nil, fmt.Errorf("%w: incident %s to %s", ErrInvalidTransition, inc.State, to)
	}
	at := m.clock()
	from := inc.State
	switch to {
	case IncidentAcknowledged:
		inc.AcknowledgedAt, inc.AcknowledgedBy = &at, actorID
	case IncidentResolved:
		inc.ResolvedAt = &at
	default:
		inc.AcknowledgedAt, inc.AcknowledgedBy = nil, nil
	}
	inc.State, inc.UpdatedAt = to, m.clock()
	m.appendIncidentTransition(inc, &from, to, at, actorID, incidentReason(from, to), details)
	return cloneIncident(inc), nil
}

// IncidentTransitions implements Store.
func (m *MemoryStore) IncidentTransitions(_ context.Context, orgID, incidentID string) ([]IncidentTransition, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []IncidentTransition
	for i := range m.incidentTransitions {
		if t := &m.incidentTransitions[i]; t.OrganizationID == orgID && t.IncidentID == incidentID {
			c := *t
			c.Details = maps.Clone(t.Details)
			out = append(out, c)
		}
	}
	slices.SortStableFunc(out, func(a, b IncidentTransition) int { return a.At.Compare(b.At) })
	return out, nil
}
