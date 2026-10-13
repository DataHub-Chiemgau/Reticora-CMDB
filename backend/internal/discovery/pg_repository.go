package discovery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const collectorSelectColumns = `
	id::text,
	organization_id::text,
	COALESCE(client_id::text, ''),
	COALESCE(site_id::text, ''),
	name,
	COALESCE(version, ''),
	status,
	status_changed_at,
	last_heartbeat_at,
	COALESCE(cert_fingerprint, ''),
	cert_not_after,
	config,
	created_at,
	updated_at
`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	// Review items are stored by package reviews.
	*reviews.PGRepository
	pool *pgxpool.Pool
}

// NewPGRepository creates a new PostgreSQL-backed discovery repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{PGRepository: reviews.NewPGRepository(pool), pool: pool}
}

// Discovery jobs, review items and suppressions carry no client column yet
// (WP-025). They are visible only when the objects they reference are visible
// under the transaction's tenant scope, whose policies filter the subqueries:
// a job through its collector, a review item through all its candidate CIs, a
// suppression through both CIs.
const (
	jobVisible         = "(discovery_job.collector_id IS NULL OR EXISTS (SELECT 1 FROM collector WHERE collector.id = discovery_job.collector_id))"
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
		// An enrolled collector is connected (active); one created by an
		// administrator waits for its first heartbeat (COL-01).
		status := CollectorPending
		var heartbeat any
		if c.Status == CollectorActive {
			status, heartbeat = CollectorActive, time.Now().UTC()
		}
		created, err := scanCollector(tx.QueryRow(ctx, `
			INSERT INTO collector (organization_id, client_id, site_id, name, version, status, last_heartbeat_at, config)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			RETURNING `+collectorSelectColumns,
			c.OrganizationID, nilIfEmpty(c.ClientID), nilIfEmpty(c.SiteID), c.Name, nilIfEmpty(c.Version), status, heartbeat, c.Config))
		if err != nil {
			return collectorError(fmt.Errorf("register collector: %w", err))
		}
		*c = *created
		return nil
	})
}

// collectorError maps the site binding of a collector to field errors.
func collectorError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.ConstraintName {
	case "collector_site_id_fkey":
		return &api.FieldError{Field: "site_id", Message: "site not found", Status: http.StatusUnprocessableEntity}
	case "collector_site_client":
		return &api.FieldError{Field: "site_id", Message: "the site belongs to another client", Status: http.StatusUnprocessableEntity}
	}
	return err
}

// Heartbeat implements Repository.
func (r *PGRepository) Heartbeat(ctx context.Context, orgID, collectorID string) (string, error) {
	var status string
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `UPDATE collector
			SET last_heartbeat_at = now(),
				status = CASE WHEN status IN ('pending', 'offline', 'active') THEN 'active' ELSE status END
			WHERE id = $1 AND organization_id = $2 AND status <> 'disabled'
			RETURNING status`, collectorID, orgID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			var disabled bool
			if qerr := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM collector WHERE id = $1 AND organization_id = $2 AND status = 'disabled')`,
				collectorID, orgID).Scan(&disabled); qerr != nil {
				return qerr
			}
			if disabled {
				status = CollectorDisabled
				return ErrCollectorDisabled
			}
			return fmt.Errorf("not found")
		}
		if err != nil {
			return fmt.Errorf("collector heartbeat: %w", err)
		}
		return nil
	})
	return status, err
}

// UpdateCollector implements Repository.
func (r *PGRepository) UpdateCollector(ctx context.Context, orgID, collectorID string, patch *CollectorPatch) (*Collector, error) {
	var out *Collector
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := scanCollector(tx.QueryRow(ctx, `SELECT `+collectorSelectColumns+
			` FROM collector WHERE id = $1 AND organization_id = $2 FOR UPDATE`, collectorID, orgID))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("not found")
		}
		if err != nil {
			return err
		}
		name, site, status := current.Name, current.SiteID, current.Status
		if patch.Name != nil {
			name = *patch.Name
		}
		if patch.SiteID != nil {
			site = *patch.SiteID
		}
		if patch.Status != nil {
			status = nextAdminStatus(status, *patch.Status)
		}
		out, err = scanCollector(tx.QueryRow(ctx, `UPDATE collector SET name = $3, site_id = $4, status = $5
			WHERE id = $1 AND organization_id = $2 RETURNING `+collectorSelectColumns,
			collectorID, orgID, name, nilIfEmpty(site), status))
		if err != nil {
			return collectorError(err)
		}
		// Disabling revokes the collector's certificates: they appear on the
		// revocation list and the NATS endpoint refuses them (COL-04).
		if status == CollectorDisabled && current.Status != CollectorDisabled {
			if _, err = tx.Exec(ctx, `UPDATE collector_certificate SET revoked_at = now(), revocation_reason = 'collector disabled'
				WHERE collector_id = $1 AND revoked_at IS NULL`, collectorID); err != nil {
				return fmt.Errorf("revoke collector certificates: %w", err)
			}
		}
		return nil
	})
	return out, err
}

type collectorScanner interface {
	Scan(dest ...any) error
}

func formatTime(t sql.NullTime) string {
	if !t.Valid {
		return ""
	}
	return t.Time.UTC().Format(time.RFC3339Nano)
}

func scanCollector(scanner collectorScanner) (*Collector, error) {
	item := &Collector{}
	var statusChanged time.Time
	var lastHeartbeat, certNotAfter sql.NullTime
	var createdAt, updatedAt time.Time
	if err := scanner.Scan(
		&item.ID,
		&item.OrganizationID,
		&item.ClientID,
		&item.SiteID,
		&item.Name,
		&item.Version,
		&item.Status,
		&statusChanged,
		&lastHeartbeat,
		&item.CertFingerprint,
		&certNotAfter,
		&item.Config,
		&createdAt,
		&updatedAt,
	); err != nil {
		return nil, err
	}
	if item.Config == nil {
		item.Config = make(map[string]any)
	}
	item.StatusChangedAt = statusChanged.UTC().Format(time.RFC3339Nano)
	item.LastHeartbeatAt = formatTime(lastHeartbeat)
	item.CertNotAfter = formatTime(certNotAfter)
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

// SuppressedPairs returns the suppressed edges (source|target|type key) of
// the organization; discovery does not recreate them (REL-03).
func (r *PGRepository) SuppressedPairs(ctx context.Context, orgID string) (map[string]bool, error) {
	pairs := make(map[string]bool)
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT relationship_suppression.source_ci_id::text, relationship_suppression.target_ci_id::text, rt.key
			FROM relationship_suppression JOIN relationship_type rt ON rt.id = relationship_suppression.relationship_type_id
			WHERE relationship_suppression.organization_id = $1 AND `+suppressionVisible,
			orgID,
		)
		if err != nil {
			return fmt.Errorf("list suppressed edges: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var src, tgt, key string
			if err := rows.Scan(&src, &tgt, &key); err != nil {
				return fmt.Errorf("scan suppressed edge: %w", err)
			}
			pairs[src+"|"+tgt+"|"+key] = true
		}
		return rows.Err()
	})
	return pairs, err
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

// EnrollCollector consumes a code and registers the collector in one
// transaction of the code's organization (COL-04): the collector is not
// enrolled yet and has no tenant, so the code is looked up by its hash with
// the read-only system flag (the hash is the credential). The consuming
// UPDATE … WHERE used_at IS NULL … RETURNING lets concurrent enrollments
// with one code succeed exactly once; a failed registration leaves the
// code unused.
func (r *PGRepository) EnrollCollector(ctx context.Context, rawCode string, c *Collector, issue CertIssuer) (*IssuedCert, error) {
	hash := enrollmentCodeHash(rawCode)
	var orgID string
	err := database.WithSystem(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		lookupErr := tx.QueryRow(ctx, `
			SELECT organization_id::text FROM collector_enrollment_code
			WHERE code_hash = $1 AND used_at IS NULL AND expires_at > now()
		`, hash).Scan(&orgID)
		if lookupErr == pgx.ErrNoRows {
			return errEnrollmentCodeInvalid
		}
		if lookupErr != nil {
			return fmt.Errorf("lookup enrollment code: %w", lookupErr)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	c.OrganizationID = orgID
	scope := database.OrgWideScope(orgID, "")
	var cert *IssuedCert
	err = database.WithTenant(ctx, r.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		var codeID string
		consumeErr := tx.QueryRow(ctx, `
			UPDATE collector_enrollment_code SET used_at = now()
			WHERE code_hash = $1 AND used_at IS NULL AND expires_at > now()
			RETURNING id::text
		`, hash).Scan(&codeID)
		if consumeErr == pgx.ErrNoRows {
			return errEnrollmentCodeInvalid
		}
		if consumeErr != nil {
			return fmt.Errorf("consume enrollment code: %w", consumeErr)
		}
		if c.Config == nil {
			c.Config = map[string]any{}
		}
		created, insErr := scanCollector(tx.QueryRow(ctx, `
			INSERT INTO collector (organization_id, client_id, site_id, name, version, status, last_heartbeat_at, config)
			VALUES ($1, $2, $3, $4, $5, 'active', now(), $6)
			RETURNING `+collectorSelectColumns,
			orgID, nilIfEmpty(c.ClientID), nilIfEmpty(c.SiteID), c.Name, nilIfEmpty(c.Version), c.Config))
		if insErr != nil {
			return collectorError(fmt.Errorf("register collector: %w", insErr))
		}
		*c = *created
		if _, uerr := tx.Exec(ctx, `UPDATE collector_enrollment_code SET used_by_collector_id = $2 WHERE id = $1`,
			codeID, c.ID); uerr != nil {
			return fmt.Errorf("record enrolled collector: %w", uerr)
		}
		if issue == nil {
			return nil
		}
		var ierr error
		if cert, ierr = issue(ctx, orgID, c.ID); ierr != nil {
			return ierr
		}
		return recordCertificateTx(ctx, tx, orgID, c, cert)
	})
	return cert, err
}

// recordCertificateTx stores an issued certificate and shows it on the
// collector.
func recordCertificateTx(ctx context.Context, tx pgx.Tx, orgID string, c *Collector, cert *IssuedCert) error {
	if _, err := tx.Exec(ctx, `INSERT INTO collector_certificate (organization_id, collector_id, serial, fingerprint, not_before, not_after)
		VALUES ($1, $2, $3, $4, $5, $6)`, orgID, c.ID, cert.Serial, cert.Fingerprint, cert.NotBefore, cert.NotAfter); err != nil {
		return fmt.Errorf("record collector certificate: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE collector SET cert_fingerprint = $2, cert_not_after = $3 WHERE id = $1`,
		c.ID, cert.Fingerprint, cert.NotAfter); err != nil {
		return fmt.Errorf("record collector certificate: %w", err)
	}
	c.CertFingerprint, c.CertNotAfter = cert.Fingerprint, cert.NotAfter.UTC().Format(time.RFC3339Nano)
	return nil
}

// RenewCertificate implements Repository: the collector is locked, its
// newest valid certificate decides whether renewal is due; the former
// certificate stays valid until it expires.
func (r *PGRepository) RenewCertificate(ctx context.Context, orgID, collectorID string, issue CertIssuer) (*IssuedCert, error) {
	var cert *IssuedCert
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		c, err := scanCollector(tx.QueryRow(ctx, `SELECT `+collectorSelectColumns+
			` FROM collector WHERE id = $1 AND organization_id = $2 FOR UPDATE`, collectorID, orgID))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("not found")
		}
		if err != nil {
			return err
		}
		if c.Status == CollectorDisabled {
			return ErrCollectorDisabled
		}
		var notBefore, notAfter time.Time
		err = tx.QueryRow(ctx, `SELECT not_before, not_after FROM collector_certificate
			WHERE collector_id = $1 AND revoked_at IS NULL AND not_after > now() ORDER BY not_after DESC LIMIT 1`,
			collectorID).Scan(&notBefore, &notAfter)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		case !NeedsRenewal(notBefore, notAfter, time.Now().UTC()):
			return ErrRenewalNotDue
		}
		if cert, err = issue(ctx, orgID, collectorID); err != nil {
			return err
		}
		return recordCertificateTx(ctx, tx, orgID, c, cert)
	})
	return cert, err
}

// errEnrollmentCodeInvalid is returned for unknown, expired and used codes
// alike, so a caller learns nothing about which case applies.
var errEnrollmentCodeInvalid = fmt.Errorf("enrollment code invalid, expired or already used")

// ProcessedRecords returns the record_ids processed within the dedupe window
// (REC-01, RecordLedger).
func (r *PGRepository) ProcessedRecords(ctx context.Context, orgID string, recordIDs []string) (map[string]bool, error) {
	seen := map[string]bool{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT record_id::text FROM ingest_record
			WHERE organization_id = $1 AND record_id = ANY($2::uuid[]) AND processed_at > now() - $3::interval`,
			orgID, recordIDs, RecordDedupeWindow.String())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			seen[id] = true
		}
		return rows.Err()
	})
	return seen, err
}

// MarkRecordsProcessed remembers processed records and forgets those older
// than the dedupe window (REC-01, RecordLedger).
func (r *PGRepository) MarkRecordsProcessed(ctx context.Context, orgID string, records []ProcessedRecord) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		for _, rec := range records {
			if _, err := tx.Exec(ctx, `INSERT INTO ingest_record (organization_id, record_id, collector_id, observed_at)
				VALUES ($1, $2, NULLIF($3, ''), $4)
				ON CONFLICT (organization_id, record_id) DO UPDATE SET processed_at = now(), observed_at = EXCLUDED.observed_at`,
				orgID, rec.RecordID, rec.CollectorID, rec.ObservedAt); err != nil {
				return fmt.Errorf("record processed ingest record: %w", err)
			}
		}
		_, err := tx.Exec(ctx, `DELETE FROM ingest_record WHERE organization_id = $1 AND processed_at <= now() - $2::interval`,
			orgID, RecordDedupeWindow.String())
		return err
	})
}

// IdentityCandidates returns the ids of the CIs an incoming device may be,
// using the indexes on serial number, hardware UUID, primary MAC, management
// IP and hostname/FQDN plus the attribute index for interface MACs, over the
// whole inventory of the organization (REC-02, NFR-01). Soft-deleted CIs are
// excluded.
func (r *PGRepository) IdentityCandidates(ctx context.Context, orgID string, q *IdentityQuery) ([]string, error) {
	var ids []string
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM ci WHERE organization_id = $1 AND deleted_at IS NULL AND `+identityPredicate+`
			ORDER BY created_at, id LIMIT 1000`, identityArgs(orgID, q)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	return ids, err
}

// DeletedIdentityCandidates returns the soft-deleted CIs an incoming device
// may be, with the fields identity resolution compares (CI-13).
func (r *PGRepository) DeletedIdentityCandidates(ctx context.Context, orgID string, q *IdentityQuery) ([]ci.Item, error) {
	var out []ci.Item
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, ci_type_id::text, COALESCE(client_id::text, ''), name,
				COALESCE(serial_number, ''), COALESCE(hardware_uuid::text, ''), COALESCE(primary_mac::text, ''),
				COALESCE(host(management_ip), ''), COALESCE(sys_object_id, ''), COALESCE(hostname, ''), COALESCE(fqdn, ''),
				COALESCE(attributes, '{}'::jsonb), deleted_at
			FROM ci WHERE organization_id = $1 AND deleted_at IS NOT NULL AND `+identityPredicate+`
			ORDER BY deleted_at DESC, id LIMIT 1000`, identityArgs(orgID, q)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item := ci.Item{OrganizationID: orgID}
			var deletedAt time.Time
			if err := rows.Scan(&item.ID, &item.CITypeID, &item.ClientID, &item.Name, &item.SerialNumber, &item.HardwareUUID,
				&item.PrimaryMAC, &item.ManagementIP, &item.SysObjectID, &item.Hostname, &item.FQDN, &item.Attributes, &deletedAt); err != nil {
				return err
			}
			item.DeletedAt = &deletedAt
			out = append(out, item)
		}
		return rows.Err()
	})
	return out, err
}

// identityPredicate matches CIs by the indexed identity columns; its
// arguments come from identityArgs.
const identityPredicate = `(
	($2 <> '' AND serial_number = $2)
	OR ($3 <> '' AND hardware_uuid = NULLIF($3, '')::uuid)
	OR primary_mac = ANY($4::macaddr[])
	OR attributes @> ANY($5::jsonb[])
	OR ($6 <> '' AND management_ip = NULLIF($6, '')::inet)
	OR lower(hostname) = ANY($7::text[])
	OR lower(fqdn) = ANY($7::text[])
)`

func identityArgs(orgID string, q *IdentityQuery) []any {
	macDocs := make([]string, 0, len(q.MACs))
	for _, mac := range q.MACs {
		macDocs = append(macDocs, `{"fingerprint":{"mac_addresses":["`+mac+`"]}}`)
	}
	return []any{orgID, q.SerialNumber, q.HardwareUUID, q.MACs, macDocs, hostOnly(q.ManagementIP), q.Hostnames}
}

// hostOnly drops a prefix length and anything that is no IP address.
func hostOnly(ip string) string {
	ip = strings.TrimSpace(ip)
	if i := strings.IndexByte(ip, '/'); i >= 0 {
		ip = ip[:i]
	}
	if net.ParseIP(ip) == nil {
		return ""
	}
	return ip
}

// DefaultVRF returns the organization's default VRF; records without vrf_id
// belong to it (REC-02).
func (r *PGRepository) DefaultVRF(ctx context.Context, orgID string) (string, error) {
	var id string
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COALESCE(default_vrf_of($1)::text, '')`, orgID).Scan(&id)
	})
	return id, err
}
