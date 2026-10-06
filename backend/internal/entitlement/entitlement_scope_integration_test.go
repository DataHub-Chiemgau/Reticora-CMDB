package entitlement_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
)

// TestEntitlementRepositoryScope runs the entitlement repository with the
// principal's tenant scope (TEN-06, WP-012). Entitlements belong to the
// organization: a principal of organization A reads A's entitlements only and
// cannot write those of organization B.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestEntitlementRepositoryScope(t *testing.T) {
	f := scopetest.Seed(t, "1c")
	repo := entitlement.NewPGRepository(f.App)
	if _, err := repo.Upsert(f.OrgCtx(f.OrgB), entitlement.Entitlement{OrganizationID: f.OrgB, FeatureKey: "monitoring", Plan: entitlement.PlanPro, Enabled: true}); err != nil {
		t.Fatalf("org B upsert: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	if _, err := repo.Upsert(ctx, entitlement.Entitlement{OrganizationID: f.OrgA, FeatureKey: entitlement.FeatureCMDBCore, Plan: entitlement.PlanStandard, Enabled: true}); err != nil {
		t.Fatalf("org A upsert: %v", err)
	}
	list, err := repo.List(ctx, f.OrgA)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, e := range list {
		if e.OrganizationID != f.OrgA {
			t.Fatalf("organization A list contains %+v", e)
		}
	}
	if len(list) == 0 {
		t.Fatal("organization A list is empty")
	}
	if _, err = repo.Upsert(ctx, entitlement.Entitlement{OrganizationID: f.OrgB, FeatureKey: "monitoring", Enabled: false}); !errors.Is(err, database.ErrTenantMismatch) {
		t.Fatalf("write organization B entitlement: got %v, want ErrTenantMismatch", err)
	}
	var enabled bool
	if err = f.Admin.QueryRow(context.Background(),
		`SELECT enabled FROM entitlement WHERE organization_id = $1 AND feature_key = 'monitoring'`, f.OrgB).Scan(&enabled); err != nil {
		t.Fatalf("read organization B entitlement: %v", err)
	}
	if !enabled {
		t.Fatal("organization B entitlement was changed")
	}
	if _, err = repo.List(context.Background(), f.OrgA); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
