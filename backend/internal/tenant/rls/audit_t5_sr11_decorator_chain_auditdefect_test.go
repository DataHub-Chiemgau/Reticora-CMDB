//go:build auditdefect

package rls_test

// Phase-1 acceptance audit, group T5-install-ops-gate: DEFECT test (fails
// until the product is fixed).
//
// WP-213 (GATE-02, CI-13 [B]) P1 "Gate-Checkliste führt CI-13 [B] als PASS
// trotz bestätigtem Defekt SR-11 (Reaktivieren → 501)" and WP-209 (SR-11)
// P2 "SR-11 als 'Low' eingestuft, obwohl CI-13-Reaktivierung in
// Produktionsverdrahtung 501 liefert": cmd/server/main.go:327 and :355 wrap
// the CI repository twice in ci.NewIndexingRepository (search indexer, then
// AI chunk indexer with consent). indexingRepository implements
// RestoreDeleted but not Restore(ctx, org, id), so discovery's review
// handler (review.go: h.ciRepo.(ciRestorer)) answers
// POST /api/v1/review-items/{id}/resolve {action: merge} of a
// resurrected_device review with 501 "this repository cannot reactivate
// CIs": the CI stays deleted, the review stays open. The existing
// resurrect_test.go uses the bare PG repository and therefore passes.
// Test gaps closed: high "Decorator-Kette aus main.go mit allen optionalen
// Schnittstellen (SR-11)" (WP-209) and, for the CI repository, high
// "Produktionsverdrahtung von cmd/server testbar machen" (WP-213).
//
// Requirement (CI-13 [B]): a device seen again after deletion is reactivated
// from the review queue: merge -> 200, deleted_at IS NULL, review resolved,
// GET /api/v1/cis/{id} 200; a ci_id that is not a candidate -> 400.
// The "bare-pg" wiring is the control (passes today), "main.go" the
// production chain (fails today).
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_08?sslmode=disable \
//     go test -tags auditdefect -count=1 -v -run 'TestAuditT5CIReactivationThroughProductionDecoratorChain' ./internal/tenant/rls/

import (
	"context"
	"encoding/json"
	"net/http"
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
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
)

// auditT5Env builds the complete PostgreSQL router. wiring "main.go"
// decorates the CI repository exactly like cmd/server/main.go (search
// indexer, then AI chunk indexer with organization consent).
func auditT5Env(t *testing.T, f *scopetest.Fixture, wiring string) (*channelEnv, ci.Repository) {
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
	if wiring == "main.go" {
		repos.CI = ci.NewIndexingRepository(repos.CI, search.NewCIIndexer(repos.Search))
		aiProvider := ai.NewProvider(&ai.ProviderConfig{}, false, nil)
		aiOptIn, _ := repos.AI.(ai.OptInSource)
		repos.CI = ci.NewIndexingRepository(repos.CI, ai.NewCIChunkIndexer(repos.AI, aiProvider).
			WithConsent(ai.OrgConsent{OptIn: aiOptIn, Entitlements: entitlements}))
	}
	mux, _, err := server.NewRouter(repos, server.Options{
		Version:      "audit-t5",
		Entitlements: entitlements,
		Dispatcher:   webhook.NewDispatcher(repos.Webhook, nil, webhook.DispatcherOptions{Deliveries: repos.WebhookDeliveries}),
		CIService:    ci.NewServiceWithLimits(repos.CI, entitlements),
		Credentials:  credential.NewService(repos.Credential, encryptor),
		OIDC:         identity.NewOIDCProvider(identity.OIDCConfig{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &channelEnv{t: t, router: middleware.TenantMiddleware(mux)}, repos.CI
}

func TestAuditT5CIReactivationThroughProductionDecoratorChain(t *testing.T) {
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
			env, repo := auditT5Env(t, f, tc.wiring)

			// The optional interface the review handler needs for CI-13.
			if _, ok := repo.(interface {
				Restore(ctx context.Context, orgID, id string) (*ci.Item, error)
			}); !ok {
				t.Errorf("wiring %s: the CI repository does not implement Restore(ctx, org, id); resurrected devices cannot be reactivated", tc.wiring)
			}

			user := f.AppUser(t, f.OrgA, "t5-sr11")
			if _, err := f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id)
				SELECT $1, $2, id FROM role WHERE organization_id = $1 AND name = 'org_admin'`, f.OrgA, user); err != nil {
				t.Fatal(err)
			}
			var serverType string
			if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&serverType); err != nil {
				t.Fatal(err)
			}
			p := &channelPrincipal{kind: "org", org: f.OrgA, user: user, scope: identity.Scope{Clients: all(), Sites: all(), Teams: all()}}

			newDeletedCI := func(name string) string {
				t.Helper()
				code, body := env.call(p, http.MethodPost, "/api/v1/cis", map[string]any{
					"ci_type_id": serverType, "name": name, "client_id": f.Client1, "serial_number": "T5-SN-" + name,
				})
				if code != http.StatusCreated {
					t.Fatalf("create CI %s: %d %s", name, code, body)
				}
				var out struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal([]byte(body), &out); err != nil || out.ID == "" {
					t.Fatalf("create CI %s: unexpected body %s (%v)", name, body, err)
				}
				if code, body := env.call(p, http.MethodDelete, "/api/v1/cis/"+out.ID, nil); code >= 300 {
					t.Fatalf("delete CI %s: %d %s", name, code, body)
				}
				return out.ID
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

			// CI-13: merge reactivates the deleted CI.
			code, body := env.call(p, http.MethodPost, "/api/v1/review-items/"+review+"/resolve",
				map[string]any{"action": "merge", "ci_id": victim})
			if code != http.StatusOK {
				t.Errorf("wiring %s: POST /api/v1/review-items/{id}/resolve merge: got %d %s, want 200", tc.wiring, code, trim(body))
			}
			var deleted bool
			if err := f.Admin.QueryRow(bg, `SELECT deleted_at IS NOT NULL FROM ci WHERE id = $1`, victim).Scan(&deleted); err != nil {
				t.Fatal(err)
			}
			if deleted {
				t.Errorf("wiring %s: the CI is still deleted after merge (deleted_at IS NOT NULL)", tc.wiring)
			}
			var status string
			if err := f.Admin.QueryRow(bg, `SELECT status FROM review_item WHERE id = $1`, review).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != "resolved" {
				t.Errorf("wiring %s: review status after merge = %q, want resolved", tc.wiring, status)
			}
			if code, _ := env.call(p, http.MethodGet, "/api/v1/cis/"+victim, nil); code != http.StatusOK {
				t.Errorf("wiring %s: GET /api/v1/cis/{id} of the reactivated CI: got %d, want 200", tc.wiring, code)
			}
		})
	}
}
