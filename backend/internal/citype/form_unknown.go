package citype

import (
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
)

// Form states over unreadable fields (SR-59, WP-872): the states of a
// resolved form must not reveal a value the reader may not read. The
// resolver evaluates only the readable values, and a condition (catalog
// rule, attribute condition, visible_when of a section) that references a
// field the policy does not let read is "unknown" and acts conservatively:
// it neither hides nor requires nor locks a field, and it does not hide a
// section. Conditions over readable fields act unchanged, so a reader
// without restricted fields gets the form as before.

// readableValues returns values without the fields the policy does not let
// read; values itself when the policy lets every key be read.
func readableValues(values map[string]any, policy FieldPolicy) map[string]any {
	var out map[string]any
	for k := range values {
		if policy.Readable(k) {
			continue
		}
		if out == nil {
			out = make(map[string]any, len(values))
			for key, v := range values {
				out[key] = v
			}
		}
		delete(out, k)
	}
	if out == nil {
		return values
	}
	return out
}

// conditionKnown reports whether every field a condition references is
// readable; a nil condition is known.
func conditionKnown(cond *rules.Rule, policy FieldPolicy) bool {
	if cond == nil {
		return true
	}
	if cond.Field != "" && !policy.Readable(cond.Field) {
		return false
	}
	for i := range cond.Rules {
		if !conditionKnown(&cond.Rules[i], policy) {
			return false
		}
	}
	return true
}

// predicatesKnown reports whether every field of the predicates is
// readable.
func predicatesKnown(preds []fieldmeta.Predicate, policy FieldPolicy) bool {
	for _, p := range preds {
		if !policy.Readable(p.Field) {
			return false
		}
	}
	return true
}

// knownDefinition returns def without the conditional clauses over
// unreadable fields: an unknown visible_when shows the field, an unknown
// required_when or read_only_when does not apply, an unknown
// allowed_values_when leaves the values of the definition. def itself when
// every clause is known.
func knownDefinition(def *fieldmeta.FieldDefinition, policy FieldPolicy) *fieldmeta.FieldDefinition {
	c := def.Conditional
	if c == nil {
		return def
	}
	visible, required, readOnly := predicatesKnown(c.VisibleWhen, policy), predicatesKnown(c.RequiredWhen, policy),
		predicatesKnown(c.ReadOnlyWhen, policy)
	allowed := c.AllowedValuesWhen == nil || predicatesKnown(c.AllowedValuesWhen.When, policy)
	if visible && required && readOnly && allowed {
		return def
	}
	known := *c
	if !visible {
		known.VisibleWhen = nil
	}
	if !required {
		known.RequiredWhen = nil
	}
	if !readOnly {
		known.ReadOnlyWhen = nil
	}
	if !allowed {
		known.AllowedValuesWhen = nil
	}
	out := *def
	out.Conditional = &known
	return &out
}
