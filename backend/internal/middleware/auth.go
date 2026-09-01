package middleware

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

type claimsContextKey struct{}

// Claims contains the subset of JWT claims used by the development auth flow.
type Claims struct {
	Subject        string                `json:"sub"`
	OrganizationID string                `json:"organization_id"`
	OrgID          string                `json:"org_id,omitempty"`
	TenantID       string                `json:"tenant_id,omitempty"`
	ClientID       string                `json:"client_id,omitempty"`
	Permissions    []identity.Permission `json:"permissions,omitempty"`
	ExpiresAt      int64                 `json:"exp,omitempty"`
	IssuedAt       int64                 `json:"iat,omitempty"`
}

// SessionVerifier verifies the signature of session tokens issued by the
// identity module.
type SessionVerifier interface {
	Validate(token string) (*identity.SessionClaims, error)
}

// APIKeyAuthenticator validates API service tokens (X-API-Key) and resolves
// their metadata.
type APIKeyAuthenticator interface {
	Validate(ctx context.Context, rawKey string) (*identity.APIKeyInfo, error)
}

// PrincipalFromContext extracts the authenticated principal populated by the
// auth middleware. Downstream middleware and handlers must source identity,
// tenant and permission information exclusively from here.
func PrincipalFromContext(ctx context.Context) (identity.Principal, bool) {
	return identity.PrincipalFromContext(ctx)
}

// AuthMiddleware parses bearer tokens and stores claims in the request context.
// SECURITY NOTE: This variant skips JWT signature verification and is intended
// for DEVELOPMENT ONLY. Production deployments must use
// AuthMiddlewareWithVerifier so that session tokens are verified
// cryptographically.
func AuthMiddleware(next http.Handler) http.Handler {
	return AuthMiddlewareWithVerifier(nil)(next)
}

// AuthMiddlewareWithVerifier returns an auth middleware that cryptographically
// verifies session tokens with the given verifier. When verifier is nil the
// middleware falls back to unverified claim parsing for local development.
func AuthMiddlewareWithVerifier(verifier SessionVerifier) func(http.Handler) http.Handler {
	return AuthMiddlewareWithAPIKeys(verifier, nil)
}

// AuthMiddlewareWithAPIKeys returns an auth middleware that authenticates
// session bearer tokens and X-API-Key service tokens, populating a single
// authenticated principal in the request context. When verifier is nil the
// middleware falls back to unverified claim parsing for local development;
// API keys are still fully verified against their store.
func AuthMiddlewareWithAPIKeys(verifier SessionVerifier, apiKeys APIKeyAuthenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !requiresAuth(r) {
				next.ServeHTTP(w, r)
				return
			}

			principal, claims, err := authenticateRequest(r, verifier, apiKeys)
			if err != nil {
				api.WriteError(w, http.StatusUnauthorized, "Unauthorized", err.Error())
				return
			}

			ctx := identity.WithPrincipal(r.Context(), principal)
			ctx = identity.WithPermissions(ctx, principal.Permissions)
			ctx = context.WithValue(ctx, claimsContextKey{}, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// authenticateRequest resolves the caller's credentials. An X-API-Key header
// takes precedence over a bearer token so service tokens keep working for
// clients that forward both.
func authenticateRequest(r *http.Request, verifier SessionVerifier, apiKeys APIKeyAuthenticator) (identity.Principal, Claims, error) {
	if apiKey := strings.TrimSpace(r.Header.Get("X-API-Key")); apiKey != "" {
		if apiKeys == nil {
			return identity.Principal{}, Claims{}, fmt.Errorf("API key authentication is not configured")
		}
		info, err := apiKeys.Validate(r.Context(), apiKey)
		if err != nil || info == nil {
			return identity.Principal{}, Claims{}, fmt.Errorf("invalid API key")
		}
		if info.OrganizationID == "" {
			return identity.Principal{}, Claims{}, fmt.Errorf("API key is missing its organization scope")
		}
		principal := identity.Principal{
			Subject:        info.ID,
			OrganizationID: info.OrganizationID,
			ClientScope:    info.ClientScope,
			Permissions:    info.Scopes,
			Type:           identity.PrincipalTypeAPIKey,
		}
		return principal, Claims{
			Subject:        info.ID,
			OrganizationID: info.OrganizationID,
			ClientID:       info.ClientScope,
		}, nil
	}

	claims, err := authenticate(r, verifier)
	if err != nil {
		return identity.Principal{}, Claims{}, err
	}
	principal := identity.Principal{
		Subject:        claims.Subject,
		OrganizationID: claims.Organization(),
		ClientScope:    claims.ClientID,
		Permissions:    claims.Permissions,
		Type:           identity.PrincipalTypeUser,
	}
	return principal, claims, nil
}

func authenticate(r *http.Request, verifier SessionVerifier) (Claims, error) {
	if verifier == nil {
		return claimsFromRequest(r)
	}

	authz := strings.TrimSpace(r.Header.Get("Authorization"))
	if authz == "" {
		return Claims{}, fmt.Errorf("missing bearer token")
	}
	parts := strings.SplitN(authz, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
		return Claims{}, fmt.Errorf("invalid authorization header")
	}

	sessionClaims, err := verifier.Validate(strings.TrimSpace(parts[1]))
	if err != nil {
		return Claims{}, fmt.Errorf("invalid session token")
	}
	if sessionClaims.OrganizationID == "" {
		return Claims{}, fmt.Errorf("organization claim is required")
	}
	if sessionClaims.ExpiresAt.IsZero() {
		return Claims{}, fmt.Errorf("session token expiry is required")
	}
	if !time.Now().UTC().Before(sessionClaims.ExpiresAt) {
		return Claims{}, fmt.Errorf("token is expired")
	}

	return Claims{
		Subject:        sessionClaims.Subject,
		OrganizationID: sessionClaims.OrganizationID,
		ClientID:       sessionClaims.ClientScope,
		Permissions:    sessionClaims.Permissions,
		IssuedAt:       sessionClaims.IssuedAt.Unix(),
		ExpiresAt:      sessionClaims.ExpiresAt.Unix(),
	}, nil
}

// TenantMiddleware attaches the tenant context. It reads the organization,
// client scope and subject exclusively from the authenticated principal
// populated by the auth middleware; client-supplied headers such as
// X-Organization-ID are never consulted, so a request can never impersonate
// another tenant by changing a header.
func TenantMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requiresAuth(r) {
			next.ServeHTTP(w, r)
			return
		}

		principal, ok := PrincipalFromContext(r.Context())
		if !ok || principal.OrganizationID == "" {
			api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
			return
		}

		ctx := tenant.WithTenant(r.Context(), tenant.TenantInfo{
			OrganizationID: principal.OrganizationID,
			ClientID:       principal.ClientScope,
			UserID:         principal.Subject,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ClaimsFromContext extracts auth claims from the request context.
func ClaimsFromContext(ctx context.Context) (Claims, bool) {
	claims, ok := ctx.Value(claimsContextKey{}).(Claims)
	return claims, ok
}

func requiresAuth(r *http.Request) bool {
	// SCIM is the inbound provisioning surface (spec §11.1) and must be
	// authenticated just like the REST API; the IdP authenticates with a
	// bearer token whose claims carry the tenant context.
	if strings.HasPrefix(r.URL.Path, "/scim/") {
		return true
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		return false
	}
	switch r.URL.Path {
	case "/api/v1/auth/config", "/api/v1/auth/callback", "/api/v1/auth/refresh":
		return false
	default:
		return true
	}
}

func claimsFromRequest(r *http.Request) (Claims, error) {
	authz := strings.TrimSpace(r.Header.Get("Authorization"))
	if authz == "" {
		return Claims{}, fmt.Errorf("missing bearer token")
	}
	parts := strings.SplitN(authz, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
		return Claims{}, fmt.Errorf("invalid authorization header")
	}
	return parseJWTClaims(parts[1])
}

func parseJWTClaims(token string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return Claims{}, fmt.Errorf("invalid JWT format")
	}

	// Even in unverified development mode the token header must declare the
	// expected asymmetric algorithm, so that alg=none or HMAC-confusion
	// tokens are rejected instead of trusted.
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Claims{}, fmt.Errorf("decode JWT header: %w", err)
	}
	var header struct {
		Algorithm string `json:"alg"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return Claims{}, fmt.Errorf("parse JWT header: %w", err)
	}
	if header.Algorithm != "RS256" {
		return Claims{}, fmt.Errorf("unexpected JWT algorithm %q", header.Algorithm)
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, fmt.Errorf("decode JWT claims: %w", err)
	}

	var raw struct {
		Subject        string          `json:"sub"`
		OrganizationID string          `json:"organization_id"`
		OrgID          string          `json:"org_id,omitempty"`
		TenantID       string          `json:"tenant_id,omitempty"`
		ClientID       string          `json:"client_id,omitempty"`
		Permissions    []string        `json:"permissions,omitempty"`
		ExpiresAt      json.RawMessage `json:"exp,omitempty"`
		IssuedAt       json.RawMessage `json:"iat,omitempty"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return Claims{}, fmt.Errorf("parse JWT claims: %w", err)
	}

	expiresAt, err := parseTimestampClaim(raw.ExpiresAt)
	if err != nil {
		return Claims{}, fmt.Errorf("parse exp claim: %w", err)
	}
	issuedAt, err := parseTimestampClaim(raw.IssuedAt)
	if err != nil {
		return Claims{}, fmt.Errorf("parse iat claim: %w", err)
	}

	claims := Claims{
		Subject:        raw.Subject,
		OrganizationID: raw.OrganizationID,
		OrgID:          raw.OrgID,
		TenantID:       raw.TenantID,
		ClientID:       raw.ClientID,
		Permissions:    permissionsFromStrings(raw.Permissions),
		ExpiresAt:      expiresAt,
		IssuedAt:       issuedAt,
	}
	if claims.Organization() == "" {
		return Claims{}, fmt.Errorf("organization claim is required")
	}
	if claims.ExpiresAt > 0 && time.Now().Unix() >= claims.ExpiresAt {
		return Claims{}, fmt.Errorf("token is expired")
	}
	return claims, nil
}

func (c Claims) Organization() string {
	if c.OrganizationID != "" {
		return c.OrganizationID
	}
	if c.OrgID != "" {
		return c.OrgID
	}
	return c.TenantID
}

func parseTimestampClaim(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}

	var unix int64
	if err := json.Unmarshal(raw, &unix); err == nil {
		return unix, nil
	}

	var ts string
	if err := json.Unmarshal(raw, &ts); err == nil {
		parsed, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			return 0, err
		}
		return parsed.Unix(), nil
	}

	return 0, fmt.Errorf("unsupported timestamp format")
}

func permissionsFromStrings(values []string) []identity.Permission {
	if len(values) == 0 {
		return nil
	}
	permissions := make([]identity.Permission, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			permissions = append(permissions, identity.Permission(trimmed))
		}
	}
	return permissions
}
