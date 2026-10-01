package locationnode_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locationnode"
)

// TestLocationNodeRepositoryClientScope runs the location node repository
// with the principal's tenant scope (TEN-06, WP-014): a principal restricted
// to client 1 neither sees nor changes a client-2 node and cannot create one.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestLocationNodeRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "23")
	repo := locationnode.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	own := &locationnode.Node{OrganizationID: f.OrgA, ClientID: f.Client1, NodeType: "floor", Name: "node-c1", Attributes: map[string]any{}}
	foreign := &locationnode.Node{OrganizationID: f.OrgA, ClientID: f.Client2, NodeType: "floor", Name: "node-c2", Attributes: map[string]any{}}
	for _, n := range []*locationnode.Node{own, foreign} {
		if err := repo.Create(orgCtx, n); err != nil {
			t.Fatalf("org-wide node %s: %v", n.Name, err)
		}
	}

	ctx := f.ClientCtx(f.Client1)
	nodes, err := repo.List(ctx, f.OrgA, locationnode.FilterParams{})
	if err != nil || len(nodes) != 1 || nodes[0].ID != own.ID {
		t.Fatalf("client-1 nodes: %+v err=%v, want only the client-1 node", nodes, err)
	}
	if _, err = repo.GetByID(ctx, f.OrgA, foreign.ID); err == nil {
		t.Fatal("client-1 principal read a client-2 node")
	}
	if err = repo.Delete(ctx, f.OrgA, foreign.ID); err == nil {
		t.Fatal("client-1 principal deleted a client-2 node")
	}
	if err = repo.Create(ctx, &locationnode.Node{OrganizationID: f.OrgA, ClientID: f.Client2, NodeType: "floor", Name: "intruder", Attributes: map[string]any{}}); err == nil {
		t.Fatal("client-1 principal created a client-2 node")
	}
	var count int
	if err = f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM location_node WHERE id = $1`, foreign.ID).Scan(&count); err != nil {
		t.Fatalf("count client-2 node: %v", err)
	}
	if count != 1 {
		t.Fatal("client-2 node was deleted")
	}
	if _, err = repo.List(context.Background(), f.OrgA, locationnode.FilterParams{}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
