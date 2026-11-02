package compliance_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/compliance"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// TestComplianceScope runs compliance with the principal's tenant scope
// (TEN-06, WP-020, WP-339): the evaluation replaces the results of the whole
// organization, so a principal restricted to client 1 is refused with
// ErrOrgWideScopeRequired and the stored results stay untouched; an org-wide
// principal evaluates both CIs, and the results a client-1 principal lists
// contain no client-2 CI.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestComplianceScope(t *testing.T) {
	f := scopetest.Seed(t, "3b")
	own := f.CI(t, f.OrgA, f.Client1, "compliance-c1")
	foreign := f.CI(t, f.OrgA, f.Client2, "compliance-c2")

	repo := compliance.NewPGRepository(f.App)
	evaluator := compliance.NewEvaluator(repo, ci.NewPGRepository(f.App))
	ctx := f.ClientCtx(f.Client1)
	orgWide := database.OrgWideScope(f.OrgA, "")
	orgCtx := database.ContextWithTenantScope(context.Background(), &orgWide)
	rule := &compliance.Rule{OrganizationID: f.OrgA, Name: "has name", Severity: "low", Category: "hygiene", Expression: compliance.JSONMap{}, Active: true}
	if err := repo.CreateRule(orgCtx, rule); err != nil {
		t.Fatalf("create rule: %v", err)
	}
	if _, err := evaluator.Evaluate(orgCtx, f.OrgA); err != nil {
		t.Fatalf("org-wide evaluate: %v", err)
	}
	stored := func() string {
		t.Helper()
		var v string
		if err := f.Admin.QueryRow(context.Background(),
			`SELECT count(*)::text || ':' || coalesce(string_agg(id::text || '@' || evaluated_at::text, ',' ORDER BY ci_id), '')
			   FROM compliance_result WHERE organization_id = $1 AND ci_id IN ($2, $3)`, f.OrgA, own, foreign).Scan(&v); err != nil {
			t.Fatalf("read results: %v", err)
		}
		return v
	}
	before := stored()
	if !strings.HasPrefix(before, "2:") {
		t.Fatalf("stored results after the org-wide evaluation: %s, want both CIs evaluated", before)
	}

	if _, err := evaluator.Evaluate(ctx, f.OrgA); !errors.Is(err, compliance.ErrOrgWideScopeRequired) {
		t.Fatalf("client-1 evaluate: got %v, want ErrOrgWideScopeRequired", err)
	}
	if after := stored(); after != before {
		t.Fatalf("results after the refused client-1 evaluation: %s, want unchanged %s", after, before)
	}
	results, total, err := repo.ListResults(ctx, f.OrgA, "", "", api.PaginationParams{Limit: 100})
	if err != nil || total != 1 || len(results) != 1 || results[0].CIID != own {
		t.Fatalf("client-1 results: total=%d %+v err=%v, want only the own CI", total, results, err)
	}
	if _, _, err = repo.ListResults(context.Background(), f.OrgA, "", "", api.PaginationParams{Limit: 10}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
