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
	Subject        string `json:"sub"`
	OrganizationID string `json:"organization_id"`
	OrgID          string `json:"org_id,omitempty"`
	TenantID       string `json:"tenant_id,omitempty"`
	ClientID       string `json:"client_id,omitempty"`
	ExpiresAt      int64  `json:"exp,omitempty"`
	IssuedAt       int64  `json:"iat,omitempty"`
}

// SessionVerifier verifies the signature of session tokens issued by the
// identity module.
type SessionVerifier interface {
	Validate(token string) (*identity.SessionClaims, error)
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
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !requiresTenant(r) {
				next.ServeHTTP(w, r)
				return
			}

			claims, err := authenticate(r, verifier)
			if err != nil {
				api.WriteError(w, http.StatusUnauthorized, "Unauthorized", err.Error())
				return
			}

			ctx := context.WithValue(r.Context(), claimsContextKey{}, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
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
		IssuedAt:       sessionClaims.IssuedAt.Unix(),
		ExpiresAt:      sessionClaims.ExpiresAt.Unix(),
	}, nil
}

// TenantMiddleware attaches tenant context from development headers or JWT claims.
func TenantMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requiresTenant(r) {
			next.ServeHTTP(w, r)
			return
		}

		// The token is authoritative: a verified organization claim must never
		// be overridable by a request header, otherwise any authenticated
		// caller could read and write another tenant's data. The headers are
		// only honoured for requests that carry no claims at all, which is the
		// unverified development mode and the header-authenticated collector.
		orgID := ""
		clientID := ""

		claims, err := claimsFromRequestOrContext(r)
		if err == nil {
			orgID = claims.Organization()
			clientID = claims.ClientID
		}

		if orgID == "" {
			orgID = strings.TrimSpace(r.Header.Get("X-Organization-ID"))
			clientID = strings.TrimSpace(r.Header.Get("X-Client-ID"))
		} else if clientID == "" {
			clientID = strings.TrimSpace(r.Header.Get("X-Client-ID"))
		}

		if orgID == "" {
			api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
			return
		}

		ctx := tenant.WithTenant(r.Context(), tenant.TenantInfo{
			OrganizationID: orgID,
			ClientID:       clientID,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ClaimsFromContext extracts auth claims from the request context.
func ClaimsFromContext(ctx context.Context) (Claims, bool) {
	claims, ok := ctx.Value(claimsContextKey{}).(Claims)
	return claims, ok
}

func requiresTenant(r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		return false
	}
	switch r.URL.Path {
	case "/api/v1/auth/config", "/api/v1/auth/login", "/api/v1/auth/callback", "/api/v1/auth/refresh":
		return false
	default:
		return true
	}
}

func claimsFromRequestOrContext(r *http.Request) (Claims, error) {
	if claims, ok := ClaimsFromContext(r.Context()); ok {
		return claims, nil
	}
	return claimsFromRequest(r)
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
