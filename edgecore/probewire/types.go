package probewire

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
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
}

// Available reports whether probes of the type may be created.
func (t Type) Available() bool { return t.Status == TypeAvailable }

// ParamDefault returns the default of a parameter from the parameter schema.
func (t Type) ParamDefault(name string) (any, bool) {
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
func (t Type) Check() error {
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
	out := make([]string, 0, len(mustLoad().types))
	for _, t := range mustLoad().types {
		out = append(out, t.Key)
	}
	return out
}

// Lookup returns the type with the key.
func Lookup(key string) (Type, bool) {
	t, ok := mustLoad().byKey[key]
	return t, ok
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
func Validate(spec ProbeSpec) []FieldError {
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
	errs = append(errs, validateObject(&s, spec.Params, "params")...)
	errs = append(errs, validateObject(&s, spec.Thresholds, "thresholds")...)
	return errs
}

// validateObject validates params or thresholds (an object or absent)
// against the parameter schema.
func validateObject(s *schema, raw json.RawMessage, prefix string) []FieldError {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil
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
