// scope_suggested: accept and dismiss only with discovery:manage, visibility
// per client (WP-382; F-031).
//
//   - TestAuditScopeSuggestedAuthzAndClientIsolation – WP-153 (DIS-09,
//     API-05), WP-156 (INS-06; moved from
//     scope_suggested_authz_auditdefect_test.go): accepting a scope_suggested
//     item creates a discovery scope (and so network scans), dismissing it
//     suppresses the subnet; both need discovery:manage besides
//     review:resolve. A client_technician (review:resolve, but neither
//     discovery:manage nor discovery:read) and an org-wide principal without
//     discovery:manage get a 403 problem on accept and dismiss, on
//     /api/v1/review-items/{id}/resolve and its deprecated discovery path;
//     the item stays open and no scope exists. The suggestion is bound to the
//     collector's client (WP-342): the item of a client-2 collector carries
//     client_id of client 2, the item of an org-wide collector client_id
//     NULL. A client-1 principal neither lists nor reads nor resolves either
//     (404, the item stays open); org-wide principals list both. Controls:
//     the client-1 engineer accepts its own client's suggestion (scope of
//     client 1), the org_admin accepts its suggestion and the org-wide one
//     (200, scope created).
//
// The test runs the complete router (server.NewRouter + TenantMiddleware)
// on PostgreSQL with principals that carry the RBA-02 standard role
// permissions (permission.StandardRolePermissions) and a client scope.
//
// Retest (F-031):
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -v -p 1 -run 'TestAuditScopeSuggestedAuthzAndClientIsolation' ./internal/server/
package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
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

	// Collectors of two clients and an org-wide one, each reporting its
	// subnets.
	repo := discovery.NewPGRepository(f.App)
	c1 := &discovery.Collector{OrganizationID: f.OrgA, ClientID: f.Client1, Name: "audit-sugg-c1", Status: discovery.CollectorActive, Config: map[string]any{}}
	c2 := &discovery.Collector{OrganizationID: f.OrgA, ClientID: f.Client2, Name: "audit-sugg-c2", Status: discovery.CollectorActive, Config: map[string]any{}}
	cOrg := &discovery.Collector{OrganizationID: f.OrgA, Name: "audit-sugg-org", Status: discovery.CollectorActive, Config: map[string]any{}}
	for _, c := range []*discovery.Collector{c1, c2, cOrg} {
		if err := repo.RegisterCollector(org, c); err != nil {
			t.Fatal(err)
		}
	}
	h := discovery.NewHandler(repo, nil)
	if _, err := h.SuggestScopes(org, f.OrgA, c1.ID, []string{"10.97.1.0/24", "10.97.2.0/24", "10.97.3.0/24", "10.97.4.0/24", "10.97.5.0/24"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.SuggestScopes(org, f.OrgA, c2.ID, []string{"10.97.9.0/24"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.SuggestScopes(org, f.OrgA, cOrg.ID, []string{"10.97.20.0/24"}); err != nil {
		t.Fatal(err)
	}
	item := func(t *testing.T, cidr string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `SELECT id::text FROM review_item WHERE organization_id = $1 AND kind = 'scope_suggested'
			AND status = 'open' AND payload->>'cidr' = $2`, f.OrgA, cidr).Scan(&id); err != nil {
			t.Fatalf("suggestion %s: %v", cidr, err)
		}
		return id
	}
	status := func(t *testing.T, id string) string {
		t.Helper()
		var s string
		if err := f.Admin.QueryRow(bg, `SELECT status FROM review_item WHERE id = $1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	itemClient := func(t *testing.T, id string) string {
		t.Helper()
		var c string
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(client_id::text, '') FROM review_item WHERE id = $1`, id).Scan(&c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	scopes := func(t *testing.T, cidr string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM discovery_scope WHERE organization_id = $1 AND target = $2`, f.OrgA, cidr).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	scopeClient := func(t *testing.T, cidr string) string {
		t.Helper()
		var c string
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(client_id::text, '') FROM discovery_scope WHERE organization_id = $1 AND target = $2`,
			f.OrgA, cidr).Scan(&c); err != nil {
			t.Fatalf("scope %s: %v", cidr, err)
		}
		return c
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
	principalWith := func(name string, perms []identity.Permission, clients identity.ScopeSet) identity.Principal {
		p := identity.Principal{Subject: f.AppUser(t, f.OrgA, name), OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
			Scope: &identity.Scope{Clients: clients, Sites: all, Teams: all}, Permissions: perms, Credential: "audit-" + name}
		p.ClientScope = p.Scope.LegacyClientScope()
		return p
	}
	principal := func(role, name string, clients identity.ScopeSet) identity.Principal {
		keys, ok := permission.StandardRolePermissions(role)
		if !ok {
			t.Fatalf("unknown standard role %s", role)
		}
		perms := make([]identity.Permission, 0, len(keys))
		for _, k := range keys {
			perms = append(perms, identity.Permission(k))
		}
		return principalWith(name, perms, clients)
	}
	client1 := identity.ScopeSet{IDs: []string{f.Client1}}
	technician := principal("client_technician", "audit-sugg-tech", client1)
	client1Engineer := principal("engineer", "audit-sugg-eng", client1)
	admin := principal("org_admin", "audit-sugg-admin", all)
	// Org-wide, may resolve and read review items, but holds no
	// discovery:manage.
	orgReviewer := principalWith("audit-sugg-reviewer", []identity.Permission{identity.PermReviewResolve, identity.PermDiscoveryRead}, all)
	if technician.Has(identity.PermDiscoveryManage) || !technician.Has(identity.PermReviewResolve) || !client1Engineer.Has(identity.PermDiscoveryManage) {
		t.Fatal("setup: client_technician must hold review:resolve without discovery:manage, the engineer discovery:manage")
	}

	call := func(p identity.Principal, method, path string, body any) (int, []byte, string) {
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
		return w.Code, w.Body.Bytes(), w.Header().Get("Content-Type")
	}
	resolveAt := func(p identity.Principal, prefix, id, action string) (int, []byte, string) {
		return call(p, http.MethodPost, prefix+id+"/resolve", map[string]any{"action": action})
	}
	resolve := func(p identity.Principal, id, action string) (int, []byte, string) {
		return resolveAt(p, "/api/v1/review-items/", id, action)
	}
	// forbidden reports whether the answer is the 403 problem of the missing
	// discovery:manage.
	forbidden := func(code int, body []byte, contentType string) bool {
		var problem struct {
			Status int    `json:"status"`
			Detail string `json:"detail"`
		}
		return code == http.StatusForbidden && contentType == api.ProblemContentType &&
			json.Unmarshal(body, &problem) == nil && problem.Status == http.StatusForbidden &&
			strings.Contains(problem.Detail, string(identity.PermDiscoveryManage))
	}
	listed := func(t *testing.T, p identity.Principal) map[string]bool {
		t.Helper()
		code, body, _ := call(p, http.MethodGet, "/api/v1/review-items?kind=scope_suggested&status=open&limit=100", nil)
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
		collectors := map[string]bool{}
		for _, it := range page.Data {
			if id, _ := it.Payload["collector_id"].(string); id != "" {
				collectors[id] = true
			}
		}
		return collectors
	}

	t.Run("suggestions are bound to the collector's client", func(t *testing.T) {
		for cidr, want := range map[string]string{"10.97.1.0/24": f.Client1, "10.97.9.0/24": f.Client2, "10.97.20.0/24": ""} {
			if got := itemClient(t, item(t, cidr)); got != want {
				t.Errorf("TEN-05: suggestion %s has client_id %q, want %q (client of its collector, NULL for an org-wide one)", cidr, got, want)
			}
		}
	})
	t.Run("client_technician without discovery:manage cannot accept (no scope created)", func(t *testing.T) {
		id := item(t, "10.97.1.0/24")
		code, body, ct := resolve(technician, id, "accept")
		if !forbidden(code, body, ct) || scopes(t, "10.97.1.0/24") != 0 || status(t, id) != "open" {
			t.Errorf("RBA-02/DIS-09 violated: client_technician accept of scope_suggested = %d %s (scopes created: %d, item %s): %.200s",
				code, ct, scopes(t, "10.97.1.0/24"), status(t, id), body)
		}
	})
	t.Run("client_technician without discovery:manage cannot dismiss", func(t *testing.T) {
		id := item(t, "10.97.2.0/24")
		code, body, ct := resolve(technician, id, "dismiss")
		if !forbidden(code, body, ct) || status(t, id) != "open" {
			t.Errorf("RBA-02/DIS-09 violated: client_technician dismiss of scope_suggested = %d %s (item now %s): %.200s", code, ct, status(t, id), body)
		}
		// The deprecated path resolves through the same effect.
		id = item(t, "10.97.5.0/24")
		for _, action := range []string{"accept", "dismiss"} {
			code, body, ct = resolveAt(technician, "/api/v1/discovery/review-items/", id, action)
			if !forbidden(code, body, ct) || status(t, id) != "open" || scopes(t, "10.97.5.0/24") != 0 {
				t.Errorf("RBA-02/DIS-09 violated: client_technician %s on the deprecated path = %d %s (item now %s, scopes %d): %.200s",
					action, code, ct, status(t, id), scopes(t, "10.97.5.0/24"), body)
			}
		}
	})
	t.Run("org-wide principal without discovery:manage cannot accept or dismiss", func(t *testing.T) {
		id := item(t, "10.97.20.0/24")
		for _, action := range []string{"accept", "dismiss"} {
			code, body, ct := resolve(orgReviewer, id, action)
			if !forbidden(code, body, ct) || status(t, id) != "open" || scopes(t, "10.97.20.0/24") != 0 {
				t.Errorf("DIS-09 violated: %s of scope_suggested without discovery:manage = %d %s (item now %s, scopes %d): %.200s",
					action, code, ct, status(t, id), scopes(t, "10.97.20.0/24"), body)
			}
		}
	})
	t.Run("client-1 principal does not list client 2's suggestions", func(t *testing.T) {
		got := listed(t, client1Engineer)
		if got[c2.ID] {
			t.Errorf("TEN violated: a client-1 principal lists client 2's suggestion (collector %s)", c2.Name)
		}
		if got[cOrg.ID] {
			t.Errorf("TEN/E-08 violated: a client-1 principal lists the suggestion of the org-wide collector (client_id NULL)")
		}
		if !got[c1.ID] {
			t.Errorf("control: the client-1 principal lists none of its own client's suggestions (%v)", got)
		}
	})
	t.Run("org-wide principals list every client's and the org-wide suggestions", func(t *testing.T) {
		for _, p := range []identity.Principal{admin, orgReviewer} {
			if got := listed(t, p); !got[c1.ID] || !got[c2.ID] || !got[cOrg.ID] {
				t.Errorf("org-wide principal %s lists the suggestions of collectors %v, want c1, c2 and the org-wide one", p.Credential, got)
			}
		}
	})
	t.Run("client-1 principal cannot dismiss client 2's suggestion", func(t *testing.T) {
		id := item(t, "10.97.9.0/24")
		code, body, _ := resolve(client1Engineer, id, "dismiss")
		if code != http.StatusNotFound || status(t, id) != "open" {
			t.Errorf("TEN violated: client-1 dismiss of client 2's scope_suggested = %d, item now %s (want 404, still open): %.200s", code, status(t, id), body)
		}
		if code, body, _ = resolve(client1Engineer, id, "accept"); code != http.StatusNotFound || status(t, id) != "open" || scopes(t, "10.97.9.0/24") != 0 {
			t.Errorf("TEN violated: client-1 accept of client 2's scope_suggested = %d, item now %s, scopes %d (want 404, still open, none): %.200s",
				code, status(t, id), scopes(t, "10.97.9.0/24"), body)
		}
		if code, body, _ = call(client1Engineer, http.MethodGet, "/api/v1/review-items/"+id, nil); code != http.StatusNotFound {
			t.Errorf("TEN violated: client-1 GET of client 2's scope_suggested = %d (want 404): %.200s", code, body)
		}
	})
	t.Run("client-1 principal cannot read or resolve the org-wide suggestion", func(t *testing.T) {
		id := item(t, "10.97.20.0/24")
		if code, body, _ := call(client1Engineer, http.MethodGet, "/api/v1/review-items/"+id, nil); code != http.StatusNotFound {
			t.Errorf("E-08 violated: client-1 GET of the org-wide suggestion = %d (want 404): %.200s", code, body)
		}
		for _, action := range []string{"dismiss", "accept"} {
			if code, body, _ := resolve(client1Engineer, id, action); code != http.StatusNotFound || status(t, id) != "open" || scopes(t, "10.97.20.0/24") != 0 {
				t.Errorf("E-09 violated: client-1 %s of the org-wide suggestion = %d, item now %s, scopes %d (want 404, still open, none): %.200s",
					action, code, status(t, id), scopes(t, "10.97.20.0/24"), body)
			}
		}
	})
	t.Run("control: client-1 engineer with discovery:manage accepts its client's suggestion", func(t *testing.T) {
		id := item(t, "10.97.4.0/24")
		if code, body, _ := resolve(client1Engineer, id, "accept"); code != http.StatusOK || scopes(t, "10.97.4.0/24") != 1 || status(t, id) != "resolved" {
			t.Fatalf("client-1 engineer accept = %d, scopes=%d, item %s: %.200s", code, scopes(t, "10.97.4.0/24"), status(t, id), body)
		}
		if got := scopeClient(t, "10.97.4.0/24"); got != f.Client1 {
			t.Errorf("the accepted scope has client_id %q, want client 1", got)
		}
	})
	t.Run("control: org_admin accepts its suggestion and the scope exists", func(t *testing.T) {
		id := item(t, "10.97.3.0/24")
		if code, body, _ := resolve(admin, id, "accept"); code != http.StatusOK || scopes(t, "10.97.3.0/24") != 1 {
			t.Errorf("org_admin accept = %d, scopes=%d: %.200s", code, scopes(t, "10.97.3.0/24"), body)
		}
		id = item(t, "10.97.20.0/24")
		if code, body, _ := resolve(admin, id, "accept"); code != http.StatusOK || scopes(t, "10.97.20.0/24") != 1 || status(t, id) != "resolved" {
			t.Fatalf("org_admin accept of the org-wide suggestion = %d, scopes=%d, item %s: %.200s", code, scopes(t, "10.97.20.0/24"), status(t, id), body)
		}
		if got := scopeClient(t, "10.97.20.0/24"); got != "" {
			t.Errorf("the scope of the org-wide suggestion has client_id %q, want NULL", got)
		}
	})
}
