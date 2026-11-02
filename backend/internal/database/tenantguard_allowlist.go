package database

// tenantGuardAllowlist lists the files outside this package that may use a
// database connection directly instead of going through WithTenant or
// WithSystem: a method that runs SQL or hands out a connection on a
// *pgxpool.Pool, *pgxpool.Conn, *pgx.Conn or *sql.DB (called, as a method
// value or through an embedded field) or a function that opens one
// (pgx.Connect, pgxpool.New, sql.Open). The architecture test
// (tenantguard_test.go, WP-041, WP-561, TEN-06) finds them by type and
// fails for every other use, for entries that no longer match a use and
// for entries without a reason, so the list only shrinks. Each entry is a
// documented system path (E-08) and names the work package that removes
// it, if any.
var tenantGuardAllowlist = map[string]string{
	// Readiness probe: checks installed extensions in pg_extension, reads no
	// tenant data.
	"internal/server/health.go": "readiness check on pg_extension",

	// Restore-test seeding command: draws a fresh organization id with
	// gen_random_uuid() before the organization exists; all tenant writes run
	// in WithTenant.
	"cmd/audit-seed/main.go": "organization id before the tenant exists",

	// Scheduler leader (JOB-02, WP-131): a session advisory lock lives on a
	// dedicated connection for as long as it is held; the connection runs
	// only pg_try_advisory_lock, pg_advisory_unlock and SELECT 1 and reads
	// no tenant data.
	"internal/jobs/scheduler.go": "advisory locks of the scheduler",

	// Attribute index job (DB-04, WP-142): CREATE INDEX CONCURRENTLY cannot
	// run in a transaction, so the job takes a dedicated connection that
	// switches to the owner role, runs only index DDL and catalog lookups
	// (pg_class, pg_index) and is closed afterwards; the definition is read
	// in WithTenant.
	"internal/citype/ddl_job.go": "index DDL outside a transaction",
}

// withSystemCallers is the release list of WithSystem (E-08, F-191,
// WP-561): every file outside this package that opens the read-only
// transaction with the system flag, which reads across organizations
// (only the SELECT-only system exceptions and the system tables return
// rows), with the reason it needs that view. Test files may call it.
// TestWithSystemCallersAreListed fails for every other caller, for entries
// that match no call any more and for entries without a reason.
var withSystemCallers = map[string]string{
	// Offline audit chain verification (AUD-04): lists every organization
	// to verify each chain in its tenant transaction.
	"cmd/audit-verify/main.go": "organizations whose audit chain the operator tool verifies",

	// Route intents (AUD-07): finds the organizations with intents past the
	// grace period and counts the pending intents for the metric; each
	// organization's intents are recorded in its tenant transaction.
	"internal/audit/route_intent.go": "organizations with due route intents and their count",

	// Attribute index job (DB-04): lists the organizations whose definitions
	// are then read in their tenant transaction.
	"internal/citype/ddl_job.go": "organizations whose definitions may use a key index",

	// Collector CA (COL-04): the revocation list, the revocation check of
	// the NATS endpoint and the collectors on a former CA cover the
	// certificates of every organization.
	"internal/discovery/ca.go": "collector certificates of every organization for CRL, revocation check and CA rotation",

	// Collector status monitor: lists the organizations it checks one by
	// one in their tenant transaction.
	"internal/discovery/collector_status.go": "organizations whose collectors the status monitor checks",

	// Discovery job reaper: lists the organizations whose overdue jobs it
	// fails in their tenant transaction.
	"internal/discovery/jobs.go": "organizations whose overdue discovery jobs are failed",

	// Superseded collector certificates (COL-04): finds the organizations
	// with certificates to revoke; the revocation runs in their tenant
	// transaction.
	"internal/discovery/natsedge.go": "organizations with superseded collector certificates",

	// Collector enrollment: the code names its organization, so it is
	// looked up before any tenant is known (system exception
	// collector_enrollment_code); the enrollment runs in that tenant.
	"internal/discovery/pg_repository.go": "organization of an enrollment code",

	// Outbox publisher (EVT-01): finds the organizations with pending or
	// expired events; publishing and cleanup run per organization.
	"internal/events/publisher.go": "organizations with pending or expired outbox events",

	// Export worker (OPS-05): counts the backlog and finds the organizations
	// with pending or stale jobs (system exception export_job); claiming
	// runs in their tenant transaction.
	"internal/export/pg_job_repository.go": "export job backlog and organizations with pending or stale jobs",

	// API key authentication: the key names its organization, so it is
	// identified by its prefix before any tenant is known (system exception
	// api_key).
	"internal/identity/apikey_store.go": "API key lookup by prefix before the tenant is known",

	// Entitlement notices: lists the organizations whose notices are sent in
	// their tenant transaction.
	"internal/jobs/entitlement_notices.go": "organizations whose entitlement notices are sent",

	// Job queue (JOB-02): finds the due and the overdue jobs of every
	// organization; claiming and failing stay in the tenant.
	"internal/jobs/pg_repository.go": "due and overdue jobs of every organization",

	// PII backfill: lists the organizations sealed one by one in their
	// tenant transaction.
	"internal/jobs/pii_backfill.go": "organizations whose rows the PII backfill seals",

	// Retention (NFR-06): lists the organizations cleaned up one by one in
	// their tenant transaction.
	"internal/jobs/retention.go": "organizations the retention job cleans up",

	// Key rotation: lists the organizations whose keys are rotated in their
	// tenant transaction.
	"internal/jobs/rotation.go": "organizations whose keys are rotated",

	// Offline detector: lists the organizations it checks one by one in
	// their tenant transaction.
	"internal/lifecycle/offline.go": "organizations the offline detector checks",

	// Alert evaluator: reads the enabled rules of every
	// organization (system exception alert_rule); a rule's state changes in
	// its organization.
	"internal/monitoring/pg_alert_store.go": "enabled alert rules of every organization",

	// Notification digests and deliveries: finds the organizations with
	// ended windows or due deliveries; each is bundled or claimed in its
	// tenant transaction.
	"internal/notify/pg_notification.go": "organizations with ended digest windows or due notification deliveries",

	// Operator entitlements: checks that the target organization exists.
	"internal/operator/entitlements.go": "existence of the target organization of the operator",

	// Operator listing: lists every organization for the platform operator.
	"internal/operator/handler.go": "organization listing of the platform operator",

	// Derivation root (SEC-06): reads the system table platform_secret,
	// which has no organization.
	"internal/platform/crypto/derivation_store.go": "derivation root in platform_secret",

	// Relationship cleanup: lists the organizations cleaned one by one in
	// their tenant transaction.
	"internal/relationship/cleanup.go": "organizations whose stale relationships are cleaned up",

	// MFA reconciliation: lists the organizations whose org_admins are bound
	// to MFA in their tenant transaction.
	"internal/user/mfa.go": "organizations whose org_admins are bound to MFA",

	// Webhook retry worker (OPS-05): counts the backlog and finds the
	// organizations with due deliveries (system exception
	// webhook_delivery); leasing runs in their tenant transaction.
	"internal/webhook/pg_delivery_store.go": "webhook delivery backlog and organizations with due deliveries",

	// Webhook secret rotation: lists the organizations whose expired
	// previous secrets are cleared in their tenant transaction.
	"internal/webhook/pg_repository.go": "organizations whose expired webhook secrets are cleared",
}

// systemWriteCallers lists the files that may call WithSystemWrite, the
// read-write transaction with the system flag (the system tables,
// rls.SystemTables). Test files may call it to probe its limits.
// TestSystemWriteCallersAreListed fails for every other caller, for entries
// that match no call any more and for entries without a reason.
var systemWriteCallers = map[string]string{
	// The derivation root of the installation (SEC-06, WP-459): created at
	// the first start, rewrapped by rotation.master_key (platform_secret).
	"internal/platform/crypto/derivation_store.go": "derivation root in platform_secret",
}

// adminPoolCallers lists the files that may call NewAdminPool, the pool
// without the runtime login check (TEN-03, WP-351). Only command-line tools
// that log in with the migration/admin login belong here; the server and
// every package under internal/ use NewPool. TestAdminPoolCallersAreListed
// finds the references by type (WP-561) and fails for every other caller,
// for entries that match no call any more and for entries without a reason.
var adminPoolCallers = map[string]string{
	// Demo and load-test seed (SIM-01, NFR-10): make seed and the load job run
	// it with the migration login.
	"cmd/seed/main.go": "seed with the migration login",

	// Offline audit chain verification (AUD-04): an operator tool, run with
	// the admin login, also against the restored database of the nightly
	// restore test.
	"cmd/audit-verify/main.go": "audit chain verification with the admin login",

	// Restore-test seeding command: creates a tenant with an audit chain in
	// the empty database of the nightly restore test with the admin login.
	"cmd/audit-seed/main.go": "restore-test seed with the admin login",
}
