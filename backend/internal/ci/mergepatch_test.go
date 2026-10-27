package ci

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"testing"
)

// TestMergePatchRFC7396 covers WP-057 (CI-04, OVR-01) with the test cases
// of RFC 7396 appendix A that apply to object targets, plus nested deletes.
func TestMergePatchRFC7396(t *testing.T) {
	for _, c := range []struct{ target, patch, want string }{
		{`{"a":"b"}`, `{"a":"c"}`, `{"a":"c"}`},
		{`{"a":"b"}`, `{"b":"c"}`, `{"a":"b","b":"c"}`},
		{`{"a":"b"}`, `{"a":null}`, `{}`},
		{`{"a":"b","b":"c"}`, `{"a":null}`, `{"b":"c"}`},
		{`{"a":["b"]}`, `{"a":"c"}`, `{"a":"c"}`},
		{`{"a":"c"}`, `{"a":["b"]}`, `{"a":["b"]}`},
		{`{"a":{"b":"c"}}`, `{"a":{"b":"d","c":null}}`, `{"a":{"b":"d"}}`},
		{`{"a":[{"b":"c"}]}`, `{"a":[1]}`, `{"a":[1]}`},
		{`{"e":null}`, `{"a":1}`, `{"e":null,"a":1}`},
		{`{"a":"foo"}`, `{"a":{"bb":{"ccc":null}}}`, `{"a":{"bb":{}}}`},
		{`{}`, `{"a":{"bb":{"ccc":null}}}`, `{"a":{"bb":{}}}`},
		{`{"net":{"vlan":10,"mtu":1500},"rack":"R1"}`, `{"net":{"mtu":null,"lag":true}}`, `{"net":{"vlan":10,"lag":true},"rack":"R1"}`},
	} {
		target, patch, want := decode(t, c.target), decode(t, c.patch), decode(t, c.want)
		before, _ := json.Marshal(target)
		got := MergePatch(target, patch)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("MergePatch(%s, %s) = %v, want %s", c.target, c.patch, got, c.want)
		}
		if after, _ := json.Marshal(target); string(after) != string(before) {
			t.Errorf("MergePatch modified its target: %s -> %s", before, after)
		}
	}
}

// TestUpdateRequestDecodesNullMembers covers WP-476 (CI-04, F-080): the
// PATCH body is decoded presence-aware. An absent member stays nil
// (unchanged), a null member is recorded in Nulls and a nullable one is set
// to "" (cleared); null on a required member is a violation of the field; a
// nested null belongs to the attribute merge patch; unknown members are
// refused as before.
func TestUpdateRequestDecodesNullMembers(t *testing.T) {
	var req UpdateRequest
	if err := json.Unmarshal([]byte(`{"hostname":null,"model":"m2","asset_id":null,"attributes":{"rack":null}}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.Hostname == nil || *req.Hostname != "" || req.AssetID == nil || *req.AssetID != "" {
		t.Errorf("null members hostname %v, asset_id %v: want both cleared (\"\")", req.Hostname, req.AssetID)
	}
	if req.Model == nil || *req.Model != "m2" {
		t.Errorf("model = %v, want m2", req.Model)
	}
	if req.FQDN != nil || req.Name != nil || req.Status != nil || req.FirmwareVersion != nil {
		t.Errorf("absent members are set: fqdn %v name %v status %v firmware_version %v", req.FQDN, req.Name, req.Status, req.FirmwareVersion)
	}
	if want := []string{"asset_id", "hostname"}; !reflect.DeepEqual(req.Nulls, want) {
		t.Errorf("Nulls = %v, want %v", req.Nulls, want)
	}
	if !reflect.DeepEqual(req.Attributes, map[string]any{"rack": nil}) {
		t.Errorf("attributes = %v, want the nested null kept for the merge patch", req.Attributes)
	}
	if err := req.nullViolations(); err != nil {
		t.Errorf("nullViolations of nullable members: %v", err)
	}
	if !req.isNull("hostname") || req.isNull("model") {
		t.Error("isNull: want hostname null and model not")
	}

	// A second decode into the same value starts from the new body.
	if err := json.Unmarshal([]byte(`{"name":null,"status":null,"last_seen_at":null,"fqdn":"a.example"}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.Name != nil || req.Status != nil || req.Hostname != nil {
		t.Errorf("required null members must stay nil and old members must be gone: name %v status %v hostname %v", req.Name, req.Status, req.Hostname)
	}
	var verr *ValidationError
	if err := req.nullViolations(); !errors.As(err, &verr) || !reflect.DeepEqual(verr.Violations, []Violation{
		{Field: "last_seen_at", Detail: "must not be null"}, {Field: "name", Detail: "must not be null"}, {Field: "status", Detail: "must not be null"},
	}) {
		t.Errorf("nullViolations = %v, want last_seen_at, name and status", err)
	}

	if err := json.Unmarshal([]byte(`{"hostnme":null}`), &req); err == nil {
		t.Error("an unknown member is accepted, want an error as with api.ReadJSON")
	}
}

func TestChangedKeys(t *testing.T) {
	before := map[string]any{"a": 1.0, "b": map[string]any{"x": 1.0}, "c": "gone"}
	after := map[string]any{"a": 1.0, "b": map[string]any{"x": 2.0}, "d": true}
	got := changedKeys(before, after)
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"b", "c", "d"}) {
		t.Errorf("changedKeys = %v", got)
	}
}

func decode(t *testing.T, raw string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
