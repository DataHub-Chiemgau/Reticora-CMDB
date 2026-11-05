package stocktake_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
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
	title := "hijacked"
	if _, err := repo.Update(ctx, f.OrgA, stB.ID, stocktake.UpdateRequest{Title: &title}); !errors.Is(err, stocktake.ErrNotFound) {
		t.Errorf("update of an organization-B stocktake: err=%v, want ErrNotFound", err)
	}
	if _, err := repo.Cancel(ctx, f.OrgA, stB.ID); !errors.Is(err, stocktake.ErrNotFound) {
		t.Errorf("cancel of an organization-B stocktake: err=%v, want ErrNotFound", err)
	}
	if got := stored(stB.ID); got != wantB {
		t.Errorf("organization-B stocktake after foreign writes: %+v, want %+v", got, wantB)
	}
}

// TestAuditStocktakeOrgWideWritesNeedOrgWideScope: update, completion, start,
// cancel (WP-848) and delete of an organization-wide stocktake by a client-1
// principal are
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
	// Start and cancel are writes of the stocktake as well (WP-848).
	if _, err := repo.Start(client1, f.OrgA, st, user); err == nil {
		t.Errorf("client-1 start of the organization-wide stocktake: accepted, want refused (E-09)")
	}
	if _, err := repo.Cancel(client1, f.OrgA, st); err == nil {
		t.Errorf("client-1 cancel of the organization-wide stocktake: accepted, want refused (E-09)")
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
// …/complete, …/start and …/cancel (WP-848) of an organization-wide
// stocktake (without area) by a principal restricted to
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
			{http.MethodPatch, path, `{"title":"renamed by a bound principal"}`},
			{http.MethodPost, path + "/complete", `{"apply_corrections":true}`},
			{http.MethodPost, path + "/start", ""},
			{http.MethodPost, path + "/cancel", ""},
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

// areaTree is a location tree of client clientID in organization A for the
// area tests (WP-848): a room with a rack, a warehouse with zone, shelf and
// the bins bin1 and bin2 on the site of the room, and a second room outside
// both.
type areaTree struct {
	site, room, rack, zone, bin1, bin2, otherRoom string
}

// seedAreaTree creates the tree through the maintenance pool.
func seedAreaTree(t *testing.T, f *scopetest.Fixture, clientID, name string) areaTree {
	t.Helper()
	ctx := context.Background()
	var tr areaTree
	tr.room = f.Room(t, clientID, name+"-room")
	tr.otherRoom = f.Room(t, clientID, name+"-other")
	if err := f.Admin.QueryRow(ctx, `INSERT INTO rack (organization_id, room_id, name) VALUES ($1, $2, $3) RETURNING id::text`,
		f.OrgA, tr.room, name+"-rack").Scan(&tr.rack); err != nil {
		t.Fatalf("seed rack %s: %v", name, err)
	}
	if err := f.Admin.QueryRow(ctx, `SELECT site_id::text FROM location WHERE id = $1`, tr.room).Scan(&tr.site); err != nil {
		t.Fatalf("site of room %s: %v", name, err)
	}
	node := func(kind, parent, suffix string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(ctx, `INSERT INTO location (organization_id, kind, parent_id, name) VALUES ($1, $2, $3, $4) RETURNING id::text`,
			f.OrgA, kind, parent, name+"-"+suffix).Scan(&id); err != nil {
			t.Fatalf("seed %s %s: %v", kind, name, err)
		}
		return id
	}
	warehouse := node("warehouse", tr.site, "warehouse")
	tr.zone = node("zone", warehouse, "zone")
	shelf := node("shelf", tr.zone, "shelf")
	tr.bin1 = node("bin", shelf, "bin1")
	tr.bin2 = node("bin", shelf, "bin2")
	return tr
}

// placeAsset sets the location of the asset id through the maintenance pool.
func placeAsset(t *testing.T, f *scopetest.Fixture, id, locationID string) {
	t.Helper()
	if _, err := f.Admin.Exec(context.Background(), `UPDATE asset SET location_id = $2 WHERE id = $1`, id, locationID); err != nil {
		t.Fatalf("place asset %s: %v", id, err)
	}
}

// stockConsumable creates a consumable of clientID with on_hand in each of
// the given bins through the maintenance pool and returns its id.
func stockConsumable(t *testing.T, f *scopetest.Fixture, clientID, name string, onHand map[string]float64) string {
	t.Helper()
	ctx := context.Background()
	id := f.ID()
	if _, err := f.Admin.Exec(ctx, `INSERT INTO consumable (id, organization_id, client_id, name) VALUES ($1, $2, $3, $4)`,
		id, f.OrgA, clientID, name); err != nil {
		t.Fatalf("seed consumable %s: %v", name, err)
	}
	for bin, qty := range onHand {
		if _, err := f.Admin.Exec(ctx, `INSERT INTO stock_level (organization_id, consumable_id, location_id, on_hand) VALUES ($1, $2, $3, $4)`,
			f.OrgA, id, bin, qty); err != nil {
			t.Fatalf("seed stock of %s in %s: %v", name, bin, err)
		}
	}
	return id
}

// stocktakeAPI serves the stocktake and location routes on the PostgreSQL
// repositories for the area tests.
type stocktakeAPI struct {
	t    *testing.T
	mux  chi.Router
	org  string
	user string
}

func newStocktakeAPI(t *testing.T, f *scopetest.Fixture, user string) *stocktakeAPI {
	mux := chi.NewRouter()
	stocktake.NewHandler(stocktake.NewPGRepository(f.App)).RegisterRoutes(mux)
	locations.NewHandler(locations.NewPGRepository(f.App)).RegisterRoutes(mux)
	return &stocktakeAPI{t: t, mux: mux, org: f.OrgA, user: user}
}

// do runs the request with the tenant scope of ctx and decodes the body.
func (a *stocktakeAPI) do(ctx context.Context, method, path, body string) (int, map[string]any) {
	a.t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	a.mux.ServeHTTP(rec, req.WithContext(tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: a.org, UserID: a.user})))
	out := map[string]any{}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			a.t.Fatalf("%s %s: body %q: %v", method, path, rec.Body.String(), err)
		}
	}
	return rec.Code, out
}

// create creates a stocktake on the area locationID (empty: none) and
// returns its id; it fails the test unless the answer is 201.
func (a *stocktakeAPI) create(ctx context.Context, title, locationID string) map[string]any {
	a.t.Helper()
	body := `{"title":"` + title + `"}`
	if locationID != "" {
		body = `{"title":"` + title + `","location_id":"` + locationID + `"}`
	}
	code, st := a.do(ctx, http.MethodPost, "/api/v1/stocktakes", body)
	if code != http.StatusCreated {
		a.t.Fatalf("create stocktake %s: %d %v, want 201", title, code, st)
	}
	return st
}

// items reads every item of the stocktake id page by page with limit.
func (a *stocktakeAPI) items(ctx context.Context, id string, limit int) []map[string]any {
	a.t.Helper()
	var all []map[string]any
	cursor := ""
	for pages := 0; pages < 50; pages++ {
		path := "/api/v1/stocktakes/" + id + "/items?limit=" + strconv.Itoa(limit)
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		code, page := a.do(ctx, http.MethodGet, path, "")
		if code != http.StatusOK {
			a.t.Fatalf("items of %s: %d %v, want 200", id, code, page)
		}
		data, _ := page["data"].([]any)
		if len(data) > limit {
			a.t.Fatalf("items of %s: %d rows on a page of %d", id, len(data), limit)
		}
		for _, d := range data {
			all = append(all, d.(map[string]any))
		}
		next, _ := page["next_cursor"].(string)
		if next == "" {
			return all
		}
		cursor = next
	}
	a.t.Fatalf("items of %s: no last page", id)
	return nil
}

// itemKeys renders items as kind:object@location=quantity, sorted.
func itemKeys(items []map[string]any) []string {
	keys := make([]string, 0, len(items))
	for _, it := range items {
		object, _ := it["asset_id"].(string)
		if object == "" {
			object, _ = it["consumable_id"].(string)
		}
		keys = append(keys, fmt.Sprintf("%v:%s@%v=%v", it["item_kind"], object, it["location_id"], it["expected_quantity"]))
	}
	slices.Sort(keys)
	return keys
}

// fieldOf returns the field of a problem body.
func fieldOf(body map[string]any) string {
	field, _ := body["field"].(string)
	return field
}

// TestStocktakeAreaSnapshot covers the area and the frozen expected stock
// (INV-01, API-06, WP-848): POST …/start of a stocktake on a room, a zone
// and a bin freezes one item per asset in the subtree (the rack of the room
// included) and one per stock_level row of a bin in the subtree; an asset
// with a free-text location only and one in another room are no part of
// it. client_id and site_id follow the node; started_by, started_at and
// total_expected are set. A later movement of the asset and a changed
// stock of the bin leave the snapshot as it was. GET …/items pages by
// cursor and refuses a forged one.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStocktakeAreaSnapshot(t *testing.T) {
	f := scopetest.Seed(t, "33")
	bg := context.Background()
	orgCtx := f.OrgCtx(f.OrgA)
	user := f.AppUser(t, f.OrgA, "area-counter")
	a := newStocktakeAPI(t, f, user)
	tr := seedAreaTree(t, f, f.Client1, "s848")

	inRoom := f.Asset(t, f.OrgA, f.Client1, "s848-room")
	inRack := f.Asset(t, f.OrgA, f.Client1, "s848-rack")
	inBin := f.Asset(t, f.OrgA, f.Client1, "s848-bin")
	elsewhere := f.Asset(t, f.OrgA, f.Client1, "s848-elsewhere")
	freeText := f.Asset(t, f.OrgA, f.Client1, "s848-free")
	placeAsset(t, f, inRoom, tr.room)
	placeAsset(t, f, inRack, tr.rack)
	placeAsset(t, f, inBin, tr.bin1)
	placeAsset(t, f, elsewhere, tr.otherRoom)
	if _, err := f.Admin.Exec(bg, `UPDATE asset SET location = 'Lager Nord' WHERE id = $1`, freeText); err != nil {
		t.Fatal(err)
	}
	cable := stockConsumable(t, f, f.Client1, "s848-cable", map[string]float64{tr.bin1: 5, tr.bin2: 3})

	asset := func(id, loc string) string { return "asset:" + id + "@" + loc + "=1" }
	stock := func(loc string, qty int) string { return fmt.Sprintf("consumable:%s@%s=%d", cable, loc, qty) }
	cases := []struct {
		name, area string
		want       []string
	}{
		{"room with rack", tr.room, []string{asset(inRoom, tr.room), asset(inRack, tr.rack)}},
		{"zone", tr.zone, []string{asset(inBin, tr.bin1), stock(tr.bin1, 5), stock(tr.bin2, 3)}},
		{"bin", tr.bin1, []string{asset(inBin, tr.bin1), stock(tr.bin1, 5)}},
	}
	started := map[string]string{}
	for _, c := range cases {
		st := a.create(orgCtx, "area "+c.name, c.area)
		id, _ := st["id"].(string)
		if st["status"] != stocktake.StatusPlanned || st["location_id"] != c.area || st["client_id"] != f.Client1 || st["site_id"] != tr.site {
			t.Errorf("%s: created %v, want planned on the area with client 1 and its site", c.name, st)
		}
		if items := a.items(orgCtx, id, 200); len(items) != 0 {
			t.Errorf("%s: items before the start %v, want none", c.name, items)
		}
		code, body := a.do(orgCtx, http.MethodPost, "/api/v1/stocktakes/"+id+"/start", "")
		if code != http.StatusOK {
			t.Fatalf("%s: start %d %v, want 200", c.name, code, body)
		}
		if body["status"] != stocktake.StatusInProgress || body["started_by"] != user || body["started_at"] == nil ||
			body["total_expected"] != float64(len(c.want)) {
			t.Errorf("%s: started %v, want in_progress by %s with total_expected %d", c.name, body, user, len(c.want))
		}
		want := slices.Clone(c.want)
		slices.Sort(want)
		if got := itemKeys(a.items(orgCtx, id, 200)); !slices.Equal(got, want) {
			t.Errorf("%s: items %v, want %v", c.name, got, want)
		}
		started[c.name] = id
	}

	// A later movement and a changed stock leave the snapshot as it was.
	svc := movement.NewService(movement.NewPGRepository(f.App))
	if err := svc.Record(orgCtx, &movement.Movement{OrganizationID: f.OrgA, AssetID: inBin, MovementType: "transfer", ToLocationID: tr.otherRoom}); err != nil {
		t.Fatalf("transfer of the asset out of the bin: %v", err)
	}
	var moved string
	if err := f.Admin.QueryRow(bg, `SELECT location_id::text FROM asset WHERE id = $1`, inBin).Scan(&moved); err != nil || moved != tr.otherRoom {
		t.Fatalf("asset after the transfer: %s %v, want %s", moved, err, tr.otherRoom)
	}
	if _, err := f.Admin.Exec(bg, `UPDATE stock_level SET on_hand = 1 WHERE consumable_id = $1 AND location_id = $2`, cable, tr.bin1); err != nil {
		t.Fatal(err)
	}
	wantBin := []string{asset(inBin, tr.bin1), stock(tr.bin1, 5)}
	slices.Sort(wantBin)
	if got := itemKeys(a.items(orgCtx, started["bin"], 200)); !slices.Equal(got, wantBin) {
		t.Errorf("bin snapshot after the movement: %v, want %v", got, wantBin)
	}

	// Cursor pagination: two pages of the zone snapshot, ascending ids.
	paged := a.items(orgCtx, started["zone"], 2)
	ids := make([]string, 0, len(paged))
	for _, it := range paged {
		ids = append(ids, it["id"].(string))
	}
	if len(ids) != 3 || !slices.IsSorted(ids) || len(slices.Compact(slices.Clone(ids))) != 3 {
		t.Errorf("zone items by pages of 2: %v, want 3 distinct ids in ascending order", ids)
	}
	code, first := a.do(orgCtx, http.MethodGet, "/api/v1/stocktakes/"+started["zone"]+"/items?limit=2", "")
	if next, _ := first["next_cursor"].(string); code != http.StatusOK || next == "" || first["has_more"] != true {
		t.Errorf("first page of the zone items: %d %v, want next_cursor and has_more", code, first)
	}
	if code, body := a.do(orgCtx, http.MethodGet, "/api/v1/stocktakes/"+started["zone"]+"/items?cursor=forged.cursor", ""); code != http.StatusBadRequest {
		t.Errorf("items with a forged cursor: %d %v, want 400", code, body)
	}
}

// TestStocktakeAreaRulesAndTransitions covers the input rules and the
// status transitions of WP-848: a node that is unknown, of another
// organization or outside the scope is 422 on location_id, so is a
// location_id that is no UUID; status and total_expected are not writable
// (400); a stocktake without area starts with 422 on location_id; start
// needs planned, cancel planned or in_progress, a new area planned, every
// other transition is 409. A new area moves client_id and site_id along.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStocktakeAreaRulesAndTransitions(t *testing.T) {
	f := scopetest.Seed(t, "33")
	bg := context.Background()
	orgCtx := f.OrgCtx(f.OrgA)
	user := f.AppUser(t, f.OrgA, "rules-counter")
	a := newStocktakeAPI(t, f, user)
	own := seedAreaTree(t, f, f.Client1, "r848-c1")
	foreign := seedAreaTree(t, f, f.Client2, "r848-c2")
	clientB := f.ID()
	var nodeB string
	if _, err := f.Admin.Exec(bg, `INSERT INTO client (id, organization_id, name, slug) VALUES ($1, $2, 'Client B', 'scopetest-33-r848-b')`, clientB, f.OrgB); err != nil {
		t.Fatal(err)
	}
	if err := f.Admin.QueryRow(bg, `INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, 'r848-b') RETURNING id::text`, f.OrgB, clientB).Scan(&nodeB); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name string
		ctx  context.Context
		body string
	}{
		{"unknown node", orgCtx, `{"title":"x","location_id":"` + f.ID() + `"}`},
		{"node of organization B", orgCtx, `{"title":"x","location_id":"` + nodeB + `"}`},
		{"client-2 node for a client-1 principal", f.ClientCtx(f.Client1), `{"title":"x","location_id":"` + foreign.room + `"}`},
		{"no UUID", orgCtx, `{"title":"x","location_id":"room-1"}`},
	} {
		if code, body := a.do(c.ctx, http.MethodPost, "/api/v1/stocktakes", c.body); code != http.StatusUnprocessableEntity || fieldOf(body) != "location_id" {
			t.Errorf("create with %s: %d %v, want 422 on location_id", c.name, code, body)
		}
	}
	for _, body := range []string{`{"title":"x","status":"in_progress"}`, `{"title":"x","total_expected":3}`} {
		if code, problem := a.do(orgCtx, http.MethodPost, "/api/v1/stocktakes", body); code != http.StatusBadRequest {
			t.Errorf("create with %s: %d %v, want 400", body, code, problem)
		}
	}

	legacy := a.create(orgCtx, "without area", "")
	legacyPath := "/api/v1/stocktakes/" + legacy["id"].(string)
	if code, body := a.do(orgCtx, http.MethodPost, legacyPath+"/start", ""); code != http.StatusUnprocessableEntity || fieldOf(body) != "location_id" {
		t.Errorf("start without area: %d %v, want 422 on location_id", code, body)
	}
	for _, body := range []string{`{"status":"completed"}`, `{"total_expected":7}`, `{"total_scanned":1}`} {
		if code, problem := a.do(orgCtx, http.MethodPatch, legacyPath, body); code != http.StatusBadRequest {
			t.Errorf("PATCH %s: %d %v, want 400", body, code, problem)
		}
	}
	if code, body := a.do(orgCtx, http.MethodPatch, legacyPath, `{"location_id":"`+f.ID()+`"}`); code != http.StatusUnprocessableEntity || fieldOf(body) != "location_id" {
		t.Errorf("PATCH with an unknown node: %d %v, want 422 on location_id", code, body)
	}
	// A planned stocktake takes an area, client and site follow the node.
	code, body := a.do(orgCtx, http.MethodPatch, legacyPath, `{"location_id":"`+foreign.zone+`"}`)
	if code != http.StatusOK || body["location_id"] != foreign.zone || body["client_id"] != f.Client2 || body["site_id"] != foreign.site {
		t.Errorf("PATCH the area of a planned stocktake: %d %v, want client 2 and its site", code, body)
	}
	code, body = a.do(orgCtx, http.MethodPatch, legacyPath, `{"location_id":"`+own.room+`","title":"moved"}`)
	if code != http.StatusOK || body["client_id"] != f.Client1 || body["site_id"] != own.site || body["title"] != "moved" {
		t.Errorf("PATCH the area back to client 1: %d %v", code, body)
	}

	path := legacyPath
	steps := []struct {
		name, method, suffix, body string
		want                       int
		status                     string
	}{
		{"start", http.MethodPost, "/start", "", http.StatusOK, stocktake.StatusInProgress},
		{"start in_progress", http.MethodPost, "/start", "", http.StatusConflict, stocktake.StatusInProgress},
		{"new area in_progress", http.MethodPatch, "", `{"location_id":"` + own.zone + `"}`, http.StatusConflict, stocktake.StatusInProgress},
		{"title in_progress", http.MethodPatch, "", `{"title":"renamed"}`, http.StatusOK, stocktake.StatusInProgress},
		{"cancel in_progress", http.MethodPost, "/cancel", "", http.StatusOK, stocktake.StatusCancelled},
		{"cancel cancelled", http.MethodPost, "/cancel", "", http.StatusConflict, stocktake.StatusCancelled},
		{"start cancelled", http.MethodPost, "/start", "", http.StatusConflict, stocktake.StatusCancelled},
		{"complete cancelled", http.MethodPost, "/complete", "", http.StatusConflict, stocktake.StatusCancelled},
	}
	for _, s := range steps {
		code, body = a.do(orgCtx, s.method, path+s.suffix, s.body)
		if code != s.want {
			t.Errorf("%s: %d %v, want %d", s.name, code, body, s.want)
		}
		var status string
		if err := f.Admin.QueryRow(bg, `SELECT status FROM stocktake WHERE id = $1`, legacy["id"]).Scan(&status); err != nil || status != s.status {
			t.Errorf("%s: stored status %s %v, want %s", s.name, status, err, s.status)
		}
	}
	planned := a.create(orgCtx, "cancel planned", own.bin2)
	code, body = a.do(orgCtx, http.MethodPost, "/api/v1/stocktakes/"+planned["id"].(string)+"/cancel", "")
	if code != http.StatusOK || body["status"] != stocktake.StatusCancelled {
		t.Errorf("cancel planned: %d %v, want 200 cancelled", code, body)
	}
	if code, body = a.do(orgCtx, http.MethodPost, "/api/v1/stocktakes/"+f.ID()+"/start", ""); code != http.StatusNotFound {
		t.Errorf("start of an unknown stocktake: %d %v, want 404", code, body)
	}
}

// TestStocktakeAreaClientScope covers TEN-06 for stocktakes with area
// (WP-848, E-09): a principal restricted to client 1 neither sees nor
// writes a stocktake on a client-2 area (404 for reading, items, start,
// cancel, PATCH and DELETE; absent from the list) and creates, starts,
// reads and cancels a stocktake on a client-1 area. A principal restricted
// to the site of the client-1 tree starts one there as well.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStocktakeAreaClientScope(t *testing.T) {
	f := scopetest.Seed(t, "33")
	orgCtx := f.OrgCtx(f.OrgA)
	user := f.AppUser(t, f.OrgA, "scope-counter")
	a := newStocktakeAPI(t, f, user)
	own := seedAreaTree(t, f, f.Client1, "c848-c1")
	foreign := seedAreaTree(t, f, f.Client2, "c848-c2")
	ownAsset := f.Asset(t, f.OrgA, f.Client1, "c848-own")
	placeAsset(t, f, ownAsset, own.rack)

	hidden := a.create(orgCtx, "client 2", foreign.room)
	hiddenPath := "/api/v1/stocktakes/" + hidden["id"].(string)
	client1 := f.ClientCtx(f.Client1)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, hiddenPath, ""},
		{http.MethodGet, hiddenPath + "/items", ""},
		{http.MethodPost, hiddenPath + "/start", ""},
		{http.MethodPost, hiddenPath + "/cancel", ""},
		{http.MethodPatch, hiddenPath, `{"title":"taken over"}`},
		{http.MethodDelete, hiddenPath, ""},
	} {
		if code, body := a.do(client1, c.method, c.path, c.body); code != http.StatusNotFound {
			t.Errorf("client-1 %s %s: %d %v, want 404", c.method, c.path, code, body)
		}
	}
	var stored string
	if err := f.Admin.QueryRow(context.Background(), `SELECT title || '/' || status FROM stocktake WHERE id = $1`, hidden["id"]).Scan(&stored); err != nil ||
		stored != "client 2/planned" {
		t.Errorf("client-2 stocktake after the client-1 writes: %s %v, want unchanged", stored, err)
	}
	code, list := a.do(client1, http.MethodGet, "/api/v1/stocktakes?limit=200", "")
	if code != http.StatusOK {
		t.Fatalf("client-1 list: %d %v", code, list)
	}
	for _, d := range list["data"].([]any) {
		if d.(map[string]any)["id"] == hidden["id"] {
			t.Errorf("client-1 list shows the client-2 stocktake %v", d)
		}
	}

	mine := a.create(client1, "client 1", own.room)
	minePath := "/api/v1/stocktakes/" + mine["id"].(string)
	if code, body := a.do(client1, http.MethodPost, minePath+"/start", ""); code != http.StatusOK || body["total_expected"] != float64(1) {
		t.Errorf("client-1 start on a client-1 area: %d %v, want 200 with one expected asset", code, body)
	}
	if items := a.items(client1, mine["id"].(string), 200); len(items) != 1 || items[0]["asset_id"] != ownAsset {
		t.Errorf("client-1 items: %v, want the asset in the rack", items)
	}
	if code, body := a.do(client1, http.MethodPost, minePath+"/cancel", ""); code != http.StatusOK {
		t.Errorf("client-1 cancel on a client-1 area: %d %v, want 200", code, body)
	}

	siteScope := database.OrgWideScope(f.OrgA, f.User)
	siteScope.Sites = database.ScopeIDs(own.site)
	siteCtx := database.ContextWithTenantScope(context.Background(), &siteScope)
	bySite := a.create(siteCtx, "site", own.zone)
	if code, body := a.do(siteCtx, http.MethodPost, "/api/v1/stocktakes/"+bySite["id"].(string)+"/start", ""); code != http.StatusOK {
		t.Errorf("site-scoped start on its site: %d %v, want 200", code, body)
	}
	if code, body := a.do(siteCtx, http.MethodGet, hiddenPath, ""); code != http.StatusNotFound {
		t.Errorf("site-scoped read of a stocktake on another site: %d %v, want 404", code, body)
	}
}

// TestStocktakeAreaKeepsLocation covers LOC-11 for the stocktake (WP-848,
// E-71): deleting a bin that is the area of a stocktake answers 409 with
// the kind stocktake, deleting the bin of a frozen item 409 with
// stocktake_item; once the stocktake is deleted the bins can go.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStocktakeAreaKeepsLocation(t *testing.T) {
	f := scopetest.Seed(t, "33")
	orgCtx := f.OrgCtx(f.OrgA)
	user := f.AppUser(t, f.OrgA, "keep-counter")
	a := newStocktakeAPI(t, f, user)
	tr := seedAreaTree(t, f, f.Client1, "k848")
	item := f.Asset(t, f.OrgA, f.Client1, "k848-asset")
	placeAsset(t, f, item, tr.bin2)

	area := a.create(orgCtx, "bin area", tr.bin1)
	zone := a.create(orgCtx, "zone area", tr.zone)
	if code, body := a.do(orgCtx, http.MethodPost, "/api/v1/stocktakes/"+zone["id"].(string)+"/start", ""); code != http.StatusOK {
		t.Fatalf("start of the zone stocktake: %d %v", code, body)
	}
	// The asset leaves the bin; the frozen item still names it.
	if _, err := f.Admin.Exec(context.Background(), `UPDATE asset SET location_id = $2 WHERE id = $1`, item, tr.room); err != nil {
		t.Fatal(err)
	}
	deps := func(body map[string]any) []string {
		var out []string
		for _, d := range body["dependencies"].([]any) {
			out = append(out, d.(string))
		}
		return out
	}
	if code, body := a.do(orgCtx, http.MethodDelete, "/api/v1/locations/"+tr.bin1, ""); code != http.StatusConflict ||
		!slices.Equal(deps(body), []string{locations.DependencyStocktake}) {
		t.Errorf("delete of the bin of a stocktake area: %d %v, want 409 [stocktake]", code, body)
	}
	if code, body := a.do(orgCtx, http.MethodDelete, "/api/v1/locations/"+tr.bin2, ""); code != http.StatusConflict ||
		!slices.Equal(deps(body), []string{locations.DependencyStocktakeItem}) {
		t.Errorf("delete of the bin of a frozen item: %d %v, want 409 [stocktake_item]", code, body)
	}
	for _, st := range []map[string]any{area, zone} {
		if code, body := a.do(orgCtx, http.MethodDelete, "/api/v1/stocktakes/"+st["id"].(string), ""); code != http.StatusNoContent {
			t.Fatalf("delete of stocktake %v: %d %v", st["title"], code, body)
		}
	}
	for _, bin := range []string{tr.bin1, tr.bin2} {
		if code, body := a.do(orgCtx, http.MethodDelete, "/api/v1/locations/"+bin, ""); code != http.StatusNoContent {
			t.Errorf("delete of bin %s after the stocktakes: %d %v, want 204", bin, code, body)
		}
	}
}

// TestStocktakeSnapshotMigration replays the down and the up part of
// migration 000260 (WP-848) in a rolled-back transaction: down removes the
// area, the snapshot and the place found and restores the organization
// policy, a stocktake of an earlier release keeps status and scope; up
// brings the columns back with NULL area for it.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStocktakeSnapshotMigration(t *testing.T) {
	f := scopetest.Seed(t, "33")
	ctx := context.Background()
	orgCtx := f.OrgCtx(f.OrgA)
	user := f.AppUser(t, f.OrgA, "mig-counter")
	a := newStocktakeAPI(t, f, user)
	tr := seedAreaTree(t, f, f.Client1, "m848")
	placeAsset(t, f, f.Asset(t, f.OrgA, f.Client1, "m848-asset"), tr.rack)
	area := a.create(orgCtx, "with area", tr.room)
	if code, body := a.do(orgCtx, http.MethodPost, "/api/v1/stocktakes/"+area["id"].(string)+"/start", ""); code != http.StatusOK {
		t.Fatalf("start: %d %v", code, body)
	}
	legacy := &stocktake.Stocktake{OrganizationID: f.OrgA, Title: "legacy", Status: stocktake.StatusInProgress, Scope: "rack"}
	if err := stocktake.NewPGRepository(f.App).Create(orgCtx, legacy); err != nil {
		t.Fatal(err)
	}
	up, err := os.ReadFile("../../migrations/000260_stocktake_snapshot.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../migrations/000260_stocktake_snapshot.down.sql")
	if err != nil {
		t.Fatal(err)
	}

	tx, err := f.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	shape := func() string {
		t.Helper()
		var s string
		if shapeErr := tx.QueryRow(ctx, `SELECT (to_regclass('stocktake_item') IS NOT NULL)::text
			|| '/' || (SELECT count(*) FROM pg_attribute WHERE attrelid = 'stocktake'::regclass AND attname IN ('location_id', 'client_id', 'site_id') AND NOT attisdropped)::text
			|| '/' || (SELECT count(*) FROM pg_attribute WHERE attrelid = 'stock_scan'::regclass AND attname = 'location_id' AND NOT attisdropped)::text
			|| '/' || (SELECT string_agg(policyname, ',' ORDER BY policyname) FROM pg_policies WHERE tablename = 'stocktake')
			|| '/' || (SELECT status || ':' || scope FROM stocktake WHERE id = $1)`, legacy.ID).Scan(&s); shapeErr != nil {
			t.Fatalf("schema shape: %v", shapeErr)
		}
		return s
	}
	if _, err = tx.Exec(ctx, string(down)); err != nil {
		t.Fatalf("down: %v", err)
	}
	if got, want := shape(), "false/0/0/stocktake_tenant_isolation/in_progress:rack"; got != want {
		t.Errorf("after down: %s, want %s", got, want)
	}
	if _, err = tx.Exec(ctx, string(up)); err != nil {
		t.Fatalf("up: %v", err)
	}
	if got, want := shape(), "true/3/1/stocktake_isolation,stocktake_scoped_delete,stocktake_scoped_update/in_progress:rack"; got != want {
		t.Errorf("after up: %s, want %s", got, want)
	}
	var area0 bool
	if err = tx.QueryRow(ctx, `SELECT location_id IS NULL AND client_id IS NULL AND site_id IS NULL FROM stocktake WHERE id = $1`, legacy.ID).Scan(&area0); err != nil || !area0 {
		t.Errorf("legacy stocktake after up: area NULL %v err=%v, want true", area0, err)
	}
}
