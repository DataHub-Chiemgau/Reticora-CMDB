package rack

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a new PostgreSQL-backed rack repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

func (r *PGRepository) withTenant(ctx context.Context, orgID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
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

type scanner interface {
	Scan(dest ...any) error
}

const rackCols = `id::text, organization_id::text, room_id::text, name, height_u, width_mm, depth_mm, COALESCE(notes,''), created_at, updated_at`

func scanRack(s scanner) (*Rack, error) {
	rk := &Rack{}
	if err := s.Scan(&rk.ID, &rk.OrganizationID, &rk.RoomID, &rk.Name, &rk.HeightU, &rk.WidthMM, &rk.DepthMM, &rk.Notes, &rk.CreatedAt, &rk.UpdatedAt); err != nil {
		return nil, err
	}
	rk.CreatedAt = rk.CreatedAt.UTC()
	rk.UpdatedAt = rk.UpdatedAt.UTC()
	return rk, nil
}

func (r *PGRepository) ListRacks(ctx context.Context, orgID, roomID string, page api.PaginationParams) ([]Rack, int, error) {
	var out []Rack
	var total int
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := "organization_id = $1"
		args := []any{orgID}
		if roomID != "" {
			where += " AND room_id = $2"
			args = append(args, roomID)
		}
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM rack WHERE "+where, args...).Scan(&total); err != nil {
			return err
		}
		q := "SELECT " + rackCols + " FROM rack WHERE " + where + fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
		args = append(args, page.Limit, page.Offset)
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			rk, err := scanRack(rows)
			if err != nil {
				return err
			}
			out = append(out, *rk)
		}
		return rows.Err()
	})
	return out, total, err
}

func (r *PGRepository) GetRack(ctx context.Context, orgID, id string) (*Rack, error) {
	var rk *Rack
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rk, err = scanRack(tx.QueryRow(ctx, "SELECT "+rackCols+" FROM rack WHERE organization_id = $1 AND id = $2", orgID, id))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return rk, err
}

func (r *PGRepository) CreateRack(ctx context.Context, rk *Rack) error {
	return r.withTenant(ctx, rk.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO rack (organization_id, room_id, name, height_u, width_mm, depth_mm, notes)
			 VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id::text, created_at, updated_at`,
			rk.OrganizationID, rk.RoomID, rk.Name, rk.HeightU, rk.WidthMM, rk.DepthMM, nilIfEmpty(rk.Notes),
		).Scan(&rk.ID, &rk.CreatedAt, &rk.UpdatedAt)
	})
}

func (r *PGRepository) UpdateRack(ctx context.Context, orgID, id string, req UpdateRackRequest) (*Rack, error) {
	var rk *Rack
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		sets := []string{}
		args := []any{orgID, id}
		pos := 3
		if req.Name != nil {
			sets = append(sets, fmt.Sprintf("name = $%d", pos))
			args = append(args, *req.Name)
			pos++
		}
		if req.HeightU != nil {
			sets = append(sets, fmt.Sprintf("height_u = $%d", pos))
			args = append(args, *req.HeightU)
			pos++
		}
		if req.WidthMM != nil {
			sets = append(sets, fmt.Sprintf("width_mm = $%d", pos))
			args = append(args, *req.WidthMM)
			pos++
		}
		if req.DepthMM != nil {
			sets = append(sets, fmt.Sprintf("depth_mm = $%d", pos))
			args = append(args, *req.DepthMM)
			pos++
		}
		if req.Notes != nil {
			sets = append(sets, fmt.Sprintf("notes = $%d", pos))
			args = append(args, nilIfEmpty(*req.Notes))
			pos++
		}
		var err error
		if len(sets) == 0 {
			rk, err = scanRack(tx.QueryRow(ctx, "SELECT "+rackCols+" FROM rack WHERE organization_id = $1 AND id = $2", orgID, id))
		} else {
			sets = append(sets, "updated_at = NOW()")
			q := "UPDATE rack SET " + strings.Join(sets, ", ") + " WHERE organization_id = $1 AND id = $2 RETURNING " + rackCols
			rk, err = scanRack(tx.QueryRow(ctx, q, args...))
		}
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return rk, err
}

func (r *PGRepository) DeleteRack(ctx context.Context, orgID, id string) error {
	return r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "DELETE FROM rack WHERE organization_id = $1 AND id = $2", orgID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

const mountCols = `id::text, organization_id::text, rack_id::text, ci_id::text, position_u, height_u, face, created_at, updated_at`

func scanMount(s scanner) (*RackMount, error) {
	m := &RackMount{}
	if err := s.Scan(&m.ID, &m.OrganizationID, &m.RackID, &m.CIID, &m.PositionU, &m.HeightU, &m.Face, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return nil, err
	}
	m.CreatedAt = m.CreatedAt.UTC()
	m.UpdatedAt = m.UpdatedAt.UTC()
	return m, nil
}

// mountsForRackTx loads all mounts for a rack within an existing transaction.
func mountsForRackTx(ctx context.Context, tx pgx.Tx, orgID, rackID string) ([]RackMount, error) {
	rows, err := tx.Query(ctx, "SELECT "+mountCols+" FROM rack_mount WHERE organization_id = $1 AND rack_id = $2", orgID, rackID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RackMount
	for rows.Next() {
		m, err := scanMount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func (r *PGRepository) ListMounts(ctx context.Context, orgID, rackID string, page api.PaginationParams) ([]RackMount, int, error) {
	var out []RackMount
	var total int
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM rack WHERE organization_id = $1 AND id = $2)", orgID, rackID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrRackNotFound
		}
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM rack_mount WHERE organization_id = $1 AND rack_id = $2", orgID, rackID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+mountCols+" FROM rack_mount WHERE organization_id = $1 AND rack_id = $2 ORDER BY position_u LIMIT $3 OFFSET $4", orgID, rackID, page.Limit, page.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			m, err := scanMount(rows)
			if err != nil {
				return err
			}
			out = append(out, *m)
		}
		return rows.Err()
	})
	return out, total, err
}

func (r *PGRepository) CreateMount(ctx context.Context, m *RackMount) error {
	return r.withTenant(ctx, m.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		var height int
		err := tx.QueryRow(ctx, "SELECT height_u FROM rack WHERE organization_id = $1 AND id = $2", m.OrganizationID, m.RackID).Scan(&height)
		if err == pgx.ErrNoRows {
			return ErrRackNotFound
		}
		if err != nil {
			return err
		}
		existing, err := mountsForRackTx(ctx, tx, m.OrganizationID, m.RackID)
		if err != nil {
			return err
		}
		if err := validateMountFit(height, m.PositionU, m.HeightU, m.Face, existing, ""); err != nil {
			return err
		}
		err = tx.QueryRow(ctx,
			`INSERT INTO rack_mount (organization_id, rack_id, ci_id, position_u, height_u, face)
			 VALUES ($1,$2,$3,$4,$5,$6) RETURNING id::text, created_at, updated_at`,
			m.OrganizationID, m.RackID, m.CIID, m.PositionU, m.HeightU, m.Face,
		).Scan(&m.ID, &m.CreatedAt, &m.UpdatedAt)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrValidation, err)
		}
		return nil
	})
}

func (r *PGRepository) GetMount(ctx context.Context, orgID, id string) (*RackMount, error) {
	var m *RackMount
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		m, err = scanMount(tx.QueryRow(ctx, "SELECT "+mountCols+" FROM rack_mount WHERE organization_id = $1 AND id = $2", orgID, id))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return m, err
}

func (r *PGRepository) UpdateMount(ctx context.Context, orgID, id string, req UpdateMountRequest) (*RackMount, error) {
	var m *RackMount
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := scanMount(tx.QueryRow(ctx, "SELECT "+mountCols+" FROM rack_mount WHERE organization_id = $1 AND id = $2", orgID, id))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		pos, height, face := current.PositionU, current.HeightU, current.Face
		if req.PositionU != nil {
			pos = *req.PositionU
		}
		if req.HeightU != nil {
			height = *req.HeightU
		}
		if req.Face != nil {
			face = *req.Face
			if !ValidFaces[face] {
				return fmt.Errorf("%w: invalid face", ErrValidation)
			}
		}
		var rackHeight int
		if err := tx.QueryRow(ctx, "SELECT height_u FROM rack WHERE organization_id = $1 AND id = $2", orgID, current.RackID).Scan(&rackHeight); err != nil {
			return err
		}
		existing, err := mountsForRackTx(ctx, tx, orgID, current.RackID)
		if err != nil {
			return err
		}
		if err := validateMountFit(rackHeight, pos, height, face, existing, id); err != nil {
			return err
		}
		m, err = scanMount(tx.QueryRow(ctx,
			`UPDATE rack_mount SET position_u = $3, height_u = $4, face = $5, updated_at = NOW()
			 WHERE organization_id = $1 AND id = $2 RETURNING `+mountCols,
			orgID, id, pos, height, face))
		return err
	})
	return m, err
}

func (r *PGRepository) DeleteMount(ctx context.Context, orgID, id string) error {
	return r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "DELETE FROM rack_mount WHERE organization_id = $1 AND id = $2", orgID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

const cableCols = `id::text, organization_id::text, COALESCE(label,''), cable_type, length_m, COALESCE(color,''),
	COALESCE(source_interface_id::text,''), COALESCE(target_interface_id::text,''), status, installed_at, created_at, updated_at`

func scanCable(s scanner) (*Cable, error) {
	c := &Cable{}
	var installedAt *time.Time
	if err := s.Scan(&c.ID, &c.OrganizationID, &c.Label, &c.CableType, &c.LengthM, &c.Color,
		&c.SourceInterfaceID, &c.TargetInterfaceID, &c.Status, &installedAt, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	if installedAt != nil {
		s := installedAt.UTC().Format(time.RFC3339)
		c.InstalledAt = &s
	}
	c.CreatedAt = c.CreatedAt.UTC()
	c.UpdatedAt = c.UpdatedAt.UTC()
	return c, nil
}

func (r *PGRepository) ListCables(ctx context.Context, orgID string, page api.PaginationParams) ([]Cable, int, error) {
	var out []Cable
	var total int
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM cable WHERE organization_id = $1", orgID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+cableCols+" FROM cable WHERE organization_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3", orgID, page.Limit, page.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCable(rows)
			if err != nil {
				return err
			}
			out = append(out, *c)
		}
		return rows.Err()
	})
	return out, total, err
}

func (r *PGRepository) GetCable(ctx context.Context, orgID, id string) (*Cable, error) {
	var c *Cable
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		c, err = scanCable(tx.QueryRow(ctx, "SELECT "+cableCols+" FROM cable WHERE organization_id = $1 AND id = $2", orgID, id))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return c, err
}

func (r *PGRepository) CreateCable(ctx context.Context, c *Cable) error {
	return r.withTenant(ctx, c.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO cable (organization_id, label, cable_type, length_m, color, source_interface_id, target_interface_id, status, installed_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id::text, created_at, updated_at`,
			c.OrganizationID, nilIfEmpty(c.Label), c.CableType, c.LengthM, nilIfEmpty(c.Color),
			nilIfEmpty(c.SourceInterfaceID), nilIfEmpty(c.TargetInterfaceID), c.Status, nilIfEmptyPtr(c.InstalledAt),
		).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
	})
}

func (r *PGRepository) UpdateCable(ctx context.Context, orgID, id string, req UpdateCableRequest) (*Cable, error) {
	var c *Cable
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		sets := []string{}
		args := []any{orgID, id}
		pos := 3
		addStr := func(col string, v *string) {
			if v == nil {
				return
			}
			sets = append(sets, fmt.Sprintf("%s = $%d", col, pos))
			args = append(args, nilIfEmpty(*v))
			pos++
		}
		if req.Label != nil {
			addStr("label", req.Label)
		}
		if req.CableType != nil {
			sets = append(sets, fmt.Sprintf("cable_type = $%d", pos))
			args = append(args, *req.CableType)
			pos++
		}
		if req.LengthM != nil {
			sets = append(sets, fmt.Sprintf("length_m = $%d", pos))
			args = append(args, *req.LengthM)
			pos++
		}
		addStr("color", req.Color)
		addStr("source_interface_id", req.SourceInterfaceID)
		addStr("target_interface_id", req.TargetInterfaceID)
		if req.Status != nil {
			sets = append(sets, fmt.Sprintf("status = $%d", pos))
			args = append(args, *req.Status)
			pos++
		}
		if req.InstalledAt != nil {
			sets = append(sets, fmt.Sprintf("installed_at = $%d", pos))
			args = append(args, nilIfEmptyPtr(req.InstalledAt))
			pos++
		}
		var err error
		if len(sets) == 0 {
			c, err = scanCable(tx.QueryRow(ctx, "SELECT "+cableCols+" FROM cable WHERE organization_id = $1 AND id = $2", orgID, id))
		} else {
			sets = append(sets, "updated_at = NOW()")
			q := "UPDATE cable SET " + strings.Join(sets, ", ") + " WHERE organization_id = $1 AND id = $2 RETURNING " + cableCols
			c, err = scanCable(tx.QueryRow(ctx, q, args...))
		}
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return c, err
}

func (r *PGRepository) DeleteCable(ctx context.Context, orgID, id string) error {
	return r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "DELETE FROM cable WHERE organization_id = $1 AND id = $2", orgID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func nilIfEmptyPtr(v *string) any {
	if v == nil || strings.TrimSpace(*v) == "" {
		return nil
	}
	return *v
}
