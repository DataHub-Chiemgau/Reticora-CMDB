package consumable_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Retest (WP-756, STK-01, STK-03, API-06, AST-07):
//
//	cd backend && go test -count=1 -p 1 -run 'TestStockBooking' ./internal/consumable/

// bookingAPI serves the movement routes behind the route authorization of
// the server (server.AuthorizeRoute) for a principal of organization A
// with the given permissions and an org-wide scope.
type bookingAPI struct {
	t      *testing.T
	f      *scopetest.Fixture
	router chi.Router
}

func newBookingAPI(t *testing.T, f *scopetest.Fixture, repo movement.Repository) *bookingAPI {
	r := chi.NewRouter()
	movement.NewHandler(movement.NewService(repo)).RegisterRoutes(r)
	return &bookingAPI{t: t, f: f, router: r}
}

// do sends the request of pattern (the route of path) as a principal
// holding perms and returns status and decoded body.
func (a *bookingAPI) do(perms []identity.Permission, method, pattern, path string, body any) (int, map[string]any) {
	a.t.Helper()
	raw := ""
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			a.t.Fatal(err)
		}
		raw = string(b)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	scope := database.OrgWideScope(a.f.OrgA, a.f.User)
	p := identity.Principal{Subject: a.f.User, OrganizationID: a.f.OrgA, Type: identity.PrincipalTypeUser,
		Scope:       &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}},
		Permissions: perms, Credential: "wp756"}
	ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), perms)
	ctx = tenant.WithTenant(database.ContextWithTenantScope(ctx, &scope), tenant.TenantInfo{OrganizationID: a.f.OrgA, UserID: a.f.User})
	rec := httptest.NewRecorder()
	server.AuthorizeRoute(method, pattern)(a.router).ServeHTTP(rec, req.WithContext(ctx))
	out := map[string]any{}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			a.t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
		}
	}
	return rec.Code, out
}

// The permissions of the cases: stock:write books quantities, asset:move
// moves assets.
var (
	stockWriter = []identity.Permission{identity.PermStockWrite, identity.PermStockRead, identity.PermAssetRead}
	assetMover  = []identity.Permission{identity.PermAssetMove, identity.PermAssetRead}
	bothWriters = []identity.Permission{identity.PermStockWrite, identity.PermStockRead, identity.PermAssetRead, identity.PermAssetMove}
)

const (
	movementsRoute = "/api/v1/stock-movements"
	aliasRoute     = "/api/v1/consumables/{id}/movements"
)

// post books body through POST /api/v1/stock-movements as a stock writer.
func (a *bookingAPI) post(body map[string]any) (int, map[string]any) {
	a.t.Helper()
	return a.do(stockWriter, http.MethodPost, movementsRoute, movementsRoute, body)
}

// ledgerCount returns the number of ledger rows of the consumable.
func ledgerCount(t *testing.T, f *scopetest.Fixture, cons string) int {
	t.Helper()
	var n int
	if err := f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM stock_movement WHERE consumable_id = $1`, cons).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestStockBookingThroughMovementService covers the quantity bookings over
// the movement service (STK-01, STK-03, API-06, AST-07, WP-756) against
// PostgreSQL: POST /api/v1/stock-movements books receipt, retrieval,
// correction (both ways) and transfer of a consumable on its bins and
// writes one ledger row (item_kind consumable) and one stock.movement each;
// other types, a location that is no bin, a location against the rules of
// the type and an asset with another type than transfer are 422; a
// shortfall is 409 insufficient-stock and leaves neither ledger row nor
// stock change; an asset transfer sets asset.location_id and needs
// asset:move besides stock:write (403 without); the route needs stock:write
// (asset:move alone 403); the deprecated GET and the POST of
// /consumables/{id}/movements (in/out, with and without location_id); the
// entitlement stock (403); opposite transfers in parallel without deadlock,
// parallel retrievals never below zero and the sum over the bins equal to
// the sum of the bookings.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStockBookingThroughMovementService(t *testing.T) {
	f := scopetest.Seed(t, "30")
	bg := context.Background()
	a := newBookingAPI(t, f, movement.NewPGRepository(f.App))
	b1 := binTree(t, f, f.OrgA, f.Client1, "wp756-a")
	b2 := binTree(t, f, f.OrgA, f.Client1, "wp756-b")
	cons := newConsumable(t, f, f.Client1, "wp756-toner")

	t.Run("four types", func(t *testing.T) {
		for _, step := range []struct {
			body   map[string]any
			b1, b2 float64
		}{
			{map[string]any{"movement_type": "receipt", "to_location_id": b1.bin, "quantity": 10}, 10, -1},
			{map[string]any{"movement_type": "transfer", "from_location_id": b1.bin, "to_location_id": b2.bin, "quantity": 4}, 6, 4},
			{map[string]any{"movement_type": "retrieval", "from_location_id": b2.bin, "quantity": 1}, 6, 3},
			{map[string]any{"movement_type": "correction", "to_location_id": b2.bin, "quantity": 2}, 6, 5},
			{map[string]any{"movement_type": "correction", "from_location_id": b1.bin, "quantity": 1}, 5, 5},
		} {
			step.body["consumable_id"] = cons
			code, body := a.post(step.body)
			if code != http.StatusCreated || body["item_kind"] != "consumable" || body["consumable_id"] != cons || body["client_id"] != f.Client1 {
				t.Fatalf("%v: %d %v", step.body, code, body)
			}
			if got1, got2 := onHand(t, f, cons, b1.bin), onHand(t, f, cons, b2.bin); got1 != step.b1 || got2 != step.b2 {
				t.Errorf("%v: on_hand %v/%v, want %v/%v", step.body["movement_type"], got1, got2, step.b1, step.b2)
			}
			var events int
			if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM event_outbox WHERE type = 'stock.movement' AND entity_id = $1
				AND data ->> 'id' = $2`, cons, body["id"]).Scan(&events); err != nil || events != 1 {
				t.Errorf("%v: %d stock.movement events (%v), want 1", step.body["movement_type"], events, err)
			}
		}
		if n := ledgerCount(t, f, cons); n != 5 {
			t.Errorf("%d ledger rows, want 5", n)
		}
	})

	t.Run("refused bookings", func(t *testing.T) {
		before := ledgerCount(t, f, cons)
		asset := f.Asset(t, f.OrgA, f.Client1, "wp756-asset-r")
		for name, tc := range map[string]struct {
			body  map[string]any
			field string
		}{
			"deployment":          {map[string]any{"movement_type": "deployment", "consumable_id": cons, "from_location_id": b1.bin, "quantity": 1}, "movement_type"},
			"repair":              {map[string]any{"movement_type": "repair", "consumable_id": cons, "to_location_id": b1.bin, "quantity": 1}, "movement_type"},
			"shelf":               {map[string]any{"movement_type": "receipt", "consumable_id": cons, "to_location_id": b1.shelf, "quantity": 1}, "to_location_id"},
			"receipt with source": {map[string]any{"movement_type": "receipt", "consumable_id": cons, "from_location_id": b1.bin, "to_location_id": b2.bin, "quantity": 1}, "to_location_id"},
			"retrieval no source": {map[string]any{"movement_type": "retrieval", "consumable_id": cons, "to_location_id": b1.bin, "quantity": 1}, "from_location_id"},
			"correction both":     {map[string]any{"movement_type": "correction", "consumable_id": cons, "from_location_id": b1.bin, "to_location_id": b2.bin, "quantity": 1}, "to_location_id"},
			"transfer same bin":   {map[string]any{"movement_type": "transfer", "consumable_id": cons, "from_location_id": b1.bin, "to_location_id": b1.bin, "quantity": 1}, "to_location_id"},
			"no quantity":         {map[string]any{"movement_type": "receipt", "consumable_id": cons, "to_location_id": b1.bin}, "quantity"},
			"zero quantity":       {map[string]any{"movement_type": "receipt", "consumable_id": cons, "to_location_id": b1.bin, "quantity": 0}, "quantity"},
			"asset correction":    {map[string]any{"movement_type": "correction", "asset_id": asset, "to_location_id": b1.bin}, "movement_type"},
			"asset receipt":       {map[string]any{"movement_type": "receipt", "asset_id": asset, "to_location_id": b1.bin}, "movement_type"},
			"asset no target":     {map[string]any{"movement_type": "transfer", "asset_id": asset}, "to_location_id"},
			"two objects":         {map[string]any{"movement_type": "receipt", "asset_id": asset, "consumable_id": cons, "to_location_id": b1.bin, "quantity": 1}, "asset_id"},
		} {
			code, body := a.do(bothWriters, http.MethodPost, movementsRoute, movementsRoute, tc.body)
			if code != http.StatusUnprocessableEntity || body["field"] != tc.field {
				t.Errorf("%s: %d %v, want 422 on %s", name, code, body, tc.field)
			}
		}
		if code, body := a.post(map[string]any{"movement_type": "receipt", "consumable_id": f.ID(), "to_location_id": b1.bin, "quantity": 1}); code != http.StatusNotFound {
			t.Errorf("unknown consumable: %d %v, want 404", code, body)
		}
		if n := ledgerCount(t, f, cons); n != before {
			t.Errorf("ledger rows after the refusals: %d, want %d", n, before)
		}
	})

	t.Run("shortfall rolls back", func(t *testing.T) {
		before, b1Before, b2Before := ledgerCount(t, f, cons), onHand(t, f, cons, b1.bin), onHand(t, f, cons, b2.bin)
		for _, body := range []map[string]any{
			{"movement_type": "retrieval", "consumable_id": cons, "from_location_id": b1.bin, "quantity": 1000},
			{"movement_type": "transfer", "consumable_id": cons, "from_location_id": b2.bin, "to_location_id": b1.bin, "quantity": 1000},
			{"movement_type": "correction", "consumable_id": cons, "from_location_id": b2.bin, "quantity": 1000},
		} {
			code, resp := a.post(body)
			if code != http.StatusConflict || resp["type"] != movement.ProblemInsufficientStock {
				t.Errorf("%v: %d %v, want 409 %s", body["movement_type"], code, resp, movement.ProblemInsufficientStock)
			}
		}
		if n := ledgerCount(t, f, cons); n != before {
			t.Errorf("ledger rows after the shortfalls: %d, want %d", n, before)
		}
		if onHand(t, f, cons, b1.bin) != b1Before || onHand(t, f, cons, b2.bin) != b2Before {
			t.Error("a refused booking changed the stock")
		}
	})

	t.Run("asset transfer", func(t *testing.T) {
		asset := f.Asset(t, f.OrgA, f.Client1, "wp756-asset")
		room := f.Room(t, f.Client1, "wp756-room")
		body := map[string]any{"movement_type": "transfer", "asset_id": asset, "to_location_id": room}
		if code, resp := a.post(body); code != http.StatusForbidden {
			t.Errorf("asset transfer with stock:write only: %d %v, want 403", code, resp)
		}
		code, resp := a.do(bothWriters, http.MethodPost, movementsRoute, movementsRoute, body)
		if code != http.StatusCreated || resp["item_kind"] != "asset" || resp["quantity"] != nil {
			t.Fatalf("asset transfer: %d %v", code, resp)
		}
		var location string
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(location_id::text, '') FROM asset WHERE id = $1`, asset).Scan(&location); err != nil || location != room {
			t.Errorf("asset location %q (%v), want %s", location, err, room)
		}
	})

	t.Run("rights", func(t *testing.T) {
		if required, _ := server.PermissionForRoute(http.MethodPost, movementsRoute); required != identity.PermStockWrite {
			t.Errorf("POST %s requires %s, want stock:write", movementsRoute, required)
		}
		if required, _ := server.PermissionForRoute(http.MethodGet, movementsRoute); required != identity.PermAssetRead {
			t.Errorf("GET %s requires %s, want asset:read", movementsRoute, required)
		}
		body := map[string]any{"movement_type": "receipt", "consumable_id": cons, "to_location_id": b1.bin, "quantity": 1}
		before := ledgerCount(t, f, cons)
		if code, resp := a.do(assetMover, http.MethodPost, movementsRoute, movementsRoute, body); code != http.StatusForbidden {
			t.Errorf("receipt with asset:move only: %d %v, want 403", code, resp)
		}
		if n := ledgerCount(t, f, cons); n != before {
			t.Errorf("the refused receipt wrote %d ledger rows", n-before)
		}
		if code, resp := a.post(body); code != http.StatusCreated {
			t.Errorf("receipt with stock:write: %d %v, want 201", code, resp)
		}
	})

	t.Run("alias", func(t *testing.T) {
		path := "/api/v1/consumables/" + cons + "/movements"
		code, body := a.do(stockWriter, http.MethodPost, aliasRoute, path, map[string]any{"direction": "in", "quantity": 1})
		if code != http.StatusUnprocessableEntity || body["field"] != "location_id" {
			t.Errorf("in without location_id on two bins: %d %v, want 422 on location_id", code, body)
		}
		code, body = a.do(stockWriter, http.MethodPost, aliasRoute, path, map[string]any{"direction": "in", "quantity": 2, "location_id": b2.bin, "reference": "PO-756"})
		mv, _ := body["movement"].(map[string]any)
		if code != http.StatusCreated || mv["movement_type"] != "receipt" || mv["direction"] != "in" || mv["location_id"] != b2.bin || mv["reference"] != "PO-756" {
			t.Fatalf("in into a bin: %d %v", code, body)
		}
		code, body = a.do(stockWriter, http.MethodPost, aliasRoute, path, map[string]any{"direction": "out", "quantity": 1, "location_id": b2.bin})
		if mv, _ = body["movement"].(map[string]any); code != http.StatusCreated || mv["movement_type"] != "retrieval" || mv["direction"] != "out" {
			t.Fatalf("out of a bin: %d %v", code, body)
		}
		if code, body = a.do(stockWriter, http.MethodPost, aliasRoute, path, map[string]any{"direction": "in", "quantity": 1, "location_id": b2.shelf}); code != http.StatusUnprocessableEntity || body["field"] != "location_id" {
			t.Errorf("in into a shelf: %d %v, want 422 on location_id", code, body)
		}
		if code, body = a.do(stockWriter, http.MethodPost, aliasRoute, path, map[string]any{"direction": "out", "quantity": 1000, "location_id": b2.bin}); code != http.StatusConflict || body["type"] != movement.ProblemInsufficientStock {
			t.Errorf("out beyond the bin: %d %v, want 409 insufficient-stock", code, body)
		}
		single := newConsumable(t, f, f.Client1, "wp756-single")
		singlePath := "/api/v1/consumables/" + single + "/movements"
		if code, body = a.do(stockWriter, http.MethodPost, aliasRoute, singlePath, map[string]any{"direction": "in", "quantity": 3, "location_id": b1.bin}); code != http.StatusCreated {
			t.Fatalf("in into the first bin: %d %v", code, body)
		}
		code, body = a.do(stockWriter, http.MethodPost, aliasRoute, singlePath, map[string]any{"direction": "out", "quantity": 1})
		if mv, _ = body["movement"].(map[string]any); code != http.StatusCreated || mv["location_id"] != b1.bin || onHand(t, f, single, b1.bin) != 2 {
			t.Errorf("out of the only bin without location_id: %d %v", code, body)
		}

		// The deprecated GET returns the rows and page of the successor,
		// each with direction.
		code, legacy := a.do(stockWriter, http.MethodGet, aliasRoute, path+"?limit=3", nil)
		code2, successor := a.do(stockWriter, http.MethodGet, movementsRoute, movementsRoute+"?limit=3&consumable_id="+cons, nil)
		if code != http.StatusOK || code2 != http.StatusOK || legacy["total"] != successor["total"] || legacy["next_cursor"] != successor["next_cursor"] {
			t.Fatalf("GET alias %d %v, successor %d %v", code, legacy, code2, successor)
		}
		rows, _ := legacy["data"].([]any)
		want, _ := successor["data"].([]any)
		if len(rows) != len(want) || len(rows) != 3 {
			t.Fatalf("alias rows %d, successor rows %d, want 3", len(rows), len(want))
		}
		for i := range rows {
			got, exp := rows[i].(map[string]any), want[i].(map[string]any)
			if got["id"] != exp["id"] || got["movement_type"] != exp["movement_type"] || got["direction"] == nil {
				t.Errorf("alias row %d %v, successor %v", i, got, exp)
			}
		}
		// Newest first: the out of b2, then the in into b2.
		if first := rows[0].(map[string]any); first["direction"] != "out" || first["location_id"] != b2.bin {
			t.Errorf("newest alias row %v, want out of %s", first, b2.bin)
		}
	})

	t.Run("entitlement stock", func(t *testing.T) {
		gate := entitlement.NewService(nil, entitlement.Options{Enforce: true})
		h := gate.Middleware(a.router)
		raw, _ := json.Marshal(map[string]any{"movement_type": "receipt", "consumable_id": cons, "to_location_id": b1.bin, "quantity": 1})
		req := httptest.NewRequest(http.MethodPost, movementsRoute, strings.NewReader(string(raw)))
		req = req.WithContext(tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA}))
		before := ledgerCount(t, f, cons)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), api.ProblemFeatureNotEntitled) {
			t.Errorf("booking without the entitlement stock: %d %s, want 403 feature-not-entitled", rec.Code, rec.Body.String())
		}
		if n := ledgerCount(t, f, cons); n != before {
			t.Errorf("the refused booking wrote %d ledger rows", n-before)
		}
	})

	t.Run("concurrency", func(t *testing.T) {
		item := newConsumable(t, f, f.Client1, "wp756-parallel")
		for _, bin := range []string{b1.bin, b2.bin} {
			if code, body := a.post(map[string]any{"movement_type": "receipt", "consumable_id": item, "to_location_id": bin, "quantity": 20}); code != http.StatusCreated {
				t.Fatalf("receipt: %d %v", code, body)
			}
		}
		// Opposite transfers between the same bins: the rows are locked in
		// location order, so none deadlocks (40P01) and all pass.
		var wg sync.WaitGroup
		codes := make([]int, 16)
		for i := range codes {
			from, to := b1.bin, b2.bin
			if i%2 == 1 {
				from, to = to, from
			}
			wg.Add(1)
			go func(i int, from, to string) {
				defer wg.Done()
				codes[i], _ = a.post(map[string]any{"movement_type": "transfer", "consumable_id": item, "from_location_id": from, "to_location_id": to, "quantity": 1})
			}(i, from, to)
		}
		wg.Wait()
		for i, code := range codes {
			if code != http.StatusCreated {
				t.Errorf("transfer %d: %d, want 201", i, code)
			}
		}
		if got1, got2 := onHand(t, f, item, b1.bin), onHand(t, f, item, b2.bin); got1 != 20 || got2 != 20 {
			t.Errorf("after the opposite transfers %v/%v, want 20/20", got1, got2)
		}

		// Parallel retrievals of 3 from a bin of 20: six pass, the others
		// are 409, the bin never drops below zero.
		retrievals := make([]int, 10)
		for i := range retrievals {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				retrievals[i], _ = a.post(map[string]any{"movement_type": "retrieval", "consumable_id": item, "from_location_id": b1.bin, "quantity": 3})
			}(i)
		}
		wg.Wait()
		passed := 0
		for _, code := range retrievals {
			switch code {
			case http.StatusCreated:
				passed++
			case http.StatusConflict:
			default:
				t.Errorf("parallel retrieval: %d, want 201 or 409", code)
			}
		}
		if passed != 6 || onHand(t, f, item, b1.bin) != 2 {
			t.Errorf("%d retrievals passed, on_hand %v; want 6 and 2", passed, onHand(t, f, item, b1.bin))
		}

		// The sum over the bins is the sum of the bookings of the ledger.
		var ledger, stock float64
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(sum(CASE
				WHEN movement_type = 'receipt' THEN quantity
				WHEN movement_type = 'retrieval' THEN -quantity
				WHEN movement_type = 'correction' AND to_location_id IS NOT NULL THEN quantity
				WHEN movement_type = 'correction' THEN -quantity
				ELSE 0 END), 0)::float8 FROM stock_movement WHERE consumable_id = $1`, item).Scan(&ledger); err != nil {
			t.Fatal(err)
		}
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(sum(on_hand), 0)::float8 FROM stock_level WHERE consumable_id = $1`, item).Scan(&stock); err != nil {
			t.Fatal(err)
		}
		if ledger != stock || stock != 22 {
			t.Errorf("sum of the bookings %v, sum over the bins %v; want both 22", ledger, stock)
		}
	})
}

// TestStockBookingNoDB covers the No-DB profile of the bookings (WP-756):
// movement.MemoryRepository books a consumable on the stock per bin of the
// consumable.MemoryRepository it is wired to, refuses a shortfall with
// 409 insufficient-stock without a ledger row and serves the alias with
// and without location_id.
func TestStockBookingNoDB(t *testing.T) {
	ctx := context.Background()
	consumables := consumable.NewMemoryRepository()
	repo := movement.NewMemoryRepository().WithConsumables(consumables)
	c := &consumable.Consumable{OrganizationID: "org-1", Name: "nodb-toner"}
	if err := consumables.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	movement.NewHandler(movement.NewService(repo)).RegisterRoutes(r)
	do := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		req = req.WithContext(tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: "org-1", UserID: "user-1"}))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		out := map[string]any{}
		if rec.Body.Len() > 0 {
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
			}
		}
		return rec.Code, out
	}
	bin1, bin2 := "00000000-0000-4000-8000-0000000000b1", "00000000-0000-4000-8000-0000000000b2"
	stock := func() map[string]float64 {
		t.Helper()
		rows, _, err := consumables.ListStock(ctx, "org-1", c.ID, api.PaginationParams{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]float64{}
		for _, s := range rows {
			out[s.LocationID] = s.OnHand
		}
		return out
	}
	for _, body := range []map[string]any{
		{"movement_type": "receipt", "consumable_id": c.ID, "to_location_id": bin1, "quantity": 5},
		{"movement_type": "transfer", "consumable_id": c.ID, "from_location_id": bin1, "to_location_id": bin2, "quantity": 2},
		{"movement_type": "correction", "consumable_id": c.ID, "from_location_id": bin2, "quantity": 1},
		{"movement_type": "retrieval", "consumable_id": c.ID, "from_location_id": bin1, "quantity": 1},
	} {
		if code, resp := do(http.MethodPost, movementsRoute, body); code != http.StatusCreated {
			t.Fatalf("%v: %d %v", body["movement_type"], code, resp)
		}
	}
	if got := stock(); got[bin1] != 2 || got[bin2] != 1 {
		t.Errorf("stock %v, want bin1 2, bin2 1", got)
	}
	code, resp := do(http.MethodPost, movementsRoute, map[string]any{"movement_type": "retrieval", "consumable_id": c.ID, "from_location_id": bin1, "quantity": 9})
	if code != http.StatusConflict || resp["type"] != movement.ProblemInsufficientStock {
		t.Errorf("shortfall: %d %v, want 409 insufficient-stock", code, resp)
	}
	path := "/api/v1/consumables/" + c.ID + "/movements"
	if code, resp = do(http.MethodPost, path, map[string]any{"direction": "out", "quantity": 1}); code != http.StatusUnprocessableEntity || resp["field"] != "location_id" {
		t.Errorf("out without location_id on two bins: %d %v, want 422 on location_id", code, resp)
	}
	if code, resp = do(http.MethodPost, path, map[string]any{"direction": "out", "quantity": 1, "location_id": bin2}); code != http.StatusCreated {
		t.Errorf("out of bin2: %d %v", code, resp)
	}
	code, resp = do(http.MethodGet, path, nil)
	rows, _ := resp["data"].([]any)
	if code != http.StatusOK || len(rows) != 5 || resp["total"] != 5.0 {
		t.Fatalf("GET alias: %d %v, want the 5 movements", code, resp)
	}
	if first := rows[0].(map[string]any); first["direction"] != "out" || first["location_id"] != bin2 {
		t.Errorf("newest alias row %v, want out of bin2", first)
	}
	if got := stock(); got[bin1] != 2 || got[bin2] != 0 {
		t.Errorf("stock %v, want bin1 2, bin2 0", got)
	}
}
