package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// Retention runs of NFR-06 and AUD-09: system jobs of the scheduler that
// delete what is older than its retention period, per organization in the
// organization's transaction, each run audited. The audit log keeps its
// chain verifiable: its oldest entries go, an audit.retention entry
// anchors the rest (audit.Purge).
//
// Not here: events (stream MaxAge, RETICORA_EVENTS_MAX_AGE, at least 30
// days; the outbox cleanup of the publisher), metrics (TimescaleDB
// retention policies, 400 days, migrations 000020 and 000174), logs (30
// days) and backups (35 days) expire where they are kept; see
// docs/backup-dr.md and docs/ops/workers.md.

// RetentionPolicy is how long each kind of data is kept.
type RetentionPolicy struct {
	AuditLogYears int
	History       time.Duration
	SoftDeleted   time.Duration
	ReviewItems   time.Duration
}

// RetentionFromDays builds a policy from configured periods.
func RetentionFromDays(auditYears, historyDays, softDeleteDays, reviewItemDays int) RetentionPolicy {
	day := 24 * time.Hour
	return RetentionPolicy{
		AuditLogYears: auditYears, History: time.Duration(historyDays) * day,
		SoftDeleted: time.Duration(softDeleteDays) * day, ReviewItems: time.Duration(reviewItemDays) * day,
	}
}

// Kinds of the retention runs.
const (
	RetentionAuditLog    = "retention.audit_log"
	RetentionHistory     = "retention.history"
	RetentionSoftDeleted = "retention.soft_deleted"
	RetentionReviewItems = "retention.review_items"
)

// Retention deletes expired data.
type Retention struct {
	pool   *pgxpool.Pool
	policy RetentionPolicy
	now    func() time.Time
}

// NewRetention creates the retention runs on pool.
func NewRetention(pool *pgxpool.Pool, policy RetentionPolicy) *Retention {
	return &Retention{pool: pool, policy: policy, now: func() time.Time { return time.Now().UTC() }}
}

// WithClock replaces the clock (tests).
func (r *Retention) WithClock(now func() time.Time) *Retention {
	r.now = now
	return r
}

// Schedules are the retention runs for the scheduler: nightly, one kind
// after the other; a missed night is not repeated (one run deletes
// everything expired).
func (r *Retention) Schedules() []Schedule {
	return []Schedule{
		{Kind: RetentionAuditLog, Spec: "17 3 * * *", MaxCatchUp: 1, Timeout: time.Hour, Run: r.RunAuditLog},
		{Kind: RetentionHistory, Spec: "27 3 * * *", MaxCatchUp: 1, Timeout: time.Hour, Run: r.RunHistory},
		{Kind: RetentionSoftDeleted, Spec: "37 3 * * *", MaxCatchUp: 1, Timeout: time.Hour, Run: r.RunSoftDeleted},
		{Kind: RetentionReviewItems, Spec: "47 3 * * *", MaxCatchUp: 1, Timeout: time.Hour, Run: r.RunReviewItems},
	}
}

// RunAuditLog removes audit entries older than the audit period.
func (r *Retention) RunAuditLog(ctx context.Context) error {
	cutoff := r.now().AddDate(-r.policy.AuditLogYears, 0, 0)
	return r.eachOrganization(ctx, RetentionAuditLog, false, func(ctx context.Context, tx pgx.Tx, orgID string) (int, error) {
		return audit.Purge(ctx, tx, orgID, cutoff)
	})
}

// RunHistory removes CI, entity and location history older than the
// history period.
func (r *Retention) RunHistory(ctx context.Context) error {
	cutoff := r.now().Add(-r.policy.History)
	return r.eachOrganization(ctx, RetentionHistory, true, func(ctx context.Context, tx pgx.Tx, orgID string) (int, error) {
		total := 0
		for _, q := range []string{
			`DELETE FROM ci_change WHERE organization_id = $1 AND created_at < $2`,
			`DELETE FROM entity_change WHERE organization_id = $1 AND created_at < $2`,
			`DELETE FROM location_change WHERE organization_id = $1 AND changed_at < $2`,
		} {
			tag, err := tx.Exec(ctx, q, orgID, cutoff)
			if err != nil {
				return total, fmt.Errorf("delete history: %w", err)
			}
			total += int(tag.RowsAffected())
		}
		return total, nil
	})
}

// RunSoftDeleted removes CIs soft-deleted longer than the soft-delete
// period (default 90 days) with HardDeleteCI: their IP addresses go, the
// references of assets, assignments, tickets and scans are released. A CI
// that still cannot be deleted stays and is counted as kept.
func (r *Retention) RunSoftDeleted(ctx context.Context) error {
	cutoff := r.now().Add(-r.policy.SoftDeleted)
	return r.eachOrganization(ctx, RetentionSoftDeleted, true, func(ctx context.Context, tx pgx.Tx, orgID string) (int, error) {
		rows, err := tx.Query(ctx, `SELECT id::text FROM ci WHERE organization_id = $1 AND deleted_at < $2`, orgID, cutoff)
		if err != nil {
			return 0, fmt.Errorf("list soft-deleted CIs: %w", err)
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return 0, fmt.Errorf("list soft-deleted CIs: %w", err)
		}
		deleted, kept := 0, 0
		for _, id := range ids {
			if _, err = tx.Exec(ctx, `SAVEPOINT retention_ci`); err != nil {
				return deleted, err
			}
			if err = HardDeleteCI(ctx, tx, orgID, id); err != nil {
				kept++
				if _, rerr := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT retention_ci`); rerr != nil {
					return deleted, rerr
				}
				continue
			}
			deleted++
		}
		if kept > 0 {
			slog.Info("soft-deleted CIs kept: still referenced", "organization_id", orgID, "kept", kept)
		}
		return deleted, nil
	})
}

// RunReviewItems removes review items resolved longer than the review
// period ago.
func (r *Retention) RunReviewItems(ctx context.Context) error {
	cutoff := r.now().Add(-r.policy.ReviewItems)
	return r.eachOrganization(ctx, RetentionReviewItems, true, func(ctx context.Context, tx pgx.Tx, orgID string) (int, error) {
		tag, err := tx.Exec(ctx, `DELETE FROM review_item WHERE organization_id = $1 AND resolved_at < $2`, orgID, cutoff)
		if err != nil {
			return 0, fmt.Errorf("delete review items: %w", err)
		}
		return int(tag.RowsAffected()), nil
	})
}

// eachOrganization runs fn for every organization in its own transaction;
// with selfAudit a run that deleted something writes an audit entry in the
// same transaction. A failing organization does not stop the others.
func (r *Retention) eachOrganization(ctx context.Context, kind string, selfAudit bool,
	fn func(ctx context.Context, tx pgx.Tx, orgID string) (int, error)) error {
	var orgs []string
	err := database.WithSystem(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM organization ORDER BY id`)
		if err != nil {
			return err
		}
		orgs, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return fmt.Errorf("%s: list organizations: %w", kind, err)
	}
	var firstErr error
	for _, orgID := range orgs {
		scope := database.OrgWideScope(orgID, "")
		err := database.WithTenant(ctx, r.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
			n, err := fn(ctx, tx, orgID)
			if err != nil || !selfAudit || n == 0 {
				return err
			}
			_, err = audit.NewPGRecorder().Record(ctx, tx, audit.Entry{
				OrganizationID: orgID, ActorType: "system", Action: kind, ResourceType: "retention", Source: audit.SourceSystem,
				Changes: map[string]interface{}{"deleted": n},
			})
			return err
		})
		if err != nil {
			slog.Error("retention run", "kind", kind, "organization_id", orgID, "error", err)
			if firstErr == nil {
				firstErr = fmt.Errorf("%s for %s: %w", kind, orgID, err)
			}
		}
	}
	return firstErr
}
