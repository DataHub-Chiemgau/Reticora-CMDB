package form_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

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

// TestSubmissionScopeFromFormClient covers WP-1093 AK3/AK4 (WFL-01): a
// submission without CI to a form of client 2 carries client 2 and is
// invisible to a principal of client 1 by row level security; a submission
// about a CI carries the client of the CI.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSubmissionScopeFromFormClient(t *testing.T) {
	f := scopetest.Seed(t, "3c")
	bg := context.Background()
	repo := form.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	foreignForm := &form.Definition{OrganizationID: f.OrgA, ClientID: f.Client2, Name: "c2-scope", Schema: form.JSONMap{}, Active: true}
	shared := &form.Definition{OrganizationID: f.OrgA, Name: "shared-scope", Schema: form.JSONMap{}, Active: true}
	for _, d := range []*form.Definition{foreignForm, shared} {
		if err := repo.CreateDefinition(orgCtx, d); err != nil {
			t.Fatal(err)
		}
	}
	foreign := &form.Submission{OrganizationID: f.OrgA, FormID: foreignForm.ID, Values: form.JSONMap{}, Status: "submitted"}
	if err := repo.CreateSubmission(orgCtx, foreign); err != nil {
		t.Fatal(err)
	}
	ci1 := f.CI(t, f.OrgA, f.Client1, "form-scope-c1")
	aboutCI := &form.Submission{OrganizationID: f.OrgA, FormID: shared.ID, Values: form.JSONMap{}, CIID: ci1, Status: "submitted"}
	if err := repo.CreateSubmission(orgCtx, aboutCI); err != nil {
		t.Fatal(err)
	}
	client := func(id string) string {
		t.Helper()
		var c string
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(client_id::text, '') FROM form_submission WHERE id = $1`, id).Scan(&c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	if got := client(foreign.ID); got != f.Client2 {
		t.Errorf("submission to the client-2 form: client %q, want client 2", got)
	}
	if got := client(aboutCI.ID); got != f.Client1 {
		t.Errorf("submission about the client-1 CI: client %q, want client 1", got)
	}
	ctx := f.ClientCtx(f.Client1)
	var visible int
	if err := database.WithRequestTenant(ctx, f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM form_submission WHERE id = $1`, foreign.ID).Scan(&visible)
	}); err != nil {
		t.Fatal(err)
	}
	if visible != 0 {
		t.Error("the submission to the client-2 form is visible to client 1 in row level security")
	}
	if _, err := repo.GetSubmission(ctx, f.OrgA, foreign.ID); err == nil {
		t.Error("client 1 reads the submission to the client-2 form")
	}
	if got, err := repo.GetSubmission(ctx, f.OrgA, aboutCI.ID); err != nil || got.ID != aboutCI.ID {
		t.Errorf("client 1 reads the submission about its CI: %+v %v", got, err)
	}
}

// TestSubmissionKeepsScopeWhenItsCIIsDeleted: a submission to an org-wide
// form about a CI of client 2 keeps client 2 when the CI is deleted for good
// (ON DELETE SET NULL on ci_id): it never falls back to NULL, which every
// client-1 principal could read (WFL-01, WP-1093). A later change of the
// form does not widen it either.
func TestSubmissionKeepsScopeWhenItsCIIsDeleted(t *testing.T) {
	f := scopetest.Seed(t, "3c")
	bg := context.Background()
	repo := form.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	shared := &form.Definition{OrganizationID: f.OrgA, Name: "shared-ci-delete", Schema: form.JSONMap{}, Active: true}
	if err := repo.CreateDefinition(orgCtx, shared); err != nil {
		t.Fatal(err)
	}
	ci2 := f.CI(t, f.OrgA, f.Client2, "form-scope-delete-c2")
	sub := &form.Submission{OrganizationID: f.OrgA, FormID: shared.ID, Values: form.JSONMap{"secret": "c2"}, CIID: ci2, Status: "submitted"}
	if err := repo.CreateSubmission(orgCtx, sub); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `DELETE FROM ci WHERE id = $1`, ci2); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `UPDATE form_submission SET form_id = form_id WHERE id = $1`, sub.ID); err != nil {
		t.Fatal(err)
	}
	var ciID, client string
	if err := f.Admin.QueryRow(bg, `SELECT COALESCE(ci_id::text, ''), COALESCE(client_id::text, '') FROM form_submission WHERE id = $1`, sub.ID).
		Scan(&ciID, &client); err != nil {
		t.Fatal(err)
	}
	if ciID != "" || client != f.Client2 {
		t.Errorf("submission after its CI was deleted: ci %q client %q, want no CI and client 2", ciID, client)
	}
	if _, err := repo.GetSubmission(f.ClientCtx(f.Client1), f.OrgA, sub.ID); err == nil {
		t.Error("client 1 reads the submission about the deleted client-2 CI")
	}
}
