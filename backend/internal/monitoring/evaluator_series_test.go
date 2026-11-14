package monitoring

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The evaluator per series and rule scope (MON-04, ALR-05, WP-984) on the
// memory stores with a fake AlarmSink that follows the alarm lifecycle:
// pending, open, resolved. The PostgreSQL cases are in
// evaluator_series_integration_test.go.

// fakeAlarm is an alarm of the fake sink.
type fakeAlarm struct {
	state     string
	firstSeen time.Time
	in        RuleAlarm
}

// fakeSink keeps the alarms that are not resolved by dedup key and logs the
// changes ("pend <key>", "open <key>", "clear <key>").
type fakeSink struct {
	mu     sync.Mutex
	alarms map[string]*fakeAlarm
	log    []string
}

func newFakeSink() *fakeSink { return &fakeSink{alarms: map[string]*fakeAlarm{}} }

func (f *fakeSink) Active(_ context.Context, _, ruleID string) (map[string]ActiveAlarm, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]ActiveAlarm{}
	for key, a := range f.alarms {
		if prefix := RuleDedupKey(ruleID, ""); strings.HasPrefix(key, prefix) {
			out[strings.TrimPrefix(key, prefix)] = ActiveAlarm{State: a.state, FirstSeenAt: a.firstSeen}
		}
	}
	return out, nil
}

func (f *fakeSink) Raise(_ context.Context, _ string, in *RuleAlarm) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := RuleDedupKey(in.RuleID, in.SeriesKey)
	a, ok := f.alarms[key]
	switch {
	case !ok && in.Pending:
		f.alarms[key] = &fakeAlarm{state: AlarmPending, firstSeen: in.ObservedAt, in: *in}
		f.log = append(f.log, "pend "+key)
	case !ok:
		f.alarms[key] = &fakeAlarm{state: AlarmOpen, firstSeen: in.ObservedAt, in: *in}
		f.log = append(f.log, "open "+key)
	case a.state == AlarmPending && !in.Pending:
		a.state, a.in = AlarmOpen, *in
		f.log = append(f.log, "open "+key)
	}
	return nil
}

func (f *fakeSink) Clear(_ context.Context, _, ruleID, seriesKey string, _ time.Time, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := RuleDedupKey(ruleID, seriesKey)
	if _, ok := f.alarms[key]; ok {
		delete(f.alarms, key)
		f.log = append(f.log, "clear "+key)
	}
	return nil
}

func (f *fakeSink) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.log...)
}

// seriesEnv is a memory metric store, a rule and the evaluator on a clock.
type seriesEnv struct {
	t     *testing.T
	store *MemoryMetricStore
	rule  AlertRule
	sink  *fakeSink
	ev    *Evaluator
	now   time.Time
}

func newSeriesEnv(t *testing.T, rule *AlertRule) *seriesEnv {
	t.Helper()
	alerts := NewAlertManager()
	rule.OrgID, rule.Enabled, rule.MetricName, rule.Severity = "org-1", true, "if_errors", "warning"
	if rule.Condition == "" {
		rule.Condition, rule.Threshold = "gt", 10
	}
	stored, err := alerts.CreateRule(t.Context(), rule)
	if err != nil {
		t.Fatal(err)
	}
	e := &seriesEnv{t: t, store: NewMemoryMetricStore(), rule: stored, sink: newFakeSink(),
		now: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
	e.ev = NewEvaluator(e.store, alerts, e.sink)
	e.ev.Now = func() time.Time { return e.now }
	return e
}

// sample stores a value of the CI with labels at the current time.
func (e *seriesEnv) sample(ci string, value float64, labels map[string]string) {
	e.t.Helper()
	if err := e.store.Ingest(e.t.Context(), []Metric{{OrgID: "org-1", CIID: ci, Name: "if_errors", Value: value,
		Labels: labels, Timestamp: e.now}}); err != nil {
		e.t.Fatal(err)
	}
}

// step advances the clock and evaluates.
func (e *seriesEnv) step(d time.Duration) {
	e.now = e.now.Add(d)
	e.ev.Evaluate(e.t.Context())
}

func (e *seriesEnv) key(ci string, labels map[string]string) string {
	return RuleDedupKey(e.rule.ID, SeriesKey(ci, e.rule.GroupBy, labels))
}

func TestEvaluatorRaisesPerSeries(t *testing.T) {
	e := newSeriesEnv(t, &AlertRule{Name: "errors"})
	e.sample("ci-a", 50, nil)
	e.sample("ci-b", 1, nil)
	e.step(time.Second)
	if got := e.sink.calls(); len(got) != 1 || got[0] != "open "+e.key("ci-a", nil) {
		t.Fatalf("CI A breaks, B not: %v, want one alarm for A", got)
	}
	e.sample("ci-b", 60, nil)
	e.step(time.Second)
	if got := e.sink.calls(); len(got) != 2 || got[1] != "open "+e.key("ci-b", nil) {
		t.Fatalf("B breaks later: %v, want a second alarm for B", got)
	}
	// A normal value of B resolves B, not A.
	e.sample("ci-b", 2, nil)
	e.step(time.Second)
	got := e.sink.calls()
	if len(got) != 3 || got[2] != "clear "+e.key("ci-b", nil) {
		t.Fatalf("B back to normal: %v, want B cleared only", got)
	}
	if _, open := e.sink.alarms[e.key("ci-a", nil)]; !open {
		t.Error("the alarm of A was resolved by a value of B")
	}
}

func TestEvaluatorGroupByLabels(t *testing.T) {
	e := newSeriesEnv(t, &AlertRule{Name: "errors per interface", GroupBy: []string{"ifIndex"}})
	e.sample("ci-a", 50, map[string]string{"ifIndex": "1", "ifName": "eth0"})
	e.sample("ci-a", 70, map[string]string{"ifIndex": "2", "ifName": "eth1"})
	e.step(time.Second)
	got := e.sink.calls()
	if len(got) != 2 || e.sink.alarms[e.key("ci-a", map[string]string{"ifIndex": "1"})] == nil ||
		e.sink.alarms[e.key("ci-a", map[string]string{"ifIndex": "2"})] == nil {
		t.Fatalf("two interfaces of one CI: %v, want two series", got)
	}
	if a := e.sink.alarms[e.key("ci-a", map[string]string{"ifIndex": "2"})]; a.in.Labels["ifIndex"] != "2" || a.in.Labels["ifName"] != "" {
		t.Errorf("labels of the series %v, want the group_by labels only", a.in.Labels)
	}
	if !strings.HasPrefix(e.key("ci-a", nil), "rule:"+e.rule.ID+":ci-a:") {
		t.Errorf("dedup key %s, want rule:<rule_id>:<ci_id>:<hash>", e.key("ci-a", nil))
	}
	// Interface 1 recovers, 2 stays.
	e.sample("ci-a", 0, map[string]string{"ifIndex": "1", "ifName": "eth0"})
	e.step(time.Second)
	if got = e.sink.calls(); len(got) != 3 || got[2] != "clear "+e.key("ci-a", map[string]string{"ifIndex": "1"}) {
		t.Fatalf("interface 1 recovers: %v", got)
	}
}

func TestEvaluatorPendingThenOpen(t *testing.T) {
	e := newSeriesEnv(t, &AlertRule{Name: "slow", Duration: 5 * time.Minute})
	key := e.key("ci-a", nil)
	e.sample("ci-a", 50, nil)
	start := e.now
	e.step(time.Second)
	if got := e.sink.calls(); len(got) != 1 || got[0] != "pend "+key || !e.sink.alarms[key].firstSeen.Equal(start) {
		t.Fatalf("breach: %v, want pending from the sample", got)
	}
	// Still within the duration: no change.
	for range 4 {
		e.sample("ci-a", 55, nil)
		e.step(time.Minute)
	}
	if got := e.sink.calls(); len(got) != 1 {
		t.Fatalf("within the duration: %v, want pending only", got)
	}
	e.sample("ci-a", 55, nil)
	e.step(time.Minute)
	e.sample("ci-a", 56, nil)
	e.step(15 * time.Second)
	if got := e.sink.calls(); len(got) != 2 || got[1] != "open "+key {
		t.Fatalf("after 5 minutes: %v, want exactly one open", got)
	}
}

func TestEvaluatorClearsOnReturn(t *testing.T) {
	e := newSeriesEnv(t, &AlertRule{Name: "errors"})
	key := e.key("ci-a", nil)
	e.sample("ci-a", 50, nil)
	e.step(time.Second)
	e.sample("ci-a", 1, nil)
	e.step(time.Second)
	e.step(time.Second)
	got := e.sink.calls()
	if len(got) != 2 || got[1] != "clear "+key {
		t.Fatalf("return below the threshold: %v, want exactly one clear", got)
	}
	// A pending alarm of a series without samples in the window is
	// resolved as well (no data).
	p := newSeriesEnv(t, &AlertRule{Name: "slow", Duration: time.Minute})
	p.sample("ci-a", 50, nil)
	p.step(time.Second)
	p.step(SeriesLookbackGrace + 2*time.Minute)
	if got := p.sink.calls(); len(got) != 2 || got[1] != "clear "+p.key("ci-a", nil) {
		t.Fatalf("no data: %v, want pend and clear", got)
	}
}

// TestEvaluatorStaleSampleDoesNotOpen: a single breaching sample that is
// seen again on every run (the collector sends nothing more) keeps the
// alarm pending after the duration; it is resolved as no_data once the
// sample leaves the window. Only a sample taken at least duration after the
// start of the breach opens it.
func TestEvaluatorStaleSampleDoesNotOpen(t *testing.T) {
	e := newSeriesEnv(t, &AlertRule{Name: "slow", Duration: 5 * time.Minute})
	key := e.key("ci-a", nil)
	e.sample("ci-a", 50, nil)
	e.step(time.Second)
	for range 8 {
		e.step(time.Minute)
	}
	if got := e.sink.calls(); len(got) != 1 || got[0] != "pend "+key {
		t.Fatalf("one breaching sample, 8 minutes later: %v, want pending only", got)
	}
	e.step(SeriesLookbackGrace)
	if got := e.sink.calls(); len(got) != 2 || got[1] != "clear "+key {
		t.Fatalf("stale sample out of the window: %v, want pend and clear", got)
	}
}

// TestDisabledOrDeletedRuleResolvesItsAlarms: the evaluator only visits the
// enabled rules, so PATCH enabled=false and DELETE resolve the alarms of
// the rule; the alarms of other rules stay.
func TestDisabledOrDeletedRuleResolvesItsAlarms(t *testing.T) {
	alerts := NewAlertManager()
	store := NewMemoryMetricStore()
	sink := newFakeSink()
	h := NewHandler(store, alerts).WithRuleAlarms(sink)
	r := chi.NewRouter()
	h.RegisterRoutes(r)
	send := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1"}))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	ids := map[string]string{}
	for _, name := range []string{"disabled", "deleted", "kept"} {
		w := send(http.MethodPost, "/api/v1/monitoring/alerts",
			`{"name":"`+name+`","metric_name":"if_errors","condition":"gt","threshold":10,"enabled":true}`)
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", name, w.Code, w.Body.String())
		}
		ids[name] = extractJSONField(w.Body.String(), "id")
	}
	now := time.Now().UTC()
	if err := store.Ingest(t.Context(), []Metric{{OrgID: "org-1", CIID: "ci-a", Name: "if_errors", Value: 50, Timestamp: now}}); err != nil {
		t.Fatal(err)
	}
	ev := NewEvaluator(store, alerts, sink)
	ev.Evaluate(t.Context())
	active := func(name string) int {
		m, _ := sink.Active(t.Context(), "org-1", ids[name])
		return len(m)
	}
	for _, name := range []string{"disabled", "deleted", "kept"} {
		if active(name) != 1 {
			t.Fatalf("rule %s: %d alarms after the breach, want 1", name, active(name))
		}
	}
	if w := send(http.MethodPatch, "/api/v1/monitoring/alerts/"+ids["disabled"], `{"enabled":false}`); w.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", w.Code, w.Body.String())
	}
	if active("disabled") != 0 {
		t.Error("the alarm of the disabled rule stayed active")
	}
	if w := send(http.MethodDelete, "/api/v1/monitoring/alerts/"+ids["deleted"], ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if active("deleted") != 0 {
		t.Error("the alarm of the deleted rule stayed active")
	}
	// The next run raises nothing for them; the kept rule keeps its alarm.
	ev.Evaluate(t.Context())
	if active("disabled") != 0 || active("deleted") != 0 || active("kept") != 1 {
		t.Errorf("after the next run: disabled %d, deleted %d, kept %d; want 0, 0, 1",
			active("disabled"), active("deleted"), active("kept"))
	}
}

func TestAlertRuleScopeValidation(t *testing.T) {
	h := NewHandler(NewMemoryMetricStore())
	r := chi.NewRouter()
	h.RegisterRoutes(r)
	post := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1"}))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	const base = `{"name":"r","metric_name":"m","condition":"gt","threshold":1`
	for body, field := range map[string]string{
		base + `,"scope":{"hosts":["a"]}}`:                       "scope.hosts",
		base + `,"scope":{"ci_type_ids":["` + uuidA + `","x"]}}`: "scope.ci_type_ids[1]",
		base + `,"scope":{"client_id":"nope"}}`:                  "scope.client_id",
		base + `,"scope":[1]}`:                                   "scope",
		base + `,"group_by":["a","b","c","d","e","f"]}`:          "group_by",
		base + `,"group_by":["a","a"]}`:                          "group_by[1]",
		base + `,"scope":{"labels":{"a":"\u0000"}}}`:             "scope.labels",
		base + `,"scope":{"labels":{"a\u0000":"b"}}}`:            "scope.labels",
	} {
		w := post(http.MethodPost, "/api/v1/monitoring/alerts", body)
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"field":"`+field+`"`) {
			t.Errorf("%s: %d %s, want 422 %s", body, w.Code, w.Body.String(), field)
		}
	}
	w := post(http.MethodPost, "/api/v1/monitoring/alerts", base+`,"scope":{"ci_ids":["`+uuidA+`"],"labels":{"ifName":"eth0"}},"group_by":["ifIndex"]}`)
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), `"group_by":["ifIndex"]`) ||
		!strings.Contains(w.Body.String(), `"scope":{"ci_ids":["`+uuidA+`"],"labels":{"ifName":"eth0"}}`) {
		t.Fatalf("valid scope: %d %s", w.Code, w.Body.String())
	}
	id := extractJSONField(w.Body.String(), "id")
	w = post(http.MethodPatch, "/api/v1/monitoring/alerts/"+id, `{"scope":{"site":"x"}}`)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"field":"scope.site"`) {
		t.Errorf("PATCH unknown scope key: %d %s", w.Code, w.Body.String())
	}
	w = post(http.MethodPatch, "/api/v1/monitoring/alerts/"+id, `{"scope":{},"group_by":[]}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"scope":{}`) || !strings.Contains(w.Body.String(), `"group_by":[]`) {
		t.Errorf("PATCH empty scope: %d %s", w.Code, w.Body.String())
	}
	// Without scope a rule gets the empty scope (every CI).
	w = post(http.MethodPost, "/api/v1/monitoring/alerts", base+`}`)
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), `"scope":{}`) || strings.Contains(w.Body.String(), "pending_since") {
		t.Errorf("rule without scope: %d %s", w.Code, w.Body.String())
	}
}

const uuidA = "0a1b2c3d-0000-4000-8000-000000000001"
