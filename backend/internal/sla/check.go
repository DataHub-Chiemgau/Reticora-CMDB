package sla

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CheckKind is the scheduler kind of the SLA breach run (TKT-01, WP-769).
const CheckKind = "sla.check"

// CheckBatch is the number of ticket_sla rows one transaction of the run
// marks at most.
const CheckBatch = 500

// Breach kinds of ticket.sla_breached.
const (
	BreachResponse   = "response"
	BreachResolution = "resolution"
)

// BreachCheck is the sla.check run of the scheduler: it marks the due times
// of the ticket SLAs that passed without first response or resolution and
// reports each once as ticket.sla_breached, the escalation to the assignee
// and the team lead (notify.Subscriptions). A due time is reported when
// its *_breach_notified_at is NULL; the run sets it, whether a read set
// the breached flag before or not. A new computation of the due times
// (ApplyForTicket, the reapplication) and a reopen clear it again.
type BreachCheck struct {
	pool *pgxpool.Pool
	// Licensed reports whether the organization holds an active
	// ticketing_standard; nil treats every organization as licensed. An
	// organization without it is skipped.
	Licensed func(ctx context.Context, orgID string) (bool, error)
	now      func() time.Time
	batch    int
}

// NewBreachCheck creates the run on pool.
func NewBreachCheck(pool *pgxpool.Pool) *BreachCheck {
	return &BreachCheck{pool: pool, now: func() time.Time { return time.Now().UTC() }, batch: CheckBatch}
}

// WithClock replaces the clock (tests).
func (c *BreachCheck) WithClock(now func() time.Time) *BreachCheck {
	c.now = now
	return c
}

// Schedule runs the check every minute; after a pause one missed slot is
// caught up.
func (c *BreachCheck) Schedule() jobs.Schedule {
	return jobs.Schedule{Kind: CheckKind, Spec: "@every 1m", MaxCatchUp: 1, Run: func(ctx context.Context) error {
		_, err := c.Run(ctx)
		return err
	}}
}

// Run checks every organization and returns the number of reported
// breaches. A failing organization does not stop the others: the errors
// are joined at the end.
func (c *BreachCheck) Run(ctx context.Context) (int, error) {
	var orgs []string
	err := database.WithSystem(ctx, c.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM organization ORDER BY id`)
		if err != nil {
			return err
		}
		orgs, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("list organizations: %w", err)
	}
	total := 0
	var errs []error
	for _, org := range orgs {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		n, oerr := c.RunOrg(ctx, org)
		total += n
		if oerr != nil {
			errs = append(errs, fmt.Errorf("organization %s: %w", org, oerr))
		}
	}
	return total, errors.Join(errs...)
}

// RunOrg checks one organization in batches of at most CheckBatch rows,
// each in its own transaction with the org-wide scope (K20); the events
// are the last statement of the batch's transaction. It returns the
// number of reported breaches.
func (c *BreachCheck) RunOrg(ctx context.Context, orgID string) (int, error) {
	scope := database.OrgWideScope(orgID, "")
	ctx = database.ContextWithTenantScope(ctx, &scope)
	if c.Licensed != nil {
		licensed, err := c.Licensed(ctx, orgID)
		if err != nil {
			return 0, fmt.Errorf("ticketing_standard license: %w", err)
		}
		if !licensed {
			return 0, nil
		}
	}
	total := 0
	for {
		rows, marked, err := c.batchOnce(ctx, &scope, orgID)
		total += marked
		if err != nil {
			return total, err
		}
		if rows < c.batch {
			return total, nil
		}
	}
}

// batchOnce marks one batch and writes its events; rows is the number of
// ticket_sla rows of the batch, marked the number of events.
func (c *BreachCheck) batchOnce(ctx context.Context, scope *database.TenantScope, orgID string) (rows, marked int, err error) {
	now := c.now()
	err = database.WithTenant(ctx, c.pool, scope, func(ctx context.Context, tx pgx.Tx) error {
		result, qerr := tx.Query(ctx, `
			WITH due AS (
				SELECT ts.id, ts.ticket_id,
				       ts.first_response_at IS NULL AND ts.response_breach_notified_at IS NULL AND ts.response_due_at < $2 AS response,
				       ts.resolved_at IS NULL AND ts.resolution_breach_notified_at IS NULL AND ts.resolution_due_at < $2 AS resolution
				  FROM ticket_sla ts
				 WHERE ts.organization_id = $1
				   AND ((ts.first_response_at IS NULL AND ts.response_breach_notified_at IS NULL AND ts.response_due_at < $2)
				     OR (ts.resolved_at IS NULL AND ts.resolution_breach_notified_at IS NULL AND ts.resolution_due_at < $2))
				 ORDER BY ts.id
				 LIMIT $3
				 FOR UPDATE OF ts SKIP LOCKED
			)
			UPDATE ticket_sla ts SET
				response_breached = ts.response_breached OR due.response,
				response_breach_notified_at = CASE WHEN due.response THEN now() ELSE ts.response_breach_notified_at END,
				resolution_breached = ts.resolution_breached OR due.resolution,
				resolution_breach_notified_at = CASE WHEN due.resolution THEN now() ELSE ts.resolution_breach_notified_at END,
				updated_at = now()
			  FROM due
			  JOIN ticket t ON t.id = due.ticket_id AND t.organization_id = $1
			  LEFT JOIN team tm ON tm.id = t.team_id AND tm.organization_id = t.organization_id
			 WHERE ts.id = due.id
			RETURNING t.id::text, t.ticket_number, t.priority, ts.sla_id::text, due.response, due.resolution,
			          ts.response_due_at, ts.resolution_due_at,
			          COALESCE(t.assignee_id::text, ''), COALESCE(t.team_id::text, ''), COALESCE(tm.lead_id::text, ''),
			          COALESCE(t.client_id::text, ''), COALESCE(t.site_id::text, '')`, orgID, now, c.batch)
		if qerr != nil {
			return fmt.Errorf("mark sla breaches: %w", qerr)
		}
		var batch []*events.Event
		rows = 0
		for result.Next() {
			rows++
			var d events.TicketSLABreachedData
			var response, resolution bool
			var responseDue, resolutionDue time.Time
			if scanErr := result.Scan(&d.ID, &d.TicketNumber, &d.Priority, &d.SLAID, &response, &resolution,
				&responseDue, &resolutionDue, &d.AssigneeID, &d.TeamID, &d.TeamLeadID, &d.ClientID, &d.SiteID); scanErr != nil {
				result.Close()
				return fmt.Errorf("mark sla breaches: %w", scanErr)
			}
			if response {
				e := d
				e.Kind, e.DueAt = BreachResponse, responseDue.UTC().Format(time.RFC3339)
				batch = append(batch, &events.Event{OrgID: orgID, Type: events.TicketSLABreached, EntityID: d.ID, Data: e})
			}
			if resolution {
				e := d
				e.Kind, e.DueAt = BreachResolution, resolutionDue.UTC().Format(time.RFC3339)
				batch = append(batch, &events.Event{OrgID: orgID, Type: events.TicketSLABreached, EntityID: d.ID, Data: e})
			}
		}
		result.Close()
		if rerr := result.Err(); rerr != nil {
			return fmt.Errorf("mark sla breaches: %w", rerr)
		}
		marked = len(batch)
		// The events are the last statement of the transaction: they are
		// published only when the marks commit.
		if _, aerr := events.NewOutbox().AppendMany(ctx, tx, batch); aerr != nil {
			return aerr
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return rows, marked, nil
}
