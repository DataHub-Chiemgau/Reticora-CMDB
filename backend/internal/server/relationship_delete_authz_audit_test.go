// Phase-1 acceptance audit, group T4-collector-load – REGRESSION test (must
// pass).
//
// WP-160 (REL-03, REL-04, RBA-02, TEN). High test gap "Authz and tenant
// isolation of DELETE/force through the real router": the existing tests
// call the relationship handler without the router's authorization. Through
// server.NewRouter + TenantMiddleware on PostgreSQL:
//   - a viewer (relationship:read only) gets 403 and nothing is suppressed;
//   - another organization's administrator gets 404 for the edge id, even
//     with force, and no suppression is written in either organization;
//   - a client-1 technician gets 404 for an edge between client-2 CIs;
//   - control: an engineer of the organization gets 409 without force and
//     204 with force; the discovery edge is gone and suppressed, a repeated
//     delete is 404.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -v -p 1 -run 'TestAuditRelationshipDeleteAuthzThroughRouter' ./internal/server/
package server_test

import (
	"context"
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
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
)

func TestAuditRelationshipDeleteAuthzThroughRouter(t *testing.T) {
	f := scopetest.Seed(t, "dc")
	bg := context.Background()

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
	principal := func(org, role, name string, clients identity.ScopeSet) identity.Principal {
		keys, ok := permission.StandardRolePermissions(role)
		if !ok {
			t.Fatalf("unknown standard role %s", role)
		}
		perms := make([]identity.Permission, 0, len(keys))
		for _, k := range keys {
			perms = append(perms, identity.Permission(k))
		}
		p := identity.Principal{Subject: f.AppUser(t, org, name), OrganizationID: org, Type: identity.PrincipalTypeUser,
			Scope: &identity.Scope{Clients: clients, Sites: all, Teams: all}, Permissions: perms, Credential: "audit-" + name}
		p.ClientScope = p.Scope.LegacyClientScope()
		return p
	}
	viewer := principal(f.OrgA, "viewer", "audit-rel-viewer", all)
	engineer := principal(f.OrgA, "engineer", "audit-rel-engineer", all)
	technician := principal(f.OrgA, "client_technician", "audit-rel-tech", identity.ScopeSet{IDs: []string{f.Client1}})
	foreignAdmin := principal(f.OrgB, "org_admin", "audit-rel-foreign", all)

	del := func(p identity.Principal, id string, force bool) int {
		t.Helper()
		path := "/api/v1/relationships/" + id
		if force {
			path += "?force=true"
		}
		req := httptest.NewRequest(http.MethodDelete, path, http.NoBody)
		ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), p.Permissions)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req.WithContext(ctx))
		return w.Code
	}
	edge := func(client, name string) string {
		t.Helper()
		src, dst := f.CI(t, f.OrgA, client, name+"-src"), f.CI(t, f.OrgA, client, name+"-dst")
		var id string
		if qerr := f.Admin.QueryRow(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source,
				verification_state, last_seen_at, last_confirmed_at, confidence)
			VALUES ($1, $2, $3, 'connected_to', 'discovery', 'unverified', now(), now(), 1.0) RETURNING id::text`, f.OrgA, src, dst).Scan(&id); qerr != nil {
			t.Fatal(qerr)
		}
		return id
	}
	exists := func(id string) bool {
		var n int
		if qerr := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_relationship WHERE id = $1`, id).Scan(&n); qerr != nil {
			t.Fatal(qerr)
		}
		return n == 1
	}
	suppressions := func() int {
		var n int
		if qerr := f.Admin.QueryRow(bg, `SELECT count(*) FROM relationship_suppression WHERE organization_id IN ($1, $2)`, f.OrgA, f.OrgB).Scan(&n); qerr != nil {
			t.Fatal(qerr)
		}
		return n
	}

	orgEdge := edge("", "audit-rel-org")
	client2Edge := edge(f.Client2, "audit-rel-c2")

	for _, tc := range []struct {
		name string
		p    identity.Principal
		id   string
		want int
	}{
		{"viewer (relationship:read only) with force", viewer, orgEdge, http.StatusForbidden},
		{"administrator of another organization with force", foreignAdmin, orgEdge, http.StatusNotFound},
		{"client-1 technician on an edge between client-2 CIs with force", technician, client2Edge, http.StatusNotFound},
	} {
		if code := del(tc.p, tc.id, true); code != tc.want || !exists(tc.id) || suppressions() != 0 {
			t.Errorf("%s: DELETE = %d (want %d), edge kept=%v, suppressions=%d (want 0)", tc.name, code, tc.want, exists(tc.id), suppressions())
		}
	}

	// Control: the organization's engineer.
	if code := del(engineer, orgEdge, false); code != http.StatusConflict || !exists(orgEdge) {
		t.Errorf("engineer without force: %d (want 409), kept=%v", code, exists(orgEdge))
	}
	if code := del(engineer, orgEdge, true); code != http.StatusNoContent || exists(orgEdge) || suppressions() != 1 {
		t.Errorf("engineer with force: %d (want 204), kept=%v, suppressions=%d (want 1)", code, exists(orgEdge), suppressions())
	}
	if code := del(engineer, orgEdge, true); code != http.StatusNotFound {
		t.Errorf("repeated delete: %d (want 404)", code)
	}
}
