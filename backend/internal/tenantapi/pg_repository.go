package tenantapi

import (
	"context"
	"fmt"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a new PostgreSQL-backed location repository.
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

const clientCols = `id::text, organization_id::text, name, slug, settings, created_at, updated_at`

type scanner interface {
	Scan(dest ...any) error
}

func scanClient(s scanner) (*Client, error) {
	c := &Client{}
	if err := s.Scan(&c.ID, &c.OrganizationID, &c.Name, &c.Slug, &c.Settings, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	if c.Settings == nil {
		c.Settings = map[string]any{}
	}
	c.CreatedAt = c.CreatedAt.UTC()
	c.UpdatedAt = c.UpdatedAt.UTC()
	return c, nil
}

// --- Clients ---

func (r *PGRepository) ListClients(orgID string, page api.PaginationParams) ([]Client, int, error) {
	ctx := context.Background()
	var out []Client
	var total int
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM client WHERE organization_id = $1", orgID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+clientCols+" FROM client WHERE organization_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3", orgID, page.Limit, page.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanClient(rows)
			if err != nil {
				return err
			}
			out = append(out, *c)
		}
		return rows.Err()
	})
	return out, total, err
}

func (r *PGRepository) GetClient(orgID, id string) (*Client, error) {
	ctx := context.Background()
	var c *Client
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		c, err = scanClient(tx.QueryRow(ctx, "SELECT "+clientCols+" FROM client WHERE organization_id = $1 AND id = $2", orgID, id))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("not found")
		}
		return err
	})
	return c, err
}

func (r *PGRepository) CreateClient(c *Client) error {
	ctx := context.Background()
	if c.Settings == nil {
		c.Settings = map[string]any{}
	}
	return r.withTenant(ctx, c.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO client (organization_id, name, slug, settings) VALUES ($1,$2,$3,$4) RETURNING id::text, created_at, updated_at`,
			c.OrganizationID, c.Name, c.Slug, c.Settings,
		).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
	})
}

func (r *PGRepository) UpdateClient(orgID, id string, req UpdateClientRequest) (*Client, error) {
	ctx := context.Background()
	var c *Client
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		sets := []string{}
		args := []any{orgID, id}
		pos := 3
		if req.Name != nil {
			sets = append(sets, fmt.Sprintf("name = $%d", pos))
			args = append(args, *req.Name)
			pos++
		}
		if req.Slug != nil {
			sets = append(sets, fmt.Sprintf("slug = $%d", pos))
			args = append(args, *req.Slug)
			pos++
		}
		if req.Settings != nil {
			sets = append(sets, fmt.Sprintf("settings = $%d", pos))
			args = append(args, req.Settings)
			pos++
		}
		var err error
		if len(sets) == 0 {
			c, err = scanClient(tx.QueryRow(ctx, "SELECT "+clientCols+" FROM client WHERE organization_id = $1 AND id = $2", orgID, id))
		} else {
			sets = append(sets, "updated_at = NOW()")
			q := "UPDATE client SET " + strings.Join(sets, ", ") + " WHERE organization_id = $1 AND id = $2 RETURNING " + clientCols
			c, err = scanClient(tx.QueryRow(ctx, q, args...))
		}
		if err == pgx.ErrNoRows {
			return fmt.Errorf("not found")
		}
		return err
	})
	return c, err
}

func (r *PGRepository) DeleteClient(orgID, id string) error {
	return r.deleteByID(orgID, "client", id)
}

func (r *PGRepository) deleteByID(orgID, table, id string) error {
	ctx := context.Background()
	return r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "DELETE FROM "+table+" WHERE organization_id = $1 AND id = $2", orgID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("not found")
		}
		return nil
	})
}

// --- Sites ---

const siteCols = `id::text, organization_id::text, client_id::text, name, COALESCE(address,''), geo_lat, geo_lon, COALESCE(notes,''), created_at, updated_at`

func scanSite(s scanner) (*Site, error) {
	si := &Site{}
	if err := s.Scan(&si.ID, &si.OrganizationID, &si.ClientID, &si.Name, &si.Address, &si.GeoLat, &si.GeoLon, &si.Notes, &si.CreatedAt, &si.UpdatedAt); err != nil {
		return nil, err
	}
	si.CreatedAt = si.CreatedAt.UTC()
	si.UpdatedAt = si.UpdatedAt.UTC()
	return si, nil
}

func (r *PGRepository) ListSites(orgID, clientID string, page api.PaginationParams) ([]Site, int, error) {
	ctx := context.Background()
	var out []Site
	var total int
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := "organization_id = $1"
		args := []any{orgID}
		if clientID != "" {
			where += " AND client_id = $2"
			args = append(args, clientID)
		}
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM site WHERE "+where, args...).Scan(&total); err != nil {
			return err
		}
		q := "SELECT " + siteCols + " FROM site WHERE " + where + fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
		args = append(args, page.Limit, page.Offset)
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			si, err := scanSite(rows)
			if err != nil {
				return err
			}
			out = append(out, *si)
		}
		return rows.Err()
	})
	return out, total, err
}

func (r *PGRepository) GetSite(orgID, id string) (*Site, error) {
	ctx := context.Background()
	var si *Site
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		si, err = scanSite(tx.QueryRow(ctx, "SELECT "+siteCols+" FROM site WHERE organization_id = $1 AND id = $2", orgID, id))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("not found")
		}
		return err
	})
	return si, err
}

func (r *PGRepository) CreateSite(s *Site) error {
	ctx := context.Background()
	return r.withTenant(ctx, s.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO site (organization_id, client_id, name, address, geo_lat, geo_lon, notes)
			 VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id::text, created_at, updated_at`,
			s.OrganizationID, s.ClientID, s.Name, nilIfEmpty(s.Address), s.GeoLat, s.GeoLon, nilIfEmpty(s.Notes),
		).Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt)
	})
}

func (r *PGRepository) UpdateSite(orgID, id string, req UpdateSiteRequest) (*Site, error) {
	ctx := context.Background()
	var si *Site
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		sets := []string{}
		args := []any{orgID, id}
		pos := 3
		if req.Name != nil {
			sets = append(sets, fmt.Sprintf("name = $%d", pos))
			args = append(args, *req.Name)
			pos++
		}
		if req.Address != nil {
			sets = append(sets, fmt.Sprintf("address = $%d", pos))
			args = append(args, nilIfEmpty(*req.Address))
			pos++
		}
		if req.GeoLat != nil {
			sets = append(sets, fmt.Sprintf("geo_lat = $%d", pos))
			args = append(args, *req.GeoLat)
			pos++
		}
		if req.GeoLon != nil {
			sets = append(sets, fmt.Sprintf("geo_lon = $%d", pos))
			args = append(args, *req.GeoLon)
			pos++
		}
		if req.Notes != nil {
			sets = append(sets, fmt.Sprintf("notes = $%d", pos))
			args = append(args, nilIfEmpty(*req.Notes))
			pos++
		}
		var err error
		if len(sets) == 0 {
			si, err = scanSite(tx.QueryRow(ctx, "SELECT "+siteCols+" FROM site WHERE organization_id = $1 AND id = $2", orgID, id))
		} else {
			sets = append(sets, "updated_at = NOW()")
			q := "UPDATE site SET " + strings.Join(sets, ", ") + " WHERE organization_id = $1 AND id = $2 RETURNING " + siteCols
			si, err = scanSite(tx.QueryRow(ctx, q, args...))
		}
		if err == pgx.ErrNoRows {
			return fmt.Errorf("not found")
		}
		return err
	})
	return si, err
}

func (r *PGRepository) DeleteSite(orgID, id string) error {
	return r.deleteByID(orgID, "site", id)
}

// --- Buildings ---

const buildingCols = `id::text, organization_id::text, site_id::text, name, floors, COALESCE(floorplan_object_key,''), created_at, updated_at`

func scanBuilding(s scanner) (*Building, error) {
	b := &Building{}
	if err := s.Scan(&b.ID, &b.OrganizationID, &b.SiteID, &b.Name, &b.Floors, &b.FloorplanObjectKey, &b.CreatedAt, &b.UpdatedAt); err != nil {
		return nil, err
	}
	b.CreatedAt = b.CreatedAt.UTC()
	b.UpdatedAt = b.UpdatedAt.UTC()
	return b, nil
}

func (r *PGRepository) ListBuildings(orgID, siteID string, page api.PaginationParams) ([]Building, int, error) {
	ctx := context.Background()
	var out []Building
	var total int
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := "organization_id = $1"
		args := []any{orgID}
		if siteID != "" {
			where += " AND site_id = $2"
			args = append(args, siteID)
		}
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM building WHERE "+where, args...).Scan(&total); err != nil {
			return err
		}
		q := "SELECT " + buildingCols + " FROM building WHERE " + where + fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
		args = append(args, page.Limit, page.Offset)
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			b, err := scanBuilding(rows)
			if err != nil {
				return err
			}
			out = append(out, *b)
		}
		return rows.Err()
	})
	return out, total, err
}

func (r *PGRepository) GetBuilding(orgID, id string) (*Building, error) {
	ctx := context.Background()
	var b *Building
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		b, err = scanBuilding(tx.QueryRow(ctx, "SELECT "+buildingCols+" FROM building WHERE organization_id = $1 AND id = $2", orgID, id))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("not found")
		}
		return err
	})
	return b, err
}

func (r *PGRepository) CreateBuilding(b *Building) error {
	ctx := context.Background()
	return r.withTenant(ctx, b.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO building (organization_id, site_id, name, floors, floorplan_object_key)
			 VALUES ($1,$2,$3,$4,$5) RETURNING id::text, created_at, updated_at`,
			b.OrganizationID, b.SiteID, b.Name, b.Floors, nilIfEmpty(b.FloorplanObjectKey),
		).Scan(&b.ID, &b.CreatedAt, &b.UpdatedAt)
	})
}

func (r *PGRepository) UpdateBuilding(orgID, id string, req UpdateBuildingRequest) (*Building, error) {
	ctx := context.Background()
	var b *Building
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		sets := []string{}
		args := []any{orgID, id}
		pos := 3
		if req.Name != nil {
			sets = append(sets, fmt.Sprintf("name = $%d", pos))
			args = append(args, *req.Name)
			pos++
		}
		if req.Floors != nil {
			sets = append(sets, fmt.Sprintf("floors = $%d", pos))
			args = append(args, *req.Floors)
			pos++
		}
		if req.FloorplanObjectKey != nil {
			sets = append(sets, fmt.Sprintf("floorplan_object_key = $%d", pos))
			args = append(args, nilIfEmpty(*req.FloorplanObjectKey))
			pos++
		}
		var err error
		if len(sets) == 0 {
			b, err = scanBuilding(tx.QueryRow(ctx, "SELECT "+buildingCols+" FROM building WHERE organization_id = $1 AND id = $2", orgID, id))
		} else {
			sets = append(sets, "updated_at = NOW()")
			q := "UPDATE building SET " + strings.Join(sets, ", ") + " WHERE organization_id = $1 AND id = $2 RETURNING " + buildingCols
			b, err = scanBuilding(tx.QueryRow(ctx, q, args...))
		}
		if err == pgx.ErrNoRows {
			return fmt.Errorf("not found")
		}
		return err
	})
	return b, err
}

func (r *PGRepository) DeleteBuilding(orgID, id string) error {
	return r.deleteByID(orgID, "building", id)
}

// --- Rooms ---

const roomCols = `id::text, organization_id::text, building_id::text, name, floor, COALESCE(room_type,'general'), created_at, updated_at`

func scanRoom(s scanner) (*Room, error) {
	rm := &Room{}
	if err := s.Scan(&rm.ID, &rm.OrganizationID, &rm.BuildingID, &rm.Name, &rm.Floor, &rm.RoomType, &rm.CreatedAt, &rm.UpdatedAt); err != nil {
		return nil, err
	}
	rm.CreatedAt = rm.CreatedAt.UTC()
	rm.UpdatedAt = rm.UpdatedAt.UTC()
	return rm, nil
}

func (r *PGRepository) ListRooms(orgID, buildingID string, page api.PaginationParams) ([]Room, int, error) {
	ctx := context.Background()
	var out []Room
	var total int
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := "organization_id = $1"
		args := []any{orgID}
		if buildingID != "" {
			where += " AND building_id = $2"
			args = append(args, buildingID)
		}
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM room WHERE "+where, args...).Scan(&total); err != nil {
			return err
		}
		q := "SELECT " + roomCols + " FROM room WHERE " + where + fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
		args = append(args, page.Limit, page.Offset)
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			rm, err := scanRoom(rows)
			if err != nil {
				return err
			}
			out = append(out, *rm)
		}
		return rows.Err()
	})
	return out, total, err
}

func (r *PGRepository) GetRoom(orgID, id string) (*Room, error) {
	ctx := context.Background()
	var rm *Room
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rm, err = scanRoom(tx.QueryRow(ctx, "SELECT "+roomCols+" FROM room WHERE organization_id = $1 AND id = $2", orgID, id))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("not found")
		}
		return err
	})
	return rm, err
}

func (r *PGRepository) CreateRoom(rm *Room) error {
	ctx := context.Background()
	if rm.RoomType == "" {
		rm.RoomType = "general"
	}
	return r.withTenant(ctx, rm.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO room (organization_id, building_id, name, floor, room_type)
			 VALUES ($1,$2,$3,$4,$5) RETURNING id::text, created_at, updated_at`,
			rm.OrganizationID, rm.BuildingID, rm.Name, rm.Floor, rm.RoomType,
		).Scan(&rm.ID, &rm.CreatedAt, &rm.UpdatedAt)
	})
}

func (r *PGRepository) UpdateRoom(orgID, id string, req UpdateRoomRequest) (*Room, error) {
	ctx := context.Background()
	var rm *Room
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		sets := []string{}
		args := []any{orgID, id}
		pos := 3
		if req.Name != nil {
			sets = append(sets, fmt.Sprintf("name = $%d", pos))
			args = append(args, *req.Name)
			pos++
		}
		if req.Floor != nil {
			sets = append(sets, fmt.Sprintf("floor = $%d", pos))
			args = append(args, *req.Floor)
			pos++
		}
		if req.RoomType != nil {
			sets = append(sets, fmt.Sprintf("room_type = $%d", pos))
			args = append(args, *req.RoomType)
			pos++
		}
		var err error
		if len(sets) == 0 {
			rm, err = scanRoom(tx.QueryRow(ctx, "SELECT "+roomCols+" FROM room WHERE organization_id = $1 AND id = $2", orgID, id))
		} else {
			sets = append(sets, "updated_at = NOW()")
			q := "UPDATE room SET " + strings.Join(sets, ", ") + " WHERE organization_id = $1 AND id = $2 RETURNING " + roomCols
			rm, err = scanRoom(tx.QueryRow(ctx, q, args...))
		}
		if err == pgx.ErrNoRows {
			return fmt.Errorf("not found")
		}
		return err
	})
	return rm, err
}

func (r *PGRepository) DeleteRoom(orgID, id string) error {
	return r.deleteByID(orgID, "room", id)
}
