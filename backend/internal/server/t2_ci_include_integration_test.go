// Phase-1 acceptance audit, group T2-auth-authz: regression test of the CI
// include parameter through the production router over PostgreSQL.
//
//   - WP-169 (API-01 include), gap (high) "Include wiring with real
//     repositories and client scope not in the suite": an org-wide principal
//     gets include=relationships,contacts,asset with both relationships, the
//     contact and the asset; a client-1 principal does not see the
//     relationship to a client-2 CI; include=bogus -> 422; include on a
//     deleted CI -> 404; a CI of another organization -> 404.
//   - WP-567 (API-05, F-223): an embedded collection capped at 200 rows is
//     marked in truncated (truncated.relationships: true); a collection
//     that is complete is not, also when the cap hides rows of another
//     client only from the organization-wide view.
//
// Retest (needs a migrated database):
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_02?sslmode=disable go test -count=1 -v -run 'TestT2CIInclude' ./internal/server/
package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"gopkg.in/yaml.v3"

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
	if capped, ok := out["truncated"]; ok {
		t.Errorf("org-wide include of complete collections: truncated %v, want absent", capped)
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

// TestT2CIIncludeSignalsTruncation: GET /cis/{id}?include= embeds at most
// 200 rows of a collection (API-05). A capped collection is marked with
// truncated.<name> = true (WP-567, F-223); before the fix the 201st
// relationship was dropped without a hint. The flag counts only the rows
// the caller may see: a client-1 principal whose 200 visible relationships
// are complete gets no flag although the CI has 201 in the organization.
func TestT2CIIncludeSignalsTruncation(t *testing.T) {
	f := scopetest.Seed(t, "e4")
	bg := context.Background()
	userID := f.AppUser(t, f.OrgA, "t2-include-cap")
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id)
		SELECT $1, $2, id FROM role WHERE organization_id = $1 AND name = 'org_admin'`, f.OrgA, userID)
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
		Version: "t2-include-cap", Entitlements: entitlements, CIService: ci.NewServiceWithLimits(repos.CI, entitlements),
		Credentials: credential.NewService(repos.Credential, encryptor), OIDC: identity.NewOIDCProvider(identity.OIDCConfig{}),
		RouteMiddleware: []func(http.Handler) http.Handler{middleware.OpenAPIValidation},
	})
	if err != nil {
		t.Fatal(err)
	}
	router := middleware.RequestID(middleware.TenantMiddleware(mux))
	orgWide := &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}}
	client1 := &identity.Scope{Clients: identity.ScopeSet{IDs: []string{f.Client1}}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}}
	get := func(scope *identity.Scope, path string) (int, map[string]json.RawMessage) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		p := identity.Principal{Subject: userID, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
			Scope: scope, Permissions: identity.AllPermissions(), Credential: "t2-include-cap"}
		ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), p.Permissions)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req.WithContext(ctx))
		var out map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
		}
		return w.Code, out
	}
	// view returns the number of embedded rows of each collection and the
	// truncated marker of the answer.
	view := func(out map[string]json.RawMessage, names ...string) (map[string]int, map[string]bool) {
		t.Helper()
		rows := map[string]int{}
		for _, name := range names {
			var list []json.RawMessage
			if err := json.Unmarshal(out[name], &list); err != nil {
				t.Fatalf("%s: %v (%s)", name, err, out[name])
			}
			rows[name] = len(list)
		}
		var truncated map[string]bool
		if raw, ok := out["truncated"]; ok {
			if err := json.Unmarshal(raw, &truncated); err != nil {
				t.Fatalf("truncated: %v (%s)", err, raw)
			}
		}
		return rows, truncated
	}

	// root: 200 relationships from client-1 CIs, one from a client-2 CI
	// and one contact. contacts: a CI with 201 contacts.
	root := f.CI(t, f.OrgA, f.Client1, "t2-cap-root")
	foreignClient := f.CI(t, f.OrgA, f.Client2, "t2-cap-client2")
	exec(`INSERT INTO ci (organization_id, client_id, ci_type_id, name, status)
		SELECT $1, $2, (SELECT id FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1), 't2-cap-peer-' || g, 'active'
		FROM generate_series(1, 200) g`, f.OrgA, f.Client1)
	exec(`INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source)
		SELECT $1, id, $2, 'depends_on', 'manual' FROM ci WHERE organization_id = $1 AND name LIKE 't2-cap-peer-%'`, f.OrgA, root)
	exec(`INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source) VALUES ($1, $2, $3, 'depends_on', 'manual')`,
		f.OrgA, foreignClient, root)
	exec(`INSERT INTO contact (organization_id, client_id, display_name, email)
		SELECT $1, $2, 'T2 Cap ' || g, 't2-cap-' || g || '@example.test' FROM generate_series(1, 201) g`, f.OrgA, f.Client1)
	exec(`INSERT INTO ci_contact (organization_id, ci_id, contact_id, relationship_type)
		SELECT $1, $2, id, 'owner' FROM contact WHERE organization_id = $1 AND email = 't2-cap-1@example.test'`, f.OrgA, root)
	manyContacts := f.CI(t, f.OrgA, f.Client1, "t2-cap-contacts")
	exec(`INSERT INTO ci_contact (organization_id, ci_id, contact_id, relationship_type)
		SELECT $1, $2, id, 'owner' FROM contact WHERE organization_id = $1 AND email LIKE 't2-cap-%@example.test'`, f.OrgA, manyContacts)

	// Root cause (F-223): 201 relationships, 200 embedded.
	code, out := get(orgWide, "/api/v1/cis/"+root+"?include=relationships,contacts")
	rows, truncated := view(out, "relationships", "contacts")
	if code != http.StatusOK || rows["relationships"] != 200 || rows["contacts"] != 1 {
		t.Fatalf("org-wide include: %d, relationships %d (want 200 of 201), contacts %d (want 1)", code, rows["relationships"], rows["contacts"])
	}
	if !truncated["relationships"] || len(truncated) != 1 {
		t.Errorf("org-wide include of 201 relationships: 200 embedded, truncated %v; want {relationships: true}", truncated)
	}

	// The client-1 view sees 200 relationships, all of them.
	code, out = get(client1, "/api/v1/cis/"+root+"?include=relationships")
	rows, truncated = view(out, "relationships")
	if code != http.StatusOK || rows["relationships"] != 200 || truncated != nil {
		t.Errorf("client-1 include: %d, relationships %d (want 200), truncated %v (want absent)", code, rows["relationships"], truncated)
	}

	// An empty collection is an empty list (api/openapi.yaml CIWithIncludes).
	if code, out = get(orgWide, "/api/v1/cis/"+foreignClient+"?include=contacts"); code != http.StatusOK || string(out["contacts"]) != "[]" {
		t.Errorf("include of a CI without contacts: %d, contacts %s, want []", code, out["contacts"])
	}

	// 201 contacts: the contacts are capped, the (empty) relationships not.
	code, out = get(orgWide, "/api/v1/cis/"+manyContacts+"?include=contacts,relationships,asset")
	rows, truncated = view(out, "relationships", "contacts")
	if code != http.StatusOK || rows["contacts"] != 200 || rows["relationships"] != 0 || !truncated["contacts"] || len(truncated) != 1 {
		t.Errorf("include of 201 contacts: %d, contacts %d, relationships %d, truncated %v; want 200, 0, {contacts: true}",
			code, rows["contacts"], rows["relationships"], truncated)
	}
}

// TestCIIncludeTruncatedInContract: api/openapi.yaml documents the marker of
// a capped collection additively (WP-567, F-223): getCI answers
// CIWithIncludes, the CI plus the embedded collections and truncated, an
// object of booleans.
func TestCIIncludeTruncatedInContract(t *testing.T) {
	raw, err := os.ReadFile("../../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	type schema struct {
		Ref                  string            `yaml:"$ref"`
		Type                 any               `yaml:"type"`
		AllOf                []schema          `yaml:"allOf"`
		Properties           map[string]schema `yaml:"properties"`
		AdditionalProperties *schema           `yaml:"additionalProperties"`
	}
	var doc struct {
		Paths      map[string]map[string]yaml.Node `yaml:"paths"`
		Components struct {
			Schemas map[string]yaml.Node `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err = yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var getCI struct {
		Responses map[string]struct {
			Content map[string]struct {
				Schema schema `yaml:"schema"`
			} `yaml:"content"`
		} `yaml:"responses"`
	}
	node := doc.Paths["/api/v1/cis/{id}"]["get"]
	if err = node.Decode(&getCI); err != nil {
		t.Fatal(err)
	}
	if ref := getCI.Responses["200"].Content["application/json"].Schema.Ref; ref != "#/components/schemas/CIWithIncludes" {
		t.Fatalf("getCI 200 schema %q, want CIWithIncludes", ref)
	}
	var with schema
	node = doc.Components.Schemas["CIWithIncludes"]
	if err = node.Decode(&with); err != nil {
		t.Fatal(err)
	}
	if len(with.AllOf) != 2 || with.AllOf[0].Ref != "#/components/schemas/CI" {
		t.Fatalf("CIWithIncludes is not the CI plus the includes: %+v", with)
	}
	truncated, ok := with.AllOf[1].Properties["truncated"]
	if !ok || truncated.Type != "object" || truncated.AdditionalProperties == nil || truncated.AdditionalProperties.Type != "boolean" {
		t.Errorf("CIWithIncludes.truncated %+v, want an object of booleans", truncated)
	}
	for _, name := range []string{"relationships", "contacts"} {
		if p, ok := with.AllOf[1].Properties[name]; !ok || p.Type != "array" {
			t.Errorf("CIWithIncludes.%s %+v, want an array", name, p)
		}
	}
}
