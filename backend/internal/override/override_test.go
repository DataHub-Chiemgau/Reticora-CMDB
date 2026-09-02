package override

import (
	"context"
	"testing"
)

func TestResolveEffectiveProtectedOverrideWins(t *testing.T) {
	fv := &FieldValue{
		DiscoveredValue: float64(32), DiscoveredSource: "agent",
		OverrideValue: float64(64), Protected: true,
	}
	if got := ResolveEffective(fv, nil); got != float64(64) {
		t.Fatalf("protected override must win, got %v", got)
	}
	if !IsDiverged(fv, nil) {
		t.Fatal("expected divergence between discovered and effective")
	}
}

func TestResolveEffectiveUnprotectedOverridePolicyRanked(t *testing.T) {
	fv := &FieldValue{
		DiscoveredValue: "a", DiscoveredSource: "agent",
		OverrideValue: "b", Protected: false,
	}
	// Default policy ranks manual_override first → override wins.
	if got := ResolveEffective(fv, nil); got != "b" {
		t.Fatalf("expected override under default policy, got %v", got)
	}
	// A policy ranking agent above manual_override lets discovery win.
	if got := ResolveEffective(fv, []string{"agent", "manual_override"}); got != "a" {
		t.Fatalf("expected discovered value under agent-first policy, got %v", got)
	}
}

func TestResolveEffectiveDiscoveredOnly(t *testing.T) {
	fv := &FieldValue{DiscoveredValue: "x", DiscoveredSource: "snmp"}
	if got := ResolveEffective(fv, nil); got != "x" {
		t.Fatalf("got %v", got)
	}
	if IsDiverged(fv, nil) {
		t.Fatal("no divergence expected without an override")
	}
}

func TestOverrideLifecycleAndRediscoverySurvival(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()

	if _, err := repo.RecordDiscovered(ctx, "org", "ci1", "ram_gb", float64(32), "agent"); err != nil {
		t.Fatal(err)
	}
	fv, err := repo.SetOverride(ctx, "org", "ci1", "ram_gb", float64(64), "user", "capacity upgrade", true)
	if err != nil {
		t.Fatal(err)
	}
	if fv.EffectiveValue != float64(64) {
		t.Fatalf("effective must be the override, got %v", fv.EffectiveValue)
	}

	// Re-discovery must not clobber the protected override (spec §13).
	fv, err = repo.RecordDiscovered(ctx, "org", "ci1", "ram_gb", float64(32), "agent")
	if err != nil {
		t.Fatal(err)
	}
	if fv.EffectiveValue != float64(64) {
		t.Fatalf("protected override must survive re-discovery, got %v", fv.EffectiveValue)
	}
	protected, err := IsProtected(ctx, repo, "org", "ci1", "ram_gb")
	if err != nil || !protected {
		t.Fatalf("expected protected=true, got %v err=%v", protected, err)
	}

	// Clearing restores the discovered value.
	fv, err = repo.ClearOverride(ctx, "org", "ci1", "ram_gb")
	if err != nil {
		t.Fatal(err)
	}
	if fv.EffectiveValue != float64(32) {
		t.Fatalf("effective must revert to discovered, got %v", fv.EffectiveValue)
	}
}

func TestSetOverrideRequiresReason(t *testing.T) {
	repo := NewMemoryRepository()
	if _, err := repo.SetOverride(context.Background(), "org", "ci1", "f", "v", "user", "", false); err == nil {
		t.Fatal("expected an error for a missing override reason")
	}
}

func TestListForCITenantScoped(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()
	if _, err := repo.RecordDiscovered(ctx, "org-a", "ci1", "f", "v", "agent"); err != nil {
		t.Fatal(err)
	}
	out, err := repo.ListForCI(ctx, "org-b", "ci1")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("expected no cross-tenant field values, got %d", len(out))
	}
}
