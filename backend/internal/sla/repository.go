package sla

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
)

var (
	// ErrNotFound: the SLA policy is unknown, of another organization or
	// outside the caller's scope, or no policy matches the ticket; all
	// answer alike (TEN-04).
	ErrNotFound = errors.New("sla policy not found")
	// ErrTicketSLANotFound: the ticket has no visible SLA state (404).
	ErrTicketSLANotFound = errors.New("ticket sla not found")
)

// Repository defines persistence operations for SLA policies and ticket state.
type Repository interface {
	ListPolicies(ctx context.Context, orgID, priority, clientID string, page api.PaginationParams) ([]Policy, int, error)
	GetPolicy(ctx context.Context, orgID, id string) (*Policy, error)
	CreatePolicy(ctx context.Context, p *Policy) error
	UpdatePolicy(ctx context.Context, orgID, id string, req UpdatePolicyRequest) (*Policy, error)
	DeletePolicy(ctx context.Context, orgID, id string) error
	ApplyForTicket(ctx context.Context, orgID string, t *ticket.Ticket, slaID string) (*TicketSLA, error)
	GetForTicket(ctx context.Context, orgID, ticketID string) (*TicketSLA, error)
	MarkFirstResponse(ctx context.Context, orgID, ticketID string, at time.Time) error
	MarkResolved(ctx context.Context, orgID, ticketID string, at time.Time) error
	ListBreaches(ctx context.Context, orgID string, filter BreachFilter, page api.PaginationParams) ([]TicketSLA, int, error)
	// Business calendars (TKT-02, WP-768). ListCalendars returns at most
	// one row more than the page limit, sorted by name and id, and the
	// number of matching calendars.
	ListCalendars(ctx context.Context, orgID string, filter BusinessCalendarFilter, page api.PaginationParams) ([]BusinessCalendar, int, error)
	GetCalendar(ctx context.Context, orgID, id string) (*BusinessCalendar, error)
	CreateCalendar(ctx context.Context, c *BusinessCalendar) error
	UpdateCalendar(ctx context.Context, orgID, id string, req *UpdateBusinessCalendarRequest) (*BusinessCalendar, error)
	DeleteCalendar(ctx context.Context, orgID, id string) error
}

var (
	// ErrCalendarNotFound: the business calendar is unknown, of another
	// organization, outside the caller's scope or (for a change) an
	// org-wide calendar of a client-scoped principal; all answer alike
	// with 404 (TEN-04, E-09).
	ErrCalendarNotFound = errors.New("business calendar not found")
	// errCalendarDefaultTaken: a second default calendar for the same
	// organization and client (409 on is_default).
	errCalendarDefaultTaken = &api.ConflictError{Field: "is_default", Message: "another calendar is already the default of this organization and client"}
)

// MemoryRepository is an in-memory implementation of Repository.
type MemoryRepository struct {
	mu       sync.RWMutex
	policies map[string]*Policy
	states   map[string]*TicketSLA
	// calendars are the stored business calendars by id; schedules the
	// schedule each ticket's due times were computed with (nil: elapsed).
	calendars map[string]*memoryCalendar
	schedules map[string]*Schedule
	nextID    int
}

// memoryCalendar is a stored business calendar of the memory repository.
type memoryCalendar struct {
	ID, OrganizationID, ClientID string
	Name                         string
	IsDefault                    bool
	Calendar                     Calendar
	CreatedAt, UpdatedAt         time.Time
}

// toAPI returns the stored calendar in the form of the REST API.
func (c *memoryCalendar) toAPI() BusinessCalendar {
	out := BusinessCalendar{ID: c.ID, OrganizationID: c.OrganizationID, Name: c.Name, IsDefault: c.IsDefault,
		WeeklyHours: append([]Window{}, c.Calendar.WeeklyHours...), Holidays: append([]Holiday{}, c.Calendar.Holidays...),
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
	if c.ClientID != "" {
		v := c.ClientID
		out.ClientID = &v
	}
	if c.Calendar.Timezone != "" {
		v := c.Calendar.Timezone
		out.Timezone = &v
	}
	return out
}

// defaultTakenLocked reports whether another calendar than id is the
// default of the organization and client (the partial unique index of
// the PostgreSQL repository).
func (r *MemoryRepository) defaultTakenLocked(orgID, clientID, id string) bool {
	for _, c := range r.calendars {
		if c.ID != id && c.OrganizationID == orgID && c.ClientID == clientID && c.IsDefault {
			return true
		}
	}
	return false
}

// ListCalendars pages the organization's calendars by name and id like
// the PostgreSQL repository (memory calendars carry no client scope).
func (r *MemoryRepository) ListCalendars(_ context.Context, orgID string, filter BusinessCalendarFilter, page api.PaginationParams) ([]BusinessCalendar, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var all []BusinessCalendar
	for _, c := range r.calendars {
		if c.OrganizationID != orgID || filter.ClientID != "" && c.ClientID != filter.ClientID {
			continue
		}
		all = append(all, c.toAPI())
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Name != all[j].Name {
			return all[i].Name < all[j].Name
		}
		return all[i].ID < all[j].ID
	})
	start := min(page.Offset, len(all))
	if page.Cursor != nil {
		name, id, err := calendarPosition(page.Cursor)
		if err != nil {
			return nil, 0, err
		}
		start = len(all)
		for i := range all {
			if api.KeysetCompare(all[i].Name, all[i].ID, api.Cursor{Value: name, ID: id}, calendarDir) {
				start = i
				break
			}
		}
	}
	end := min(start+max(page.Limit, 0)+1, len(all))
	return all[start:end], len(all), nil
}

func (r *MemoryRepository) GetCalendar(_ context.Context, orgID, id string) (*BusinessCalendar, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.calendars[id]
	if !ok || c.OrganizationID != orgID {
		return nil, ErrCalendarNotFound
	}
	out := c.toAPI()
	return &out, nil
}

func (r *MemoryRepository) CreateCalendar(_ context.Context, b *BusinessCalendar) error {
	if err := b.validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	clientID := ""
	if b.ClientID != nil {
		clientID = *b.ClientID
	}
	if b.IsDefault && r.defaultTakenLocked(b.OrganizationID, clientID, "") {
		return errCalendarDefaultTaken
	}
	r.nextID++
	now := time.Now().UTC()
	c := &memoryCalendar{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", r.nextID), OrganizationID: b.OrganizationID,
		ClientID: clientID, Name: b.Name, IsDefault: b.IsDefault, Calendar: b.Calendar(), CreatedAt: now, UpdatedAt: now}
	r.calendars[c.ID] = c
	*b = c.toAPI()
	return nil
}

func (r *MemoryRepository) UpdateCalendar(_ context.Context, orgID, id string, req *UpdateBusinessCalendarRequest) (*BusinessCalendar, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.calendars[id]
	if !ok || c.OrganizationID != orgID {
		return nil, ErrCalendarNotFound
	}
	next := c.toAPI()
	req.apply(&next)
	if err := next.validate(); err != nil {
		return nil, err
	}
	if next.IsDefault && r.defaultTakenLocked(orgID, c.ClientID, id) {
		return nil, errCalendarDefaultTaken
	}
	c.Name, c.IsDefault, c.Calendar, c.UpdatedAt = next.Name, next.IsDefault, next.Calendar(), time.Now().UTC()
	out := c.toAPI()
	return &out, nil
}

// DeleteCalendar removes the calendar and clears the references of the
// policies and SLA states like ON DELETE SET NULL; computed due times stay.
func (r *MemoryRepository) DeleteCalendar(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.calendars[id]
	if !ok || c.OrganizationID != orgID {
		return ErrCalendarNotFound
	}
	delete(r.calendars, id)
	for _, p := range r.policies {
		if p.OrganizationID == orgID && p.CalendarID != nil && *p.CalendarID == id {
			p.CalendarID = nil
		}
	}
	for _, s := range r.states {
		if s.OrganizationID == orgID && s.CalendarID != nil && *s.CalendarID == id {
			s.CalendarID = nil
		}
	}
	return nil
}

// NewMemoryRepository creates a memory SLA repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{policies: make(map[string]*Policy), states: make(map[string]*TicketSLA),
		calendars: make(map[string]*memoryCalendar), schedules: make(map[string]*Schedule)}
}

// checkCalendarLocked mirrors checkCalendarBinding of the PostgreSQL
// repository: the calendar exists in the organization and its client is
// empty or the policy's.
func (r *MemoryRepository) checkCalendarLocked(orgID string, calendarID *string, clientID string) error {
	if calendarID == nil {
		return nil
	}
	c, ok := r.calendars[*calendarID]
	if !ok || c.OrganizationID != orgID || c.ClientID != "" && c.ClientID != clientID {
		return errCalendarBinding
	}
	return nil
}

func (r *MemoryRepository) ListPolicies(_ context.Context, orgID, priority, clientID string, page api.PaginationParams) ([]Policy, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Policy
	for _, p := range r.policies {
		if p.OrganizationID != orgID || (priority != "" && p.Priority != priority) || (clientID != "" && p.ClientID != clientID) {
			continue
		}
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return pagePolicies(out, page)
}

func (r *MemoryRepository) GetPolicy(_ context.Context, orgID, id string) (*Policy, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.policies[id]
	if !ok || p.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	cp := *p
	return &cp, nil
}

func (r *MemoryRepository) CreatePolicy(_ context.Context, p *Policy) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkCalendarLocked(p.OrganizationID, p.CalendarID, p.ClientID); err != nil {
		return err
	}
	r.nextID++
	// UUID form, as the handlers accept only UUID ids (API-03).
	p.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", r.nextID)
	now := time.Now().UTC()
	p.CreatedAt, p.UpdatedAt = now, now
	cp := *p
	r.policies[p.ID] = &cp
	return nil
}

func (r *MemoryRepository) UpdatePolicy(_ context.Context, orgID, id string, req UpdatePolicyRequest) (*Policy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.policies[id]
	if !ok || p.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	next := *p
	if req.ClientID != nil {
		next.ClientID = *req.ClientID
	}
	if req.CalendarID.Set {
		next.CalendarID = nil
		if req.CalendarID.Value != "" {
			v := req.CalendarID.Value
			next.CalendarID = &v
		}
	}
	if err := r.checkCalendarLocked(orgID, next.CalendarID, next.ClientID); err != nil {
		return nil, err
	}
	p.ClientID, p.CalendarID = next.ClientID, next.CalendarID
	if req.Name != nil {
		p.Name = *req.Name
	}
	if req.Priority != nil {
		p.Priority = *req.Priority
	}
	if req.ResponseTargetMinutes != nil {
		p.ResponseTargetMinutes = *req.ResponseTargetMinutes
	}
	if req.ResolutionTargetMinutes != nil {
		p.ResolutionTargetMinutes = *req.ResolutionTargetMinutes
	}
	if req.BusinessCalendar != nil {
		p.BusinessCalendar = *req.BusinessCalendar
	}
	p.UpdatedAt = time.Now().UTC()
	cp := *p
	return &cp, nil
}

func (r *MemoryRepository) DeletePolicy(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.policies[id]
	if !ok || p.OrganizationID != orgID {
		return ErrNotFound
	}
	delete(r.policies, id)
	return nil
}

func (r *MemoryRepository) ApplyForTicket(_ context.Context, orgID string, t *ticket.Ticket, slaID string) (*TicketSLA, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	policy := r.findPolicyLocked(orgID, t.Priority, slaID)
	if policy == nil {
		return nil, ErrNotFound
	}
	calendarID, sched := r.scheduleLocked(orgID, policy)
	r.nextID++
	state, err := buildState(fmt.Sprintf("00000000-0000-4000-8000-%012d", r.nextID), orgID, t, policy, sched)
	if err != nil {
		return nil, err
	}
	state.CalendarID = calendarID
	r.states[t.ID] = state
	r.schedules[t.ID] = sched
	cp := *state
	fillRemaining(&cp, sched, time.Now().UTC())
	return &cp, nil
}

func (r *MemoryRepository) GetForTicket(_ context.Context, orgID, ticketID string) (*TicketSLA, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.states[ticketID]
	if !ok || state.OrganizationID != orgID {
		return nil, ErrTicketSLANotFound
	}
	now := time.Now().UTC()
	refreshState(state, now)
	cp := *state
	fillRemaining(&cp, r.schedules[ticketID], now)
	return &cp, nil
}

func (r *MemoryRepository) MarkFirstResponse(_ context.Context, orgID, ticketID string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if state, ok := r.states[ticketID]; ok && state.OrganizationID == orgID && state.FirstResponseAt == nil {
		v := at.UTC()
		state.FirstResponseAt = &v
		refreshState(state, v)
	}
	return nil
}

func (r *MemoryRepository) MarkResolved(_ context.Context, orgID, ticketID string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if state, ok := r.states[ticketID]; ok && state.OrganizationID == orgID && state.ResolvedAt == nil {
		v := at.UTC()
		state.ResolvedAt = &v
		refreshState(state, v)
	}
	return nil
}

func (r *MemoryRepository) ListBreaches(_ context.Context, orgID string, filter BreachFilter, page api.PaginationParams) ([]TicketSLA, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	var out []TicketSLA
	for _, state := range r.states {
		if state.OrganizationID != orgID {
			continue
		}
		refreshState(state, now)
		breached := state.ResponseBreached || state.ResolutionBreached
		atRisk := !breached && (state.FirstResponseAt == nil && state.ResponseDueAt.Sub(now) <= time.Hour || state.ResolvedAt == nil && state.ResolutionDueAt.Sub(now) <= time.Hour)
		if filter.Status == "breached" && !breached {
			continue
		}
		if filter.Status == "at_risk" && !atRisk {
			continue
		}
		item := *state
		fillRemaining(&item, r.schedules[state.TicketID], now)
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ResolutionDueAt.Before(out[j].ResolutionDueAt) })
	return pageStates(out, page)
}

// findPolicyLocked returns the named policy or the newest org-wide policy
// of the priority: memory tickets carry no client, so like the PostgreSQL
// repository for a ticket without client only org policies match.
func (r *MemoryRepository) findPolicyLocked(orgID, priority, slaID string) *Policy {
	if slaID != "" {
		p := r.policies[slaID]
		if p != nil && p.OrganizationID == orgID {
			return p
		}
		return nil
	}
	var best *Policy
	for _, p := range r.policies {
		if p.OrganizationID == orgID && p.Priority == priority && p.ClientID == "" &&
			(best == nil || p.CreatedAt.After(best.CreatedAt) || p.CreatedAt.Equal(best.CreatedAt) && p.ID > best.ID) {
			best = p
		}
	}
	return best
}

// scheduleLocked applies the calendar chain without the client level
// (memory tickets carry no client): the policy's calendar, the org
// default, the built-in standard; without a zone at the calendar it
// computes in UTC. A nil schedule means elapsed time.
func (r *MemoryRepository) scheduleLocked(orgID string, p *Policy) (*string, *Schedule) {
	if !p.BusinessCalendar {
		return nil, nil
	}
	var chosen *memoryCalendar
	if p.CalendarID != nil {
		if c, ok := r.calendars[*p.CalendarID]; ok && c.OrganizationID == orgID {
			chosen = c
		}
	}
	if chosen == nil {
		for _, c := range r.calendars {
			if c.OrganizationID == orgID && c.ClientID == "" && c.IsDefault {
				chosen = c
				break
			}
		}
	}
	if chosen == nil {
		cal := DefaultCalendar()
		return nil, cal.In(time.UTC)
	}
	loc := time.UTC
	if chosen.Calendar.Timezone != "" {
		if l, err := time.LoadLocation(chosen.Calendar.Timezone); err == nil {
			loc = l
		}
	}
	id := chosen.ID
	return &id, chosen.Calendar.In(loc)
}

func buildState(id, orgID string, t *ticket.Ticket, p *Policy, sched *Schedule) (*TicketSLA, error) {
	created := t.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	due := func(minutes int) (time.Time, error) {
		if sched == nil {
			return created.Add(time.Duration(minutes) * time.Minute), nil
		}
		return sched.Add(created, minutes)
	}
	responseDue, err := due(p.ResponseTargetMinutes)
	if err != nil {
		return nil, err
	}
	resolutionDue, err := due(p.ResolutionTargetMinutes)
	if err != nil {
		return nil, err
	}
	state := &TicketSLA{ID: id, OrganizationID: orgID, TicketID: t.ID, SLAID: p.ID, ResponseDueAt: responseDue, ResolutionDueAt: resolutionDue, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if t.ResolvedAt != "" {
		if parsed, err := time.Parse(time.RFC3339, t.ResolvedAt); err == nil {
			state.ResolvedAt = &parsed
		}
	}
	refreshState(state, time.Now().UTC())
	return state, nil
}

func refreshState(state *TicketSLA, now time.Time) {
	if state.FirstResponseAt != nil {
		state.ResponseBreached = state.FirstResponseAt.After(state.ResponseDueAt)
	} else {
		state.ResponseBreached = now.After(state.ResponseDueAt)
	}
	if state.ResolvedAt != nil {
		state.ResolutionBreached = state.ResolvedAt.After(state.ResolutionDueAt)
	} else {
		state.ResolutionBreached = now.After(state.ResolutionDueAt)
	}
	state.UpdatedAt = now
}

func pagePolicies(items []Policy, page api.PaginationParams) ([]Policy, int, error) {
	total := len(items)
	start := min(page.Offset, total)
	end := min(start+page.Limit, total)
	return items[start:end], total, nil
}
func pageStates(items []TicketSLA, page api.PaginationParams) ([]TicketSLA, int, error) {
	total := len(items)
	start := min(page.Offset, total)
	end := min(start+page.Limit, total)
	return items[start:end], total, nil
}
