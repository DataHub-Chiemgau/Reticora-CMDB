package server_test

// Phase-1 acceptance audit, group T3-data-ingest: test harness (no test of
// its own). It runs the complete server.NewRouter with authorization and the
// OpenAPI validation middleware, as cmd/server/main.go wires it, against
// PostgreSQL. Used by audit_t3_review_routes_integration_test.go (WP-088
// regression, WP-373), audit_t3_router_auditdefect_test.go (WP-091 defect)
// and vlan_routes_integration_test.go (WP-371).
//
// Retest (regression part):
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_03?sslmode=disable \
//     go test -count=1 -v -run 'TestAuditT3' ./internal/server/

import (
	"bytes"
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
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
)

type auditT3Router struct {
	t      *testing.T
	f      *scopetest.Fixture
	router http.Handler
	// user is the calling principal, other a second user of the same org.
	user  string
	other string
}

func newAuditT3Router(t *testing.T, tag string) *auditT3Router {
	t.Helper()
	f := scopetest.Seed(t, tag)
	user := f.AppUser(t, f.OrgA, "audit-t3-caller")
	other := f.AppUser(t, f.OrgA, "audit-t3-other")
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
		Version: "audit-t3", Entitlements: entitlements,
		CIService:       ci.NewServiceWithLimits(repos.CI, entitlements).WithFieldResolver(repos.CIType),
		Credentials:     credential.NewService(repos.Credential, encryptor),
		OIDC:            identity.NewOIDCProvider(identity.OIDCConfig{}),
		Egress:          egress.Options{AllowPrivate: true},
		RouteMiddleware: []func(http.Handler) http.Handler{middleware.OpenAPIValidation},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &auditT3Router{t: t, f: f, router: middleware.TenantMiddleware(mux), user: user, other: other}
}

// call sends one request as an org-wide user principal with perms.
func (r *auditT3Router) call(perms []identity.Permission, method, path, body string) *httptest.ResponseRecorder {
	r.t.Helper()
	return r.callAs(r.user, identity.PrincipalTypeUser, perms, method, path, body)
}

// callAs sends one request as the org-wide principal subject of type typ
// (an API key acts as its owner, a user or a service account).
func (r *auditT3Router) callAs(subject string, typ identity.PrincipalType, perms []identity.Permission, method, path, body string) *httptest.ResponseRecorder {
	r.t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	principal := identity.Principal{Subject: subject, OrganizationID: r.f.OrgA, Type: typ,
		Scope:       &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}},
		Permissions: perms, Credential: "audit-t3"}
	ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), principal), principal.Permissions)
	w := httptest.NewRecorder()
	r.router.ServeHTTP(w, req.WithContext(ctx))
	return w
}

func (r *auditT3Router) str(sql string, args ...any) string {
	r.t.Helper()
	var s string
	if err := r.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
		r.t.Fatalf("%s: %v", sql, err)
	}
	return s
}

// reviewItem inserts an open review item of kind.
func (r *auditT3Router) reviewItem(kind string) string {
	r.t.Helper()
	return r.str(`INSERT INTO review_item (organization_id, kind, status, payload) VALUES ($1, $2, 'open', '{}') RETURNING id::text`, r.f.OrgA, kind)
}

func auditT3Without(p identity.Permission) []identity.Permission {
	var out []identity.Permission
	for _, x := range identity.AllPermissions() {
		if x != p {
			out = append(out, x)
		}
	}
	return out
}
