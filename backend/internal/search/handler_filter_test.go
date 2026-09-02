package search

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

type erroringPermissions struct {
	permission.Repository
	err error
}

func (e erroringPermissions) HasPermission(context.Context, string, string, string) (bool, error) {
	return false, e.err
}

type stubPermissions struct {
	permission.Repository
	allowed map[string]bool
	calls   int
}

func (s *stubPermissions) HasPermission(_ context.Context, _, _, key string) (bool, error) {
	s.calls++
	return s.allowed[key], nil
}

func TestFilterAllowedPropagatesPermissionError(t *testing.T) {
	boom := errors.New("db unreachable")
	h := NewHandler(nil, erroringPermissions{err: boom})
	hits := []Hit{{Document: Document{EntityType: "ci"}}, {Document: Document{EntityType: "asset"}}}
	_, err := h.filterAllowed(context.Background(), tenant.TenantInfo{OrganizationID: "org", UserID: "user"}, hits)
	if !errors.Is(err, boom) {
		t.Fatalf("expected permission error to propagate, got %v", err)
	}
}

func TestFilterAllowedFiltersByPermission(t *testing.T) {
	perms := &stubPermissions{allowed: map[string]bool{"ci:read": true, "asset:read": false}}
	h := NewHandler(nil, perms)
	hits := []Hit{{Document: Document{EntityType: "ci"}}, {Document: Document{EntityType: "asset"}}, {Document: Document{EntityType: "ci"}}}
	out, err := h.filterAllowed(context.Background(), tenant.TenantInfo{OrganizationID: "org", UserID: "user"}, hits)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 ci hits, got %d", len(out))
	}
	if perms.calls != 2 {
		t.Fatalf("expected cached lookups (2 calls), got %d", perms.calls)
	}
}

func TestFilterAllowedSkipsWithoutUser(t *testing.T) {
	h := NewHandler(nil, erroringPermissions{err: errors.New("must not be called")})
	hits := []Hit{{Document: Document{EntityType: "ci"}}}
	out, err := h.filterAllowed(context.Background(), tenant.TenantInfo{OrganizationID: "org"}, hits)
	if err != nil || len(out) != 1 {
		t.Fatalf("expected passthrough without user, got %v / %d", err, len(out))
	}
}

func TestPermissionForCoversIndexedEntityTypes(t *testing.T) {
	// Every entity type inserted by ReindexTenant must map to a permission
	// so filterAllowed does not silently drop indexed documents.
	for _, entity := range []string{"ci", "asset", "document", "ticket", "contact", "compliance", "location", "reservation"} {
		if permissionFor(entity) == "" {
			t.Fatalf("permissionFor(%q) returned empty", entity)
		}
	}
}
