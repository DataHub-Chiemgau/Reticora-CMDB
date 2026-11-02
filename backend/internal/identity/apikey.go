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
	"slices"
	"sort"
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

// ErrAPIKeyUnavailable: a well-formed key could not be checked because its
// store or its owner's rights could not be read (database outage). The key
// is not known to be invalid, so the request is refused with 503, not 401
// (OPS-06).
var ErrAPIKeyUnavailable = errors.New("identity: API key verification unavailable")

// APIKeyStore provides persistence for API key metadata.
type APIKeyStore interface {
	// LookupByPrefix identifies a key by its public prefix before any
	// tenant is known (system read). It returns nil for unknown and revoked
	// keys.
	LookupByPrefix(ctx context.Context, prefix string) (*StoredAPIKey, error)
	// MarkUsed updates the last_used_at timestamp of a key of orgID, at
	// most once a minute.
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
	// CreatorScope is the scope in which the creator held each key
	// permission when binding the key to its service account (F-397,
	// migration 000185): the key never acts beyond it. nil for keys not
	// bound and for keys bound before the migration (see creatorCeiling).
	CreatorScope map[Permission]Scope
	RotatedFrom  *string
	ExpiresAt    *time.Time
	RevokedAt    *time.Time
	LastUsedAt   *time.Time
	CreatedAt    time.Time
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
// key's permissions and the owner's current rights, in the owner's scope. A
// key bound to a service account acts with the account's current rights
// within the scope its creator held at binding (creatorCeiling, F-397).
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
		return nil, fmt.Errorf("%w: lookup API key: %w", ErrAPIKeyUnavailable, err)
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
			return nil, fmt.Errorf("%w: resolve API key owner rights: %w", ErrAPIKeyUnavailable, grantErr)
		}
		owner := ResolveAccess(grants)
		if stored.ServiceAccountID != nil {
			// A bound account does not widen its creator's scope (F-397,
			// like E-98 for webhook subscriptions).
			ceiling, ceilingErr := s.creatorCeiling(ctx, stored)
			if ceilingErr != nil {
				return nil, fmt.Errorf("%w: resolve API key creator scope: %w", ErrAPIKeyUnavailable, ceilingErr)
			}
			owner = withinCeiling(&owner, ceiling)
		}
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

// creatorScopeOf is the scope in which p holds each of perms (the scope
// GrantCeiling checks), recorded on a key bound to a service account
// (F-397). Permissions p does not hold are left out.
func creatorScopeOf(p *Principal, perms []Permission) map[Permission]Scope {
	out := make(map[Permission]Scope, len(perms))
	for _, perm := range perms {
		if s, ok := principalScopeFor(p, perm); ok {
			out[perm] = intersectScope(&s, &s)
		}
	}
	return out
}

// creatorCeiling is the scope a bound key may act in per permission: the
// scope its creator held at binding (StoredAPIKey.CreatorScope). A key bound
// before migration 000185 has none recorded; it is limited to its creator's
// current rights (fail-closed, docs/betrieb/upgrade-hinweise.md), and to
// nothing without an owner resolver.
func (s *APIKeyService) creatorCeiling(ctx context.Context, stored *StoredAPIKey) (map[Permission]Scope, error) {
	if stored.CreatorScope != nil {
		return stored.CreatorScope, nil
	}
	if s.owners == nil {
		return map[Permission]Scope{}, nil
	}
	grants, err := s.owners.AccessGrants(ctx, stored.OrganizationID, stored.CreatedBy)
	if err != nil {
		return nil, err
	}
	creator := ResolveAccess(grants)
	return creatorScopeOf(&Principal{Permissions: creator.Permissions, Scope: &creator.Scope, PermissionScopes: creator.PermissionScopes}, stored.Permissions), nil
}

// withinCeiling limits access to the permissions of ceiling, each to the
// intersection of its scope with the ceiling's; the union scope (the tenant
// scope of the requests) is limited to the union of the ceiling scopes. In
// every dimension the result lies within both, so the RLS shows the key no
// row the account or the creator does not reach.
func withinCeiling(access *Access, ceiling map[Permission]Scope) Access {
	out := Access{Permissions: make([]Permission, 0, len(access.Permissions))}
	held := make(map[Permission]Scope, len(access.Permissions))
	bounds := make([]Scope, 0, len(access.Permissions))
	for _, p := range access.Permissions {
		bound, ok := ceiling[p]
		if !ok {
			continue
		}
		scope := access.Scope
		if narrower, ok := access.PermissionScopes[p]; ok {
			scope = narrower
		}
		out.Permissions = append(out.Permissions, p)
		held[p] = intersectScope(&scope, &bound)
		bounds = append(bounds, bound)
	}
	union := unionScopes(bounds)
	out.Scope = intersectScope(&access.Scope, &union)
	for _, p := range out.Permissions {
		if s := held[p]; !s.equal(&out.Scope) {
			if out.PermissionScopes == nil {
				out.PermissionScopes = make(map[Permission]Scope)
			}
			out.PermissionScopes[p] = s
		}
	}
	return out
}

// intersectScope is the scope covered by both a and b, per dimension.
func intersectScope(a, b *Scope) Scope {
	return Scope{Clients: intersectSet(a.Clients, b.Clients), Sites: intersectSet(a.Sites, b.Sites), Teams: intersectSet(a.Teams, b.Teams)}
}

// unionScopes unites scopes per dimension; without scopes it is empty.
func unionScopes(scopes []Scope) Scope {
	var out Scope
	for i := range scopes {
		out.Clients = unionSet(out.Clients, scopes[i].Clients)
		out.Sites = unionSet(out.Sites, scopes[i].Sites)
		out.Teams = unionSet(out.Teams, scopes[i].Teams)
	}
	return out
}

// intersectSet returns the IDs in both sets, lower case and sorted.
func intersectSet(a, b ScopeSet) ScopeSet {
	if a.All && b.All {
		return ScopeSet{All: true}
	}
	if a.All {
		a, b = b, a
	}
	in := make(map[string]bool, len(b.IDs))
	for _, id := range b.IDs {
		in[strings.ToLower(strings.TrimSpace(id))] = true
	}
	ids := make([]string, 0, len(a.IDs))
	for _, id := range a.IDs {
		if id = strings.ToLower(strings.TrimSpace(id)); id != "" && (b.All || in[id]) && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ScopeSet{IDs: ids}
}

func unionSet(a, b ScopeSet) ScopeSet {
	if a.All || b.All {
		return ScopeSet{All: true}
	}
	merged := ScopeSet{IDs: append(append([]string(nil), a.IDs...), b.IDs...)}
	return intersectSet(merged, ScopeSet{All: true})
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
