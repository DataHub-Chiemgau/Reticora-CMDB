package ci

import (
	"encoding/json"
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
