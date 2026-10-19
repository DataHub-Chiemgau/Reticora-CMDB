//go:build auditdefect

package database_test

// Phase-1 acceptance audit, group T1-tenancy: DEFECT test (fails until the
// product is fixed).
//
// WP-064 (TEN-03, DB-04; CH19; acceptance criterion 3 "Startprüfung
// verifiziert effektive Rechte (inkl. geerbter) und verweigert den Start bei
// Abweichung") P2 "Server logs in as a SUPERUSER in the packaged install;
// the startup role check only inspects current_user and accepts it, so
// RESET ROLE escapes RLS": role_check.go seeds the reachable roles with
// current_user and VerifyRLSEnforced checks current_user only; the session
// (login) role is never checked. TEST_DATABASE_URL logs in as the bootstrap
// superuser exactly like deploy/docker-compose (RETICORA_DATABASE_URL with
// POSTGRES_USER), and NewPool accepts it.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -tags auditdefect -count=1 -v -run 'TestAuditT1StartupRefusesEscalatableLogin' ./internal/database/

import (
	"context"
	"os"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

func TestAuditT1StartupRefusesEscalatableLogin(t *testing.T) {
	f := scopetest.Seed(t, "e9")
	ctx := context.Background()
	var loginSuper bool
	if err := f.Admin.QueryRow(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname = session_user`).Scan(&loginSuper); err != nil {
		t.Fatal(err)
	}
	if !loginSuper {
		t.Fatalf("setup: the login of TEST_DATABASE_URL is not a superuser; this test needs the superuser login of the packaged install")
	}
	logins := map[string]string{
		"superuser login (packaged install)": os.Getenv("TEST_DATABASE_URL"),
		"BYPASSRLS login":                    t1LoginRole(t, f, "audit_t1_wp064_bypass", "BYPASSRLS"),
	}
	for name, dsn := range logins {
		t.Run(name, func(t *testing.T) {
			pool, err := database.NewPool(ctx, dsn)
			if err == nil {
				defer pool.Close()
				t.Errorf("TEN-03/CH19: NewPool accepted a session whose login role regains SUPERUSER/BYPASSRLS via RESET ROLE; want a refusal naming the session user")
				conn, aerr := pool.Acquire(ctx)
				if aerr != nil {
					t.Fatal(aerr)
				}
				defer conn.Release()
				var super, bypass bool
				if _, err := conn.Exec(ctx, `RESET ROLE`); err != nil {
					t.Fatal(err)
				}
				if err := conn.QueryRow(ctx, `SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&super, &bypass); err != nil {
					t.Fatal(err)
				}
				t.Logf("evidence: after RESET ROLE on a pooled connection current_user has superuser=%v bypassrls=%v", super, bypass)
				_ = conn.Conn().Close(ctx)
			}
		})
	}
}
