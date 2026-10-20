package signup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/operator"
)

// TestSignupCreatesOrganizationAtomically covers WP-111 (TLC-01, API-05)
// against PostgreSQL with the application role: a verified signup creates
// the organization with slug, default VRF, the first user as org_admin, the
// trial entitlements and an operator_audit entry; a taken slug gets a
// suffix; a failing IdP rolls everything back; signup_request is invisible
// to tenant transactions.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL).
func TestSignupCreatesOrganizationAtomically(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()
	appPool, err := database.NewPool(ctx, scopetest.AppDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer appPool.Close()
	adminPool, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()

	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	orgName := "Signup Test " + hex.EncodeToString(suffix)
	var created []string
	t.Cleanup(func() {
		for _, id := range created {
			_, _ = adminPool.Exec(context.Background(), `DELETE FROM organization WHERE id = $1`, id)
		}
		_, _ = adminPool.Exec(context.Background(), `DELETE FROM signup_request WHERE organization_name = $1`, orgName)
	})

	mailer, accounts := &stubMailer{}, &stubAccounts{}
	cfg := DefaultConfig()
	cfg.VerifyURL = "https://app.example/signup/verify"
	svc := NewService(NewPGRepository(appPool), mailer, accounts, cfg)

	signup := func(email string) (string, error) {
		t.Helper()
		if err = svc.Submit(ctx, Request{OrganizationName: orgName, Email: email, DisplayName: "Ada"}); err != nil {
			t.Fatalf("Submit: %v", err)
		}
		return tokenOf(t, mailer.link), nil
	}

	token, _ := signup("ada@signup.example")
	res, err := svc.Verify(ctx, Verification{Token: token, Password: "correct horse battery"})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	created = append(created, res.OrganizationID)
	if res.OrganizationSlug != Slugify(orgName) {
		t.Errorf("slug = %q, want %q", res.OrganizationSlug, Slugify(orgName))
	}

	// Organization, trial settings and default VRF.
	var (
		slug, plan string
		settings   map[string]any
		vrfs       int
	)
	if err = adminPool.QueryRow(ctx, `SELECT slug, plan, settings FROM organization WHERE id = $1`, res.OrganizationID).
		Scan(&slug, &plan, &settings); err != nil {
		t.Fatalf("organization: %v", err)
	}
	if slug != res.OrganizationSlug || plan != "standard" {
		t.Errorf("organization slug/plan = %s/%s", slug, plan)
	}
	if trial, _ := settings["trial"].(map[string]any); trial["fallback_plan"] != "essential" || trial["ends_at"] == nil {
		t.Errorf("trial settings = %v", settings)
	}
	if err = adminPool.QueryRow(ctx, `SELECT count(*) FROM vrf WHERE organization_id = $1 AND is_default`, res.OrganizationID).Scan(&vrfs); err != nil || vrfs != 1 {
		t.Errorf("default VRFs = %d (%v)", vrfs, err)
	}

	// First user: org_admin, linked to the IdP on first login by address.
	var (
		email   string
		subject *string
		role    string
	)
	if err = adminPool.QueryRow(ctx, `SELECT u.email, u.oidc_subject, r.name FROM app_user u
		JOIN role_assignment ra ON ra.user_id = u.id JOIN role r ON r.id = ra.role_id
		WHERE u.organization_id = $1`, res.OrganizationID).Scan(&email, &subject, &role); err != nil {
		t.Fatalf("first user: %v", err)
	}
	if email != "ada@signup.example" || subject != nil || role != "org_admin" {
		t.Errorf("first user = %s/%v/%s", email, subject, role)
	}
	if len(accounts.created) != 1 || accounts.created[0].OrganizationID != res.OrganizationID {
		t.Errorf("IdP accounts = %+v", accounts.created)
	}

	// Trial entitlements.
	rows, err := adminPool.Query(ctx, `SELECT feature_key, plan, source, limits, valid_until FROM entitlement WHERE organization_id = $1`, res.OrganizationID)
	if err != nil {
		t.Fatal(err)
	}
	ents := map[string]bool{}
	for rows.Next() {
		var (
			key, entPlan, source string
			limits               map[string]int64
			validUntil           *time.Time
		)
		if err = rows.Scan(&key, &entPlan, &source, &limits, &validUntil); err != nil {
			t.Fatal(err)
		}
		ents[key] = true
		if entPlan != "standard" || source != "selfsignup" {
			t.Errorf("%s: plan %s source %s", key, entPlan, source)
		}
		if key == entitlement.FeatureCMDBCore && (limits[entitlement.LimitMaxCIs] != 100 || validUntil != nil) {
			t.Errorf("cmdb_core limits %v valid_until %v", limits, validUntil)
		}
		if key == entitlement.FeatureTicketing && (validUntil == nil || !validUntil.Equal(res.TrialEndsAt)) {
			t.Errorf("ticketing valid_until = %v, want %v", validUntil, res.TrialEndsAt)
		}
	}
	rows.Close()
	if len(ents) != len(entitlement.PlanFeatures(entitlement.PlanStandard)) {
		t.Errorf("entitlements = %v", ents)
	}

	// operator_audit entry without the address; the chain still verifies.
	var (
		kind    string
		details map[string]any
		reqID   string
	)
	if err = adminPool.QueryRow(ctx, `SELECT operator_kind, details, operator_id FROM operator_audit
		WHERE action = $1 AND target_org = $2`, AuditAction, res.OrganizationID).Scan(&kind, &details, &reqID); err != nil {
		t.Fatalf("operator_audit: %v", err)
	}
	if raw, _ := json.Marshal(details); kind != "signup" || details["slug"] != res.OrganizationSlug ||
		strings.Contains(string(raw), "ada@signup.example") {
		t.Errorf("operator_audit kind %s details %v", kind, details)
	}
	if v, verr := operator.NewAuditor(appPool).Verify(ctx); verr != nil || !v.Valid {
		t.Errorf("operator audit chain: %+v %v", v, verr)
	}
	var status string
	var completedOrg *string
	if err = adminPool.QueryRow(ctx, `SELECT status, completed_org_id::text FROM signup_request WHERE id = $1`, reqID).
		Scan(&status, &completedOrg); err != nil || status != "completed" || completedOrg == nil || *completedOrg != res.OrganizationID {
		t.Errorf("signup request = %s/%v (%v)", status, completedOrg, err)
	}
	if _, err = svc.Verify(ctx, Verification{Token: token, Password: "correct horse battery"}); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("reused token: %v", err)
	}

	// The same name again: the slug gets a suffix.
	token2, _ := signup("bob@signup.example")
	res2, err := svc.Verify(ctx, Verification{Token: token2, Password: "correct horse battery"})
	if err != nil {
		t.Fatalf("second Verify: %v", err)
	}
	created = append(created, res2.OrganizationID)
	if res2.OrganizationSlug != res.OrganizationSlug+"-2" {
		t.Errorf("second slug = %q, want %q", res2.OrganizationSlug, res.OrganizationSlug+"-2")
	}

	// A failing IdP rolls back the whole creation.
	token3, _ := signup("eve@signup.example")
	accounts.err = errors.New("idp down")
	before := countRows(ctx, t, adminPool, `SELECT count(*) FROM organization WHERE name = $1`, orgName)
	if _, err = svc.Verify(ctx, Verification{Token: token3, Password: "correct horse battery"}); err == nil {
		t.Fatal("Verify succeeded with a failing IdP")
	}
	if after := countRows(ctx, t, adminPool, `SELECT count(*) FROM organization WHERE name = $1`, orgName); after != before {
		t.Errorf("organizations after rollback = %d, want %d", after, before)
	}
	if n := countRows(ctx, t, adminPool, `SELECT count(*) FROM app_user WHERE email = 'eve@signup.example'`); n != 0 {
		t.Errorf("users after rollback = %d", n)
	}
	if n := countRows(ctx, t, adminPool, `SELECT count(*) FROM signup_request WHERE token_hash = $1 AND status = 'pending'`, hashToken(token3)); n != 1 {
		t.Errorf("request is no longer pending after rollback")
	}
	accounts.err = nil

	// Tenant transactions neither read nor write signup_request.
	scope := database.OrgWideScope(res.OrganizationID, "")
	if err = database.WithTenant(ctx, appPool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		var n int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM signup_request`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Errorf("tenant transaction sees %d signup requests", n)
		}
		sp, spErr := tx.Begin(ctx)
		if spErr != nil {
			return spErr
		}
		defer func() { _ = sp.Rollback(ctx) }()
		if _, err = sp.Exec(ctx, `INSERT INTO signup_request (email, organization_name, display_name, token_hash, expires_at)
			VALUES ('x@example.com', 'x', 'x', 'tenant-write', now())`); err == nil {
			t.Error("tenant transaction wrote a signup request")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func countRows(ctx context.Context, t *testing.T, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := q.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// TestOperatorCreatesOrganizationAtomically covers WP-113 (TLC-02) against
// PostgreSQL: an operator creates an organization with plan and limits
// through the signup's creation chain; the operator's audit entry is written
// in the same transaction; an explicit, taken slug is a conflict and leaves
// nothing behind.
func TestOperatorCreatesOrganizationAtomically(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()
	appPool, err := database.NewPool(ctx, scopetest.AppDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer appPool.Close()
	adminPool, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()

	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	slug := "op-test-" + hex.EncodeToString(suffix)
	t.Cleanup(func() {
		_, _ = adminPool.Exec(context.Background(), `DELETE FROM organization WHERE slug = $1`, slug)
	})

	accounts := &stubInviter{}
	svc := NewService(NewPGRepository(appPool), nil, accounts, DefaultConfig())
	op := operator.Identity{ID: "op-wp113", Kind: "oidc"}
	req := &operator.NewOrganization{Name: "Operator Test", Slug: slug, Plan: "pro", AdminEmail: "admin@op.example",
		Limits: map[string]int64{entitlement.LimitMaxCIs: 7000, entitlement.LimitMaxUsers: 40}}
	created, err := svc.CreateOrganization(ctx, op, req)
	if err != nil {
		t.Fatalf("CreateOrganization: %v", err)
	}
	if created.Slug != slug || !created.AdminInvited || len(accounts.invited) != 1 {
		t.Errorf("created = %+v, invited %d", created, len(accounts.invited))
	}

	var (
		plan     string
		settings map[string]any
	)
	if err = adminPool.QueryRow(ctx, `SELECT plan, settings FROM organization WHERE id = $1`, created.ID).Scan(&plan, &settings); err != nil {
		t.Fatal(err)
	}
	if plan != "pro" || settings["trial"] != nil {
		t.Errorf("organization plan %s settings %v", plan, settings)
	}
	if n := countRows(ctx, t, adminPool, `SELECT count(*) FROM vrf WHERE organization_id = $1 AND is_default`, created.ID); n != 1 {
		t.Errorf("default VRFs = %d", n)
	}
	if n := countRows(ctx, t, adminPool, `SELECT count(*) FROM app_user u JOIN role_assignment ra ON ra.user_id = u.id
		JOIN role r ON r.id = ra.role_id WHERE u.organization_id = $1 AND u.email = 'admin@op.example' AND r.name = 'org_admin'`, created.ID); n != 1 {
		t.Errorf("org_admin users = %d", n)
	}
	var limits map[string]int64
	if err = adminPool.QueryRow(ctx, `SELECT limits FROM entitlement WHERE organization_id = $1 AND feature_key = 'cmdb_core'
		AND source = 'manual' AND plan = 'pro' AND valid_until IS NULL`, created.ID).Scan(&limits); err != nil {
		t.Fatalf("cmdb_core entitlement: %v", err)
	}
	if limits[entitlement.LimitMaxCIs] != 7000 || limits[entitlement.LimitMaxUsers] != 40 {
		t.Errorf("cmdb_core limits = %v", limits)
	}
	if n := countRows(ctx, t, adminPool, `SELECT count(*) FROM entitlement WHERE organization_id = $1`, created.ID); n != len(entitlement.PlanFeatures(entitlement.PlanPro)) {
		t.Errorf("entitlements = %d", n)
	}
	var details map[string]any
	if err = adminPool.QueryRow(ctx, `SELECT details FROM operator_audit WHERE action = 'org.create' AND target_org = $1
		AND operator_id = 'op-wp113' AND operator_kind = 'oidc' AND status = 201`, created.ID).Scan(&details); err != nil {
		t.Fatalf("operator_audit: %v", err)
	}
	if details["slug"] != slug || details["plan"] != "pro" {
		t.Errorf("audit details = %v", details)
	}

	// The same slug again: conflict, no second organization, the invited
	// account is removed again.
	req.AdminEmail = "other@op.example"
	before := countRows(ctx, t, adminPool, `SELECT count(*) FROM organization`)
	if _, err = svc.CreateOrganization(ctx, op, req); !errors.Is(err, operator.ErrSlugTaken) {
		t.Fatalf("taken slug: %v, want ErrSlugTaken", err)
	}
	if after := countRows(ctx, t, adminPool, `SELECT count(*) FROM organization`); after != before {
		t.Errorf("organizations %d → %d after a conflict", before, after)
	}
	if n := countRows(ctx, t, adminPool, `SELECT count(*) FROM app_user WHERE email = 'other@op.example'`); n != 0 {
		t.Errorf("users after conflict = %d", n)
	}
}
