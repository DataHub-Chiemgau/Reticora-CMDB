package credential

import (
	"cmp"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

// MemoryRepository is an in-memory implementation of Repository for development/testing.
type MemoryRepository struct {
	mu          sync.RWMutex
	credentials map[string]*StoredCredential // keyed by id
	deks        map[string]*OrgDEK           // keyed by organization_id
}

// NewMemoryRepository creates a new in-memory credential repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		credentials: make(map[string]*StoredCredential),
		deks:        make(map[string]*OrgDEK),
	}
}

func (r *MemoryRepository) GetOrgDEK(_ context.Context, orgID string) (*OrgDEK, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	dek, ok := r.deks[orgID]
	if !ok {
		return nil, ErrNotFound
	}
	result := *dek
	return &result, nil
}

func (r *MemoryRepository) CreateOrgDEK(_ context.Context, orgID string, encryptedDEK []byte, keyVersion int) (*OrgDEK, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, err := generateID()
	if err != nil {
		return nil, err
	}
	dek := &OrgDEK{
		ID:             id,
		OrganizationID: orgID,
		EncryptedDEK:   encryptedDEK,
		KeyVersion:     keyVersion,
		CreatedAt:      time.Now().UTC(),
	}
	r.deks[orgID] = dek
	return dek, nil
}

func (r *MemoryRepository) Create(_ context.Context, cred *StoredCredential) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, err := generateID()
	if err != nil {
		return err
	}
	cred.ID = id
	stored := *cred
	r.credentials[cred.ID] = &stored
	return nil
}

func (r *MemoryRepository) Get(_ context.Context, orgID, id string) (*StoredCredential, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cred, ok := r.credentials[id]
	if !ok || cred.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	result := *cred
	return &result, nil
}

func (r *MemoryRepository) List(_ context.Context, orgID string) ([]Credential, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var results []Credential
	for _, c := range r.credentials {
		if c.OrganizationID == orgID {
			results = append(results, c.Credential)
		}
	}
	// The order of the PostgreSQL repository (newest first, then id): the
	// list is paged by offset (API-02).
	slices.SortFunc(results, func(a, b Credential) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), cmp.Compare(b.ID, a.ID))
	})
	return results, nil
}

func (r *MemoryRepository) Update(_ context.Context, cred *StoredCredential) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.credentials[cred.ID]
	if !ok || existing.OrganizationID != cred.OrganizationID {
		return ErrNotFound
	}
	stored := *cred
	r.credentials[cred.ID] = &stored
	return nil
}

func (r *MemoryRepository) Delete(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cred, ok := r.credentials[id]
	if !ok || cred.OrganizationID != orgID {
		return ErrNotFound
	}
	delete(r.credentials, id)
	return nil
}

// ListStored returns every credential of the organization including
// ciphertext (implements StoredLister for DEK rotation).
func (r *MemoryRepository) ListStored(_ context.Context, orgID string) ([]StoredCredential, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var results []StoredCredential
	for _, c := range r.credentials {
		if c.OrganizationID == orgID {
			results = append(results, *c)
		}
	}
	return results, nil
}

// UpdateOrgDEK replaces the wrapped DEK and bumps the key version
// (implements DEKUpdater for DEK rotation).
func (r *MemoryRepository) UpdateOrgDEK(_ context.Context, orgID string, encryptedDEK []byte, keyVersion int) (*OrgDEK, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	dek, ok := r.deks[orgID]
	if !ok {
		return nil, ErrNotFound
	}
	dek.EncryptedDEK = encryptedDEK
	dek.KeyVersion = keyVersion
	result := *dek
	return &result, nil
}

// generateID creates a simple random hex ID for in-memory use.
func generateID() (string, error) {
	b := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", fmt.Errorf("credential: failed to generate random ID: %w", err)
	}
	// Format as UUID-like string
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// ReplaceSecret implements SecretReplacer.
func (r *MemoryRepository) ReplaceSecret(ctx context.Context, orgID, id string, plaintext []byte, enc *crypto.EnvelopeEncryptor,
	inTx func(ctx context.Context, cred *Credential) error) (*Credential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cred, ok := r.credentials[id]
	if !ok || cred.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	if cred.Source == SourceVaultRef {
		return nil, ErrVaultRefHasNoSecret
	}
	dekRow, ok := r.deks[orgID]
	if !ok {
		return nil, ErrNotFound
	}
	dek, err := enc.UnwrapDEK(dekRow.EncryptedDEK)
	if err != nil {
		return nil, err
	}
	ciphertext, err := crypto.Encrypt(dek, plaintext)
	if err != nil {
		return nil, err
	}
	next := *cred
	now := time.Now().UTC()
	next.Ciphertext = ciphertext
	next.SecretVersion = cred.SecretVersion + 1
	next.RotatedAt = &now
	next.UpdatedAt = now
	out := next.Credential
	if inTx != nil {
		if txErr := inTx(ctx, &out); txErr != nil {
			return nil, txErr
		}
	}
	r.credentials[id] = &next
	return &out, nil
}

// UpdateMetadata implements MetadataUpdater.
func (r *MemoryRepository) UpdateMetadata(_ context.Context, orgID, id string, u MetadataUpdate) (*Credential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cred, ok := r.credentials[id]
	if !ok || cred.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	if u.Name != nil {
		cred.Name = *u.Name
	}
	if u.SetMaxAge {
		cred.MaxAgeDays = u.MaxAgeDays
	}
	// A change of the reference raises secret_version (WP-894).
	if u.Ref.Changes() && cred.VaultRef != nil {
		ref := *cred.VaultRef
		if u.Ref.Path != nil {
			ref.Path = *u.Ref.Path
		}
		if u.Ref.SetVersion {
			ref.Version = u.Ref.Version
		}
		if u.Ref.FieldMap != nil {
			ref.FieldMap = u.Ref.FieldMap
		}
		cred.VaultRef = &ref
		cred.SecretVersion++
	}
	cred.UpdatedAt = time.Now().UTC()
	out := cred.Credential
	return &out, nil
}
