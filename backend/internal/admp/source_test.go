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

// TestClientSourceMapsConnection: the address is one line of street,
// number with suffix, postal code and city, empty parts left out; the form
// is business only when every holder is Gewerblich or Genossenschaft, else
// (also without holders) private; the carrier goes into carrier,
// carrier_short_name and carrier_number (WP-1075, E-252).
func TestClientSourceMapsConnection(t *testing.T) {
	for _, tc := range []struct {
		parts [5]string
		want  string
	}{
		{[5]string{"Bahnhofstraße", "12", "a", "83278", "Traunstein"}, "Bahnhofstraße 12a, 83278 Traunstein"},
		{[5]string{"Hauptstraße", "3", "", "83313", "Siegsdorf"}, "Hauptstraße 3, 83313 Siegsdorf"},
		{[5]string{"", "", "", "83313", "Siegsdorf"}, "83313 Siegsdorf"},
		{[5]string{"Seeweg", "", "", "", ""}, "Seeweg"},
		{[5]string{" ", "", "", "", ""}, ""},
	} {
		p := tc.parts
		if got := AddressLine(p[0], p[1], p[2], p[3], p[4]); got != tc.want {
			t.Errorf("%q: %q, want %q", p, got, tc.want)
		}
	}
	h := func(types ...string) []readonly.Holder {
		out := []readonly.Holder{}
		for _, typ := range types {
			out = append(out, readonly.Holder{Type: typ})
		}
		return out
	}
	for _, tc := range []struct {
		holders []readonly.Holder
		want    string
	}{
		{nil, AddressFormPrivate}, {h(), AddressFormPrivate}, {h("Gewerblich"), AddressFormBusiness},
		{h("Gewerblich", "Genossenschaft"), AddressFormBusiness}, {h("Gewerblich", "Privat"), AddressFormPrivate},
		{h("unbekannt"), AddressFormPrivate},
	} {
		if got := AddressFormOf(tc.holders); got != tc.want {
			t.Errorf("%v: %s, want %s", tc.holders, got, tc.want)
		}
	}
	number := 4711.0
	d := connectionData(&readonly.Connection{ID: 4711, CarrierID: 9, ContractID: "V-1", Street: "Weg", HouseNumber: "1",
		Holders: h("Gewerblich")}, &readonly.Organization{ID: 9, Name: "Netz AG", ShortName: "NA", Number: &number})
	want := &ConnectionData{ExternalID: "4711", Carrier: "Netz AG", ContractID: "V-1", AddressText: "Weg 1",
		Properties: map[string]any{"carrier_id": int64(9), "carrier_short_name": "NA", "carrier_number": 4711.0}}
	if !reflect.DeepEqual(d, want) {
		t.Errorf("business %+v, want %+v", d, want)
	}
	if d = connectionData(&readonly.Connection{ID: 1, CarrierID: 9, Street: "Weg"}, nil); d.AddressText != "" ||
		d.PrivateAddress != "Weg" || d.Carrier != "" {
		t.Errorf("private without carrier %+v", d)
	}
}
