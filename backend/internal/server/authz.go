package server

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/go-chi/chi/v5"
)

// readPermissionFor maps a resource segment to the permission required to
// read it. The table mirrors the canonical permission catalog in
// internal/permission; the write permission is derived by replacing the
// ":read" suffix with ":write".
var readPermissionFor = map[string]identity.Permission{
	"cis": identity.PermCIRead,
	// Listing API keys shows metadata only, but it is still key management.
	"api-keys":             identity.PermAPIKeyManage,
	"relationships":        identity.PermRelationshipRead,
	"topology":             identity.PermTopologyRead,
	"sites":                identity.PermLocationRead,
	"buildings":            identity.PermLocationRead,
	"rooms":                identity.PermLocationRead,
	"racks":                identity.PermRackRead,
	"rack-mounts":          identity.PermRackRead,
	"mounts":               identity.PermRackRead,
	"contacts":             identity.PermContactRead,
	"ci-contacts":          identity.PermContactRead,
	"collectors":           identity.PermDiscoveryRead,
	"discovery":            identity.PermDiscoveryRead,
	"discovery-scopes":     identity.PermDiscoveryRead,
	"review-items":         identity.PermDiscoveryRead,
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
	"service-accounts":     identity.PermUserRead,
	// Invitations name addresses and grant roles: read and write need
	// user:manage (TLC-03).
	"invitations": identity.PermUserManage,
	"teams":       identity.PermUserRead,
	"clients":     identity.PermUserRead,
	// The organization is the root of the location hierarchy (LOC-01).
	"org":          identity.PermLocationRead,
	"roles":        identity.PermRoleRead,
	"permissions":  identity.PermPermissionRead,
	"entitlements": identity.PermEntitlementRead,
	"audit":        identity.PermAuditRead,
	"audit-log":    identity.PermAuditRead,
	// Catalog paths of API-05.
	"impact":           identity.PermTopologyRead,
	"exports":          identity.PermCIRead,
	"role-assignments": identity.PermRoleRead,
	"webhooks":         identity.PermWebhookRead,
	// Notification channels and their deliveries are administered like
	// webhooks (NTF-01, NTF-04).
	"notification-channels":   identity.PermWebhookRead,
	"notification-deliveries": identity.PermWebhookRead,
	"credentials":             identity.PermCredentialRead,
	"export":                  identity.PermCIRead,
	"ip-addresses":            identity.PermIPAMRead,
	"ips":                     identity.PermIPAMRead,
	"subnets":                 identity.PermIPAMRead,
	"vlans":                   identity.PermIPAMRead,
	"vrfs":                    identity.PermIPAMRead,
	"network-interfaces":      identity.PermIPAMRead,
	"cables":                  identity.PermRackRead,
	"forms":                   identity.PermFormRead,
	"form-submissions":        identity.PermFormRead,
	"workflows":               identity.PermWorkflowRead,
	"workflow-runs":           identity.PermWorkflowRead,
	"compliance":              identity.PermComplianceRead,
	"security":                identity.PermSecurityRead,
	"monitoring":              identity.PermMonitoringRead,
	// Privacy/DSGVO acts on other people's personal data; even the read-side
	// retention policy requires the manage permission.
	"privacy": identity.PermUserManage,
	"iga":     identity.PermIGARead,
	"scim":    identity.PermIGARead,
	"search":  identity.PermSearchRead,
	"ai":      identity.PermAIRead,
	// Enterprise CMDB + asset/inventory extension (spec §21). Read side;
	// writes are derived from the read→write suffix or overridden below.
	"ci-types":  identity.PermCIRead,
	"ci-fields": identity.PermCIRead,
	// attribute_definition (MET-10, API-05).
	"attribute-definitions": identity.PermCIRead,
	"field-definitions":     identity.PermCIRead,
	"relationship-types":    identity.PermRelationshipRead,
	"lifecycle-definitions": identity.PermAssetRead,
	"lifecycle-transitions": identity.PermAssetRead,
	"locations":             identity.PermLocationRead,
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
	// VRFs are managed with their own key (TEN-10, RBA-01).
	"vrfs": identity.PermVRFManage,
	// Channels hold provider credentials; deliveries are only read.
	"notification-channels":   identity.PermWebhookManage,
	"notification-deliveries": identity.PermWebhookManage,
	// Review items are only written by resolving them (REC-08).
	"review-items": identity.PermReviewResolve,

	"sites":     identity.PermLocationWrite,
	"buildings": identity.PermLocationWrite,
	"rooms":     identity.PermLocationWrite,

	"contacts":    identity.PermContactWrite,
	"ci-contacts": identity.PermContactWrite,
	"users":       identity.PermUserManage,
	// Service accounts are principals like users (RBA-08).
	"service-accounts": identity.PermUserManage,
	"invitations":      identity.PermUserManage,
	"teams":            identity.PermTeamManage,
	// RBA-01: collectors and discovery jobs have their own manage keys.
	"collectors":       identity.PermCollectorManage,
	"discovery":        identity.PermDiscoveryManage,
	"discovery-scopes": identity.PermDiscoveryManage,
	// Resolving reconciliation conflicts is resolving review items.
	"reconciliation": identity.PermReviewResolve,
	"clients":        identity.PermUserManage,
	"org":            identity.PermUserManage,
	"roles":          identity.PermRoleManage,
	"permissions":    identity.PermPermissionManage,
	"entitlements":   identity.PermEntitlementManage, // no tenant write route: the operator writes (ENT-04)
	"webhooks":       identity.PermWebhookManage,
	"credentials":    identity.PermCredentialManage,
	"export":         identity.PermExportRun,
	"search":         identity.PermSearchWrite,
	// Audit integrity verification is a read-side operation; the audit trail
	// itself is append-only and written by the system, not the API.
	"audit":     identity.PermAuditRead,
	"audit-log": identity.PermAuditRead,
	"impact":    identity.PermTopologyRead,
	"exports":   identity.PermExportRun,
	// Assigning a role is managing roles (RBA-01).
	"role-assignments": identity.PermRoleManage,
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
	"attribute-definitions": identity.PermCIAttributeManage,
	"field-definitions":     identity.PermCIInstanceAttributeManage,
	"relationship-types":    identity.PermRelationshipTypeManage,
	"lifecycle-definitions": identity.PermLifecycleManage,
	"lifecycle-transitions": identity.PermLifecycleManage,
	"locations":             identity.PermLocationWrite,
	"stock-movements":       identity.PermAssetMove,
	"inventory":             identity.PermInventoryManage,
	"movements":             identity.PermAssetMove,
	"reservations":          identity.PermAssetReserve,
	"children":              identity.PermAssetWrite,
	"compositions":          identity.PermAssetWrite,
	"override":              identity.PermOverrideWrite,
	"source-policy":         identity.PermReconciliationManage,
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
	// Deleting a CI is its own right; ci:write does not delete. Restoring
	// undoes a deletion and needs the same right (CI-05).
	{http.MethodDelete, "cis/*", identity.PermCIDelete},
	{http.MethodPost, "cis/*/restore", identity.PermCIDelete},
	// A merge deletes the victim (CI-12).
	{http.MethodPost, "cis/*/merge", identity.PermCIDelete},
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
	// Collectors report with the ingest credential; managing them is
	// collector:manage (RBA-01).
	{http.MethodPost, "collectors/*/heartbeat", identity.PermDiscoveryIngest},
	{http.MethodPost, "collectors/*/certificate", identity.PermDiscoveryIngest},
	{http.MethodPost, "discovery/ingest", identity.PermDiscoveryIngest},
	// Resolving a review item is its own right (RBA-01, RBA-02).
	{http.MethodPost, "discovery/review-items/*/resolve", identity.PermReviewResolve},
	{http.MethodPost, "review-items/*/resolve", identity.PermReviewResolve},
	// Reading jobs (RBA-01); starting an export stays export:run.
	{http.MethodGet, "export/jobs", identity.PermJobRead},
	{http.MethodGet, "export/jobs/*", identity.PermJobRead},
	{http.MethodGet, "exports", identity.PermJobRead},
	{http.MethodGet, "exports/*", identity.PermJobRead},
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
	// routeOperator marks the operator path /api/v1/admin (SEC-07): the
	// tenant authorization does not apply; internal/operator admits
	// operators only and audits every request.
	routeOperator
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
	if strings.HasPrefix(path, "/api/v1/admin/") {
		return "", routeOperator
	}

	// Public authentication endpoints are unauthenticated by design.
	switch path {
	case "/api/v1/auth/config", "/api/v1/auth/callback", "/api/v1/auth/refresh", "/api/v1/auth/logout":
		return "", routePublic
	case "/api/v1/collectors/enroll":
		// Zero-config onboarding: the single-use enrollment code is the
		// credential; the collector has no bearer token before enrolling.
		return "", routePublic
	case "/api/v1/invitations/accept":
		// The invitation token is the credential; the invited person has no
		// account yet (TLC-03).
		return "", routePublic
	case "/api/v1/signup", "/api/v1/signup/verify", "/api/v1/signup/challenge":
		// Self-signup (TLC-01): no account exists yet; the verification
		// token is the credential.
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
		// /me is the signed-in principal itself and needs no permission;
		// /me/permissions is read-only self-service.
		if len(segments) == 1 {
			return "", routeProtected
		}
		return identity.PermPermissionRead, routeProtected
	case "auth":
		// /auth/me requires authentication but no specific permission.
		return "", routeProtected
	case "notifications":
		// The notification center serves the signed-in user's own inbox
		// and preferences (NTF-04).
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
			case routePublic, routeOperator:
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
	// auditor stores the audit entry of each successful write (AUD-07).
	auditor middleware.RouteAuditor
}

func (a authorizingRouter) With(middlewares ...func(http.Handler) http.Handler) chi.Router {
	return authorizingRouter{Router: a.Router.With(middlewares...), prefix: a.prefix, after: a.after, auditor: a.auditor}
}

// auditExemptions are the write routes that write no audit entry of their
// own, with the reason (AUD-07, WP-133). Every other write route records
// one entry per successful request; audit_coverage_test.go keeps this list
// and the route table in step.
var auditExemptions = map[string]string{
	"POST /api/v1/audit/verify": "reads the chain",
	"POST /api/v1/search/query": "a search, reads",
	"POST /api/v1/ai/ask":       "a question to the assistant, reads",
	"POST /api/v1/graphql":      "the BFF refuses mutations, reads",
	// Marking one's own notifications read changes no shared state.
	"POST /api/v1/notifications/{id}/read": "marks the own inbox read",
	"POST /api/v1/notifications/read-all":  "marks the own inbox read",
	// No tenant context: sign-in, sign-up and enrollment precede the
	// organization or the session; the identity provider logs sign-ins.
	"POST /api/v1/auth/callback":      "sign-in, no tenant context",
	"POST /api/v1/auth/logout":        "sign-out, no tenant context",
	"POST /api/v1/auth/refresh":       "session refresh, no tenant context",
	"POST /api/v1/signup":             "self-signup before the organization exists",
	"POST /api/v1/signup/verify":      "self-signup before the organization exists",
	"POST /api/v1/collectors/enroll":  "enrollment code exchange, no tenant context",
	"POST /api/v1/invitations/accept": "public acceptance link, no tenant context",
	// The operator path has its own chain (operator_audit, SEC-07).
	"POST /api/v1/admin/auth/callback":          "operator path, operator_audit",
	"POST /api/v1/admin/orgs":                   "operator path, operator_audit",
	"POST /api/v1/admin/orgs/{id}/entitlements": "operator path, operator_audit",
	// Measurements and heartbeats are not history (AUD-03); the CI changes
	// of an ingest are audited by the CI repository with source collector.
	"POST /api/v1/agents/telemetry":          "measurements; CI changes audited by the CI repository",
	"POST /api/v1/agents/{id}/heartbeat":     "heartbeat",
	"POST /api/v1/collectors/{id}/heartbeat": "heartbeat",
	"POST /api/v1/monitoring/metrics":        "measurements",
	"POST /api/v1/discovery/ingest":          "ingest; CI changes audited by the CI repository",
	"POST /api/v1/ingest/bulk":               "ingest; CI changes audited by the CI repository",
}

// collectorRoutes are the write routes whose changes come from collectors
// and agents (source collector).
var collectorRoutes = map[string]bool{
	"POST /api/v1/agents/telemetry": true, "POST /api/v1/agents/{id}/heartbeat": true,
	"POST /api/v1/collectors/{id}/heartbeat": true, "POST /api/v1/discovery/ingest": true,
	"POST /api/v1/collectors/{id}/certificate": true,
	"POST /api/v1/ingest/bulk":                 true,
}

// auditedRoutes records, per write route registered through an
// authorizingRouter, whether it writes an audit entry (coverage test).
var auditedRoutes sync.Map

func isWriteMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func (a authorizingRouter) fullPattern(pattern string) string {
	if a.prefix == "" {
		return pattern
	}
	return strings.TrimSuffix(a.prefix, "/") + "/" + strings.TrimPrefix(pattern, "/")
}

func (a authorizingRouter) handle(method, pattern string, h http.Handler) {
	full := a.fullPattern(pattern)
	chain := append([]func(http.Handler) http.Handler{AuthorizeRoute(method, full)}, a.after...)
	if isWriteMethod(method) {
		// Behind idempotency: a replayed response writes no second entry.
		key := method + " " + full
		_, exempt := auditExemptions[key]
		source := ""
		if collectorRoutes[key] {
			source = audit.SourceCollector
		}
		chain = append(chain, middleware.AuditRoute(method, full, a.auditor, !exempt, source))
		auditedRoutes.Store(key, !exempt)
	}
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
		fn(authorizingRouter{Router: r, prefix: a.fullPattern(pattern), after: a.after, auditor: a.auditor})
	})
}

// Mount is intentionally unsupported on the authorizing router: a mounted
// sub-router would bypass per-route permission enforcement. Domain handlers
// must register routes through Get/Post/... or Route so every route resolves
// to a permission.
func (a authorizingRouter) Mount(pattern string, h http.Handler) {
	panic(fmt.Sprintf("server: Mount(%q) on the authorizing router would bypass authorization; register routes explicitly", pattern))
}
