package database_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// testDatabaseURL returns the test database URL or skips the test when the
// environment does not provide a PostgreSQL instance (e.g. local runs
// without the CI Postgres service).
func testDatabaseURL(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	return dsn
}

// TestMigrationsApplied verifies the database reachable at TEST_DATABASE_URL
// has the migration schema applied. The CI job is expected to run
// `make migrate-up DATABASE_URL=$TEST_DATABASE_URL` before `go test`.
func TestMigrationsApplied(t *testing.T) {
	dsn := testDatabaseURL(t)
	db, err := database.Connect(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer db.Close()

	for _, table := range []string{"organization", "ci", "ci_change", "audit_log", "subnet", "ip_address", "network_interface"} {
		var name string
		err := db.QueryRowContext(context.Background(), "SELECT tablename FROM pg_tables WHERE schemaname='public' AND tablename=$1", table).Scan(&name)
		if err != nil {
			t.Fatalf("expected table %q to exist: %v", table, err)
		}
	}
}

// TestMigrationsAreSorted guards against duplicate or misordered migration
// version numbers, which would break golang-migrate.
func TestMigrationsAreSorted(t *testing.T) {
	entries, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.up.sql"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	versions := map[string]bool{}
	sorted := sort.StringsAreSorted(entries)
	if !sorted {
		t.Fatal("migration files are not lexicographically sorted")
	}
	for _, e := range entries {
		v := strings.SplitN(filepath.Base(e), "_", 2)[0]
		if versions[v] {
			t.Fatalf("duplicate migration version %s", v)
		}
		versions[v] = true
		down := strings.Replace(e, ".up.sql", ".down.sql", 1)
		if _, err := os.Stat(down); err != nil {
			t.Fatalf("missing down migration for %s", e)
		}
	}
}
