package training_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/training"
)

// TestTrainingRepositoryScope runs the training repository with the
// principal's tenant scope (TEN-06, WP-019). Trainings belong to the
// organization (team and person scope follow with WP-029): a principal of
// organization A manages A's courses only, courses of organization B stay
// invisible, and a call without scope fails closed.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestTrainingRepositoryScope(t *testing.T) {
	f := scopetest.Seed(t, "37")
	repo := training.NewPGRepository(f.App)
	courseB := &training.Course{OrganizationID: f.OrgB, Title: "B"}
	if err := repo.Create(f.OrgCtx(f.OrgB), courseB); err != nil {
		t.Fatalf("organization B course: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	courseA := &training.Course{OrganizationID: f.OrgA, Title: "A"}
	if err := repo.Create(ctx, courseA); err != nil {
		t.Fatalf("organization A course: %v", err)
	}
	list, _, err := repo.List(ctx, f.OrgA, training.FilterParams{}, api.PaginationParams{Limit: 100})
	if err != nil || len(list) != 1 || list[0].ID != courseA.ID {
		t.Fatalf("organization A courses: %+v err=%v, want only the A course", list, err)
	}
	if err = repo.Delete(ctx, f.OrgB, courseB.ID); !errors.Is(err, database.ErrTenantMismatch) {
		t.Fatalf("delete organization B course: got %v, want ErrTenantMismatch", err)
	}
	var count int
	if err = f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM training WHERE id = $1`, courseB.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("organization B course: count=%d err=%v, want 1", count, err)
	}
	if _, _, err = repo.List(context.Background(), f.OrgA, training.FilterParams{}, api.PaginationParams{Limit: 10}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
