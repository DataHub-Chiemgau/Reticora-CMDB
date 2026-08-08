package export

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const jobColumns = `id::text, organization_id::text, initiated_by::text, format, status, filters,
	COALESCE(object_key, ''), row_count, file_size_bytes, COALESCE(error_message, ''),
	started_at, completed_at, expires_at, created_at, updated_at`

// PGJobRepository implements JobRepository on PostgreSQL with RLS. The
// worker's cross-tenant claim is the single exception and uses the
// `app.system` flag, mirroring webhook.PGDeliveryStore.ClaimDue; request
// paths always run with `app.org_id` set.
type PGJobRepository struct{ pool *pgxpool.Pool }

// NewPGJobRepository creates a PostgreSQL-backed export job repository.
func NewPGJobRepository(pool *pgxpool.Pool) *PGJobRepository {
	return &PGJobRepository{pool: pool}
}

func (r *PGJobRepository) inTx(ctx context.Context, setting, value string, fn func(context.Context, pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config($1, $2, true)", setting, value); err != nil {
		return fmt.Errorf("set session context: %w", err)
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func scanJob(row pgx.Row) (*Job, error) {
	var job Job
	var filters []byte
	err := row.Scan(&job.ID, &job.OrganizationID, &job.InitiatedBy, &job.Format, &job.Status,
		&filters, &job.ObjectKey, &job.RowCount, &job.FileSizeBytes, &job.ErrorMessage,
		&job.StartedAt, &job.CompletedAt, &job.ExpiresAt, &job.CreatedAt, &job.UpdatedAt)
	if err != nil {
		return nil, err
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
	return r.inTx(ctx, "app.org_id", orgID, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			INSERT INTO export_job (organization_id, initiated_by, format, status, filters)
			VALUES (current_setting('app.org_id')::uuid, NULLIF($1, '')::uuid, $2, $3, $4)
			RETURNING `+jobColumns,
			job.InitiatedBy, job.Format, JobStatusPending, filters)
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
	err := r.inTx(ctx, "app.org_id", orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		job, err = scanJob(tx.QueryRow(ctx,
			"SELECT "+jobColumns+" FROM export_job WHERE organization_id = current_setting('app.org_id')::uuid AND id = $1", id))
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
	err := r.inTx(ctx, "app.org_id", orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			"SELECT COUNT(*) FROM export_job WHERE organization_id = current_setting('app.org_id')::uuid").Scan(&total); err != nil {
			return fmt.Errorf("count export jobs: %w", err)
		}
		rows, err := tx.Query(ctx,
			"SELECT "+jobColumns+" FROM export_job WHERE organization_id = current_setting('app.org_id')::uuid ORDER BY created_at DESC, id DESC LIMIT $1 OFFSET $2",
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
	return r.inTx(ctx, "app.org_id", orgID, func(ctx context.Context, tx pgx.Tx) error {
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
	return r.inTx(ctx, "app.org_id", orgID, func(ctx context.Context, tx pgx.Tx) error {
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
	return r.inTx(ctx, "app.org_id", orgID, func(ctx context.Context, tx pgx.Tx) error {
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

// ClaimPending atomically marks up to limit pending jobs across all tenants
// as running and returns them oldest first. It is the only method that
// bypasses tenant scoping and is used exclusively by the export worker.
func (r *PGJobRepository) ClaimPending(ctx context.Context, limit int) ([]Job, error) {
	var out []Job
	err := r.inTx(ctx, "app.system", "on", func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			WITH due AS (
				SELECT id FROM export_job WHERE status = $1 ORDER BY created_at ASC, id ASC LIMIT $2
				FOR UPDATE SKIP LOCKED
			)
			UPDATE export_job SET status = $3, started_at = now(), updated_at = now()
			FROM due WHERE export_job.id = due.id
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
