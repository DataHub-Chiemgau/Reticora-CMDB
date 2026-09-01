package keymgmt

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const itemCols = `id::text, organization_id::text, COALESCE(client_id::text,''), name, key_type, COALESCE(identifier,''), status, COALESCE(location,''), COALESCE(notes,''), created_at, updated_at`

const assignmentCols = `id::text, organization_id::text, key_item_id::text, assigned_to::text, issued_at, returned_at, COALESCE(issued_by::text,''), COALESCE(notes,''), created_at`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a PostgreSQL-backed key repository.
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

func scanItem(s pgx.Row) (*Item, error) {
	it := &Item{}
	if err := s.Scan(&it.ID, &it.OrganizationID, &it.ClientID, &it.Name, &it.KeyType, &it.Identifier, &it.Status, &it.Location, &it.Notes, &it.CreatedAt, &it.UpdatedAt); err != nil {
		return nil, err
	}
	return it, nil
}

func scanAssignment(s pgx.Row) (*Assignment, error) {
	a := &Assignment{}
	if err := s.Scan(&a.ID, &a.OrganizationID, &a.KeyItemID, &a.AssignedTo, &a.IssuedAt, &a.ReturnedAt, &a.IssuedBy, &a.Notes, &a.CreatedAt); err != nil {
		return nil, err
	}
	return a, nil
}

func (r *PGRepository) List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Item, int, error) {
	out := []Item{}
	var total int
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := []string{"organization_id = $1"}
		args := []any{orgID}
		pos := 2
		if filter.KeyType != "" {
			where = append(where, fmt.Sprintf("key_type = $%d", pos))
			args = append(args, filter.KeyType)
			pos++
		}
		if filter.Status != "" {
			where = append(where, fmt.Sprintf("status = $%d", pos))
			args = append(args, filter.Status)
			pos++
		}
		if filter.ClientID != "" {
			where = append(where, fmt.Sprintf("client_id = $%d", pos))
			args = append(args, filter.ClientID)
			pos++
		}
		if filter.Search != "" {
			where = append(where, fmt.Sprintf("name ILIKE $%d", pos))
			args = append(args, "%"+filter.Search+"%")
			pos++
		}
		clause := strings.Join(where, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM key_item WHERE "+clause, args...).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+itemCols+" FROM key_item WHERE "+clause+fmt.Sprintf(" ORDER BY name ASC LIMIT $%d OFFSET $%d", pos, pos+1), append(args, page.Limit, page.Offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			it, err := scanItem(rows)
			if err != nil {
				return err
			}
			out = append(out, *it)
		}
		return rows.Err()
	})
	return out, total, err
}

func (r *PGRepository) GetByID(ctx context.Context, orgID, id string) (*Item, error) {
	var it *Item
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		it, err = scanItem(tx.QueryRow(ctx, "SELECT "+itemCols+" FROM key_item WHERE organization_id = $1 AND id = $2", orgID, id))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("key item not found")
		}
		return err
	})
	return it, err
}

func (r *PGRepository) Create(ctx context.Context, item *Item) error {
	return r.withTenant(ctx, item.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if item.Status == "" {
			item.Status = "available"
		}
		if item.KeyType == "" {
			item.KeyType = "physical"
		}
		return tx.QueryRow(ctx, `
			INSERT INTO key_item (organization_id, client_id, name, key_type, identifier, status, location, notes)
			VALUES ($1, NULLIF($2,'')::uuid, $3, $4, $5, $6, $7, $8)
			RETURNING id::text, created_at, updated_at
		`, item.OrganizationID, item.ClientID, item.Name, item.KeyType, item.Identifier, item.Status, item.Location, item.Notes).
			Scan(&item.ID, &item.CreatedAt, &item.UpdatedAt)
	})
}

func (r *PGRepository) Update(ctx context.Context, orgID, id string, req UpdateItemRequest) (*Item, error) {
	var it *Item
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		sets := []string{}
		args := []any{orgID, id}
		pos := 3
		set := func(col string, val any) {
			sets = append(sets, fmt.Sprintf("%s = $%d", col, pos))
			args = append(args, val)
			pos++
		}
		if req.ClientID != nil {
			set("client_id", nilIfEmpty(*req.ClientID))
		}
		if req.Name != nil {
			set("name", *req.Name)
		}
		if req.KeyType != nil {
			set("key_type", *req.KeyType)
		}
		if req.Identifier != nil {
			set("identifier", *req.Identifier)
		}
		if req.Status != nil {
			set("status", *req.Status)
		}
		if req.Location != nil {
			set("location", *req.Location)
		}
		if req.Notes != nil {
			set("notes", *req.Notes)
		}
		var err error
		if len(sets) == 0 {
			it, err = scanItem(tx.QueryRow(ctx, "SELECT "+itemCols+" FROM key_item WHERE organization_id = $1 AND id = $2", orgID, id))
		} else {
			sets = append(sets, "updated_at = now()")
			it, err = scanItem(tx.QueryRow(ctx, "UPDATE key_item SET "+strings.Join(sets, ", ")+" WHERE organization_id = $1 AND id = $2 RETURNING "+itemCols, args...))
		}
		if err == pgx.ErrNoRows {
			return fmt.Errorf("key item not found")
		}
		return err
	})
	return it, err
}

func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	return r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "DELETE FROM key_item WHERE organization_id = $1 AND id = $2", orgID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("key item not found")
		}
		return nil
	})
}

// Issue records the handover and marks the item issued, atomically.
func (r *PGRepository) Issue(ctx context.Context, a *Assignment) (*Item, error) {
	var it *Item
	err := r.withTenant(ctx, a.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		it, err = scanItem(tx.QueryRow(ctx, "SELECT "+itemCols+" FROM key_item WHERE organization_id = $1 AND id = $2 FOR UPDATE", a.OrganizationID, a.KeyItemID))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("key item not found")
		}
		if err != nil {
			return err
		}
		if it.Status != "available" {
			return fmt.Errorf("key item is not available")
		}
		now := time.Now().UTC()
		a.IssuedAt = now
		if err := tx.QueryRow(ctx, `
			INSERT INTO key_assignment (organization_id, key_item_id, assigned_to, issued_at, issued_by, notes)
			VALUES ($1, $2, $3::uuid, $4, NULLIF($5,'')::uuid, $6)
			RETURNING id::text, created_at
		`, a.OrganizationID, a.KeyItemID, a.AssignedTo, a.IssuedAt, a.IssuedBy, a.Notes).
			Scan(&a.ID, &a.CreatedAt); err != nil {
			return err
		}
		it, err = scanItem(tx.QueryRow(ctx, "UPDATE key_item SET status = 'issued', updated_at = now() WHERE organization_id = $1 AND id = $2 RETURNING "+itemCols, a.OrganizationID, a.KeyItemID))
		return err
	})
	return it, err
}

// Return closes the open assignment and marks the item available.
func (r *PGRepository) Return(ctx context.Context, orgID, keyItemID string) (*Item, error) {
	var it *Item
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		it, err = scanItem(tx.QueryRow(ctx, "SELECT "+itemCols+" FROM key_item WHERE organization_id = $1 AND id = $2 FOR UPDATE", orgID, keyItemID))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("key item not found")
		}
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, "UPDATE key_assignment SET returned_at = now() WHERE organization_id = $1 AND key_item_id = $2 AND returned_at IS NULL", orgID, keyItemID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("key item is not issued")
		}
		it, err = scanItem(tx.QueryRow(ctx, "UPDATE key_item SET status = 'available', updated_at = now() WHERE organization_id = $1 AND id = $2 RETURNING "+itemCols, orgID, keyItemID))
		return err
	})
	return it, err
}

func (r *PGRepository) ListAssignments(ctx context.Context, orgID, keyItemID string) ([]Assignment, error) {
	out := []Assignment{}
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT "+assignmentCols+" FROM key_assignment WHERE organization_id = $1 AND key_item_id = $2 ORDER BY issued_at DESC", orgID, keyItemID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAssignment(rows)
			if err != nil {
				return err
			}
			out = append(out, *a)
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
