package identity

import (
	"context"
	"sort"
	"strings"
)

// PrincipalType identifies the kind of authenticated caller.
type PrincipalType string

const (
	// PrincipalTypeUser is an interactive user authenticated by a session token.
	PrincipalTypeUser PrincipalType = "user"
	// PrincipalTypeAPIKey is a service token authenticated via X-API-Key.
	PrincipalTypeAPIKey PrincipalType = "api_key"
)

// NoScopeID restricts a legacy client scope string to no client at all. It is
// used when a principal holds no client: an empty string would mean "all
// clients of the organization" to the client-scope helpers.
const NoScopeID = "00000000-0000-0000-0000-000000000000"

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
	// ClientScope is an optional client (sub-tenant) scope as a comma
	// separated list. For user sessions it is derived from Scope.
	ClientScope string
	// Scope is the union of the scopes of all role grants (RBA-03). It is set
	// for user sessions resolved from role assignments; nil means the
	// credential carries no resolved scope (API keys, legacy tokens).
	Scope *Scope
	// PermissionScopes holds the scope of every permission whose scope is
	// narrower than Scope. Permissions missing here are valid in Scope.
	PermissionScopes map[Permission]Scope
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

// ScopeFor returns the scope in which the principal holds the permission.
// Without a resolved scope it returns false.
func (p *Principal) ScopeFor(required Permission) (Scope, bool) {
	if p.Scope == nil || !p.Has(required) {
		return Scope{}, false
	}
	if s, ok := p.PermissionScopes[required]; ok {
		return s, true
	}
	return *p.Scope, true
}

// ScopeSet is the value of one scope dimension: either all objects of the
// organization (All) or exactly the listed IDs. An empty set grants nothing.
type ScopeSet struct {
	All bool     `json:"all,omitempty"`
	IDs []string `json:"ids,omitempty"`
}

// Scope is the set of clients, sites and teams a grant or a principal is
// restricted to (TEN-04, CH11, CH25).
type Scope struct {
	Clients ScopeSet `json:"clients"`
	Sites   ScopeSet `json:"sites"`
	Teams   ScopeSet `json:"teams"`
}

// OrgWide reports whether the scope covers the whole organization.
func (s *Scope) OrgWide() bool { return s.Clients.All && s.Sites.All && s.Teams.All }

// LegacyClientScope encodes the client dimension for the comma separated
// client scope of tenant.TenantInfo: empty for all clients, NoScopeID for no
// client.
func (s *Scope) LegacyClientScope() string {
	if s.Clients.All {
		return ""
	}
	if len(s.Clients.IDs) == 0 {
		return NoScopeID
	}
	return strings.Join(s.Clients.IDs, ",")
}

func (s *Scope) equal(o *Scope) bool {
	return s.Clients.equal(o.Clients) && s.Sites.equal(o.Sites) && s.Teams.equal(o.Teams)
}

func (s ScopeSet) equal(o ScopeSet) bool {
	if s.All != o.All || len(s.IDs) != len(o.IDs) {
		return false
	}
	for i := range s.IDs {
		if s.IDs[i] != o.IDs[i] {
			return false
		}
	}
	return true
}

// Grant is one role assignment: the permissions of the role together with the
// scope it was assigned in. Empty ID lists leave the dimension unrestricted, so
// a grant without any restriction is org-wide (RBA-03).
type Grant struct {
	Permissions []Permission
	Clients     []string
	Sites       []string
	Teams       []string
}

// OrgWideGrant grants the permissions in the whole organization.
func OrgWideGrant(permissions []Permission) Grant { return Grant{Permissions: permissions} }

func (g *Grant) orgWide() bool { return len(g.Clients) == 0 && len(g.Sites) == 0 && len(g.Teams) == 0 }

// Access is the resolved authorization of a user: the union of the
// permissions of all grants, the union scope used for the database tenant
// scope and the narrower scope of individual permissions.
type Access struct {
	Permissions      []Permission
	Scope            Scope
	PermissionScopes map[Permission]Scope
}

// ResolveAccess combines role grants according to RBA-03: permissions are the
// union of all grants; scopes are the union of the scope sets per dimension.
// An org-wide grant sets every dimension to all (NULL in the GUCs). A dimension
// no grant restricts is not limited by the grants (e.g. a client assignment
// leaves sites unrestricted within the client). Without any grant every
// dimension is empty, so the principal sees nothing (E-08, fail-closed).
// Team scopes act in addition to client and site scopes (E-11, intersection).
func ResolveAccess(grants []Grant) Access {
	access := Access{Scope: aggregateScope(grants)}
	byPermission := make(map[Permission][]Grant)
	order := make([]Permission, 0)
	for _, g := range grants {
		for _, p := range g.Permissions {
			if p == "" {
				continue
			}
			if _, ok := byPermission[p]; !ok {
				order = append(order, p)
			}
			byPermission[p] = append(byPermission[p], g)
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	access.Permissions = order
	for _, p := range order {
		s := aggregateScope(byPermission[p])
		if !s.equal(&access.Scope) {
			if access.PermissionScopes == nil {
				access.PermissionScopes = make(map[Permission]Scope)
			}
			access.PermissionScopes[p] = s
		}
	}
	return access
}

func aggregateScope(grants []Grant) Scope {
	if len(grants) == 0 {
		return Scope{}
	}
	for i := range grants {
		if grants[i].orgWide() {
			all := ScopeSet{All: true}
			return Scope{Clients: all, Sites: all, Teams: all}
		}
	}
	return Scope{
		Clients: unionDimension(grants, func(g Grant) []string { return g.Clients }),
		Sites:   unionDimension(grants, func(g Grant) []string { return g.Sites }),
		Teams:   unionDimension(grants, func(g Grant) []string { return g.Teams }),
	}
}

func unionDimension(grants []Grant, ids func(Grant) []string) ScopeSet {
	seen := make(map[string]struct{})
	restricted := false
	for _, g := range grants {
		values := ids(g)
		if len(values) == 0 {
			continue
		}
		restricted = true
		for _, id := range values {
			if id = strings.TrimSpace(id); id != "" {
				seen[strings.ToLower(id)] = struct{}{}
			}
		}
	}
	if !restricted {
		return ScopeSet{All: true}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return ScopeSet{IDs: out}
}
