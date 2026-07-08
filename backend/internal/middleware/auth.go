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

// AuthMiddleware parses bearer tokens and stores claims in the request context.
// SECURITY NOTE: This implementation skips JWT signature verification and is
// intended for DEVELOPMENT ONLY. In production, tokens MUST be verified against
// the OIDC provider's public keys (JWKS endpoint). A production-ready version
// should accept an OIDC issuer URL configuration and validate signatures,
// audience, and expiry cryptographically.
func AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requiresTenant(r) {
			next.ServeHTTP(w, r)
			return
		}

		claims, err := claimsFromRequest(r)
		if err != nil {
			api.WriteError(w, http.StatusUnauthorized, "Unauthorized", err.Error())
			return
		}

		ctx := context.WithValue(r.Context(), claimsContextKey{}, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// TenantMiddleware attaches tenant context from development headers or JWT claims.
func TenantMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requiresTenant(r) {
			next.ServeHTTP(w, r)
			return
		}

		orgID := strings.TrimSpace(r.Header.Get("X-Organization-ID"))
		clientID := strings.TrimSpace(r.Header.Get("X-Client-ID"))

		if orgID == "" {
			claims, err := claimsFromRequestOrContext(r)
			if err != nil {
				api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
				return
			}
			orgID = claims.Organization()
			if clientID == "" {
				clientID = claims.ClientID
			}
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
	case "/api/v1/auth/login", "/api/v1/auth/callback", "/api/v1/auth/refresh":
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
