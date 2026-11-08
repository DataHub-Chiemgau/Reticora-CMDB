package citype

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// inheritRepo seeds the chain system "inh-root" → org-a "inh-mid" →
// org-a "inh-leaf" and a type of org-b.
func inheritRepo() *MemoryRepository {
	repo := NewMemoryRepository()
	repo.SeedType(&Type{ID: "inh-root", Key: "inh_root", Name: "Root", IsSystem: true, IsActive: true,
		Fields: []Field{
			{Name: "vendor", DataType: fieldmeta.TypeText, Inheritable: true},
			{Name: "secret", DataType: fieldmeta.TypeText, Inheritable: false},
			{Name: "tier", DataType: fieldmeta.TypeEnum, EnumValues: []string{"a", "b", "c"}, Inheritable: true},
		}})
	repo.SeedType(&Type{ID: "inh-mid", OrganizationID: "org-a", Key: "inh_mid", Name: "Mid", IsActive: true,
		ParentTypeID: "inh-root",
		Fields: []Field{
			{Name: "tier", DataType: fieldmeta.TypeEnum, EnumValues: []string{"a", "b"}, Inheritable: true},
			{Name: "mid_only", DataType: fieldmeta.TypeText, Inheritable: false},
		}})
	repo.SeedType(&Type{ID: "inh-leaf", OrganizationID: "org-a", Key: "inh_leaf", Name: "Leaf", IsActive: true,
		ParentTypeID: "inh-mid",
		Fields: []Field{
			{Name: "vendor", DataType: fieldmeta.TypeText, Required: true, Inheritable: true},
		}})
	repo.SeedType(&Type{ID: "org-b-type", OrganizationID: "org-b", Key: "inh_b", Name: "B", IsActive: true})
	return repo
}

func typeIDs(types []Type) []string {
	out := make([]string, 0, len(types))
	for i := range types {
		out = append(out, types[i].ID)
	}
	return out
}

// TestMemoryTypeChainAndSubtree covers WP-248 (MET-06) without a database:
// the chain runs root → type, the subtree holds the type and its visible
// descendants, and the parent rules of trg_ci_type_parent_guard apply to
// the in-memory repository (unknown or foreign parent, system rule, cycle,
// at most MaxTypeChainDepth levels including the moved subtree).
func TestMemoryTypeChainAndSubtree(t *testing.T) {
	ctx := context.Background()
	repo := inheritRepo()

	chain, err := repo.TypeChain(ctx, "org-a", "inh-leaf")
	if err != nil {
		t.Fatal(err)
	}
	if got := typeIDs(chain); !slices.Equal(got, []string{"inh-root", "inh-mid", "inh-leaf"}) {
		t.Errorf("chain = %v", got)
	}
	if len(chain[0].Fields) != 3 {
		t.Errorf("root fields = %+v", chain[0].Fields)
	}
	subtree, err := repo.TypeSubtree(ctx, "org-a", "inh-root")
	if err != nil {
		t.Fatal(err)
	}
	if got := typeIDs(subtree); !slices.Equal(got, []string{"inh-root", "inh-mid", "inh-leaf"}) {
		t.Errorf("subtree = %v", got)
	}
	// org-b sees the system root but not the types of org-a.
	if subtree, _ = repo.TypeSubtree(ctx, "org-b", "inh-root"); len(subtree) != 1 {
		t.Errorf("subtree for org-b = %v", typeIDs(subtree))
	}
	if _, err = repo.TypeChain(ctx, "org-b", "inh-leaf"); err == nil {
		t.Error("chain of a type of another organization: no error")
	}

	parent := func(id string) *string { return &id }
	var fe *api.FieldError
	var ce *api.ConflictError
	for name, tc := range map[string]struct {
		id, parent string
		conflict   bool
	}{
		"unknown parent":             {id: "inh-mid", parent: "no-such-type"},
		"parent of another org":      {id: "inh-mid", parent: "org-b-type"},
		"own parent":                 {id: "inh-mid", parent: "inh-mid", conflict: true},
		"descendant as parent":       {id: "inh-mid", parent: "inh-leaf", conflict: true},
		"system type below org type": {id: "inh-root", parent: "inh-leaf"},
	} {
		_, err = repo.Update(ctx, "org-a", tc.id, &UpdateTypeRequest{ParentTypeID: parent(tc.parent)})
		switch {
		case tc.conflict && (!errors.As(err, &ce) || ce.Field != "parent_type_id"):
			t.Errorf("%s: err = %v, want a conflict on parent_type_id", name, err)
		case !tc.conflict && (!errors.As(err, &fe) || fe.Field != "parent_type_id" || fe.Status != http.StatusUnprocessableEntity):
			t.Errorf("%s: err = %v, want 422 on parent_type_id", name, err)
		}
	}
	// Unknown and foreign parent read alike.
	_, errUnknown := repo.Update(ctx, "org-a", "inh-mid", &UpdateTypeRequest{ParentTypeID: parent("no-such-type")})
	_, errForeign := repo.Update(ctx, "org-a", "inh-mid", &UpdateTypeRequest{ParentTypeID: parent("org-b-type")})
	if errUnknown.Error() != errForeign.Error() {
		t.Errorf("unknown %q and foreign %q parent differ", errUnknown, errForeign)
	}

	// Depth: a chain of six types is allowed, the seventh level is not.
	prev := "inh-leaf" // level 3
	for level := 4; level <= MaxTypeChainDepth; level++ {
		typ := &Type{OrganizationID: "org-a", Key: fmt.Sprintf("lvl%d", level), Name: fmt.Sprintf("Level %d", level), ParentTypeID: prev}
		if err = repo.Create(ctx, typ); err != nil {
			t.Fatalf("level %d: %v", level, err)
		}
		prev = typ.ID
	}
	err = repo.Create(ctx, &Type{OrganizationID: "org-a", Key: "lvl7", Name: "Level 7", ParentTypeID: prev})
	if !errors.As(err, &fe) || fe.Field != "parent_type_id" {
		t.Errorf("seventh level: err = %v, want 422 on parent_type_id", err)
	}
	// Moving a subtree counts its height: org-a "solo" with one child
	// below level 5 would reach level 7.
	solo := &Type{OrganizationID: "org-a", Key: "solo", Name: "Solo"}
	if err = repo.Create(ctx, solo); err != nil {
		t.Fatal(err)
	}
	if err = repo.Create(ctx, &Type{OrganizationID: "org-a", Key: "solo_child", Name: "Solo child", ParentTypeID: solo.ID}); err != nil {
		t.Fatal(err)
	}
	chain, _ = repo.TypeChain(ctx, "org-a", prev)
	level5 := chain[4].ID
	if _, err = repo.Update(ctx, "org-a", solo.ID, &UpdateTypeRequest{ParentTypeID: &level5}); !errors.As(err, &fe) || fe.Field != "parent_type_id" {
		t.Errorf("moved subtree at level 7: err = %v, want 422 on parent_type_id", err)
	}
	level4 := chain[3].ID
	if _, err = repo.Update(ctx, "org-a", solo.ID, &UpdateTypeRequest{ParentTypeID: &level4}); err != nil {
		t.Errorf("moved subtree at level 6: %v", err)
	}
	// "" makes the type a root again.
	moved, err := repo.Update(ctx, "org-a", solo.ID, &UpdateTypeRequest{ParentTypeID: parent("")})
	if err != nil || moved.ParentTypeID != "" {
		t.Errorf("back to root: %+v %v", moved, err)
	}
}

// TestResolveFieldsOverChain: the resolver composes system → organization →
// chain root → type; of the ancestors only inheritable definitions apply,
// the type refines without removing (narrower enum, required OR-ed).
func TestResolveFieldsOverChain(t *testing.T) {
	repo := inheritRepo()
	defs, err := repo.ResolveFields(context.Background(), "org-a", "inh-leaf", "")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]fieldmeta.FieldDefinition{}
	for _, d := range defs {
		byName[d.Name] = d
	}
	if _, ok := byName["secret"]; ok {
		t.Error("non-inheritable definition of the root was inherited")
	}
	if _, ok := byName["mid_only"]; ok {
		t.Error("non-inheritable definition of the parent was inherited")
	}
	if !byName["vendor"].Required {
		t.Errorf("vendor = %+v, want required from the leaf", byName["vendor"])
	}
	if !slices.Equal(byName["tier"].EnumValues, []string{"a", "b"}) {
		t.Errorf("tier = %v, want the intersection [a b]", byName["tier"].EnumValues)
	}
	// The type itself brings its non-inheritable definitions.
	defs, err = repo.ResolveFields(context.Background(), "org-a", "inh-mid", "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(defs, func(d fieldmeta.FieldDefinition) bool { return d.Name == "mid_only" }) {
		t.Errorf("own non-inheritable definition missing: %+v", defs)
	}
}

// TestResolveFieldsTypeChainLayers is the TST-01 [P2] case "inheritance"
// (WP-326, MET-06) as composition layers: ResolveFields over the chain
// root → mid → leaf of a fake lister (layeredLister) without system and
// organization layers. The leaf inherits the inheritable definitions of
// its ancestors, a definition with inheritable = false stays with its
// type, the subtype refines (narrower bounds, intersection of the allowed
// values, a later default) and removes nothing (a required inherited from
// the root stays required, a pattern of the mid stays), and a conflict
// over the chain (data type, empty range, no allowed value left) is a
// *CompositionError with key and code, also when the conflicting
// definitions lie two levels apart.
func TestResolveFieldsTypeChainLayers(t *testing.T) {
	ctx := context.Background()
	chain := func(leafFields ...Field) layeredLister {
		return layeredLister{globals: []Field{}, chain: []Type{
			{ID: "root", Fields: []Field{
				{Name: "vendor", DataType: fieldmeta.TypeText, Required: true, Inheritable: true},
				{Name: "ports", DataType: fieldmeta.TypeInteger, Inheritable: true,
					Validation: &fieldmeta.ValidationRules{Min: f64(1), Max: f64(48)}},
				{Name: "tier", DataType: fieldmeta.TypeEnum, EnumValues: []string{"a", "b", "c"}, DefaultValue: "a", Inheritable: true},
				{Name: "root_secret", DataType: fieldmeta.TypeText, Inheritable: false},
			}},
			{ID: "mid", ParentTypeID: "root", Fields: []Field{
				{Name: "ports", DataType: fieldmeta.TypeInteger, Inheritable: true,
					Validation: &fieldmeta.ValidationRules{Min: f64(4)}},
				{Name: "code", DataType: fieldmeta.TypeText, Inheritable: true,
					Validation: &fieldmeta.ValidationRules{Pattern: "^[A-Z]"}},
				{Name: "mid_only", DataType: fieldmeta.TypeText, Inheritable: false},
			}},
			{ID: "leaf", ParentTypeID: "mid", Fields: leafFields},
		}}
	}

	defs, err := ResolveFields(ctx, chain(
		Field{Name: "vendor", DataType: fieldmeta.TypeText, Required: false},
		Field{Name: "ports", DataType: fieldmeta.TypeInteger, Validation: &fieldmeta.ValidationRules{Max: f64(24)}},
		Field{Name: "tier", DataType: fieldmeta.TypeEnum, EnumValues: []string{"b", "c", "d"}, DefaultValue: "c"},
		Field{Name: "code", DataType: fieldmeta.TypeText},
		Field{Name: "leaf_only", DataType: fieldmeta.TypeText, Inheritable: false},
	), "org", "leaf", "")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]fieldmeta.FieldDefinition{}
	for _, d := range defs {
		byName[d.Name] = d
	}
	for _, name := range []string{"root_secret", "mid_only"} {
		if _, ok := byName[name]; ok {
			t.Errorf("%s: a definition with inheritable = false of an ancestor reached the leaf", name)
		}
	}
	if _, ok := byName["leaf_only"]; !ok {
		t.Error("the leaf's own definition with inheritable = false is missing")
	}
	if v := byName["vendor"]; !v.Required {
		t.Errorf("vendor = %+v: the required inherited from the root was dropped", v)
	}
	if p := byName["ports"].Validation; p == nil || *p.Min != 4 || *p.Max != 24 {
		t.Errorf("ports = %+v, want 4..24 (min of mid, max of leaf)", p)
	}
	if tier := byName["tier"]; !slices.Equal(tier.EnumValues, []string{"b", "c"}) || tier.DefaultValue != "c" {
		t.Errorf("tier = %+v, want [b c] with the leaf's default", tier)
	}
	if code := byName["code"]; code.Validation == nil || code.Validation.Pattern != "^[A-Z]" {
		t.Errorf("code = %+v: the pattern inherited from mid was dropped", code)
	}
	// The mid alone inherits from the root only.
	if defs, err = ResolveFields(ctx, chain(), "org", "mid", ""); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(defs, func(d fieldmeta.FieldDefinition) bool { return d.Name == "mid_only" }) ||
		slices.ContainsFunc(defs, func(d fieldmeta.FieldDefinition) bool { return d.Name == "root_secret" }) {
		t.Errorf("mid = %+v, want its own mid_only without root_secret", defs)
	}

	for name, tc := range map[string]struct {
		leaf Field
		key  string
		code string
	}{
		"data type differs from the root":     {Field{Name: "vendor", DataType: fieldmeta.TypeInteger}, "vendor", CodeDatatypeConflict},
		"range empty against mid":             {Field{Name: "ports", DataType: fieldmeta.TypeInteger, Validation: &fieldmeta.ValidationRules{Max: f64(3)}}, "ports", CodeRangeEmpty},
		"no allowed value left from the root": {Field{Name: "tier", DataType: fieldmeta.TypeEnum, EnumValues: []string{"x"}}, "tier", CodeEnumEmpty},
	} {
		_, err = ResolveFields(ctx, chain(tc.leaf), "org", "leaf", "")
		var conflict *CompositionError
		if !errors.As(err, &conflict) || conflict.CompositionKey() != tc.key || conflict.CompositionCode() != tc.code {
			t.Errorf("%s: err = %v, want a composition error of %s with %s", name, err, tc.key, tc.code)
		}
	}
	// A definition with inheritable = false of an ancestor does not
	// conflict with the leaf: it is no layer of the leaf.
	if _, err = ResolveFields(ctx, chain(Field{Name: "root_secret", DataType: fieldmeta.TypeInteger}), "org", "leaf", ""); err != nil {
		t.Errorf("leaf over a non-inheritable definition of the root: %v", err)
	}
}

// TestTypeHandlerErrorContract covers the error contract of Handler.Create
// and Handler.Update (WP-248): an unknown type is 404, a cycle and a taken
// key 409, parent errors 422; every parent error is RFC 7807 with field
// parent_type_id.
func TestTypeHandlerErrorContract(t *testing.T) {
	repo := inheritRepo()
	mux := chi.NewRouter()
	NewHandler(repo).RegisterRoutes(mux)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-a", UserID: "user-a"})
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	field := func(w *httptest.ResponseRecorder) string {
		var p api.FieldProblem
		_ = json.Unmarshal(w.Body.Bytes(), &p)
		return p.Field
	}

	w := do(http.MethodPost, "/api/v1/ci-types", `{"name":"Sub","key":"sub","parent_type_id":"inh-mid"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create subtype: %d %s", w.Code, w.Body.String())
	}
	var created Type
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	if created.ParentTypeID != "inh-mid" {
		t.Errorf("created parent = %q", created.ParentTypeID)
	}
	if w = do(http.MethodPost, "/api/v1/ci-types", `{"name":"Sub","key":"sub"}`); w.Code != http.StatusConflict {
		t.Errorf("taken key: %d %s, want 409", w.Code, w.Body.String())
	}
	for name, parent := range map[string]string{"unknown": "no-such-type", "foreign": "org-b-type"} {
		w = do(http.MethodPost, "/api/v1/ci-types", `{"name":"X`+name+`","parent_type_id":"`+parent+`"}`)
		if w.Code != http.StatusUnprocessableEntity || field(w) != "parent_type_id" {
			t.Errorf("create with %s parent: %d %s, want 422 on parent_type_id", name, w.Code, w.Body.String())
		}
	}

	if w = do(http.MethodPatch, "/api/v1/ci-types/no-such-type", `{"name":"x"}`); w.Code != http.StatusNotFound {
		t.Errorf("unknown type: %d %s, want 404", w.Code, w.Body.String())
	}
	w = do(http.MethodPatch, "/api/v1/ci-types/inh-mid", `{"parent_type_id":"`+created.ID+`"}`)
	if w.Code != http.StatusConflict || field(w) != "parent_type_id" {
		t.Errorf("cycle: %d %s, want 409 on parent_type_id", w.Code, w.Body.String())
	}
	w = do(http.MethodPatch, "/api/v1/ci-types/inh-mid", `{"parent_type_id":"org-b-type"}`)
	if w.Code != http.StatusUnprocessableEntity || field(w) != "parent_type_id" {
		t.Errorf("foreign parent: %d %s, want 422 on parent_type_id", w.Code, w.Body.String())
	}
	w = do(http.MethodPatch, "/api/v1/ci-types/"+created.ID, `{"parent_type_id":""}`)
	if w.Code != http.StatusOK {
		t.Fatalf("make root: %d %s", w.Code, w.Body.String())
	}
	var updated Type
	_ = json.Unmarshal(w.Body.Bytes(), &updated)
	if updated.ParentTypeID != "" {
		t.Errorf("root still has parent %q", updated.ParentTypeID)
	}
}
