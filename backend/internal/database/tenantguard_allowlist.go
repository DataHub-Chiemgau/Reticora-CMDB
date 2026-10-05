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
}
