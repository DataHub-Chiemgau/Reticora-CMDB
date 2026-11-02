package scopetest_test

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// escalations lists every role reachable from session_user, the login
// itself included, that is SUPERUSER or BYPASSRLS. Memberships are followed
// whatever their INHERIT and SET options: a RESET ROLE or SET ROLE on a pooled
// connection may lead to any of them.
func escalations(ctx context.Context, t *testing.T, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}) []string {
	t.Helper()
	rows, err := q.Query(ctx, `
		WITH RECURSIVE reachable(oid) AS (
			SELECT r.oid FROM pg_roles r WHERE r.rolname = session_user
			UNION
			SELECT m.roleid FROM pg_auth_members m JOIN reachable ON m.member = reachable.oid
		)
		SELECT r.rolname FROM reachable JOIN pg_roles r ON r.oid = reachable.oid
		 WHERE r.rolsuper OR r.rolbypassrls
		 ORDER BY 1`)
	if err != nil {
		t.Fatalf("reachable roles: %v", err)
	}
	roles, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("reachable roles: %v", err)
	}
	return roles
}

// TestAppDSNIsNotEscalatable covers WP-349 (TEN-03, F-057): the application
// pools of the integration tests log in as a role without SUPERUSER and
// BYPASSRLS that reaches no role with these attributes, so a RESET ROLE on a
// pooled connection does not lift RLS. The admin DSN (TEST_DATABASE_URL) is
// the negative control: the same check detects it as escalatable. With
// TEST_APP_DATABASE_URL set (CI) the login of the DB job is checked, and the
// login AppDSN derives without it in a second run.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL).
func TestAppDSNIsNotEscalatable(t *testing.T) {
	adminDSN := os.Getenv("TEST_DATABASE_URL")
	if adminDSN == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()

	check := func(t *testing.T) {
		dsn := scopetest.AppDSN(t)
		if again := scopetest.AppDSN(t); again != dsn {
			t.Fatalf("AppDSN is not stable: %q, then %q", dsn, again)
		}
		if dsn == adminDSN {
			t.Error("AppDSN returns the admin DSN TEST_DATABASE_URL")
		}
		pool, err := database.NewPool(ctx, dsn)
		if err != nil {
			t.Fatalf("application pool: %v", err)
		}
		defer pool.Close()
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Release()

		var current, session string
		var super, bypass bool
		if err = conn.QueryRow(ctx, `
			SELECT current_user::text, session_user::text, r.rolsuper, r.rolbypassrls
			  FROM pg_roles r WHERE r.rolname = session_user`).Scan(&current, &session, &super, &bypass); err != nil {
			t.Fatal(err)
		}
		if current != database.DefaultAppRole {
			t.Errorf("pooled connection runs as %s, want %s", current, database.DefaultAppRole)
		}
		if super || bypass {
			t.Errorf("session_user %s has rolsuper = %t, rolbypassrls = %t; want both false", session, super, bypass)
		}
		if got := escalations(ctx, t, conn); len(got) > 0 {
			t.Errorf("session_user %s reaches SUPERUSER/BYPASSRLS roles %v", session, got)
		}
		rows, err := conn.Query(ctx, `
			SELECT m.roleid::regrole::text FROM pg_auth_members m
			 WHERE m.member = (SELECT oid FROM pg_roles WHERE rolname = session_user) ORDER BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		memberOf, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(memberOf, []string{database.DefaultAppRole}) {
			t.Errorf("session_user %s is a member of %v, want only %s", session, memberOf, database.DefaultAppRole)
		}

		// A RESET ROLE returns to the login, which still cannot bypass RLS.
		if _, err = conn.Exec(ctx, `RESET ROLE`); err != nil {
			t.Fatal(err)
		}
		if err = conn.QueryRow(ctx, `
			SELECT current_user::text, r.rolsuper, r.rolbypassrls
			  FROM pg_roles r WHERE r.rolname = current_user`).Scan(&current, &super, &bypass); err != nil {
			t.Fatal(err)
		}
		if current != session || super || bypass {
			t.Errorf("after RESET ROLE: current_user %s (rolsuper = %t, rolbypassrls = %t), want %s without both", current, super, bypass, session)
		}
		// The connection must not go back to the pool as the login.
		_ = conn.Conn().Close(ctx)
	}

	t.Run("AppDSN", check)
	if os.Getenv("TEST_APP_DATABASE_URL") != "" {
		t.Run("DerivedLogin", func(t *testing.T) {
			t.Setenv("TEST_APP_DATABASE_URL", "")
			check(t)
		})
	}

	// Negative control: the admin login is escalatable and detected as such.
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if got := escalations(ctx, t, admin); len(got) == 0 {
		t.Error("the admin DSN TEST_DATABASE_URL is not detected as escalatable; the check proves nothing")
	}
}
