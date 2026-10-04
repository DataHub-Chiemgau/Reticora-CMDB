package export

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const jobColumns = `id::text, organization_id::text, COALESCE(initiated_by::text, ''), format, status, filters,
	COALESCE(object_key, ''), row_count, file_size_bytes, COALESCE(error_message, ''),
	started_at, completed_at, expires_at, created_at, updated_at,
	scope_recorded, scope_clients::text[], scope_sites::text[], scope_teams::text[]`

// ownJob restricts request paths to the jobs of the principal: the user id
// comes from the transaction's tenant scope, not from the request. It is
// compared as text, so a principal without a user id (or one that is no
// uuid) sees no job instead of failing.
const ownJob = `initiated_by::text = NULLIF(current_setting('app.user_id', true), '')`

// PGJobRepository implements JobRepository on PostgreSQL with RLS. Request
// paths run in database.WithTenant with the principal's scope; the worker
// finds pending jobs with the read-only system flag and claims them per
// organization, mirroring webhook.PGDeliveryStore.ClaimDue.
type PGJobRepository struct{ pool *pgxpool.Pool }

// NewPGJobRepository creates a PostgreSQL-backed export job repository.
func NewPGJobRepository(pool *pgxpool.Pool) *PGJobRepository {
	return &PGJobRepository{pool: pool}
}

// workerScope is the scope of the export worker's state changes (claim,
// complete, fail). The worker runs outside a request on behalf of the job's
// organization (E-08); the export itself runs with the creator's scope
// snapshot (JobWorker.process).
func workerScope(orgID string) *database.TenantScope {
	scope := database.OrgWideScope(orgID, "")
	return &scope
}

func scanJob(row pgx.Row) (*Job, error) {
	var job Job
	var filters []byte
	var recorded bool
	var scope JobScope
	err := row.Scan(&job.ID, &job.OrganizationID, &job.InitiatedBy, &job.Format, &job.Status,
		&filters, &job.ObjectKey, &job.RowCount, &job.FileSizeBytes, &job.ErrorMessage,
		&job.StartedAt, &job.CompletedAt, &job.ExpiresAt, &job.CreatedAt, &job.UpdatedAt,
		&recorded, &scope.Clients, &scope.Sites, &scope.Teams)
	if err != nil {
		return nil, err
	}
	if recorded {
		job.Scope = &scope
	}
	if len(filters) > 0 {
		if err := json.Unmarshal(filters, &job.Filters); err != nil {
			return nil, fmt.Errorf("decode export job filters: %w", err)
		}
	}
	return &job, nil
}

func (r *PGJobRepository) CreateJob(ctx context.Context, orgID string, job *Job) error {
	filters, err := json.Marshal(job.Filters)
	if err != nil {
		return fmt.Errorf("encode export job filters: %w", err)
	}
	if job.Scope == nil {
		return fmt.Errorf("create export job: missing scope snapshot")
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO export_job (organization_id, initiated_by, format, status, filters,
				scope_recorded, scope_clients, scope_sites, scope_teams)
			VALUES (current_setting('app.org_id')::uuid, NULLIF($1, '')::uuid, $2, $3, $4,
				true, $5::uuid[], $6::uuid[], $7::uuid[])
			RETURNING `+jobColumns,
			job.InitiatedBy, job.Format, JobStatusPending, filters,
			job.Scope.Clients, job.Scope.Sites, job.Scope.Teams)
		stored, err := scanJob(row)
		if err != nil {
			return fmt.Errorf("create export job: %w", err)
		}
		*job = *stored
		return nil
	})
}

func (r *PGJobRepository) GetJob(ctx context.Context, orgID, id string) (*Job, error) {
	var job *Job
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		job, err = scanJob(tx.QueryRow(ctx,
			"SELECT "+jobColumns+" FROM export_job WHERE organization_id = current_setting('app.org_id')::uuid AND "+ownJob+" AND id = $1", id))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("export job not found")
		}
		return err
	})
	return job, err
}

func (r *PGJobRepository) ListJobs(ctx context.Context, orgID string, limit, offset int) ([]Job, int, error) {
	var out []Job
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			"SELECT COUNT(*) FROM export_job WHERE organization_id = current_setting('app.org_id')::uuid AND "+ownJob).Scan(&total); err != nil {
			return fmt.Errorf("count export jobs: %w", err)
		}
		rows, err := tx.Query(ctx,
			"SELECT "+jobColumns+" FROM export_job WHERE organization_id = current_setting('app.org_id')::uuid AND "+ownJob+" ORDER BY created_at DESC, id DESC LIMIT $1 OFFSET $2",
			limit, offset)
		if err != nil {
			return fmt.Errorf("list export jobs: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			job, err := scanJob(rows)
			if err != nil {
				return err
			}
			out = append(out, *job)
		}
		return rows.Err()
	})
	return out, total, err
}

func (r *PGJobRepository) MarkRunning(ctx context.Context, orgID, id string, at time.Time) error {
	return database.WithTenant(ctx, r.pool, workerScope(orgID), func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE export_job SET status = $1, started_at = $2, updated_at = $2
			WHERE organization_id = current_setting('app.org_id')::uuid AND id = $3 AND status = $4`,
			JobStatusRunning, at.UTC(), id, JobStatusPending)
		if err != nil {
			return fmt.Errorf("mark export job running: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("export job is not pending")
		}
		return nil
	})
}

func (r *PGJobRepository) CompleteJob(ctx context.Context, orgID, id string, objectKey string, rowCount int, fileSize int64, completedAt, expiresAt time.Time) error {
	return database.WithTenant(ctx, r.pool, workerScope(orgID), func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE export_job SET status = $1, object_key = $2, row_count = $3, file_size_bytes = $4,
				completed_at = $5, expires_at = $6, updated_at = $5
			WHERE organization_id = current_setting('app.org_id')::uuid AND id = $7`,
			JobStatusCompleted, objectKey, rowCount, fileSize, completedAt.UTC(), expiresAt.UTC(), id)
		if err != nil {
			return fmt.Errorf("complete export job: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("export job not found")
		}
		return nil
	})
}

func (r *PGJobRepository) FailJob(ctx context.Context, orgID, id, message string, at time.Time) error {
	return database.WithTenant(ctx, r.pool, workerScope(orgID), func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE export_job SET status = $1, error_message = $2, completed_at = $3, updated_at = $3
			WHERE organization_id = current_setting('app.org_id')::uuid AND id = $4`,
			JobStatusFailed, message, at.UTC(), id)
		if err != nil {
			return fmt.Errorf("fail export job: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("export job not found")
		}
		return nil
	})
}

// ClaimPending marks up to limit pending jobs across all tenants as running
// and returns them oldest first; it is used exclusively by the export worker.
// The system flag only finds the organizations with pending jobs; each
// organization's jobs are claimed in its own tenant transaction, because the
// system flag cannot write (WP-022).
func (r *PGJobRepository) ClaimPending(ctx context.Context, limit int) ([]Job, error) {
	var orgIDs []string
	err := database.WithSystem(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT organization_id::text FROM export_job WHERE status = $1
			GROUP BY organization_id ORDER BY min(created_at)`, JobStatusPending)
		if err != nil {
			return fmt.Errorf("find pending export jobs: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return fmt.Errorf("scan organization: %w", err)
			}
			orgIDs = append(orgIDs, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}

	var out []Job
	for _, orgID := range orgIDs {
		if len(out) >= limit {
			break
		}
		claimed, err := r.claimPendingForOrg(ctx, orgID, limit-len(out))
		if err != nil {
			return out, err
		}
		out = append(out, claimed...)
	}
	return out, nil
}

func (r *PGJobRepository) claimPendingForOrg(ctx context.Context, orgID string, limit int) ([]Job, error) {
	var out []Job
	err := database.WithTenant(ctx, r.pool, workerScope(orgID), func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			WITH due AS (
				SELECT id AS job_id FROM export_job WHERE status = $1 ORDER BY created_at ASC, id ASC LIMIT $2
				FOR UPDATE SKIP LOCKED
			)
			UPDATE export_job SET status = $3, started_at = now(), updated_at = now()
			FROM due WHERE export_job.id = due.job_id
			RETURNING `+jobColumns, JobStatusPending, limit, JobStatusRunning)
		if err != nil {
			return fmt.Errorf("claim pending export jobs: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			job, err := scanJob(rows)
			if err != nil {
				return err
			}
			out = append(out, *job)
		}
		return rows.Err()
	})
	return out, err
}
