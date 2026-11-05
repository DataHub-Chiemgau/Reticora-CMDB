package stocktake_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
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
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/stocktake"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// legacyScan inserts a scan of an earlier release (scan_result given, no
// place found) through the maintenance pool; stock_scan_derive_scope
// derives client and site from the asset, the stocktake has no area.
func legacyScan(t *testing.T, f *scopetest.Fixture, stocktakeID, assetID, user, result string) string {
	t.Helper()
	var id string
	if err := f.Admin.QueryRow(context.Background(), `INSERT INTO stock_scan (organization_id, stocktake_id, asset_id, scanned_by, scan_result)
		VALUES ($1, $2, $3, $4, $5) RETURNING id::text`, f.OrgA, stocktakeID, assetID, user, result).Scan(&id); err != nil {
		t.Fatalf("legacy scan of %s: %v", assetID, err)
	}
	return id
}

// legacyStocktake creates an organization-wide in-progress stocktake of an
// earlier release (no area, no snapshot) through the repository.
func legacyStocktake(t *testing.T, f *scopetest.Fixture, orgID, title string) string {
	t.Helper()
	st := &stocktake.Stocktake{OrganizationID: orgID, Title: title, Status: stocktake.StatusInProgress, Scope: "full"}
	if err := stocktake.NewPGRepository(f.App).Create(f.OrgCtx(orgID), st); err != nil {
		t.Fatalf("create stocktake %s: %v", title, err)
	}
	return st.ID
}

// TestStocktakeRepositoryClientScope runs the stocktake repository with the
// principal's tenant scope (TEN-06, WP-018, rewritten by WP-287): the
// scans carry client_id and site_id (stock_scan_derive_scope) and the
// policies of stock_scan apply them. In an organization-wide stocktake of
// an earlier release a principal restricted to client 1 sees only the scans
// with client 1; the scans of a stocktake on a client-2 area are hidden
// with the stocktake (404), and in a stocktake on a client-1 area it cannot
// scan a client-2 asset (404).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStocktakeRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "33")
	bg := context.Background()
	own := f.Asset(t, f.OrgA, f.Client1, "scopetest-33-own")
	foreign := f.Asset(t, f.OrgA, f.Client2, "scopetest-33-foreign")
	user := f.AppUser(t, f.OrgA, "counter")
	repo := stocktake.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	client1 := f.ClientCtx(f.Client1)
	page := api.PaginationParams{Limit: 100}

	legacy := legacyStocktake(t, f, f.OrgA, "Inventur")
	ownScan := legacyScan(t, f, legacy, own, user, "missing")
	legacyScan(t, f, legacy, foreign, user, "missing")
	var clients []string
	rows, err := f.Admin.Query(bg, `SELECT client_id::text FROM stock_scan WHERE stocktake_id = $1 ORDER BY client_id = $2 DESC`, legacy, f.Client1)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var c string
		if err = rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		clients = append(clients, c)
	}
	rows.Close()
	if !slices.Equal(clients, []string{f.Client1, f.Client2}) {
		t.Fatalf("derived clients of the legacy scans: %v, want client 1 and client 2", clients)
	}
	scans, total, err := repo.ListScans(client1, f.OrgA, legacy, page)
	if err != nil || total != 1 || len(scans) != 1 || scans[0].ID != ownScan || scans[0].ClientID != f.Client1 {
		t.Fatalf("client-1 scans of the legacy stocktake: total=%d %+v err=%v, want only the own one", total, scans, err)
	}
	if scans, total, err = repo.ListScans(orgCtx, f.OrgA, legacy, page); err != nil || total != 2 || len(scans) != 2 {
		t.Fatalf("org-wide scans of the legacy stocktake: total=%d %+v err=%v, want both", total, scans, err)
	}
	diff, total, err := repo.Differences(client1, f.OrgA, legacy, page)
	if err != nil || total != 1 || len(diff) != 1 || diff[0].AssetID != own || diff[0].Classification != stocktake.ScanMissing {
		t.Fatalf("client-1 differences of the legacy stocktake: total=%d %+v err=%v, want the own missing asset", total, diff, err)
	}
	if _, _, err = repo.ListScans(bg, f.OrgA, legacy, page); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}

	// A stocktake on a client-2 area: its scans are hidden with it.
	user2 := f.AppUser(t, f.OrgA, "counter-2")
	a := newStocktakeAPI(t, f, user2)
	tr2 := seedAreaTree(t, f, f.Client2, "t287-c2")
	placeAsset(t, f, foreign, tr2.room)
	hidden := a.create(orgCtx, "client 2", tr2.site)["id"].(string)
	if code, body := a.do(orgCtx, http.MethodPost, "/api/v1/stocktakes/"+hidden+"/start", ""); code != http.StatusOK {
		t.Fatalf("start of the client-2 stocktake: %d %v", code, body)
	}
	if code, body := a.do(orgCtx, http.MethodPost, "/api/v1/stocktakes/"+hidden+"/scans",
		`{"asset_id":"`+foreign+`","location_id":"`+tr2.room+`"}`); code != http.StatusCreated || body["client_id"] != f.Client2 {
		t.Fatalf("org-wide scan in the client-2 stocktake: %d %v, want 201 with client 2", code, body)
	}
	for _, c := range []struct{ method, suffix, body string }{
		{http.MethodGet, "/scans", ""},
		{http.MethodGet, "/differences", ""},
		{http.MethodPost, "/scans", `{"asset_id":"` + own + `","location_id":"` + tr2.room + `"}`},
	} {
		if code, body := a.do(client1, c.method, "/api/v1/stocktakes/"+hidden+c.suffix, c.body); code != http.StatusNotFound {
			t.Errorf("client-1 %s …%s of the client-2 stocktake: %d %v, want 404", c.method, c.suffix, code, body)
		}
	}

	// A stocktake on a client-1 area: the client-1 principal scans its own
	// asset, not the client-2 one.
	tr1 := seedAreaTree(t, f, f.Client1, "t287-c1")
	mine := a.create(client1, "client 1", tr1.site)["id"].(string)
	if code, body := a.do(client1, http.MethodPost, "/api/v1/stocktakes/"+mine+"/start", ""); code != http.StatusOK {
		t.Fatalf("client-1 start: %d %v", code, body)
	}
	if code, body := a.do(client1, http.MethodPost, "/api/v1/stocktakes/"+mine+"/scans",
		`{"asset_id":"`+foreign+`","location_id":"`+tr1.room+`"}`); code != http.StatusNotFound {
		t.Errorf("client-1 scan of a client-2 asset: %d %v, want 404", code, body)
	}
	if code, body := a.do(client1, http.MethodPost, "/api/v1/stocktakes/"+mine+"/scans",
		`{"asset_id":"`+own+`","location_id":"`+tr1.room+`"}`); code != http.StatusCreated || body["scan_result"] != stocktake.ScanUnexpected ||
		body["client_id"] != f.Client1 || body["site_id"] != tr1.site {
		t.Errorf("client-1 scan of its own asset: %d %v, want 201 unexpected with client 1 and the site", code, body)
	}
	var stored int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM stock_scan WHERE stocktake_id = $1 AND asset_id = $2`, mine, foreign).Scan(&stored); err != nil || stored != 0 {
		t.Errorf("client-2 asset scans in the client-1 stocktake: %d %v, want 0", stored, err)
	}
}

// TestStocktakeForeignClientCorrectionAndComplete covers the completion of a
// stocktake of an earlier release (TEN-06, WP-537; rewritten by WP-638,
// F-365, E-09, and WP-287): it has no snapshot, so the completion is 409
// for an org-wide principal and ErrNotFound for one restricted to client 1
// (403 in the handler); no asset changes, the stocktake stays open and can
// be cancelled. A stocktake of another organization the client-1 principal
// can neither complete nor update nor cancel (ErrNotFound).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStocktakeForeignClientCorrectionAndComplete(t *testing.T) {
	f := scopetest.Seed(t, "33")
	bg := context.Background()
	own := f.Asset(t, f.OrgA, f.Client1, "scopetest-33-own")
	foreign := f.Asset(t, f.OrgA, f.Client2, "scopetest-33-foreign")
	if _, err := f.Admin.Exec(bg, `UPDATE asset SET location = 'rack-a' WHERE id = ANY($1::uuid[])`, []string{own, foreign}); err != nil {
		t.Fatalf("asset locations: %v", err)
	}
	user := f.AppUser(t, f.OrgA, "counter")
	repo := stocktake.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	st := legacyStocktake(t, f, f.OrgA, "Inventur")
	legacyScan(t, f, st, own, user, "missing")
	legacyScan(t, f, st, foreign, user, "unexpected")
	stB := legacyStocktake(t, f, f.OrgB, "Inventur B")

	assets := func() string {
		var s string
		if err := f.Admin.QueryRow(bg, `SELECT string_agg(status || '@' || COALESCE(location, ''), ',' ORDER BY id) FROM asset WHERE id = ANY($1::uuid[])`,
			[]string{own, foreign}).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	stored := func(id string) string {
		var s string
		if err := f.Admin.QueryRow(bg, `SELECT title || '/' || status || '/' || (completed_at IS NOT NULL)::text FROM stocktake WHERE id = $1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	wantAssets, wantOpen := assets(), stored(st)

	ctx := f.ClientCtx(f.Client1)
	if completion, err := repo.Complete(ctx, f.OrgA, st); !errors.Is(err, stocktake.ErrNotFound) {
		t.Errorf("client-1 completion of the organization-wide stocktake: %+v err=%v, want ErrNotFound", completion, err)
	}
	if completion, err := repo.Complete(orgCtx, f.OrgA, st); !errors.Is(err, stocktake.ErrNoSnapshot) {
		t.Errorf("org-wide completion of a stocktake without snapshot: %+v err=%v, want ErrNoSnapshot (409)", completion, err)
	}
	if got := assets(); got != wantAssets {
		t.Errorf("assets after the refused completions: %s, want %s", got, wantAssets)
	}
	if got := stored(st); got != wantOpen {
		t.Errorf("stocktake after the refused completions: %s, want %s", got, wantOpen)
	}
	if cancelled, err := repo.Cancel(orgCtx, f.OrgA, st); err != nil || cancelled.Status != stocktake.StatusCancelled {
		t.Errorf("org-wide cancel of the stocktake without snapshot: %+v %v, want cancelled", cancelled, err)
	}

	wantB := stored(stB)
	if _, err := repo.Complete(ctx, f.OrgA, stB); !errors.Is(err, stocktake.ErrNotFound) {
		t.Errorf("completion of an organization-B stocktake: err=%v, want ErrNotFound", err)
	}
	title := "hijacked"
	if _, err := repo.Update(ctx, f.OrgA, stB, stocktake.UpdateRequest{Title: &title}); !errors.Is(err, stocktake.ErrNotFound) {
		t.Errorf("update of an organization-B stocktake: err=%v, want ErrNotFound", err)
	}
	if _, err := repo.Cancel(ctx, f.OrgA, stB); !errors.Is(err, stocktake.ErrNotFound) {
		t.Errorf("cancel of an organization-B stocktake: err=%v, want ErrNotFound", err)
	}
	if got := stored(stB); got != wantB {
		t.Errorf("organization-B stocktake after foreign writes: %s, want %s", got, wantB)
	}
}

// TestAuditStocktakeOrgWideWritesNeedOrgWideScope: update, completion, start,
// cancel (WP-848) and delete of an organization-wide stocktake by a client-1
// principal are refused and change nothing (TEN-05, TEN-06, E-09; F-365,
// found by WP-537, closed by WP-638). Before WP-638 the client-1 principal
// completed it, so the client-2 differences could no longer be corrected,
// and its delete removed the client-2 scans with the stocktake (stock_scan
// ON DELETE CASCADE). Since WP-287 the org-wide completion of such a
// stocktake is ErrNoSnapshot (409) and no asset changes; the org-wide
// principal cancels it.
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
		id := legacyStocktake(t, f, f.OrgA, title)
		for _, a := range []string{own, foreign} {
			legacyScan(t, f, id, a, user, "missing")
		}
		return id
	}
	type row struct {
		title, status string
		scans         int
		foreign       string
	}
	stored := func(id string) row {
		t.Helper()
		var r row
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE((SELECT title FROM stocktake WHERE id = $1), '<deleted>'),
			COALESCE((SELECT status FROM stocktake WHERE id = $1), ''), (SELECT count(*) FROM stock_scan WHERE stocktake_id = $1),
			(SELECT status FROM asset WHERE id = $2)`, id, foreign).
			Scan(&r.title, &r.status, &r.scans, &r.foreign); err != nil {
			t.Fatalf("stored stocktake %s: %v", id, err)
		}
		return r
	}

	client1 := f.ClientCtx(f.Client1)
	st := newStocktake("k5 inventory")
	want := stored(st)

	title := "renamed by client 1"
	if _, err := repo.Update(client1, f.OrgA, st, stocktake.UpdateRequest{Title: &title}); err == nil {
		t.Errorf("client-1 update of the organization-wide stocktake: accepted, want refused (E-09)")
	}
	if _, err := repo.Complete(client1, f.OrgA, st); err == nil {
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
	// The organization-wide principal cannot complete it either (no
	// snapshot, WP-287), and no asset changes; it cancels it.
	if _, err := repo.Complete(orgCtx, f.OrgA, st); !errors.Is(err, stocktake.ErrNoSnapshot) {
		t.Errorf("organization-wide completion without snapshot: %v, want ErrNoSnapshot", err)
	}
	if got := stored(st); got != want {
		t.Errorf("organization-wide stocktake after the refused completion: %+v, want %+v", got, want)
	}
	if _, err := repo.Cancel(orgCtx, f.OrgA, st); err != nil {
		t.Errorf("organization-wide cancel: %v", err)
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
// stocktake (without area) by a principal restricted to client 1 or to a
// site answer 403 problem+json and leave the stocktake, its scans and the
// assets unchanged. Such a stocktake takes no scans (WP-287): 404 for the
// client-1 principal, 409 for the org-wide one, whose completion is 409 as
// well; it updates and deletes the stocktake.
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
	room := f.Room(t, f.Client1, "h287-room")

	st := legacyStocktake(t, f, f.OrgA, "handler inventory")
	legacyScan(t, f, st, foreign, user, "missing")

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
			(SELECT status FROM asset WHERE id = $2)`, st, foreign).
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
	path := "/api/v1/stocktakes/" + st
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

	// No scan without snapshot: hidden from the client-1 principal (the
	// stocktake is read FOR SHARE under the restrictive update policy), 409
	// for the org-wide one.
	scan := `{"asset_id":"` + own + `","location_id":"` + room + `"}`
	if w := do(bound["client-1"], http.MethodPost, path+"/scans", scan); w.Code != http.StatusNotFound {
		t.Errorf("client-1 scan in the organization-wide stocktake: %d %s, want 404", w.Code, w.Body.String())
	}
	if w := do(orgCtx, http.MethodPost, path+"/scans", scan); w.Code != http.StatusConflict {
		t.Errorf("org-wide scan in a stocktake without snapshot: %d %s, want 409", w.Code, w.Body.String())
	}
	if w := do(orgCtx, http.MethodPost, path+"/complete", `{"apply_corrections":true}`); w.Code != http.StatusConflict {
		t.Errorf("org-wide completion without snapshot: %d %s, want 409", w.Code, w.Body.String())
	}
	if got := stored(); got != want {
		t.Errorf("stocktake after the refused scans and completion: %+v, want %+v", got, want)
	}

	// Positive control: the org-wide principal updates and deletes.
	if w := do(orgCtx, http.MethodPatch, path, `{"title":"renamed org-wide"}`); w.Code != http.StatusOK {
		t.Fatalf("org-wide update: %d %s, want 200", w.Code, w.Body.String())
	}
	if w := do(orgCtx, http.MethodDelete, path, ""); w.Code != http.StatusNoContent {
		t.Fatalf("org-wide delete: %d %s, want 204", w.Code, w.Body.String())
	}
	if got := stored(); got.title != "<deleted>" || got.scans != 0 || got.foreign != want.foreign {
		t.Errorf("stocktake after the org-wide delete: %+v, want deleted with its scans, the asset unchanged", got)
	}
}

// fieldPolicy returns the field policy of a role with the permissions
// stocktake:read and asset:read whose asset field grants are levels.
func fieldPolicy(t *testing.T, f *scopetest.Fixture, levels map[string]string) *permission.FieldPolicy {
	t.Helper()
	bg := context.Background()
	role := f.ID()
	perms := []identity.Permission{"stocktake:read", "asset:read"}
	raw, err := json.Marshal(perms)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Admin.Exec(bg, `INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, $3, $4::jsonb)`,
		role, f.OrgA, "wp287-"+role, string(raw)); err != nil {
		t.Fatal(err)
	}
	for key, level := range levels {
		if _, err = f.Admin.Exec(bg, `INSERT INTO field_grant (organization_id, role_id, object_kind, field_key, level) VALUES ($1, $2, 'asset', $3, $4)`,
			f.OrgA, role, key, level); err != nil {
			t.Fatal(err)
		}
	}
	policy, err := permission.NewFieldResolver(nil, nil, permission.NewPGFieldGrants(f.App), nil).
		PolicyForGrants(bg, f.OrgA, []identity.Grant{{Permissions: perms, RoleID: role}}, nil)
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	return policy
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
// migration 000260 (WP-848) in a rolled-back transaction, below 000265
// (WP-287), whose down runs first and whose up runs last: down removes the
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
	scanUp, err := os.ReadFile("../../migrations/000265_stock_scan_classification.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	scanDown, err := os.ReadFile("../../migrations/000265_stock_scan_classification.down.sql")
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
	if _, err = tx.Exec(ctx, string(scanDown)); err != nil {
		t.Fatalf("down of 000265: %v", err)
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
	if _, err = tx.Exec(ctx, string(scanUp)); err != nil {
		t.Fatalf("up of 000265: %v", err)
	}
	var area0 bool
	if err = tx.QueryRow(ctx, `SELECT location_id IS NULL AND client_id IS NULL AND site_id IS NULL FROM stocktake WHERE id = $1`, legacy.ID).Scan(&area0); err != nil || !area0 {
		t.Errorf("legacy stocktake after up: area NULL %v err=%v, want true", area0, err)
	}
}

// pages reads every row of the list path page by page with limit and
// returns the rows in order.
func (a *stocktakeAPI) pages(ctx context.Context, path string, limit int) []map[string]any {
	a.t.Helper()
	var all []map[string]any
	cursor := ""
	for range 50 {
		p := path + "?limit=" + strconv.Itoa(limit)
		if cursor != "" {
			p += "&cursor=" + url.QueryEscape(cursor)
		}
		code, page := a.do(ctx, http.MethodGet, p, "")
		if code != http.StatusOK {
			a.t.Fatalf("GET %s: %d %v, want 200", p, code, page)
		}
		data, _ := page["data"].([]any)
		if len(data) > limit {
			a.t.Fatalf("GET %s: %d rows on a page of %d", p, len(data), limit)
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
	a.t.Fatalf("GET %s: no last page", path)
	return nil
}

// differenceKey renders a difference as kind:object:classification
// expected@location>counted@location.
func differenceKey(d map[string]any) string {
	object, _ := d["asset_id"].(string)
	if object == "" {
		object, _ = d["consumable_id"].(string)
	}
	return fmt.Sprintf("%v:%s:%v %v@%v>%v@%v", d["item_kind"], object, d["classification"],
		d["expected_quantity"], d["expected_location_id"], d["counted_quantity"], d["found_location_id"])
}

// scanWorld is an in-progress stocktake on the site of an area tree of
// client 1 with a frozen snapshot: the assets stay (room), moved (rack)
// and gone (bin1), the consumable cable with 5 in bin1 and 3 in bin2. extra
// (another room) and the consumable plug (no stock) are not in it.
type scanWorld struct {
	f                                 *scopetest.Fixture
	a                                 *stocktakeAPI
	tr                                areaTree
	id, path                          string
	stay, moved, gone, extra, foreign string
	cable, plug                       string
}

func newScanWorld(t *testing.T, name string) *scanWorld {
	t.Helper()
	f := scopetest.Seed(t, "33")
	w := &scanWorld{f: f, tr: seedAreaTree(t, f, f.Client1, name)}
	w.a = newStocktakeAPI(t, f, f.AppUser(t, f.OrgA, name+"-counter"))
	w.stay = f.Asset(t, f.OrgA, f.Client1, name+"-stay")
	w.moved = f.Asset(t, f.OrgA, f.Client1, name+"-moved")
	w.gone = f.Asset(t, f.OrgA, f.Client1, name+"-gone")
	w.extra = f.Asset(t, f.OrgA, f.Client1, name+"-extra")
	w.foreign = f.Asset(t, f.OrgA, f.Client2, name+"-foreign")
	placeAsset(t, f, w.stay, w.tr.room)
	placeAsset(t, f, w.moved, w.tr.rack)
	placeAsset(t, f, w.gone, w.tr.bin1)
	placeAsset(t, f, w.extra, w.tr.otherRoom)
	w.cable = stockConsumable(t, f, f.Client1, name+"-cable", map[string]float64{w.tr.bin1: 5, w.tr.bin2: 3})
	w.plug = stockConsumable(t, f, f.Client1, name+"-plug", nil)
	w.id = w.a.create(f.OrgCtx(f.OrgA), name, w.tr.site)["id"].(string)
	w.path = "/api/v1/stocktakes/" + w.id
	if code, body := w.a.do(f.OrgCtx(f.OrgA), http.MethodPost, w.path+"/start", ""); code != http.StatusOK || body["total_expected"] != float64(5) {
		t.Fatalf("start: %d %v, want 200 with 5 expected items", code, body)
	}
	return w
}

// scan records a scan as the org-wide principal and returns status and body.
func (w *scanWorld) scan(body string) (int, map[string]any) {
	return w.a.do(w.f.OrgCtx(w.f.OrgA), http.MethodPost, w.path+"/scans", body)
}

// assetScan and stockScan render the bodies of a scan.
func assetScan(id, loc string) string { return `{"asset_id":"` + id + `","location_id":"` + loc + `"}` }
func stockScan(id, loc string, qty float64) string {
	return fmt.Sprintf(`{"consumable_id":"%s","location_id":"%s","quantity":%v,"scan_method":"barcode","code":"C-1"}`, id, loc, qty)
}

// recordScans records the scans of the classification and difference
// tests and returns the ids of the recorded scans in order.
func (w *scanWorld) recordScans(t *testing.T) []string {
	t.Helper()
	tr := w.tr
	var ids []string
	for _, c := range []struct {
		name, body, result string
	}{
		{"asset of the snapshot at its place", assetScan(w.stay, tr.room), stocktake.ScanFound},
		{"asset of the snapshot at another place", assetScan(w.moved, tr.room), stocktake.ScanFound},
		{"asset outside the snapshot", assetScan(w.extra, tr.room), stocktake.ScanUnexpected},
		{"consumable of the snapshot, first count", stockScan(w.cable, tr.bin1, 2), stocktake.ScanFound},
		{"consumable of the snapshot, second count", stockScan(w.cable, tr.bin1, 3), stocktake.ScanFound},
		{"consumable of the snapshot, short", stockScan(w.cable, tr.bin2, 1), stocktake.ScanFound},
		{"consumable without snapshot row", stockScan(w.plug, tr.bin2, 4), stocktake.ScanUnexpected},
	} {
		code, body := w.scan(c.body)
		if code != http.StatusCreated || body["scan_result"] != c.result || body["client_id"] != w.f.Client1 || body["site_id"] != tr.site {
			t.Fatalf("%s: %d %v, want 201 %s with client 1 and the site of the area", c.name, code, body, c.result)
		}
		ids = append(ids, body["id"].(string))
	}
	return ids
}

// TestStocktakeScanClassification covers the scans of WP-287 (INV-01): only
// an in-progress stocktake with snapshot takes scans (409 for planned,
// completed, cancelled and a stocktake without snapshot); the server
// classifies found (in the snapshot) and unexpected; scan_result, ci_id and
// location_found are no input (400); a place found outside the area, an
// unknown one and a consumable outside a bin are 422 on location_id; an
// unknown asset or consumable is 404. A scan writes no stocktake row; the
// totals of the in-progress stocktake are computed when read.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStocktakeScanClassification(t *testing.T) {
	w := newScanWorld(t, "c287")
	f, tr := w.f, w.tr
	bg := context.Background()
	orgCtx := f.OrgCtx(f.OrgA)
	var before string
	if err := f.Admin.QueryRow(bg, `SELECT updated_at::text || total_scanned FROM stocktake WHERE id = $1`, w.id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	w.recordScans(t)
	var after string
	if err := f.Admin.QueryRow(bg, `SELECT updated_at::text || total_scanned FROM stocktake WHERE id = $1`, w.id).Scan(&after); err != nil || after != before {
		t.Errorf("stocktake row after the scans: %s %v, want unchanged %s", after, err, before)
	}

	for _, c := range []struct {
		name, body string
		want       int
		field      string
	}{
		{"scan_result as input", `{"asset_id":"` + w.stay + `","location_id":"` + tr.room + `","scan_result":"missing"}`, http.StatusBadRequest, ""},
		{"ci_id as input", `{"asset_id":"` + w.stay + `","location_id":"` + tr.room + `","ci_id":"` + f.ID() + `"}`, http.StatusBadRequest, ""},
		{"location_found as input", `{"asset_id":"` + w.stay + `","location_id":"` + tr.room + `","location_found":"Raum A"}`, http.StatusBadRequest, ""},
		{"place found outside the area", assetScan(w.stay, tr.otherRoom), http.StatusUnprocessableEntity, "location_id"},
		{"unknown place found", assetScan(w.stay, f.ID()), http.StatusUnprocessableEntity, "location_id"},
		{"consumable outside a bin", stockScan(w.cable, tr.shelfOf(t, f), 1), http.StatusUnprocessableEntity, "location_id"},
		{"unknown asset", assetScan(f.ID(), tr.room), http.StatusNotFound, ""},
		{"unknown consumable", stockScan(f.ID(), tr.bin1, 1), http.StatusNotFound, ""},
	} {
		code, body := w.scan(c.body)
		if code != c.want || fieldOf(body) != c.field {
			t.Errorf("%s: %d %v, want %d (field %q)", c.name, code, body, c.want, c.field)
		}
	}

	code, body := w.a.do(orgCtx, http.MethodGet, w.path, "")
	if code != http.StatusOK || body["total_scanned"] != float64(6) || body["total_missing"] != float64(1) || body["total_surplus"] != float64(2) {
		t.Errorf("in-progress stocktake: %d %v, want 6 scanned, 1 missing (gone), 2 unexpected (extra, plug)", code, body)
	}

	// Scans need in_progress and a snapshot.
	planned := w.a.create(orgCtx, "c287 planned", tr.room)["id"].(string)
	cancelled := w.a.create(orgCtx, "c287 cancelled", tr.room)["id"].(string)
	if code, body = w.a.do(orgCtx, http.MethodPost, "/api/v1/stocktakes/"+cancelled+"/start", ""); code != http.StatusOK {
		t.Fatalf("start: %d %v", code, body)
	}
	if code, body = w.a.do(orgCtx, http.MethodPost, "/api/v1/stocktakes/"+cancelled+"/cancel", ""); code != http.StatusOK {
		t.Fatalf("cancel: %d %v", code, body)
	}
	completed := w.a.create(orgCtx, "c287 completed", tr.room)["id"].(string)
	for _, step := range []string{"/start", "/complete"} {
		if code, body = w.a.do(orgCtx, http.MethodPost, "/api/v1/stocktakes/"+completed+step, ""); code != http.StatusOK {
			t.Fatalf("%s: %d %v", step, code, body)
		}
	}
	legacy := legacyStocktake(t, f, f.OrgA, "c287 legacy")
	for name, id := range map[string]string{"planned": planned, "cancelled": cancelled, "completed": completed, "without snapshot": legacy} {
		if code, body = w.a.do(orgCtx, http.MethodPost, "/api/v1/stocktakes/"+id+"/scans", assetScan(w.stay, tr.room)); code != http.StatusConflict {
			t.Errorf("scan of a %s stocktake: %d %v, want 409", name, code, body)
		}
	}
	if code, body = w.a.do(orgCtx, http.MethodPost, "/api/v1/stocktakes/"+f.ID()+"/scans", assetScan(w.stay, tr.room)); code != http.StatusNotFound {
		t.Errorf("scan of an unknown stocktake: %d %v, want 404", code, body)
	}
}

// shelfOf returns the shelf of the tree, the parent of bin1.
func (tr areaTree) shelfOf(t *testing.T, f *scopetest.Fixture) string {
	t.Helper()
	var shelf string
	if err := f.Admin.QueryRow(context.Background(), `SELECT parent_id::text FROM location WHERE id = $1`, tr.bin1).Scan(&shelf); err != nil {
		t.Fatal(err)
	}
	return shelf
}

// TestStocktakeDifferencesAndCompletion covers the difference list and the
// completion of WP-287 (INV-01, API-06): GET …/differences lists missing,
// unexpected, an asset at another place and a consumable with another
// quantity, embeds the asset projected by the field permissions
// (permission.Project, a field without read grant is left out) and the
// consumable; it pages by cursor like GET …/scans, and the deprecated alias
// …/difference answers the same. The transitional completion writes the
// result into the snapshot (counted_quantity, classification, an item per
// unexpected object) and the totals, leaves asset.status and asset.location
// unchanged and takes no further scans.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStocktakeDifferencesAndCompletion(t *testing.T) {
	w := newScanWorld(t, "d287")
	f, tr := w.f, w.tr
	bg := context.Background()
	orgCtx := f.OrgCtx(f.OrgA)
	if _, err := f.Admin.Exec(bg, `UPDATE asset SET serial_number = 'SN-d287', location = 'Lager Nord' WHERE id = $1`, w.moved); err != nil {
		t.Fatal(err)
	}
	scanIDs := w.recordScans(t)

	want := []string{
		fmt.Sprintf("asset:%s:missing 1@%s>0@<nil>", w.gone, tr.bin1),
		fmt.Sprintf("asset:%s:found 1@%s>1@%s", w.moved, tr.rack, tr.room),
		fmt.Sprintf("asset:%s:unexpected 0@<nil>>1@%s", w.extra, tr.room),
		fmt.Sprintf("consumable:%s:found 3@%s>1@%s", w.cable, tr.bin2, tr.bin2),
		fmt.Sprintf("consumable:%s:unexpected 0@<nil>>4@%s", w.plug, tr.bin2),
	}
	slices.Sort(want)
	keys := func(rows []map[string]any) []string {
		out := make([]string, 0, len(rows))
		for _, d := range rows {
			out = append(out, differenceKey(d))
		}
		slices.Sort(out)
		return out
	}
	all := w.a.pages(orgCtx, w.path+"/differences", 200)
	if got := keys(all); !slices.Equal(got, want) {
		t.Errorf("differences:\n  %v\nwant\n  %v", got, want)
	}
	for _, d := range all {
		switch {
		case d["asset_id"] == w.moved:
			embedded, _ := d["asset"].(map[string]any)
			if embedded["id"] != w.moved || embedded["serial_number"] != "SN-d287" || embedded["location_id"] != tr.rack {
				t.Errorf("embedded asset of the moved asset: %v", embedded)
			}
		case d["consumable_id"] == w.plug:
			embedded, _ := d["consumable"].(map[string]any)
			if embedded["id"] != w.plug || embedded["name"] != "d287-plug" {
				t.Errorf("embedded consumable of the plug: %v", embedded)
			}
		}
	}
	paged := w.a.pages(orgCtx, w.path+"/differences", 2)
	if got := keys(paged); !slices.Equal(got, want) {
		t.Errorf("differences by pages of 2: %v, want %v", got, want)
	}
	if alias := w.a.pages(orgCtx, w.path+"/difference", 200); !slices.Equal(keys(alias), want) {
		t.Errorf("deprecated alias …/difference: %v, want %v", keys(alias), want)
	}
	if code, body := w.a.do(orgCtx, http.MethodGet, w.path+"/differences?cursor=forged.cursor", ""); code != http.StatusBadRequest {
		t.Errorf("differences with a forged cursor: %d %v, want 400", code, body)
	}

	// The serial number without read grant is left out of the embedded asset.
	policy := fieldPolicy(t, f, map[string]string{"core.serial_number": "none"})
	_, page := w.a.do(permission.WithPolicy(orgCtx, policy), http.MethodGet, w.path+"/differences?limit=200", "")
	data, _ := page["data"].([]any)
	projected := 0
	for _, d := range data {
		entry := d.(map[string]any)
		if entry["asset_id"] != w.moved {
			continue
		}
		embedded, _ := entry["asset"].(map[string]any)
		if _, leaked := embedded["serial_number"]; leaked || embedded["id"] != w.moved || embedded["name"] == nil {
			t.Errorf("projected embedded asset: %v, want id and name without serial_number", embedded)
		}
		projected++
	}
	if projected != 1 {
		t.Errorf("projected differences: %v, want the moved asset once", page)
	}

	// Scans page newest first by cursor.
	scans := w.a.pages(orgCtx, w.path+"/scans", 3)
	got := make([]string, 0, len(scans))
	for _, s := range scans {
		got = append(got, s["id"].(string))
	}
	if !slices.Equal(got, reversed(scanIDs)) {
		t.Errorf("scans by pages of 3: %v, want the recorded scans newest first %v", got, reversed(scanIDs))
	}

	// The transitional completion fixes the result.
	type assetRow struct{ status, location, locationID string }
	assets := func() map[string]assetRow {
		out := map[string]assetRow{}
		rows, err := f.Admin.Query(bg, `SELECT id::text, status, COALESCE(location, ''), COALESCE(location_id::text, '') FROM asset WHERE id = ANY($1::uuid[])`,
			[]string{w.stay, w.moved, w.gone, w.extra})
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var r assetRow
			if err = rows.Scan(&id, &r.status, &r.location, &r.locationID); err != nil {
				t.Fatal(err)
			}
			out[id] = r
		}
		return out
	}
	wantAssets := assets()
	code, body := w.a.do(orgCtx, http.MethodPost, w.path+"/complete", `{"apply_corrections":true}`)
	st, _ := body["stocktake"].(map[string]any)
	if code != http.StatusOK || st["status"] != stocktake.StatusCompleted || st["total_scanned"] != float64(6) ||
		st["total_missing"] != float64(1) || st["total_surplus"] != float64(2) || st["total_expected"] != float64(5) || body["corrections_applied"] != float64(0) {
		t.Fatalf("completion: %d %v, want completed with 6 scanned, 1 missing, 2 unexpected", code, body)
	}
	if got := assets(); !maps.Equal(got, wantAssets) {
		t.Errorf("assets after the completion: %v, want unchanged %v", got, wantAssets)
	}
	result := map[string]string{}
	for _, it := range w.a.items(orgCtx, w.id, 200) {
		object, _ := it["asset_id"].(string)
		if object == "" {
			object = it["consumable_id"].(string) + "@" + it["location_id"].(string)
		}
		result[object] = fmt.Sprintf("%v %v>%v", it["classification"], it["expected_quantity"], it["counted_quantity"])
	}
	wantResult := map[string]string{
		w.stay: "found 1>1", w.moved: "found 1>1", w.gone: "missing 1>0", w.extra: "unexpected 0>1",
		w.cable + "@" + tr.bin1: "found 5>5", w.cable + "@" + tr.bin2: "found 3>1", w.plug + "@" + tr.bin2: "unexpected 0>4",
	}
	if !maps.Equal(result, wantResult) {
		t.Errorf("items after the completion: %v, want %v", result, wantResult)
	}
	if after := w.a.pages(orgCtx, w.path+"/differences", 200); !slices.Equal(keys(after), want) {
		t.Errorf("differences after the completion: %v, want %v", keys(after), want)
	}
	if code, body = w.scan(assetScan(w.gone, tr.bin1)); code != http.StatusConflict {
		t.Errorf("scan after the completion: %d %v, want 409", code, body)
	}
	if code, body = w.a.do(orgCtx, http.MethodPost, w.path+"/complete", ""); code != http.StatusConflict {
		t.Errorf("second completion: %d %v, want 409", code, body)
	}
}

// reversed returns a reversed copy of ids.
func reversed(ids []string) []string {
	out := slices.Clone(ids)
	slices.Reverse(out)
	return out
}

// TestStockScanMigration replays migration 000265 (WP-287) in a rolled-back
// transaction: down restores the result values of earlier releases, rows
// recorded with them then get unexpected (surplus, wrong_location), found
// (damaged) or keep missing and found by up, the original value in
// legacy_scan_result, client and site of the scanned asset; a scan of a
// stocktake with area takes its client and site. A second down restores the
// original values and turns an unexpected row without original value into
// surplus.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStockScanMigration(t *testing.T) {
	f := scopetest.Seed(t, "33")
	ctx := context.Background()
	user := f.AppUser(t, f.OrgA, "mig287-counter")
	own := f.Asset(t, f.OrgA, f.Client1, "mig287-own")
	foreign := f.Asset(t, f.OrgA, f.Client2, "mig287-foreign")
	legacy := legacyStocktake(t, f, f.OrgA, "mig287 legacy")
	tr := seedAreaTree(t, f, f.Client2, "mig287")
	area := newStocktakeAPI(t, f, user).create(f.OrgCtx(f.OrgA), "mig287 area", tr.room)["id"].(string)
	up, err := os.ReadFile("../../migrations/000265_stock_scan_classification.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../migrations/000265_stock_scan_classification.down.sql")
	if err != nil {
		t.Fatal(err)
	}

	tx, err := f.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, string(down)); err != nil {
		t.Fatalf("down: %v", err)
	}
	ids := map[string]string{}
	for _, c := range []struct{ name, stocktakeID, assetID, result string }{
		{"surplus", legacy, own, "surplus"},
		{"wrong_location", legacy, foreign, "wrong_location"},
		{"damaged", legacy, own, "damaged"},
		{"missing", legacy, own, "missing"},
		{"found", area, own, "found"},
	} {
		var id string
		if err = tx.QueryRow(ctx, `INSERT INTO stock_scan (organization_id, stocktake_id, asset_id, scanned_by, scan_result)
			VALUES ($1, $2, $3, $4, $5) RETURNING id::text`, f.OrgA, c.stocktakeID, c.assetID, user, c.result).Scan(&id); err != nil {
			t.Fatalf("insert %s scan before up: %v", c.name, err)
		}
		ids[c.name] = id
	}
	if _, err = tx.Exec(ctx, string(up)); err != nil {
		t.Fatalf("up: %v", err)
	}
	row := func(id string) string {
		t.Helper()
		var s string
		if rowErr := tx.QueryRow(ctx, `SELECT scan_result || '/' || COALESCE(legacy_scan_result, '-') || '/' ||
			COALESCE((client_id = $2)::text, 'null') || '/' || COALESCE((client_id = $3)::text, 'null')
			FROM stock_scan WHERE id = $1`, id, f.Client1, f.Client2).Scan(&s); rowErr != nil {
			t.Fatalf("scan %s: %v", id, rowErr)
		}
		return s
	}
	for name, want := range map[string]string{
		"surplus":        "unexpected/surplus/true/false",
		"wrong_location": "unexpected/wrong_location/false/true",
		"damaged":        "found/damaged/true/false",
		"missing":        "missing/missing/true/false",
		"found":          "found/found/false/true",
	} {
		if got := row(ids[name]); got != want {
			t.Errorf("%s scan after up: %s, want %s (result/legacy/client 1/client 2)", name, got, want)
		}
	}
	var areaSite bool
	if err = tx.QueryRow(ctx, `SELECT site_id = $2 FROM stock_scan WHERE id = $1`, ids["found"], tr.site).Scan(&areaSite); err != nil || !areaSite {
		t.Errorf("scan of the area stocktake after up: site of the area %v %v, want true", areaSite, err)
	}
	var fresh string
	if err = tx.QueryRow(ctx, `INSERT INTO stock_scan (organization_id, stocktake_id, asset_id, scanned_by, scan_result)
		VALUES ($1, $2, $3, $4, 'unexpected') RETURNING id::text`, f.OrgA, area, foreign, user).Scan(&fresh); err != nil {
		t.Fatalf("unexpected scan after up: %v", err)
	}
	if _, err = tx.Exec(ctx, string(down)); err != nil {
		t.Fatalf("second down: %v", err)
	}
	for name, want := range map[string]string{"surplus": "surplus", "wrong_location": "wrong_location", "damaged": "damaged",
		"missing": "missing", "found": "found", "fresh": "surplus"} {
		id := ids[name]
		if name == "fresh" {
			id = fresh
		}
		var got string
		if err = tx.QueryRow(ctx, `SELECT scan_result FROM stock_scan WHERE id = $1`, id).Scan(&got); err != nil || got != want {
			t.Errorf("%s scan after the second down: %s %v, want %s", name, got, err, want)
		}
	}
	var policies string
	if err = tx.QueryRow(ctx, `SELECT string_agg(policyname, ',' ORDER BY policyname) FROM pg_policies WHERE tablename = 'stock_scan'`).Scan(&policies); err != nil ||
		policies != "stock_scan_tenant_isolation" {
		t.Errorf("policies of stock_scan after the second down: %s %v, want stock_scan_tenant_isolation", policies, err)
	}
}
