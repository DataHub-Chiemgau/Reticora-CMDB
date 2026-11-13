package alarm

import (
	"context"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
)

// RuleSink is the alarm lifecycle as the rule evaluator sees it
// (monitoring.AlarmSink, WP-984): the alarms of source rule, one per series
// of a rule, keyed rule:<rule_id>:<series key>. Package monitoring does not
// import this package; the server wires the sink.
type RuleSink struct {
	svc *Service
}

// NewRuleSink returns the sink on the lifecycle service.
func NewRuleSink(svc *Service) *RuleSink { return &RuleSink{svc: svc} }

var _ monitoring.AlarmSink = (*RuleSink)(nil)

// activePage is the page size Active reads the alarms of a rule with.
const activePage = 1000

// Active implements monitoring.AlarmSink: the alarms of the rule that are
// not resolved, by series key.
func (s *RuleSink) Active(ctx context.Context, orgID, ruleID string) (map[string]monitoring.ActiveAlarm, error) {
	out := map[string]monitoring.ActiveAlarm{}
	f := &ListFilter{States: []string{StatePending, StateOpen, StateAcknowledged}, SourceKinds: []string{SourceRule},
		SourceID: ruleID, Limit: activePage}
	for {
		page, err := s.svc.Store().List(ctx, orgID, f)
		if err != nil {
			return nil, err
		}
		for i := range page.Items {
			a := &page.Items[i]
			out[a.SeriesKey] = monitoring.ActiveAlarm{State: a.State, FirstSeenAt: a.FirstSeenAt, Severity: a.Severity}
		}
		if page.Next == nil {
			return out, nil
		}
		f.After = page.Next
	}
}

// Raise implements monitoring.AlarmSink: a pending alarm (Pend) or the
// opening of the series' alarm (Raise).
func (s *RuleSink) Raise(ctx context.Context, orgID string, in *monitoring.RuleAlarm) error {
	raise := &RaiseInput{SourceKind: SourceRule, SourceID: strings.ToLower(in.RuleID), SeriesKey: in.SeriesKey,
		Labels: in.Labels, Severity: in.Severity, Summary: in.Summary, Details: in.Details, ObservedAt: in.ObservedAt}
	if in.CIID != "" {
		ci := in.CIID
		raise.CIID = &ci
	}
	var err error
	if in.Pending {
		_, err = s.svc.Pend(ctx, orgID, raise)
	} else {
		_, err = s.svc.Raise(ctx, orgID, raise)
	}
	return err
}

// Clear implements monitoring.AlarmSink.
func (s *RuleSink) Clear(ctx context.Context, orgID, ruleID, seriesKey string, at time.Time, reason string) error {
	_, err := s.svc.Clear(ctx, orgID, DedupKey(SourceRule, strings.ToLower(ruleID), seriesKey), at, reason)
	return err
}
