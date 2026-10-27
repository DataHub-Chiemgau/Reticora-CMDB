//go:build auditdefect

// Defect test of a finding from the implementation of K4 (WP-463, F-359,
// audit/g1-reaudit/FINDINGS.md): PATCH /api/v1/org requires user:manage, the
// route authorization accepts any scope of it, and the handler checks the
// organization-wide scope only for ai_opt_in. A holder of user:manage bound
// to one client therefore renames the organization and changes its time
// zone, currency, locale and settings, which every client of the
// organization inherits (RBA-03, TEN-06, LOC-01).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -tags auditdefect -count=1 -v -run 'TestAuditK4OrgFieldsRequireOrgWideUserManage' ./internal/tenantapi/
package tenantapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
)

func TestAuditK4OrgFieldsRequireOrgWideUserManage(t *testing.T) {
	f := scopetest.Seed(t, "21")
	bg := context.Background()
	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{Enforce: true})
	if _, err := entitlements.Provision(f.OrgCtx(f.OrgA), f.OrgA, entitlement.PlanEssential); err != nil {
		t.Fatalf("provision: %v", err)
	}
	clientAdmin := f.AppUser(t, f.OrgA, "k4-org-client-admin")

	masterKey, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	mux, _, err := server.NewRouter(repos, server.Options{
		Version: "k4-org-fields", Entitlements: entitlements, CIService: ci.NewServiceWithLimits(repos.CI, entitlements),
		Credentials: credential.NewService(repos.Credential, encryptor), OIDC: identity.NewOIDCProvider(identity.OIDCConfig{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := middleware.Chain(middleware.TenantMiddleware, entitlements.Middleware)(mux)
	all := identity.ScopeSet{All: true}
	client1 := &identity.Scope{Clients: identity.ScopeSet{IDs: []string{f.Client1}}, Sites: all, Teams: all}
	patch := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/org", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		p := identity.Principal{Subject: clientAdmin, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
			Scope: client1, Permissions: []identity.Permission{identity.PermUserManage, identity.PermLocationRead},
			Credential: "k4-org-fields-" + clientAdmin}
		ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), p.Permissions)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req.WithContext(ctx))
		return w
	}
	type orgRow struct{ name, timezone, currency, locale, settings string }
	row := func() orgRow {
		t.Helper()
		var r orgRow
		if err := f.Admin.QueryRow(bg, `SELECT name, COALESCE(timezone, ''), COALESCE(currency, ''), COALESCE(locale, ''), COALESCE(settings::text, '')
			FROM organization WHERE id = $1`, f.OrgA).Scan(&r.name, &r.timezone, &r.currency, &r.locale, &r.settings); err != nil {
			t.Fatal(err)
		}
		return r
	}

	for _, body := range []string{
		`{"name":"k4 renamed by a client admin"}`,
		`{"timezone":"Pacific/Auckland"}`,
		`{"currency":"JPY"}`,
		`{"locale":"ja-JP"}`,
		`{"settings":{"k4_probe":"set by a client admin"}}`,
	} {
		before := row()
		w := patch(body)
		after := row()
		if w.Code != http.StatusForbidden || after != before {
			t.Errorf("PATCH /api/v1/org %s by a holder of user:manage bound to client 1: %d, organization %+v -> %+v; "+
				"want 403 and the organization unchanged (only an org-wide holder changes organization-level fields)", body, w.Code, before, after)
		}
	}
}
