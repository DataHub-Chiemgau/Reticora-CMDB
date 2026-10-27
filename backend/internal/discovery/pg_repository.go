package discovery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
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
	// lockTimeout replaces ingestLockTimeout when set (tests).
	lockTimeout time.Duration
	// bus announces disables (WithDisableAnnouncer).
	bus *nats.Conn
}

// NewPGRepository creates a new PostgreSQL-backed discovery repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{PGRepository: reviews.NewPGRepository(pool), pool: pool}
}

// WithDisableAnnouncer makes UpdateCollector announce every disable on the
// internal NATS once committed (AnnounceDisabled): the edge worker of one
// replica, whichever, cuts the collector's open connections (COL-04, F-025).
func (r *PGRepository) WithDisableAnnouncer(nc *nats.Conn) *PGRepository {
	r.bus = nc
	return r
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

// CountCollectors implements Repository. It counts in an org-wide
// transaction: max_collectors is a quota of the organization, and a client-
// or site-scoped caller must not stay below it by seeing fewer collectors.
func (r *PGRepository) CountCollectors(ctx context.Context, orgID string) (int64, error) {
	scope := database.OrgWideScope(orgID, "")
	var n int64
	err := database.WithTenant(ctx, r.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		n, err = countCollectors(ctx, tx, orgID)
		return err
	})
	return n, err
}

func countCollectors(ctx context.Context, tx pgx.Tx, orgID string) (int64, error) {
	var n int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM collector WHERE organization_id = $1 AND status <> 'disabled'`, orgID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count collectors: %w", err)
	}
	return n, nil
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

// CollectorStatus implements Repository.
func (r *PGRepository) CollectorStatus(ctx context.Context, orgID, collectorID string) (string, error) {
	if !audit.IsUUID(collectorID) {
		return "", api.ErrNotFound
	}
	var status string
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT status FROM collector WHERE id = $1 AND organization_id = $2`, collectorID, orgID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return api.ErrNotFound
		}
		return err
	})
	return status, err
}

// UpdateCollector implements Repository.
func (r *PGRepository) UpdateCollector(ctx context.Context, orgID, collectorID string, patch *CollectorPatch) (*Collector, error) {
	// A malformed id names no collector either (404, F-257).
	if !audit.IsUUID(collectorID) {
		return nil, api.ErrNotFound
	}
	var out *Collector
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := scanCollector(tx.QueryRow(ctx, `SELECT `+collectorSelectColumns+
			` FROM collector WHERE id = $1 AND organization_id = $2 FOR UPDATE`, collectorID, orgID))
		if errors.Is(err, pgx.ErrNoRows) {
			return api.ErrNotFound
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
	// Open connections outlive the revocation: the edge workers cut them.
	// A repeated disable is announced again (another cut).
	if err == nil && r.bus != nil && patch.Status != nil && out.Status == CollectorDisabled {
		if aerr := AnnounceDisabled(r.bus, orgID, collectorID); aerr != nil {
			slog.Error("announce the disable of a collector", "event", "discovery.collector.kick_failed",
				"organization_id", orgID, "collector_id", collectorID, "error", aerr)
		}
	}
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

// LookupCITypeIDs resolves several type names or keys with one statement
// (CITypeBatchLookup, NFR-03): per name the same type LookupCITypeID finds,
// an org-specific type before a global one.
func (r *PGRepository) LookupCITypeIDs(ctx context.Context, orgID string, namesOrKeys []string) (map[string]string, error) {
	types := map[string]string{}
	if len(namesOrKeys) == 0 {
		return types, nil
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT DISTINCT ON (k.name) k.name, t.id::text
			FROM unnest($1::text[]) AS k(name)
			JOIN ci_type t ON (t.key = k.name OR t.name = k.name)
			  AND (t.organization_id IS NULL OR t.organization_id = $2)
			ORDER BY k.name, t.organization_id NULLS LAST, t.id
		`, namesOrKeys, orgID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name, id string
			if err := rows.Scan(&name, &id); err != nil {
				return err
			}
			types[name] = id
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("lookup ci types: %w", err)
	}
	return types, nil
}

// RunInTx implements TxRunner: fn runs in one transaction of the context's
// tenant scope, which every repository call with fn's context joins.
func (r *PGRepository) RunInTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error {
	return database.WithRequestTenantTx(ctx, r.pool, orgID, fn)
}

// LockCI implements CILocker: the CI row stays locked until the context
// transaction ends.
func (r *PGRepository) LockCI(ctx context.Context, orgID, ciID string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM ci WHERE id = $1 AND organization_id = $2 FOR UPDATE`, ciID, orgID); err != nil {
			return fmt.Errorf("lock ci: %w", err)
		}
		return nil
	})
}

// The identity locks of the ingest lanes (WP-334); the record and batch
// transactions find them by type assertion.
var _ IdentityLocker = (*PGRepository)(nil)

// ingestLockTimeout bounds the wait for the identity and max_cis locks of an
// ingest transaction. Only ingest transactions take them, but the lane
// goroutine that waits serves every organization, so no holder may stall it
// for longer: the transaction fails with 55P03 and the record runs again
// after a pause (retryReason). A wait behind other ingest transactions, one
// batch each, stays far below it; a delivery that fails on it in its batch
// and in every record attempt (5 waits) still ends within AckWait. The
// other lock waits of the transaction keep their bounds.
const ingestLockTimeout = 20 * time.Second

// lockTimeoutSetting is the lock_timeout of a lock statement: the
// repository's own for the tests, ingestLockTimeout otherwise.
func (r *PGRepository) lockTimeoutSetting() string {
	d := ingestLockTimeout
	if r.lockTimeout > 0 {
		d = r.lockTimeout
	}
	return strconv.FormatInt(d.Milliseconds(), 10) + "ms"
}

// boundedLocksSQL is one statement that sets lock_timeout to the parameter
// timeout, takes the advisory locks of locks and restores the
// transaction's lock_timeout: prev is read before bound sets the timeout
// (bound reads prev), locks reads bound in its FROM list, so its lock
// functions run after it, and the outer set_config runs after count(*) has
// taken every lock.
func boundedLocksSQL(timeout, locks string) string {
	return `WITH prev AS MATERIALIZED (SELECT current_setting('lock_timeout') AS v),
bound AS MATERIALIZED (SELECT set_config('lock_timeout', ` + timeout + `, true) FROM prev),
locked AS MATERIALIZED (` + locks + `)
SELECT set_config('lock_timeout', prev.v, true), n.locks FROM prev, (SELECT count(*) AS locks FROM locked) n`
}

// identityLockSQL takes the identity locks of a transaction in one
// statement: the organization's lock ($1, identityOrgLockClass) first,
// shared, or exclusive with $3, then the keys ($2, identityKeyLockClass)
// exclusively in ascending order, each wait bounded by $4. The lock
// functions run after the sort (EXPLAIN: Result above Sort), so
// transactions that share keys never wait for each other in a cycle.
var identityLockSQL = boundedLocksSQL("$4", `SELECT CASE WHEN s.o = 0 AND NOT $3 THEN pg_advisory_xact_lock_shared(s.c, s.k) ELSE pg_advisory_xact_lock(s.c, s.k) END
	FROM bound, (SELECT 0 AS o, `+strconv.Itoa(int(identityOrgLockClass))+` AS c, $1::int AS k
		UNION ALL SELECT DISTINCT 1, `+strconv.Itoa(int(identityKeyLockClass))+`, unnest($2::int[])) s
	ORDER BY s.o, s.k`)

// LockIdentities implements IdentityLocker with identityLockSQL (WP-334).
func (r *PGRepository) LockIdentities(ctx context.Context, orgID string, locks IdentityLocks) error {
	keys := locks.Keys
	if keys == nil || locks.Exclusive {
		keys = []int32{}
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, identityLockSQL, locks.Org, keys, locks.Exclusive, r.lockTimeoutSetting()); err != nil {
			return fmt.Errorf("lock identity keys: %w", err)
		}
		return nil
	})
}

// ciCountLockNamespace keys the max_cis lock of an organization in
// ciCountLockClass.
const ciCountLockNamespace = "reticora/max-cis\x00"

// ciCountLockSQL takes the max_cis lock ($2 in ciCountLockClass), shared or
// exclusive with $3, its wait bounded by $1.
var ciCountLockSQL = boundedLocksSQL("$1", `SELECT CASE WHEN $3 THEN pg_advisory_xact_lock(`+strconv.Itoa(int(ciCountLockClass))+`, $2::int)
	ELSE pg_advisory_xact_lock_shared(`+strconv.Itoa(int(ciCountLockClass))+`, $2::int) END FROM bound`)

// LockCICount implements CICountLocker: it takes the organization's max_cis
// lock, exclusive or shared, until the end of the context transaction and
// then counts the CIs the transaction sees, as ci.List counts them. The
// count is a statement of its own, so it sees what every holder before
// committed.
func (r *PGRepository) LockCICount(ctx context.Context, orgID string, exclusive bool) (int64, error) {
	key := ingestLockKey(ciCountLockNamespace + strings.ToLower(orgID))
	var count int64
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, ciCountLockSQL, r.lockTimeoutSetting(), key, exclusive); err != nil {
			return fmt.Errorf("lock max_cis: %w", err)
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM ci WHERE organization_id = $1 AND deleted_at IS NULL`, orgID).Scan(&count); err != nil {
			return fmt.Errorf("count cis: %w", err)
		}
		return nil
	})
	return count, err
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
// code unused and writes no collector.enrolled.
func (r *PGRepository) EnrollCollector(ctx context.Context, rawCode string, c *Collector, issue CertIssuer) (*IssuedCert, error) {
	return r.EnrollCollectorWithQuota(ctx, rawCode, c, issue, nil)
}

// EnrollCollectorWithQuota implements Repository: max_collectors is
// counted and checked in the enrollment transaction before the code is
// consumed, so a refusal rolls back and leaves the code unused.
func (r *PGRepository) EnrollCollectorWithQuota(ctx context.Context, rawCode string, c *Collector, issue CertIssuer, quota QuotaGuard) (*IssuedCert, error) {
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
		if quota != nil {
			current, countErr := countCollectors(ctx, tx, orgID)
			if countErr != nil {
				return countErr
			}
			// The guard reads the entitlements in this transaction.
			qctx := database.ContextWithTx(database.ContextWithTenantScope(ctx, &scope), tx)
			if qerr := quota.AllowCreate(qctx, orgID, entitlement.LimitMaxCollectors, current); qerr != nil {
				return qerr
			}
		}
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
		if issue != nil {
			// No change of the collector CA runs while the certificate is
			// issued and recorded (lockCAShared).
			if lerr := lockCAShared(ctx, tx); lerr != nil {
				return lerr
			}
			var ierr error
			if cert, ierr = issue(ctx, orgID, c.ID); ierr != nil {
				return ierr
			}
			if rerr := recordCertificateTx(ctx, tx, orgID, c, cert); rerr != nil {
				return rerr
			}
		}
		// collector.enrolled (EVT-02) commits with the collector, after its
		// other writes; the data leaves out the configuration.
		_, eerr := events.NewOutbox().Append(ctx, tx, &events.Event{OrgID: orgID, Type: events.CollectorEnrolled, EntityID: c.ID,
			Data: map[string]any{"id": c.ID, "name": c.Name, "version": c.Version, "client_id": c.ClientID, "site_id": c.SiteID,
				"status": c.Status, "cert_fingerprint": c.CertFingerprint, "cert_not_after": c.CertNotAfter}})
		return eerr
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

// RenewEdgeCertificate issues the successor of the collector's certificate
// predecessor (serial, hex) for a renewal request whose signature with that
// certificate's key the caller verified (CertificateRenewal): the
// collector is locked, it must not be disabled, the predecessor must be its
// own and not revoked, and a predecessor renewed within the replay window
// of the request gets no second successor (ErrRenewalNotDue). The successor
// is recorded and audited (collector.certificate_issued) in the same
// transaction; the predecessor stays valid until the collector connects
// with the successor or SupersededGrace passed (RunEdge).
func (r *PGRepository) RenewEdgeCertificate(ctx context.Context, orgID, collectorID, predecessor string, issue CertIssuer) (*IssuedCert, error) {
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
		var current, renewed bool
		if err = tx.QueryRow(ctx, `SELECT p.revoked_at IS NULL, EXISTS (SELECT 1 FROM collector_certificate n
				WHERE n.collector_id = p.collector_id AND n.created_at > p.created_at AND n.created_at > now() - make_interval(secs => $3))
			FROM collector_certificate p WHERE p.collector_id = $1 AND p.serial = $2`,
			collectorID, predecessor, renewalReplayWindow.Seconds()).Scan(&current, &renewed); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		switch {
		case !current:
			return errCertificateNotCurrent
		case renewed:
			return fmt.Errorf("%w: the certificate was renewed within the last %s", ErrRenewalNotDue, renewalReplayWindow)
		}
		if lerr := lockCAShared(ctx, tx); lerr != nil {
			return lerr
		}
		if cert, err = issue(ctx, orgID, collectorID); err != nil {
			return err
		}
		if rerr := recordCertificateTx(ctx, tx, orgID, c, cert); rerr != nil {
			return rerr
		}
		_, err = audit.NewPGRecorder().Record(ctx, tx, audit.Entry{OrganizationID: orgID, ActorType: "system",
			Action: "collector.certificate_issued", ResourceType: "collector", ResourceID: collectorID, Source: audit.SourceCollector,
			Changes: map[string]interface{}{"serial": cert.Serial, "predecessor_serial": predecessor, "fingerprint": cert.Fingerprint,
				"not_after": cert.NotAfter.UTC().Format(time.RFC3339)}})
		return err
	})
	return cert, err
}

// errCertificateNotCurrent: the certificate of a renewal request is no
// recorded, unrevoked certificate of the collector.
var errCertificateNotCurrent = errors.New("the certificate is no current certificate of the collector")

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
// than the dedupe window (REC-01, RecordLedger), in one statement for the
// whole batch (NFR-03). The record ids must be distinct; an expired entry of
// a record of the batch is refreshed, not deleted. The lanes of an
// organization write the ledger side by side (WP-334): each forgets at most
// 1000 expired entries and skips those another lane is deleting.
func (r *PGRepository) MarkRecordsProcessed(ctx context.Context, orgID string, records []ProcessedRecord) error {
	ids := make([]string, len(records))
	collectors := make([]string, len(records))
	observed := make([]time.Time, len(records))
	for i, rec := range records {
		ids[i], collectors[i], observed[i] = rec.RecordID, rec.CollectorID, rec.ObservedAt
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `WITH expired AS (
				DELETE FROM ingest_record WHERE (organization_id, record_id) IN (
					SELECT organization_id, record_id FROM ingest_record
					WHERE organization_id = $1 AND processed_at <= now() - $5::interval AND record_id <> ALL($2::uuid[])
					ORDER BY processed_at LIMIT 1000 FOR UPDATE SKIP LOCKED))
			INSERT INTO ingest_record (organization_id, record_id, collector_id, observed_at)
			SELECT $1, r.record_id, NULLIF(r.collector_id, ''), r.observed_at
			FROM unnest($2::uuid[], $3::text[], $4::timestamptz[]) AS r(record_id, collector_id, observed_at)
			ON CONFLICT (organization_id, record_id) DO UPDATE SET processed_at = now(), observed_at = EXCLUDED.observed_at`,
			orgID, ids, collectors, observed, RecordDedupeWindow.String()); err != nil {
			return fmt.Errorf("record processed ingest records: %w", err)
		}
		return nil
	})
}

// identityPage is the page size of the candidate query; the pages run
// until the matches are exhausted, so there is no upper bound (NFR-01).
const identityPage = 1000

// IdentityCandidates returns the ids of the CIs an incoming device may be,
// using the indexes on serial number, hardware UUID, management IP and
// hostname/FQDN plus the MAC index of ci_identity_mac for primary and
// interface MACs, over the whole inventory of the organization (REC-02,
// NFR-01). The matches are read page by page on (created_at, id) without an
// upper bound (WP-205). Soft-deleted CIs are excluded.
func (r *PGRepository) IdentityCandidates(ctx context.Context, orgID string, q *IdentityQuery) ([]string, error) {
	var ids []string
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var (
			afterCreated time.Time
			afterID      string
		)
		predicate, base, ok := identityFilter(orgID, q)
		if !ok {
			return nil
		}
		for {
			n := len(base)
			args := append(append([]any{}, base...), afterID, afterCreated, identityPage)
			rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT id::text, created_at FROM ci
				WHERE organization_id = $1 AND deleted_at IS NULL AND %s
				AND ($%d::text = '' OR (created_at, id) > ($%d::timestamptz, NULLIF($%d::text, '')::uuid))
				ORDER BY created_at, id LIMIT $%d`, predicate, n+1, n+2, n+1, n+3), args...)
			if err != nil {
				return err
			}
			page := 0
			for rows.Next() {
				if err := rows.Scan(&afterID, &afterCreated); err != nil {
					rows.Close()
					return err
				}
				ids = append(ids, afterID)
				page++
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			if page < identityPage {
				return nil
			}
		}
	})
	return ids, err
}

// DiscoveryEdge finds the edge of a type between two CIs with an index
// lookup instead of listing every edge of the source (WP-205): a core switch
// with thousands of neighbours is re-confirmed without a scan or a cap.
func (r *PGRepository) DiscoveryEdge(ctx context.Context, orgID, sourceID, targetID, relType string) (id, source string, found bool, err error) {
	err = database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		qerr := tx.QueryRow(ctx, `SELECT id::text, source FROM ci_relationship
			WHERE organization_id = $1 AND source_ci_id = $2 AND target_ci_id = $3 AND rel_type = $4
			ORDER BY created_at, id LIMIT 1`, orgID, sourceID, targetID, relType).Scan(&id, &source)
		if errors.Is(qerr, pgx.ErrNoRows) {
			return nil
		}
		if qerr != nil {
			return qerr
		}
		found = true
		return nil
	})
	return id, source, found, err
}

// DiscoveryEdges finds the edges of several (source, target, type) keys with
// one statement, each as DiscoveryEdge finds it: the oldest edge of the key
// (WP-608, NFR-03). A key without an edge is missing from the map. The
// re-discovery of a batch reads the edges of all its devices at once
// instead of one transaction per edge.
func (r *PGRepository) DiscoveryEdges(ctx context.Context, orgID string, keys []EdgeKey) (map[EdgeKey]FoundEdge, error) {
	out := make(map[EdgeKey]FoundEdge, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	sources, targets, types := make([]string, len(keys)), make([]string, len(keys)), make([]string, len(keys))
	for i, k := range keys {
		sources[i], targets[i], types[i] = k.SourceCIID, k.TargetCIID, k.RelType
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT DISTINCT ON (k.n) k.n, r.id::text, r.source
			FROM unnest($2::uuid[], $3::uuid[], $4::text[]) WITH ORDINALITY AS k(source_ci_id, target_ci_id, rel_type, n)
			JOIN ci_relationship r ON r.organization_id = $1 AND r.source_ci_id = k.source_ci_id
				AND r.target_ci_id = k.target_ci_id AND r.rel_type = k.rel_type
			ORDER BY k.n, r.created_at, r.id`, orgID, sources, targets, types)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n int
			var e FoundEdge
			if err := rows.Scan(&n, &e.ID, &e.Source); err != nil {
				return err
			}
			out[keys[n-1]] = e
		}
		return rows.Err()
	})
	return out, err
}

// DeletedIdentityCandidates returns the soft-deleted CIs an incoming device
// may be, with the fields identity resolution compares (CI-13).
func (r *PGRepository) DeletedIdentityCandidates(ctx context.Context, orgID string, q *IdentityQuery) ([]ci.Item, error) {
	var out []ci.Item
	predicate, args, ok := identityFilter(orgID, q)
	if !ok {
		return out, nil
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, ci_type_id::text, COALESCE(client_id::text, ''), name,
				COALESCE(serial_number, ''), COALESCE(hardware_uuid::text, ''), COALESCE(primary_mac::text, ''),
				COALESCE(host(management_ip), ''), COALESCE(sys_object_id, ''), COALESCE(hostname, ''), COALESCE(fqdn, ''),
				COALESCE(attributes, '{}'::jsonb), deleted_at
			FROM ci WHERE organization_id = $1 AND deleted_at IS NOT NULL AND `+predicate+`
			ORDER BY deleted_at DESC, id LIMIT 1000`, args...)
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

// identityFilter matches CIs by the indexed identity columns of q: only
// the parts q carries become terms, so the planner combines the indexes on
// serial number, hardware UUID, management IP, hostname/FQDN and the primary
// key (BitmapOr) instead of scanning the inventory (NFR-01, NFR-03).
// Hostnames compare with the stored lower-case columns (migration 000134):
// lower() is not leakproof and keeps an index unused under RLS. Primary and
// interface MACs come from ci_identity_mac (migration 000161, WP-434) with
// leakproof equality only; the ids are an array of an InitPlan, because an
// IN sub-select within the OR would be a filter on every CI of the
// organization. The arguments start at $2 after the organization; ok is
// false when q carries no identity.
func identityFilter(orgID string, q *IdentityQuery) (predicate string, args []any, ok bool) {
	args = []any{orgID}
	var terms []string
	add := func(term string, arg any) {
		args = append(args, arg)
		terms = append(terms, fmt.Sprintf(term, len(args)))
	}
	if q.SerialNumber != "" {
		add("serial_number = $%d", q.SerialNumber)
	}
	if q.HardwareUUID != "" {
		add("hardware_uuid = $%d::uuid", q.HardwareUUID)
	}
	if len(q.MACs) > 0 {
		add("id = ANY (ARRAY(SELECT ci_id FROM ci_identity_mac WHERE organization_id = $1 AND mac = ANY($%d::macaddr[])))", q.MACs)
	}
	if ip := hostOnly(q.ManagementIP); ip != "" {
		add("management_ip = $%d::inet", ip)
	}
	if len(q.Hostnames) > 0 {
		add("(hostname_lc = ANY($%[1]d::text[]) OR fqdn_lc = ANY($%[1]d::text[]))", q.Hostnames)
	}
	if len(terms) == 0 {
		return "", nil, false
	}
	return "(" + strings.Join(terms, " OR ") + ")", args, true
}

// identityBranch is one branch of the batch candidate queries (WP-333): the
// CIs one kind of identity value matches, with the kind and the ordinality n
// of the matching value in its array.
type identityBranch struct {
	name string
	sql  string
}

// identityBranches builds the branches of the batch candidate query for the
// non-empty kinds of keys, mirroring identityFilter term by term with the
// same operators: the arrays are cast on the parameter, never on the
// column, so every join stays an index condition under row level security
// (texteq, uuid_eq, macaddr_eq and network_eq are leakproof). The arguments
// follow the organization ($1). Live CIs come with created_at; deleted CIs
// with at most 1000 per value in (deleted_at DESC, id) order, a superset of
// the 1000 newest per record, which the caller caps.
func identityBranches(keys *IdentityKeys, deleted bool) (branches []identityBranch, args []any) {
	add := func(values []string) int {
		args = append(args, values)
		return len(args) + 1
	}
	live := func(name, kind, unnest, cond string) {
		branches = append(branches, identityBranch{name, fmt.Sprintf(`SELECT c.id, c.created_at, '%s'::text AS kind, k.n
			FROM unnest(%s) WITH ORDINALITY AS k(v, n) JOIN ci c ON %s
			WHERE c.organization_id = $1 AND c.deleted_at IS NULL`, kind, unnest, cond)})
	}
	gone := func(name, kind, unnest, cond string) {
		branches = append(branches, identityBranch{name, fmt.Sprintf(`SELECT '%s'::text AS kind, k.n, d.id
			FROM unnest(%s) WITH ORDINALITY AS k(v, n)
			CROSS JOIN LATERAL (SELECT c.id FROM ci c WHERE c.organization_id = $1 AND c.deleted_at IS NOT NULL AND %s
				ORDER BY c.deleted_at DESC, c.id LIMIT 1000) d`, kind, unnest, cond)})
	}
	branch := func(name, kind, unnest, liveCond, goneCond string) {
		if deleted {
			gone(name, kind, unnest, goneCond)
		} else {
			live(name, kind, unnest, liveCond)
		}
	}
	if len(keys.Serials) > 0 {
		branch("serial", "serial", fmt.Sprintf("$%d::text[]", add(keys.Serials)), "c.serial_number = k.v", "c.serial_number = k.v")
	}
	if len(keys.UUIDs) > 0 {
		branch("uuid", "uuid", fmt.Sprintf("$%d::text[]::uuid[]", add(keys.UUIDs)), "c.hardware_uuid = k.v", "c.hardware_uuid = k.v")
	}
	if len(keys.MACs) > 0 {
		// Primary and interface MACs come from ci_identity_mac (migration
		// 000161, WP-434): (organization_id, mac) and the join on the
		// primary key of ci are index conditions; one row per CI and MAC.
		// The live branch repeats the values as mac = ANY on the side
		// table, so its scan reads only the batch's MACs whatever join
		// order the planner picks (with outdated statistics it may start
		// from the side table and leave mac = k.v to the join). The
		// parameter is bound as macaddr[]: a cast from text[] is no
		// leakproof expression and would stay above the RLS barrier.
		n := add(keys.MACs)
		if deleted {
			branches = append(branches, identityBranch{"mac", fmt.Sprintf(`SELECT 'mac'::text AS kind, k.n, d.id
			FROM unnest($%d::macaddr[]) WITH ORDINALITY AS k(v, n)
			CROSS JOIN LATERAL (SELECT c.id FROM ci_identity_mac m JOIN ci c ON c.id = m.ci_id
				WHERE m.organization_id = $1 AND m.mac = k.v AND c.organization_id = $1 AND c.deleted_at IS NOT NULL
				ORDER BY c.deleted_at DESC, c.id LIMIT 1000) d`, n)})
		} else {
			branches = append(branches, identityBranch{"mac", fmt.Sprintf(`SELECT c.id, c.created_at, 'mac'::text AS kind, k.n
			FROM unnest($%[1]d::macaddr[]) WITH ORDINALITY AS k(v, n)
			JOIN ci_identity_mac m ON m.organization_id = $1 AND m.mac = k.v AND m.mac = ANY($%[1]d::macaddr[])
			JOIN ci c ON c.id = m.ci_id
			WHERE c.organization_id = $1 AND c.deleted_at IS NULL`, n)})
		}
	}
	if len(keys.IPs) > 0 {
		branch("ip", "ip", fmt.Sprintf("$%d::text[]::inet[]", add(keys.IPs)), "c.management_ip = k.v", "c.management_ip = k.v")
	}
	if len(keys.Hostnames) > 0 {
		n := add(keys.Hostnames)
		if deleted {
			gone("host", "host", fmt.Sprintf("$%d::text[]", n), "(c.hostname_lc = k.v OR c.fqdn_lc = k.v)")
		} else {
			live("hostname", "host", fmt.Sprintf("$%d::text[]", n), "c.hostname_lc = k.v")
			live("fqdn", "host", fmt.Sprintf("$%d::text[]", n), "c.fqdn_lc = k.v")
		}
	}
	return branches, args
}

// identityHitsSQL is the live candidate query of branches, read page by
// page on (created_at, id, kind, n) from parameter first on. UNION drops the
// repeated hit of a host name that is hostname and FQDN (a MAC that is
// primary and in the fingerprint is one row of ci_identity_mac).
func identityHitsSQL(branches []identityBranch, first int) string {
	parts := make([]string, len(branches))
	for i, b := range branches {
		parts[i] = b.sql
	}
	return fmt.Sprintf(`/* ingest identity hits */ SELECT h.id::text, h.created_at, h.kind, h.n FROM (%s) h
		WHERE $%[2]d::text = '' OR (h.created_at, h.id, h.kind, h.n) > ($%[3]d::timestamptz, NULLIF($%[2]d::text, '')::uuid, $%[4]d::text, $%[5]d::bigint)
		ORDER BY h.created_at, h.id, h.kind, h.n LIMIT $%[6]d`,
		strings.Join(parts, "\n\t\tUNION\n\t\t"), first, first+1, first+2, first+3, first+4)
}

// deletedHitsSQL is the deleted candidate query of branches.
func deletedHitsSQL(branches []identityBranch) string {
	parts := make([]string, len(branches))
	for i, b := range branches {
		parts[i] = b.sql
	}
	return `/* ingest deleted identity hits */ WITH hit AS (` + strings.Join(parts, "\n\t\tUNION\n\t\t") + `)
		SELECT hit.kind, hit.n, c.id::text, c.ci_type_id::text, COALESCE(c.client_id::text, ''), c.name,
			COALESCE(c.serial_number, ''), COALESCE(c.hardware_uuid::text, ''), COALESCE(c.primary_mac::text, ''),
			COALESCE(host(c.management_ip), ''), COALESCE(c.sys_object_id, ''), COALESCE(c.hostname, ''),
			COALESCE(c.fqdn, ''), COALESCE(c.attributes, '{}'::jsonb), c.deleted_at
		FROM hit JOIN ci c ON c.id = hit.id
		ORDER BY c.deleted_at DESC, c.id, hit.kind, hit.n`
}

// IdentityHits implements BatchIdentityFinder: the live CIs matching the
// identity values of a whole batch with one query, each with the value it
// matched, read page by page without an upper bound like
// IdentityCandidates (NFR-03, WP-333).
func (r *PGRepository) IdentityHits(ctx context.Context, orgID string, keys *IdentityKeys) ([]IdentityHit, error) {
	branches, base := identityBranches(keys, false)
	if len(branches) == 0 {
		return nil, nil
	}
	query := identityHitsSQL(branches, len(base)+2)
	var hits []IdentityHit
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var (
			afterCreated time.Time
			afterID      string
			afterKind    string
			afterN       int64
		)
		for {
			args := append(append([]any{orgID}, base...), afterID, afterCreated, afterKind, afterN, identityPage)
			rows, err := tx.Query(ctx, query, args...)
			if err != nil {
				return err
			}
			page := 0
			for rows.Next() {
				if err := rows.Scan(&afterID, &afterCreated, &afterKind, &afterN); err != nil {
					rows.Close()
					return err
				}
				hits = append(hits, IdentityHit{CIID: afterID, Key: IdentityKey{Kind: afterKind, Index: int(afterN) - 1}})
				page++
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			if page < identityPage {
				return nil
			}
		}
	})
	return hits, err
}

// DeletedIdentityHits implements BatchIdentityFinder: the soft-deleted CIs
// matching the identity values of a batch with one query, at most 1000 per
// value, with the fields identity resolution compares (CI-13, WP-333).
func (r *PGRepository) DeletedIdentityHits(ctx context.Context, orgID string, keys *IdentityKeys) ([]DeletedIdentityHit, error) {
	branches, base := identityBranches(keys, true)
	if len(branches) == 0 {
		return nil, nil
	}
	var hits []DeletedIdentityHit
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, deletedHitsSQL(branches), append([]any{orgID}, base...)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var hit DeletedIdentityHit
			var n int64
			var deletedAt time.Time
			item := &hit.CI
			item.OrganizationID = orgID
			if err := rows.Scan(&hit.Key.Kind, &n, &item.ID, &item.CITypeID, &item.ClientID, &item.Name, &item.SerialNumber,
				&item.HardwareUUID, &item.PrimaryMAC, &item.ManagementIP, &item.SysObjectID, &item.Hostname, &item.FQDN,
				&item.Attributes, &deletedAt); err != nil {
				return err
			}
			hit.Key.Index = int(n) - 1
			item.DeletedAt = &deletedAt
			hits = append(hits, hit)
		}
		return rows.Err()
	})
	return hits, err
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
