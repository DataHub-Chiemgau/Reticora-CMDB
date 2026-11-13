package monitoring

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
)

// The evaluator of the alert rules (MON-04, ALR-05, WP-984): every rule is
// evaluated per series, a CI and the values of its group_by labels inside
// the rule's scope. The state of a series is its alarm (WP-981): pending
// while the condition has not held for the rule's duration, open once it
// has, resolved when it no longer holds. The evaluator reads that state
// through AlarmSink, so a restart neither loses a pending start nor reports
// a series twice; package monitoring does not import package alarm.

// EvaluationInterval is the cycle of the evaluator (worker monitoring).
const EvaluationInterval = 15 * time.Second

// SeriesLookbackGrace is added to a rule's duration for the window in which
// the latest sample of a series counts.
const SeriesLookbackGrace = 15 * time.Minute

// MaxGroupBy is the most label keys a rule groups its series by.
const MaxGroupBy = 5

// Limits of a rule scope.
const (
	maxScopeCIs    = 1000
	maxScopeTypes  = 100
	maxScopeLabels = 16
	maxLabelKeyLen = 100
)

// Reasons of the clears of the evaluator.
const (
	// ClearReasonCleared: the condition no longer holds.
	ClearReasonCleared = "cleared"
	// ClearReasonNoData: the series has no sample in the window any more
	// (or left the scope).
	ClearReasonNoData = "no_data"
	// ClearReasonRuleDisabled and ClearReasonRuleDeleted: the rule left
	// the evaluation (ResolveRuleAlarms).
	ClearReasonRuleDisabled = "rule_disabled"
	ClearReasonRuleDeleted  = "rule_deleted"
)

// ResolveRuleAlarms resolves every alarm of a rule that is not resolved,
// for a rule that is disabled or deleted: the evaluator only visits the
// enabled rules (ListEnabled), so nothing else would resolve them. Like the
// evaluator it works on the whole organization (the rule belongs to no
// client), also when the caller's scope is narrower. A nil sink does
// nothing.
func ResolveRuleAlarms(ctx context.Context, sink AlarmSink, orgID, ruleID, reason string) error {
	if sink == nil {
		return nil
	}
	scope := database.OrgWideScope(orgID, "")
	ctx = database.ContextWithTenantScope(ctx, &scope)
	active, err := sink.Active(ctx, orgID, ruleID)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(active))
	for key := range active {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var errs []error
	for _, key := range keys {
		errs = append(errs, sink.Clear(ctx, orgID, ruleID, key, time.Time{}, reason))
	}
	return errors.Join(errs...)
}

// RuleScope selects the CIs of a rule (ALR-05). Every key is optional; the
// empty scope is every CI of the organization.
type RuleScope struct {
	// CIIDs are CIs of the rule.
	CIIDs []string `json:"ci_ids,omitempty"`
	// CITypeIDs are CI types of the rule, each with its subtypes
	// (ci_type_subtree).
	CITypeIDs []string `json:"ci_type_ids,omitempty"`
	// ClientID and SiteID are those of the CI.
	ClientID string `json:"client_id,omitempty"`
	SiteID   string `json:"site_id,omitempty"`
	// Labels must equal the labels of a sample, key by key.
	Labels map[string]string `json:"labels,omitempty"`
}

// IsEmpty reports whether the scope selects every CI.
func (s *RuleScope) IsEmpty() bool {
	return len(s.CIIDs) == 0 && len(s.CITypeIDs) == 0 && s.ClientID == "" && s.SiteID == "" && len(s.Labels) == 0
}

// scopeKeys are the keys of a scope.
var scopeKeys = []string{"ci_ids", "ci_type_ids", "client_id", "site_id", "labels"}

// unprocessableField is the 422 of a field.
func unprocessableField(field, message string) *api.FieldError {
	return &api.FieldError{Field: field, Message: message, Status: http.StatusUnprocessableEntity}
}

// ParseRuleScope reads a scope: null or absent is the empty scope; an
// unknown key, a value of the wrong kind and an id that is no UUID are 422
// with the field (scope.ci_type_ids[1]). Whether the ids exist in the
// organization the store checks (PGAlertStore).
// validation: field errors with fixed texts.
func ParseRuleScope(raw json.RawMessage) (RuleScope, error) {
	var scope RuleScope
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return scope, nil
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil || members == nil {
		return scope, unprocessableField("scope", "must be an object")
	}
	keys := make([]string, 0, len(members))
	for k := range members {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		field := "scope." + k
		value := members[k]
		var err error
		switch k {
		case "ci_ids":
			scope.CIIDs, err = parseIDList(field, value, maxScopeCIs)
		case "ci_type_ids":
			scope.CITypeIDs, err = parseIDList(field, value, maxScopeTypes)
		case "client_id":
			scope.ClientID, err = parseID(field, value)
		case "site_id":
			scope.SiteID, err = parseID(field, value)
		case "labels":
			scope.Labels, err = parseScopeLabels(field, value)
		default:
			return RuleScope{}, unprocessableField(field, "unknown scope key; allowed are "+strings.Join(scopeKeys, ", "))
		}
		if err != nil {
			return RuleScope{}, err
		}
	}
	return scope, nil
}

func parseID(field string, raw json.RawMessage) (string, error) {
	var id string
	if err := json.Unmarshal(raw, &id); err != nil || !api.IsUUID(id) {
		return "", unprocessableField(field, "must be a UUID")
	}
	return strings.ToLower(id), nil
}

func parseIDList(field string, raw json.RawMessage, limit int) ([]string, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, unprocessableField(field, "must be an array of UUIDs")
	}
	if len(items) > limit {
		return nil, unprocessableField(field, "at most "+strconv.Itoa(limit)+" entries")
	}
	out := make([]string, 0, len(items))
	for i, item := range items {
		id, err := parseID(field+"["+strconv.Itoa(i)+"]", item)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

func parseScopeLabels(field string, raw json.RawMessage) (map[string]string, error) {
	var labels map[string]string
	if err := json.Unmarshal(raw, &labels); err != nil {
		return nil, unprocessableField(field, "must be an object of strings")
	}
	if len(labels) > maxScopeLabels {
		return nil, unprocessableField(field, "at most "+strconv.Itoa(maxScopeLabels)+" labels")
	}
	for k, v := range labels {
		if strings.TrimSpace(k) == "" || len(k) > maxLabelKeyLen {
			return nil, unprocessableField(field, "label keys are 1 to 100 characters")
		}
		// PostgreSQL refuses NUL in jsonb (22P05): 422, not 500.
		if strings.ContainsRune(k, 0) || strings.ContainsRune(v, 0) {
			return nil, unprocessableField(field, "label keys and values must not contain NUL characters")
		}
	}
	return labels, nil
}

// ValidateGroupBy checks the label keys of a rule's series: at most
// MaxGroupBy distinct keys of 1 to 100 characters (422 group_by).
// validation: field errors with fixed texts.
func ValidateGroupBy(groupBy []string) error {
	if len(groupBy) > MaxGroupBy {
		return unprocessableField("group_by", "at most "+strconv.Itoa(MaxGroupBy)+" label keys")
	}
	seen := map[string]bool{}
	for i, k := range groupBy {
		field := "group_by[" + strconv.Itoa(i) + "]"
		if strings.TrimSpace(k) == "" || len(k) > maxLabelKeyLen || strings.ContainsRune(k, 0) {
			return unprocessableField(field, "a label key is 1 to 100 characters")
		}
		if seen[k] {
			return unprocessableField(field, "label key named twice")
		}
		seen[k] = true
	}
	return nil
}

// SeriesQuery selects the latest sample of every series of a rule.
type SeriesQuery struct {
	OrgID   string
	Name    string
	From    time.Time
	To      time.Time
	Scope   RuleScope
	GroupBy []string
}

// SeriesPoint is the latest sample of a series: its CI, the values of the
// group_by labels it has, and the sample.
type SeriesPoint struct {
	CIID      string
	Labels    map[string]string
	Timestamp time.Time
	Value     float64
}

// SeriesStore reads the latest sample of every series (PGMetricStore,
// MemoryMetricStore).
type SeriesStore interface {
	LatestBySeries(ctx context.Context, q *SeriesQuery) ([]SeriesPoint, error)
}

// SeriesKey is the key of a series of a rule: the CI and a hash of the
// values of the group_by labels in their order. The dedup key of its alarm
// is rule:<rule_id>:<series key>.
func SeriesKey(ciID string, groupBy []string, labels map[string]string) string {
	h := sha256.New()
	for _, k := range groupBy {
		v, ok := labels[k]
		h.Write([]byte(k))
		if ok {
			h.Write([]byte{1})
			h.Write([]byte(v))
		}
		h.Write([]byte{0})
	}
	return ciID + ":" + hex.EncodeToString(h.Sum(nil))[:16]
}

// RuleDedupKey is the dedup key of the alarm of a series of a rule.
func RuleDedupKey(ruleID, seriesKey string) string { return "rule:" + ruleID + ":" + seriesKey }

// RuleAlarm is a breach of a series the evaluator raises.
type RuleAlarm struct {
	RuleID    string
	SeriesKey string
	CIID      string
	Labels    map[string]string
	Severity  string
	Summary   string
	// Details holds the measured value, the threshold and the rule.
	Details map[string]any
	// Pending raises a pending alarm (the duration has not elapsed yet);
	// otherwise the alarm opens (a pending one moves to open).
	Pending bool
	// ObservedAt is the start of the breach (pending) or the opening.
	ObservedAt time.Time
}

// ActiveAlarm is the state of the alarm of a series that is not resolved.
type ActiveAlarm struct {
	State       string
	FirstSeenAt time.Time
}

// Alarm states the evaluator reads.
const (
	AlarmPending = "pending"
	AlarmOpen    = "open"
)

// AlarmSink is the alarm lifecycle the evaluator writes (alarm.RuleSink
// over alarm.Service): Active reads the alarms of a rule that are not
// resolved by series key, Raise pends or opens the alarm of a series,
// Clear resolves it.
type AlarmSink interface {
	Active(ctx context.Context, orgID, ruleID string) (map[string]ActiveAlarm, error)
	Raise(ctx context.Context, orgID string, in *RuleAlarm) error
	Clear(ctx context.Context, orgID, ruleID, seriesKey string, at time.Time, reason string) error
}

// conditionMet reports whether value satisfies the rule condition.
func conditionMet(condition string, value, threshold float64) bool {
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

// Evaluator evaluates the enabled alert rules per series on a ticker.
type Evaluator struct {
	series SeriesStore
	alerts AlertStore
	sink   AlarmSink
	// Now is overridable for tests.
	Now func() time.Time
}

// NewEvaluator builds an evaluator over the series of series, the rules of
// alerts and the alarm lifecycle sink.
func NewEvaluator(series SeriesStore, alerts AlertStore, sink AlarmSink) *Evaluator {
	return &Evaluator{series: series, alerts: alerts, sink: sink, Now: time.Now}
}

// HealthRefresher recomputes ci.health from the monitoring state (DB-05).
type HealthRefresher interface {
	RefreshHealth(ctx context.Context) error
}

// HealthRefreshingStore is implemented by stores that can recompute the
// health of the CIs (the PostgreSQL metric store).
type HealthRefreshingStore interface {
	HealthRefresher() HealthRefresher
}

// healthWorker is the worker name of the health refresh's metrics and
// switch (config.WorkerHealth, OPS-02/05).
const healthWorker = "health"

// HealthWorker recomputes the health of the CIs on a ticker of its own,
// apart from the alert evaluator: the metrics of MON-02 and ci.health
// (DB-05) belong to G1, the alert rules to the module monitoring (F-087).
type HealthWorker struct {
	health HealthRefresher
}

// NewHealthWorker builds the worker of the health refresh.
func NewHealthWorker(health HealthRefresher) *HealthWorker {
	return &HealthWorker{health: health}
}

// Run refreshes the health every interval (default one minute) until ctx is
// cancelled. It blocks and is meant to run in its own goroutine.
func (w *HealthWorker) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		w.Refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Refresh recomputes the health once; every call is a cycle of the worker
// health.
func (w *HealthWorker) Refresh(ctx context.Context) {
	failures := 0
	if err := w.health.RefreshHealth(ctx); err != nil {
		failures++
		slog.Error("ci health refresh failed", "error", err)
	}
	observability.ObserveWorkerRun(healthWorker, failures)
}

// Run evaluates rules every interval (default EvaluationInterval) until ctx
// is cancelled. It blocks and is meant to run in its own goroutine.
func (e *Evaluator) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = EvaluationInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		e.Evaluate(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Evaluate checks every enabled rule once. A rule that fails counts as an
// error of the run (reticora_worker_errors_total{worker="monitoring"}) and
// does not stop the other rules.
func (e *Evaluator) Evaluate(ctx context.Context) {
	failures := 0
	defer func() { observability.ObserveWorkerRun(evaluatorWorker, failures) }()
	rules, err := e.alerts.ListEnabled(ctx)
	if err != nil {
		failures++
		slog.Error("alert evaluation: list rules failed", "error", err)
		return
	}
	for i := range rules {
		if ctx.Err() != nil {
			return
		}
		rule := &rules[i]
		if err := e.evaluateRule(ctx, rule); err != nil {
			failures++
			slog.Error("alert evaluation failed", "rule_id", rule.ID, "organization_id", rule.OrgID, "rule", rule.Name, "error", err)
		}
	}
}

// evaluatorWorker is the worker name of the evaluator's metrics and switch
// (OPS-02/05).
const evaluatorWorker = "monitoring"

// evaluateRule evaluates a rule per series:
//   - the condition holds and the series has no alarm: a pending alarm from
//     the sample on (an open one at once for a rule without duration);
//   - the condition holds in a sample taken at least duration after the
//     start of the pending alarm: it opens (a single breaching sample that
//     is seen again on later runs does not open it);
//   - the condition does not hold: the alarm of the series is resolved;
//   - an alarm whose series has no sample in the window any more (or left
//     the scope) is resolved as well (no_data).
//
// Each change happens once per series: an open alarm stays as it is while
// the condition holds.
func (e *Evaluator) evaluateRule(ctx context.Context, rule *AlertRule) error {
	if e.sink == nil {
		return errors.New("no alarm sink")
	}
	// The evaluator runs outside a request and reads the metrics of the
	// rule's organization org-wide (E-08).
	scope := database.OrgWideScope(rule.OrgID, "")
	ctx = database.ContextWithTenantScope(ctx, &scope)
	now := e.Now().UTC()
	points, err := e.series.LatestBySeries(ctx, &SeriesQuery{
		OrgID: rule.OrgID, Name: rule.MetricName, From: now.Add(-(rule.Duration + SeriesLookbackGrace)), To: now,
		Scope: rule.Scope, GroupBy: rule.GroupBy,
	})
	if err != nil {
		return err
	}
	active, err := e.sink.Active(ctx, rule.OrgID, rule.ID)
	if err != nil {
		return err
	}
	var errs []error
	seen := make(map[string]bool, len(points))
	for i := range points {
		p := &points[i]
		key := SeriesKey(p.CIID, rule.GroupBy, p.Labels)
		seen[key] = true
		cur, has := active[key]
		if !conditionMet(rule.Condition, p.Value, rule.Threshold) {
			if has {
				errs = append(errs, e.sink.Clear(ctx, rule.OrgID, rule.ID, key, now, ClearReasonCleared))
			}
			continue
		}
		switch {
		case !has:
			// The breach starts with the sample; a rule with a duration
			// pends first and opens on a later run.
			errs = append(errs, e.sink.Raise(ctx, rule.OrgID, ruleAlarm(rule, key, p, rule.Duration > 0, p.Timestamp)))
		case cur.State == AlarmPending && !p.Timestamp.Before(cur.FirstSeenAt.Add(rule.Duration)):
			// A sample at least duration after the start of the breach
			// confirms it; the same stale sample seen on every run does not.
			errs = append(errs, e.sink.Raise(ctx, rule.OrgID, ruleAlarm(rule, key, p, false, now)))
		}
	}
	for key := range active {
		if !seen[key] {
			errs = append(errs, e.sink.Clear(ctx, rule.OrgID, rule.ID, key, now, ClearReasonNoData))
		}
	}
	return errors.Join(errs...)
}

// maxSummary is the longest summary of an alarm (alarm.SummaryMaxLen).
const maxSummary = 500

// ruleAlarm is the alarm of the series key of a rule for the sample p.
func ruleAlarm(rule *AlertRule, key string, p *SeriesPoint, pending bool, at time.Time) *RuleAlarm {
	summary := fmt.Sprintf("%s: %s %s %g (value %g)", rule.Name, rule.MetricName, rule.Condition, rule.Threshold, p.Value)
	if r := []rune(summary); len(r) > maxSummary {
		summary = string(r[:maxSummary])
	}
	return &RuleAlarm{
		RuleID: rule.ID, SeriesKey: key, CIID: p.CIID, Labels: p.Labels, Severity: rule.Severity, Summary: summary,
		Details: map[string]any{"value": p.Value, "threshold": rule.Threshold, "condition": rule.Condition,
			"metric_name": rule.MetricName, "rule_id": rule.ID, "rule_name": rule.Name},
		Pending: pending, ObservedAt: at,
	}
}

// LatestBySeries implements SeriesStore: the latest sample of every series
// in the window, samples without CI form none. A scope on types, client or
// site needs the CI lookup (WithCIInfo); without it nothing matches.
func (s *MemoryMetricStore) LatestBySeries(ctx context.Context, q *SeriesQuery) ([]SeriesPoint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	type seriesID struct{ ci, labels string }
	latest := map[seriesID]SeriesPoint{}
	for i := range s.metrics {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		m := &s.metrics[i]
		if m.OrgID != q.OrgID || m.CIID == "" || m.Name != q.Name || m.Timestamp.Before(q.From) || m.Timestamp.After(q.To) {
			continue
		}
		if !s.inScope(m, &q.Scope) {
			continue
		}
		labels := map[string]string{}
		for _, k := range q.GroupBy {
			if v, ok := m.Labels[k]; ok {
				labels[k] = v
			}
		}
		id := seriesID{ci: m.CIID, labels: SeriesKey("", q.GroupBy, labels)}
		if cur, ok := latest[id]; !ok || m.Timestamp.After(cur.Timestamp) {
			latest[id] = SeriesPoint{CIID: m.CIID, Labels: labels, Timestamp: m.Timestamp.UTC(), Value: m.Value}
		}
	}
	out := make([]SeriesPoint, 0, len(latest))
	for _, p := range latest {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CIID != out[j].CIID {
			return out[i].CIID < out[j].CIID
		}
		return SeriesKey("", q.GroupBy, out[i].Labels) < SeriesKey("", q.GroupBy, out[j].Labels)
	})
	return out, nil
}

// inScope reports whether the sample lies in the scope.
func (s *MemoryMetricStore) inScope(m *Metric, scope *RuleScope) bool {
	if len(scope.CIIDs) > 0 && !containsFold(scope.CIIDs, m.CIID) {
		return false
	}
	for k, v := range scope.Labels {
		if m.Labels[k] != v {
			return false
		}
	}
	if len(scope.CITypeIDs) == 0 && scope.ClientID == "" && scope.SiteID == "" {
		return true
	}
	if s.ciInfo == nil {
		return false
	}
	info, ok := s.ciInfo(m.OrgID, m.CIID)
	if !ok {
		return false
	}
	if scope.ClientID != "" && !strings.EqualFold(info.ClientID, scope.ClientID) {
		return false
	}
	if scope.SiteID != "" && !strings.EqualFold(info.SiteID, scope.SiteID) {
		return false
	}
	if len(scope.CITypeIDs) == 0 {
		return true
	}
	for _, t := range info.TypeIDs {
		if containsFold(scope.CITypeIDs, t) {
			return true
		}
	}
	return false
}

func containsFold(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}
