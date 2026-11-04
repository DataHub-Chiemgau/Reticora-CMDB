package citype_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
)

// The helpers drive the set API of setAPI (attribute_set_integration_test.go)
// for the composition of WP-709 (MET-16).

// childType posts a type below parent.
func (a *setAPI) childType(org, key, parent string) citype.Type {
	a.t.Helper()
	var typ citype.Type
	a.must(http.StatusCreated, org, http.MethodPost, "/api/v1/ci-types",
		`{"key":"`+key+`","name":"`+key+`","parent_type_id":"`+parent+`"}`, &typ)
	return typ
}

// mustSetAttribute creates a set attribute and returns its id.
func (a *setAPI) mustSetAttribute(org, setID, body string) string {
	a.t.Helper()
	w := a.setAttribute(org, setID, body)
	if w.Code != http.StatusCreated {
		a.t.Fatalf("set attribute %s: %d %s", body, w.Code, w.Body.String())
	}
	var d citype.AttributeDefinition
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		a.t.Fatal(err)
	}
	return d.ID
}

// assignment is the body of PUT /ci-types/{id}/attribute-sets: the sets
// at the positions 10, 20, … in the given order.
func assignment(setIDs ...string) string {
	items := make([]string, 0, len(setIDs))
	for i, id := range setIDs {
		items = append(items, `{"attribute_set_id":"`+id+`","position":`+strconv.Itoa((i+1)*10)+`}`)
	}
	return `{"attribute_sets":[` + strings.Join(items, ",") + `]}`
}

func (a *setAPI) putSets(org, typeID string, setIDs ...string) *citype.TypeAttributeSets {
	a.t.Helper()
	var doc citype.TypeAttributeSets
	a.must(http.StatusOK, org, http.MethodPut, "/api/v1/ci-types/"+typeID+"/attribute-sets", assignment(setIDs...), &doc)
	return &doc
}

func (a *setAPI) typeOf(org, id string) citype.Type {
	a.t.Helper()
	var typ citype.Type
	a.must(http.StatusOK, org, http.MethodGet, "/api/v1/ci-types/"+id, "", &typ)
	return typ
}

// prop is the materialized property of key (nil: absent).
func prop(typ *citype.Type, key string) map[string]any {
	p, _ := properties(typ)[key].(map[string]any)
	return p
}

// resolved returns the effective definitions of a type (and CI) by name.
func (a *setAPI) resolved(org, typeID, ciID string) map[string]fieldmeta.FieldDefinition {
	a.t.Helper()
	defs, err := a.repo.ResolveFields(a.ctx(org), org, typeID, ciID)
	if err != nil {
		a.t.Fatalf("resolve fields of %s: %v", typeID, err)
	}
	out := map[string]fieldmeta.FieldDefinition{}
	for _, d := range defs {
		out[d.Name] = d
	}
	return out
}

func asJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

// TestAttributeSetSchemaVersionAgainstPostgres covers MET-16 (WP-709):
// schema_version of a type rises exactly once when a set with attributes
// is assigned (one PUT of several sets is one change), when two sets with
// a common key and different defaults swap places, when a set attribute
// changes and when a set with attributes is deactivated; a new order that
// leaves the schema as it is keeps the version. A CI references the new
// version after the next write of its attributes (trg_ci_schema_version).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAttributeSetSchemaVersionAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "c9")
	a := newSetAPI(t, f)
	bg := context.Background()
	org := f.OrgA
	typ := a.createType(org, "wp709-version")
	rack, two, three := a.createSet(org, "wp709-v-rack"), a.createSet(org, "wp709-v-two"), a.createSet(org, "wp709-v-three")
	rackDef := a.mustSetAttribute(org, rack.ID, `{"key":"wp709_rack","data_type":"text"}`)
	a.mustSetAttribute(org, two.ID, `{"key":"wp709_label","data_type":"text","default":"two"}`)
	a.mustSetAttribute(org, three.ID, `{"key":"wp709_label","data_type":"text","default":"three"}`)

	version := a.typeOf(org, typ.ID).SchemaVersion
	step := func(name string, rises bool) citype.Type {
		t.Helper()
		got := a.typeOf(org, typ.ID)
		want := version
		if rises {
			want++
		}
		if got.SchemaVersion != want {
			t.Errorf("%s: schema_version %d, want %d", name, got.SchemaVersion, want)
		}
		version = got.SchemaVersion
		return got
	}

	// A CI of the type records the current version.
	ciID := f.CI(t, org, f.Client1, "wp709-version-ci")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET ci_type_id = $2 WHERE id = $1`, ciID, typ.ID); err != nil {
		t.Fatal(err)
	}

	a.putSets(org, typ.ID, rack.ID, two.ID, three.ID)
	got := step("assign three sets in one PUT", true)
	if prop(&got, "wp709_rack") == nil || prop(&got, "wp709_label")["default"] != "three" {
		t.Errorf("schema after the assignment: %v", properties(&got))
	}
	a.putSets(org, typ.ID, rack.ID, three.ID, two.ID)
	if got = step("swap two sets with a common key", true); prop(&got, "wp709_label")["default"] != "two" {
		t.Errorf("default after the swap = %v, want two (the later set wins)", prop(&got, "wp709_label")["default"])
	}
	a.putSets(org, typ.ID, three.ID, rack.ID, two.ID)
	step("a new order without a schema change", false)

	var ciVersion int
	if err := f.Admin.QueryRow(bg, `SELECT schema_version FROM ci WHERE id = $1`, ciID).Scan(&ciVersion); err != nil || ciVersion >= version {
		t.Fatalf("ci schema_version = %d (%v), want the version before the sets (type now %d)", ciVersion, err, version)
	}
	a.must(http.StatusOK, org, http.MethodPatch, "/api/v1/attribute-definitions/"+rackDef, `{"required":true}`, nil)
	if got = step("change a set attribute", true); !slices.Contains(got.RequiredFields, "wp709_rack") {
		t.Errorf("required after the change = %v", got.RequiredFields)
	}
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET attributes = attributes || '{"wp709_rack": "R1"}' WHERE id = $1`, ciID); err != nil {
		t.Fatal(err)
	}
	if err := f.Admin.QueryRow(bg, `SELECT schema_version FROM ci WHERE id = $1`, ciID).Scan(&ciVersion); err != nil || ciVersion != version {
		t.Errorf("ci schema_version after the write = %d (%v), want %d", ciVersion, err, version)
	}

	a.must(http.StatusOK, org, http.MethodPatch, "/api/v1/attribute-sets/"+rack.ID, `{"is_active":false}`, nil)
	if got = step("deactivate a set with attributes", true); prop(&got, "wp709_rack") != nil {
		t.Errorf("the inactive set is materialized: %v", properties(&got))
	}
	if _, ok := a.resolved(org, typ.ID, "")["wp709_rack"]; ok {
		t.Error("ResolveFields composes the inactive set")
	}
	a.must(http.StatusOK, org, http.MethodPatch, "/api/v1/attribute-sets/"+rack.ID, `{"description":"still inactive"}`, nil)
	step("a change of the set beside is_active", false)
}

// TestAttributeSetCompositionRulesAgainstPostgres covers MET-16 (WP-709):
// a set tightens the type (required OR, intersection of the allowed
// values) and the instance tightens the set; the sets follow the type
// chain, so the set of the parent type comes before that of the child and
// the default of the child's set wins; a set attribute with inheritable
// false of the parent's set is missing at the child; an inactive set is
// missing in attribute_schema and in ResolveFields. The materialized
// schema and ResolveFields agree.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAttributeSetCompositionRulesAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "c9")
	a := newSetAPI(t, f)
	bg := context.Background()
	org := f.OrgA
	parent := a.createType(org, "wp709-rules-parent")
	child := a.childType(org, "wp709-rules-child", parent.ID)
	a.must(http.StatusCreated, org, http.MethodPost, "/api/v1/attribute-definitions",
		`{"ci_type_id":"`+parent.ID+`","key":"wp709_level","data_type":"enum","enum_values":["a","b","c"],"inheritable":true}`, nil)
	ps, cs := a.createSet(org, "wp709-rules-p"), a.createSet(org, "wp709-rules-c")
	a.mustSetAttribute(org, ps.ID, `{"key":"wp709_level","data_type":"enum","enum_values":["b","c","d"],"required":true,"inheritable":true}`)
	a.mustSetAttribute(org, ps.ID, `{"key":"wp709_shared","data_type":"text","default":"parent","inheritable":true}`)
	a.mustSetAttribute(org, ps.ID, `{"key":"wp709_private","data_type":"text","inheritable":false}`)
	a.mustSetAttribute(org, cs.ID, `{"key":"wp709_shared","data_type":"text","default":"child","inheritable":true}`)
	a.putSets(org, parent.ID, ps.ID)
	a.putSets(org, child.ID, cs.ID)

	for _, typeID := range []string{parent.ID, child.ID} {
		typ := a.typeOf(org, typeID)
		level := prop(&typ, "wp709_level")
		if asJSON(level["enum"]) != `["b","c"]` || !slices.Contains(typ.RequiredFields, "wp709_level") {
			t.Errorf("%s: wp709_level = %v, required %v; want the intersection [b c], required", typ.Key, level, typ.RequiredFields)
		}
		defs := a.resolved(org, typeID, "")
		if d := defs["wp709_level"]; !d.Required || !reflect.DeepEqual(d.EnumValues, []string{"b", "c"}) {
			t.Errorf("%s: resolved wp709_level = %+v", typ.Key, d)
		}
	}
	parentType, childType := a.typeOf(org, parent.ID), a.typeOf(org, child.ID)
	if prop(&parentType, "wp709_private") == nil || prop(&parentType, "wp709_shared")["default"] != "parent" {
		t.Errorf("parent schema: %v", properties(&parentType))
	}
	if prop(&childType, "wp709_private") != nil {
		t.Error("the child inherits a set attribute with inheritable false")
	}
	if got := prop(&childType, "wp709_shared")["default"]; got != "child" {
		t.Errorf("child default = %v, want child (the child's set comes after the parent's)", got)
	}
	childDefs := a.resolved(org, child.ID, "")
	if _, ok := childDefs["wp709_private"]; ok || childDefs["wp709_shared"].DefaultValue != "child" {
		t.Errorf("resolved child: %+v", childDefs)
	}

	// The instance tightens the set.
	ciID := f.CI(t, org, f.Client1, "wp709-rules-ci")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET ci_type_id = $2 WHERE id = $1`, ciID, child.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.repo.UpsertInstanceField(a.ctx(org), org, ciID, &citype.UpsertInstanceFieldRequest{
		Name: "wp709_level", DataType: "enum", EnumValues: []string{"c", "d"},
	}); err != nil {
		t.Fatalf("instance field: %v", err)
	}
	if d := a.resolved(org, child.ID, ciID)["wp709_level"]; !d.Required || !reflect.DeepEqual(d.EnumValues, []string{"c"}) {
		t.Errorf("resolved with the instance: %+v, want required and [c]", d)
	}

	// An inactive set is missing in the schema and in ResolveFields.
	a.must(http.StatusOK, org, http.MethodPatch, "/api/v1/attribute-sets/"+cs.ID, `{"is_active":false}`, nil)
	childType = a.typeOf(org, child.ID)
	if got := prop(&childType, "wp709_shared")["default"]; got != "parent" {
		t.Errorf("child default with the child's set inactive = %v, want parent", got)
	}
	a.must(http.StatusOK, org, http.MethodPatch, "/api/v1/attribute-sets/"+ps.ID, `{"is_active":false}`, nil)
	parentType = a.typeOf(org, parent.ID)
	if prop(&parentType, "wp709_shared") != nil || prop(&parentType, "wp709_private") != nil ||
		asJSON(prop(&parentType, "wp709_level")["enum"]) != `["a","b","c"]` {
		t.Errorf("parent schema with its set inactive: %v", properties(&parentType))
	}
	for _, typeID := range []string{parent.ID, child.ID} {
		defs := a.resolved(org, typeID, "")
		if _, ok := defs["wp709_shared"]; ok || defs["wp709_level"].Required {
			t.Errorf("ResolveFields of %s composes an inactive set: %+v", typeID, defs)
		}
	}
}

// TestAttributeSetCompositionConflictsAgainstPostgres covers MET-16
// (WP-709): a data type conflict between a set and the type, or the
// organization's global attributes, is a 422 on attribute_set_id with the
// code of the conflict when the set list is replaced, and assignment and
// schema_version stay unchanged; a conflict caused by a changed set
// attribute is a 422 on key.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAttributeSetCompositionConflictsAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "c9")
	a := newSetAPI(t, f)
	org := f.OrgA
	typ := a.createType(org, "wp709-conflict")
	child := a.childType(org, "wp709-conflict-child", typ.ID)
	a.must(http.StatusCreated, org, http.MethodPost, "/api/v1/attribute-definitions",
		`{"ci_type_id":"`+typ.ID+`","key":"wp709_size","data_type":"integer"}`, nil)
	a.must(http.StatusCreated, org, http.MethodPost, "/api/v1/attribute-definitions",
		`{"ci_type_id":"`+typ.ID+`","key":"wp709_mode","data_type":"enum","enum_values":["a","b"]}`, nil)
	a.must(http.StatusCreated, org, http.MethodPost, "/api/v1/attribute-definitions",
		`{"ci_type_id":"`+child.ID+`","key":"wp709_child_only","data_type":"boolean"}`, nil)
	a.must(http.StatusCreated, org, http.MethodPost, "/api/v1/attribute-definitions",
		`{"key":"wp709_org_global","data_type":"integer"}`, nil)
	ok := a.createSet(org, "wp709-conflict-ok")
	modeDef := a.mustSetAttribute(org, ok.ID, `{"key":"wp709_mode","data_type":"enum","enum_values":["b","c"]}`)
	a.putSets(org, typ.ID, ok.ID)
	before := a.typeOf(org, typ.ID)

	for name, c := range map[string]struct{ key, body string }{
		"type attribute":       {"wp709_size", `{"key":"wp709_size","data_type":"text"}`},
		"subtype attribute":    {"wp709_child_only", `{"key":"wp709_child_only","data_type":"text","inheritable":true}`},
		"org global attribute": {"wp709_org_global", `{"key":"wp709_org_global","data_type":"text"}`},
	} {
		set := a.createSet(org, "wp709-conflict-"+strings.ReplaceAll(name, " ", "-"))
		a.mustSetAttribute(org, set.ID, c.body)
		w := a.call(org, http.MethodPut, "/api/v1/ci-types/"+typ.ID+"/attribute-sets", assignment(ok.ID, set.ID))
		p := problemOf(w)
		if w.Code != http.StatusUnprocessableEntity || p.Field != "attribute_set_id" ||
			!strings.Contains(w.Body.String(), citype.CodeDatatypeConflict) || !strings.Contains(w.Body.String(), c.key) {
			t.Errorf("%s: %d %s, want 422 on attribute_set_id with %s", name, w.Code, w.Body.String(), citype.CodeDatatypeConflict)
		}
		var doc citype.TypeAttributeSets
		a.must(http.StatusOK, org, http.MethodGet, "/api/v1/ci-types/"+typ.ID+"/attribute-sets", "", &doc)
		if len(doc.AttributeSets) != 1 || doc.AttributeSets[0].AttributeSetID != ok.ID {
			t.Errorf("%s: assignment after the refusal = %+v", name, doc.AttributeSets)
		}
		if after := a.typeOf(org, typ.ID); after.SchemaVersion != before.SchemaVersion {
			t.Errorf("%s: schema_version %d after the refusal, want %d", name, after.SchemaVersion, before.SchemaVersion)
		}
	}

	// A changed set attribute that leaves no allowed value with the type.
	w := a.call(org, http.MethodPatch, "/api/v1/attribute-definitions/"+modeDef, `{"enum_values":["c"]}`)
	if p := problemOf(w); w.Code != http.StatusUnprocessableEntity || p.Field != "key" {
		t.Errorf("PATCH of the set attribute: %d %s, want 422 on key", w.Code, w.Body.String())
	}
	// A new set attribute of an assigned set that conflicts with the type.
	if w = a.setAttribute(org, ok.ID, `{"key":"wp709_size","data_type":"boolean"}`); w.Code != http.StatusUnprocessableEntity ||
		problemOf(w).Field != "key" {
		t.Errorf("new conflicting set attribute: %d %s, want 422 on key", w.Code, w.Body.String())
	}
	if after := a.typeOf(org, typ.ID); after.SchemaVersion != before.SchemaVersion ||
		asJSON(prop(&after, "wp709_mode")["enum"]) != `["b"]` {
		t.Errorf("type after the refused changes: version %d (before %d), mode %v",
			after.SchemaVersion, before.SchemaVersion, prop(&after, "wp709_mode"))
	}
	// A deactivated set leaves the composition; a type attribute may then
	// take its key with another data type. Activating the set again would
	// bring the conflict back: 422 on is_active with the code, the set stays
	// inactive.
	react := a.createSet(org, "wp709-conflict-react")
	a.mustSetAttribute(org, react.ID, `{"key":"wp709_react","data_type":"integer"}`)
	a.putSets(org, child.ID, react.ID)
	a.must(http.StatusOK, org, http.MethodPatch, "/api/v1/attribute-sets/"+react.ID, `{"is_active":false}`, nil)
	a.must(http.StatusCreated, org, http.MethodPost, "/api/v1/attribute-definitions",
		`{"ci_type_id":"`+child.ID+`","key":"wp709_react","data_type":"text"}`, nil)
	w = a.call(org, http.MethodPatch, "/api/v1/attribute-sets/"+react.ID, `{"is_active":true}`)
	if p := problemOf(w); w.Code != http.StatusUnprocessableEntity || p.Field != "is_active" ||
		!strings.Contains(w.Body.String(), citype.CodeDatatypeConflict) {
		t.Errorf("reactivating a conflicting set: %d %s, want 422 on is_active with %s",
			w.Code, w.Body.String(), citype.CodeDatatypeConflict)
	}
	var still citype.AttributeSet
	a.must(http.StatusOK, org, http.MethodGet, "/api/v1/attribute-sets/"+react.ID, "", &still)
	if still.IsActive {
		t.Error("the refused reactivation left the set active")
	}
}

// TestAttributeSetCloneAndUniqueAgainstPostgres covers MET-16 and DB-04
// (WP-709): Clone takes the set assignments of the source with their
// positions, materializes them and names them in the audit entry
// ci_type.cloned; a unique set attribute applies to the whole
// organization, also across two types with the same set, as UniqueConflicts
// and IndexStatement treat it like a global attribute.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAttributeSetCloneAndUniqueAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "c9")
	a := newSetAPI(t, f)
	bg := context.Background()
	org := f.OrgA
	src := a.createType(org, "wp709-clone-src")
	s1, s2 := a.createSet(org, "wp709-clone-1"), a.createSet(org, "wp709-clone-2")
	a.mustSetAttribute(org, s1.ID, `{"key":"wp709_clone_attr","data_type":"text","required":true}`)
	a.must(http.StatusOK, org, http.MethodPut, "/api/v1/ci-types/"+src.ID+"/attribute-sets", `{"attribute_sets":[
		{"attribute_set_id":"`+s2.ID+`","position":7},{"attribute_set_id":"`+s1.ID+`","position":3}]}`, nil)

	var clone citype.Type
	a.must(http.StatusCreated, org, http.MethodPost, "/api/v1/ci-types/"+src.ID+"/clone", `{"key":"wp709-clone-copy","name":"wp709-clone-copy"}`, &clone)
	var doc citype.TypeAttributeSets
	a.must(http.StatusOK, org, http.MethodGet, "/api/v1/ci-types/"+clone.ID+"/attribute-sets", "", &doc)
	want := []citype.AttributeSetAssignment{{AttributeSetID: s1.ID, Position: 3}, {AttributeSetID: s2.ID, Position: 7}}
	if !reflect.DeepEqual(doc.AttributeSets, want) {
		t.Errorf("clone assignment = %+v, want %+v", doc.AttributeSets, want)
	}
	if got := a.typeOf(org, clone.ID); prop(&got, "wp709_clone_attr") == nil || !slices.Contains(got.RequiredFields, "wp709_clone_attr") {
		t.Errorf("clone schema lacks the set attribute: %v", properties(&got))
	}
	var changes struct {
		After struct {
			AttributeSets []citype.AttributeSetAssignment `json:"attribute_sets"`
		} `json:"after"`
	}
	var raw []byte
	if err := f.Admin.QueryRow(bg, `SELECT changes FROM audit_log WHERE organization_id = $1 AND action = 'ci_type.cloned'
		AND resource_id = $2`, org, clone.ID).Scan(&raw); err != nil {
		t.Fatalf("audit ci_type.cloned: %v", err)
	}
	if err := json.Unmarshal(raw, &changes); err != nil || !reflect.DeepEqual(changes.After.AttributeSets, want) {
		t.Errorf("audit ci_type.cloned attribute_sets = %s (%v)", raw, err)
	}

	// unique over two types with the same set.
	uniq := a.createSet(org, "wp709-unique")
	serialID := a.mustSetAttribute(org, uniq.ID, `{"key":"wp709_serial","data_type":"text","unique":true}`)
	rackID := a.mustSetAttribute(org, uniq.ID, `{"key":"wp709_shelf","data_type":"text","searchable":true}`)
	t1, t2 := a.createType(org, "wp709-unique-1"), a.createType(org, "wp709-unique-2")
	a.putSets(org, t1.ID, uniq.ID)
	a.putSets(org, t2.ID, uniq.ID)
	ciID := f.CI(t, org, f.Client1, "wp709-unique-ci")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET ci_type_id = $2, attributes = '{"wp709_serial": "SN-1"}' WHERE id = $1`,
		ciID, t1.ID); err != nil {
		t.Fatal(err)
	}
	ciRepo := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	conflicts, err := ciRepo.UniqueConflicts(a.ctx(org), org, t2.ID, f.Client2, "", map[string]any{"wp709_serial": "SN-1"})
	if err != nil || !slices.Equal(conflicts, []string{"wp709_serial"}) {
		t.Errorf("UniqueConflicts on the other type and client = %v (%v), want [wp709_serial]", conflicts, err)
	}
	for id, check := range map[string]func(d *citype.AttributeDefinition) bool{
		serialID: func(d *citype.AttributeDefinition) bool {
			stmt := citype.IndexStatement(d)
			return strings.Contains(stmt, citype.IndexName(d.ID, true)) && !strings.Contains(stmt, "ci_type_id")
		},
		rackID: citype.UsesKeyIndex,
	} {
		d, gerr := a.repo.GetAttributeDefinition(a.ctx(org), org, id)
		if gerr != nil {
			t.Fatal(gerr)
		}
		if !check(d) {
			t.Errorf("%s is not indexed like a global attribute: %q", d.Key, citype.IndexStatement(d))
		}
	}
}
