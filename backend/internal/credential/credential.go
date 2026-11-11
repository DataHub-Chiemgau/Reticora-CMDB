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
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
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
	KindNAS         = "nas" // NAS plugin account (Synology DSM: username, password; E-68)
	// The logins of vault connections (CRD-03, WP-893): AppRole (role_id,
	// secret_id) and token. The collector files them under vault, no plugin
	// reads them.
	KindVaultAppRole = "vault_approle"
	KindVaultToken   = "vault_token"
)

var (
	// ErrNotFound is returned when a credential does not exist.
	ErrNotFound = errors.New("credential: not found")
	// ErrInvalidKind is returned for unsupported credential kinds.
	ErrInvalidKind = errors.New("credential: invalid kind")
)

// validKinds is the set of allowed credential kinds.
var validKinds = map[string]bool{
	KindSNMPv2c:      true,
	KindSNMPv3:       true,
	KindSSHPassword:  true,
	KindSSHKey:       true,
	KindRedfish:      true,
	KindIPMI:         true,
	KindWMI:          true,
	KindAPIToken:     true,
	KindNUT:          true,
	KindNAS:          true,
	KindVaultAppRole: true,
	KindVaultToken:   true,
}

// vaultLoginFields are the required members of the secret of a vault login
// (AK2 of WP-893).
var vaultLoginFields = map[string][]string{
	KindVaultAppRole: {"role_id", "secret_id"},
	KindVaultToken:   {"token"},
}

// checkVaultLoginSecret requires the members of a vault login as non-empty
// strings; other kinds are not checked.
func checkVaultLoginSecret(kind string, secret map[string]any) error {
	for _, field := range vaultLoginFields[kind] {
		if v, ok := secret[field].(string); !ok || v == "" {
			return &api.FieldError{Field: "secret", Status: http.StatusUnprocessableEntity,
				Message: "a credential of kind " + kind + " needs " + strings.Join(vaultLoginFields[kind], " and ")}
		}
	}
	return nil
}

// Credential is the domain model for an encrypted credential.
type Credential struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	ClientID       string `json:"client_id,omitempty"`
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	Scope          string `json:"scope,omitempty"`
	KeyVersion     int    `json:"key_version"`
	// SecretVersion counts the secrets the credential had: 1 at creation,
	// raised by every PUT /credentials/{id}/secret (CRD-05, WP-889).
	SecretVersion int `json:"secret_version"`
	// RotatedAt is the time of the last replacement of the secret; nil
	// while the secret of the creation applies.
	RotatedAt *time.Time `json:"rotated_at"`
	// MaxAgeDays is the age limit of the secret in days (1–3650); nil is
	// none. WP-897 checks it.
	MaxAgeDays *int `json:"max_age_days"`
	// Source is stored (an encrypted secret) or vault_ref (a reference
	// into a vault connection, no secret; CRD-02, WP-894).
	Source string `json:"source"`
	// VaultRef is the reference of a vault_ref credential.
	VaultRef  *VaultRef `json:"vault_ref,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
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
	// VaultRef replaces Secret for a reference into a vault connection
	// (WP-894); exactly one of them is sent.
	VaultRef *VaultRef `json:"vault_ref,omitempty"`
	// MaxAgeDays is the optional age limit of the secret (1–3650).
	MaxAgeDays *int `json:"max_age_days,omitempty"`
}

// MetadataUpdate is a change of name and age limit (PATCH, WP-889).
// SetMaxAge marks that max_age_days was sent (nil removes the limit).
type MetadataUpdate struct {
	Name       *string
	MaxAgeDays *int
	SetMaxAge  bool
	// Ref changes the reference of a vault_ref credential (WP-894); any
	// change raises secret_version.
	Ref VaultRefUpdate
}

// SecretReplacer replaces the secret of a credential in one transaction
// (WP-889): it locks the organization's DEK (a concurrent key rotation
// waits), encrypts plaintext with it, raises secret_version, sets
// rotated_at and runs inTx (the event of the replacement) before the
// commit. PGRepository and MemoryRepository implement it.
type SecretReplacer interface {
	ReplaceSecret(ctx context.Context, orgID, id string, plaintext []byte, enc *crypto.EnvelopeEncryptor,
		inTx func(ctx context.Context, cred *Credential) error) (*Credential, error)
}

// MetadataUpdater changes name and age limit of a credential without
// touching its secret (WP-889).
type MetadataUpdater interface {
	UpdateMetadata(ctx context.Context, orgID, id string, u MetadataUpdate) (*Credential, error)
}

// ErrUnsupported is returned when the store cannot replace secrets or
// change metadata.
var ErrUnsupported = errors.New("credential store does not support this change")

// MaxAgeDaysValid reports whether an age limit lies in 1–3650.
func MaxAgeDaysValid(days int) bool { return days >= 1 && days <= 3650 }

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
	// cycles counts the bundle requests that failed on the server's side
	// (worker credential_bundles, OPS-05); nil counts none.
	cycles *observability.Cycles
	// bindings is the binding service (WP-890); a copy of the service
	// (WithCycles) shares it.
	bindings *BindingService
	// vaults is the vault connection service (WP-893), shared like bindings.
	vaults *VaultService
	// vault resolves references of connections with resolve_at server
	// (WP-896); nil: Decrypt answers ErrVaultNotConfigured for them.
	vault *VaultResolver
}

// NewService creates a new credential service.
func NewService(repo Repository, encryptor *crypto.EnvelopeEncryptor) *Service {
	s := &Service{
		repo:      repo,
		encryptor: encryptor,
	}
	bindingRepo, ok := repo.(BindingRepository)
	if !ok {
		bindingRepo = newMemoryBindings()
	}
	s.bindings = &BindingService{creds: s, bindings: bindingRepo, groups: NoDeviceGroups{}}
	vaultRepo, ok := repo.(VaultRepository)
	if !ok {
		vaultRepo = newMemoryVaults()
	}
	s.vaults = &VaultService{creds: s, vaults: vaultRepo}
	return s
}

// Create stores a new encrypted credential. It retrieves (or creates) the org DEK,
// encrypts the secret, and stores the ciphertext.
func (s *Service) Create(ctx context.Context, req CreateRequest) (*Credential, error) { //nolint:gocritic // API of credential.Service used by iga, discovery and jobs (WP-301 changes jobs in parallel)
	if !validKinds[req.Kind] {
		return nil, ErrInvalidKind
	}
	if err := checkSource(&req); err != nil {
		return nil, err
	}
	if req.VaultRef != nil {
		return s.createVaultRef(ctx, &req)
	}
	if err := checkVaultLoginSecret(req.Kind, req.Secret); err != nil {
		return nil, err
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
			SecretVersion:  1,
			MaxAgeDays:     req.MaxAgeDays,
			Source:         SourceStored,
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

// createVaultRef stores a reference into a vault connection: no secret,
// no DEK (WP-894).
func (s *Service) createVaultRef(ctx context.Context, req *CreateRequest) (*Credential, error) {
	if err := s.checkVaultRef(ctx, req.OrganizationID, req.Kind, req.ClientID, req.VaultRef); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	stored := &StoredCredential{Credential: Credential{
		OrganizationID: req.OrganizationID,
		ClientID:       req.ClientID,
		Name:           req.Name,
		Kind:           req.Kind,
		Scope:          req.Scope,
		KeyVersion:     1,
		SecretVersion:  1,
		MaxAgeDays:     req.MaxAgeDays,
		Source:         SourceVaultRef,
		VaultRef:       req.VaultRef,
		CreatedAt:      now,
		UpdatedAt:      now,
	}}
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
	if stored.Source == SourceVaultRef {
		// A reference is read from the vault in memory, only for a
		// connection with resolve_at server (WP-894, WP-896).
		return s.decryptVaultRef(ctx, orgID, stored)
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

// ReplaceSecret replaces the secret of a credential (PUT
// /credentials/{id}/secret, CRD-05, WP-889); the new value is never
// returned. inTx runs in the transaction of the replacement (the event
// credential.rotated); it may be nil.
func (s *Service) ReplaceSecret(ctx context.Context, orgID, id string, secret map[string]any,
	inTx func(ctx context.Context, cred *Credential) error) (*Credential, error) {
	replacer, ok := s.repo.(SecretReplacer)
	if !ok {
		return nil, ErrUnsupported
	}
	// A vault login keeps the members of its kind (WP-893); an unknown id
	// is reported by the replacement.
	if current, err := s.repo.Get(ctx, orgID, id); err == nil {
		if current.Source == SourceVaultRef {
			return nil, ErrVaultRefHasNoSecret
		}
		if loginErr := checkVaultLoginSecret(current.Kind, secret); loginErr != nil {
			return nil, loginErr
		}
	}
	if _, err := s.getOrCreateDEK(ctx, orgID); err != nil {
		return nil, err
	}
	plaintext, err := json.Marshal(secret)
	if err != nil {
		return nil, err
	}
	return replacer.ReplaceSecret(ctx, orgID, id, plaintext, s.encryptor, inTx)
}

// UpdateMetadata changes name and age limit (PATCH /credentials/{id}).
func (s *Service) UpdateMetadata(ctx context.Context, orgID, id string, u MetadataUpdate) (*Credential, error) {
	updater, ok := s.repo.(MetadataUpdater)
	if !ok {
		return nil, ErrUnsupported
	}
	if u.Ref.Changes() {
		current, err := s.repo.Get(ctx, orgID, id)
		if err != nil {
			return nil, err
		}
		if refErr := checkVaultRefUpdate(current, &u.Ref); refErr != nil {
			return nil, refErr
		}
	}
	return updater.UpdateMetadata(ctx, orgID, id, u)
}

// Delete removes a credential.
func (s *Service) Delete(ctx context.Context, orgID, id string) error {
	// The login of a vault connection is not deleted (WP-893): 409, in
	// the memory store as well, where no foreign key refuses.
	login, err := s.vaults.loginOfConnection(ctx, orgID, id)
	if err != nil {
		return err
	}
	if login {
		return ErrCredentialIsVaultLogin
	}
	return credentialDeleteError(s.repo.Delete(ctx, orgID, id))
}

// RotationResult reports the outcome of a DEK rotation.
type RotationResult struct {
	// KeyVersion is the new DEK version.
	KeyVersion int `json:"key_version"`
	// Rotated is the number of credential ciphertexts re-encrypted.
	Rotated int `json:"rotated"`
	// Subjects is the number of PII vault entries re-encrypted (SEC-05).
	Subjects int `json:"pii_subjects"`
}

// RotateKeys generates a fresh DEK for the organization, re-encrypts every
// stored credential ciphertext with it and atomically bumps the key version.
// Existing plaintext secrets never leave the platform — re-encryption happens
// in-process under the envelope-encryption boundary.
func (s *Service) RotateKeys(ctx context.Context, orgID string) (*RotationResult, error) {
	// The database store rotates in one transaction (SEC-06).
	if rotator, ok := s.repo.(KeyRotator); ok {
		if _, err := s.getOrCreateDEK(ctx, orgID); err != nil {
			return nil, err
		}
		return rotator.RotateOrgDEK(ctx, orgID, s.encryptor)
	}
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
	// A vault reference has no ciphertext (WP-894); Rotated counts the
	// stored secrets only.
	stored = slices.DeleteFunc(stored, func(c StoredCredential) bool { return c.Source == SourceVaultRef })
	reEncrypted := make([]StoredCredential, len(stored))
	for i := range stored {
		cred := stored[i]
		plaintext, cryptErr := crypto.Decrypt(oldDEK, cred.Ciphertext)
		if cryptErr != nil {
			return nil, fmt.Errorf("credential %s: %w", cred.ID, cryptErr)
		}
		ciphertext, cryptErr := crypto.Encrypt(newDEK, plaintext)
		if cryptErr != nil {
			return nil, fmt.Errorf("credential %s: %w", cred.ID, cryptErr)
		}
		cred.Ciphertext = ciphertext
		reEncrypted[i] = cred
	}

	// The DEK was read above, so a failure here is an error of the store; it
	// must not restart the key version at 1 (F-123).
	current, err := s.repo.GetOrgDEK(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("read key version: %w", err)
	}
	version := current.KeyVersion + 1

	for i := range reEncrypted {
		cred := reEncrypted[i]
		cred.KeyVersion = version
		if updateErr := s.repo.Update(ctx, &cred); updateErr != nil {
			return nil, fmt.Errorf("credential %s: %w", cred.ID, updateErr)
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
