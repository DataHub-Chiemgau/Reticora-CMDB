package contract

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// PGContracts stores contracts in PostgreSQL (migration 000227). Every
// access runs through database.WithRequestTenant, so row level security
// applies the organization and the client scope of the principal (E-09):
// a client-scoped principal reads org-wide contracts and those of its
// clients and writes only the latter.
type PGContracts struct {
	pool *pgxpool.Pool
}

// NewPGContracts returns the PostgreSQL contract repository.
func NewPGContracts(pool *pgxpool.Pool) *PGContracts {
	return &PGContracts{pool: pool}
}

const contractColumns = `id::text, organization_id::text, client_id::text, supplier_id::text, number, kind,
	start_date::text, end_date::text, notice_period_days, value::text, currency, status, notes, version,
	created_at, updated_at`

// contractSortColumns maps the sort fields onto their expression and cast
// (whitelist; the request never names a column). A missing date sorts as
// 'infinity', so the keyset comparison never meets NULL.
var contractSortColumns = map[string][2]string{
	"number":     {"number", "text"},
	"start_date": {"COALESCE(start_date, 'infinity'::date)", "date"},
	"end_date":   {"COALESCE(end_date, 'infinity'::date)", "date"},
	"created_at": {"created_at", "timestamptz"},
}

// contractFilterColumns maps the filter fields onto their columns.
var contractFilterColumns = map[string]string{
	"status": "status", "kind": "kind", "supplier_id": "supplier_id", "client_id": "client_id", "end_date": "end_date",
}

func scanContract(row pgx.Row) (*Contract, error) {
	c := &Contract{}
	var value *string
	if err := row.Scan(&c.ID, &c.OrganizationID, &c.ClientID, &c.SupplierID, &c.Number, &c.Kind,
		&c.StartDate, &c.EndDate, &c.NoticePeriodDays, &value, &c.Currency, &c.Status, &c.Notes, &c.Version,
		&c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	if value != nil {
		n := json.Number(*value)
		c.Value = &n
	}
	c.CreatedAt, c.UpdatedAt = c.CreatedAt.UTC(), c.UpdatedAt.UTC()
	return c, nil
}

// numericArg is the value as a parameter of a numeric column.
func numericArg(v *json.Number) *string {
	if v == nil {
		return nil
	}
	s := v.String()
	return &s
}

// contractDBError maps the constraints of contract onto the request
// fields: a taken number is 409, a client or supplier of another
// organization 422, the CHECKs 422 on their field. Other errors pass to
// api.WriteRepoError.
func contractDBError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.ConstraintName {
	case "contract_organization_id_number_key":
		return errNumberTaken
	case "contract_client_id_fkey":
		return errClient
	case "contract_supplier_id_fkey":
		return errSupplier
	case "contract_dates_check":
		return errEndDate
	case "contract_notice_period_days_check":
		return errNotice
	case "contract_currency_check":
		return errCurrency
	case "contract_kind_check":
		return errKind
	case "contract_status_check":
		return errStatus
	}
	return err
}

// checkSupplier verifies that supplierID (if any) names a supplier visible
// under the scope that is org-wide or of the contract's client: a supplier
// of another client, another organization or outside the caller's scope is
// 422 on supplier_id.
func checkSupplier(ctx context.Context, tx pgx.Tx, orgID string, c *Contract) error {
	if c.SupplierID == nil {
		return nil
	}
	var client *string
	err := tx.QueryRow(ctx, `SELECT client_id::text FROM supplier WHERE organization_id = $1 AND id = $2::uuid`,
		orgID, *c.SupplierID).Scan(&client)
	if errors.Is(err, pgx.ErrNoRows) {
		return errSupplier
	}
	if err != nil {
		return fmt.Errorf("read supplier: %w", err)
	}
	if client != nil && (c.ClientID == nil || *c.ClientID != *client) {
		return errSupplierClient
	}
	return nil
}

// ListContracts reads the contracts visible under the scope with the list
// query: one row more than the page limit for the keyset cursor.
func (r *PGContracts) ListContracts(ctx context.Context, orgID string, q *api.ListQuery, page api.PaginationParams) ([]Contract, int, error) {
	field, dir := contractOrder(q)
	if page.Cursor != nil {
		if err := contractCursorPosition(page.Cursor, field, dir); err != nil {
			return nil, 0, err
		}
	}
	items := make([]Contract, 0)
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := "organization_id = $1"
		args := []any{orgID}
		for _, f := range q.Filters {
			column, ok := contractFilterColumns[f.Field]
			if !ok {
				return &api.FieldError{Field: "filter[" + f.Field + "]", Message: "unknown filter field", Status: http.StatusUnprocessableEntity}
			}
			clause, values := f.SQL(column, len(args)+1)
			where += " AND " + clause
			args = append(args, values...)
		}
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM contract WHERE "+where, args...).Scan(&total); err != nil {
			return fmt.Errorf("count contracts: %w", err)
		}
		sortColumn := contractSortColumns[field]
		if page.Cursor != nil {
			where += " AND " + api.KeysetClause(sortColumn[0], sortColumn[1], dir, len(args)+1)
			args = append(args, page.Cursor.Value, page.Cursor.ID)
		}
		order := "ASC"
		if dir == "desc" {
			order = "DESC"
		}
		tail := fmt.Sprintf(" ORDER BY %s %s, id %s LIMIT $%d", sortColumn[0], order, order, len(args)+1)
		args = append(args, page.Limit+1)
		if page.Cursor == nil && page.Offset > 0 {
			tail += fmt.Sprintf(" OFFSET $%d", len(args)+1)
			args = append(args, page.Offset)
		}
		rows, err := tx.Query(ctx, "SELECT "+contractColumns+" FROM contract WHERE "+where+tail, args...)
		if err != nil {
			return fmt.Errorf("list contracts: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			c, scanErr := scanContract(rows)
			if scanErr != nil {
				return scanErr
			}
			items = append(items, *c)
		}
		return rows.Err()
	})
	return items, total, err
}

// GetContract reads a contract visible under the scope.
func (r *PGContracts) GetContract(ctx context.Context, orgID, id string) (*Contract, error) {
	if !api.IsUUID(id) {
		return nil, ErrContractNotFound
	}
	var c *Contract
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		c, err = scanContract(tx.QueryRow(ctx, "SELECT "+contractColumns+" FROM contract WHERE organization_id = $1 AND id = $2", orgID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrContractNotFound
		}
		return err
	})
	return c, err
}

// CreateContract stores a contract with version 1; a missing currency is
// the currency of the organization. A client of another organization is
// 422 (foreign key with the organization, K19), a client outside the
// caller's scope and an org-wide contract of a client-scoped principal
// violate WITH CHECK (403, E-09).
func (r *PGContracts) CreateContract(ctx context.Context, c *Contract) error {
	return database.WithRequestTenant(ctx, r.pool, c.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if c.Currency == "" {
			if err := tx.QueryRow(ctx, `SELECT currency FROM organization WHERE id = $1`, c.OrganizationID).Scan(&c.Currency); err != nil {
				return fmt.Errorf("read organization currency: %w", err)
			}
		}
		if err := validateContract(c); err != nil {
			return err
		}
		if err := checkSupplier(ctx, tx, c.OrganizationID, c); err != nil {
			return err
		}
		created, err := scanContract(tx.QueryRow(ctx, `
			INSERT INTO contract (organization_id, client_id, supplier_id, number, kind, start_date, end_date,
			                      notice_period_days, value, currency, status, notes)
			VALUES ($1, $2::uuid, $3::uuid, $4, $5, $6::date, $7::date, $8, $9::numeric, $10, $11, $12)
			RETURNING `+contractColumns, c.OrganizationID, c.ClientID, c.SupplierID, c.Number, c.Kind, c.StartDate,
			c.EndDate, c.NoticePeriodDays, numericArg(c.Value), c.Currency, c.Status, c.Notes))
		if err != nil {
			return contractDBError(err)
		}
		*c = *created
		return nil
	})
}

// UpdateContract changes a contract the principal may change: the row is
// locked with FOR UPDATE, which applies contract_scoped_update, so an
// org-wide contract of a client-scoped principal is not found (404). The
// check of the request (field permissions) runs first, then If-Match after
// the organization setting; every change raises the version by one.
func (r *PGContracts) UpdateContract(ctx context.Context, orgID, id string, req *UpdateContractRequest) (*Contract, error) {
	if !api.IsUUID(id) {
		return nil, ErrContractNotFound
	}
	if err := req.nonNull(); err != nil {
		return nil, err
	}
	var c *Contract
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := scanContract(tx.QueryRow(ctx, "SELECT "+contractColumns+" FROM contract WHERE organization_id = $1 AND id = $2 FOR UPDATE", orgID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrContractNotFound
		}
		if err != nil {
			return err
		}
		if req.Check != nil {
			if checkErr := req.Check(current); checkErr != nil {
				return checkErr
			}
		}
		required := false
		if req.IfMatch == nil {
			if required, err = requireIfMatch(ctx, tx, orgID); err != nil {
				return err
			}
		}
		if matchErr := checkContractIfMatch(req.IfMatch, required, current.Version); matchErr != nil {
			return matchErr
		}
		req.Apply(current)
		if invalid := validateContract(current); invalid != nil {
			return invalid
		}
		if req.SupplierID.Set {
			if supplierErr := checkSupplier(ctx, tx, orgID, current); supplierErr != nil {
				return supplierErr
			}
		}
		c, err = scanContract(tx.QueryRow(ctx, `
			UPDATE contract SET supplier_id = $3::uuid, number = $4, kind = $5, start_date = $6::date, end_date = $7::date,
			       notice_period_days = $8, value = $9::numeric, currency = $10, status = $11, notes = $12,
			       version = version + 1
			WHERE organization_id = $1 AND id = $2
			RETURNING `+contractColumns, orgID, id, current.SupplierID, current.Number, current.Kind, current.StartDate,
			current.EndDate, current.NoticePeriodDays, numericArg(current.Value), current.Currency, current.Status, current.Notes))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrContractNotFound
		}
		if err != nil {
			return contractDBError(err)
		}
		return nil
	})
	return c, err
}

// DeleteContract removes a contract the principal may delete
// (contract_scoped_delete; otherwise 404). The trigger
// contract_object_grants removes its object grants.
func (r *PGContracts) DeleteContract(ctx context.Context, orgID, id string) error {
	if !api.IsUUID(id) {
		return ErrContractNotFound
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmd, err := tx.Exec(ctx, "DELETE FROM contract WHERE organization_id = $1 AND id = $2", orgID, id)
		if err != nil {
			return fmt.Errorf("delete contract: %w", err)
		}
		if cmd.RowsAffected() == 0 {
			return ErrContractNotFound
		}
		return nil
	})
}
