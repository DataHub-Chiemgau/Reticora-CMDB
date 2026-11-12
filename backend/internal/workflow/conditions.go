package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
)

// Conditions of a workflow definition are MET-45 condition nodes (CH18,
// E-229 option 1): the list of a version is combined with all (an empty
// list always holds), the optional trigger.filter is a single node. Both
// are checked with rules.ParseCondition when a definition is written and
// evaluated with rules.EvaluateCondition over EvaluationValues.

// maxValueDepth is the depth up to which EvaluationValues descends into
// nested objects of a run context (AK3 of WP-1087); deeper objects are a
// value of their path.
const maxValueDepth = 4

// errConditionInvalid fails a run whose stored condition is no MET-45
// node (a version deactivated by the migration of WP-1087 that still has
// a waiting run): the run fails instead of deciding silently.
var errConditionInvalid = errors.New("workflow condition is not a valid MET-45 condition")

// parseCondition checks one condition node strictly.
// validation: the texts of rules.ParseCondition, naming the path that fails.
func parseCondition(node any) (rules.Rule, error) {
	raw, err := json.Marshal(node)
	if err != nil {
		return rules.Rule{}, fmt.Errorf("condition: not JSON")
	}
	return rules.ParseCondition(raw)
}

// pathToken is one property of a parser path, e.g. rules[1] or field.
var pathToken = regexp.MustCompile(`^[a-z_]+(\[\d+\])*$`)

// conditionPath is the field of a refused condition: field joined with the
// path rules.ParseCondition puts in front of its message (conditions[0]
// with "rules[1].field: …" is conditions[0].rules[1].field).
func conditionPath(field, message string) string {
	parts := strings.Split(message, ": ")
	path := []string{field}
	for _, p := range parts[:len(parts)-1] {
		for _, token := range strings.Split(p, ".") {
			if !pathToken.MatchString(token) {
				return strings.Join(path, ".")
			}
			if token != "condition" {
				path = append(path, token)
			}
		}
	}
	return strings.Join(path, ".")
}

// conditionFieldError is the 422 of a refused condition at field.
func conditionFieldError(field, message string) *api.FieldError {
	return &api.FieldError{Field: conditionPath(field, message), Message: message, Status: http.StatusUnprocessableEntity}
}

// ValidateConditions checks every condition of a definition: a refused
// node is a 422 on conditions[i] with the path of the parser message.
func ValidateConditions(conditions []JSONMap) error {
	for i, c := range conditions {
		if _, err := parseCondition(map[string]any(c)); err != nil {
			return conditionFieldError(fmt.Sprintf("conditions[%d]", i), err.Error())
		}
	}
	return nil
}

// ValidateTrigger checks the optional filter of a trigger, a single MET-45
// node (422 on trigger.filter). It is evaluated from WP-1090 on.
func ValidateTrigger(trigger JSONMap) error {
	filter, has := trigger["filter"]
	if !has || filter == nil {
		return nil
	}
	if _, err := parseCondition(filter); err != nil {
		return conditionFieldError("trigger.filter", err.Error())
	}
	return nil
}

// ValidateDefinition checks trigger filter and conditions of a definition
// before it is written.
func ValidateDefinition(trigger JSONMap, conditions []JSONMap) error {
	if err := ValidateTrigger(trigger); err != nil {
		return err
	}
	return ValidateConditions(conditions)
}

// EvaluationValues is the flat map rules.EvaluateCondition reads for a run
// context: every key under its dot path (event.type, event.entity_id,
// data.to_state, ci.status, ci.attributes.rack_units, …). Nested objects
// are descended up to depth 4 and are a value of their own path as well,
// so not_empty holds for a present object; lists are a value.
func EvaluationValues(runCtx JSONMap) map[string]any {
	out := make(map[string]any, len(runCtx))
	flatten(out, "", map[string]any(runCtx), 1)
	return out
}

func flatten(out map[string]any, prefix string, m map[string]any, depth int) {
	for k, v := range m {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		out[path] = v
		if depth >= maxValueDepth {
			continue
		}
		switch child := v.(type) {
		case map[string]any:
			flatten(out, path, child, depth+1)
		case JSONMap:
			flatten(out, path, child, depth+1)
		}
	}
}

// conditionsHold evaluates the conditions of a version over the run
// context, combined with all. A stored node that is no MET-45 condition
// is an error, never a silent false.
func conditionsHold(conditions []JSONMap, runCtx JSONMap) (bool, error) {
	if len(conditions) == 0 {
		return true, nil
	}
	values := EvaluationValues(runCtx)
	for _, c := range conditions {
		cond, err := parseCondition(map[string]any(c))
		if err != nil {
			return false, errConditionInvalid
		}
		if !rules.EvaluateCondition(&cond, values) {
			return false, nil
		}
	}
	return true, nil
}

// met45Only are the operators of MET-45 the G1 evaluator did not know: a
// node with one of them was written in MET-45 form.
var met45Only = []string{rules.OpAll, rules.OpAny, rules.OpNot, rules.OpIn, rules.OpNotIn, rules.OpGt, rules.OpGte, rules.OpLt, rules.OpLte, rules.OpEmpty}

// MigrateLegacyCondition maps a condition of the G1 evaluator
// ({field, op, value} with eq, ne, exists, not_empty, contains, matches)
// to MET-45 under E-229 option 1, the rule of migration
// 000330_workflow_conditions_ast: eq, ne and contains are taken 1:1 (a
// missing op was eq), exists becomes not_empty, not_empty loses its value.
// A missing, null, array or object value of eq, ne or contains has no
// MET-45 form and is refused as well. matches and every other operator are
// refused (false): the definition is deactivated. A node already in MET-45
// form (a logic node or in, not_in, gt, gte, lt, lte, empty) stays as it
// is, so a second run of the migration changes nothing.
func MigrateLegacyCondition(c JSONMap) (JSONMap, bool) {
	if op, _ := c["op"].(string); slices.Contains(met45Only, op) {
		if _, err := parseCondition(map[string]any(c)); err != nil {
			return nil, false
		}
		return c, true
	}
	field, ok := c["field"].(string)
	if !ok || strings.TrimSpace(field) == "" {
		return nil, false
	}
	op := "eq"
	if raw, has := c["op"]; has {
		s, isString := raw.(string)
		if !isString {
			return nil, false
		}
		if s != "" {
			op = s
		}
	}
	var out JSONMap
	switch op {
	case "eq", "ne", "contains":
		out = JSONMap{"op": op, "field": field, "value": c["value"]}
	case "exists", "not_empty":
		out = JSONMap{"op": rules.OpNotEmpty, "field": field}
	default:
		return nil, false
	}
	if _, err := parseCondition(map[string]any(out)); err != nil {
		return nil, false
	}
	return out, true
}
