package database_test

import (
	"context"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestAppPoolDisablesJIT checks WP-610 (U5 of the WP-439 load runs, NFR-02):
// the application pools run without JIT compilation. The row level security
// subqueries put the cost of the global search above jit_above_cost, and 69
// of its 75 ms went into compiling. SHOW jit is off on every connection the
// pool opens, in a tenant transaction as the application role, and also when
// the database URL asks for JIT. The runtime pool (NewPool: server, its
// workers, collector-ca) is checked as it is; the pool of the command-line
// tools (NewAdminPool: seed, audit-verify, audit-seed) through the
// construction it shares with NewPool (NewAppRolePool), as NewAdminPool
// may not be referenced under internal/.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL).
func TestAppPoolDisablesJIT(t *testing.T) {
	ctx := context.Background()
	adminDSN := testDatabaseURL(t)
	appDSN := scopetest.AppDSN(t)
	withJIT := func(dsn string) string {
		if strings.Contains(dsn, "?") {
			return dsn + "&jit=on"
		}
		return dsn + "?jit=on"
	}

	// A connection without the pool configuration gets what the URL asks
	// for, so the cases below would see JIT without disableJIT.
	maintenance, err := database.NewMaintenancePool(ctx, withJIT(adminDSN))
	if err != nil {
		t.Fatalf("maintenance pool: %v", err)
	}
	t.Cleanup(maintenance.Close)
	var jit string
	if err = maintenance.QueryRow(ctx, `SHOW jit`).Scan(&jit); err != nil || jit != "on" {
		t.Fatalf("maintenance pool from a URL with jit=on: SHOW jit = %q, %v; want on", jit, err)
	}

	for _, c := range []struct {
		name    string
		newPool func(context.Context, string) (*pgxpool.Pool, error)
		dsn     string
	}{
		{"NewPool", database.NewPool, appDSN},
		{"NewPool from a URL with jit=on", database.NewPool, withJIT(appDSN)},
		{"pool of the command-line tools", database.NewAppRolePool, adminDSN},
		{"pool of the command-line tools from a URL with jit=on", database.NewAppRolePool, withJIT(adminDSN)},
	} {
		t.Run(c.name, func(t *testing.T) {
			pool, err := c.newPool(ctx, c.dsn)
			if err != nil {
				t.Fatalf("open pool: %v", err)
			}
			defer pool.Close()

			// Every connection the pool opens, held at the same time.
			conns := make([]*pgxpool.Conn, 0, 6)
			defer func() {
				for _, conn := range conns {
					conn.Release()
				}
			}()
			for i := 0; i < cap(conns); i++ {
				conn, acquireErr := pool.Acquire(ctx)
				if acquireErr != nil {
					t.Fatalf("acquire connection %d: %v", i, acquireErr)
				}
				conns = append(conns, conn)
				if err = conn.QueryRow(ctx, `SHOW jit`).Scan(&jit); err != nil || jit != "off" {
					t.Errorf("connection %d: SHOW jit = %q, %v; want off", i, jit, err)
				}
			}

			// A tenant transaction as the application role.
			scope := database.OrgWideScope(scopeOrgA, scopeUser)
			var role string
			err = database.WithTenant(ctx, pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
				return tx.QueryRow(ctx, `SELECT current_user, current_setting('jit')`).Scan(&role, &jit)
			})
			if err != nil || role != database.DefaultAppRole || jit != "off" {
				t.Errorf("tenant transaction: current_user %q, jit %q, %v; want %s and off", role, jit, err, database.DefaultAppRole)
			}
		})
	}
}
