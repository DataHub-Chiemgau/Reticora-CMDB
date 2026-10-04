package ci_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// TestCIRepositoryClientScope runs the CI repository with the principal's
// tenant scope from the request context (TEN-06, WP-010): a principal
// restricted to client 1 neither sees nor changes a CI of client 2 or of
// another organization, and a call without scope fails closed.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCIRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "10")
	own := f.CI(t, f.OrgA, f.Client1, "ci-client-1")
	foreign := f.CI(t, f.OrgA, f.Client2, "ci-client-2")
	otherOrg := f.CI(t, f.OrgB, "", "ci-org-b")

	repo := ci.NewPGRepository(f.App)
	ctx := f.ClientCtx(f.Client1)
	page := api.PaginationParams{Limit: 100}

	items, _, err := repo.List(ctx, f.OrgA, ci.FilterParams{}, page)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	seen := map[string]bool{}
	for _, it := range items {
		seen[it.ID] = true
	}
	if !seen[own] || seen[foreign] || seen[otherOrg] {
		t.Fatalf("client-1 list: own=%v foreign=%v other org=%v, want true/false/false", seen[own], seen[foreign], seen[otherOrg])
	}

	if _, err = repo.GetByID(ctx, f.OrgA, foreign); err == nil {
		t.Fatal("client-1 principal read a CI of client 2")
	}

	name := "renamed"
	if _, err = repo.Update(ctx, f.OrgA, foreign, ci.UpdateRequest{Name: &name}); err == nil {
		t.Fatal("client-1 principal updated a CI of client 2")
	}
	if err = repo.Delete(ctx, f.OrgA, foreign); err == nil {
		t.Fatal("client-1 principal deleted a CI of client 2")
	}
	var gotName string
	var deleted bool
	if err = f.Admin.QueryRow(context.Background(),
		`SELECT name, deleted_at IS NOT NULL FROM ci WHERE id = $1`, foreign).Scan(&gotName, &deleted); err != nil {
		t.Fatalf("read foreign ci: %v", err)
	}
	if gotName != "ci-client-2" || deleted {
		t.Fatalf("foreign CI changed: name=%q deleted=%v", gotName, deleted)
	}

	// Moving the own CI to client 2 and creating a CI for client 2 both
	// violate the policy's WITH CHECK.
	client2 := f.Client2
	if _, err = repo.Update(ctx, f.OrgA, own, ci.UpdateRequest{ClientID: &client2}); err == nil {
		t.Fatal("client-1 principal moved a CI to client 2")
	}
	typeID := ciTypeOf(t, f, own)
	if err = repo.Create(ctx, &ci.Item{OrganizationID: f.OrgA, ClientID: f.Client2, CITypeID: typeID, Name: "new-c2", Status: "active"}); err == nil {
		t.Fatal("client-1 principal created a CI for client 2")
	}
	created := &ci.Item{OrganizationID: f.OrgA, ClientID: f.Client1, CITypeID: typeID, Name: "new-c1", Status: "active"}
	if err = repo.Create(ctx, created); err != nil {
		t.Fatalf("client-1 principal could not create a CI for client 1: %v", err)
	}

	// An org-wide principal of organization A sees both clients.
	items, _, err = repo.List(f.OrgCtx(f.OrgA), f.OrgA, ci.FilterParams{}, page)
	if err != nil {
		t.Fatalf("org-wide list: %v", err)
	}
	seen = map[string]bool{}
	for _, it := range items {
		seen[it.ID] = true
	}
	if !seen[own] || !seen[foreign] || seen[otherOrg] {
		t.Fatalf("org-wide list: own=%v foreign=%v other org=%v, want true/true/false", seen[own], seen[foreign], seen[otherOrg])
	}

	if _, _, err = repo.List(context.Background(), f.OrgA, ci.FilterParams{}, page); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
	if _, err = repo.GetByID(ctx, f.OrgB, otherOrg); !errors.Is(err, database.ErrTenantMismatch) {
		t.Fatalf("read with another organization: got %v, want ErrTenantMismatch", err)
	}
}

func ciTypeOf(t *testing.T, f *scopetest.Fixture, ciID string) string {
	t.Helper()
	var typeID string
	if err := f.Admin.QueryRow(context.Background(), `SELECT ci_type_id::text FROM ci WHERE id = $1`, ciID).Scan(&typeID); err != nil {
		t.Fatalf("read ci type: %v", err)
	}
	return typeID
}
