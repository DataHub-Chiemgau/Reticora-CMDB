package rls_test

// Phase-1 acceptance audit, group T5-install-ops-gate: regression test of
// the CI repository wiring of cmd/server (WP-418, t5-sr11-decorator).
//
// WP-213 (GATE-02, CI-13 [B]) and WP-209 (SR-11), finding F-037: cmd/server
// wrapped the CI repository twice in ci.NewIndexingRepository (search
// indexer, then AI chunk indexer with consent). indexingRepository
// implemented RestoreDeleted but not Restore(ctx, org, id), so discovery's
// review handler (review.go: h.ciRepo.(ciRestorer)) answered
// POST /api/v1/review-items/{id}/resolve {action: merge} of a
// resurrected_device review with 501 "this repository cannot reactivate
// CIs": the CI stayed deleted, the review open. Nor did it forward
// UniqueConflicts, so the MET-15 check before the write was skipped. The
// existing resurrect_test.go uses the bare PG repository and passed.
// cmd/server now builds the chain with bootstrap.NewCIRepository; the
// "main.go" wiring here is that function, "bare-pg" the control.
//
// Requirement (CI-13 [B]): a device seen again after deletion is reactivated
// from the review queue: merge -> 200, deleted_at IS NULL, review resolved,
// audit ci.restored, GET /api/v1/cis/{id} 200; a ci_id that is not a
// candidate -> 400; a principal scoped to another client -> 404, nothing
// reactivated. In the production chain also:
//   - F-301 (E-03): the AI chunk decorator only with the module ai: the G1
//     default plus a new CI leaves no ai_chunk row, RETICORA_MODULES=ai
//     writes one; the search index is kept in both.
//   - MET-15: before the unique index of an attribute exists, a second CI
//     with the same value -> 409 naming the attribute (also on PATCH),
//     another value -> 201.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<DB>?sslmode=disable \
//     go test -count=1 -v -run 'TestAuditT5CIReactivationThroughProductionDecoratorChain' ./internal/tenant/rls/

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ai"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/bootstrap"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
)

// decoratorChainEnv builds the complete PostgreSQL router. wiring "main.go"
// decorates the CI repository with bootstrap.NewCIRepository as
// cmd/server/main.go does, with the late modules of spec (RETICORA_MODULES)
// and the organization consent of the router; "bare-pg" keeps the
// PostgreSQL repository.
func decoratorChainEnv(t *testing.T, f *scopetest.Fixture, wiring, spec string) (*channelEnv, ci.Repository) {
	t.Helper()
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{Enforce: false})
	var modules *server.ModuleSet
	if wiring == "main.go" {
		if modules, err = server.ParseModules(spec); err != nil {
			t.Fatal(err)
		}
		repos.CI = bootstrap.NewCIRepository(repos.CI, repos.Search, repos.AI, ai.NewProvider(&ai.ProviderConfig{}, false, nil),
			ai.NewOrgConsent(repos.AI, entitlements), modules)
	}
	mux, _, err := server.NewRouter(repos, server.Options{
		Version:      "wp418",
		Modules:      modules,
		Entitlements: entitlements,
		Dispatcher:   webhook.NewDispatcher(repos.Webhook, nil, webhook.DispatcherOptions{Deliveries: repos.WebhookDeliveries}),
		CIService:    ci.NewServiceWithLimits(repos.CI, entitlements).WithFieldResolver(repos.CIType),
		Credentials:  credential.NewService(repos.Credential, encryptor),
		OIDC:         identity.NewOIDCProvider(identity.OIDCConfig{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &channelEnv{t: t, router: middleware.TenantMiddleware(mux)}, repos.CI
}

// decoratorChainAdmin is an org_admin user of organization A with every
// permission and the whole organization in scope.
func decoratorChainAdmin(t *testing.T, f *scopetest.Fixture, name string) *channelPrincipal {
	t.Helper()
	user := f.AppUser(t, f.OrgA, name)
	if _, err := f.Admin.Exec(context.Background(), `INSERT INTO role_assignment (organization_id, user_id, role_id)
		SELECT $1, $2, id FROM role WHERE organization_id = $1 AND name = 'org_admin'`, f.OrgA, user); err != nil {
		t.Fatal(err)
	}
	return &channelPrincipal{kind: "org", org: f.OrgA, user: user, scope: identity.Scope{Clients: all(), Sites: all(), Teams: all()}}
}

// decoratorChainCreate creates a CI through POST /api/v1/cis and returns
// its id.
func decoratorChainCreate(t *testing.T, env *channelEnv, p *channelPrincipal, body map[string]any) string {
	t.Helper()
	code, resp := env.call(p, http.MethodPost, "/api/v1/cis", body)
	if code != http.StatusCreated {
		t.Fatalf("create CI %v: %d %s", body["name"], code, resp)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(resp), &out); err != nil || out.ID == "" {
		t.Fatalf("create CI %v: unexpected body %s (%v)", body["name"], resp, err)
	}
	return out.ID
}

func TestAuditT5CIReactivationThroughProductionDecoratorChain(t *testing.T) {
	serverType := func(t *testing.T, f *scopetest.Fixture) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(context.Background(), `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}

	for _, tc := range []struct {
		wiring string
		tag    string
	}{
		{"bare-pg", "d5"},
		{"main.go", "d6"},
	} {
		t.Run(tc.wiring, func(t *testing.T) {
			f := scopetest.Seed(t, tc.tag)
			bg := context.Background()
			// The G1 default of cmd/server: no late module.
			env, repo := decoratorChainEnv(t, f, tc.wiring, "")

			// The optional interface the review handler needs for CI-13.
			if _, ok := repo.(interface {
				Restore(ctx context.Context, orgID, id string) (*ci.Item, error)
			}); !ok {
				t.Errorf("wiring %s: the CI repository does not implement Restore(ctx, org, id); resurrected devices cannot be reactivated", tc.wiring)
			}

			p := decoratorChainAdmin(t, f, "t5-sr11")
			typ := serverType(t, f)
			newDeletedCI := func(name string) string {
				t.Helper()
				id := decoratorChainCreate(t, env, p, map[string]any{
					"ci_type_id": typ, "name": name, "client_id": f.Client1, "serial_number": "T5-SN-" + name,
				})
				if code, body := env.call(p, http.MethodDelete, "/api/v1/cis/"+id, nil); code >= 300 {
					t.Fatalf("delete CI %s: %d %s", name, code, body)
				}
				return id
			}
			newReview := func(candidate string) string {
				t.Helper()
				var id string
				if err := f.Admin.QueryRow(bg, `INSERT INTO review_item (organization_id, kind, payload, candidate_ci_ids)
					VALUES ($1, 'resurrected_device', '{"name":"t5-sr11"}', ARRAY[$2::uuid]) RETURNING id::text`, f.OrgA, candidate).Scan(&id); err != nil {
					t.Fatal(err)
				}
				return id
			}

			// Negative: a ci_id outside the candidates is refused with 400.
			other := newDeletedCI("t5-sr11-other-" + tc.wiring)
			victim := newDeletedCI("t5-sr11-" + tc.wiring)
			review := newReview(victim)
			if code, body := env.call(p, http.MethodPost, "/api/v1/review-items/"+review+"/resolve",
				map[string]any{"action": "merge", "ci_id": other}); code != http.StatusBadRequest {
				t.Errorf("wiring %s: merge with a non-candidate ci_id: got %d %s, want 400", tc.wiring, code, trim(body))
			}
			// Negative (TEN-02): a principal scoped to client 2 does not reach
			// the review of a client-1 CI and reactivates nothing.
			client2 := &channelPrincipal{kind: "client", org: f.OrgA, user: p.user, scope: identity.Scope{Clients: only(f.Client2), Sites: all(), Teams: all()}}
			if code, body := env.call(client2, http.MethodPost, "/api/v1/review-items/"+review+"/resolve",
				map[string]any{"action": "merge", "ci_id": victim}); code != http.StatusNotFound {
				t.Errorf("wiring %s: merge as a client-2 principal: got %d %s, want 404", tc.wiring, code, trim(body))
			}
			var stillDeleted bool
			if err := f.Admin.QueryRow(bg, `SELECT deleted_at IS NOT NULL FROM ci WHERE id = $1`, victim).Scan(&stillDeleted); err != nil {
				t.Fatal(err)
			}
			if !stillDeleted {
				t.Fatalf("wiring %s: a client-2 principal reactivated a CI of client 1", tc.wiring)
			}

			// CI-13: merge reactivates the deleted CI.
			code, body := env.call(p, http.MethodPost, "/api/v1/review-items/"+review+"/resolve",
				map[string]any{"action": "merge", "ci_id": victim})
			if code != http.StatusOK {
				t.Errorf("wiring %s: POST /api/v1/review-items/{id}/resolve merge: got %d %s, want 200", tc.wiring, code, trim(body))
			}
			var deleted bool
			var status string
			var restored, otherDeleted int
			if err := f.Admin.QueryRow(bg, `SELECT
				(SELECT deleted_at IS NOT NULL FROM ci WHERE id = $1),
				(SELECT status FROM review_item WHERE id = $2),
				(SELECT count(*) FROM audit_log WHERE organization_id = $3 AND resource_id = $1 AND action = 'ci.restored'),
				(SELECT count(*) FROM ci WHERE id = $4 AND deleted_at IS NOT NULL)`,
				victim, review, f.OrgA, other).Scan(&deleted, &status, &restored, &otherDeleted); err != nil {
				t.Fatal(err)
			}
			if deleted {
				t.Errorf("wiring %s: the CI is still deleted after merge (deleted_at IS NOT NULL)", tc.wiring)
			}
			if status != "resolved" {
				t.Errorf("wiring %s: review status after merge = %q, want resolved", tc.wiring, status)
			}
			if restored != 1 {
				t.Errorf("wiring %s: %d ci.restored audit entries for the CI, want 1", tc.wiring, restored)
			}
			if otherDeleted != 1 {
				t.Errorf("wiring %s: the refused non-candidate CI was reactivated", tc.wiring)
			}
			if code, _ := env.call(p, http.MethodGet, "/api/v1/cis/"+victim, nil); code != http.StatusOK {
				t.Errorf("wiring %s: GET /api/v1/cis/{id} of the reactivated CI: got %d, want 200", tc.wiring, code)
			}
			if tc.wiring == "main.go" {
				// The delete took the CI out of the search index; the
				// reactivation indexes it again.
				var docs int
				if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM search_document WHERE entity_type = 'ci' AND entity_id = $1`, victim).Scan(&docs); err != nil {
					t.Fatal(err)
				}
				if docs != 1 {
					t.Errorf("wiring main.go: %d search documents of the reactivated CI, want 1", docs)
				}
			}
		})
	}

	// F-301: the AI chunk decorator only with the module ai.
	t.Run("ai_chunk", func(t *testing.T) {
		f := scopetest.Seed(t, "d8")
		bg := context.Background()
		p := decoratorChainAdmin(t, f, "wp418-ai")
		typ := serverType(t, f)
		for _, mc := range []struct {
			spec       string // RETICORA_MODULES
			wantChunks bool
		}{
			{"", false},
			{"ai", true},
		} {
			env, _ := decoratorChainEnv(t, f, "main.go", mc.spec)
			id := decoratorChainCreate(t, env, p, map[string]any{"ci_type_id": typ, "name": "wp418-ai-" + mc.spec, "client_id": f.Client1})
			var chunks, docs int
			if err := f.Admin.QueryRow(bg, `SELECT
				(SELECT count(*) FROM ai_chunk WHERE entity_type = 'ci' AND entity_id = $1),
				(SELECT count(*) FROM search_document WHERE entity_type = 'ci' AND entity_id = $1)`, id).Scan(&chunks, &docs); err != nil {
				t.Fatal(err)
			}
			if mc.wantChunks && chunks == 0 {
				t.Errorf("RETICORA_MODULES=%q: no ai_chunk row for the new CI, want one", mc.spec)
			}
			if !mc.wantChunks && chunks != 0 {
				t.Errorf("RETICORA_MODULES=%q (G1 default): %d ai_chunk row(s) for the new CI, want 0 (E-03, F-301)", mc.spec, chunks)
			}
			if docs != 1 {
				t.Errorf("RETICORA_MODULES=%q: %d search documents for the new CI, want 1", mc.spec, docs)
			}
		}
	})

	// MET-15: the service checks the unique attributes in the production
	// chain before the write.
	t.Run("unique_conflict", func(t *testing.T) {
		f := scopetest.Seed(t, "d9")
		bg := context.Background()
		ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
		types := citype.NewPGRepository(f.App)
		typ := &citype.Type{OrganizationID: f.OrgA, Key: "wp418-ups", Name: "wp418-ups", IsObservable: true}
		if err := types.Create(ctx, typ); err != nil {
			t.Fatalf("create type: %v", err)
		}
		const field = "wp418_inventory_no"
		def := &citype.AttributeDefinition{CITypeID: typ.ID, Key: field, DataType: "string", Unique: true, UniqueScope: "org"}
		if err := types.CreateAttributeDefinition(ctx, f.OrgA, def); err != nil {
			t.Fatalf("define %s: %v", field, err)
		}
		// No index job runs here: until the DDL job built the unique index,
		// only the check of the service refuses a taken value.
		var indexes int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM pg_indexes WHERE indexname = $1`, citype.IndexName(def.ID, true)).Scan(&indexes); err != nil {
			t.Fatal(err)
		}
		if indexes != 0 {
			t.Fatalf("the unique index of %s exists; the case would not test the check before the write", field)
		}

		env, _ := decoratorChainEnv(t, f, "main.go", "")
		p := decoratorChainAdmin(t, f, "wp418-unique")
		body := func(name, value string) map[string]any {
			return map[string]any{"ci_type_id": typ.ID, "name": name, "client_id": f.Client1, "attributes": map[string]any{field: value}}
		}
		// conflict asserts a 409 that names the unique attribute.
		conflict := func(label string, code int, resp string) {
			t.Helper()
			var problem struct {
				Violations []struct {
					Field string `json:"field"`
				} `json:"violations"`
			}
			_ = json.Unmarshal([]byte(resp), &problem)
			if code != http.StatusConflict || len(problem.Violations) != 1 || problem.Violations[0].Field != field {
				t.Errorf("%s: got %d %s, want 409 naming %s (ci.UniqueError, MET-15)", label, code, trim(resp), field)
			}
		}
		decoratorChainCreate(t, env, p, body("wp418-a", "INV-1"))
		code, resp := env.call(p, http.MethodPost, "/api/v1/cis", body("wp418-b", "INV-1"))
		conflict("POST a second CI with the same value", code, resp)
		second := decoratorChainCreate(t, env, p, body("wp418-c", "INV-2"))
		code, resp = env.call(p, http.MethodPatch, "/api/v1/cis/"+second, map[string]any{"attributes": map[string]any{field: "INV-1"}})
		conflict("PATCH another CI to the taken value", code, resp)
		var holders int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci WHERE organization_id = $1 AND deleted_at IS NULL AND attributes ->> $2 = 'INV-1'`,
			f.OrgA, field).Scan(&holders); err != nil {
			t.Fatal(err)
		}
		if holders != 1 {
			t.Errorf("%d active CIs hold %s = INV-1, want 1", holders, field)
		}
	})
}
