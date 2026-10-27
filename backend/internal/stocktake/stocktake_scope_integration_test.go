package stocktake_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/stocktake"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// TestStocktakeRepositoryClientScope runs the stocktake repository with the
// principal's tenant scope (TEN-06, WP-018): in an organization-wide
// stocktake a principal restricted to client 1 neither sees scans or
// differences of client-2 assets nor scans them.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStocktakeRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "33")
	own := f.Asset(t, f.OrgA, f.Client1, "scopetest-33-own")
	foreign := f.Asset(t, f.OrgA, f.Client2, "scopetest-33-foreign")
	user := f.AppUser(t, f.OrgA, "counter")

	repo := stocktake.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	st := &stocktake.Stocktake{OrganizationID: f.OrgA, Title: "Inventur", Status: "in_progress", Scope: "full"}
	if err := repo.Create(orgCtx, st); err != nil {
		t.Fatalf("create stocktake: %v", err)
	}
	scan := func(assetID string) *stocktake.StockScan {
		return &stocktake.StockScan{OrganizationID: f.OrgA, StocktakeID: st.ID, AssetID: assetID, ScannedBy: user, ScanMethod: "manual", ScanResult: "missing"}
	}
	if err := repo.AddScan(orgCtx, scan(foreign)); err != nil {
		t.Fatalf("org-wide scan: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	if err := repo.AddScan(ctx, scan(foreign)); err == nil {
		t.Fatal("client-1 principal scanned a client-2 asset")
	}
	if err := repo.AddScan(ctx, scan(own)); err != nil {
		t.Fatalf("client-1 scan of own asset: %v", err)
	}
	page := api.PaginationParams{Limit: 100}
	scans, total, err := repo.ListScans(ctx, f.OrgA, st.ID, page)
	if err != nil || total != 1 || len(scans) != 1 || scans[0].AssetID != own {
		t.Fatalf("client-1 scans: total=%d %+v err=%v, want only the own one", total, scans, err)
	}
	diff, total, err := repo.Difference(ctx, f.OrgA, st.ID, page)
	if err != nil || total != 1 || len(diff) != 1 || diff[0].Scan.AssetID != own {
		t.Fatalf("client-1 difference: total=%d %+v err=%v, want only the own asset", total, diff, err)
	}
	var count int
	if err = f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM stock_scan WHERE asset_id = $1`, foreign).Scan(&count); err != nil || count != 1 {
		t.Fatalf("client-2 scans: count=%d err=%v, want 1", count, err)
	}
	if _, _, err = repo.ListScans(context.Background(), f.OrgA, st.ID, page); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}

// TestStocktakeForeignClientCorrectionAndComplete covers the completion of a
// stocktake with inventory corrections (TEN-06, WP-537; rewritten by WP-638,
// F-365, E-09). Stocktakes are organization-wide, their corrections write the
// scanned assets: a principal restricted to client 1 cannot complete a
// stocktake with differences of a client-1 and a client-2 asset
// (ErrNotFound); the stocktake stays open and no asset is corrected. The
// org-wide completion then corrects the client-1 and both client-2
// differences. A stocktake of another organization the client-1 principal can
// neither complete nor update (ErrNotFound), and it stays unchanged.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStocktakeForeignClientCorrectionAndComplete(t *testing.T) {
	f := scopetest.Seed(t, "33")
	bg := context.Background()
	own := f.Asset(t, f.OrgA, f.Client1, "scopetest-33-own")
	foreign := f.Asset(t, f.OrgA, f.Client2, "scopetest-33-foreign")
	moved := f.Asset(t, f.OrgA, f.Client2, "scopetest-33-moved")
	if _, err := f.Admin.Exec(bg, `UPDATE asset SET location = 'rack-a' WHERE id = ANY($1::uuid[])`, []string{own, foreign, moved}); err != nil {
		t.Fatalf("asset locations: %v", err)
	}
	user := f.AppUser(t, f.OrgA, "counter")

	repo := stocktake.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	st := &stocktake.Stocktake{OrganizationID: f.OrgA, Title: "Inventur", Status: "in_progress", Scope: "full"}
	if err := repo.Create(orgCtx, st); err != nil {
		t.Fatalf("create stocktake: %v", err)
	}
	for _, s := range []*stocktake.StockScan{
		{OrganizationID: f.OrgA, StocktakeID: st.ID, AssetID: own, ScannedBy: user, ScanMethod: "manual", ScanResult: "missing"},
		{OrganizationID: f.OrgA, StocktakeID: st.ID, AssetID: foreign, ScannedBy: user, ScanMethod: "manual", ScanResult: "missing"},
		{OrganizationID: f.OrgA, StocktakeID: st.ID, AssetID: moved, ScannedBy: user, ScanMethod: "manual", ScanResult: "wrong_location", LocationFound: "rack-z"},
	} {
		if err := repo.AddScan(orgCtx, s); err != nil {
			t.Fatalf("org-wide scan of %s: %v", s.AssetID, err)
		}
	}
	stB := &stocktake.Stocktake{OrganizationID: f.OrgB, Title: "Inventur B", Status: "in_progress", Scope: "full"}
	if err := repo.Create(f.OrgCtx(f.OrgB), stB); err != nil {
		t.Fatalf("create stocktake of organization B: %v", err)
	}
	type assetRow struct{ status, location string }
	asset := func(id string) assetRow {
		var r assetRow
		if err := f.Admin.QueryRow(bg, `SELECT status, COALESCE(location, '') FROM asset WHERE id = $1`, id).Scan(&r.status, &r.location); err != nil {
			t.Fatalf("stored asset %s: %v", id, err)
		}
		return r
	}
	type stocktakeRow struct {
		title, status string
		completed     bool
	}
	stored := func(id string) stocktakeRow {
		var r stocktakeRow
		if err := f.Admin.QueryRow(bg, `SELECT title, status, completed_at IS NOT NULL FROM stocktake WHERE id = $1`, id).Scan(&r.title, &r.status, &r.completed); err != nil {
			t.Fatalf("stored stocktake %s: %v", id, err)
		}
		return r
	}
	untouched := assetRow{status: "in_stock", location: "rack-a"}

	// The client-1 principal cannot complete the organization-wide
	// stocktake (E-09): no asset is corrected, the stocktake stays open.
	ctx := f.ClientCtx(f.Client1)
	wantOpen := stored(st.ID)
	if completion, err := repo.Complete(ctx, f.OrgA, st.ID, true); !errors.Is(err, stocktake.ErrNotFound) {
		t.Errorf("client-1 completion of the organization-wide stocktake: %+v err=%v, want ErrNotFound", completion, err)
	}
	for _, id := range []string{own, foreign, moved} {
		if got := asset(id); got != untouched {
			t.Errorf("asset %s after the refused client-1 completion: %+v, want %+v", id, got, untouched)
		}
	}
	if got := stored(st.ID); got != wantOpen || got.status != "in_progress" || got.completed {
		t.Errorf("stocktake after the refused client-1 completion: %+v, want %+v (in_progress)", got, wantOpen)
	}

	// The org-wide completion corrects the client-1 difference and both
	// client-2 differences.
	completion, err := repo.Complete(orgCtx, f.OrgA, st.ID, true)
	if err != nil {
		t.Fatalf("organization-wide completion of the stocktake: %v", err)
	}
	corrected := map[string]bool{}
	for _, c := range completion.Corrections {
		if !c.Applied {
			t.Errorf("organization-wide completion: correction of %s not applied: %+v", c.AssetID, c)
		}
		corrected[c.AssetID] = true
	}
	if completion.CorrectionsApplied != 3 || len(completion.Corrections) != 3 || !corrected[own] || !corrected[foreign] || !corrected[moved] {
		t.Errorf("organization-wide completion: %d applied %+v, want the three differences", completion.CorrectionsApplied, completion.Corrections)
	}
	if got := asset(own); got != (assetRow{status: "lost", location: "rack-a"}) {
		t.Errorf("client-1 asset after completion: %+v, want lost at rack-a", got)
	}
	if got := asset(foreign); got != (assetRow{status: "lost", location: "rack-a"}) {
		t.Errorf("client-2 asset after completion: %+v, want lost at rack-a", got)
	}
	if got := asset(moved); got != (assetRow{status: "in_stock", location: "rack-z"}) {
		t.Errorf("moved client-2 asset after completion: %+v, want in_stock at rack-z", got)
	}
	if got := stored(st.ID); got.status != "completed" || !got.completed {
		t.Errorf("stocktake after organization-wide completion: %+v, want completed", got)
	}

	// A stocktake of another organization is not found for completion or
	// update and stays unchanged.
	wantB := stored(stB.ID)
	if _, err := repo.Complete(ctx, f.OrgA, stB.ID, true); !errors.Is(err, stocktake.ErrNotFound) {
		t.Errorf("completion of an organization-B stocktake: err=%v, want ErrNotFound", err)
	}
	title, status := "hijacked", "cancelled"
	if _, err := repo.Update(ctx, f.OrgA, stB.ID, stocktake.UpdateRequest{Title: &title, Status: &status}); !errors.Is(err, stocktake.ErrNotFound) {
		t.Errorf("update of an organization-B stocktake: err=%v, want ErrNotFound", err)
	}
	if got := stored(stB.ID); got != wantB {
		t.Errorf("organization-B stocktake after foreign writes: %+v, want %+v", got, wantB)
	}
}

// TestAuditStocktakeOrgWideWritesNeedOrgWideScope: update, completion and
// delete of an organization-wide stocktake by a client-1 principal are
// refused and change nothing; afterwards an organization-wide principal
// completes it and corrects the client-2 asset (TEN-05, TEN-06, E-09; F-365,
// found by WP-537, closed by WP-638). A stocktake has no client column; it is
// a row of the whole organization (migration 000012). Before WP-638 the
// client-1 principal completed it, so the client-2 differences could no
// longer be corrected (ErrAlreadyCompleted), and its delete removed the
// client-2 scans with the stocktake (stock_scan ON DELETE CASCADE).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAuditStocktakeOrgWideWritesNeedOrgWideScope(t *testing.T) {
	f := scopetest.Seed(t, "33")
	bg := context.Background()
	own := f.Asset(t, f.OrgA, f.Client1, "scopetest-33-k5-own")
	foreign := f.Asset(t, f.OrgA, f.Client2, "scopetest-33-k5-foreign")
	user := f.AppUser(t, f.OrgA, "k5-counter")
	repo := stocktake.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)

	newStocktake := func(title string) string {
		t.Helper()
		st := &stocktake.Stocktake{OrganizationID: f.OrgA, Title: title, Status: "in_progress", Scope: "full"}
		if err := repo.Create(orgCtx, st); err != nil {
			t.Fatalf("create stocktake: %v", err)
		}
		for _, a := range []string{own, foreign} {
			if err := repo.AddScan(orgCtx, &stocktake.StockScan{OrganizationID: f.OrgA, StocktakeID: st.ID, AssetID: a,
				ScannedBy: user, ScanMethod: "manual", ScanResult: "missing"}); err != nil {
				t.Fatalf("org-wide scan of %s: %v", a, err)
			}
		}
		return st.ID
	}
	type row struct {
		title, status string
		scans         int
	}
	stored := func(id string) row {
		t.Helper()
		var r row
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE((SELECT title FROM stocktake WHERE id = $1), '<deleted>'),
			COALESCE((SELECT status FROM stocktake WHERE id = $1), ''), (SELECT count(*) FROM stock_scan WHERE stocktake_id = $1)`, id).
			Scan(&r.title, &r.status, &r.scans); err != nil {
			t.Fatalf("stored stocktake %s: %v", id, err)
		}
		return r
	}
	assetStatus := func(id string) string {
		t.Helper()
		var s string
		if err := f.Admin.QueryRow(bg, `SELECT status FROM asset WHERE id = $1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	client1 := f.ClientCtx(f.Client1)
	st := newStocktake("k5 inventory")
	want := stored(st)

	title := "renamed by client 1"
	if _, err := repo.Update(client1, f.OrgA, st, stocktake.UpdateRequest{Title: &title}); err == nil {
		t.Errorf("client-1 update of the organization-wide stocktake: accepted, want refused (E-09)")
	}
	if _, err := repo.Complete(client1, f.OrgA, st, true); err == nil {
		t.Errorf("client-1 completion of the organization-wide stocktake with client-2 differences: accepted, want refused (E-09)")
	}
	if got := stored(st); got != want {
		t.Errorf("organization-wide stocktake after the client-1 writes: %+v, want %+v", got, want)
	}
	// The organization-wide principal can still complete it and corrects
	// the client-2 difference.
	completion, err := repo.Complete(orgCtx, f.OrgA, st, true)
	if err != nil {
		t.Errorf("organization-wide completion after the client-1 writes: %v, want the completion with both corrections", err)
	} else if completion.CorrectionsApplied != 2 {
		t.Errorf("organization-wide completion: %d corrections applied, want 2", completion.CorrectionsApplied)
	}
	if got := assetStatus(foreign); got != "lost" {
		t.Errorf("client-2 asset after the organization-wide completion: %s, want lost", got)
	}

	doomed := newStocktake("k5 inventory delete")
	wantDoomed := stored(doomed)
	if err := repo.Delete(client1, f.OrgA, doomed); err == nil {
		t.Errorf("client-1 delete of the organization-wide stocktake: accepted, want refused (E-09)")
	}
	if got := stored(doomed); got != wantDoomed {
		t.Errorf("organization-wide stocktake after the client-1 delete: %+v, want %+v (client-2 scans included)", got, wantDoomed)
	}
}

// TestStocktakeHandlerOrgWideWritesForbidden runs the HTTP handler on the
// PostgreSQL repository (WP-638, F-365, E-09): PATCH, DELETE and POST
// …/complete of an organization-wide stocktake by a principal restricted to
// client 1 or to a site answer 403 problem+json and leave the stocktake, its
// scans and the assets unchanged; the client-1 principal still scans its own
// asset (201), and the org-wide principal updates, completes and deletes.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStocktakeHandlerOrgWideWritesForbidden(t *testing.T) {
	f := scopetest.Seed(t, "33")
	bg := context.Background()
	own := f.Asset(t, f.OrgA, f.Client1, "scopetest-33-h-own")
	foreign := f.Asset(t, f.OrgA, f.Client2, "scopetest-33-h-foreign")
	user := f.AppUser(t, f.OrgA, "h-counter")
	repo := stocktake.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	mux := chi.NewRouter()
	stocktake.NewHandler(repo).RegisterRoutes(mux)

	st := &stocktake.Stocktake{OrganizationID: f.OrgA, Title: "handler inventory", Status: "in_progress", Scope: "full"}
	if err := repo.Create(orgCtx, st); err != nil {
		t.Fatalf("create stocktake: %v", err)
	}
	if err := repo.AddScan(orgCtx, &stocktake.StockScan{OrganizationID: f.OrgA, StocktakeID: st.ID, AssetID: foreign,
		ScannedBy: user, ScanMethod: "manual", ScanResult: "missing"}); err != nil {
		t.Fatalf("org-wide scan of the client-2 asset: %v", err)
	}

	do := func(scopeCtx context.Context, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		ctx := tenant.WithTenant(scopeCtx, tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(ctx))
		return w
	}
	type row struct {
		title, status string
		scans         int
		foreign       string
	}
	stored := func() row {
		t.Helper()
		var r row
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE((SELECT title FROM stocktake WHERE id = $1), '<deleted>'),
			COALESCE((SELECT status FROM stocktake WHERE id = $1), ''), (SELECT count(*) FROM stock_scan WHERE stocktake_id = $1),
			(SELECT status FROM asset WHERE id = $2)`, st.ID, foreign).
			Scan(&r.title, &r.status, &r.scans, &r.foreign); err != nil {
			t.Fatalf("stored stocktake: %v", err)
		}
		return r
	}

	siteScope := database.OrgWideScope(f.OrgA, f.User)
	siteScope.Sites = database.ScopeIDs(f.ID())
	bound := map[string]context.Context{
		"client-1": f.ClientCtx(f.Client1),
		"site":     database.ContextWithTenantScope(bg, &siteScope),
	}
	path := "/api/v1/stocktakes/" + st.ID
	want := stored()
	for name, ctx := range bound {
		for _, c := range []struct{ method, path, body string }{
			{http.MethodPatch, path, `{"title":"renamed by a bound principal","status":"cancelled"}`},
			{http.MethodPost, path + "/complete", `{"apply_corrections":true}`},
			{http.MethodDelete, path, ""},
		} {
			w := do(ctx, c.method, c.path, c.body)
			if w.Code != http.StatusForbidden {
				t.Errorf("%s %s %s: %d %s, want 403", name, c.method, c.path, w.Code, w.Body.String())
				continue
			}
			if ct := w.Header().Get("Content-Type"); ct != api.ProblemContentType {
				t.Errorf("%s %s %s: Content-Type %q, want %s", name, c.method, c.path, ct, api.ProblemContentType)
			}
			var p api.ProblemDetail
			if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || p.Status != http.StatusForbidden {
				t.Errorf("%s %s %s: problem %+v err=%v, want status 403", name, c.method, c.path, p, err)
			}
		}
		if got := stored(); got != want {
			t.Errorf("stocktake after the %s writes: %+v, want %+v", name, got, want)
		}
	}

	// The client-1 principal still scans its own asset.
	if w := do(bound["client-1"], http.MethodPost, path+"/scans", `{"asset_id":"`+own+`","scan_result":"found"}`); w.Code != http.StatusCreated {
		t.Fatalf("client-1 scan of its own asset: %d %s, want 201", w.Code, w.Body.String())
	}
	want.scans++
	if got := stored(); got != want {
		t.Errorf("stocktake after the client-1 scan: %+v, want %+v", got, want)
	}

	// Positive control: the org-wide principal updates, completes and deletes.
	if w := do(orgCtx, http.MethodPatch, path, `{"title":"renamed org-wide"}`); w.Code != http.StatusOK {
		t.Fatalf("org-wide update: %d %s, want 200", w.Code, w.Body.String())
	}
	if w := do(orgCtx, http.MethodPost, path+"/complete", `{"apply_corrections":true}`); w.Code != http.StatusOK {
		t.Fatalf("org-wide completion: %d %s, want 200", w.Code, w.Body.String())
	}
	if got := stored(); got != (row{title: "renamed org-wide", status: "completed", scans: want.scans, foreign: "lost"}) {
		t.Errorf("stocktake after the org-wide completion: %+v, want renamed, completed, client-2 asset lost", got)
	}
	if w := do(orgCtx, http.MethodDelete, path, ""); w.Code != http.StatusNoContent {
		t.Fatalf("org-wide delete: %d %s, want 204", w.Code, w.Body.String())
	}
	if got := stored(); got.title != "<deleted>" || got.scans != 0 {
		t.Errorf("stocktake after the org-wide delete: %+v, want deleted with its scans", got)
	}
}
