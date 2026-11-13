package monitoring_test

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
)

// The metric context in one query on PostgreSQL (WP-985, AK2) and the
// stages with hysteresis through alarm.RuleSink: the same aggregates as the
// memory store, a counter reset without negative rate, escalation of the
// severity and the clear condition.

func TestThresholdContextOnPostgreSQL(t *testing.T) {
	e := newEvalEnv(t)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp985-context")
	start := e.now.Add(-4 * time.Minute)
	// Outside the window of 3 minutes back from the latest sample.
	e.sample(ci, "wp985_octets", 7, nil, start.Add(-10*time.Minute))
	for i, v := range []float64{4294967000, 4294967200, 100, 400} {
		e.sample(ci, "wp985_octets", v, nil, start.Add(time.Duration(i)*time.Minute))
	}
	points, err := e.store.LatestBySeries(e.f.OrgCtx(e.f.OrgA), &monitoring.SeriesQuery{OrgID: e.f.OrgA, Name: "wp985_octets",
		From: e.now.Add(-time.Hour), To: e.now, Window: 3 * time.Minute})
	if err != nil || len(points) != 1 {
		t.Fatalf("series: %+v %v", points, err)
	}
	c := points[0].Context
	if c.Samples != 4 || c.Value != 400 || c.Max != 4294967200 || c.Min != 100 || c.Delta != 400-4294967000 {
		t.Errorf("context %+v", c)
	}
	if want := (4294967000.0 + 4294967200 + 100 + 400) / 4; math.Abs(c.Avg-want) > 1e-3 {
		t.Errorf("avg %g, want %g", c.Avg, want)
	}
	// 200 + 100 (reset) + 300 over 180 s.
	if c.Rate == nil || math.Abs(*c.Rate-600.0/180) > 1e-9 {
		t.Errorf("rate %v, want %g", c.Rate, 600.0/180)
	}
	// Window 0: the latest sample alone, no rate.
	points, err = e.store.LatestBySeries(e.f.OrgCtx(e.f.OrgA), &monitoring.SeriesQuery{OrgID: e.f.OrgA, Name: "wp985_octets",
		From: e.now.Add(-time.Hour), To: e.now})
	if err != nil || len(points) != 1 || points[0].Context.Samples != 1 || points[0].Context.Rate != nil ||
		points[0].Context.Avg != 400 {
		t.Errorf("window 0: %+v %v", points, err)
	}
}

func TestThresholdStagesOnPostgreSQL(t *testing.T) {
	e := newEvalEnv(t)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp985-stages")
	rule, err := e.store.AlertStore().CreateRule(e.f.OrgCtx(e.f.OrgA), &monitoring.AlertRule{OrgID: e.f.OrgA,
		Name: "wp985 stages", MetricName: "wp985_cpu", Enabled: true,
		CriticalCondition: json.RawMessage(`{"op":"gt","field":"value","value":90}`),
		WarningCondition:  json.RawMessage(`{"op":"gt","field":"avg","value":70}`),
		ClearCondition:    json.RawMessage(`{"op":"lt","field":"value","value":60}`),
		EvalWindow:        time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if rule.Condition != "" || rule.EvalWindow != time.Minute || string(rule.ClearCondition) != `{"op":"lt","field":"value","value":60}` {
		t.Fatalf("stored rule: %+v", rule)
	}
	at := e.now.Add(-10 * time.Minute)
	step := func(value float64) alarm.Alarm {
		t.Helper()
		at = at.Add(2 * time.Minute)
		e.now = at
		e.sample(ci, "wp985_cpu", value, nil, at)
		e.evaluator().Evaluate(context.Background())
		return e.alarmsOf(rule.ID)[ci]
	}
	for _, s := range []struct {
		value    float64
		severity string
	}{
		{75, "warning"},  // avg over the minute: 75
		{95, "critical"}, // raised
		{80, "critical"}, // not lowered
		{65, "critical"}, // no stage, the clear condition does not hold
		{55, ""},         // resolved
	} {
		if got := step(s.value); got.Severity != s.severity {
			t.Fatalf("value %g: alarm %q %q, want %q", s.value, got.State, got.Severity, s.severity)
		}
	}
	// The old form of a rule with stages is empty; a PATCH with the old
	// form makes it one stage again.
	threshold, cond := 80.0, "gt"
	updated, err := e.store.AlertStore().UpdateRule(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, rule.ID,
		&monitoring.UpdateAlertRuleRequest{Condition: &cond, Threshold: &threshold})
	if err != nil || updated.Condition != "gt" || updated.Threshold != 80 || updated.Severity != "warning" ||
		updated.CriticalCondition != nil || updated.ClearCondition != nil {
		t.Fatalf("PATCH old form: %+v %v", updated, err)
	}
}
