package identity

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGAPIKeyStore implements APIKeyStore backed by PostgreSQL.
type PGAPIKeyStore struct {
	pool *pgxpool.Pool
}

// NewPGAPIKeyStore creates a new PostgreSQL-backed API key store.
func NewPGAPIKeyStore(pool *pgxpool.Pool) *PGAPIKeyStore {
	return &PGAPIKeyStore{pool: pool}
}

// LookupByPrefix returns stored key metadata for a given public ID prefix.
func (s *PGAPIKeyStore) LookupByPrefix(ctx context.Context, prefix string) (*StoredAPIKey, error) {
	query := `
		SELECT id, organization_id, name, key_hash, key_prefix, permissions,
		       created_by, expires_at, revoked_at, last_used_at, created_at
		FROM api_key
		WHERE key_prefix = $1 AND revoked_at IS NULL
	`

	row := s.pool.QueryRow(ctx, query, prefix)

	var key StoredAPIKey
	var expiresAt, revokedAt, lastUsedAt *time.Time
	var permissions []string

	if err := row.Scan(
		&key.ID,
		&key.OrganizationID,
		&key.Name,
		&key.KeyHash,
		&key.KeyPrefix,
		&permissions,
		&key.CreatedBy,
		&expiresAt,
		&revokedAt,
		&lastUsedAt,
		&key.CreatedAt,
	); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("query api_key: %w", err)
	}

	key.ExpiresAt = expiresAt
	key.RevokedAt = revokedAt
	key.LastUsedAt = lastUsedAt
	key.Permissions = make([]Permission, len(permissions))
	for i, p := range permissions {
		key.Permissions[i] = Permission(p)
	}

	return &key, nil
}

// Save persists a new API key record.
func (s *PGAPIKeyStore) Save(ctx context.Context, key StoredAPIKey) error {
	permissions := make([]string, len(key.Permissions))
	for i, p := range key.Permissions {
		permissions[i] = string(p)
	}

	query := `
		INSERT INTO api_key (organization_id, name, key_hash, key_prefix, permissions, created_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`
	_, err := s.pool.Exec(ctx, query,
		key.OrganizationID,
		key.Name,
		key.KeyHash,
		key.KeyPrefix,
		permissions,
		key.CreatedBy,
		key.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("insert api_key: %w", err)
	}

	return nil
}

// MarkUsed updates the last_used_at timestamp.
func (s *PGAPIKeyStore) MarkUsed(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx,
		"UPDATE api_key SET last_used_at = now() WHERE id = $1",
		id,
	)
	if err != nil {
		return fmt.Errorf("update api_key last_used: %w", err)
	}
	return nil
}
