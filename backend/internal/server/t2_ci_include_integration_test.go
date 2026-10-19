// Phase-1 acceptance audit, group T2-auth-authz: regression test of the CI
// include parameter through the production router over PostgreSQL.
//
//   - WP-169 (API-01 include), gap (high) "Include wiring with real
//     repositories and client scope not in the suite": an org-wide principal
//     gets include=relationships,contacts,asset with both relationships, the
//     contact and the asset; a client-1 principal does not see the
//     relationship to a client-2 CI; include=bogus -> 422; include on a
//     deleted CI -> 404; a CI of another organization -> 404.
//
// Retest (needs a migrated database):
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_02?sslmode=disable go test -count=1 -v -run 'TestT2CIIncludeWiring' ./internal/server/
package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestT2CIIncludeWiring(t *testing.T) {
	f := scopetest.Seed(t, "e4")
	bg := context.Background()
	userID := f.AppUser(t, f.OrgA, "t2-include-admin")
	if _, err := f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id)
		SELECT $1, $2, id FROM role WHERE organization_id = $1 AND name = 'org_admin'`, f.OrgA, userID); err != nil {
		t.Fatal(err)
	}
	masterKey, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{Enforce: false})
	mux, _, err := server.NewRouter(repos, server.Options{
		Version: "t2-include", Entitlements: entitlements, CIService: ci.NewServiceWithLimits(repos.CI, entitlements),
		Credentials: credential.NewService(repos.Credential, encryptor), OIDC: identity.NewOIDCProvider(identity.OIDCConfig{}),
		RouteMiddleware: []func(http.Handler) http.Handler{middleware.OpenAPIValidation},
	})
	if err != nil {
		t.Fatal(err)
	}
	router := middleware.RequestID(middleware.TenantMiddleware(mux))
	orgWide := &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}}
	client1 := &identity.Scope{Clients: identity.ScopeSet{IDs: []string{f.Client1}}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}}
	call := func(scope *identity.Scope, method, path string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		p := identity.Principal{Subject: userID, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
			Scope: scope, Permissions: identity.AllPermissions(), Credential: "t2-include"}
		ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), p.Permissions)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req.WithContext(ctx))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	get := func(scope *identity.Scope, path string) (int, map[string]any) {
		t.Helper()
		return call(scope, http.MethodGet, path)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	root := f.CI(t, f.OrgA, f.Client1, "t2-include-root")
	peer := f.CI(t, f.OrgA, f.Client1, "t2-include-peer")
	foreignClient := f.CI(t, f.OrgA, f.Client2, "t2-include-client2")
	for _, e := range [][2]string{{peer, root}, {foreignClient, root}} {
		exec(`INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source) VALUES ($1, $2, $3, 'depends_on', 'manual')`, f.OrgA, e[0], e[1])
	}
	var contactID string
	if err := f.Admin.QueryRow(bg, `INSERT INTO contact (id, organization_id, client_id, display_name, email, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, 'T2 Contact', 't2-include@example.test', now(), now()) RETURNING id::text`, f.OrgA, f.Client1).Scan(&contactID); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO ci_contact (id, organization_id, ci_id, contact_id, relationship_type, created_at, client_id) VALUES (gen_random_uuid(), $1, $2, $3, 'owner', now(), $4)`,
		f.OrgA, root, contactID, f.Client1)
	asset := f.Asset(t, f.OrgA, f.Client1, "t2-include-asset")
	exec(`UPDATE asset SET ci_id = $2 WHERE id = $1`, asset, root)
	exec(`UPDATE ci SET asset_id = $2 WHERE id = $1`, root, asset)

	relatedTo := func(out map[string]any) (n int, seesClient2 bool) {
		rels, _ := out["relationships"].([]any)
		for _, r := range rels {
			m, _ := r.(map[string]any)
			if m["source_ci_id"] == foreignClient || m["target_ci_id"] == foreignClient {
				seesClient2 = true
			}
		}
		return len(rels), seesClient2
	}
	assetID := func(out map[string]any) string {
		a, _ := out["asset"].(map[string]any)
		id, _ := a["id"].(string)
		return id
	}
	contacts := func(out map[string]any) int {
		c, _ := out["contacts"].([]any)
		return len(c)
	}

	code, out := get(orgWide, "/api/v1/cis/"+root+"?include=relationships,contacts,asset")
	if n, _ := relatedTo(out); code != http.StatusOK || n != 2 || contacts(out) != 1 || assetID(out) != asset {
		t.Errorf("org-wide include: %d, relationships %d (want 2), contacts %d (want 1), asset %q (want %s)", code, n, contacts(out), assetID(out), asset)
	}
	code, out = get(client1, "/api/v1/cis/"+root+"?include=relationships,contacts,asset")
	if n, seen := relatedTo(out); code != http.StatusOK || seen || n != 1 || contacts(out) != 1 || assetID(out) != asset {
		t.Errorf("client-1 include: %d, relationships %d (want 1), client-2 relationship visible %v (want false), contacts %d, asset %q", code, n, seen, contacts(out), assetID(out))
	}
	if code, _ := get(orgWide, "/api/v1/cis/"+root+"?include=bogus"); code != http.StatusUnprocessableEntity {
		t.Errorf("include=bogus: %d, want 422", code)
	}
	if code, out := call(orgWide, http.MethodDelete, "/api/v1/cis/"+peer); code != http.StatusNoContent && code != http.StatusOK {
		t.Fatalf("delete CI: %d %v", code, out)
	}
	if code, _ := get(orgWide, "/api/v1/cis/"+peer+"?include=relationships"); code != http.StatusNotFound {
		t.Errorf("include on a deleted CI: %d, want 404", code)
	}
	orgBCI := f.CI(t, f.OrgB, "", "t2-include-org-b")
	if code, _ := get(orgWide, "/api/v1/cis/"+orgBCI+"?include=relationships,contacts,asset"); code != http.StatusNotFound {
		t.Errorf("include on a CI of another organization: %d, want 404", code)
	}
}
