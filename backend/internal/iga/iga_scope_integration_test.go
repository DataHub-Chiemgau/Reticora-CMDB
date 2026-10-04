package iga_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/iga"
)

// TestIGARepositoryScope runs the IGA repository with the principal's tenant
// scope (TEN-06, WP-021). Access reviews belong to the organization: a
// principal of organization A sees A's reviews only, naming organization B
// fails, and a call without scope fails closed.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestIGARepositoryScope(t *testing.T) {
	f := scopetest.Seed(t, "40")
	repo := iga.NewPGRepository(f.App)
	if err := repo.CreateReview(f.OrgCtx(f.OrgB), &iga.AccessReview{OrganizationID: f.OrgB, Name: "b", Status: "draft"}, nil); err != nil {
		t.Fatalf("organization B review: %v", err)
	}
	ctx := f.ClientCtx(f.Client1)
	reviewA := &iga.AccessReview{OrganizationID: f.OrgA, Name: "a", Status: "draft"}
	if err := repo.CreateReview(ctx, reviewA, nil); err != nil {
		t.Fatalf("organization A review: %v", err)
	}
	list, total, err := repo.ListReviews(ctx, f.OrgA, "", api.PaginationParams{Limit: 100})
	if err != nil || total != 1 || len(list) != 1 || list[0].ID != reviewA.ID {
		t.Fatalf("organization A reviews: total=%d %+v err=%v, want only the A review", total, list, err)
	}
	if _, _, err = repo.ListReviews(ctx, f.OrgB, "", api.PaginationParams{Limit: 10}); !errors.Is(err, database.ErrTenantMismatch) {
		t.Fatalf("list organization B reviews: got %v, want ErrTenantMismatch", err)
	}
	if _, _, err = repo.ListReviews(context.Background(), f.OrgA, "", api.PaginationParams{Limit: 10}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
