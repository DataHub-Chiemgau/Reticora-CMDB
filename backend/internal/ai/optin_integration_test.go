package ai_test

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ai"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// TestAIOptInIsPerOrganization covers WP-076 (AI-02) against PostgreSQL:
// the opt-in defaults to off, is read in the organization's tenant
// transaction and is per organization.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAIOptInIsPerOrganization(t *testing.T) {
	f := scopetest.Seed(t, "67")
	repo := ai.NewPGRepository(f.App)
	if on, err := repo.AIOptIn(f.OrgCtx(f.OrgA), f.OrgA); err != nil || on {
		t.Fatalf("default opt-in: %v, %v; want off", on, err)
	}
	if _, err := f.Admin.Exec(context.Background(), `UPDATE organization SET ai_opt_in = true WHERE id = $1`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	if on, err := repo.AIOptIn(f.OrgCtx(f.OrgA), f.OrgA); err != nil || !on {
		t.Fatalf("opt-in of org A: %v, %v; want on", on, err)
	}
	if on, err := repo.AIOptIn(f.OrgCtx(f.OrgB), f.OrgB); err != nil || on {
		t.Fatalf("opt-in of org B: %v, %v; want off", on, err)
	}
	if _, err := repo.AIOptIn(f.OrgCtx(f.OrgB), f.OrgA); err == nil {
		t.Fatal("org B read the opt-in of org A")
	}
}
