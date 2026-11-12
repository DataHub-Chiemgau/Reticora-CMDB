package jobs

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
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
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
	// RetentionMergeSnapshots ends what a CI merge keeps (CI-12, F-251).
	RetentionMergeSnapshots = "retention.merge_snapshots"
	// RetentionBulkChangeSet ends the change sets of bulk jobs (BLK-02,
	// WP-306).
	RetentionBulkChangeSet = "retention.bulk_change_set"
	// BulkChangeSetRetention is how long the change set of a bulk job is
	// kept (WP-135): a fixed period, far beyond the revert window of 24 h.
	BulkChangeSetRetention = 30 * 24 * time.Hour
)

// Retention deletes expired data.
type Retention struct {
	pool   *pgxpool.Pool
	policy RetentionPolicy
	now    func() time.Time
	// blobs holds the document content (WithBlobs); nil keeps every
	// soft-deleted document with stored content.
	blobs blob.Store
}

// documentBucket is the bucket of the document content (document.Handler).
const documentBucket = "reticora-documents"

// actionDocumentPurged is the audit action of a document the soft-delete
// run removes for good (document.ActionPurged, WP-732).
const actionDocumentPurged = "document.purged"

// NewRetention creates the retention runs on pool.
func NewRetention(pool *pgxpool.Pool, policy RetentionPolicy) *Retention {
	return &Retention{pool: pool, policy: policy, now: func() time.Time { return time.Now().UTC() }}
}

// WithBlobs sets the blob store of the document content, whose objects the
// soft-delete run deletes with the documents (WP-732).
func (r *Retention) WithBlobs(store blob.Store) *Retention {
	r.blobs = store
	return r
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
		{Kind: RetentionMergeSnapshots, Spec: "57 3 * * *", MaxCatchUp: 1, Timeout: time.Hour, Run: r.RunMergeSnapshots},
		{Kind: RetentionBulkChangeSet, Spec: "7 4 * * *", MaxCatchUp: 1, Timeout: time.Hour, Run: r.RunBulkChangeSet},
	}
}

// RunAuditLog removes audit entries older than the audit period.
func (r *Retention) RunAuditLog(ctx context.Context) error {
	cutoff := r.now().AddDate(-r.policy.AuditLogYears, 0, 0)
	return r.eachOrganization(ctx, RetentionAuditLog, false, func(ctx context.Context, tx pgx.Tx, orgID string) (int, error) {
		return audit.Purge(ctx, tx, orgID, cutoff)
	})
}

// historyAnchor is the anchor of the CI of the expression col (AUD-08,
// WP-793): taken_at of its youngest ci_snapshot before the cut-off $2;
// NULL without one.
func historyAnchor(col string) string {
	return `(SELECT max(s.taken_at) FROM ci_snapshot s WHERE s.organization_id = $1 AND s.ci_id = ` + col + ` AND s.taken_at < $2)`
}

// assetHistoryAnchor is the anchor of the asset of the expression col
// (AUD-08, WP-795): taken_at of its youngest asset_snapshot before the
// cut-off $2; NULL without one.
func assetHistoryAnchor(col string) string {
	return `(SELECT max(s.taken_at) FROM asset_snapshot s WHERE s.organization_id = $1 AND s.asset_id = ` + col + ` AND s.taken_at < $2)`
}

// historyRetention are the deletions of retention.history ($1: the
// organization, $2: the cut-off). The point-in-time state of a CI
// (GET /cis/{id}/as-of) replays its last ci_snapshot and the rows after
// it, so the rows of a CI go only up to its anchor (historyAnchor): the
// ci_change rows of the CI they replay for (COALESCE(origin_ci_id, ci_id),
// a merged victim included) and its entity_change rows up to taken_at, the
// relationship_change rows up to the anchor of each CI whose edges they
// replay (both endpoints and, for an edge rewired by a merge, the
// endpoints before) at their observed_at; without an anchor they stay. The
// snapshots before the cut-off go except the anchor. A row between the
// anchor and the cut-off stays, at most a week longer (the weekly
// snapshot). An asset (GET /assets/{id}/as-of, WP-795) follows the same
// rule with its asset_snapshot anchor (assetHistoryAnchor): its
// entity_change rows go up to taken_at, its older snapshots go. Entity
// changes of other types and location moves go at the cut-off.
var historyRetention = []string{
	`DELETE FROM ci_change c WHERE c.organization_id = $1 AND c.created_at < $2
		AND c.created_at <= ` + historyAnchor("COALESCE(c.origin_ci_id, c.ci_id)"),
	`DELETE FROM entity_change e WHERE e.organization_id = $1 AND e.created_at < $2
		AND CASE e.entity_type
			WHEN 'ci' THEN e.created_at <= ` + historyAnchor("e.entity_id") + `
			WHEN 'asset' THEN e.created_at <= ` + assetHistoryAnchor("e.entity_id") + `
			ELSE true END`,
	`DELETE FROM location_change WHERE organization_id = $1 AND changed_at < $2`,
	// The IPAM history (IPM-08, WP-908): the same period, no anchor.
	`DELETE FROM ipam_change WHERE organization_id = $1 AND changed_at < $2`,
	`DELETE FROM relationship_change c WHERE c.organization_id = $1 AND c.created_at < $2
		AND NOT EXISTS (SELECT 1 FROM (VALUES (c.source_ci_id), (c.target_ci_id),
				(CASE WHEN c.change_kind = 'rewired' THEN (c.before->>'source_ci_id')::uuid END),
				(CASE WHEN c.change_kind = 'rewired' THEN (c.before->>'target_ci_id')::uuid END)) AS e(ci_id)
			WHERE e.ci_id IS NOT NULL AND c.observed_at > COALESCE(` + historyAnchor("e.ci_id") + `, '-infinity'))`,
	`DELETE FROM ci_snapshot s WHERE s.organization_id = $1 AND s.taken_at < ` + historyAnchor("s.ci_id"),
	`DELETE FROM asset_snapshot s WHERE s.organization_id = $1 AND s.taken_at < ` + assetHistoryAnchor("s.asset_id"),
}

// RunHistory removes CI, entity, location, relationship and IPAM history
// (relationship_change, WP-771; ipam_change, WP-908) and CI and asset snapshots older than the
// history period, keeping per CI and asset its anchor snapshot and the
// rows after it (historyRetention, WP-793, WP-795).
func (r *Retention) RunHistory(ctx context.Context) error {
	cutoff := r.now().Add(-r.policy.History)
	return r.eachOrganization(ctx, RetentionHistory, true, func(ctx context.Context, tx pgx.Tx, orgID string) (int, error) {
		total := 0
		for _, q := range historyRetention {
			tag, err := tx.Exec(ctx, q, orgID, cutoff)
			if err != nil {
				return total, fmt.Errorf("delete history: %w", err)
			}
			total += int(tag.RowsAffected())
		}
		return total, nil
	})
}

// RunSoftDeleted removes CIs and documents soft-deleted longer than the
// soft-delete period (default 90 days). CIs go with HardDeleteCI: their IP
// addresses go, the references of assets, assignments, tickets and scans
// are released. A CI that still cannot be deleted stays and is counted as
// kept. Documents go first, outside the transaction of the CIs
// (purgeDocuments); the run entry counts both.
func (r *Retention) RunSoftDeleted(ctx context.Context) error {
	cutoff := r.now().Add(-r.policy.SoftDeleted)
	return r.eachOrganizationAfter(ctx, RetentionSoftDeleted, func(ctx context.Context, orgID string) (int, error) {
		return r.purgeDocuments(ctx, orgID, cutoff)
	}, func(ctx context.Context, tx pgx.Tx, orgID string) (int, error) {
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

// expiredDocument is a soft-deleted document past the period with the
// storage keys of its content.
type expiredDocument struct {
	id   string
	keys []string
}

// purgeDocuments removes the documents of the organization soft-deleted
// before cutoff (MGT-01, WP-732) in three steps without a lock during the
// blob transfer (K20): (1) one transaction reads the documents and the
// keys of their versions (and of the document itself) and ends; (2)
// without an open transaction every key is deleted from the blob store (a
// delete marker in the versioned bucket, NFR-05; the bucket lifecycle rule
// removes the old object versions after the backup period, E-118); (3) a
// document whose keys are all gone is deleted in a transaction of its own
// that checks deleted_at again (versions and links follow by ON DELETE
// CASCADE), lowers organization_usage by the sizes of its versions and
// writes document.purged with source system last. A blob error, or a
// missing blob store, keeps the document with stored content; it counts as
// kept and the next run tries again. It returns the purged documents.
func (r *Retention) purgeDocuments(ctx context.Context, orgID string, cutoff time.Time) (int, error) {
	scope := database.OrgWideScope(orgID, "")
	var expired []expiredDocument
	err := database.WithTenant(ctx, r.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT d.id::text,
			       ARRAY(SELECT k FROM (SELECT v.storage_key AS k FROM document_version v WHERE v.document_id = d.id
			                            UNION SELECT d.storage_key) keys WHERE k <> '' ORDER BY k)
			  FROM document d
			 WHERE d.organization_id = $1 AND d.deleted_at < $2
			 ORDER BY d.deleted_at, d.id`, orgID, cutoff)
		if err != nil {
			return fmt.Errorf("list soft-deleted documents: %w", err)
		}
		expired, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (expiredDocument, error) {
			var d expiredDocument
			scanErr := row.Scan(&d.id, &d.keys)
			return d, scanErr
		})
		if err != nil {
			return fmt.Errorf("list soft-deleted documents: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	purged, kept := 0, 0
	for i := range expired {
		if !r.deleteBlobs(ctx, orgID, &expired[i]) {
			kept++
			continue
		}
		ok, perr := r.purgeDocument(ctx, &scope, orgID, expired[i].id, cutoff)
		if perr != nil {
			return purged, perr
		}
		if ok {
			purged++
		}
	}
	if kept > 0 {
		slog.Info("soft-deleted documents kept: content not deleted", "organization_id", orgID, "kept", kept)
	}
	return purged, nil
}

// deleteBlobs deletes the stored content of d and reports whether all of
// it is gone; an object already missing counts as deleted.
func (r *Retention) deleteBlobs(ctx context.Context, orgID string, d *expiredDocument) bool {
	if len(d.keys) == 0 {
		return true
	}
	if r.blobs == nil {
		return false
	}
	for _, key := range d.keys {
		if err := r.blobs.Delete(ctx, documentBucket, key); err != nil && !errors.Is(err, blob.ErrNotFound) {
			slog.Warn("retention: delete document content", "organization_id", orgID, "document_id", d.id, "error", err)
			return false
		}
	}
	return true
}

// purgeDocument deletes document id when it is still soft-deleted before
// cutoff, releases the sizes of its versions from the usage of the
// organization and writes document.purged; it reports whether it deleted.
func (r *Retention) purgeDocument(ctx context.Context, scope *database.TenantScope, orgID, id string, cutoff time.Time) (bool, error) {
	deleted := false
	err := database.WithTenant(ctx, r.pool, scope, func(ctx context.Context, tx pgx.Tx) error {
		var freed int64
		var versions int
		if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(size), 0), count(*) FROM document_version WHERE document_id = $1`,
			id).Scan(&freed, &versions); err != nil {
			return fmt.Errorf("sum document versions: %w", err)
		}
		var title string
		err := tx.QueryRow(ctx, `DELETE FROM document WHERE id = $1 AND organization_id = $2 AND deleted_at < $3 RETURNING title`,
			id, orgID, cutoff).Scan(&title)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("purge document: %w", err)
		}
		if freed > 0 {
			if _, err = tx.Exec(ctx, `UPDATE organization_usage SET storage_bytes = GREATEST(storage_bytes - $2, 0), updated_at = NOW()
				WHERE organization_id = $1`, orgID, freed); err != nil {
				return fmt.Errorf("release storage usage: %w", err)
			}
		}
		if _, err = audit.NewPGRecorder().Record(ctx, tx, audit.Entry{
			OrganizationID: orgID, ActorType: "system", Action: actionDocumentPurged, ResourceType: "document", ResourceID: id,
			Source: audit.SourceSystem, Changes: map[string]any{"title": title, "versions": versions, "released_bytes": freed},
		}); err != nil {
			return fmt.Errorf("audit %s: %w", actionDocumentPurged, err)
		}
		deleted = true
		return nil
	})
	return deleted && err == nil, err
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

// RunMergeSnapshots ends what a CI merge keeps (CI-12, F-251): the
// snapshot of a merge, with copies of both CIs and of the moved rows
// (contacts among them), is emptied once its unmerge period passed
// (snapshot_until, 30 days); the merge row with its tombstone is deleted
// once tombstone_until (12 months) passed, so GET on the victim no longer
// redirects to the survivor. The periods are those the merge recorded in
// its row. The cutoff is never later than the database clock, by which the
// unmerge decides whether a snapshot expired (an emptied snapshot it took
// for valid would restore nothing). The count is the emptied snapshots
// plus the deleted rows.
func (r *Retention) RunMergeSnapshots(ctx context.Context) error {
	now := r.now()
	return r.eachOrganization(ctx, RetentionMergeSnapshots, true, func(ctx context.Context, tx pgx.Tx, orgID string) (int, error) {
		deleted, err := tx.Exec(ctx, `DELETE FROM ci_merge WHERE organization_id = $1 AND tombstone_until < LEAST($2::timestamptz, now())`, orgID, now)
		if err != nil {
			return 0, fmt.Errorf("delete expired merge tombstones: %w", err)
		}
		emptied, err := tx.Exec(ctx, `UPDATE ci_merge SET snapshot = '{}'::jsonb
			WHERE organization_id = $1 AND snapshot_until < LEAST($2::timestamptz, now()) AND snapshot <> '{}'::jsonb`, orgID, now)
		if err != nil {
			return int(deleted.RowsAffected()), fmt.Errorf("empty expired merge snapshots: %w", err)
		}
		return int(deleted.RowsAffected() + emptied.RowsAffected()), nil
	})
}

// RunBulkChangeSet deletes the change set rows of bulk jobs (bulk_change_set,
// WP-305) created more than BulkChangeSetRetention ago (BLK-02, WP-306);
// their jobs are long past the revert window.
func (r *Retention) RunBulkChangeSet(ctx context.Context) error {
	cutoff := r.now().Add(-BulkChangeSetRetention)
	return r.eachOrganization(ctx, RetentionBulkChangeSet, true, func(ctx context.Context, tx pgx.Tx, orgID string) (int, error) {
		tag, err := tx.Exec(ctx, `DELETE FROM bulk_change_set WHERE organization_id = $1 AND created_at < $2`, orgID, cutoff)
		if err != nil {
			return 0, fmt.Errorf("delete bulk change sets: %w", err)
		}
		return int(tag.RowsAffected()), nil
	})
}

// eachOrganization runs fn for every organization in its own transaction;
// with selfAudit a run that deleted something writes an audit entry in the
// same transaction. A failing organization does not stop the others.
func (r *Retention) eachOrganization(ctx context.Context, kind string, selfAudit bool,
	fn func(ctx context.Context, tx pgx.Tx, orgID string) (int, error)) error {
	return r.runOrganizations(ctx, kind, selfAudit, nil, fn)
}

// eachOrganizationAfter is eachOrganization with self audit that first runs
// before for the organization without an open transaction (K20); the run
// entry counts what both deleted. A failing before does not stop fn.
func (r *Retention) eachOrganizationAfter(ctx context.Context, kind string, before func(ctx context.Context, orgID string) (int, error),
	fn func(ctx context.Context, tx pgx.Tx, orgID string) (int, error)) error {
	return r.runOrganizations(ctx, kind, true, before, fn)
}

// runOrganizations implements eachOrganization and eachOrganizationAfter.
func (r *Retention) runOrganizations(ctx context.Context, kind string, selfAudit bool, before func(ctx context.Context, orgID string) (int, error),
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
		prior := 0
		var beforeErr error
		if before != nil {
			prior, beforeErr = before(ctx, orgID)
		}
		scope := database.OrgWideScope(orgID, "")
		err := database.WithTenant(ctx, r.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
			n, err := fn(ctx, tx, orgID)
			n += prior
			if err != nil || !selfAudit || n == 0 {
				return err
			}
			_, err = audit.NewPGRecorder().Record(ctx, tx, audit.Entry{
				OrganizationID: orgID, ActorType: "system", Action: kind, ResourceType: "retention", Source: audit.SourceSystem,
				Changes: map[string]interface{}{"deleted": n},
			})
			return err
		})
		err = errors.Join(beforeErr, err)
		if err != nil {
			slog.Error("retention run", "kind", kind, "organization_id", orgID, "error", err)
			if firstErr == nil {
				firstErr = fmt.Errorf("%s for %s: %w", kind, orgID, err)
			}
		}
	}
	return firstErr
}
