package probewire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var registerKeys = []string{
	"icmp", "tcp", "http", "snmp_status", "snmp_if", "snmp_custom", "redfish", "bgp", "ospf", "isis",
	"bfd", "optical", "bng", "pon", "dns", "dhcp", "stamp", "twamp_control", "gnmi", "tr069", "usp",
	"rpki_rtr", "rpki_roa",
}

func repoFile(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestRegistryMatchesContract covers AK6: the embedded copy equals
// api/probe-types.json byte for byte.
func TestRegistryMatchesContract(t *testing.T) {
	if !bytes.Equal(contractJSON, repoFile(t, "api/probe-types.json")) {
		t.Fatal("edgecore/probewire/probe-types.json differs from api/probe-types.json; run go generate ./probewire in edgecore/")
	}
}

// TestRegistryComplete covers AK1, AK3 and AK6: 23 keys in contract order,
// every entry satisfies $defs/entry, every parameter schema is supported.
func TestRegistryComplete(t *testing.T) {
	if got := TypeKeys(); !slices.Equal(got, registerKeys) {
		t.Fatalf("register keys = %v, want %v", got, registerKeys)
	}
	var doc struct {
		Schema string                     `json:"$schema"`
		Types  map[string]json.RawMessage `json:"types"`
	}
	if err := json.Unmarshal(contractJSON, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Schema != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("$schema = %q", doc.Schema)
	}
	for _, key := range registerKeys {
		if errs := ValidateEntry(doc.Types[key]); len(errs) > 0 {
			t.Errorf("%s violates $defs/entry: %v", key, errs)
		}
		typ, _ := Lookup(key)
		if err := CheckSchema(typ.Params); err != nil {
			t.Errorf("%s params schema: %v", key, err)
		}
		wantStatus := TypePlanned
		if key == "icmp" || key == "tcp" {
			wantStatus = TypeAvailable
		}
		if typ.Status != wantStatus {
			t.Errorf("%s status = %s, want %s", key, typ.Status, wantStatus)
		}
	}
	// The entry schema itself rejects a broken entry.
	bad := json.RawMessage(`{"status":"beta","min_interval_s":0,"extra":1}`)
	if errs := ValidateEntry(bad); len(errs) < 3 {
		t.Fatalf("broken entry accepted: %v", errs)
	}
	if _, ok := Lookup("icmp_ping"); ok {
		t.Fatal("long form icmp_ping is registered")
	}
}

// TestEntryConsistency covers AK2 table driven over all types, plus entries
// that break each rule.
func TestEntryConsistency(t *testing.T) {
	for _, typ := range Types() {
		t.Run(typ.Key, func(t *testing.T) {
			if err := typ.Check(); err != nil {
				t.Fatal(err)
			}
		})
	}
	base, _ := Lookup("tcp")
	for name, mutate := range map[string]func(*Type){
		"interval min above default": func(x *Type) { x.MinIntervalS = x.DefaultIntervalS + 1 },
		"interval default above max": func(x *Type) { x.MaxIntervalS = x.DefaultIntervalS - 1 },
		"timeout min above default":  func(x *Type) { x.MinTimeoutS = x.DefaultTimeoutS + 1 },
		"timeout default above max":  func(x *Type) { x.MaxTimeoutS = x.DefaultTimeoutS - 1 },
		"timeout equals interval":    func(x *Type) { x.DefaultTimeoutS = x.DefaultIntervalS; x.MaxTimeoutS = x.DefaultIntervalS },
		"zero limit":                 func(x *Type) { x.MaxRatePerCollector = 0 },
		"negative limit":             func(x *Type) { x.MaxRows = -1 },
		"unknown status":             func(x *Type) { x.Status = "beta" },
	} {
		x := base
		mutate(&x)
		if err := x.Check(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func validSpec(typ Type) ProbeSpec {
	return ProbeSpec{
		ID: "p", Type: typ.Key, Target: "192.0.2.1",
		IntervalMS: typ.DefaultIntervalS * 1000, TimeoutMS: typ.DefaultTimeoutS * 1000,
		DownCount: typ.DefaultDownCount, UpCount: typ.DefaultUpCount,
	}
}

func hasField(errs []FieldError, field string) bool {
	return slices.ContainsFunc(errs, func(e FieldError) bool { return e.Field == field })
}

// TestValidateLimits covers AK4 and PRT-21 (limits test) for every type.
func TestValidateLimits(t *testing.T) {
	for _, typ := range Types() {
		t.Run(typ.Key, func(t *testing.T) {
			ok := validSpec(typ)
			errs := Validate(ok)
			if typ.Available() {
				if len(errs) > 0 {
					t.Fatalf("default spec rejected: %v", errs)
				}
			} else {
				if !hasField(errs, "type") || len(errs) != 1 {
					t.Fatalf("planned type: errs = %v, want only type", errs)
				}
			}
			cases := []struct {
				name   string
				mutate func(*ProbeSpec)
				field  string
			}{
				{"interval below minimum", func(s *ProbeSpec) { s.IntervalMS = typ.MinIntervalS*1000 - 1 }, "interval_s"},
				{"interval above maximum", func(s *ProbeSpec) { s.IntervalMS = typ.MaxIntervalS*1000 + 1 }, "interval_s"},
				{"timeout below minimum", func(s *ProbeSpec) { s.TimeoutMS = typ.MinTimeoutS*1000 - 1 }, "timeout_s"},
				{"timeout above maximum", func(s *ProbeSpec) {
					s.IntervalMS = typ.MaxIntervalS * 1000
					s.TimeoutMS = typ.MaxTimeoutS*1000 + 1
				}, "timeout_s"},
				{"down count 0", func(s *ProbeSpec) { s.DownCount = 0 }, "down_count"},
				{"down count 21", func(s *ProbeSpec) { s.DownCount = 21 }, "down_count"},
				{"up count 21", func(s *ProbeSpec) { s.UpCount = 21 }, "up_count"},
				{"unknown parameter", func(s *ProbeSpec) { s.Params = json.RawMessage(`{"no_such_param":1}`) }, "params.no_such_param"},
				{"unknown threshold", func(s *ProbeSpec) { s.Thresholds = json.RawMessage(`{"no_such_param":1}`) }, "thresholds.no_such_param"},
				{"params no object", func(s *ProbeSpec) { s.Params = json.RawMessage(`[1]`) }, "params"},
			}
			for _, c := range cases {
				s := validSpec(typ)
				c.mutate(&s)
				if errs := Validate(s); !hasField(errs, c.field) {
					t.Errorf("%s: errs = %v, want field %s", c.name, errs, c.field)
				}
			}
			// Timeout = interval violates only timeout < interval (PRT-02):
			// the value lies within both the interval and the timeout
			// range. Types whose maximum timeout is below the minimum
			// interval cannot violate the rule and are skipped.
			if v := min(typ.MaxTimeoutS, typ.MaxIntervalS); v >= typ.MinIntervalS && v >= typ.MinTimeoutS {
				s := validSpec(typ)
				s.IntervalMS, s.TimeoutMS = v*1000, v*1000
				var errs []FieldError
				for _, e := range Validate(s) {
					if e.Field != "type" { // planned types also report type
						errs = append(errs, e)
					}
				}
				if len(errs) != 1 || errs[0].Field != "timeout_s" || !strings.Contains(errs[0].Message, "below the interval") {
					t.Errorf("timeout equals interval (%d s): errs = %v, want only timeout_s below the interval", v, errs)
				}
			} else {
				t.Logf("timeout equals interval: max_timeout_s %d < min_interval_s %d, rule cannot be violated", typ.MaxTimeoutS, typ.MinIntervalS)
			}
			// The edges themselves are allowed.
			edge := validSpec(typ)
			edge.IntervalMS = typ.MaxIntervalS * 1000
			edge.TimeoutMS = typ.MaxTimeoutS * 1000
			edge.DownCount, edge.UpCount = MaxCount, MinCount
			if errs := Validate(edge); hasField(errs, "interval_s") || hasField(errs, "timeout_s") || hasField(errs, "down_count") {
				t.Errorf("upper edges rejected: %v", errs)
			}
		})
	}
	if errs := Validate(ProbeSpec{Type: "icmp_ping"}); !hasField(errs, "type") {
		t.Fatalf("unknown type: %v", errs)
	}
}

// TestIcmpDefaults covers AK2 for icmp (PRT-02, E-177).
func TestIcmpDefaults(t *testing.T) {
	typ, ok := Lookup("icmp")
	if !ok {
		t.Fatal("icmp missing")
	}
	if typ.DefaultIntervalS != 30 || typ.DefaultTimeoutS != 10 || typ.DefaultDownCount != 5 || typ.DefaultUpCount != 1 {
		t.Fatalf("defaults = %d/%d/%d/%d", typ.DefaultIntervalS, typ.DefaultTimeoutS, typ.DefaultDownCount, typ.DefaultUpCount)
	}
	if typ.MinIntervalS != 10 || typ.MaxIntervalS != 3600 || typ.MinTimeoutS != 1 || typ.MaxTimeoutS != 10 {
		t.Fatalf("limits = %d-%d s, %d-%d s", typ.MinIntervalS, typ.MaxIntervalS, typ.MinTimeoutS, typ.MaxTimeoutS)
	}
	if v, ok := typ.ParamDefault("loss_warn"); !ok || v != 0.5 {
		t.Fatalf("loss_warn default = %v, %v", v, ok)
	}
	if v, ok := typ.ParamDefault("rtt_warn_ms"); ok {
		t.Fatalf("rtt_warn_ms has a default: %v", v)
	}
	if v, ok := typ.ParamDefault("count"); !ok || v != 3.0 {
		t.Fatalf("count default = %v", v)
	}
	if v, ok := typ.ParamDefault("spacing_ms"); !ok || v != 1000.0 {
		t.Fatalf("spacing_ms default = %v", v)
	}
	spec := validSpec(typ)
	spec.Params = json.RawMessage(`{"count":6}`)
	if errs := Validate(spec); !hasField(errs, "params.count") {
		t.Fatalf("count 6: %v", errs)
	}
	spec.Params = json.RawMessage(`{"count":5,"spacing_ms":1000,"rtt_warn_ms":200,"loss_warn":0.25}`)
	if errs := Validate(spec); len(errs) > 0 {
		t.Fatalf("valid params: %v", errs)
	}
	// Interval 10 s: timeout at most 9 s (timeout < interval).
	spec.Params = nil
	spec.IntervalMS, spec.TimeoutMS = 10000, 10000
	if errs := Validate(spec); !hasField(errs, "timeout_s") {
		t.Fatalf("timeout = interval at 10 s: %v", errs)
	}
	spec.TimeoutMS = 9000
	if errs := Validate(spec); len(errs) > 0 {
		t.Fatalf("9 s / 10 s: %v", errs)
	}
}

var docRow = regexp.MustCompile("^\\| `([a-z0-9_]+)` \\| (planned|available) \\| (\\d+) / (\\d+) / (\\d+) \\| (\\d+) / (\\d+) / (\\d+) \\| (\\d+) / (\\d+) \\| (\\d+) \\| (\\d+) \\| (\\d+) \\| (\\d+) \\| (\\d+) \\| (\\d+) \\| ([^|]+) \\| ([^|]+) \\|$")

// TestDocsTypeTableMatchesContract covers AK6: the type table of
// docs/betrieb/monitoring-probes.md agrees with the contract.
func TestDocsTypeTableMatchesContract(t *testing.T) {
	doc := string(repoFile(t, "docs/betrieb/monitoring-probes.md"))
	_, rest, ok := strings.Cut(doc, "<!-- probe-types:begin -->")
	table, _, ok2 := strings.Cut(rest, "<!-- probe-types:end -->")
	if !ok || !ok2 {
		t.Fatal("type table markers missing")
	}
	var keys []string
	for _, line := range strings.Split(strings.TrimSpace(table), "\n")[2:] {
		m := docRow.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			t.Fatalf("unparsable row: %s", line)
		}
		typ, ok := Lookup(m[1])
		if !ok {
			t.Fatalf("doc row %s has no register entry", m[1])
		}
		keys = append(keys, m[1])
		n := func(i int) int64 { v, _ := strconv.ParseInt(m[i], 10, 64); return v }
		list := func(v []string) string {
			if len(v) == 0 {
				return "–"
			}
			return strings.Join(v, ", ")
		}
		got := fmt.Sprint(m[2], n(3), n(4), n(5), n(6), n(7), n(8), n(9), n(10), n(11), n(12), n(13), n(14), n(15), n(16),
			strings.TrimSpace(m[17]), strings.TrimSpace(m[18]))
		want := fmt.Sprint(typ.Status, typ.DefaultIntervalS, typ.MinIntervalS, typ.MaxIntervalS,
			typ.DefaultTimeoutS, typ.MinTimeoutS, typ.MaxTimeoutS, int64(typ.DefaultDownCount), int64(typ.DefaultUpCount),
			int64(typ.MaxParallelPerTarget), int64(typ.MaxParallelPerCollector), int64(typ.MaxRatePerCollector),
			int64(typ.MaxProbesPerTarget), typ.MaxPayloadBytes, typ.MaxRows, list(typ.CredentialNamespaces), list(typ.Capabilities))
		if got != want {
			t.Errorf("doc row %s = %s, contract %s", m[1], got, want)
		}
	}
	if !slices.Equal(keys, TypeKeys()) {
		t.Fatalf("doc keys = %v, want %v", keys, TypeKeys())
	}
}
