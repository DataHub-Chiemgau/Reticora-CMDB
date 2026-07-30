package contact

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

// NewPGRepository creates a new PostgreSQL-backed contact repository.
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

const contactCols = `id::text, organization_id::text, COALESCE(client_id::text,''), display_name,
	COALESCE(email,''), COALESCE(phone,''), COALESCE(role,''), COALESCE(department,''), COALESCE(notes,''),
	created_at, updated_at`

type scanner interface {
	Scan(dest ...any) error
}

func scanContact(s scanner) (*Contact, error) {
	c := &Contact{}
	if err := s.Scan(&c.ID, &c.OrganizationID, &c.ClientID, &c.DisplayName,
		&c.Email, &c.Phone, &c.Role, &c.Department, &c.Notes, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.CreatedAt = c.CreatedAt.UTC()
	c.UpdatedAt = c.UpdatedAt.UTC()
	return c, nil
}

func (r *PGRepository) List(orgID, clientID string, page api.PaginationParams) ([]Contact, int, error) {
	ctx := context.Background()
	var out []Contact
	var total int
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := "organization_id = $1"
		args := []any{orgID}
		if clientID != "" {
			where += " AND client_id = $2"
			args = append(args, clientID)
		}
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM contact WHERE "+where, args...).Scan(&total); err != nil {
			return err
		}
		q := "SELECT " + contactCols + " FROM contact WHERE " + where + fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
		args = append(args, page.Limit, page.Offset)
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanContact(rows)
			if err != nil {
				return err
			}
			out = append(out, *c)
		}
		return rows.Err()
	})
	return out, total, err
}

func (r *PGRepository) GetByID(orgID, id string) (*Contact, error) {
	ctx := context.Background()
	var c *Contact
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		c, err = scanContact(tx.QueryRow(ctx, "SELECT "+contactCols+" FROM contact WHERE organization_id = $1 AND id = $2", orgID, id))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("not found")
		}
		return err
	})
	return c, err
}

func (r *PGRepository) Create(c *Contact) error {
	ctx := context.Background()
	return r.withTenant(ctx, c.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO contact (organization_id, client_id, display_name, email, phone, role, department, notes)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id::text, created_at, updated_at`,
			c.OrganizationID, nilIfEmpty(c.ClientID), c.DisplayName, nilIfEmpty(c.Email), nilIfEmpty(c.Phone),
			nilIfEmpty(c.Role), nilIfEmpty(c.Department), nilIfEmpty(c.Notes),
		).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
	})
}

func (r *PGRepository) Update(orgID, id string, req UpdateContactRequest) (*Contact, error) {
	ctx := context.Background()
	var c *Contact
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		sets := []string{}
		args := []any{orgID, id}
		pos := 3
		add := func(col string, v *string) {
			if v == nil {
				return
			}
			sets = append(sets, fmt.Sprintf("%s = $%d", col, pos))
			args = append(args, nilIfEmpty(*v))
			pos++
		}
		add("client_id", req.ClientID)
		if req.DisplayName != nil {
			sets = append(sets, fmt.Sprintf("display_name = $%d", pos))
			args = append(args, *req.DisplayName)
			pos++
		}
		add("email", req.Email)
		add("phone", req.Phone)
		add("role", req.Role)
		add("department", req.Department)
		add("notes", req.Notes)
		var err error
		if len(sets) == 0 {
			c, err = scanContact(tx.QueryRow(ctx, "SELECT "+contactCols+" FROM contact WHERE organization_id = $1 AND id = $2", orgID, id))
		} else {
			sets = append(sets, "updated_at = NOW()")
			q := "UPDATE contact SET " + strings.Join(sets, ", ") + " WHERE organization_id = $1 AND id = $2 RETURNING " + contactCols
			c, err = scanContact(tx.QueryRow(ctx, q, args...))
		}
		if err == pgx.ErrNoRows {
			return fmt.Errorf("not found")
		}
		return err
	})
	return c, err
}

func (r *PGRepository) Delete(orgID, id string) error {
	ctx := context.Background()
	return r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "DELETE FROM contact WHERE organization_id = $1 AND id = $2", orgID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("not found")
		}
		return nil
	})
}

const ciContactCols = `id::text, organization_id::text, ci_id::text, contact_id::text, relationship_type, created_at`

func scanCIContact(s scanner) (*CIContact, error) {
	l := &CIContact{}
	if err := s.Scan(&l.ID, &l.OrganizationID, &l.CIID, &l.ContactID, &l.RelationshipType, &l.CreatedAt); err != nil {
		return nil, err
	}
	l.CreatedAt = l.CreatedAt.UTC()
	return l, nil
}

func (r *PGRepository) ListForCI(orgID, ciID string, page api.PaginationParams) ([]CIContact, int, error) {
	ctx := context.Background()
	var out []CIContact
	var total int
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM ci_contact WHERE organization_id = $1 AND ci_id = $2", orgID, ciID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+ciContactCols+" FROM ci_contact WHERE organization_id = $1 AND ci_id = $2 ORDER BY created_at DESC LIMIT $3 OFFSET $4", orgID, ciID, page.Limit, page.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			l, err := scanCIContact(rows)
			if err != nil {
				return err
			}
			out = append(out, *l)
		}
		return rows.Err()
	})
	return out, total, err
}

func (r *PGRepository) Link(link *CIContact) error {
	ctx := context.Background()
	return r.withTenant(ctx, link.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO ci_contact (organization_id, ci_id, contact_id, relationship_type)
			 VALUES ($1,$2,$3,$4) RETURNING id::text, created_at`,
			link.OrganizationID, link.CIID, link.ContactID, link.RelationshipType,
		).Scan(&link.ID, &link.CreatedAt)
	})
}

func (r *PGRepository) Unlink(orgID, id string) error {
	ctx := context.Background()
	return r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "DELETE FROM ci_contact WHERE organization_id = $1 AND id = $2", orgID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("not found")
		}
		return nil
	})
}
