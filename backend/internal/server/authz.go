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
	"cis":                  identity.PermCIRead,
	"relationships":        identity.PermRelationshipRead,
	"topology":             identity.PermTopologyRead,
	"sites":                identity.PermSiteRead,
	"buildings":            identity.PermSiteRead,
	"rooms":                identity.PermSiteRead,
	"racks":                identity.PermRackRead,
	"rack-mounts":          identity.PermRackRead,
	"contacts":             identity.PermContactRead,
	"ci-contacts":          identity.PermContactRead,
	"collectors":           identity.PermDiscoveryRead,
	"discovery":            identity.PermDiscoveryRead,
	"assets":               identity.PermAssetRead,
	"asset-locations":      identity.PermAssetWrite,
	"assignments":          identity.PermAssignmentRead,
	"documents":            identity.PermDocumentRead,
	"stocktakes":           identity.PermStocktakeRead,
	"consumables":          identity.PermConsumableRead,
	"orders":               identity.PermOrderRead,
	"maintenance-windows":  identity.PermMaintenanceRead,
	"disposal-records":     identity.PermDisposalRead,
	"keys":                 identity.PermKeyRead,
	"trainings":            identity.PermTrainingRead,
	"training-assignments": identity.PermTrainingWrite,
	"desks":                identity.PermDeskRead,
	"desk-bookings":        identity.PermDeskRead,
	"tickets":              identity.PermTicketRead,
	"slas":                 identity.PermSLARead,
	"users":                identity.PermUserRead,
	"teams":                identity.PermUserRead,
	"clients":              identity.PermUserRead,
	"roles":                identity.PermRoleRead,
	"permissions":          identity.PermPermissionRead,
	"entitlements":         identity.PermEntitlementRead,
	"audit":                identity.PermAuditRead,
	"webhooks":             identity.PermWebhookRead,
	"credentials":          identity.PermCredentialRead,
	"export":               identity.PermCIRead,
	"ip-addresses":         identity.PermIPAMRead,
	"subnets":              identity.PermIPAMRead,
	"network-interfaces":   identity.PermIPAMRead,
	"cables":               identity.PermRackRead,
	"forms":                identity.PermFormRead,
	"form-submissions":     identity.PermFormRead,
	"workflows":            identity.PermWorkflowRead,
	"workflow-runs":        identity.PermWorkflowRead,
	"compliance":           identity.PermComplianceRead,
	"security":             identity.PermSecurityRead,
	"monitoring":           identity.PermMonitoringRead,
	// Privacy/DSGVO acts on other people's personal data; even the read-side
	// retention policy requires the manage permission.
	"privacy": identity.PermUserManage,
	"iga":     identity.PermIGARead,
	"scim":    identity.PermIGARead,
	"search":  identity.PermSearchRead,
	"ai":      identity.PermAIRead,
	// Enterprise CMDB + asset/inventory extension (spec §21). Read side;
	// writes are derived from the read→write suffix or overridden below.
	"ci-types":              identity.PermCIRead,
	"ci-fields":             identity.PermCIRead,
	"field-definitions":     identity.PermCIRead,
	"relationship-types":    identity.PermRelationshipRead,
	"lifecycle-definitions": identity.PermAssetRead,
	"lifecycle-transitions": identity.PermAssetRead,
	"locations":             identity.PermSiteRead,
	"stock-movements":       identity.PermAssetRead,
	"inventory":             identity.PermAssetRead,
	"movements":             identity.PermAssetRead,
	"reservations":          identity.PermAssetRead,
	"children":              identity.PermAssetRead,
	"compositions":          identity.PermAssetRead,
	"fields":                identity.PermCIRead,
	"override":              identity.PermCIRead,
	"reconciliation":        identity.PermDiscoveryRead,
	"source-policy":         identity.PermDiscoveryRead,
	"history":               identity.PermAuditRead,
	"saved-views":           identity.PermSavedViewRead,
	"dependencies":          identity.PermTopologyRead,
	"blast-radius":          identity.PermTopologyRead,
	"state":                 identity.PermCIRead,
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
	"me":       identity.PermPermissionRead,
	"ci-types": identity.PermCITypeManageNew,
	"api-keys": identity.PermAPIKeyManage,
	"ingest":   identity.PermDiscoveryIngest,
	// Enterprise CMDB + asset/inventory extension write side (spec §21).
	"ci-fields":             identity.PermCIAttributeManage,
	"field-definitions":     identity.PermCIInstanceAttributeManage,
	"relationship-types":    identity.PermRelationshipTypeManage,
	"lifecycle-definitions": identity.PermLifecycleManage,
	"lifecycle-transitions": identity.PermLifecycleManage,
	"locations":             identity.PermSiteWrite,
	"stock-movements":       identity.PermAssetMove,
	"inventory":             identity.PermInventoryManage,
	"movements":             identity.PermAssetMove,
	"reservations":          identity.PermAssetReserve,
	"children":              identity.PermAssetWrite,
	"compositions":          identity.PermAssetWrite,
	"override":              identity.PermOverrideWrite,
	"source-policy":         identity.PermReconciliationResolve,
	"saved-views":           identity.PermSavedViewWrite,
	// Read-only surfaces: writes (if any) stay on the read permission so no
	// non-existent derived write permission is required.
	"history":      identity.PermAuditRead,
	"dependencies": identity.PermTopologyRead,
	"blast-radius": identity.PermTopologyRead,
	// The AI assistant has a read-style permission only; conversations and
	// questions are protected by ai:read regardless of method.
	"ai": identity.PermAIRead,
	// Privacy/DSGVO: retention configuration and the erasure workflow act on
	// other people's personal data, so every method requires user:manage.
	"privacy": identity.PermUserManage,
}

// routeRule maps one operation to its permission ahead of the resource
// tables. Path segments of pattern are literals or "*" for a path parameter;
// method "*" matches every method. Rules cover actions and nested
// sub-resources whose permission differs from the one of the first path
// segment (RBA-04, RBA-06), so a nested route cannot borrow the broader right
// of its parent resource.
type routeRule struct {
	method     string
	pattern    string
	permission identity.Permission
}

// routeRules are checked in order; the first match wins.
var routeRules = []routeRule{
	// Deleting a CI is its own right; ci:write does not delete.
	{http.MethodDelete, "cis/*", identity.PermCIDelete},
	// Instance attributes (MET-14, CI-10) and manual overrides (CI-10).
	{http.MethodPut, "cis/*/field-definitions", identity.PermCIInstanceAttributeManage},
	{http.MethodDelete, "cis/*/field-definitions/*", identity.PermCIInstanceAttributeManage},
	{"*", "cis/*/fields/*/override", identity.PermOverrideWrite},
	// Lifecycle transitions of CIs and assets (RBA-06).
	{http.MethodPost, "cis/*/lifecycle-transitions", identity.PermLifecycleTransition},
	{http.MethodPost, "assets/*/lifecycle-transitions", identity.PermLifecycleTransition},
	// Nested sub-resources are protected by their own resource's rights.
	{http.MethodGet, "cis/*/contacts", identity.PermContactRead},
	{http.MethodPost, "cis/*/contacts", identity.PermContactWrite},
	{http.MethodGet, "cis/*/interfaces", identity.PermIPAMRead},
	{http.MethodPost, "cis/*/interfaces", identity.PermIPAMWrite},
	{http.MethodGet, "cis/*/relationships", identity.PermRelationshipRead},
	{http.MethodGet, "cis/*/dependencies", identity.PermTopologyRead},
	{http.MethodGet, "cis/*/blast-radius", identity.PermTopologyRead},
	// Approving or rejecting an order is not editing it.
	{http.MethodPost, "orders/*/approve", identity.PermOrderApprove},
	{http.MethodPost, "orders/*/reject", identity.PermOrderApprove},
	// Reconciliation settings (RBA-06); reading them stays discovery:read.
	{http.MethodPut, "reconciliation/source-policy", identity.PermReconciliationManage},
}

// matchRouteRule returns the permission of the first rule matching the
// method and the path segments below /api/v1/.
func matchRouteRule(method string, segments []string) (identity.Permission, bool) {
	for _, rule := range routeRules {
		if rule.method != "*" && rule.method != method {
			continue
		}
		parts := strings.Split(rule.pattern, "/")
		if len(parts) != len(segments) {
			continue
		}
		matched := true
		for i, part := range parts {
			if part != "*" && part != segments[i] {
				matched = false
				break
			}
		}
		if matched {
			return rule.permission, true
		}
	}
	return "", false
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
	// SCIM is authenticated provisioning surface, not public.
	if strings.HasPrefix(path, "/scim/") {
		if method == http.MethodGet || method == http.MethodHead {
			return identity.PermIGARead, routeProtected
		}
		return identity.PermIGAWrite, routeProtected
	}

	if !strings.HasPrefix(path, "/api/") {
		return "", routePublic
	}

	// Public authentication endpoints are unauthenticated by design.
	switch path {
	case "/api/v1/auth/config", "/api/v1/auth/callback", "/api/v1/auth/refresh", "/api/v1/auth/logout":
		return "", routePublic
	case "/api/v1/collectors/enroll":
		// Zero-config onboarding: the single-use enrollment code is the
		// credential; the collector has no bearer token before enrolling.
		return "", routePublic
	}

	rest := strings.TrimPrefix(path, "/api/v1/")
	segments := strings.Split(strings.Trim(rest, "/"), "/")
	if len(segments) == 0 || segments[0] == "" {
		return "", routeUnmapped
	}

	if permission, ok := matchRouteRule(method, segments); ok {
		return permission, routeProtected
	}

	resource := segments[0]
	switch resource {
	case "ingest":
		// Collector/agent ingest requires the ingest permission regardless
		// of method.
		return identity.PermDiscoveryIngest, routeProtected
	case "agents":
		// Agent telemetry ingest has its own permission; management routes
		// (policy, kill-switch) use agent:manage.
		if len(segments) > 1 && segments[1] == "telemetry" {
			return identity.PermAgentIngest, routeProtected
		}
		if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
			return identity.PermAgentRead, routeProtected
		}
		return identity.PermAgentManage, routeProtected
	case "me":
		// /me/permissions is read-only self-service for any authenticated
		// principal.
		return identity.PermPermissionRead, routeProtected
	case "auth":
		// /auth/me requires authentication but no specific permission.
		return "", routeProtected
	case "graphql":
		// The GraphQL BFF only reads; each resolver checks its own read
		// permission and entitlement (GQL-04), so the route requires an
		// authenticated principal only.
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
	// after runs behind the route authorization, in order (API-04:
	// Authz -> Validation -> Idempotency -> Handler).
	after []func(http.Handler) http.Handler
}

func (a authorizingRouter) With(middlewares ...func(http.Handler) http.Handler) chi.Router {
	return authorizingRouter{Router: a.Router.With(middlewares...), prefix: a.prefix, after: a.after}
}

func (a authorizingRouter) fullPattern(pattern string) string {
	if a.prefix == "" {
		return pattern
	}
	return strings.TrimSuffix(a.prefix, "/") + "/" + strings.TrimPrefix(pattern, "/")
}

func (a authorizingRouter) handle(method, pattern string, h http.Handler) {
	chain := append([]func(http.Handler) http.Handler{AuthorizeRoute(method, a.fullPattern(pattern))}, a.after...)
	a.Router.With(chain...).Method(method, pattern, h)
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
		fn(authorizingRouter{Router: r, prefix: a.fullPattern(pattern), after: a.after})
	})
}

// Mount is intentionally unsupported on the authorizing router: a mounted
// sub-router would bypass per-route permission enforcement. Domain handlers
// must register routes through Get/Post/... or Route so every route resolves
// to a permission.
func (a authorizingRouter) Mount(pattern string, h http.Handler) {
	panic(fmt.Sprintf("server: Mount(%q) on the authorizing router would bypass authorization; register routes explicitly", pattern))
}
