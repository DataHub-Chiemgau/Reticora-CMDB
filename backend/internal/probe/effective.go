package probe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Effective values of a probe (PRB-01, WP-937, E-177, E-178): every field
// is resolved over the levels type default (api/probe-types.json) →
// monitoring_profile of the organization → of the probe's client → of the
// monitoring groups of the probe's CI → of the CI → probe.params; the most
// specific level that sets a field wins and the field names it as origin.
//
// An override set (monitoring_profile.params, probe.params) is a JSON
// object with the keys interval_s, timeout_s, down_count and up_count
// (integers; seconds), thresholds (an object of threshold values) and the
// type parameters of the register entry as flat keys, e.g.
//
//	{"interval_s": 60, "count": 5, "thresholds": {"rtt_warn_ms": 200}}
//
// Thresholds and type parameters are resolved per key. Values are checked
// against the type limits when written (ValidateLayer); a stored value
// outside the limits of the current register (the limits changed after the
// write) is clamped to the limit and marked clamped, and the timeout is
// lowered below the interval when needed.

// OriginDefault is the origin of a value from the type default of the
// register; the other origins are the levels (LevelOrg … LevelProbe).
const OriginDefault = "default"

// Origins lists the origins of an effective value from the widest to the
// narrowest.
var Origins = []string{OriginDefault, LevelOrg, LevelClient, LevelGroup, LevelCI, LevelProbe}

// Reserved keys of an override set; every other key is a type parameter.
const (
	KeyIntervalS  = "interval_s"
	KeyTimeoutS   = "timeout_s"
	KeyDownCount  = "down_count"
	KeyUpCount    = "up_count"
	KeyThresholds = "thresholds"
)

// Field is one effective value with its origin. Clamped marks a stored
// value that was moved to the type limits of the current register; GroupID
// names the deciding monitoring group when Origin is group. Value is an
// int64 for the interval, timeout and counts, and the decoded JSON value
// (json.Number for numbers) for thresholds and type parameters; a type
// parameter without default and without override has the value nil.
type Field struct {
	Value   any    `json:"value"`
	Origin  string `json:"origin"`
	Clamped bool   `json:"clamped"`
	GroupID string `json:"group_id,omitempty"`
}

// Int returns the value of an integer field (interval, timeout, counts).
func (f Field) Int() int64 {
	switch v := f.Value.(type) {
	case int64:
		return v
	case json.Number:
		n, _ := v.Int64()
		return n
	}
	return 0
}

// EffectiveSpec are the effective values of one probe. TypeKnown is false
// when the type is missing from the register; the fields are then empty.
type EffectiveSpec struct {
	ProbeID       string           `json:"probe_id"`
	Type          string           `json:"type"`
	Target        string           `json:"target"`
	ParentProbeID string           `json:"parent_probe_id,omitempty"`
	TypeKnown     bool             `json:"-"`
	IntervalS     Field            `json:"interval_s"`
	TimeoutS      Field            `json:"timeout_s"`
	DownCount     Field            `json:"down_count"`
	UpCount       Field            `json:"up_count"`
	Thresholds    map[string]Field `json:"thresholds"`
	Params        map[string]Field `json:"params"`
}

// Clamped reports whether any field was clamped.
func (e *EffectiveSpec) Clamped() bool {
	for _, f := range []Field{e.IntervalS, e.TimeoutS, e.DownCount, e.UpCount} {
		if f.Clamped {
			return true
		}
	}
	for _, m := range []map[string]Field{e.Thresholds, e.Params} {
		for _, f := range m {
			if f.Clamped {
				return true
			}
		}
	}
	return false
}

// ProbeSpec returns the wire specification the collector executes
// (milliseconds; parameters without value are left out) under epoch.
func (e *EffectiveSpec) ProbeSpec(epoch int64) probewire.ProbeSpec {
	return probewire.ProbeSpec{
		ID:         e.ProbeID,
		Type:       e.Type,
		Target:     e.Target,
		IntervalMS: e.IntervalS.Int() * 1000,
		TimeoutMS:  e.TimeoutS.Int() * 1000,
		DownCount:  int(e.DownCount.Int()),
		UpCount:    int(e.UpCount.Int()),
		Params:     valuesJSON(e.Params),
		Thresholds: valuesJSON(e.Thresholds),
		ParentID:   e.ParentProbeID,
		Epoch:      epoch,
	}
}

// valuesJSON encodes the values of fields that have one; nil without any.
func valuesJSON(fields map[string]Field) json.RawMessage {
	out := map[string]any{}
	for k, f := range fields {
		if f.Value != nil {
			out[k] = f.Value
		}
	}
	if len(out) == 0 {
		return nil
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return b
}

// Overrides is a decoded override set: the top-level keys of the JSON
// object, numbers as json.Number. An invalid set is empty.
type Overrides map[string]any

// ParseOverrides decodes an override set (monitoring_profile.params,
// probe.params). Anything but a JSON object yields no overrides.
func ParseOverrides(raw json.RawMessage) Overrides {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil
	}
	return m
}

// threshold returns the threshold key of the set.
func (o Overrides) threshold(key string) (any, bool) {
	t, ok := o[KeyThresholds].(map[string]any)
	if !ok {
		return nil, false
	}
	v, ok := t[key]
	return v, ok
}

// thresholdKeys returns the threshold keys of the set.
func (o Overrides) thresholdKeys() []string {
	t, _ := o[KeyThresholds].(map[string]any)
	keys := make([]string, 0, len(t))
	for k := range t {
		keys = append(keys, k)
	}
	return keys
}

// GroupOverrides is the override set of a monitoring group the probe's CI
// is a member of, with the group's rank: the higher priority wins, on a tie
// the lexicographically smaller name (E-178).
type GroupOverrides struct {
	GroupID   string
	Name      string
	Priority  int
	Overrides Overrides
}

// Layers are the override sets of the levels above the probe for the
// probe's type: the organization's profile, the profile of the probe's
// client, the profiles of the monitoring groups of its CI (any order) and
// the CI's profile. A level without profile is nil.
type Layers struct {
	Org    Overrides
	Client Overrides
	Groups []GroupOverrides
	CI     Overrides
}

// rankedGroups returns the groups in rank order (priority descending, name,
// then id), independent of the read order.
func rankedGroups(groups []GroupOverrides) []GroupOverrides {
	if len(groups) < 2 {
		return groups
	}
	out := append([]GroupOverrides(nil), groups...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].GroupID < out[j].GroupID
	})
	return out
}

// Resolve returns the effective values of the probe over the layers with
// the type entry of the register.
func Resolve(p *Probe, layers Layers) EffectiveSpec {
	t, ok := probewire.Lookup(p.Type)
	if !ok {
		return EffectiveSpec{ProbeID: p.ID, Type: p.Type, Target: p.Target, ParentProbeID: p.ParentProbeID}
	}
	return resolveWith(&t, p, ParseOverrides(p.Params), layers)
}

// resolveWith resolves against the type entry t (tests pass a changed
// entry to model a limit change of the register).
func resolveWith(t *probewire.Type, p *Probe, own Overrides, layers Layers) EffectiveSpec {
	ps := paramSchemaOf(t)
	groups := rankedGroups(layers.Groups)
	r := resolver{own: own, layers: layers, groups: groups}
	e := EffectiveSpec{
		ProbeID: p.ID, Type: p.Type, Target: p.Target, ParentProbeID: p.ParentProbeID, TypeKnown: true,
		Thresholds: map[string]Field{}, Params: map[string]Field{},
	}
	e.IntervalS = r.intField(KeyIntervalS, t.DefaultIntervalS, t.MinIntervalS, t.MaxIntervalS)
	e.TimeoutS = r.intField(KeyTimeoutS, t.DefaultTimeoutS, t.MinTimeoutS, t.MaxTimeoutS)
	e.DownCount = r.intField(KeyDownCount, int64(t.DefaultDownCount), probewire.MinCount, probewire.MaxCount)
	e.UpCount = r.intField(KeyUpCount, int64(t.DefaultUpCount), probewire.MinCount, probewire.MaxCount)
	// PRT-02: the timeout stays below the interval.
	if iv := e.IntervalS.Int(); e.TimeoutS.Int() >= iv {
		e.TimeoutS.Value = max(iv-1, 1)
		e.TimeoutS.Clamped = true
	}
	for _, name := range ps.order {
		prop := ps.props[name]
		e.Params[name] = r.valueField(name, prop, false)
	}
	for _, name := range r.thresholdKeys() {
		prop, known := ps.props[name]
		if !known {
			continue // not a parameter of the type (any more)
		}
		if f := r.valueField(name, prop, true); f.Origin != "" {
			e.Thresholds[name] = f
		}
	}
	return e
}

// resolver looks a key up over the levels.
type resolver struct {
	own    Overrides
	layers Layers
	groups []GroupOverrides
}

// pick returns the value of the most specific level whose get yields one,
// with its origin and, for the group level, the deciding group.
func (r *resolver) pick(get func(Overrides) (any, bool)) (val any, origin, groupID string) {
	if v, ok := get(r.layers.Org); ok {
		val, origin = v, LevelOrg
	}
	if v, ok := get(r.layers.Client); ok {
		val, origin = v, LevelClient
	}
	for _, g := range r.groups {
		if v, ok := get(g.Overrides); ok {
			val, origin, groupID = v, LevelGroup, g.GroupID
			break
		}
	}
	if v, ok := get(r.layers.CI); ok {
		val, origin, groupID = v, LevelCI, ""
	}
	if v, ok := get(r.own); ok {
		val, origin, groupID = v, LevelProbe, ""
	}
	return val, origin, groupID
}

// intField resolves an integer field and clamps it to [lo, hi].
func (r *resolver) intField(key string, def, lo, hi int64) Field {
	get := func(o Overrides) (any, bool) {
		n, ok := o[key].(json.Number)
		if !ok {
			return nil, false
		}
		i, err := n.Int64()
		if err != nil {
			return nil, false
		}
		return i, true
	}
	val, origin, groupID := r.pick(get)
	if origin == "" {
		return Field{Value: def, Origin: OriginDefault}
	}
	v := val.(int64)
	f := Field{Value: v, Origin: origin, GroupID: groupID}
	if v < lo {
		f.Value, f.Clamped = lo, true
	} else if v > hi {
		f.Value, f.Clamped = hi, true
	}
	return f
}

// valueField resolves a type parameter (threshold false) or a threshold.
// A stored value that no longer fits the parameter schema is clamped to the
// numeric limits or, when that is impossible, replaced by the default.
func (r *resolver) valueField(name string, prop *propSchema, threshold bool) Field {
	get := func(o Overrides) (any, bool) {
		if threshold {
			return o.threshold(name)
		}
		v, ok := o[name]
		return v, ok
	}
	val, origin, groupID := r.pick(get)
	if origin == "" {
		if threshold {
			return Field{}
		}
		return Field{Value: prop.def, Origin: OriginDefault}
	}
	f := Field{Value: val, Origin: origin, GroupID: groupID}
	fixed, changed, ok := prop.fit(val)
	switch {
	case !ok && threshold:
		// A threshold has no default: the invalid value is left out.
		return Field{Value: nil, Origin: origin, GroupID: groupID, Clamped: true}
	case !ok:
		return Field{Value: prop.def, Origin: OriginDefault, Clamped: true}
	case changed:
		f.Value, f.Clamped = fixed, true
	}
	return f
}

// thresholdKeys collects the threshold keys of every level, sorted.
func (r *resolver) thresholdKeys() []string {
	seen := map[string]bool{}
	add := func(o Overrides) {
		for _, k := range o.thresholdKeys() {
			seen[k] = true
		}
	}
	add(r.layers.Org)
	add(r.layers.Client)
	for _, g := range r.groups {
		add(g.Overrides)
	}
	add(r.layers.CI)
	add(r.own)
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// propSchema is the part of a parameter schema the resolution needs to fit
// a stored value to the current register: type, numeric limits, enum and
// default. Strings, arrays and objects are taken as stored when the JSON
// type matches; their finer rules were checked when written.
type propSchema struct {
	typ        string
	min, max   *float64
	exclMin    *float64
	enum       []string
	def        any
	hasDefault bool
}

// paramSchema is the decoded parameter schema of one type entry.
type paramSchema struct {
	props map[string]*propSchema
	order []string
}

// schemaCache holds the decoded parameter schemas by their JSON text, so a
// batch decodes each type's schema once.
var schemaCache sync.Map

func paramSchemaOf(t *probewire.Type) *paramSchema {
	if v, ok := schemaCache.Load(string(t.Params)); ok {
		return v.(*paramSchema)
	}
	ps := decodeParamSchema(t.Params)
	schemaCache.Store(string(t.Params), ps)
	return ps
}

func decodeParamSchema(raw json.RawMessage) *paramSchema {
	var s struct {
		Properties map[string]struct {
			Type             string            `json:"type"`
			Minimum          *float64          `json:"minimum"`
			Maximum          *float64          `json:"maximum"`
			ExclusiveMinimum *float64          `json:"exclusiveMinimum"`
			Enum             []json.RawMessage `json:"enum"`
			Default          json.RawMessage   `json:"default"`
		} `json:"properties"`
	}
	ps := &paramSchema{props: map[string]*propSchema{}}
	if err := json.Unmarshal(raw, &s); err != nil {
		return ps
	}
	for name, p := range s.Properties {
		prop := &propSchema{typ: p.Type, min: p.Minimum, max: p.Maximum, exclMin: p.ExclusiveMinimum}
		for _, e := range p.Enum {
			prop.enum = append(prop.enum, canonical(e))
		}
		if len(p.Default) > 0 {
			dec := json.NewDecoder(bytes.NewReader(p.Default))
			dec.UseNumber()
			if err := dec.Decode(&prop.def); err == nil {
				prop.hasDefault = true
			}
		}
		ps.props[name] = prop
		ps.order = append(ps.order, name)
	}
	sort.Strings(ps.order)
	return ps
}

// canonical returns the compact JSON text of a value for enum comparison.
func canonical(raw []byte) string {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return string(raw)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// fit fits a stored value to the schema: ok false when the JSON type or the
// enum does not match, changed true when a number was clamped (fixed is the
// clamped value).
func (p *propSchema) fit(v any) (fixed any, changed, ok bool) {
	if len(p.enum) > 0 {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, false, false
		}
		found := false
		for _, e := range p.enum {
			if e == string(b) {
				found = true
				break
			}
		}
		if !found {
			return nil, false, false
		}
	}
	switch p.typ {
	case "integer", "number":
		n, isNum := v.(json.Number)
		if !isNum {
			return nil, false, false
		}
		f, err := n.Float64()
		if err != nil {
			return nil, false, false
		}
		if p.typ == "integer" && f != math.Trunc(f) {
			return nil, false, false
		}
		g := f
		if p.min != nil && g < *p.min {
			g = *p.min
		}
		if p.max != nil && g > *p.max {
			g = *p.max
		}
		if p.exclMin != nil && g <= *p.exclMin {
			return nil, false, false
		}
		if g == f {
			return v, false, true
		}
		return json.Number(strconv.FormatFloat(g, 'f', -1, 64)), true, true
	case "string":
		_, ok = v.(string)
	case "boolean":
		_, ok = v.(bool)
	case "array":
		_, ok = v.([]any)
	case "object":
		_, ok = v.(map[string]any)
	default:
		ok = true
	}
	return v, false, ok
}

// ValidateLayer checks an override set of one level for the probe type
// typeKey when it is written (WP-939, WP-946): type known and available,
// interval, timeout and counts within the type limits (the timeout below
// the interval when the set has both), thresholds and type parameters
// against the parameter schema. The errors are the field errors of
// probewire.Validate for the fields the set contains; a field the set
// leaves out is resolved from the levels below and not checked here.
func ValidateLayer(typeKey string, params json.RawMessage) []probewire.FieldError {
	t, ok := probewire.Lookup(typeKey)
	if !ok {
		return []probewire.FieldError{{Field: "type", Message: fmt.Sprintf("unknown probe type %q", typeKey)}}
	}
	if len(bytes.TrimSpace(params)) == 0 {
		params = json.RawMessage(`{}`)
	}
	dec := json.NewDecoder(bytes.NewReader(params))
	dec.UseNumber()
	var set map[string]any
	if err := dec.Decode(&set); err != nil || set == nil {
		return []probewire.FieldError{{Field: "params", Message: "must be a JSON object"}}
	}
	var errs []probewire.FieldError
	// A field the set leaves out takes the value that cannot conflict:
	// the widest interval and the shortest timeout, so the check "timeout
	// below the interval" fires only for a set with both.
	spec := probewire.ProbeSpec{
		Type: typeKey, Target: "-",
		IntervalMS: t.MaxIntervalS * 1000, TimeoutMS: t.MinTimeoutS * 1000,
		DownCount: t.DefaultDownCount, UpCount: t.DefaultUpCount,
	}
	ints := []struct {
		key string
		set func(int64)
	}{
		{KeyIntervalS, func(v int64) { spec.IntervalMS = secondsToMS(v) }},
		{KeyTimeoutS, func(v int64) { spec.TimeoutMS = secondsToMS(v) }},
		{KeyDownCount, func(v int64) { spec.DownCount = clampInt(v) }},
		{KeyUpCount, func(v int64) { spec.UpCount = clampInt(v) }},
	}
	present := map[string]bool{}
	for _, f := range ints {
		raw, has := set[f.key]
		if !has {
			continue
		}
		n, isNum := raw.(json.Number)
		v, err := n.Int64()
		if !isNum || err != nil {
			errs = append(errs, probewire.FieldError{Field: f.key, Message: "must be an integer"})
			continue
		}
		present[f.key] = true
		f.set(v)
	}
	typeParams := map[string]any{}
	for k, v := range set {
		switch k {
		case KeyIntervalS, KeyTimeoutS, KeyDownCount, KeyUpCount:
		case KeyThresholds:
			present[KeyThresholds] = true
			b, _ := json.Marshal(v)
			spec.Thresholds = b
		default:
			typeParams[k] = v
		}
	}
	if len(typeParams) > 0 {
		b, _ := json.Marshal(typeParams)
		spec.Params = b
	}
	for _, fe := range probewire.Validate(spec) {
		if layerHasField(fe.Field, present, typeParams) {
			errs = append(errs, fe)
		}
	}
	sort.SliceStable(errs, func(i, j int) bool { return errs[i].Field < errs[j].Field })
	return errs
}

// secondsToMS converts seconds to milliseconds and saturates instead of
// overflowing, so a huge or very negative value stays outside the type
// limits and Validate refuses it rather than wrapping into the range.
func secondsToMS(v int64) int64 {
	const limit = math.MaxInt64 / 1000
	if v > limit {
		return math.MaxInt64
	}
	if v < -limit {
		return math.MinInt64
	}
	return v * 1000
}

// clampInt keeps a count inside int for the spec; Validate reports the
// range.
func clampInt(v int64) int {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < math.MinInt32 {
		return math.MinInt32
	}
	return int(v)
}

// layerHasField reports whether a field error of probewire.Validate refers
// to a field of the override set.
func layerHasField(field string, present map[string]bool, typeParams map[string]any) bool {
	switch {
	case field == "type":
		return true
	case field == KeyIntervalS || field == KeyTimeoutS || field == KeyDownCount || field == KeyUpCount:
		return present[field]
	case field == KeyThresholds || strings.HasPrefix(field, KeyThresholds+"."):
		return present[KeyThresholds]
	case field == "params":
		return len(typeParams) > 0
	case strings.HasPrefix(field, "params."):
		name := strings.TrimPrefix(field, "params.")
		if i := strings.IndexAny(name, ".["); i >= 0 {
			name = name[:i]
		}
		_, ok := typeParams[name]
		return ok
	}
	return false
}
