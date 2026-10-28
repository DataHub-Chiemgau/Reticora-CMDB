package rack

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

type scanner interface {
	Scan(dest ...any) error
}

const rackCols = `id::text, organization_id::text, room_id::text, name, height_units, width_mm, depth_mm, COALESCE(notes,''), created_at, updated_at`

// rackConstraintFields maps the rack constraints onto their request field
// (LOC-06).
var rackConstraintFields = map[string]string{
	"rack_room_name_key":      "name",
	"rack_height_units_check": "height_units",
	"rack_room_id_fkey":       "room_id",
}

func scanRack(s scanner) (*Rack, error) {
	rk := &Rack{}
	if err := s.Scan(&rk.ID, &rk.OrganizationID, &rk.RoomID, &rk.Name, &rk.HeightUnits, &rk.WidthMM, &rk.DepthMM, &rk.Notes, &rk.CreatedAt, &rk.UpdatedAt); err != nil {
		return nil, err
	}
	rk.CreatedAt = rk.CreatedAt.UTC()
	rk.UpdatedAt = rk.UpdatedAt.UTC()
	return rk, nil
}

// Racks carry the client and site of their location row and mounts those of
// their CI, both with client and site policies (migrations 000152, 000061);
// cables carry no scope column. The predicates below stay as defense in
// depth (WP-393): racks, mounts and cables are visible only when the objects
// they hang off are visible under the transaction's tenant scope, whose
// location and ci policies filter the subqueries: a rack through its 1:1
// location row (organization, client and site predicate, migration 000062
// section 6), a mount through its rack and CI, a cable through the CIs of
// both interfaces. A LEFT JOIN over room and building would admit the rack of
// an invisible building (WP-340, F-002).
const (
	rackVisible  = `EXISTS (SELECT 1 FROM location WHERE location.id = rack.id)`
	mountVisible = "EXISTS (SELECT 1 FROM rack WHERE rack.id = rack_mount.rack_id AND " + rackVisible + ")" +
		" AND EXISTS (SELECT 1 FROM ci WHERE ci.id = rack_mount.ci_id)"
	cableVisible = `(cable.source_interface_id IS NULL OR EXISTS (
		SELECT 1 FROM network_interface JOIN ci ON ci.id = network_interface.ci_id
		WHERE network_interface.id = cable.source_interface_id))
	AND (cable.target_interface_id IS NULL OR EXISTS (
		SELECT 1 FROM network_interface JOIN ci ON ci.id = network_interface.ci_id
		WHERE network_interface.id = cable.target_interface_id))`
)

// requireVisible fails with ErrNotFound when the written row of table does not
// satisfy visible, i.e. when a write placed it under an object outside the
// tenant scope; the transaction is then rolled back.
func requireVisible(ctx context.Context, tx pgx.Tx, table, visible, id string) error {
	var ok bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM "+table+" WHERE id = $1 AND "+visible+")", id).Scan(&ok); err != nil {
		return fmt.Errorf("check %s visibility: %w", table, err)
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

func (r *PGRepository) ListRacks(ctx context.Context, orgID, roomID string, page api.PaginationParams) ([]Rack, int, error) {
	var out []Rack
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := "organization_id = $1 AND " + rackVisible
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
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rk, err = scanRack(tx.QueryRow(ctx, "SELECT "+rackCols+" FROM rack WHERE organization_id = $1 AND id = $2 AND "+rackVisible, orgID, id))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return rk, err
}

func (r *PGRepository) CreateRack(ctx context.Context, rk *Rack) error {
	return database.WithRequestTenant(ctx, r.pool, rk.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`INSERT INTO rack (organization_id, room_id, name, height_units, width_mm, depth_mm, notes)
			 VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id::text, created_at, updated_at`,
			rk.OrganizationID, rk.RoomID, rk.Name, rk.HeightUnits, rk.WidthMM, rk.DepthMM, nilIfEmpty(rk.Notes),
		).Scan(&rk.ID, &rk.CreatedAt, &rk.UpdatedAt); err != nil {
			return api.FieldDBError(err, rackConstraintFields)
		}
		return requireVisible(ctx, tx, "rack", rackVisible, rk.ID)
	})
}

// lockRackHeight locks the rack row (FOR UPDATE) and returns its height.
// Writers that compare mounts with the height (UpdateRack, CreateMount,
// UpdateMount) take this lock first, so a mount and a reduction of the
// height cannot pass their checks against each other's old state (RCK-01).
func lockRackHeight(ctx context.Context, tx pgx.Tx, orgID, id string) (int, error) {
	var height int
	err := tx.QueryRow(ctx, "SELECT height_units FROM rack WHERE organization_id = $1 AND id = $2 AND "+rackVisible+" FOR UPDATE",
		orgID, id).Scan(&height)
	return height, err
}

// checkHeightCoversMounts refuses a height below the highest unit occupied
// by a mount of the rack (RCK-01: position_u + height_u − 1 ≤ height_units)
// with 409 on height_units; the message names that unit. The caller holds
// the lock of lockRackHeight.
//
// The unit counts every mount of the rack, also those of CIs outside the
// caller's client or site scope (F-360): rack_highest_mounted_unit
// (migration 000172) reads it with the scope widened to the organization and
// returns only the number, NULL for a rack the caller cannot see.
func checkHeightCoversMounts(ctx context.Context, tx pgx.Tx, id string, height int) error {
	var top *int
	if err := tx.QueryRow(ctx, "SELECT rack_highest_mounted_unit($1)", id).Scan(&top); err != nil {
		return fmt.Errorf("highest mounted unit: %w", err)
	}
	if top == nil {
		return ErrNotFound
	}
	if height < *top {
		return heightBelowMountsError(*top)
	}
	return nil
}

func (r *PGRepository) UpdateRack(ctx context.Context, orgID, id string, req UpdateRackRequest) (*Rack, error) {
	var rk *Rack
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := lockRackHeight(ctx, tx, orgID, id); err != nil {
			if err == pgx.ErrNoRows {
				return ErrNotFound
			}
			return err
		}
		if req.HeightUnits != nil {
			if err := checkHeightCoversMounts(ctx, tx, id, *req.HeightUnits); err != nil {
				return err
			}
		}
		sets := []string{}
		args := []any{orgID, id}
		pos := 3
		if req.Name != nil {
			sets = append(sets, fmt.Sprintf("name = $%d", pos))
			args = append(args, *req.Name)
			pos++
		}
		if req.HeightUnits != nil {
			sets = append(sets, fmt.Sprintf("height_units = $%d", pos))
			args = append(args, *req.HeightUnits)
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
			rk, err = scanRack(tx.QueryRow(ctx, "SELECT "+rackCols+" FROM rack WHERE organization_id = $1 AND id = $2 AND "+rackVisible, orgID, id))
		} else {
			sets = append(sets, "updated_at = NOW()")
			q := "UPDATE rack SET " + strings.Join(sets, ", ") + " WHERE organization_id = $1 AND id = $2 AND " + rackVisible + " RETURNING " + rackCols
			rk, err = scanRack(tx.QueryRow(ctx, q, args...))
		}
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return api.FieldDBError(err, rackConstraintFields)
		}
		// The update may move the row under an object outside the scope.
		return requireVisible(ctx, tx, "rack", rackVisible, id)
	})
	return rk, err
}

func (r *PGRepository) DeleteRack(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var found bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM rack WHERE organization_id = $1 AND id::text = $2 AND "+rackVisible+")",
			orgID, id).Scan(&found); err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		// Mounted CIs keep the rack (LOC-11): no cascade to rack_mount.
		if err := locations.CheckDeletable(ctx, tx, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM rack WHERE organization_id = $1 AND id = $2", orgID, id); err != nil {
			return locations.DeleteError(err)
		}
		return nil
	})
}

const mountCols = `id::text, organization_id::text, rack_id::text, ci_id::text, position_u, height_u, face, created_at, updated_at`

// listedMountCols are the mountCols of ListMounts, qualified because of the
// joins, followed by the names of the CI (mci) and its type (mct).
const listedMountCols = `rack_mount.id::text, rack_mount.organization_id::text, rack_mount.rack_id::text, rack_mount.ci_id::text,
	rack_mount.position_u, rack_mount.height_u, rack_mount.face, rack_mount.created_at, rack_mount.updated_at,
	mci.name, COALESCE(mct.name, '')`

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
// The occupancy check needs every mount, including those of CIs outside the
// tenant scope, so the result is used for validation only and never returned.
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
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		// A rack outside the scope answers 404, not an empty list (F-180).
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM rack WHERE organization_id = $1 AND id = $2 AND "+rackVisible+")", orgID, rackID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrRackNotFound
		}
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM rack_mount WHERE organization_id = $1 AND rack_id = $2 AND "+mountVisible, orgID, rackID).Scan(&total); err != nil {
			return err
		}
		// The names of the CI and its type come with the mount (F-252); the
		// joins run under the same policies as mountVisible, so they name
		// only CIs the scope sees.
		rows, err := tx.Query(ctx, "SELECT "+listedMountCols+` FROM rack_mount
			JOIN ci mci ON mci.id = rack_mount.ci_id
			LEFT JOIN ci_type mct ON mct.id = mci.ci_type_id
			WHERE rack_mount.organization_id = $1 AND rack_mount.rack_id = $2 AND `+mountVisible+`
			ORDER BY rack_mount.position_u LIMIT $3 OFFSET $4`, orgID, rackID, page.Limit, page.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			m := RackMount{}
			if err := rows.Scan(&m.ID, &m.OrganizationID, &m.RackID, &m.CIID, &m.PositionU, &m.HeightU, &m.Face,
				&m.CreatedAt, &m.UpdatedAt, &m.CIName, &m.CIType); err != nil {
				return err
			}
			m.CreatedAt = m.CreatedAt.UTC()
			m.UpdatedAt = m.UpdatedAt.UTC()
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, total, err
}

// CreateMount implements Repository. Rack and CI ids are checked before any
// SQL; the CI must be visible under the caller's scope, so an unknown CI,
// one of another organization and one of a client or site outside the scope
// all fail with ErrCINotFound, before the foreign key (checked without RLS)
// or the policy of rack_mount could tell them apart (TEN-04, API-03).
func (r *PGRepository) CreateMount(ctx context.Context, m *RackMount) error {
	if !api.IsUUID(m.RackID) {
		return ErrRackNotFound
	}
	if err := api.RequireUUID("ci_id", m.CIID); err != nil {
		return err
	}
	return database.WithRequestTenant(ctx, r.pool, m.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		// The rack row stays locked until commit: a concurrent reduction of
		// its height waits for the mount and then sees it (RCK-01).
		height, err := lockRackHeight(ctx, tx, m.OrganizationID, m.RackID)
		if err == pgx.ErrNoRows {
			return ErrRackNotFound
		}
		if err != nil {
			return err
		}
		var ciVisible bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM ci WHERE organization_id = $1 AND id = $2)",
			m.OrganizationID, m.CIID).Scan(&ciVisible); err != nil {
			return fmt.Errorf("check ci of mount: %w", err)
		}
		if !ciVisible {
			return ErrCINotFound
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
			return mountWriteError(err)
		}
		// The mounted CI must be visible under the scope as well.
		if visErr := requireVisible(ctx, tx, "rack_mount", mountVisible, m.ID); visErr != nil {
			return visErr
		}
		return recordPlacement(ctx, tx, m.OrganizationID, m.CIID, "ci.rack_mounted", nil, m)
	})
}

func (r *PGRepository) GetMount(ctx context.Context, orgID, id string) (*RackMount, error) {
	var m *RackMount
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		m, err = scanMount(tx.QueryRow(ctx, "SELECT "+mountCols+" FROM rack_mount WHERE organization_id = $1 AND id = $2 AND "+mountVisible, orgID, id))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return m, err
}

// MountByCI implements Repository.
func (r *PGRepository) MountByCI(ctx context.Context, orgID, ciID string) (*RackMount, error) {
	if !api.IsUUID(ciID) {
		return nil, ErrNotFound
	}
	var m *RackMount
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		m, err = scanMount(tx.QueryRow(ctx, "SELECT "+mountCols+" FROM rack_mount WHERE organization_id = $1 AND ci_id = $2 AND "+mountVisible, orgID, ciID))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return m, err
}

func (r *PGRepository) UpdateMount(ctx context.Context, orgID, id string, req UpdateMountRequest) (*RackMount, error) {
	var m *RackMount
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := scanMount(tx.QueryRow(ctx, "SELECT "+mountCols+" FROM rack_mount WHERE organization_id = $1 AND id = $2 AND "+mountVisible, orgID, id))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		pos, height, face, rackID := current.PositionU, current.HeightU, current.Face, current.RackID
		if req.RackID != nil && *req.RackID != "" {
			rackID = *req.RackID
		}
		if req.PositionU != nil {
			pos = *req.PositionU
		}
		if req.HeightU != nil {
			height = *req.HeightU
		}
		if req.Face != nil {
			face = *req.Face
			if !ValidFaces[face] {
				return ruleError(ErrValidation, "invalid face")
			}
		}
		// Lock the target rack as CreateMount does (RCK-01).
		rackHeight, heightErr := lockRackHeight(ctx, tx, orgID, rackID)
		if heightErr == pgx.ErrNoRows {
			return ErrRackNotFound
		}
		if heightErr != nil {
			return heightErr
		}
		existing, err := mountsForRackTx(ctx, tx, orgID, rackID)
		if err != nil {
			return err
		}
		if err := validateMountFit(rackHeight, pos, height, face, existing, id); err != nil {
			return err
		}
		m, err = scanMount(tx.QueryRow(ctx,
			`UPDATE rack_mount SET rack_id = $6, position_u = $3, height_u = $4, face = $5, updated_at = NOW()
			 WHERE organization_id = $1 AND id = $2 RETURNING `+mountCols,
			orgID, id, pos, height, face, rackID))
		if err != nil {
			return mountWriteError(err)
		}
		return recordPlacement(ctx, tx, orgID, m.CIID, "ci.rack_moved", current, m)
	})
	return m, err
}

func (r *PGRepository) DeleteMount(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := scanMount(tx.QueryRow(ctx,
			"DELETE FROM rack_mount WHERE organization_id = $1 AND id = $2 AND "+mountVisible+" RETURNING "+mountCols, orgID, id))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return recordPlacement(ctx, tx, orgID, current.CIID, "ci.rack_unmounted", current, nil)
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
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM cable WHERE organization_id = $1 AND "+cableVisible, orgID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+cableCols+" FROM cable WHERE organization_id = $1 AND "+cableVisible+" ORDER BY created_at DESC LIMIT $2 OFFSET $3", orgID, page.Limit, page.Offset)
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
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		c, err = scanCable(tx.QueryRow(ctx, "SELECT "+cableCols+" FROM cable WHERE organization_id = $1 AND id = $2 AND "+cableVisible, orgID, id))
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return c, err
}

func (r *PGRepository) CreateCable(ctx context.Context, c *Cable) error {
	return database.WithRequestTenant(ctx, r.pool, c.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`INSERT INTO cable (organization_id, label, cable_type, length_m, color, source_interface_id, target_interface_id, status, installed_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id::text, created_at, updated_at`,
			c.OrganizationID, nilIfEmpty(c.Label), c.CableType, c.LengthM, nilIfEmpty(c.Color),
			nilIfEmpty(c.SourceInterfaceID), nilIfEmpty(c.TargetInterfaceID), c.Status, nilIfEmptyPtr(c.InstalledAt),
		).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return err
		}
		return requireVisible(ctx, tx, "cable", cableVisible, c.ID)
	})
}

func (r *PGRepository) UpdateCable(ctx context.Context, orgID, id string, req UpdateCableRequest) (*Cable, error) {
	var c *Cable
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
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
			c, err = scanCable(tx.QueryRow(ctx, "SELECT "+cableCols+" FROM cable WHERE organization_id = $1 AND id = $2 AND "+cableVisible, orgID, id))
		} else {
			sets = append(sets, "updated_at = NOW()")
			q := "UPDATE cable SET " + strings.Join(sets, ", ") + " WHERE organization_id = $1 AND id = $2 AND " + cableVisible + " RETURNING " + cableCols
			c, err = scanCable(tx.QueryRow(ctx, q, args...))
		}
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		// The update may move the row under an object outside the scope.
		return requireVisible(ctx, tx, "cable", cableVisible, id)
	})
	return c, err
}

func (r *PGRepository) DeleteCable(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "DELETE FROM cable WHERE organization_id = $1 AND id = $2 AND "+cableVisible, orgID, id)
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

// mountWriteError maps a failed mount write: a collision the exclusion
// constraint caught (concurrent writers), a CI that is mounted already and
// a child CI of a composition are conflicts (409) with a fixed domain
// message; the rest stays a database error for api.WriteRepoError, whose
// constraint, policy or trigger text never reaches the client (F-047).
func mountWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "23P01":
			return ruleError(ErrConflict, "overlaps another mount")
		case pgErr.Code == "23505":
			return ruleError(ErrConflict, "ci already mounted")
		case pgErr.ConstraintName == "rack_mount_composition_child":
			return ruleError(ErrConflict, "the CI is part of a composition; its parent is mounted")
		}
	}
	return fmt.Errorf("write rack mount: %w", err)
}
