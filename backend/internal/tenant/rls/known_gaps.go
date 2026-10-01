package rls

// Rule names one requirement the RLS catalog test
// (catalog_integration_test.go) checks for every tenant table, i.e. every table
// of schema public with an organization_id column plus organization itself.
type Rule string

// Rules of the RLS catalog test. Policies count when they apply to PUBLIC or
// the application role reticora_app.
const (
	// RuleRLSEnabled: ENABLE ROW LEVEL SECURITY (pg_class.relrowsecurity).
	RuleRLSEnabled Rule = "rls-enabled"
	// RuleRLSForced: FORCE ROW LEVEL SECURITY (pg_class.relforcerowsecurity).
	RuleRLSForced Rule = "rls-forced"
	// RuleCommands: SELECT, INSERT, UPDATE and DELETE are each covered by a
	// permissive policy.
	RuleCommands Rule = "policy-commands"
	// RuleUsing: every policy for SELECT, UPDATE, DELETE or ALL has USING.
	RuleUsing Rule = "using"
	// RuleWithCheck: every policy for INSERT, UPDATE or ALL has an explicit
	// WITH CHECK.
	RuleWithCheck Rule = "with-check"
	// RuleOrgPredicate: USING and WITH CHECK of every command use OrgGUC.
	RuleOrgPredicate Rule = "org-predicate"
	// RuleSystemWrite: no policy lets the system flag SystemGUC write
	// (INSERT, UPDATE, DELETE); it may only widen SELECT for claiming.
	RuleSystemWrite Rule = "system-write"
	// RuleGlobalRows: no policy lets INSERT, UPDATE or DELETE reach global
	// catalog rows (organization_id IS NULL).
	RuleGlobalRows Rule = "global-rows"
	// RuleClientScope: tables with client_id use ClientScopeGUC in USING and
	// WITH CHECK of every command.
	RuleClientScope Rule = "client-scope"
	// RuleSiteScope: tables with site_id use SiteScopeGUC in USING and WITH
	// CHECK of every command (CH25).
	RuleSiteScope Rule = "site-scope"
	// RuleTeamScope: tables with team_id use TeamScopeGUC in USING and WITH
	// CHECK of every command (CH25).
	RuleTeamScope Rule = "team-scope"
	// RuleOrgColumn: every table of schema public except organization and
	// schema_migrations has an organization_id column.
	RuleOrgColumn Rule = "org-column"
)

// Session variables (GUCs) the policies are expected to reference. Site and
// team scope do not exist yet; WP-027 and WP-029 introduce them under these
// names or adjust the constants when they close the gaps.
const (
	OrgGUC         = "app.org_id"
	ClientScopeGUC = "app.client_scope"
	SiteScopeGUC   = "app.site_scope"
	TeamScopeGUC   = "app.team_scope"
	SystemGUC      = "app.system"
)

// Gap is a known violation of a catalog rule together with the work package
// of docs/plan/implementierungsplan.md that closes it.
type Gap struct {
	Table string
	Rule  Rule
	WP    string
}

// KnownGapsBaselineMigration is the newest migration at the time the gap list
// was recorded. Every listed table must already exist in a migration up to this
// version: new tables have to meet all rules and must not be added here.
const KnownGapsBaselineMigration = 57

// KnownGaps lists every rule violation of the migrated schema at
// KnownGapsBaselineMigration. The catalog test fails for violations missing
// here and for entries that no longer occur, so the list only shrinks: the
// work package that closes a gap removes its entry in the same change.
//
// Tables that only inherit client or site from a referenced object (CI child
// tables, documents, tickets, desks) have no own scope column yet, so no rule
// applies to them; WP-025, WP-028 and WP-029 add the columns together with the
// policies, after which client-scope and site-scope cover them automatically.
var KnownGaps = []Gap{
	// System policies (app.system) are writable instead of SELECT-only.
	{"alert_rule", RuleSystemWrite, "WP-022"},
	{"collector_enrollment_code", RuleSystemWrite, "WP-022"},
	{"export_job", RuleSystemWrite, "WP-022"},
	{"webhook_dead_letter", RuleSystemWrite, "WP-022"},
	{"webhook_delivery", RuleSystemWrite, "WP-022"},

	// Tables with client_id whose policies only check the organization.
	{"asset", RuleClientScope, "WP-023"},
	{"consumable", RuleClientScope, "WP-023"},
	{"form_def", RuleClientScope, "WP-023"},
	{"internal_order", RuleClientScope, "WP-023"},
	{"key_item", RuleClientScope, "WP-023"},
	{"location_node", RuleClientScope, "WP-023"},
	{"maintenance_notification", RuleClientScope, "WP-023"},
	{"quantity_item", RuleClientScope, "WP-023"},
	{"sla", RuleClientScope, "WP-023"},

	// Global catalog rows can be changed or deleted by every tenant.
	{"ci_type", RuleGlobalRows, "WP-024"},
	{"lifecycle_definition", RuleGlobalRows, "WP-024"},
	{"lifecycle_state", RuleGlobalRows, "WP-024"},
	{"lifecycle_transition", RuleGlobalRows, "WP-024"},
	{"relationship_type", RuleGlobalRows, "WP-024"},

	// Tables without organization_id.
	{"ci_type_attribute", RuleOrgColumn, "WP-024"},
	{"permission", RuleOrgColumn, "WP-024"},
	{"team_member", RuleOrgColumn, "WP-024"},
	{"user_custom_role", RuleOrgColumn, "WP-024"},

	// Tables with site_id without site scope.
	{"building", RuleSiteScope, "WP-027"},
	{"ci", RuleSiteScope, "WP-027"},
	{"location_node", RuleSiteScope, "WP-027"},
	{"subnet", RuleSiteScope, "WP-027"},

	// Tables with team_id without team scope.
	{"ticket", RuleTeamScope, "WP-029"},

	// Hypertable without row level security (TEC-06 spike WP-039 first).
	{"metric_sample", RuleRLSEnabled, "WP-040"},
	{"metric_sample", RuleRLSForced, "WP-040"},
	{"metric_sample", RuleCommands, "WP-040"},
}
