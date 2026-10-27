package credential

import (
	"context"
	"fmt"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
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

func (r *PGRepository) GetOrgDEK(ctx context.Context, orgID string) (*OrgDEK, error) {
	var dek *OrgDEK
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
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
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
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
	return database.WithRequestTenant(ctx, r.pool, cred.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
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
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
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
	items := make([]Credential, 0)
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf("SELECT %s FROM credential WHERE organization_id = $1 ORDER BY created_at DESC, id DESC", credentialSelectColumns)
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
	return database.WithRequestTenant(ctx, r.pool, cred.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
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
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
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

// ListStored returns every credential of the organization including its
// ciphertext (implements StoredLister for DEK rotation).
func (r *PGRepository) ListStored(ctx context.Context, orgID string) ([]StoredCredential, error) {
	items := make([]StoredCredential, 0)
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf("SELECT %s FROM credential WHERE organization_id = $1 ORDER BY created_at ASC", credentialSelectColumns)
		rows, err := tx.Query(ctx, query, orgID)
		if err != nil {
			return fmt.Errorf("list stored credentials: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			item, err := scanStoredCredential(rows)
			if err != nil {
				return fmt.Errorf("scan credential: %w", err)
			}
			items = append(items, *item)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate credentials: %w", err)
		}
		return nil
	})
	return items, err
}

// UpdateOrgDEK replaces the wrapped DEK row and bumps the key version
// (implements DEKUpdater for DEK rotation).
func (r *PGRepository) UpdateOrgDEK(ctx context.Context, orgID string, encryptedDEK []byte, keyVersion int) (*OrgDEK, error) {
	var dek *OrgDEK
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		query := fmt.Sprintf(`
			UPDATE org_dek
			SET encrypted_dek = $2, key_version = $3, created_at = now()
			WHERE organization_id = $1
			RETURNING %s
		`, orgDEKSelectColumns)
		var err error
		dek, err = scanOrgDEK(tx.QueryRow(ctx, query, orgID, encryptedDEK, keyVersion))
		if err != nil {
			if err == pgx.ErrNoRows {
				return ErrNotFound
			}
			return fmt.Errorf("update org dek: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return dek, nil
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

// KeyRotator rotates and rewraps an organization's DEK in one transaction
// (SEC-06): a failure leaves DEK and credentials as they were.
type KeyRotator interface {
	RotateOrgDEK(ctx context.Context, orgID string, enc *crypto.EnvelopeEncryptor) (*RotationResult, error)
	RewrapOrgDEK(ctx context.Context, orgID string, enc *crypto.EnvelopeEncryptor) (bool, error)
}

// RotateOrgDEK implements KeyRotator: a fresh DEK, every credential and
// PII vault entry of the organization re-encrypted under it with the new key version, the old
// DEK replaced — all in one transaction, so the old DEK is retired exactly
// when nothing depends on it anymore. The DEK's created_at records the
// rotation.
func (r *PGRepository) RotateOrgDEK(ctx context.Context, orgID string, enc *crypto.EnvelopeEncryptor) (*RotationResult, error) {
	var result *RotationResult
	scope := database.OrgWideScope(orgID, "")
	err := database.WithTenant(ctx, r.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		var wrapped []byte
		var version int
		err := tx.QueryRow(ctx, `SELECT encrypted_dek, key_version FROM org_dek WHERE organization_id = $1 FOR UPDATE`, orgID).
			Scan(&wrapped, &version)
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock org dek: %w", err)
		}
		oldDEK, err := enc.UnwrapDEK(wrapped)
		if err != nil {
			return fmt.Errorf("unwrap org dek: %w", err)
		}
		newDEK, err := crypto.GenerateDEK()
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id::text, ciphertext FROM credential WHERE organization_id = $1 FOR UPDATE`, orgID)
		if err != nil {
			return fmt.Errorf("lock credentials: %w", err)
		}
		type stored struct {
			id         string
			ciphertext []byte
		}
		creds, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (stored, error) {
			var c stored
			scanErr := row.Scan(&c.id, &c.ciphertext)
			return c, scanErr
		})
		if err != nil {
			return fmt.Errorf("lock credentials: %w", err)
		}
		version++
		for _, c := range creds {
			plain, decErr := crypto.Decrypt(oldDEK, c.ciphertext)
			if decErr != nil {
				return fmt.Errorf("credential %s: %w", c.id, decErr)
			}
			ciphertext, encErr := crypto.Encrypt(newDEK, plain)
			if encErr != nil {
				return fmt.Errorf("credential %s: %w", c.id, encErr)
			}
			if _, err = tx.Exec(ctx, `UPDATE credential SET ciphertext = $2, key_version = $3, updated_at = now() WHERE id = $1`,
				c.id, ciphertext, version); err != nil {
				return fmt.Errorf("credential %s: %w", c.id, err)
			}
		}
		// The PII vault shares the data key (SEC-05).
		subjects, err := pii.ReencryptTx(ctx, tx, orgID, oldDEK, newDEK, version)
		if err != nil {
			return err
		}
		newWrapped, err := enc.WrapDEK(newDEK)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE org_dek SET encrypted_dek = $2, key_version = $3, created_at = now() WHERE organization_id = $1`,
			orgID, newWrapped, version); err != nil {
			return fmt.Errorf("replace org dek: %w", err)
		}
		result = &RotationResult{KeyVersion: version, Rotated: len(creds), Subjects: subjects}
		return nil
	})
	return result, err
}

// RewrapOrgDEK implements KeyRotator: the DEK wrapped with a previous
// master key is wrapped with the current one; it reports whether it
// changed anything.
func (r *PGRepository) RewrapOrgDEK(ctx context.Context, orgID string, enc *crypto.EnvelopeEncryptor) (bool, error) {
	changed := false
	scope := database.OrgWideScope(orgID, "")
	err := database.WithTenant(ctx, r.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		var wrapped []byte
		err := tx.QueryRow(ctx, `SELECT encrypted_dek FROM org_dek WHERE organization_id = $1 FOR UPDATE`, orgID).Scan(&wrapped)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock org dek: %w", err)
		}
		if enc.IsCurrent(wrapped) {
			return nil
		}
		rewrapped, err := enc.Rewrap(wrapped)
		if err != nil {
			return fmt.Errorf("rewrap org dek: %w", err)
		}
		if _, err = tx.Exec(ctx, `UPDATE org_dek SET encrypted_dek = $2 WHERE organization_id = $1`, orgID, rewrapped); err != nil {
			return fmt.Errorf("store rewrapped org dek: %w", err)
		}
		changed = true
		return nil
	})
	return changed, err
}
