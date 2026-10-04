package form_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/form"
)

// TestFormRepositoryClientScope runs the form repository with the principal's
// tenant scope (TEN-06, WP-021): a principal restricted to client 1 neither
// sees a client-2 form nor submissions about client-2 CIs, and cannot submit
// a form about a client-2 CI.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestFormRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "3c")
	foreignCI := f.CI(t, f.OrgA, f.Client2, "form-c2")
	repo := form.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	shared := &form.Definition{OrganizationID: f.OrgA, Name: "shared", Schema: form.JSONMap{}, Active: true}
	foreignForm := &form.Definition{OrganizationID: f.OrgA, ClientID: f.Client2, Name: "c2", Schema: form.JSONMap{}, Active: true}
	for _, d := range []*form.Definition{shared, foreignForm} {
		if err := repo.CreateDefinition(orgCtx, d); err != nil {
			t.Fatalf("org-wide form %s: %v", d.Name, err)
		}
	}
	foreignSub := &form.Submission{OrganizationID: f.OrgA, FormID: shared.ID, Values: form.JSONMap{}, CIID: foreignCI, Status: "submitted"}
	if err := repo.CreateSubmission(orgCtx, foreignSub); err != nil {
		t.Fatalf("org-wide submission: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	page := api.PaginationParams{Limit: 100}
	defs, _, err := repo.ListDefinitions(ctx, f.OrgA, "", false, page)
	if err != nil || len(defs) != 1 || defs[0].ID != shared.ID {
		t.Fatalf("client-1 forms: %+v err=%v, want only the shared form", defs, err)
	}
	subs, total, err := repo.ListSubmissions(ctx, f.OrgA, form.SubmissionFilter{}, page)
	if err != nil || total != 0 || len(subs) != 0 {
		t.Fatalf("client-1 submissions: total=%d err=%v, want none", total, err)
	}
	if err = repo.CreateSubmission(ctx, &form.Submission{OrganizationID: f.OrgA, FormID: shared.ID, Values: form.JSONMap{}, CIID: foreignCI, Status: "submitted"}); err == nil {
		t.Fatal("client-1 principal submitted a form about a client-2 CI")
	}
	if err = repo.CreateSubmission(ctx, &form.Submission{OrganizationID: f.OrgA, FormID: shared.ID, Values: form.JSONMap{}, Status: "submitted"}); err != nil {
		t.Fatalf("client-1 submission without CI: %v", err)
	}
	if _, _, err = repo.ListSubmissions(context.Background(), f.OrgA, form.SubmissionFilter{}, page); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}
