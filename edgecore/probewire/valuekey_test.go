package probewire

import (
	"errors"
	"maps"
	"testing"
)

// TestValueKeyRoundTrip covers the key form of labeled values (WP-1003,
// WP-1004): sorted labels, escaped quotes and backslashes, and the parse
// back into name and labels.
func TestValueKeyRoundTrip(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
		key    string
	}{
		{"probe_bfd_truncated", nil, "probe_bfd_truncated"},
		{"probe_bfd_session_state", map[string]string{"peer": "192.0.2.1"}, `probe_bfd_session_state{peer="192.0.2.1"}`},
		{"probe_optical_rx_dbm", map[string]string{"ifName": "Te1/0/1", "ifIndex": "12"},
			`probe_optical_rx_dbm{ifIndex="12",ifName="Te1/0/1"}`},
		{"x", map[string]string{"a": `say "hi" \ ok`, "b": ""}, `x{a="say \"hi\" \\ ok",b=""}`},
		{"x", map[string]string{"a": "1,b=\"2\"}"}, `x{a="1,b=\"2\"}"}`},
	}
	for _, c := range cases {
		key := ValueKey(c.name, c.labels)
		if key != c.key {
			t.Errorf("ValueKey(%s, %v) = %s, want %s", c.name, c.labels, key, c.key)
		}
		name, labels, err := ParseValueKey(key)
		if err != nil || name != c.name || !maps.Equal(labels, c.labels) {
			t.Errorf("ParseValueKey(%s) = %s %v %v", key, name, labels, err)
		}
	}
	for _, bad := range []string{`x{`, `{a="1"}`, `x{a=1}`, `x{a="1"`, `x{a="1"b="2"}`, `x{="1"}`} {
		if _, _, err := ParseValueKey(bad); !errors.Is(err, ErrValueKey) {
			t.Errorf("ParseValueKey(%s) = %v, want ErrValueKey", bad, err)
		}
	}
}
