package graphqlbff

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// The tests of this file cover WP-036 (GQL-04): every resolver checks its
// read permission and entitlement, responses carry only the selected fields
// and mutations are refused.

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

func run(t *testing.T, h *Handler, perms []identity.Permission, body string) (int, map[string]any) {
	t.Helper()
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/graphql", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "user-1"})
	if perms != nil {
		ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: "user-1", OrganizationID: "org-1",
			Permissions: perms, Type: identity.PrincipalTypeUser})
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req.WithContext(ctx))
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return w.Code, out
}

func gql(query string) string {
	raw, _ := json.Marshal(map[string]string{"query": query})
	return string(raw)
}

func TestResolversCheckTheirReadPermission(t *testing.T) {
	h, id := newFixture(t)
	for _, c := range []struct {
		name  string
		perms []identity.Permission
		query string
		want  int
	}{
		{"cis with ci:read", []identity.Permission{identity.PermCIRead}, `{ cis { totalCount } }`, http.StatusOK},
		{"cis without ci:read", []identity.Permission{identity.PermRelationshipRead}, `{ cis { totalCount } }`, http.StatusForbidden},
		{"ci without ci:read", []identity.Permission{}, `{ ci(id: "` + id + `") { name } }`, http.StatusForbidden},
		{"relationships with ci:read only", []identity.Permission{identity.PermCIRead}, `{ relationships(ciId: "` + id + `") { id } }`, http.StatusForbidden},
		{"relationships with relationship:read", []identity.Permission{identity.PermRelationshipRead}, `{ relationships(ciId: "` + id + `") { id } }`, http.StatusOK},
		{"currentUser without permissions", []identity.Permission{}, `{ currentUser { id } }`, http.StatusOK},
		{"no principal", nil, `{ currentUser { id } }`, http.StatusUnauthorized},
	} {
		if got, body := run(t, h, c.perms, gql(c.query)); got != c.want {
			t.Errorf("%s: status %d (%v), want %d", c.name, got, body, c.want)
		}
	}
}

func TestResolversCheckEntitlement(t *testing.T) {
	h, _ := newFixture(t)
	h.schema.RegisterQuery("tickets", Field{
		Resolve:    func(context.Context, map[string]any) (any, error) { return []map[string]any{{"id": "t1"}}, nil },
		Permission: identity.PermTicketRead,
		Feature:    "ticketing",
	})
	perms := []identity.Permission{identity.PermTicketRead}
	h.entitlements = nil
	if got, _ := run(t, h, perms, gql(`{ tickets { id } }`)); got != http.StatusForbidden {
		t.Errorf("gated field without entitlement checker: status %d, want 403", got)
	}
	h.WithEntitlements(entitled{})
	if got, _ := run(t, h, perms, gql(`{ tickets { id } }`)); got != http.StatusForbidden {
		t.Errorf("gated field without entitlement: status %d, want 403", got)
	}
	h.WithEntitlements(entitled{"ticketing": true})
	if got, _ := run(t, h, perms, gql(`{ tickets { id } }`)); got != http.StatusOK {
		t.Errorf("gated field with entitlement: status %d, want 200", got)
	}
}

func TestResponseContainsOnlySelectedFields(t *testing.T) {
	h, id := newFixture(t)
	perms := []identity.Permission{identity.PermCIRead}

	status, body := run(t, h, perms, gql(`{ ci(id: "`+id+`") { name label: hostname } }`))
	if status != http.StatusOK {
		t.Fatalf("status %d: %v", status, body)
	}
	got := body["data"].(map[string]any)["ci"].(map[string]any)
	if len(got) != 2 || got["name"] != "core-router" || got["label"] != "rtr01" {
		t.Errorf("projected ci %v, want only name and aliased hostname", got)
	}

	status, body = run(t, h, perms, gql(`{ cis(first: 10) { totalCount edges { node { name } } } }`))
	if status != http.StatusOK {
		t.Fatalf("status %d: %v", status, body)
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), "SN-SECRET") || strings.Contains(string(raw), "pageInfo") || strings.Contains(string(raw), "nodes") {
		t.Errorf("connection carries unselected fields: %s", raw)
	}
	if !strings.Contains(string(raw), `"name":"core-router"`) {
		t.Errorf("connection lacks the selected node name: %s", raw)
	}

	if status, _ := run(t, h, perms, gql(`{ ci(id: "`+id+`") }`)); status != http.StatusBadRequest {
		t.Errorf("query without selection set: status %d, want 400", status)
	}
}

func TestMutationsAreRefused(t *testing.T) {
	h, _ := newFixture(t)
	perms := []identity.Permission{identity.PermCIRead, identity.PermCIWrite}
	status, body := run(t, h, perms, gql(`mutation { cis { totalCount } }`))
	if status != http.StatusBadRequest || !strings.Contains(strings.ToLower(body["errors"].([]any)[0].(map[string]any)["message"].(string)), "mutation") {
		t.Errorf("mutation: status %d body %v, want 400", status, body)
	}
}
