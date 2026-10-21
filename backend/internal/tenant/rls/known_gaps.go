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
	// RuleOperatorOnly: tables of OperatorTables have ENABLE and FORCE ROW
	// LEVEL SECURITY, and every policy requires OperatorGUC, so tenant
	// transactions neither read nor write them.
	RuleOperatorOnly Rule = "operator-only"
	// RuleLocatedScope: every tenant table with a location or CI reference
	// (a column location_id, room_id, rack_id or ci_id, also with a prefix
	// such as from_location_id or child_ci_id, or a node of the location
	// tree whose id references location) carries the client and the site
	// dimension of the referenced object (client_id and site_id, also with
	// the prefix source_ or target_; for site its id) (TEN-02), or is listed
	// in DerivedScopeTables. client-scope and site-scope then check the
	// predicates.
	RuleLocatedScope Rule = "located-scope"
)

// DerivedScope documents a tenant table with a location or CI reference
// that carries no client_id and site_id of its own (RuleLocatedScope).
type DerivedScope struct {
	// Path names the references the client and site of a row follow and
	// the predicate that applies them.
	Path string
	// Reason says why the table has no scope columns.
	Reason string
	// TestFile (relative to backend/) and TestName name the test that
	// checks the derivation; the catalog test requires both to exist.
	TestFile, TestName string
}

// DerivedScopeTables are the tables RuleLocatedScope reports whose scope is
// derived from the objects they reference instead of own columns. This is a
// documented exception, not a gap: the catalog test fails for an entry
// without its test and for an entry of a table the rule does not report
// (for example after the table got client_id and site_id), so the list
// only shrinks.
var DerivedScopeTables = map[string]DerivedScope{
	"asset_movement": {
		Path: "asset_id -> asset, quantity_item_id -> quantity_item (movement.movementVisible)",
		Reason: "a movement belongs to its item; from_location_id and to_location_id are history, " +
			"the visibility follows the item's client and site",
		TestFile: "internal/movement/movement_scope_integration_test.go", TestName: "TestMovementRepositoryClientScope",
	},
	"assignment": {
		Path:     "asset_id -> asset, ci_id -> ci (assignment.assignmentVisible)",
		Reason:   "an assignment is visible only when its asset and CI are; it has no location of its own",
		TestFile: "internal/assignment/assignment_scope_integration_test.go", TestName: "TestAssignmentRepositoryClientScope",
	},
	"building": {
		Path:     "site_id -> site (site predicate in building_isolation, client via tenantapi.buildingVisible)",
		Reason:   "a building carries its site; the site determines the client",
		TestFile: "internal/tenantapi/tenantapi_scope_integration_test.go", TestName: "TestTenantRepositoryClientScope",
	},
	"composition": {
		Path:     "parent_asset_id, child_asset_id -> asset, child_ci_id -> ci (composition.compositionVisible)",
		Reason:   "a composition row links two items and is visible only when both are",
		TestFile: "internal/composition/composition_scope_integration_test.go", TestName: "TestCompositionRepositoryClientScope",
	},
	"form_submission": {
		Path:     "form_id -> form_def, ci_id -> ci, ticket_id -> ticket (form.submissionVisible)",
		Reason:   "a submission is visible only when its form, CI and ticket are",
		TestFile: "internal/form/form_scope_integration_test.go", TestName: "TestFormRepositoryClientScope",
	},
	"metric_sample": {
		Path:     "ci_id -> ci (security-barrier views metric_sample_v and metric_sample_1h_v, migration 000143)",
		Reason:   "TimescaleDB hypertable without row level security (ViewProtectedTables)",
		TestFile: "internal/monitoring/audit_t1_metric_scope_integration_test.go", TestName: "TestMetricViewsHonourScope",
	},
	"stock_scan": {
		Path:     "asset_id -> asset, ci_id -> ci (stocktake.scanVisible)",
		Reason:   "a scan is visible only when the scanned asset and CI are",
		TestFile: "internal/stocktake/stocktake_scope_integration_test.go", TestName: "TestStocktakeRepositoryClientScope",
	},
}

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

// OperatorTables are organization-spanning tables of the operator path
// (SEC-07, WP-070) without organization_id, signup_request, which exists
// before its organization (WP-111), and scheduler_run, the state of the
// organization-spanning system job scheduler (JOB-02, WP-131), and
// collector_ca, the internal CA of the collector certificates (COL-04,
// WP-145). This is a documented exception to RuleOrgColumn, not a gap: the
// catalog test checks RuleOperatorOnly for them instead.
var OperatorTables = []string{"operator_audit", "signup_request", "scheduler_run", "collector_ca"}

// Session variables (GUCs) the policies are expected to reference. Site and
// team scope do not exist yet; WP-027 and WP-029 introduce them under these
// names or adjust the constants when they close the gaps.
const (
	OrgGUC         = "app.org_id"
	ClientScopeGUC = "app.client_scope"
	SiteScopeGUC   = "app.site_scope"
	TeamScopeGUC   = "app.team_scope"
	SystemGUC      = "app.system"
	OperatorGUC    = "app.operator"
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

// The list is empty (WP-065): every rule holds for every tenant table, and
// the behavioral matrix (matrix_integration_test.go) confirms the policies
// with real statements of the application role per scope and command.
//
// KnownGaps lists every rule violation of the migrated schema at
// KnownGapsBaselineMigration. The catalog test fails for violations missing
// here and for entries that no longer occur, so the list only shrinks: the
// work package that closes a gap removes its entry in the same change.
//
// Tables with a location or CI reference carry client_id and site_id, which
// client-scope and site-scope check, or are documented in
// DerivedScopeTables (RuleLocatedScope, WP-393).
var KnownGaps = []Gap{}
