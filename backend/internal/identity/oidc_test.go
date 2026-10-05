package identity

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestValidateIDTokenAcceptsValidToken proves the happy path: a correctly
// signed token from the issuer's JWKS validates and exposes its claims.
func TestValidateIDTokenAcceptsValidToken(t *testing.T) {
	fake := newFakeOIDCServer(t)
	provider := fake.provider("reticora-app")

	claims, err := provider.ValidateIDToken(context.Background(), fake.idToken(nil))
	if err != nil {
		t.Fatalf("expected a valid token to pass, got %v", err)
	}
	if claims.Subject != "user-123" {
		t.Fatalf("unexpected subject %q", claims.Subject)
	}
	if len(claims.Groups) == 0 {
		t.Fatal("expected groups to be decoded")
	}
}

// TestValidateIDTokenRejectsBadSignature proves that a token signed by an
// attacker key is rejected even though its claims are well-formed.
func TestValidateIDTokenRejectsBadSignature(t *testing.T) {
	fake := newFakeOIDCServer(t)
	provider := fake.provider("reticora-app")

	attackerKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	forged := signJWT(t, attackerKey, fake.keyID, map[string]any{
		"iss": fake.issuer(),
		"sub": "attacker",
		"aud": "reticora-app",
		"exp": time.Now().Add(5 * time.Minute).Unix(),
	})

	if _, err := provider.ValidateIDToken(context.Background(), forged); err == nil {
		t.Fatal("expected a token with an invalid signature to be rejected")
	}
}

// TestValidateIDTokenRejectsTamperedPayload proves that modifying the payload
// after signing invalidates the token.
func TestValidateIDTokenRejectsTamperedPayload(t *testing.T) {
	fake := newFakeOIDCServer(t)
	provider := fake.provider("reticora-app")

	token := fake.idToken(nil)
	parts := strings.Split(token, ".")
	payload, err := json.Marshal(map[string]any{
		"iss": fake.issuer(),
		"sub": "attacker",
		"aud": "reticora-app",
		"exp": time.Now().Add(5 * time.Minute).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString(payload) + "." + parts[2]

	if _, err := provider.ValidateIDToken(context.Background(), tampered); err == nil {
		t.Fatal("expected a tampered token to be rejected")
	}
}

// TestValidateIDTokenRejectsWrongAudience proves the aud check: a token meant
// for a different client must not be accepted.
func TestValidateIDTokenRejectsWrongAudience(t *testing.T) {
	fake := newFakeOIDCServer(t)
	provider := fake.provider("reticora-app")

	if _, err := provider.ValidateIDToken(context.Background(), fake.idToken(map[string]any{
		"aud": "other-client",
	})); err == nil {
		t.Fatal("expected a token with a foreign audience to be rejected")
	}

	// Multiple audiences without azp naming this client must be rejected too.
	if _, err := provider.ValidateIDToken(context.Background(), fake.idToken(map[string]any{
		"aud": []string{"reticora-app", "other-client"},
		"azp": "other-client",
	})); err == nil {
		t.Fatal("expected a token with a mismatched azp to be rejected")
	}
}

// TestValidateIDTokenRejectsMissingAudience proves that a token without an
// aud claim is rejected.
func TestValidateIDTokenRejectsMissingAudience(t *testing.T) {
	fake := newFakeOIDCServer(t)
	provider := fake.provider("reticora-app")

	if _, err := provider.ValidateIDToken(context.Background(), fake.idToken(map[string]any{
		"aud": nil,
	})); err == nil {
		t.Fatal("expected a token without an audience to be rejected")
	}
}

// TestValidateIDTokenRejectsExpiredAndFutureTokens proves the temporal checks.
func TestValidateIDTokenRejectsExpiredAndFutureTokens(t *testing.T) {
	fake := newFakeOIDCServer(t)
	provider := fake.provider("reticora-app")

	if _, err := provider.ValidateIDToken(context.Background(), fake.idToken(map[string]any{
		"exp": time.Now().Add(-time.Minute).Unix(),
	})); err == nil {
		t.Fatal("expected an expired token to be rejected")
	}

	if _, err := provider.ValidateIDToken(context.Background(), fake.idToken(map[string]any{
		"exp": nil,
	})); err == nil {
		t.Fatal("expected a token without expiry to be rejected")
	}

	if _, err := provider.ValidateIDToken(context.Background(), fake.idToken(map[string]any{
		"iat": time.Now().Add(10 * time.Minute).Unix(),
	})); err == nil {
		t.Fatal("expected a token issued in the future to be rejected")
	}

	if _, err := provider.ValidateIDToken(context.Background(), fake.idToken(map[string]any{
		"nbf": time.Now().Add(10 * time.Minute).Unix(),
	})); err == nil {
		t.Fatal("expected a not-yet-valid token to be rejected")
	}
}

// TestValidateIDTokenRejectsWrongIssuer proves the iss check.
func TestValidateIDTokenRejectsWrongIssuer(t *testing.T) {
	fake := newFakeOIDCServer(t)
	provider := fake.provider("reticora-app")

	if _, err := provider.ValidateIDToken(context.Background(), fake.idToken(map[string]any{
		"iss": "https://evil.example.com",
	})); err == nil {
		t.Fatal("expected a token from an unexpected issuer to be rejected")
	}
}

// TestValidateIDTokenRejectsUnexpectedAlgorithm proves that alg=none and
// HMAC-confusion headers are rejected before signature processing.
func TestValidateIDTokenRejectsUnexpectedAlgorithm(t *testing.T) {
	fake := newFakeOIDCServer(t)
	provider := fake.provider("reticora-app")

	for _, alg := range []string{"none", "HS256", "RS512"} {
		header, err := json.Marshal(map[string]string{"alg": alg, "typ": "JWT", "kid": fake.keyID})
		if err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(map[string]any{
			"iss": fake.issuer(),
			"sub": "attacker",
			"aud": "reticora-app",
			"exp": time.Now().Add(5 * time.Minute).Unix(),
		})
		if err != nil {
			t.Fatal(err)
		}
		token := base64.RawURLEncoding.EncodeToString(header) + "." +
			base64.RawURLEncoding.EncodeToString(payload) + "." +
			base64.RawURLEncoding.EncodeToString([]byte("sig"))

		if _, err := provider.ValidateIDToken(context.Background(), token); err == nil {
			t.Fatalf("expected alg=%q token to be rejected", alg)
		}
	}
}

// TestValidateIDTokenRejectsUnknownKeyID proves that a token referencing a key
// the provider does not publish is rejected.
func TestValidateIDTokenRejectsUnknownKeyID(t *testing.T) {
	fake := newFakeOIDCServer(t)
	provider := fake.provider("reticora-app")

	token := signJWT(t, fake.privateKey, "no-such-key", map[string]any{
		"iss": fake.issuer(),
		"sub": "user-123",
		"aud": "reticora-app",
		"exp": time.Now().Add(5 * time.Minute).Unix(),
	})

	if _, err := provider.ValidateIDToken(context.Background(), token); err == nil {
		t.Fatal("expected a token with an unknown kid to be rejected")
	}
}

// TestValidateIDTokenNonceCheck proves the optional nonce enforcement: a
// mismatching nonce is rejected, a matching one accepted.
func TestValidateIDTokenNonceCheck(t *testing.T) {
	fake := newFakeOIDCServer(t)
	provider := fake.provider("reticora-app")

	token := fake.idToken(map[string]any{"nonce": "expected-nonce"})

	if _, err := provider.ValidateIDTokenWithNonce(context.Background(), token, "expected-nonce"); err != nil {
		t.Fatalf("expected matching nonce to pass, got %v", err)
	}
	if _, err := provider.ValidateIDTokenWithNonce(context.Background(), token, "other-nonce"); err == nil {
		t.Fatal("expected a mismatched nonce to be rejected")
	}
	if _, err := provider.ValidateIDTokenWithNonce(context.Background(), fake.idToken(nil), "expected-nonce"); err == nil {
		t.Fatal("expected a missing nonce to be rejected when one is expected")
	}
}

// TestValidateIDTokenRejectsIssuerJWKSWithoutKeys proves validation fails
// closed when the provider serves no usable signing keys.
func TestValidateIDTokenFailsClosedWithoutJWKS(t *testing.T) {
	fake := newFakeOIDCServer(t)

	// Point the provider at a server that has no discovery document at all.
	provider := NewOIDCProvider(OIDCConfig{IssuerURL: "http://127.0.0.1:1", ClientID: "reticora-app"})
	if _, err := provider.ValidateIDToken(context.Background(), fake.idToken(nil)); err == nil {
		t.Fatal("expected validation to fail when JWKS cannot be retrieved")
	}
}

// TestValidateIDTokenReadsOrganizationAttribute covers WP-043 (AUT-09, CH26):
// the organization is the organization_id user attribute, as a string or a
// single-valued array; several values or a non-UUID are rejected, and a token
// without the attribute carries no organization.
func TestValidateIDTokenReadsOrganizationAttribute(t *testing.T) {
	fake := newFakeOIDCServer(t)
	provider := fake.provider("reticora-app")
	org := "123e4567-e89b-12d3-a456-426614174000"
	for _, c := range []struct {
		name    string
		value   any
		want    string
		wantErr bool
	}{
		{"string", org, org, false},
		{"single-valued array", []string{org}, org, false},
		{"absent", nil, "", false},
		{"two values", []string{org, "223e4567-e89b-12d3-a456-426614174000"}, "", true},
		{"not a UUID", "reticora-demo", "", true},
	} {
		claims, err := provider.ValidateIDToken(context.Background(), fake.idToken(map[string]any{"organization_id": c.value}))
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: accepted, want an error", c.name)
			}
			continue
		}
		if err != nil || claims.OrganizationID != c.want {
			t.Errorf("%s: organization %q, %v; want %q", c.name, claims.OrganizationID, err, c.want)
		}
	}
	claims, err := provider.ValidateIDToken(context.Background(), fake.idToken(map[string]any{"email_verified": false}))
	if err != nil || claims.EmailVerified {
		t.Errorf("email_verified=false: %+v, %v", claims, err)
	}
}
