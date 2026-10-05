package identity

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
)

// ErrSessionRevoked is returned by Validate for a token issued before the
// sessions of its user were revoked (TLC-04, AUT-02).
var ErrSessionRevoked = errors.New("identity: session has been revoked")

// SessionRevocationTTL is how long a revocation must be remembered: a token
// stays usable for its lifetime and can be refreshed within the refresh
// window after expiry.
const SessionRevocationTTL = sessionLifetime + refreshWindow

// revocationCheckTimeout bounds the blacklist lookup of a token validation.
const revocationCheckTimeout = 2 * time.Second

// SessionRevocations is the blacklist of revoked user sessions: every token of
// a user issued at or before the revocation time is rejected. Deactivation
// writes it so that sessions and refresh tokens end immediately (AUT-02).
type SessionRevocations struct {
	store cache.Store
}

// NewSessionRevocations creates a blacklist on the shared cache store. With
// the Redis store every replica sees a revocation at once.
func NewSessionRevocations(store cache.Store) *SessionRevocations {
	return &SessionRevocations{store: store}
}

func revocationKey(userID string) string { return "session-revoked:" + userID }

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

// SessionIssuer issues and validates internal RS256 session JWTs.
type SessionIssuer struct {
	privateKey *rsa.PrivateKey
	publicKey  *rsa.PublicKey
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

// NewSessionIssuer creates a session issuer from a PEM-encoded RSA private key.
func NewSessionIssuer(privateKeyPEM []byte) (*SessionIssuer, error) {
	block, rest := pem.Decode(privateKeyPEM)
	_ = rest // trailing data after the first PEM block is intentionally ignored
	if block == nil {
		return nil, errors.New("identity: decode private key PEM: no PEM block found")
	}

	var (
		privateKey *rsa.PrivateKey
		err        error
	)

	switch block.Type {
	case "RSA PRIVATE KEY":
		privateKey, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	default:
		var parsed any
		parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
		if err == nil {
			var ok bool
			privateKey, ok = parsed.(*rsa.PrivateKey)
			if !ok {
				return nil, errors.New("identity: private key is not RSA")
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("identity: parse private key: %w", err)
	}

	return &SessionIssuer{
		privateKey: privateKey,
		publicKey:  &privateKey.PublicKey,
	}, nil
}

// Issue signs the provided claims as an RS256 JWT.
func (s *SessionIssuer) Issue(claims SessionClaims) (string, error) {
	if s == nil || s.privateKey == nil {
		return "", errors.New("identity: session issuer is not configured")
	}

	header, err := json.Marshal(map[string]string{
		"alg": "RS256",
		"typ": "JWT",
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
	if s == nil || s.publicKey == nil {
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
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, fmt.Errorf("identity: parse JWT header: %w", err)
	}
	if header.Algorithm != "RS256" {
		return nil, fmt.Errorf("identity: unexpected JWT algorithm %q", header.Algorithm)
	}

	signingInput := parts[0] + "." + parts[1]
	hash := sha256.Sum256([]byte(signingInput))
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("identity: decode JWT signature: %w", err)
	}
	if err := rsa.VerifyPKCS1v15(s.publicKey, crypto.SHA256, hash[:], signature); err != nil {
		return nil, fmt.Errorf("identity: verify JWT signature: %w", err)
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
	}

	return &claims, nil
}
