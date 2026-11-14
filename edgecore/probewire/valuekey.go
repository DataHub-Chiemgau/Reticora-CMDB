package probewire

import (
	"errors"
	"sort"
	"strings"
)

// Labeled values (WP-1003, WP-1004): a value of Result.Values that belongs
// to one row of a table (a BFD session, an interface) carries its labels in
// its key, in the form of the Prometheus exposition format:
//
//	probe_bfd_session_state{peer="192.0.2.1"}
//	probe_optical_rx_dbm{ifIndex="12",ifName="Te1/0/1"}
//
// The labels are sorted by name; a backslash and a double quote in a label
// value are escaped with a backslash. A key without labels is the plain
// value name. The metric bridge (WP-976) splits a key with ParseValueKey
// into the value name and its labels.

// ValueKey returns the key of the value name with labels.
func ValueKey(name string, labels map[string]string) string {
	if len(labels) == 0 {
		return name
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(name)
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteString(`="`)
		b.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(labels[k]))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

// ErrValueKey is returned by ParseValueKey for a malformed key.
var ErrValueKey = errors.New("probewire: malformed value key")

// ParseValueKey splits a key of ValueKey into the value name and its labels
// (nil for a key without labels).
func ParseValueKey(key string) (name string, labels map[string]string, err error) {
	name, rest, found := strings.Cut(key, "{")
	if !found {
		return key, nil, nil
	}
	if name == "" || !strings.HasSuffix(rest, "}") {
		return "", nil, ErrValueKey
	}
	rest = strings.TrimSuffix(rest, "}")
	labels = map[string]string{}
	for rest != "" {
		label, after, ok := strings.Cut(rest, `="`)
		if !ok || label == "" {
			return "", nil, ErrValueKey
		}
		var value strings.Builder
		i := 0
		closed := false
		for i < len(after) {
			c := after[i]
			if c == '\\' && i+1 < len(after) {
				value.WriteByte(after[i+1])
				i += 2
				continue
			}
			if c == '"' {
				closed = true
				break
			}
			value.WriteByte(c)
			i++
		}
		if !closed {
			return "", nil, ErrValueKey
		}
		labels[label] = value.String()
		rest = after[i+1:]
		if rest != "" {
			if rest[0] != ',' {
				return "", nil, ErrValueKey
			}
			rest = rest[1:]
		}
	}
	return name, labels, nil
}
