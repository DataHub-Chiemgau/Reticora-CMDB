package database_test

// Phase-1 acceptance audit, group T1-tenancy: REGRESSION test (passes).
//
// WP-064 (TEN-03, DB-04; CH19) gap "Startup check must consider the session
// (login) role": the recommended runtime login (LOGIN NOSUPERUSER
// NOBYPASSRLS, member of reticora_app only) passes the startup checks of
// NewPool, and a RESET ROLE on such a connection does not regain any
// SUPERUSER or BYPASSRLS right. The test creates a cluster-wide login role
// with a unique name and drops it again.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -count=1 -v -run 'TestAuditT1DedicatedLoginRolePassesContract' ./internal/database/

import (
	"context"
	"net/url"
	"os"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// t1LoginRole creates the login role name with the extra attributes (for
// example "BYPASSRLS"), grants it reticora_app and returns a DSN that logs
// in as it. The role is dropped when the test ends.
func t1LoginRole(t *testing.T, f *scopetest.Fixture, name, attributes string) string {
	t.Helper()
	ctx := context.Background()
	const password = "t1-audit-login-secret"
	drop := func() {
		// Pooled sessions of the role must end before it can be dropped.
		_, _ = f.Admin.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = $1`, name)
		_, _ = f.Admin.Exec(ctx, `DROP ROLE IF EXISTS `+name)
	}
	drop()
	t.Cleanup(drop)
	for _, stmt := range []string{
		`CREATE ROLE ` + name + ` LOGIN NOSUPERUSER ` + attributes + ` PASSWORD '` + password + `'`,
		`GRANT ` + database.DefaultAppRole + ` TO ` + name,
	} {
		if _, err := f.Admin.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	u, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(name, password)
	return u.String()
}

func TestAuditT1DedicatedLoginRolePassesContract(t *testing.T) {
	f := scopetest.Seed(t, "e8")
	ctx := context.Background()
	dsn := t1LoginRole(t, f, "audit_t1_wp064_login", "NOBYPASSRLS")

	pool, err := database.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPool with a NOSUPERUSER/NOBYPASSRLS login that is a member of %s: %v", database.DefaultAppRole, err)
	}
	defer pool.Close()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	var current, session string
	var super, bypass bool
	if err := conn.QueryRow(ctx, `SELECT current_user::text, session_user::text`).Scan(&current, &session); err != nil {
		t.Fatal(err)
	}
	if current != database.DefaultAppRole || session != "audit_t1_wp064_login" {
		t.Fatalf("pooled session runs as %s (login %s), want %s (login audit_t1_wp064_login)", current, session, database.DefaultAppRole)
	}
	if _, err := conn.Exec(ctx, `RESET ROLE`); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&super, &bypass); err != nil {
		t.Fatal(err)
	}
	if super || bypass {
		t.Errorf("RESET ROLE on the dedicated login regains superuser=%v bypassrls=%v", super, bypass)
	}
	// The pool must not reuse the connection that left the application role.
	_ = conn.Conn().Close(ctx)
}
