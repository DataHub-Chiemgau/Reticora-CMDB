package operator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

func post(mux http.Handler, path, body string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	for k, v := range header {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// TestEntitlementsAreWrittenByOperatorsOnly covers WP-074 (ENT-04, E-12,
// API-05): the tenant API is read-only, even for a principal holding every
// permission (org_admin with entitlement:manage); entitlements are written
// under /api/v1/admin/orgs/{id}/entitlements by operators only.
func TestEntitlementsAreWrittenByOperatorsOnly(t *testing.T) {
	const org = "11111111-1111-4111-8111-111111111111"
	svc := entitlement.NewService(entitlement.NewMemoryRepository(), entitlement.Options{Enforce: true})
	issuer := testIssuer(t)
	mux := testRouter(NewHandler(Config{BreakGlassToken: testBreakGlass}, nil, issuer, NewAuditor(nil), nil).WithEntitlements(svc))
	path := "/api/v1/admin/orgs/" + org + "/entitlements"
	body := `{"feature_key":"ticketing","plan":"standard","limits":{"max_cis":2500},"source":"billing"}`

	// Tenant API: no write route, whatever the permissions.
	tenantMux := chi.NewRouter()
	entitlement.NewHandler(svc).RegisterRoutes(tenantMux)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/entitlements", strings.NewReader(body))
	ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: org})
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: "admin", OrganizationID: org,
		Permissions: identity.AllPermissions(), Type: identity.PrincipalTypeUser})
	w := httptest.NewRecorder()
	tenantMux.ServeHTTP(w, req.WithContext(ctx))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("tenant write with every permission: %d, want 405", w.Code)
	}

	now := time.Now().UTC()
	tenantToken, err := issuer.Issue(identity.SessionClaims{Subject: "admin", OrganizationID: org,
		Permissions: identity.AllPermissions(), IssuedAt: now, ExpiresAt: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if w = post(mux, path, body, map[string]string{"Authorization": "Bearer " + tenantToken}); w.Code != http.StatusUnauthorized {
		t.Fatalf("tenant session on the operator path: %d, want 401", w.Code)
	}
	if svc.IsEnabled(context.Background(), org, entitlement.FeatureTicketing) {
		t.Fatal("refused write changed the entitlement")
	}

	operatorToken, err := issuer.Issue(identity.SessionClaims{Subject: "op-1", Operator: true, IssuedAt: now, ExpiresAt: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	auth := map[string]string{"Authorization": "Bearer " + operatorToken}
	if w = post(mux, path, body, auth); w.Code != http.StatusOK {
		t.Fatalf("operator grant: %d %s", w.Code, w.Body.String())
	}
	if !svc.IsEnabled(context.Background(), org, entitlement.FeatureTicketing) {
		t.Error("operator grant not effective")
	}
	if w = post(mux, path, `{"feature_key":"cmdb_core","enabled":false}`, auth); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("disabling cmdb_core: %d, want 422", w.Code)
	}
	if w = get(mux, path, auth); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"feature_key":"ticketing"`) {
		t.Errorf("operator list: %d %s", w.Code, w.Body.String())
	}

	// The reviewed route table has no tenant entitlement write.
	reviewed, err := os.ReadFile("../server/testdata/route_permissions.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(reviewed), "\n") {
		if strings.HasPrefix(line, "POST /api/v1/entitlements") || strings.HasPrefix(line, "PUT /api/v1/entitlements") {
			t.Errorf("tenant entitlement write route: %s", line)
		}
	}
}

// TestOperatorEntitlementAudit covers WP-074 (ENT-04) against PostgreSQL:
// an operator grant is recorded in operator_audit with the target
// organization and the previous and new state; an unknown organization is
// 404.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestOperatorEntitlementAudit(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	f := scopetest.Seed(t, "66")
	bg := context.Background()
	svc := entitlement.NewService(entitlement.NewPGRepository(f.App), entitlement.Options{Enforce: true})
	mux := testRouter(NewHandler(Config{BreakGlassToken: testBreakGlass}, nil, testIssuer(t), NewAuditor(f.App), f.App).WithEntitlements(svc))
	auth := map[string]string{BreakGlassHeader: testBreakGlass}

	if w := post(mux, "/api/v1/admin/orgs/"+f.OrgA+"/entitlements", `{"feature_key":"discovery","limits":{"max_collectors":3}}`, auth); w.Code != http.StatusOK {
		t.Fatalf("first grant: %d %s", w.Code, w.Body.String())
	}
	if w := post(mux, "/api/v1/admin/orgs/"+f.OrgA+"/entitlements", `{"feature_key":"discovery","limits":{"max_collectors":7}}`, auth); w.Code != http.StatusOK {
		t.Fatalf("second grant: %d %s", w.Code, w.Body.String())
	}
	if w := post(mux, "/api/v1/admin/orgs/00000000-0000-4000-8000-0000000000ff/entitlements", `{"feature_key":"discovery"}`, auth); w.Code != http.StatusNotFound {
		t.Errorf("unknown organization: %d, want 404", w.Code)
	}

	scope := database.OrgWideScope(f.OrgA, "")
	if q, err := svc.Quota(database.ContextWithTenantScope(bg, &scope), f.OrgA, entitlement.LimitMaxCollectors); err != nil || q == nil || *q != 7 {
		t.Fatalf("stored quota %v, %v", q, err)
	}

	var raw []byte
	err := f.Admin.QueryRow(bg, `SELECT details FROM operator_audit
		WHERE action = 'entitlement.grant' AND target_org = $1 ORDER BY id DESC LIMIT 1`, f.OrgA).Scan(&raw)
	if err != nil {
		t.Fatalf("operator audit entry: %v", err)
	}
	var details struct {
		Before entitlement.Entitlement `json:"before"`
		After  entitlement.Entitlement `json:"after"`
	}
	if err = json.Unmarshal(raw, &details); err != nil {
		t.Fatal(err)
	}
	if details.Before.Limits[entitlement.LimitMaxCollectors] != 3 || details.After.Limits[entitlement.LimitMaxCollectors] != 7 {
		t.Errorf("audit details %s", raw)
	}
}
