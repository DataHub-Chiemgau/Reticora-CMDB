package monitoring

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The threshold language of the alert rules (ALR-05, MET-45, MON-04,
// WP-985) on the memory stores: stages, metric context, hysteresis, the old
// form and the gate monitoring_network (E-151).

// stageSink is an alarm sink that follows the lifecycle with severities
// (alarm.Service.Raise): a higher severity raises an open alarm, a lower one
// changes nothing.
type stageSink struct {
	mu     sync.Mutex
	alarms map[string]*ActiveAlarm
	log    []string
}

func newStageSink() *stageSink { return &stageSink{alarms: map[string]*ActiveAlarm{}} }

func (s *stageSink) Active(_ context.Context, _, ruleID string) (map[string]ActiveAlarm, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]ActiveAlarm{}
	prefix := RuleDedupKey(ruleID, "")
	for key, a := range s.alarms {
		if strings.HasPrefix(key, prefix) {
			out[strings.TrimPrefix(key, prefix)] = *a
		}
	}
	return out, nil
}

func (s *stageSink) Raise(_ context.Context, _ string, in *RuleAlarm) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := RuleDedupKey(in.RuleID, in.SeriesKey)
	a, ok := s.alarms[key]
	switch {
	case !ok && in.Pending:
		s.alarms[key] = &ActiveAlarm{State: AlarmPending, FirstSeenAt: in.ObservedAt, Severity: in.Severity}
		s.log = append(s.log, "pend "+in.Severity)
	case !ok:
		s.alarms[key] = &ActiveAlarm{State: AlarmOpen, FirstSeenAt: in.ObservedAt, Severity: in.Severity}
		s.log = append(s.log, "open "+in.Severity)
	case a.State == AlarmPending && !in.Pending:
		a.State = AlarmOpen
		if severityRank(in.Severity) > severityRank(a.Severity) {
			a.Severity = in.Severity
		}
		s.log = append(s.log, "open "+a.Severity)
	case a.State == AlarmOpen && severityRank(in.Severity) > severityRank(a.Severity):
		a.Severity = in.Severity
		s.log = append(s.log, "raise "+in.Severity)
	}
	return nil
}

func (s *stageSink) Clear(_ context.Context, _, ruleID, seriesKey string, _ time.Time, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := RuleDedupKey(ruleID, seriesKey)
	if _, ok := s.alarms[key]; ok {
		delete(s.alarms, key)
		s.log = append(s.log, "clear "+reason)
	}
	return nil
}

// state is the state and severity of the only alarm ("" when none).
func (s *stageSink) state() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.alarms {
		return a.State + " " + a.Severity
	}
	return ""
}

// stageEnv is a memory metric store, one rule and the evaluator on a
// clock that moves a minute per sample.
type stageEnv struct {
	t     *testing.T
	store *MemoryMetricStore
	sink  *stageSink
	ev    *Evaluator
	now   time.Time
}

func newStageEnv(t *testing.T, rule *AlertRule) *stageEnv {
	t.Helper()
	alerts := NewAlertManager()
	rule.OrgID, rule.Enabled, rule.MetricName, rule.Name = "org-1", true, "cpu", "cpu"
	if _, err := alerts.CreateRule(t.Context(), rule); err != nil {
		t.Fatal(err)
	}
	e := &stageEnv{t: t, store: NewMemoryMetricStore(), sink: newStageSink(),
		now: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
	e.ev = NewEvaluator(e.store, alerts, e.sink)
	e.ev.Now = func() time.Time { return e.now }
	return e
}

// step stores a value a minute after the last one and evaluates; it
// returns the state of the alarm afterwards.
func (e *stageEnv) step(value float64) string {
	e.t.Helper()
	e.now = e.now.Add(time.Minute)
	if err := e.store.Ingest(e.t.Context(), []Metric{{OrgID: "org-1", CIID: "ci-a", Name: "cpu", Value: value,
		Timestamp: e.now}}); err != nil {
		e.t.Fatal(err)
	}
	e.ev.Evaluate(e.t.Context())
	return e.sink.state()
}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func TestThresholdHysteresis(t *testing.T) {
	e := newStageEnv(t, &AlertRule{CriticalCondition: raw(`{"op":"gt","field":"value","value":90}`),
		ClearCondition: raw(`{"op":"lt","field":"value","value":85}`)})
	for _, step := range []struct {
		value float64
		want  string
	}{
		{80, ""},
		{91, "open critical"},
		{88, "open critical"}, // no stage holds, the clear condition neither
		{86, "open critical"},
		{84, ""}, // the clear condition holds
		{88, ""}, // below the stage: nothing new
	} {
		if got := e.step(step.value); got != step.want {
			t.Fatalf("value %g: alarm %q, want %q (log %v)", step.value, got, step.want, e.sink.log)
		}
	}
	// Without a clear condition the alarm is resolved once no stage holds.
	e = newStageEnv(t, &AlertRule{CriticalCondition: raw(`{"op":"gt","field":"value","value":90}`)})
	if got := e.step(91); got != "open critical" {
		t.Fatalf("value 91: %q", got)
	}
	if got := e.step(88); got != "" {
		t.Errorf("value 88 without clear condition: %q, want resolved", got)
	}
}

func TestThresholdRateCounterReset(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	c := contextOf([]timedValue{{t0, 4294967200}, {t0.Add(10 * time.Second), 100}})
	if c.Rate == nil || *c.Rate < 0 {
		t.Fatalf("rate over a counter reset: %v, want non-negative", c.Rate)
	}
	if *c.Rate != 10 {
		t.Errorf("rate over a counter reset: %g, want 10 (the new value 100 over 10 s)", *c.Rate)
	}
	// Rise, reset and rise: 100 + 50 + 30 over 30 s.
	c = contextOf([]timedValue{{t0, 1000}, {t0.Add(10 * time.Second), 1100}, {t0.Add(20 * time.Second), 50},
		{t0.Add(30 * time.Second), 80}})
	if c.Rate == nil || *c.Rate != 6 {
		t.Errorf("rate with a reset in between: %v, want 6", c.Rate)
	}
	if c.Delta != -920 || c.Max != 1100 || c.Min != 50 || c.Avg != 557.5 || c.Value != 80 {
		t.Errorf("context %+v", c)
	}
	// One sample has no rate; a condition on rate does not hold then.
	c = contextOf([]timedValue{{t0, 5}})
	if c.Rate != nil {
		t.Errorf("rate of one sample: %v, want none", *c.Rate)
	}
	cond, err := ParseRuleCondition(FieldWarningCondition, raw(`{"op":"gte","field":"rate","value":0}`))
	if err != nil || rules.EvaluateCondition(cond, c.Values()) {
		t.Errorf("rate gte 0 without rate: err %v, holds %v", err, rules.EvaluateCondition(cond, c.Values()))
	}

	// Through the evaluator: the rate over the window of a counter that
	// wraps raises no alarm on "rate lt 0" and stays positive.
	e := newStageEnv(t, &AlertRule{WarningCondition: raw(`{"op":"lt","field":"rate","value":0}`),
		EvalWindow: 10 * time.Minute})
	for _, v := range []float64{4294967000, 4294967200, 100, 300} {
		if got := e.step(v); got != "" {
			t.Fatalf("counter %g: alarm %q, want none", v, got)
		}
	}
	points, err := e.store.LatestBySeries(t.Context(), &SeriesQuery{OrgID: "org-1", Name: "cpu", From: e.now.Add(-time.Hour),
		To: e.now, Window: 10 * time.Minute})
	if err != nil || len(points) != 1 || points[0].Context.Rate == nil {
		t.Fatalf("series: %+v %v", points, err)
	}
	// 200 + 100 + 200 over 3 minutes.
	if got := *points[0].Context.Rate; got != 500.0/180 {
		t.Errorf("rate of the series: %g, want %g", got, 500.0/180)
	}
}

func TestThresholdStageEscalationOnly(t *testing.T) {
	e := newStageEnv(t, &AlertRule{
		CriticalCondition: raw(`{"op":"gt","field":"value","value":90}`),
		WarningCondition:  raw(`{"op":"gt","field":"value","value":70}`),
		InfoCondition:     raw(`{"op":"gt","field":"value","value":50}`),
	})
	for _, step := range []struct {
		value float64
		want  string
	}{
		{75, "open warning"},
		{95, "open critical"}, // warning → critical raises
		{75, "open critical"}, // critical → warning does not lower
		{55, "open critical"}, // nor info
		{40, ""},              // no stage: resolved
	} {
		if got := e.step(step.value); got != step.want {
			t.Fatalf("value %g: alarm %q, want %q (log %v)", step.value, got, step.want, e.sink.log)
		}
	}
	// With a duration the higher stage has to hold for it, too.
	e = newStageEnv(t, &AlertRule{
		CriticalCondition: raw(`{"op":"gt","field":"value","value":90}`),
		WarningCondition:  raw(`{"op":"gt","field":"value","value":70}`),
		Duration:          2 * time.Minute,
	})
	for _, step := range []struct {
		value float64
		want  string
	}{
		{75, "pending warning"},
		{75, "pending warning"},
		{75, "open warning"},
		{95, "open warning"}, // the critical stage starts
		{95, "open warning"},
		{95, "open critical"}, // held for 2 minutes
	} {
		if got := e.step(step.value); got != step.want {
			t.Fatalf("duration: value %g: alarm %q, want %q (log %v)", step.value, got, step.want, e.sink.log)
		}
	}
}

func TestThresholdRejectsUnknownField(t *testing.T) {
	h := NewHandler(NewMemoryMetricStore())
	r := chi.NewRouter()
	h.RegisterRoutes(r)
	send := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1"}))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	const base = `{"name":"r","metric_name":"m"`
	for body, field := range map[string]string{
		base + `,"warning_condition":{"op":"all","rules":[{"op":"gt","field":"value","value":1},{"op":"gt","field":"cpu","value":1}]}}`: "warning_condition.rules[1].field",
		base + `,"critical_condition":{"op":"gt","field":"cpu","value":1}}`:                                                             "critical_condition.field",
		base + `,"critical_condition":{"op":"gt","field":"value","value":"x"}}`:                                                         "critical_condition.value",
		base + `,"critical_condition":{"op":"required"}}`:                                                                               "critical_condition.op",
		base + `,"info_condition":[1]}`:                                                                                                 "info_condition",
		base + `,"warning_condition":{"op":"gt","field":"value","value":1},"clear_condition":{"op":"lt","field":"avg2","value":1}}`:     "clear_condition.field",
		base + `}`: "critical_condition",
		base + `,"warning_condition":{"op":"gt","field":"value","value":1},"eval_window":"25h"}`:            "eval_window",
		base + `,"warning_condition":{"op":"gt","field":"value","value":1},"condition":"lt","threshold":3}`: "condition",
		base + `,"condition":"ge","threshold":3}`:                                                           "condition",
		base + `,"condition":"gt","threshold":3,"severity":"fatal"}`:                                        "severity",
	} {
		w := send(http.MethodPost, "/api/v1/monitoring/alerts", body)
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"field":"`+field+`"`) {
			t.Errorf("%s: %d %s, want 422 %s", body, w.Code, w.Body.String(), field)
		}
	}

	// A valid rule with every field of the metric context.
	w := send(http.MethodPost, "/api/v1/monitoring/alerts", base+`,"critical_condition":{"op":"any","rules":[
		{"op":"gt","field":"value","value":90},{"op":"gt","field":"avg","value":80},{"op":"gt","field":"max","value":99},
		{"op":"lt","field":"min","value":0},{"op":"gt","field":"delta","value":5},{"op":"not","rules":[{"op":"lte","field":"rate","value":3}]}]},
		"clear_condition":{"op":"lt","field":"value","value":85},"eval_window":"10m"}`)
	if w.Code != http.StatusCreated || strings.Contains(w.Body.String(), `"condition"`) ||
		!strings.Contains(w.Body.String(), `"eval_window":"10m0s"`) || !strings.Contains(w.Body.String(), `"warning_condition":null`) {
		t.Fatalf("valid rule: %d %s", w.Code, w.Body.String())
	}
	id := extractJSONField(w.Body.String(), "id")
	if w = send(http.MethodPatch, "/api/v1/monitoring/alerts/"+id,
		`{"clear_condition":{"op":"lt","field":"cpu","value":1}}`); w.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(w.Body.String(), `"field":"clear_condition.field"`) {
		t.Errorf("PATCH unknown field: %d %s", w.Code, w.Body.String())
	}
	if w = send(http.MethodPatch, "/api/v1/monitoring/alerts/"+id, `{"critical_condition":null}`); w.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(w.Body.String(), `"field":"critical_condition"`) {
		t.Errorf("PATCH without any stage: %d %s", w.Code, w.Body.String())
	}
	if w = send(http.MethodPatch, "/api/v1/monitoring/alerts/"+id, `{"threshold":3}`); w.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(w.Body.String(), `"field":"condition"`) {
		t.Errorf("PATCH threshold of a rule without old form: %d %s", w.Code, w.Body.String())
	}

	// The old form is taken over into the stage of its severity and comes
	// back in the response; a PATCH of the threshold moves the stage.
	w = send(http.MethodPost, "/api/v1/monitoring/alerts", base+`,"condition":"gt","threshold":90,"severity":"critical"}`)
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), `"critical_condition":{"op":"gt","field":"value","value":90}`) ||
		!strings.Contains(w.Body.String(), `"condition":"gt"`) || !strings.Contains(w.Body.String(), `"threshold":90`) ||
		!strings.Contains(w.Body.String(), `"severity":"critical"`) || !strings.Contains(w.Body.String(), `"eval_window":"5m0s"`) {
		t.Fatalf("old form: %d %s", w.Code, w.Body.String())
	}
	id = extractJSONField(w.Body.String(), "id")
	w = send(http.MethodPatch, "/api/v1/monitoring/alerts/"+id, `{"threshold":95,"severity":"warning"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"warning_condition":{"op":"gt","field":"value","value":95}`) ||
		!strings.Contains(w.Body.String(), `"critical_condition":null`) || !strings.Contains(w.Body.String(), `"threshold":95`) {
		t.Errorf("PATCH old form: %d %s", w.Code, w.Body.String())
	}
	// A rule with a clear condition has no old form any more.
	w = send(http.MethodPatch, "/api/v1/monitoring/alerts/"+id, `{"clear_condition":{"op":"lt","field":"value","value":80}}`)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"threshold"`) || strings.Contains(w.Body.String(), `"severity"`) {
		t.Errorf("PATCH clear condition: %d %s", w.Code, w.Body.String())
	}
}

// legacyConditionMet is the evaluation of the old form (conditionMet of
// evaluator.go before WP-985).
func legacyConditionMet(condition string, value, threshold float64) bool {
	switch condition {
	case "gt":
		return value > threshold
	case "lt":
		return value < threshold
	case "eq":
		return value == threshold
	}
	return false
}

func TestLegacyRulesEquivalent(t *testing.T) {
	for _, condition := range LegacyConditions {
		for _, threshold := range []float64{0, 10, -3.5, 90.25, 1e9} {
			for _, sev := range StageOrder {
				rule := AlertRule{Condition: condition, Threshold: threshold, Severity: sev}
				if err := normalizeRule(&rule); err != nil {
					t.Fatalf("%s %g: %v", condition, threshold, err)
				}
				// The migration builds the same stage (000348).
				want := `{"op":"` + condition + `","field":"value","value":` + jsonNumber(threshold) + `}`
				if got := string(*rule.conditionOf(stageField(sev))); got != want {
					t.Errorf("stage of %s %g: %s, want %s", condition, threshold, got, want)
				}
				stages, err := rule.stages()
				if err != nil {
					t.Fatal(err)
				}
				for _, v := range []float64{threshold - 1, threshold - 1e-9, threshold, threshold + 1e-9, threshold + 1} {
					c := SeriesContext{Value: v, Avg: v, Max: v, Min: v}
					holds := stages.match(c.Values()) == sev
					if old := legacyConditionMet(condition, v, threshold); holds != old {
						t.Errorf("%s %g at %g: AST %v, old %v", condition, threshold, v, holds, old)
					}
					// Without a clear condition the alarm clears when the
					// old condition no longer holds.
					if stages.clears(c.Values()) == legacyConditionMet(condition, v, threshold) {
						t.Errorf("%s %g at %g: clears %v", condition, threshold, v, stages.clears(c.Values()))
					}
				}
				if c, thr, s, ok := rule.legacyForm(); !ok || c != condition || thr != threshold || s != sev {
					t.Errorf("old form of %s %g: %s %g %s %v", condition, threshold, c, thr, s, ok)
				}
			}
		}
	}
}

func jsonNumber(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

// features answers Check from a map of organizations.
type features map[string]bool

func (f features) Check(_ context.Context, orgID, key string) (entitlement.Entitlement, bool, error) {
	return entitlement.Entitlement{}, key == entitlement.FeatureMonitoringNetwork && f[orgID], nil
}

// failingFeatures fails every lookup.
type failingFeatures struct{}

func (failingFeatures) Check(context.Context, string, string) (entitlement.Entitlement, bool, error) {
	return entitlement.Entitlement{}, false, errors.New("entitlement store down")
}

func TestThresholdRulesRequireMonitoringNetwork(t *testing.T) {
	if got := ThresholdFeature(); got != entitlement.FeatureMonitoringNetwork {
		t.Fatalf("feature of the threshold rules: %s, want %s (E-151)", got, entitlement.FeatureMonitoringNetwork)
	}
	gate := features{}
	e := newStageEnv(t, &AlertRule{CriticalCondition: raw(`{"op":"gt","field":"value","value":90}`)})
	e.ev.WithFeatures(gate)
	if got := e.step(95); got != "" {
		t.Fatalf("without monitoring_network: alarm %q, want none", got)
	}
	rules, err := e.ev.alerts.ListRules(t.Context(), "org-1")
	if err != nil || len(rules) != 1 {
		t.Fatalf("the rule stays stored: %v %v", rules, err)
	}
	gate["org-1"] = true
	if got := e.step(95); got != "open critical" {
		t.Fatalf("with monitoring_network: alarm %q, want open critical", got)
	}
	// After a downgrade the rule raises nothing and its alarm is resolved.
	gate["org-1"] = false
	if got := e.step(95); got != "" {
		t.Errorf("after the downgrade: alarm %q, want resolved", got)
	}
	if last := e.sink.log[len(e.sink.log)-1]; last != "clear "+ClearReasonNotEntitled {
		t.Errorf("last change %q, want clear %s", last, ClearReasonNotEntitled)
	}
}

// TestThresholdEntitlementLookupFailureKeepsAlarms: a failed entitlement
// lookup is no downgrade; the open alarm stays open (no not_entitled clear).
func TestThresholdEntitlementLookupFailureKeepsAlarms(t *testing.T) {
	e := newStageEnv(t, &AlertRule{CriticalCondition: raw(`{"op":"gt","field":"value","value":90}`)})
	e.ev.WithFeatures(features{"org-1": true})
	if got := e.step(95); got != "open critical" {
		t.Fatalf("entitled: alarm %q, want open critical", got)
	}
	before := len(e.sink.log)
	e.ev.WithFeatures(failingFeatures{})
	if got := e.step(95); got != "open critical" {
		t.Fatalf("lookup failed: alarm %q, want still open critical", got)
	}
	for _, change := range e.sink.log[before:] {
		if strings.HasPrefix(change, "clear") {
			t.Fatalf("lookup failure cleared the alarm: %v", e.sink.log[before:])
		}
	}
}
