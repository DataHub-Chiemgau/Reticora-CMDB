package database_test

// Phase-1 acceptance audit, group T1-tenancy: startup check of the session
// (login) role (WP-064, WP-352; TEN-03, DB-04; CH19; F-057).
//
// TestAuditT1DedicatedLoginRolePassesContract (regression): the recommended
// runtime login (LOGIN NOSUPERUSER NOBYPASSRLS, member of reticora_app only)
// passes the startup checks of NewPool, and a RESET ROLE on such a connection
// does not regain any SUPERUSER or BYPASSRLS right.
//
// TestStartupRefusesEscalatableLogin (formerly the defect test
// TestAuditT1StartupRefusesEscalatableLogin): NewPool refuses every login
// that is SUPERUSER or BYPASSRLS or reaches such a role, inherited or by SET
// ROLE, with a RoleContractError naming the session user; a RESET ROLE would
// otherwise hand every pooled session the rights back.
//
// The tests create cluster-wide login roles with unique names and drop them
// again.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -count=1 -v -run '^(TestAuditT1DedicatedLoginRolePassesContract|TestStartupRefusesEscalatableLogin)$' ./internal/database/

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
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

// TestStartupRefusesEscalatableLogin covers F-057 (TEN-03, CH19): NewPool
// refuses a login that is SUPERUSER (the bootstrap superuser of the packaged
// install, TEST_DATABASE_URL) or BYPASSRLS, or that reaches a SUPERUSER or
// BYPASSRLS role inherited or by SET ROLE, although every pooled connection
// switched into reticora_app. The refusal is a *RoleContractError that names
// the session user.
func TestStartupRefusesEscalatableLogin(t *testing.T) {
	f := scopetest.Seed(t, "e9")
	ctx := context.Background()
	var loginName string
	var loginSuper bool
	if err := f.Admin.QueryRow(ctx, `SELECT session_user::text, rolsuper FROM pg_roles WHERE rolname = session_user`).Scan(&loginName, &loginSuper); err != nil {
		t.Fatal(err)
	}
	if !loginSuper {
		t.Fatalf("setup: the login of TEST_DATABASE_URL is not a superuser; this test needs the superuser login of the packaged install")
	}

	// NOLOGIN roles a login reaches through its memberships.
	escalating := map[string]string{
		"audit_t1_wp352_su":     "SUPERUSER",
		"audit_t1_wp352_bypass": "NOSUPERUSER BYPASSRLS",
	}
	for name, attributes := range escalating {
		drop := func() { _, _ = f.Admin.Exec(ctx, `DROP ROLE IF EXISTS `+name) }
		drop()
		if _, err := f.Admin.Exec(ctx, `CREATE ROLE `+name+` NOLOGIN `+attributes); err != nil {
			t.Fatal(err)
		}
		// Registered before the logins, so it runs after their drop.
		t.Cleanup(drop)
	}
	grant := func(role, login string, inherit bool) {
		t.Helper()
		stmt := `GRANT ` + role + ` TO ` + login + ` WITH INHERIT FALSE, SET TRUE`
		if inherit {
			stmt = `GRANT ` + role + ` TO ` + login + ` WITH INHERIT TRUE`
		}
		if _, err := f.Admin.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	setRoleSU := t1LoginRole(t, f, "audit_t1_wp352_set_su", "NOBYPASSRLS")
	grant("audit_t1_wp352_su", "audit_t1_wp352_set_su", false)
	inheritBypass := t1LoginRole(t, f, "audit_t1_wp352_inh_bypass", "NOBYPASSRLS")
	grant("audit_t1_wp352_bypass", "audit_t1_wp352_inh_bypass", true)

	cases := []struct {
		name, dsn, session, violation string
	}{
		{"superuser login (packaged install)", os.Getenv("TEST_DATABASE_URL"), loginName,
			"session user " + loginName + " is SUPERUSER"},
		{"BYPASSRLS login", t1LoginRole(t, f, "audit_t1_wp064_bypass", "BYPASSRLS"), "audit_t1_wp064_bypass",
			"session user audit_t1_wp064_bypass is BYPASSRLS"},
		{"login reaching a superuser role via SET ROLE", setRoleSU, "audit_t1_wp352_set_su",
			"role audit_t1_wp352_su (reachable from session user audit_t1_wp352_set_su via SET ROLE) is SUPERUSER"},
		{"login inheriting a BYPASSRLS role", inheritBypass, "audit_t1_wp352_inh_bypass",
			"role audit_t1_wp352_bypass (reachable from session user audit_t1_wp352_inh_bypass, inherited) is BYPASSRLS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool, err := database.NewPool(ctx, tc.dsn)
			if err == nil {
				defer pool.Close()
				t.Errorf("TEN-03/CH19: NewPool accepted a session whose login role regains SUPERUSER/BYPASSRLS via RESET ROLE or SET ROLE; want a refusal naming the session user")
				conn, aerr := pool.Acquire(ctx)
				if aerr != nil {
					t.Fatal(aerr)
				}
				defer conn.Release()
				var super, bypass bool
				if _, err = conn.Exec(ctx, `RESET ROLE`); err != nil {
					t.Fatal(err)
				}
				if err = conn.QueryRow(ctx, `SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&super, &bypass); err != nil {
					t.Fatal(err)
				}
				t.Logf("evidence: after RESET ROLE on a pooled connection current_user has superuser=%v bypassrls=%v", super, bypass)
				_ = conn.Conn().Close(ctx)
				return
			}
			var contractErr *database.RoleContractError
			if !errors.As(err, &contractErr) {
				t.Fatalf("NewPool refused with %v, want a *RoleContractError naming the session user", err)
			}
			if contractErr.SessionUser != tc.session || contractErr.Role != database.DefaultAppRole {
				t.Errorf("RoleContractError for role %q, session user %q; want %q, %q", contractErr.Role, contractErr.SessionUser, database.DefaultAppRole, tc.session)
			}
			if !strings.Contains(err.Error(), `session user "`+tc.session+`"`) {
				t.Errorf("error does not name the session user %s: %v", tc.session, err)
			}
			found := false
			for _, v := range contractErr.Violations {
				found = found || strings.HasPrefix(v, tc.violation)
			}
			if !found {
				t.Errorf("violation %q not reported; got:\n%v", tc.violation, contractErr.Violations)
			}
		})
	}
}
