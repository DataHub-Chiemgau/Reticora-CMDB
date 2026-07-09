package credential

import (
	"context"
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
	return dek, nil
}

func (r *MemoryRepository) CreateOrgDEK(_ context.Context, orgID string, encryptedDEK []byte, keyVersion int) (*OrgDEK, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	dek := &OrgDEK{
		ID:             generateID(),
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
	cred.ID = generateID()
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

// generateID creates a simple random hex ID for in-memory use.
func generateID() string {
	dek, _ := crypto.GenerateDEK()
	// Use first 16 bytes as hex UUID-like identifier
	const hexChars = "0123456789abcdef"
	id := make([]byte, 36)
	copy(id, "00000000-0000-0000-0000-")
	for i := 24; i < 36; i++ {
		id[i] = hexChars[dek[i-24]%16]
	}
	return string(id)
}
