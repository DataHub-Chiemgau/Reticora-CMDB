package entitlement

import (
	"context"
	"errors"
	"math/big"
	"net/http"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// budgetService returns an enforcing service whose organization "o" has
// monitoring with limits.
func budgetService(t *testing.T, limits map[string]int64) *Service {
	t.Helper()
	repo := NewMemoryRepository()
	if _, err := repo.Upsert(context.Background(), &Entitlement{OrganizationID: "o", FeatureKey: FeatureMonitoring,
		Plan: PlanStandard, Enabled: true, Limits: limits}); err != nil {
		t.Fatal(err)
	}
	return NewService(repo, Options{Enforce: true})
}

// specs returns n active icmp probes with distinct targets at interval.
func specs(n int, typ string, interval int64) []EffectiveSpec {
	out := make([]EffectiveSpec, 0, n)
	for i := range n {
		out = append(out, EffectiveSpec{Type: typ, Target: typ + "-" + string(rune('a'+i%26)) + string(rune('a'+i/26)), IntervalS: interval, Active: true})
	}
	return out
}

// TestBudgetPerMinute covers AK1 of WP-940: 30 s are 2 checks per minute
// per probe, 7 s exactly 60/7; targets count distinct targets of active
// probes, per type as well.
func TestBudgetPerMinute(t *testing.T) {
	b := BudgetOf([]EffectiveSpec{
		{Type: "icmp", Target: "a", IntervalS: 30, Active: true},
		{Type: "tcp", Target: "a", IntervalS: 30, Active: true},
		{Type: "icmp", Target: "b", IntervalS: 7, Active: true},
		{Type: "icmp", Target: "c", IntervalS: 10, Active: false},
	})
	want := new(big.Rat).Add(big.NewRat(4, 1), big.NewRat(60, 7))
	if b.PerMinute.Cmp(want) != 0 {
		t.Errorf("per minute = %s, want %s", b.PerMinute.RatString(), want.RatString())
	}
	if b.Targets != 2 || b.PerType["icmp"] != 2 || b.PerType["tcp"] != 1 || b.MinIntervalS != 7 {
		t.Errorf("budget = %v", b)
	}
	if one := BudgetOf(specs(1, "icmp", 30)); one.PerMinute.Cmp(big.NewRat(2, 1)) != 0 {
		t.Errorf("30 s: %s per minute, want 2", one.PerMinute.RatString())
	}
	if empty := BudgetOf(nil); empty.Targets != 0 || empty.PerMinute.Sign() != 0 || empty.MinIntervalS != 0 {
		t.Errorf("empty budget = %v", empty)
	}
}

// TestAllowProbeChangeAtLimit covers AK2: the limits are reachable exactly,
// one above is 403 entitlement-limit naming quota and limit.
func TestAllowProbeChangeAtLimit(t *testing.T) {
	svc := budgetService(t, map[string]int64{LimitMaxMonitoredTargets: 5, LimitMaxProbesPerMinute: 10})
	ctx := context.Background()
	before := BudgetOf(specs(4, "icmp", 30))
	if err := svc.AllowProbeChange(ctx, "o", before, BudgetOf(specs(5, "icmp", 30))); err != nil {
		t.Fatalf("5 of 5 targets, 10 of 10 per minute: %v", err)
	}
	var le *LimitExceededError
	err := svc.AllowProbeChange(ctx, "o", before, BudgetOf(specs(6, "icmp", 60)))
	if !errors.As(err, &le) || le.FeatureKey != LimitMaxMonitoredTargets || le.Limit != 5 {
		t.Fatalf("6 targets: %v, want max_monitored_targets 5", err)
	}
	if !le.LimitExceeded() || le.ProblemType() != api.ProblemEntitlementLimit {
		t.Errorf("6 targets: %v is no 403 entitlement-limit", err)
	}
	// 10 checks per minute exactly, then 10 + 60/59.
	at := BudgetOf(specs(5, "icmp", 30))
	over := BudgetOf(append(specs(4, "icmp", 30), EffectiveSpec{Type: "icmp", Target: "icmp-ea", IntervalS: 29, Active: true}))
	if err := svc.AllowProbeChange(ctx, "o", at, over); !errors.As(err, &le) || le.FeatureKey != LimitMaxProbesPerMinute || le.Limit != 10 {
		t.Fatalf("over 10 per minute: %v, want max_probes_per_minute", err)
	}
}

// TestAllowProbeChangeTypeCap covers the cap max_targets_<type> (E-154).
func TestAllowProbeChangeTypeCap(t *testing.T) {
	svc := budgetService(t, map[string]int64{"max_targets_icmp": 2})
	ctx := context.Background()
	if err := svc.AllowProbeChange(ctx, "o", BudgetOf(specs(1, "icmp", 30)), BudgetOf(specs(2, "icmp", 30))); err != nil {
		t.Fatalf("2 of 2 icmp targets: %v", err)
	}
	var le *LimitExceededError
	if err := svc.AllowProbeChange(ctx, "o", BudgetOf(specs(2, "icmp", 30)), BudgetOf(specs(3, "icmp", 30))); !errors.As(err, &le) ||
		le.FeatureKey != "max_targets_icmp" || le.Limit != 2 {
		t.Fatalf("3 icmp targets: %v, want max_targets_icmp 2", err)
	}
	// Another type is not capped.
	if err := svc.AllowProbeChange(ctx, "o", BudgetOf(nil), BudgetOf(specs(5, "tcp", 30))); err != nil {
		t.Errorf("tcp targets without cap: %v", err)
	}
}

// TestAllowProbeChangeIntervalBelowMinimum covers min_probe_interval_s:
// 422 on interval_s.
func TestAllowProbeChangeIntervalBelowMinimum(t *testing.T) {
	svc := budgetService(t, map[string]int64{LimitMinProbeIntervalS: 30})
	ctx := context.Background()
	var fe *api.FieldError
	err := svc.AllowProbeChange(ctx, "o", BudgetOf(specs(1, "icmp", 30)), BudgetOf(specs(1, "icmp", 20)))
	if !errors.As(err, &fe) || fe.Field != "interval_s" || fe.Status != http.StatusUnprocessableEntity {
		t.Fatalf("20 s: %v, want 422 on interval_s", err)
	}
	if err := svc.AllowProbeChange(ctx, "o", BudgetOf(nil), BudgetOf(specs(1, "icmp", 30))); err != nil {
		t.Errorf("30 s: %v", err)
	}
}

// TestDecreaseNeverRefused: a change that raises nothing passes even above
// every limit (stock after a plan change, WP-942 AK3).
func TestDecreaseNeverRefused(t *testing.T) {
	svc := budgetService(t, map[string]int64{LimitMaxMonitoredTargets: 1, LimitMaxProbesPerMinute: 1,
		LimitMinProbeIntervalS: 60, "max_targets_icmp": 1})
	ctx := context.Background()
	before := BudgetOf(specs(10, "icmp", 10))
	for name, after := range map[string]ProbeBudget{
		"fewer":     BudgetOf(specs(9, "icmp", 10)),
		"slower":    BudgetOf(specs(10, "icmp", 20)),
		"unchanged": BudgetOf(specs(10, "icmp", 10)),
		"none":      BudgetOf(nil),
	} {
		if err := svc.AllowProbeChange(ctx, "o", before, after); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Without enforcement every change passes.
	off := NewService(NewMemoryRepository(), Options{})
	if err := off.AllowProbeChange(ctx, "o", BudgetOf(nil), before); err != nil {
		t.Errorf("enforcement off: %v", err)
	}
}
