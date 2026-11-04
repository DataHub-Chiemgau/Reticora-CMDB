package sla

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const policyColumns = `id::text, organization_id::text, COALESCE(client_id::text, ''), name, priority, response_target_minutes, resolution_target_minutes, business_calendar, calendar_id::text, created_at, updated_at`
const ticketSLAColumns = `id::text, organization_id::text, ticket_id::text, sla_id::text, response_due_at, resolution_due_at, first_response_at, resolved_at, response_breached, resolution_breached, calendar_id::text, created_at, updated_at`

// errCalendarBinding: the calendar_id of a policy names no calendar
// visible under the scope, or one bound to another client than the
// policy's (TKT-02). 400 on calendar_id, alike for both.
var errCalendarBinding = &api.FieldError{Field: "calendar_id", Message: "business calendar not found or bound to another client"}

// checkCalendarBinding verifies that calendarID (if any) names a calendar
// visible in the transaction whose client is NULL or clientID.
func checkCalendarBinding(ctx context.Context, tx pgx.Tx, orgID string, calendarID *string, clientID string) error {
	if calendarID == nil {
		return nil
	}
	if !api.IsUUID(*calendarID) {
		return errCalendarBinding
	}
	var calendarClient string
	err := tx.QueryRow(ctx, `SELECT COALESCE(client_id::text, '') FROM business_calendar WHERE organization_id = $1 AND id = $2`, orgID, *calendarID).Scan(&calendarClient)
	if err == pgx.ErrNoRows {
		return errCalendarBinding
	}
	if err != nil {
		return fmt.Errorf("read business calendar: %w", err)
	}
	if calendarClient != "" && calendarClient != clientID {
		return errCalendarBinding
	}
	return nil
}

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct{ pool *pgxpool.Pool }

// NewPGRepository creates a PostgreSQL SLA repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository { return &PGRepository{pool: pool} }

// ticketSLAVisible restricts SLA states to visible tickets; it mirrors the
// ticket repository's visibility (related CI and asset) until WP-028 derives
// client and site for tickets.
const ticketSLAVisible = `EXISTS (SELECT 1 FROM ticket WHERE ticket.id = ticket_sla.ticket_id
	AND (ticket.related_ci_id IS NULL OR EXISTS (SELECT 1 FROM ci WHERE ci.id = ticket.related_ci_id))
	AND (ticket.related_asset_id IS NULL OR EXISTS (SELECT 1 FROM asset WHERE asset.id = ticket.related_asset_id)))`

func (r *PGRepository) ListPolicies(ctx context.Context, orgID, priority, clientID string, page api.PaginationParams) ([]Policy, int, error) {
	items := make([]Policy, 0)
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := []string{"organization_id = $1"}
		args := []any{orgID}
		pos := 2
		if priority != "" {
			where = append(where, fmt.Sprintf("priority = $%d", pos))
			args = append(args, priority)
			pos++
		}
		if clientID != "" {
			where = append(where, fmt.Sprintf("client_id = $%d", pos))
			args = append(args, clientID)
			pos++
		}
		clause := strings.Join(where, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM sla WHERE "+clause, args...).Scan(&total); err != nil {
			return fmt.Errorf("count sla policies: %w", err)
		}
		query := fmt.Sprintf("SELECT %s FROM sla WHERE %s ORDER BY name ASC LIMIT $%d OFFSET $%d", policyColumns, clause, pos, pos+1)
		rows, err := tx.Query(ctx, query, append(args, page.Limit, page.Offset)...)
		if err != nil {
			return fmt.Errorf("list sla policies: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanPolicy(rows)
			if err != nil {
				return err
			}
			items = append(items, *item)
		}
		return rows.Err()
	})
	return items, total, err
}

func (r *PGRepository) GetPolicy(ctx context.Context, orgID, id string) (*Policy, error) {
	var item *Policy
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		item, err = scanPolicy(tx.QueryRow(ctx, "SELECT "+policyColumns+" FROM sla WHERE organization_id = $1 AND id = $2", orgID, id))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return item, err
}

func (r *PGRepository) CreatePolicy(ctx context.Context, p *Policy) error {
	return database.WithRequestTenant(ctx, r.pool, p.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if err := checkCalendarBinding(ctx, tx, p.OrganizationID, p.CalendarID, p.ClientID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			INSERT INTO sla (organization_id, client_id, name, priority, response_target_minutes, resolution_target_minutes, business_calendar, calendar_id)
			VALUES ($1, NULLIF($2, '')::uuid, $3, $4, $5, $6, $7, $8::uuid)
			RETURNING id::text, created_at, updated_at`, p.OrganizationID, p.ClientID, p.Name, p.Priority, p.ResponseTargetMinutes, p.ResolutionTargetMinutes, p.BusinessCalendar, p.CalendarID).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
	})
}

func (r *PGRepository) UpdatePolicy(ctx context.Context, orgID, id string, req UpdatePolicyRequest) (*Policy, error) {
	var item *Policy
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		set := []string{}
		args := []any{id, orgID}
		pos := 3
		if req.ClientID != nil {
			set = append(set, fmt.Sprintf("client_id = NULLIF($%d, '')::uuid", pos))
			args = append(args, *req.ClientID)
			pos++
		}
		if req.Name != nil {
			set = append(set, fmt.Sprintf("name = $%d", pos))
			args = append(args, *req.Name)
			pos++
		}
		if req.Priority != nil {
			set = append(set, fmt.Sprintf("priority = $%d", pos))
			args = append(args, *req.Priority)
			pos++
		}
		if req.ResponseTargetMinutes != nil {
			set = append(set, fmt.Sprintf("response_target_minutes = $%d", pos))
			args = append(args, *req.ResponseTargetMinutes)
			pos++
		}
		if req.ResolutionTargetMinutes != nil {
			set = append(set, fmt.Sprintf("resolution_target_minutes = $%d", pos))
			args = append(args, *req.ResolutionTargetMinutes)
			pos++
		}
		if req.BusinessCalendar != nil {
			set = append(set, fmt.Sprintf("business_calendar = $%d", pos))
			args = append(args, *req.BusinessCalendar)
			pos++
		}
		if req.CalendarID.Set {
			if req.CalendarID.Value != "" && !api.IsUUID(req.CalendarID.Value) {
				return errCalendarBinding
			}
			set = append(set, fmt.Sprintf("calendar_id = NULLIF($%d, '')::uuid", pos))
			args = append(args, req.CalendarID.Value)
		}
		if len(set) == 0 {
			var err error
			item, err = scanPolicy(tx.QueryRow(ctx, "SELECT "+policyColumns+" FROM sla WHERE id = $1 AND organization_id = $2", id, orgID))
			if err == pgx.ErrNoRows {
				return ErrNotFound
			}
			return err
		}
		set = append(set, "updated_at = now()")
		var err error
		item, err = scanPolicy(tx.QueryRow(ctx, fmt.Sprintf("UPDATE sla SET %s WHERE id = $1 AND organization_id = $2 RETURNING %s", strings.Join(set, ", "), policyColumns), args...))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		// The calendar stays bound to the policy's client, also when only
		// client_id changes (TKT-02); a failed check rolls the update back.
		return checkCalendarBinding(ctx, tx, orgID, item.CalendarID, item.ClientID)
	})
	return item, err
}

func (r *PGRepository) DeletePolicy(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmd, err := tx.Exec(ctx, "DELETE FROM sla WHERE organization_id = $1 AND id = $2", orgID, id)
		if err != nil {
			return fmt.Errorf("delete sla policy: %w", err)
		}
		if cmd.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ApplyForTicket selects the policy of the ticket (the named one or the
// best match for its priority and client, a client policy before the org
// policy) and computes the due times from the ticket's creation (TKT-02).
// With business_calendar the first calendar of the chain applies: the
// policy's calendar, the client's default, the organization's default,
// the built-in standard; its zone is the calendar's, else the client's,
// else the organization's. The chosen calendar is stored with the state;
// a new computation resets the breach notifications.
func (r *PGRepository) ApplyForTicket(ctx context.Context, orgID string, t *ticket.Ticket, slaID string) (*TicketSLA, error) {
	// The policy id reaches SQL only as a UUID (API-03, F-047); a malformed
	// one names no policy.
	if slaID != "" && !api.IsUUID(slaID) {
		return nil, ErrNotFound
	}
	var state *TicketSLA
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var clientID string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(client_id::text, '') FROM ticket WHERE organization_id = $1 AND id = $2`, orgID, t.ID).Scan(&clientID); err != nil {
			if err == pgx.ErrNoRows {
				return ticket.ErrNotFound
			}
			return fmt.Errorf("read ticket client: %w", err)
		}
		policyID := slaID
		if policyID == "" {
			if err := tx.QueryRow(ctx, `SELECT id::text FROM sla WHERE organization_id = $1 AND priority = $2 AND (client_id IS NULL OR client_id = NULLIF($3, '')::uuid) ORDER BY client_id NULLS LAST, created_at DESC LIMIT 1`, orgID, t.Priority, clientID).Scan(&policyID); err != nil {
				if err == pgx.ErrNoRows {
					return ErrNotFound
				}
				return fmt.Errorf("select sla policy: %w", err)
			}
		}
		created := t.CreatedAt
		if created.IsZero() {
			created = time.Now().UTC()
		}
		var responseMinutes, resolutionMinutes int
		var businessCalendar bool
		var policyCalendar *string
		if err := tx.QueryRow(ctx, "SELECT response_target_minutes, resolution_target_minutes, business_calendar, calendar_id::text FROM sla WHERE organization_id = $1 AND id = $2", orgID, policyID).Scan(&responseMinutes, &resolutionMinutes, &businessCalendar, &policyCalendar); err != nil {
			if err == pgx.ErrNoRows {
				return ErrNotFound
			}
			return fmt.Errorf("read sla policy: %w", err)
		}
		var calendarID *string
		var cal *Calendar
		var loc *time.Location
		if businessCalendar {
			var resolveErr error
			calendarID, cal, loc, resolveErr = resolveCalendar(ctx, tx, orgID, clientID, policyCalendar)
			if resolveErr != nil {
				return resolveErr
			}
		}
		responseDue, err := addTarget(created, responseMinutes, cal, loc)
		if err != nil {
			return err
		}
		resolutionDue, err := addTarget(created, resolutionMinutes, cal, loc)
		if err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `
			INSERT INTO ticket_sla (organization_id, ticket_id, sla_id, response_due_at, resolution_due_at, calendar_id, sla_explicit)
			VALUES ($1, $2, $3, $4, $5, $6::uuid, $7)
			ON CONFLICT (organization_id, ticket_id) DO UPDATE SET
				sla_id = EXCLUDED.sla_id,
				response_due_at = EXCLUDED.response_due_at,
				resolution_due_at = EXCLUDED.resolution_due_at,
				calendar_id = EXCLUDED.calendar_id,
				sla_explicit = EXCLUDED.sla_explicit,
				response_breach_notified_at = NULL,
				resolution_breach_notified_at = NULL,
				updated_at = now()
			RETURNING `+ticketSLAColumns, orgID, t.ID, policyID, responseDue, resolutionDue, calendarID, slaID != "")
		state, err = scanTicketSLA(row)
		if err != nil {
			return fmt.Errorf("upsert ticket sla: %w", err)
		}
		return refreshTicketSLA(ctx, tx, state.ID)
	})
	if err != nil {
		return nil, err
	}
	return r.GetForTicket(ctx, orgID, state.TicketID)
}

// resolveCalendar walks the calendar chain of TKT-02: the policy's
// calendar, the default calendar of the ticket's client, the default
// calendar of the organization, the built-in standard (calendarID nil).
// The zone is the calendar's, else client.timezone of the ticket's
// client, else organization.timezone.
func resolveCalendar(ctx context.Context, tx pgx.Tx, orgID, clientID string, policyCalendar *string) (*string, *Calendar, *time.Location, error) {
	var id, timezone string
	var weekly, holidays []byte
	err := tx.QueryRow(ctx, `
		SELECT id::text, COALESCE(timezone, ''), weekly_hours, holidays FROM business_calendar
		WHERE organization_id = $1
		  AND (id = $2::uuid OR (is_default AND (client_id IS NULL OR client_id = NULLIF($3, '')::uuid)))
		ORDER BY COALESCE(id = $2::uuid, false) DESC, client_id NULLS LAST
		LIMIT 1`, orgID, policyCalendar, clientID).Scan(&id, &timezone, &weekly, &holidays)
	var calendarID *string
	cal := DefaultCalendar()
	switch {
	case err == pgx.ErrNoRows:
	case err != nil:
		return nil, nil, nil, fmt.Errorf("read business calendar: %w", err)
	default:
		calendarID = &id
		if cal, err = decodeCalendar(timezone, weekly, holidays); err != nil {
			return nil, nil, nil, err
		}
	}
	if cal.Timezone == "" {
		if err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT timezone FROM client WHERE organization_id = $1 AND id = NULLIF($2, '')::uuid), (SELECT timezone FROM organization WHERE id = $1), 'UTC')`, orgID, clientID).Scan(&timezone); err != nil {
			return nil, nil, nil, fmt.Errorf("read time zone: %w", err)
		}
		cal.Timezone = timezone
	}
	loc, err := time.LoadLocation(cal.Timezone)
	if err != nil {
		loc = time.UTC
	}
	return calendarID, &cal, loc, nil
}

// decodeCalendar builds a Calendar from the stored columns.
func decodeCalendar(timezone string, weekly, holidays []byte) (Calendar, error) {
	cal := Calendar{Timezone: timezone}
	if err := json.Unmarshal(weekly, &cal.WeeklyHours); err != nil {
		return cal, fmt.Errorf("decode weekly hours: %w", err)
	}
	if err := json.Unmarshal(holidays, &cal.Holidays); err != nil {
		return cal, fmt.Errorf("decode holidays: %w", err)
	}
	return cal, nil
}

// annotate fills the remaining minutes and the paused flag of the states
// (TKT-02): business minutes of the stored calendar, of the built-in
// standard when the policy counts business time without a stored one, or
// elapsed minutes. The zone follows the chain of resolveCalendar.
func annotate(ctx context.Context, tx pgx.Tx, orgID string, states []TicketSLA, now time.Time) error {
	if len(states) == 0 {
		return nil
	}
	ids := make([]string, len(states))
	for i := range states {
		ids[i] = states[i].ID
	}
	rows, err := tx.Query(ctx, `
		SELECT ts.id::text, COALESCE(s.business_calendar, false), bc.id IS NOT NULL,
		       COALESCE(bc.timezone, c.timezone, o.timezone, 'UTC'),
		       COALESCE(bc.weekly_hours, '[]'::jsonb), COALESCE(bc.holidays, '[]'::jsonb)
		  FROM ticket_sla ts
		  LEFT JOIN sla s ON s.id = ts.sla_id AND s.organization_id = ts.organization_id
		  LEFT JOIN ticket t ON t.id = ts.ticket_id AND t.organization_id = ts.organization_id
		  LEFT JOIN business_calendar bc ON bc.id = ts.calendar_id AND bc.organization_id = ts.organization_id
		  LEFT JOIN client c ON c.id = t.client_id AND c.organization_id = ts.organization_id
		  LEFT JOIN organization o ON o.id = ts.organization_id
		 WHERE ts.organization_id = $1 AND ts.id = ANY($2::uuid[])`, orgID, ids)
	if err != nil {
		return fmt.Errorf("read sla calendars: %w", err)
	}
	defer rows.Close()
	schedules := map[string]*Schedule{}
	for rows.Next() {
		var id, timezone string
		var business, stored bool
		var weekly, holidays []byte
		if scanErr := rows.Scan(&id, &business, &stored, &timezone, &weekly, &holidays); scanErr != nil {
			return scanErr
		}
		var cal Calendar
		switch {
		case stored:
			if cal, err = decodeCalendar(timezone, weekly, holidays); err != nil {
				return err
			}
		case business:
			cal = DefaultCalendar()
		default:
			continue
		}
		loc, locErr := time.LoadLocation(timezone)
		if locErr != nil {
			loc = time.UTC
		}
		schedules[id] = cal.In(loc)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return rowsErr
	}
	for i := range states {
		fillRemaining(&states[i], schedules[states[i].ID], now)
	}
	return nil
}

// fillRemaining sets the remaining minutes and the paused flag of one
// state; a nil schedule counts elapsed time.
func fillRemaining(state *TicketSLA, s *Schedule, now time.Time) {
	remaining := func(due time.Time) *int {
		var v int
		if s == nil {
			v = int(due.Sub(now) / time.Minute)
		} else {
			v = s.BusinessMinutesBetween(now, due)
		}
		return &v
	}
	state.ResponseRemainingMinutes, state.ResolutionRemainingMinutes = nil, nil
	if state.FirstResponseAt == nil {
		state.ResponseRemainingMinutes = remaining(state.ResponseDueAt)
	}
	if state.ResolvedAt == nil {
		state.ResolutionRemainingMinutes = remaining(state.ResolutionDueAt)
	}
	state.Paused = s != nil && !s.IsBusinessMoment(now)
}

func (r *PGRepository) GetForTicket(ctx context.Context, orgID, ticketID string) (*TicketSLA, error) {
	var state *TicketSLA
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, "SELECT id::text FROM ticket_sla WHERE "+ticketSLAVisible+" AND organization_id = $1 AND ticket_id = $2", orgID, ticketID).Scan(&id); err != nil {
			if err == pgx.ErrNoRows {
				return ErrTicketSLANotFound
			}
			return err
		}
		if err := refreshTicketSLA(ctx, tx, id); err != nil {
			return err
		}
		var err error
		state, err = scanTicketSLA(tx.QueryRow(ctx, "SELECT "+ticketSLAColumns+" FROM ticket_sla WHERE "+ticketSLAVisible+" AND id = $1 AND organization_id = $2", id, orgID))
		if err != nil {
			return err
		}
		annotated := []TicketSLA{*state}
		if annotateErr := annotate(ctx, tx, orgID, annotated, time.Now().UTC()); annotateErr != nil {
			return annotateErr
		}
		*state = annotated[0]
		return nil
	})
	return state, err
}

func (r *PGRepository) MarkFirstResponse(ctx context.Context, orgID, ticketID string, at time.Time) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE ticket_sla SET first_response_at = COALESCE(first_response_at, $3), updated_at = now() WHERE `+ticketSLAVisible+` AND organization_id = $1 AND ticket_id = $2`, orgID, ticketID, at.UTC())
		if err != nil {
			return fmt.Errorf("mark first response: %w", err)
		}
		return refreshTicketSLAByTicket(ctx, tx, orgID, ticketID)
	})
}

func (r *PGRepository) MarkResolved(ctx context.Context, orgID, ticketID string, at time.Time) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE ticket_sla SET resolved_at = COALESCE(resolved_at, $3), updated_at = now() WHERE `+ticketSLAVisible+` AND organization_id = $1 AND ticket_id = $2`, orgID, ticketID, at.UTC())
		if err != nil {
			return fmt.Errorf("mark resolved: %w", err)
		}
		return refreshTicketSLAByTicket(ctx, tx, orgID, ticketID)
	})
}

// ReapplyForTicket chooses the policy again after a change of the ticket's
// priority or object (WP-769), unless the state names its policy
// explicitly (sla_explicit): ApplyForTicket computes the due times from the
// ticket's creation with the calendar of the chain then valid and clears
// the breach notifications; first response and resolution stay.
func (r *PGRepository) ReapplyForTicket(ctx context.Context, orgID string, t *ticket.Ticket) error {
	var explicit bool
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT sla_explicit FROM ticket_sla WHERE `+ticketSLAVisible+` AND organization_id = $1 AND ticket_id = $2`,
			orgID, t.ID).Scan(&explicit)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("read ticket sla: %w", err)
	}
	if explicit {
		return nil
	}
	_, err = r.ApplyForTicket(ctx, orgID, t, "")
	return err
}

// MarkReopened clears the resolution of a resolved ticket's state after a
// reopen (WP-769): resolved_at and resolution_breach_notified_at become
// NULL and resolution_breached is computed at the moment of the reopen;
// the due time stays, so an overdue one is reported again by the next
// sla.check run. A state without resolution is unchanged.
func (r *PGRepository) MarkReopened(ctx context.Context, orgID, ticketID string, at time.Time) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE ticket_sla SET resolved_at = NULL, resolution_breach_notified_at = NULL,
			resolution_breached = $3 > resolution_due_at, updated_at = now()
			WHERE `+ticketSLAVisible+` AND organization_id = $1 AND ticket_id = $2 AND resolved_at IS NOT NULL`, orgID, ticketID, at.UTC())
		if err != nil {
			return fmt.Errorf("mark reopened: %w", err)
		}
		return nil
	})
}

func (r *PGRepository) ListBreaches(ctx context.Context, orgID string, filter BreachFilter, page api.PaginationParams) ([]TicketSLA, int, error) {
	items := make([]TicketSLA, 0)
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE ticket_sla SET response_breached = CASE WHEN first_response_at IS NULL THEN now() > response_due_at ELSE first_response_at > response_due_at END, resolution_breached = CASE WHEN resolved_at IS NULL THEN now() > resolution_due_at ELSE resolved_at > resolution_due_at END, updated_at = now() WHERE `+ticketSLAVisible+` AND organization_id = $1`, orgID); err != nil {
			return err
		}
		where := "organization_id = $1"
		if filter.Status == "breached" {
			where += " AND (response_breached OR resolution_breached)"
		}
		if filter.Status == "at_risk" {
			where += " AND NOT (response_breached OR resolution_breached) AND ((first_response_at IS NULL AND response_due_at <= now() + interval '1 hour') OR (resolved_at IS NULL AND resolution_due_at <= now() + interval '1 hour'))"
		}
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM ticket_sla WHERE "+ticketSLAVisible+" AND "+where, orgID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+ticketSLAColumns+" FROM ticket_sla WHERE "+ticketSLAVisible+" AND "+where+" ORDER BY resolution_due_at ASC LIMIT $2 OFFSET $3", orgID, page.Limit, page.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanTicketSLA(rows)
			if err != nil {
				return err
			}
			items = append(items, *item)
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			return rowsErr
		}
		rows.Close()
		return annotate(ctx, tx, orgID, items, time.Now().UTC())
	})
	return items, total, err
}

type scanner interface{ Scan(dest ...any) error }

func scanPolicy(s scanner) (*Policy, error) {
	p := &Policy{}
	if err := s.Scan(&p.ID, &p.OrganizationID, &p.ClientID, &p.Name, &p.Priority, &p.ResponseTargetMinutes, &p.ResolutionTargetMinutes, &p.BusinessCalendar, &p.CalendarID, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	return p, nil
}

func scanTicketSLA(s scanner) (*TicketSLA, error) {
	state := &TicketSLA{}
	var first, resolved sql.NullTime
	if err := s.Scan(&state.ID, &state.OrganizationID, &state.TicketID, &state.SLAID, &state.ResponseDueAt, &state.ResolutionDueAt, &first, &resolved, &state.ResponseBreached, &state.ResolutionBreached, &state.CalendarID, &state.CreatedAt, &state.UpdatedAt); err != nil {
		return nil, err
	}
	if first.Valid {
		v := first.Time.UTC()
		state.FirstResponseAt = &v
	}
	if resolved.Valid {
		v := resolved.Time.UTC()
		state.ResolvedAt = &v
	}
	return state, nil
}

func refreshTicketSLA(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `UPDATE ticket_sla SET response_breached = CASE WHEN first_response_at IS NULL THEN now() > response_due_at ELSE first_response_at > response_due_at END, resolution_breached = CASE WHEN resolved_at IS NULL THEN now() > resolution_due_at ELSE resolved_at > resolution_due_at END, updated_at = now() WHERE `+ticketSLAVisible+` AND id = $1`, id)
	return err
}

func refreshTicketSLAByTicket(ctx context.Context, tx pgx.Tx, orgID, ticketID string) error {
	_, err := tx.Exec(ctx, `UPDATE ticket_sla SET response_breached = CASE WHEN first_response_at IS NULL THEN now() > response_due_at ELSE first_response_at > response_due_at END, resolution_breached = CASE WHEN resolved_at IS NULL THEN now() > resolution_due_at ELSE resolved_at > resolution_due_at END, updated_at = now() WHERE `+ticketSLAVisible+` AND organization_id = $1 AND ticket_id = $2`, orgID, ticketID)
	return err
}

// calendarColumns are the columns of a BusinessCalendar (scanCalendar).
const calendarColumns = `id::text, organization_id::text, client_id::text, name, timezone, weekly_hours, holidays, is_default, created_at, updated_at`

// calendarConstraints maps the partial unique index of the default
// calendar onto its request field (409 on is_default).
var calendarConstraints = map[string]string{"business_calendar_default": "is_default"}

// calendarDBError turns a second default per organization and client into
// the conflict on is_default; other errors pass unchanged.
func calendarDBError(err error) error {
	mapped := api.FieldDBError(err, calendarConstraints)
	var conflict *api.ConflictError
	if errors.As(mapped, &conflict) {
		return errCalendarDefaultTaken
	}
	return mapped
}

// errCalendarClient: client_id names no client visible under the scope
// (400 on client_id, alike for unknown and foreign clients).
var errCalendarClient = &api.FieldError{Field: "client_id", Message: "client not found"}

func scanCalendar(s scanner) (*BusinessCalendar, error) {
	c := &BusinessCalendar{}
	var weekly, holidays []byte
	if err := s.Scan(&c.ID, &c.OrganizationID, &c.ClientID, &c.Name, &c.Timezone, &weekly, &holidays, &c.IsDefault, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(weekly, &c.WeeklyHours); err != nil {
		return nil, fmt.Errorf("decode weekly hours: %w", err)
	}
	if err := json.Unmarshal(holidays, &c.Holidays); err != nil {
		return nil, fmt.Errorf("decode holidays: %w", err)
	}
	if c.WeeklyHours == nil {
		c.WeeklyHours = []Window{}
	}
	if c.Holidays == nil {
		c.Holidays = []Holiday{}
	}
	return c, nil
}

// calendarJSON encodes the windows and holidays for the jsonb columns.
func calendarJSON(c *BusinessCalendar) (weekly, holidays string, err error) {
	w, err := json.Marshal(c.WeeklyHours)
	if err != nil {
		return "", "", fmt.Errorf("encode weekly hours: %w", err)
	}
	h, err := json.Marshal(c.Holidays)
	if err != nil {
		return "", "", fmt.Errorf("encode holidays: %w", err)
	}
	return string(w), string(h), nil
}

// ListCalendars reads the calendars visible under the scope (org-wide and
// those of the principal's clients, E-09), sorted by name and id, one row
// more than the page limit for the keyset cursor.
func (r *PGRepository) ListCalendars(ctx context.Context, orgID string, filter BusinessCalendarFilter, page api.PaginationParams) ([]BusinessCalendar, int, error) {
	items := make([]BusinessCalendar, 0)
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := "organization_id = $1"
		args := []any{orgID}
		pos := 2
		if filter.ClientID != "" {
			where += fmt.Sprintf(" AND client_id = $%d::uuid", pos)
			args = append(args, filter.ClientID)
			pos++
		}
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM business_calendar WHERE "+where, args...).Scan(&total); err != nil {
			return fmt.Errorf("count business calendars: %w", err)
		}
		if page.Cursor != nil {
			name, id, posErr := calendarPosition(page.Cursor)
			if posErr != nil {
				return posErr
			}
			where += " AND " + api.KeysetClause(calendarSort, "text", calendarDir, pos)
			args = append(args, name, id)
			pos += 2
		}
		tail := fmt.Sprintf(" ORDER BY name ASC, id ASC LIMIT $%d", pos)
		args = append(args, page.Limit+1)
		if page.Cursor == nil && page.Offset > 0 {
			tail += fmt.Sprintf(" OFFSET $%d", pos+1)
			args = append(args, page.Offset)
		}
		rows, err := tx.Query(ctx, "SELECT "+calendarColumns+" FROM business_calendar WHERE "+where+tail, args...)
		if err != nil {
			return fmt.Errorf("list business calendars: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			item, scanErr := scanCalendar(rows)
			if scanErr != nil {
				return scanErr
			}
			items = append(items, *item)
		}
		return rows.Err()
	})
	return items, total, err
}

func (r *PGRepository) GetCalendar(ctx context.Context, orgID, id string) (*BusinessCalendar, error) {
	if !api.IsUUID(id) {
		return nil, ErrCalendarNotFound
	}
	var item *BusinessCalendar
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		item, err = scanCalendar(tx.QueryRow(ctx, "SELECT "+calendarColumns+" FROM business_calendar WHERE organization_id = $1 AND id = $2", orgID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCalendarNotFound
		}
		return err
	})
	return item, err
}

// CreateCalendar stores a calendar after Calendar.Validate. client_id must
// name a client visible under the scope (400 on client_id); an org-wide
// calendar of a client-scoped principal violates WITH CHECK (E-09, 403);
// a second default is 409 on is_default.
func (r *PGRepository) CreateCalendar(ctx context.Context, c *BusinessCalendar) error {
	if err := c.validate(); err != nil {
		return err
	}
	if c.ClientID != nil && !api.IsUUID(*c.ClientID) {
		return errCalendarClient
	}
	weekly, holidays, err := calendarJSON(c)
	if err != nil {
		return err
	}
	return database.WithRequestTenant(ctx, r.pool, c.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if c.ClientID != nil {
			var visible bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM client WHERE organization_id = $1 AND id = $2::uuid)`, c.OrganizationID, *c.ClientID).Scan(&visible); err != nil {
				return fmt.Errorf("read client: %w", err)
			}
			if !visible {
				return errCalendarClient
			}
		}
		created, err := scanCalendar(tx.QueryRow(ctx, `
			INSERT INTO business_calendar (organization_id, client_id, name, timezone, weekly_hours, holidays, is_default)
			VALUES ($1, $2::uuid, $3, $4, $5::jsonb, $6::jsonb, $7)
			RETURNING `+calendarColumns, c.OrganizationID, c.ClientID, c.Name, c.Timezone, weekly, holidays, c.IsDefault))
		if err != nil {
			return calendarDBError(err)
		}
		*c = *created
		return nil
	})
}

// UpdateCalendar changes a calendar the principal may change: the row is
// locked with FOR UPDATE, which applies business_calendar_scoped_update,
// so an org-wide calendar of a client-scoped principal is not found (404)
// and stays unchanged. The merged calendar passes Calendar.Validate.
func (r *PGRepository) UpdateCalendar(ctx context.Context, orgID, id string, req *UpdateBusinessCalendarRequest) (*BusinessCalendar, error) {
	if !api.IsUUID(id) {
		return nil, ErrCalendarNotFound
	}
	var item *BusinessCalendar
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := scanCalendar(tx.QueryRow(ctx, "SELECT "+calendarColumns+" FROM business_calendar WHERE organization_id = $1 AND id = $2 FOR UPDATE", orgID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCalendarNotFound
		}
		if err != nil {
			return err
		}
		req.apply(current)
		if invalid := current.validate(); invalid != nil {
			return invalid
		}
		weekly, holidays, err := calendarJSON(current)
		if err != nil {
			return err
		}
		item, err = scanCalendar(tx.QueryRow(ctx, `
			UPDATE business_calendar SET name = $3, timezone = $4, weekly_hours = $5::jsonb, holidays = $6::jsonb, is_default = $7
			WHERE organization_id = $1 AND id = $2
			RETURNING `+calendarColumns, orgID, id, current.Name, current.Timezone, weekly, holidays, current.IsDefault))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCalendarNotFound
		}
		if err != nil {
			return calendarDBError(err)
		}
		return nil
	})
	return item, err
}

// DeleteCalendar removes a calendar the principal may delete
// (business_calendar_scoped_delete; otherwise 404). The foreign keys set
// sla.calendar_id and ticket_sla.calendar_id to NULL; due times stay.
func (r *PGRepository) DeleteCalendar(ctx context.Context, orgID, id string) error {
	if !api.IsUUID(id) {
		return ErrCalendarNotFound
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmd, err := tx.Exec(ctx, "DELETE FROM business_calendar WHERE organization_id = $1 AND id = $2", orgID, id)
		if err != nil {
			return fmt.Errorf("delete business calendar: %w", err)
		}
		if cmd.RowsAffected() == 0 {
			return ErrCalendarNotFound
		}
		return nil
	})
}
