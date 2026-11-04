package order_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Retest (WP-847, STK-05):
//
//	cd backend && go test -count=1 -p 1 -run 'TestOrderTargetBin' ./internal/order/

// binTree creates site, warehouse, zone, shelf and bin of clientID in orgID
// through the location repository and returns shelf and bin.
func binTree(t *testing.T, f *scopetest.Fixture, orgID, clientID, name string) (shelf, bin string) {
	t.Helper()
	repo := locations.NewPGRepository(f.App)
	ctx := f.OrgCtx(orgID)
	parent := ""
	ids := map[locations.Kind]string{}
	for _, kind := range []locations.Kind{locations.KindSite, locations.KindWarehouse, locations.KindZone, locations.KindShelf, locations.KindBin} {
		req := locations.CreateRequest{Kind: kind, ParentID: parent, Name: name + "-" + string(kind)}
		if kind == locations.KindSite {
			req.ClientID = clientID
		}
		l, err := repo.Create(ctx, orgID, req)
		if err != nil {
			t.Fatalf("create %s %s: %v", kind, name, err)
		}
		parent, ids[kind] = l.ID, l.ID
	}
	return ids[locations.KindShelf], ids[locations.KindBin]
}

// TestOrderTargetBinMatchesClient covers the target bin of an internal order
// (STK-05, WP-847) through the order routes: a target location that is no
// bin is 422; a bin of another client than the order, a bin outside the
// principal's scope, a bin of another organization and an unknown id are
// 422 on target_location_id with the same answer; a fitting bin is stored on
// create (201) and update (200); without a client every bin in the scope
// fits; a new client of the order is checked against its bin; a change
// outside draft is 409. Deleting a bin an order names is refused with 409
// and the kind internal_order (LOC-11), also when the order is outside the
// deleting principal's scope (foreign key, locations.DeleteError).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestOrderTargetBinMatchesClient(t *testing.T) {
	w := newOrderWorld(t)
	f := w.f
	bg := context.Background()
	shelf1, bin1 := binTree(t, f, f.OrgA, f.Client1, "tb1")
	_, bin2 := binTree(t, f, f.OrgA, f.Client2, "tb2")
	_, spare1 := binTree(t, f, f.OrgA, f.Client1, "tb3")
	// A bin of organization B (its own client).
	clientB := f.ID()
	if _, err := f.Admin.Exec(bg, `INSERT INTO client (id, organization_id, name, slug) VALUES ($1, $2, 'Client B', $3)`,
		clientB, f.OrgB, "scopetest-31-target-cb"); err != nil {
		t.Fatal(err)
	}
	_, binB := binTree(t, f, f.OrgB, clientB, "tbB")

	client1 := `"client_id":"` + f.Client1 + `"`
	targetBody := func(prefix, bin string) string { return `{` + prefix + `"target_location_id":"` + bin + `"}` }
	createBody := func(bin string) string { return targetBody(`"title":"target",`+client1+`,`, bin) }
	refused := func(rec *httptest.ResponseRecorder) string {
		t.Helper()
		detail, field := problem(t, rec)
		if field != "target_location_id" {
			t.Errorf("422 on field %q (%s), want target_location_id", field, detail)
		}
		return detail
	}
	post := func(body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		return w.do(t, call{user: w.requester, method: http.MethodPost, path: "/api/v1/orders", body: body}, want)
	}
	patch := func(id, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		return w.do(t, call{user: w.requester, method: http.MethodPatch, path: "/api/v1/orders/" + id, body: body}, want)
	}
	storedTarget := func(id string) string {
		t.Helper()
		var target string
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(target_location_id::text, '') FROM internal_order WHERE id = $1`, id).Scan(&target); err != nil {
			t.Fatal(err)
		}
		return target
	}

	// Create: a shelf is no bin; the misfits share one answer.
	if detail := refused(post(createBody(shelf1), http.StatusUnprocessableEntity)); detail == "" {
		t.Error("no detail for a target location that is no bin")
	}
	unknown := refused(post(createBody(f.ID()), http.StatusUnprocessableEntity))
	for name, bin := range map[string]string{"bin of client 2": bin2, "bin of organization B": binB, "not a uuid": "nope"} {
		if detail := refused(post(createBody(bin), http.StatusUnprocessableEntity)); detail != unknown {
			t.Errorf("create with %s: %q, want the answer of an unknown id %q", name, detail, unknown)
		}
	}
	created := decodeOrder(t, post(createBody(bin1), http.StatusCreated))
	if created.TargetLocationID != bin1 || storedTarget(created.ID) != bin1 {
		t.Fatalf("created order target %q (stored %q), want %s", created.TargetLocationID, storedTarget(created.ID), bin1)
	}
	if got := decodeOrder(t, w.do(t, call{user: w.requester, method: http.MethodGet, path: "/api/v1/orders/" + created.ID}, http.StatusOK)); got.TargetLocationID != bin1 {
		t.Errorf("read order target %q, want %s", got.TargetLocationID, bin1)
	}

	// A principal restricted to client 2 sees no client-1 bin: the same
	// answer as an unknown id for its own order.
	scoped2 := w.do(t, call{ctx: f.ClientCtx(f.Client2), user: w.requester, method: http.MethodPost, path: "/api/v1/orders",
		body: `{"title":"c2","client_id":"` + f.Client2 + `","target_location_id":"` + bin1 + `"}`}, http.StatusUnprocessableEntity)
	if detail := refused(scoped2); detail != unknown {
		t.Errorf("client-2 principal with a client-1 bin: %q, want %q", detail, unknown)
	}

	// Without a client every bin in the scope fits.
	orgWide := decodeOrder(t, post(`{"title":"org-wide","target_location_id":"`+bin2+`"}`, http.StatusCreated))
	if orgWide.TargetLocationID != bin2 {
		t.Fatalf("org-wide order target %q, want %s", orgWide.TargetLocationID, bin2)
	}

	// Update in draft: misfits keep the stored bin, a fitting bin is stored.
	if detail := refused(patch(created.ID, targetBody("", bin2), http.StatusUnprocessableEntity)); detail != unknown {
		t.Errorf("update with a client-2 bin: %q, want %q", detail, unknown)
	}
	refused(patch(created.ID, targetBody("", shelf1), http.StatusUnprocessableEntity))
	if storedTarget(created.ID) != bin1 {
		t.Errorf("refused updates changed the target to %s", storedTarget(created.ID))
	}
	if got := decodeOrder(t, patch(created.ID, targetBody("", spare1), http.StatusOK)); got.TargetLocationID != spare1 {
		t.Errorf("updated target %q, want %s", got.TargetLocationID, spare1)
	}
	// A new client must fit the bin the order keeps.
	refused(patch(orgWide.ID, `{`+client1+`}`, http.StatusUnprocessableEntity))
	if got := decodeOrder(t, patch(orgWide.ID, targetBody(client1+`,`, bin1), http.StatusOK)); got.ClientID != f.Client1 || got.TargetLocationID != bin1 {
		t.Errorf("client and target together: %+v", got)
	}
	if got := decodeOrder(t, patch(orgWide.ID, targetBody("", ""), http.StatusOK)); got.TargetLocationID != "" || storedTarget(orgWide.ID) != "" {
		t.Errorf("empty target_location_id kept %q", got.TargetLocationID)
	}
	if got := decodeOrder(t, patch(orgWide.ID, targetBody("", bin1), http.StatusOK)); got.TargetLocationID != bin1 {
		t.Errorf("target set again: %q, want %s", got.TargetLocationID, bin1)
	}

	// Outside draft the target is frozen.
	w.transition(t, created.ID, "submit", w.requester, http.StatusOK)
	patch(created.ID, targetBody("", bin1), http.StatusConflict)
	if storedTarget(created.ID) != spare1 {
		t.Errorf("submitted order target %s, want %s", storedTarget(created.ID), spare1)
	}

	// LOC-11: the bin of an order is not deleted.
	mux := chi.NewRouter()
	locations.NewHandler(locations.NewPGRepository(f.App)).RegisterRoutes(mux)
	deleteBin := func(ctx context.Context, bin string) (int, []string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/locations/"+bin, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req.WithContext(tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: f.OrgA})))
		var body struct {
			Dependencies []string `json:"dependencies"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body.Dependencies
	}
	if code, deps := deleteBin(f.OrgCtx(f.OrgA), spare1); code != http.StatusConflict || !slices.Equal(deps, []string{locations.DependencyInternalOrder}) {
		t.Errorf("delete of the bin of an order: %d %v, want 409 [internal_order]", code, deps)
	}
	// An order the deleting principal cannot see keeps its bin through the
	// foreign key (the repository would refuse this pair; the maintenance
	// pool writes it to reach the backstop).
	if _, err := f.Admin.Exec(bg, `INSERT INTO internal_order (organization_id, client_id, order_number, title, currency, target_location_id)
		VALUES ($1, $2, 'ORD-T847', 'hidden', 'EUR', $3)`, f.OrgA, f.Client2, bin1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `UPDATE internal_order SET target_location_id = NULL WHERE id = $1`, orgWide.ID); err != nil {
		t.Fatal(err)
	}
	if code, deps := deleteBin(f.ClientCtx(f.Client1), bin1); code != http.StatusConflict || !slices.Equal(deps, []string{locations.DependencyInternalOrder}) {
		t.Errorf("delete of a bin a hidden order names: %d %v, want 409 [internal_order]", code, deps)
	}
	var left int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM location WHERE id IN ($1, $2)`, bin1, spare1).Scan(&left); err != nil || left != 2 {
		t.Errorf("bins after refused deletes: %d err=%v, want 2", left, err)
	}
	// Without an order the bin goes.
	if _, err := f.Admin.Exec(bg, `UPDATE internal_order SET target_location_id = NULL WHERE organization_id = $1`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	if code, deps := deleteBin(f.OrgCtx(f.OrgA), bin1); code != http.StatusNoContent {
		t.Errorf("delete of a free bin: %d %v, want 204", code, deps)
	}
}
