package contract

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// PGRepository stores suppliers in PostgreSQL (migration 000223). Every
// access runs through database.WithRequestTenant, so row level security
// applies the organization and the client scope of the principal (E-09):
// a client-scoped principal reads org-wide suppliers and those of its
// clients and writes only the latter.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository returns the PostgreSQL supplier repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

const supplierColumns = `id::text, organization_id::text, client_id::text, name, external_ref,
	contact_id::text, notes, version, created_at, updated_at`

// supplierColumnsOf maps the filter and sort fields onto their columns
// (whitelist; the request never names a column).
var supplierColumnsOf = map[string]string{
	"name": "name", "client_id": "client_id", "external_ref": "external_ref", "created_at": "created_at",
}

func scanSupplier(row pgx.Row) (*Supplier, error) {
	s := &Supplier{}
	if err := row.Scan(&s.ID, &s.OrganizationID, &s.ClientID, &s.Name, &s.ExternalRef,
		&s.ContactID, &s.Notes, &s.Version, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	s.CreatedAt, s.UpdatedAt = s.CreatedAt.UTC(), s.UpdatedAt.UTC()
	return s, nil
}

// supplierDBError maps the constraints of supplier onto the request
// fields: a taken name is 409, a client or contact of another organization
// 422. Other errors pass to api.WriteRepoError.
func supplierDBError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.ConstraintName {
	case "supplier_organization_id_name_key":
		return errNameTaken
	case "supplier_client_id_fkey":
		return errClient
	case "supplier_contact_id_fkey":
		return errContact
	}
	return err
}

// checkContact verifies that contactID (if any) names a contact visible
// under the scope: a contact of another client or organization is 422.
func checkContact(ctx context.Context, tx pgx.Tx, orgID string, contactID *string) error {
	if contactID == nil {
		return nil
	}
	var visible bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM contact WHERE organization_id = $1 AND id = $2::uuid)`,
		orgID, *contactID).Scan(&visible); err != nil {
		return fmt.Errorf("read contact: %w", err)
	}
	if !visible {
		return errContact
	}
	return nil
}

// ListSuppliers reads the suppliers visible under the scope with the list
// query: one row more than the page limit for the keyset cursor.
func (r *PGRepository) ListSuppliers(ctx context.Context, orgID string, q *api.ListQuery, page api.PaginationParams) ([]Supplier, int, error) {
	field, dir := supplierOrder(q)
	if page.Cursor != nil {
		if err := cursorPosition(page.Cursor, field, dir); err != nil {
			return nil, 0, err
		}
	}
	items := make([]Supplier, 0)
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := "organization_id = $1"
		args := []any{orgID}
		for _, f := range q.Filters {
			column, ok := supplierColumnsOf[f.Field]
			if !ok {
				return &api.FieldError{Field: "filter[" + f.Field + "]", Message: "unknown filter field", Status: http.StatusUnprocessableEntity}
			}
			clause, values := f.SQL(column, len(args)+1)
			where += " AND " + clause
			args = append(args, values...)
		}
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM supplier WHERE "+where, args...).Scan(&total); err != nil {
			return fmt.Errorf("count suppliers: %w", err)
		}
		column, castType := supplierColumnsOf[field], "text"
		if field == "created_at" {
			castType = "timestamptz"
		}
		if page.Cursor != nil {
			where += " AND " + api.KeysetClause(column, castType, dir, len(args)+1)
			args = append(args, page.Cursor.Value, page.Cursor.ID)
		}
		order := "ASC"
		if dir == "desc" {
			order = "DESC"
		}
		tail := fmt.Sprintf(" ORDER BY %s %s, id %s LIMIT $%d", column, order, order, len(args)+1)
		args = append(args, page.Limit+1)
		if page.Cursor == nil && page.Offset > 0 {
			tail += fmt.Sprintf(" OFFSET $%d", len(args)+1)
			args = append(args, page.Offset)
		}
		rows, err := tx.Query(ctx, "SELECT "+supplierColumns+" FROM supplier WHERE "+where+tail, args...)
		if err != nil {
			return fmt.Errorf("list suppliers: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			s, scanErr := scanSupplier(rows)
			if scanErr != nil {
				return scanErr
			}
			items = append(items, *s)
		}
		return rows.Err()
	})
	return items, total, err
}

// GetSupplier reads a supplier visible under the scope.
func (r *PGRepository) GetSupplier(ctx context.Context, orgID, id string) (*Supplier, error) {
	if !api.IsUUID(id) {
		return nil, ErrSupplierNotFound
	}
	var s *Supplier
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		s, err = scanSupplier(tx.QueryRow(ctx, "SELECT "+supplierColumns+" FROM supplier WHERE organization_id = $1 AND id = $2", orgID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSupplierNotFound
		}
		return err
	})
	return s, err
}

// CreateSupplier stores a supplier with version 1. A client of another
// organization is 422 (foreign key with the organization, K19), a client
// outside the caller's scope and an org-wide supplier of a client-scoped
// principal violate WITH CHECK (403, E-09).
func (r *PGRepository) CreateSupplier(ctx context.Context, s *Supplier) error {
	if err := validate(s); err != nil {
		return err
	}
	return database.WithRequestTenant(ctx, r.pool, s.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if err := checkContact(ctx, tx, s.OrganizationID, s.ContactID); err != nil {
			return err
		}
		created, err := scanSupplier(tx.QueryRow(ctx, `
			INSERT INTO supplier (organization_id, client_id, name, external_ref, contact_id, notes)
			VALUES ($1, $2::uuid, $3, $4, $5::uuid, $6)
			RETURNING `+supplierColumns, s.OrganizationID, s.ClientID, s.Name, s.ExternalRef, s.ContactID, s.Notes))
		if err != nil {
			return supplierDBError(err)
		}
		*s = *created
		return nil
	})
}

// requireIfMatch reads the organization setting require_if_match.
func requireIfMatch(ctx context.Context, tx pgx.Tx, orgID string) (bool, error) {
	var required bool
	if err := tx.QueryRow(ctx, `SELECT COALESCE((settings->>'require_if_match')::boolean, false) FROM organization WHERE id = $1`,
		orgID).Scan(&required); err != nil {
		return false, fmt.Errorf("read if-match setting: %w", err)
	}
	return required, nil
}

// UpdateSupplier changes a supplier the principal may change: the row is
// locked with FOR UPDATE, which applies supplier_scoped_update, so an
// org-wide supplier of a client-scoped principal is not found (404). The
// If-Match check follows the organization setting; every change raises the
// version by one.
func (r *PGRepository) UpdateSupplier(ctx context.Context, orgID, id string, req *UpdateSupplierRequest) (*Supplier, error) {
	if !api.IsUUID(id) {
		return nil, ErrSupplierNotFound
	}
	var s *Supplier
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := scanSupplier(tx.QueryRow(ctx, "SELECT "+supplierColumns+" FROM supplier WHERE organization_id = $1 AND id = $2 FOR UPDATE", orgID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSupplierNotFound
		}
		if err != nil {
			return err
		}
		required := false
		if req.IfMatch == nil {
			if required, err = requireIfMatch(ctx, tx, orgID); err != nil {
				return err
			}
		}
		if matchErr := checkIfMatch(req.IfMatch, required, current.Version); matchErr != nil {
			return matchErr
		}
		req.apply(current)
		if invalid := validate(current); invalid != nil {
			return invalid
		}
		if req.ContactID.Set {
			if contactErr := checkContact(ctx, tx, orgID, current.ContactID); contactErr != nil {
				return contactErr
			}
		}
		s, err = scanSupplier(tx.QueryRow(ctx, `
			UPDATE supplier SET name = $3, external_ref = $4, contact_id = $5::uuid, notes = $6, version = version + 1
			WHERE organization_id = $1 AND id = $2
			RETURNING `+supplierColumns, orgID, id, current.Name, current.ExternalRef, current.ContactID, current.Notes))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSupplierNotFound
		}
		if err != nil {
			return supplierDBError(err)
		}
		return nil
	})
	return s, err
}

// DeleteSupplier removes a supplier the principal may delete
// (supplier_scoped_delete; otherwise 404). A supplier still referenced is
// a foreign key violation (422).
func (r *PGRepository) DeleteSupplier(ctx context.Context, orgID, id string) error {
	if !api.IsUUID(id) {
		return ErrSupplierNotFound
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmd, err := tx.Exec(ctx, "DELETE FROM supplier WHERE organization_id = $1 AND id = $2", orgID, id)
		if err != nil {
			return fmt.Errorf("delete supplier: %w", err)
		}
		if cmd.RowsAffected() == 0 {
			return ErrSupplierNotFound
		}
		return nil
	})
}
