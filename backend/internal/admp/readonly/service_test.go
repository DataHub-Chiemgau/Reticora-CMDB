package readonly

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The public properties of a service (ADM-05, E-253, WP-1074) against the
// fixture of getV1Dienst:idPropertiesPublic.

// TestListServicePropertiesDecodesFixture: every kind of SerializeableData
// (number, string, boolean, null, object, array) decodes; label is label,
// else title; order and timestamp as sent; isPublic is kept for the
// caller; description and input are dropped.
func TestListServicePropertiesDecodesFixture(t *testing.T) {
	m := newFixtureADMP(t)
	props, err := docClient(t, m, "", validLogin, nil).ListServiceProperties(context.Background(), 815)
	if err != nil {
		t.Fatal(err)
	}
	if len(props) != 7 {
		t.Fatalf("%d properties, want 7", len(props))
	}
	byKey := map[string]ServiceProperty{}
	for _, p := range props {
		byKey[p.Key] = p
	}
	order := func(v int64) *int64 { return &v }
	for key, want := range map[string]ServiceProperty{
		"bandwidth_down": {Key: "bandwidth_down", Value: float64(1000), Label: "Bandbreite down", Order: order(2), Public: true},
		"technology":     {Key: "technology", Value: "FTTH", Label: "Technologie", Order: order(1), Public: true},
		"static_ip":      {Key: "static_ip", Value: true, Label: "Feste IP", Public: true},
		"note":           {Key: "note", Value: nil, Label: "Notiz", Order: order(4), Public: true},
		"vlan":           {Key: "vlan", Value: map[string]any{"outer": float64(7), "inner": float64(100)}, Label: "VLAN", Order: order(5), Public: true},
		"ports":          {Key: "ports", Value: []any{float64(1), "two", false}, Label: "Ports", Order: order(6), Public: true},
		"internal":       {Key: "internal", Value: "internal-canary", Label: "Intern", Order: order(7)},
	} {
		got := byKey[key]
		got.Timestamp = nil
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %+v, want %+v", key, got, want)
		}
	}
	if ts := byKey["technology"].Timestamp; ts == nil || *ts != 1700000001 {
		t.Errorf("timestamp %v", ts)
	}
	raw, _ := json.Marshal(props)
	for _, dropped := range []string{"description-canary", "input-canary"} {
		if strings.Contains(string(raw), dropped) {
			t.Errorf("the result carries %s", dropped)
		}
	}
	typ := reflect.TypeFor[ServiceProperty]()
	for i := range typ.NumField() {
		if name := typ.Field(i).Name; name == "Description" || name == "Input" || name == "Service" {
			t.Errorf("ServiceProperty has the member %s", name)
		}
	}
}

// TestListServicePropertiesUsesPublicPath: the properties are read from
// /properties/public only; /properties (all properties) is never called
// and is outside the allowlist.
func TestListServicePropertiesUsesPublicPath(t *testing.T) {
	m := newFixtureADMP(t)
	c := docClient(t, m, "", validLogin, nil)
	ctx := context.Background()
	if _, err := c.ListServiceProperties(ctx, 815); err != nil {
		t.Fatal(err)
	}
	if n := m.count("GET " + fixtureBase + "/V1/dienst/815/properties/public"); n != 1 {
		t.Errorf("public reads %d", n)
	}
	if err := c.getJSON(ctx, "/V1/dienst/815/properties", nil, nil); ClassOf(err) != ClassPathNotAllowed {
		t.Errorf("GET /properties: %v, want path_not_allowed", err)
	}
	for key := range m.hits {
		if strings.HasSuffix(key, "/properties") {
			t.Errorf("the mock saw %s", key)
		}
	}
	m.set("getV1Dienst:idPropertiesPublic", fixture{Status: 200, Body: json.RawMessage(`[{"value":1}]`)})
	if _, err := c.ListServiceProperties(ctx, 815); ClassOf(err) != ClassDecode {
		t.Errorf("property without key: %v, want decode", err)
	}
}
