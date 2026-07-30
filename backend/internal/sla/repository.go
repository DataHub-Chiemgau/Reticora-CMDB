package sla

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
)

// Repository defines persistence operations for SLA policies and ticket state.
type Repository interface {
	ListPolicies(orgID, priority, clientID string, page api.PaginationParams) ([]Policy, int, error)
	GetPolicy(orgID, id string) (*Policy, error)
	CreatePolicy(p *Policy) error
	UpdatePolicy(orgID, id string, req UpdatePolicyRequest) (*Policy, error)
	DeletePolicy(orgID, id string) error
	ApplyForTicket(orgID string, t *ticket.Ticket, slaID string) (*TicketSLA, error)
	GetForTicket(orgID, ticketID string) (*TicketSLA, error)
	MarkFirstResponse(orgID, ticketID string, at time.Time) error
	MarkResolved(orgID, ticketID string, at time.Time) error
	ListBreaches(orgID string, filter BreachFilter, page api.PaginationParams) ([]TicketSLA, int, error)
}

// MemoryRepository is an in-memory implementation of Repository.
type MemoryRepository struct {
	mu       sync.RWMutex
	policies map[string]*Policy
	states   map[string]*TicketSLA
	nextID   int
}

// NewMemoryRepository creates a memory SLA repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{policies: make(map[string]*Policy), states: make(map[string]*TicketSLA)}
}

func (r *MemoryRepository) ListPolicies(orgID, priority, clientID string, page api.PaginationParams) ([]Policy, int, error) {
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

func (r *MemoryRepository) GetPolicy(orgID, id string) (*Policy, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.policies[id]
	if !ok || p.OrganizationID != orgID {
		return nil, fmt.Errorf("sla policy not found")
	}
	cp := *p
	return &cp, nil
}

func (r *MemoryRepository) CreatePolicy(p *Policy) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	p.ID = fmt.Sprintf("sla-%d", r.nextID)
	now := time.Now().UTC()
	p.CreatedAt, p.UpdatedAt = now, now
	cp := *p
	r.policies[p.ID] = &cp
	return nil
}

func (r *MemoryRepository) UpdatePolicy(orgID, id string, req UpdatePolicyRequest) (*Policy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.policies[id]
	if !ok || p.OrganizationID != orgID {
		return nil, fmt.Errorf("sla policy not found")
	}
	if req.ClientID != nil {
		p.ClientID = *req.ClientID
	}
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

func (r *MemoryRepository) DeletePolicy(orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.policies[id]
	if !ok || p.OrganizationID != orgID {
		return fmt.Errorf("sla policy not found")
	}
	delete(r.policies, id)
	return nil
}

func (r *MemoryRepository) ApplyForTicket(orgID string, t *ticket.Ticket, slaID string) (*TicketSLA, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	policy := r.findPolicyLocked(orgID, t.Priority, t.RelatedCIID, slaID)
	if policy == nil {
		return nil, fmt.Errorf("sla policy not found")
	}
	r.nextID++
	state := buildState(fmt.Sprintf("ticket-sla-%d", r.nextID), orgID, t, policy)
	r.states[t.ID] = state
	cp := *state
	return &cp, nil
}

func (r *MemoryRepository) GetForTicket(orgID, ticketID string) (*TicketSLA, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.states[ticketID]
	if !ok || state.OrganizationID != orgID {
		return nil, fmt.Errorf("ticket sla not found")
	}
	refreshState(state, time.Now().UTC())
	cp := *state
	return &cp, nil
}

func (r *MemoryRepository) MarkFirstResponse(orgID, ticketID string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if state, ok := r.states[ticketID]; ok && state.OrganizationID == orgID && state.FirstResponseAt == nil {
		v := at.UTC()
		state.FirstResponseAt = &v
		refreshState(state, v)
	}
	return nil
}

func (r *MemoryRepository) MarkResolved(orgID, ticketID string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if state, ok := r.states[ticketID]; ok && state.OrganizationID == orgID && state.ResolvedAt == nil {
		v := at.UTC()
		state.ResolvedAt = &v
		refreshState(state, v)
	}
	return nil
}

func (r *MemoryRepository) ListBreaches(orgID string, filter BreachFilter, page api.PaginationParams) ([]TicketSLA, int, error) {
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
		out = append(out, *state)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ResolutionDueAt.Before(out[j].ResolutionDueAt) })
	return pageStates(out, page)
}

func (r *MemoryRepository) findPolicyLocked(orgID, priority, _ string, slaID string) *Policy {
	if slaID != "" {
		p := r.policies[slaID]
		if p != nil && p.OrganizationID == orgID {
			return p
		}
		return nil
	}
	var best *Policy
	for _, p := range r.policies {
		if p.OrganizationID == orgID && p.Priority == priority && (best == nil || p.ClientID != "") {
			best = p
		}
	}
	return best
}

func buildState(id, orgID string, t *ticket.Ticket, p *Policy) *TicketSLA {
	created := t.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	state := &TicketSLA{ID: id, OrganizationID: orgID, TicketID: t.ID, SLAID: p.ID, ResponseDueAt: created.Add(time.Duration(p.ResponseTargetMinutes) * time.Minute), ResolutionDueAt: created.Add(time.Duration(p.ResolutionTargetMinutes) * time.Minute), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if t.ResolvedAt != "" {
		if parsed, err := time.Parse(time.RFC3339, t.ResolvedAt); err == nil {
			state.ResolvedAt = &parsed
		}
	}
	refreshState(state, time.Now().UTC())
	return state
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
