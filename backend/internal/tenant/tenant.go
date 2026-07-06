// Package tenant provides multi-tenancy primitives for Reticora.
// Each request is scoped to an organization (and optionally a client)
// using PostgreSQL Row-Level Security.
package tenant

import "context"

// TenantInfo holds the current request's tenant context.
type TenantInfo struct {
	OrganizationID string
	ClientID       string // optional, for client-scoped access
}

type contextKey struct{}

// WithTenant returns a new context carrying the tenant info.
func WithTenant(ctx context.Context, t TenantInfo) context.Context {
	return context.WithValue(ctx, contextKey{}, t)
}

// FromContext extracts tenant info from the context.
// Returns zero value if not set.
func FromContext(ctx context.Context) TenantInfo {
	t, _ := ctx.Value(contextKey{}).(TenantInfo)
	return t
}
