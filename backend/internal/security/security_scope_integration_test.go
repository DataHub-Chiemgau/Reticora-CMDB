package security_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/security"
)

// TestSecurityRepositoryClientScope runs the security finding repository with
// the principal's tenant scope (TEN-06, WP-012): a principal restricted to
// client 1 neither sees, counts, creates nor updates findings about a
// client-2 CI.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSecurityRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "1d")
	ownCI := f.CI(t, f.OrgA, f.Client1, "security-c1")
	foreignCI := f.CI(t, f.OrgA, f.Client2, "security-c2")

	repo := security.NewPGRepository(f.App)
	own := &security.Finding{OrganizationID: f.OrgA, CIID: ownCI, Kind: "vulnerability", Severity: "high", Title: "own"}
	foreign := &security.Finding{OrganizationID: f.OrgA, CIID: foreignCI, Kind: "vulnerability", Severity: "critical", Title: "foreign"}
	for _, fd := range []*security.Finding{own, foreign} {
		if err := repo.Create(f.OrgCtx(f.OrgA), fd); err != nil {
			t.Fatalf("org-wide create %s: %v", fd.Title, err)
		}
	}

	ctx := f.ClientCtx(f.Client1)
	list, total, err := repo.List(ctx, f.OrgA, security.FilterParams{}, api.PaginationParams{Limit: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 || len(list) != 1 || list[0].ID != own.ID {
		t.Fatalf("client-1 list: total=%d %+v, want only the client-1 finding", total, list)
	}
	summary, err := repo.Summary(ctx, f.OrgA)
	if err != nil || summary["critical"] != 0 || summary["high"] != 1 {
		t.Fatalf("client-1 summary: %v err=%v, want high=1 critical=0", summary, err)
	}
	if _, err = repo.GetByID(ctx, f.OrgA, foreign.ID); err == nil {
		t.Fatal("client-1 principal read a finding about a client-2 CI")
	}
	resolved := "resolved"
	if _, err = repo.Update(ctx, f.OrgA, foreign.ID, security.UpdateFindingRequest{Status: &resolved}); err == nil {
		t.Fatal("client-1 principal updated a finding about a client-2 CI")
	}
	if err = repo.Create(ctx, &security.Finding{OrganizationID: f.OrgA, CIID: foreignCI, Kind: "vulnerability", Title: "intruder"}); err == nil {
		t.Fatal("client-1 principal created a finding about a client-2 CI")
	}

	var status string
	if err = f.Admin.QueryRow(context.Background(), `SELECT status FROM security_finding WHERE id = $1`, foreign.ID).Scan(&status); err != nil {
		t.Fatalf("read foreign finding: %v", err)
	}
	if status != "open" {
		t.Fatalf("client-2 finding changed to %q", status)
	}
	if _, err = repo.Summary(context.Background(), f.OrgA); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("summary without scope: got %v, want ErrNoTenantScope", err)
	}
}
