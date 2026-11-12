package compliance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/security"
)

// A compliance rule is one MET-45 condition node (SEC-03, CH18, E-229
// option 1): expression decides pass or fail, the optional applies_when
// whether the rule applies at all (with ci_type_id). Both read the inputs
// of a CI (Inputs): its columns, its attributes, its lifecycle state and
// the facts of its open findings (CMP-02).

// inputColumns are the CI columns a rule reads.
var inputColumns = []string{"name", "status", "manufacturer", "model", "serial_number", "management_ip",
	"firmware_version", "os_name", "os_version", "ci_type_id", "client_id", "site_id", "lifecycle_state"}

// Inputs of the open findings of a CI.
const (
	InputMaxOpenSeverity     = "finding.max_open_severity"
	InputMaxOpenSeverityRank = "finding.max_open_severity_rank"
	// InputOpenCountPrefix is followed by the kind of a finding.
	InputOpenCountPrefix = "finding.open_count."
)

// kindComplianceViolation is the kind of the findings compliance writes; it
// is no input, so a rule never reads its own results.
const kindComplianceViolation = "compliance_violation"

// severities of a finding by rank; none (0) is a CI without open finding.
var severities = []string{"none", "low", "medium", "high", "critical"}

// SeverityRank is the rank 0..4 of a severity (none .. critical).
func SeverityRank(severity string) int {
	if i := slices.Index(severities, severity); i > 0 {
		return i
	}
	return 0
}

// FindingFacts are the open findings of one CI (status open or
// acknowledged), without compliance_violation: the highest severity rank
// and the count per kind.
type FindingFacts struct {
	MaxSeverityRank int
	OpenCount       map[string]int
}

// inputKinds are the finding kinds of finding.open_count.<kind>.
func inputKinds() []string {
	out := make([]string, 0, len(security.Kinds))
	for _, k := range security.Kinds {
		if k != kindComplianceViolation {
			out = append(out, k)
		}
	}
	return out
}

// Inputs is the flat map of a CI that rules.EvaluateCondition reads: the
// columns of inputColumns, attributes.<key>, finding.max_open_severity,
// finding.max_open_severity_rank and finding.open_count.<kind> for every
// kind (0 without such finding).
func Inputs(item *ci.Item, facts FindingFacts) map[string]any {
	in := map[string]any{
		"name": item.Name, "status": item.Status, "manufacturer": item.Manufacturer, "model": item.Model,
		"serial_number": item.SerialNumber, "management_ip": item.ManagementIP, "firmware_version": item.FirmwareVersion,
		"os_name": item.OSName, "os_version": item.OSVersion, "ci_type_id": item.CITypeID, "client_id": item.ClientID,
		"site_id": item.SiteID, "lifecycle_state": item.LifecycleState,
	}
	for k, v := range item.Attributes {
		in["attributes."+k] = v
	}
	rank := min(max(facts.MaxSeverityRank, 0), len(severities)-1)
	in[InputMaxOpenSeverity] = severities[rank]
	in[InputMaxOpenSeverityRank] = rank
	for _, kind := range inputKinds() {
		in[InputOpenCountPrefix+kind] = facts.OpenCount[kind]
	}
	return in
}

// isInput reports whether a rule may read field.
func isInput(field string) bool {
	switch {
	case slices.Contains(inputColumns, field), field == InputMaxOpenSeverity, field == InputMaxOpenSeverityRank:
		return true
	case strings.HasPrefix(field, "attributes."):
		return len(field) > len("attributes.")
	case strings.HasPrefix(field, InputOpenCountPrefix):
		return slices.Contains(inputKinds(), strings.TrimPrefix(field, InputOpenCountPrefix))
	}
	return false
}

// parseRuleCondition checks a rule condition strictly.
// validation: the texts of rules.ParseCondition, naming the path that fails.
func parseRuleCondition(node JSONMap) (rules.Rule, error) {
	raw, err := json.Marshal(node)
	if err != nil {
		return rules.Rule{}, fmt.Errorf("condition: not JSON")
	}
	return rules.ParseCondition(raw)
}

// unknownInput returns the path and the text of the first comparison of
// cond that reads no input (empty path: none).
// validation: a fixed text naming the field of the rule.
func unknownInput(cond *rules.Rule, path string) (at, message string) {
	if slices.Contains(rules.LogicOps, cond.Op) {
		for i := range cond.Rules {
			if p, m := unknownInput(&cond.Rules[i], fmt.Sprintf("%s.rules[%d]", path, i)); p != "" {
				return p, m
			}
		}
		return "", ""
	}
	if !isInput(cond.Field) {
		return path + ".field", fmt.Sprintf("%q is no input of a compliance rule", cond.Field)
	}
	return "", ""
}

// pathToken is one property of a parser path, e.g. rules[1] or field.
var pathToken = regexp.MustCompile(`^[a-z_]+(\[\d+\])*$`)

// conditionPath joins field with the path rules.ParseCondition puts in
// front of its message (expression with "rules[1].field: …" is
// expression.rules[1].field).
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

// ruleFieldError is the 422 of a refused condition.
func ruleFieldError(field, message string) *api.FieldError {
	return &api.FieldError{Field: field, Message: message, Status: http.StatusUnprocessableEntity}
}

// ValidateCondition checks the condition of field (expression or
// applies_when): a MET-45 node whose comparisons read only inputs of
// Inputs. A refused node is a 422 on the path of the error, e.g.
// expression.rules[1].field.
func ValidateCondition(field string, node JSONMap) error {
	cond, err := parseRuleCondition(node)
	if err != nil {
		return ruleFieldError(conditionPath(field, err.Error()), err.Error())
	}
	if at, message := unknownInput(&cond, field); at != "" {
		return ruleFieldError(at, message)
	}
	return nil
}

// holds evaluates a stored condition over the inputs of a CI; ok is false
// for a condition that is no MET-45 node (a rule the migration of WP-1125
// deactivated and someone activated again).
func holds(node JSONMap, inputs map[string]any) (result, ok bool) {
	cond, err := parseRuleCondition(node)
	if err != nil {
		return false, false
	}
	return rules.EvaluateCondition(&cond, inputs), true
}
