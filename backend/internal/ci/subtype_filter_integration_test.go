package ci_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/topology"
)

// subtypeFixture is the type tree of the WP-708 tests in organization A:
// P with the children C and S, G below C, and the unrelated root O. P
// defines the searchable wp708_rack and the unique wp708_tag (both
// inheritable) and the unique wp708_local without inheritable.
type subtypeFixture struct {
	f                *scopetest.Fixture
	ctx              context.Context
	types            *citype.PGRepository
	p, c, g, s, o    string
	rack, tag, local *citype.AttributeDefinition
}

func newSubtypeFixture(t *testing.T) *subtypeFixture {
	t.Helper()
	f := scopetest.Seed(t, "a7")
	x := &subtypeFixture{f: f, types: citype.NewPGRepository(f.App)}
	x.ctx = tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	create := func(key, parent string) string {
		t.Helper()
		typ := &citype.Type{OrganizationID: f.OrgA, Key: key, Name: key, ParentTypeID: parent, IsObservable: true}
		if err := x.types.Create(x.ctx, typ); err != nil {
			t.Fatalf("create type %s: %v", key, err)
		}
		return typ.ID
	}
	x.p = create("wp708-p", "")
	x.c = create("wp708-c", x.p)
	x.g = create("wp708-g", x.c)
	x.s = create("wp708-s", x.p)
	x.o = create("wp708-o", "")
	x.rack = &citype.AttributeDefinition{CITypeID: x.p, Key: "wp708_rack", DataType: "text", Searchable: true, Inheritable: true}
	x.tag = &citype.AttributeDefinition{CITypeID: x.p, Key: "wp708_tag", DataType: "text", Unique: true, UniqueScope: "org", Inheritable: true}
	x.local = &citype.AttributeDefinition{CITypeID: x.p, Key: "wp708_local", DataType: "text", Unique: true, UniqueScope: "org"}
	for _, d := range []*citype.AttributeDefinition{x.rack, x.tag, x.local} {
		if err := x.types.CreateAttributeDefinition(x.ctx, f.OrgA, d); err != nil {
			t.Fatalf("create definition %s: %v", d.Key, err)
		}
	}
	return x
}

// ci inserts a CI of the type with the attributes (maintenance pool).
func (x *subtypeFixture) ci(t *testing.T, typeID, name string, attrs map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(attrs)
	if err != nil {
		t.Fatal(err)
	}
	id := x.f.ID()
	if _, err := x.f.Admin.Exec(context.Background(), `INSERT INTO ci (id, organization_id, client_id, ci_type_id, name, status, attributes)
		VALUES ($1, $2, $3, $4, $5, 'active', $6)`, id, x.f.OrgA, x.f.Client1, typeID, name, raw); err != nil {
		t.Fatalf("insert ci %s: %v", name, err)
	}
	return id
}

// TestSubtypeFilterAgainstPostgres covers WP-708 (MET-06) for the type
// filters: the CI list with ci_type_id, filter[ci_type_id] and
// filter[ci_type_id][in] returns the CIs of the subtypes with the matching
// total, several type filters intersect, an attribute filter on an
// inherited searchable attribute with the parent type finds the CIs of the
// subtypes, and the topology with and without a root includes the
// subtypes (without TypeSubtrees the type must match).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSubtypeFilterAgainstPostgres(t *testing.T) {
	x := newSubtypeFixture(t)
	f := x.f
	p1 := x.ci(t, x.p, "wp708-p1", map[string]any{"wp708_rack": "R1"})
	c1 := x.ci(t, x.c, "wp708-c1", map[string]any{"wp708_rack": "R1"})
	g1 := x.ci(t, x.g, "wp708-g1", map[string]any{"wp708_rack": "R2"})
	s1 := x.ci(t, x.s, "wp708-s1", map[string]any{"wp708_rack": "R1"})
	o1 := x.ci(t, x.o, "wp708-o1", nil)

	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(ci.NewPGRepository(f.App))).RegisterRoutes(mux)
	list := func(query url.Values) ([]string, int) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/cis?"+query.Encode(), http.NoBody).WithContext(x.ctx))
		var body struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			Total int `json:"total"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("%s: %d %s", query.Encode(), w.Code, w.Body.String())
		}
		ids := make([]string, 0, len(body.Data))
		for _, row := range body.Data {
			ids = append(ids, row.ID)
		}
		slices.Sort(ids)
		return ids, body.Total
	}
	sorted := func(ids ...string) []string {
		slices.Sort(ids)
		return ids
	}
	for _, c := range []struct {
		name  string
		query url.Values
		want  []string
	}{
		{"ci_type_id of the root", url.Values{"ci_type_id": {x.p}}, sorted(p1, c1, g1, s1)},
		{"ci_type_id of a child", url.Values{"ci_type_id": {x.c}}, sorted(c1, g1)},
		{"ci_type_id of a leaf", url.Values{"ci_type_id": {x.g}}, sorted(g1)},
		{"filter[ci_type_id]", url.Values{"filter[ci_type_id]": {x.c}}, sorted(c1, g1)},
		{"filter[ci_type_id][in]", url.Values{"filter[ci_type_id][in]": {x.c + "," + x.s}}, sorted(c1, g1, s1)},
		{"filter[ci_type_id][in] with an unrelated type", url.Values{"filter[ci_type_id][in]": {x.g + "," + x.o}}, sorted(g1, o1)},
		{"two type filters intersect", url.Values{"ci_type_id": {x.p}, "filter[ci_type_id]": {x.c}}, sorted(c1, g1)},
		{"inherited attribute with the parent type", url.Values{"ci_type_id": {x.p}, "filter[attributes.wp708_rack]": {"R1"}}, sorted(p1, c1, s1)},
		{"inherited attribute with a subtype", url.Values{"ci_type_id": {x.c}, "filter[attributes.wp708_rack]": {"R1"}}, sorted(c1)},
		{"inherited attribute with a leaf, in", url.Values{"filter[ci_type_id][in]": {x.g}, "filter[attributes.wp708_rack][in]": {"R1,R2"}}, sorted(g1)},
		{"inherited attribute without a type", url.Values{"filter[attributes.wp708_rack]": {"R1"}}, sorted(p1, c1, s1)},
	} {
		got, total := list(c.query)
		if !slices.Equal(got, c.want) || total != len(c.want) {
			t.Errorf("%s: %v (total %d), want %v", c.name, got, total, c.want)
		}
	}
	// A page of one keeps the total of the subtree.
	if got, total := list(url.Values{"ci_type_id": {x.p}, "limit": {"1"}}); len(got) != 1 || total != 4 {
		t.Errorf("page of one: %v total %d, want one CI of 4", got, total)
	}
	// The attribute is not defined for the unrelated type.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/cis?"+url.Values{"ci_type_id": {x.o}, "filter[attributes.wp708_rack]": {"R1"}}.Encode(),
		http.NoBody).WithContext(x.ctx))
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("attribute filter with an unrelated type: %d %s, want 422", w.Code, w.Body.String())
	}
	// A type id that is no UUID stays a 400.
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/cis?ci_type_id=no-uuid", http.NoBody).WithContext(x.ctx))
	if w.Code != http.StatusBadRequest {
		t.Errorf("ci_type_id no UUID: %d %s, want 400", w.Code, w.Body.String())
	}

	// Topology: p1 is connected with every other CI.
	if _, err := f.Admin.Exec(context.Background(), `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES
		($1, $2, $3, 'connected_to'), ($1, $2, $4, 'connected_to'), ($1, $2, $5, 'connected_to'), ($1, $2, $6, 'connected_to')`,
		f.OrgA, p1, c1, g1, s1, o1); err != nil {
		t.Fatal(err)
	}
	nodes := func(h *topology.Handler, root, typeID string) []string {
		t.Helper()
		g, err := h.Graph(x.ctx, f.OrgA, root, 2, &ci.FilterParams{TypeID: typeID})
		if err != nil {
			t.Fatalf("topology root %q type %s: %v", root, typeID, err)
		}
		ids := make([]string, 0, len(g.Nodes))
		for _, n := range g.Nodes {
			ids = append(ids, n.ID)
		}
		slices.Sort(ids)
		return ids
	}
	plain := topology.NewHandler(ci.NewPGRepository(f.App), relationship.NewPGRepository(f.App))
	wired := topology.NewHandler(ci.NewPGRepository(f.App), relationship.NewPGRepository(f.App)).WithTypeSubtrees(x.types)
	for _, c := range []struct {
		name      string
		h         *topology.Handler
		root, typ string
		want      []string
	}{
		{"without a root", wired, "", x.c, sorted(c1, g1)},
		{"without a root, parent type", wired, "", x.p, sorted(p1, c1, g1, s1)},
		{"from the root", wired, p1, x.c, sorted(p1, c1, g1)},
		{"from the root, leaf type", wired, p1, x.g, sorted(p1, g1)},
		{"from the root, unwired: the type must match", plain, p1, x.c, sorted(p1, c1)},
	} {
		if got := nodes(c.h, c.root, c.typ); !slices.Equal(got, c.want) {
			t.Errorf("topology %s: %v, want %v", c.name, got, c.want)
		}
	}
}

// TestSubtypeUniqueAgainstPostgres covers WP-708 (MET-06, MET-15) for the
// uniqueness: an inherited unique attribute holds across the subtree of
// the defining type (two CIs of sibling subtypes with the same value are
// 409 with violations), a definition without inheritable only for its
// type, and a parent change that brings CIs with the same value into one
// subtree is 409 on parent_type_id.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSubtypeUniqueAgainstPostgres(t *testing.T) {
	x := newSubtypeFixture(t)
	f := x.f
	x.ci(t, x.s, "wp708-s1", map[string]any{"wp708_tag": "T1"})
	p1 := x.ci(t, x.p, "wp708-p1", map[string]any{"wp708_local": "L1"})

	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(ci.NewPGRepository(f.App))).RegisterRoutes(mux)
	post := func(body map[string]any) (int, map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/cis", bytes.NewReader(raw)).WithContext(x.ctx))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	// G (below C) and S are siblings below P, which defines wp708_tag.
	code, out := post(map[string]any{"ci_type_id": x.g, "name": "wp708-g1", "client_id": f.Client1,
		"attributes": map[string]any{"wp708_tag": "T1"}})
	if code != http.StatusConflict {
		t.Fatalf("same inherited unique value in a sibling subtype: %d %v, want 409", code, out)
	}
	if v, _ := out["violations"].([]any); len(v) != 1 || v[0].(map[string]any)["field"] != "wp708_tag" {
		t.Errorf("violations %v, want wp708_tag", out["violations"])
	}
	if code, out = post(map[string]any{"ci_type_id": x.g, "name": "wp708-g2", "client_id": f.Client1,
		"attributes": map[string]any{"wp708_tag": "T2"}}); code != http.StatusCreated {
		t.Fatalf("another value: %d %v", code, out)
	}

	// wp708_local without inheritable covers the CIs of P only.
	repo := ci.NewPGRepository(f.App)
	for _, c := range []struct {
		name, typ string
		want      []string
	}{
		{"same type", x.p, []string{"wp708_local"}},
		{"subtype", x.c, nil},
	} {
		got, err := repo.UniqueConflicts(x.ctx, f.OrgA, c.typ, "", "", map[string]any{"wp708_local": "L1"})
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("wp708_local, %s: %v %v, want %v", c.name, got, err, c.want)
		}
	}
	if got, err := repo.UniqueConflicts(x.ctx, f.OrgA, x.p, "", p1, map[string]any{"wp708_local": "L1"}); err != nil || len(got) != 0 {
		t.Errorf("own value: %v %v", got, err)
	}

	// M, a root, holds T1 as well; below P it would share it with s1.
	m := &citype.Type{OrganizationID: f.OrgA, Key: "wp708-m", Name: "wp708-m", IsObservable: true}
	if err := x.types.Create(x.ctx, m); err != nil {
		t.Fatal(err)
	}
	x.ci(t, m.ID, "wp708-m1", map[string]any{"wp708_tag": "T1"})
	parent := x.c
	_, err := x.types.Update(x.ctx, f.OrgA, m.ID, citype.UpdateTypeRequest{ParentTypeID: &parent})
	var fe *api.FieldError
	if !errors.As(err, &fe) || fe.Field != "parent_type_id" || fe.Status != http.StatusConflict {
		t.Fatalf("parent change that joins duplicates: %v, want 409 on parent_type_id", err)
	}
	// Below O nothing inherits wp708_tag.
	other := x.o
	if _, err = x.types.Update(x.ctx, f.OrgA, m.ID, citype.UpdateTypeRequest{ParentTypeID: &other}); err != nil {
		t.Fatalf("parent change without duplicates: %v", err)
	}
}
