package export

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
)

// OrgExportCleanup is the system job org.export_cleanup (TLC-05, WP-1151):
// hourly it removes the objects of every full export whose download period
// (24 hours after completion) has ended and marks the export expired: the
// job stays succeeded (the job model has no status expired), its result
// records expired_at, from which GET answers the status expired and the
// download 410. Cancelled and failed exports lose their objects 24 hours
// after their end the same way.
type OrgExportCleanup struct {
	pool   *pgxpool.Pool
	blobs  blob.Store
	bucket string
	now    func() time.Time
}

// NewOrgExportCleanup builds the cleanup on the application pool and the
// object storage.
func NewOrgExportCleanup(pool *pgxpool.Pool, blobs blob.Store) *OrgExportCleanup {
	return &OrgExportCleanup{pool: pool, blobs: blobs, bucket: ExportBucket, now: time.Now}
}

// WithClock sets the clock of the cleanup (tests).
func (c *OrgExportCleanup) WithClock(now func() time.Time) *OrgExportCleanup {
	c.now = now
	return c
}

// Schedule is the hourly system job.
func (c *OrgExportCleanup) Schedule() jobs.Schedule {
	return jobs.Schedule{Kind: OrgExportCleanupKind, Spec: "23 * * * *", MaxCatchUp: 1, Timeout: 30 * time.Minute, Run: c.Run}
}

// expiredExport is an export whose download period ended.
type expiredExport struct {
	id     string
	status string
	result []byte
}

// Run removes the objects of the expired exports of every organization; it
// returns the first error after trying all organizations.
func (c *OrgExportCleanup) Run(ctx context.Context) error {
	if c.pool == nil || c.blobs == nil {
		return nil
	}
	orgs, err := database.OrganizationIDs(ctx, c.pool)
	if err != nil {
		return fmt.Errorf("org export cleanup: list organizations: %w", err)
	}
	var first error
	for _, orgID := range orgs {
		if _, err := c.RunOrg(ctx, orgID); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// RunOrg removes the objects of the expired exports of one organization
// and returns how many it marked expired. Besides the completed exports it
// takes the cancelled and failed ones that ended 24 hours ago: a cancel
// after the last check of the run leaves the objects the job wrote. Their
// keys come from manifest.json in the store, which the exporter writes
// before every part; so do those of a completed export whose result cannot
// be decoded. An export whose manifest cannot be read keeps no expired_at:
// the next run tries again and the error is returned.
func (c *OrgExportCleanup) RunOrg(ctx context.Context, orgID string) (int, error) {
	now := c.now().UTC()
	scope := database.OrgWideScope(orgID, "")
	var due []expiredExport
	err := database.WithTenant(ctx, c.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, status, COALESCE(result, '{}'::jsonb) FROM job
			WHERE organization_id = $1 AND kind = $2 AND status IN ('succeeded', 'cancelled', 'failed')
			  AND COALESCE(finished_at, updated_at) <= $3
			  AND COALESCE(result->>'expired_at', '') = ''
			ORDER BY COALESCE(finished_at, updated_at)`, orgID, OrgExportJobKind, now.Add(-OrgExportTTL))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e expiredExport
			if err := rows.Scan(&e.id, &e.status, &e.result); err != nil {
				return err
			}
			due = append(due, e)
		}
		return rows.Err()
	})
	if err != nil {
		return 0, fmt.Errorf("org export cleanup: read expired exports: %w", err)
	}
	done := 0
	var first error
	for i := range due {
		e := &due[i]
		m, err := c.manifestOf(ctx, orgID, e)
		if err == nil {
			err = c.remove(ctx, m)
		}
		if err != nil {
			slog.ErrorContext(ctx, "org export cleanup: remove objects", "export", e.id, "organization_id", orgID, "error", err)
			if first == nil {
				first = fmt.Errorf("org export cleanup: export %s: %w", e.id, err)
			}
			continue
		}
		err = database.WithTenant(ctx, c.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
			_, xerr := tx.Exec(ctx, `UPDATE job SET result = COALESCE(result, '{}'::jsonb) || jsonb_build_object('expired_at', $3::timestamptz)
				WHERE organization_id = $1 AND id = $2`, orgID, e.id, now)
			return xerr
		})
		if err != nil {
			return done, fmt.Errorf("org export cleanup: mark expired: %w", err)
		}
		done++
	}
	return done, first
}

// manifestOf returns the manifest whose objects the cleanup removes: the
// result of a completed export when it names objects, else manifest.json
// in the store (an export
// without one wrote no object). The keys lie below the job of the row,
// whatever the manifest says.
func (c *OrgExportCleanup) manifestOf(ctx context.Context, orgID string, e *expiredExport) (*OrgExportManifest, error) {
	var m OrgExportManifest
	fromResult := false
	if e.status == string(jobs.StatusSucceeded) {
		if err := json.Unmarshal(e.result, &m); err != nil {
			slog.WarnContext(ctx, "org export cleanup: decode result, reading the stored manifest", "export", e.id, "error", err)
		} else {
			// A result naming no object is no manifest of the export.
			fromResult = len(m.keys()) > 0
		}
	}
	if !fromResult {
		stored, err := (&OrgExporter{blobs: c.blobs, bucket: c.bucket}).loadManifest(ctx, orgID, e.id)
		if err != nil {
			return nil, fmt.Errorf("read the stored manifest: %w", err)
		}
		if stored != nil {
			m = *stored
		}
	}
	m.OrganizationID, m.JobID = orgID, e.id
	return &m, nil
}

// remove deletes every object the manifest names and the manifest; a key
// outside the export is not touched.
func (c *OrgExportCleanup) remove(ctx context.Context, m *OrgExportManifest) error {
	prefix := OrgExportPrefix(m.OrganizationID, m.JobID) + "/"
	for _, key := range append(m.keys(), OrgExportManifestKey(m.OrganizationID, m.JobID)) {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if err := c.blobs.Delete(ctx, c.bucket, key); err != nil && !errors.Is(err, blob.ErrNotFound) {
			return err
		}
	}
	return nil
}
