package movement_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// TestMovementServiceRecordTx covers STK-04, AST-07 and EVT-03 (WP-274)
// against PostgreSQL as the runtime role: two concurrent transfers of one
// asset run one after the other (row lock), and the asset ends at the
// target of the movement committed last; RecordTx in a caller transaction
// that rolls back leaves neither a movement nor an event_outbox row; a
// booking writes exactly one stock.movement whose entity is the asset and
// whose data carries the derived client_id and site_id; a location of
// another client is 422 on its field; a withdrawal beyond the stock of a
// quantity item is ErrInsufficientStock and keeps the level, a transfer
// keeps it as well.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestMovementServiceRecordTx(t *testing.T) {
	f := scopetest.Seed(t, "2f")
	bg := context.Background()
	ctx := f.OrgCtx(f.OrgA)
	svc := movement.NewService(movement.NewPGRepository(f.App))
	room1, room2 := f.Room(t, f.Client1, "svc-r1"), f.Room(t, f.Client1, "svc-r2")
	foreignRoom := f.Room(t, f.Client2, "svc-r3")
	asset := f.Asset(t, f.OrgA, f.Client1, "svc-a1")
	scalar := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := f.Admin.QueryRow(bg, sql, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return s
	}
	transfer := func(to string) *movement.Movement {
		return &movement.Movement{OrganizationID: f.OrgA, AssetID: asset, MovementType: "transfer", ToLocationID: to}
	}

	t.Run("concurrent transfers", func(t *testing.T) {
		locked, release := make(chan struct{}), make(chan struct{})
		first := make(chan error, 1)
		go func() {
			first <- database.WithRequestTenant(ctx, f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
				if err := movement.RecordTx(ctx, tx, transfer(room1)); err != nil {
					close(locked)
					return err
				}
				close(locked)
				<-release
				return nil
			})
		}()
		<-locked
		second := make(chan error, 1)
		go func() { second <- svc.Record(ctx, transfer(room2)) }()
		// The second booking waits for the row lock of the first.
		deadline := time.Now().Add(10 * time.Second)
		for {
			var waiting int
			if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM pg_stat_activity
				WHERE wait_event_type = 'Lock' AND query LIKE '%FROM asset%FOR UPDATE%'`).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting > 0 {
				break
			}
			select {
			case err := <-second:
				t.Fatalf("second transfer finished while the first held the asset: %v", err)
			default:
			}
			if time.Now().After(deadline) {
				t.Fatal("the second transfer never waited for the lock of the first")
			}
			time.Sleep(20 * time.Millisecond)
		}
		close(release)
		if err := <-first; err != nil {
			t.Fatalf("first transfer: %v", err)
		}
		if err := <-second; err != nil {
			t.Fatalf("second transfer: %v", err)
		}
		if got := scalar(`SELECT location_id::text FROM asset WHERE id = $1`, asset); got != room2 {
			t.Errorf("asset location after both transfers: %s, want the target of the last commit %s", got, room2)
		}
	})

	t.Run("rollback", func(t *testing.T) {
		movements := scalar(`SELECT count(*)::text FROM stock_movement WHERE asset_id = $1`, asset)
		events := scalar(`SELECT count(*)::text FROM event_outbox WHERE type = 'stock.movement' AND entity_id = $1`, asset)
		errAbort := errors.New("caller aborts")
		err := database.WithRequestTenant(ctx, f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
			if recErr := svc.RecordTx(ctx, tx, transfer(room1)); recErr != nil {
				return recErr
			}
			return errAbort
		})
		if !errors.Is(err, errAbort) {
			t.Fatalf("rolled back booking: %v, want the caller's error", err)
		}
		if got := scalar(`SELECT count(*)::text FROM stock_movement WHERE asset_id = $1`, asset); got != movements {
			t.Errorf("movements after the rollback: %s, want %s", got, movements)
		}
		if got := scalar(`SELECT count(*)::text FROM event_outbox WHERE type = 'stock.movement' AND entity_id = $1`, asset); got != events {
			t.Errorf("stock.movement events after the rollback: %s, want %s", got, events)
		}
		if got := scalar(`SELECT location_id::text FROM asset WHERE id = $1`, asset); got != room2 {
			t.Errorf("asset location after the rollback: %s, want %s", got, room2)
		}
	})

	t.Run("event", func(t *testing.T) {
		m := transfer(room1)
		if err := svc.Record(ctx, m); err != nil {
			t.Fatal(err)
		}
		site := siteOf(t, f, room1)
		if m.ClientID != f.Client1 || m.SiteID != site || m.MovementType != "transfer" || m.OccurredAt.IsZero() {
			t.Errorf("booked movement %+v, want client %s, site %s and occurred_at", m, f.Client1, site)
		}
		rows, err := f.Admin.Query(bg, `SELECT entity_id, data FROM event_outbox WHERE type = 'stock.movement' AND data->>'id' = $1`, m.ID)
		if err != nil {
			t.Fatal(err)
		}
		type event struct {
			entity string
			data   map[string]any
		}
		var got []event
		for rows.Next() {
			var e event
			var raw []byte
			if err = rows.Scan(&e.entity, &raw); err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(raw, &e.data); err != nil {
				t.Fatal(err)
			}
			got = append(got, e)
		}
		rows.Close()
		if len(got) != 1 {
			t.Fatalf("stock.movement events of the booking: %d, want 1", len(got))
		}
		e := got[0]
		if e.entity != asset || e.data["asset_id"] != asset || e.data["movement_type"] != "transfer" ||
			e.data["client_id"] != f.Client1 || e.data["site_id"] != site || e.data["item_kind"] != "asset" {
			t.Errorf("stock.movement entity %s data %v, want the asset with client %s and site %s", e.entity, e.data, f.Client1, site)
		}
	})

	t.Run("foreign client location", func(t *testing.T) {
		movements := scalar(`SELECT count(*)::text FROM stock_movement WHERE asset_id = $1`, asset)
		err := svc.Record(ctx, transfer(foreignRoom))
		var fe *api.FieldError
		if !errors.As(err, &fe) || fe.Field != "to_location_id" || fe.Status != http.StatusUnprocessableEntity {
			t.Fatalf("transfer to a location of client 2: %v, want 422 on to_location_id", err)
		}
		if got := scalar(`SELECT count(*)::text FROM stock_movement WHERE asset_id = $1`, asset); got != movements {
			t.Errorf("movements after the refused transfer: %s, want %s", got, movements)
		}
	})

	t.Run("stock", func(t *testing.T) {
		item := f.ID()
		if _, err := f.Admin.Exec(bg, `INSERT INTO quantity_item (id, organization_id, client_id, name, stock_level, location_id)
			VALUES ($1, $2, $3, 'svc-cable', 5, $4)`, item, f.OrgA, f.Client1, room1); err != nil {
			t.Fatal(err)
		}
		level := func() string {
			return scalar(`SELECT stock_level::float8::text FROM quantity_item WHERE id = $1`, item)
		}
		book := func(kind string, qty float64) error {
			return svc.Record(ctx, &movement.Movement{OrganizationID: f.OrgA, QuantityItemID: item, MovementType: kind, Quantity: &qty})
		}
		if err := book("retrieval", 6); !errors.Is(err, movement.ErrInsufficientStock) {
			t.Errorf("withdrawal beyond the stock: %v, want ErrInsufficientStock", err)
		}
		if got := level(); got != "5" {
			t.Errorf("level after the refused withdrawal: %s, want 5", got)
		}
		if err := book("transfer", 3); err != nil {
			t.Fatal(err)
		}
		if got := level(); got != "5" {
			t.Errorf("level after a transfer: %s, want 5", got)
		}
		if err := book("retrieval", 5); err != nil {
			t.Fatal(err)
		}
		if err := book("receipt", 2); err != nil {
			t.Fatal(err)
		}
		if got := level(); got != "2" {
			t.Errorf("level after withdrawing 5 and receiving 2: %s, want 2", got)
		}
		if got := scalar(`SELECT count(*)::text FROM event_outbox WHERE type = 'stock.movement' AND entity_id = $1`, item); got != "3" {
			t.Errorf("stock.movement events of the quantity item: %s, want 3", got)
		}
	})
}

// movementAPI serves the movement and consumable routes over PostgreSQL
// for an org-wide principal of organization A.
type movementAPI struct {
	t   *testing.T
	f   *scopetest.Fixture
	mux *chi.Mux
}

func newMovementAPI(t *testing.T, f *scopetest.Fixture) *movementAPI {
	mux := chi.NewRouter()
	movement.NewHandler(movement.NewService(movement.NewPGRepository(f.App))).RegisterRoutes(mux)
	consumable.NewHandler(consumable.NewPGRepository(f.App)).RegisterRoutes(mux)
	return &movementAPI{t: t, f: f, mux: mux}
}

// do sends the request and returns status and decoded body.
func (a *movementAPI) do(method, path string, body any) (int, map[string]any) {
	a.t.Helper()
	var reader *strings.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			a.t.Fatal(err)
		}
		reader = strings.NewReader(string(raw))
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	ctx := tenant.WithTenant(a.f.OrgCtx(a.f.OrgA), tenant.TenantInfo{OrganizationID: a.f.OrgA, UserID: a.f.User})
	rec := httptest.NewRecorder()
	a.mux.ServeHTTP(rec, req.WithContext(ctx))
	out := map[string]any{}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			a.t.Fatalf("%s %s: body %s: %v", method, path, rec.Body.String(), err)
		}
	}
	return rec.Code, out
}

// ids returns the ids of the data of a list answer.
func ids(body map[string]any) []string {
	data, _ := body["data"].([]any)
	out := make([]string, 0, len(data))
	for _, d := range data {
		if m, ok := d.(map[string]any); ok {
			out = append(out, m["id"].(string))
		}
	}
	return out
}

// TestStockMovementAPI covers STK-04 and AST-07 (E-38, WP-274) over the
// handlers against PostgreSQL: POST /api/v1/stock-movements refuses the
// types of the domain paths with 422 movement-type-reserved and legacy
// names, consumable_id, quantity on an asset, an asset type not booked
// through the route and a missing quantity with 422 on the field; it books
// an asset transfer and a quantity receipt. GET filters by consumable_id,
// occurred_from/occurred_to (RFC 3339, else 422) and movement_type
// (unknown 422) and pages with the keyset cursor newest first. DELETE of a
// consumable and of a quantity item with movements is 409 with
// dependencies ["stock_movement"].
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStockMovementAPI(t *testing.T) {
	f := scopetest.Seed(t, "2f")
	bg := context.Background()
	a := newMovementAPI(t, f)
	room := f.Room(t, f.Client1, "api-r1")
	asset := f.Asset(t, f.OrgA, f.Client1, "api-a1")
	item, cons := f.ID(), f.ID()
	if _, err := f.Admin.Exec(bg, `INSERT INTO quantity_item (id, organization_id, client_id, name, stock_level) VALUES ($1, $2, $3, 'api-cable', 3)`,
		item, f.OrgA, f.Client1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO consumable (id, organization_id, client_id, name) VALUES ($1, $2, $3, 'api-toner')`,
		cons, f.OrgA, f.Client1); err != nil {
		t.Fatal(err)
	}
	// One piece in a bin (WP-283): the retrieval below empties it, so the
	// delete is kept by the ledger only.
	if _, err := f.Admin.Exec(bg, `INSERT INTO stock_level (organization_id, consumable_id, location_id, on_hand) VALUES ($1, $2, $3, 1)`,
		f.OrgA, cons, stockBin(t, f, f.Client1, "api-bin")); err != nil {
		t.Fatal(err)
	}

	t.Run("refused bookings", func(t *testing.T) {
		for _, kind := range []string{"assignment", "return", "reservation", "release", "disposal"} {
			code, body := a.do(http.MethodPost, "/api/v1/stock-movements", map[string]any{"movement_type": kind, "asset_id": asset})
			if code != http.StatusUnprocessableEntity || body["type"] != movement.ProblemMovementTypeReserved {
				t.Errorf("POST %s: %d %v, want 422 %s", kind, code, body, movement.ProblemMovementTypeReserved)
			}
		}
		one := 1.0
		for name, tc := range map[string]struct {
			body  map[string]any
			field string
		}{
			"legacy name":           {map[string]any{"movement_type": "warehouse_transfer", "asset_id": asset}, "movement_type"},
			"unknown type":          {map[string]any{"movement_type": "teleport", "asset_id": asset}, "movement_type"},
			"consumable":            {map[string]any{"movement_type": "receipt", "consumable_id": cons, "quantity": one}, "consumable_id"},
			"asset with quantity":   {map[string]any{"movement_type": "transfer", "asset_id": asset, "quantity": one}, "quantity"},
			"quantity type":         {map[string]any{"movement_type": "deployment", "quantity_item_id": item, "quantity": one}, "movement_type"},
			"quantity without qty":  {map[string]any{"movement_type": "receipt", "quantity_item_id": item}, "quantity"},
			"two objects":           {map[string]any{"movement_type": "receipt", "asset_id": asset, "quantity_item_id": item}, "asset_id"},
			"foreign item kind":     {map[string]any{"movement_type": "receipt", "asset_id": asset, "item_kind": "quantity_item"}, "item_kind"},
			"unknown ticket":        {map[string]any{"movement_type": "correction", "asset_id": asset, "ticket_id": f.ID()}, "ticket_id"},
			"withdrawal over stock": {map[string]any{"movement_type": "retrieval", "quantity_item_id": item, "quantity": 9.0}, ""},
		} {
			code, body := a.do(http.MethodPost, "/api/v1/stock-movements", tc.body)
			if tc.field == "" {
				if code != http.StatusConflict || body["type"] != movement.ProblemInsufficientStock {
					t.Errorf("%s: %d %v, want 409 %s", name, code, body, movement.ProblemInsufficientStock)
				}
				continue
			}
			if code != http.StatusUnprocessableEntity || body["field"] != tc.field {
				t.Errorf("%s: %d %v, want 422 on %s", name, code, body, tc.field)
			}
		}
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM stock_movement WHERE asset_id = $1 OR quantity_item_id = $2`, asset, item).Scan(&n); err != nil || n != 0 {
			t.Errorf("movements after the refused bookings: %d (%v), want none", n, err)
		}
	})

	var transferID string
	t.Run("bookings", func(t *testing.T) {
		code, body := a.do(http.MethodPost, "/api/v1/stock-movements", map[string]any{"movement_type": "transfer", "asset_id": asset, "to_location_id": room})
		if code != http.StatusCreated || body["client_id"] != f.Client1 || body["occurred_at"] == nil || body["quantity"] != nil {
			t.Fatalf("asset transfer: %d %v", code, body)
		}
		transferID, _ = body["id"].(string)
		code, body = a.do(http.MethodPost, "/api/v1/stock-movements", map[string]any{"movement_type": "receipt", "quantity_item_id": item, "quantity": 2})
		if code != http.StatusCreated || body["quantity"] != 2.0 {
			t.Fatalf("quantity receipt: %d %v", code, body)
		}
	})

	t.Run("filters and cursor", func(t *testing.T) {
		if _, err := consumable.NewPGRepository(f.App).AddMovement(f.OrgCtx(f.OrgA), &consumable.Movement{OrganizationID: f.OrgA,
			ConsumableID: cons, Direction: "out", Quantity: 1, Reference: "api"}); err != nil {
			t.Fatal(err)
		}
		code, body := a.do(http.MethodGet, "/api/v1/stock-movements?consumable_id="+cons, nil)
		data, _ := body["data"].([]any)
		if code != http.StatusOK || len(data) != 1 || data[0].(map[string]any)["consumable_id"] != cons ||
			data[0].(map[string]any)["movement_type"] != "retrieval" {
			t.Errorf("filter consumable_id: %d %v, want the one consumable retrieval", code, body)
		}

		// Three corrections of the asset at fixed times.
		base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
		var timed []string
		for i := range 3 {
			id := f.ID()
			if _, err := f.Admin.Exec(bg, `INSERT INTO stock_movement (id, organization_id, item_kind, asset_id, movement_type, occurred_at)
				VALUES ($1, $2, 'asset', $3, 'correction', $4)`, id, f.OrgA, asset, base.Add(time.Duration(i)*time.Hour)); err != nil {
				t.Fatal(err)
			}
			timed = append(timed, id)
		}
		window := url.Values{"asset_id": {asset}, "occurred_from": {base.Add(30 * time.Minute).Format(time.RFC3339)},
			"occurred_to": {base.Add(time.Hour).Format(time.RFC3339)}}
		if code, body = a.do(http.MethodGet, "/api/v1/stock-movements?"+window.Encode(), nil); code != http.StatusOK || !slices.Equal(ids(body), timed[1:2]) {
			t.Errorf("filter occurred_from/occurred_to: %d %v, want %v", code, ids(body), timed[1:2])
		}
		for _, q := range []string{"occurred_from=yesterday", "occurred_to=2026-13-01", "movement_type=bin_transfer", "consumable_id=x"} {
			field, _, _ := strings.Cut(q, "=")
			if code, body = a.do(http.MethodGet, "/api/v1/stock-movements?"+q, nil); code != http.StatusUnprocessableEntity || body["field"] != field {
				t.Errorf("GET ?%s: %d %v, want 422 on %s", q, code, body, field)
			}
		}

		// The asset has the transfer (now) and the three corrections:
		// pages of two, newest first, through the keyset cursor.
		want := []string{transferID, timed[2], timed[1], timed[0]}
		var got []string
		path := "/api/v1/stock-movements?asset_id=" + asset + "&limit=2"
		for pages := 0; pages < 3; pages++ {
			code, body = a.do(http.MethodGet, path, nil)
			if code != http.StatusOK {
				t.Fatalf("page %d: %d %v", pages, code, body)
			}
			if body["total"] != 4.0 {
				t.Errorf("page %d total %v, want 4", pages, body["total"])
			}
			got = append(got, ids(body)...)
			next, _ := body["next_cursor"].(string)
			if next == "" {
				break
			}
			path = "/api/v1/stock-movements?asset_id=" + asset + "&limit=2&cursor=" + url.QueryEscape(next)
		}
		if !slices.Equal(got, want) {
			t.Errorf("paged movements %v, want %v", got, want)
		}
		// The deprecated asset path reads the same ledger.
		if code, body = a.do(http.MethodGet, "/api/v1/assets/"+asset+"/movements?limit=200", nil); code != http.StatusOK || !slices.Equal(ids(body), want) {
			t.Errorf("GET /assets/{id}/movements: %d %v, want %v", code, ids(body), want)
		}
	})

	t.Run("delete with movements", func(t *testing.T) {
		for _, path := range []string{"/api/v1/consumables/" + cons, "/api/v1/inventory/items/" + item} {
			code, body := a.do(http.MethodDelete, path, nil)
			deps, _ := body["dependencies"].([]any)
			if code != http.StatusConflict || len(deps) != 1 || deps[0] != "stock_movement" {
				t.Errorf("DELETE %s: %d %v, want 409 with dependencies [stock_movement]", path, code, body)
			}
		}
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT (SELECT count(*) FROM consumable WHERE id = $1) + (SELECT count(*) FROM quantity_item WHERE id = $2)`,
			cons, item).Scan(&n); err != nil || n != 2 {
			t.Errorf("consumable and quantity item after the refused deletes: %d (%v), want both", n, err)
		}
	})
}
