package server

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/metamodel"
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
	"consumables":          identity.PermStockRead, // RBA-05, E-106: stock:* replaces consumable:*
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
	"business-calendars":   identity.PermSLARead,      // business calendars of the SLA clock (WP-768); writes sla:write
	"import-profiles":      identity.PermImportRun,    // import mapping profiles (WP-774, E-106); writes import:manage
	"imports":              identity.PermImportRun,    // import runs (WP-301): read, start and preview need import:run
	"tags":                 identity.PermCIRead,       // tag catalog (CI-08, WP-753, E-106); writes tag:manage
	"suppliers":            identity.PermContractRead, // suppliers (MGT-12, WP-736, E-106); writes contract:write
	"contracts":            identity.PermContractRead, // contracts (MGT-12, WP-269, E-106); writes contract:write
	"users":                identity.PermUserRead,
	"service-accounts":     identity.PermUserRead,
	// Field permissions (RBA-07, WP-726, E-106): reading the configuration
	// is managing it, like api-keys; only org_admin holds the key.
	"field-permissions": identity.PermFieldPermissionManage,
	// Invitations name addresses and grant roles: read and write need
	// user:manage (TLC-03).
	"invitations": identity.PermUserManage,
	"teams":       identity.PermUserRead,
	// Every standard role with a client reference holds location:read
	// (RBA-02); RLS limits the list to the clients of the scope, and the
	// scope switcher offers them (UI-01, F-373). Writes stay user:manage.
	"clients": identity.PermLocationRead,
	// The organization is the root of the location hierarchy (LOC-01).
	"org":          identity.PermLocationRead,
	"roles":        identity.PermRoleRead,
	"permissions":  identity.PermPermissionRead,
	"entitlements": identity.PermEntitlementRead,
	// The modules of this release (WP-214) steer the navigation like the
	// entitlements.
	"modules":   identity.PermEntitlementRead,
	"audit":     identity.PermAuditRead,
	"audit-log": identity.PermAuditRead,
	// Catalog paths of API-05.
	"impact":  identity.PermTopologyRead,
	"exports": identity.PermCIRead,
	// Jobs (API-09): cancel and revert check the permission of the kind.
	"jobs":             identity.PermJobRead,
	"role-assignments": identity.PermRoleRead,
	"webhooks":         identity.PermWebhookRead,
	// Notification channels hold provider credentials and their deliveries
	// name recipients: reading needs notification:manage like writing
	// (RBA-01, RBA-02: org_admin only; NTF-01). Channels and deliveries
	// belong to the whole organization: the handler refuses a holder bound
	// to a client, site or team with 403 (RBA-03, F-418). The own inbox
	// below /notifications is self-service.
	"notification-channels":   identity.PermNotificationManage,
	"notification-deliveries": identity.PermNotificationManage,
	"credentials":             identity.PermCredentialRead,
	// Credential bindings (CRD-01, WP-890): read with credential:read.
	"credential-bindings": identity.PermCredentialRead,
	"export":              identity.PermCIRead,
	"ip-addresses":        identity.PermIPAMRead,
	"ips":                 identity.PermIPAMRead,
	"subnets":             identity.PermIPAMRead,
	"vlans":               identity.PermIPAMRead,
	"vrfs":                identity.PermIPAMRead,
	"network-interfaces":  identity.PermIPAMRead,
	"cables":              identity.PermRackRead,
	"forms":               identity.PermFormRead,
	"form-submissions":    identity.PermFormRead,
	"workflows":           identity.PermWorkflowRead,
	"workflow-runs":       identity.PermWorkflowRead,
	"compliance":          identity.PermComplianceRead,
	"security":            identity.PermFindingRead,
	// Phase 3 (WP-866, E-160, E-161): IPAM/ASN and ADMP. The writes of ipam
	// follow the suffix convention (ipam:write); the ADMP accounts get
	// admp:manage with WP-925.
	"ipam":                     identity.PermIPAMRead,
	"ipam-sections":            identity.PermIPAMRead,
	"asns":                     identity.PermIPAMRead,
	"asn-ranges":               identity.PermIPAMRead,
	"bgp-peerings":             identity.PermIPAMRead,
	"nat-translations":         identity.PermIPAMRead,
	"admp":                     identity.PermADMPRead,
	"escalation-policies":      identity.PermEscalationManage,
	"notification-escalations": identity.PermEscalationManage,
	"monitoring":               identity.PermMonitoringRead,
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
	"lifecycle-models":      identity.PermAssetRead,    // E-38 successor of lifecycle-definitions (E-106)
	"templates":             identity.PermCIRead,       // metamodel templates read like ci-types (E-38, E-106)
	"attribute-sets":        identity.PermCIRead,       // attribute sets read like ci-types (WP-250, E-106)
	"rules":                 identity.PermCIRead,       // rule catalog reads like ci-types (WP-716, E-106)
	"layouts":               identity.PermCIRead,       // form layouts read like ci-types (WP-259, E-106)
	"metamodel":             identity.PermCITypeManage, // drafts are unpublished admin work: reading needs citype:manage (WP-260, E-106)
	"locations":             identity.PermLocationRead,
	"stock-movements":       identity.PermAssetRead,
	"inventory":             identity.PermStockRead, // inventory items and availability are stock (E-106)
	"movements":             identity.PermAssetRead,
	"reservations":          identity.PermReservationRead,
	"children":              identity.PermAssetRead,
	"compositions":          identity.PermAssetRead,
	"fields":                identity.PermCIRead,
	"override":              identity.PermCIRead,
	"reconciliation":        identity.PermDiscoveryRead,
	"source-policy":         identity.PermDiscoveryRead,
	"history":               identity.PermCIRead, // minimum; the handler checks the entity type and audit:read (CI-06, E-78)
	"saved-views":           identity.PermSavedViewRead,
	"dependencies":          identity.PermTopologyRead,
	"blast-radius":          identity.PermTopologyRead,
	"state":                 identity.PermCIRead,

	// Suppressions of removed discovery edges (F-273): listing reads
	// relationships; lifting one derives relationship:write, the right of
	// the forced DELETE /relationships/{id} that creates it.
	"relationship-suppressions": identity.PermRelationshipRead,
}

// writePermissionOverrides covers resources whose write permission does not
// follow the read→write suffix convention.
var writePermissionOverrides = map[string]identity.Permission{
	// VRFs are managed with their own key (TEN-10, RBA-01).
	"vrfs": identity.PermVRFManage,
	// Notification channels have their own key, not webhook:manage (RBA-01).
	"notification-channels":   identity.PermNotificationManage,
	"notification-deliveries": identity.PermNotificationManage,
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
	"permissions":    identity.PermRoleManage,        // no write route: role grants are replaced below /roles
	"entitlements":   identity.PermEntitlementManage, // no tenant write route: the operator writes (ENT-04)
	"modules":        identity.PermEntitlementManage, // no write route: RETICORA_MODULES sets them (WP-214)
	"webhooks":       identity.PermWebhookManage,
	"credentials":    identity.PermCredentialManage,
	// Binding a credential is its own right (WP-870, WP-890).
	"credential-bindings": identity.PermCredentialBind,
	// The only write below /admp is the account management of WP-925 with
	// admp:manage (WP-870); until then no write route exists and writes
	// need the org_admin key entitlement:manage (fail-closed).
	"admp": identity.PermEntitlementManage,
	// Escalation chains are read and written with one key (NTF-02, E-161).
	"escalation-policies":      identity.PermEscalationManage,
	"notification-escalations": identity.PermEscalationManage,
	"export":                   identity.PermExportRun,
	"search":                   identity.PermSearchWrite,
	// Audit integrity verification is a read-side operation; the audit trail
	// itself is append-only and written by the system, not the API.
	"audit":     identity.PermAuditRead,
	"audit-log": identity.PermAuditRead,
	"impact":    identity.PermTopologyRead,
	"exports":   identity.PermExportRun,
	"jobs":      identity.PermJobRead,
	// Assigning a role is managing roles (RBA-01).
	"role-assignments": identity.PermRoleManage,
	// Topology is read-only; the graph is derived from CIs and relationships.
	"topology": identity.PermTopologyRead,
	"iga":      identity.PermIGAWrite,
	"scim":     identity.PermIGAWrite,
	"me":       identity.PermPermissionRead,
	// CI types are managed with the RBA-01 key.
	"ci-types": identity.PermCITypeManage,
	"api-keys": identity.PermAPIKeyManage,
	"ingest":   identity.PermDiscoveryIngest,
	// Field grants, object grants and sensitive fields (WP-726).
	"field-permissions": identity.PermFieldPermissionManage,
	// Enterprise CMDB + asset/inventory extension write side (spec §21).
	"ci-fields":             identity.PermCIAttributeManage,
	"attribute-definitions": identity.PermCIAttributeManage,
	"field-definitions":     identity.PermCIInstanceAttributeManage,
	"relationship-types":    identity.PermRelationshipTypeManage,
	"lifecycle-definitions": identity.PermLifecycleManage,
	"lifecycle-transitions": identity.PermLifecycleManage,
	"lifecycle-models":      identity.PermLifecycleManage,
	"templates":             identity.PermCITypeManage, // metamodel resources write like ci-types (E-106)
	"attribute-sets":        identity.PermCITypeManage, // like ci-types (WP-250, E-106)
	"rules":                 identity.PermCITypeManage, // rule catalog writes like ci-types (WP-716, E-106)
	"layouts":               identity.PermCITypeManage, // form layouts write like ci-types (WP-259, E-106)
	"metamodel":             identity.PermCITypeManage, // drafts (WP-260); test and publish map their own rules (WP-721, WP-261)
	"import-profiles":       identity.PermImportManage, // only org_admin manages profiles, engineer runs imports (WP-774, E-106)
	"tags":                  identity.PermTagManage,    // the tag catalog and its merge (CI-08, WP-753, E-106)
	"locations":             identity.PermLocationWrite,
	"stock-movements":       identity.PermStockWrite, // quantity bookings (E-38); an asset transfer also needs asset:move in the handler (E-142, WP-756)
	"inventory":             identity.PermStockWrite, // former inventory:manage, translated by 000192
	"movements":             identity.PermAssetMove,
	"reservations":          identity.PermReservationWrite,
	"children":              identity.PermAssetWrite,
	"compositions":          identity.PermAssetWrite,
	"override":              identity.PermOverrideWrite,
	"source-policy":         identity.PermReconciliationManage,
	"saved-views":           identity.PermSavedViewWrite,
	// Read-only surfaces: writes (if any) stay on the read permission so no
	// non-existent derived write permission is required. The state snapshot
	// job (POST /history/snapshots) stays on audit:read.
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
	// Acknowledging alarms, incidents and notification escalations is its
	// own right (RBA-05, E-161, WP-866).
	{http.MethodPost, "monitoring/alarms/*/ack", identity.PermAlertAcknowledge},
	{http.MethodPost, "monitoring/alarms/*/unack", identity.PermAlertAcknowledge},
	{http.MethodPost, "monitoring/incidents/*/ack", identity.PermAlertAcknowledge},
	{http.MethodPost, "notification-escalations/*/acknowledge", identity.PermAlertAcknowledge},
	// Deleting a CI is its own right; ci:write does not delete. Restoring
	// undoes a deletion and needs the same right (CI-05).
	{http.MethodDelete, "cis/*", identity.PermCIDelete},
	{http.MethodPost, "cis/*/restore", identity.PermCIDelete},
	// A merge deletes the victim, the unmerge restores it (CI-12).
	{http.MethodPost, "cis/*/merge", identity.PermCIDelete},
	{http.MethodPost, "cis/*/unmerge", identity.PermCIDelete},
	// Instance attributes (MET-14, CI-10) and manual overrides (CI-10).
	{http.MethodPut, "cis/*/field-definitions", identity.PermCIInstanceAttributeManage},
	{http.MethodDelete, "cis/*/field-definitions/*", identity.PermCIInstanceAttributeManage},
	// Instance attribute values have their own right (MET-14, WP-256);
	// reading them is ci:read through the segment cis.
	{http.MethodPut, "cis/*/instance-attributes/*", identity.PermCIInstanceAttributeManage},
	{http.MethodDelete, "cis/*/instance-attributes/*", identity.PermCIInstanceAttributeManage},
	{"*", "cis/*/fields/*/override", identity.PermOverrideWrite},
	// Lifecycle transitions of CIs and assets (RBA-06).
	{http.MethodPost, "cis/*/lifecycle-transitions", identity.PermLifecycleTransition},
	{http.MethodPost, "assets/*/lifecycle-transitions", identity.PermLifecycleTransition},
	// Nested sub-resources are protected by their own resource's rights.
	// The credential bindings of a CI (CRD-01, WP-890).
	{http.MethodGet, "cis/*/credentials", identity.PermCredentialRead},
	{http.MethodPut, "cis/*/credentials", identity.PermCredentialBind},
	// The effective credentials of a CI (CRD-04, WP-891): references only.
	{http.MethodGet, "cis/*/credentials/effective", identity.PermCredentialRead},
	{http.MethodGet, "cis/*/contacts", identity.PermContactRead},
	{http.MethodPost, "cis/*/contacts", identity.PermContactWrite},
	{http.MethodGet, "cis/*/interfaces", identity.PermIPAMRead},
	{http.MethodPost, "cis/*/interfaces", identity.PermIPAMWrite},
	{http.MethodGet, "cis/*/relationships", identity.PermRelationshipRead},
	{http.MethodGet, "cis/*/dependencies", identity.PermTopologyRead},
	{http.MethodGet, "cis/*/blast-radius", identity.PermTopologyRead},
	// The attachments of a ticket are documents (WP-765); linking them
	// stays POST /documents/{id}/links.
	{http.MethodGet, "tickets/*/attachments", identity.PermDocumentRead},
	// Completing a stocktake is its own action (E-106); holders of
	// stocktake:write received stocktake:complete with migration 000192.
	{http.MethodPost, "stocktakes/*/complete", identity.PermStocktakeComplete},
	// Action paths of phase 2 below assets (E-106): handing out, taking
	// back and transferring an asset is an assignment (WP-275), reserving it
	// a reservation (WP-277); without a rule they would derive asset:write.
	{http.MethodPost, "assets/*/assign", identity.PermAssignmentWrite},
	{http.MethodPost, "assets/*/return", identity.PermAssignmentWrite},
	{http.MethodPost, "assets/*/transfer", identity.PermAssignmentWrite},
	{http.MethodPost, "assets/*/reserve", identity.PermReservationWrite},
	// Converting a reservation assigns its asset (WP-277), like POST
	// /assignments; without a rule it would derive reservation:write.
	{http.MethodPost, "reservations/*/convert", identity.PermAssignmentWrite},
	// Linking a CI to an asset writes ci.asset_id like PATCH /cis/{id}
	// (WP-272); the handler also requires asset:write.
	{http.MethodPost, "assets/*/cis", identity.PermCIWrite},
	{http.MethodDelete, "assets/*/cis/*", identity.PermCIWrite},
	// Approving or rejecting an order is not editing it.
	{http.MethodPost, "orders/*/approve", identity.PermOrderApprove},
	{http.MethodPost, "orders/*/reject", identity.PermOrderApprove},
	// The goods receipt books stock (WP-760, E-106); without a rule it would
	// derive order:write.
	{http.MethodPost, "orders/*/receive", identity.PermStockWrite},
	// Reconciliation settings (RBA-06); reading them stays discovery:read.
	{http.MethodPut, "reconciliation/source-policy", identity.PermReconciliationManage},
	// Collectors report with the ingest credential; managing them is
	// collector:manage (RBA-01). Certificates are renewed over the
	// collector's NATS channel, not the API (F-012).
	{http.MethodPost, "collectors/*/heartbeat", identity.PermDiscoveryIngest},
	{http.MethodPost, "discovery/ingest", identity.PermDiscoveryIngest},
	// Resolving a review item is its own right (RBA-01, RBA-02).
	{http.MethodPost, "discovery/review-items/*/resolve", identity.PermReviewResolve},
	{http.MethodPost, "review-items/*/resolve", identity.PermReviewResolve},
	// Opening a ticket from a review item is writing a ticket; the handler
	// also requires review:resolve (WP-291).
	{http.MethodPost, "review-items/*/ticket", identity.PermTicketWrite},
	// Reading jobs (RBA-01); starting an export stays export:run.
	{http.MethodGet, "export/jobs", identity.PermJobRead},
	{http.MethodGet, "export/jobs/*", identity.PermJobRead},
	{http.MethodGet, "exports", identity.PermJobRead},
	{http.MethodGet, "exports/*", identity.PermJobRead},
	// Assigning roles to a service account is managing roles, as for users
	// (RBA-08, RBA-01); the account itself stays user:manage.
	{http.MethodPut, "service-accounts/*/roles", identity.PermRoleManage},
	// Publishing a template version directly (MET-53, MET-04, E-106);
	// the other template writes stay citype:manage through the segment.
	{http.MethodPost, "templates/*/versions/*/publish", identity.PermMetamodelPublish},
	// The test run of a metamodel draft and its report (WP-721, MET-53):
	// the report names CIs of every client, so reading it needs the
	// publish right as well (E-106: org_admin only).
	{"*", "metamodel/drafts/*/test", identity.PermMetamodelPublish},
	// The publication of a tested draft (WP-261, MET-53, E-106).
	{http.MethodPost, "metamodel/drafts/*/publish", identity.PermMetamodelPublish},
	// The import of a metamodel file as a draft (WP-722, MET-54): its own
	// rule, not the segment fallback; each item needs the permission of
	// its resource route as well (metamodel.ItemPermission).
	{http.MethodPost, "metamodel/import", identity.PermCITypeManage},
	// The structured search reads (F-055, SRC-03); the handler also
	// requires the read right of the queried entity kind. Rebuilding the
	// index (search/reindex) stays search:write.
	{http.MethodPost, "search/query", identity.PermSearchRead},
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

	// The GraphQL BFF only reads; each resolver checks its own read
	// permission and entitlement (GQL-04), so the route requires an
	// authenticated principal only; the handler admits sessions only.
	if strings.HasPrefix(path, "/bff/") {
		return "", routeProtected
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
		// /me/permissions is read-only self-service as well (OpenAPI
		// getMyPermissions: the caller's own effective permissions, no
		// 403): every user reads its own user_id and rights, also without
		// permission:read (custom roles, client_technician), which the UI
		// needs for the cancel rule of the job center and the saved views.
		if len(segments) == 1 || (len(segments) == 2 && segments[1] == "permissions" &&
			(method == http.MethodGet || method == http.MethodHead)) {
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
	case "labels":
		// The code lookup (WP-762, INV-03) names assets, consumables and
		// locations: the handler checks the read permission of the
		// object's kind (asset:read, stock:read, location:read) and
		// answers 404 without it; the route needs a principal only.
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
	// modules drops the routes of switched-off late modules (WP-214); nil
	// registers every route.
	modules *ModuleSet
	// drafts captures the writes with ?draft into metamodel drafts
	// (WP-720); nil (no database) answers them with 503.
	drafts metamodel.Repository
}

func (a *authorizingRouter) With(middlewares ...func(http.Handler) http.Handler) chi.Router {
	return &authorizingRouter{Router: a.Router.With(middlewares...), prefix: a.prefix, after: a.after, auditor: a.auditor,
		modules: a.modules, drafts: a.drafts}
}

// auditExemptions are the write routes that write no audit entry of their
// own, with the reason (AUD-07, WP-133). Every other write route records
// one entry per successful request; audit_coverage_test.go keeps this list
// and the route table in step.
var auditExemptions = map[string]string{
	"POST /api/v1/audit/verify": "reads the chain",
	"POST /api/v1/search/query": "a search, reads",
	"POST /api/v1/ai/ask":       "a question to the assistant, reads",
	"POST /bff/graphql":         "the BFF refuses mutations, reads",
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
	"POST /api/v1/admin/auth/callback":            "operator path, operator_audit",
	"POST /api/v1/admin/orgs":                     "operator path, operator_audit",
	"POST /api/v1/admin/orgs/{id}/entitlements":   "operator path, operator_audit",
	"PUT /api/v1/admin/orgs/{id}/plan":            "operator path, operator_audit",
	"POST /api/v1/admin/orgs/{id}/addons/{key}":   "operator path, operator_audit",
	"DELETE /api/v1/admin/orgs/{id}/addons/{key}": "operator path, operator_audit",
	// The Alertmanager bridge (OPS-05, E-75) records a queued alert itself,
	// in the tenant transaction of the operator organization; refusals go to
	// operator_audit.
	"POST /api/v1/admin/alerts/alertmanager": "operator path, audited by the bridge in the operator organization",
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
	"POST /api/v1/ingest/bulk": true,
}

// domainRecordedRoutes are the audited write routes whose domain recorder
// writes the audit entry of every successful request in the transaction of
// the change, with old and new values (AUD-07), with the recorder. They get
// no audit intent and so no transaction of the route audit (NFR-10, E-17,
// WP-504); every other audited write route stores an intent before the
// handler runs. TestEveryWriteRouteAuditedOnPostgreSQL
// (audit_coverage_test.go) keeps this list and the recorders in step.
var domainRecordedRoutes = map[string]string{
	// CI writes (AUD-03 history with old and new values).
	"POST /api/v1/cis":                                  "ci.PGRepository.Create",
	"PATCH /api/v1/cis/{id}":                            "ci.PGRepository.Update",
	"DELETE /api/v1/cis/{id}":                           "ci.PGRepository.Delete",
	"POST /api/v1/cis/bulk":                             "ci.PGRepository.Update per CI",
	"POST /api/v1/cis/{id}/merge":                       "ci.PGRepository (merge)",
	"POST /api/v1/cis/{id}/restore":                     "ci.PGRepository.RestoreDeleted",
	"POST /api/v1/cis/{id}/type":                        "ci.PGRepository.ChangeType",
	"PUT /api/v1/cis/{id}/instance-attributes/{key}":    "ci.PGRepository.Update",
	"DELETE /api/v1/cis/{id}/instance-attributes/{key}": "ci.PGRepository.Update",
	"PUT /api/v1/cis/{id}/templates":                    "ci.PGRepository.Update",
	"PUT /api/v1/cis/{id}/tags":                         "ci.PGRepository.ReplaceCITags",
	"PUT /api/v1/cis/{id}/fields/{name}/override":       "override.PGRepository",
	"DELETE /api/v1/cis/{id}/fields/{name}/override":    "override.PGRepository",
	// Tag catalog (CI-08, WP-753): tag.created, tag.updated, tag.deleted,
	// tag.merged (with ci.updated per CI of a merge).
	"POST /api/v1/tags":            "ci.PGRepository.CreateTag",
	"PATCH /api/v1/tags/{id}":      "ci.PGRepository.UpdateTag",
	"DELETE /api/v1/tags/{id}":     "ci.PGRepository.DeleteTag",
	"POST /api/v1/tags/{id}/merge": "ci.PGRepository.MergeTag",
	// Metamodel (MET-15).
	"POST /api/v1/ci-types":                                  "citype.PGRepository",
	"POST /api/v1/ci-types/{id}/activate":                    "citype.PGRepository",
	"POST /api/v1/ci-types/{id}/clone":                       "citype.PGRepository",
	"POST /api/v1/ci-types/{id}/deactivate":                  "citype.PGRepository",
	"PUT /api/v1/ci-types/{id}/fields":                       "citype.PGRepository",
	"DELETE /api/v1/ci-types/{id}/fields/{name}":             "citype.PGRepository",
	"PUT /api/v1/ci-fields/global":                           "citype.PGRepository",
	"DELETE /api/v1/ci-fields/global/{name}":                 "citype.PGRepository",
	"PUT /api/v1/cis/{id}/field-definitions":                 "citype.PGRepository",
	"DELETE /api/v1/cis/{id}/field-definitions/{name}":       "citype.PGRepository",
	"POST /api/v1/attribute-definitions":                     "citype.PGRepository",
	"PATCH /api/v1/attribute-definitions/{id}":               "citype.PGRepository",
	"DELETE /api/v1/attribute-definitions/{id}":              "citype.PGRepository",
	"POST /api/v1/attribute-sets":                            "citype.PGRepository",
	"PATCH /api/v1/attribute-sets/{id}":                      "citype.PGRepository",
	"DELETE /api/v1/attribute-sets/{id}":                     "citype.PGRepository",
	"PUT /api/v1/ci-types/{id}/attribute-sets":               "citype.PGRepository",
	"POST /api/v1/templates":                                 "citype.PGRepository",
	"PATCH /api/v1/templates/{id}":                           "citype.PGRepository",
	"DELETE /api/v1/templates/{id}":                          "citype.PGRepository",
	"POST /api/v1/templates/{id}/clone":                      "citype.PGRepository",
	"POST /api/v1/templates/{id}/versions":                   "citype.PGRepository",
	"PUT /api/v1/templates/{id}/versions/{version}":          "citype.PGRepository",
	"POST /api/v1/templates/{id}/versions/{version}/publish": "citype.PGRepository",
	"POST /api/v1/rules":                                     "citype.PGRepository",
	"PATCH /api/v1/rules/{id}":                               "citype.PGRepository",
	"DELETE /api/v1/rules/{id}":                              "citype.PGRepository",
	"POST /api/v1/layouts":                                   "citype.PGRepository",
	"PATCH /api/v1/layouts/{id}":                             "citype.PGRepository",
	"DELETE /api/v1/layouts/{id}":                            "citype.PGRepository",
	"POST /api/v1/metamodel/drafts":                          "metamodel.PGRepository",
	"PATCH /api/v1/metamodel/drafts/{id}":                    "metamodel.PGRepository",
	"DELETE /api/v1/metamodel/drafts/{id}":                   "metamodel.PGRepository",
	"POST /api/v1/metamodel/drafts/{id}/items":               "metamodel.PGRepository",
	"DELETE /api/v1/metamodel/drafts/{id}/items/{item_id}":   "metamodel.PGRepository",
	"POST /api/v1/metamodel/import":                          "metamodel.PGRepository.CreateImportDraft (metamodel_draft.created, item_added)",
	"POST /api/v1/metamodel/drafts/{id}/publish":             "metamodel.PGRepository.Publish (metamodel.published with the entries of its elements)",
	// Locations and rack placement (DOD-01).
	// CI types hidden per client (WP-858): client.hidden_ci_types_changed.
	"PUT /api/v1/clients/{id}/hidden-ci-types": "tenantapi.PGRepository.SetHiddenCITypes",
	"POST /api/v1/locations":                   "locations.PGRepository",
	"DELETE /api/v1/locations/{id}":            "locations.PGRepository",
	"POST /api/v1/mounts":                      "rack placement",
	"PATCH /api/v1/mounts/{id}":                "rack placement",
	"DELETE /api/v1/mounts/{id}":               "rack placement",
	"POST /api/v1/racks/{id}/mounts":           "rack placement",
	"PUT /api/v1/racks/{id}/mounts/{ci_id}":    "rack placement",
	"DELETE /api/v1/racks/{id}/mounts/{ci_id}": "rack placement",
	// Access: API keys, service accounts, users (RBA-05, AUT-08).
	"POST /api/v1/api-keys":                   "identity.PGAPIKeyStore",
	"POST /api/v1/api-keys/{id}/rotate":       "identity.PGAPIKeyStore",
	"DELETE /api/v1/api-keys/{id}":            "identity.PGAPIKeyStore",
	"POST /api/v1/service-accounts":           "user service accounts",
	"PATCH /api/v1/service-accounts/{id}":     "user service accounts",
	"PUT /api/v1/service-accounts/{id}/roles": "user service accounts",
	"DELETE /api/v1/service-accounts/{id}":    "user service accounts",
	"DELETE /api/v1/role-assignments/{id}":    "user.PGRepository (revocation)",
	"POST /api/v1/users/{id}/anonymize":       "user.PGRepository",
	"DELETE /api/v1/users/{id}":               "user.PGRepository (user.deleted, F-393)",
	"DELETE /scim/v2/Users/{id}":              "user.PGRepository (deactivation)",

	// Lifting a suppression (F-273): relationship.suppression_lifted with
	// the values of the suppression. The forced DELETE
	// /relationships/{id} of a discovery edge writes
	// relationship.suppression_created in its transaction in the same way,
	// which takes the place of its route entry; every other deletion keeps
	// the route entry, so that route stays a route-audited one.
	"DELETE /api/v1/relationship-suppressions/{id}": "relationship.PGRepository.LiftSuppression",

	// Lifecycle transitions (LCY-02, WP-724): ci.lifecycle_changed or
	// asset.lifecycle_changed with the old and new state and the reason,
	// in the transaction of the transition.
	"POST /api/v1/cis/{id}/lifecycle-transitions":    "lifecycle.PGStateStore.RecordTransition",
	"POST /api/v1/assets/{id}/lifecycle-transitions": "lifecycle.PGStateStore.RecordTransition",

	// Asset↔CI links (AST-04, WP-272): asset.linked and asset.unlinked
	// with the CI and the primary flag, last in the transaction, besides
	// ci.updated of the CI.
	"POST /api/v1/assets/{id}/cis":           "asset.PGRepository.RecordLink",
	"DELETE /api/v1/assets/{id}/cis/{ci_id}": "asset.PGRepository.RecordLink",

	// Document links (MGT-01, WP-267): document.linked and
	// document.unlinked with link id and target, in the transaction of the
	// change.
	"POST /api/v1/documents/{id}/links":             "document.PGRepository.LinkDocument",
	"DELETE /api/v1/documents/{id}/links/{link_id}": "document.PGRepository.UnlinkDocument",
	// Soft delete of a document (MGT-01, WP-732): document.deleted in the
	// transaction that sets deleted_at; the route audit stays the fallback.
	"DELETE /api/v1/documents/{id}": "document.PGRepository.Delete",

	// Disposal records (MGT-02, WP-751): disposal_record.created with
	// record_hash, object, method and DIN 66399 classification, last in the
	// transaction of the record.
	"POST /api/v1/disposal-records": "disposal.PGRepository.Create",
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

func (a *authorizingRouter) fullPattern(pattern string) string {
	if a.prefix == "" {
		return pattern
	}
	return strings.TrimSuffix(a.prefix, "/") + "/" + strings.TrimPrefix(pattern, "/")
}

func (a *authorizingRouter) handle(method, pattern string, h http.Handler) {
	full := a.fullPattern(pattern)
	// A switched-off module has no routes: they answer 404 (WP-214).
	if !a.modules.RouteAllowed(full) {
		return
	}
	chain := []func(http.Handler) http.Handler{AuthorizeRoute(method, full)}
	if guard := draftGuard(a.modules, a.drafts, method, full); guard != nil {
		chain = append(chain, guard)
	}
	chain = append(chain, a.after...)
	if isWriteMethod(method) {
		// Behind idempotency: a replayed response writes no second entry.
		key := method + " " + full
		_, exempt := auditExemptions[key]
		source := ""
		if collectorRoutes[key] {
			source = audit.SourceCollector
		}
		mode := middleware.AuditIntent
		switch {
		case exempt:
			mode = middleware.AuditNone
		case domainRecordedRoutes[key] != "":
			mode = middleware.AuditDomain
		}
		chain = append(chain, middleware.AuditRoute(method, full, a.auditor, mode, source))
		auditedRoutes.Store(key, !exempt)
	}
	if _, capable := metamodel.DraftCapableRoutes[method+" "+full]; capable && a.modules.Enabled(ModuleMetamodel) {
		// Innermost: behind authorization, validation, idempotency (a
		// replayed POST answers the stored 202) and the audit, whose
		// domain recorder is the draft's (metamodel_draft.item_added).
		chain = append(chain, metamodel.CaptureDraft(a.drafts, method, full))
	}
	a.Router.With(chain...).Method(method, pattern, h)
}

// draftPreviewRoute is the only read route that evaluates ?draft: the
// preview of a form with the items of a draft (E-113).
const draftPreviewRoute = "GET /api/v1/ci-types/{id}/form"

// draftGuard returns the fail-closed check of ?draft of a route (WP-720,
// MET-53), whatever the module state, or nil: a write route outside
// metamodel.DraftCapableRoutes (including metamodel.DraftExemptRoutes)
// refuses ?draft with 400 draft-unsupported; with the module metamodel
// switched off the capable routes and the form preview answer ?draft with
// 404, so a client that expects a draft never writes productively. The
// preview gets the draft source in its context. Without ?draft every route
// runs unchanged.
func draftGuard(modules *ModuleSet, drafts metamodel.Repository, method, full string) func(http.Handler) http.Handler {
	key := method + " " + full
	_, capable := metamodel.DraftCapableRoutes[key]
	moduleOn := modules.Enabled(ModuleMetamodel)
	var refuse func(w http.ResponseWriter)
	switch {
	case isWriteMethod(method) && !capable:
		refuse = func(w http.ResponseWriter) {
			api.WriteProblem(w, http.StatusBadRequest, "urn:reticora:problem:draft-unsupported", "Bad Request",
				"this route cannot write into a metamodel draft")
		}
	case (capable || key == draftPreviewRoute) && !moduleOn:
		refuse = func(w http.ResponseWriter) {
			api.WriteProblem(w, http.StatusNotFound, "urn:reticora:problem:not-found", "Not Found", "Modul metamodel abgeschaltet")
		}
	case key == draftPreviewRoute:
		forms, ok := drafts.(citype.DraftForms)
		if !ok {
			return nil
		}
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Has(metamodel.DraftParam) {
					r = r.WithContext(citype.WithDraftForms(r.Context(), forms))
				}
				next.ServeHTTP(w, r)
			})
		}
	default:
		return nil
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Has(metamodel.DraftParam) {
				refuse(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (a *authorizingRouter) Method(method, pattern string, h http.Handler) {
	a.handle(method, pattern, h)
}
func (a *authorizingRouter) Get(pattern string, h http.HandlerFunc) {
	a.handle(http.MethodGet, pattern, h)
}
func (a *authorizingRouter) Post(pattern string, h http.HandlerFunc) {
	a.handle(http.MethodPost, pattern, h)
}
func (a *authorizingRouter) Put(pattern string, h http.HandlerFunc) {
	a.handle(http.MethodPut, pattern, h)
}
func (a *authorizingRouter) Patch(pattern string, h http.HandlerFunc) {
	a.handle(http.MethodPatch, pattern, h)
}
func (a *authorizingRouter) Delete(pattern string, h http.HandlerFunc) {
	a.handle(http.MethodDelete, pattern, h)
}
func (a *authorizingRouter) Head(pattern string, h http.HandlerFunc) {
	a.handle(http.MethodHead, pattern, h)
}
func (a *authorizingRouter) Options(pattern string, h http.HandlerFunc) {
	a.handle(http.MethodOptions, pattern, h)
}
func (a *authorizingRouter) Trace(pattern string, h http.HandlerFunc) {
	a.handle(http.MethodTrace, pattern, h)
}
func (a *authorizingRouter) Connect(pattern string, h http.HandlerFunc) {
	a.handle(http.MethodConnect, pattern, h)
}

func (a *authorizingRouter) Route(pattern string, fn func(r chi.Router)) chi.Router {
	return a.Router.Route(pattern, func(r chi.Router) {
		fn(&authorizingRouter{Router: r, prefix: a.fullPattern(pattern), after: a.after, auditor: a.auditor, modules: a.modules,
			drafts: a.drafts})
	})
}

// Mount is intentionally unsupported on the authorizing router: a mounted
// sub-router would bypass per-route permission enforcement. Domain handlers
// must register routes through Get/Post/... or Route so every route resolves
// to a permission.
func (a *authorizingRouter) Mount(pattern string, h http.Handler) {
	panic(fmt.Sprintf("server: Mount(%q) on the authorizing router would bypass authorization; register routes explicitly", pattern))
}
