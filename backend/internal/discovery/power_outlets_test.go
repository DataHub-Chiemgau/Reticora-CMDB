package discovery

import (
	"encoding/json"
	"reflect"
	"testing"
)

// outletsJSON is the former outletsOf: the outlets of the power value read
// through a JSON round trip. It is the oracle outletsOf must match.
func outletsJSON(item *IngestItem) []DeviceOutlet {
	raw, ok := item.RawData["power"]
	if !ok || raw == nil {
		return nil
	}
	var p DevicePower
	b, err := json.Marshal(raw)
	if err != nil || json.Unmarshal(b, &p) != nil {
		return nil
	}
	return p.Outlets
}

// TestOutletsOfMatchesJSONRoundTrip pins the outlets of every shape the
// power value takes, nil and empty slices included, against the JSON
// round trip.
func TestOutletsOfMatchesJSONRoundTrip(t *testing.T) {
	outlets := []DeviceOutlet{{Name: "web01 PSU1", State: "on"}, {Name: "db01-B"}, {Name: "<&> \u2028é", State: "off"}}
	var nilPower *DevicePower
	for name, raw := range map[string]map[string]any{
		"no power":               {},
		"nil power":              {"power": nil},
		"typed nil pointer":      {"power": nilPower},
		"pointer nil outlets":    {"power": &DevicePower{DeviceType: "pdu", DrawWatts: 12.5}},
		"pointer empty outlets":  {"power": &DevicePower{DeviceType: "pdu", Outlets: []DeviceOutlet{}}},
		"pointer outlets":        {"power": &DevicePower{DeviceType: "pdu", DrawWatts: 230, Outlets: outlets}},
		"value nil outlets":      {"power": DevicePower{DeviceType: "ups"}},
		"value empty outlets":    {"power": DevicePower{Outlets: []DeviceOutlet{}}},
		"value outlets":          {"power": DevicePower{Outlets: outlets}},
		"queued map":             {"power": map[string]any{"device_type": "pdu", "outlets": []any{map[string]any{"name": "web01 PSU1", "state": "on"}}}},
		"queued map no outlets":  {"power": map[string]any{"device_type": "pdu"}},
		"queued map empty":       {"power": map[string]any{"outlets": []any{}}},
		"queued map bad outlets": {"power": map[string]any{"outlets": "web01"}},
		"string":                 {"power": "pdu"},
	} {
		item := IngestItem{RawData: raw}
		got, want := outletsOf(&item), outletsJSON(&item)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: outletsOf = %#v, want %#v", name, got, want)
		}
	}
}
