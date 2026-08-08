package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/go-chi/chi/v5"
)

// readPermissionFor maps a resource segment to the permission required to
// read it. The table mirrors the canonical permission catalog in
// internal/permission; the write permission is derived by replacing the
// ":read" suffix with ":write".
var readPermissionFor = map[string]identity.Permission{
	"cis":                identity.PermCIRead,
	"relationships":      identity.PermRelationshipRead,
	"topology":           identity.PermTopologyRead,
	"sites":              identity.PermSiteRead,
	"buildings":          identity.PermSiteRead,
	"rooms":              identity.PermSiteRead,
	"racks":              identity.PermRackRead,
	"rack-mounts":        identity.PermRackRead,
	"contacts":           identity.PermContactRead,
	"ci-contacts":        identity.PermContactRead,
	"collectors":         identity.PermDiscoveryRead,
	"discovery":          identity.PermDiscoveryRead,
	"assets":             identity.PermAssetRead,
	"assignments":        identity.PermAssignmentRead,
	"documents":          identity.PermDocumentRead,
	"stocktakes":         identity.PermStocktakeRead,
	"tickets":            identity.PermTicketRead,
	"slas":               identity.PermSLARead,
	"users":              identity.PermUserRead,
	"teams":              identity.PermUserRead,
	"clients":            identity.PermUserRead,
	"roles":              identity.PermRoleRead,
	"permissions":        identity.PermPermissionRead,
	"entitlements":       identity.PermEntitlementRead,
	"audit":              identity.PermAuditRead,
	"webhooks":           identity.PermWebhookRead,
	"credentials":        identity.PermCredentialRead,
	"export":             identity.PermCIRead,
	"ip-addresses":       identity.PermIPAMRead,
	"subnets":            identity.PermIPAMRead,
	"network-interfaces": identity.PermIPAMRead,
	"cables":             identity.PermRackRead,
	"forms":              identity.PermFormRead,
	"form-submissions":   identity.PermFormRead,
	"workflows":          identity.PermWorkflowRead,
	"workflow-runs":      identity.PermWorkflowRead,
	"compliance":         identity.PermComplianceRead,
	"monitoring":         identity.PermMonitoringRead,
	"iga":                identity.PermIGARead,
	"scim":               identity.PermIGARead,
	"search":             identity.PermSearchRead,
	"ai":                 identity.PermAIRead,
	"graphql":            identity.PermCIRead,
}

// writePermissionOverrides covers resources whose write permission does not
// follow the read→write suffix convention.
var writePermissionOverrides = map[string]identity.Permission{
	"sites":     identity.PermSiteWrite,
	"buildings": identity.PermSiteWrite,
	"rooms":     identity.PermSiteWrite,

	"contacts":     identity.PermContactWrite,
	"ci-contacts":  identity.PermContactWrite,
	"users":        identity.PermUserManage,
	"teams":        identity.PermUserManage,
	"clients":      identity.PermUserManage,
	"roles":        identity.PermRoleManage,
	"permissions":  identity.PermPermissionManage,
	"entitlements": identity.PermEntitlementManage,
	"webhooks":     identity.PermWebhookManage,
	"credentials":  identity.PermCredentialManage,
	"export":       identity.PermExportRun,
	"search":       identity.PermSearchWrite,
	// Audit integrity verification is a read-side operation; the audit trail
	// itself is append-only and written by the system, not the API.
	"audit": identity.PermAuditRead,
	// Topology is read-only; the graph is derived from CIs and relationships.
	"topology": identity.PermTopologyRead,
	"iga":      identity.PermIGAWrite,
	"scim":     identity.PermIGAWrite,
	"graphql":  identity.PermCIWrite,
	"me":       identity.PermPermissionRead,
	"ci-types": identity.PermCITypeManage,
	"api-keys": identity.PermAPIKeyManage,
	"ingest":   identity.PermDiscoveryIngest,
	// The AI assistant has a read-style permission only; conversations and
	// questions are protected by ai:read regardless of method.
	"ai": identity.PermAIRead,
}

// routeAccess describes how a route participates in authorization.
type routeAccess int

const (
	// routeUnmapped marks an /api/ route with no permission mapping. The
	// authorization middleware fails closed for these, and the router test
	// fails the build so the gap is fixed instead of shipped.
	routeUnmapped routeAccess = iota
	// routePublic marks routes that are intentionally reachable without an
	// authenticated principal (health, metrics, public auth endpoints).
	routePublic
	// routeProtected marks routes that require an authenticated principal
	// holding the resolved permission.
	routeProtected
)

// PermissionForRoute resolves the authorization requirement of an API route.
// routePublic is returned for the deliberately unauthenticated endpoints;
// routeProtected with the required permission for every mapped route; and
// routeUnmapped otherwise.
func PermissionForRoute(method, path string) (identity.Permission, routeAccess) {
	if !strings.HasPrefix(path, "/api/") {
		return "", routePublic
	}

	// Public authentication endpoints are unauthenticated by design.
	switch path {
	case "/api/v1/auth/config", "/api/v1/auth/callback", "/api/v1/auth/refresh":
		return "", routePublic
	}

	rest := strings.TrimPrefix(path, "/api/v1/")
	segments := strings.Split(strings.Trim(rest, "/"), "/")
	if len(segments) == 0 || segments[0] == "" {
		return "", routeUnmapped
	}

	resource := segments[0]
	switch resource {
	case "ingest":
		// Collector/agent ingest requires the ingest permission regardless
		// of method.
		return identity.PermDiscoveryIngest, routeProtected
	case "me":
		// /me/permissions is read-only self-service for any authenticated
		// principal.
		return identity.PermPermissionRead, routeProtected
	case "auth":
		// /auth/me requires authentication but no specific permission.
		return "", routeProtected
	}

	if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
		if read, ok := readPermissionFor[resource]; ok {
			return read, routeProtected
		}
		return "", routeUnmapped
	}

	if override, ok := writePermissionOverrides[resource]; ok {
		return override, routeProtected
	}
	if read, ok := readPermissionFor[resource]; ok {
		return writeFromRead(read), routeProtected
	}
	return "", routeUnmapped
}

// writeFromRead derives the write permission from a read permission key
// (resource:read -> resource:write).
func writeFromRead(read identity.Permission) identity.Permission {
	key := string(read)
	if strings.HasSuffix(key, ":read") {
		return identity.Permission(strings.TrimSuffix(key, ":read") + ":write")
	}
	return read
}

// AuthorizeRoute returns the middleware enforcing the given route's
// permission. It runs per route (registered via chi's With) after the auth
// middleware populated the principal; unmapped routes fail closed with 403.
func AuthorizeRoute(method, path string) func(http.Handler) http.Handler {
	required, access := PermissionForRoute(method, path)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch access {
			case routePublic:
				next.ServeHTTP(w, r)
				return
			case routeUnmapped:
				// Fail closed: a route that was never assigned a permission
				// must not silently stay reachable.
				api.WriteError(w, http.StatusForbidden, "Forbidden", "route has no permission mapping")
				return
			}

			principal, ok := identity.PrincipalFromContext(r.Context())
			if !ok {
				api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing authenticated principal")
				return
			}
			if required != "" && !principal.Has(required) {
				api.WriteError(w, http.StatusForbidden, "Forbidden", "missing required permission: "+string(required))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// authorizingRouter wraps a chi.Router so that every route registered through
// it is wrapped with its permission middleware. Handlers keep registering
// routes exactly as before; the wiring here is the single place that attaches
// authorization.
type authorizingRouter struct {
	chi.Router
	prefix string
}

func (a authorizingRouter) With(middlewares ...func(http.Handler) http.Handler) chi.Router {
	return authorizingRouter{Router: a.Router.With(middlewares...), prefix: a.prefix}
}

func (a authorizingRouter) fullPattern(pattern string) string {
	if a.prefix == "" {
		return pattern
	}
	return strings.TrimSuffix(a.prefix, "/") + "/" + strings.TrimPrefix(pattern, "/")
}

func (a authorizingRouter) handle(method, pattern string, h http.Handler) {
	a.Router.With(AuthorizeRoute(method, a.fullPattern(pattern))).Method(method, pattern, h)
}

func (a authorizingRouter) Method(method, pattern string, h http.Handler) {
	a.handle(method, pattern, h)
}
func (a authorizingRouter) Get(pattern string, h http.HandlerFunc) {
	a.handle(http.MethodGet, pattern, h)
}
func (a authorizingRouter) Post(pattern string, h http.HandlerFunc) {
	a.handle(http.MethodPost, pattern, h)
}
func (a authorizingRouter) Put(pattern string, h http.HandlerFunc) {
	a.handle(http.MethodPut, pattern, h)
}
func (a authorizingRouter) Patch(pattern string, h http.HandlerFunc) {
	a.handle(http.MethodPatch, pattern, h)
}
func (a authorizingRouter) Delete(pattern string, h http.HandlerFunc) {
	a.handle(http.MethodDelete, pattern, h)
}
func (a authorizingRouter) Head(pattern string, h http.HandlerFunc) {
	a.handle(http.MethodHead, pattern, h)
}
func (a authorizingRouter) Options(pattern string, h http.HandlerFunc) {
	a.handle(http.MethodOptions, pattern, h)
}
func (a authorizingRouter) Trace(pattern string, h http.HandlerFunc) {
	a.handle(http.MethodTrace, pattern, h)
}
func (a authorizingRouter) Connect(pattern string, h http.HandlerFunc) {
	a.handle(http.MethodConnect, pattern, h)
}

func (a authorizingRouter) Route(pattern string, fn func(r chi.Router)) chi.Router {
	return a.Router.Route(pattern, func(r chi.Router) {
		fn(authorizingRouter{Router: r, prefix: a.fullPattern(pattern)})
	})
}

// Mount is intentionally unsupported on the authorizing router: a mounted
// sub-router would bypass per-route permission enforcement. Domain handlers
// must register routes through Get/Post/... or Route so every route resolves
// to a permission.
func (a authorizingRouter) Mount(pattern string, h http.Handler) {
	panic(fmt.Sprintf("server: Mount(%q) on the authorizing router would bypass authorization; register routes explicitly", pattern))
}
