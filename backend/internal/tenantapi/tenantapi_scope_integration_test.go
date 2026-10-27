package tenantapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ai"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/bootstrap"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenantapi"
)

// TestTenantRepositoryClientScope runs the client/site/building/room
// repository with the principal's tenant scope (TEN-06, WP-014): a principal
// restricted to client 1 neither sees nor changes client 2, its sites or the
// buildings and rooms on them, and cannot place a building on a client-2
// site.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestTenantRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "21")
	repo := tenantapi.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	ownSite := &tenantapi.Site{OrganizationID: f.OrgA, ClientID: f.Client1, Name: "site-c1"}
	foreignSite := &tenantapi.Site{OrganizationID: f.OrgA, ClientID: f.Client2, Name: "site-c2"}
	for _, s := range []*tenantapi.Site{ownSite, foreignSite} {
		if err := repo.CreateSite(orgCtx, s); err != nil {
			t.Fatalf("org-wide site %s: %v", s.Name, err)
		}
	}
	foreignBuilding := &tenantapi.Building{OrganizationID: f.OrgA, SiteID: foreignSite.ID, Name: "building-c2"}
	if err := repo.CreateBuilding(orgCtx, foreignBuilding); err != nil {
		t.Fatalf("org-wide building: %v", err)
	}
	foreignRoom := &tenantapi.Room{OrganizationID: f.OrgA, BuildingID: foreignBuilding.ID, Name: "room-c2"}
	if err := repo.CreateRoom(orgCtx, foreignRoom); err != nil {
		t.Fatalf("org-wide room: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	page := api.PaginationParams{Limit: 100}
	clients, _, err := repo.ListClients(ctx, f.OrgA, page)
	if err != nil || len(clients) != 1 || clients[0].ID != f.Client1 {
		t.Fatalf("client-1 clients: %+v err=%v, want only client 1", clients, err)
	}
	sites, _, err := repo.ListSites(ctx, f.OrgA, "", page)
	if err != nil || len(sites) != 1 || sites[0].ID != ownSite.ID {
		t.Fatalf("client-1 sites: %+v err=%v, want only the client-1 site", sites, err)
	}
	buildings, total, err := repo.ListBuildings(ctx, f.OrgA, "", page)
	if err != nil || total != 0 || len(buildings) != 0 {
		t.Fatalf("client-1 buildings: total=%d %+v err=%v, want none", total, buildings, err)
	}
	rooms, total, err := repo.ListRooms(ctx, f.OrgA, "", page)
	if err != nil || total != 0 || len(rooms) != 0 {
		t.Fatalf("client-1 rooms: total=%d %+v err=%v, want none", total, rooms, err)
	}
	if _, err = repo.GetRoom(ctx, f.OrgA, foreignRoom.ID); err == nil {
		t.Fatal("client-1 principal read a room on a client-2 site")
	}
	name := "renamed"
	if _, err = repo.UpdateBuilding(ctx, f.OrgA, foreignBuilding.ID, tenantapi.UpdateBuildingRequest{Name: &name}); err == nil {
		t.Fatal("client-1 principal renamed a building on a client-2 site")
	}
	if err = repo.DeleteRoom(ctx, f.OrgA, foreignRoom.ID); err == nil {
		t.Fatal("client-1 principal deleted a room on a client-2 site")
	}
	if err = repo.DeleteClient(ctx, f.OrgA, f.Client2); err == nil {
		t.Fatal("client-1 principal deleted client 2")
	}
	if err = repo.CreateBuilding(ctx, &tenantapi.Building{OrganizationID: f.OrgA, SiteID: foreignSite.ID, Name: "intruder"}); err == nil {
		t.Fatal("client-1 principal placed a building on a client-2 site")
	}
	if err = repo.CreateBuilding(ctx, &tenantapi.Building{OrganizationID: f.OrgA, SiteID: ownSite.ID, Name: "building-c1"}); err != nil {
		t.Fatalf("client-1 building on own site: %v", err)
	}

	var count int
	if err = f.Admin.QueryRow(context.Background(),
		`SELECT (SELECT count(*) FROM room WHERE id = $1) + (SELECT count(*) FROM building WHERE id = $2 AND name = 'building-c2')`,
		foreignRoom.ID, foreignBuilding.ID).Scan(&count); err != nil {
		t.Fatalf("count client-2 rows: %v", err)
	}
	if count != 2 {
		t.Fatal("client-2 room or building was changed")
	}
	if _, _, err = repo.ListSites(context.Background(), f.OrgA, "", page); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}

// optInProvider is an enabled external AI provider that counts the chats it
// is sent.
type optInProvider struct{ chats atomic.Int32 }

func (p *optInProvider) Enabled() bool           { return true }
func (p *optInProvider) EmbeddingsEnabled() bool { return true }
func (p *optInProvider) Chat([]ai.Message) (string, int, int, error) {
	p.chats.Add(1)
	return "fake answer", 1, 1, nil
}
func (p *optInProvider) Embed(string) ([]float64, error) { return []float64{1, 0}, nil }

// TestOrganizationAIOptInThroughAPI covers WP-463 (AI-02, F-074) through the
// production router over PostgreSQL: an org-wide holder of user:manage
// (org_admin) sets ai_opt_in with PATCH /api/v1/org (200), the AI opt-in
// source reads the new value and the change is audited as
// organization.ai_opt_in_changed with the old and the new value; a
// client-bound holder of user:manage is refused (403) and changes nothing;
// organization B stays untouched; with the ai add-on POST /api/v1/ai/ask
// then answers 200 without a restart; a principal of an organization
// without a row gets 404 problem+json.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestOrganizationAIOptInThroughAPI(t *testing.T) {
	f := scopetest.Seed(t, "21")
	bg := context.Background()
	orgCtx := f.OrgCtx(f.OrgA)
	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{Enforce: true})

	admin := f.AppUser(t, f.OrgA, "optin-admin")
	if _, err := f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id)
		SELECT $1, $2, id FROM role WHERE organization_id = $1 AND name = 'org_admin'`, f.OrgA, admin); err != nil {
		t.Fatal(err)
	}
	clientAdmin := f.AppUser(t, f.OrgA, "optin-client-admin")
	title := "t2aioptinapiprobe"
	ciID := f.CI(t, f.OrgA, "", title)
	if err := search.NewPGRepository(f.App).IndexDocument(orgCtx, search.Document{OrganizationID: f.OrgA, EntityType: "ci", EntityID: ciID,
		Title: title, URL: "/cis/" + ciID}); err != nil {
		t.Fatalf("index search document: %v", err)
	}
	if err := ai.NewPGRepository(f.App).UpsertChunk(orgCtx, ai.Chunk{OrganizationID: f.OrgA, EntityType: "ci", EntityID: ciID,
		Title: title, Content: title + " runs the billing database", URL: "/cis/" + ciID}); err != nil {
		t.Fatalf("upsert chunk: %v", err)
	}
	if _, err := entitlements.Provision(orgCtx, f.OrgA, entitlement.PlanEssential); err != nil {
		t.Fatalf("provision: %v", err)
	}
	if _, err := entitlements.Grant(orgCtx, entitlement.Entitlement{OrganizationID: f.OrgA, FeatureKey: entitlement.FeatureAI,
		Plan: entitlement.PlanEssential, Enabled: true, Source: "manual"}); err != nil {
		t.Fatalf("grant ai add-on: %v", err)
	}

	masterKey, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	provider := &optInProvider{}
	modules, err := server.ParseModules("ai")
	if err != nil {
		t.Fatal(err)
	}
	repos.CI = bootstrap.NewCIRepository(repos.CI, repos.Search, repos.AI, provider, ai.NewOrgConsent(repos.AI, entitlements), modules)
	mux, _, err := server.NewRouter(repos, server.Options{
		Version: "t2-ai-optin", Modules: modules, Entitlements: entitlements, CIService: ci.NewServiceWithLimits(repos.CI, entitlements),
		Credentials: credential.NewService(repos.Credential, encryptor), OIDC: identity.NewOIDCProvider(identity.OIDCConfig{}),
		AIProvider: provider,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := middleware.Chain(middleware.TenantMiddleware, entitlements.Middleware)(mux)
	all := identity.ScopeSet{All: true}
	orgWide := &identity.Scope{Clients: all, Sites: all, Teams: all}
	client1 := &identity.Scope{Clients: identity.ScopeSet{IDs: []string{f.Client1}}, Sites: all, Teams: all}
	call := func(method, path, body, orgID, userID string, scope *identity.Scope) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		p := identity.Principal{Subject: userID, OrganizationID: orgID, Type: identity.PrincipalTypeUser,
			Scope: scope, Permissions: identity.AllPermissions(), Credential: "t2-ai-optin-" + userID}
		ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), p.Permissions)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req.WithContext(ctx))
		return w
	}
	type orgRow struct {
		optIn   bool
		updated time.Time
	}
	row := func(orgID string) orgRow {
		t.Helper()
		var r orgRow
		if err := f.Admin.QueryRow(bg, `SELECT ai_opt_in, updated_at FROM organization WHERE id = $1`, orgID).Scan(&r.optIn, &r.updated); err != nil {
			t.Fatal(err)
		}
		return r
	}
	type change struct {
		actor, resource string
		old, new        any
	}
	auditRows := func() []change {
		t.Helper()
		rows, err := f.Admin.Query(bg, `SELECT COALESCE(actor_id::text, ''), COALESCE(resource_id::text, ''), changes->'ai_opt_in' FROM audit_log
			WHERE organization_id = $1 AND action = 'organization.ai_opt_in_changed' ORDER BY seq`, f.OrgA)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []change
		for rows.Next() {
			var c change
			var values map[string]any
			if err := rows.Scan(&c.actor, &c.resource, &values); err != nil {
				t.Fatal(err)
			}
			c.old, c.new = values["old"], values["new"]
			out = append(out, c)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if row(f.OrgA).optIn || row(f.OrgB).optIn {
		t.Fatal("the organizations start with ai_opt_in = true; want false (migration 000087)")
	}
	before := row(f.OrgB)

	// Without the opt-in the Ask path refuses.
	if w := call(http.MethodPost, "/api/v1/ai/ask", `{"question":"t2aioptinapiprobe"}`, f.OrgA, admin, orgWide); w.Code != http.StatusForbidden {
		t.Fatalf("POST /api/v1/ai/ask before the opt-in: %d %s; want 403", w.Code, w.Body.String())
	}

	// The org_admin sets the opt-in: 200, the opt-in source of OrgConsent
	// reads it, one audit entry with the old and the new value.
	w := call(http.MethodPatch, "/api/v1/org", `{"ai_opt_in":true}`, f.OrgA, admin, orgWide)
	var o tenantapi.Organization
	_ = json.Unmarshal(w.Body.Bytes(), &o)
	if w.Code != http.StatusOK || !o.AIOptIn || o.ID != f.OrgA {
		t.Fatalf("org_admin PATCH /api/v1/org ai_opt_in=true: %d %s; want 200 with ai_opt_in true", w.Code, w.Body.String())
	}
	if ok, err := ai.NewPGRepository(f.App).AIOptIn(orgCtx, f.OrgA); err != nil || !ok {
		t.Fatalf("AIOptIn after PATCH: %v, %v; want true", ok, err)
	}
	if entries := auditRows(); len(entries) != 1 || entries[0] != (change{actor: admin, resource: f.OrgA, old: false, new: true}) {
		t.Fatalf("audit entries organization.ai_opt_in_changed: %+v; want one by %s from false to true", entries, admin)
	}
	// GET shows the value; the same value again writes no entry.
	if w = call(http.MethodGet, "/api/v1/org", "", f.OrgA, admin, orgWide); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ai_opt_in":true`) {
		t.Errorf("GET /api/v1/org: %d %s; want ai_opt_in true", w.Code, w.Body.String())
	}
	if w = call(http.MethodPatch, "/api/v1/org", `{"ai_opt_in":true}`, f.OrgA, admin, orgWide); w.Code != http.StatusOK || len(auditRows()) != 1 {
		t.Errorf("unchanged ai_opt_in: %d, %d audit entries; want 200 and still one", w.Code, len(auditRows()))
	}

	// A holder of user:manage bound to client 1 may not change it.
	w = call(http.MethodPatch, "/api/v1/org", `{"ai_opt_in":false}`, f.OrgA, clientAdmin, client1)
	if w.Code != http.StatusForbidden || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") {
		t.Errorf("client-bound PATCH /api/v1/org ai_opt_in=false: %d %q %s; want 403 problem+json",
			w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
	if !row(f.OrgA).optIn || len(auditRows()) != 1 {
		t.Errorf("client-bound PATCH changed ai_opt_in or wrote an audit entry: opt-in %v, %d entries", row(f.OrgA).optIn, len(auditRows()))
	}
	// Organization B is untouched.
	if after := row(f.OrgB); after != before {
		t.Errorf("organization B changed: %+v -> %+v", before, after)
	}

	// The consent reads the new value without a restart.
	if w = call(http.MethodPost, "/api/v1/ai/ask", `{"question":"t2aioptinapiprobe"}`, f.OrgA, admin, orgWide); w.Code != http.StatusOK || provider.chats.Load() != 1 {
		t.Errorf("POST /api/v1/ai/ask after the opt-in: %d %s, %d chat(s); want 200 and one chat", w.Code, w.Body.String(), provider.chats.Load())
	}

	// A principal of an organization without a row: 404 problem+json.
	w = call(http.MethodPatch, "/api/v1/org", `{"ai_opt_in":true}`, f.ID(), f.ID(), orgWide)
	if w.Code != http.StatusNotFound || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") {
		t.Errorf("PATCH /api/v1/org of an unknown organization: %d %q %s; want 404 problem+json",
			w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
}
