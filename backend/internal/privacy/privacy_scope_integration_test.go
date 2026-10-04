package privacy_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/privacy"
)

// TestPrivacyRepositoryScope runs the retention policy repository with the
// principal's tenant scope (TEN-06, WP-011). The policy belongs to the
// organization: a principal of organization A reads and writes A's policy
// only; naming organization B fails, and a call without scope fails closed.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestPrivacyRepositoryScope(t *testing.T) {
	f := scopetest.Seed(t, "19")
	repo := privacy.NewPGRepository(f.App)
	if _, err := repo.UpsertPolicy(f.OrgCtx(f.OrgB), &privacy.RetentionPolicy{OrganizationID: f.OrgB, RetentionDays: 30, Mode: "delete"}); err != nil {
		t.Fatalf("org B upsert: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	stored, err := repo.UpsertPolicy(ctx, &privacy.RetentionPolicy{OrganizationID: f.OrgA, RetentionDays: 90, Mode: "anonymize"})
	if err != nil {
		t.Fatalf("org A upsert: %v", err)
	}
	if stored.OrganizationID != f.OrgA || stored.RetentionDays != 90 {
		t.Fatalf("org A policy: %+v", stored)
	}
	if _, err = repo.GetPolicy(ctx, f.OrgB); !errors.Is(err, database.ErrTenantMismatch) {
		t.Fatalf("read organization B policy: got %v, want ErrTenantMismatch", err)
	}
	if _, err = repo.UpsertPolicy(ctx, &privacy.RetentionPolicy{OrganizationID: f.OrgB, RetentionDays: 1, Mode: "delete"}); !errors.Is(err, database.ErrTenantMismatch) {
		t.Fatalf("write organization B policy: got %v, want ErrTenantMismatch", err)
	}
	var days int
	if err = f.Admin.QueryRow(context.Background(), `SELECT retention_days FROM privacy_retention_policy WHERE organization_id = $1`, f.OrgB).Scan(&days); err != nil {
		t.Fatalf("read organization B policy: %v", err)
	}
	if days != 30 {
		t.Fatalf("organization B policy changed to %d days", days)
	}
	if _, err = repo.GetPolicy(context.Background(), f.OrgA); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("read without scope: got %v, want ErrNoTenantScope", err)
	}
}
