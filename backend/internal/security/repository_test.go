package security

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

func TestFindingLifecycleAndSummary(t *testing.T) {
	repo := NewMemoryRepository()
	f := &Finding{OrganizationID: "org-1", CIID: "ci-1", Kind: "vulnerability", Severity: "high", Title: "OpenSSL CVE-2024-0001", PackageName: "openssl", InstalledVersion: "3.0.2", FixedVersion: "3.0.7", Reference: "CVE-2024-0001"}
	if err := repo.Create(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if f.Status != "open" {
		t.Fatalf("expected open, got %s", f.Status)
	}
	summary, _ := repo.Summary(context.Background(), "org-1")
	if summary["high"] != 1 {
		t.Fatalf("expected 1 high open, got %v", summary)
	}
	// resolve drops it from summary
	resolved := "resolved"
	if _, err := repo.Update(context.Background(), "org-1", f.ID, UpdateFindingRequest{Status: &resolved}); err != nil {
		t.Fatal(err)
	}
	summary, _ = repo.Summary(context.Background(), "org-1")
	if summary["high"] != 0 {
		t.Fatalf("expected 0 high open after resolve, got %v", summary)
	}
	// tenant isolation
	if _, err := repo.GetByID(context.Background(), "org-2", f.ID); err == nil {
		t.Fatal("cross-tenant read must fail")
	}
	// filters
	items, total, _ := repo.List(context.Background(), "org-1", FilterParams{Kind: "vulnerability"}, api.PaginationParams{Limit: 10})
	if total != 1 || items[0].ID != f.ID {
		t.Fatalf("expected 1 vulnerability, got %d", total)
	}
}
