//go:build auditdefect

// Phase-1 acceptance audit, group T4-collector-load – DEFECT test (must fail
// until the defect is fixed).
//
// WP-153 (DIS-09, TEN client isolation, RBA-02 role matrix). Finding P2
// "scope_suggested bypasses discovery:manage and leaks across clients (list
// and permanent dismiss proven)": POST /api/v1/review-items/{id}/resolve
// needs only review:resolve and has no per-kind check, so a
// client_technician (review:resolve, but neither discovery:manage nor
// discovery:read) creates discovery scopes by accepting a suggestion; the
// review_item RLS is organization-wide and SuggestScopes does not bind the
// item to the collector's client, so a client-1 principal lists client 2's
// subnets and collector names and dismisses client 2's suggestion for good.
//
// Requirement: accepting or dismissing a scope_suggested item creates or
// suppresses a discovery scope and needs discovery:manage; client principals
// see and change only their client's items (a foreign item is not found).
// The org_admin accept is the control.
//
// The test runs the complete router (server.NewRouter + TenantMiddleware)
// on PostgreSQL with principals that carry the RBA-02 standard role
// permissions (permission.StandardRolePermissions) and a client scope.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -tags auditdefect -count=1 -v -p 1 -run 'TestAuditScopeSuggestedAuthzAndClientIsolation' ./internal/server/
package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
)

func TestAuditScopeSuggestedAuthzAndClientIsolation(t *testing.T) {
	f := scopetest.Seed(t, "da")
	bg := context.Background()
	org := f.OrgCtx(f.OrgA)

	// Two collectors of different clients, each reporting its subnets.
	repo := discovery.NewPGRepository(f.App)
	c1 := &discovery.Collector{OrganizationID: f.OrgA, ClientID: f.Client1, Name: "audit-sugg-c1", Status: discovery.CollectorActive, Config: map[string]any{}}
	c2 := &discovery.Collector{OrganizationID: f.OrgA, ClientID: f.Client2, Name: "audit-sugg-c2", Status: discovery.CollectorActive, Config: map[string]any{}}
	for _, c := range []*discovery.Collector{c1, c2} {
		if err := repo.RegisterCollector(org, c); err != nil {
			t.Fatal(err)
		}
	}
	h := discovery.NewHandler(repo, nil)
	if _, err := h.SuggestScopes(org, f.OrgA, c1.ID, []string{"10.97.1.0/24", "10.97.2.0/24", "10.97.3.0/24"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.SuggestScopes(org, f.OrgA, c2.ID, []string{"10.97.9.0/24"}); err != nil {
		t.Fatal(err)
	}
	item := func(cidr string) string {
		var id string
		if err := f.Admin.QueryRow(bg, `SELECT id::text FROM review_item WHERE organization_id = $1 AND kind = 'scope_suggested'
			AND status = 'open' AND payload->>'cidr' = $2`, f.OrgA, cidr).Scan(&id); err != nil {
			t.Fatalf("suggestion %s: %v", cidr, err)
		}
		return id
	}
	status := func(id string) string {
		var s string
		if err := f.Admin.QueryRow(bg, `SELECT status FROM review_item WHERE id = $1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	scopes := func(cidr string) int {
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM discovery_scope WHERE organization_id = $1 AND target = $2`, f.OrgA, cidr).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

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
	mux, _, err := server.NewRouter(repos, server.Options{
		Version: "audit-t4", Entitlements: entitlements,
		CIService:   ci.NewServiceWithLimits(repos.CI, entitlements),
		Credentials: credential.NewService(repos.Credential, encryptor),
		OIDC:        identity.NewOIDCProvider(identity.OIDCConfig{}),
		Egress:      egress.Options{AllowPrivate: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	router := middleware.TenantMiddleware(mux)

	all := identity.ScopeSet{All: true}
	principal := func(role, name string, clients identity.ScopeSet) identity.Principal {
		keys, ok := permission.StandardRolePermissions(role)
		if !ok {
			t.Fatalf("unknown standard role %s", role)
		}
		perms := make([]identity.Permission, 0, len(keys))
		for _, k := range keys {
			perms = append(perms, identity.Permission(k))
		}
		p := identity.Principal{Subject: f.AppUser(t, f.OrgA, name), OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
			Scope: &identity.Scope{Clients: clients, Sites: all, Teams: all}, Permissions: perms, Credential: "audit-" + name}
		p.ClientScope = p.Scope.LegacyClientScope()
		return p
	}
	client1 := identity.ScopeSet{IDs: []string{f.Client1}}
	technician := principal("client_technician", "audit-sugg-tech", client1)
	client1Engineer := principal("engineer", "audit-sugg-eng", client1)
	admin := principal("org_admin", "audit-sugg-admin", all)

	call := func(p identity.Principal, method, path string, body any) (int, []byte) {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), p.Permissions)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req.WithContext(ctx))
		return w.Code, w.Body.Bytes()
	}
	resolve := func(p identity.Principal, id, action string) (int, []byte) {
		return call(p, http.MethodPost, "/api/v1/review-items/"+id+"/resolve", map[string]any{"action": action})
	}

	t.Run("client_technician without discovery:manage cannot accept (no scope created)", func(t *testing.T) {
		id := item("10.97.1.0/24")
		code, body := resolve(technician, id, "accept")
		if code != http.StatusForbidden || scopes("10.97.1.0/24") != 0 {
			t.Errorf("RBA-02/DIS-09 violated: client_technician accept of scope_suggested = %d (scopes created: %d, item %s): %.200s",
				code, scopes("10.97.1.0/24"), status(id), body)
		}
	})
	t.Run("client_technician without discovery:manage cannot dismiss", func(t *testing.T) {
		id := item("10.97.2.0/24")
		code, body := resolve(technician, id, "dismiss")
		if code != http.StatusForbidden || status(id) != "open" {
			t.Errorf("RBA-02/DIS-09 violated: client_technician dismiss of scope_suggested = %d (item now %s): %.200s", code, status(id), body)
		}
	})
	t.Run("client-1 principal does not list client 2's suggestions", func(t *testing.T) {
		code, body := call(client1Engineer, http.MethodGet, "/api/v1/review-items?kind=scope_suggested&status=open&limit=100", nil)
		if code != http.StatusOK {
			t.Fatalf("list: %d %.200s", code, body)
		}
		var page struct {
			Data []struct {
				Payload map[string]any `json:"payload"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatal(err)
		}
		own := 0
		for _, it := range page.Data {
			switch it.Payload["collector_id"] {
			case c2.ID:
				t.Errorf("TEN violated: a client-1 principal lists client 2's suggestion cidr=%v collector=%v", it.Payload["cidr"], it.Payload["collector_name"])
			case c1.ID:
				own++
			}
		}
		if own == 0 {
			t.Errorf("control: the client-1 principal lists none of its own client's suggestions (%d items)", len(page.Data))
		}
	})
	t.Run("client-1 principal cannot dismiss client 2's suggestion", func(t *testing.T) {
		id := item("10.97.9.0/24")
		code, body := resolve(client1Engineer, id, "dismiss")
		if code != http.StatusNotFound || status(id) != "open" {
			t.Errorf("TEN violated: client-1 dismiss of client 2's scope_suggested = %d, item now %s (want 404, still open): %.200s", code, status(id), body)
		}
	})
	t.Run("control: org_admin accepts its suggestion and the scope exists", func(t *testing.T) {
		id := item("10.97.3.0/24")
		if code, body := resolve(admin, id, "accept"); code != http.StatusOK || scopes("10.97.3.0/24") != 1 {
			t.Errorf("org_admin accept = %d, scopes=%d: %.200s", code, scopes("10.97.3.0/24"), body)
		}
	})
}
