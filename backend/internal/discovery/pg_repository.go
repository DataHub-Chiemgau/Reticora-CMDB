package discovery

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const collectorSelectColumns = `
	id::text,
	organization_id::text,
	COALESCE(client_id::text, ''),
	name,
	COALESCE(version, ''),
	status,
	last_heartbeat,
	config,
	created_at,
	updated_at
`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a new PostgreSQL-backed discovery repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

// Discovery jobs, review items and suppressions carry no client column yet
// (WP-025). They are visible only when the objects they reference are visible
// under the transaction's tenant scope, whose policies filter the subqueries:
// a job through its collector, a review item through all its candidate CIs, a
// suppression through both CIs.
const (
	jobVisible    = "(discovery_job.collector_id IS NULL OR EXISTS (SELECT 1 FROM collector WHERE collector.id = discovery_job.collector_id))"
	reviewVisible = `NOT EXISTS (
		SELECT 1 FROM unnest(review_item.candidate_ci_ids) AS candidate(id)
		WHERE NOT EXISTS (SELECT 1 FROM ci WHERE ci.id = candidate.id))`
	suppressionVisible = `EXISTS (SELECT 1 FROM ci WHERE ci.id = relationship_suppression.source_ci_id)
	AND EXISTS (SELECT 1 FROM ci WHERE ci.id = relationship_suppression.target_ci_id)`
)

// requireVisible fails with "not found" when the written row of table does not
// satisfy visible, i.e. when it references an object outside the tenant
// scope; the transaction is then rolled back.
func requireVisible(ctx context.Context, tx pgx.Tx, table, visible, id string) error {
	var ok bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM "+table+" WHERE id = $1 AND "+visible+")", id).Scan(&ok); err != nil {
		return fmt.Errorf("check %s visibility: %w", table, err)
	}
	if !ok {
		return fmt.Errorf("not found")
	}
	return nil
}

func (r *PGRepository) ListCollectors(ctx context.Context, orgID string, page api.PaginationParams) ([]Collector, int, error) {
	items := make([]Collector, 0)
	var total int

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM collector WHERE organization_id = $1", orgID).Scan(&total); err != nil {
			return fmt.Errorf("count collectors: %w", err)
		}

		query := fmt.Sprintf(
			"SELECT %s FROM collector WHERE organization_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3",
			collectorSelectColumns,
		)
		rows, err := tx.Query(ctx, query, orgID, page.Limit, page.Offset)
		if err != nil {
			return fmt.Errorf("list collectors: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			item, err := scanCollector(rows)
			if err != nil {
				return fmt.Errorf("scan collector: %w", err)
			}
			items = append(items, *item)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate collectors: %w", err)
		}
		return nil
	})

	return items, total, err
}

func (r *PGRepository) RegisterCollector(ctx context.Context, c *Collector) error {
	return database.WithRequestTenant(ctx, r.pool, c.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if c.Config == nil {
			c.Config = make(map[string]any)
		}
		status := c.Status
		if status == "" {
			status = "online"
		}
		now := time.Now().UTC()
		query := `
			INSERT INTO collector (
				organization_id,
				client_id,
				name,
				version,
				status,
				last_heartbeat,
				config
			) VALUES ($1, $2, $3, $4, $5, $6, $7)
			RETURNING id::text, status, last_heartbeat, created_at, updated_at
		`
		var lastHeartbeat sql.NullTime
		var createdAt time.Time
		var updatedAt time.Time
		if err := tx.QueryRow(ctx, query,
			c.OrganizationID,
			nilIfEmpty(c.ClientID),
			c.Name,
			nilIfEmpty(c.Version),
			status,
			now,
			c.Config,
		).Scan(&c.ID, &c.Status, &lastHeartbeat, &createdAt, &updatedAt); err != nil {
			return fmt.Errorf("register collector: %w", err)
		}
		if lastHeartbeat.Valid {
			c.LastHeartbeat = lastHeartbeat.Time.UTC().Format(time.RFC3339Nano)
		}
		c.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
		c.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
		return nil
	})
}

func (r *PGRepository) Heartbeat(ctx context.Context, orgID, collectorID string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmdTag, err := tx.Exec(ctx,
			"UPDATE collector SET status = 'online', last_heartbeat = NOW(), updated_at = NOW() WHERE id = $1 AND organization_id = $2",
			collectorID,
			orgID,
		)
		if err != nil {
			return fmt.Errorf("collector heartbeat: %w", err)
		}
		if cmdTag.RowsAffected() == 0 {
			return fmt.Errorf("not found")
		}
		return nil
	})
}

type collectorScanner interface {
	Scan(dest ...any) error
}

func scanCollector(scanner collectorScanner) (*Collector, error) {
	item := &Collector{}
	var lastHeartbeat sql.NullTime
	var createdAt time.Time
	var updatedAt time.Time
	if err := scanner.Scan(
		&item.ID,
		&item.OrganizationID,
		&item.ClientID,
		&item.Name,
		&item.Version,
		&item.Status,
		&lastHeartbeat,
		&item.Config,
		&createdAt,
		&updatedAt,
	); err != nil {
		return nil, err
	}
	if item.Config == nil {
		item.Config = make(map[string]any)
	}
	if lastHeartbeat.Valid {
		item.LastHeartbeat = lastHeartbeat.Time.UTC().Format(time.RFC3339Nano)
	}
	item.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	item.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
	return item, nil
}

func nilIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// ─── Discovery jobs ─────────────────────────────────────────────────────────

const jobSelectColumns = `
	id::text,
	organization_id::text,
	COALESCE(collector_id::text, ''),
	job_type,
	status,
	config,
	result_summary,
	started_at,
	completed_at,
	created_at
`

func (r *PGRepository) ListJobs(ctx context.Context, orgID string, filter JobFilter, page api.PaginationParams) ([]Job, int, error) {
	items := make([]Job, 0)
	var total int

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := "organization_id = $1 AND " + jobVisible
		args := []any{orgID}
		if filter.Status != "" {
			args = append(args, filter.Status)
			where += fmt.Sprintf(" AND status = $%d", len(args))
		}
		if filter.CollectorID != "" {
			args = append(args, filter.CollectorID)
			where += fmt.Sprintf(" AND collector_id = $%d", len(args))
		}

		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM discovery_job WHERE "+where, args...).Scan(&total); err != nil {
			return fmt.Errorf("count discovery jobs: %w", err)
		}

		query := fmt.Sprintf(
			"SELECT %s FROM discovery_job WHERE %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d",
			jobSelectColumns, where, len(args)+1, len(args)+2,
		)
		args = append(args, page.Limit, page.Offset)
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("list discovery jobs: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			job, err := scanJob(rows)
			if err != nil {
				return fmt.Errorf("scan discovery job: %w", err)
			}
			items = append(items, *job)
		}
		return rows.Err()
	})

	return items, total, err
}

func (r *PGRepository) CreateJob(ctx context.Context, j *Job) error {
	return database.WithRequestTenant(ctx, r.pool, j.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if j.Config == nil {
			j.Config = map[string]any{}
		}
		status := j.Status
		if status == "" {
			status = JobStatusPending
		}
		query := `
			INSERT INTO discovery_job (organization_id, collector_id, job_type, status, config)
			VALUES ($1, $2, $3, $4, $5::jsonb)
			RETURNING id::text, status, created_at
		`
		var createdAt time.Time
		if err := tx.QueryRow(ctx, query,
			j.OrganizationID,
			j.CollectorID,
			j.JobType,
			status,
			j.Config,
		).Scan(&j.ID, &j.Status, &createdAt); err != nil {
			return fmt.Errorf("create discovery job: %w", err)
		}
		j.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
		return requireVisible(ctx, tx, "discovery_job", jobVisible, j.ID)
	})
}

func (r *PGRepository) GetJob(ctx context.Context, orgID, id string) (*Job, error) {
	var job *Job
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf("SELECT %s FROM discovery_job WHERE id = $1 AND organization_id = $2 AND %s", jobSelectColumns, jobVisible)
		row := tx.QueryRow(ctx, query, id, orgID)
		scanned, err := scanJob(row)
		if err != nil {
			return err
		}
		job = scanned
		return nil
	})
	if err != nil {
		return nil, err
	}
	return job, nil
}

func scanJob(scanner collectorScanner) (*Job, error) {
	job := &Job{}
	var startedAt sql.NullTime
	var completedAt sql.NullTime
	var createdAt time.Time
	if err := scanner.Scan(
		&job.ID,
		&job.OrganizationID,
		&job.CollectorID,
		&job.JobType,
		&job.Status,
		&job.Config,
		&job.ResultSummary,
		&startedAt,
		&completedAt,
		&createdAt,
	); err != nil {
		return nil, err
	}
	if job.Config == nil {
		job.Config = map[string]any{}
	}
	if startedAt.Valid {
		job.StartedAt = startedAt.Time.UTC().Format(time.RFC3339Nano)
	}
	if completedAt.Valid {
		job.CompletedAt = completedAt.Time.UTC().Format(time.RFC3339Nano)
	}
	job.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	return job, nil
}

// ─── Reconciliation review items ────────────────────────────────────────────

const reviewSelectColumns = `
	id::text,
	organization_id::text,
	kind,
	status,
	payload,
	COALESCE(candidate_ci_ids::text[], '{}'),
	COALESCE(resolved_by::text, ''),
	resolved_at,
	COALESCE(resolution, ''),
	created_at,
	updated_at
`

func (r *PGRepository) ListReviewItems(ctx context.Context, orgID string, filter ReviewFilter, page api.PaginationParams) ([]ReviewItem, int, error) {
	items := make([]ReviewItem, 0)
	var total int

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := "organization_id = $1 AND " + reviewVisible
		args := []any{orgID}
		if filter.Status != "" {
			args = append(args, filter.Status)
			where += fmt.Sprintf(" AND status = $%d", len(args))
		}
		if filter.Kind != "" {
			args = append(args, filter.Kind)
			where += fmt.Sprintf(" AND kind = $%d", len(args))
		}

		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM review_item WHERE "+where, args...).Scan(&total); err != nil {
			return fmt.Errorf("count review items: %w", err)
		}

		query := fmt.Sprintf(
			"SELECT %s FROM review_item WHERE %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d",
			reviewSelectColumns, where, len(args)+1, len(args)+2,
		)
		args = append(args, page.Limit, page.Offset)
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("list review items: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			item, err := scanReviewItem(rows)
			if err != nil {
				return fmt.Errorf("scan review item: %w", err)
			}
			items = append(items, *item)
		}
		return rows.Err()
	})

	return items, total, err
}

func (r *PGRepository) CreateReviewItem(ctx context.Context, item *ReviewItem) error {
	return database.WithRequestTenant(ctx, r.pool, item.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if item.Payload == nil {
			item.Payload = map[string]any{}
		}
		status := item.Status
		if status == "" {
			status = ReviewStatusOpen
		}
		query := `
			INSERT INTO review_item (organization_id, kind, status, payload, candidate_ci_ids)
			VALUES ($1, $2, $3, $4::jsonb, $5::uuid[])
			RETURNING id::text, status, created_at, updated_at
		`
		var createdAt time.Time
		var updatedAt time.Time
		candidates := item.CandidateCIIDs
		if candidates == nil {
			candidates = []string{}
		}
		if err := tx.QueryRow(ctx, query,
			item.OrganizationID,
			item.Kind,
			status,
			item.Payload,
			candidates,
		).Scan(&item.ID, &item.Status, &createdAt, &updatedAt); err != nil {
			return fmt.Errorf("create review item: %w", err)
		}
		item.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
		item.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
		return requireVisible(ctx, tx, "review_item", reviewVisible, item.ID)
	})
}

func (r *PGRepository) GetReviewItem(ctx context.Context, orgID, id string) (*ReviewItem, error) {
	var item *ReviewItem
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf("SELECT %s FROM review_item WHERE id = $1 AND organization_id = $2 AND %s", reviewSelectColumns, reviewVisible)
		row := tx.QueryRow(ctx, query, id, orgID)
		scanned, err := scanReviewItem(row)
		if err != nil {
			return err
		}
		item = scanned
		return nil
	})
	if err != nil {
		return nil, err
	}
	return item, nil
}

func (r *PGRepository) ResolveReviewItem(ctx context.Context, orgID, id string, resolution Resolution) (*ReviewItem, error) {
	var item *ReviewItem
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf(`
			UPDATE review_item
			SET status = $3,
			    resolution = $4,
			    resolved_by = NULLIF($5, '')::uuid,
			    resolved_at = now(),
			    updated_at = now()
			WHERE id = $1 AND organization_id = $2 AND %s
			RETURNING %s
		`, reviewVisible, reviewSelectColumns)
		row := tx.QueryRow(ctx, query, id, orgID, resolution.Status, resolution.Note, resolution.ResolvedBy)
		scanned, err := scanReviewItem(row)
		if err != nil {
			return err
		}
		item = scanned
		return nil
	})
	if err != nil {
		return nil, err
	}
	return item, nil
}

func scanReviewItem(scanner collectorScanner) (*ReviewItem, error) {
	item := &ReviewItem{}
	var candidates []string
	var resolvedAt sql.NullTime
	var createdAt time.Time
	var updatedAt time.Time
	if err := scanner.Scan(
		&item.ID,
		&item.OrganizationID,
		&item.Kind,
		&item.Status,
		&item.Payload,
		&candidates,
		&item.ResolvedBy,
		&resolvedAt,
		&item.Resolution,
		&createdAt,
		&updatedAt,
	); err != nil {
		return nil, err
	}
	if item.Payload == nil {
		item.Payload = map[string]any{}
	}
	if len(candidates) > 0 {
		item.CandidateCIIDs = candidates
	}
	if resolvedAt.Valid {
		item.ResolvedAt = resolvedAt.Time.UTC().Format(time.RFC3339Nano)
	}
	item.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	item.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
	return item, nil
}

// SuppressedPairs returns the set of suppressed (source|target) CI pairs for the
// organization, used to skip discovery-derived topology edges.
func (r *PGRepository) SuppressedPairs(ctx context.Context, orgID string) (map[string]bool, error) {
	pairs := make(map[string]bool)
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			"SELECT source_ci_id::text, target_ci_id::text FROM relationship_suppression WHERE organization_id = $1 AND "+suppressionVisible,
			orgID,
		)
		if err != nil {
			return fmt.Errorf("list suppressed pairs: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var src, tgt string
			if err := rows.Scan(&src, &tgt); err != nil {
				return fmt.Errorf("scan suppressed pair: %w", err)
			}
			pairs[src+"|"+tgt] = true
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return pairs, nil
}

// LookupCITypeID resolves a CI type name or key to its canonical UUID inside
// the requesting tenant's RLS context. Global system types (organization_id
// IS NULL) are readable by every tenant since migration 000040; org-specific
// types are scoped to the requesting tenant by both the query and RLS.
func (r *PGRepository) LookupCITypeID(ctx context.Context, orgID, nameOrID string) (string, error) {
	var id string
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT id::text FROM ci_type
			WHERE (key = $1 OR name = $1)
			  AND (organization_id IS NULL OR organization_id = $2)
			ORDER BY organization_id NULLS LAST
			LIMIT 1
		`, nameOrID, orgID).Scan(&id)
	})
	if err != nil {
		return "", fmt.Errorf("lookup ci type %q: %w", nameOrID, err)
	}
	return id, nil
}

// CreateEnrollmentCode stores the hash of a new enrollment code.
func (r *PGRepository) CreateEnrollmentCode(ctx context.Context, code *EnrollmentCode) error {
	return database.WithRequestTenant(ctx, r.pool, code.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO collector_enrollment_code (organization_id, code_hash, label, expires_at)
			VALUES ($1, $2, $3, $4)
			RETURNING id::text, created_at
		`, code.OrganizationID, enrollmentCodeHash(code.RawCode()), code.Label, code.ExpiresAt).
			Scan(&code.ID, &code.CreatedAt)
	})
}

// RedeemEnrollmentCode atomically validates and consumes a code. Codes are
// looked up by hash outside the tenant context (the collector is not yet
// enrolled and has no tenant), then the org scope is enforced on the update.
func (r *PGRepository) RedeemEnrollmentCode(ctx context.Context, rawCode, collectorID string) (string, error) {
	hash := enrollmentCodeHash(rawCode)
	// The code hash is globally unique, so the org lookup and the consume
	// update run in a single statement. RLS is bypassed for this deliberately
	// unauthenticated path by using the code_hash (the credential) as the
	// lookup key; the org scope is enforced by the UPDATE's tenant context.
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin redeem tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var orgID string
	// The code hash is the credential; the lookup runs under the dedicated
	// app.system flag because there is no tenant context before enrollment.
	if _, err := tx.Exec(ctx, "SELECT set_config('app.system', 'on', true)"); err != nil {
		return "", fmt.Errorf("set system context: %w", err)
	}
	err = tx.QueryRow(ctx, `
		SELECT organization_id::text FROM collector_enrollment_code
		WHERE code_hash = $1 AND used_at IS NULL AND expires_at > now()
	`, hash).Scan(&orgID)
	if err == pgx.ErrNoRows {
		return "", fmt.Errorf("enrollment code invalid, expired or already used")
	}
	if err != nil {
		return "", fmt.Errorf("lookup enrollment code: %w", err)
	}

	if _, err := tx.Exec(ctx, "SELECT set_config('app.org_id', $1, true)", orgID); err != nil {
		return "", fmt.Errorf("set tenant context: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE collector_enrollment_code
		SET used_at = now(), used_by_collector_id = NULLIF($2, '')::uuid
		WHERE code_hash = $1 AND used_at IS NULL
	`, hash, collectorID); err != nil {
		return "", fmt.Errorf("consume enrollment code: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit redeem: %w", err)
	}
	return orgID, nil
}
