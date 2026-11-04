package citype

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
)

func f64(v float64) *float64 { return &v }
func intp(v int) *int        { return &v }

// TestComposeRules covers the composition rules of MET-16: for the same
// key required is OR-ed, the allowed values are intersected, min/max and
// the length bounds take the narrowest range, all patterns apply, the
// default of the last layer that sets one wins; a different data type, an
// empty value set or an empty range is an error.
func TestComposeRules(t *testing.T) {
	base := []fieldmeta.FieldDefinition{
		{Name: "tier", DataType: fieldmeta.TypeEnum, EnumValues: []string{"a", "b", "c"}, DefaultValue: "a", Label: "Tier"},
		{Name: "ports", DataType: fieldmeta.TypeInteger, Required: true,
			Validation: &fieldmeta.ValidationRules{Min: f64(1), Max: f64(512)}},
		{Name: "code", DataType: fieldmeta.TypeText,
			Validation: &fieldmeta.ValidationRules{Pattern: "^[A-Z]", MaxLength: intp(10), Options: []any{"AX", "BX", "CY"}}},
		{Name: "note", DataType: fieldmeta.TypeTextarea},
	}
	typed := []fieldmeta.FieldDefinition{
		{Name: "owner", DataType: fieldmeta.TypeText},
		{Name: "tier", DataType: fieldmeta.TypeEnum, EnumValues: []string{"c", "b", "d"}, Required: true},
		{Name: "ports", DataType: fieldmeta.TypeInteger, DefaultValue: "24",
			Validation: &fieldmeta.ValidationRules{Min: f64(8), Max: f64(1024)}},
		{Name: "code", DataType: fieldmeta.TypeText,
			Validation: &fieldmeta.ValidationRules{Pattern: "X$", MinLength: intp(2), Options: []any{"BX", "CY", "DX"}}},
		{Name: "note", DataType: fieldmeta.TypeMultiline, Label: "Note"},
	}
	got, err := Compose(base, typed)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	byName := map[string]fieldmeta.FieldDefinition{}
	for _, d := range got {
		names = append(names, d.Name)
		byName[d.Name] = d
	}
	if want := []string{"tier", "ports", "code", "note", "owner"}; !reflect.DeepEqual(names, want) {
		t.Errorf("order = %v, want %v", names, want)
	}

	tier := byName["tier"]
	if !tier.Required || !reflect.DeepEqual(tier.EnumValues, []string{"b", "c"}) || tier.DefaultValue != "a" || tier.Label != "Tier" {
		t.Errorf("tier = %+v", tier)
	}
	ports := byName["ports"]
	if !ports.Required || *ports.Validation.Min != 8 || *ports.Validation.Max != 512 || ports.DefaultValue != "24" {
		t.Errorf("ports = %+v %+v", ports, ports.Validation)
	}
	code := byName["code"]
	v := code.Validation
	if v.Pattern != "^[A-Z]" || !reflect.DeepEqual(v.Patterns, []string{"X$"}) || *v.MinLength != 2 || *v.MaxLength != 10 ||
		!reflect.DeepEqual(v.Options, []any{"BX", "CY"}) {
		t.Errorf("code validation = %+v", v)
	}
	// Every rule of both layers applies to the composed definition.
	for value, ok := range map[string]bool{"BX": true, "CY": false, "AX": false} {
		if verr := fieldmeta.ValidateValue(&code, value); (verr == nil) != ok {
			t.Errorf("code %q: %v, want ok=%v", value, verr, ok)
		}
	}
	if note := byName["note"]; note.Label != "Note" || note.DataType != fieldmeta.TypeMultiline {
		t.Errorf("note = %+v (former type names compose with the MET-11 name)", note)
	}

	// A layer that sets nothing keeps the rules of the wider one.
	got, err = Compose(base[1:2], []fieldmeta.FieldDefinition{{Name: "ports", DataType: fieldmeta.TypeInteger}})
	if err != nil || !got[0].Required || *got[0].Validation.Min != 1 {
		t.Errorf("ports without rules = %+v %v", got, err)
	}
}

func TestComposeConflicts(t *testing.T) {
	yes, no := true, false
	for name, tc := range map[string]struct {
		base, top fieldmeta.FieldDefinition
		code      string
	}{
		"data type": {
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeText},
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeInteger},
			CodeDatatypeConflict,
		},
		"reference target": {
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeUser},
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeReference, ReferenceTarget: "team"},
			CodeDatatypeConflict,
		},
		"multi_value": {
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeText, MultiValue: &yes},
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeText, MultiValue: &no},
			CodeDatatypeConflict,
		},
		"empty value set": {
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeEnum, EnumValues: []string{"a"}},
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeEnum, EnumValues: []string{"b"}},
			CodeEnumEmpty,
		},
		"empty range": {
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeDecimal, Validation: &fieldmeta.ValidationRules{Max: f64(5)}},
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeDecimal, Validation: &fieldmeta.ValidationRules{Min: f64(6)}},
			CodeRangeEmpty,
		},
		"empty length range": {
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeText, Validation: &fieldmeta.ValidationRules{MinLength: intp(4)}},
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeText, Validation: &fieldmeta.ValidationRules{MaxLength: intp(3)}},
			CodeLengthRangeEmpty,
		},
	} {
		_, err := Compose([]fieldmeta.FieldDefinition{tc.base}, []fieldmeta.FieldDefinition{tc.top})
		var conflict *CompositionError
		if !errors.As(err, &conflict) || conflict.Key != "k" {
			t.Errorf("%s: err = %v, want a composition error of k", name, err)
			continue
		}
		if conflict.Code != tc.code || conflict.CompositionCode() != tc.code {
			t.Errorf("%s: code = %q, want %q", name, conflict.CompositionCode(), tc.code)
		}
		if !strings.HasSuffix(err.Error(), "(MET-16)") || !strings.HasPrefix(err.Error(), "attribute k: ") {
			t.Errorf("%s: message = %q", name, err.Error())
		}
	}
	// The specific reference names name their target.
	if _, err := Compose([]fieldmeta.FieldDefinition{{Name: "k", DataType: fieldmeta.TypeCIRef}},
		[]fieldmeta.FieldDefinition{{Name: "k", DataType: fieldmeta.TypeReference}}); err != nil {
		t.Errorf("ci_ref and reference to ci: %v", err)
	}
	// An instance definition does not know multi_value (nil): it is no
	// conflict and takes the value of the type attribute below.
	got, err := Compose([]fieldmeta.FieldDefinition{{Name: "k", DataType: fieldmeta.TypeText, MultiValue: &yes}},
		[]fieldmeta.FieldDefinition{{Name: "k", DataType: fieldmeta.TypeText}})
	if err != nil {
		t.Fatalf("instance over a multi-valued type attribute: %v", err)
	}
	if got[0].MultiValue == nil || !*got[0].MultiValue {
		t.Errorf("composed multi_value = %v, want true", got[0].MultiValue)
	}
}

// TestComposeReferenceTargetOf: the composition compares the targets of
// fieldmeta.ReferenceTargetOf (WP-254): an alias type and reference with
// the same target compose, reference without target refers to a CI, file
// to a document; a different target is a data type conflict.
func TestComposeReferenceTargetOf(t *testing.T) {
	for name, tc := range map[string]struct {
		base, top fieldmeta.FieldDefinition
		target    string
		conflict  bool
	}{
		"user and reference to user": {
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeUser},
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeReference, ReferenceTarget: "user"}, "user", false,
		},
		"location and reference to location": {
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeLocation},
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeReference, ReferenceTarget: "location"}, "location", false,
		},
		"asset_ref and reference to asset": {
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeAssetRef},
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeReference, ReferenceTarget: "asset"}, "asset", false,
		},
		"reference without target and ci_ref": {
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeReference},
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeCIRef}, "ci", false,
		},
		"file and file": {
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeFile},
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeFile}, "document", false,
		},
		"team and location": {
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeTeam},
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeLocation}, "", true,
		},
		"reference to ci and reference to asset": {
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeReference},
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeReference, ReferenceTarget: "asset"}, "", true,
		},
	} {
		got, err := Compose([]fieldmeta.FieldDefinition{tc.base}, []fieldmeta.FieldDefinition{tc.top})
		if tc.conflict {
			var conflict *CompositionError
			if !errors.As(err, &conflict) || conflict.Code != CodeDatatypeConflict {
				t.Errorf("%s: err = %v, want %s", name, err, CodeDatatypeConflict)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if target := fieldmeta.ReferenceTargetOf(&got[0]); target != tc.target {
			t.Errorf("%s: composed target = %q, want %q", name, target, tc.target)
		}
		if referenceTarget(&tc.base) != fieldmeta.ReferenceTargetOf(&tc.base) {
			t.Errorf("%s: referenceTarget differs from fieldmeta.ReferenceTargetOf", name)
		}
	}
}

// layeredLister serves one definition per layer and the set layers
// (WP-709) as given; globals and chain, when set, replace the system and
// organization layer and the one-type chain (WP-255).
type layeredLister struct {
	system, org, typed, instance Field
	sets                         [][]Field
	globals                      []Field
	chain                        []Type
	templates                    [][]templateItem
}

func (l layeredLister) ListSetFields(context.Context, string, string) ([][]Field, error) {
	return l.sets, nil
}

func (l layeredLister) ListGlobalFields(context.Context, string) ([]Field, error) {
	if l.globals != nil {
		return l.globals, nil
	}
	return []Field{l.system, l.org}, nil
}

func (l layeredLister) TypeChain(_ context.Context, _, typeID string) ([]Type, error) {
	if l.chain != nil {
		// The chain root → typeID along ParentTypeID.
		out := []Type{}
		for cur := typeID; cur != ""; {
			i := slices.IndexFunc(l.chain, func(t Type) bool { return t.ID == cur })
			if i < 0 {
				break
			}
			out = append([]Type{l.chain[i]}, out...)
			cur = l.chain[i].ParentTypeID
		}
		return out, nil
	}
	return []Type{{ID: typeID, Fields: []Field{l.typed}}}, nil
}

func (l layeredLister) TypeCategory(_ context.Context, _, typeID string) (string, error) {
	if i := slices.IndexFunc(l.chain, func(t Type) bool { return t.ID == typeID }); i >= 0 {
		return l.chain[i].Category, nil
	}
	return "", nil
}

func (l layeredLister) ListInstanceFields(context.Context, string, string) ([]InstanceField, error) {
	in := l.instance
	if in.Name == "" {
		return nil, nil
	}
	return []InstanceField{{Name: in.Name, DataType: in.DataType, Required: in.Required, Validation: in.Validation,
		DefaultValue: in.DefaultValue, EnumValues: in.EnumValues, ReferenceTarget: in.ReferenceTarget}}, nil
}

// ciLayers implements ciLayerLister: the instance layer and the templates
// of the CI (WP-710) in position order.
func (l layeredLister) ciLayers(ctx context.Context, orgID, ciID string) ([]InstanceField, [][]templateItem, ruleScope, error) {
	in, err := l.ListInstanceFields(ctx, orgID, ciID)
	return in, l.templates, ruleScope{}, err
}

// TestResolveFieldsComposesLayers: the effective fields compose system
// base → organization → type → instance; no layer drops the rules of a
// wider one.
func TestResolveFieldsComposesLayers(t *testing.T) {
	lister := layeredLister{
		system:   Field{Name: "size", DataType: fieldmeta.TypeInteger, IsSystem: true, Validation: &fieldmeta.ValidationRules{Min: f64(0)}},
		org:      Field{Name: "size", DataType: fieldmeta.TypeInteger, Validation: &fieldmeta.ValidationRules{Max: f64(100)}},
		typed:    Field{Name: "size", DataType: fieldmeta.TypeInteger, Required: true},
		instance: Field{Name: "size", DataType: fieldmeta.TypeInteger, Validation: &fieldmeta.ValidationRules{Min: f64(10)}},
	}
	defs, err := ResolveFields(context.Background(), lister, "org", "type", "ci")
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 || !defs[0].Required || *defs[0].Validation.Min != 10 || *defs[0].Validation.Max != 100 {
		t.Fatalf("resolved = %+v", defs)
	}

	lister.instance.DataType = fieldmeta.TypeText
	_, err = ResolveFields(context.Background(), lister, "org", "type", "ci")
	var conflict *CompositionError
	if !errors.As(err, &conflict) || conflict.CompositionKey() != "size" {
		t.Errorf("instance with another data type: %v", err)
	}

	// Every layer (WP-252): system base → organization → chain root →
	// parent → type (WP-248) → set (WP-709) → two templates (WP-710) →
	// instance, each narrowing size once.
	yes := true
	full := layeredLister{
		globals: []Field{
			{Name: "size", DataType: fieldmeta.TypeInteger, IsSystem: true, Validation: &fieldmeta.ValidationRules{Min: f64(0)}},
			{Name: "size", DataType: fieldmeta.TypeInteger, Validation: &fieldmeta.ValidationRules{Max: f64(1000)}},
		},
		chain: []Type{
			{ID: "root", Fields: []Field{{Name: "size", DataType: fieldmeta.TypeInteger, Inheritable: true,
				Validation: &fieldmeta.ValidationRules{Max: f64(500)}}}},
			{ID: "parent", ParentTypeID: "root", Fields: []Field{{Name: "size", DataType: fieldmeta.TypeInteger, Inheritable: true,
				Validation: &fieldmeta.ValidationRules{Min: f64(2)}}}},
			{ID: "type", ParentTypeID: "parent", Fields: []Field{{Name: "size", DataType: fieldmeta.TypeInteger,
				Validation: &fieldmeta.ValidationRules{Max: f64(400)}}}},
		},
		sets: [][]Field{{{Name: "size", DataType: fieldmeta.TypeInteger, Validation: &fieldmeta.ValidationRules{Max: f64(300)}}}},
		templates: [][]templateItem{
			{templateItemOf(AttributeDefinition{Key: "size", DataType: fieldmeta.TypeInteger}, nil, `"7"`)},
			{templateItemOf(AttributeDefinition{Key: "size", DataType: fieldmeta.TypeInteger}, &yes, "")},
		},
		instance: Field{Name: "size", DataType: fieldmeta.TypeInteger, Validation: &fieldmeta.ValidationRules{Max: f64(200)}},
	}
	defs, err = ResolveFields(context.Background(), full, "org", "type", "ci")
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 || !defs[0].Required || *defs[0].Validation.Min != 2 || *defs[0].Validation.Max != 200 ||
		defs[0].DefaultValue != "7" {
		t.Fatalf("resolved over every layer = %+v %+v, want required, 2..200, default 7", defs, defs[0].Validation)
	}
}

// templateItemOf is an item of a template version over def with the
// required and default overrides (WP-251).
func templateItemOf(def AttributeDefinition, required *bool, defaultJSON string) templateItem {
	it := templateItem{def: def, item: TemplateVersionItem{Key: def.Key, RequiredOverride: required}}
	if defaultJSON != "" {
		it.item.DefaultOverride = []byte(defaultJSON)
	}
	return it
}

// TestCompositionConformanceMatrix is the conformance matrix of MET-16
// (WP-252, TST-01 part "template composition"): for each rule a case that
// composes and a case with the expected code where the rule can fail
// (required-OR, allOf and the default cannot; their second case shows that
// a later layer does not loosen them), then the layer cases: a type chain
// of three levels, two sets in position order, two combined templates
// (ABN-01 c, E-37), the organization's globals after the system base, the
// instance refinement and a type with several conflicts (ComposeAll). A
// case either lists layers or a lister whose layers compositionLayers
// builds (the one layer order of ResolveFields and CheckComposition).
func TestCompositionConformanceMatrix(t *testing.T) {
	type def = fieldmeta.FieldDefinition
	yes, no := true, false
	text := func(name string) def { return def{Name: name, DataType: fieldmeta.TypeText} }
	withV := func(d def, v fieldmeta.ValidationRules) def { d.Validation = &v; return d }
	enum := func(name string, values ...string) def {
		return def{Name: name, DataType: fieldmeta.TypeEnum, EnumValues: values}
	}
	cases := []struct {
		name   string
		layers [][]def
		lister *layeredLister
		// codes are the expected conflicts in order ("" none).
		codes []string
		// check inspects the composed definition of k (rule cases) or the
		// composed list (layer cases); "" is fine.
		check func(byName map[string]def) string
	}{
		{name: "required-OR: a narrower layer makes it required",
			layers: [][]def{{text("k")}, {{Name: "k", DataType: fieldmeta.TypeText, Required: true}}},
			check:  wantField("k", func(d def) bool { return d.Required })},
		{name: "required-OR: a later layer cannot drop it",
			layers: [][]def{{{Name: "k", DataType: fieldmeta.TypeText, Required: true}}, {text("k")}},
			check:  wantField("k", func(d def) bool { return d.Required })},
		{name: "enum_values intersect",
			layers: [][]def{{enum("k", "a", "b", "c")}, {enum("k", "c", "b", "d")}},
			check:  wantField("k", func(d def) bool { return reflect.DeepEqual(d.EnumValues, []string{"b", "c"}) })},
		{name: "enum_values without a common value",
			layers: [][]def{{enum("k", "a")}, {enum("k", "b")}}, codes: []string{CodeEnumEmpty}},
		{name: "validation.options intersect",
			layers: [][]def{{withV(text("k"), fieldmeta.ValidationRules{Options: []any{"x", "y"}})},
				{withV(text("k"), fieldmeta.ValidationRules{Options: []any{"y", "z"}})}},
			check: wantField("k", func(d def) bool { return reflect.DeepEqual(d.Validation.Options, []any{"y"}) })},
		{name: "validation.options without a common value",
			layers: [][]def{{withV(text("k"), fieldmeta.ValidationRules{Options: []any{"x"}})},
				{withV(text("k"), fieldmeta.ValidationRules{Options: []any{"z"}})}},
			codes: []string{CodeEnumEmpty}},
		{name: "enum_values and validation.options intersect",
			layers: [][]def{{enum("k", "a", "b")}, {{Name: "k", DataType: fieldmeta.TypeEnum,
				Validation: &fieldmeta.ValidationRules{Options: []any{"b", "c"}}}}},
			check: wantField("k", func(d def) bool { return reflect.DeepEqual(d.EnumValues, []string{"b"}) })},
		{name: "min/max take the narrowest range",
			layers: [][]def{{withV(def{Name: "k", DataType: fieldmeta.TypeInteger}, fieldmeta.ValidationRules{Min: f64(1), Max: f64(100)})},
				{withV(def{Name: "k", DataType: fieldmeta.TypeInteger}, fieldmeta.ValidationRules{Min: f64(5), Max: f64(200)})}},
			check: wantField("k", func(d def) bool { return *d.Validation.Min == 5 && *d.Validation.Max == 100 })},
		{name: "min/max with an empty range",
			layers: [][]def{{withV(def{Name: "k", DataType: fieldmeta.TypeInteger}, fieldmeta.ValidationRules{Max: f64(4)})},
				{withV(def{Name: "k", DataType: fieldmeta.TypeInteger}, fieldmeta.ValidationRules{Min: f64(5)})}},
			codes: []string{CodeRangeEmpty}},
		{name: "min_length/max_length take the narrowest range",
			layers: [][]def{{withV(text("k"), fieldmeta.ValidationRules{MinLength: intp(1), MaxLength: intp(20)})},
				{withV(text("k"), fieldmeta.ValidationRules{MinLength: intp(3), MaxLength: intp(30)})}},
			check: wantField("k", func(d def) bool { return *d.Validation.MinLength == 3 && *d.Validation.MaxLength == 20 })},
		{name: "min_length/max_length with an empty range",
			layers: [][]def{{withV(text("k"), fieldmeta.ValidationRules{MaxLength: intp(2)})},
				{withV(text("k"), fieldmeta.ValidationRules{MinLength: intp(3)})}},
			codes: []string{CodeLengthRangeEmpty}},
		{name: "patterns all apply (allOf)",
			layers: [][]def{{withV(text("k"), fieldmeta.ValidationRules{Pattern: "^a"})},
				{withV(text("k"), fieldmeta.ValidationRules{Pattern: "z$"})}},
			check: wantField("k", func(d def) bool {
				return d.Validation.Pattern == "^a" && reflect.DeepEqual(d.Validation.Patterns, []string{"z$"}) &&
					fieldmeta.ValidateValue(&d, "abz") == nil && fieldmeta.ValidateValue(&d, "ab") != nil
			})},
		{name: "patterns: a later layer cannot drop one",
			layers: [][]def{{withV(text("k"), fieldmeta.ValidationRules{Pattern: "^a"})}, {text("k")}},
			check:  wantField("k", func(d def) bool { return d.Validation != nil && d.Validation.Pattern == "^a" })},
		{name: "default of the last layer that sets one",
			layers: [][]def{{{Name: "k", DataType: fieldmeta.TypeText, DefaultValue: "a"}},
				{{Name: "k", DataType: fieldmeta.TypeText, DefaultValue: "b"}}},
			check: wantField("k", func(d def) bool { return d.DefaultValue == "b" })},
		{name: "default: a layer without one keeps it",
			layers: [][]def{{{Name: "k", DataType: fieldmeta.TypeText, DefaultValue: "a"}}, {text("k")}},
			check:  wantField("k", func(d def) bool { return d.DefaultValue == "a" })},
		{name: "data type: former names compose with the MET-11 name",
			layers: [][]def{{{Name: "k", DataType: fieldmeta.TypeTextarea}}, {{Name: "k", DataType: fieldmeta.TypeMultiline}}},
			check:  wantField("k", func(d def) bool { return d.DataType == fieldmeta.TypeMultiline })},
		{name: "data type differs",
			layers: [][]def{{text("k")}, {{Name: "k", DataType: fieldmeta.TypeInteger}}}, codes: []string{CodeDatatypeConflict}},
		{name: "reference target: alias and reference with the same target",
			layers: [][]def{{{Name: "k", DataType: fieldmeta.TypeUser}}, {{Name: "k", DataType: fieldmeta.TypeReference, ReferenceTarget: "user"}}}},
		{name: "reference target differs",
			layers: [][]def{{{Name: "k", DataType: fieldmeta.TypeReference, ReferenceTarget: "user"}},
				{{Name: "k", DataType: fieldmeta.TypeReference, ReferenceTarget: "team"}}},
			codes: []string{CodeDatatypeConflict}},
		{name: "multi_value equal",
			layers: [][]def{{{Name: "k", DataType: fieldmeta.TypeText, MultiValue: &yes}}, {{Name: "k", DataType: fieldmeta.TypeText, MultiValue: &yes}}},
			check:  wantField("k", func(d def) bool { return d.MultiValue != nil && *d.MultiValue })},
		{name: "multi_value differs",
			layers: [][]def{{{Name: "k", DataType: fieldmeta.TypeText, MultiValue: &yes}}, {{Name: "k", DataType: fieldmeta.TypeText, MultiValue: &no}}},
			codes:  []string{CodeDatatypeConflict}},
		{name: "multi_value missing on the instance is no conflict",
			layers: [][]def{{{Name: "k", DataType: fieldmeta.TypeText, MultiValue: &yes}}, {text("k")}},
			check:  wantField("k", func(d def) bool { return d.MultiValue != nil && *d.MultiValue })},

		{name: "type chain of three levels root → leaf",
			lister: &layeredLister{chain: []Type{
				{ID: "root", Fields: []Field{{Name: "k", DataType: fieldmeta.TypeInteger, Inheritable: true,
					Validation: &fieldmeta.ValidationRules{Max: f64(100)}}, {Name: "private", DataType: fieldmeta.TypeText}}},
				{ID: "mid", ParentTypeID: "root", Fields: []Field{{Name: "k", DataType: fieldmeta.TypeInteger, Inheritable: true,
					Validation: &fieldmeta.ValidationRules{Min: f64(10)}}}},
				{ID: "leaf", ParentTypeID: "mid", Fields: []Field{{Name: "k", DataType: fieldmeta.TypeInteger, Required: true}}},
			}, globals: []Field{}},
			check: func(m map[string]def) string {
				k, ok := m["k"]
				switch {
				case !ok || !k.Required || *k.Validation.Min != 10 || *k.Validation.Max != 100:
					return "k is not composed root → mid → leaf"
				case len(m) != 1:
					return "a definition of an ancestor that is not inheritable reached the leaf"
				}
				return ""
			}},
		{name: "two sets in position order",
			lister: &layeredLister{globals: []Field{}, typed: Field{Name: "k", DataType: fieldmeta.TypeEnum, EnumValues: []string{"a", "b", "c"}},
				sets: [][]Field{
					{{Name: "k", DataType: fieldmeta.TypeEnum, EnumValues: []string{"b", "c"}, DefaultValue: "b"}},
					{{Name: "k", DataType: fieldmeta.TypeEnum, DefaultValue: "c"}},
				}},
			check: wantField("k", func(d def) bool { return reflect.DeepEqual(d.EnumValues, []string{"b", "c"}) && d.DefaultValue == "c" })},
		{name: "two combined templates (ABN-01 c, E-37)",
			lister: &layeredLister{globals: []Field{}, typed: Field{Name: "k", DataType: fieldmeta.TypeText},
				templates: [][]templateItem{
					{templateItemOf(AttributeDefinition{Key: "k", DataType: fieldmeta.TypeText}, &yes, `"first"`),
						templateItemOf(AttributeDefinition{Key: "only_first", DataType: fieldmeta.TypeText}, nil, "")},
					{templateItemOf(AttributeDefinition{Key: "k", DataType: fieldmeta.TypeText}, &no, `"second"`)},
				}},
			check: func(m map[string]def) string {
				if k := m["k"]; !k.Required || k.DefaultValue != "second" {
					return "k: want required from the first template, default of the second"
				}
				if _, ok := m["only_first"]; !ok {
					return "a key of a template alone is missing"
				}
				return ""
			}},
		{name: "organization's globals after the system base",
			lister: &layeredLister{
				system: Field{Name: "k", DataType: fieldmeta.TypeInteger, IsSystem: true, Validation: &fieldmeta.ValidationRules{Max: f64(10)}},
				org:    Field{Name: "k", DataType: fieldmeta.TypeInteger, Required: true, Validation: &fieldmeta.ValidationRules{Max: f64(5)}},
				typed:  Field{Name: "other", DataType: fieldmeta.TypeText}},
			check: wantField("k", func(d def) bool { return d.Required && *d.Validation.Max == 5 })},
		{name: "organization's global against the system base",
			lister: &layeredLister{
				system: Field{Name: "k", DataType: fieldmeta.TypeInteger, IsSystem: true},
				org:    Field{Name: "k", DataType: fieldmeta.TypeText},
				typed:  Field{Name: "other", DataType: fieldmeta.TypeText}},
			codes: []string{CodeDatatypeConflict}},
		{name: "instance refines the type as the last layer",
			lister: &layeredLister{globals: []Field{},
				typed:    Field{Name: "k", DataType: fieldmeta.TypeInteger, Validation: &fieldmeta.ValidationRules{Max: f64(100)}},
				instance: Field{Name: "k", DataType: fieldmeta.TypeInteger, Validation: &fieldmeta.ValidationRules{Max: f64(50)}}},
			check: wantField("k", func(d def) bool { return *d.Validation.Max == 50 })},
		{name: "instance with another data type",
			lister: &layeredLister{globals: []Field{},
				typed:    Field{Name: "k", DataType: fieldmeta.TypeInteger},
				instance: Field{Name: "k", DataType: fieldmeta.TypeText}},
			codes: []string{CodeDatatypeConflict}},
		{name: "a type with several conflicts reports all (ComposeAll)",
			lister: &layeredLister{globals: []Field{
				{Name: "a", DataType: fieldmeta.TypeInteger, IsSystem: true, Validation: &fieldmeta.ValidationRules{Max: f64(1)}},
				{Name: "b", DataType: fieldmeta.TypeText, IsSystem: true},
			}, chain: []Type{{ID: "type", Fields: []Field{
				{Name: "b", DataType: fieldmeta.TypeInteger},
				{Name: "a", DataType: fieldmeta.TypeInteger, Validation: &fieldmeta.ValidationRules{Min: f64(2)}},
			}}}, sets: [][]Field{{{Name: "a", DataType: fieldmeta.TypeText}}}},
			// b conflicts first in the type layer, a in the same layer; a's
			// set conflict is not reported a second time.
			codes: []string{CodeDatatypeConflict, CodeRangeEmpty}},
	}
	if len(cases) < 25 {
		t.Fatalf("the matrix has %d cases, want at least 25", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			layers := tc.layers
			if tc.lister != nil {
				typeID := "type"
				if tc.lister.chain != nil {
					typeID = tc.lister.chain[len(tc.lister.chain)-1].ID
				}
				var err error
				if layers, _, err = compositionLayers(context.Background(), tc.lister, "org", typeID, "ci", layerSource{}); err != nil {
					t.Fatal(err)
				}
			}
			got, conflicts := ComposeAll(layers...)
			var codes []string
			for _, c := range conflicts {
				codes = append(codes, c.Code)
			}
			if !slices.Equal(codes, tc.codes) {
				t.Fatalf("conflicts = %v, want codes %v", conflicts, tc.codes)
			}
			// Compose keeps its behavior: the first conflict, or the list.
			composed, err := Compose(layers...)
			if len(tc.codes) > 0 {
				var conflict *CompositionError
				if !errors.As(err, &conflict) || conflict.Key != conflicts[0].Key || conflict.Code != conflicts[0].Code {
					t.Errorf("Compose = %v, want the first conflict %v", err, conflicts[0])
				}
				return
			}
			if err != nil || !reflect.DeepEqual(composed, got) {
				t.Errorf("Compose = %+v %v, want %+v", composed, err, got)
			}
			if tc.check != nil {
				byName := map[string]def{}
				for _, d := range got {
					byName[d.Name] = d
				}
				if problem := tc.check(byName); problem != "" {
					t.Errorf("%s: %+v", problem, got)
				}
			}
		})
	}
}

// wantField checks the composed definition of name.
func wantField(name string, ok func(fieldmeta.FieldDefinition) bool) func(map[string]fieldmeta.FieldDefinition) string {
	return func(m map[string]fieldmeta.FieldDefinition) string {
		d, found := m[name]
		if !found || !ok(d) {
			return "unexpected composition of " + name
		}
		return ""
	}
}

// TestComposeAllCollectsConflicts (WP-252): ComposeAll reports each key in
// conflict once, with its key and code, in the order of the conflicts;
// keys without a conflict compose; Compose returns the first conflict.
func TestComposeAllCollectsConflicts(t *testing.T) {
	base := []fieldmeta.FieldDefinition{
		{Name: "a", DataType: fieldmeta.TypeText},
		{Name: "b", DataType: fieldmeta.TypeEnum, EnumValues: []string{"x"}},
		{Name: "c", DataType: fieldmeta.TypeInteger, Validation: &fieldmeta.ValidationRules{Max: f64(1)}},
		{Name: "ok", DataType: fieldmeta.TypeText},
	}
	top := []fieldmeta.FieldDefinition{
		{Name: "b", DataType: fieldmeta.TypeEnum, EnumValues: []string{"y"}},
		{Name: "a", DataType: fieldmeta.TypeInteger},
		{Name: "ok", DataType: fieldmeta.TypeText, Required: true},
	}
	third := []fieldmeta.FieldDefinition{
		{Name: "a", DataType: fieldmeta.TypeBoolean},
		{Name: "c", DataType: fieldmeta.TypeInteger, Validation: &fieldmeta.ValidationRules{Min: f64(2)}},
	}
	got, conflicts := ComposeAll(base, top, third)
	type kc struct{ key, code string }
	var keys []kc
	for _, c := range conflicts {
		keys = append(keys, kc{c.Key, c.Code})
	}
	want := []kc{{"b", CodeEnumEmpty}, {"a", CodeDatatypeConflict}, {"c", CodeRangeEmpty}}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("conflicts = %v, want %v", keys, want)
	}
	if len(got) != 4 || !got[3].Required {
		t.Errorf("composed = %+v, want every key, ok required", got)
	}
	_, err := Compose(base, top, third)
	var conflict *CompositionError
	if !errors.As(err, &conflict) || conflict.Key != "b" {
		t.Errorf("Compose = %v, want the first conflict (b)", err)
	}
	if out, none := ComposeAll(base); none != nil || len(out) != 4 {
		t.Errorf("one layer: %+v %v", out, none)
	}
}

// TestResolveFieldsComposesSetLayers (WP-709, MET-16): the set layers come
// between the type chain and the instance, one per set in the order
// ListSetFields gives: a set tightens the type (required OR, intersection
// of the allowed values), the later set's default wins, the instance
// tightens the sets; a set whose data type conflicts with the type is a
// *CompositionError.
func TestResolveFieldsComposesSetLayers(t *testing.T) {
	lister := layeredLister{
		system: Field{Name: "base", DataType: fieldmeta.TypeText, IsSystem: true},
		org:    Field{Name: "org_only", DataType: fieldmeta.TypeText},
		typed:  Field{Name: "tier", DataType: fieldmeta.TypeEnum, EnumValues: []string{"a", "b", "c"}, DefaultValue: "a"},
		instance: Field{Name: "tier", DataType: fieldmeta.TypeEnum, Required: true,
			Validation: &fieldmeta.ValidationRules{Options: []any{"b"}}},
		sets: [][]Field{
			{{Name: "tier", DataType: fieldmeta.TypeEnum, EnumValues: []string{"b", "c", "d"}, DefaultValue: "c"},
				{Name: "rack", DataType: fieldmeta.TypeText, Required: true}},
			{{Name: "tier", DataType: fieldmeta.TypeEnum, DefaultValue: "b"}},
		},
	}
	defs, err := ResolveFields(context.Background(), lister, "org", "type", "ci")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]fieldmeta.FieldDefinition{}
	var names []string
	for _, d := range defs {
		names = append(names, d.Name)
		byName[d.Name] = d
	}
	if want := []string{"base", "org_only", "tier", "rack"}; !reflect.DeepEqual(names, want) {
		t.Errorf("order = %v, want %v (system, org, type chain, sets)", names, want)
	}
	tier := byName["tier"]
	if !tier.Required || !reflect.DeepEqual(tier.EnumValues, []string{"b"}) || tier.DefaultValue != "b" {
		t.Errorf("tier = %+v, want required, values [b] (type, sets and instance intersected), default b of the later set", tier)
	}
	if !byName["rack"].Required {
		t.Errorf("rack = %+v, want required from the set", byName["rack"])
	}

	// Without a type there is no set layer.
	if defs, err = ResolveFields(context.Background(), lister, "org", "", ""); err != nil || len(defs) != 2 {
		t.Errorf("without type: %+v %v", defs, err)
	}

	// A set with another data type than the type conflicts.
	lister.sets = [][]Field{{{Name: "tier", DataType: fieldmeta.TypeInteger}}}
	lister.instance = Field{Name: "other", DataType: fieldmeta.TypeText}
	_, err = ResolveFields(context.Background(), lister, "org", "type", "")
	var conflict *CompositionError
	if !errors.As(err, &conflict) || conflict.Key != "tier" || conflict.Code != CodeDatatypeConflict {
		t.Errorf("set with another data type: %v, want a %s on tier", err, CodeDatatypeConflict)
	}
}

// TestResolveFieldsAppliesTo covers the applicability of global attributes
// (MET-13, WP-255) with layeredLister: a system global applies to every
// type, an organization's global with applies_to all as well; categories
// applies to the types of a listed category (the type's own category, not
// that of an ancestor); types applies to a listed type and its subtypes,
// not to a sibling; an ID of a type that no longer exists matches nothing;
// without a type only all applies.
func TestResolveFieldsAppliesTo(t *testing.T) {
	lister := layeredLister{
		globals: []Field{
			{Name: "sys_base", DataType: fieldmeta.TypeText, IsSystem: true, Scope: "global"},
			{Name: "org_all", DataType: fieldmeta.TypeText, Scope: "global", AppliesTo: AppliesToAll},
			{Name: "org_infra", DataType: fieldmeta.TypeText, Scope: "global", AppliesTo: AppliesToCategories,
				AppliesToCategories: []string{"infrastructure"}},
			{Name: "org_x", DataType: fieldmeta.TypeText, Scope: "global", AppliesTo: AppliesToTypes,
				AppliesToTypeIDs: []string{"x", "gone"}},
		},
		chain: []Type{
			{ID: "root", Category: "service"},
			{ID: "x", ParentTypeID: "root", Category: "infrastructure"},
			{ID: "x-sub", ParentTypeID: "x", Category: "service"},
			{ID: "y", ParentTypeID: "root", Category: "infrastructure"},
		},
	}
	for typeID, want := range map[string][]string{
		"root":  {"sys_base", "org_all"},
		"x":     {"sys_base", "org_all", "org_infra", "org_x"},
		"x-sub": {"sys_base", "org_all", "org_x"},
		"y":     {"sys_base", "org_all", "org_infra"},
		"":      {"sys_base", "org_all"},
	} {
		defs, err := ResolveFields(context.Background(), lister, "org", typeID, "")
		if err != nil {
			t.Fatalf("%q: %v", typeID, err)
		}
		var names []string
		for _, d := range defs {
			names = append(names, d.Name)
		}
		if !reflect.DeepEqual(names, want) {
			t.Errorf("type %q resolves %v, want %v", typeID, names, want)
		}
	}

	// A system global is not restricted even when it carried a mode.
	lister.globals[0].AppliesTo, lister.globals[0].AppliesToCategories = AppliesToCategories, []string{"none"}
	defs, err := ResolveFields(context.Background(), lister, "org", "root", "")
	if err != nil || len(defs) != 2 || defs[0].Name != "sys_base" {
		t.Errorf("system global with a mode: %+v %v", defs, err)
	}
}

// TestMemoryDeleteFieldNoGlobalFallback (WP-255): DELETE of a type field
// does not fall back to an organization's global field of the same name;
// the global path deletes it.
func TestMemoryDeleteFieldNoGlobalFallback(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	repo.SeedType(&Type{ID: "t1", OrganizationID: "org", Key: "t1", Name: "t1", IsActive: true})
	if _, err := repo.UpsertField(ctx, "org", "", &UpsertFieldRequest{Scope: "global", Name: "cost_center", DataType: "text"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteField(ctx, "org", "t1", "cost_center"); err == nil {
		t.Error("type path deleted the global field")
	}
	if globals, _ := repo.ListGlobalFields(ctx, "org"); len(globals) != 1 {
		t.Errorf("globals after the type path = %+v, want cost_center kept", globals)
	}
	if err := repo.DeleteField(ctx, "org", "", "cost_center"); err != nil {
		t.Errorf("global path: %v", err)
	}
	if globals, _ := repo.ListGlobalFields(ctx, "org"); len(globals) != 0 {
		t.Errorf("globals after the global path = %+v", globals)
	}
}

// TestComposeComputed covers WP-258 (MET-10 computed, MET-16): the
// expression of a computed attribute stays when a narrower layer has none
// (also over several layers and from AttributeDefinition through
// Field.Definition); the same expression in two layers composes; two
// different expressions are a computed-conflict; a computed attribute that
// another layer makes required, gives a default or makes unique is a
// computed-constraint.
func TestComposeComputed(t *testing.T) {
	expr := func(raw string) *rules.Expr {
		t.Helper()
		e, err := rules.ParseExpr([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		return &e
	}
	days := expr(`{"op":"days_between","args":[{"op":"today"},{"op":"field","field":"asset.warranty_end"}]}`)
	other := expr(`{"op":"days_between","args":[{"op":"field","field":"asset.purchase_date"},{"op":"today"}]}`)
	computed := fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeInteger, Computed: days}
	plain := fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeInteger, Label: "Days left"}

	got, err := Compose([]fieldmeta.FieldDefinition{computed}, []fieldmeta.FieldDefinition{plain}, []fieldmeta.FieldDefinition{plain})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Computed == nil || !got[0].Computed.Equal(days) || got[0].Label != "Days left" {
		t.Errorf("composed = %+v, want the expression of the base kept", got[0])
	}
	got, err = Compose([]fieldmeta.FieldDefinition{plain}, []fieldmeta.FieldDefinition{computed})
	if err != nil || got[0].Computed == nil {
		t.Errorf("computed in the narrower layer: %+v %v", got, err)
	}
	same := computed
	same.Computed = expr(`{"op":"days_between","args":[{"op":"today"},{"op":"field","field":"asset.warranty_end"}]}`)
	if _, err = Compose([]fieldmeta.FieldDefinition{computed}, []fieldmeta.FieldDefinition{same}); err != nil {
		t.Errorf("the same expression in two layers: %v", err)
	}
	// From the stored definition through the legacy field shape.
	stored := AttributeDefinition{Key: "k", DataType: fieldmeta.TypeInteger, Computed: days}
	if def := stored.definition(); def.Computed == nil || !def.Computed.Equal(days) {
		t.Errorf("definition() = %+v, want the expression", def)
	}

	for name, tc := range map[string]struct {
		base, top fieldmeta.FieldDefinition
		code      string
	}{
		"different expressions": {computed, fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeInteger, Computed: other},
			CodeComputedConflict},
		"required of the narrower layer": {computed, fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeInteger, Required: true},
			CodeComputedConstraint},
		"required of the wider layer": {fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeInteger, Required: true}, computed,
			CodeComputedConstraint},
		"default of the narrower layer": {computed, fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeInteger, DefaultValue: "3"},
			CodeComputedConstraint},
		"default of the wider layer": {fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeInteger, DefaultValue: "3"}, computed,
			CodeComputedConstraint},
		"unique of the narrower layer": {computed, fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeInteger, Unique: true},
			CodeComputedConstraint},
		"unique of the wider layer": {fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeInteger, Unique: true}, computed,
			CodeComputedConstraint},
	} {
		_, cerr := Compose([]fieldmeta.FieldDefinition{tc.base}, []fieldmeta.FieldDefinition{tc.top})
		var conflict *CompositionError
		if !errors.As(cerr, &conflict) || conflict.Key != "k" || conflict.Code != tc.code {
			t.Errorf("%s: err = %v, want a composition error of k with code %s", name, cerr, tc.code)
			continue
		}
		if !strings.HasSuffix(cerr.Error(), "(MET-16)") {
			t.Errorf("%s: message = %q", name, cerr.Error())
		}
	}
	// A layer over the conflict reports nothing more (ComposeAll).
	_, conflicts := ComposeAll([]fieldmeta.FieldDefinition{computed},
		[]fieldmeta.FieldDefinition{{Name: "k", DataType: fieldmeta.TypeInteger, Required: true}},
		[]fieldmeta.FieldDefinition{{Name: "k", DataType: fieldmeta.TypeInteger, Computed: other}})
	if len(conflicts) != 1 || conflicts[0].Code != CodeComputedConstraint {
		t.Errorf("conflicts = %v, want one computed-constraint", conflicts)
	}
}
