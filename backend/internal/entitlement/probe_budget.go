package entitlement

import (
	"context"
	"fmt"
	"math/big"
	"slices"
)

// Probe budget (PKG-04, NFR-11, PRT-01, WP-940, E-154): the load of the
// monitoring of an organization, computed from the targets and the
// effective intervals of its active probes. The monitoring domain (package
// probe) computes the budget before and after a configuration change in its
// write transaction under the lock database.LockQuota(ctx, orgID,
// ProbeBudgetLock), taken before any row lock, and refuses the change with
// AllowProbeChange. The function part here knows no monitoring table.

// ProbeBudgetLock is the quota lock of the probe budget: every write that
// may raise the load of the probes of an organization (create, change,
// enable) takes it exclusively first in its transaction, so the budgets it
// counts afterwards include every change committed before.
const ProbeBudgetLock = "probe_budget"

// EffectiveSpec is what the budget needs of the effective values of one
// probe (probe.EffectiveSpec, WP-937): Target identifies the monitored
// target (the same target of two probes is counted once), IntervalS is the
// effective interval in seconds, Active whether the probe runs (enabled
// and not suspended). Inactive probes and probes without interval count
// nothing.
type EffectiveSpec struct {
	Type      string
	Target    string
	IntervalS int64
	Active    bool
}

// ProbeBudget is the load of a set of probes: Targets the distinct targets
// of the active probes (max_monitored_targets), PerMinute the checks per
// minute Σ 60/interval_s, exact as a fraction (max_probes_per_minute),
// PerType the distinct targets per probe type of the register
// (max_targets_<type>), MinIntervalS the shortest interval of an active
// probe (0 without one; min_probe_interval_s).
type ProbeBudget struct {
	Targets      int64
	PerMinute    *big.Rat
	PerType      map[string]int64
	MinIntervalS int64
}

// BudgetOf computes the budget of specs.
func BudgetOf(specs []EffectiveSpec) ProbeBudget {
	b := ProbeBudget{PerMinute: new(big.Rat), PerType: map[string]int64{}}
	targets := map[string]bool{}
	perType := map[[2]string]bool{}
	for _, s := range specs {
		if !s.Active || s.IntervalS <= 0 {
			continue
		}
		if !targets[s.Target] {
			targets[s.Target] = true
			b.Targets++
		}
		if key := [2]string{s.Type, s.Target}; !perType[key] {
			perType[key] = true
			b.PerType[s.Type]++
		}
		b.PerMinute.Add(b.PerMinute, big.NewRat(60, s.IntervalS))
		if b.MinIntervalS == 0 || s.IntervalS < b.MinIntervalS {
			b.MinIntervalS = s.IntervalS
		}
	}
	return b
}

// perMinute returns the checks per minute of b (0 for a budget without
// value).
func (b *ProbeBudget) perMinute() *big.Rat {
	if b.PerMinute == nil {
		return new(big.Rat)
	}
	return b.PerMinute
}

// AllowProbeChange admits a change of the probes of the organization whose
// budget goes from before to after (PKG-04, WP-940). Only increases are
// checked: a shorter shortest interval against min_probe_interval_s
// (CheckInterval, 422 on interval_s), more targets against
// max_monitored_targets, more checks per minute against
// max_probes_per_minute and more targets of a type against
// max_targets_<type> (LimitExceededError, 403 entitlement-limit with the
// quota and its limit). A limit is reachable exactly, a value above it is
// not. A decrease is never refused; with enforcement off, without the
// monitoring feature for a change that raises nothing, or without stored
// limits every change passes. The caller holds the lock ProbeBudgetLock
// and counted both budgets in its transaction.
func (s *Service) AllowProbeChange(ctx context.Context, orgID string, before, after ProbeBudget) error {
	if !s.opts.Enforce {
		return nil
	}
	intervalDown := after.MinIntervalS > 0 && (before.MinIntervalS == 0 || after.MinIntervalS < before.MinIntervalS)
	targetsUp := after.Targets > before.Targets
	minuteUp := after.perMinute().Cmp(before.perMinute()) > 0
	var typesUp []string
	for typ, n := range after.PerType {
		if n > before.PerType[typ] {
			typesUp = append(typesUp, typ)
		}
	}
	if !intervalDown && !targetsUp && !minuteUp && len(typesUp) == 0 {
		return nil
	}
	if intervalDown {
		if err := s.CheckInterval(ctx, orgID, int(min(after.MinIntervalS, int64(^uint32(0)>>1)))); err != nil {
			return err
		}
	}
	ent, entitled, err := s.Check(ctx, orgID, FeatureMonitoring)
	if err != nil {
		return err
	}
	if !entitled {
		return &FeatureNotEntitledError{FeatureKey: FeatureMonitoring}
	}
	hide := !countVisible(ctx)
	if limit, ok := ent.Limits[LimitMaxMonitoredTargets]; ok && targetsUp && after.Targets > limit {
		return &LimitExceededError{FeatureKey: LimitMaxMonitoredTargets, Limit: limit, Current: before.Targets, HideCurrent: hide}
	}
	if limit, ok := ent.Limits[LimitMaxProbesPerMinute]; ok && minuteUp && after.perMinute().Cmp(new(big.Rat).SetInt64(limit)) > 0 {
		return &LimitExceededError{FeatureKey: LimitMaxProbesPerMinute, Limit: limit, Current: ceilRat(before.perMinute()), HideCurrent: hide}
	}
	slices.Sort(typesUp)
	for _, typ := range typesUp {
		quota := TargetTypeQuotaPrefix + typ
		if limit, ok := ent.Limits[quota]; ok && after.PerType[typ] > limit {
			return &LimitExceededError{FeatureKey: quota, Limit: limit, Current: before.PerType[typ], HideCurrent: hide}
		}
	}
	return nil
}

// MinProbeIntervalS returns min_probe_interval_s of the organization (0
// without a stored value, without monitoring or with enforcement off). The
// resolution of the effective values raises a shorter interval to it
// (WP-940 AK4).
func (s *Service) MinProbeIntervalS(ctx context.Context, orgID string) (int64, error) {
	if !s.opts.Enforce {
		return 0, nil
	}
	ent, entitled, err := s.Check(ctx, orgID, FeatureMonitoring)
	if err != nil || !entitled {
		return 0, err
	}
	return max(ent.Limits[LimitMinProbeIntervalS], 0), nil
}

// ceilRat rounds r up to an integer (the checks per minute in use as the
// refusal names them).
func ceilRat(r *big.Rat) int64 {
	q, m := new(big.Int).QuoRem(r.Num(), r.Denom(), new(big.Int))
	if m.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return 0
	}
	return q.Int64()
}

// String formats the budget for logs and test messages.
func (b ProbeBudget) String() string {
	return fmt.Sprintf("targets=%d per_minute=%s per_type=%v min_interval_s=%d", b.Targets, b.perMinute().RatString(), b.PerType, b.MinIntervalS)
}
