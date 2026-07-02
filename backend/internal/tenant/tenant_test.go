package tenant

import (
	"context"
	"testing"
)

func TestTenantContext(t *testing.T) {
	ctx := context.Background()
	info := TenantInfo{
		OrganizationID: "org-123",
		ClientID:       "client-456",
	}

	ctx = WithTenant(ctx, info)
	got := FromContext(ctx)

	if got.OrganizationID != "org-123" {
		t.Errorf("expected org-123, got %s", got.OrganizationID)
	}
	if got.ClientID != "client-456" {
		t.Errorf("expected client-456, got %s", got.ClientID)
	}
}

func TestTenantContextEmpty(t *testing.T) {
	ctx := context.Background()
	got := FromContext(ctx)

	if got.OrganizationID != "" {
		t.Errorf("expected empty org, got %s", got.OrganizationID)
	}
}
