package identity

import (
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
)

// SessionIssuer issues and validates internal RS256 session JWTs.
type SessionIssuer struct {
	privateKey *rsa.PrivateKey
	publicKey  *rsa.PublicKey
}

// NewSessionIssuer creates a session issuer from a PEM-encoded RSA private key.
func NewSessionIssuer(privateKeyPEM []byte) (*SessionIssuer, error) {
	block, _ := pem.Decode(privateKeyPEM)
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

// Validate verifies a session JWT signature and parses its claims.
func (s *SessionIssuer) Validate(token string) (*SessionClaims, error) {
	if s == nil || s.publicKey == nil {
		return nil, errors.New("identity: session issuer is not configured")
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("identity: invalid JWT format")
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
	if !claims.ExpiresAt.IsZero() && time.Now().After(claims.ExpiresAt) {
		return nil, errors.New("identity: session token is expired")
	}

	return &claims, nil
}
