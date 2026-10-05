package identity

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// discoveryDocument is the subset of the OIDC discovery document used here.
type discoveryDocument struct {
	Issuer  string `json:"issuer"`
	JWKSURI string `json:"jwks_uri"`
}

// jwksResponse is the JSON Web Key Set envelope.
type jwksResponse struct {
	Keys []json.RawMessage `json:"keys"`
}

// jwk is the subset of JWK fields supported for signature verification.
type jwk struct {
	KeyType   string `json:"kty"`
	KeyID     string `json:"kid"`
	Algorithm string `json:"alg"`
	Use       string `json:"use"`
	N         string `json:"n"`
	E         string `json:"e"`
	X         string `json:"x"`
	Y         string `json:"y"`
	Curve     string `json:"crv"`
}

// keyFunc resolves the verification key for a token header.
type keyFunc func(kid, alg string) (any, error)

// jwksCache fetches and caches the provider's JSON Web Key Set.
type jwksCache struct {
	mu     sync.RWMutex
	keys   []jwk
	expiry time.Time
}

const (
	jwksCacheTTL    = 10 * time.Minute
	maxResponseSize = 1 << 20 // 1 MiB cap for discovery/JWKS/token responses
)

// discover fetches the issuer's discovery document and validates that the
// declared issuer matches the configured one.
func (p *OIDCProvider) discover(ctx context.Context) (*discoveryDocument, error) {
	base := strings.TrimRight(strings.TrimSpace(p.config.IssuerURL), "/")
	if base == "" {
		return nil, errors.New("identity: OIDC issuer URL is required")
	}

	body, err := p.getJSON(ctx, base+"/.well-known/openid-configuration")
	if err != nil {
		return nil, fmt.Errorf("identity: fetch OIDC discovery document: %w", err)
	}

	var doc discoveryDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("identity: parse OIDC discovery document: %w", err)
	}
	if strings.TrimSpace(doc.JWKSURI) == "" {
		return nil, errors.New("identity: discovery document is missing jwks_uri")
	}
	if iss := strings.TrimSpace(doc.Issuer); iss != "" && !sameIssuer(iss, p.config.IssuerURL) {
		return nil, fmt.Errorf("identity: discovery issuer %q does not match configured issuer", doc.Issuer)
	}
	return &doc, nil
}

func sameIssuer(a, b string) bool {
	return strings.TrimRight(strings.TrimSpace(a), "/") == strings.TrimRight(strings.TrimSpace(b), "/")
}

// verificationKeys returns the cached JWKS, refreshing it when expired. On a
// refresh failure the previous key set is kept when one exists.
func (c *jwksCache) verificationKeys(ctx context.Context, p *OIDCProvider) ([]jwk, error) {
	c.mu.RLock()
	if len(c.keys) > 0 && time.Now().Before(c.expiry) {
		keys := c.keys
		c.mu.RUnlock()
		return keys, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.keys) > 0 && time.Now().Before(c.expiry) {
		return c.keys, nil
	}

	doc, err := p.discover(ctx)
	if err != nil {
		if len(c.keys) > 0 {
			return c.keys, nil
		}
		return nil, err
	}

	body, err := p.getJSON(ctx, doc.JWKSURI)
	if err != nil {
		if len(c.keys) > 0 {
			return c.keys, nil
		}
		return nil, fmt.Errorf("identity: fetch JWKS: %w", err)
	}

	var set jwksResponse
	if err := json.Unmarshal(body, &set); err != nil {
		if len(c.keys) > 0 {
			return c.keys, nil
		}
		return nil, fmt.Errorf("identity: parse JWKS: %w", err)
	}

	keys := make([]jwk, 0, len(set.Keys))
	for _, raw := range set.Keys {
		var key jwk
		if err := json.Unmarshal(raw, &key); err != nil {
			continue
		}
		if key.Use != "" && key.Use != "sig" {
			continue
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil, errors.New("identity: JWKS contains no usable signing keys")
	}

	c.keys = keys
	c.expiry = time.Now().Add(jwksCacheTTL)
	return keys, nil
}

// getJSON performs a bounded GET request and returns the response body.
func (p *OIDCProvider) getJSON(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
}

// resolveKey selects the verification key matching the token header. When the
// token's kid is unknown the cache is refreshed once to pick up key rotation.
func (p *OIDCProvider) resolveKey(ctx context.Context, kid, alg string) (any, error) {
	keys, err := p.jwks.verificationKeys(ctx, p)
	if err != nil {
		return nil, err
	}

	if key, err := selectKey(keys, kid, alg); err == nil {
		return key, nil
	}

	// Force a refresh once: the provider may have rotated keys.
	p.jwks.mu.Lock()
	p.jwks.expiry = time.Time{}
	p.jwks.mu.Unlock()

	keys, err = p.jwks.verificationKeys(ctx, p)
	if err != nil {
		return nil, err
	}
	return selectKey(keys, kid, alg)
}

func selectKey(keys []jwk, kid, alg string) (any, error) {
	kid = strings.TrimSpace(kid)
	var matched *jwk
	for i := range keys {
		key := &keys[i]
		if kid != "" && key.KeyID != kid {
			continue
		}
		pub, err := key.publicKey()
		if err != nil || !supportsAlgorithm(pub, alg) {
			continue
		}
		if matched != nil {
			return nil, errors.New("identity: ambiguous JWKS key selection")
		}
		matched = key
	}
	if matched == nil {
		return nil, fmt.Errorf("identity: no JWKS key matches kid %q", kid)
	}
	return matched.publicKey()
}

func supportsAlgorithm(pub any, alg string) bool {
	switch alg {
	case "RS256":
		_, ok := pub.(*rsa.PublicKey)
		return ok
	case "ES256":
		key, ok := pub.(*ecdsa.PublicKey)
		return ok && key.Curve == elliptic.P256()
	default:
		return false
	}
}

// publicKey converts a JWK to a crypto public key.
func (k *jwk) publicKey() (any, error) {
	switch k.KeyType {
	case "RSA":
		n, err := decodeBase64BigInt(k.N)
		if err != nil {
			return nil, fmt.Errorf("identity: invalid RSA modulus: %w", err)
		}
		e, err := decodeBase64BigInt(k.E)
		if err != nil {
			return nil, fmt.Errorf("identity: invalid RSA exponent: %w", err)
		}
		if !e.IsInt64() || e.Int64() <= 0 || e.Int64() > int64(^uint32(0)) {
			return nil, errors.New("identity: unsupported RSA exponent")
		}
		return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
	case "EC":
		if k.Curve != "P-256" {
			return nil, fmt.Errorf("identity: unsupported EC curve %q", k.Curve)
		}
		x, err := decodeBase64BigInt(k.X)
		if err != nil {
			return nil, fmt.Errorf("identity: invalid EC x coordinate: %w", err)
		}
		y, err := decodeBase64BigInt(k.Y)
		if err != nil {
			return nil, fmt.Errorf("identity: invalid EC y coordinate: %w", err)
		}
		if !elliptic.P256().IsOnCurve(x, y) {
			return nil, errors.New("identity: EC point is not on P-256")
		}
		return &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, nil
	default:
		return nil, fmt.Errorf("identity: unsupported JWK key type %q", k.KeyType)
	}
}

func decodeBase64BigInt(value string) (*big.Int, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return nil, err
	}
	return new(big.Int).SetBytes(raw), nil
}

func sha256Sum(input string) [32]byte {
	return sha256.Sum256([]byte(input))
}

// verifyJWTSignature verifies the signature of a compact JWT using the given
// algorithm and key. Only RS256 and ES256 are accepted.
func verifyJWTSignature(signingInput string, signature []byte, alg string, key any) error {
	switch alg {
	case "RS256":
		pub, ok := key.(*rsa.PublicKey)
		if !ok {
			return errors.New("identity: RS256 requires an RSA key")
		}
		hash := sha256Sum(signingInput)
		return rsa.VerifyPKCS1v15(pub, crypto.SHA256, hash[:], signature)
	case "ES256":
		pub, ok := key.(*ecdsa.PublicKey)
		if !ok {
			return errors.New("identity: ES256 requires an ECDSA key")
		}
		hash := sha256Sum(signingInput)
		half := len(signature) / 2
		if len(signature) == 0 || len(signature)%2 != 0 {
			return errors.New("identity: malformed ES256 signature")
		}
		r := new(big.Int).SetBytes(signature[:half])
		s := new(big.Int).SetBytes(signature[half:])
		if !ecdsa.Verify(pub, hash[:], r, s) {
			return errors.New("identity: invalid ES256 signature")
		}
		return nil
	default:
		return fmt.Errorf("identity: unsupported signing algorithm %q", alg)
	}
}

// RSAKeyThumbprint returns the JWK thumbprint of an RSA public key (RFC 7638,
// SHA-256, base64url). It is the key id (kid) of the session signing keys.
func RSAKeyThumbprint(pub *rsa.PublicKey) (string, error) {
	if pub == nil || pub.N == nil {
		return "", errors.New("identity: RSA public key is required")
	}
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())
	n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	// The members in lexicographic order without whitespace, as RFC 7638
	// requires; the values are base64url and need no escaping.
	sum := sha256.Sum256([]byte(`{"e":"` + e + `","kty":"RSA","n":"` + n + `"}`))
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}
