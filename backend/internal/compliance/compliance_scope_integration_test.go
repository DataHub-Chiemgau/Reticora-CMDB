package compliance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/compliance"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// TestComplianceScope runs compliance with the principal's tenant scope
// (TEN-06, WP-020): an evaluation triggered by a principal restricted to
// client 1 still covers the whole organization, while the results that
// principal lists contain no client-2 CI.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestComplianceScope(t *testing.T) {
	f := scopetest.Seed(t, "3b")
	own := f.CI(t, f.OrgA, f.Client1, "compliance-c1")
	foreign := f.CI(t, f.OrgA, f.Client2, "compliance-c2")

	repo := compliance.NewPGRepository(f.App)
	ctx := f.ClientCtx(f.Client1)
	rule := &compliance.Rule{OrganizationID: f.OrgA, Name: "has name", Severity: "low", Category: "hygiene", Expression: compliance.JSONMap{}, Active: true}
	if err := repo.CreateRule(ctx, rule); err != nil {
		t.Fatalf("create rule: %v", err)
	}
	if _, err := compliance.NewEvaluator(repo, ci.NewPGRepository(f.App)).Evaluate(ctx, f.OrgA); err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	var stored int
	if err := f.Admin.QueryRow(context.Background(),
		`SELECT count(*) FROM compliance_result WHERE organization_id = $1 AND ci_id IN ($2, $3)`, f.OrgA, own, foreign).Scan(&stored); err != nil {
		t.Fatalf("count results: %v", err)
	}
	if stored != 2 {
		t.Fatalf("stored results: %d, want both CIs evaluated", stored)
	}
	results, total, err := repo.ListResults(ctx, f.OrgA, "", "", api.PaginationParams{Limit: 100})
	if err != nil || total != 1 || len(results) != 1 || results[0].CIID != own {
		t.Fatalf("client-1 results: total=%d %+v err=%v, want only the own CI", total, results, err)
	}
	if _, _, err = repo.ListResults(context.Background(), f.OrgA, "", "", api.PaginationParams{Limit: 10}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
