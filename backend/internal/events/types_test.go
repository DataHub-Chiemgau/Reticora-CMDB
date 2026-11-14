package events_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
)

// TestPhase3TypesAreRegistered covers EVT-03 [P3] (WP-867, WP-868, E-161):
// the reservation of phase 2 ended; every type of phase 3 is a registered
// type with a frame and stands in the type enum of api/events.schema.json,
// the module types of WP-868 among them; an unknown type is still refused by
// Append and AppendMany.
func TestPhase3TypesAreRegistered(t *testing.T) {
	raw, err := os.ReadFile("../../../api/events.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties struct {
			Type struct {
				Enum []string `json:"enum"`
			} `json:"type"`
		} `json:"properties"`
	}
	if err = json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	for _, typ := range events.Phase3Types {
		if !events.IsType(typ) {
			t.Errorf("IsType(%s) = false", typ)
		}
		if _, ok := events.Payloads[typ]; !ok {
			t.Errorf("%s has no frame", typ)
		}
		if !slices.Contains(schema.Properties.Type.Enum, typ) {
			t.Errorf("%s is missing from the type enum of the schema", typ)
		}
	}
	for _, typ := range []string{"maintenance.scheduled", "maintenance.started", "maintenance.ended", "workflow.run_started",
		"workflow.run_succeeded", "workflow.run_failed", "workflow.run_cancelled", "workflow.approval_requested",
		"workflow.approval_decided", "workflow.webhook", "finding.created", "finding.changed", "finding.resolved",
		"notification.escalated", "notification.acknowledged", "entitlement.changed", "org.export_completed",
		"org.deletion_requested", "org.deletion_cancelled"} {
		if !slices.Contains(events.Phase3Types, typ) {
			t.Errorf("%s is no type of phase 3", typ)
		}
	}
	outbox := events.NewOutbox()
	for _, typ := range []string{"workflow.started", "finding.updated", "maintenance.window_changed"} {
		if events.IsType(typ) {
			t.Errorf("IsType(%s) = true", typ)
		}
		if _, err := outbox.Append(context.Background(), nil, &events.Event{OrgID: "o", Type: typ}); err == nil || !strings.Contains(err.Error(), "unknown event type") {
			t.Errorf("Append(%s) = %v, want unknown event type", typ, err)
		}
		if _, err := outbox.AppendMany(context.Background(), nil, []*events.Event{{OrgID: "o", Type: typ}}); err == nil || !strings.Contains(err.Error(), "unknown event type") {
			t.Errorf("AppendMany(%s) = %v, want unknown event type", typ, err)
		}
	}
}

// TestPayloadFramesMatchSchema covers EVT-03 (WP-246): every phase 2 type
// has a frame in Payloads, a $defs entry of the same name and an if/then on
// its type that binds data to the entry and requires entity_id and
// entity_seq. The Go struct and the entry have the same fields and required
// fields (required: no omitempty); the entry allows further fields,
// personal fields (pii.IsPersonalKey) are surrogate references, and its
// examples decode into the struct without unknown fields.
func TestPayloadFramesMatchSchema(t *testing.T) {
	raw, err := os.ReadFile("../../../api/events.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		AllOf []struct {
			If struct {
				Properties struct {
					Type struct {
						Const string `json:"const"`
					} `json:"type"`
				} `json:"properties"`
				Required []string `json:"required"`
			} `json:"if"`
			Then struct {
				Required   []string `json:"required"`
				Properties struct {
					Data struct {
						Ref string `json:"$ref"`
					} `json:"data"`
				} `json:"properties"`
			} `json:"then"`
		} `json:"allOf"`
		Defs map[string]map[string]any `json:"$defs"`
	}
	if err = json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}

	bound := map[string]bool{}
	for _, cond := range schema.AllOf {
		typ := cond.If.Properties.Type.Const
		if bound[typ] {
			t.Errorf("%s: several if/then", typ)
		}
		bound[typ] = true
		if _, ok := events.Payloads[typ]; !ok {
			t.Errorf("if/then for %q, which has no frame in Payloads", typ)
		}
		if !slices.Equal(cond.If.Required, []string{"type"}) || !slices.Equal(cond.Then.Required, []string{"entity_id", "entity_seq"}) ||
			cond.Then.Properties.Data.Ref != "#/$defs/"+typ {
			t.Errorf("%s: if/then requires %v, then %v and binds data to %q", typ, cond.If.Required, cond.Then.Required, cond.Then.Properties.Data.Ref)
		}
	}
	for name := range schema.Defs {
		if _, ok := events.Payloads[name]; !ok && name != "pii" {
			t.Errorf("$defs entry %q is no frame of Payloads", name)
		}
	}

	piiType := reflect.TypeOf(events.PIIRef{})
	for typ, frame := range events.Payloads {
		if !events.IsType(typ) {
			t.Errorf("Payloads names %q, which is no event type", typ)
		}
		if !bound[typ] {
			t.Errorf("%s: no if/then", typ)
		}
		def, ok := schema.Defs[typ]
		if !ok {
			t.Errorf("%s: no $defs entry", typ)
			continue
		}
		if def["type"] != "object" || def["additionalProperties"] != true {
			t.Errorf("%s: $defs entry is no object open for further fields", typ)
		}
		if desc, _ := def["description"].(string); !strings.Contains(desc, "Origin: ") || !strings.Contains(desc, "Producer: WP-") {
			t.Errorf("%s: description names no origin and producer: %q", typ, desc)
		}
		props, _ := def["properties"].(map[string]any)
		var required []string
		for _, r := range asSlice(def["required"]) {
			required = append(required, r.(string))
		}

		goType := reflect.TypeOf(frame)
		var goRequired []string
		seen := map[string]bool{}
		for i := range goType.NumField() {
			field := goType.Field(i)
			name, opts, _ := strings.Cut(field.Tag.Get("json"), ",")
			seen[name] = true
			if !strings.Contains(opts, "omitempty") {
				goRequired = append(goRequired, name)
			}
			prop, isProp := props[name].(map[string]any)
			if !isProp {
				t.Errorf("%s.%s: Go field missing in the $defs entry", typ, name)
				continue
			}
			ft := field.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if pii.IsPersonalKey(name) && (prop["$ref"] != "#/$defs/pii" || ft != piiType) {
				t.Errorf("%s.%s: personal field is no surrogate reference ($ref #/$defs/pii, events.PIIRef)", typ, name)
			}
		}
		for name := range props {
			if !seen[name] {
				t.Errorf("%s.%s: schema property without a Go field", typ, name)
			}
		}
		sort.Strings(required)
		sort.Strings(goRequired)
		if !reflect.DeepEqual(required, goRequired) {
			t.Errorf("%s: schema requires %v, Go requires %v", typ, required, goRequired)
		}

		examples := asSlice(def["examples"])
		if len(examples) == 0 {
			t.Errorf("%s: no example", typ)
		}
		for _, example := range examples {
			body, _ := json.Marshal(example)
			dec := json.NewDecoder(bytes.NewReader(body))
			dec.DisallowUnknownFields()
			if err := dec.Decode(reflect.New(goType).Interface()); err != nil {
				t.Errorf("%s: example %s does not decode into %s: %v", typ, body, goType.Name(), err)
			}
			fields, _ := example.(map[string]any)
			for _, name := range required {
				if _, present := fields[name]; !present {
					t.Errorf("%s: example %s lacks the required %s", typ, body, name)
				}
			}
		}
	}
}

// TestNotifySubscriptionsUseRegisteredTypes covers EVT-03 (WP-246): every
// event of notify.Subscriptions is a registered event type.
func TestNotifySubscriptionsUseRegisteredTypes(t *testing.T) {
	for event := range notify.Subscriptions {
		if !events.IsType(event) {
			t.Errorf("notify.Subscriptions uses %q, which is no type of events.Types", event)
		}
	}
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}
