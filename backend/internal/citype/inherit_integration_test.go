package citype_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// inheritAPI drives the CI type API of WP-248 against PostgreSQL as an
// organization's metamodel administrator.
type inheritAPI struct {
	t    *testing.T
	f    *scopetest.Fixture
	mux  *chi.Mux
	repo *citype.PGRepository
	user string
}

func newInheritAPI(t *testing.T, f *scopetest.Fixture) *inheritAPI {
	repo := citype.NewPGRepository(f.App)
	mux := chi.NewRouter()
	citype.NewHandler(repo).RegisterRoutes(mux)
	return &inheritAPI{t: t, f: f, mux: mux, repo: repo, user: f.AppUser(t, f.OrgA, "wp248-admin")}
}

func (a *inheritAPI) ctx(org string) context.Context {
	scope := database.OrgWideScope(org, a.user)
	ctx := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope),
		tenant.TenantInfo{OrganizationID: org, UserID: a.user})
	return audit.WithRequestInfo(ctx, audit.RequestInfo{RequestID: "wp248", CorrelationID: "wp248", Source: audit.SourceUI})
}

func (a *inheritAPI) call(org, method, path, body string) *httptest.ResponseRecorder {
	a.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(a.ctx(org))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.mux.ServeHTTP(w, req)
	return w
}

// create posts a type and returns it; parent may be empty.
func (a *inheritAPI) create(key, parent string) citype.Type {
	a.t.Helper()
	body := `{"key":"` + key + `","name":"` + key + `"`
	if parent != "" {
		body += `,"parent_type_id":"` + parent + `"`
	}
	w := a.call(a.f.OrgA, http.MethodPost, "/api/v1/ci-types", body+"}")
	if w.Code != http.StatusCreated {
		a.t.Fatalf("create %s: %d %s", key, w.Code, w.Body.String())
	}
	var typ citype.Type
	if err := json.Unmarshal(w.Body.Bytes(), &typ); err != nil {
		a.t.Fatal(err)
	}
	return typ
}

// define writes an attribute definition (ci_type_id empty: org-global).
func (a *inheritAPI) define(typeID, body string) *httptest.ResponseRecorder {
	a.t.Helper()
	if typeID != "" {
		body = `{"ci_type_id":"` + typeID + `",` + strings.TrimPrefix(body, "{")
	}
	return a.call(a.f.OrgA, http.MethodPost, "/api/v1/attribute-definitions", body)
}

func (a *inheritAPI) mustDefine(typeID, body string) string {
	a.t.Helper()
	w := a.define(typeID, body)
	if w.Code != http.StatusCreated {
		a.t.Fatalf("define %s on %s: %d %s", body, typeID, w.Code, w.Body.String())
	}
	var d citype.AttributeDefinition
	_ = json.Unmarshal(w.Body.Bytes(), &d)
	return d.ID
}

func (a *inheritAPI) get(id string) citype.Type {
	a.t.Helper()
	w := a.call(a.f.OrgA, http.MethodGet, "/api/v1/ci-types/"+id, "")
	if w.Code != http.StatusOK {
		a.t.Fatalf("get %s: %d %s", id, w.Code, w.Body.String())
	}
	var typ citype.Type
	_ = json.Unmarshal(w.Body.Bytes(), &typ)
	return typ
}

func (a *inheritAPI) reparent(id, parent string) *httptest.ResponseRecorder {
	a.t.Helper()
	return a.call(a.f.OrgA, http.MethodPatch, "/api/v1/ci-types/"+id, `{"parent_type_id":"`+parent+`"}`)
}

func problemOf(w *httptest.ResponseRecorder) api.FieldProblem {
	var p api.FieldProblem
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	return p
}

func properties(typ *citype.Type) map[string]any {
	props, _ := typ.AttributeSchema["properties"].(map[string]any)
	return props
}

func systemTypeID(t *testing.T, f *scopetest.Fixture, key string) string {
	t.Helper()
	var id string
	if err := f.Admin.QueryRow(context.Background(),
		`SELECT id::text FROM ci_type WHERE key = $1 AND organization_id IS NULL`, key).Scan(&id); err != nil {
		t.Fatalf("system type %s: %v", key, err)
	}
	return id
}

// TestTypeInheritanceAgainstPostgres covers WP-248 (MET-06, MET-01)
// against PostgreSQL: a chain of three levels with a system type as root.
// The subtype's materialized schema holds the inherited attributes,
// inheritable=false is not inherited, a subtype refines without removing
// (required stays, enum values are intersected). A change of an attribute
// of the parent raises schema_version of the descendants, a parent change
// regenerates the moved subtree. TypeChain, TypeSubtree and ResolveFields
// follow the chain; ChangeType (CI-11) reports inherited required fields.
// Export and the audit entries ci_type.created/ci_type.updated carry
// parent_type_id.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestTypeInheritanceAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "8a")
	bg := context.Background()
	a := newInheritAPI(t, f)
	root := systemTypeID(t, f, "generic_device")

	mid := a.create("wp248-mid", root)
	if mid.ParentTypeID != root {
		t.Fatalf("mid parent = %q, want %s", mid.ParentTypeID, root)
	}
	a.mustDefine(mid.ID, `{"key":"wp248_tier","data_type":"enum","enum_values":["a","b","c"]}`)
	a.mustDefine(mid.ID, `{"key":"wp248_req","data_type":"text","required":true}`)
	a.mustDefine(mid.ID, `{"key":"wp248_private","data_type":"text","inheritable":false}`)
	leaf := a.create("wp248-leaf", mid.ID)
	a.mustDefine(leaf.ID, `{"key":"wp248_tier","data_type":"enum","enum_values":["b","c","d"]}`)
	a.mustDefine(leaf.ID, `{"key":"wp248_req","data_type":"text","required":false}`)

	got := a.get(leaf.ID)
	props := properties(&got)
	if props["wp248_req"] == nil || props["wp248_tier"] == nil {
		t.Fatalf("leaf schema lacks inherited attributes: %v", props)
	}
	if props["wp248_private"] != nil {
		t.Error("inheritable=false was inherited")
	}
	tier, _ := props["wp248_tier"].(map[string]any)
	if enum, _ := json.Marshal(tier["enum"]); string(enum) != `["b","c"]` {
		t.Errorf("leaf tier enum = %s, want the intersection [b c]", enum)
	}
	if !slices.Contains(got.RequiredFields, "wp248_req") {
		t.Errorf("leaf required = %v, want wp248_req (required cannot be removed)", got.RequiredFields)
	}
	if midSchema := a.get(mid.ID); properties(&midSchema)["wp248_private"] == nil {
		t.Error("the type lost its own non-inheritable attribute")
	}

	ctxA := a.ctx(f.OrgA)
	chain, err := a.repo.TypeChain(ctxA, f.OrgA, leaf.ID)
	if err != nil {
		t.Fatal(err)
	}
	var chainIDs []string
	for i := range chain {
		chainIDs = append(chainIDs, chain[i].ID)
	}
	if !slices.Equal(chainIDs, []string{root, mid.ID, leaf.ID}) {
		t.Errorf("chain = %v", chainIDs)
	}
	if !slices.ContainsFunc(chain[1].Fields, func(fl citype.Field) bool { return fl.Name == "wp248_private" && !fl.Inheritable }) {
		t.Errorf("chain fields of mid = %+v", chain[1].Fields)
	}
	subtree, err := a.repo.TypeSubtree(ctxA, f.OrgA, mid.ID)
	if err != nil || len(subtree) != 2 || subtree[0].ID != mid.ID || subtree[1].ID != leaf.ID {
		t.Errorf("subtree = %+v %v", subtree, err)
	}
	defs, err := a.repo.ResolveFields(ctxA, f.OrgA, leaf.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	var resolved []string
	for _, d := range defs {
		resolved = append(resolved, d.Name)
		if d.Name == "wp248_req" && !d.Required {
			t.Error("resolved wp248_req is not required")
		}
	}
	if !slices.Contains(resolved, "wp248_tier") || slices.Contains(resolved, "wp248_private") {
		t.Errorf("resolved = %v", resolved)
	}

	// CI-11: the type change names the inherited required attribute.
	ciRepo := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	ciID := f.CI(t, f.OrgA, f.Client1, "wp248-ci")
	res, err := ciRepo.ChangeType(ctxA, f.OrgA, ciID, leaf.ID, ci.TypeChangeManual)
	if err != nil {
		t.Fatalf("change type: %v", err)
	}
	if !slices.ContainsFunc(res.Findings, func(fd ci.TypeFinding) bool { return fd.Field == "wp248_req" }) {
		t.Errorf("findings = %+v, want the inherited wp248_req", res.Findings)
	}

	// A change of the parent's attribute raises the descendants' version.
	before := a.get(leaf.ID).SchemaVersion
	if w := a.call(f.OrgA, http.MethodPut, "/api/v1/ci-types/"+mid.ID+"/fields",
		`{"name":"wp248_note","data_type":"text"}`); w.Code != http.StatusOK {
		t.Fatalf("parent field: %d %s", w.Code, w.Body.String())
	}
	after := a.get(leaf.ID)
	if after.SchemaVersion <= before || properties(&after)["wp248_note"] == nil {
		t.Errorf("leaf after parent change: version %d (before %d), props %v", after.SchemaVersion, before, properties(&after))
	}

	// A parent change regenerates the moved subtree.
	other := a.create("wp248-other", "")
	a.mustDefine(other.ID, `{"key":"wp248_other","data_type":"text"}`)
	child := a.create("wp248-leaf-child", leaf.ID)
	childBefore := a.get(child.ID)
	if properties(&childBefore)["wp248_tier"] == nil {
		t.Fatalf("grandchild schema = %v", properties(&childBefore))
	}
	w := a.reparent(leaf.ID, other.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("reparent: %d %s", w.Code, w.Body.String())
	}
	var moved citype.Type
	_ = json.Unmarshal(w.Body.Bytes(), &moved)
	if moved.ParentTypeID != other.ID || properties(&moved)["wp248_other"] == nil || properties(&moved)["wp248_note"] != nil {
		t.Errorf("moved leaf: parent %q, props %v", moved.ParentTypeID, properties(&moved))
	}
	childAfter := a.get(child.ID)
	if childAfter.SchemaVersion <= childBefore.SchemaVersion || properties(&childAfter)["wp248_other"] == nil {
		t.Errorf("grandchild after the move: version %d (before %d), props %v",
			childAfter.SchemaVersion, childBefore.SchemaVersion, properties(&childAfter))
	}
	// Back to a root: the schema holds the own attributes only.
	if w = a.reparent(child.ID, ""); w.Code != http.StatusOK {
		t.Fatalf("make root: %d %s", w.Code, w.Body.String())
	}
	if rooted := a.get(child.ID); rooted.ParentTypeID != "" || properties(&rooted)["wp248_other"] != nil {
		t.Errorf("rooted child: parent %q, props %v", rooted.ParentTypeID, properties(&rooted))
	}

	// Export and audit carry parent_type_id.
	w = a.call(f.OrgA, http.MethodGet, "/api/v1/ci-types/export", "")
	var export citype.MetamodelExport
	if err = json.Unmarshal(w.Body.Bytes(), &export); err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(export.CITypes, func(c citype.Type) bool { return c.ID == leaf.ID }); i < 0 ||
		export.CITypes[i].ParentTypeID != other.ID {
		t.Errorf("export lacks the parent of the leaf")
	}
	for action, want := range map[string]string{"ci_type.created": mid.ID, "ci_type.updated": leaf.ID} {
		var parent string
		if err = f.Admin.QueryRow(bg, `SELECT COALESCE(changes -> 'after' ->> 'parent_type_id', '') FROM audit_log
			WHERE organization_id = $1 AND action = $2 AND resource_id = $3 ORDER BY seq DESC LIMIT 1`,
			f.OrgA, action, want).Scan(&parent); err != nil {
			t.Fatalf("audit %s: %v", action, err)
		}
		if wantParent := map[string]string{mid.ID: root, leaf.ID: other.ID}[want]; parent != wantParent {
			t.Errorf("audit %s parent_type_id = %q, want %q", action, parent, wantParent)
		}
	}

	// E-117: every system type carries category and icon; the category
	// filter finds the physical system types.
	var missing []string
	rows, err := f.Admin.Query(bg, `SELECT key FROM ci_type WHERE organization_id IS NULL AND key = ANY($1)
		AND (category IS NULL OR icon IS NULL)`, systemTypeKeys)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var key string
		_ = rows.Scan(&key)
		missing = append(missing, key)
	}
	rows.Close()
	var count int
	_ = f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_type WHERE organization_id IS NULL AND key = ANY($1)`, systemTypeKeys).Scan(&count)
	if len(missing) > 0 || count != len(systemTypeKeys) {
		t.Errorf("system types without category or icon: %v (%d of %d present)", missing, count, len(systemTypeKeys))
	}
	w = a.call(f.OrgA, http.MethodGet, "/api/v1/ci-types?category=network&limit=200", "")
	var list api.ListResponse[citype.Type]
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	var network []string
	for i := range list.Data {
		if list.Data[i].IsSystem {
			network = append(network, list.Data[i].Key)
		}
	}
	for _, key := range []string{"switch", "router", "firewall", "access_point"} {
		if !slices.Contains(network, key) {
			t.Errorf("category=network lacks %s: %v", key, network)
		}
	}
}

// systemTypeKeys are the 23 system types of 000021 and 000104 (E-117).
var systemTypeKeys = []string{
	"switch", "router", "firewall", "access_point", "server", "hypervisor", "vm", "client", "pdu", "ups",
	"nas", "storage_array", "printer", "ip_phone", "camera", "generic_device", "patch_panel",
	"business_service", "application", "database", "cluster", "cloud_resource", "software",
}

// TestTypeParentRulesAgainstPostgres covers the write rules of WP-248
// against PostgreSQL: a cycle is 409, the seventh level 422 (also when a
// subtree is moved), an unknown and a foreign parent are 422 alike, of two
// concurrent opposite moves exactly one fails, a parent change whose
// subtree cannot be composed is 422 on parent_type_id, an empty enum
// intersection over org-global attribute, parent and subtype is 422 on
// key. Every error is RFC 7807 with its field.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestTypeParentRulesAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "8a")
	a := newInheritAPI(t, f)
	root := systemTypeID(t, f, "generic_device")

	// Levels 2 … 6 below the system root.
	levels := []string{root}
	for _, key := range []string{"wp248-l2", "wp248-l3", "wp248-l4", "wp248-l5", "wp248-l6"} {
		levels = append(levels, a.create(key, levels[len(levels)-1]).ID)
	}
	w := a.call(f.OrgA, http.MethodPost, "/api/v1/ci-types", `{"key":"wp248-l7","name":"wp248-l7","parent_type_id":"`+levels[5]+`"}`)
	if w.Code != http.StatusUnprocessableEntity || problemOf(w).Field != "parent_type_id" {
		t.Errorf("seventh level: %d %s", w.Code, w.Body.String())
	}
	// A subtree of height 2 below level 5 would reach level 7.
	top := a.create("wp248-top", "")
	a.create("wp248-top-child", top.ID)
	if w = a.reparent(top.ID, levels[4]); w.Code != http.StatusUnprocessableEntity || problemOf(w).Field != "parent_type_id" {
		t.Errorf("moved subtree at level 7: %d %s", w.Code, w.Body.String())
	}
	if w = a.reparent(top.ID, levels[3]); w.Code != http.StatusOK {
		t.Errorf("moved subtree at level 6: %d %s", w.Code, w.Body.String())
	}

	// Cycle: the type itself and a descendant.
	for name, parent := range map[string]string{"own": levels[2], "descendant": levels[4]} {
		if w = a.reparent(levels[2], parent); w.Code != http.StatusConflict || problemOf(w).Field != "parent_type_id" {
			t.Errorf("cycle over %s: %d %s", name, w.Code, w.Body.String())
		}
	}

	// Unknown and foreign parent are indistinguishable.
	wb := a.call(f.OrgB, http.MethodPost, "/api/v1/ci-types", `{"key":"wp248-foreign","name":"wp248-foreign"}`)
	if wb.Code != http.StatusCreated {
		t.Fatalf("org B type: %d %s", wb.Code, wb.Body.String())
	}
	var foreignType citype.Type
	_ = json.Unmarshal(wb.Body.Bytes(), &foreignType)
	foreign := foreignType.ID
	unknown := f.ID()
	wu, wf := a.reparent(levels[1], unknown), a.reparent(levels[1], foreign)
	pu, pf := problemOf(wu), problemOf(wf)
	pu.TraceID, pf.TraceID = "", ""
	if wu.Code != http.StatusUnprocessableEntity || wf.Code != http.StatusUnprocessableEntity ||
		pu != pf || pu.Field != "parent_type_id" {
		t.Errorf("unknown %d %s / foreign %d %s", wu.Code, wu.Body.String(), wf.Code, wf.Body.String())
	}
	for name, parent := range map[string]string{"unknown": unknown, "foreign": foreign, "no uuid": "nope"} {
		w = a.call(f.OrgA, http.MethodPost, "/api/v1/ci-types", `{"key":"wp248-x","name":"wp248-x","parent_type_id":"`+parent+`"}`)
		if w.Code != http.StatusUnprocessableEntity || problemOf(w).Field != "parent_type_id" {
			t.Errorf("create with %s parent: %d %s", name, w.Code, w.Body.String())
		}
	}
	if w = a.call(f.OrgA, http.MethodPatch, "/api/v1/ci-types/"+unknown, `{"name":"x"}`); w.Code != http.StatusNotFound {
		t.Errorf("unknown type: %d %s, want 404", w.Code, w.Body.String())
	}
	if w = a.call(f.OrgA, http.MethodPatch, "/api/v1/ci-types/"+root, `{"name":"x"}`); w.Code != http.StatusNotFound {
		t.Errorf("system type: %d %s, want 404", w.Code, w.Body.String())
	}
	if w = a.call(f.OrgA, http.MethodPost, "/api/v1/ci-types", `{"key":"wp248-top","name":"wp248-dup"}`); w.Code != http.StatusConflict {
		t.Errorf("taken key: %d %s, want 409", w.Code, w.Body.String())
	}

	// Concurrent opposite moves: exactly one succeeds.
	for i := range 3 {
		x := a.create("wp248-race-x"+string(rune('a'+i)), "")
		y := a.create("wp248-race-y"+string(rune('a'+i)), "")
		var wg sync.WaitGroup
		errs := make([]error, 2)
		start := make(chan struct{})
		for j, pair := range [][2]string{{x.ID, y.ID}, {y.ID, x.ID}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				parent := pair[1]
				_, errs[j] = a.repo.Update(a.ctx(f.OrgA), f.OrgA, pair[0], citype.UpdateTypeRequest{ParentTypeID: &parent})
			}()
		}
		close(start)
		wg.Wait()
		failed := 0
		for _, err := range errs {
			var ce *api.ConflictError
			switch {
			case err == nil:
			case errors.As(err, &ce):
				failed++
			default:
				t.Errorf("race %d: unexpected error %v", i, err)
			}
		}
		if failed != 1 {
			t.Errorf("race %d: %d of the opposite moves failed, want exactly 1 (%v)", i, failed, errs)
		}
	}

	// Composition: a parent of another data type (database) and a parent
	// whose enum leaves nothing with the org-global attribute (Go layer).
	sub := a.create("wp248-sub", "")
	a.mustDefine(sub.ID, `{"key":"wp248_kind","data_type":"text"}`)
	a.mustDefine(sub.ID, `{"key":"wp248_shade","data_type":"enum","enum_values":["red","blue"]}`)
	a.mustDefine("", `{"key":"wp248_shade","data_type":"enum","enum_values":["red","green"]}`)
	intParent := a.create("wp248-int-parent", "")
	a.mustDefine(intParent.ID, `{"key":"wp248_kind","data_type":"integer"}`)
	w = a.reparent(sub.ID, intParent.ID)
	if w.Code != http.StatusUnprocessableEntity || problemOf(w).Field != "parent_type_id" || !strings.Contains(w.Body.String(), "wp248_kind") {
		t.Errorf("data type conflict on move: %d %s", w.Code, w.Body.String())
	}
	greenParent := a.create("wp248-green-parent", "")
	a.mustDefine(greenParent.ID, `{"key":"wp248_shade","data_type":"enum","enum_values":["blue","green"]}`)
	w = a.reparent(sub.ID, greenParent.ID)
	if w.Code != http.StatusUnprocessableEntity || problemOf(w).Field != "parent_type_id" || !strings.Contains(w.Body.String(), "wp248_shade") {
		t.Errorf("empty enum intersection on move: %d %s", w.Code, w.Body.String())
	}
	if got := a.get(sub.ID); got.ParentTypeID != "" {
		t.Errorf("refused move kept parent %q", got.ParentTypeID)
	}

	// Writing a definition: org-global [red green], parent [green blue],
	// subtype [blue] leaves nothing → 422 on key.
	colorParent := a.create("wp248-color-parent", "")
	a.mustDefine("", `{"key":"wp248_color","data_type":"enum","enum_values":["red","green"]}`)
	a.mustDefine(colorParent.ID, `{"key":"wp248_color","data_type":"enum","enum_values":["green","blue"]}`)
	colorChild := a.create("wp248-color-child", colorParent.ID)
	w = a.define(colorChild.ID, `{"key":"wp248_color","data_type":"enum","enum_values":["blue"]}`)
	if w.Code != http.StatusUnprocessableEntity || problemOf(w).Field != "key" || !strings.Contains(w.Body.String(), "wp248_color") {
		t.Errorf("empty enum intersection over the chain: %d %s", w.Code, w.Body.String())
	}
	if w = a.define(colorChild.ID, `{"key":"wp248_color","data_type":"enum","enum_values":["green"]}`); w.Code != http.StatusCreated {
		t.Errorf("refining enum: %d %s", w.Code, w.Body.String())
	}
}
