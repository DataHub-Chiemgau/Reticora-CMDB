package monitoring

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
)

// The threshold language of the alert rules (ALR-05, MET-45, E-201,
// WP-985): every stage of a rule (critical, warning, info) and its clear
// condition is a MET-45 condition ($defs/condition of api/rules.schema.json,
// rules.ParseCondition) over the metric context of a series. The context
// holds the aggregates of the samples of the series over the rule's
// eval_window; a condition may only name its fields.

// The fields of the metric context.
const (
	// FieldValue is the latest value of the series.
	FieldValue = "value"
	// FieldAvg, FieldMax and FieldMin aggregate the values in the window.
	FieldAvg = "avg"
	FieldMax = "max"
	FieldMin = "min"
	// FieldDelta is the latest minus the first value in the window.
	FieldDelta = "delta"
	// FieldRate is the increase per second over the window; a drop of the
	// value is a counter reset, from which the new value counts (never
	// negative). A window with one sample has no rate (absent field).
	FieldRate = "rate"
)

// MetricContextFields are the fields a condition of an alert rule may name.
var MetricContextFields = []string{FieldValue, FieldAvg, FieldMax, FieldMin, FieldDelta, FieldRate}

// Severities of the stages, in the order they are evaluated.
const (
	SeverityCritical = "critical"
	SeverityWarning  = "warning"
	SeverityInfo     = "info"
)

// StageOrder is the order of evaluation of the stages: the first one that
// holds is the severity of the alarm.
var StageOrder = []string{SeverityCritical, SeverityWarning, SeverityInfo}

// severityRank orders the severities, lowest first.
func severityRank(sev string) int {
	i := slices.Index(StageOrder, sev)
	if i < 0 {
		return 0
	}
	return len(StageOrder) - i
}

// Fields of the conditions of a rule (and of their 422 errors).
const (
	FieldCriticalCondition = "critical_condition"
	FieldWarningCondition  = "warning_condition"
	FieldInfoCondition     = "info_condition"
	FieldClearCondition    = "clear_condition"
)

// stageField is the field of the condition of a stage.
func stageField(severity string) string { return severity + "_condition" }

// DefaultEvalWindow is the window of a rule that names none.
const DefaultEvalWindow = 5 * time.Minute

// MaxEvalWindow bounds eval_window (CHECK alert_rule_eval_window_check).
const MaxEvalWindow = 24 * time.Hour

// LegacyConditions are the operators of the old rule form (condition,
// threshold), deprecated by WP-985.
var LegacyConditions = []string{"gt", "lt", "eq"}

// SeriesContext is the metric context of a series over the window of a
// rule: the aggregates of its samples in time order.
type SeriesContext struct {
	Value float64
	Avg   float64
	Max   float64
	Min   float64
	Delta float64
	// Rate is nil for a window of one sample (or of samples at one time).
	Rate *float64
	// Samples is the number of samples in the window.
	Samples int
}

// Values are the fields of the context for rules.EvaluateCondition; an
// absent rate is an absent field.
func (c *SeriesContext) Values() map[string]any {
	v := map[string]any{FieldValue: c.Value, FieldAvg: c.Avg, FieldMax: c.Max, FieldMin: c.Min, FieldDelta: c.Delta}
	if c.Rate != nil {
		v[FieldRate] = *c.Rate
	}
	return v
}

// timedValue is a sample of a series.
type timedValue struct {
	at    time.Time
	value float64
}

// contextOf is the metric context of the samples of a series, oldest first.
// The increase of the rate adds every rise; a drop is a counter reset and
// adds the new value (the counter restarted from zero).
func contextOf(samples []timedValue) SeriesContext {
	if len(samples) == 0 {
		return SeriesContext{}
	}
	first, last := samples[0], samples[len(samples)-1]
	c := SeriesContext{Value: last.value, Max: first.value, Min: first.value, Delta: last.value - first.value,
		Samples: len(samples)}
	var sum, increase float64
	for i, s := range samples {
		sum += s.value
		c.Max = math.Max(c.Max, s.value)
		c.Min = math.Min(c.Min, s.value)
		if i == 0 {
			continue
		}
		if prev := samples[i-1].value; s.value >= prev {
			increase += s.value - prev
		} else {
			increase += s.value
		}
	}
	c.Avg = sum / float64(len(samples))
	if span := last.at.Sub(first.at).Seconds(); span > 0 {
		rate := increase / span
		c.Rate = &rate
	}
	return c
}

// ruleStages are the parsed conditions of a rule.
type ruleStages struct {
	// stages by severity (StageOrder); nil is a stage the rule has not.
	stages map[string]*rules.Rule
	clear  *rules.Rule
}

// match is the severity of the first stage that holds for the values;
// empty when none holds.
func (s *ruleStages) match(values map[string]any) string {
	for _, sev := range StageOrder {
		if c := s.stages[sev]; c != nil && rules.EvaluateCondition(c, values) {
			return sev
		}
	}
	return ""
}

// clears reports whether an open alarm is resolved: its clear condition
// holds, or (without one) no stage holds.
func (s *ruleStages) clears(values map[string]any) bool {
	if s.clear != nil {
		return rules.EvaluateCondition(s.clear, values)
	}
	return s.match(values) == ""
}

// conditionOf is the stage of the rule for a severity, or the clear
// condition for FieldClearCondition.
func (r *AlertRule) conditionOf(field string) *json.RawMessage {
	switch field {
	case FieldCriticalCondition:
		return &r.CriticalCondition
	case FieldWarningCondition:
		return &r.WarningCondition
	case FieldInfoCondition:
		return &r.InfoCondition
	case FieldClearCondition:
		return &r.ClearCondition
	}
	return nil
}

// isNull reports whether a raw condition is absent or null.
func isNull(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || string(t) == "null"
}

// stages parses the conditions of the rule (checked when the rule was
// written).
func (r *AlertRule) stages() (*ruleStages, error) {
	out := &ruleStages{stages: map[string]*rules.Rule{}}
	for _, sev := range StageOrder {
		c, err := ParseRuleCondition(stageField(sev), *r.conditionOf(stageField(sev)))
		if err != nil {
			return nil, err
		}
		if c != nil {
			out.stages[sev] = c
		}
	}
	c, err := ParseRuleCondition(FieldClearCondition, r.ClearCondition)
	if err != nil {
		return nil, err
	}
	out.clear = c
	return out, nil
}

// pathToken is one property of a parser path, e.g. rules[1] or field.
var pathToken = regexp.MustCompile(`^[a-z_]+(\[\d+\])*$`)

// conditionPath joins field with the path rules.ParseCondition puts in
// front of its message ("rules[1].field: …" of warning_condition is
// warning_condition.rules[1].field), like compliance.conditionPath.
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

// ParseRuleCondition checks the condition of field (a stage or the clear
// condition): null or absent is no condition (nil); otherwise a MET-45
// condition whose comparisons name only fields of the metric context. A
// refused condition is a 422 on the path of the error, e.g.
// warning_condition.rules[1].field.
// validation: the texts of rules.ParseCondition and fixed texts naming the field.
func ParseRuleCondition(field string, raw json.RawMessage) (*rules.Rule, error) {
	if isNull(raw) {
		return nil, nil
	}
	cond, err := rules.ParseCondition(raw)
	if err != nil {
		msg := err.Error()
		parts := strings.Split(msg, ": ")
		return nil, unprocessableField(conditionPath(field, msg), parts[len(parts)-1])
	}
	if path, message := unknownMetricField(&cond, field); path != "" {
		return nil, unprocessableField(path, message)
	}
	return &cond, nil
}

// unknownMetricField returns the path and the text of the first comparison
// of cond that names no field of the metric context (empty path: none).
func unknownMetricField(cond *rules.Rule, path string) (at, message string) {
	if slices.Contains(rules.LogicOps, cond.Op) {
		for i := range cond.Rules {
			if p, m := unknownMetricField(&cond.Rules[i], fmt.Sprintf("%s.rules[%d]", path, i)); p != "" {
				return p, m
			}
		}
		return "", ""
	}
	if !slices.Contains(MetricContextFields, cond.Field) {
		return path + ".field", fmt.Sprintf("%q is no field of the metric context; allowed are %s",
			cond.Field, strings.Join(MetricContextFields, ", "))
	}
	return "", ""
}

// LegacyCondition is the stage condition of the old form: value compared
// with the threshold.
func LegacyCondition(condition string, threshold float64) json.RawMessage {
	raw, _ := json.Marshal(struct {
		Op    string  `json:"op"`
		Field string  `json:"field"`
		Value float64 `json:"value"`
	}{condition, FieldValue, threshold})
	return raw
}

// legacyForm is the old form of the rule (condition, threshold, severity)
// when the rule has one: exactly one stage that compares value with gt, lt
// or eq against a number, and no clear condition.
func (r *AlertRule) legacyForm() (condition string, threshold float64, severity string, ok bool) {
	if !isNull(r.ClearCondition) {
		return "", 0, "", false
	}
	for _, sev := range StageOrder {
		raw := *r.conditionOf(stageField(sev))
		if isNull(raw) {
			continue
		}
		if severity != "" {
			return "", 0, "", false
		}
		var node map[string]any
		if err := json.Unmarshal(raw, &node); err != nil || len(node) != 3 {
			return "", 0, "", false
		}
		op, _ := node["op"].(string)
		field, _ := node["field"].(string)
		value, isNumber := node["value"].(float64)
		if !slices.Contains(LegacyConditions, op) || field != FieldValue || !isNumber {
			return "", 0, "", false
		}
		condition, threshold, severity = op, value, sev
	}
	return condition, threshold, severity, severity != ""
}

// syncLegacy sets condition, threshold and severity from the stages: their
// old form, or empty when the rule has none.
func (r *AlertRule) syncLegacy() {
	condition, threshold, severity, ok := r.legacyForm()
	if !ok {
		condition, threshold, severity = "", 0, ""
	}
	r.Condition, r.Threshold, r.Severity = condition, threshold, severity
}

// hasStage reports whether the rule has at least one stage.
func (r *AlertRule) hasStage() bool {
	for _, sev := range StageOrder {
		if !isNull(*r.conditionOf(stageField(sev))) {
			return true
		}
	}
	return false
}

// normalizeRule brings a rule into its stored form: a rule of the old form
// (condition and threshold, no stage) gets the stage of its severity
// (warning when none is given); null conditions are nil; the old fields
// follow the stages (syncLegacy). It checks every condition (422 with the
// path), that at least one stage is set and eval_window.
// validation: field errors with fixed texts.
func normalizeRule(r *AlertRule) error {
	r.Actions = ruleActions(r)
	for _, f := range []string{FieldCriticalCondition, FieldWarningCondition, FieldInfoCondition, FieldClearCondition} {
		if c := r.conditionOf(f); isNull(*c) {
			*c = nil
		}
	}
	if r.Condition != "" {
		if r.Severity == "" {
			r.Severity = SeverityWarning
		}
		if r.hasStage() {
			// The old form next to the stages only as their echo (a rule
			// read and sent back).
			condition, threshold, severity, ok := r.legacyForm()
			if !ok || condition != r.Condition || threshold != r.Threshold || severity != r.Severity {
				return unprocessableField("condition", "give either the stages or the deprecated condition and threshold")
			}
		}
		if !slices.Contains(LegacyConditions, r.Condition) {
			return unprocessableField("condition", "must be one of gt, lt or eq")
		}
		if !slices.Contains(StageOrder, r.Severity) {
			return unprocessableField("severity", "must be one of critical, warning or info")
		}
		*r.conditionOf(stageField(r.Severity)) = LegacyCondition(r.Condition, r.Threshold)
	} else if r.Severity != "" && !r.hasStage() {
		return unprocessableField("condition", "is required with severity")
	}
	if !r.hasStage() {
		return unprocessableField(FieldCriticalCondition,
			"at least one of critical_condition, warning_condition and info_condition is required")
	}
	for _, f := range []string{FieldCriticalCondition, FieldWarningCondition, FieldInfoCondition, FieldClearCondition} {
		c := r.conditionOf(f)
		parsed, err := ParseRuleCondition(f, *c)
		if err != nil {
			return err
		}
		if parsed != nil {
			// Stored compact, so the old form is recognized and the
			// response is stable.
			var buf bytes.Buffer
			if err := json.Compact(&buf, *c); err == nil {
				*c = buf.Bytes()
			}
		}
	}
	if err := checkEvalWindow(r.EvalWindow); err != nil {
		return err
	}
	r.syncLegacy()
	return nil
}

// checkEvalWindow refuses a window outside 0..MaxEvalWindow (422).
func checkEvalWindow(d time.Duration) error {
	if d < 0 || d > MaxEvalWindow {
		return &api.FieldError{Field: "eval_window", Message: "must be between 0s and 24h",
			Status: http.StatusUnprocessableEntity}
	}
	return nil
}

// applyUpdate applies a partial update to rule and normalizes it
// (normalizeRule). The old form (condition, threshold, severity) replaces
// the stages by the stage of the severity; what it does not name comes from
// the old form of the rule, and a rule without one needs condition and
// threshold. The old form and the stages in one request are 422.
// validation: field errors with fixed texts, the parse error of a duration.
func applyUpdate(rule *AlertRule, req *UpdateAlertRuleRequest) error {
	if req.Name != nil {
		rule.Name = *req.Name
	}
	if req.Duration != nil {
		d, err := time.ParseDuration(*req.Duration)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", *req.Duration, err)
		}
		if err := checkAlertDuration(d); err != nil {
			return err
		}
		rule.Duration = d
	}
	if req.EvalWindow != nil {
		d, err := time.ParseDuration(*req.EvalWindow)
		if err != nil {
			return &api.FieldError{Field: "eval_window", Message: "must be a duration such as 5m",
				Status: http.StatusUnprocessableEntity}
		}
		rule.EvalWindow = d
	}
	if req.Enabled != nil {
		rule.Enabled = *req.Enabled
	}
	if req.ParsedScope != nil {
		rule.Scope = *req.ParsedScope
	}
	if req.GroupBy != nil {
		rule.GroupBy = append([]string{}, *req.GroupBy...)
	}
	switch {
	case req.ParsedActions != nil:
		a := *req.ParsedActions
		rule.Actions = &a
	case len(req.Actions) > 0:
		a, err := events.ParseAlarmActions(req.Actions)
		if err != nil {
			return err
		}
		rule.Actions = &a
	}
	stageFields := map[string]json.RawMessage{FieldCriticalCondition: req.CriticalCondition,
		FieldWarningCondition: req.WarningCondition, FieldInfoCondition: req.InfoCondition,
		FieldClearCondition: req.ClearCondition}
	stagesGiven := false
	for _, raw := range stageFields {
		if raw != nil {
			stagesGiven = true
		}
	}
	legacyGiven := req.Condition != nil || req.Threshold != nil || req.Severity != nil
	switch {
	case legacyGiven && stagesGiven:
		return unprocessableField("condition", "give either the stages or the deprecated condition and threshold")
	case legacyGiven:
		condition, threshold, severity, ok := rule.legacyForm()
		if !ok && (req.Condition == nil || req.Threshold == nil) {
			return unprocessableField("condition", "the rule has stages; give condition and threshold or the stages")
		}
		if req.Condition != nil {
			condition = *req.Condition
		}
		if req.Threshold != nil {
			threshold = *req.Threshold
		}
		if req.Severity != nil {
			severity = *req.Severity
		}
		rule.CriticalCondition, rule.WarningCondition, rule.InfoCondition, rule.ClearCondition = nil, nil, nil, nil
		rule.Condition, rule.Threshold, rule.Severity = condition, threshold, severity
	default:
		for f, raw := range stageFields {
			if raw != nil {
				*rule.conditionOf(f) = raw
			}
		}
		rule.Condition, rule.Threshold, rule.Severity = "", 0, ""
	}
	return normalizeRule(rule)
}
