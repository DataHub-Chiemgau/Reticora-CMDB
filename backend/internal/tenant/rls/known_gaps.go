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
	// (INSERT, UPDATE, DELETE); it may only widen SELECT for claiming. It
	// applies to the tenant tables; the tables of SystemTables and
	// SystemCatalogTables are written with the system flag by design.
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
	// RuleTeamScope: tables with team_id or owner_team_id use TeamScopeGUC
	// in USING and WITH CHECK of every command (CH25, TEN-05).
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
	// RuleSystemOnly: tables of SystemTables have ENABLE and FORCE ROW LEVEL
	// SECURITY and the TRUNCATE guard, every policy requires SystemGUC
	// (USING and WITH CHECK), none covers DELETE, and reticora_app holds
	// neither DELETE nor TRUNCATE: only the system context reads
	// (database.WithSystem) and writes (database.WithSystemWrite) them, and
	// no one deletes their rows.
	RuleSystemOnly Rule = "system-only"
	// RuleSystemCatalog: tables of SystemCatalogTables have no
	// organization_id, ENABLE and FORCE ROW LEVEL SECURITY and the TRUNCATE
	// guard; one SELECT policy without condition lets every context read,
	// INSERT, UPDATE and DELETE are each covered, and every policy for them
	// (no ALL policy) requires SystemGUC in USING and WITH CHECK: only the
	// system context (database.WithSystemWrite) writes them.
	RuleSystemCatalog Rule = "system-catalog"
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
	"building": {
		Path:     "site_id -> site (site predicate in building_isolation, client via tenantapi.buildingVisible)",
		Reason:   "a building carries its site; the site determines the client",
		TestFile: "internal/tenantapi/tenantapi_scope_integration_test.go", TestName: "TestTenantRepositoryClientScope",
	},
	"internal_order": {
		Path: "target_location_id -> location (bin check in the order repository on every write, order.checkTargetBin)",
		Reason: "the target bin is a delivery target, not a location of the order; " +
			"the visibility of the order follows its client_id",
		TestFile: "internal/order/order_target_integration_test.go", TestName: "TestOrderTargetBinMatchesClient",
	},
	"metric_sample": {
		Path:     "ci_id -> ci (security-barrier views metric_sample_v and metric_sample_1h_v, migration 000143)",
		Reason:   "TimescaleDB hypertable without row level security (ViewProtectedTables)",
		TestFile: "internal/monitoring/audit_t1_metric_scope_integration_test.go", TestName: "TestMetricViewsHonourScope",
	},
}

// ViewProtectedTables maps tables that cannot carry row level security to
// the security-barrier view the application must use instead. TimescaleDB
// refuses RLS on hypertables with compression and continuous aggregates
// (docs/decisions/0001-timescale-rls.md, WP-040). This is a documented
// exception to the RLS rules, not a gap: the catalog test checks
// RuleViewBarrier for these tables instead.
// probe_result (WP-945, ADR 0002) is protected like metric_sample; its
// client and site come from the probe inside probe_result_v, it has no
// column RuleLocatedScope reports and so no DerivedScopeTables entry.
var ViewProtectedTables = map[string]string{"metric_sample": "metric_sample_v", "probe_result": "probe_result_v"}

// GlobalCatalogTables are global catalogs without organization_id (E-10,
// WP-024). They are maintained by migrations only; the application role may
// read but not write them. This is a documented exception to RuleOrgColumn,
// not a gap: the catalog test checks RuleReadOnlyCatalog for them instead.
// compliance_control is the control catalog of ISO 27001, NIS2 and KRITIS
// (SEC-04, WP-1128, E-238).
var GlobalCatalogTables = []string{"permission", "compliance_control"}

// OperatorTables are organization-spanning tables of the operator path
// (SEC-07, WP-070) without organization_id, signup_request, which exists
// before its organization (WP-111), and scheduler_run, the state of the
// organization-spanning system job scheduler (JOB-02, WP-131), and
// collector_ca, the internal CA of the collector certificates (COL-04,
// WP-145). This is a documented exception to RuleOrgColumn, not a gap: the
// catalog test checks RuleOperatorOnly for them instead.
var OperatorTables = []string{"operator_audit", "signup_request", "scheduler_run", "collector_ca"}

// SystemTables are tables of the installation without organization_id that
// only the server itself reaches, through the system flag: platform_secret
// holds the derivation root of the derived secrets, sealed with the master
// key (SEC-06, WP-459). No tenant and no operator transaction reads it, the
// server reads it with database.WithSystem and creates and reseals it with
// database.WithSystemWrite. This is a documented exception to RuleOrgColumn,
// not a gap: the catalog test checks RuleSystemOnly for them instead.
var SystemTables = []string{"platform_secret"}

// SystemCatalogTables are the global catalogs of the installation that a
// job of the server maintains (SEC-02, WP-1117): the vulnerability catalog
// of the feeds (vuln_source, vuln_advisory, vuln_affected). They have no
// organization_id, every context reads them (the version match of an
// organization reads them in its tenant transaction), and only the system
// context writes them (database.WithSystemWrite), deletes replaced ranges
// included. Neither GlobalCatalogTables (maintained by migrations only,
// RuleReadOnlyCatalog) nor SystemTables (read only with the system flag,
// no DELETE, RuleSystemOnly) fit. This is a documented exception to
// RuleOrgColumn and RuleSystemWrite, not a gap: the catalog test checks
// RuleSystemCatalog for them instead.
var SystemCatalogTables = []string{"vuln_source", "vuln_advisory", "vuln_affected"}

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

// LeakyIndex documents an index of a table with row level security that
// the application role cannot use as an index condition (SR-04, K22): its
// expression or predicate calls a function that is not leakproof, or its
// operator class has only operators that are not leakproof. Under the
// policies of the table the planner evaluates such a condition only after
// the policies, so a query of reticora_app reads the table instead; the
// index still serves the owner and the constraint it backs (unique,
// exclusion), which is checked without the policies.
type LeakyIndex struct {
	// Table is the table of the index.
	Table string
	// Leaky names the functions and operator classes that are not
	// leakproof, as LeakyIndexQuery reports them.
	Leaky string
	// Reason says why the index stays and what serves the reads of the
	// application role instead.
	Reason string
}

// LeakyIndexes are the indexes LeakyIndexQuery reports, by index name
// (WP-836). TestRLSIndexesAreLeakproof (index_catalog_integration_test.go)
// fails for a reported index missing here and for an entry that is no
// longer reported, so the list only shrinks: a new index on a table with
// row level security uses leakproof operators (a stored column compared
// with texteq, a side table such as ci_identity_mac, or reticora_attr_text
// of migration 000162), and the change that drops or replaces a listed
// index removes its entry. The exception is an index the plan of its work
// package demands and no leakproof operator can serve: the index of an
// exclusion constraint over ranges (desk_booking_no_overlap,
// asn_range_no_overlap) or a GIN index over an array the API filters on
// (idx_security_finding_cve_ids); its entry names the work package. reticora_attr_text is leakproof (E-73, WP-435),
// so the attribute indexes idx_attr_* are not listed. No migration of
// phase 2 sets a function LEAKPROOF (E-133).
var LeakyIndexes = map[string]LeakyIndex{
	"asn_range_no_overlap": {
		Table: "asn_range", Leaky: "function int8range(bigint,bigint,text), operator class range_ops",
		Reason: "exclusion constraint against overlapping AS number ranges of an organization (000316, WP-1066); the " +
			"constraint is checked without the policies, no read of the application role filters on the range",
	},
	"desk_booking_no_overlap": {
		Table: "desk_booking", Leaky: "function tstzrange(timestamp with time zone,timestamp with time zone), operator class range_ops",
		Reason: "exclusion constraint against overlapping bookings of a desk (000065); the constraint is checked " +
			"without the policies, no read of the application role filters on the range",
	},
	"idx_ci_attributes_path": {
		Table: "ci", Leaky: "operator class jsonb_path_ops",
		Reason: "GIN index over ci.attributes (000109); @> (jsonb_contains) is not leakproof, the application role " +
			"filters attributes through reticora_attr_text and idx_attr_* (E-73, WP-435) and matches identity " +
			"MACs through ci_identity_mac (WP-434)",
	},
	"idx_location_path": {
		Table: "location", Leaky: "operator class gist_ltree_ops",
		Reason: "subtree index of the location tree (000062); the ltree operators are not leakproof, subtree " +
			"reads of the application role scan the locations of the organization",
	},
	"idx_pii_subject_tokens": {
		Table: "pii_subject", Leaky: "operator class array_ops",
		Reason: "blind tokens of the PII vault (000126); @> (arraycontains) is not leakproof, the contact search " +
			"compares the tokens per search document like the full-text search (E-133 option 3)",
	},
	"idx_review_item_candidates": {
		Table: "review_item", Leaky: "operator class array_ops",
		Reason: "candidate CIs of a review (000140); the array operators are not leakproof, the review list reads " +
			"the open reviews of the organization",
	},
	"idx_saved_view_shared_roles": {
		Table: "saved_view", Leaky: "operator class array_ops",
		Reason: "shared roles of a saved view (000133); && (arrayoverlap) is not leakproof, an organization holds " +
			"few saved views",
	},
	"idx_search_document_fts": {
		Table: "search_document", Leaky: "operator class tsvector_ops",
		Reason: "full-text index over the stored search_vector (000030); @@ (ts_match_vq) is not leakproof, the " +
			"search of the application role stays linear within the budget up to 10,000 objects " +
			"(E-133 option 3, E-17; TestSearchPlanUnderRLS)",
	},
	"idx_security_finding_cve_ids": {
		Table: "security_finding", Leaky: "operator class array_ops",
		Reason: "CVE ids of the findings (000315, WP-1116); @> (arraycontains) is not leakproof, the filter cve of " +
			"GET /security/findings reads the findings of the organization within the caller's scope",
	},
	"idx_vuln_advisory_cve_ids": {
		Table: "vuln_advisory", Leaky: "operator class array_ops",
		Reason: "CVE ids of the global vulnerability catalog (000333, WP-1117); the catalog belongs to no organization and " +
			"its SELECT policy is USING (true), so no row hides behind a policy the operator could leak",
	},
	"idx_signup_request_email": {
		Table: "signup_request", Leaky: "function lower(text)",
		Reason: "signup requests of the operator path (000103, OperatorTables); lower is not leakproof, the " +
			"operator reads few rows",
	},
	"location_path_key": {
		Table: "location", Leaky: "operator class ltree_ops",
		Reason: "unique constraint on the materialized path (000062); ltree_eq is not leakproof, the constraint " +
			"is checked without the policies",
	},
	"rack_mount_no_overlap": {
		Table: "rack_mount",
		Leaky: "function int4pl(integer,integer), function int4range(integer,integer), function rack_face_range(text), " +
			"operator class range_ops",
		Reason: "exclusion constraint against overlapping rack mounts (000112); the constraint is checked " +
			"without the policies, no read of the application role filters on the ranges",
	},
	"review_item_open_override_conflict": {
		Table: "review_item", Leaky: "function jsonb_object_field_text(jsonb,text)",
		Reason: "unique index of the open override conflicts (000076) over payload ->> 'ci_id' and 'field'; it " +
			"deduplicates the inserts, checked without the policies",
	},
	"review_item_open_serial_mismatch": {
		Table: "review_item", Leaky: "function jsonb_object_field_text(jsonb,text)",
		Reason: "unique index of the open serial mismatches (000088) over payload ->> 'ci_id'; it deduplicates " +
			"the inserts, checked without the policies",
	},
	"review_item_open_unlicensed_ci": {
		Table: "review_item", Leaky: "function jsonb_object_field_text(jsonb,text)",
		Reason: "unique index of the open unlicensed CIs (000085) over payload ->> 'device_key'; it " +
			"deduplicates the inserts, checked without the policies",
	},
}

// LeakyIndexQuery lists the indexes of the tables of schema public with
// row level security that the application role cannot use as an index
// condition (SR-04, K22, WP-836), one row per index: table, index and its
// findings, sorted and comma separated. A finding is
//
//   - "function <signature>": the expression or the predicate of the index
//     (pg_index.indexprs, indpred) calls a function, directly or as the
//     function of an operator, that is not leakproof (pg_proc.proleakproof);
//   - "expression <node>": the expression or the predicate holds a
//     conversion through the text representation (CoerceViaIO, its input
//     and output functions are not in the tree) or a comparison whose
//     operators the query does not resolve (MinMaxExpr, RowCompareExpr);
//   - "operator class <name>": no search operator of the operator class of
//     a key column (pg_opclass, pg_amop) with the column type on the left is
//     leakproof, so no condition on the column can use the index.
//
// TestRLSIndexesAreLeakproof and TestIndexStatementLeakproof
// (internal/citype) run it.
const LeakyIndexQuery = `
WITH idx AS (
    SELECT t.relname AS table_name, ic.relname AS index_name, i.indclass,
           coalesce(i.indexprs::text, '') || ' ' || coalesce(i.indpred::text, '') AS tree
      FROM pg_index i
      JOIN pg_class t ON t.oid = i.indrelid
      JOIN pg_class ic ON ic.oid = i.indexrelid
      JOIN pg_namespace n ON n.oid = t.relnamespace
     WHERE n.nspname = 'public' AND t.relrowsecurity
), called AS (
    SELECT idx.index_name, m[1]::oid AS fn
      FROM idx, regexp_matches(idx.tree, ':(?:funcid|opfuncid) (\d+)', 'g') AS m
    UNION
    SELECT idx.index_name, o.oprcode::oid
      FROM idx, regexp_matches(idx.tree, ':opno (\d+)', 'g') AS m
      JOIN pg_operator o ON o.oid = m[1]::oid
), findings AS (
    SELECT c.index_name, 'function ' || p.oid::regprocedure::text AS finding
      FROM called c JOIN pg_proc p ON p.oid = c.fn
     WHERE NOT p.proleakproof
    UNION
    SELECT idx.index_name, 'expression ' || lower(m[1])
      FROM idx, regexp_matches(idx.tree, '\{(COERCEVIAIO|MINMAXEXPR|ROWCOMPAREEXPR)\M', 'g') AS m
    UNION
    SELECT idx.index_name, 'operator class ' || oc.opcname
      FROM idx, unnest(idx.indclass::oid[]) AS k(opclass)
      JOIN pg_opclass oc ON oc.oid = k.opclass
     WHERE NOT EXISTS (
           SELECT 1
             FROM pg_amop ao
             JOIN pg_operator o ON o.oid = ao.amopopr
             JOIN pg_proc p ON p.oid = o.oprcode
            WHERE ao.amopfamily = oc.opcfamily AND ao.amoppurpose = 's'
              AND ao.amoplefttype = oc.opcintype AND p.proleakproof)
)
SELECT idx.table_name, f.index_name, string_agg(f.finding, ', ' ORDER BY f.finding COLLATE "C")
  FROM findings f JOIN idx USING (index_name)
 GROUP BY idx.table_name, f.index_name
 ORDER BY f.index_name COLLATE "C"`

// ReferenceWithoutOrganization documents a foreign key between two tenant
// tables that names the referenced row by its id only (SR-03, WP-838).
// PostgreSQL checks foreign keys without row level security, so such a
// reference accepts the id of an object of another organization; only the
// application keeps the reference inside its organization.
type ReferenceWithoutOrganization struct {
	// Constraint is the name of the foreign key.
	Constraint string
	// Target is the referenced table.
	Target string
	// Reason says why the reference has no organization yet and until when.
	Reason string
}

// ReferencesWithoutOrganization are the foreign keys between two tenant
// tables (tables of schema public with an organization_id column) without
// organization_id, by "table.columns" (WP-838, SR-03). References to tables
// with global rows (organization_id nullable, such as ci_type, E-10) do not
// count. TestTenantReferencesCarryOrganization
// (object_reference_integration_test.go) fails for such a foreign key
// missing here and for an entry that no longer exists or now carries the
// organization, so the list only shrinks: a new reference includes the
// organization, (column, organization_id) REFERENCES … (id,
// organization_id) (K19, patterns 000136 and 000194), and the change that
// fixes a listed reference removes its entry. Migration 000194 made the
// references of the phase-2 modules on ci, asset, location and consumable
// composite; the references of desk, key and training are composite since
// 000177 (WP-639) and are not listed (E-137). Migration 000287 (WP-876,
// SR-03 rest I) made the 59 references of the core, location, network,
// rack, role and discovery tables composite, migration 000288 (WP-877,
// SR-03 rest II) the remaining 55: the list is empty and stays empty, every
// new reference between tenant tables carries the organization.
var ReferencesWithoutOrganization = map[string]ReferenceWithoutOrganization{}
