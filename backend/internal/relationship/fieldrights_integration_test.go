package relationship_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// relRightsFixture is the fixture of the field permission tests of edges
// (RBA-07, WP-770).
type relRightsFixture struct {
	f       *scopetest.Fixture
	mux     *chi.Mux
	repo    *relationship.PGRepository
	types   *relationshiptype.PGRepository
	grants  *permission.PGFieldGrants
	roles   map[string]string // name → role id
	typeIDs map[string]string // name → relationship_type id
	cis     map[string]string // name → CI id
}

// relPerms are the permissions of every role of the tests.
var relPerms = []identity.Permission{"ci:read", "relationship:read", "relationship:write"}

func seedRelRights(t *testing.T) *relRightsFixture {
	t.Helper()
	f := scopetest.Seed(t, "ab")
	bg := context.Background()
	x := &relRightsFixture{f: f, repo: relationship.NewPGRepository(f.App), types: relationshiptype.NewPGRepository(f.App),
		grants: permission.NewPGFieldGrants(f.App), roles: map[string]string{}, typeIDs: map[string]string{}, cis: map[string]string{}}
	for _, c := range []struct{ name, client string }{
		{"a1", f.Client1}, {"a2", f.Client1}, {"b1", f.Client2}, {"b2", f.Client2}, {"ow", ""},
	} {
		x.cis[c.name] = f.CI(t, f.OrgA, c.client, "wp770-"+c.name)
	}
	for _, name := range []string{"full", "clientA", "clientB", "unscoped", "other"} {
		id := f.ID()
		if _, err := f.Admin.Exec(bg, `INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, $3, $4)`,
			id, f.OrgA, "wp770-"+name, `["ci:read","relationship:read","relationship:write"]`); err != nil {
			t.Fatalf("seed role %s: %v", name, err)
		}
		x.roles[name] = id
	}
	for _, key := range []string{"wp770-cables", "wp770-open"} {
		id := f.ID()
		if _, err := f.Admin.Exec(bg, `INSERT INTO relationship_type (id, organization_id, key, display_name, reverse_name)
			VALUES ($1, $2, $3, $3, $3)`, id, f.OrgA, key); err != nil {
			t.Fatalf("seed relationship type %s: %v", key, err)
		}
		x.typeIDs[key] = id
	}
	var system string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM relationship_type WHERE key = 'powered_by' AND organization_id IS NULL`).Scan(&system); err != nil {
		t.Fatalf("system type powered_by: %v", err)
	}
	x.typeIDs["powered_by"] = system
	x.mux = chi.NewRouter()
	relationship.NewHandler(x.repo, x.types).RegisterRoutes(x.mux)
	return x
}

// edge inserts an edge through the maintenance pool (the scope columns are
// derived by its trigger) and returns its id.
func (x *relRightsFixture) edge(t *testing.T, source, target, relType string, attrs map[string]any, notes string, confidence any) string {
	t.Helper()
	raw, err := json.Marshal(attrs)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err = x.f.Admin.QueryRow(context.Background(), `INSERT INTO ci_relationship
		(organization_id, source_ci_id, target_ci_id, rel_type, attributes, source, notes, confidence)
		VALUES ($1, $2, $3, $4, $5::jsonb, 'manual', NULLIF($6, ''), $7::numeric) RETURNING id::text`,
		x.f.OrgA, x.cis[source], x.cis[target], relType, string(raw), notes, confidence).Scan(&id); err != nil {
		t.Fatalf("seed edge %s %s→%s: %v", relType, source, target, err)
	}
	return id
}

// reset removes every field permission row of organization A.
func (x *relRightsFixture) reset(t *testing.T) {
	t.Helper()
	for _, table := range []string{"field_grant", "object_grant", "field_sensitivity"} {
		if _, err := x.f.Admin.Exec(context.Background(), `DELETE FROM `+table+` WHERE organization_id = $1`, x.f.OrgA); err != nil {
			t.Fatalf("reset %s: %v", table, err)
		}
	}
}

// typeGrant stores a grant of the object kind relationship for the role on
// the relationship type (typeName) or the field key.
func (x *relRightsFixture) grant(t *testing.T, role, typeName, fieldKey, level string) {
	t.Helper()
	var typeID, key any
	if typeName != "" {
		typeID = x.typeIDs[typeName]
	}
	if fieldKey != "" {
		key = fieldKey
	}
	if _, err := x.f.Admin.Exec(context.Background(), `INSERT INTO field_grant
		(organization_id, role_id, object_kind, relationship_type_id, field_key, level) VALUES ($1, $2, 'relationship', $3, $4, $5)`,
		x.f.OrgA, x.roles[role], typeID, key, level); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
}

// assignment is a role assignment of the principal: the role and its client
// scope (empty: the whole organization).
type assignment struct {
	role   string
	client string
}

// ctx returns the request context of an org-wide principal of organization
// A whose field policy is that of the assignments, resolved from the
// stored field permissions.
func (x *relRightsFixture) ctx(t *testing.T, assignments ...assignment) context.Context {
	t.Helper()
	grants := make([]identity.Grant, 0, len(assignments))
	for _, a := range assignments {
		g := identity.Grant{RoleID: x.roles[a.role], Permissions: relPerms}
		if a.client != "" {
			g.Clients = []string{a.client}
		}
		grants = append(grants, g)
	}
	resolver := permission.NewFieldResolver(nil, nil, x.grants, nil)
	policy, err := resolver.PolicyForGrants(context.Background(), x.f.OrgA, grants, nil)
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	return tenant.WithTenant(permission.WithPolicy(x.f.OrgCtx(x.f.OrgA), policy),
		tenant.TenantInfo{OrganizationID: x.f.OrgA, UserID: x.f.User})
}

func (x *relRightsFixture) do(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
	w := httptest.NewRecorder()
	x.mux.ServeHTTP(w, req)
	return w
}

// edgeJSON is an edge of an answer with the members the tests look at.
type edgeJSON struct {
	ID         string                     `json:"id"`
	Attributes map[string]json.RawMessage `json:"attributes"`
	Raw        map[string]json.RawMessage `json:"-"`
}

func decodeEdge(t *testing.T, raw json.RawMessage) edgeJSON {
	t.Helper()
	var e edgeJSON
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatalf("decode edge: %v", err)
	}
	if err := json.Unmarshal(raw, &e.Raw); err != nil {
		t.Fatalf("decode edge: %v", err)
	}
	return e
}

// list returns the edges of a GET list answer by id.
func (x *relRightsFixture) list(t *testing.T, ctx context.Context, path string) map[string]edgeJSON {
	t.Helper()
	w := x.do(ctx, http.MethodGet, path, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
	}
	var body struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	out := map[string]edgeJSON{}
	for _, raw := range body.Data {
		e := decodeEdge(t, raw)
		out[e.ID] = e
	}
	return out
}

// include returns the edges as GET /cis/{id}?include=relationships embeds
// them: the list of the CI, projected by relationship.ProjectEdges as the
// include function of server/router.go does.
func (x *relRightsFixture) include(t *testing.T, ctx context.Context, ci string) map[string]edgeJSON {
	t.Helper()
	rels, _, err := x.repo.List(ctx, x.f.OrgA, x.cis[ci], api.PaginationParams{Limit: 200})
	if err != nil {
		t.Fatalf("include list: %v", err)
	}
	if rels, err = relationship.ProjectEdges(ctx, x.f.OrgA, rels, x.types); err != nil {
		t.Fatalf("include projection: %v", err)
	}
	raw, err := json.Marshal(rels)
	if err != nil {
		t.Fatal(err)
	}
	var items []json.RawMessage
	if err = json.Unmarshal(raw, &items); err != nil {
		t.Fatal(err)
	}
	out := map[string]edgeJSON{}
	for _, item := range items {
		e := decodeEdge(t, item)
		out[e.ID] = e
	}
	return out
}

// attrKeys returns the sorted attribute keys of the edge.
func (e *edgeJSON) attrKeys() []string {
	keys := make([]string, 0, len(e.Attributes))
	for k := range e.Attributes {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// expectEdge checks the attribute keys and whether notes and confidence are
// present.
func expectEdge(t *testing.T, where string, edges map[string]edgeJSON, id string, keys []string, withCore bool) {
	t.Helper()
	e, ok := edges[id]
	if !ok {
		t.Errorf("%s: edge %s missing", where, id)
		return
	}
	if e.Attributes == nil {
		t.Errorf("%s: edge %s without attributes object", where, id)
	}
	if got := e.attrKeys(); !slices.Equal(got, keys) {
		t.Errorf("%s: edge %s attributes %v, want %v", where, id, got, keys)
	}
	_, hasNotes := e.Raw["notes"]
	_, hasConfidence := e.Raw["confidence"]
	if hasNotes != withCore || hasConfidence != withCore {
		t.Errorf("%s: edge %s notes %v confidence %v, want %v", where, id, hasNotes, hasConfidence, withCore)
	}
	for _, member := range []string{"id", "rel_type", "source_ci_id", "target_ci_id", "source", "verification_state", "created_at"} {
		if _, ok := e.Raw[member]; !ok {
			t.Errorf("%s: edge %s lost the member %s", where, id, member)
		}
	}
}

func forbiddenFields(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	var body struct {
		Type   string   `json:"type"`
		Fields []string `json:"fields"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode 403: %v", err)
	}
	if body.Type != api.ProblemTypeForbidden {
		t.Errorf("403 type %q, want %q", body.Type, api.ProblemTypeForbidden)
	}
	return body.Fields
}

// TestRelationshipFieldRights covers RBA-07 at the relation level (WP-770):
// the fields attributes.<key>, notes and confidence of an edge follow the
// grants of the object kind relationship (type grants, field grants, field
// sensitivity, object grants) in GET /relationships, GET
// /cis/{id}/relationships and the include relationships of GET /cis/{id};
// POST and PATCH check them by the write rule of WP-265; both ends of the
// edge count (E-140); without grants the answers are unchanged.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRelationshipFieldRights(t *testing.T) {
	x := seedRelRights(t)
	full := assignment{role: "full"}
	attrs := map[string]any{"cost_center": "X", "secret": "s"}
	cables := x.edge(t, "a1", "a2", "wp770-cables", attrs, "note", 0.5)
	power := x.edge(t, "a1", "a2", "powered_by", map[string]any{"outlet": "3"}, "feed", 0.9)
	open := x.edge(t, "a1", "a2", "wp770-open", map[string]any{"cost_center": "Z"}, "open", 0.7)
	ciPath := "/api/v1/cis/" + x.cis["a1"] + "/relationships"

	t.Run("without grants the answers are unchanged", func(t *testing.T) {
		x.reset(t)
		// A grant of a role the principal does not hold restricts nothing.
		x.grant(t, "other", "wp770-cables", "", "none")
		for _, ctx := range []context.Context{x.ctx(t, full), x.ctx(t, full, assignment{role: "clientA", client: x.f.Client1})} {
			for _, filter := range []relationship.ListFilter{{}, {CIID: x.cis["a1"]}} {
				path := "/api/v1/relationships"
				if filter.CIID != "" {
					path = ciPath
				}
				got := x.do(ctx, http.MethodGet, path, "")
				page := api.ParsePagination(httptest.NewRequest(http.MethodGet, path, nil))
				rels, total, err := x.repo.ListFiltered(ctx, x.f.OrgA, filter, page)
				if err != nil {
					t.Fatal(err)
				}
				want := httptest.NewRecorder()
				api.WriteJSON(want, http.StatusOK, api.ListResponse[relationship.Relationship]{Data: rels, Total: total,
					Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total})
				if !bytes.Equal(got.Body.Bytes(), want.Body.Bytes()) {
					t.Errorf("GET %s differs from the answer without field permissions:\n%s\n%s", path, got.Body.String(), want.Body.String())
				}
			}
			rels, _, err := x.repo.List(ctx, x.f.OrgA, x.cis["a1"], api.PaginationParams{Limit: 200})
			if err != nil {
				t.Fatal(err)
			}
			projected, err := relationship.ProjectEdges(ctx, x.f.OrgA, rels, x.types)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(rels)
			after, _ := json.Marshal(projected)
			if !bytes.Equal(before, after) {
				t.Errorf("include relationships changed without field permissions")
			}
		}
	})

	t.Run("type grant none", func(t *testing.T) {
		x.reset(t)
		x.grant(t, "full", "wp770-cables", "", "none")
		x.grant(t, "full", "powered_by", "", "none")
		ctx := x.ctx(t, full)
		for where, edges := range map[string]map[string]edgeJSON{
			"GET /relationships":      x.list(t, ctx, "/api/v1/relationships"),
			"GET /cis/{id}/relations": x.list(t, ctx, ciPath),
			"include relationships":   x.include(t, ctx, "a1"),
		} {
			expectEdge(t, where, edges, cables, []string{}, false)
			expectEdge(t, where, edges, power, []string{}, false)
			expectEdge(t, where, edges, open, []string{"cost_center"}, true)
		}
		body := `{"source_ci_id":"` + x.cis["a2"] + `","target_ci_id":"` + x.cis["a1"] + `","rel_type":"wp770-cables",` +
			`"attributes":{"cost_center":"x","alpha":null},"notes":"n"}`
		w := x.do(ctx, http.MethodPost, "/api/v1/relationships", body)
		if w.Code != http.StatusForbidden {
			t.Fatalf("POST with attributes: %d %s, want 403", w.Code, w.Body.String())
		}
		if got := forbiddenFields(t, w); !slices.Equal(got, []string{"attributes.alpha", "attributes.cost_center", "notes"}) {
			t.Errorf("POST 403 fields %v", got)
		}
		w = x.do(ctx, http.MethodPost, "/api/v1/relationships", `{"source_ci_id":"`+x.cis["a2"]+`","target_ci_id":"`+x.cis["a1"]+`","rel_type":"wp770-cables"}`)
		if w.Code != http.StatusCreated {
			t.Fatalf("POST without attributes and notes: %d %s, want 201", w.Code, w.Body.String())
		}
		created := decodeEdge(t, w.Body.Bytes())
		if len(created.Attributes) != 0 || created.Attributes == nil {
			t.Errorf("POST answer attributes %v, want {}", created.Attributes)
		}
		var stored string
		if err := x.f.Admin.QueryRow(context.Background(), `SELECT attributes::text FROM ci_relationship WHERE id = $1`, created.ID).Scan(&stored); err != nil || !strings.Contains(stored, "provenance") {
			t.Errorf("stored attributes %s (%v), want the provenance", stored, err)
		}
		if _, err := x.f.Admin.Exec(context.Background(), `DELETE FROM ci_relationship WHERE id = $1`, created.ID); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("type grant read", func(t *testing.T) {
		x.reset(t)
		x.grant(t, "full", "wp770-cables", "", "read")
		ctx := x.ctx(t, full)
		expectEdge(t, "GET", x.list(t, ctx, "/api/v1/relationships"), cables, []string{"cost_center", "secret"}, true)
		path := "/api/v1/relationships/" + cables
		w := x.do(ctx, http.MethodPatch, path, `{"attributes":{"cost_center":"Y"}}`)
		if w.Code != http.StatusForbidden || !slices.Equal(forbiddenFields(t, w), []string{"attributes.cost_center"}) {
			t.Fatalf("PATCH with a changed attribute: %d %s, want 403 on attributes.cost_center", w.Code, w.Body.String())
		}
		var stored string
		if err := x.f.Admin.QueryRow(context.Background(), `SELECT attributes->>'cost_center' FROM ci_relationship WHERE id = $1`, cables).Scan(&stored); err != nil || stored != "X" {
			t.Fatalf("attribute after 403: %q (%v), want X", stored, err)
		}
		w = x.do(ctx, http.MethodPatch, path, `{"attributes":{"cost_center":"X"},"notes":"note","confidence":0.5}`)
		if w.Code != http.StatusOK {
			t.Fatalf("PATCH with unchanged values: %d %s, want 200", w.Code, w.Body.String())
		}
		w = x.do(ctx, http.MethodPatch, path, `{"notes":"changed","confidence":0.6}`)
		if w.Code != http.StatusForbidden || !slices.Equal(forbiddenFields(t, w), []string{"confidence", "notes"}) {
			t.Fatalf("PATCH with changed notes and confidence: %d %s, want 403", w.Code, w.Body.String())
		}
		w = x.do(ctx, http.MethodPatch, path, `{"verification_state":"disputed"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("PATCH with verification_state only: %d %s, want 200", w.Code, w.Body.String())
		}
	})

	t.Run("field grant none under type grant write", func(t *testing.T) {
		x.reset(t)
		x.grant(t, "full", "wp770-cables", "", "write")
		x.grant(t, "full", "", "attributes.secret", "none")
		ctx := x.ctx(t, full)
		expectEdge(t, "GET", x.list(t, ctx, "/api/v1/relationships"), cables, []string{"cost_center"}, true)
		w := x.do(ctx, http.MethodPatch, "/api/v1/relationships/"+cables, `{"attributes":{"cost_center":"W","secret":"s"}}`)
		if w.Code != http.StatusForbidden || !slices.Equal(forbiddenFields(t, w), []string{"attributes.secret"}) {
			t.Fatalf("PATCH of an unreadable attribute with its value: %d %s, want 403 on attributes.secret", w.Code, w.Body.String())
		}
		w = x.do(ctx, http.MethodPatch, "/api/v1/relationships/"+cables, `{"attributes":{"cost_center":"W"}}`)
		if w.Code != http.StatusOK {
			t.Fatalf("PATCH of a writable attribute: %d %s", w.Code, w.Body.String())
		}
		patched := decodeEdge(t, w.Body.Bytes())
		if got := patched.attrKeys(); !slices.Equal(got, []string{"cost_center"}) {
			t.Errorf("PATCH answer attributes %v, want only cost_center", got)
		}
	})

	t.Run("field sensitivity without grant", func(t *testing.T) {
		x.reset(t)
		if _, err := x.f.Admin.Exec(context.Background(), `INSERT INTO field_sensitivity (organization_id, object_kind, field_key)
			VALUES ($1, 'relationship', 'attributes.secret')`, x.f.OrgA); err != nil {
			t.Fatal(err)
		}
		expectEdge(t, "GET", x.list(t, x.ctx(t, full), "/api/v1/relationships"), cables, []string{"cost_center"}, true)
	})

	t.Run("object grant none on one edge", func(t *testing.T) {
		x.reset(t)
		if _, err := x.f.Admin.Exec(context.Background(), `INSERT INTO object_grant (organization_id, grantee_role_id, object_kind, object_id, level)
			VALUES ($1, $2, 'relationship', $3, 'none')`, x.f.OrgA, x.roles["full"], cables); err != nil {
			t.Fatal(err)
		}
		edges := x.list(t, x.ctx(t, full), "/api/v1/relationships")
		expectEdge(t, "GET", edges, cables, []string{}, false)
		expectEdge(t, "GET", edges, open, []string{"cost_center"}, true)
		expectEdge(t, "GET", edges, power, []string{"outlet"}, true)
	})

	t.Run("scope of both ends", func(t *testing.T) {
		x.reset(t)
		x.grant(t, "clientA", "wp770-cables", "", "none")
		cross := x.edge(t, "a1", "b1", "wp770-cables", map[string]any{"k": 1}, "n", 0.1)
		inB := x.edge(t, "b1", "b2", "wp770-cables", map[string]any{"k": 2}, "n", 0.2)
		bOrgWide := x.edge(t, "b1", "ow", "wp770-cables", map[string]any{"k": 3}, "n", 0.3)
		aOrgWide := x.edge(t, "a1", "ow", "wp770-cables", map[string]any{"k": 4}, "n", 0.4)
		a := assignment{role: "clientA", client: x.f.Client1}
		b := assignment{role: "clientB", client: x.f.Client2}
		edges := x.list(t, x.ctx(t, a, b), "/api/v1/relationships")
		expectEdge(t, "A and B", edges, cross, []string{}, false)
		expectEdge(t, "A and B", edges, inB, []string{"k"}, true)
		// The org-wide end lies only in the scope of assignments without a
		// client scope (E-140): neither assignment covers it.
		expectEdge(t, "A and B", edges, bOrgWide, []string{}, false)
		expectEdge(t, "A and B", edges, aOrgWide, []string{}, false)

		// An assignment without client scope decides the org-wide end.
		x.grant(t, "unscoped", "wp770-cables", "", "read")
		u := assignment{role: "unscoped"}
		edges = x.list(t, x.ctx(t, a, b, u), "/api/v1/relationships")
		expectEdge(t, "A, B and unscoped read", edges, bOrgWide, []string{"k"}, true)
		w := x.do(x.ctx(t, a, b, u), http.MethodPatch, "/api/v1/relationships/"+bOrgWide, `{"notes":"m"}`)
		if w.Code != http.StatusForbidden {
			t.Errorf("PATCH of an edge whose org-wide end is read only: %d %s, want 403", w.Code, w.Body.String())
		}
		w = x.do(x.ctx(t, a, b, u), http.MethodPatch, "/api/v1/relationships/"+inB, `{"notes":"m"}`)
		if w.Code != http.StatusOK {
			t.Errorf("PATCH of an edge within B: %d %s, want 200", w.Code, w.Body.String())
		}
		// POST between a CI of A and one of B: the lower level (A: none).
		w = x.do(x.ctx(t, a, b), http.MethodPost, "/api/v1/relationships",
			`{"source_ci_id":"`+x.cis["b2"]+`","target_ci_id":"`+x.cis["a2"]+`","rel_type":"wp770-cables","notes":"n"}`)
		if w.Code != http.StatusForbidden || !slices.Equal(forbiddenFields(t, w), []string{"notes"}) {
			t.Errorf("POST across A and B with notes: %d %s, want 403 on notes", w.Code, w.Body.String())
		}
		x.reset(t)
		x.grant(t, "unscoped", "wp770-cables", "", "none")
		edges = x.list(t, x.ctx(t, a, b, u), "/api/v1/relationships")
		expectEdge(t, "A, B and unscoped none", edges, bOrgWide, []string{}, false)
		for _, id := range []string{cross, inB, bOrgWide, aOrgWide} {
			if _, err := x.f.Admin.Exec(context.Background(), `DELETE FROM ci_relationship WHERE id = $1`, id); err != nil {
				t.Fatal(err)
			}
		}
	})

	t.Run("tenant row with a system key", func(t *testing.T) {
		x.reset(t)
		x.grant(t, "full", "powered_by", "", "none")
		tenantRow := x.f.ID()
		if _, err := x.f.Admin.Exec(context.Background(), `INSERT INTO relationship_type (id, organization_id, key, display_name, reverse_name)
			VALUES ($1, $2, 'powered_by', 'powered by', 'powers')`, tenantRow, x.f.OrgA); err != nil {
			t.Fatalf("tenant row powered_by: %v", err)
		}
		x.typeIDs["tenant powered_by"] = tenantRow
		// The grant of the system row does not reach the edges: the tenant
		// row is their type.
		expectEdge(t, "system row grant", x.list(t, x.ctx(t, full), "/api/v1/relationships"), power, []string{"outlet"}, true)
		x.grant(t, "full", "tenant powered_by", "", "none")
		expectEdge(t, "tenant row grant", x.list(t, x.ctx(t, full), "/api/v1/relationships"), power, []string{}, false)
	})
}
