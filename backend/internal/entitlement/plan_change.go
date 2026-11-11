package entitlement

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// TransitionOptions tune PlanTransition.
type TransitionOptions struct {
	// ResetLimits sets every quota of the rows of origin plan to the
	// default of the new plan, overrides included (internal, for the
	// fallback plan after a trial, WP-884).
	ResetLimits bool
}

// PlanTransition computes the rows a change of plan from → to writes
// (PKG-02, WP-882): a feature of the new plan without row gets one of
// origin plan; a row of origin plan inside the new plan is enabled, one
// outside it disabled (row, limits and valid_until stay; ENT-08: nothing is
// deleted); every row of origin plan takes the new plan. A quota of a row of
// origin plan inside the new plan that is missing or equal to the default of
// the old plan becomes the default of the new plan (no default: the entry
// goes, unlimited); a different value is an override and stays. Rows of
// origin addon or manual are never written. Only the rows that change are
// returned, ordered by feature key.
func PlanTransition(from, to Plan, rows []Entitlement, opts TransitionOptions) []Entitlement {
	from, to = normalizePlan(from), normalizePlan(to)
	features := PlanFeatures(to)
	oldDefaults, newDefaults := PlanQuotaDefaults(from), PlanQuotaDefaults(to)
	var out []Entitlement
	seen := map[string]bool{}
	for i := range rows {
		row := rows[i]
		seen[row.FeatureKey] = true
		origin := row.Origin
		if origin == "" {
			origin = DefaultOrigin(row.FeatureKey, row.Plan)
		}
		if origin != OriginPlan {
			continue
		}
		next := row
		next.Origin = OriginPlan
		next.Plan = to
		next.Limits = maps.Clone(row.Limits)
		if next.Limits == nil {
			next.Limits = map[string]int64{}
		}
		inPlan := slices.Contains(features, row.FeatureKey)
		next.Enabled = inPlan
		if inPlan {
			transitionLimits(row.FeatureKey, next.Limits, oldDefaults, newDefaults, opts.ResetLimits)
		}
		if row.Plan != to || row.Enabled != next.Enabled || !maps.Equal(row.Limits, next.Limits) {
			out = append(out, next)
		}
	}
	for _, f := range features {
		if seen[f] {
			continue
		}
		limits := map[string]int64{}
		for quota, value := range newDefaults {
			if feature, _ := QuotaFeatureOf(quota); feature == f {
				limits[quota] = value
			}
		}
		out = append(out, Entitlement{FeatureKey: f, Plan: to, Enabled: true, Limits: limits,
			Source: "manual", Origin: OriginPlan})
	}
	slices.SortFunc(out, func(a, b Entitlement) int { return strings.Compare(a.FeatureKey, b.FeatureKey) })
	return out
}

// transitionLimits applies the limit rule of PlanTransition to the limits
// of one row of the feature.
func transitionLimits(feature string, limits, oldDefaults, newDefaults map[string]int64, reset bool) {
	quotas := map[string]bool{}
	for _, m := range []map[string]int64{oldDefaults, newDefaults, limits} {
		for quota := range m {
			if f, _ := QuotaFeatureOf(quota); f == feature {
				quotas[quota] = true
			}
		}
	}
	for quota := range quotas {
		value, stored := limits[quota]
		oldDefault, hadDefault := oldDefaults[quota]
		follows := reset || !stored || (hadDefault && value == oldDefault)
		if !follows {
			continue
		}
		if def, ok := newDefaults[quota]; ok {
			limits[quota] = def
		} else {
			delete(limits, quota)
		}
	}
}

// PlanWriter locks and writes organization.plan (WP-882). PGRepository and
// MemoryRepository implement it.
type PlanWriter interface {
	// LockOrganizationPlan reads the plan of the organization and locks
	// its row until the end of the context transaction.
	LockOrganizationPlan(ctx context.Context, orgID string) (Plan, error)
	// SetOrganizationPlan stores the plan and plan_changed_at = now().
	SetOrganizationPlan(ctx context.Context, orgID string, plan Plan) (time.Time, error)
}

// ErrPlanChangeUnavailable is returned by ChangePlan when the repository
// cannot write the plan of an organization.
var ErrPlanChangeUnavailable = errors.New("entitlement: plan change is not available")

// PlanChange is the result of ChangePlan.
type PlanChange struct {
	From      Plan
	To        Plan
	ChangedAt *time.Time
	// Unchanged: the organization already had the plan; nothing was written.
	Unchanged bool
	// Before are the stored rows before the change, Written the rows the
	// change wrote (PlanTransition).
	Before  []Entitlement
	Written []Entitlement
	// Changed are the feature keys and quotas that changed, sorted.
	Changed []string
}

// ChangePlan changes the plan of the organization (PKG-02, WP-882): in the
// context transaction it locks organization.plan, writes the rows of
// PlanTransition through Grant and stores the new plan. The stored rows are
// read from the repository, not the cache; the cache of every instance is
// dropped after the commit (Grant). A plan equal to the current one changes
// nothing (Unchanged).
func (s *Service) ChangePlan(ctx context.Context, orgID string, to Plan, opts TransitionOptions) (PlanChange, error) {
	writer, ok := s.repo.(PlanWriter)
	if !ok {
		return PlanChange{}, ErrPlanChangeUnavailable
	}
	to = normalizePlan(to)
	from, err := writer.LockOrganizationPlan(ctx, orgID)
	if err != nil {
		return PlanChange{}, err
	}
	before, err := s.repo.List(ctx, orgID)
	if err != nil {
		return PlanChange{}, err
	}
	change := PlanChange{From: from, To: to, Before: before}
	if from == to && !opts.ResetLimits {
		change.Unchanged = true
		return change, nil
	}
	changed := map[string]bool{}
	prev := map[string]Entitlement{}
	for i := range before {
		prev[before[i].FeatureKey] = before[i]
	}
	rows := PlanTransition(from, to, before, opts)
	for i := range rows {
		row := &rows[i]
		row.OrganizationID = orgID
		stored, grantErr := s.Grant(ctx, row)
		if grantErr != nil {
			return PlanChange{}, fmt.Errorf("plan change %s: %w", row.FeatureKey, grantErr)
		}
		change.Written = append(change.Written, stored)
		old, existed := prev[row.FeatureKey]
		if !existed || old.Enabled != stored.Enabled {
			changed[row.FeatureKey] = true
		}
		for quota := range quotaKeys(old.Limits, stored.Limits) {
			ov, oOK := old.Limits[quota]
			nv, nOK := stored.Limits[quota]
			if oOK != nOK || ov != nv {
				changed[quota] = true
			}
		}
	}
	at, err := writer.SetOrganizationPlan(ctx, orgID, to)
	if err != nil {
		return PlanChange{}, err
	}
	change.ChangedAt = &at
	change.Changed = slices.Sorted(maps.Keys(changed))
	return change, nil
}

func quotaKeys(a, b map[string]int64) map[string]bool {
	out := map[string]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}
