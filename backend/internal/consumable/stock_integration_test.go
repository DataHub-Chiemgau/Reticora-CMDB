package consumable_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Retest (WP-283, STK-01, STK-02, STK-03, API-06):
//
//	cd backend && go test -count=1 -p 1 -run 'TestConsumable' ./internal/consumable/

const stockMigration = "../../migrations/000252_consumable_stock_level"

// tree holds the nodes of a storage branch site > warehouse > zone > shelf
// > bin.
type tree struct {
	site, zone, shelf, bin string
}

// binTree creates a storage branch of clientID in orgID through the
// location repository.
func binTree(t *testing.T, f *scopetest.Fixture, orgID, clientID, name string) tree {
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
	return tree{site: ids[locations.KindSite], zone: ids[locations.KindZone], shelf: ids[locations.KindShelf], bin: ids[locations.KindBin]}
}

// book applies b in a request transaction of ctx.
func book(ctx context.Context, f *scopetest.Fixture, b *consumable.Booking) error {
	return database.WithRequestTenant(ctx, f.App, b.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		return consumable.ApplyBooking(ctx, tx, b)
	})
}

// listStock reads the stock rows of the consumable in a request transaction
// of ctx.
func listStock(ctx context.Context, f *scopetest.Fixture, id string) ([]consumable.Stock, error) {
	var rows []consumable.Stock
	err := database.WithRequestTenant(ctx, f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rows, _, err = consumable.ListStock(ctx, tx, f.OrgA, id, api.PaginationParams{Limit: 100})
		return err
	})
	return rows, err
}

// onHand returns on_hand of the consumable in the bin read without RLS, -1
// without row.
func onHand(t *testing.T, f *scopetest.Fixture, cons, bin string) float64 {
	t.Helper()
	var v float64
	err := f.Admin.QueryRow(context.Background(), `SELECT on_hand::float8 FROM stock_level WHERE consumable_id = $1 AND location_id = $2`,
		cons, bin).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return -1
	}
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// newConsumable creates a consumable of clientID (empty: org-wide) through
// the repository of an org-wide principal.
func newConsumable(t *testing.T, f *scopetest.Fixture, clientID, name string) string {
	t.Helper()
	c := &consumable.Consumable{OrganizationID: f.OrgA, ClientID: clientID, Name: name}
	if err := consumable.NewPGRepository(f.App).Create(f.OrgCtx(f.OrgA), c); err != nil {
		t.Fatalf("create consumable %s: %v", name, err)
	}
	return c.ID
}

// stockAPI serves the consumable routes for an org-wide principal of
// organization A.
type stockAPI struct {
	t      *testing.T
	router chi.Router
}

func newStockAPI(t *testing.T, f *scopetest.Fixture) *stockAPI {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			scope := database.OrgWideScope(f.OrgA, f.User)
			ctx := tenant.WithTenant(database.ContextWithTenantScope(req.Context(), &scope),
				tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	consumable.NewHandler(consumable.NewPGRepository(f.App)).RegisterRoutes(r)
	return &stockAPI{t: t, router: r}
}

func (a *stockAPI) do(method, path, body string) (int, map[string]any) {
	a.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	a.router.ServeHTTP(rec, req)
	out := map[string]any{}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			a.t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
		}
	}
	return rec.Code, out
}

// TestConsumableStockPerBin covers the stock of consumables per bin
// (STK-01, STK-02, STK-03, API-06, WP-283): the checks of stock_level
// (on_hand >= 0, reserved <= on_hand, only bins, client of the bin);
// ApplyBooking per type (receipt upsert, retrieval shortfall 409
// insufficient-stock, correction both ways, transfer, 422 for a location
// that is no bin or of another client); opposite transfers in parallel
// without deadlock; a bin moved to another site carries its rows along; a
// client change of the consumable against its bins is 422; GET
// /consumables/{id}/stock with cursor; the transitional movements with and
// without bin; LOC-11 409 for a bin with stock; 409 on deleting a stocked
// consumable; low_stock over the sum; the entitlement stock (403); no
// reference to ci.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestConsumableStockPerBin(t *testing.T) {
	f := scopetest.Seed(t, "30")
	bg := context.Background()
	orgCtx := f.OrgCtx(f.OrgA)
	b1 := binTree(t, f, f.OrgA, f.Client1, "stk-a")
	b1b := binTree(t, f, f.OrgA, f.Client1, "stk-b")
	b2 := binTree(t, f, f.OrgA, f.Client2, "stk-c")
	cons := newConsumable(t, f, f.Client1, "stk-toner")
	other := newConsumable(t, f, f.Client2, "stk-c2")
	booking := func(kind, from, to string, qty float64) *consumable.Booking {
		return &consumable.Booking{OrganizationID: f.OrgA, ConsumableID: cons, MovementType: kind, FromLocationID: from, ToLocationID: to, Quantity: qty}
	}

	t.Run("checks", func(t *testing.T) {
		tx, err := f.Admin.Begin(bg)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(bg) }()
		try := func(sql string, args ...any) string {
			t.Helper()
			sp, spErr := tx.Begin(bg)
			if spErr != nil {
				t.Fatal(spErr)
			}
			defer func() { _ = sp.Rollback(bg) }()
			if _, execErr := sp.Exec(bg, sql, args...); execErr != nil {
				var pgErr *pgconn.PgError
				if errors.As(execErr, &pgErr) {
					return pgErr.Code + " " + pgErr.ConstraintName
				}
				t.Fatal(execErr)
			}
			return ""
		}
		insert := `INSERT INTO stock_level (organization_id, consumable_id, location_id, on_hand, reserved) VALUES ($1, $2, $3, $4, $5)`
		for name, tc := range map[string]struct {
			cons, location string
			onHand, res    float64
			want           string
		}{
			"negative on_hand":        {cons, b1.bin, -1, 0, "23514 stock_level_on_hand_check"},
			"reserved above on_hand":  {cons, b1.bin, 1, 2, "23514 stock_level_reserved_check"},
			"negative reserved":       {cons, b1.bin, 1, -1, "23514 stock_level_reserved_check"},
			"shelf":                   {cons, b1.shelf, 1, 0, "23514 stock_level_location_bin"},
			"bin of another client":   {other, b1.bin, 1, 0, "23514 stock_level_location_client"},
			"fitting bin and amounts": {cons, b1.bin, 2, 2, ""},
		} {
			if got := try(insert, f.OrgA, tc.cons, tc.location, tc.onHand, tc.res); got != tc.want {
				t.Errorf("%s: %q, want %q", name, got, tc.want)
			}
		}
		var client, site string
		if err = tx.QueryRow(bg, `INSERT INTO stock_level (organization_id, consumable_id, location_id, client_id, site_id)
			VALUES ($1, $2, $3, $4, $4) RETURNING client_id::text, site_id::text`, f.OrgA, cons, b1b.bin, f.Client2).Scan(&client, &site); err != nil {
			t.Fatal(err)
		}
		if client != f.Client1 || site != b1b.site {
			t.Errorf("derived client %s and site %s, want %s and %s", client, site, f.Client1, b1b.site)
		}
	})

	t.Run("bookings", func(t *testing.T) {
		for _, b := range []*consumable.Booking{booking("receipt", "", b1.bin, 5), booking("receipt", "", b1.bin, 3)} {
			if err := book(orgCtx, f, b); err != nil {
				t.Fatalf("receipt: %v", err)
			}
		}
		var rows int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM stock_level WHERE consumable_id = $1`, cons).Scan(&rows); err != nil || rows != 1 || onHand(t, f, cons, b1.bin) != 8 {
			t.Fatalf("two receipts: %d rows (%v), on_hand %v; want one row with 8", rows, err, onHand(t, f, cons, b1.bin))
		}
		err := book(orgCtx, f, booking("retrieval", b1.bin, "", 9))
		var typed api.TypedProblem
		if !errors.Is(err, consumable.ErrInsufficientStock) || !errors.Is(err, movement.ErrInsufficientStock) ||
			!errors.As(err, &typed) || typed.ProblemType() != movement.ProblemInsufficientStock {
			t.Fatalf("retrieval beyond the bin: %v, want insufficient-stock", err)
		}
		steps := []struct {
			b             *consumable.Booking
			first, second float64
		}{
			{booking("correction", "", b1.bin, 2), 10, -1},
			{booking("correction", b1.bin, "", 1), 9, -1},
			{booking("transfer", b1.bin, b1b.bin, 4), 5, 4},
			{booking("retrieval", b1b.bin, "", 4), 5, 0},
		}
		for _, s := range steps {
			if err = book(orgCtx, f, s.b); err != nil {
				t.Fatalf("%s: %v", s.b.MovementType, err)
			}
			if first, second := onHand(t, f, cons, b1.bin), onHand(t, f, cons, b1b.bin); first != s.first || second != s.second {
				t.Errorf("after %s: on_hand %v and %v, want %v and %v", s.b.MovementType, first, second, s.first, s.second)
			}
		}
		for name, tc := range map[string]struct {
			b     *consumable.Booking
			field string
		}{
			"shelf":                 {booking("receipt", "", b1.shelf, 1), "to_location_id"},
			"bin of another client": {booking("receipt", "", b2.bin, 1), "to_location_id"},
			"unknown bin":           {booking("retrieval", f.ID(), "", 1), "from_location_id"},
			"two bins correction":   {booking("correction", b1.bin, b1b.bin, 1), "to_location_id"},
		} {
			var fe *api.FieldError
			if err = book(orgCtx, f, tc.b); !errors.As(err, &fe) || fe.Field != tc.field || fe.Status != http.StatusUnprocessableEntity {
				t.Errorf("%s: %v, want 422 on %s", name, err, tc.field)
			}
		}
	})

	t.Run("opposite transfers in parallel", func(t *testing.T) {
		if err := book(orgCtx, f, booking("receipt", "", b1b.bin, 5)); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make(chan error, 40)
		for i := 0; i < 20; i++ {
			for _, pair := range [][2]string{{b1.bin, b1b.bin}, {b1b.bin, b1.bin}} {
				wg.Add(1)
				go func(from, to string) {
					defer wg.Done()
					errs <- book(orgCtx, f, booking("transfer", from, to, 1))
				}(pair[0], pair[1])
			}
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil && !errors.Is(err, consumable.ErrInsufficientStock) {
				t.Errorf("parallel transfer: %v", err)
			}
		}
		if sum := onHand(t, f, cons, b1.bin) + onHand(t, f, cons, b1b.bin); sum != 10 {
			t.Errorf("stock after the transfers: %v, want 10", sum)
		}
	})

	t.Run("bin moves to another site", func(t *testing.T) {
		target := binTree(t, f, f.OrgA, f.Client1, "stk-d")
		moved := binTree(t, f, f.OrgA, f.Client1, "stk-e")
		if err := book(orgCtx, f, booking("receipt", "", moved.bin, 1)); err != nil {
			t.Fatal(err)
		}
		if _, err := locations.NewPGRepository(f.App).Move(orgCtx, f.OrgA, moved.shelf, target.zone); err != nil {
			t.Fatalf("move shelf: %v", err)
		}
		var site string
		if err := f.Admin.QueryRow(bg, `SELECT site_id::text FROM stock_level WHERE consumable_id = $1 AND location_id = $2`, cons, moved.bin).Scan(&site); err != nil || site != target.site {
			t.Errorf("site of the moved bin's stock: %s (%v), want %s", site, err, target.site)
		}
	})

	a := newStockAPI(t, f)
	t.Run("client change against the bins", func(t *testing.T) {
		code, body := a.do(http.MethodPatch, "/api/v1/consumables/"+cons, `{"client_id":"`+f.Client2+`"}`)
		if code != http.StatusUnprocessableEntity {
			t.Errorf("PATCH client of a stocked consumable: %d %v, want 422", code, body)
		}
		var client string
		if err := f.Admin.QueryRow(bg, `SELECT client_id::text FROM consumable WHERE id = $1`, cons).Scan(&client); err != nil || client != f.Client1 {
			t.Errorf("client after the refused change: %s (%v)", client, err)
		}
	})

	t.Run("stock list", func(t *testing.T) {
		code, body := a.do(http.MethodGet, "/api/v1/consumables/"+cons+"/stock?limit=1", "")
		data, _ := body["data"].([]any)
		next, _ := body["next_cursor"].(string)
		if code != http.StatusOK || len(data) != 1 || next == "" || body["has_more"] != true {
			t.Fatalf("first stock page: %d %v", code, body)
		}
		row, _ := data[0].(map[string]any)
		for _, key := range []string{"location_id", "on_hand", "reserved", "updated_at"} {
			if _, ok := row[key]; !ok {
				t.Errorf("stock row without %s: %v", key, row)
			}
		}
		seen := []any{row["location_id"]}
		for next != "" {
			code, body = a.do(http.MethodGet, "/api/v1/consumables/"+cons+"/stock?limit=1&cursor="+next, "")
			if code != http.StatusOK {
				t.Fatalf("next stock page: %d %v", code, body)
			}
			data, _ = body["data"].([]any)
			for _, d := range data {
				seen = append(seen, d.(map[string]any)["location_id"])
			}
			next, _ = body["next_cursor"].(string)
		}
		var want int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM stock_level WHERE consumable_id = $1`, cons).Scan(&want); err != nil || len(seen) != want {
			t.Errorf("paged %d bins %v, want %d (%v)", len(seen), seen, want, err)
		}
		if code, _ = a.do(http.MethodGet, "/api/v1/consumables/"+f.ID()+"/stock", ""); code != http.StatusNotFound {
			t.Errorf("stock of an unknown consumable: %d, want 404", code)
		}
	})

	t.Run("transitional movements", func(t *testing.T) {
		code, body := a.do(http.MethodPost, "/api/v1/consumables/"+cons+"/movements", `{"direction":"in","quantity":1}`)
		if code != http.StatusUnprocessableEntity || body["field"] != "location_id" {
			t.Errorf("in without bin on several bins: %d %v, want 422 on location_id", code, body)
		}
		code, body = a.do(http.MethodPost, "/api/v1/consumables/"+cons+"/movements", `{"direction":"in","quantity":2,"location_id":"`+b1.bin+`"}`)
		mv, _ := body["movement"].(map[string]any)
		if code != http.StatusCreated || mv["location_id"] != b1.bin {
			t.Fatalf("in into a bin: %d %v", code, body)
		}
		var to string
		if err := f.Admin.QueryRow(bg, `SELECT to_location_id::text FROM stock_movement WHERE id = $1`, mv["id"]).Scan(&to); err != nil || to != b1.bin {
			t.Errorf("ledger row of the receipt: to %s (%v), want %s", to, err, b1.bin)
		}
		code, body = a.do(http.MethodPost, "/api/v1/consumables/"+cons+"/movements", `{"direction":"out","quantity":1000,"location_id":"`+b1.bin+`"}`)
		if code != http.StatusConflict || body["type"] != movement.ProblemInsufficientStock {
			t.Errorf("out beyond the bin: %d %v, want 409 insufficient-stock", code, body)
		}
		code, body = a.do(http.MethodPost, "/api/v1/consumables/"+cons+"/movements", `{"direction":"in","quantity":1,"location_id":"`+b1.shelf+`"}`)
		if code != http.StatusUnprocessableEntity || body["field"] != "location_id" {
			t.Errorf("in into a shelf: %d %v, want 422 on location_id", code, body)
		}
		single := newConsumable(t, f, f.Client2, "stk-single")
		code, body = a.do(http.MethodPost, "/api/v1/consumables/"+single+"/movements", `{"direction":"in","quantity":3,"location_id":"`+b2.bin+`"}`)
		if code != http.StatusCreated {
			t.Fatalf("in into the only bin: %d %v", code, body)
		}
		code, body = a.do(http.MethodPost, "/api/v1/consumables/"+single+"/movements", `{"direction":"out","quantity":1}`)
		c, _ := body["consumable"].(map[string]any)
		if code != http.StatusCreated || c["stock_level"] != 2.0 || onHand(t, f, single, b2.bin) != 2 {
			t.Errorf("out of the only bin without location_id: %d %v", code, body)
		}
	})

	t.Run("deletes", func(t *testing.T) {
		item := newConsumable(t, f, f.Client1, "stk-delete")
		branch := binTree(t, f, f.OrgA, f.Client1, "stk-f")
		if err := book(orgCtx, f, &consumable.Booking{OrganizationID: f.OrgA, ConsumableID: item, MovementType: "receipt", ToLocationID: branch.bin, Quantity: 2}); err != nil {
			t.Fatal(err)
		}
		var de *locations.DependencyError
		if err := locations.NewPGRepository(f.App).Delete(orgCtx, f.OrgA, branch.bin); !errors.As(err, &de) || !slices.Equal(de.Kinds, []string{locations.DependencyQuantityItem}) {
			t.Errorf("delete of a bin with stock: %v, want dependencies [quantity_item]", err)
		}
		if code, body := a.do(http.MethodDelete, "/api/v1/consumables/"+item, ""); code != http.StatusConflict {
			t.Errorf("delete of a stocked consumable: %d %v, want 409", code, body)
		}
		if err := book(orgCtx, f, &consumable.Booking{OrganizationID: f.OrgA, ConsumableID: item, MovementType: "retrieval", FromLocationID: branch.bin, Quantity: 2}); err != nil {
			t.Fatal(err)
		}
		if code, body := a.do(http.MethodDelete, "/api/v1/consumables/"+item, ""); code != http.StatusNoContent {
			t.Errorf("delete of a consumable with an empty bin: %d %v, want 204", code, body)
		}
		if onHand(t, f, item, branch.bin) != -1 {
			t.Error("the empty stock row outlived its consumable")
		}
	})

	t.Run("low stock over the sum", func(t *testing.T) {
		repo := consumable.NewPGRepository(f.App)
		low := newConsumable(t, f, f.Client1, "stk-low")
		minLevel := 4.0
		if _, err := repo.Update(orgCtx, f.OrgA, low, consumable.UpdateConsumableRequest{MinLevel: &minLevel}); err != nil {
			t.Fatal(err)
		}
		lowListed := func() bool {
			t.Helper()
			list, _, err := repo.List(orgCtx, f.OrgA, consumable.FilterParams{LowStock: true, Search: "stk-low"}, api.PaginationParams{Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			return len(list) == 1 && list[0].ID == low
		}
		for _, bin := range []string{b1.bin, b1b.bin} {
			if err := book(orgCtx, f, &consumable.Booking{OrganizationID: f.OrgA, ConsumableID: low, MovementType: "receipt", ToLocationID: bin, Quantity: 2}); err != nil {
				t.Fatal(err)
			}
		}
		if !lowListed() {
			t.Error("2 + 2 <= 4: the consumable is not low on stock")
		}
		if err := book(orgCtx, f, &consumable.Booking{OrganizationID: f.OrgA, ConsumableID: low, MovementType: "receipt", ToLocationID: b1.bin, Quantity: 1}); err != nil {
			t.Fatal(err)
		}
		if lowListed() {
			t.Error("3 + 2 > 4: the consumable is still low on stock")
		}
		if c, err := repo.GetByID(orgCtx, f.OrgA, low); err != nil || c.StockLevel != 5 {
			t.Errorf("stock_level %+v (%v), want the sum 5", c, err)
		}
	})

	t.Run("entitlement stock", func(t *testing.T) {
		gate := entitlement.NewService(nil, entitlement.Options{Enforce: true})
		h := gate.Middleware(a.router)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/consumables/"+cons+"/stock", nil)
		req = req.WithContext(tenant.WithTenant(orgCtx, tenant.TenantInfo{OrganizationID: f.OrgA}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), api.ProblemFeatureNotEntitled) {
			t.Errorf("stock list without the entitlement stock: %d %s, want 403 feature-not-entitled", rec.Code, rec.Body.String())
		}
	})

	t.Run("never a CI", func(t *testing.T) {
		var refs int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM pg_constraint WHERE contype = 'f'
			AND conrelid IN ('consumable'::regclass, 'stock_level'::regclass) AND confrelid = 'ci'::regclass`).Scan(&refs); err != nil || refs != 0 {
			t.Errorf("%d foreign keys of consumable or stock_level on ci (%v), want none", refs, err)
		}
		var cols int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM information_schema.columns
			WHERE table_name IN ('consumable', 'stock_level') AND column_name LIKE 'ci%'`).Scan(&cols); err != nil || cols != 0 {
			t.Errorf("%d CI columns on consumable or stock_level (%v), want none", cols, err)
		}
	})
}

// TestConsumableStockMigration covers the data migration of 000252 (E-40,
// WP-283) in a rolled-back transaction: after its down part consumables
// get a stock and a free-text location; the up part quarantines a positive
// stock as stock_without_bin, a negative one as negative_stock and a
// location text without stock as location_without_bin, creates no
// stock_level row and drops both columns. The down part brings them back:
// the sum of on_hand where a consumable has rows, else the quarantined
// values, and removes its quarantine entries.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestConsumableStockMigration(t *testing.T) {
	f := scopetest.Seed(t, "30")
	bg := context.Background()
	rawUp, err := os.ReadFile(stockMigration + ".up.sql")
	if err != nil {
		t.Fatal(err)
	}
	rawDown, err := os.ReadFile(stockMigration + ".down.sql")
	if err != nil {
		t.Fatal(err)
	}
	branch := binTree(t, f, f.OrgA, f.Client1, "mig252")
	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, execErr := tx.Exec(bg, sql, args...); execErr != nil {
			t.Fatalf("%.60s...: %v", strings.TrimSpace(sql), execErr)
		}
	}
	exec(string(rawDown))
	positive, negative, located, empty := f.ID(), f.ID(), f.ID(), f.ID()
	for _, c := range []struct {
		id, location string
		level        float64
	}{{positive, "Shelf 1", 4}, {negative, "", -2}, {located, "Cellar", 0}, {empty, "", 0}} {
		exec(`INSERT INTO consumable (id, organization_id, client_id, name, stock_level, location) VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''))`,
			c.id, f.OrgA, f.Client1, "mig252-"+c.id, c.level, c.location)
	}
	exec(string(rawUp))

	reasons := map[string]string{}
	rows, err := tx.Query(bg, `SELECT row_data ->> 'id', reason FROM migration_quarantine
		WHERE migration = '000252' AND source_table = 'consumable' AND organization_id = $1`, f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, reason string
		if err = rows.Scan(&id, &reason); err != nil {
			t.Fatal(err)
		}
		reasons[id] = reason
	}
	rows.Close()
	want := map[string]string{positive: "stock_without_bin", negative: "negative_stock", located: "location_without_bin"}
	if len(reasons) != len(want) {
		t.Errorf("quarantine %v, want %v", reasons, want)
	}
	for id, reason := range want {
		if reasons[id] != reason {
			t.Errorf("quarantine of %s: %q, want %q", id, reasons[id], reason)
		}
	}
	var leftover, levels int
	if err = tx.QueryRow(bg, `SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'consumable' AND column_name IN ('stock_level', 'location')`).Scan(&leftover); err != nil || leftover != 0 {
		t.Errorf("%d former columns left on consumable (%v)", leftover, err)
	}
	if err = tx.QueryRow(bg, `SELECT count(*) FROM stock_level WHERE consumable_id = ANY ($1::uuid[])`,
		[]string{positive, negative, located, empty}).Scan(&levels); err != nil || levels != 0 {
		t.Errorf("%d stock_level rows from the migration (%v), want none", levels, err)
	}

	// The operator books the stock of positive into a bin; down sums it.
	exec(`INSERT INTO stock_level (organization_id, consumable_id, location_id, on_hand) VALUES ($1, $2, $3, 7)`, f.OrgA, positive, branch.bin)
	exec(string(rawDown))
	for _, c := range []struct {
		id, location string
		level        float64
	}{{positive, "Shelf 1", 7}, {negative, "", -2}, {located, "Cellar", 0}, {empty, "", 0}} {
		var level float64
		var location string
		if err = tx.QueryRow(bg, `SELECT stock_level::float8, COALESCE(location, '') FROM consumable WHERE id = $1`, c.id).Scan(&level, &location); err != nil {
			t.Fatal(err)
		}
		if level != c.level || location != c.location {
			t.Errorf("after down %s: %v %q, want %v %q", c.id, level, location, c.level, c.location)
		}
	}
	var entries int
	if err = tx.QueryRow(bg, `SELECT count(*) FROM migration_quarantine WHERE migration = '000252'`).Scan(&entries); err != nil || entries != 0 {
		t.Errorf("%d quarantine entries of 000252 after down (%v)", entries, err)
	}
	exec(string(rawUp))
}
