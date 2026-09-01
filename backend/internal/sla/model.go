// Package sla provides SLA policy and per-ticket state management.
package sla

import "time"

// Policy defines response and resolution targets for a priority.
type Policy struct {
	ID                      string    `json:"id"`
	OrganizationID          string    `json:"organization_id"`
	ClientID                string    `json:"client_id,omitempty"`
	Name                    string    `json:"name"`
	Priority                string    `json:"priority"`
	ResponseTargetMinutes   int       `json:"response_target_minutes"`
	ResolutionTargetMinutes int       `json:"resolution_target_minutes"`
	BusinessCalendar        bool      `json:"business_calendar"`
	CreatedAt               time.Time `json:"created_at"`
	UpdatedAt               time.Time `json:"updated_at"`
}

// TicketSLA is the SLA clock state attached to a ticket.
type TicketSLA struct {
	ID                 string     `json:"id"`
	OrganizationID     string     `json:"organization_id"`
	TicketID           string     `json:"ticket_id"`
	SLAID              string     `json:"sla_id"`
	ResponseDueAt      time.Time  `json:"response_due_at"`
	ResolutionDueAt    time.Time  `json:"resolution_due_at"`
	FirstResponseAt    *time.Time `json:"first_response_at,omitempty"`
	ResolvedAt         *time.Time `json:"resolved_at,omitempty"`
	ResponseBreached   bool       `json:"response_breached"`
	ResolutionBreached bool       `json:"resolution_breached"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// CreatePolicyRequest is the payload for creating an SLA policy.
type CreatePolicyRequest struct {
	ClientID string `json:"client_id,omitempty"`
	Name     string `json:"name"`
	Priority string `json:"priority"`
	// ResponseTimeMinutes/ResolutionTimeMinutes are accepted as aliases for
	// the canonical response_target_minutes/resolution_target_minutes fields.
	ResponseTimeMinutes     *int `json:"response_time_minutes,omitempty"`
	ResolutionTimeMinutes   *int `json:"resolution_time_minutes,omitempty"`
	ResponseTargetMinutes   int  `json:"response_target_minutes"`
	ResolutionTargetMinutes int  `json:"resolution_target_minutes"`
	BusinessCalendar        bool `json:"business_calendar"`
}

// Normalize applies the response/resolution time aliases onto the canonical
// target fields.
func (r *CreatePolicyRequest) Normalize() {
	if r.ResponseTargetMinutes == 0 && r.ResponseTimeMinutes != nil {
		r.ResponseTargetMinutes = *r.ResponseTimeMinutes
	}
	if r.ResolutionTargetMinutes == 0 && r.ResolutionTimeMinutes != nil {
		r.ResolutionTargetMinutes = *r.ResolutionTimeMinutes
	}
}

// UpdatePolicyRequest is the payload for updating an SLA policy.
type UpdatePolicyRequest struct {
	ClientID                *string `json:"client_id,omitempty"`
	Name                    *string `json:"name,omitempty"`
	Priority                *string `json:"priority,omitempty"`
	ResponseTargetMinutes   *int    `json:"response_target_minutes,omitempty"`
	ResolutionTargetMinutes *int    `json:"resolution_target_minutes,omitempty"`
	BusinessCalendar        *bool   `json:"business_calendar,omitempty"`
}

// AttachTicketRequest selects a policy; empty sla_id evaluates the best match.
type AttachTicketRequest struct {
	SLAID string `json:"sla_id,omitempty"`
}

// BreachFilter controls breach listing.
type BreachFilter struct {
	Status string
}
