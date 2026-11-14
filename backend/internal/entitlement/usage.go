package entitlement

import (
	"context"
	"slices"
	"strings"
)

// Counter counts the current use of one quota for an organization, with
// the rule of the AllowCreate caller of that quota.
type Counter func(ctx context.Context, orgID string) (int64, error)

// UsageCounters are the counters of the quotas (PKG-05, WP-885), keyed by
// quota (max_cis, max_collectors, max_users, max_api_keys,
// max_storage_mb); server.NewRouter wires them from the repositories. A
// quota without counter reports 0.
type UsageCounters map[string]Counter

// MonitoringUsage counts the monitoring use of an organization: probe
// targets in total and per probe type, configured probes per minute. Until
// WP-940 no implementation is set and the values are 0.
type MonitoringUsage interface {
	MonitoredTargets(ctx context.Context, orgID string) (int64, error)
	TargetsOfType(ctx context.Context, orgID, probeType string) (int64, error)
	ProbesPerMinute(ctx context.Context, orgID string) (int64, error)
}

// QuotaUsage is the use of one quota: used (null when the caller may not
// see the count of the organization, F-414, or for min_probe_interval_s,
// which counts nothing), the limit (null: unlimited) and the origin of the
// limit (plan or override, LimitOrigin).
type QuotaUsage struct {
	Quota  string `json:"quota"`
	Used   *int64 `json:"used"`
	Limit  *int64 `json:"limit"`
	Origin string `json:"origin"`
}

// WithUsageCounters sets the counters of the usage report.
func (s *Service) WithUsageCounters(c UsageCounters) *Service {
	s.counters = c
	return s
}

// WithMonitoringUsage sets the monitoring counters (WP-940).
func (s *Service) WithMonitoringUsage(m MonitoringUsage) *Service {
	s.monitoring = m
	return s
}

// UsageVisible reports whether the caller of ctx may see the counts of the
// whole organization (an org-wide principal, F-414).
func UsageVisible(ctx context.Context) bool { return countVisible(ctx) }

// Usage reports every quota of Quotas and every stored max_targets_<type>
// with its use, limit and origin (PKG-05, WP-885). showCounts false leaves
// used null; the limits stay visible.
func (s *Service) Usage(ctx context.Context, orgID string, showCounts bool) ([]QuotaUsage, error) {
	stored, err := s.load(ctx, orgID)
	if err != nil {
		return nil, err
	}
	plan, _, err := s.OrganizationPlan(ctx, orgID)
	if err != nil {
		return nil, err
	}
	limits := map[string]int64{}
	for i := range stored {
		for quota, value := range stored[i].Limits {
			if feature, ok := QuotaFeatureOf(quota); ok && feature == stored[i].FeatureKey {
				limits[quota] = value
			}
		}
	}
	quotas := slices.Clone(Quotas)
	var typed []string
	for quota := range limits {
		if targetTypeQuota.MatchString(quota) {
			typed = append(typed, quota)
		}
	}
	slices.Sort(typed)
	quotas = append(quotas, typed...)

	out := make([]QuotaUsage, 0, len(quotas))
	for _, quota := range quotas {
		u := QuotaUsage{Quota: quota}
		if value, ok := limits[quota]; ok {
			v := value
			u.Limit = &v
		}
		u.Origin = LimitOrigin(plan, quota, u.Limit)
		if showCounts && quota != LimitMinProbeIntervalS {
			used, countErr := s.count(ctx, orgID, quota)
			if countErr != nil {
				return nil, countErr
			}
			u.Used = &used
		}
		out = append(out, u)
	}
	return out, nil
}

// count returns the use of one quota.
func (s *Service) count(ctx context.Context, orgID, quota string) (int64, error) {
	switch {
	case quota == LimitMaxMonitoredTargets:
		if s.monitoring == nil {
			return 0, nil
		}
		return s.monitoring.MonitoredTargets(ctx, orgID)
	case quota == LimitMaxProbesPerMinute:
		if s.monitoring == nil {
			return 0, nil
		}
		return s.monitoring.ProbesPerMinute(ctx, orgID)
	case targetTypeQuota.MatchString(quota):
		if s.monitoring == nil {
			return 0, nil
		}
		return s.monitoring.TargetsOfType(ctx, orgID, strings.TrimPrefix(quota, "max_targets_"))
	}
	if counter, ok := s.counters[quota]; ok && counter != nil {
		return counter(ctx, orgID)
	}
	return 0, nil
}

// PlanInfo is one plan of the plan matrix: its features and default
// quotas (E-151, E-153).
type PlanInfo struct {
	Plan          Plan             `json:"plan"`
	Features      []string         `json:"features"`
	QuotaDefaults map[string]int64 `json:"quota_defaults"`
}

// PlanMatrix lists the four plans with PlanFeatures and PlanQuotaDefaults.
func PlanMatrix() []PlanInfo {
	out := make([]PlanInfo, 0, 4)
	for _, plan := range []Plan{PlanEssential, PlanStandard, PlanPro, PlanEnterprise} {
		out = append(out, PlanInfo{Plan: plan, Features: PlanFeatures(plan), QuotaDefaults: PlanQuotaDefaults(plan)})
	}
	return out
}

// PlanMatrixResponse is the answer of the plan matrix paths: the plans and
// the add-ons, which no plan includes.
type PlanMatrixResponse struct {
	Data   []PlanInfo  `json:"data"`
	Addons []AddonInfo `json:"addons"`
}

// QuotaUsageResponse is the answer of the usage reports.
type QuotaUsageResponse struct {
	Data []QuotaUsage `json:"data"`
	Plan Plan         `json:"plan"`
}
