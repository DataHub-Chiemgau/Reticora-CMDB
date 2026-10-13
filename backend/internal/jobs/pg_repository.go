package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// PGRepository stores jobs in PostgreSQL. Jobs belong to the organization
// and carry no client or site, so every operation runs with the
// organization-wide scope of orgID — from requests and from workers alike.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a PostgreSQL job repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository { return &PGRepository{pool: pool} }

const jobColumns = `id::text, COALESCE(organization_id::text, ''), kind, status, progress, params, result,
	COALESCE(error, ''), COALESCE(idempotency_key, ''), COALESCE(created_by::text, ''), attempts, max_attempts,
	run_after, COALESCE(timeout_seconds, 0), COALESCE(ref_table, ''), COALESCE(ref_id::text, ''),
	created_at, updated_at, started_at, finished_at`

func (r *PGRepository) inOrg(ctx context.Context, orgID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	scope := database.OrgWideScope(orgID, "")
	return database.WithTenant(ctx, r.pool, &scope, fn)
}

func scanJob(row pgx.Row) (*Job, error) {
	j := &Job{}
	var status string
	var result []byte
	if err := row.Scan(&j.ID, &j.OrganizationID, &j.Kind, &status, &j.Progress, &j.Params, &result,
		&j.Error, &j.IdempotencyKey, &j.CreatedBy, &j.Attempts, &j.MaxAttempts, &j.RunAfter, &j.TimeoutSeconds,
		&j.RefTable, &j.RefID, &j.CreatedAt, &j.UpdatedAt, &j.StartedAt, &j.FinishedAt); err != nil {
		return nil, err
	}
	j.Status = Status(status)
	if len(result) > 0 {
		j.Result = result
	}
	return j, nil
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Insert implements Repository.
func (r *PGRepository) Insert(ctx context.Context, job *Job) (bool, error) {
	created := false
	err := r.inOrg(ctx, job.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		params := job.Params
		if len(params) == 0 {
			params = []byte("{}")
		}
		timeout := any(nil)
		if job.TimeoutSeconds > 0 {
			timeout = job.TimeoutSeconds
		}
		maxAttempts := max(job.MaxAttempts, 1)
		inserted, err := scanJob(tx.QueryRow(ctx, `
			INSERT INTO job (organization_id, kind, status, params, idempotency_key, created_by, max_attempts,
				run_after, timeout_seconds, ref_table, ref_id)
			VALUES ($1, $2, 'queued', $3, $4, $5::uuid, $6, $7, $8, $9, $10::uuid)
			ON CONFLICT DO NOTHING
			RETURNING `+jobColumns,
			job.OrganizationID, job.Kind, params, nilIfEmpty(job.IdempotencyKey), surrogate(job.CreatedBy),
			maxAttempts, job.RunAfter, timeout, nilIfEmpty(job.RefTable), nilIfEmpty(job.RefID)))
		if err == nil {
			*job, created = *inserted, true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("insert job: %w", err)
		}
		// The idempotency key or the reference is taken: the first job.
		existing, err := scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM job
			WHERE organization_id = $1 AND ((kind = $2 AND idempotency_key = $3) OR (ref_table = $4 AND ref_id = $5::uuid))`,
			job.OrganizationID, job.Kind, nilIfEmpty(job.IdempotencyKey), nilIfEmpty(job.RefTable), nilIfEmpty(job.RefID)))
		if err != nil {
			return fmt.Errorf("read existing job: %w", err)
		}
		*job = *existing
		return nil
	})
	return created, err
}

func (r *PGRepository) getOne(ctx context.Context, orgID, where string, args ...any) (*Job, error) {
	var out *Job
	err := r.inOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		j, err := scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM job WHERE organization_id = $1 AND `+where,
			append([]any{orgID}, args...)...))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		out = j
		return err
	})
	return out, err
}

// Get implements Repository.
func (r *PGRepository) Get(ctx context.Context, orgID, id string) (*Job, error) {
	if !isUUID(id) {
		return nil, ErrNotFound
	}
	return r.getOne(ctx, orgID, "id = $2", id)
}

// GetByRef implements Repository.
func (r *PGRepository) GetByRef(ctx context.Context, orgID, refTable, refID string) (*Job, error) {
	if !isUUID(refID) {
		return nil, ErrNotFound
	}
	return r.getOne(ctx, orgID, "ref_table = $2 AND ref_id = $3", refTable, refID)
}

// List implements Repository.
func (r *PGRepository) List(ctx context.Context, orgID string, filter Filter, page api.PaginationParams) ([]Job, int, error) {
	out := []Job{}
	var total int
	err := r.inOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where, args := []string{"organization_id = $1"}, []any{orgID}
		if filter.Kind != "" {
			args = append(args, filter.Kind)
			where = append(where, fmt.Sprintf("kind = $%d", len(args)))
		}
		if filter.Status != "" {
			args = append(args, string(filter.Status))
			where = append(where, fmt.Sprintf("status = $%d", len(args)))
		}
		cond := strings.Join(where, " AND ")
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM job WHERE `+cond, args...).Scan(&total); err != nil {
			return fmt.Errorf("count jobs: %w", err)
		}
		limit := page.Limit
		if limit <= 0 {
			limit = 50
		}
		rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT %s FROM job WHERE %s ORDER BY created_at DESC, id LIMIT $%d OFFSET $%d`,
			jobColumns, cond, len(args)+1, len(args)+2), append(args, limit, page.Offset)...)
		if err != nil {
			return fmt.Errorf("list jobs: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			j, err := scanJob(rows)
			if err != nil {
				return err
			}
			out = append(out, *j)
		}
		return rows.Err()
	})
	return out, total, err
}

// ClaimNext implements Repository: FOR UPDATE SKIP LOCKED lets concurrent
// workers take different jobs.
func (r *PGRepository) ClaimNext(ctx context.Context, orgID string, kinds []string, now time.Time) (*Job, error) {
	var out *Job
	err := r.inOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		j, err := scanJob(tx.QueryRow(ctx, `
			UPDATE job SET status = 'running', attempts = attempts + 1, started_at = $3, finished_at = NULL
			WHERE id = (
				SELECT id FROM job
				WHERE organization_id = $1 AND status = 'queued' AND kind = ANY($2) AND run_after <= $3
				ORDER BY run_after, created_at
				FOR UPDATE SKIP LOCKED
				LIMIT 1)
			RETURNING `+jobColumns, orgID, kinds, now))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("claim job: %w", err)
		}
		out = j
		return nil
	})
	return out, err
}

// ClaimID implements Repository: the status check and the change are one
// statement, so of two workers holding the same job only one claims it.
func (r *PGRepository) ClaimID(ctx context.Context, orgID, id string, now time.Time) (*Job, error) {
	if !isUUID(id) {
		return nil, ErrNotFound
	}
	var out *Job
	err := r.inOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		j, err := scanJob(tx.QueryRow(ctx, `
			UPDATE job SET status = 'running', attempts = attempts + 1, started_at = $3, finished_at = NULL
			WHERE organization_id = $1 AND id = $2 AND status = 'queued' AND run_after <= $3
			RETURNING `+jobColumns, orgID, id, now))
		if errors.Is(err, pgx.ErrNoRows) {
			var exists bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM job WHERE organization_id = $1 AND id = $2)`,
				orgID, id).Scan(&exists); err != nil {
				return fmt.Errorf("claim job: %w", err)
			}
			if !exists {
				return ErrNotFound
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("claim job: %w", err)
		}
		out = j
		return nil
	})
	return out, err
}

// QueuedDue implements Repository. It reads across organizations with the
// system flag, which only widens SELECT; claiming stays in the tenant.
func (r *PGRepository) QueuedDue(ctx context.Context, now time.Time, limit int) ([]Job, error) {
	var out []Job
	err := database.WithSystem(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+jobColumns+` FROM job
			WHERE status = 'queued' AND run_after <= $1 AND organization_id IS NOT NULL
			ORDER BY run_after, created_at LIMIT $2`, now, limit)
		if err != nil {
			return fmt.Errorf("list due jobs: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			j, err := scanJob(rows)
			if err != nil {
				return err
			}
			out = append(out, *j)
		}
		return rows.Err()
	})
	return out, err
}

// Transition implements Repository; the status check and the change are
// one statement.
func (r *PGRepository) Transition(ctx context.Context, orgID, id string, from []Status, u *Update) (*Job, error) {
	if !isUUID(id) {
		return nil, ErrNotFound
	}
	var out *Job
	err := r.inOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		fromText := make([]string, len(from))
		for i, s := range from {
			fromText[i] = string(s)
		}
		var finished any
		if u.Status.Terminal() {
			finished = u.FinishedAt
		}
		j, err := scanJob(tx.QueryRow(ctx, `
			UPDATE job SET status = $3,
				progress = COALESCE($4, progress),
				result = COALESCE($5, result),
				error = COALESCE($6, error),
				run_after = COALESCE($7, run_after),
				started_at = COALESCE($8, started_at),
				attempts = attempts + CASE WHEN $9 THEN 1 ELSE 0 END,
				finished_at = $10
			WHERE organization_id = $1 AND id = $2 AND status = ANY($11)
			RETURNING `+jobColumns,
			orgID, id, string(u.Status), u.Progress, nullJSON(u.Result), u.Error, u.RunAfter, u.StartedAt,
			u.IncAttempts, finished, fromText))
		if errors.Is(err, pgx.ErrNoRows) {
			var current string
			if gerr := tx.QueryRow(ctx, `SELECT status FROM job WHERE organization_id = $1 AND id = $2`, orgID, id).Scan(&current); gerr != nil {
				return ErrNotFound
			}
			return fmt.Errorf("%w: %s → %s", ErrTransition, current, u.Status)
		}
		if err != nil {
			return fmt.Errorf("update job: %w", err)
		}
		out = j
		return nil
	})
	return out, err
}

// RunningSince implements Repository.
func (r *PGRepository) RunningSince(ctx context.Context, orgID string, t time.Time) ([]Job, error) {
	var out []Job
	err := r.inOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+jobColumns+` FROM job
			WHERE organization_id = $1 AND status = 'running' AND started_at < $2`, orgID, t)
		if err != nil {
			return fmt.Errorf("list running jobs: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			j, err := scanJob(rows)
			if err != nil {
				return err
			}
			out = append(out, *j)
		}
		return rows.Err()
	})
	return out, err
}

// surrogate is created_by as stored: a UUID surrogate, otherwise none.
func surrogate(createdBy string) any {
	if !isUUID(createdBy) {
		return nil
	}
	return createdBy
}

func nullJSON(raw []byte) any {
	if raw == nil {
		return nil
	}
	return raw
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
				return false
			}
		}
	}
	return true
}
