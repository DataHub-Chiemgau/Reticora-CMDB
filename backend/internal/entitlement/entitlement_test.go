package entitlement

import (
	"context"
	"testing"
)

func TestEntitlementService(t *testing.T) {
	svc := NewService()
	ctx := context.Background()

	// No entitlements initially
	if svc.IsEnabled(ctx, "org-1", "ticketing") {
		t.Error("expected ticketing to be disabled")
	}

	// Grant and check
	svc.Grant("org-1", "ticketing", PlanEssential)
	if !svc.IsEnabled(ctx, "org-1", "ticketing") {
		t.Error("expected ticketing to be enabled")
	}

	// Different org should not have it
	if svc.IsEnabled(ctx, "org-2", "ticketing") {
		t.Error("expected ticketing to be disabled for org-2")
	}

	// Different feature should not be enabled
	if svc.IsEnabled(ctx, "org-1", "iga") {
		t.Error("expected iga to be disabled")
	}
}
