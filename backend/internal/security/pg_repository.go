package security

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const findingCols = `id::text, organization_id::text, COALESCE(ci_id::text,''), kind, severity, title, COALESCE(detail,''), COALESCE(package_name,''), COALESCE(installed_version,''), COALESCE(fixed_version,''), COALESCE(reference,''), status, detected_at, resolved_at, created_at, updated_at`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a PostgreSQL-backed security repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

func (r *PGRepository) withTenant(ctx context.Context, orgID string, fn func(context.Context, pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('app.org_id', $1, true)", orgID); err != nil {
		return fmt.Errorf("set tenant context: %w", err)
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func scanFinding(s pgx.Row) (*Finding, error) {
	f := &Finding{}
	if err := s.Scan(&f.ID, &f.OrganizationID, &f.CIID, &f.Kind, &f.Severity, &f.Title, &f.Detail, &f.PackageName, &f.InstalledVersion, &f.FixedVersion, &f.Reference, &f.Status, &f.DetectedAt, &f.ResolvedAt, &f.CreatedAt, &f.UpdatedAt); err != nil {
		return nil, err
	}
	return f, nil
}

func (r *PGRepository) List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Finding, int, error) {
	out := []Finding{}
	var total int
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := []string{"organization_id = $1"}
		args := []any{orgID}
		pos := 2
		for col, val := range map[string]string{"ci_id": filter.CIID, "kind": filter.Kind, "severity": filter.Severity, "status": filter.Status} {
			if val != "" {
				where = append(where, fmt.Sprintf("%s = $%d", col, pos))
				args = append(args, val)
				pos++
			}
		}
		clause := strings.Join(where, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM security_finding WHERE "+clause, args...).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+findingCols+" FROM security_finding WHERE "+clause+fmt.Sprintf(" ORDER BY detected_at DESC LIMIT $%d OFFSET $%d", pos, pos+1), append(args, page.Limit, page.Offset)...)
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
	var f *Finding
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		f, err = scanFinding(tx.QueryRow(ctx, "SELECT "+findingCols+" FROM security_finding WHERE organization_id = $1 AND id = $2", orgID, id))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("finding not found")
		}
		return err
	})
	return f, err
}

func (r *PGRepository) Create(ctx context.Context, f *Finding) error {
	return r.withTenant(ctx, f.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if f.Status == "" {
			f.Status = "open"
		}
		if f.Severity == "" {
			f.Severity = "medium"
		}
		if f.DetectedAt.IsZero() {
			f.DetectedAt = time.Now().UTC()
		}
		return tx.QueryRow(ctx, `
			INSERT INTO security_finding (organization_id, ci_id, kind, severity, title, detail, package_name, installed_version, fixed_version, reference, status, detected_at)
			VALUES ($1, NULLIF($2,'')::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			RETURNING id::text, created_at, updated_at
		`, f.OrganizationID, f.CIID, f.Kind, f.Severity, f.Title, f.Detail, f.PackageName, f.InstalledVersion, f.FixedVersion, f.Reference, f.Status, f.DetectedAt).
			Scan(&f.ID, &f.CreatedAt, &f.UpdatedAt)
	})
}

func (r *PGRepository) Update(ctx context.Context, orgID, id string, req UpdateFindingRequest) (*Finding, error) {
	var f *Finding
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		sets := []string{"updated_at = now()"}
		args := []any{orgID, id}
		pos := 3
		if req.Status != nil {
			sets = append(sets, fmt.Sprintf("status = $%d", pos))
			args = append(args, *req.Status)
			pos++
			if *req.Status == "resolved" || *req.Status == "false_positive" {
				sets = append(sets, "resolved_at = now()")
			} else {
				sets = append(sets, "resolved_at = NULL")
			}
		}
		var err error
		f, err = scanFinding(tx.QueryRow(ctx, "UPDATE security_finding SET "+strings.Join(sets, ", ")+" WHERE organization_id = $1 AND id = $2 RETURNING "+findingCols, args...))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("finding not found")
		}
		return err
	})
	return f, err
}

func (r *PGRepository) Summary(ctx context.Context, orgID string) (map[string]int, error) {
	out := map[string]int{}
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT severity, COUNT(*) FROM security_finding WHERE organization_id = $1 AND status = 'open' GROUP BY severity", orgID)
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
