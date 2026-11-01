package citype

import (
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
)

// TestAttributeDefinitionReservedKeyNamesKeyField: a reserved namespace as
// key of an attribute definition is a 400 that names the field key, not
// data_type (F-408, GLO-13, API-03); the other rejections of
// fieldmeta.ValidateDefinition name the attribute they concern.
// Retest: cd backend && go test -count=1 -run '^TestAttributeDefinitionReservedKeyNamesKeyField$' ./internal/citype/
func TestAttributeDefinitionReservedKeyNamesKeyField(t *testing.T) {
	cases := map[string]struct {
		def   AttributeDefinition
		field string
	}{
		"empty key":        {AttributeDefinition{Key: " ", DataType: fieldmeta.TypeText}, "key"},
		"unknown type":     {AttributeDefinition{Key: "x", DataType: "nope"}, "data_type"},
		"reference target": {AttributeDefinition{Key: "x", DataType: fieldmeta.TypeText, ReferenceTarget: "nope"}, "reference_target"},
		"conditional op": {AttributeDefinition{Key: "x", DataType: fieldmeta.TypeText,
			Conditional: &fieldmeta.ConditionalRules{VisibleWhen: []fieldmeta.Predicate{{Field: "y", Op: "nope"}}}}, "conditional"},
		"conditional field": {AttributeDefinition{Key: "x", DataType: fieldmeta.TypeText,
			Conditional: &fieldmeta.ConditionalRules{RequiredWhen: []fieldmeta.Predicate{{Op: "eq"}}}}, "conditional"},
	}
	for name := range fieldmeta.ReservedNames {
		cases["reserved "+name] = struct {
			def   AttributeDefinition
			field string
		}{AttributeDefinition{Key: name, DataType: fieldmeta.TypeText}, "key"}
	}
	for _, name := range slices.Sorted(maps.Keys(cases)) {
		tc := cases[name]
		d := tc.def
		err := d.Validate()
		var fe *api.FieldError
		if !errors.As(err, &fe) {
			t.Errorf("%s: Validate() = %v, want a field error on %s", name, err, tc.field)
			continue
		}
		if fe.Field != tc.field {
			t.Errorf("%s: field %q (%s), want %q", name, fe.Field, fe.Message, tc.field)
		}
		// The problem a client receives (POST /attribute-definitions).
		rec := httptest.NewRecorder()
		if !api.WriteFieldError(rec, err) {
			t.Fatalf("%s: WriteFieldError did not answer", name)
		}
		var body struct {
			Field  string `json:"field"`
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusBadRequest || body.Field != tc.field || !strings.HasPrefix(body.Detail, tc.field+": ") {
			t.Errorf("%s: %d field=%q detail=%q, want 400 field %q", name, rec.Code, body.Field, body.Detail, tc.field)
		}
		if strings.HasPrefix(name, "reserved ") && !strings.Contains(body.Detail, "reserved namespace (GLO-13)") {
			t.Errorf("%s: detail %q does not name the reserved namespace", name, body.Detail)
		}
	}
}
