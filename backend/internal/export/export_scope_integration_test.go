package export_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
)

// TestExportScope runs the export request paths with the principal's tenant
// scope (TEN-06, WP-015): a synchronous export by a principal restricted to
// client 1 contains no client-2 CI, and export jobs of another organization
// are neither listed nor readable.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestExportScope(t *testing.T) {
	f := scopetest.Seed(t, "27")
	f.CI(t, f.OrgA, f.Client1, "export-own-ci")
	f.CI(t, f.OrgA, f.Client2, "export-foreign-ci")

	ctx := f.ClientCtx(f.Client1)
	var buf bytes.Buffer
	rows, err := export.RenderFormat(ctx, ci.NewPGRepository(f.App), f.OrgA, "json", export.JobFilters{}, &buf)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if rows != 1 || !strings.Contains(out, "export-own-ci") || strings.Contains(out, "export-foreign-ci") {
		t.Fatalf("client-1 export: %d rows %s, want only the client-1 CI", rows, out)
	}

	jobs := export.NewPGJobRepository(f.App)
	jobB := &export.Job{Format: "csv", InitiatedBy: f.User}
	if err = jobs.CreateJob(f.OrgCtx(f.OrgB), f.OrgB, jobB); err != nil {
		t.Fatalf("create organization B job: %v", err)
	}
	jobA := &export.Job{Format: "csv", InitiatedBy: f.User}
	if err = jobs.CreateJob(ctx, f.OrgA, jobA); err != nil {
		t.Fatalf("create organization A job: %v", err)
	}
	list, total, err := jobs.ListJobs(ctx, f.OrgA, 100, 0)
	if err != nil || total != 1 || len(list) != 1 || list[0].ID != jobA.ID {
		t.Fatalf("organization A jobs: total=%d %+v err=%v, want only the A job", total, list, err)
	}
	if _, err = jobs.GetJob(ctx, f.OrgB, jobB.ID); !errors.Is(err, database.ErrTenantMismatch) {
		t.Fatalf("read organization B job: got %v, want ErrTenantMismatch", err)
	}
	if _, _, err = jobs.ListJobs(context.Background(), f.OrgA, 10, 0); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
