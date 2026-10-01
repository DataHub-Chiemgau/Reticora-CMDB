package override_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
)

// TestOverrideRepositoryClientScope runs the override repository with the
// principal's tenant scope (TEN-06, WP-011): a principal restricted to client
// 1 neither sees nor changes the field values, overrides and conflicts of a
// client-2 CI.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestOverrideRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "17")
	own := f.CI(t, f.OrgA, f.Client1, "override-c1")
	foreign := f.CI(t, f.OrgA, f.Client2, "override-c2")

	repo := override.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	for _, id := range []string{own, foreign} {
		if _, err := repo.RecordDiscovered(orgCtx, f.OrgA, id, "hostname", "discovered", "snmp"); err != nil {
			t.Fatalf("org-wide discovered value on %s: %v", id, err)
		}
		if _, err := repo.SetOverride(orgCtx, f.OrgA, id, "hostname", "manual", f.User, "fixture", true); err != nil {
			t.Fatalf("org-wide override on %s: %v", id, err)
		}
	}

	ctx := f.ClientCtx(f.Client1)
	// Listing resolves the effective value of every row in the same
	// transaction; it used to fail with "conn busy" as soon as a row existed.
	values, err := repo.ListForCI(ctx, f.OrgA, own)
	if err != nil || len(values) != 1 || values[0].EffectiveValue != "manual" {
		t.Fatalf("client-1 field values of own CI: %+v err=%v, want one value with effective \"manual\"", values, err)
	}
	values, err = repo.ListForCI(ctx, f.OrgA, foreign)
	if err != nil || len(values) != 0 {
		t.Fatalf("client-1 field values of client-2 CI: len=%d err=%v, want 0/nil", len(values), err)
	}
	if _, err = repo.Get(ctx, f.OrgA, foreign, "hostname"); err == nil {
		t.Fatal("client-1 principal read a field value of a client-2 CI")
	}
	if _, err = repo.SetOverride(ctx, f.OrgA, foreign, "hostname", "hijacked", f.User, "test", true); err == nil {
		t.Fatal("client-1 principal set an override on a client-2 CI")
	}
	if _, err = repo.RecordDiscovered(ctx, f.OrgA, foreign, "hostname", "hijacked", "snmp"); err == nil {
		t.Fatal("client-1 principal recorded a discovered value on a client-2 CI")
	}
	if _, err = repo.ClearOverride(ctx, f.OrgA, foreign, "hostname"); err == nil {
		t.Fatal("client-1 principal cleared an override of a client-2 CI")
	}
	conflicts, total, err := repo.Conflicts(ctx, f.OrgA, api.PaginationParams{Limit: 100})
	if err != nil {
		t.Fatalf("conflicts: %v", err)
	}
	if total != 1 || len(conflicts) != 1 || conflicts[0].CIID != own {
		t.Fatalf("client-1 conflicts: total=%d %+v, want only the client-1 CI", total, conflicts)
	}

	var discovered, overridden string
	if err = f.Admin.QueryRow(context.Background(),
		`SELECT discovered_value #>> '{}', override_value #>> '{}' FROM ci_field_value WHERE ci_id = $1 AND field_name = 'hostname'`,
		foreign).Scan(&discovered, &overridden); err != nil {
		t.Fatalf("read foreign field value: %v", err)
	}
	if discovered != "discovered" || overridden != "manual" {
		t.Fatalf("client-2 field value changed: discovered=%q override=%q", discovered, overridden)
	}

	if _, err = repo.ListForCI(context.Background(), f.OrgA, own); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
