package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// API key format of AUT-04: rk_live_ or rk_test_, a 12 character Base62
// public prefix and a 40 character Base62 secret, without separator. Keys
// issued before WP-067 carry an underscore between prefix and secret; they
// stay valid until they are rotated or revoked.
const (
	APIKeyEnvironmentLive = "live"
	APIKeyEnvironmentTest = "test"
	apiKeyPublicIDLength  = 12
	apiKeySecretLength    = 40
	base62Alphabet        = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

func apiKeyPrefixFor(environment string) string { return "rk_" + environment + "_" }

// APIKeyStore provides persistence for API key metadata.
type APIKeyStore interface {
	// LookupByPrefix identifies a key by its public prefix before any
	// tenant is known (system read). It returns nil for unknown and revoked
	// keys.
	LookupByPrefix(ctx context.Context, prefix string) (*StoredAPIKey, error)
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
	Environment    string
	Permissions    []Permission
	CreatedBy      string
	// ServiceAccountID binds the key to a service account (RBA-08): the key
	// then acts with the account's rights instead of its creator's.
	ServiceAccountID *string
	RotatedFrom      *string
	ExpiresAt        *time.Time
	RevokedAt        *time.Time
	LastUsedAt       *time.Time
	CreatedAt        time.Time
}

// OwnerAccessResolver loads the current role grants of a key owner (RBA-08):
// a key never grants more than its owner holds now.
type OwnerAccessResolver interface {
	AccessGrants(ctx context.Context, orgID, userID string) ([]Grant, error)
}

// ServiceAccountAccessResolver loads the role grants of a service account;
// an inactive or unknown account has none.
type ServiceAccountAccessResolver interface {
	ServiceAccountGrants(ctx context.Context, orgID, id string) ([]Grant, error)
}

// APIKeyService manages API key creation and validation.
type APIKeyService struct {
	store APIKeyStore
	// owners resolves the owner's current rights; nil keeps the key's own
	// permissions (tests and --no-db).
	owners OwnerAccessResolver
	// serviceAccounts resolves the rights of service accounts; a key bound
	// to an account without it grants nothing.
	serviceAccounts ServiceAccountAccessResolver
}

// WithServiceAccountAccess resolves keys bound to a service account with the
// account's rights (RBA-08).
func (s *APIKeyService) WithServiceAccountAccess(r ServiceAccountAccessResolver) *APIKeyService {
	s.serviceAccounts = r
	return s
}

// NewAPIKeyService constructs a new API key service.
func NewAPIKeyService() *APIKeyService {
	return &APIKeyService{}
}

// NewAPIKeyServiceWithStore constructs an API key service backed by a persistent store.
func NewAPIKeyServiceWithStore(store APIKeyStore) *APIKeyService {
	return &APIKeyService{store: store}
}

// WithOwnerAccess makes Validate grant the intersection of the key's
// permissions and the owner's current rights, within the owner's scope
// (AUT-04).
func (s *APIKeyService) WithOwnerAccess(owners OwnerAccessResolver) *APIKeyService {
	s.owners = owners
	return s
}

// GeneratedAPIKey is a new key: the plaintext, shown once, and the values
// stored for it.
type GeneratedAPIKey struct {
	Plaintext   string
	KeyPrefix   string
	KeyHash     string
	Environment string
}

// GenerateAPIKey creates a key of the environment (live or test).
func GenerateAPIKey(environment string) (GeneratedAPIKey, error) {
	if environment == "" {
		environment = APIKeyEnvironmentLive
	}
	if environment != APIKeyEnvironmentLive && environment != APIKeyEnvironmentTest {
		return GeneratedAPIKey{}, fmt.Errorf("identity: API key environment must be %q or %q", APIKeyEnvironmentLive, APIKeyEnvironmentTest)
	}
	publicID, err := randomBase62(apiKeyPublicIDLength)
	if err != nil {
		return GeneratedAPIKey{}, fmt.Errorf("identity: generate API key prefix: %w", err)
	}
	secret, err := randomBase62(apiKeySecretLength)
	if err != nil {
		return GeneratedAPIKey{}, fmt.Errorf("identity: generate API key secret: %w", err)
	}
	plaintext := apiKeyPrefixFor(environment) + publicID + secret
	return GeneratedAPIKey{Plaintext: plaintext, KeyPrefix: publicID, KeyHash: HashKey(plaintext), Environment: environment}, nil
}

// Generate creates a new live API key and returns the plaintext token with its metadata.
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
	key, err := GenerateAPIKey(APIKeyEnvironmentLive)
	if err != nil {
		return "", APIKeyInfo{}, err
	}
	return key.Plaintext, APIKeyInfo{
		ID:             key.KeyPrefix,
		OrganizationID: orgID,
		Scopes:         append([]Permission(nil), scopes...),
		ExpiresAt:      expiresAt,
	}, nil
}

// HashKey returns the SHA-256 hash of an API key for storage.
func HashKey(plaintext string) string {
	h := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(h[:])
}

// Validate identifies a presented key, checks hash, revocation, expiry and
// environment, and returns the rights it grants: the intersection of the
// key's permissions and the owner's current rights, in the owner's scope.
func (s *APIKeyService) Validate(ctx context.Context, rawKey string) (*APIKeyInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("identity: API key service is nil")
	}

	publicID, environment, err := parseAPIKey(rawKey)
	if err != nil {
		return nil, err
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

	// Constant-time comparison of the stored hash.
	keyHash := HashKey(rawKey)
	if subtle.ConstantTimeCompare([]byte(keyHash), []byte(stored.KeyHash)) != 1 {
		return nil, errors.New("identity: invalid API key")
	}
	if stored.Environment != "" && stored.Environment != environment {
		return nil, errors.New("identity: invalid API key")
	}
	if stored.RevokedAt != nil {
		return nil, errors.New("identity: API key has been revoked")
	}
	if stored.ExpiresAt != nil && stored.ExpiresAt.Before(time.Now()) {
		return nil, errors.New("identity: API key has expired")
	}

	info := &APIKeyInfo{
		ID:             stored.KeyPrefix,
		KeyID:          stored.ID,
		OwnerID:        stored.CreatedBy,
		Environment:    environment,
		OrganizationID: stored.OrganizationID,
		Scopes:         stored.Permissions,
		ExpiresAt:      stored.ExpiresAt,
	}
	resolve := func() ([]Grant, error) { return s.owners.AccessGrants(ctx, stored.OrganizationID, stored.CreatedBy) }
	if stored.ServiceAccountID != nil {
		// The key acts as its service account (RBA-08).
		info.OwnerID = *stored.ServiceAccountID
		resolve = func() ([]Grant, error) {
			if s.serviceAccounts == nil {
				return nil, nil
			}
			return s.serviceAccounts.ServiceAccountGrants(ctx, stored.OrganizationID, *stored.ServiceAccountID)
		}
	}
	if s.owners != nil || stored.ServiceAccountID != nil {
		grants, grantErr := resolve()
		if grantErr != nil {
			return nil, fmt.Errorf("identity: resolve API key owner rights: %w", grantErr)
		}
		owner := ResolveAccess(grants)
		info.Scopes = intersectPermissions(stored.Permissions, owner.Permissions)
		scope := owner.Scope
		info.Scope = &scope
		info.ClientScope = scope.LegacyClientScope()
		info.PermissionScopes = make(map[Permission]Scope)
		for _, p := range info.Scopes {
			if narrower, ok := owner.PermissionScopes[p]; ok {
				info.PermissionScopes[p] = narrower
			}
		}
	}

	// Update last used timestamp (best effort)
	_ = s.store.MarkUsed(ctx, stored.OrganizationID, stored.ID)

	return info, nil
}

// intersectPermissions returns the key permissions the owner also holds, in
// the key's order.
func intersectPermissions(key, owner []Permission) []Permission {
	held := make(map[Permission]bool, len(owner))
	for _, p := range owner {
		held[p] = true
	}
	out := make([]Permission, 0, len(key))
	for _, p := range key {
		if held[p] {
			out = append(out, p)
		}
	}
	return out
}

// parseAPIKey returns the public prefix and the environment of a key in the
// AUT-04 format or the legacy format with separator.
func parseAPIKey(rawKey string) (publicID, environment string, err error) {
	var remainder string
	switch {
	case strings.HasPrefix(rawKey, apiKeyPrefixFor(APIKeyEnvironmentLive)):
		environment, remainder = APIKeyEnvironmentLive, strings.TrimPrefix(rawKey, apiKeyPrefixFor(APIKeyEnvironmentLive))
	case strings.HasPrefix(rawKey, apiKeyPrefixFor(APIKeyEnvironmentTest)):
		environment, remainder = APIKeyEnvironmentTest, strings.TrimPrefix(rawKey, apiKeyPrefixFor(APIKeyEnvironmentTest))
	default:
		return "", "", errors.New("identity: invalid API key prefix")
	}
	publicID, secret := "", ""
	switch len(remainder) {
	case apiKeyPublicIDLength + apiKeySecretLength:
		publicID, secret = remainder[:apiKeyPublicIDLength], remainder[apiKeyPublicIDLength:]
	case apiKeyPublicIDLength + 1 + apiKeySecretLength:
		// Legacy live keys: prefix, underscore, secret.
		if environment != APIKeyEnvironmentLive || remainder[apiKeyPublicIDLength] != '_' {
			return "", "", errors.New("identity: invalid API key format")
		}
		publicID, secret = remainder[:apiKeyPublicIDLength], remainder[apiKeyPublicIDLength+1:]
	default:
		return "", "", errors.New("identity: invalid API key length")
	}
	if !isBase62(publicID) || !isBase62(secret) {
		return "", "", errors.New("identity: API key contains invalid characters")
	}
	return publicID, environment, nil
}

// randomBase62 draws length characters uniformly from the Base62 alphabet.
func randomBase62(length int) (string, error) {
	buf := make([]byte, length)
	limit := big.NewInt(int64(len(base62Alphabet)))
	for i := range buf {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", err
		}
		buf[i] = base62Alphabet[n.Int64()]
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
