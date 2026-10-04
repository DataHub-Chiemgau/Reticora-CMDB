package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	apiKeyPrefix         = "rk_live_"
	apiKeyPublicIDLength = 12
	apiKeySecretLength   = 40
	base62Alphabet       = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

// APIKeyStore provides persistence for API key metadata.
type APIKeyStore interface {
	// LookupByPrefix returns stored key metadata for a given public ID prefix.
	LookupByPrefix(ctx context.Context, prefix string) (*StoredAPIKey, error)
	// Save persists a new API key record.
	Save(ctx context.Context, key StoredAPIKey) error
	// MarkUsed updates the last_used_at timestamp of a key of orgID.
	MarkUsed(ctx context.Context, orgID, id string) error
}

// StoredAPIKey represents a persisted API key record.
type StoredAPIKey struct {
	ID             string
	OrganizationID string
	Name           string
	KeyHash        string
	KeyPrefix      string
	Permissions    []Permission
	CreatedBy      string
	ExpiresAt      *time.Time
	RevokedAt      *time.Time
	LastUsedAt     *time.Time
	CreatedAt      time.Time
}

// APIKeyService manages API key creation and validation.
type APIKeyService struct {
	store APIKeyStore
}

// NewAPIKeyService constructs a new API key service.
func NewAPIKeyService() *APIKeyService {
	return &APIKeyService{}
}

// NewAPIKeyServiceWithStore constructs an API key service backed by a persistent store.
func NewAPIKeyServiceWithStore(store APIKeyStore) *APIKeyService {
	return &APIKeyService{store: store}
}

// Generate creates a new API key and returns the plaintext token with its metadata.
func (s *APIKeyService) Generate(orgID string, name string, scopes []Permission, expiresAt *time.Time) (plaintext string, info APIKeyInfo, err error) {
	if s == nil {
		return "", APIKeyInfo{}, errors.New("identity: API key service is nil")
	}
	if strings.TrimSpace(orgID) == "" {
		return "", APIKeyInfo{}, errors.New("identity: organization ID is required")
	}
	if strings.TrimSpace(name) == "" {
		return "", APIKeyInfo{}, errors.New("identity: API key name is required")
	}

	publicID, err := randomBase62(apiKeyPublicIDLength)
	if err != nil {
		return "", APIKeyInfo{}, fmt.Errorf("identity: generate API key prefix: %w", err)
	}
	secret, err := randomBase62(apiKeySecretLength)
	if err != nil {
		return "", APIKeyInfo{}, fmt.Errorf("identity: generate API key secret: %w", err)
	}

	plaintext = apiKeyPrefix + publicID + "_" + secret
	info = APIKeyInfo{
		ID:             publicID,
		OrganizationID: orgID,
		Scopes:         append([]Permission(nil), scopes...),
		ExpiresAt:      expiresAt,
	}
	return plaintext, info, nil
}

// HashKey returns the SHA-256 hash of an API key for storage.
func HashKey(plaintext string) string {
	h := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(h[:])
}

// Validate validates the API key format and looks up the key in the persistent store.
func (s *APIKeyService) Validate(ctx context.Context, rawKey string) (*APIKeyInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("identity: API key service is nil")
	}

	publicID, secret, err := parseAPIKey(rawKey)
	if err != nil {
		return nil, err
	}

	canonical := apiKeyPrefix + publicID + "_" + secret
	if subtle.ConstantTimeCompare([]byte(rawKey), []byte(canonical)) != 1 {
		return nil, errors.New("identity: invalid API key")
	}

	if s.store == nil {
		return nil, errors.New("identity: API key store not configured")
	}

	stored, err := s.store.LookupByPrefix(ctx, publicID)
	if err != nil {
		return nil, fmt.Errorf("identity: lookup API key: %w", err)
	}
	if stored == nil {
		return nil, errors.New("identity: API key not found")
	}

	// Verify the hash matches
	keyHash := HashKey(rawKey)
	if subtle.ConstantTimeCompare([]byte(keyHash), []byte(stored.KeyHash)) != 1 {
		return nil, errors.New("identity: invalid API key")
	}

	// Check revocation
	if stored.RevokedAt != nil {
		return nil, errors.New("identity: API key has been revoked")
	}

	// Check expiration
	if stored.ExpiresAt != nil && stored.ExpiresAt.Before(time.Now()) {
		return nil, errors.New("identity: API key has expired")
	}

	// Update last used timestamp (best effort)
	_ = s.store.MarkUsed(ctx, stored.OrganizationID, stored.ID)

	return &APIKeyInfo{
		ID:             stored.KeyPrefix,
		OrganizationID: stored.OrganizationID,
		Scopes:         stored.Permissions,
		ExpiresAt:      stored.ExpiresAt,
	}, nil
}

func parseAPIKey(rawKey string) (publicID string, secret string, err error) {
	if !strings.HasPrefix(rawKey, apiKeyPrefix) {
		return "", "", errors.New("identity: invalid API key prefix")
	}

	remainder := strings.TrimPrefix(rawKey, apiKeyPrefix)
	parts := strings.Split(remainder, "_")
	if len(parts) != 2 {
		return "", "", errors.New("identity: invalid API key format")
	}
	if len(parts[0]) != apiKeyPublicIDLength || len(parts[1]) != apiKeySecretLength {
		return "", "", errors.New("identity: invalid API key length")
	}
	if !isBase62(parts[0]) || !isBase62(parts[1]) {
		return "", "", errors.New("identity: API key contains invalid characters")
	}
	return parts[0], parts[1], nil
}

func randomBase62(length int) (string, error) {
	buf := make([]byte, length)
	random := make([]byte, length)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	for i := range buf {
		buf[i] = base62Alphabet[int(random[i])%len(base62Alphabet)]
	}
	return string(buf), nil
}

func isBase62(value string) bool {
	for _, r := range value {
		if !strings.ContainsRune(base62Alphabet, r) {
			return false
		}
	}
	return true
}
