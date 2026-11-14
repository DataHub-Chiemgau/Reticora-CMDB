package probewire

import (
	"bytes"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
)

//go:generate cp ../../api/probe-types.json probe-types.json

// contractJSON is the embedded copy of api/probe-types.json (PRT-01). A
// conformance test keeps it byte-identical with the contract.
//
//go:embed probe-types.json
var contractJSON []byte

// Type statuses of the register.
const (
	TypeAvailable = "available"
	TypePlanned   = "planned"
)

// Probe-wide count limits (E-154): down and up count of every probe.
const (
	MinCount = 1
	MaxCount = 20
)

// Type is one entry of the probe type register: status, defaults, system
// limits (E-154), credential namespaces, collector capabilities (PRT-18) and
// the JSON Schema of the parameters. Times are seconds, the rate is checks
// per second.
type Type struct {
	Key                     string          `json:"-"`
	Status                  string          `json:"status"`
	MinIntervalS            int64           `json:"min_interval_s"`
	DefaultIntervalS        int64           `json:"default_interval_s"`
	MaxIntervalS            int64           `json:"max_interval_s"`
	MinTimeoutS             int64           `json:"min_timeout_s"`
	DefaultTimeoutS         int64           `json:"default_timeout_s"`
	MaxTimeoutS             int64           `json:"max_timeout_s"`
	DefaultDownCount        int             `json:"default_down_count"`
	DefaultUpCount          int             `json:"default_up_count"`
	MaxParallelPerTarget    int             `json:"max_parallel_per_target"`
	MaxParallelPerCollector int             `json:"max_parallel_per_collector"`
	MaxRatePerCollector     int             `json:"max_rate_per_collector"`
	MaxProbesPerTarget      int             `json:"max_probes_per_target"`
	MaxPayloadBytes         int64           `json:"max_payload_bytes"`
	MaxRows                 int64           `json:"max_rows"`
	CredentialNamespaces    []string        `json:"credential_namespaces"`
	Capabilities            []string        `json:"capabilities"`
	Params                  json.RawMessage `json:"params"`
	// Templates are preset parameter sets by name (snmp_custom, WP-997);
	// each satisfies Params.
	Templates map[string]json.RawMessage `json:"templates,omitempty"`
}

// Available reports whether probes of the type may be created.
func (t *Type) Available() bool { return t.Status == TypeAvailable }

// ParamDefault returns the default of a parameter from the parameter schema.
func (t *Type) ParamDefault(name string) (any, bool) {
	var s schema
	if err := json.Unmarshal(t.Params, &s); err != nil {
		return nil, false
	}
	p, ok := s.Properties[name]
	if !ok || p.Default == nil {
		return nil, false
	}
	var v any
	if err := json.Unmarshal(p.Default, &v); err != nil {
		return nil, false
	}
	return v, true
}

// Check checks the consistency rules of an entry (WP-934 AK2): the
// interval and timeout ranges are ordered, the default timeout lies below
// the default interval and every limit is positive.
func (t *Type) Check() error {
	var errs []string
	for name, v := range map[string]int64{
		"min_interval_s": t.MinIntervalS, "default_interval_s": t.DefaultIntervalS, "max_interval_s": t.MaxIntervalS,
		"min_timeout_s": t.MinTimeoutS, "default_timeout_s": t.DefaultTimeoutS, "max_timeout_s": t.MaxTimeoutS,
		"default_down_count": int64(t.DefaultDownCount), "default_up_count": int64(t.DefaultUpCount),
		"max_parallel_per_target": int64(t.MaxParallelPerTarget), "max_parallel_per_collector": int64(t.MaxParallelPerCollector),
		"max_rate_per_collector": int64(t.MaxRatePerCollector), "max_probes_per_target": int64(t.MaxProbesPerTarget),
		"max_payload_bytes": t.MaxPayloadBytes, "max_rows": t.MaxRows,
	} {
		if v <= 0 {
			errs = append(errs, name+" must be > 0")
		}
	}
	if t.MinIntervalS > t.DefaultIntervalS || t.DefaultIntervalS > t.MaxIntervalS {
		errs = append(errs, "min_interval_s <= default_interval_s <= max_interval_s violated")
	}
	if t.MinTimeoutS > t.DefaultTimeoutS || t.DefaultTimeoutS > t.MaxTimeoutS {
		errs = append(errs, "min_timeout_s <= default_timeout_s <= max_timeout_s violated")
	}
	if t.DefaultTimeoutS >= t.DefaultIntervalS {
		errs = append(errs, "default_timeout_s must be below default_interval_s")
	}
	if t.DefaultDownCount > MaxCount || t.DefaultUpCount > MaxCount {
		errs = append(errs, fmt.Sprintf("default counts must be at most %d", MaxCount))
	}
	if t.Status != TypeAvailable && t.Status != TypePlanned {
		errs = append(errs, "status must be planned or available")
	}
	var s schema
	if err := json.Unmarshal(t.Params, &s); err != nil || s.Type != "object" {
		errs = append(errs, "params must be a JSON Schema of type object")
	}
	if len(errs) == 0 {
		return nil
	}
	sort.Strings(errs)
	return fmt.Errorf("probe type %s: %s", t.Key, strings.Join(errs, "; "))
}

// register is the decoded contract.
type register struct {
	types []Type
	byKey map[string]Type
	entry json.RawMessage
}

var (
	registerOnce sync.Once
	registerVal  register
	registerErr  error
)

func load() (register, error) {
	registerOnce.Do(func() { registerVal, registerErr = parseContract(contractJSON) })
	return registerVal, registerErr
}

func mustLoad() register {
	r, err := load()
	if err != nil {
		panic(err)
	}
	return r
}

// parseContract decodes the contract, keeping the order of the types.
func parseContract(data []byte) (register, error) {
	var doc struct {
		Defs struct {
			Entry json.RawMessage `json:"entry"`
		} `json:"$defs"`
		Types json.RawMessage `json:"types"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return register{}, fmt.Errorf("probewire: probe-types.json: %w", err)
	}
	keys, err := objectKeys(doc.Types)
	if err != nil {
		return register{}, fmt.Errorf("probewire: probe-types.json types: %w", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(doc.Types, &raw); err != nil {
		return register{}, err
	}
	r := register{byKey: map[string]Type{}, entry: doc.Defs.Entry}
	for _, k := range keys {
		var t Type
		if err := json.Unmarshal(raw[k], &t); err != nil {
			return register{}, fmt.Errorf("probewire: probe type %s: %w", k, err)
		}
		t.Key = k
		r.types = append(r.types, t)
		r.byKey[k] = t
	}
	return r, nil
}

// objectKeys returns the keys of a JSON object in document order.
func objectKeys(obj json.RawMessage) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(obj))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("not an object")
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		keys = append(keys, tok.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

// Types returns every registered probe type in contract order.
func Types() []Type {
	return slices.Clone(mustLoad().types)
}

// TypeKeys returns the keys of every registered probe type.
func TypeKeys() []string {
	types := mustLoad().types
	out := make([]string, 0, len(types))
	for i := range types {
		out = append(out, types[i].Key)
	}
	return out
}

// Lookup returns the type with the key.
func Lookup(key string) (Type, bool) {
	t, ok := mustLoad().byKey[key]
	return t, ok
}

// typeMetrics are the names of Result.values a probe type declares (WP-976,
// E-179): the server stores only these as metric samples of the probe's
// CI. The executors of the collector (collector/probe) produce them.
var typeMetrics = map[string][]string{
	"icmp": {"rtt_min_ms", "rtt_max_ms", "jitter_ms"},
	"http": {"dns_ms", "connect_ms", "tls_ms", "ttfb_ms", "status_code", "cert_expiry_s"},
	"dns":  {"probe_dns_response_seconds", "probe_dns_rcode", "probe_dns_answer_count", "probe_dns_soa_serial"},
	"redfish": {"probe_redfish_health", "probe_redfish_temperature_celsius", "probe_redfish_fan_rpm",
		"probe_redfish_psu_input_watts", "probe_redfish_psu_state", "probe_redfish_truncated"},
	"snmp_status": {"probe_snmp_reboot", "probe_snmp_uptime_seconds", "probe_snmp_response_seconds"},
	"snmp_if": {"probe_if_oper_status", "probe_if_admin_status", "probe_if_in_bps", "probe_if_out_bps",
		"probe_if_util_ratio", "probe_if_errors_per_s", "probe_if_discards_per_s", "probe_if_truncated",
		"probe_if_interfaces", "probe_if_critical_unseen"},
	"snmp_custom": {"probe_snmp_custom", "probe_snmp_custom_truncated"},
	"bgp": {"probe_bgp_peer_state", "probe_bgp_established_seconds", "probe_bgp_prefixes_received",
		"probe_bgp_updates_in_total", "probe_bgp_updates_out_total", "probe_bgp_truncated", "probe_bgp_peers", "probe_bgp_peers_established",
		"probe_bgp_peers_unseen", "probe_bgp_session_resets"},
	"bng": {"probe_bng_sessions", "probe_bng_truncated"},
	"ospf": {"probe_ospf_neighbor_state", "probe_ospf_neighbor_events_total", "probe_ospf_neighbors",
		"probe_ospf_neighbors_full", "probe_ospf_truncated", "probe_ospf_neighbors_unseen"},
	"isis": {"probe_isis_adjacency_state", "probe_isis_adjacencies", "probe_isis_adjacencies_up",
		"probe_isis_truncated", "probe_isis_adjacencies_unseen"},
	"tr069": {"probe_tr069_devices", "probe_tr069_devices_stale", "probe_tr069_last_inform_age_seconds",
		"probe_tr069_parameter", "probe_tr069_truncated"},
	"rpki_rtr": {"probe_rpki_rtr_up", "probe_rpki_serial", "probe_rpki_vrps", "probe_rpki_data_age_seconds",
		"probe_rpki_protocol_version"},
	"bfd": {"probe_bfd_session_state", "probe_bfd_diag", "probe_bfd_truncated", "probe_bfd_sessions",
		"probe_bfd_sessions_down", "probe_bfd_sessions_unseen"},
	"optical": {"probe_optical_rx_dbm", "probe_optical_tx_dbm", "probe_optical_temperature_celsius",
		"probe_optical_bias_ma", "probe_optical_voltage_volts", "probe_optical_los",
		"probe_optical_sensors_unavailable", "probe_optical_sensors_unmapped", "probe_optical_truncated",
		"probe_optical_interfaces", "probe_optical_interfaces_not_found"},
}

// MetricNames returns the value names the type declares (none for an
// unknown type).
func MetricNames(typeKey string) []string {
	return slices.Clone(typeMetrics[typeKey])
}

// DeclaresMetric reports whether the type declares the value name.
func DeclaresMetric(typeKey, name string) bool {
	return slices.Contains(typeMetrics[typeKey], name)
}

// FieldError names the field a validation error refers to (type,
// interval_s, timeout_s, down_count, up_count, params.<name>,
// thresholds.<name>), so that the API can answer 422 per field.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e FieldError) Error() string { return e.Field + ": " + e.Message }

// Validate checks a probe specification against the register (WP-934 AK4):
// type known and available, interval and timeout within the type limits,
// timeout below the interval, down and up count between 1 and 20, and
// params and thresholds against the parameter schema of the type. It
// reports every violation; an unknown type stops the checks after the type.
func Validate(spec *ProbeSpec) []FieldError {
	t, ok := Lookup(spec.Type)
	if !ok {
		return []FieldError{{Field: "type", Message: fmt.Sprintf("unknown probe type %q", spec.Type)}}
	}
	var errs []FieldError
	add := func(field, format string, args ...any) {
		errs = append(errs, FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
	}
	if !t.Available() {
		add("type", "probe type %s is planned and cannot be used yet", t.Key)
	}
	switch {
	case spec.IntervalMS < t.MinIntervalS*1000:
		add("interval_s", "interval must be at least %d s", t.MinIntervalS)
	case spec.IntervalMS > t.MaxIntervalS*1000:
		add("interval_s", "interval must be at most %d s", t.MaxIntervalS)
	}
	switch {
	case spec.TimeoutMS < t.MinTimeoutS*1000:
		add("timeout_s", "timeout must be at least %d s", t.MinTimeoutS)
	case spec.TimeoutMS > t.MaxTimeoutS*1000:
		add("timeout_s", "timeout must be at most %d s", t.MaxTimeoutS)
	case spec.TimeoutMS >= spec.IntervalMS:
		add("timeout_s", "timeout must be below the interval")
	}
	if spec.DownCount < MinCount || spec.DownCount > MaxCount {
		add("down_count", "down_count must be between %d and %d", MinCount, MaxCount)
	}
	if spec.UpCount < MinCount || spec.UpCount > MaxCount {
		add("up_count", "up_count must be between %d and %d", MinCount, MaxCount)
	}
	var s schema
	if err := json.Unmarshal(t.Params, &s); err != nil {
		add("type", "probe type %s has an invalid parameter schema", t.Key)
		return errs
	}
	// Required parameters apply to params only, also when params are
	// absent; thresholds name a subset of the parameters.
	errs = append(errs, validateObject(&s, spec.Params, "params", len(s.Required) > 0)...)
	thresholds := s
	thresholds.Required = nil
	errs = append(errs, validateObject(&thresholds, spec.Thresholds, "thresholds", false)...)
	if check := paramChecks[t.Key]; check != nil {
		params, ok := decodeObject(spec.Params)
		if !ok && len(bytes.TrimSpace(spec.Params)) == 0 {
			// Absent params take the defaults; the checks across fields
			// (icmp: packets within the timeout) apply to them as well.
			params, ok = map[string]any{}, true
		}
		if ok {
			// A field the schema already rejected is reported once.
			for _, e := range check(spec, params) {
				if !slices.ContainsFunc(errs, func(x FieldError) bool { return x.Field == e.Field }) {
					errs = append(errs, e)
				}
			}
		}
	}
	return errs
}

// paramChecks are the checks of a type's parameters that the schema subset
// cannot express (WP-970: send in bytes, expect as RE2 expression;
// WP-1010: qname as DNS name; WP-969: icmp packets within the timeout and
// family against the target). They run on decodable params and report
// field errors.
var paramChecks = map[string]func(spec *ProbeSpec, params map[string]any) []FieldError{
	"icmp":        checkICMPParams,
	"snmp_status": func(_ *ProbeSpec, params map[string]any) []FieldError { return checkSNMPStatusParams(params) },
	"snmp_if":     checkSNMPIfParams,
	"snmp_custom": func(_ *ProbeSpec, params map[string]any) []FieldError { return checkSNMPCustomParams(params) },
	"bfd":         checkBFDParams,
	"optical":     checkOpticalParams,
	"redfish":     checkRedfishParams,
	"bgp":         func(_ *ProbeSpec, params map[string]any) []FieldError { return checkBGPParams(params) },
	"bng":         func(_ *ProbeSpec, params map[string]any) []FieldError { return checkBNGParams(params) },
	"ospf":        func(_ *ProbeSpec, params map[string]any) []FieldError { return checkOSPFParams(params) },
	"isis":        func(_ *ProbeSpec, params map[string]any) []FieldError { return checkISISParams(params) },
	"tr069":       func(_ *ProbeSpec, params map[string]any) []FieldError { return checkTR069Params(params) },
	"tcp":         func(_ *ProbeSpec, params map[string]any) []FieldError { return checkTCPParams(params) },
	"dns":         func(_ *ProbeSpec, params map[string]any) []FieldError { return checkDNSParams(params) },
	"http":        func(_ *ProbeSpec, params map[string]any) []FieldError { return checkHTTPParams(params) },
}

// MaxSNMPRegex is the longest regex of an snmp_status check (PRT-05).
const MaxSNMPRegex = 256

// snmpOID is an OID in dotted form with at least two arcs, optionally
// with a leading dot.
var snmpOID = regexp.MustCompile(`^\.?[0-9]+(\.[0-9]+)+$`)

// checkSNMPStatusParams checks the OID checks of the snmp_status type
// (PRT-05, WP-995 AK3): every oid is an OID in dotted form, lt and gt take
// a number, in a non-empty comma-separated list and regex an RE2
// expression of at most 256 characters.
func checkSNMPStatusParams(params map[string]any) []FieldError {
	items, _ := params["checks"].([]any)
	var errs []FieldError
	for i, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		prefix := fmt.Sprintf("params.checks[%d]", i)
		if oid, ok := obj["oid"].(string); ok && !snmpOID.MatchString(strings.TrimSpace(oid)) {
			errs = append(errs, FieldError{Field: prefix + ".oid", Message: "must be an OID in dotted form"})
		}
		value, _ := obj["value"].(string)
		switch obj["op"] {
		case "lt", "gt":
			if _, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err != nil {
				errs = append(errs, FieldError{Field: prefix + ".value", Message: "must be a number for lt and gt"})
			}
		case "in":
			if strings.Trim(value, " ,") == "" {
				errs = append(errs, FieldError{Field: prefix + ".value", Message: "must list at least one value (comma separated)"})
			}
		case "regex":
			if len([]rune(value)) > MaxSNMPRegex {
				errs = append(errs, FieldError{Field: prefix + ".value", Message: fmt.Sprintf("regex must be at most %d characters", MaxSNMPRegex)})
			} else if _, err := regexp.Compile(value); err != nil {
				errs = append(errs, FieldError{Field: prefix + ".value", Message: "must be a valid RE2 regular expression"})
			}
		}
	}
	return errs
}

// checkSNMPIfParams checks the selection and thresholds of the snmp_if
// type (PRT-06, WP-996 AK2): name_regex and alias_regex are RE2
// expressions and util_crit lies above util_warn (thresholds before
// params, like the executor reads them).
func checkSNMPIfParams(spec *ProbeSpec, params map[string]any) []FieldError {
	var errs []FieldError
	for _, name := range []string{"name_regex", "alias_regex"} {
		if re, ok := params[name].(string); ok {
			if _, err := regexp.Compile(re); err != nil {
				errs = append(errs, FieldError{Field: "params." + name, Message: "must be a valid RE2 regular expression"})
			}
		}
	}
	var thresholds map[string]any
	if spec != nil {
		thresholds, _ = decodeObject(spec.Thresholds)
	}
	level := func(name string) (float64, string, bool) {
		if v, ok := numberOf(thresholds[name]); ok {
			return v, "thresholds." + name, true
		}
		if v, ok := numberOf(params[name]); ok {
			return v, "params." + name, true
		}
		return 0, "", false
	}
	warn, _, hasWarn := level("util_warn")
	crit, critField, hasCrit := level("util_crit")
	if hasWarn && hasCrit && crit <= warn {
		errs = append(errs, FieldError{Field: critField, Message: "util_crit must be above util_warn"})
	}
	return errs
}

// snmpMetricName and snmpIndexLabel are the rules for the name and the
// index label of an snmp_custom metric (MON-03, WP-997 AK1).
var (
	snmpMetricName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	snmpIndexLabel = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
)

// checkSNMPCustomParams checks the metrics of the snmp_custom type
// (MON-03, WP-997 AK1): a unique name after its rule, exactly one of oid
// and column in dotted form, an index label after its rule (not metric or
// unit) and only with a column, and crit beyond warn in the direction.
func checkSNMPCustomParams(params map[string]any) []FieldError {
	items, _ := params["metrics"].([]any)
	var errs []FieldError
	seen := map[string]bool{}
	for i, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		prefix := fmt.Sprintf("params.metrics[%d]", i)
		add := func(field, msg string) { errs = append(errs, FieldError{Field: prefix + field, Message: msg}) }
		if name, ok := obj["name"].(string); ok {
			switch {
			case !snmpMetricName.MatchString(name):
				add(".name", "must match ^[a-z][a-z0-9_]{0,62}$")
			case seen[name]:
				add(".name", "is used twice")
			}
			seen[name] = true
		}
		oid, hasOID := obj["oid"].(string)
		column, hasColumn := obj["column"].(string)
		switch {
		case hasOID == hasColumn:
			add(".oid", "set exactly one of oid and column")
		case hasOID && !snmpOID.MatchString(strings.TrimSpace(oid)):
			add(".oid", "must be an OID in dotted form")
		case hasColumn && !snmpOID.MatchString(strings.TrimSpace(column)):
			add(".column", "must be an OID in dotted form")
		}
		if label, ok := obj["index_label"].(string); ok {
			switch {
			case !hasColumn:
				add(".index_label", "is allowed with column only")
			case !snmpIndexLabel.MatchString(label) || label == "metric" || label == "unit":
				add(".index_label", "must match ^[a-z][a-z0-9_]{0,31}$ and not be metric or unit")
			}
		}
		warn, hasWarn := numberOf(obj["warn"])
		crit, hasCrit := numberOf(obj["crit"])
		below := obj["direction"] == "below"
		if hasWarn && hasCrit && ((below && crit > warn) || (!below && crit < warn)) {
			add(".crit", "must not lie before warn in the direction of the metric")
		}
	}
	return errs
}

// checkBFDParams checks the selection of the bfd type (PRT-08, WP-1003
// AK3): exactly one of sessions (1 to 1000 neighbor addresses, IPv4 or
// IPv6) and all: true; both or neither is a field error on
// params.sessions.
func checkBFDParams(_ *ProbeSpec, params map[string]any) []FieldError {
	sessions, hasSessions := params["sessions"].([]any)
	all, _ := params["all"].(bool)
	switch {
	case hasSessions && all:
		return []FieldError{{Field: "params.sessions", Message: "set either sessions or all, not both"}}
	case !hasSessions && !all:
		return []FieldError{{Field: "params.sessions", Message: "set sessions (1 to 1000 neighbor addresses) or all: true"}}
	case hasSessions && len(sessions) == 0:
		return []FieldError{{Field: "params.sessions", Message: "must list 1 to 1000 neighbor addresses"}}
	}
	var errs []FieldError
	for i, item := range sessions {
		if str, ok := item.(string); ok {
			if _, err := netip.ParseAddr(strings.TrimSpace(str)); err != nil {
				errs = append(errs, FieldError{Field: fmt.Sprintf("params.sessions[%d]", i), Message: "must be an IPv4 or IPv6 address"})
			}
		}
	}
	return errs
}

// checkRedfishParams checks the parameters of the redfish type (MON-03,
// WP-998 AK5): system_id and every chassis_ids entry is a plain Redfish
// resource Id (letters, digits, '.', '_', '-', not "." or ".."), and a
// critical temperature lies above its warning temperature. The levels are
// merged like the executor reads them (thresholds before params).
func checkRedfishParams(spec *ProbeSpec, params map[string]any) []FieldError {
	var errs []FieldError
	if id, ok := params["system_id"].(string); ok && !redfishID(id) {
		errs = append(errs, FieldError{Field: "params.system_id", Message: "must be a Redfish resource Id (letters, digits, '.', '_', '-')"})
	}
	if ids, ok := params["chassis_ids"].([]any); ok {
		for i, item := range ids {
			if id, ok := item.(string); ok && !redfishID(id) {
				errs = append(errs, FieldError{Field: fmt.Sprintf("params.chassis_ids[%d]", i), Message: "must be a Redfish resource Id (letters, digits, '.', '_', '-')"})
			}
		}
	}
	var thresholds map[string]any
	if spec != nil {
		thresholds, _ = decodeObject(spec.Thresholds)
	}
	level := func(name string) (float64, string, bool) {
		if v, ok := numberOf(thresholds[name]); ok {
			return v, "thresholds." + name, true
		}
		if v, ok := numberOf(params[name]); ok {
			return v, "params." + name, true
		}
		return 0, "", false
	}
	warn, _, hasWarn := level("temp_warn")
	crit, critField, hasCrit := level("temp_crit")
	if hasWarn && hasCrit && crit <= warn {
		errs = append(errs, FieldError{Field: critField, Message: "temp_crit must be above temp_warn"})
	}
	return errs
}

// checkBGPParams checks the bgp parameters (PRT-07, WP-1001 AK3): every
// peers[i].address is an IPv4 or IPv6 address, listed once.
func checkBGPParams(params map[string]any) []FieldError {
	items, _ := params["peers"].([]any)
	var errs []FieldError
	seen := map[netip.Addr]bool{}
	for i, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		s, ok := obj["address"].(string)
		if !ok {
			continue
		}
		field := fmt.Sprintf("params.peers[%d].address", i)
		addr, err := netip.ParseAddr(strings.TrimSpace(s))
		switch {
		case err != nil:
			errs = append(errs, FieldError{Field: field, Message: "must be an IPv4 or IPv6 address"})
		case seen[addr.WithZone("").Unmap()]:
			errs = append(errs, FieldError{Field: field, Message: "is listed twice"})
		default:
			seen[addr.WithZone("").Unmap()] = true
		}
	}
	return errs
}

// Defaults of the bng drop ratios (PRT-10, api/probe-types.json).
const (
	bngDefaultDropWarn = 0.2
	bngDefaultDropCrit = 0.5
)

// checkBNGParams checks the drop ratios of the bng type (PRT-10, WP-1006
// AK3): drop_warn_ratio does not exceed drop_crit_ratio (absent ones take
// their defaults).
func checkBNGParams(params map[string]any) []FieldError {
	warn, hasWarn := numberOf(params["drop_warn_ratio"])
	crit, hasCrit := numberOf(params["drop_crit_ratio"])
	if !hasWarn {
		warn = bngDefaultDropWarn
	}
	if !hasCrit {
		crit = bngDefaultDropCrit
	}
	if (hasWarn || hasCrit) && warn > crit {
		field := "params.drop_warn_ratio"
		if !hasWarn {
			field = "params.drop_crit_ratio"
		}
		return []FieldError{{Field: field, Message: "drop_warn_ratio must not exceed drop_crit_ratio"}}
	}
	return nil
}

// checkOSPFParams checks the ospf parameters (PRT-07, WP-1002 AK1): every
// neighbors[i].router_id is a router ID in dotted IPv4 form and
// min_neighbors does not exceed max_neighbors.
func checkOSPFParams(params map[string]any) []FieldError {
	errs := checkEntries(params, "neighbors", "router_id", "must be a router ID in dotted IPv4 form", func(s string) bool {
		a, err := netip.ParseAddr(strings.TrimSpace(s))
		return err == nil && a.Is4()
	})
	return append(errs, checkMinMax(params, "min_neighbors", "max_neighbors")...)
}

// checkISISParams checks the isis parameters (PRT-07, WP-1002 AK2): every
// adjacencies[i].system_id is a system ID of six octets (hex digits,
// optionally separated by '.', ':' or '-') and min_adjacencies does not
// exceed max_adjacencies.
func checkISISParams(params map[string]any) []FieldError {
	errs := checkEntries(params, "adjacencies", "system_id", "must be a system ID of six octets (e.g. 1921.6800.1001)", func(s string) bool {
		digits := strings.NewReplacer(".", "", ":", "", "-", "").Replace(strings.TrimSpace(s))
		raw, err := hex.DecodeString(digits)
		return err == nil && len(raw) == 6
	})
	return append(errs, checkMinMax(params, "min_adjacencies", "max_adjacencies")...)
}

// tr069Path is a TR-069 parameter path (Device.DeviceInfo.UpTime,
// InternetGatewayDevice.WANDevice.1.…).
var tr069Path = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*(\.[A-Za-z0-9_-]+)*$`)

// checkTR069Params checks the tr069 parameters (PRT-15, WP-1018 AK4):
// acs_url is an http or https URL with a host and without user info,
// query or fragment; every parameters[i].path is a TR-069 path, given once;
// a critical value lies beyond its warning value in the direction of the
// parameter.
func checkTR069Params(params map[string]any) []FieldError {
	var errs []FieldError
	if raw, ok := params["acs_url"].(string); ok {
		u, err := url.Parse(strings.TrimSpace(raw))
		switch {
		case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
			errs = append(errs, FieldError{Field: "params.acs_url", Message: "must be an http or https URL"})
		case u.User != nil || u.RawQuery != "" || u.Fragment != "":
			errs = append(errs, FieldError{Field: "params.acs_url", Message: "must not carry user info, a query or a fragment"})
		}
	}
	items, _ := params["parameters"].([]any)
	seen := map[string]bool{}
	for i, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		prefix := fmt.Sprintf("params.parameters[%d]", i)
		if path, ok := obj["path"].(string); ok {
			switch {
			case !tr069Path.MatchString(path):
				errs = append(errs, FieldError{Field: prefix + ".path", Message: "must be a TR-069 parameter path such as Device.DeviceInfo.UpTime"})
			case seen[path]:
				errs = append(errs, FieldError{Field: prefix + ".path", Message: "is listed twice"})
			}
			seen[path] = true
		}
		warn, hasWarn := numberOf(obj["warn"])
		crit, hasCrit := numberOf(obj["crit"])
		below := obj["direction"] == "below"
		if hasWarn && hasCrit && ((below && crit >= warn) || (!below && crit <= warn)) {
			errs = append(errs, FieldError{Field: prefix + ".crit", Message: "must lie beyond warn in the direction of the parameter"})
		}
	}
	return errs
}

// checkEntries checks the string field of every object of the array
// parameter list.
func checkEntries(params map[string]any, list, field, message string, valid func(string) bool) []FieldError {
	items, _ := params[list].([]any)
	var errs []FieldError
	for i, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if s, ok := obj[field].(string); ok && !valid(s) {
			errs = append(errs, FieldError{Field: fmt.Sprintf("params.%s[%d].%s", list, i, field), Message: message})
		}
	}
	return errs
}

// checkMinMax reports a minimum above the maximum (an absent maximum is
// its schema default, the row limit of the type).
func checkMinMax(params map[string]any, minName, maxName string) []FieldError {
	lo, ok := numberOf(params[minName])
	if !ok {
		return nil
	}
	hi, ok := numberOf(params[maxName])
	if ok && lo > hi {
		return []FieldError{{Field: "params." + minName, Message: minName + " must not exceed " + maxName}}
	}
	return nil
}

// redfishID reports whether id is a plain Redfish resource Id, the same
// rule as the collector's redfish.ValidID.
func redfishID(id string) bool {
	if id == "" || len(id) > 64 || id == "." || id == ".." {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// Defaults of the optical Rx thresholds (E-186, api/probe-types.json).
const (
	opticalDefaultRxWarnDBm = -20
	opticalDefaultRxCritDBm = -25
)

// checkOpticalParams checks the selection and thresholds of the optical
// type (PRT-09, WP-1004 AK5, AK6): exactly one of interfaces (1 to 512
// ifName or ifIndex) and all: true, else a field error on
// params.interfaces; a critical level must lie below its warning level.
// The levels are merged like the executor reads them (thresholds before
// params before the defaults), and the error names the field the
// critical level comes from (thresholds.rx_crit_dbm or params.rx_crit_dbm).
func checkOpticalParams(spec *ProbeSpec, params map[string]any) []FieldError {
	var errs []FieldError
	interfaces, hasInterfaces := params["interfaces"].([]any)
	all, _ := params["all"].(bool)
	switch {
	case hasInterfaces && all:
		errs = append(errs, FieldError{Field: "params.interfaces", Message: "set either interfaces or all, not both"})
	case !hasInterfaces && !all:
		errs = append(errs, FieldError{Field: "params.interfaces", Message: "set interfaces (1 to 512 ifName or ifIndex) or all: true"})
	case hasInterfaces && len(interfaces) == 0:
		errs = append(errs, FieldError{Field: "params.interfaces", Message: "must list 1 to 512 interfaces"})
	}
	var thresholds map[string]any
	if spec != nil {
		thresholds, _ = decodeObject(spec.Thresholds)
	}
	// level returns the effective value of name and the field it comes
	// from ("" for a default).
	level := func(name string, def float64, hasDef bool) (float64, string, bool) {
		if v, ok := numberOf(thresholds[name]); ok {
			return v, "thresholds." + name, true
		}
		if v, ok := numberOf(params[name]); ok {
			return v, "params." + name, true
		}
		return def, "", hasDef
	}
	for _, dir := range []string{"rx", "tx"} {
		warn, warnField, hasWarn := level(dir+"_warn_dbm", opticalDefaultRxWarnDBm, dir == "rx")
		crit, critField, hasCrit := level(dir+"_crit_dbm", opticalDefaultRxCritDBm, dir == "rx")
		if hasWarn && hasCrit && crit >= warn {
			field := critField
			switch {
			case field != "":
			case strings.HasPrefix(warnField, "thresholds."):
				field = warnField // the default critical level against a warning level of thresholds
			default:
				field = "params." + dir + "_crit_dbm"
			}
			errs = append(errs, FieldError{Field: field, Message: dir + "_crit_dbm must be below " + dir + "_warn_dbm"})
		}
	}
	return errs
}

// Defaults of the icmp parameters count and spacing_ms (api/probe-types.json).
const (
	icmpDefaultCount     = 3
	icmpDefaultSpacingMS = 1000
)

// checkICMPParams checks the icmp parameters against the rest of the spec
// (PRT-02, WP-969 AK3): every echo request goes out within the timeout,
// (count - 1) x spacing_ms below timeout_ms (reported on spacing_ms when
// it is set, else on count), and family v4 or v6 matches an address
// literal target.
func checkICMPParams(spec *ProbeSpec, params map[string]any) []FieldError {
	var errs []FieldError
	count, spacing := float64(icmpDefaultCount), float64(icmpDefaultSpacingMS)
	if v, ok := numberOf(params["count"]); ok {
		count = v
	}
	_, spacingSet := params["spacing_ms"]
	if v, ok := numberOf(params["spacing_ms"]); ok {
		spacing = v
	}
	if spec.TimeoutMS > 0 && (count-1)*spacing >= float64(spec.TimeoutMS) {
		field := "params.count"
		if spacingSet {
			field = "params.spacing_ms"
		}
		errs = append(errs, FieldError{Field: field, Message: "(count - 1) x spacing_ms must be below the timeout"})
	}
	if family, ok := params["family"].(string); ok {
		if ip, err := netip.ParseAddr(strings.TrimSpace(spec.Target)); err == nil {
			ip = ip.Unmap()
			if (family == "v6" && ip.Is4()) || (family == "v4" && ip.Is6()) {
				errs = append(errs, FieldError{Field: "params.family", Message: "does not match the address family of the target"})
			}
		}
	}
	return errs
}

// checkHTTPParams checks the parameters of the http type (PRT-04,
// WP-971): url with scheme http or https and a host, body_regex as RE2
// expression and only with GET, every entry of expected_status a code or
// range between 100 and 599.
func checkHTTPParams(params map[string]any) []FieldError {
	var errs []FieldError
	if raw, ok := params["url"].(string); ok {
		if _, err := ParseHTTPURL(raw); err != nil {
			errs = append(errs, FieldError{Field: "params.url", Message: err.Error()})
		}
	}
	if re, ok := params["body_regex"].(string); ok {
		if _, err := regexp.Compile(re); err != nil {
			errs = append(errs, FieldError{Field: "params.body_regex", Message: "must be a valid RE2 regular expression"})
		} else if m, _ := params["method"].(string); m == "HEAD" {
			errs = append(errs, FieldError{Field: "params.body_regex", Message: "is allowed with method GET only"})
		}
	}
	if list, ok := params["expected_status"].([]any); ok {
		for i, item := range list {
			if s, ok := item.(string); ok {
				if _, _, err := ParseStatusRange(s); err != nil {
					errs = append(errs, FieldError{Field: fmt.Sprintf("params.expected_status[%d]", i), Message: err.Error()})
				}
			}
		}
	}
	return errs
}

// ParseHTTPURL parses the url of an http probe: scheme http or https and a
// host.
// validation: fixed texts.
func ParseHTTPURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" {
		return nil, errors.New("must be an http or https URL with a host")
	}
	if u.User != nil {
		return nil, errors.New("must not carry user information; use a credential of the namespace http")
	}
	return u, nil
}

// ParseStatusRange parses an expected status: a code ("200") or a range
// ("200-299") between 100 and 599.
// validation: fixed texts.
func ParseStatusRange(s string) (lo, hi int, err error) {
	from, to, isRange := strings.Cut(strings.TrimSpace(s), "-")
	lo, err1 := strconv.Atoi(strings.TrimSpace(from))
	hi = lo
	var err2 error
	if isRange {
		hi, err2 = strconv.Atoi(strings.TrimSpace(to))
	}
	if err1 != nil || err2 != nil || lo < 100 || hi > 599 || lo > hi {
		return 0, 0, errors.New("must be a status code or range between 100 and 599")
	}
	return lo, hi, nil
}

// checkDNSParams checks qname of the dns type (PRT-12): a DNS name whose
// labels hold 1 to 63 bytes; surrounding spaces are ignored, a single
// trailing dot is allowed and "." alone is the root.
func checkDNSParams(params map[string]any) []FieldError {
	qname, ok := params["qname"].(string)
	if !ok || ValidDNSName(qname) {
		return nil
	}
	return []FieldError{{Field: "params.qname", Message: "must be a DNS name with labels of 1 to 63 bytes"}}
}

// ValidDNSName reports whether name is a DNS name with labels of 1 to 63
// bytes and at most 253 bytes in its presentation form (RFC 1035 2.3.4).
func ValidDNSName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "." {
		return true
	}
	name = strings.TrimSuffix(name, ".")
	if name == "" || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
	}
	return true
}

// MaxTCPSend is the largest send string of the tcp type in bytes (PRT-03).
const MaxTCPSend = 256

// checkTCPParams checks send (at most 256 bytes after decoding the
// escapes) and expect (a valid RE2 regular expression) of the tcp type
// (PRT-03).
func checkTCPParams(params map[string]any) []FieldError {
	var errs []FieldError
	if send, ok := params["send"].(string); ok && len(UnescapeSend(send)) > MaxTCPSend {
		errs = append(errs, FieldError{Field: "params.send", Message: fmt.Sprintf("must be at most %d bytes", MaxTCPSend)})
	}
	if expect, ok := params["expect"].(string); ok {
		if _, err := regexp.Compile(expect); err != nil {
			errs = append(errs, FieldError{Field: "params.expect", Message: "must be a valid RE2 regular expression"})
		}
	}
	return errs
}

// UnescapeSend decodes the escapes \r, \n, \t and \\ of a send string;
// any other backslash stays as it is.
func UnescapeSend(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			var c byte
			switch s[i+1] {
			case 'r':
				c = '\r'
			case 'n':
				c = '\n'
			case 't':
				c = '\t'
			case '\\':
				c = '\\'
			}
			if c != 0 {
				b.WriteByte(c)
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func decodeObject(raw json.RawMessage) (map[string]any, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, false
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, false
	}
	return m, true
}

// validateObject validates params or thresholds (an object or absent)
// against the parameter schema. With required set, absent params are
// validated as an empty object, so that a missing required parameter is
// reported.
func validateObject(s *schema, raw json.RawMessage, prefix string, required bool) []FieldError {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		if !required {
			return nil
		}
		raw = json.RawMessage(`{}`)
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return []FieldError{{Field: prefix, Message: "must be a JSON object"}}
	}
	if _, ok := v.(map[string]any); !ok {
		return []FieldError{{Field: prefix, Message: "must be a JSON object"}}
	}
	return s.validate(v, prefix)
}

// schema is the subset of JSON Schema 2020-12 the register uses: type,
// properties, required, additionalProperties false, items, enum, minimum,
// maximum, exclusiveMinimum, minLength, maxLength, maxItems, default and
// description.
type schema struct {
	Type                 string             `json:"type"`
	Properties           map[string]*schema `json:"properties"`
	Required             []string           `json:"required"`
	AdditionalProperties *bool              `json:"additionalProperties"`
	Items                *schema            `json:"items"`
	Enum                 []json.RawMessage  `json:"enum"`
	Minimum              *float64           `json:"minimum"`
	Maximum              *float64           `json:"maximum"`
	ExclusiveMinimum     *float64           `json:"exclusiveMinimum"`
	MinLength            *int               `json:"minLength"`
	MaxLength            *int               `json:"maxLength"`
	MaxItems             *int               `json:"maxItems"`
	Default              json.RawMessage    `json:"default"`
	Description          string             `json:"description"`
}

// schemaKeywords are the keywords schema understands; a parameter schema
// using another keyword is rejected by CheckSchema.
var schemaKeywords = map[string]bool{
	"type": true, "properties": true, "required": true, "additionalProperties": true, "items": true,
	"enum": true, "minimum": true, "maximum": true, "exclusiveMinimum": true, "minLength": true,
	"maxLength": true, "maxItems": true, "default": true, "description": true,
}

// CheckSchema reports whether raw is a schema made only of the supported
// keywords and types, with defaults that satisfy their own schema.
func CheckSchema(raw json.RawMessage) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	for k := range m {
		if !schemaKeywords[k] {
			return fmt.Errorf("unsupported schema keyword %q", k)
		}
	}
	var s schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return err
	}
	switch s.Type {
	case "object", "array", "string", "integer", "number", "boolean":
	default:
		return fmt.Errorf("unsupported schema type %q", s.Type)
	}
	if s.Default != nil {
		var v any
		dec := json.NewDecoder(bytes.NewReader(s.Default))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			return err
		}
		if errs := s.validate(v, "default"); len(errs) > 0 {
			return fmt.Errorf("default violates its schema: %s", errs[0].Message)
		}
	}
	for name, p := range m {
		switch name {
		case "properties":
			var props map[string]json.RawMessage
			if err := json.Unmarshal(p, &props); err != nil {
				return err
			}
			for pn, ps := range props {
				if err := CheckSchema(ps); err != nil {
					return fmt.Errorf("%s: %w", pn, err)
				}
			}
		case "items":
			if err := CheckSchema(p); err != nil {
				return fmt.Errorf("items: %w", err)
			}
		}
	}
	return nil
}

func (s *schema) validate(v any, path string) []FieldError {
	fail := func(format string, args ...any) []FieldError {
		return []FieldError{{Field: path, Message: fmt.Sprintf(format, args...)}}
	}
	if len(s.Enum) > 0 {
		enc, _ := json.Marshal(v)
		found := false
		for _, e := range s.Enum {
			var ev any
			_ = json.Unmarshal(e, &ev)
			ee, _ := json.Marshal(ev)
			if bytes.Equal(enc, ee) {
				found = true
				break
			}
		}
		if !found {
			return fail("value is not allowed")
		}
	}
	switch s.Type {
	case "object":
		obj, ok := v.(map[string]any)
		if !ok {
			return fail("must be an object")
		}
		var errs []FieldError
		for _, r := range s.Required {
			if _, ok := obj[r]; !ok {
				errs = append(errs, FieldError{Field: path + "." + r, Message: "is required"})
			}
		}
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p, known := s.Properties[k]
			if !known {
				if s.AdditionalProperties != nil && !*s.AdditionalProperties {
					errs = append(errs, FieldError{Field: path + "." + k, Message: "unknown parameter"})
				}
				continue
			}
			errs = append(errs, p.validate(obj[k], path+"."+k)...)
		}
		return errs
	case "array":
		arr, ok := v.([]any)
		if !ok {
			return fail("must be an array")
		}
		if s.MaxItems != nil && len(arr) > *s.MaxItems {
			return fail("must have at most %d items", *s.MaxItems)
		}
		var errs []FieldError
		if s.Items != nil {
			for i, item := range arr {
				errs = append(errs, s.Items.validate(item, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
		return errs
	case "string":
		str, ok := v.(string)
		if !ok {
			return fail("must be a string")
		}
		n := len([]rune(str))
		if s.MinLength != nil && n < *s.MinLength {
			return fail("must be at least %d characters", *s.MinLength)
		}
		if s.MaxLength != nil && n > *s.MaxLength {
			return fail("must be at most %d characters", *s.MaxLength)
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fail("must be a boolean")
		}
	case "integer", "number":
		f, ok := numberOf(v)
		if !ok {
			return fail("must be a number")
		}
		if s.Type == "integer" && f != math.Trunc(f) {
			return fail("must be an integer")
		}
		if s.Minimum != nil && f < *s.Minimum {
			return fail("must be at least %v", *s.Minimum)
		}
		if s.ExclusiveMinimum != nil && f <= *s.ExclusiveMinimum {
			return fail("must be greater than %v", *s.ExclusiveMinimum)
		}
		if s.Maximum != nil && f > *s.Maximum {
			return fail("must be at most %v", *s.Maximum)
		}
	}
	return nil
}

func numberOf(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case float64:
		return n, true
	}
	return 0, false
}

// ValidateEntry validates one raw register entry against the entry schema
// ($defs/entry) of the contract.
func ValidateEntry(raw json.RawMessage) []FieldError {
	r, err := load()
	if err != nil {
		return []FieldError{{Field: "$defs/entry", Message: err.Error()}}
	}
	var s schema
	if err := json.Unmarshal(r.entry, &s); err != nil {
		return []FieldError{{Field: "$defs/entry", Message: err.Error()}}
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return []FieldError{{Field: "entry", Message: err.Error()}}
	}
	return s.validate(v, "entry")
}
