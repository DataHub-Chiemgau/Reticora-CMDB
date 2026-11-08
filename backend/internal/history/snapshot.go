package history

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CI snapshots (AUD-08, WP-312): ci_snapshot holds the state of a CI at
// taken_at, the base of its replay (TrailUpTo). The weekly system job
// writes one for every CI whose trail or edges changed since its last
// snapshot; the merge and the unmerge write one for both CIs from their
// live rows, because they do not historize the rows they change field by
// field.
//
// Asset snapshots (AUD-08, WP-795): asset_snapshot holds the state of an
// asset at taken_at, the base of its replay (assetTrail). The weekly job
// writes them under the same rules as for CIs, for every asset whose
// entity_change rows changed since its last snapshot, but only while the
// module inventory is enabled (WithAssets).

// WeeklySnapshotKind is the scheduler kind of the weekly CI snapshot.
const WeeklySnapshotKind = "history.weekly_snapshot"

// SnapshotSchemaVersion is the format version of ci_snapshot.state
// (snapshotDocument).
const SnapshotSchemaVersion = 1

// Reasons of a ci_snapshot (CHECK ci_snapshot_reason_check).
const (
	SnapshotReasonWeekly  = "weekly"
	SnapshotReasonMerge   = "merge"
	SnapshotReasonUnmerge = "unmerge"
	SnapshotReasonInitial = "initial"
)

// weeklySnapshotBatch bounds the CIs one transaction of the weekly run
// handles.
const weeklySnapshotBatch = 500

// weeklySnapshotLag is how long before the start of a run its snapshots are
// taken: a write whose transaction began before taken_at and commits during
// the run has created_at (now() of the writer) before taken_at and is part
// of the snapshot, not lost behind it.
const weeklySnapshotLag = time.Hour

// insertSnapshot writes a ci_snapshot of the CI ciID at takenAt (nil: now()
// of tx) with its current edges; the scope trigger sets client and site.
func insertSnapshot(ctx context.Context, tx pgx.Tx, orgID, ciID, reason string, takenAt *time.Time, doc *snapshotDocument, ciVersion *int64) error {
	state, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("encode ci snapshot: %w", err)
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO ci_snapshot (organization_id, ci_id, taken_at, reason, state, relationships, schema_version, ci_version)
		VALUES ($1, $2::uuid, COALESCE($3::timestamptz, now()), $4, $5::jsonb, `+relationshipsOf("$2::uuid")+`, $6, $7)`,
		orgID, ciID, takenAt, reason, state, SnapshotSchemaVersion, ciVersion); err != nil {
		return fmt.Errorf("write ci snapshot: %w", err)
	}
	return nil
}

// WriteLiveSnapshotTx writes a ci_snapshot of the CI ciID from its live
// row in the writer's transaction tx, taken at its now(): item is the CI
// in the form its creation records (ci.Item), version its version, deleted
// and mergedIntoID its deletion and the survivor of a merge. The personal
// values are sealed with pii.SealTx (SEC-05). The merge and the unmerge
// call it for both CIs (reasons merge and unmerge, CI-12), since they
// write the rows back without field rows in ci_change.
func WriteLiveSnapshotTx(ctx context.Context, tx pgx.Tx, orgID, ciID, reason string, item any, version int64, deleted bool, mergedIntoID string) error {
	sealed, _, err := pii.SealTx(ctx, tx, orgID, item)
	if err != nil {
		return fmt.Errorf("seal ci snapshot: %w", err)
	}
	fields, _ := sealed.(map[string]any)
	if fields == nil {
		return fmt.Errorf("ci snapshot of %s: the live row is no object", ciID)
	}
	doc := snapshotDocument{Fields: fields, Deleted: deleted, MergedIntoID: mergedIntoID}
	if deleted && mergedIntoID == "" {
		if doc.Restore, err = lastDeletion(ctx, tx, orgID, ciID); err != nil {
			return err
		}
	}
	return insertSnapshot(ctx, tx, orgID, ciID, reason, nil, &doc, &version)
}

// lastDeletion reads what a restore of the deleted CI brings back: the
// status and the released asset link of its last delete row, as
// ci.PGRepository.restore reads them; nil without one.
func lastDeletion(ctx context.Context, tx pgx.Tx, orgID, ciID string) (*restoreState, error) {
	r := &restoreState{}
	err := tx.QueryRow(ctx, `SELECT COALESCE(old_value->>'status', ''), COALESCE(new_value->>'unlinked_asset_id', '')
		FROM ci_change WHERE organization_id = $1 AND ((ci_id = $2::uuid AND origin_ci_id IS NULL) OR origin_ci_id = $2::uuid)
			AND change_type = 'delete' AND field_name IS NULL
		ORDER BY created_at DESC LIMIT 1`, orgID, ciID).Scan(&r.Status, &r.AssetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read deletion: %w", err)
	}
	return r, nil
}

// WeeklySnapshot is the system job history.weekly_snapshot (AUD-08,
// WP-312): every Sunday it writes a ci_snapshot of each CI whose trail or
// edges changed since its last snapshot, so a replay reads at most a week
// of ci_change rows.
type WeeklySnapshot struct {
	pool *pgxpool.Pool
	now  func() time.Time
	// assets: the module inventory is enabled, the run snapshots the
	// assets too (WithAssets).
	assets bool
}

// NewWeeklySnapshot creates the weekly CI snapshot job.
func NewWeeklySnapshot(pool *pgxpool.Pool) *WeeklySnapshot {
	return &WeeklySnapshot{pool: pool, now: func() time.Time { return time.Now().UTC() }}
}

// WithClock replaces the clock (tests).
func (w *WeeklySnapshot) WithClock(now func() time.Time) *WeeklySnapshot {
	w.now = now
	return w
}

// WithAssets makes the run snapshot the assets as well (asset_snapshot,
// WP-795): systemJobs passes whether the module inventory is enabled, to
// which /api/v1/assets belongs.
func (w *WeeklySnapshot) WithAssets(enabled bool) *WeeklySnapshot {
	w.assets = enabled
	return w
}

// Schedule registers the run on Sundays at 02:07.
func (w *WeeklySnapshot) Schedule() jobs.Schedule {
	return jobs.Schedule{Kind: WeeklySnapshotKind, Spec: "7 2 * * 0", MaxCatchUp: 1, Timeout: 2 * time.Hour,
		Run: func(ctx context.Context) error {
			_, err := w.Run(ctx)
			return err
		}}
}

// Run snapshots every organization and returns the number of snapshots
// written. The organizations are read as the system; a failing
// organization does not stop the others, the errors are joined at the end.
func (w *WeeklySnapshot) Run(ctx context.Context) (int, error) {
	var orgs []string
	err := database.WithSystem(ctx, w.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM organization ORDER BY id`)
		if err != nil {
			return err
		}
		orgs, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("%s: list organizations: %w", WeeklySnapshotKind, err)
	}
	total := 0
	var errs []error
	for _, orgID := range orgs {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		n, oerr := w.RunOrg(ctx, orgID)
		total += n
		if oerr != nil {
			slog.Error("weekly ci snapshot", "organization_id", orgID, "error", oerr)
			errs = append(errs, fmt.Errorf("%s for %s: %w", WeeklySnapshotKind, orgID, oerr))
		}
	}
	return total, errors.Join(errs...)
}

// snapshotCandidate is a CI of a batch with what decides its snapshot.
type snapshotCandidate struct {
	id string
	// last is taken_at of its last snapshot; nil without one.
	last *time.Time
	// changed: ci_change rows after the last snapshot up to the cut-off,
	// or edges other than those of the last snapshot.
	changed bool
	// created: a creation row; first: the time of its first row.
	created bool
	first   *time.Time
}

// RunOrg snapshots the CIs of one organization (runCIs) and, with
// WithAssets, its assets (runAssets) and returns the number of snapshots
// written.
func (w *WeeklySnapshot) RunOrg(ctx context.Context, orgID string) (int, error) {
	cutoff := w.now().Add(-weeklySnapshotLag)
	written, err := w.runCIs(ctx, orgID, cutoff)
	if err != nil || !w.assets {
		return written, err
	}
	assets, err := w.runAssets(ctx, orgID, cutoff)
	return written + assets, err
}

// runCIs snapshots the CIs of one organization in batches of at most
// weeklySnapshotBatch CIs, each batch in its own transaction with the
// organization-wide scope. The snapshots are taken an hour before the start
// of the run (weeklySnapshotLag); their state is the replay of the sealed
// trail up to then (the last snapshot and the ci_change rows after it), so
// it keeps the surrogates of the trail. A CI without a snapshot whose first
// row lies after the cut-off is left to the next run; one without a
// snapshot and without a creation row is snapshotted from its live row
// (reason initial, taken at now()).
func (w *WeeklySnapshot) runCIs(ctx context.Context, orgID string, cutoff time.Time) (int, error) {
	written := 0
	after := "00000000-0000-0000-0000-000000000000"
	for {
		var batch int
		scope := database.OrgWideScope(orgID, "")
		err := database.WithTenant(ctx, w.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
			candidates, err := snapshotCandidates(ctx, tx, orgID, after, cutoff)
			if err != nil {
				return err
			}
			batch = len(candidates)
			for i := range candidates {
				c := &candidates[i]
				after = c.id
				wrote, serr := snapshotCI(ctx, tx, orgID, c, cutoff)
				if serr != nil {
					return fmt.Errorf("ci %s: %w", c.id, serr)
				}
				if wrote {
					written++
				}
			}
			return nil
		})
		if err != nil {
			return written, err
		}
		if batch < weeklySnapshotBatch {
			return written, nil
		}
	}
}

// snapshotCandidates reads the next batch of CIs after the id after, with
// their last snapshot and whether their trail ($3: the cut-off) or edges
// changed since.
func snapshotCandidates(ctx context.Context, tx pgx.Tx, orgID, after string, cutoff time.Time) ([]snapshotCandidate, error) {
	rows, err := tx.Query(ctx, `
		WITH batch AS (
			SELECT c.id FROM ci c WHERE c.organization_id = $1 AND c.id > $2::uuid ORDER BY c.id LIMIT $4)
		SELECT b.id::text, s.taken_at,
		       EXISTS (SELECT 1 FROM ci_change ch WHERE ch.organization_id = $1
		                  AND ((ch.ci_id = b.id AND ch.origin_ci_id IS NULL) OR ch.origin_ci_id = b.id)
		                  AND ch.created_at > COALESCE(s.taken_at, '-infinity') AND ch.created_at <= $3)
		           OR (s.taken_at IS NOT NULL AND `+relationshipsOf("b.id")+` IS DISTINCT FROM s.relationships),
		       EXISTS (SELECT 1 FROM ci_change ch WHERE ch.organization_id = $1
		                  AND ((ch.ci_id = b.id AND ch.origin_ci_id IS NULL) OR ch.origin_ci_id = b.id) AND ch.change_type = 'create'),
		       (SELECT min(ch.created_at) FROM ci_change ch WHERE ch.organization_id = $1
		                  AND ((ch.ci_id = b.id AND ch.origin_ci_id IS NULL) OR ch.origin_ci_id = b.id))
		FROM batch b
		LEFT JOIN LATERAL (SELECT taken_at, relationships FROM ci_snapshot
			WHERE organization_id = $1 AND ci_id = b.id ORDER BY taken_at DESC LIMIT 1) s ON true
		ORDER BY b.id`, orgID, after, cutoff, weeklySnapshotBatch)
	if err != nil {
		return nil, fmt.Errorf("read snapshot candidates: %w", err)
	}
	defer rows.Close()
	var out []snapshotCandidate
	for rows.Next() {
		var c snapshotCandidate
		if err := rows.Scan(&c.id, &c.last, &c.changed, &c.created, &c.first); err != nil {
			return nil, fmt.Errorf("read snapshot candidates: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// relationshipsOf is the edge set of the CI of the expression col in the
// organization $1 as a snapshot keeps it: id, type and both endpoints with
// their client and site, ordered by id; no edge is an empty array.
func relationshipsOf(col string) string {
	return `COALESCE((
	SELECT jsonb_agg(jsonb_build_object('id', r.id, 'rel_type', r.rel_type,
		'source_ci_id', r.source_ci_id, 'target_ci_id', r.target_ci_id,
		'source_client_id', r.source_client_id, 'source_site_id', r.source_site_id,
		'target_client_id', r.target_client_id, 'target_site_id', r.target_site_id) ORDER BY r.id)
	FROM ci_relationship r WHERE r.organization_id = $1 AND (r.source_ci_id = ` + col + ` OR r.target_ci_id = ` + col + `)), '[]'::jsonb)`
}

// snapshotCI writes the weekly snapshot of one candidate, if it needs one,
// and reports whether it did.
func snapshotCI(ctx context.Context, tx pgx.Tx, orgID string, c *snapshotCandidate, cutoff time.Time) (bool, error) {
	if c.last == nil && !c.created {
		return true, initialSnapshot(ctx, tx, orgID, c.id)
	}
	if c.last == nil && (c.first == nil || c.first.After(cutoff)) {
		return false, nil
	}
	if c.last != nil && !c.changed {
		return false, nil
	}
	// A snapshot taken after the cut-off (merge, unmerge, initial) is the
	// base; the new one is not taken before it.
	at := cutoff
	if c.last != nil && c.last.After(at) {
		at = *c.last
	}
	trail, err := ciTrailTx(ctx, tx, orgID, c.id, at)
	if err != nil {
		return false, err
	}
	if len(trail) == 0 {
		return false, nil
	}
	state := Replay(trail)
	var version *int64
	for i := range trail {
		if trail[i].ciVersion != nil {
			version = trail[i].ciVersion
		}
	}
	doc := state.document()
	return true, insertSnapshot(ctx, tx, orgID, c.id, SnapshotReasonWeekly, &at, &doc, version)
}

// initialSnapshot writes the first snapshot of a CI without a recorded
// creation from its live row (reason initial): its stored columns without
// the derived search columns, the personal values sealed.
func initialSnapshot(ctx context.Context, tx pgx.Tx, orgID, ciID string) error {
	var raw []byte
	var version int64
	var deleted bool
	var mergedInto string
	if err := tx.QueryRow(ctx, `SELECT jsonb_strip_nulls(to_jsonb(c) - 'hostname_lc' - 'fqdn_lc' - 'serial_number_lc' - 'warranty_days_left_num'),
			c.version, c.deleted_at IS NOT NULL, COALESCE(c.merged_into_id::text, '')
		FROM ci c WHERE c.organization_id = $1 AND c.id = $2::uuid`, orgID, ciID).Scan(&raw, &version, &deleted, &mergedInto); err != nil {
		return fmt.Errorf("read ci: %w", err)
	}
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		return fmt.Errorf("decode ci: %w", err)
	}
	return WriteLiveSnapshotTx(ctx, tx, orgID, ciID, SnapshotReasonInitial, row, version, deleted, mergedInto)
}

// insertAssetSnapshot writes an asset_snapshot of the asset assetID at
// takenAt (nil: now() of tx); the scope trigger sets client and site.
func insertAssetSnapshot(ctx context.Context, tx pgx.Tx, orgID, assetID, reason string, takenAt *time.Time, doc *snapshotDocument) error {
	state, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("encode asset snapshot: %w", err)
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO asset_snapshot (organization_id, asset_id, taken_at, reason, state, schema_version)
		VALUES ($1, $2::uuid, COALESCE($3::timestamptz, now()), $4, $5::jsonb, $6)`,
		orgID, assetID, takenAt, reason, state, SnapshotSchemaVersion); err != nil {
		return fmt.Errorf("write asset snapshot: %w", err)
	}
	return nil
}

// runAssets snapshots the assets of one organization like runCIs: in
// batches of at most weeklySnapshotBatch assets, each in its own
// transaction with the organization-wide scope, taken at the cut-off from
// the replay of the sealed trail (the last asset_snapshot and the
// entity_change rows after it). An asset without a snapshot and without a
// creation row is snapshotted from its live row (reason initial).
func (w *WeeklySnapshot) runAssets(ctx context.Context, orgID string, cutoff time.Time) (int, error) {
	written := 0
	after := "00000000-0000-0000-0000-000000000000"
	for {
		var batch int
		scope := database.OrgWideScope(orgID, "")
		err := database.WithTenant(ctx, w.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
			candidates, err := assetSnapshotCandidates(ctx, tx, orgID, after, cutoff)
			if err != nil {
				return err
			}
			batch = len(candidates)
			for i := range candidates {
				c := &candidates[i]
				after = c.id
				wrote, serr := snapshotAsset(ctx, tx, orgID, c, cutoff)
				if serr != nil {
					return fmt.Errorf("asset %s: %w", c.id, serr)
				}
				if wrote {
					written++
				}
			}
			return nil
		})
		if err != nil {
			return written, err
		}
		if batch < weeklySnapshotBatch {
			return written, nil
		}
	}
}

// assetSnapshotCandidates reads the next batch of assets after the id
// after, with their last snapshot and whether their trail ($3: the
// cut-off) changed since.
func assetSnapshotCandidates(ctx context.Context, tx pgx.Tx, orgID, after string, cutoff time.Time) ([]snapshotCandidate, error) {
	rows, err := tx.Query(ctx, `
		WITH batch AS (
			SELECT a.id FROM asset a WHERE a.organization_id = $1 AND a.id > $2::uuid ORDER BY a.id LIMIT $4)
		SELECT b.id::text, s.taken_at,
		       EXISTS (SELECT 1 FROM entity_change e WHERE e.organization_id = $1 AND e.entity_type = 'asset' AND e.entity_id = b.id
		                  AND e.created_at > COALESCE(s.taken_at, '-infinity') AND e.created_at <= $3),
		       EXISTS (SELECT 1 FROM entity_change e WHERE e.organization_id = $1 AND e.entity_type = 'asset' AND e.entity_id = b.id
		                  AND e.change_type = 'create'),
		       (SELECT min(e.created_at) FROM entity_change e WHERE e.organization_id = $1 AND e.entity_type = 'asset' AND e.entity_id = b.id)
		FROM batch b
		LEFT JOIN LATERAL (SELECT taken_at FROM asset_snapshot
			WHERE organization_id = $1 AND asset_id = b.id ORDER BY taken_at DESC LIMIT 1) s ON true
		ORDER BY b.id`, orgID, after, cutoff, weeklySnapshotBatch)
	if err != nil {
		return nil, fmt.Errorf("read asset snapshot candidates: %w", err)
	}
	defer rows.Close()
	var out []snapshotCandidate
	for rows.Next() {
		var c snapshotCandidate
		if err := rows.Scan(&c.id, &c.last, &c.changed, &c.created, &c.first); err != nil {
			return nil, fmt.Errorf("read asset snapshot candidates: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// snapshotAsset writes the weekly snapshot of one asset candidate, if it
// needs one, and reports whether it did; the rules are those of
// snapshotCI.
func snapshotAsset(ctx context.Context, tx pgx.Tx, orgID string, c *snapshotCandidate, cutoff time.Time) (bool, error) {
	if c.last == nil && !c.created {
		return true, initialAssetSnapshot(ctx, tx, orgID, c.id)
	}
	if c.last == nil && (c.first == nil || c.first.After(cutoff)) {
		return false, nil
	}
	if c.last != nil && !c.changed {
		return false, nil
	}
	at := cutoff
	if c.last != nil && c.last.After(at) {
		at = *c.last
	}
	trail, err := assetTrailTx(ctx, tx, orgID, c.id, at)
	if err != nil {
		return false, err
	}
	if len(trail) == 0 {
		return false, nil
	}
	state := Replay(trail)
	doc := state.document()
	return true, insertAssetSnapshot(ctx, tx, orgID, c.id, SnapshotReasonWeekly, &at, &doc)
}

// initialAssetSnapshot writes the first snapshot of an asset without a
// recorded creation from its live row (reason initial), taken at now():
// its typed columns in the form of the trail (asset_history_form, WP-794),
// its custom fields and its tags, the personal values sealed with
// pii.SealTx (SEC-05).
func initialAssetSnapshot(ctx context.Context, tx pgx.Tx, orgID, assetID string) error {
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT jsonb_strip_nulls(asset_history_form(a)
			|| jsonb_build_object('custom_fields', COALESCE(a.custom_fields, '{}'::jsonb),
				'tags', COALESCE((SELECT jsonb_agg(t.key ORDER BY t.key) FROM asset_tag_link l JOIN tag t ON t.id = l.tag_id
					WHERE l.asset_id = a.id), '[]'::jsonb)))
		FROM asset a WHERE a.organization_id = $1 AND a.id = $2::uuid`, orgID, assetID).Scan(&raw); err != nil {
		return fmt.Errorf("read asset: %w", err)
	}
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		return fmt.Errorf("decode asset: %w", err)
	}
	sealed, _, err := pii.SealTx(ctx, tx, orgID, row)
	if err != nil {
		return fmt.Errorf("seal asset snapshot: %w", err)
	}
	fields, _ := sealed.(map[string]any)
	if fields == nil {
		return fmt.Errorf("asset snapshot of %s: the live row is no object", assetID)
	}
	return insertAssetSnapshot(ctx, tx, orgID, assetID, SnapshotReasonInitial, nil, &snapshotDocument{Fields: fields})
}
