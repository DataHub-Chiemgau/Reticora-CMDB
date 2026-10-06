package entitlement_test

import (
	"context"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
)

// TestEntitlementModel covers WP-071 (ENT-01) against PostgreSQL: rows carry
// limits as JSONB, valid_until and a source; the database itself refuses an
// unknown source and a disabled or expiring cmdb_core; a stored quota
// replaces the plan default.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestEntitlementModel(t *testing.T) {
	f := scopetest.Seed(t, "63")
	bg := context.Background()
	ctx := f.OrgCtx(f.OrgA)
	repo := entitlement.NewPGRepository(f.App)
	svc := entitlement.NewService(repo, entitlement.Options{Enforce: true})

	until := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	if _, err := svc.Grant(ctx, entitlement.Entitlement{OrganizationID: f.OrgA, FeatureKey: entitlement.FeatureDiscovery,
		Plan: entitlement.PlanStandard, Enabled: true, Limits: map[string]int64{entitlement.LimitMaxCollectors: 7},
		ValidUntil: &until, Source: "billing"}); err != nil {
		t.Fatal(err)
	}
	items, err := repo.List(ctx, f.OrgA)
	if err != nil || len(items) != 1 {
		t.Fatalf("list: %+v, %v", items, err)
	}
	got := items[0]
	if got.Limits[entitlement.LimitMaxCollectors] != 7 || got.ValidUntil == nil || !got.ValidUntil.Equal(until) || got.Source != "billing" {
		t.Errorf("stored entitlement %+v", got)
	}
	if q, _ := svc.Quota(ctx, f.OrgA, entitlement.LimitMaxCollectors); q == nil || *q != 7 {
		t.Errorf("quota max_collectors %v, want 7", q)
	}
	if q, _ := svc.Quota(ctx, f.OrgA, entitlement.LimitMaxCIs); q != nil {
		t.Errorf("quota max_cis %v, want unlimited without a stored limit (E-34)", *q)
	}

	for name, stmt := range map[string]string{
		"unknown source":    `INSERT INTO entitlement (organization_id, feature_key, source) VALUES ($1, 'webhooks', 'gift')`,
		"disabled core":     `INSERT INTO entitlement (organization_id, feature_key, enabled) VALUES ($1, 'cmdb_core', false)`,
		"expiring core":     `INSERT INTO entitlement (organization_id, feature_key, valid_until) VALUES ($1, 'cmdb_core', now())`,
		"limits not object": `INSERT INTO entitlement (organization_id, feature_key, limits) VALUES ($1, 'topology', '[1]')`,
	} {
		if _, err = f.Admin.Exec(bg, stmt, f.OrgA); err == nil {
			t.Errorf("%s accepted by the database", name)
		}
	}
}
