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
}

// NewMemoryStore returns an empty memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{alarms: map[string]*Alarm{}, clock: now}
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
		!eq(a.CIID, f.CIID), !eq(a.ClientID, f.ClientID), !eq(a.SiteID, f.SiteID), !eq(a.IncidentID, f.IncidentID),
		f.Suppressed != nil && *f.Suppressed != (a.SuppressedReason != nil),
		f.To != nil && !a.FirstSeenAt.Before(*f.To),
		f.From != nil && a.ResolvedAt != nil && a.ResolvedAt.Before(*f.From):
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
		if c := b.FirstSeenAt.Compare(a.FirstSeenAt); c != 0 {
			return c
		}
		return strings.Compare(b.ID, a.ID)
	})
	if f.After != nil {
		after := f.After
		items = slices.DeleteFunc(items, func(a Alarm) bool {
			c := a.FirstSeenAt.Compare(after.FirstSeenAt)
			return c > 0 || (c == 0 && a.ID >= after.ID)
		})
	}
	limit := limitOf(f)
	if len(items) > limit+1 {
		items = items[:limit+1]
	}
	return paged(items, limit), nil
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
func (m *MemoryStore) Transitions(_ context.Context, orgID, alarmID string) ([]Transition, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Transition
	for i := range m.transitions {
		if t := &m.transitions[i]; t.OrganizationID == orgID && t.AlarmID == alarmID {
			out = append(out, *t)
		}
	}
	slices.SortStableFunc(out, func(a, b Transition) int { return a.At.Compare(b.At) })
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
