package audit

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// Scope of the audit list (TEN-06, TEN-09, AUD-05, WP-016, F-389): audit_log
// carries no client, site or team column, so a principal restricted to some
// clients, sites or teams sees an entry only when it may read the object
// the entry is about. The rule hangs on the entry's resource_type, which
// the domain recorders write as the singular domain type ("ci", "asset")
// and the route audit as the first segment of the route
// (middleware.RouteAction: "racks", "relationships", "Users"). Both forms
// stay in the log (no rewrite of the hash-chained history), so the register
// names both.
//
// An entry is visible to a restricted principal when the row its
// resource_id names exists in one of the type's tables under the
// principal's tenant scope: the table's row-level security filters client,
// site and team, and for tables whose policy does not, the rows they refer
// to (scopeParents, scopedGrants) must be visible as well, or the scope
// snapshot the row carries must lie in the principal's (scopeSnapshots).
// Entries of an unknown type, of a type whose objects no restricted
// principal may see in the log (nil tables) and of a deleted or unnamed
// object are hidden (fail-closed); entries about the organization itself
// (orgResources) are visible to every principal of it. An org-wide
// principal sees the whole log. TestEveryAuditedRouteHasAScopeRule
// (server) and TestEveryRecordedResourceTypeHasAScopeRule keep the
// register and the auditors in step.

// resourceTables names, per resource type, the tables of the row an entry's
// resource_id names; nil: restricted principals never see the entries.
var resourceTables = map[string][]string{
	// Domain recorders (singular types).
	"api_key":                  {"api_key"},
	"asset":                    {"asset"},
	"attribute_definition":     {"attribute_definition"},
	"ci":                       {"ci"},
	"ci_type":                  {"ci_type"},
	"collector":                {"collector"},
	"location":                 {"location"},
	"relationship_suppression": {"relationship_suppression"},
	"review_item":              {"review_item"},
	"role_assignment":          {"role_assignment", "user_custom_role"},
	"service_account":          {"service_account"},
	"user":                     {"app_user"},
	"user_invitation":          {"user_invitation"},
	// Types of earlier recorders that entries in the log still carry.
	"contact":    {"contact"},
	"credential": {"credential"},
	"document":   {"document"},
	"ticket":     {"ticket"},

	// Route audit (the first route segment): the route's resource_id is
	// its first id parameter or the id of the created row, so the tables
	// are those of the route snapshot register (route_snapshot.go) under
	// this segment.
	"agents":                    {"endpoint_agent", "agent_enrollment_token"},
	"ai":                        {"ai_conversation"},
	"api-keys":                  {"api_key"},
	"asset-locations":           {"asset_location"},
	"assets":                    {"asset"},
	"assignments":               {"assignment"},
	"attribute-definitions":     {"attribute_definition"},
	"buildings":                 {"building"},
	"cables":                    {"cable"},
	"ci-contacts":               {"ci_contact"},
	"ci-types":                  {"ci_type"},
	"cis":                       {"ci"},
	"clients":                   {"client"},
	"collectors":                {"collector", "collector_enrollment_code"},
	"compliance":                {"compliance_rule"},
	"compositions":              {"composition"},
	"consumables":               {"consumable"},
	"contacts":                  {"contact"},
	"credentials":               {"credential"},
	"desk-bookings":             {"desk_booking"},
	"desks":                     {"desk"},
	"discovery":                 {"discovery_job", "review_item"},
	"discovery-scopes":          {"discovery_scope"},
	"disposal-records":          {"disposal_record"},
	"documents":                 {"document"},
	"export":                    {"export_job"},
	"exports":                   {"export_job"},
	"forms":                     {"form_def"},
	"history":                   {"job"},
	"iga":                       {"iga_connector", "iga_access_request", "iga_access_review", "iga_lifecycle_policy", "iga_drift_finding", "iga_provisioning_task"},
	"inventory":                 {"quantity_item"},
	"invitations":               {"user_invitation"},
	"ip-addresses":              {"ip_address"},
	"ips":                       {"ip_address"},
	"jobs":                      {"job"},
	"keys":                      {"key_item"},
	"lifecycle-definitions":     {"lifecycle_definition"},
	"locations":                 {"location"},
	"maintenance-windows":       {"maintenance_window"},
	"monitoring":                {"alert_rule"},
	"mounts":                    {"rack_mount"},
	"network-interfaces":        {"network_interface"},
	"notification-channels":     {"notification_channel"},
	"orders":                    {"internal_order"},
	"rack-mounts":               {"rack_mount"},
	"racks":                     {"rack"},
	"relationship-suppressions": {"relationship_suppression"},
	"relationships":             {"ci_relationship"},
	"reservations":              {"reservation"},
	"review-items":              {"review_item"},
	"role-assignments":          {"user_custom_role", "role_assignment"},
	"roles":                     {"custom_role", "role", "user_custom_role", "role_assignment"},
	"rooms":                     {"room"},
	"saved-views":               {"saved_view"},
	"security":                  {"security_finding"},
	"service-accounts":          {"service_account"},
	"sites":                     {"site"},
	"slas":                      {"sla"},
	"stock-movements":           {"asset_movement"},
	"stocktakes":                {"stocktake"},
	"subnets":                   {"subnet"},
	"teams":                     {"team"},
	"tickets":                   {"ticket"},
	"training-assignments":      {"training_assignment"},
	"trainings":                 {"training"},
	"users":                     {"app_user"},
	"vlans":                     {"vlan"},
	"vrfs":                      {"vrf"},
	"webhooks":                  {"webhook_subscription"},
	"workflow-runs":             {"workflow_run"},
	"workflows":                 {"workflow_def"},
	// SCIM (/scim/v2/<Resource>).
	"Groups": {"team"},
	"Users":  {"app_user"},
	// The notification preferences of a user and the privacy settings and
	// erasures name no row a scope could place.
	"notifications": nil,
	"privacy":       nil,
}

// orgResources are the types whose entries are about the organization
// itself, its metamodel keyed by name or system runs over the whole
// organization: no client, site or team object, visible to every principal
// of the organization.
var orgResources = map[string]bool{
	"audit":              true, // synthetic entries (cmd/audit-seed)
	"audit-log":          true, // chain verification
	"audit_log":          true, // retention anchor (Purge)
	"ci-fields":          true, // global field definitions, keyed by name
	"notification":       true, // platform alert of the operator organization
	"org":                true,
	"org_dek":            true,
	"organization":       true,
	"reconciliation":     true, // the organization's source policy
	"relationship-types": true, // metamodel, keyed by key
	"retention":          true,
	"search":             true, // index rebuild
}

// scopeParents are, per table whose policy does not filter client and site
// (or not all of them), the columns naming rows that do: the row is
// visible only when every row it names is.
var scopeParents = map[string]map[string]string{
	"asset_location": {"asset_id": "asset"},
	"asset_movement": {"asset_id": "asset", "quantity_item_id": "quantity_item", "from_location_id": "location",
		"to_location_id": "location", "ticket_id": "ticket", "order_id": "internal_order", "document_id": "document"},
	"assignment":      {"asset_id": "asset", "ci_id": "ci"},
	"building":        {"site_id": "site"},
	"cable":           {"source_interface_id": "network_interface", "target_interface_id": "network_interface"},
	"composition":     {"parent_asset_id": "asset", "child_ci_id": "ci", "child_asset_id": "asset"},
	"reservation":     {"asset_id": "asset", "quantity_item_id": "quantity_item", "ticket_id": "ticket", "order_id": "internal_order"},
	"role_assignment": {"scope_client_id": "client", "scope_site_id": "site"},
}

// scopedGrants are the tables whose scope_type and scope_id name the client
// or site a role grant or invitation applies to.
var scopedGrants = map[string]bool{"user_custom_role": true, "user_invitation": true}

// scopeSnapshots are the tables whose rows carry the scope snapshot of the
// principal that made them (scope_clients, scope_sites, scope_teams) while
// their policy is the organization's: the snapshot must lie in the
// principal's scope, as on the export's own read path (WP-409); a row
// without snapshot is the organization's.
var scopeSnapshots = map[string]bool{"export_job": true}

// snapshotInScope is the condition that the snapshot of row t lies in the
// principal's scope in every dimension.
var snapshotInScope = func() string {
	conds := make([]string, 0, 3)
	for _, d := range []struct{ guc, column string }{
		{database.ClientScopeGUC, "scope_clients"}, {database.SiteScopeGUC, "scope_sites"}, {database.TeamScopeGUC, "scope_teams"},
	} {
		conds = append(conds, fmt.Sprintf("(NULLIF(current_setting('%[1]s', true), '') IS NULL OR "+
			"(t.%[2]s IS NOT NULL AND t.%[2]s <@ string_to_array(current_setting('%[1]s', true), ',')::uuid[]))", d.guc, d.column))
	}
	return strings.Join(conds, " AND ")
}()

// resourceTypeName is the form of a resource type the register may hold
// (it is written into the SQL of the list).
var resourceTypeName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

// HasScopeRule reports whether the audit list has a scope rule for entries
// of resourceType (architecture tests): without one, restricted principals
// never see them.
func HasScopeRule(resourceType string) bool {
	_, known := resourceTables[resourceType]
	return known || orgResources[resourceType]
}

// restrictedResource is the condition of the audit list for a principal
// restricted to some clients, sites or teams.
var restrictedResource = buildRestrictedResource()

// buildRestrictedResource writes the condition as a CASE over the type,
// which evaluates only the row check of the entry's type; the types with
// the same tables (singular and plural) share one branch, which keeps the
// plan of the list small. Organization resources pass, any other type
// (unknown, nil tables) does not.
func buildRestrictedResource() string {
	byPred := map[string][]string{}
	for t, tables := range resourceTables {
		checkResourceType(t)
		if orgResources[t] {
			panic("audit: resource type " + t + " is both an organization resource and a table resource")
		}
		if len(tables) == 0 {
			continue
		}
		rows := make([]string, len(tables))
		for i, table := range tables {
			rows[i] = visibleRow(table)
		}
		pred := strings.Join(rows, " OR ")
		byPred[pred] = append(byPred[pred], t)
	}
	org := make([]string, 0, len(orgResources))
	for t := range orgResources {
		checkResourceType(t)
		org = append(org, t)
	}
	branches := make([]string, 0, len(byPred))
	for pred, types := range byPred {
		branches = append(branches, fmt.Sprintf("\n\tWHEN audit_log.resource_type IN (%s) THEN %s", quoted(types), pred))
	}
	sort.Strings(branches)
	return "CASE\n\tWHEN audit_log.resource_type IN (" + quoted(org) + ") THEN true" + strings.Join(branches, "") + "\n\tELSE false END"
}

func checkResourceType(t string) {
	if !resourceTypeName.MatchString(t) {
		panic("audit: invalid resource type " + t)
	}
}

// quoted lists the types as sorted SQL literals.
func quoted(types []string) string {
	sort.Strings(types)
	out := make([]string, len(types))
	for i, t := range types {
		out[i] = "'" + t + "'"
	}
	return strings.Join(out, ", ")
}

// visibleRow is the condition that the row of table the entry names is
// visible under the principal's tenant scope, with the rows it refers to.
func visibleRow(table string) string {
	if !identifier.MatchString(table) {
		panic("audit: invalid table " + table)
	}
	conds := []string{"t.id = audit_log.resource_id"}
	parents := scopeParents[table]
	columns := make([]string, 0, len(parents))
	for c := range parents {
		columns = append(columns, c)
	}
	sort.Strings(columns)
	for _, c := range columns {
		conds = append(conds, fmt.Sprintf("(t.%s IS NULL OR EXISTS (SELECT 1 FROM %s p WHERE p.id = t.%s))", c, parents[c], c))
	}
	if scopedGrants[table] {
		conds = append(conds, `CASE t.scope_type WHEN 'client' THEN EXISTS (SELECT 1 FROM client p WHERE p.id = t.scope_id)
		WHEN 'site' THEN EXISTS (SELECT 1 FROM site p WHERE p.id = t.scope_id) ELSE true END`)
	}
	if scopeSnapshots[table] {
		conds = append(conds, snapshotInScope)
	}
	return "EXISTS (SELECT 1 FROM " + table + " t WHERE " + strings.Join(conds, " AND ") + ")"
}
