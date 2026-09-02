package composition

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// These tests run against a migrated PostgreSQL instance and are skipped
// unless TEST_DATABASE_URL is set.
//
// They are the regression tests for the composition-integrity defect fixed by
// migration 000057: nothing prevented a serialized parent asset from becoming
// its own ancestor, so an operator could build A -> B -> C -> A and produce an
// unwalkable ownership graph.

const (
	cycOrg    = "cccc3333-0000-4000-8000-000000000001"
	cycAssetA = "cccc3333-0000-4000-8000-0000000000a1"
	cycAssetB = "cccc3333-0000-4000-8000-0000000000b1"
	cycAssetC = "cccc3333-0000-4000-8000-0000000000c1"
)

func setupCompositionDB(t *testing.T) *PGRepository {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()

	admin, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatalf("maintenance pool: %v", err)
	}
	t.Cleanup(admin.Close)

	cleanup := func() {
		// session_replication_role is session-scoped, so this does not race
		// with other packages running against the same database.
		_, _ = admin.Exec(ctx, "SET session_replication_role = replica")
		for _, stmt := range []string{
			`DELETE FROM composition WHERE organization_id = $1`,
			`DELETE FROM asset WHERE organization_id = $1`,
			`DELETE FROM organization WHERE id = $1`,
		} {
			if _, err := admin.Exec(ctx, stmt, cycOrg); err != nil {
				t.Logf("cleanup %q: %v", stmt, err)
			}
		}
		_, _ = admin.Exec(ctx, "SET session_replication_role = origin")
	}
	cleanup()
	t.Cleanup(cleanup)

	if _, err := admin.Exec(ctx,
		`INSERT INTO organization (id, name, slug) VALUES ($1, 'Composition Cycle Org', 'composition-cycle-org')`,
		cycOrg); err != nil {
		t.Fatalf("seed organization: %v", err)
	}
	for _, a := range []struct{ id, name, tag string }{
		{cycAssetA, "Chassis A", "CYC-A"},
		{cycAssetB, "Chassis B", "CYC-B"},
		{cycAssetC, "Chassis C", "CYC-C"},
	} {
		if _, err := admin.Exec(ctx,
			`INSERT INTO asset (id, organization_id, name, asset_tag) VALUES ($1, $2, $3, $4)`,
			a.id, cycOrg, a.name, a.tag); err != nil {
			t.Fatalf("seed asset %s: %v", a.name, err)
		}
	}

	pool, err := database.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("application pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return NewPGRepository(pool)
}

func TestCompositionRejectsRecursiveOwnership(t *testing.T) {
	repo := setupCompositionDB(t)
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: cycOrg})

	t.Run("asset cannot be its own parent", func(t *testing.T) {
		err := repo.Create(ctx, &Composition{
			OrganizationID: cycOrg, ParentAssetID: cycAssetA, ChildAssetID: cycAssetA,
		})
		if err == nil {
			t.Fatal("expected self-parent link to be rejected")
		}
		if !strings.Contains(err.Error(), "composition cycle") {
			t.Fatalf("expected a cycle error, got %v", err)
		}
	})

	t.Run("legitimate chain is allowed", func(t *testing.T) {
		if err := repo.Create(ctx, &Composition{
			OrganizationID: cycOrg, ParentAssetID: cycAssetA, ChildAssetID: cycAssetB,
		}); err != nil {
			t.Fatalf("A -> B must be allowed: %v", err)
		}
		if err := repo.Create(ctx, &Composition{
			OrganizationID: cycOrg, ParentAssetID: cycAssetB, ChildAssetID: cycAssetC,
		}); err != nil {
			t.Fatalf("B -> C must be allowed: %v", err)
		}
	})

	t.Run("closing the cycle is rejected", func(t *testing.T) {
		err := repo.Create(ctx, &Composition{
			OrganizationID: cycOrg, ParentAssetID: cycAssetC, ChildAssetID: cycAssetA,
		})
		if err == nil {
			t.Fatal("expected C -> A to be rejected as a cycle")
		}
		if !strings.Contains(err.Error(), "composition cycle") {
			t.Fatalf("expected a cycle error, got %v", err)
		}
	})
}
