// Package pii is the PII vault (SEC-05, CH16): personal data of users and
// contacts is stored encrypted under the organization's data key in
// pii_subject, and every other table references the person only by the
// surrogate id. Resolving a surrogate works only within its organization
// (row level security and an explicit organization check); a deleted
// subject cannot be resolved anymore, which is the basis of
// crypto-shredding (the deletion and export workflows follow in P3).
package pii

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

// Kinds of subjects.
const (
	KindUser    = "user"
	KindContact = "contact"
	KindPerson  = "person"
)

// Data is the personal data of a subject, e.g. name, email, phone.
type Data map[string]string

// Subject is a resolved vault entry.
type Subject struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	Kind           string `json:"kind"`
	Data           Data   `json:"data"`
}

// Errors of the vault.
var (
	// ErrNotFound: the surrogate does not exist in the organization or was
	// deleted.
	ErrNotFound = errors.New("pii subject not found")
	// ErrNoKey: the vault has no master key encryptor.
	ErrNoKey = errors.New("pii vault needs the master key")
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isUUID(s string) bool { return uuidPattern.MatchString(s) }

// sealed is the encrypted plaintext: the data bound to its subject and
// organization, so a payload copied to another row does not resolve.
type sealed struct {
	Subject      string `json:"s"`
	Organization string `json:"o"`
	Data         Data   `json:"d"`
}

// Vault stores and resolves subjects.
type Vault struct {
	pool *pgxpool.Pool
	enc  *crypto.EnvelopeEncryptor
}

// NewVault creates a vault on the pool with the master key encryptor that
// wraps the organizations' data keys.
func NewVault(pool *pgxpool.Pool, enc *crypto.EnvelopeEncryptor) *Vault {
	return &Vault{pool: pool, enc: enc}
}

// Create stores data as a new subject of the organization and returns its
// surrogate id.
func (v *Vault) Create(ctx context.Context, orgID, kind string, data Data) (string, error) {
	var id string
	err := database.WithRequestTenant(ctx, v.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = v.CreateTx(ctx, tx, orgID, kind, data)
		return err
	})
	return id, err
}

// Resolve returns the subject of a surrogate id.
func (v *Vault) Resolve(ctx context.Context, orgID, id string) (*Subject, error) {
	var out *Subject
	err := database.WithRequestTenant(ctx, v.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		subjects, err := v.ResolveTx(ctx, tx, orgID, []string{id})
		if err != nil {
			return err
		}
		if out = subjects[id]; out == nil {
			return ErrNotFound
		}
		return nil
	})
	return out, err
}

// ResolveMany returns the subjects of the surrogate ids by id; unknown and
// deleted ids are left out.
func (v *Vault) ResolveMany(ctx context.Context, orgID string, ids []string) (map[string]*Subject, error) {
	var out map[string]*Subject
	err := database.WithRequestTenant(ctx, v.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = v.ResolveTx(ctx, tx, orgID, ids)
		return err
	})
	return out, err
}

// Update replaces the data of a subject.
func (v *Vault) Update(ctx context.Context, orgID, id string, data Data) error {
	return database.WithRequestTenant(ctx, v.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return v.UpdateTx(ctx, tx, orgID, id, data)
	})
}

// Delete removes a subject; it cannot be resolved afterwards.
func (v *Vault) Delete(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, v.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return DeleteTx(ctx, tx, orgID, id)
	})
}

// CreateTx is Create inside the caller's tenant transaction.
func (v *Vault) CreateTx(ctx context.Context, tx pgx.Tx, orgID, kind string, data Data) (string, error) {
	return v.createTx(ctx, tx, orgID, kind, data, "")
}

// createTx stores a subject; a parent (the person a value belongs to)
// takes the subject with it when it is deleted.
func (v *Vault) createTx(ctx context.Context, tx pgx.Tx, orgID, kind string, data Data, parent string) (string, error) {
	dek, version, err := v.orgKey(ctx, tx, orgID)
	if err != nil {
		return "", err
	}
	var id string
	if err = tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		return "", fmt.Errorf("new surrogate: %w", err)
	}
	payload, err := seal(dek, id, orgID, data)
	if err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO pii_subject (surrogate_id, organization_id, kind, encrypted_payload, dek_ref, parent_id, search_tokens)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, id, orgID, kind, payload, version, nilIfEmpty(parent), searchTokens(dek, kind, data)); err != nil {
		return "", fmt.Errorf("store pii subject: %w", err)
	}
	return id, nil
}

// ResolveTx is ResolveMany inside the caller's tenant transaction.
func (v *Vault) ResolveTx(ctx context.Context, tx pgx.Tx, orgID string, ids []string) (map[string]*Subject, error) {
	out := map[string]*Subject{}
	valid := make([]string, 0, len(ids))
	for _, id := range ids {
		if isUUID(id) {
			valid = append(valid, id)
		}
	}
	if len(valid) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT surrogate_id::text, kind, encrypted_payload, dek_ref FROM pii_subject
		WHERE organization_id = $1 AND surrogate_id = ANY($2::uuid[])`, orgID, valid)
	if err != nil {
		return nil, fmt.Errorf("read pii subjects: %w", err)
	}
	type row struct {
		id, kind string
		payload  []byte
		version  int
	}
	stored, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var s row
		scanErr := r.Scan(&s.id, &s.kind, &s.payload, &s.version)
		return s, scanErr
	})
	if err != nil {
		return nil, fmt.Errorf("read pii subjects: %w", err)
	}
	if len(stored) == 0 {
		return out, nil
	}
	dek, version, err := v.orgKey(ctx, tx, orgID)
	if err != nil {
		return nil, err
	}
	for _, s := range stored {
		if s.version != version {
			return nil, fmt.Errorf("pii subject %s: encrypted with data key version %d, current is %d", s.id, s.version, version)
		}
		data, oerr := open(dek, s.id, orgID, s.payload)
		if oerr != nil {
			return nil, oerr
		}
		out[s.id] = &Subject{ID: s.id, OrganizationID: orgID, Kind: s.kind, Data: data}
	}
	return out, nil
}

// UpdateTx is Update inside the caller's tenant transaction.
func (v *Vault) UpdateTx(ctx context.Context, tx pgx.Tx, orgID, id string, data Data) error {
	if !isUUID(id) {
		return ErrNotFound
	}
	dek, version, err := v.orgKey(ctx, tx, orgID)
	if err != nil {
		return err
	}
	payload, err := seal(dek, id, orgID, data)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE pii_subject SET encrypted_payload = $3, dek_ref = $4,
			search_tokens = CASE WHEN kind = 'value' THEN '{}' ELSE $5::text[] END
		WHERE organization_id = $1 AND surrogate_id = $2`, orgID, id, payload, version, searchTokens(dek, "", data))
	if err != nil {
		return fmt.Errorf("update pii subject: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteTx is Delete inside the caller's tenant transaction.
func DeleteTx(ctx context.Context, tx pgx.Tx, orgID, id string) error {
	if !isUUID(id) {
		return ErrNotFound
	}
	tag, err := tx.Exec(ctx, `DELETE FROM pii_subject WHERE organization_id = $1 AND surrogate_id = $2`, orgID, id)
	if err != nil {
		return fmt.Errorf("delete pii subject: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// orgKey returns the organization's data key and its version; an
// organization without one gets it (the credential store shares it).
func (v *Vault) orgKey(ctx context.Context, tx pgx.Tx, orgID string) (dek []byte, version int, err error) {
	if v.enc == nil {
		return nil, 0, ErrNoKey
	}
	var wrapped []byte
	err = tx.QueryRow(ctx, `SELECT encrypted_dek, key_version FROM org_dek WHERE organization_id = $1`, orgID).
		Scan(&wrapped, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		fresh, gerr := crypto.GenerateDEK()
		if gerr != nil {
			return nil, 0, gerr
		}
		if wrapped, err = v.enc.WrapDEK(fresh); err != nil {
			return nil, 0, err
		}
		// A concurrent first use stores its key first: take that one.
		if _, err = tx.Exec(ctx, `INSERT INTO org_dek (organization_id, encrypted_dek, key_version) VALUES ($1, $2, 1)
			ON CONFLICT (organization_id) DO NOTHING`, orgID, wrapped); err != nil {
			return nil, 0, fmt.Errorf("create org dek: %w", err)
		}
		err = tx.QueryRow(ctx, `SELECT encrypted_dek, key_version FROM org_dek WHERE organization_id = $1`, orgID).
			Scan(&wrapped, &version)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("read org dek: %w", err)
	}
	if dek, err = v.enc.UnwrapDEK(wrapped); err != nil {
		return nil, 0, fmt.Errorf("unwrap org dek: %w", err)
	}
	return dek, version, nil
}

func seal(dek []byte, id, orgID string, data Data) ([]byte, error) {
	plain, err := json.Marshal(sealed{Subject: id, Organization: orgID, Data: data})
	if err != nil {
		return nil, fmt.Errorf("encode pii subject: %w", err)
	}
	return crypto.Encrypt(dek, plain)
}

func open(dek []byte, id, orgID string, payload []byte) (Data, error) {
	plain, err := crypto.Decrypt(dek, payload)
	if err != nil {
		return nil, fmt.Errorf("decrypt pii subject %s: %w", id, err)
	}
	var s sealed
	if err = json.Unmarshal(plain, &s); err != nil {
		return nil, fmt.Errorf("decode pii subject %s: %w", id, err)
	}
	if s.Subject != id || s.Organization != orgID {
		return nil, fmt.Errorf("pii subject %s: payload belongs to another subject", id)
	}
	return s.Data, nil
}

// ReencryptTx moves every subject of the organization from the data key
// oldDEK to newDEK with the new version, inside the rotation's
// transaction (SEC-06); it returns how many it re-encrypted.
func ReencryptTx(ctx context.Context, tx pgx.Tx, orgID string, oldDEK, newDEK []byte, version int) (int, error) {
	rows, err := tx.Query(ctx, `SELECT surrogate_id::text, kind, encrypted_payload FROM pii_subject
		WHERE organization_id = $1 FOR UPDATE`, orgID)
	if err != nil {
		return 0, fmt.Errorf("lock pii subjects: %w", err)
	}
	type row struct {
		id, kind string
		payload  []byte
	}
	stored, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var s row
		scanErr := r.Scan(&s.id, &s.kind, &s.payload)
		return s, scanErr
	})
	if err != nil {
		return 0, fmt.Errorf("lock pii subjects: %w", err)
	}
	for _, s := range stored {
		plain, derr := crypto.Decrypt(oldDEK, s.payload)
		if derr != nil {
			return 0, fmt.Errorf("pii subject %s: %w", s.id, derr)
		}
		payload, eerr := crypto.Encrypt(newDEK, plain)
		if eerr != nil {
			return 0, fmt.Errorf("pii subject %s: %w", s.id, eerr)
		}
		// The blind index follows the data key.
		var content sealed
		if uerr := json.Unmarshal(plain, &content); uerr != nil {
			return 0, fmt.Errorf("pii subject %s: %w", s.id, uerr)
		}
		if _, err = tx.Exec(ctx, `UPDATE pii_subject SET encrypted_payload = $2, dek_ref = $3, search_tokens = $4 WHERE surrogate_id = $1`,
			s.id, payload, version, searchTokens(newDEK, s.kind, content.Data)); err != nil {
			return 0, fmt.Errorf("pii subject %s: %w", s.id, err)
		}
	}
	return len(stored), nil
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
