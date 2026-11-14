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

// availableKeys are the types whose work package has set them available.
var availableKeys = []string{
	"icmp", "tcp", "dns", "http", "snmp_status", "snmp_if", "snmp_custom", "bgp", "bng", "pon", "bfd", "optical", "redfish", "ospf", "isis", "tr069", "rpki_rtr", "dhcp",
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
		if slices.Contains(availableKeys, key) {
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
		Params: requiredParams(typ),
	}
}

// selectionParams are valid params of the types with a selection rule.
var selectionParams = map[string]string{"bfd": `{"all":true}`, "optical": `{"all":true}`,
	"tr069": `{"acs_url":"https://acs.example.org:7557","inform_interval_s":300}`}

// requiredParams returns params with a valid value for every required
// parameter of the type (nil without required parameters).
func requiredParams(typ Type) json.RawMessage {
	// Types whose selection the schema cannot require (exactly one of two
	// parameters, checked by paramChecks).
	if p, ok := selectionParams[typ.Key]; ok {
		return json.RawMessage(p)
	}
	var s schema
	if err := json.Unmarshal(typ.Params, &s); err != nil || len(s.Required) == 0 {
		return nil
	}
	out := map[string]any{}
	for _, name := range s.Required {
		p := s.Properties[name]
		switch {
		case p == nil:
		case len(p.Enum) > 0:
			out[name] = p.Enum[0]
		case p.Type == "integer" || p.Type == "number":
			v := 1.0
			if p.Minimum != nil {
				v = *p.Minimum
			}
			out[name] = v
		case p.Type == "boolean":
			out[name] = false
		case p.Type == "array":
			out[name] = []any{}
		case name == "url":
			out[name] = "https://example.org/health"
		default:
			out[name] = "example.org"
		}
	}
	b, _ := json.Marshal(out)
	return b
}

func hasField(errs []FieldError, field string) bool {
	return slices.ContainsFunc(errs, func(e FieldError) bool { return e.Field == field })
}

// TestValidateLimits covers AK4 and PRT-21 (limits test) for every type.
func TestValidateLimits(t *testing.T) {
	for _, typ := range Types() {
		t.Run(typ.Key, func(t *testing.T) {
			ok := validSpec(typ)
			errs := Validate(&ok)
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
				if errs := Validate(&s); !hasField(errs, c.field) {
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
				for _, e := range Validate(&s) {
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
			if errs := Validate(&edge); hasField(errs, "interval_s") || hasField(errs, "timeout_s") || hasField(errs, "down_count") {
				t.Errorf("upper edges rejected: %v", errs)
			}
		})
	}
	if errs := Validate(&ProbeSpec{Type: "icmp_ping"}); !hasField(errs, "type") {
		t.Fatalf("unknown type: %v", errs)
	}
	typeLimitCases(t)
}

// customMetrics returns snmp_custom params with n distinct scalar metrics.
func customMetrics(n int) string {
	items := make([]string, n)
	for i := range items {
		items[i] = fmt.Sprintf(`{"name":"m%d","oid":"1.3.6.1.4.1.9999.%d.0"}`, i, i+1)
	}
	return `{"metrics":[` + strings.Join(items, ",") + `]}`
}

// TestTemplatesMatchSchema covers WP-997 AK4: every template of a type is
// a valid params object of its type; snmp_custom carries ups_rfc1628 and
// pdu_apc_rpdu2.
func TestTemplatesMatchSchema(t *testing.T) {
	n := 0
	for _, typ := range Types() {
		for name, raw := range typ.Templates {
			n++
			s := validSpec(typ)
			s.Params = raw
			if errs := Validate(&s); len(errs) > 0 {
				t.Errorf("%s template %s: %v", typ.Key, name, errs)
			}
		}
	}
	custom, _ := Lookup("snmp_custom")
	for _, name := range []string{"ups_rfc1628", "pdu_apc_rpdu2"} {
		if _, ok := custom.Templates[name]; !ok {
			t.Errorf("snmp_custom lacks template %s", name)
		}
	}
	if n < 2 {
		t.Fatalf("%d templates", n)
	}
}

// typeLimitCases are the type-specific limits of TestValidateLimits: the
// rows and thresholds of snmp_if (WP-996 AK2, AK6) and the metrics of
// snmp_custom (WP-997 AK1, AK5).
func typeLimitCases(t *testing.T) {
	t.Helper()
	for name, c := range map[string]struct {
		typ, params, thresholds, field string
		intervalMS, timeoutMS          int64
	}{
		"snmp_if max_interfaces 1025": {typ: "snmp_if", params: `{"max_interfaces":1025}`, field: "params.max_interfaces"},
		"snmp_if interval 59 s":       {typ: "snmp_if", intervalMS: 59_000, field: "interval_s"},
		"snmp_if timeout 31 s":        {typ: "snmp_if", timeoutMS: 31_000, field: "timeout_s"},
		"snmp_if util_warn 1.5":       {typ: "snmp_if", params: `{"util_warn":1.5}`, field: "params.util_warn"},
		"snmp_if util_crit below warn": {typ: "snmp_if", params: `{"util_warn":0.8}`, thresholds: `{"util_crit":0.7}`,
			field: "thresholds.util_crit"},
		"snmp_if bad regex":         {typ: "snmp_if", params: `{"name_regex":"(Gi"}`, field: "params.name_regex"},
		"snmp_custom 65 metrics":    {typ: "snmp_custom", params: customMetrics(65), field: "params.metrics"},
		"snmp_custom interval 29 s": {typ: "snmp_custom", intervalMS: 29_000, field: "interval_s"},
		"snmp_custom timeout 16 s":  {typ: "snmp_custom", timeoutMS: 16_000, field: "timeout_s"},
		"snmp_custom duplicate name": {typ: "snmp_custom",
			params: `{"metrics":[{"name":"a","oid":"1.3.6.1.2.1.1.7.0"},{"name":"a","oid":"1.3.6.1.2.1.1.3.0"}]}`,
			field:  "params.metrics[1].name"},
		"snmp_custom oid and column": {typ: "snmp_custom",
			params: `{"metrics":[{"name":"a","oid":"1.3.6.1.2.1.1.7.0","column":"1.3.6.1.2.1.2.2.1.10"}]}`,
			field:  "params.metrics[0].oid"},
		"snmp_custom scale 0": {typ: "snmp_custom", params: `{"metrics":[{"name":"a","oid":"1.3.6.1.2.1.1.7.0","scale":0}]}`,
			field: "params.metrics[0].scale"},
	} {
		typ, _ := Lookup(c.typ)
		s := validSpec(typ)
		if c.params != "" {
			s.Params = json.RawMessage(c.params)
		}
		if c.thresholds != "" {
			s.Thresholds = json.RawMessage(c.thresholds)
		}
		if c.intervalMS > 0 {
			s.IntervalMS = c.intervalMS
		}
		if c.timeoutMS > 0 {
			s.IntervalMS, s.TimeoutMS = 300_000, c.timeoutMS
		}
		if errs := Validate(&s); !hasField(errs, c.field) {
			t.Errorf("%s: errs = %v, want field %s", name, errs, c.field)
		}
	}
	typ, _ := Lookup("snmp_if")
	if typ.MaxRows != 1024 || typ.MinIntervalS != 60 || typ.MaxTimeoutS != 30 {
		t.Errorf("snmp_if limits = rows %d, interval ≥ %d s, timeout ≤ %d s", typ.MaxRows, typ.MinIntervalS, typ.MaxTimeoutS)
	}
	edge := validSpec(typ)
	edge.Params = json.RawMessage(`{"max_interfaces":1024,"util_warn":0.7,"util_crit":0.9,"name_regex":"^Gi"}`)
	if errs := Validate(&edge); len(errs) > 0 {
		t.Errorf("snmp_if edges rejected: %v", errs)
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
	if errs := Validate(&spec); !hasField(errs, "params.count") {
		t.Fatalf("count 6: %v", errs)
	}
	spec.Params = json.RawMessage(`{"count":5,"spacing_ms":1000,"rtt_warn_ms":200,"loss_warn":0.25}`)
	if errs := Validate(&spec); len(errs) > 0 {
		t.Fatalf("valid params: %v", errs)
	}
	// Interval 10 s: timeout at most 9 s (timeout < interval).
	spec.Params = nil
	spec.IntervalMS, spec.TimeoutMS = 10000, 10000
	if errs := Validate(&spec); !hasField(errs, "timeout_s") {
		t.Fatalf("timeout = interval at 10 s: %v", errs)
	}
	spec.TimeoutMS = 9000
	if errs := Validate(&spec); len(errs) > 0 {
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

// TestTCPLimits covers WP-970 AK1 and AK5 (PRT-03): defaults 60 s / 5 s,
// interval ≥ 10 s, timeout ≤ 30 s and below the interval, 2 parallel
// checks per target, the parameter schema with port required, send at
// most 256 bytes and expect as RE2 expression.
func TestTCPLimits(t *testing.T) {
	typ, ok := Lookup("tcp")
	if !ok || !typ.Available() {
		t.Fatal("tcp missing or not available")
	}
	if typ.DefaultIntervalS != 60 || typ.DefaultTimeoutS != 5 || typ.MinIntervalS != 10 || typ.MaxTimeoutS != 30 || typ.MaxParallelPerTarget != 2 {
		t.Fatalf("tcp limits = %+v", typ)
	}
	if typ.MaxPayloadBytes != 1024 {
		t.Fatalf("max_payload_bytes = %d, want 1024 (read cap)", typ.MaxPayloadBytes)
	}
	if v, ok := typ.ParamDefault("family"); !ok || v != "auto" {
		t.Fatalf("family default = %v", v)
	}
	base := ProbeSpec{ID: "p", Type: "tcp", Target: "192.0.2.1", IntervalMS: 60_000, TimeoutMS: 5_000, DownCount: 3, UpCount: 1}
	cases := []struct {
		name   string
		mutate func(*ProbeSpec)
		fields []string
	}{
		{"valid", func(s *ProbeSpec) {
			s.Params = json.RawMessage(`{"port":25,"send":"QUIT\\r\\n","expect":"^220 ","family":"v6","rtt_warn_ms":200}`)
			s.Thresholds = json.RawMessage(`{"rtt_warn_ms":150}`)
		}, nil},
		{"port missing", func(s *ProbeSpec) {}, []string{"params.port"}},
		{"port missing with other params", func(s *ProbeSpec) { s.Params = json.RawMessage(`{"expect":"x"}`) }, []string{"params.port"}},
		{"port 0", func(s *ProbeSpec) { s.Params = json.RawMessage(`{"port":0}`) }, []string{"params.port"}},
		{"port 65536", func(s *ProbeSpec) { s.Params = json.RawMessage(`{"port":65536}`) }, []string{"params.port"}},
		{"interval 9 s", func(s *ProbeSpec) {
			s.Params = json.RawMessage(`{"port":22}`)
			s.IntervalMS = 9_000
			s.TimeoutMS = 1_000
		}, []string{"interval_s"}},
		{"timeout 31 s", func(s *ProbeSpec) { s.Params = json.RawMessage(`{"port":22}`); s.TimeoutMS = 31_000 }, []string{"timeout_s"}},
		{"timeout = interval", func(s *ProbeSpec) {
			s.Params = json.RawMessage(`{"port":22}`)
			s.IntervalMS = 10_000
			s.TimeoutMS = 10_000
		}, []string{"timeout_s"}},
		{"send 257 bytes", func(s *ProbeSpec) {
			s.Params = json.RawMessage(`{"port":22,"send":"` + strings.Repeat("a", 257) + `"}`)
		}, []string{"params.send"}},
		{"send 129 two-byte runes", func(s *ProbeSpec) {
			s.Params = json.RawMessage(`{"port":22,"send":"` + strings.Repeat("ä", 129) + `"}`)
		}, []string{"params.send"}},
		{"send 129 escapes = 129 bytes", func(s *ProbeSpec) {
			s.Params = json.RawMessage(`{"port":25,"send":"` + strings.Repeat(`\\n`, 129) + `"}`)
		}, nil},
		{"send 256 escapes = 256 bytes", func(s *ProbeSpec) {
			s.Params = json.RawMessage(`{"port":25,"send":"` + strings.Repeat(`\\r`, 256) + `"}`)
		}, nil},
		{"send 257 escapes = 257 bytes", func(s *ProbeSpec) {
			s.Params = json.RawMessage(`{"port":25,"send":"` + strings.Repeat(`\\n`, 257) + `"}`)
		}, []string{"params.send"}},
		{"expect no RE2", func(s *ProbeSpec) { s.Params = json.RawMessage(`{"port":22,"expect":"(?<=a)b"}`) }, []string{"params.expect"}},
		{"expect 1025 characters", func(s *ProbeSpec) {
			s.Params = json.RawMessage(`{"port":22,"expect":"` + strings.Repeat("a", 1025) + `"}`)
		}, []string{"params.expect"}},
		{"family unknown", func(s *ProbeSpec) { s.Params = json.RawMessage(`{"port":22,"family":"v5"}`) }, []string{"params.family"}},
		{"old parameter connect_warn_ms", func(s *ProbeSpec) { s.Params = json.RawMessage(`{"port":22,"connect_warn_ms":5}`) }, []string{"params.connect_warn_ms"}},
		{"threshold without port", func(s *ProbeSpec) {
			s.Params = json.RawMessage(`{"port":22}`)
			s.Thresholds = json.RawMessage(`{"rtt_warn_ms":0}`)
		}, []string{"thresholds.rtt_warn_ms"}},
	}
	for _, c := range cases {
		spec := base
		c.mutate(&spec)
		var got []string
		for _, e := range Validate(&spec) {
			got = append(got, e.Field)
		}
		if !slices.Equal(got, c.fields) {
			t.Errorf("%s: fields = %v, want %v", c.name, got, c.fields)
		}
	}
	if got := UnescapeSend(`A\r\n\t\\\x`); got != "A\r\n\t\\\\x" {
		t.Fatalf("UnescapeSend = %q", got)
	}
}

// TestICMPLimits covers WP-969 AK1 to AK3 (PRT-02): parameter schema of
// icmp (count 1-5, size 0-1472, spacing_ms 200-1000, family auto|v4|v6,
// rtt_warn_ms, loss_warn 0-1) and the field errors of Validate for
// interval 10 s-1 h, timeout 1-10 s and below the interval (also timeout
// = interval), (count - 1) x spacing_ms >= timeout and a family that does
// not match an address literal.
func TestICMPLimits(t *testing.T) {
	typ, ok := Lookup("icmp")
	if !ok || !typ.Available() {
		t.Fatal("icmp missing or not available")
	}
	for name, want := range map[string]any{"count": 3.0, "size": 56.0, "spacing_ms": 1000.0, "family": "auto", "loss_warn": 0.5} {
		if v, ok := typ.ParamDefault(name); !ok || v != want {
			t.Errorf("default of %s = %v (%v), want %v", name, v, ok, want)
		}
	}
	if typ.MaxPayloadBytes != 1472 {
		t.Errorf("max_payload_bytes = %d, want 1472", typ.MaxPayloadBytes)
	}
	base := ProbeSpec{ID: "p", Type: "icmp", Target: "192.0.2.1", IntervalMS: 30_000, TimeoutMS: 10_000, DownCount: 5, UpCount: 1}
	cases := []struct {
		name   string
		mutate func(*ProbeSpec)
		fields []string
	}{
		{"defaults", func(*ProbeSpec) {}, nil},
		{"all parameters", func(s *ProbeSpec) {
			s.Params = json.RawMessage(`{"count":5,"size":1472,"spacing_ms":200,"family":"v4","rtt_warn_ms":100,"loss_warn":1}`)
		}, nil},
		{"interval 10 s, timeout 9 s", func(s *ProbeSpec) { s.IntervalMS, s.TimeoutMS = 10_000, 9_000 }, nil},
		{"interval 1 h", func(s *ProbeSpec) { s.IntervalMS = 3_600_000 }, nil},
		{"interval 9.999 s", func(s *ProbeSpec) { s.IntervalMS, s.TimeoutMS = 9_999, 5_000 }, []string{"interval_s"}},
		{"interval above 1 h", func(s *ProbeSpec) { s.IntervalMS = 3_600_001 }, []string{"interval_s"}},
		{"timeout 1 s with one packet", func(s *ProbeSpec) {
			s.TimeoutMS = 1_000
			s.Params = json.RawMessage(`{"count":1}`)
		}, nil},
		{"timeout below 1 s", func(s *ProbeSpec) {
			s.TimeoutMS = 999
			s.Params = json.RawMessage(`{"count":1}`)
		}, []string{"timeout_s"}},
		{"timeout above 10 s", func(s *ProbeSpec) { s.TimeoutMS = 10_001 }, []string{"timeout_s"}},
		{"timeout = interval", func(s *ProbeSpec) { s.IntervalMS, s.TimeoutMS = 10_000, 10_000 }, []string{"timeout_s"}},
		{"timeout above interval", func(s *ProbeSpec) { s.IntervalMS, s.TimeoutMS = 10_000, 10_001 }, []string{"timeout_s"}},
		{"defaults do not fit 2 s", func(s *ProbeSpec) { s.TimeoutMS = 2_000 }, []string{"params.count"}},
		{"packets fit 2 s", func(s *ProbeSpec) {
			s.TimeoutMS = 2_000
			s.Params = json.RawMessage(`{"count":5,"spacing_ms":400}`)
		}, nil},
		{"packets exactly the timeout", func(s *ProbeSpec) {
			s.TimeoutMS = 2_000
			s.Params = json.RawMessage(`{"count":5,"spacing_ms":500}`)
		}, []string{"params.spacing_ms"}},
		{"count 0", func(s *ProbeSpec) { s.Params = json.RawMessage(`{"count":0}`) }, []string{"params.count"}},
		{"count 6", func(s *ProbeSpec) { s.Params = json.RawMessage(`{"count":6}`) }, []string{"params.count"}},
		{"size -1", func(s *ProbeSpec) { s.Params = json.RawMessage(`{"size":-1}`) }, []string{"params.size"}},
		{"size 1473", func(s *ProbeSpec) { s.Params = json.RawMessage(`{"size":1473}`) }, []string{"params.size"}},
		{"spacing 199", func(s *ProbeSpec) { s.Params = json.RawMessage(`{"spacing_ms":199}`) }, []string{"params.spacing_ms"}},
		{"spacing 1001", func(s *ProbeSpec) { s.Params = json.RawMessage(`{"spacing_ms":1001}`) }, []string{"params.spacing_ms"}},
		{"family unknown", func(s *ProbeSpec) { s.Params = json.RawMessage(`{"family":"ipx"}`) }, []string{"params.family"}},
		{"family v6 for an IPv4 literal", func(s *ProbeSpec) { s.Params = json.RawMessage(`{"family":"v6"}`) }, []string{"params.family"}},
		{"family v4 for an IPv6 literal", func(s *ProbeSpec) {
			s.Target = "2001:db8::1"
			s.Params = json.RawMessage(`{"family":"v4"}`)
		}, []string{"params.family"}},
		{"family v6 for an IPv6 literal", func(s *ProbeSpec) {
			s.Target = "2001:db8::1"
			s.Params = json.RawMessage(`{"family":"v6"}`)
		}, nil},
		{"family v4 for a host name", func(s *ProbeSpec) {
			s.Target = "router.example"
			s.Params = json.RawMessage(`{"family":"v4"}`)
		}, nil},
		{"loss_warn above 1", func(s *ProbeSpec) { s.Params = json.RawMessage(`{"loss_warn":1.5}`) }, []string{"params.loss_warn"}},
		{"old parameter payload_bytes", func(s *ProbeSpec) { s.Params = json.RawMessage(`{"payload_bytes":56}`) }, []string{"params.payload_bytes"}},
	}
	for _, c := range cases {
		spec := base
		c.mutate(&spec)
		var got []string
		for _, e := range Validate(&spec) {
			got = append(got, e.Field)
		}
		if !slices.Equal(got, c.fields) {
			t.Errorf("%s: fields = %v, want %v", c.name, got, c.fields)
		}
	}
}

// TestHTTPLimits covers WP-971 AK1 and AK7 (PRT-04): defaults 60 s / 10 s,
// interval >= 30 s, timeout <= 60 s and below the interval, 2 parallel
// checks per target, url required (http or https with a host), only GET and
// HEAD, body_regex as RE2 and with GET only, expected_status as codes and
// ranges, follow_redirects 0 to 5, max_body at most 1 MiB and only the
// headers Accept, User-Agent and Host.
func TestHTTPLimits(t *testing.T) {
	typ, ok := Lookup("http")
	if !ok || !typ.Available() {
		t.Fatal("http missing or not available")
	}
	if typ.DefaultIntervalS != 60 || typ.DefaultTimeoutS != 10 || typ.MinIntervalS != 30 || typ.MaxTimeoutS != 60 ||
		typ.MaxParallelPerTarget != 2 || !slices.Equal(typ.CredentialNamespaces, []string{"http"}) {
		t.Fatalf("http limits = %+v", typ)
	}
	for name, want := range map[string]any{"method": "GET", "max_body": float64(65536), "follow_redirects": float64(0),
		"cert_warn_days": float64(21)} {
		if v, ok := typ.ParamDefault(name); !ok || v != want {
			t.Errorf("%s default = %v, want %v", name, v, want)
		}
	}
	if v, ok := typ.ParamDefault("expected_status"); !ok || fmt.Sprint(v) != "[200-399]" {
		t.Errorf("expected_status default = %v", v)
	}
	base := ProbeSpec{ID: "p", Type: "http", Target: "192.0.2.1", IntervalMS: 60_000, TimeoutMS: 10_000, DownCount: 3, UpCount: 1}
	cases := []struct {
		name   string
		params string
		fields []string
	}{
		{"valid", `{"url":"https://example.org/health","method":"GET","expected_status":["200","301-302"],"body_regex":"^ok",
			"max_body":1048576,"follow_redirects":5,"headers":{"Accept":"text/plain","User-Agent":"r","Host":"x"},"cert_warn_days":30,
			"rtt_warn_ms":500}`, nil},
		{"head", `{"url":"http://192.0.2.1:8080/","method":"HEAD"}`, nil},
		{"url missing", `{}`, []string{"params.url"}},
		{"url ftp", `{"url":"ftp://example.org/"}`, []string{"params.url"}},
		{"url without host", `{"url":"https:///x"}`, []string{"params.url"}},
		{"url with user info", `{"url":"https://u:p@example.org/"}`, []string{"params.url"}},
		{"method POST", `{"url":"https://example.org/","method":"POST"}`, []string{"params.method"}},
		{"method PUT", `{"url":"https://example.org/","method":"PUT"}`, []string{"params.method"}},
		{"body_regex invalid", `{"url":"https://example.org/","body_regex":"("}`, []string{"params.body_regex"}},
		{"body_regex with HEAD", `{"url":"https://example.org/","method":"HEAD","body_regex":"ok"}`, []string{"params.body_regex"}},
		{"body_regex 1025", `{"url":"https://example.org/","body_regex":"` + strings.Repeat("a", 1025) + `"}`, []string{"params.body_regex"}},
		{"status 600", `{"url":"https://example.org/","expected_status":["600"]}`, []string{"params.expected_status[0]"}},
		{"status range reversed", `{"url":"https://example.org/","expected_status":["200","299-200"]}`, []string{"params.expected_status[1]"}},
		{"status as number", `{"url":"https://example.org/","expected_status":[200]}`, []string{"params.expected_status[0]"}},
		{"redirects 6", `{"url":"https://example.org/","follow_redirects":6}`, []string{"params.follow_redirects"}},
		{"max_body 1 MiB + 1", `{"url":"https://example.org/","max_body":1048577}`, []string{"params.max_body"}},
		{"header Authorization", `{"url":"https://example.org/","headers":{"Authorization":"Bearer x"}}`, []string{"params.headers.Authorization"}},
		{"header Cookie", `{"url":"https://example.org/","headers":{"Cookie":"a=b"}}`, []string{"params.headers.Cookie"}},
	}
	for _, c := range cases {
		s := base
		s.Params = json.RawMessage(c.params)
		var fields []string
		for _, e := range Validate(&s) {
			fields = append(fields, e.Field)
		}
		if !slices.Equal(fields, c.fields) {
			t.Errorf("%s: fields = %v, want %v", c.name, fields, c.fields)
		}
	}
	params := json.RawMessage(`{"url":"https://example.org/"}`)
	for name, spec := range map[string]ProbeSpec{
		"interval 29 s":      {IntervalMS: 29_000, TimeoutMS: 10_000},
		"timeout 61 s":       {IntervalMS: 120_000, TimeoutMS: 61_000},
		"timeout = interval": {IntervalMS: 30_000, TimeoutMS: 30_000},
	} {
		s := base
		s.IntervalMS, s.TimeoutMS, s.Params = spec.IntervalMS, spec.TimeoutMS, params
		if errs := Validate(&s); len(errs) != 1 {
			t.Errorf("%s: errs = %v, want one", name, errs)
		}
	}
}

// TestTypesDeclareMetricNames covers the value names of the types (WP-976
// AK3): icmp, http and dns declare theirs, an unknown type none, and
// MetricNames returns a copy.
func TestTypesDeclareMetricNames(t *testing.T) {
	for typ, name := range map[string]string{"icmp": "jitter_ms", "http": "ttfb_ms", "dns": "probe_dns_rcode",
		"redfish": "probe_redfish_temperature_celsius", "ospf": "probe_ospf_neighbor_state", "isis": "probe_isis_adjacency_state",
		"tr069": "probe_tr069_last_inform_age_seconds", "rpki_rtr": "probe_rpki_vrps", "dhcp": "probe_dhcp_response_seconds",
		"bfd": "probe_bfd_session_state", "optical": "probe_optical_rx_dbm", "snmp_status": "probe_snmp_reboot",
		"snmp_if": "probe_if_in_bps", "snmp_custom": "probe_snmp_custom",
		"bgp": "probe_bgp_peer_state", "bng": "probe_bng_sessions",
		"pon": "probe_pon_onu_status_change"} {
		if !DeclaresMetric(typ, name) || !slices.Contains(MetricNames(typ), name) {
			t.Errorf("%s does not declare %s", typ, name)
		}
	}
	if DeclaresMetric("icmp", "ttfb_ms") || DeclaresMetric("tcp", "rtt_min_ms") || DeclaresMetric("unknown", "jitter_ms") {
		t.Error("a name is declared for a type that does not produce it")
	}
	if names := MetricNames("unknown"); len(names) != 0 {
		t.Errorf("unknown type declares %v", names)
	}
	names := MetricNames("icmp")
	names[0] = "changed"
	if !DeclaresMetric("icmp", "rtt_min_ms") || MetricNames("icmp")[0] != "rtt_min_ms" {
		t.Error("MetricNames shares the register")
	}
}
