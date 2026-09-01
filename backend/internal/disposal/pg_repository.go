package disposal

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const recordCols = `id::text, organization_id::text, COALESCE(asset_id::text,''), COALESCE(ci_id::text,''), method, COALESCE(certificate_ref,''), COALESCE(data_carrier,''), COALESCE(performed_by,''), performed_at, COALESCE(notes,''), created_at`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a PostgreSQL-backed disposal repository.
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

func scanRecord(s pgx.Row) (*Record, error) {
	rec := &Record{}
	if err := s.Scan(&rec.ID, &rec.OrganizationID, &rec.AssetID, &rec.CIID, &rec.Method, &rec.CertificateRef, &rec.DataCarrier, &rec.PerformedBy, &rec.PerformedAt, &rec.Notes, &rec.CreatedAt); err != nil {
		return nil, err
	}
	return rec, nil
}

func (r *PGRepository) List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Record, int, error) {
	out := []Record{}
	var total int
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := []string{"organization_id = $1"}
		args := []any{orgID}
		pos := 2
		if filter.Method != "" {
			where = append(where, fmt.Sprintf("method = $%d", pos))
			args = append(args, filter.Method)
			pos++
		}
		if filter.AssetID != "" {
			where = append(where, fmt.Sprintf("asset_id = $%d", pos))
			args = append(args, filter.AssetID)
			pos++
		}
		if filter.CIID != "" {
			where = append(where, fmt.Sprintf("ci_id = $%d", pos))
			args = append(args, filter.CIID)
			pos++
		}
		clause := strings.Join(where, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM disposal_record WHERE "+clause, args...).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+recordCols+" FROM disposal_record WHERE "+clause+fmt.Sprintf(" ORDER BY performed_at DESC LIMIT $%d OFFSET $%d", pos, pos+1), append(args, page.Limit, page.Offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			rec, err := scanRecord(rows)
			if err != nil {
				return err
			}
			out = append(out, *rec)
		}
		return rows.Err()
	})
	return out, total, err
}

func (r *PGRepository) GetByID(ctx context.Context, orgID, id string) (*Record, error) {
	var rec *Record
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rec, err = scanRecord(tx.QueryRow(ctx, "SELECT "+recordCols+" FROM disposal_record WHERE organization_id = $1 AND id = $2", orgID, id))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("disposal record not found")
		}
		return err
	})
	return rec, err
}

func (r *PGRepository) Create(ctx context.Context, rec *Record) error {
	return r.withTenant(ctx, rec.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if rec.PerformedAt.IsZero() {
			rec.PerformedAt = time.Now().UTC()
		}
		return tx.QueryRow(ctx, `
			INSERT INTO disposal_record (organization_id, asset_id, ci_id, method, certificate_ref, data_carrier, performed_by, performed_at, notes)
			VALUES ($1, NULLIF($2,'')::uuid, NULLIF($3,'')::uuid, $4, $5, $6, $7, $8, $9)
			RETURNING id::text, created_at
		`, rec.OrganizationID, rec.AssetID, rec.CIID, rec.Method, rec.CertificateRef, rec.DataCarrier, rec.PerformedBy, rec.PerformedAt, rec.Notes).
			Scan(&rec.ID, &rec.CreatedAt)
	})
}
