package keymgmt_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/keymgmt"
)

// TestKeyRepositoryClientScope runs the key management repository with the
// principal's tenant scope (TEN-06, WP-019): a principal restricted to client
// 1 neither sees a client-2 key nor its handovers and cannot issue it.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestKeyRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "36")
	holder := f.AppUser(t, f.OrgA, "holder")
	repo := keymgmt.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	own := &keymgmt.Item{OrganizationID: f.OrgA, ClientID: f.Client1, Name: "key-c1", KeyType: "physical", Status: "available"}
	foreign := &keymgmt.Item{OrganizationID: f.OrgA, ClientID: f.Client2, Name: "key-c2", KeyType: "physical", Status: "available"}
	for _, k := range []*keymgmt.Item{own, foreign} {
		if err := repo.Create(orgCtx, k); err != nil {
			t.Fatalf("org-wide key %s: %v", k.Name, err)
		}
	}
	if _, err := repo.Issue(orgCtx, &keymgmt.Assignment{OrganizationID: f.OrgA, KeyItemID: foreign.ID, AssignedTo: holder}); err != nil {
		t.Fatalf("org-wide issue: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	list, _, err := repo.List(ctx, f.OrgA, keymgmt.FilterParams{}, api.PaginationParams{Limit: 100})
	if err != nil || len(list) != 1 || list[0].ID != own.ID {
		t.Fatalf("client-1 keys: %+v err=%v, want only the own one", list, err)
	}
	if handovers, listErr := repo.ListAssignments(ctx, f.OrgA, foreign.ID); listErr != nil || len(handovers) != 0 {
		t.Fatalf("client-1 handovers of client-2 key: %+v err=%v, want none", handovers, listErr)
	}
	if _, err = repo.Return(ctx, f.OrgA, foreign.ID); err == nil {
		t.Fatal("client-1 principal took back a client-2 key")
	}
	if _, err = repo.Issue(ctx, &keymgmt.Assignment{OrganizationID: f.OrgA, KeyItemID: own.ID, AssignedTo: holder}); err != nil {
		t.Fatalf("client-1 issue of own key: %v", err)
	}
	var status string
	if err = f.Admin.QueryRow(context.Background(), `SELECT status FROM key_item WHERE id = $1`, foreign.ID).Scan(&status); err != nil || status != "issued" {
		t.Fatalf("client-2 key: status=%q err=%v, want issued", status, err)
	}
	if _, _, err = repo.List(context.Background(), f.OrgA, keymgmt.FilterParams{}, api.PaginationParams{Limit: 10}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
