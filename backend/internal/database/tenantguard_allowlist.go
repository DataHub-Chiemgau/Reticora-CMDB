package database

// tenantGuardAllowlist lists the files outside this package that may call a
// connection pool directly (Query, QueryRow, Exec, Begin, BeginTx, SendBatch,
// CopyFrom, Acquire) instead of going through WithTenant or WithSystem. The
// architecture test (tenantguard_test.go, WP-041, TEN-06) fails for every
// other direct pool call and for entries that no longer match a call, so the
// list only shrinks. Each entry is a documented system path (E-08) and names
// the work package that removes it, if any.
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

// adminPoolCallers lists the files that may call NewAdminPool, the pool
// without the runtime login check (TEN-03, WP-351). Only command-line tools
// that log in with the migration/admin login belong here; the server and
// every package under internal/ use NewPool. TestAdminPoolCallersAreListed
// fails for every other caller and for entries that match no call any more.
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
