package identity

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
)

// Lifetimes of AUT-02: the access token lives 15 minutes, so role changes
// take effect at the next refresh at the latest. The refresh token expires
// after RefreshTokenLifetime without use and is rotated on every refresh; a
// refresh family ends after RefreshFamilyLifetime regardless of use.
const (
	sessionLifetime       = 15 * time.Minute
	RefreshTokenLifetime  = 24 * time.Hour
	RefreshFamilyLifetime = 7 * 24 * time.Hour
)

// ErrSessionRevoked is returned by Validate for a token issued before the
// sessions of its user were revoked or whose token id was revoked at logout
// (TLC-04, AUT-02).
var ErrSessionRevoked = errors.New("identity: session has been revoked")

// SessionRevocationTTL is how long a revocation must be remembered: every
// access token and every refresh token issued before it has expired by then.
const SessionRevocationTTL = sessionLifetime + RefreshTokenLifetime

// revocationCheckTimeout bounds the blacklist lookup of a token validation.
const revocationCheckTimeout = 2 * time.Second

// SessionRevocations is the blacklist of revoked sessions in the shared cache
// store (Redis): per user, every token issued at or before the revocation
// time is rejected (deactivation, TLC-04); per token id, a single access token
// is rejected until it expires (logout, AUT-02).
type SessionRevocations struct {
	store cache.Store
}

// NewSessionRevocations creates a blacklist on the shared cache store. With
// the Redis store every replica sees a revocation at once.
func NewSessionRevocations(store cache.Store) *SessionRevocations {
	return &SessionRevocations{store: store}
}

func revocationKey(userID string) string { return "session-revoked:" + userID }

func tokenRevocationKey(tokenID string) string { return "session-token-revoked:" + tokenID }

// RevokeUser rejects every session of the user issued at or before at.
func (r *SessionRevocations) RevokeUser(ctx context.Context, userID string, at time.Time) error {
	if r == nil || r.store == nil {
		return errors.New("identity: session revocation store is not configured")
	}
	if strings.TrimSpace(userID) == "" {
		return errors.New("identity: user id is required")
	}
	value := at.UTC().Format(time.RFC3339Nano)
	if err := r.store.Set(ctx, revocationKey(userID), value, SessionRevocationTTL); err != nil {
		return fmt.Errorf("identity: revoke sessions: %w", err)
	}
	return nil
}

// RevokedAt returns the last revocation time of the user's sessions, or the
// zero time when none is recorded.
func (r *SessionRevocations) RevokedAt(ctx context.Context, userID string) (time.Time, error) {
	value, ok, err := r.store.Get(ctx, revocationKey(userID))
	if err != nil {
		return time.Time{}, fmt.Errorf("identity: read session revocation: %w", err)
	}
	if !ok {
		return time.Time{}, nil
	}
	at, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("identity: parse session revocation: %w", err)
	}
	return at, nil
}

// RevokeToken rejects the access token with the given id until it expires.
func (r *SessionRevocations) RevokeToken(ctx context.Context, tokenID string, expiresAt time.Time) error {
	if r == nil || r.store == nil {
		return errors.New("identity: session revocation store is not configured")
	}
	ttl := time.Until(expiresAt)
	if strings.TrimSpace(tokenID) == "" || ttl <= 0 {
		return nil
	}
	if err := r.store.Set(ctx, tokenRevocationKey(tokenID), "1", ttl); err != nil {
		return fmt.Errorf("identity: revoke session token: %w", err)
	}
	return nil
}

// tokenRevoked reports whether the access token id was revoked.
func (r *SessionRevocations) tokenRevoked(ctx context.Context, tokenID string) (bool, error) {
	if tokenID == "" {
		return false, nil
	}
	_, ok, err := r.store.Get(ctx, tokenRevocationKey(tokenID))
	if err != nil {
		return false, fmt.Errorf("identity: read session token revocation: %w", err)
	}
	return ok, nil
}

// SessionIssuer issues and validates internal RS256 session JWTs. Tokens carry
// the key id (kid, RFC 7638 thumbprint) of the signing key; previous keys
// stay valid for verification during a key rotation (SEC-06).
type SessionIssuer struct {
	privateKey *rsa.PrivateKey
	keyID      string
	// verifyKeys maps a key id to its public key: the signing key plus the
	// previous keys of a rotation overlap.
	verifyKeys map[string]*rsa.PublicKey
	// revocations rejects tokens of users whose sessions were revoked; nil
	// disables the check (tests without a cache store).
	revocations *SessionRevocations
}

// WithRevocations makes Validate reject tokens issued before a revocation of
// their user.
func (s *SessionIssuer) WithRevocations(r *SessionRevocations) *SessionIssuer {
	s.revocations = r
	return s
}

// Revocations returns the session blacklist, or nil when none is configured.
func (s *SessionIssuer) Revocations() *SessionRevocations {
	if s == nil {
		return nil
	}
	return s.revocations
}

// KeyID returns the key id of the signing key.
func (s *SessionIssuer) KeyID() string { return s.keyID }

// NewSessionIssuer creates a session issuer from a PEM-encoded RSA private key.
func NewSessionIssuer(privateKeyPEM []byte) (*SessionIssuer, error) {
	privateKey, err := parseRSAPrivateKey(privateKeyPEM)
	if err != nil {
		return nil, err
	}
	keyID, err := RSAKeyThumbprint(&privateKey.PublicKey)
	if err != nil {
		return nil, err
	}
	return &SessionIssuer{
		privateKey: privateKey,
		keyID:      keyID,
		verifyKeys: map[string]*rsa.PublicKey{keyID: &privateKey.PublicKey},
	}, nil
}

// WithPreviousKeys accepts tokens signed with the given keys (PEM, private
// or public) for the overlap of a key rotation; new tokens are signed with
// the current key only.
func (s *SessionIssuer) WithPreviousKeys(pems ...[]byte) error {
	for _, p := range pems {
		pub, err := parseRSAPublicKey(p)
		if err != nil {
			return err
		}
		kid, err := RSAKeyThumbprint(pub)
		if err != nil {
			return err
		}
		s.verifyKeys[kid] = pub
	}
	return nil
}

func parseRSAPrivateKey(privateKeyPEM []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(privateKeyPEM) // trailing data after the first PEM block is ignored
	if block == nil {
		return nil, errors.New("identity: decode private key PEM: no PEM block found")
	}
	if block.Type == "RSA PRIVATE KEY" {
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("identity: parse private key: %w", err)
		}
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("identity: parse private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("identity: private key is not RSA")
	}
	return key, nil
}

func parseRSAPublicKey(keyPEM []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("identity: decode key PEM: no PEM block found")
	}
	switch block.Type {
	case "PUBLIC KEY":
		parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("identity: parse public key: %w", err)
		}
		pub, ok := parsed.(*rsa.PublicKey)
		if !ok {
			return nil, errors.New("identity: public key is not RSA")
		}
		return pub, nil
	case "RSA PUBLIC KEY":
		pub, err := x509.ParsePKCS1PublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("identity: parse public key: %w", err)
		}
		return pub, nil
	}
	key, err := parseRSAPrivateKey(keyPEM)
	if err != nil {
		return nil, err
	}
	return &key.PublicKey, nil
}

// Issue signs the provided claims as an RS256 JWT with the key id in the
// header.
func (s *SessionIssuer) Issue(claims SessionClaims) (string, error) {
	if s == nil || s.privateKey == nil {
		return "", errors.New("identity: session issuer is not configured")
	}

	header, err := json.Marshal(map[string]string{
		"alg": "RS256",
		"typ": "JWT",
		"kid": s.keyID,
	})
	if err != nil {
		return "", fmt.Errorf("identity: marshal JWT header: %w", err)
	}

	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("identity: marshal JWT claims: %w", err)
	}

	encodedHeader := base64.RawURLEncoding.EncodeToString(header)
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	signingInput := encodedHeader + "." + encodedPayload

	hash := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, s.privateKey, crypto.SHA256, hash[:])
	if err != nil {
		return "", fmt.Errorf("identity: sign JWT: %w", err)
	}

	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// Validate verifies a session JWT signature, parses its claims and rejects
// tokens revoked through the blacklist. A blacklist that cannot be read fails
// the validation. Callers are responsible for enforcing expiry semantics.
func (s *SessionIssuer) Validate(token string) (*SessionClaims, error) {
	if s == nil || s.privateKey == nil {
		return nil, errors.New("identity: session issuer is not configured")
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("identity: invalid JWT format")
	}

	// Pin the signing algorithm so that alg=none or HMAC-confusion tokens are
	// rejected before any signature material is processed.
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("identity: decode JWT header: %w", err)
	}
	var header struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, fmt.Errorf("identity: parse JWT header: %w", err)
	}
	if header.Algorithm != "RS256" {
		return nil, fmt.Errorf("identity: unexpected JWT algorithm %q", header.Algorithm)
	}
	// Tokens without kid predate key ids (long-lived agent credentials) and
	// are verified with the current key only.
	keyID := header.KeyID
	if keyID == "" {
		keyID = s.keyID
	}
	publicKey, ok := s.verifyKeys[keyID]
	if !ok {
		return nil, fmt.Errorf("identity: unknown JWT key id %q", header.KeyID)
	}

	signingInput := parts[0] + "." + parts[1]
	hash := sha256.Sum256([]byte(signingInput))
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("identity: decode JWT signature: %w", err)
	}
	if verifyErr := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, hash[:], signature); verifyErr != nil {
		return nil, fmt.Errorf("identity: verify JWT signature: %w", verifyErr)
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("identity: decode JWT payload: %w", err)
	}

	var claims SessionClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("identity: parse JWT claims: %w", err)
	}

	if s.revocations != nil {
		ctx, cancel := context.WithTimeout(context.Background(), revocationCheckTimeout)
		defer cancel()
		revokedAt, err := s.revocations.RevokedAt(ctx, claims.Subject)
		if err != nil {
			return nil, err
		}
		if !revokedAt.IsZero() && !claims.IssuedAt.After(revokedAt) {
			return nil, ErrSessionRevoked
		}
		revoked, err := s.revocations.tokenRevoked(ctx, claims.ID)
		if err != nil {
			return nil, err
		}
		if revoked {
			return nil, ErrSessionRevoked
		}
	}

	return &claims, nil
}

// randomToken returns n random bytes, URL-safe base64 encoded.
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("identity: random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// newTokenID returns a random JWT id (jti).
func newTokenID() (string, error) { return randomToken(16) }

// Refresh token errors. Every one of them ends the session; the handler
// answers 401 and clears the refresh cookie.
var (
	ErrRefreshInvalid = errors.New("identity: refresh token is invalid or expired")
	// ErrRefreshReused: a rotated refresh token was presented again, which
	// means it leaked; the whole refresh family is revoked.
	ErrRefreshReused = errors.New("identity: refresh token was already used; session revoked")
)

// RefreshGrant is the session a refresh token stands for: its family, the
// start of the family and the claims of the last access token (without
// iat, exp and jti).
type RefreshGrant struct {
	Family  string        `json:"family"`
	Started time.Time     `json:"started"`
	Issued  time.Time     `json:"issued"`
	Claims  SessionClaims `json:"claims"`
}

// RefreshSessions stores the rotating refresh tokens in the shared cache
// store (Redis): only the SHA-256 of a token is a key. Every refresh consumes
// its token and issues a new one of the same family; presenting a consumed
// token again revokes the family (reuse detection). Logout revokes the family
// (AUT-02), and a user revocation (deactivation) rejects every refresh token
// issued before it.
type RefreshSessions struct {
	store       cache.Store
	revocations *SessionRevocations
}

// NewRefreshSessions creates the refresh token store. revocations may be nil
// (no user blacklist).
func NewRefreshSessions(store cache.Store, revocations *SessionRevocations) *RefreshSessions {
	return &RefreshSessions{store: store, revocations: revocations}
}

func refreshKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func refreshFamilyRevokedKey(family string) string { return "refresh-family-revoked:" + family }

// Issue stores a new refresh token for the grant and returns it. A grant
// without family starts a new one.
func (r *RefreshSessions) Issue(ctx context.Context, grant *RefreshGrant) (string, error) {
	now := time.Now().UTC()
	if grant.Family == "" {
		family, err := randomToken(16)
		if err != nil {
			return "", err
		}
		grant.Family, grant.Started = family, now
	}
	grant.Issued = now
	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(grant)
	if err != nil {
		return "", fmt.Errorf("identity: encode refresh grant: %w", err)
	}
	ttl := min(RefreshTokenLifetime, time.Until(grant.Started.Add(RefreshFamilyLifetime)))
	if ttl <= 0 {
		return "", ErrRefreshInvalid
	}
	if err := r.store.Set(ctx, "refresh:"+refreshKey(token), string(raw), ttl); err != nil {
		return "", fmt.Errorf("identity: store refresh token: %w", err)
	}
	return token, nil
}

// lookup reads the grant of a refresh token.
func (r *RefreshSessions) lookup(ctx context.Context, token string) (*RefreshGrant, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrRefreshInvalid
	}
	raw, ok, err := r.store.Get(ctx, "refresh:"+refreshKey(token))
	if err != nil {
		return nil, fmt.Errorf("identity: read refresh token: %w", err)
	}
	if !ok {
		return nil, ErrRefreshInvalid
	}
	var grant RefreshGrant
	if err := json.Unmarshal([]byte(raw), &grant); err != nil {
		return nil, fmt.Errorf("identity: decode refresh grant: %w", err)
	}
	return &grant, nil
}

// Consume validates a refresh token and uses it up. It fails for unknown,
// expired, reused and revoked tokens; the caller issues the successor with
// Issue on the returned grant.
func (r *RefreshSessions) Consume(ctx context.Context, token string) (*RefreshGrant, error) {
	grant, err := r.lookup(ctx, token)
	if err != nil {
		return nil, err
	}
	_, revoked, err := r.store.Get(ctx, refreshFamilyRevokedKey(grant.Family))
	if err != nil {
		return nil, fmt.Errorf("identity: read refresh family: %w", err)
	}
	if revoked {
		return nil, ErrSessionRevoked
	}
	uses, err := r.store.Increment(ctx, "refresh-used:"+refreshKey(token), RefreshTokenLifetime)
	if err != nil {
		return nil, fmt.Errorf("identity: consume refresh token: %w", err)
	}
	if uses > 1 {
		if err := r.RevokeFamily(ctx, grant.Family); err != nil {
			return nil, err
		}
		return nil, ErrRefreshReused
	}
	if time.Now().After(grant.Started.Add(RefreshFamilyLifetime)) {
		return nil, ErrRefreshInvalid
	}
	if r.revocations != nil {
		revokedAt, err := r.revocations.RevokedAt(ctx, grant.Claims.Subject)
		if err != nil {
			return nil, err
		}
		if !revokedAt.IsZero() && !grant.Issued.After(revokedAt) {
			return nil, ErrSessionRevoked
		}
	}
	return grant, nil
}

// RevokeFamily ends every refresh token of the family.
func (r *RefreshSessions) RevokeFamily(ctx context.Context, family string) error {
	if err := r.store.Set(ctx, refreshFamilyRevokedKey(family), "1", RefreshFamilyLifetime); err != nil {
		return fmt.Errorf("identity: revoke refresh family: %w", err)
	}
	return nil
}

// Revoke ends the session of a refresh token (logout). An unknown token is
// not an error.
func (r *RefreshSessions) Revoke(ctx context.Context, token string) error {
	grant, err := r.lookup(ctx, token)
	if errors.Is(err, ErrRefreshInvalid) {
		return nil
	}
	if err != nil {
		return err
	}
	return r.RevokeFamily(ctx, grant.Family)
}
