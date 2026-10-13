package events

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestEnvelopeMatchesSchema covers EVT-01 and EVT-02 (WP-136): the
// envelope's JSON fields, the required ones and the event types are those
// of api/events.schema.json.
func TestEnvelopeMatchesSchema(t *testing.T) {
	raw, err := os.ReadFile("../../../api/events.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err = json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	var fields, required []string
	typ := reflect.TypeOf(Envelope{})
	for i := range typ.NumField() {
		name, opts, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		fields = append(fields, name)
		if opts != "omitempty" {
			required = append(required, name)
		}
	}
	var props []string
	for name := range schema.Properties {
		props = append(props, name)
	}
	sort.Strings(fields)
	sort.Strings(props)
	sort.Strings(required)
	sort.Strings(schema.Required)
	if !reflect.DeepEqual(fields, props) || !reflect.DeepEqual(required, schema.Required) {
		t.Errorf("fields %v / schema %v; required %v / schema %v", fields, props, required, schema.Required)
	}
	if !reflect.DeepEqual(schema.Properties["type"].Enum, Types) {
		t.Errorf("types %v, schema %v", Types, schema.Properties["type"].Enum)
	}
	env := Envelope{OrgID: "o-1", Type: CIChanged}
	if env.Subject() != "events.o-1.ci.changed" || IsType("ci.updated") || !IsType(JobFailed) {
		t.Error("subject or types")
	}
}
