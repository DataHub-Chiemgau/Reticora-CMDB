package stocktake_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/stocktake"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// parentOf returns the parent node of the location id.
func parentOf(t *testing.T, f *scopetest.Fixture, id string) string {
	t.Helper()
	var parent string
	if err := f.Admin.QueryRow(context.Background(), `SELECT parent_id::text FROM location WHERE id = $1`, id).Scan(&parent); err != nil {
		t.Fatalf("parent of %s: %v", id, err)
	}
	return parent
}

// TestStocktakeAreaEveryNodeKind covers INV-02 [P2] (WP-288): a stocktake
// on a node of each of the eight kinds of the location tree is created and
// started; the frozen snapshot holds exactly the assets and the stock of
// the subtree (location.path <@): a bin only its own, a zone, shelf and
// warehouse every bin below it, a room and its building the assets of the
// racks as well. The asset in a room of another site is in none of them.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStocktakeAreaEveryNodeKind(t *testing.T) {
	f := scopetest.Seed(t, "33")
	orgCtx := f.OrgCtx(f.OrgA)
	user := f.AppUser(t, f.OrgA, "kinds-counter")
	a := newStocktakeAPI(t, f, user)
	tr := seedAreaTree(t, f, f.Client1, "n288")
	building := parentOf(t, f, tr.room)
	shelf := parentOf(t, f, tr.bin1)
	warehouse := parentOf(t, f, tr.zone)

	inRoom := f.Asset(t, f.OrgA, f.Client1, "n288-room")
	inRack := f.Asset(t, f.OrgA, f.Client1, "n288-rack")
	inBin := f.Asset(t, f.OrgA, f.Client1, "n288-bin")
	elsewhere := f.Asset(t, f.OrgA, f.Client1, "n288-elsewhere")
	placeAsset(t, f, inRoom, tr.room)
	placeAsset(t, f, inRack, tr.rack)
	placeAsset(t, f, inBin, tr.bin1)
	placeAsset(t, f, elsewhere, tr.otherRoom)
	cable := stockConsumable(t, f, f.Client1, "n288-cable", map[string]float64{tr.bin1: 4, tr.bin2: 2})

	asset := func(id, loc string) string { return "asset:" + id + "@" + loc + "=1" }
	stock := func(loc string, qty int) string { return fmt.Sprintf("consumable:%s@%s=%d", cable, loc, qty) }
	storage := []string{asset(inBin, tr.bin1), stock(tr.bin1, 4), stock(tr.bin2, 2)}
	cases := []struct {
		kind, node string
		want       []string
	}{
		{"site", tr.site, append([]string{asset(inRoom, tr.room), asset(inRack, tr.rack)}, storage...)},
		{"building", building, []string{asset(inRoom, tr.room), asset(inRack, tr.rack)}},
		{"room", tr.room, []string{asset(inRoom, tr.room), asset(inRack, tr.rack)}},
		{"rack", tr.rack, []string{asset(inRack, tr.rack)}},
		{"warehouse", warehouse, storage},
		{"zone", tr.zone, storage},
		{"shelf", shelf, storage},
		{"bin", tr.bin1, []string{asset(inBin, tr.bin1), stock(tr.bin1, 4)}},
	}
	for _, c := range cases {
		var kind string
		if err := f.Admin.QueryRow(context.Background(), `SELECT kind FROM location WHERE id = $1`, c.node).Scan(&kind); err != nil || kind != c.kind {
			t.Fatalf("node of kind %s: kind %q %v", c.kind, kind, err)
		}
		st := a.create(orgCtx, "kind "+c.kind, c.node)
		id, _ := st["id"].(string)
		if st["location_id"] != c.node || st["client_id"] != f.Client1 || st["site_id"] != tr.site {
			t.Errorf("%s: created %v, want the node with client 1 and its site", c.kind, st)
		}
		code, body := a.do(orgCtx, http.MethodPost, "/api/v1/stocktakes/"+id+"/start", "")
		if code != http.StatusOK || body["status"] != stocktake.StatusInProgress || body["total_expected"] != float64(len(c.want)) {
			t.Errorf("%s: start %d %v, want 200 in_progress with total_expected %d", c.kind, code, body, len(c.want))
			continue
		}
		want := slices.Clone(c.want)
		slices.Sort(want)
		if got := itemKeys(a.items(orgCtx, id, 200)); !slices.Equal(got, want) {
			t.Errorf("%s: items %v, want %v", c.kind, got, want)
		}
	}
}

// problemShape is the part of a problem body that must not tell a foreign
// node from an unknown one.
func problemShape(code int, body map[string]any) string {
	return fmt.Sprintf("%d %v %v %v %v %v", code, body["type"], body["status"], body["title"], body["field"], body["detail"])
}

// TestStocktakeAreaForeignNodeLikeUnknown covers WP-288: a location_id of
// another organization, and one of client 2 for a principal restricted to
// client 1, answer exactly like an unknown id (status, problem type, title,
// detail and the field location_id), and no stocktake is created.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStocktakeAreaForeignNodeLikeUnknown(t *testing.T) {
	f := scopetest.Seed(t, "33")
	bg := context.Background()
	user := f.AppUser(t, f.OrgA, "foreign-counter")
	a := newStocktakeAPI(t, f, user)
	foreign := seedAreaTree(t, f, f.Client2, "f288-c2")
	clientB := f.ID()
	var nodeB string
	if _, err := f.Admin.Exec(bg, `INSERT INTO client (id, organization_id, name, slug) VALUES ($1, $2, 'Client B', 'scopetest-33-f288-b')`, clientB, f.OrgB); err != nil {
		t.Fatal(err)
	}
	if err := f.Admin.QueryRow(bg, `INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, 'f288-b') RETURNING id::text`, f.OrgB, clientB).Scan(&nodeB); err != nil {
		t.Fatal(err)
	}
	create := func(ctx context.Context, node string) string {
		code, body := a.do(ctx, http.MethodPost, "/api/v1/stocktakes", `{"title":"f288","location_id":"`+node+`"}`)
		if code != http.StatusUnprocessableEntity || fieldOf(body) != "location_id" {
			t.Errorf("create on %s: %d %v, want 422 on location_id", node, code, body)
		}
		return problemShape(code, body)
	}
	unknownOrg := create(f.OrgCtx(f.OrgA), f.ID())
	unknownClient := create(f.ClientCtx(f.Client1), f.ID())
	if got := create(f.OrgCtx(f.OrgA), nodeB); got != unknownOrg {
		t.Errorf("node of organization B: %s, want the answer to an unknown id %s", got, unknownOrg)
	}
	if got := create(f.ClientCtx(f.Client1), foreign.bin1); got != unknownClient {
		t.Errorf("client-2 bin for a client-1 principal: %s, want the answer to an unknown id %s", got, unknownClient)
	}
	var n int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM stocktake WHERE organization_id IN ($1, $2) AND title = 'f288'`, f.OrgA, f.OrgB).Scan(&n); err != nil || n != 0 {
		t.Errorf("stocktakes after the refused creates: %d %v, want 0", n, err)
	}
}

// serveAs runs the request through h with an org-wide tenant scope of
// orgID and decodes the body.
func serveAs(t *testing.T, f *scopetest.Fixture, h http.Handler, orgID, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(tenant.WithTenant(f.OrgCtx(orgID), tenant.TenantInfo{OrganizationID: orgID, UserID: f.User})))
	out := map[string]any{}
	// The router's own 404 (module off) is plain text.
	if rec.Body.Len() > 0 && strings.Contains(rec.Header().Get("Content-Type"), "json") {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s %s: body %q: %v", method, path, rec.Body.String(), err)
		}
	}
	return rec.Code, out
}

// TestStocktakeStandardEntitlement covers the org gate of the stocktake
// (INV-02, ENT-06, E-44, WP-288) with the PostgreSQL entitlement store:
// an organization in plan essential gets 403 feature-not-entitled on GET
// and POST /api/v1/stocktakes, one in plan standard, pro or enterprise
// 2xx. With the module switched off (no stocktake routes behind the
// middleware) an entitled organization gets 404, one that is not entitled
// still 403, because the gate runs before the router.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStocktakeStandardEntitlement(t *testing.T) {
	f := scopetest.Seed(t, "33")
	bg := context.Background()
	mux := chi.NewRouter()
	stocktake.NewHandler(stocktake.NewPGRepository(f.App)).RegisterRoutes(mux)
	// provision replaces the entitlement rows of the organizations by
	// those of their plans and returns a fresh service (empty cache).
	provision := func(plans map[string]entitlement.Plan) *entitlement.Service {
		t.Helper()
		for orgID := range plans {
			if _, err := f.Admin.Exec(bg, `DELETE FROM entitlement WHERE organization_id = $1`, orgID); err != nil {
				t.Fatalf("reset entitlements of %s: %v", orgID, err)
			}
		}
		svc := entitlement.NewService(entitlement.NewPGRepository(f.App), entitlement.Options{Enforce: true})
		if _, err := svc.ProvisionOrganizations(bg, plans); err != nil {
			t.Fatalf("provision %v: %v", plans, err)
		}
		return svc
	}
	notEntitled := func(what string, code int, body map[string]any) {
		t.Helper()
		if code != http.StatusForbidden || body["type"] != api.ProblemFeatureNotEntitled {
			t.Errorf("%s: %d %v, want 403 %s", what, code, body, api.ProblemFeatureNotEntitled)
		}
	}

	for _, plan := range []entitlement.Plan{entitlement.PlanStandard, entitlement.PlanPro, entitlement.PlanEnterprise} {
		svc := provision(map[string]entitlement.Plan{f.OrgA: plan, f.OrgB: entitlement.PlanEssential})
		gated := svc.Middleware(mux)
		if code, body := serveAs(t, f, gated, f.OrgA, http.MethodGet, "/api/v1/stocktakes", ""); code != http.StatusOK {
			t.Errorf("%s: GET %d %v, want 200", plan, code, body)
		}
		if code, body := serveAs(t, f, gated, f.OrgA, http.MethodPost, "/api/v1/stocktakes", `{"title":"e288-`+string(plan)+`"}`); code != http.StatusCreated {
			t.Errorf("%s: POST %d %v, want 201", plan, code, body)
		}
		code, body := serveAs(t, f, gated, f.OrgB, http.MethodGet, "/api/v1/stocktakes", "")
		notEntitled("essential GET", code, body)
		code, body = serveAs(t, f, gated, f.OrgB, http.MethodPost, "/api/v1/stocktakes", `{"title":"e288-essential"}`)
		notEntitled("essential POST", code, body)
	}
	var n int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM stocktake WHERE organization_id = $1 AND title = 'e288-essential'`, f.OrgB).Scan(&n); err != nil || n != 0 {
		t.Errorf("stocktakes of the essential organization: %d %v, want 0", n, err)
	}

	// Module off: the router has no stocktake routes.
	svc := provision(map[string]entitlement.Plan{f.OrgA: entitlement.PlanStandard, f.OrgB: entitlement.PlanEssential})
	off := chi.NewRouter()
	locations.NewHandler(locations.NewPGRepository(f.App)).RegisterRoutes(off)
	gatedOff := svc.Middleware(off)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if code, body := serveAs(t, f, gatedOff, f.OrgA, method, "/api/v1/stocktakes", `{"title":"e288-off"}`); code != http.StatusNotFound {
			t.Errorf("module off, entitled %s: %d %v, want 404", method, code, body)
		}
		code, body := serveAs(t, f, gatedOff, f.OrgB, method, "/api/v1/stocktakes", `{"title":"e288-off"}`)
		notEntitled("module off, not entitled "+method, code, body)
	}
}

// TestStocktakeBinAreaBlocksDelete covers LOC-11 for a bin as area
// (WP-288, entries of WP-848): deleting a bin a stocktake counts answers
// 409 with stocktake in dependencies; the bin stays.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStocktakeBinAreaBlocksDelete(t *testing.T) {
	f := scopetest.Seed(t, "33")
	orgCtx := f.OrgCtx(f.OrgA)
	user := f.AppUser(t, f.OrgA, "bin-counter")
	a := newStocktakeAPI(t, f, user)
	tr := seedAreaTree(t, f, f.Client1, "b288")
	a.create(orgCtx, "bin b288", tr.bin2)
	code, body := a.do(orgCtx, http.MethodDelete, "/api/v1/locations/"+tr.bin2, "")
	deps, _ := body["dependencies"].([]any)
	if code != http.StatusConflict || !slices.Contains(deps, any(locations.DependencyStocktake)) {
		t.Errorf("delete of a bin with a stocktake: %d %v, want 409 with %s", code, body, locations.DependencyStocktake)
	}
	var n int
	if err := f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM location WHERE id = $1`, tr.bin2).Scan(&n); err != nil || n != 1 {
		t.Errorf("bin after the refused delete: %d %v, want it kept", n, err)
	}
}
