package admp

import (
	"reflect"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/admp/readonly"
)

// TestClientSourceMapsServiceAndProperties: a Dienst becomes kind
// (template name, else template:<id>, else unknown), template_id and the
// life cycle in RFC 3339 (E-253); the public properties become
// values.<key> = {value, label, order}, a property with isPublic false is
// dropped (WP-1073, WP-1074).
func TestClientSourceMapsServiceAndProperties(t *testing.T) {
	ms, sec := 1700000000000.0, 1700200000.0
	three := int64(3)
	d := serviceData(&readonly.Service{ID: 815, ConnectionID: 4711, TemplateID: &three, Booked: &ms, Provisioned: &sec})
	want := &ServiceData{ExternalID: "815", ConnectionExternalID: "4711", Kind: "template:3", Properties: map[string]any{
		"template_id": int64(3),
		"lifecycle":   map[string]any{"booked_at": "2023-11-14T22:13:20Z", "provisioned_at": "2023-11-17T05:46:40Z"},
	}}
	if !reflect.DeepEqual(d, want) {
		t.Errorf("service %+v, want %+v", d, want)
	}
	if d = serviceData(&readonly.Service{ID: 1, TemplateName: "FTTH 1000", TemplateID: &three}); d.Kind != "FTTH 1000" {
		t.Errorf("kind %q", d.Kind)
	}
	if d = serviceData(&readonly.Service{ID: 1}); d.Kind != "unknown" || d.ConnectionExternalID != "" {
		t.Errorf("bare service %+v", d)
	}
	one := int64(1)
	values := serviceValues([]readonly.ServiceProperty{
		{Key: "technology", Value: "FTTH", Label: "Technologie", Order: &one, Public: true},
		{Key: "note", Value: nil, Label: "Notiz", Public: true},
		{Key: "internal", Value: "x", Label: "Intern", Public: false},
	})
	wantValues := map[string]any{
		"technology": map[string]any{"value": "FTTH", "label": "Technologie", "order": int64(1)},
		"note":       map[string]any{"value": nil, "label": "Notiz"},
	}
	if !reflect.DeepEqual(values, wantValues) {
		t.Errorf("values %v, want %v", values, wantValues)
	}
}
