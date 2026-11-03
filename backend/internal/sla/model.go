// Package sla provides SLA policy and per-ticket state management.
package sla

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
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

// OptionalID is a nullable id or text of an update request: Set reports
// whether the field was sent, an empty Value with Set means null.
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

// BusinessCalendar is a stored business calendar of an organization or
// one of its clients (TKT-02, WP-768): working windows per weekday in
// local wall time, holidays and an optional time zone. ClientID nil is an
// org-wide calendar; Timezone nil derives the zone from the ticket's
// client, else the organization. At most one calendar per organization
// and client is the default of the calendar chain.
type BusinessCalendar struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	ClientID       *string   `json:"client_id"`
	Name           string    `json:"name"`
	Timezone       *string   `json:"timezone"`
	WeeklyHours    []Window  `json:"weekly_hours"`
	Holidays       []Holiday `json:"holidays"`
	IsDefault      bool      `json:"is_default"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Calendar returns the computation model of the stored calendar.
func (b *BusinessCalendar) Calendar() Calendar {
	cal := Calendar{WeeklyHours: b.WeeklyHours, Holidays: b.Holidays}
	if b.Timezone != nil {
		cal.Timezone = *b.Timezone
	}
	return cal
}

// validate checks name and calendar of a calendar about to be stored with
// Calendar.Validate (WP-292) and normalizes an empty time zone and an
// absent holiday list.
//
// validation: fixed texts of Calendar.Validate and the name rule.
func (b *BusinessCalendar) validate() error {
	b.Name = strings.TrimSpace(b.Name)
	if b.Name == "" {
		return &api.FieldError{Field: "name", Message: "must not be empty"}
	}
	if b.Timezone != nil && *b.Timezone == "" {
		b.Timezone = nil
	}
	if b.Holidays == nil {
		b.Holidays = []Holiday{}
	}
	cal := b.Calendar()
	return cal.Validate()
}

// CreateBusinessCalendarRequest is the payload of POST
// /api/v1/business-calendars. client_id is set only here; without
// weekly_hours the built-in standard (Monday to Friday, 08:00–18:00)
// applies.
type CreateBusinessCalendarRequest struct {
	ClientID    *string   `json:"client_id,omitempty"`
	Name        string    `json:"name"`
	Timezone    *string   `json:"timezone,omitempty"`
	WeeklyHours *[]Window `json:"weekly_hours,omitempty"`
	Holidays    []Holiday `json:"holidays,omitempty"`
	IsDefault   bool      `json:"is_default"`
}

// UpdateBusinessCalendarRequest is the payload of PATCH
// /api/v1/business-calendars/{id}: sent fields change, timezone null
// derives the zone again. client_id cannot change; a sent client_id is
// refused with 400 on client_id.
type UpdateBusinessCalendarRequest struct {
	ClientID    OptionalID `json:"client_id"`
	Name        *string    `json:"name,omitempty"`
	Timezone    OptionalID `json:"timezone"`
	WeeklyHours *[]Window  `json:"weekly_hours,omitempty"`
	Holidays    *[]Holiday `json:"holidays,omitempty"`
	IsDefault   *bool      `json:"is_default,omitempty"`
}

// apply merges the request into the stored calendar b.
func (req *UpdateBusinessCalendarRequest) apply(b *BusinessCalendar) {
	if req.Name != nil {
		b.Name = *req.Name
	}
	if req.Timezone.Set {
		b.Timezone = nil
		if req.Timezone.Value != "" {
			v := req.Timezone.Value
			b.Timezone = &v
		}
	}
	if req.WeeklyHours != nil {
		b.WeeklyHours = *req.WeeklyHours
	}
	if req.Holidays != nil {
		b.Holidays = *req.Holidays
	}
	if req.IsDefault != nil {
		b.IsDefault = *req.IsDefault
	}
}

// BusinessCalendarListResponse is the page of GET
// /api/v1/business-calendars, sorted by name and id.
type BusinessCalendarListResponse = api.ListResponse[BusinessCalendar]

// BusinessCalendarFilter narrows the calendar list; ClientID is a UUID or
// empty.
type BusinessCalendarFilter struct {
	ClientID string
}

// Keyset order of the calendar list (API-02): name, then id.
const (
	calendarSort = "name"
	calendarDir  = "asc"
)

// calendarPosition returns the (name, id) position of a keyset cursor of
// the calendar list; a cursor of another order or with a malformed id is
// api.ErrInvalidCursor.
//
// validation: fixed texts wrapping api.ErrInvalidCursor (API-03).
func calendarPosition(c *api.Cursor) (name, id string, err error) {
	if !c.Matches(calendarSort, calendarDir) {
		return "", "", fmt.Errorf("%w: sort order changed", api.ErrInvalidCursor)
	}
	if !api.IsUUID(c.ID) {
		return "", "", fmt.Errorf("%w: malformed position", api.ErrInvalidCursor)
	}
	return c.Value, c.ID, nil
}

// calendarPage is the list response of the rows a repository read (at
// most one more than the limit): when more follow, the page ends at the
// limit and next_cursor is the keyset cursor after its last row.
func calendarPage(rows []BusinessCalendar, total int, page api.PaginationParams) BusinessCalendarListResponse {
	list := BusinessCalendarListResponse{Data: rows, Total: total, Limit: page.Limit, Offset: page.Offset}
	if page.Cursor != nil {
		list.Offset = 0
	}
	if page.Limit > 0 && len(rows) > page.Limit {
		list.Data = rows[:page.Limit]
		last := list.Data[page.Limit-1]
		list.HasMore = true
		list.NextCursor = api.EncodeCursor(api.Cursor{Sort: calendarSort, Dir: calendarDir, Value: last.Name, ID: last.ID})
	}
	return list
}
