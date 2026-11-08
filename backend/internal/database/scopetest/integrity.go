package scopetest

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// integrityScript is tests/integrity-check.sql, resolved relative to this
// file: the working directory of a test is its package directory, which
// differs from package to package.
func integrityScript(t testing.TB) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("scopetest: cannot resolve the path of tests/integrity-check.sql")
	}
	// backend/internal/database/scopetest → repository root.
	path := filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "tests", "integrity-check.sql")
	script, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("scopetest: %v", err)
	}
	return string(script)
}

// IntegrityViolations runs tests/integrity-check.sql for the organization
// orgID and returns the checks with violations and their counts (ABN-03,
// WP-207, WP-328). pool is a pool without row level security, as a rule
// Fixture.Admin: the script reads every table of the organization. It runs
// in a transaction of its own that sets integrity.org (set_config, local to
// the transaction) and is rolled back. A script that returns no row fails
// the test, as does any error.
func IntegrityViolations(t testing.TB, pool *pgxpool.Pool, orgID string) map[string]int {
	t.Helper()
	script := integrityScript(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("integrity check: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('integrity.org', $1, true)`, orgID); err != nil {
		t.Fatalf("integrity check: %v", err)
	}
	rows, err := tx.Query(ctx, script)
	if err != nil {
		t.Fatalf("integrity check: %v", err)
	}
	defer rows.Close()
	out := map[string]int{}
	checks := 0
	for rows.Next() {
		var name string
		var n int
		if scanErr := rows.Scan(&name, &n); scanErr != nil {
			t.Fatalf("integrity check: %v", scanErr)
		}
		checks++
		if n > 0 {
			out[name] = n
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatalf("integrity check: %v", err)
	}
	if checks == 0 {
		t.Fatal("integrity check returned no checks")
	}
	return out
}
