package identity

import "context"

// PrincipalType identifies the kind of authenticated caller.
type PrincipalType string

const (
	// PrincipalTypeUser is an interactive user authenticated by a session token.
	PrincipalTypeUser PrincipalType = "user"
	// PrincipalTypeAPIKey is a service token authenticated via X-API-Key.
	PrincipalTypeAPIKey PrincipalType = "api_key"
)

// Principal is the single authenticated-identity representation for the
// request path. It is populated exclusively by the authentication middleware
// from verified credentials — never from client-supplied headers — and every
// downstream consumer (tenant context, rate limiting, idempotency,
// authorization) must read the identity from here.
type Principal struct {
	// Subject is the user ID for user principals or the key ID for API keys.
	Subject string
	// OrganizationID scopes every query the principal makes.
	OrganizationID string
	// ClientScope is an optional client (sub-tenant) scope.
	ClientScope string
	// Permissions is the effective permission set used for authorization.
	Permissions []Permission
	// Type distinguishes interactive users from service tokens.
	Type PrincipalType
}

type principalContextKey struct{}

// WithPrincipal stores the authenticated principal in the request context.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	p.Permissions = append([]Permission(nil), p.Permissions...)
	return context.WithValue(ctx, principalContextKey{}, p)
}

// PrincipalFromContext extracts the authenticated principal from the context.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalContextKey{}).(Principal)
	if !ok {
		return Principal{}, false
	}
	p.Permissions = append([]Permission(nil), p.Permissions...)
	return p, true
}

// Has reports whether the principal holds the given permission.
func (p Principal) Has(required Permission) bool {
	return HasPermission(p.Permissions, required)
}
