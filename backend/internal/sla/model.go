// Package sla provides SLA policy and per-ticket state management.
package sla

import (
	"encoding/json"
	"time"
)

// Policy defines response and resolution targets for a priority.
type Policy struct {
	ID                      string `json:"id"`
	OrganizationID          string `json:"organization_id"`
	ClientID                string `json:"client_id,omitempty"`
	Name                    string `json:"name"`
	Priority                string `json:"priority"`
	ResponseTargetMinutes   int    `json:"response_target_minutes"`
	ResolutionTargetMinutes int    `json:"resolution_target_minutes"`
	BusinessCalendar        bool   `json:"business_calendar"`
	// CalendarID names the business calendar of the policy (TKT-02); nil
	// lets the calendar chain decide (client default, org default,
	// built-in standard).
	CalendarID *string   `json:"calendar_id"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
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
	// CalendarID is the calendar the due times were computed with; nil
	// means elapsed time or the built-in standard calendar.
	CalendarID *string `json:"calendar_id"`
	// ResponseRemainingMinutes and ResolutionRemainingMinutes are the
	// minutes until the due time in business minutes of the calendar (in
	// minutes for elapsed time); negative once overdue, nil once the
	// first response or the resolution is recorded.
	ResponseRemainingMinutes   *int `json:"response_remaining_minutes"`
	ResolutionRemainingMinutes *int `json:"resolution_remaining_minutes"`
	// Paused is true while the current moment lies outside business hours
	// or on a holiday; always false for elapsed time.
	Paused    bool      `json:"paused"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
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
	// CalendarID names a business calendar visible under the scope whose
	// client is NULL or the policy's client (TKT-02).
	CalendarID *string `json:"calendar_id,omitempty"`
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
	// CalendarID changes the policy's calendar when sent; null clears it.
	CalendarID OptionalID `json:"calendar_id"`
}

// OptionalID is a nullable id of an update request: Set reports whether
// the field was sent, an empty Value with Set means null.
type OptionalID struct {
	Set   bool
	Value string
}

// UnmarshalJSON records that the field was sent; null clears the value.
func (o *OptionalID) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Value = ""
		return nil
	}
	return json.Unmarshal(b, &o.Value)
}

// AttachTicketRequest selects a policy; empty sla_id evaluates the best match.
type AttachTicketRequest struct {
	SLAID string `json:"sla_id,omitempty"`
}

// BreachFilter controls breach listing.
type BreachFilter struct {
	Status string
}
