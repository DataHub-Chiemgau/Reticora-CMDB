package admp

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// Conflicts of the account store (409).
var (
	errNameTaken = &api.ConflictError{Field: "name", Message: "an ADMP account of this name exists"}
	// errActiveTaken: a client has one enabled account, the organization
	// one enabled organization-wide account (AK2).
	errActiveTaken = &api.ConflictError{Field: "enabled",
		Message: "the client or the organization already has an enabled ADMP account; disable it first"}
)

// --- PostgreSQL -----------------------------------------------------------

// PGAccountRepository stores the accounts in admp_account under row level
// security.
type PGAccountRepository struct {
	pool *pgxpool.Pool
}

// NewPGAccountRepository creates the PostgreSQL account store.
func NewPGAccountRepository(pool *pgxpool.Pool) *PGAccountRepository {
	return &PGAccountRepository{pool: pool}
}

const accountColumns = `id::text, organization_id::text, COALESCE(client_id::text, ''), name, base_url, COALESCE(auth_url, ''),
	COALESCE(credential_id::text, ''), rate_limit_rps::float8, timeout_ms, enabled, last_ok_at, last_error_class,
	created_at, updated_at`

func scanAccount(row pgx.Row) (*Account, error) {
	var a Account
	err := row.Scan(&a.ID, &a.OrganizationID, &a.ClientID, &a.Name, &a.BaseURL, &a.AuthURL, &a.CredentialID,
		&a.RateLimitRPS, &a.TimeoutMS, &a.Enabled, &a.LastOKAt, &a.LastErrorClass, &a.CreatedAt, &a.UpdatedAt)
	return &a, err
}

// accountError maps the constraints of admp_account.
func accountError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.ConstraintName {
	case "admp_account_name_key":
		return errNameTaken
	case "uq_admp_account_client_active", "uq_admp_account_org_active":
		return errActiveTaken
	case "admp_account_client_fkey":
		return fieldError("client_id", "client not found")
	case "admp_account_credential_fkey":
		return fieldError("credential_id", "credential not found")
	}
	return err
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// CreateAccount implements AccountRepository.
func (r *PGAccountRepository) CreateAccount(ctx context.Context, a *Account) error {
	return database.WithRequestTenant(ctx, r.pool, a.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		stored, err := scanAccount(tx.QueryRow(ctx, `INSERT INTO admp_account (organization_id, client_id, name, base_url,
				auth_url, credential_id, rate_limit_rps, timeout_ms, enabled)
			VALUES (current_setting('app.org_id')::uuid, $1, $2, $3, $4, $5, $6, $7, $8) RETURNING `+accountColumns,
			nilIfEmpty(a.ClientID), a.Name, a.BaseURL, nilIfEmpty(a.AuthURL), nilIfEmpty(a.CredentialID), a.RateLimitRPS,
			a.TimeoutMS, a.Enabled))
		if err != nil {
			return accountError(err)
		}
		*a = *stored
		return nil
	})
}

// ListAccounts implements AccountRepository.
func (r *PGAccountRepository) ListAccounts(ctx context.Context, orgID string) ([]Account, error) {
	out := []Account{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+accountColumns+` FROM admp_account ORDER BY name, id`)
		if err != nil {
			return fmt.Errorf("list admp accounts: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			a, scanErr := scanAccount(rows)
			if scanErr != nil {
				return fmt.Errorf("scan admp account: %w", scanErr)
			}
			out = append(out, *a)
		}
		return rows.Err()
	})
	return out, err
}

// GetAccount implements AccountRepository.
func (r *PGAccountRepository) GetAccount(ctx context.Context, orgID, id string) (*Account, error) {
	if !api.IsUUID(id) {
		return nil, ErrAccountNotFound
	}
	var out *Account
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		a, err := scanAccount(tx.QueryRow(ctx, `SELECT `+accountColumns+` FROM admp_account WHERE id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAccountNotFound
		}
		out = a
		return err
	})
	return out, err
}

// UpdateAccount implements AccountRepository.
func (r *PGAccountRepository) UpdateAccount(ctx context.Context, a *Account) error {
	if !api.IsUUID(a.ID) {
		return ErrAccountNotFound
	}
	return database.WithRequestTenant(ctx, r.pool, a.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		stored, err := scanAccount(tx.QueryRow(ctx, `UPDATE admp_account SET name = $2, base_url = $3, auth_url = $4,
				credential_id = $5, rate_limit_rps = $6, timeout_ms = $7, enabled = $8
			WHERE id = $1 RETURNING `+accountColumns,
			a.ID, a.Name, a.BaseURL, nilIfEmpty(a.AuthURL), nilIfEmpty(a.CredentialID), a.RateLimitRPS, a.TimeoutMS, a.Enabled))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAccountNotFound
		}
		if err != nil {
			return accountError(err)
		}
		*a = *stored
		return nil
	})
}

// DeleteAccount implements AccountRepository; the cache of the account
// goes with it (cascade, WP-928).
func (r *PGAccountRepository) DeleteAccount(ctx context.Context, orgID, id string) error {
	if !api.IsUUID(id) {
		return ErrAccountNotFound
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM admp_account WHERE id = $1`, id)
		if err != nil {
			return accountError(err)
		}
		if tag.RowsAffected() == 0 {
			return ErrAccountNotFound
		}
		return nil
	})
}

var _ AccountRepository = (*PGAccountRepository)(nil)

// --- memory (--no-db) -----------------------------------------------------

// MemoryAccountRepository keeps the accounts in memory with the rules of
// the table: unique name, one enabled account per client and one
// organization-wide, the client scope of the caller (E-09). Without
// foreign key a deleted credential is noticed by the AccountService
// (UncheckedCredentials); the client is not checked against the tenant
// hierarchy, like the other memory stores of --no-db.
type MemoryAccountRepository struct {
	mu   sync.Mutex
	rows map[string]*Account
}

// NewMemoryAccountRepository creates the in-memory account store.
func NewMemoryAccountRepository() *MemoryAccountRepository {
	return &MemoryAccountRepository{rows: map[string]*Account{}}
}

// UncheckedCredentials implements credentialsUnchecked: the store keeps a
// credential reference without foreign key.
func (m *MemoryAccountRepository) UncheckedCredentials() {}

// visible reports whether the caller of ctx reads an account of clientID:
// only those of its clients, the organization-wide ones only an
// organization-wide caller (AK6, E-09).
func visible(ctx context.Context, clientID string) bool {
	return inScope(ctx, clientID)
}

func (m *MemoryAccountRepository) conflict(a *Account) error {
	for _, other := range m.rows {
		if other.ID == a.ID || other.OrganizationID != a.OrganizationID {
			continue
		}
		if other.Name == a.Name {
			return errNameTaken
		}
		if a.Enabled && other.Enabled && other.ClientID == a.ClientID {
			return errActiveTaken
		}
	}
	return nil
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

// CreateAccount implements AccountRepository.
func (m *MemoryAccountRepository) CreateAccount(ctx context.Context, a *Account) error {
	if !inScope(ctx, a.ClientID) {
		return ErrOutsideScope
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.conflict(a); err != nil {
		return err
	}
	id, err := newID()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	a.ID, a.CreatedAt, a.UpdatedAt = id, now, now
	stored := *a
	stored.Credential = nil
	m.rows[id] = &stored
	return nil
}

// ListAccounts implements AccountRepository.
func (m *MemoryAccountRepository) ListAccounts(ctx context.Context, orgID string) ([]Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Account{}
	for _, a := range m.rows {
		if a.OrganizationID == orgID && visible(ctx, a.ClientID) {
			out = append(out, *a)
		}
	}
	slices.SortFunc(out, func(a, b Account) int { return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID)) })
	return out, nil
}

// GetAccount implements AccountRepository.
func (m *MemoryAccountRepository) GetAccount(ctx context.Context, orgID, id string) (*Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.rows[id]
	if !ok || a.OrganizationID != orgID || !visible(ctx, a.ClientID) {
		return nil, ErrAccountNotFound
	}
	out := *a
	return &out, nil
}

// UpdateAccount implements AccountRepository.
func (m *MemoryAccountRepository) UpdateAccount(ctx context.Context, a *Account) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.rows[a.ID]
	if !ok || current.OrganizationID != a.OrganizationID || !visible(ctx, current.ClientID) {
		return ErrAccountNotFound
	}
	if !inScope(ctx, current.ClientID) {
		return ErrOutsideScope
	}
	if err := m.conflict(a); err != nil {
		return err
	}
	a.ClientID, a.LastOKAt, a.LastErrorClass, a.CreatedAt = current.ClientID, current.LastOKAt, current.LastErrorClass, current.CreatedAt
	// As trg_admp_account_credential_missing: a removed credential marks
	// the account, an attached one clears the mark.
	switch {
	case current.CredentialID != "" && a.CredentialID == "":
		missing := ErrorCredentialMissing
		a.LastErrorClass = &missing
	case a.CredentialID != "" && current.LastErrorClass != nil && *current.LastErrorClass == ErrorCredentialMissing:
		a.LastErrorClass = nil
	}
	a.UpdatedAt = time.Now().UTC()
	stored := *a
	stored.Credential = nil
	m.rows[a.ID] = &stored
	return nil
}

// DeleteAccount implements AccountRepository.
func (m *MemoryAccountRepository) DeleteAccount(ctx context.Context, orgID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.rows[id]
	if !ok || a.OrganizationID != orgID || !visible(ctx, a.ClientID) {
		return ErrAccountNotFound
	}
	if !inScope(ctx, a.ClientID) {
		return ErrOutsideScope
	}
	delete(m.rows, id)
	return nil
}

// clientOf returns the client of an account ("" for an organization-wide
// one) and whether it exists, without the caller's scope: the memory cache
// derives its scope from it like the triggers of admp_cache.
func (m *MemoryAccountRepository) clientOf(orgID, id string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.rows[id]
	if !ok || a.OrganizationID != orgID {
		return "", false
	}
	return a.ClientID, true
}

var _ AccountRepository = (*MemoryAccountRepository)(nil)
