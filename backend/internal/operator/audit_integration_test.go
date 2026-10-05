package operator

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
)

// TestOperatorAuditChain covers WP-070 (SEC-07) against PostgreSQL: every
// /admin request, refused ones included, lands in operator_audit with the
// operator and its kind; the hash chain verifies and detects a changed row;
// the application role cannot change rows; tenant transactions neither read
// nor write the table.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL).
func TestOperatorAuditChain(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()
	app, err := database.NewPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	admin, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()

	auditor := NewAuditor(app)
	var lastID int64
	if err = admin.QueryRow(ctx, `SELECT COALESCE(max(id), 0) FROM operator_audit`).Scan(&lastID); err != nil {
		t.Fatal(err)
	}

	mux := testRouter(NewHandler(Config{BreakGlassToken: testBreakGlass}, nil, testIssuer(t), auditor, app))
	if w := get(mux, "/api/v1/admin/orgs", map[string]string{BreakGlassHeader: testBreakGlass}); w.Code != http.StatusOK {
		t.Fatalf("list orgs: %d %s", w.Code, w.Body.String())
	}
	if w := get(mux, "/api/v1/admin/audit", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous audit read: %d", w.Code)
	}

	type row struct {
		operator, kind, action string
		status                 int
	}
	var rows []row
	r, err := admin.Query(ctx, `SELECT operator_id, operator_kind, action, status FROM operator_audit WHERE id > $1 ORDER BY id`, lastID)
	if err != nil {
		t.Fatal(err)
	}
	for r.Next() {
		var x row
		if err = r.Scan(&x.operator, &x.kind, &x.action, &x.status); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, x)
	}
	r.Close()
	want := []row{{"break-glass", KindBreakGlass, "GET /orgs", 200}, {"anonymous", KindOIDC, "GET /audit", 401}}
	if len(rows) != len(want) || rows[0] != want[0] || rows[1] != want[1] {
		t.Fatalf("operator audit rows %+v, want %+v", rows, want)
	}

	result, err := auditor.Verify(ctx)
	if err != nil || !result.Valid || result.Checked < 2 {
		t.Fatalf("verify: %+v, %v", result, err)
	}

	// The application role cannot change rows, even in the operator context.
	err = database.WithOperator(ctx, app, func(ctx context.Context, tx pgx.Tx) error {
		_, execErr := tx.Exec(ctx, `UPDATE operator_audit SET action = 'forged' WHERE id = (SELECT max(id) FROM operator_audit)`)
		return execErr
	})
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("update by the application role: %v, want permission denied", err)
	}

	// Tenant transactions see nothing and cannot write.
	scope := database.OrgWideScope("11111111-1111-4111-8111-111111111111", "")
	err = database.WithTenant(ctx, app, &scope, func(ctx context.Context, tx pgx.Tx) error {
		var n int
		if scanErr := tx.QueryRow(ctx, `SELECT count(*) FROM operator_audit`).Scan(&n); scanErr != nil {
			return scanErr
		}
		if n != 0 {
			t.Errorf("tenant transaction reads %d operator audit rows", n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = database.WithTenant(ctx, app, &scope, func(ctx context.Context, tx pgx.Tx) error {
		_, execErr := tx.Exec(ctx, `INSERT INTO operator_audit (timestamp, operator_id, operator_kind, action, status, previous_hash, entry_hash)
			VALUES (now(), 'x', 'oidc', 'x', 200, 'x', 'forged-by-tenant')`)
		return execErr
	})
	if err == nil || !strings.Contains(err.Error(), "row-level security") {
		t.Errorf("tenant insert into operator_audit: %v, want a row level security violation", err)
	}

	// A changed row breaks the chain at that row; restoring it heals it.
	var target int64
	var action string
	if err = admin.QueryRow(ctx, `SELECT id, action FROM operator_audit WHERE id > $1 ORDER BY id LIMIT 1`, lastID).Scan(&target, &action); err != nil {
		t.Fatal(err)
	}
	tamper := func(value string) {
		t.Helper()
		tx, txErr := admin.Begin(ctx)
		if txErr != nil {
			t.Fatal(txErr)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, txErr = tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); txErr != nil {
			t.Fatal(txErr)
		}
		if _, txErr = tx.Exec(ctx, `UPDATE operator_audit SET action = $2 WHERE id = $1`, target, value); txErr != nil {
			t.Fatal(txErr)
		}
		if txErr = tx.Commit(ctx); txErr != nil {
			t.Fatal(txErr)
		}
	}
	tamper("GET /forged")
	if result, err = auditor.Verify(ctx); err != nil || result.Valid || result.Broken == nil || *result.Broken != target {
		t.Errorf("verify after tampering: %+v, %v; want broken at %d", result, err, target)
	}
	tamper(action)
	if result, err = auditor.Verify(ctx); err != nil || !result.Valid {
		t.Errorf("verify after restoring: %+v, %v", result, err)
	}
}
