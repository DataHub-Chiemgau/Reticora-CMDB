package graphqlbff

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The tests of this file cover GQL-01 and SEC-10 (WP-184): the gqlgen BFF
// under /bff/graphql answers sessions only, enforces depth, complexity and
// deadline; and GQL-04 (WP-036): every resolver checks its read permission
// and entitlement, the answer carries the selected fields only and
// mutations are refused.

type entitled map[string]bool

func (e entitled) IsEnabled(_ context.Context, _, feature string) bool { return e[feature] }

func newFixture(t *testing.T) (*Handler, string) {
	t.Helper()
	cis := ci.NewMemoryRepository()
	rels := relationship.NewMemoryRepository()
	item := &ci.Item{OrganizationID: "org-1", Name: "core-router", CITypeID: "server", Status: "active",
		Hostname: "rtr01", SerialNumber: "SN-SECRET", Attributes: map[string]any{}}
	if err := cis.Create(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	other := &ci.Item{OrganizationID: "org-1", Name: "edge", CITypeID: "server", Status: "active", Attributes: map[string]any{}}
	if err := cis.Create(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if err := rels.Create(context.Background(), &relationship.Relationship{OrganizationID: "org-1",
		SourceCIID: item.ID, TargetCIID: other.ID, RelType: "connected_to", Source: "manual"}); err != nil {
		t.Fatal(err)
	}
	return NewHandler(cis, rels).WithEntitlements(entitled{FeatureCMDBCore: true}), item.ID
}

type gqlResponse struct {
	Data   map[string]any `json:"data"`
	Errors []struct {
		Message    string         `json:"message"`
		Extensions map[string]any `json:"extensions"`
	} `json:"errors"`
}

func (r gqlResponse) code() string {
	if len(r.Errors) == 0 {
		return ""
	}
	code, _ := r.Errors[0].Extensions["code"].(string)
	return code
}

// run sends body as a session principal (or as principalType) with perms;
// perms nil sends no principal at all.
func runAs(t *testing.T, h *Handler, principalType identity.PrincipalType, perms []identity.Permission, body string) (int, gqlResponse) {
	t.Helper()
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, Path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "user-1"})
	if perms != nil {
		ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: "user-1", OrganizationID: "org-1",
			Permissions: perms, Type: principalType})
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req.WithContext(ctx))
	var out gqlResponse
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func run(t *testing.T, h *Handler, perms []identity.Permission, body string) (int, gqlResponse) {
	t.Helper()
	return runAs(t, h, identity.PrincipalTypeUser, perms, body)
}

func gql(query string) string {
	raw, _ := json.Marshal(map[string]string{"query": query})
	return string(raw)
}

// TestOnlySessionsReachTheBFF: no principal is 401, an API key 403 (GQL-01).
func TestOnlySessionsReachTheBFF(t *testing.T) {
	h, _ := newFixture(t)
	perms := []identity.Permission{identity.PermCIRead}
	if code, _ := run(t, h, nil, gql(`{ currentUser { id } }`)); code != http.StatusUnauthorized {
		t.Errorf("no principal: %d", code)
	}
	if code, _ := runAs(t, h, identity.PrincipalTypeAPIKey, perms, gql(`{ cis { totalCount } }`)); code != http.StatusForbidden {
		t.Errorf("API key: %d, want 403", code)
	}
	if code, resp := run(t, h, perms, gql(`{ cis { totalCount } }`)); code != http.StatusOK || len(resp.Errors) != 0 {
		t.Errorf("session: %d %+v", code, resp)
	}
}

// TestLimits covers SEC-10: depth over 10, a query over the complexity
// budget and the deadline are refused.
func TestLimits(t *testing.T) {
	h, _ := newFixture(t)
	perms := []identity.Permission{identity.PermCIRead}
	deep := `{ __schema { types { fields { type { ofType { ofType { ofType { ofType { ofType { ofType { name } } } } } } } } } } }`
	if _, resp := run(t, h, perms, gql(deep)); len(resp.Errors) == 0 || !strings.Contains(resp.Errors[0].Message, "limit of 10") {
		t.Errorf("depth 11: %+v", resp)
	}
	shallow := `{ __schema { types { fields { type { ofType { name } } } } } }`
	if _, resp := run(t, h, perms, gql(shallow)); len(resp.Errors) != 0 {
		t.Errorf("introspection within the limits: %+v", resp)
	}
	fields := "id name status ciTypeId clientId siteId locationId hostname fqdn serialNumber manufacturer model managementIp lifecycleState health version createdAt updatedAt"
	page := "cis(first: 100) { nodes { " + fields + " } }"
	if _, resp := run(t, h, perms, gql("{ a: "+page+" }")); len(resp.Errors) != 0 {
		t.Errorf("one full page: %+v", resp)
	}
	if _, resp := run(t, h, perms, gql("{ a: "+page+" b: "+page+" c: "+page+" }")); len(resp.Errors) == 0 ||
		!strings.Contains(resp.Errors[0].Message, "complexity") {
		t.Errorf("three full pages: %+v", resp)
	}

	slow := NewHandler(blockingCIs{ci.NewMemoryRepository()}, relationship.NewMemoryRepository()).WithEntitlements(entitled{FeatureCMDBCore: true})
	slow.deadline = 50 * time.Millisecond
	start := time.Now()
	_, resp := run(t, slow, perms, gql(`{ cis { totalCount } }`))
	if time.Since(start) > 5*time.Second || len(resp.Errors) == 0 {
		t.Errorf("deadline: %v %+v", time.Since(start), resp)
	}
}

// blockingCIs answers a list only when the request context ends.
type blockingCIs struct{ *ci.MemoryRepository }

func (b blockingCIs) List(ctx context.Context, _ string, _ ci.FilterParams, _ api.PaginationParams) ([]ci.Item, int, error) {
	<-ctx.Done()
	return nil, 0, ctx.Err()
}

func TestResolversCheckTheirReadPermission(t *testing.T) {
	h, id := newFixture(t)
	for _, c := range []struct {
		name  string
		perms []identity.Permission
		query string
		code  string
	}{
		{"cis with ci:read", []identity.Permission{identity.PermCIRead}, `{ cis { totalCount } }`, ""},
		{"cis without ci:read", []identity.Permission{identity.PermRelationshipRead}, `{ cis { totalCount } }`, codeForbidden},
		{"ci without ci:read", []identity.Permission{}, `{ ci(id: "` + id + `") { name } }`, codeForbidden},
		{"relationships with ci:read only", []identity.Permission{identity.PermCIRead}, `{ relationships(ciId: "` + id + `") { id } }`, codeForbidden},
		{"relationships with relationship:read", []identity.Permission{identity.PermRelationshipRead}, `{ relationships(ciId: "` + id + `") { id relType } }`, ""},
		{"currentUser without permissions", []identity.Permission{}, `{ currentUser { id organizationId } }`, ""},
	} {
		if status, resp := run(t, h, c.perms, gql(c.query)); status != http.StatusOK || resp.code() != c.code {
			t.Errorf("%s: status %d code %q (%+v), want %q", c.name, status, resp.code(), resp, c.code)
		}
	}
}

func TestResolversCheckEntitlement(t *testing.T) {
	h, _ := newFixture(t)
	perms := []identity.Permission{identity.PermCIRead}
	h.resolver.entitlements = nil
	if _, resp := run(t, h, perms, gql(`{ cis { totalCount } }`)); resp.code() != codeForbidden {
		t.Errorf("without entitlement checker: %+v", resp)
	}
	h.WithEntitlements(entitled{})
	if _, resp := run(t, h, perms, gql(`{ cis { totalCount } }`)); resp.code() != codeForbidden {
		t.Errorf("without entitlement: %+v", resp)
	}
	h.WithEntitlements(entitled{FeatureCMDBCore: true})
	if _, resp := run(t, h, perms, gql(`{ cis { totalCount } }`)); len(resp.Errors) != 0 {
		t.Errorf("with entitlement: %+v", resp)
	}
}

func TestResponseContainsOnlySelectedFields(t *testing.T) {
	h, id := newFixture(t)
	perms := []identity.Permission{identity.PermCIRead}

	_, resp := run(t, h, perms, gql(`{ ci(id: "`+id+`") { name label: hostname } }`))
	got, _ := resp.Data["ci"].(map[string]any)
	if len(got) != 2 || got["name"] != "core-router" || got["label"] != "rtr01" {
		t.Errorf("ci %v, want only name and the aliased hostname", resp)
	}
	_, resp = run(t, h, perms, gql(`{ cis(first: 1) { totalCount pageInfo { hasNextPage endCursor } edges { node { name } } } }`))
	raw, _ := json.Marshal(resp)
	if strings.Contains(string(raw), "SN-SECRET") || strings.Contains(string(raw), "nodes") || !strings.Contains(string(raw), `"hasNextPage":true`) {
		t.Errorf("connection: %s", raw)
	}
	firstName := func(r gqlResponse) string {
		conn, _ := r.Data["cis"].(map[string]any)
		edges, _ := conn["edges"].([]any)
		if len(edges) != 1 {
			return ""
		}
		name, _ := edges[0].(map[string]any)["node"].(map[string]any)["name"].(string)
		return name
	}
	conn, _ := resp.Data["cis"].(map[string]any)
	cursor, _ := conn["pageInfo"].(map[string]any)["endCursor"].(string)
	_, second := run(t, h, perms, gql(`{ cis(first: 1, after: "`+cursor+`") { edges { node { name } } pageInfo { hasNextPage } } }`))
	if raw, _ = json.Marshal(second); firstName(second) == "" || firstName(second) == firstName(resp) || !strings.Contains(string(raw), `"hasNextPage":false`) {
		t.Errorf("second page: %s after %s", raw, firstName(resp))
	}
	if status, resp := run(t, h, perms, gql(`{ ci(id: "`+id+`") }`)); status == http.StatusOK && len(resp.Errors) == 0 {
		t.Errorf("query without selection set accepted: %+v", resp)
	}
}

func TestMutationsAreRefused(t *testing.T) {
	h, _ := newFixture(t)
	perms := []identity.Permission{identity.PermCIRead, identity.PermCIWrite}
	_, resp := run(t, h, perms, gql(`mutation { cis { totalCount } }`))
	if len(resp.Errors) == 0 || !strings.Contains(strings.ToLower(resp.Errors[0].Message), "mutation") || resp.Data != nil {
		t.Errorf("mutation: %+v", resp)
	}
}
