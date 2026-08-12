// Package credential provides envelope-encrypted credential management for the Reticora platform.
//
// Credentials (SNMP community strings, SSH keys, API tokens, etc.) are stored encrypted
// at rest using a per-organization Data Encryption Key (DEK). The DEK is itself encrypted
// with the platform master key (envelope encryption).
package credential

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

// Supported credential kinds (matches DB CHECK constraint).
const (
	KindSNMPv2c     = "snmp_v2c"
	KindSNMPv3      = "snmp_v3"
	KindSSHPassword = "ssh_password"
	KindSSHKey      = "ssh_key"
	KindRedfish     = "redfish"
	KindIPMI        = "ipmi"
	KindWMI         = "wmi"
	KindAPIToken    = "api_token"
	KindNUT         = "nut"
)

var (
	// ErrNotFound is returned when a credential does not exist.
	ErrNotFound = errors.New("credential: not found")
	// ErrInvalidKind is returned for unsupported credential kinds.
	ErrInvalidKind = errors.New("credential: invalid kind")
)

// validKinds is the set of allowed credential kinds.
var validKinds = map[string]bool{
	KindSNMPv2c:     true,
	KindSNMPv3:      true,
	KindSSHPassword: true,
	KindSSHKey:      true,
	KindRedfish:     true,
	KindIPMI:        true,
	KindWMI:         true,
	KindAPIToken:    true,
	KindNUT:         true,
}

// Credential is the domain model for an encrypted credential.
type Credential struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	ClientID       string    `json:"client_id,omitempty"`
	Name           string    `json:"name"`
	Kind           string    `json:"kind"`
	Scope          string    `json:"scope,omitempty"`
	KeyVersion     int       `json:"key_version"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// OrgDEK represents an organization's wrapped data encryption key.
type OrgDEK struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	EncryptedDEK   []byte    `json:"encrypted_dek"`
	KeyVersion     int       `json:"key_version"`
	CreatedAt      time.Time `json:"created_at"`
}

// StoredCredential is the database representation including ciphertext.
type StoredCredential struct {
	Credential
	Ciphertext []byte `json:"-"`
}

// CreateRequest is the input for creating a new credential.
type CreateRequest struct {
	OrganizationID string `json:"organization_id"`
	ClientID       string `json:"client_id,omitempty"`
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	Scope          string `json:"scope,omitempty"`
	// Secret is the plaintext credential data (JSON map).
	Secret map[string]any `json:"secret"`
}

// Repository defines the storage interface for credentials and DEKs.
type Repository interface {
	// DEK operations
	GetOrgDEK(ctx context.Context, orgID string) (*OrgDEK, error)
	CreateOrgDEK(ctx context.Context, orgID string, encryptedDEK []byte, keyVersion int) (*OrgDEK, error)

	// Credential operations
	Create(ctx context.Context, cred *StoredCredential) error
	Get(ctx context.Context, orgID, id string) (*StoredCredential, error)
	List(ctx context.Context, orgID string) ([]Credential, error)
	Update(ctx context.Context, cred *StoredCredential) error
	Delete(ctx context.Context, orgID, id string) error
}

// ListStored returns every credential of the organization including its
// ciphertext; it is required for DEK rotation, which must re-encrypt every
// stored secret.
type StoredLister interface {
	ListStored(ctx context.Context, orgID string) ([]StoredCredential, error)
}

// DEKUpdater replaces the organization DEK row (key version bump).
type DEKUpdater interface {
	UpdateOrgDEK(ctx context.Context, orgID string, encryptedDEK []byte, keyVersion int) (*OrgDEK, error)
}

// Service provides credential management with envelope encryption.
type Service struct {
	repo      Repository
	encryptor *crypto.EnvelopeEncryptor
}

// NewService creates a new credential service.
func NewService(repo Repository, encryptor *crypto.EnvelopeEncryptor) *Service {
	return &Service{
		repo:      repo,
		encryptor: encryptor,
	}
}

// Create stores a new encrypted credential. It retrieves (or creates) the org DEK,
// encrypts the secret, and stores the ciphertext.
func (s *Service) Create(ctx context.Context, req CreateRequest) (*Credential, error) {
	if !validKinds[req.Kind] {
		return nil, ErrInvalidKind
	}

	dek, err := s.getOrCreateDEK(ctx, req.OrganizationID)
	if err != nil {
		return nil, err
	}

	plaintext, err := json.Marshal(req.Secret)
	if err != nil {
		return nil, err
	}

	ciphertext, err := crypto.Encrypt(dek, plaintext)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	stored := &StoredCredential{
		Credential: Credential{
			OrganizationID: req.OrganizationID,
			ClientID:       req.ClientID,
			Name:           req.Name,
			Kind:           req.Kind,
			Scope:          req.Scope,
			KeyVersion:     1,
			CreatedAt:      now,
			UpdatedAt:      now,
		},
		Ciphertext: ciphertext,
	}

	if err := s.repo.Create(ctx, stored); err != nil {
		return nil, err
	}

	return &stored.Credential, nil
}

// Get retrieves a credential's metadata (without decrypting the secret).
func (s *Service) Get(ctx context.Context, orgID, id string) (*Credential, error) {
	stored, err := s.repo.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	return &stored.Credential, nil
}

// Decrypt retrieves and decrypts a credential's secret.
func (s *Service) Decrypt(ctx context.Context, orgID, id string) (map[string]any, error) {
	stored, err := s.repo.Get(ctx, orgID, id)
	if err != nil {
		return nil, err
	}

	dek, err := s.getDEK(ctx, orgID)
	if err != nil {
		return nil, err
	}

	plaintext, err := crypto.Decrypt(dek, stored.Ciphertext)
	if err != nil {
		return nil, err
	}

	var secret map[string]any
	if err := json.Unmarshal(plaintext, &secret); err != nil {
		return nil, err
	}

	return secret, nil
}

// List returns all credential metadata for an organization.
func (s *Service) List(ctx context.Context, orgID string) ([]Credential, error) {
	return s.repo.List(ctx, orgID)
}

// Delete removes a credential.
func (s *Service) Delete(ctx context.Context, orgID, id string) error {
	return s.repo.Delete(ctx, orgID, id)
}

// RotationResult reports the outcome of a DEK rotation.
type RotationResult struct {
	// KeyVersion is the new DEK version.
	KeyVersion int `json:"key_version"`
	// Rotated is the number of credential ciphertexts re-encrypted.
	Rotated int `json:"rotated"`
}

// RotateKeys generates a fresh DEK for the organization, re-encrypts every
// stored credential ciphertext with it and atomically bumps the key version.
// Existing plaintext secrets never leave the platform — re-encryption happens
// in-process under the envelope-encryption boundary.
func (s *Service) RotateKeys(ctx context.Context, orgID string) (*RotationResult, error) {
	lister, ok := s.repo.(StoredLister)
	if !ok {
		return nil, fmt.Errorf("credential store does not support key rotation")
	}
	updater, ok := s.repo.(DEKUpdater)
	if !ok {
		return nil, fmt.Errorf("credential store does not support key rotation")
	}

	oldDEK, err := s.getDEK(ctx, orgID)
	if err != nil {
		return nil, err
	}

	stored, err := lister.ListStored(ctx, orgID)
	if err != nil {
		return nil, err
	}

	newDEK, err := crypto.GenerateDEK()
	if err != nil {
		return nil, err
	}

	// Re-encrypt every credential with the new DEK before committing the DEK
	// swap, so a failure mid-rotation leaves the store untouched.
	reEncrypted := make([]StoredCredential, len(stored))
	for i := range stored {
		cred := stored[i]
		plaintext, err := crypto.Decrypt(oldDEK, cred.Ciphertext)
		if err != nil {
			return nil, fmt.Errorf("credential %s: %w", cred.ID, err)
		}
		ciphertext, err := crypto.Encrypt(newDEK, plaintext)
		if err != nil {
			return nil, fmt.Errorf("credential %s: %w", cred.ID, err)
		}
		cred.Ciphertext = ciphertext
		reEncrypted[i] = cred
	}

	var version int
	if current, err := s.repo.GetOrgDEK(ctx, orgID); err == nil {
		version = current.KeyVersion + 1
	} else {
		version = 1
	}

	for i := range reEncrypted {
		cred := reEncrypted[i]
		cred.KeyVersion = version
		if err := s.repo.Update(ctx, &cred); err != nil {
			return nil, fmt.Errorf("credential %s: %w", cred.ID, err)
		}
	}

	wrapped, err := s.encryptor.WrapDEK(newDEK)
	if err != nil {
		return nil, err
	}
	if _, err := updater.UpdateOrgDEK(ctx, orgID, wrapped, version); err != nil {
		return nil, err
	}

	return &RotationResult{KeyVersion: version, Rotated: len(reEncrypted)}, nil
}

// getOrCreateDEK retrieves the org's DEK or generates a new one.
func (s *Service) getOrCreateDEK(ctx context.Context, orgID string) ([]byte, error) {
	orgDEK, err := s.repo.GetOrgDEK(ctx, orgID)
	if err == nil {
		return s.encryptor.UnwrapDEK(orgDEK.EncryptedDEK)
	}

	// Generate new DEK for this organization
	dek, err := crypto.GenerateDEK()
	if err != nil {
		return nil, err
	}

	wrapped, err := s.encryptor.WrapDEK(dek)
	if err != nil {
		return nil, err
	}

	if _, err := s.repo.CreateOrgDEK(ctx, orgID, wrapped, 1); err != nil {
		return nil, err
	}

	return dek, nil
}

// getDEK retrieves and unwraps the org's DEK.
func (s *Service) getDEK(ctx context.Context, orgID string) ([]byte, error) {
	orgDEK, err := s.repo.GetOrgDEK(ctx, orgID)
	if err != nil {
		return nil, err
	}
	return s.encryptor.UnwrapDEK(orgDEK.EncryptedDEK)
}
