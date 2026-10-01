package monitoring

import (
	"context"
	"log/slog"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

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

// Evaluator evaluates enabled alert rules against the metric store. It runs
// on a ticker and notifies whenever a rule's condition holds continuously
// for the rule duration (state transitions are persisted in the AlertStore,
// so restarts neither spam notifications nor forget pending durations).
type Evaluator struct {
	metrics  MetricStore
	alerts   AlertStore
	notifier Notifier
	// Now is overridable for tests.
	Now func() time.Time
}

// NewEvaluator builds an evaluator; when notifier is nil, a slog notifier is
// used so fired alerts are at least visible in the server logs.
func NewEvaluator(metrics MetricStore, alerts AlertStore, notifier Notifier) *Evaluator {
	if notifier == nil {
		notifier = NotifierFunc(func(_ context.Context, ev AlertEvent) {
			slog.Warn("alert fired",
				"rule_id", ev.Rule.ID,
				"rule", ev.Rule.Name,
				"metric", ev.Rule.MetricName,
				"severity", ev.Rule.Severity,
				"org_id", ev.Rule.OrgID,
				"value", ev.Value,
				"threshold", ev.Rule.Threshold,
			)
		})
	}
	return &Evaluator{metrics: metrics, alerts: alerts, notifier: notifier, Now: time.Now}
}

// Run evaluates rules every interval until ctx is cancelled. It blocks and
// is meant to run in its own goroutine.
func (e *Evaluator) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
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

// Evaluate checks every enabled rule once against the latest sample of its
// metric.
func (e *Evaluator) Evaluate(ctx context.Context) {
	rules, err := e.alerts.ListEnabled(ctx)
	if err != nil {
		slog.Error("alert evaluation: list rules failed", "error", err)
		return
	}
	for _, rule := range rules {
		if ctx.Err() != nil {
			return
		}
		if err := e.evaluateRule(ctx, rule); err != nil {
			slog.Error("alert evaluation failed", "rule_id", rule.ID, "rule", rule.Name, "error", err)
		}
	}
}

// evaluateRule evaluates a single rule:
//   - no sample in the lookback window → condition not met, pending cleared;
//   - condition met → pending since first observed; once the condition has
//     held for rule.Duration the alert fires exactly once per firing;
//   - condition not met → pending cleared (duration restarts next time).
func (e *Evaluator) evaluateRule(ctx context.Context, rule AlertRule) error {
	// The evaluator runs outside a request and reads the metrics of the
	// rule's organization org-wide (E-08; WP-022 moves it to a system
	// principal per organization).
	scope := database.OrgWideScope(rule.OrgID, "")
	ctx = database.ContextWithTenantScope(ctx, &scope)
	now := e.Now().UTC()
	// Look far enough back to observe the duration plus one evaluation gap.
	lookback := rule.Duration + 15*time.Minute
	points, err := e.metrics.Query(ctx, MetricQuery{
		OrgID: rule.OrgID,
		Name:  rule.MetricName,
		From:  now.Add(-lookback),
		To:    now,
	})
	if err != nil {
		return err
	}

	// The latest sample decides whether the condition currently holds.
	var latest *MetricPoint
	if len(points) > 0 {
		latest = &points[len(points)-1]
	}
	if latest == nil || !conditionMet(rule.Condition, latest.Value, rule.Threshold) {
		if rule.PendingSince != nil {
			return e.alerts.ClearPending(ctx, rule)
		}
		return nil
	}

	// The condition currently holds. Determine since when: the persisted
	// pending marker wins (durations survive restarts); otherwise the
	// condition history is derived from the samples — the breach started at
	// the first consecutive in-window sample that still holds.
	pendingSince := now
	if rule.PendingSince != nil {
		pendingSince = *rule.PendingSince
	} else {
		breachStart := latest.Timestamp
		for i := len(points) - 2; i >= 0; i-- {
			if !conditionMet(rule.Condition, points[i].Value, rule.Threshold) {
				break
			}
			breachStart = points[i].Timestamp
		}
		pendingSince = breachStart
		if err := e.alerts.MarkPending(ctx, rule, pendingSince); err != nil {
			return err
		}
	}

	if now.Sub(pendingSince) < rule.Duration {
		return nil // condition not yet held long enough
	}
	if rule.LastFiredAt != nil && !rule.LastFiredAt.Before(pendingSince) {
		return nil // already notified for this firing
	}

	e.notifier.NotifyAlert(ctx, AlertEvent{
		Rule:    rule,
		Value:   latest.Value,
		Sample:  Metric{OrgID: rule.OrgID, Name: rule.MetricName, Value: latest.Value, Timestamp: latest.Timestamp},
		FiredAt: now,
	})
	return e.alerts.MarkFired(ctx, rule, now)
}
