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
