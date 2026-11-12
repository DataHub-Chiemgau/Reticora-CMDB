package alarm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
)

// RetentionKind is the system job that ends resolved alarms after the
// retention period of ALR-01 (400 days).
const RetentionKind = "alarm.retention"

// RetentionBatch is the largest portion of alarms one statement deletes.
const RetentionBatch = 10000

// RetentionRun deletes the resolved alarms whose resolved_at is older than
// Retention, per organization in its tenant transaction and in portions
// of at most RetentionBatch; their transitions follow by cascade under
// app.alarm_retention = 'on' (migration 000313). Alarms that are not
// resolved stay whatever their age. The resolved incidents follow the same
// rule with their incident_transition (WP-1027, migration 000326).
type RetentionRun struct {
	pool  *pgxpool.Pool
	clock func() time.Time
	batch int
}

// NewRetention returns the retention run on pool.
func NewRetention(pool *pgxpool.Pool) *RetentionRun {
	return &RetentionRun{pool: pool, clock: now, batch: RetentionBatch}
}

// WithClock replaces the clock (tests).
func (r *RetentionRun) WithClock(clock func() time.Time) *RetentionRun {
	r.clock = clock
	return r
}

// Schedule runs the retention daily at 04:37 UTC (leader only, like every
// system job); a missed day is caught up once.
func (r *RetentionRun) Schedule() jobs.Schedule {
	return jobs.Schedule{Kind: RetentionKind, Spec: "37 4 * * *", MaxCatchUp: 1, Timeout: time.Hour,
		Run: func(ctx context.Context) error {
			deleted, err := r.Run(ctx)
			if deleted > 0 {
				slog.InfoContext(ctx, "alarm retention", "deleted", deleted)
			}
			return err
		}}
}

// Run deletes the expired alarms of every organization and returns their
// number. A failing organization does not stop the others.
func (r *RetentionRun) Run(ctx context.Context) (int, error) {
	orgs, err := database.OrganizationIDs(ctx, r.pool)
	if err != nil {
		return 0, fmt.Errorf("%s: list organizations: %w", RetentionKind, err)
	}
	cutoff := r.clock().Add(-Retention)
	total := 0
	var errs []error
	for _, orgID := range orgs {
		n, err := r.RunOrganization(ctx, orgID, cutoff)
		total += n
		if err != nil {
			slog.ErrorContext(ctx, "alarm retention", "organization_id", orgID, "error", err)
			errs = append(errs, fmt.Errorf("%s for %s: %w", RetentionKind, orgID, err))
		}
	}
	return total, errors.Join(errs...)
}

// RunOrganization deletes the alarms and incidents of one organization
// resolved before cutoff, one portion of each per transaction, and audits
// the run when it deleted something. The transitions of both follow by
// cascade; an alarm of a deleted incident keeps its own period (its
// incident_id becomes NULL, migration 000326). It returns the number of
// deleted alarms and incidents.
func (r *RetentionRun) RunOrganization(ctx context.Context, orgID string, cutoff time.Time) (int, error) {
	scope := database.OrgWideScope(orgID, "")
	total := 0
	for {
		alarms, incidents := 0, 0
		err := database.WithTenant(ctx, r.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT set_config('app.alarm_retention', 'on', true)`); err != nil {
				return fmt.Errorf("declare retention: %w", err)
			}
			tag, err := tx.Exec(ctx, `DELETE FROM incident WHERE id IN (
				SELECT id FROM incident WHERE organization_id = $1 AND state = 'resolved' AND resolved_at < $2
				 ORDER BY resolved_at LIMIT $3)`, orgID, cutoff, r.batch)
			if err != nil {
				return fmt.Errorf("delete resolved incidents: %w", err)
			}
			incidents = int(tag.RowsAffected())
			tag, err = tx.Exec(ctx, `DELETE FROM alarm WHERE id IN (
				SELECT id FROM alarm WHERE organization_id = $1 AND state = 'resolved' AND resolved_at < $2
				 ORDER BY resolved_at LIMIT $3)`, orgID, cutoff, r.batch)
			if err != nil {
				return fmt.Errorf("delete resolved alarms: %w", err)
			}
			alarms = int(tag.RowsAffected())
			if _, err = tx.Exec(ctx, `SELECT set_config('app.alarm_retention', 'off', true)`); err != nil {
				return fmt.Errorf("end retention: %w", err)
			}
			if alarms == 0 && incidents == 0 {
				return nil
			}
			_, err = audit.NewPGRecorder().Record(ctx, tx, audit.Entry{
				OrganizationID: orgID, ActorType: "system", Action: RetentionKind, ResourceType: "retention",
				Source: audit.SourceSystem, Changes: map[string]interface{}{"deleted": alarms, "deleted_incidents": incidents},
			})
			return err
		})
		if err != nil {
			return total, err
		}
		total += alarms + incidents
		if alarms < r.batch && incidents < r.batch {
			return total, nil
		}
	}
}
