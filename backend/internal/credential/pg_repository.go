package credential

import (
	"context"
	"fmt"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const credentialSelectColumns = `
	id::text,
	organization_id::text,
	COALESCE(client_id::text, ''),
	name,
	kind,
	COALESCE(scope, ''),
	key_version,
	created_at,
	updated_at,
	ciphertext
`

const orgDEKSelectColumns = `
	id::text,
	organization_id::text,
	encrypted_dek,
	key_version,
	created_at
`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a new PostgreSQL-backed credential repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

// withTenant executes fn within a transaction that has app.org_id set for RLS.
func (r *PGRepository) withTenant(ctx context.Context, orgID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, "SELECT set_config('app.org_id', $1, true)", orgID); err != nil {
		return fmt.Errorf("set tenant context: %w", err)
	}

	if scope := tenant.ClientScope(ctx); scope != "" {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.client_scope', $1, true)", scope); err != nil {
			return fmt.Errorf("set client scope: %w", err)
		}
	}

	if err := fn(ctx, tx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (r *PGRepository) GetOrgDEK(ctx context.Context, orgID string) (*OrgDEK, error) {
	var dek *OrgDEK
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf("SELECT %s FROM org_dek WHERE organization_id = $1", orgDEKSelectColumns)
		var err error
		dek, err = scanOrgDEK(tx.QueryRow(ctx, query, orgID))
		if err != nil {
			if err == pgx.ErrNoRows {
				return ErrNotFound
			}
			return fmt.Errorf("get org dek: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return dek, nil
}

func (r *PGRepository) CreateOrgDEK(ctx context.Context, orgID string, encryptedDEK []byte, keyVersion int) (*OrgDEK, error) {
	var dek *OrgDEK
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf(`
			INSERT INTO org_dek (
				organization_id,
				encrypted_dek,
				key_version
			) VALUES ($1, $2, $3)
			RETURNING %s
		`, orgDEKSelectColumns)
		var err error
		dek, err = scanOrgDEK(tx.QueryRow(ctx, query, orgID, encryptedDEK, keyVersion))
		if err != nil {
			return fmt.Errorf("create org dek: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return dek, nil
}

func (r *PGRepository) Create(ctx context.Context, cred *StoredCredential) error {
	return r.withTenant(ctx, cred.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		query := `
			INSERT INTO credential (
				organization_id,
				client_id,
				name,
				kind,
				ciphertext,
				key_version,
				scope
			) VALUES ($1, $2, $3, $4, $5, $6, $7)
			RETURNING id::text, created_at, updated_at
		`
		if err := tx.QueryRow(ctx, query,
			cred.OrganizationID,
			nilIfEmpty(cred.ClientID),
			cred.Name,
			cred.Kind,
			cred.Ciphertext,
			cred.KeyVersion,
			nilIfEmpty(cred.Scope),
		).Scan(&cred.ID, &cred.CreatedAt, &cred.UpdatedAt); err != nil {
			return fmt.Errorf("create credential: %w", err)
		}
		return nil
	})
}

func (r *PGRepository) Get(ctx context.Context, orgID, id string) (*StoredCredential, error) {
	var cred *StoredCredential
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf("SELECT %s FROM credential WHERE id = $1 AND organization_id = $2", credentialSelectColumns)
		var err error
		cred, err = scanStoredCredential(tx.QueryRow(ctx, query, id, orgID))
		if err != nil {
			if err == pgx.ErrNoRows {
				return ErrNotFound
			}
			return fmt.Errorf("get credential: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return cred, nil
}

func (r *PGRepository) List(ctx context.Context, orgID string) ([]Credential, error) {
	var items []Credential
	err := r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf("SELECT %s FROM credential WHERE organization_id = $1 ORDER BY created_at DESC", credentialSelectColumns)
		rows, err := tx.Query(ctx, query, orgID)
		if err != nil {
			return fmt.Errorf("list credentials: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			item, err := scanStoredCredential(rows)
			if err != nil {
				return fmt.Errorf("scan credential: %w", err)
			}
			items = append(items, item.Credential)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate credentials: %w", err)
		}
		return nil
	})
	return items, err
}

func (r *PGRepository) Update(ctx context.Context, cred *StoredCredential) error {
	return r.withTenant(ctx, cred.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		setClauses := make([]string, 0, 6)
		args := []any{cred.ID, cred.OrganizationID}
		argPos := 3

		addField := func(column string, value any) {
			setClauses = append(setClauses, fmt.Sprintf("%s = $%d", column, argPos))
			args = append(args, value)
			argPos++
		}

		addField("client_id", nilIfEmpty(cred.ClientID))
		addField("name", cred.Name)
		addField("kind", cred.Kind)
		addField("ciphertext", cred.Ciphertext)
		addField("key_version", cred.KeyVersion)
		addField("scope", nilIfEmpty(cred.Scope))
		setClauses = append(setClauses, "updated_at = NOW()")

		query := fmt.Sprintf(
			"UPDATE credential SET %s WHERE id = $1 AND organization_id = $2 RETURNING created_at, updated_at",
			strings.Join(setClauses, ", "),
		)
		if err := tx.QueryRow(ctx, query, args...).Scan(&cred.CreatedAt, &cred.UpdatedAt); err != nil {
			if err == pgx.ErrNoRows {
				return ErrNotFound
			}
			return fmt.Errorf("update credential: %w", err)
		}
		return nil
	})
}

func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	return r.withTenant(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmdTag, err := tx.Exec(ctx, "DELETE FROM credential WHERE id = $1 AND organization_id = $2", id, orgID)
		if err != nil {
			return fmt.Errorf("delete credential: %w", err)
		}
		if cmdTag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

type credentialScanner interface {
	Scan(dest ...any) error
}

func scanStoredCredential(scanner credentialScanner) (*StoredCredential, error) {
	item := &StoredCredential{}
	if err := scanner.Scan(
		&item.ID,
		&item.OrganizationID,
		&item.ClientID,
		&item.Name,
		&item.Kind,
		&item.Scope,
		&item.KeyVersion,
		&item.CreatedAt,
		&item.UpdatedAt,
		&item.Ciphertext,
	); err != nil {
		return nil, err
	}
	return item, nil
}

func scanOrgDEK(scanner credentialScanner) (*OrgDEK, error) {
	item := &OrgDEK{}
	if err := scanner.Scan(
		&item.ID,
		&item.OrganizationID,
		&item.EncryptedDEK,
		&item.KeyVersion,
		&item.CreatedAt,
	); err != nil {
		return nil, err
	}
	return item, nil
}

func nilIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
