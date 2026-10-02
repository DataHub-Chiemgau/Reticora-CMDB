package maintenance_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/maintenance"
)

// TestMaintenanceRepositoryClientScope runs the maintenance repository with
// the principal's tenant scope (TEN-06, WP-020): a principal restricted to
// client 1 neither sees a window that only affects client-2 CIs nor plans
// one for a client-2 CI.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestMaintenanceRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "3a")
	ownCI := f.CI(t, f.OrgA, f.Client1, "maint-c1")
	foreignCI := f.CI(t, f.OrgA, f.Client2, "maint-c2")

	repo := maintenance.NewPGRepository(f.App)
	start := time.Now().UTC().Add(24 * time.Hour)
	win := func(ciID string) *maintenance.Window {
		return &maintenance.Window{OrganizationID: f.OrgA, Title: "Patch", StartsAt: start, EndsAt: start.Add(time.Hour), CIIDs: []string{ciID}}
	}
	foreign := win(foreignCI)
	if err := repo.Create(f.OrgCtx(f.OrgA), foreign); err != nil {
		t.Fatalf("org-wide window: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	if err := repo.Create(ctx, win(foreignCI)); err == nil {
		t.Fatal("client-1 principal planned maintenance of a client-2 CI")
	}
	own := win(ownCI)
	if err := repo.Create(ctx, own); err != nil {
		t.Fatalf("client-1 window for own CI: %v", err)
	}
	list, total, err := repo.List(ctx, f.OrgA, maintenance.FilterParams{}, api.PaginationParams{Limit: 100})
	if err != nil || total != 1 || len(list) != 1 || list[0].ID != own.ID {
		t.Fatalf("client-1 windows: total=%d %+v err=%v, want only the own one", total, list, err)
	}
	if _, err = repo.GetByID(ctx, f.OrgA, foreign.ID); err == nil {
		t.Fatal("client-1 principal read a window of a client-2 CI")
	}
	if err = repo.Delete(ctx, f.OrgA, foreign.ID); err == nil {
		t.Fatal("client-1 principal deleted a window of a client-2 CI")
	}
	var count int
	if err = f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM maintenance_window WHERE id = $1`, foreign.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("client-2 window: count=%d err=%v, want 1", count, err)
	}
	if _, _, err = repo.List(context.Background(), f.OrgA, maintenance.FilterParams{}, api.PaginationParams{Limit: 10}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
