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
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"
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
// under /api/v1/admin/orgs/{id}/entitlements by operators only. The grant
// is recorded with its change; no audit write fails (WP-564, F-208).
func TestEntitlementsAreWrittenByOperatorsOnly(t *testing.T) {
	const org = "11111111-1111-4111-8111-111111111111"
	logs := captureLog(t)
	svc := entitlement.NewService(entitlement.NewMemoryRepository(), entitlement.Options{Enforce: true})
	issuer := testIssuer(t)
	audit := &memoryAudit{}
	mux := testRouter(withAudit(NewHandler(Config{BreakGlassToken: testBreakGlass}, nil, issuer, nil, nil).WithEntitlements(svc), audit))
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

	// The stored grant is one entry (entitlement.grant); refused requests
	// are recorded as requests.
	var grants, storedRequests int
	for _, e := range audit.entries {
		switch {
		case e.Action == "entitlement.grant" && e.Status == http.StatusOK && e.TargetOrg == org:
			grants++
		case e.Action == "POST /orgs/"+org+"/entitlements" && e.Status == http.StatusOK:
			storedRequests++
		}
	}
	if grants != 1 || storedRequests != 0 {
		t.Errorf("audit entries %+v, want one entitlement.grant and no second entry of the stored grant", audit.entries)
	}
	if strings.Contains(logs.String(), "operator audit failed") {
		t.Errorf("log: %s", logs.String())
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

// captureLog collects the default logger's output for the test.
func captureLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return buf
}

// syncBuffer is a bytes.Buffer safe for concurrent writers.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestOperatorGrantRefusesUnknownKeys covers WP-564 (ENT-04, ENT-02, F-209)
// at the HTTP boundary: a feature key outside ENT-02, the module keys and
// the add-ons, or a quota key outside max_cis, max_collectors, max_users
// and max_api_keys is 422 with the field named; nothing is stored. Every
// known key is accepted, and the specification lists exactly these keys.
func TestOperatorGrantRefusesUnknownKeys(t *testing.T) {
	const org = "11111111-1111-4111-8111-111111111111"
	svc := entitlement.NewService(entitlement.NewMemoryRepository(), entitlement.Options{Enforce: true})
	audit := &memoryAudit{}
	mux := testRouter(withAudit(NewHandler(Config{BreakGlassToken: testBreakGlass}, nil, testIssuer(t), nil, nil).WithEntitlements(svc), audit))
	path := "/api/v1/admin/orgs/" + org + "/entitlements"
	auth := map[string]string{BreakGlassHeader: testBreakGlass}

	for name, tc := range map[string]struct{ body, field string }{
		"feature key with a hyphen": {`{"feature_key":"rack-view"}`, "feature_key"},
		"unknown feature key":       {`{"feature_key":"export_datev"}`, "feature_key"},
		"feature key in upper case": {`{"feature_key":"Discovery"}`, "feature_key"},
		"quota typo":                {`{"feature_key":"rack_view","limits":{"max_cis_typo":5}}`, "limits"},
		"quota of another name":     {`{"feature_key":"discovery","limits":{"collectors":5}}`, "limits"},
		"disabled cmdb_core":        {`{"feature_key":"cmdb_core","enabled":false}`, "enabled"},
	} {
		w := post(mux, path, tc.body, auth)
		var problem struct {
			Status int    `json:"status"`
			Field  string `json:"field"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &problem)
		if w.Code != http.StatusUnprocessableEntity || problem.Status != http.StatusUnprocessableEntity || problem.Field != tc.field {
			t.Errorf("%s: %d %s, want 422 with field %s", name, w.Code, w.Body.String(), tc.field)
		}
	}
	if items, err := svc.List(context.Background(), org); err != nil || len(items) != 1 || items[0].FeatureKey != entitlement.FeatureCMDBCore {
		t.Errorf("refused grants stored rows: %+v, %v", items, err)
	}
	if slices.Contains(audit.actions(), "entitlement.grant") {
		t.Errorf("refused grant recorded as a change: %v", audit.actions())
	}

	for _, feature := range entitlement.FeatureKeys() {
		body := `{"feature_key":"` + feature + `"}`
		if feature == entitlement.FeatureCMDBCore {
			body = `{"feature_key":"cmdb_core","limits":{"max_cis":10,"max_users":5}}`
		}
		if w := post(mux, path, body, auth); w.Code != http.StatusOK {
			t.Errorf("grant %s: %d %s", feature, w.Code, w.Body.String())
		}
	}
	for quota, feature := range entitlement.QuotaFeature {
		if w := post(mux, path, `{"feature_key":"`+feature+`","limits":{"`+quota+`":3}}`, auth); w.Code != http.StatusOK {
			t.Errorf("quota %s on %s: %d %s", quota, feature, w.Code, w.Body.String())
		}
	}

	// api/openapi.yaml documents the same keys and the 422.
	raw, err := os.ReadFile("../../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths      map[string]map[string]any `yaml:"paths"`
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Enum          []string `yaml:"enum"`
					PropertyNames struct {
						Enum []string `yaml:"enum"`
					} `yaml:"propertyNames"`
				} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err = yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	request := spec.Components.Schemas["GrantEntitlementRequest"].Properties
	if got := request["feature_key"].Enum; !slices.Equal(got, entitlement.FeatureKeys()) {
		t.Errorf("openapi feature_key enum %v, want %v", got, entitlement.FeatureKeys())
	}
	if got := request["limits"].PropertyNames.Enum; !slices.Equal(got, entitlement.Quotas) {
		t.Errorf("openapi limits keys %v, want %v", got, entitlement.Quotas)
	}
	grant, _ := spec.Paths["/api/v1/admin/orgs/{id}/entitlements"]["post"].(map[string]any)
	responses, _ := grant["responses"].(map[string]any)
	if _, ok := responses["422"]; !ok {
		t.Error("openapi: POST /api/v1/admin/orgs/{id}/entitlements documents no 422")
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

	// The entry verifies as stored: details with nested structs hash in
	// their read-back form (regression, WP-111).
	entry, err := scanEntry(f.Admin.QueryRow(bg, `SELECT `+auditColumns+` FROM operator_audit
		WHERE action = 'entitlement.grant' AND target_org = $1 ORDER BY id DESC LIMIT 1`, f.OrgA))
	if err != nil {
		t.Fatal(err)
	}
	if want, err := entryHash(&entry); err != nil || want != entry.EntryHash {
		t.Errorf("entitlement.grant entry hash does not verify: %v", err)
	}
}
