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
	// The tables belong to reticora_owner, which reticora_app may SET ROLE to
	// for runtime index DDL (migration 000078); FORCE binds the owner to the
	// policies as well. database.VerifyRoleContract checks the same at startup.
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
	// RuleOrgColumn: every table of schema public except organization,
	// schema_migrations and GlobalCatalogTables has an organization_id column.
	RuleOrgColumn Rule = "org-column"
	// RuleReadOnlyCatalog: tables of GlobalCatalogTables grant reticora_app
	// no INSERT, UPDATE or DELETE.
	RuleReadOnlyCatalog Rule = "read-only-catalog"
	// RuleViewBarrier: tables of ViewProtectedTables grant reticora_app no
	// privilege at all; their view is a security barrier with the
	// organization predicate and grants reticora_app SELECT and INSERT only.
	RuleViewBarrier Rule = "view-barrier"
)

// ViewProtectedTables maps tables that cannot carry row level security to
// the security-barrier view the application must use instead. TimescaleDB
// refuses RLS on hypertables with compression and continuous aggregates
// (docs/decisions/0001-timescale-rls.md, WP-040). This is a documented
// exception to the RLS rules, not a gap: the catalog test checks
// RuleViewBarrier for these tables instead.
var ViewProtectedTables = map[string]string{"metric_sample": "metric_sample_v"}

// GlobalCatalogTables are global catalogs without organization_id (E-10,
// WP-024). They are maintained by migrations only; the application role may
// read but not write them. This is a documented exception to RuleOrgColumn,
// not a gap: the catalog test checks RuleReadOnlyCatalog for them instead.
var GlobalCatalogTables = []string{"permission"}

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
var KnownGaps = []Gap{}
