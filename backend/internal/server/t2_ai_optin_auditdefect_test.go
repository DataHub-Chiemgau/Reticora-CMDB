//go:build auditdefect

// Phase-1 acceptance audit, group T2-auth-authz: defect test of the AI
// opt-in on the assistant's Ask path, through the production router over
// PostgreSQL.
//
//   - WP-076 (AI-02), finding P2 "The AI assistant Ask path sends tenant data
//     to the external provider without org opt-in"; gap (high) "The Ask path
//     must respect the opt-in". AI-02: external AI processing only with the
//     organization's opt-in (organization.ai_opt_in) and the ai add-on.
//     OrgConsent is wired only into the CI chunk indexer (cmd/server/main.go);
//     router.go builds ai.NewHandler and ai.NewRetriever without it, so
//     POST /api/v1/ai/ask embeds the question and sends the CI context to the
//     provider for an organization that never opted in.
//   - WP-076 (AI-02), finding P2 "The organization AI opt-in cannot be set
//     through any API, UI or operator path": no write operation of the
//     contract carries ai_opt_in (TestAuditT2AIOptInSettableThroughAPI).
//
// The organization holds the ai add-on (as granted by the operator), the
// user holds ci:read through the org_admin role, a CI is indexed for search
// and as an AI chunk. A positive control with ai_opt_in = true shows the
// fixture reaches the provider.
//
// Retest (needs a migrated database):
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_02?sslmode=disable go test -tags auditdefect -count=1 -v -run 'TestAuditT2AI' ./internal/server/
//	(TestAuditT2AIOptInSettableThroughAPI needs no database)
package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ai"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"gopkg.in/yaml.v3"
)

// t2CountingProvider is an enabled external provider that counts what it is
// sent.
type t2CountingProvider struct{ chats, embeds atomic.Int32 }

func (p *t2CountingProvider) Enabled() bool           { return true }
func (p *t2CountingProvider) EmbeddingsEnabled() bool { return true }
func (p *t2CountingProvider) Chat([]ai.Message) (string, int, int, error) {
	p.chats.Add(1)
	return "fake answer", 1, 1, nil
}

func (p *t2CountingProvider) Embed(string) ([]float64, error) {
	p.embeds.Add(1)
	return []float64{1, 0}, nil
}

// TestAuditT2AIAskRequiresOrgOptIn: without the organization's opt-in the
// Ask path refuses and the provider receives nothing.
func TestAuditT2AIAskRequiresOrgOptIn(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Fatal("TEST_DATABASE_URL is required for this audit test")
	}
	f := scopetest.Seed(t, "ed")
	bg := context.Background()
	orgCtx := f.OrgCtx(f.OrgA)
	userID := f.AppUser(t, f.OrgA, "t2-ai-user")
	if _, err := f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id)
		SELECT $1, $2, id FROM role WHERE organization_id = $1 AND name = 'org_admin'`, f.OrgA, userID); err != nil {
		t.Fatal(err)
	}
	ciID := f.CI(t, f.OrgA, "", "t2aioptinprobe alpha")
	if err := search.NewPGRepository(f.App).IndexDocument(orgCtx, search.Document{OrganizationID: f.OrgA, EntityType: "ci", EntityID: ciID,
		Title: "t2aioptinprobe alpha", URL: "/cis/" + ciID}); err != nil {
		t.Fatalf("index search document: %v", err)
	}
	if err := ai.NewPGRepository(f.App).UpsertChunk(orgCtx, ai.Chunk{OrganizationID: f.OrgA, EntityType: "ci", EntityID: ciID,
		Title: "t2aioptinprobe alpha", Content: "t2aioptinprobe alpha runs the billing database", URL: "/cis/" + ciID}); err != nil {
		t.Fatalf("upsert chunk: %v", err)
	}

	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{Enforce: true})
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
	provider := &t2CountingProvider{}
	mux, _, err := server.NewRouter(repos, server.Options{
		Version: "t2-ai", Entitlements: entitlements, CIService: ci.NewServiceWithLimits(repos.CI, entitlements),
		Credentials: credential.NewService(repos.Credential, encryptor), OIDC: identity.NewOIDCProvider(identity.OIDCConfig{}),
		AIProvider: provider,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := middleware.Chain(middleware.TenantMiddleware, entitlements.Middleware)(mux)
	ask := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/ask", strings.NewReader(`{"question":"t2aioptinprobe"}`))
		req.Header.Set("Content-Type", "application/json")
		p := identity.Principal{Subject: userID, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
			Scope:       &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}},
			Permissions: identity.AllPermissions(), Credential: "t2-ai"}
		ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), p.Permissions)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req.WithContext(ctx))
		return w
	}
	setOptIn := func(v bool) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, `UPDATE organization SET ai_opt_in = $2 WHERE id = $1`, f.OrgA, v); err != nil {
			t.Fatal(err)
		}
	}

	setOptIn(false)
	w := ask()
	if w.Code < 400 || w.Code >= 500 || provider.chats.Load() != 0 || provider.embeds.Load() != 0 {
		t.Errorf("POST /api/v1/ai/ask for an organization without AI opt-in: %d %s; provider got %d chat and %d embedding call(s); want a 4xx refusal and no provider call (AI-02)",
			w.Code, strings.TrimSpace(w.Body.String()), provider.chats.Load(), provider.embeds.Load())
	}

	// Positive control: with the opt-in the question reaches the provider.
	setOptIn(true)
	before := provider.chats.Load()
	if w := ask(); w.Code != http.StatusOK || provider.chats.Load() <= before {
		t.Errorf("control: ask with opt-in: %d %s, chat calls %d -> %d; want 200 and a provider call", w.Code, strings.TrimSpace(w.Body.String()), before, provider.chats.Load())
	}
}

// TestAuditT2AIOptInSettableThroughAPI: AI-02 "Opt-in pro Org" is a decision
// of the organization (or the operator); the contract must offer a write
// operation (POST, PUT or PATCH) whose request body carries ai_opt_in.
func TestAuditT2AIOptInSettableThroughAPI(t *testing.T) {
	raw, err := os.ReadFile("../../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	resolve := func(v any) map[string]any {
		m, _ := v.(map[string]any)
		for m != nil {
			ref, ok := m["$ref"].(string)
			if !ok {
				break
			}
			var cur any = doc
			for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
				cur = cur.(map[string]any)[part]
			}
			m, _ = cur.(map[string]any)
		}
		return m
	}
	var hasOptIn func(schema map[string]any, depth int) bool
	hasOptIn = func(schema map[string]any, depth int) bool {
		if schema == nil || depth > 8 {
			return false
		}
		if props, ok := schema["properties"].(map[string]any); ok {
			if _, ok := props["ai_opt_in"]; ok {
				return true
			}
		}
		for _, key := range []string{"allOf", "oneOf", "anyOf"} {
			subs, _ := schema[key].([]any)
			for _, sub := range subs {
				if hasOptIn(resolve(sub), depth+1) {
					return true
				}
			}
		}
		return false
	}
	var writers []string
	bodies := 0
	for path, item := range doc["paths"].(map[string]any) {
		for _, method := range []string{"post", "put", "patch"} {
			op, ok := item.(map[string]any)[method].(map[string]any)
			if !ok {
				continue
			}
			body := resolve(op["requestBody"])
			content, _ := body["content"].(map[string]any)
			media, _ := content["application/json"].(map[string]any)
			if media != nil {
				bodies++
			}
			if hasOptIn(resolve(media["schema"]), 0) {
				writers = append(writers, strings.ToUpper(method)+" "+path)
			}
		}
	}
	if bodies < 50 {
		t.Fatalf("only %d JSON request bodies found; the spec reader is broken", bodies)
	}
	if len(writers) == 0 {
		t.Errorf("no POST/PUT/PATCH operation of api/openapi.yaml accepts ai_opt_in; the organization AI opt-in of AI-02 can only be set by SQL")
	}
}
