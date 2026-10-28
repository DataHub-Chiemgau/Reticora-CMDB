package citype

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
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
		if verr := fieldmeta.ValidateValue(code, value); (verr == nil) != ok {
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
	for name, tc := range map[string]struct{ base, top fieldmeta.FieldDefinition }{
		"data type": {
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeText},
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeInteger},
		},
		"reference target": {
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeUser},
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeReference, ReferenceTarget: "team"},
		},
		"empty value set": {
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeEnum, EnumValues: []string{"a"}},
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeEnum, EnumValues: []string{"b"}},
		},
		"empty range": {
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeDecimal, Validation: &fieldmeta.ValidationRules{Max: f64(5)}},
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeDecimal, Validation: &fieldmeta.ValidationRules{Min: f64(6)}},
		},
		"empty length range": {
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeText, Validation: &fieldmeta.ValidationRules{MinLength: intp(4)}},
			fieldmeta.FieldDefinition{Name: "k", DataType: fieldmeta.TypeText, Validation: &fieldmeta.ValidationRules{MaxLength: intp(3)}},
		},
	} {
		_, err := Compose([]fieldmeta.FieldDefinition{tc.base}, []fieldmeta.FieldDefinition{tc.top})
		var conflict *CompositionError
		if !errors.As(err, &conflict) || conflict.Key != "k" {
			t.Errorf("%s: err = %v, want a composition error of k", name, err)
		}
	}
	// The specific reference names name their target.
	if _, err := Compose([]fieldmeta.FieldDefinition{{Name: "k", DataType: fieldmeta.TypeCIRef}},
		[]fieldmeta.FieldDefinition{{Name: "k", DataType: fieldmeta.TypeReference}}); err != nil {
		t.Errorf("ci_ref and reference to ci: %v", err)
	}
}

// layeredLister serves one definition per layer.
type layeredLister struct{ system, org, typed, instance Field }

func (l layeredLister) ListGlobalFields(context.Context, string) ([]Field, error) {
	return []Field{l.system, l.org}, nil
}

func (l layeredLister) ListFields(context.Context, string, string) ([]Field, error) {
	return []Field{l.typed}, nil
}

func (l layeredLister) ListInstanceFields(context.Context, string, string) ([]InstanceField, error) {
	in := l.instance
	return []InstanceField{{Name: in.Name, DataType: in.DataType, Required: in.Required, Validation: in.Validation}}, nil
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
}
