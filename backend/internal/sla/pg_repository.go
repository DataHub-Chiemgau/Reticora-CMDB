package sla

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const policyColumns = `id::text, organization_id::text, COALESCE(client_id::text, ''), name, priority, response_target_minutes, resolution_target_minutes, business_calendar, created_at, updated_at`
const ticketSLAColumns = `id::text, organization_id::text, ticket_id::text, sla_id::text, response_due_at, resolution_due_at, first_response_at, resolved_at, response_breached, resolution_breached, created_at, updated_at`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct{ pool *pgxpool.Pool }

// NewPGRepository creates a PostgreSQL SLA repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository { return &PGRepository{pool: pool} }

func (r *PGRepository) withTenant(ctx context.Context, orgID string, fn func(context.Context, pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('app.org_id', $1, true)", orgID); err != nil {
		return fmt.Errorf("set tenant context: %w", err)
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *PGRepository) ListPolicies(ctx context.Context, orgID, priority, clientID string, page api.PaginationParams) ([]Policy, int, error) {
	items := make([]Policy, 0)
	var total int
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
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
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		item, err = scanPolicy(tx.QueryRow(ctx, "SELECT "+policyColumns+" FROM sla WHERE organization_id = $1 AND id = $2", orgID, id))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("sla policy not found")
		}
		return err
	})
	return item, err
}

func (r *PGRepository) CreatePolicy(ctx context.Context, p *Policy) error {
	return r.withTenant(ctx, p.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO sla (organization_id, client_id, name, priority, response_target_minutes, resolution_target_minutes, business_calendar)
			VALUES ($1, NULLIF($2, '')::uuid, $3, $4, $5, $6, $7)
			RETURNING id::text, created_at, updated_at`, p.OrganizationID, p.ClientID, p.Name, p.Priority, p.ResponseTargetMinutes, p.ResolutionTargetMinutes, p.BusinessCalendar).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
	})
}

func (r *PGRepository) UpdatePolicy(ctx context.Context, orgID, id string, req UpdatePolicyRequest) (*Policy, error) {
	var item *Policy
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
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
		if len(set) == 0 {
			var err error
			item, err = scanPolicy(tx.QueryRow(ctx, "SELECT "+policyColumns+" FROM sla WHERE id = $1 AND organization_id = $2", id, orgID))
			if err == pgx.ErrNoRows {
				return fmt.Errorf("sla policy not found")
			}
			return err
		}
		set = append(set, "updated_at = now()")
		var err error
		item, err = scanPolicy(tx.QueryRow(ctx, fmt.Sprintf("UPDATE sla SET %s WHERE id = $1 AND organization_id = $2 RETURNING %s", strings.Join(set, ", "), policyColumns), args...))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("sla policy not found")
		}
		return err
	})
	return item, err
}

func (r *PGRepository) DeletePolicy(ctx context.Context, orgID, id string) error {
	return r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmd, err := tx.Exec(ctx, "DELETE FROM sla WHERE organization_id = $1 AND id = $2", orgID, id)
		if err != nil {
			return fmt.Errorf("delete sla policy: %w", err)
		}
		if cmd.RowsAffected() == 0 {
			return fmt.Errorf("sla policy not found")
		}
		return nil
	})
}

func (r *PGRepository) ApplyForTicket(ctx context.Context, orgID string, t *ticket.Ticket, slaID string) (*TicketSLA, error) {
	var state *TicketSLA
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		policyID := slaID
		if policyID == "" {
			if err := tx.QueryRow(ctx, `SELECT id::text FROM sla WHERE organization_id = $1 AND priority = $2 AND (client_id IS NULL OR client_id::text = $3) ORDER BY client_id NULLS LAST, created_at DESC LIMIT 1`, orgID, t.Priority, "").Scan(&policyID); err != nil {
				if err == pgx.ErrNoRows {
					return fmt.Errorf("sla policy not found")
				}
				return fmt.Errorf("select sla policy: %w", err)
			}
		}
		created := t.CreatedAt
		if created.IsZero() {
			created = time.Now().UTC()
		}
		var responseMinutes, resolutionMinutes int
		if err := tx.QueryRow(ctx, "SELECT response_target_minutes, resolution_target_minutes FROM sla WHERE organization_id = $1 AND id = $2", orgID, policyID).Scan(&responseMinutes, &resolutionMinutes); err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("sla policy not found")
			}
			return fmt.Errorf("read sla policy: %w", err)
		}
		row := tx.QueryRow(ctx, `
			INSERT INTO ticket_sla (organization_id, ticket_id, sla_id, response_due_at, resolution_due_at)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (organization_id, ticket_id) DO UPDATE SET
				sla_id = EXCLUDED.sla_id,
				response_due_at = EXCLUDED.response_due_at,
				resolution_due_at = EXCLUDED.resolution_due_at,
				updated_at = now()
			RETURNING `+ticketSLAColumns, orgID, t.ID, policyID, created.Add(time.Duration(responseMinutes)*time.Minute), created.Add(time.Duration(resolutionMinutes)*time.Minute))
		var err error
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

func (r *PGRepository) GetForTicket(ctx context.Context, orgID, ticketID string) (*TicketSLA, error) {
	var state *TicketSLA
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, "SELECT id::text FROM ticket_sla WHERE organization_id = $1 AND ticket_id = $2", orgID, ticketID).Scan(&id); err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("ticket sla not found")
			}
			return err
		}
		if err := refreshTicketSLA(ctx, tx, id); err != nil {
			return err
		}
		var err error
		state, err = scanTicketSLA(tx.QueryRow(ctx, "SELECT "+ticketSLAColumns+" FROM ticket_sla WHERE id = $1 AND organization_id = $2", id, orgID))
		return err
	})
	return state, err
}

func (r *PGRepository) MarkFirstResponse(ctx context.Context, orgID, ticketID string, at time.Time) error {
	return r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE ticket_sla SET first_response_at = COALESCE(first_response_at, $3), updated_at = now() WHERE organization_id = $1 AND ticket_id = $2`, orgID, ticketID, at.UTC())
		if err != nil {
			return fmt.Errorf("mark first response: %w", err)
		}
		return refreshTicketSLAByTicket(ctx, tx, orgID, ticketID)
	})
}

func (r *PGRepository) MarkResolved(ctx context.Context, orgID, ticketID string, at time.Time) error {
	return r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE ticket_sla SET resolved_at = COALESCE(resolved_at, $3), updated_at = now() WHERE organization_id = $1 AND ticket_id = $2`, orgID, ticketID, at.UTC())
		if err != nil {
			return fmt.Errorf("mark resolved: %w", err)
		}
		return refreshTicketSLAByTicket(ctx, tx, orgID, ticketID)
	})
}

func (r *PGRepository) ListBreaches(ctx context.Context, orgID string, filter BreachFilter, page api.PaginationParams) ([]TicketSLA, int, error) {
	items := make([]TicketSLA, 0)
	var total int
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE ticket_sla SET response_breached = CASE WHEN first_response_at IS NULL THEN now() > response_due_at ELSE first_response_at > response_due_at END, resolution_breached = CASE WHEN resolved_at IS NULL THEN now() > resolution_due_at ELSE resolved_at > resolution_due_at END, updated_at = now() WHERE organization_id = $1`, orgID); err != nil {
			return err
		}
		where := "organization_id = $1"
		if filter.Status == "breached" {
			where += " AND (response_breached OR resolution_breached)"
		}
		if filter.Status == "at_risk" {
			where += " AND NOT (response_breached OR resolution_breached) AND ((first_response_at IS NULL AND response_due_at <= now() + interval '1 hour') OR (resolved_at IS NULL AND resolution_due_at <= now() + interval '1 hour'))"
		}
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM ticket_sla WHERE "+where, orgID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+ticketSLAColumns+" FROM ticket_sla WHERE "+where+" ORDER BY resolution_due_at ASC LIMIT $2 OFFSET $3", orgID, page.Limit, page.Offset)
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
		return rows.Err()
	})
	return items, total, err
}

type scanner interface{ Scan(dest ...any) error }

func scanPolicy(s scanner) (*Policy, error) {
	p := &Policy{}
	if err := s.Scan(&p.ID, &p.OrganizationID, &p.ClientID, &p.Name, &p.Priority, &p.ResponseTargetMinutes, &p.ResolutionTargetMinutes, &p.BusinessCalendar, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	return p, nil
}

func scanTicketSLA(s scanner) (*TicketSLA, error) {
	state := &TicketSLA{}
	var first, resolved sql.NullTime
	if err := s.Scan(&state.ID, &state.OrganizationID, &state.TicketID, &state.SLAID, &state.ResponseDueAt, &state.ResolutionDueAt, &first, &resolved, &state.ResponseBreached, &state.ResolutionBreached, &state.CreatedAt, &state.UpdatedAt); err != nil {
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
	_, err := tx.Exec(ctx, `UPDATE ticket_sla SET response_breached = CASE WHEN first_response_at IS NULL THEN now() > response_due_at ELSE first_response_at > response_due_at END, resolution_breached = CASE WHEN resolved_at IS NULL THEN now() > resolution_due_at ELSE resolved_at > resolution_due_at END, updated_at = now() WHERE id = $1`, id)
	return err
}

func refreshTicketSLAByTicket(ctx context.Context, tx pgx.Tx, orgID, ticketID string) error {
	_, err := tx.Exec(ctx, `UPDATE ticket_sla SET response_breached = CASE WHEN first_response_at IS NULL THEN now() > response_due_at ELSE first_response_at > response_due_at END, resolution_breached = CASE WHEN resolved_at IS NULL THEN now() > resolution_due_at ELSE resolved_at > resolution_due_at END, updated_at = now() WHERE organization_id = $1 AND ticket_id = $2`, orgID, ticketID)
	return err
}
