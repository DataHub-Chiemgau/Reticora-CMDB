package probe

import (
	"encoding/json"
	"maps"
)

// MaxResultValueBytes is the budget for the encoded values of one result.
// A result travels in one uplink message of at most MaxUplinkMessageBytes
// (256 KiB, backend probe.MaxMessageBytes); a larger one is quarantined and
// lost. The budget leaves room for the envelope, the labels and the other
// fields of the result.
const MaxResultValueBytes = 192 << 10

// valueNumberBytes is the most a float64 takes in JSON
// ("-1.2345678901234567e-308") plus the colon and the comma.
const valueNumberBytes = 26

// ValueBudget charges the values of a result against MaxResultValueBytes
// (or Limit, when set) so that per-row values of a large table (WP-996,
// WP-997) stop before the result outgrows one uplink message. The zero
// value is ready to use.
type ValueBudget struct {
	// Limit overrides MaxResultValueBytes (tests).
	Limit int
	used  int
}

// ValueBytes returns what the value under key adds to the encoded values.
func ValueBytes(key string) int {
	b, err := json.Marshal(key)
	if err != nil {
		return len(key)*6 + 2 + valueNumberBytes
	}
	return len(b) + valueNumberBytes
}

// Add copies src into dst when all of it fits into the rest of the budget
// and charges it; otherwise dst stays unchanged and Add reports false. A
// caller adds the values of one row at a time, so a row is either complete
// or absent.
func (b *ValueBudget) Add(dst, src map[string]float64) bool {
	n := 0
	for k := range src {
		n += ValueBytes(k)
	}
	limit := b.Limit
	if limit <= 0 {
		limit = MaxResultValueBytes
	}
	if b.used+n > limit {
		return false
	}
	b.used += n
	maps.Copy(dst, src)
	return true
}
