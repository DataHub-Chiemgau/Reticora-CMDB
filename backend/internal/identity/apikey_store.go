package identity

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrAPIKeyNotFound is returned for an unknown or foreign key id.
var ErrAPIKeyNotFound = errors.New("identity: API key not found")

// PGAPIKeyStore implements APIKeyStore backed by PostgreSQL.
type PGAPIKeyStore struct {
	pool *pgxpool.Pool
}

// NewPGAPIKeyStore creates a new PostgreSQL-backed API key store.
func NewPGAPIKeyStore(pool *pgxpool.Pool) *PGAPIKeyStore {
	return &PGAPIKeyStore{pool: pool}
}

const apiKeyColumns = `id, organization_id, name, key_hash, key_prefix, environment, permissions,
	created_by, rotated_from, expires_at, revoked_at, last_used_at, created_at`

func scanAPIKey(row pgx.Row) (*StoredAPIKey, error) {
	var key StoredAPIKey
	var permissions []string
	if err := row.Scan(&key.ID, &key.OrganizationID, &key.Name, &key.KeyHash, &key.KeyPrefix, &key.Environment,
		&permissions, &key.CreatedBy, &key.RotatedFrom, &key.ExpiresAt, &key.RevokedAt, &key.LastUsedAt, &key.CreatedAt); err != nil {
		return nil, err
	}
	key.Permissions = make([]Permission, len(permissions))
	for i, p := range permissions {
		key.Permissions[i] = Permission(p)
	}
	return &key, nil
}

// LookupByPrefix identifies a key by its public prefix (hash index). The key
// names its organization, so no tenant is known yet: the lookup is a
// read-only system transaction (database.WithSystem, E-08) whose only
// exception is the SELECT policy api_key_system_select (migration 000080).
// Everything after the identification runs in the key organization's tenant
// transaction.
func (s *PGAPIKeyStore) LookupByPrefix(ctx context.Context, prefix string) (*StoredAPIKey, error) {
	var key *StoredAPIKey
	err := database.WithSystem(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		found, err := scanAPIKey(tx.QueryRow(ctx, `SELECT `+apiKeyColumns+` FROM api_key
			WHERE key_prefix = $1 AND revoked_at IS NULL`, prefix))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("query api_key: %w", err)
		}
		key = found
		return nil
	})
	return key, err
}

func permissionStrings(perms []Permission) []string {
	out := make([]string, len(perms))
	for i, p := range perms {
		out[i] = string(p)
	}
	return out
}

func insertAPIKey(ctx context.Context, tx pgx.Tx, key *StoredAPIKey) (*StoredAPIKey, error) {
	environment := key.Environment
	if environment == "" {
		environment = APIKeyEnvironmentLive
	}
	created, err := scanAPIKey(tx.QueryRow(ctx, `
		INSERT INTO api_key (organization_id, name, key_hash, key_prefix, environment, permissions, created_by, rotated_from, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING `+apiKeyColumns,
		key.OrganizationID, key.Name, key.KeyHash, key.KeyPrefix, environment,
		permissionStrings(key.Permissions), key.CreatedBy, key.RotatedFrom, key.ExpiresAt))
	if err != nil {
		return nil, fmt.Errorf("insert api_key: %w", err)
	}
	return created, nil
}

// recordAPIKeyAudit writes an audit entry for a key change in tx. The secret
// and its hash are never part of it.
func recordAPIKeyAudit(ctx context.Context, tx pgx.Tx, key *StoredAPIKey, action string, changes map[string]any) error {
	actor := tenant.FromContext(ctx).UserID
	actorType := "user"
	if actor == "" {
		actorType = "system"
	}
	if changes == nil {
		changes = map[string]any{}
	}
	changes["name"] = key.Name
	changes["key_prefix"] = key.KeyPrefix
	if _, err := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{
		OrganizationID: key.OrganizationID,
		ActorID:        actor,
		ActorType:      actorType,
		Action:         action,
		ResourceType:   "api_key",
		ResourceID:     key.ID,
		Changes:        changes,
	}); err != nil {
		return fmt.Errorf("audit %s: %w", action, err)
	}
	return nil
}

// Save persists a new API key record in the creating principal's tenant
// transaction.
func (s *PGAPIKeyStore) Save(ctx context.Context, key *StoredAPIKey) error {
	_, err := s.Create(ctx, key)
	return err
}

// Create persists a new API key in the creating principal's tenant
// transaction and audits it.
func (s *PGAPIKeyStore) Create(ctx context.Context, key *StoredAPIKey) (*StoredAPIKey, error) {
	var created *StoredAPIKey
	err := database.WithRequestTenant(ctx, s.pool, key.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if created, err = insertAPIKey(ctx, tx, key); err != nil {
			return err
		}
		return recordAPIKeyAudit(ctx, tx, created, "api_key.created", map[string]any{
			"permissions": permissionStrings(created.Permissions), "environment": created.Environment, "owner": created.CreatedBy,
		})
	})
	return created, err
}

// List returns the keys of the organization, newest first, without hashes.
func (s *PGAPIKeyStore) List(ctx context.Context, orgID string) ([]StoredAPIKey, error) {
	var keys []StoredAPIKey
	err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+apiKeyColumns+` FROM api_key WHERE organization_id = $1 ORDER BY created_at DESC, id`, orgID)
		if err != nil {
			return fmt.Errorf("list api_key: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			key, scanErr := scanAPIKey(rows)
			if scanErr != nil {
				return fmt.Errorf("scan api_key: %w", scanErr)
			}
			key.KeyHash = ""
			keys = append(keys, *key)
		}
		return rows.Err()
	})
	return keys, err
}

func lockAPIKey(ctx context.Context, tx pgx.Tx, orgID, id string) (*StoredAPIKey, error) {
	key, err := scanAPIKey(tx.QueryRow(ctx, `SELECT `+apiKeyColumns+` FROM api_key
		WHERE organization_id = $1 AND id = $2 FOR UPDATE`, orgID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAPIKeyNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read api_key: %w", err)
	}
	return key, nil
}

// Revoke ends a key at once. Revoking a revoked key is a no-op.
func (s *PGAPIKeyStore) Revoke(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		key, err := lockAPIKey(ctx, tx, orgID, id)
		if err != nil {
			return err
		}
		if key.RevokedAt != nil {
			return nil
		}
		if _, err = tx.Exec(ctx, `UPDATE api_key SET revoked_at = now() WHERE id = $1`, id); err != nil {
			return fmt.Errorf("revoke api_key: %w", err)
		}
		return recordAPIKeyAudit(ctx, tx, key, "api_key.revoked", nil)
	})
}

// RotatedAPIKey is the result of a rotation: the successor with its
// plaintext (shown once) and the previous key with its shortened expiry.
type RotatedAPIKey struct {
	Created   *StoredAPIKey
	Previous  *StoredAPIKey
	Plaintext string
}

// Rotate replaces a key: the new key takes over name, permissions, owner,
// environment and expiry; the old key stays valid until overlapUntil (or its
// own earlier expiry) so clients can switch without downtime (SEC-06). Both
// changes and the audit entry commit together.
func (s *PGAPIKeyStore) Rotate(ctx context.Context, orgID, id string, overlapUntil time.Time) (*RotatedAPIKey, error) {
	var created, old *StoredAPIKey
	var generated GeneratedAPIKey
	err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var txErr error
		if old, txErr = lockAPIKey(ctx, tx, orgID, id); txErr != nil {
			return txErr
		}
		if old.RevokedAt != nil || (old.ExpiresAt != nil && !old.ExpiresAt.After(time.Now())) {
			return ErrAPIKeyNotFound
		}
		if generated, txErr = GenerateAPIKey(old.Environment); txErr != nil {
			return txErr
		}
		next := &StoredAPIKey{
			OrganizationID: orgID, Name: old.Name, KeyHash: generated.KeyHash, KeyPrefix: generated.KeyPrefix,
			Environment: old.Environment, Permissions: old.Permissions, CreatedBy: old.CreatedBy,
			RotatedFrom: &old.ID, ExpiresAt: old.ExpiresAt,
		}
		if created, txErr = insertAPIKey(ctx, tx, next); txErr != nil {
			return txErr
		}
		if txErr = tx.QueryRow(ctx, `UPDATE api_key SET expires_at = LEAST(COALESCE(expires_at, $2), $2)
			WHERE id = $1 RETURNING expires_at`, old.ID, overlapUntil).Scan(&old.ExpiresAt); txErr != nil {
			return fmt.Errorf("shorten rotated api_key: %w", txErr)
		}
		return recordAPIKeyAudit(ctx, tx, created, "api_key.rotated", map[string]any{
			"rotated_from": old.ID, "old_key_expires_at": old.ExpiresAt,
		})
	})
	if err != nil {
		return nil, err
	}
	old.KeyHash = ""
	return &RotatedAPIKey{Created: created, Previous: old, Plaintext: generated.Plaintext}, nil
}

// MarkUsed updates the last_used_at timestamp. It runs while the key is being
// validated, before a principal exists, on behalf of the key's organization,
// so it uses an org-wide scope (E-08).
func (s *PGAPIKeyStore) MarkUsed(ctx context.Context, orgID, id string) error {
	scope := database.OrgWideScope(orgID, "")
	return database.WithTenant(ctx, s.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "UPDATE api_key SET last_used_at = now() WHERE id = $1", id); err != nil {
			return fmt.Errorf("update api_key last_used: %w", err)
		}
		return nil
	})
}

// RevokeUserAPIKeys revokes every active API key the user created, inside the
// caller's transaction, so a deactivation and the revocation of the user's
// service tokens commit together (TLC-04). It returns the number of keys
// revoked.
func RevokeUserAPIKeys(ctx context.Context, tx pgx.Tx, orgID, userID string) (int64, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE api_key SET revoked_at = now(), updated_at = now()
		WHERE organization_id = $1 AND created_by = $2 AND revoked_at IS NULL
	`, orgID, userID)
	if err != nil {
		return 0, fmt.Errorf("revoke api keys of user: %w", err)
	}
	return tag.RowsAffected(), nil
}

// APIKeyRepository is the identification (APIKeyStore) and the management
// (APIKeyManager) of API keys.
type APIKeyRepository interface {
	APIKeyStore
	APIKeyManager
}

// MemoryAPIKeyStore keeps API keys in memory for tests and --no-db.
type MemoryAPIKeyStore struct {
	mu   sync.Mutex
	keys []*StoredAPIKey
}

// NewMemoryAPIKeyStore creates an empty in-memory store.
func NewMemoryAPIKeyStore() *MemoryAPIKeyStore { return &MemoryAPIKeyStore{} }

func cloneAPIKey(k *StoredAPIKey) *StoredAPIKey {
	c := *k
	c.Permissions = append([]Permission(nil), k.Permissions...)
	return &c
}

// LookupByPrefix implements APIKeyStore.
func (m *MemoryAPIKeyStore) LookupByPrefix(_ context.Context, prefix string) (*StoredAPIKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range m.keys {
		if k.KeyPrefix == prefix && k.RevokedAt == nil {
			return cloneAPIKey(k), nil
		}
	}
	return nil, nil
}

// Create implements APIKeyManager.
func (m *MemoryAPIKeyStore) Create(_ context.Context, key *StoredAPIKey) (*StoredAPIKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := cloneAPIKey(key)
	id, err := randomBase62(22)
	if err != nil {
		return nil, err
	}
	k.ID, k.CreatedAt = id, time.Now().UTC()
	if k.Environment == "" {
		k.Environment = APIKeyEnvironmentLive
	}
	m.keys = append(m.keys, k)
	return cloneAPIKey(k), nil
}

// List implements APIKeyManager.
func (m *MemoryAPIKeyStore) List(_ context.Context, orgID string) ([]StoredAPIKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []StoredAPIKey
	for i := len(m.keys) - 1; i >= 0; i-- {
		if m.keys[i].OrganizationID == orgID {
			k := cloneAPIKey(m.keys[i])
			k.KeyHash = ""
			out = append(out, *k)
		}
	}
	return out, nil
}

func (m *MemoryAPIKeyStore) find(orgID, id string) *StoredAPIKey {
	for _, k := range m.keys {
		if k.OrganizationID == orgID && k.ID == id {
			return k
		}
	}
	return nil
}

// Revoke implements APIKeyManager.
func (m *MemoryAPIKeyStore) Revoke(_ context.Context, orgID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.find(orgID, id)
	if k == nil {
		return ErrAPIKeyNotFound
	}
	if k.RevokedAt == nil {
		now := time.Now().UTC()
		k.RevokedAt = &now
	}
	return nil
}

// Rotate implements APIKeyManager.
func (m *MemoryAPIKeyStore) Rotate(ctx context.Context, orgID, id string, overlapUntil time.Time) (*RotatedAPIKey, error) {
	m.mu.Lock()
	old := m.find(orgID, id)
	if old == nil || old.RevokedAt != nil || (old.ExpiresAt != nil && !old.ExpiresAt.After(time.Now())) {
		m.mu.Unlock()
		return nil, ErrAPIKeyNotFound
	}
	// The successor keeps the key's own expiry; the old key ends with the
	// overlap at the latest.
	expiry := old.ExpiresAt
	if old.ExpiresAt == nil || overlapUntil.Before(*old.ExpiresAt) {
		until := overlapUntil
		old.ExpiresAt = &until
	}
	previous := cloneAPIKey(old)
	m.mu.Unlock()
	generated, err := GenerateAPIKey(previous.Environment)
	if err != nil {
		return nil, err
	}
	created, err := m.Create(ctx, &StoredAPIKey{
		OrganizationID: orgID, Name: previous.Name, KeyHash: generated.KeyHash, KeyPrefix: generated.KeyPrefix,
		Environment: previous.Environment, Permissions: previous.Permissions, CreatedBy: previous.CreatedBy,
		RotatedFrom: &previous.ID, ExpiresAt: expiry,
	})
	if err != nil {
		return nil, err
	}
	previous.KeyHash = ""
	return &RotatedAPIKey{Created: created, Previous: previous, Plaintext: generated.Plaintext}, nil
}

// MarkUsed implements APIKeyStore.
func (m *MemoryAPIKeyStore) MarkUsed(_ context.Context, orgID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if k := m.find(orgID, id); k != nil {
		now := time.Now().UTC()
		k.LastUsedAt = &now
	}
	return nil
}
