package desk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrAlreadyBooked reports an active booking of the desk that overlaps the
// requested window.
var ErrAlreadyBooked = errors.New("desk already booked in this time window")

const deskCols = `id::text, organization_id::text, COALESCE(room_id::text,''), name, status, attributes, created_at, updated_at`

const bookingCols = `id::text, organization_id::text, desk_id::text, user_id::text, starts_at, ends_at, status, created_at`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a PostgreSQL-backed desk repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

func scanDesk(s pgx.Row) (*Desk, error) {
	d := &Desk{}
	var attrs []byte
	if err := s.Scan(&d.ID, &d.OrganizationID, &d.RoomID, &d.Name, &d.Status, &attrs, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return nil, err
	}
	if len(attrs) > 0 {
		json.Unmarshal(attrs, &d.Attributes)
	}
	if d.Attributes == nil {
		d.Attributes = map[string]any{}
	}
	return d, nil
}

func scanBooking(s pgx.Row) (*Booking, error) {
	b := &Booking{}
	if err := s.Scan(&b.ID, &b.OrganizationID, &b.DeskID, &b.UserID, &b.StartsAt, &b.EndsAt, &b.Status, &b.CreatedAt); err != nil {
		return nil, err
	}
	return b, nil
}

// Desks and bookings carry no client or site column yet (WP-029). A desk is
// visible when its room's site is visible under the transaction's tenant
// scope, whose site policy filters the subquery; a booking follows its desk.
const (
	deskVisible = `(desk.room_id IS NULL OR EXISTS (SELECT 1 FROM room LEFT JOIN building ON building.id = room.building_id
		WHERE room.id = desk.room_id
		AND (building.site_id IS NULL OR EXISTS (SELECT 1 FROM site WHERE site.id = building.site_id))))`
	bookingVisible = "EXISTS (SELECT 1 FROM desk WHERE desk.id = desk_booking.desk_id AND " + deskVisible + ")"
)

// requireVisible fails with "not found" when the written row of table does not
// satisfy visible, i.e. when a write pointed it at an object outside the
// tenant scope; the transaction is then rolled back.
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

func (r *PGRepository) List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Desk, int, error) {
	out := []Desk{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := []string{"organization_id = $1"}
		args := []any{orgID}
		pos := 2
		if filter.RoomID != "" {
			where = append(where, fmt.Sprintf("room_id = $%d", pos))
			args = append(args, filter.RoomID)
			pos++
		}
		if filter.Status != "" {
			where = append(where, fmt.Sprintf("status = $%d", pos))
			args = append(args, filter.Status)
			pos++
		}
		if filter.Search != "" {
			where = append(where, fmt.Sprintf("name ILIKE $%d", pos))
			args = append(args, "%"+filter.Search+"%")
			pos++
		}
		clause := strings.Join(where, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM desk WHERE "+deskVisible+" AND "+clause, args...).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+deskCols+" FROM desk WHERE "+deskVisible+" AND "+clause+fmt.Sprintf(" ORDER BY name ASC LIMIT $%d OFFSET $%d", pos, pos+1), append(args, page.Limit, page.Offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDesk(rows)
			if err != nil {
				return err
			}
			out = append(out, *d)
		}
		return rows.Err()
	})
	return out, total, err
}

func (r *PGRepository) GetByID(ctx context.Context, orgID, id string) (*Desk, error) {
	var d *Desk
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		d, err = scanDesk(tx.QueryRow(ctx, "SELECT "+deskCols+" FROM desk WHERE "+deskVisible+" AND organization_id = $1 AND id = $2", orgID, id))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("desk not found")
		}
		return err
	})
	return d, err
}

func (r *PGRepository) Create(ctx context.Context, d *Desk) error {
	return database.WithRequestTenant(ctx, r.pool, d.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if d.Status == "" {
			d.Status = "available"
		}
		if d.Attributes == nil {
			d.Attributes = map[string]any{}
		}
		attrs, _ := json.Marshal(d.Attributes)
		if err := tx.QueryRow(ctx, `
			INSERT INTO desk (organization_id, room_id, name, status, attributes)
			VALUES ($1, NULLIF($2,'')::uuid, $3, $4, $5::jsonb)
			RETURNING id::text, created_at, updated_at
		`, d.OrganizationID, d.RoomID, d.Name, d.Status, string(attrs)).
			Scan(&d.ID, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return err
		}
		return requireVisible(ctx, tx, "desk", deskVisible, d.ID)
	})
}

func (r *PGRepository) Update(ctx context.Context, orgID, id string, req UpdateDeskRequest) (*Desk, error) {
	var d *Desk
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		sets := []string{}
		args := []any{orgID, id}
		pos := 3
		if req.RoomID != nil {
			sets = append(sets, fmt.Sprintf("room_id = $%d", pos))
			args = append(args, nilIfEmpty(*req.RoomID))
			pos++
		}
		if req.Name != nil {
			sets = append(sets, fmt.Sprintf("name = $%d", pos))
			args = append(args, *req.Name)
			pos++
		}
		if req.Status != nil {
			sets = append(sets, fmt.Sprintf("status = $%d", pos))
			args = append(args, *req.Status)
			pos++
		}
		if req.Attributes != nil {
			attrs, _ := json.Marshal(req.Attributes)
			sets = append(sets, fmt.Sprintf("attributes = $%d::jsonb", pos))
			args = append(args, string(attrs))
			pos++
		}
		var err error
		if len(sets) == 0 {
			d, err = scanDesk(tx.QueryRow(ctx, "SELECT "+deskCols+" FROM desk WHERE "+deskVisible+" AND organization_id = $1 AND id = $2", orgID, id))
		} else {
			sets = append(sets, "updated_at = now()")
			d, err = scanDesk(tx.QueryRow(ctx, "UPDATE desk SET "+strings.Join(sets, ", ")+" WHERE "+deskVisible+" AND organization_id = $1 AND id = $2 RETURNING "+deskCols, args...))
		}
		if err == pgx.ErrNoRows {
			return fmt.Errorf("desk not found")
		}
		if err != nil {
			return err
		}
		// The update may move the desk into a room outside the scope.
		return requireVisible(ctx, tx, "desk", deskVisible, id)
	})
	return d, err
}

func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "DELETE FROM desk WHERE "+deskVisible+" AND organization_id = $1 AND id = $2", orgID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("desk not found")
		}
		return nil
	})
}

// Book reserves a desk. Overlapping active bookings of the same desk are
// rejected by the exclusion constraint desk_booking_no_overlap (migration
// 000065), which also holds for concurrent requests.
func (r *PGRepository) Book(ctx context.Context, b *Booking) (*Booking, error) {
	err := database.WithRequestTenant(ctx, r.pool, b.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if !b.EndsAt.After(b.StartsAt) {
			return fmt.Errorf("ends_at must be after starts_at")
		}
		b.Status = "active"
		if err := tx.QueryRow(ctx, `
			INSERT INTO desk_booking (organization_id, desk_id, user_id, starts_at, ends_at, status)
			VALUES ($1, $2::uuid, $3::uuid, $4, $5, 'active')
			RETURNING id::text, created_at
		`, b.OrganizationID, b.DeskID, b.UserID, b.StartsAt, b.EndsAt).
			Scan(&b.ID, &b.CreatedAt); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23P01" { // exclusion_violation
				return ErrAlreadyBooked
			}
			return err
		}
		return requireVisible(ctx, tx, "desk_booking", bookingVisible, b.ID)
	})
	return b, err
}

func (r *PGRepository) CancelBooking(ctx context.Context, orgID, bookingID string) (*Booking, error) {
	var b *Booking
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		b, err = scanBooking(tx.QueryRow(ctx, "UPDATE desk_booking SET status = 'cancelled' WHERE "+bookingVisible+" AND organization_id = $1 AND id = $2 RETURNING "+bookingCols, orgID, bookingID))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("booking not found")
		}
		return err
	})
	return b, err
}

func (r *PGRepository) ListBookings(ctx context.Context, orgID, deskID string) ([]Booking, error) {
	out := []Booking{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT "+bookingCols+" FROM desk_booking WHERE "+bookingVisible+" AND organization_id = $1 AND desk_id = $2 ORDER BY starts_at ASC", orgID, deskID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			b, err := scanBooking(rows)
			if err != nil {
				return err
			}
			out = append(out, *b)
		}
		return rows.Err()
	})
	return out, err
}

func nilIfEmpty(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}
