package security

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
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

const findingCols = `id::text, organization_id::text, COALESCE(ci_id::text,''), COALESCE(client_id::text,''), COALESCE(site_id::text,''),
	kind, severity, title, COALESCE(detail,''), COALESCE(package_name,''), COALESCE(installed_version,''), COALESCE(fixed_version,''),
	COALESCE(reference,''), status, COALESCE(status_reason,''), source, cve_ids, cvss_score::float8, COALESCE(cvss_vector,''), kev,
	COALESCE(advisory_id,''), fingerprint, detected_at, last_seen_at, resolved_at, created_at, updated_at`

// PGRepository implements Repository backed by PostgreSQL with RLS. Its
// writes append finding.* to the outbox as the last write of their
// transaction (Konvention 20): an outbox error rolls the change back.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a PostgreSQL-backed security repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

// visibleCI restricts findings about a CI to CIs visible under the
// transaction's tenant scope (the ci policy filters the subquery); the
// scope columns of security_finding (000061) are checked by its policies
// as well. Findings without a CI stay organization-wide.
const visibleCI = "(security_finding.ci_id IS NULL OR EXISTS (SELECT 1 FROM ci WHERE ci.id = security_finding.ci_id))"

func scanFinding(s pgx.Row, extra ...any) (*Finding, error) {
	f := &Finding{}
	dest := []any{&f.ID, &f.OrganizationID, &f.CIID, &f.ClientID, &f.SiteID,
		&f.Kind, &f.Severity, &f.Title, &f.Detail, &f.PackageName, &f.InstalledVersion, &f.FixedVersion,
		&f.Reference, &f.Status, &f.StatusReason, &f.Source, &f.CVEIDs, &f.CVSSScore, &f.CVSSVector, &f.KEV,
		&f.AdvisoryID, &f.Fingerprint, &f.DetectedAt, &f.LastSeenAt, &f.ResolvedAt, &f.CreatedAt, &f.UpdatedAt}
	if err := s.Scan(append(dest, extra...)...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if f.CVEIDs == nil {
		f.CVEIDs = []string{}
	}
	return f, nil
}

// emit appends the finding.* event of f to the outbox right before the
// commit of tx.
func emit(ctx context.Context, tx pgx.Tx, typ string, f *Finding) error {
	data := eventData(f)
	return database.BeforeCommit(database.ContextWithTx(ctx, tx), func() error {
		_, err := events.NewOutbox().Append(ctx, tx, &events.Event{OrgID: f.OrganizationID, Type: typ, EntityID: f.ID, Data: data})
		return err
	})
}

func (r *PGRepository) List(ctx context.Context, orgID string, filter *FilterParams, page api.PaginationParams) ([]Finding, int, error) {
	out := []Finding{}
	if filter.CIID != "" && !api.IsUUID(filter.CIID) {
		return out, 0, nil
	}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := []string{"organization_id = $1", visibleCI}
		args := []any{orgID}
		for _, c := range []struct{ col, val string }{
			{"ci_id", filter.CIID}, {"kind", filter.Kind}, {"severity", filter.Severity}, {"status", filter.Status},
			{"source", filter.Source},
		} {
			if c.val != "" {
				args = append(args, c.val)
				where = append(where, fmt.Sprintf("%s = $%d", c.col, len(args)))
			}
		}
		if filter.CVE != "" {
			args = append(args, []string{filter.CVE})
			where = append(where, fmt.Sprintf("cve_ids @> $%d::text[]", len(args)))
		}
		clause := strings.Join(where, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM security_finding WHERE "+clause, args...).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+findingCols+" FROM security_finding WHERE "+clause+
			fmt.Sprintf(" ORDER BY detected_at DESC, id DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2),
			append(args, page.Limit, page.Offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			f, err := scanFinding(rows)
			if err != nil {
				return err
			}
			out = append(out, *f)
		}
		return rows.Err()
	})
	return out, total, err
}

func (r *PGRepository) GetByID(ctx context.Context, orgID, id string) (*Finding, error) {
	if !api.IsUUID(id) {
		return nil, ErrNotFound
	}
	var f *Finding
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		f, err = scanFinding(tx.QueryRow(ctx, "SELECT "+findingCols+" FROM security_finding WHERE organization_id = $1 AND id = $2 AND "+visibleCI, orgID, id))
		return err
	})
	return f, err
}

// Create implements Repository.
func (r *PGRepository) Create(ctx context.Context, f *Finding) error {
	_, err := r.Upsert(ctx, f)
	return err
}

// Upsert implements Repository: the existing finding of the fingerprint is
// read and locked (FOR UPDATE), then one INSERT … ON CONFLICT writes it, so
// the event follows what the statement did.
func (r *PGRepository) Upsert(ctx context.Context, f *Finding) (UpsertResult, error) {
	prepare(f)
	if f.DetectedAt.IsZero() {
		f.DetectedAt = time.Now().UTC()
	}
	var res UpsertResult
	err := database.WithRequestTenant(ctx, r.pool, f.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if f.CIID != "" {
			var visible bool
			if !api.IsUUID(f.CIID) {
				return errCINotFound
			}
			if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM ci WHERE id = $1::uuid)", f.CIID).Scan(&visible); err != nil {
				return fmt.Errorf("check ci visibility: %w", err)
			}
			if !visible {
				return errCINotFound
			}
		}
		// The previous status and severity, locked until the commit; a
		// concurrent first report inserts meanwhile and is updated below
		// without event.
		var prevStatus, prevSeverity *string
		err := tx.QueryRow(ctx, `SELECT status, severity FROM security_finding WHERE organization_id = $1 AND fingerprint = $2 FOR UPDATE`,
			f.OrganizationID, f.Fingerprint).Scan(&prevStatus, &prevSeverity)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("read finding: %w", err)
		}
		var inserted bool
		row, err := scanFinding(tx.QueryRow(ctx, `
			INSERT INTO security_finding AS s (organization_id, ci_id, kind, severity, title, detail, package_name,
				installed_version, fixed_version, reference, status, detected_at, source, cve_ids, cvss_score, cvss_vector,
				kev, advisory_id, fingerprint, last_seen_at)
			VALUES ($1, NULLIF($3, '')::uuid, $4, $5, $6, NULLIF($7, ''), NULLIF($8, ''), NULLIF($9, ''), NULLIF($10, ''),
				NULLIF($11, ''), $12, $13, $14, $15, $16, NULLIF($17, ''), $18, NULLIF($19, ''), $2, now())
			ON CONFLICT (organization_id, fingerprint) DO UPDATE SET
				last_seen_at = now(),
				severity = EXCLUDED.severity,
				title = EXCLUDED.title,
				detail = EXCLUDED.detail,
				package_name = EXCLUDED.package_name,
				installed_version = EXCLUDED.installed_version,
				cve_ids = EXCLUDED.cve_ids,
				cvss_score = EXCLUDED.cvss_score,
				cvss_vector = EXCLUDED.cvss_vector,
				kev = EXCLUDED.kev,
				advisory_id = EXCLUDED.advisory_id,
				fixed_version = EXCLUDED.fixed_version,
				status = CASE WHEN s.status = 'resolved' THEN 'open' ELSE s.status END,
				status_reason = CASE WHEN s.status = 'resolved' THEN 'reappeared' ELSE s.status_reason END,
				resolved_at = CASE WHEN s.status = 'resolved' THEN NULL ELSE s.resolved_at END,
				updated_at = CASE WHEN s.status = 'resolved'
				                    OR (s.severity, s.title, s.detail, s.package_name, s.installed_version, s.cve_ids, s.cvss_score,
				                        s.cvss_vector, s.kev, s.advisory_id, s.fixed_version)
				                       IS DISTINCT FROM (EXCLUDED.severity, EXCLUDED.title, EXCLUDED.detail, EXCLUDED.package_name,
				                       EXCLUDED.installed_version, EXCLUDED.cve_ids, EXCLUDED.cvss_score,
				                       EXCLUDED.cvss_vector, EXCLUDED.kev, EXCLUDED.advisory_id, EXCLUDED.fixed_version)
				                  THEN now() ELSE s.updated_at END
			RETURNING `+findingCols+`, (xmax = 0)`,
			f.OrganizationID, f.Fingerprint, f.CIID, f.Kind, f.Severity, f.Title, f.Detail, f.PackageName,
			f.InstalledVersion, f.FixedVersion, f.Reference, f.Status, f.DetectedAt, f.Source, f.CVEIDs, f.CVSSScore,
			f.CVSSVector, f.KEV, f.AdvisoryID), &inserted)
		if err != nil {
			return api.FieldDBError(err, map[string]string{
				"security_finding_kind_check": "kind", "security_finding_severity_check": "severity",
				"security_finding_source_check": "source", "security_finding_cvss_score_check": "cvss_score",
			})
		}
		switch {
		case inserted:
			res.Created = true
		case prevStatus != nil:
			res.Reappeared = *prevStatus == StatusResolved
			res.SeverityChanged = prevSeverity != nil && *prevSeverity != row.Severity
		}
		*f = *row
		if typ := res.EventType(); typ != "" {
			return emit(ctx, tx, typ, row)
		}
		return nil
	})
	return res, err
}

// Update implements Repository: the status is read and locked, the
// transition checked and the change written with its event in one
// transaction.
func (r *PGRepository) Update(ctx context.Context, orgID, id string, req UpdateFindingRequest) (*Finding, error) {
	if req.Status == nil {
		return nil, errNoStatus
	}
	if !api.IsUUID(id) {
		return nil, ErrNotFound
	}
	var f *Finding
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var from string
		err := tx.QueryRow(ctx, "SELECT status FROM security_finding WHERE organization_id = $1 AND id = $2 AND "+visibleCI+" FOR UPDATE",
			orgID, id).Scan(&from)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if terr := CheckTransition(from, *req.Status, req.StatusReason); terr != nil {
			return terr
		}
		f, err = scanFinding(tx.QueryRow(ctx, `UPDATE security_finding
			SET status = $3, status_reason = $4, updated_at = now(),
			    resolved_at = CASE WHEN $3 = 'resolved' THEN now() END
			WHERE organization_id = $1 AND id = $2
			RETURNING `+findingCols, orgID, id, *req.Status, req.StatusReason))
		if err != nil {
			return err
		}
		return emit(ctx, tx, statusEventType(f.Status), f)
	})
	if err != nil {
		return nil, err
	}
	return f, nil
}

// FindingsByCIs returns, for the BFF (GQL-02, WP-1136), up to perCI
// findings of each CI of ciIDs whose status is one of statuses (empty:
// every status), newest first per CI, in one statement (row_number per
// ci_id) under row level security and the scope of ctx.
func (r *PGRepository) FindingsByCIs(ctx context.Context, orgID string, ciIDs, statuses []string, perCI int) ([]Finding, error) {
	out := []Finding{}
	if len(ciIDs) == 0 {
		return out, nil
	}
	if statuses == nil {
		statuses = []string{}
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+findingCols+` FROM (
				SELECT security_finding.*, row_number() OVER (PARTITION BY ci_id ORDER BY detected_at DESC, id DESC) AS rn
				FROM security_finding
				WHERE organization_id = $1 AND ci_id = ANY ($2::uuid[]) AND (cardinality($3::text[]) = 0 OR status = ANY ($3::text[]))
				  AND `+visibleCI+`
			) security_finding WHERE rn <= $4 ORDER BY ci_id, rn`, orgID, ciIDs, statuses, perCI)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			f, err := scanFinding(rows)
			if err != nil {
				return err
			}
			out = append(out, *f)
		}
		return rows.Err()
	})
	return out, err
}

func (r *PGRepository) Summary(ctx context.Context, orgID string) (map[string]int, error) {
	out := map[string]int{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT severity, COUNT(*) FROM security_finding WHERE organization_id = $1 AND status = 'open' AND "+visibleCI+" GROUP BY severity", orgID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var sev string
			var n int
			if err := rows.Scan(&sev, &n); err != nil {
				return err
			}
			out[sev] = n
		}
		return rows.Err()
	})
	return out, err
}
