package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
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
	app, err := database.NewPool(ctx, scopetest.AppDSN(t))
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

// TestOperatorGrantIsAtomicWithItsAuditEntry covers WP-564 (SEC-07, ENT-04,
// F-208) against PostgreSQL. A failing auditor (operator_audit refuses the
// entries of one operator through a test constraint, so the insert fails
// after the entitlement row was written in the same transaction): the
// grant answers 5xx and the entitlement row stays unchanged; /admin/me of
// that operator answers 500 instead of its identity. A working auditor:
// the grant answers 200, the row changes, exactly one entitlement.grant
// entry with the previous and the new state is written, and the chain
// verifies.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestOperatorGrantIsAtomicWithItsAuditEntry(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	f := scopetest.Seed(t, "66")
	bg := context.Background()
	const failing = "op-wp564-audit-fails"
	dropProbe := func() {
		if _, err := f.Admin.Exec(bg, `ALTER TABLE operator_audit DROP CONSTRAINT IF EXISTS wp564_audit_fails`); err != nil {
			t.Fatal(err)
		}
	}
	dropProbe()
	if _, err := f.Admin.Exec(bg, `ALTER TABLE operator_audit ADD CONSTRAINT wp564_audit_fails
		CHECK (operator_id <> '`+failing+`') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dropProbe)

	issuer := testIssuer(t)
	auditor := NewAuditor(f.App)
	svc := entitlement.NewService(entitlement.NewPGRepository(f.App), entitlement.Options{Enforce: true})
	mux := testRouter(NewHandler(Config{BreakGlassToken: testBreakGlass}, nil, issuer, auditor, f.App).WithEntitlements(svc))
	session := func(subject string) map[string]string {
		t.Helper()
		now := time.Now().UTC()
		token, err := issuer.Issue(identity.SessionClaims{Subject: subject, Operator: true, IssuedAt: now, ExpiresAt: now.Add(time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		return map[string]string{"Authorization": "Bearer " + token}
	}
	path := "/api/v1/admin/orgs/" + f.OrgA + "/entitlements"
	stored := func() int64 {
		t.Helper()
		var n int64
		if err := f.Admin.QueryRow(bg, `SELECT (limits->>'max_collectors')::bigint FROM entitlement
			WHERE organization_id = $1 AND feature_key = 'discovery'`, f.OrgA).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if w := post(mux, path, `{"feature_key":"discovery","limits":{"max_collectors":3}}`, session("op-1")); w.Code != http.StatusOK {
		t.Fatalf("first grant: %d %s", w.Code, w.Body.String())
	}
	var mark int64
	if err := f.Admin.QueryRow(bg, `SELECT COALESCE(max(id), 0) FROM operator_audit`).Scan(&mark); err != nil {
		t.Fatal(err)
	}

	// Failing auditor: no change without its entry.
	if w := post(mux, path, `{"feature_key":"discovery","limits":{"max_collectors":9}}`, session(failing)); w.Code < 500 {
		t.Errorf("grant with a failing audit: %d %s, want 5xx", w.Code, w.Body.String())
	}
	if n := stored(); n != 3 {
		t.Errorf("grant with a failing audit changed the entitlement: max_collectors %d, want 3", n)
	}
	if w := get(mux, "/api/v1/admin/me", session(failing)); w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), failing) {
		t.Errorf("/admin/me with a failing audit: %d %s, want 500 without the identity", w.Code, w.Body.String())
	}

	// Working auditor: the change and exactly one entry.
	if w := post(mux, path, `{"feature_key":"discovery","limits":{"max_collectors":7}}`, session("op-1")); w.Code != http.StatusOK {
		t.Fatalf("grant: %d %s", w.Code, w.Body.String())
	}
	if n := stored(); n != 7 {
		t.Errorf("grant not stored: max_collectors %d, want 7", n)
	}
	type row struct {
		operator, action, target string
		status                   int
		details                  []byte
	}
	var rows []row
	r, err := f.Admin.Query(bg, `SELECT operator_id, action, COALESCE(target_org::text, ''), status, details
		FROM operator_audit WHERE id > $1 ORDER BY id`, mark)
	if err != nil {
		t.Fatal(err)
	}
	for r.Next() {
		var x row
		if err = r.Scan(&x.operator, &x.action, &x.target, &x.status, &x.details); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, x)
	}
	r.Close()
	if len(rows) != 1 || rows[0].operator != "op-1" || rows[0].action != "entitlement.grant" || rows[0].target != f.OrgA || rows[0].status != http.StatusOK {
		t.Fatalf("operator audit after the failed and the stored grant: %+v, want one entitlement.grant of op-1", rows)
	}
	var details struct {
		Before entitlement.Entitlement `json:"before"`
		After  entitlement.Entitlement `json:"after"`
	}
	if err = json.Unmarshal(rows[0].details, &details); err != nil {
		t.Fatal(err)
	}
	if details.Before.Limits[entitlement.LimitMaxCollectors] != 3 || details.After.Limits[entitlement.LimitMaxCollectors] != 7 {
		t.Errorf("audit details %s, want before 3 and after 7", rows[0].details)
	}
	if result, err := auditor.Verify(bg); err != nil || !result.Valid {
		t.Errorf("verify: %+v, %v", result, err)
	}
}

// TestOperatorAuditRecordsAnyRequestPath covers WP-663 (F-392, SEC-07,
// API-03) against PostgreSQL: an /admin request whose path holds a NUL
// byte or invalid UTF-8 (%00, %FF, %C3%28) is answered like any other
// (401 anonymously, 4xx for an operator) and recorded, instead of 500 "the
// operator audit entry could not be written" (SQLSTATE 22021 from the
// action) with an ERROR log. The action names the path percent-encoded,
// so it stays text and tells %00 from a literal "%00" (%2500); a target
// organization that is no UUID is not recorded (the action names it)
// instead of failing the uuid column (SQLSTATE 22P02), and an upper-case
// one is recorded lower-case, as the column returns it, so the entry
// verifies. The chain verifies.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL).
func TestOperatorAuditRecordsAnyRequestPath(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()
	app, err := database.NewPool(ctx, scopetest.AppDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	admin, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	auditor := NewAuditor(app)
	svc := entitlement.NewService(entitlement.NewPGRepository(app), entitlement.Options{Enforce: true})
	mux := testRouter(NewHandler(Config{BreakGlassToken: testBreakGlass}, nil, testIssuer(t), auditor, app).WithEntitlements(svc))
	var mark int64
	if err = admin.QueryRow(ctx, `SELECT COALESCE(max(id), 0) FROM operator_audit`).Scan(&mark); err != nil {
		t.Fatal(err)
	}

	const unknown = "00000000-0000-4000-8000-0000000000fd"
	const noUUID = "zzzzzzzz-zzzz-zzzz-zzzz-zzzzzzzzzzzz"
	glass := map[string]string{BreakGlassHeader: testBreakGlass}
	type row struct {
		operator, action, target string
		status                   int
	}
	var want []row
	for _, c := range []struct {
		method, path string
		header       map[string]string
		status       int
		action       string
		target       string
	}{
		{http.MethodGet, "/api/v1/admin/orgs/%00/entitlements", nil, http.StatusUnauthorized, "GET /orgs/%00/entitlements", ""},
		{http.MethodPost, "/api/v1/admin/orgs/%FF/entitlements", nil, http.StatusUnauthorized, "POST /orgs/%FF/entitlements", ""},
		{http.MethodGet, "/api/v1/admin/orgs/x%00y/entitlements", glass, 0, "GET /orgs/x%00y/entitlements", ""},
		{http.MethodGet, "/api/v1/admin/orgs/%C3%28/entitlements", glass, 0, "GET /orgs/%C3%28/entitlements", ""},
		{http.MethodGet, "/api/v1/admin/orgs/%2500/entitlements", glass, http.StatusNotFound, "GET /orgs/%2500/entitlements", ""},
		{http.MethodGet, "/api/v1/admin/orgs/" + noUUID + "/entitlements", glass, http.StatusNotFound, "GET /orgs/" + noUUID + "/entitlements", ""},
		{http.MethodGet, "/api/v1/admin/orgs/" + unknown + "/entitlements", glass, http.StatusNotFound, "GET /orgs/" + unknown + "/entitlements", unknown},
		// An upper-case id is stored lower-case; the entry hashes so.
		{http.MethodGet, "/api/v1/admin/orgs/" + strings.ToUpper(unknown) + "/entitlements", glass, http.StatusNotFound,
			"GET /orgs/" + strings.ToUpper(unknown) + "/entitlements", unknown},
	} {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(`{}`))
		for k, v := range c.header {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if c.status != 0 && w.Code != c.status || w.Code < 400 || w.Code >= 500 {
			t.Errorf("%s %s = %d %s; want a client error (%d), not a failed operator audit", c.method, c.path, w.Code,
				strings.TrimSpace(w.Body.String()), c.status)
		}
		operator := "anonymous"
		if c.header != nil {
			operator = "break-glass"
		}
		want = append(want, row{operator, c.action, c.target, w.Code})
	}
	if strings.Contains(logs.String(), "operator audit failed") {
		t.Errorf("an /admin request with an invalid path failed its operator audit:\n%s", logs.String())
	}

	var rows []row
	r, err := admin.Query(ctx, `SELECT operator_id, action, COALESCE(target_org::text, ''), status
		FROM operator_audit WHERE id > $1 ORDER BY id`, mark)
	if err != nil {
		t.Fatal(err)
	}
	for r.Next() {
		var x row
		if err = r.Scan(&x.operator, &x.action, &x.target, &x.status); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, x)
	}
	r.Close()
	if !slices.Equal(rows, want) {
		t.Errorf("operator audit rows\n%+v\nwant\n%+v", rows, want)
	}
	if result, verr := auditor.Verify(ctx); verr != nil || !result.Valid {
		t.Errorf("verify: %+v, %v", result, verr)
	}
}
