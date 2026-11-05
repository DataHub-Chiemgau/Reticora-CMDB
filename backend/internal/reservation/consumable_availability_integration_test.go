package reservation_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reservation"
)

// Tests of WP-759 (STK-03, API-06, E-38, E-44, E-138) against PostgreSQL
// (TEST_DATABASE_URL, scopetest.Seed):
//
//	cd backend && go test -count=1 -p 1 -run 'TestConsumableAvailability' ./internal/reservation/
//
// Case 1: availability per bin after reserving, releasing and converting,
// filter location_id, cursor pagination, 404 and client scope. Case 2: the
// conversion of a quantity reservation into one retrieval. Case 3:
// reservations racing retrievals of POST /api/v1/stock-movements. Case 4:
// the alias inventory/availability. Case 5: the feature stock (E-44) and
// the permission stock:write (E-138).

const problemFeatureNotEntitled = "https://reticora.io/problems/feature-not-entitled"

// stockFeature is an entitlement checker that grants every feature, the
// feature stock only with stock.
type stockFeature struct{ stock bool }

func (c stockFeature) IsEnabled(_ context.Context, _, feature string) bool {
	return feature != "stock" || c.stock
}

// refusingTaker books every movement through the movement service but
// refuses the retrieval out of a reservation (case 2).
type refusingTaker struct{ movements *movement.Service }

func (b refusingTaker) RecordTx(ctx context.Context, tx pgx.Tx, m *movement.Movement) error {
	return b.movements.RecordTx(ctx, tx, m)
}

func (refusingTaker) RecordReservedRetrievalTx(context.Context, pgx.Tx, *movement.Movement) error {
	return errBookingRefused
}

// binRow is one row of GET /api/v1/consumables/{id}/availability.
type binRow struct {
	LocationID string  `json:"location_id"`
	OnHand     float64 `json:"on_hand"`
	Reserved   float64 `json:"reserved"`
	Available  float64 `json:"available"`
}

// binPage is the answer of GET /api/v1/consumables/{id}/availability.
type binPage struct {
	Data       []binRow `json:"data"`
	Total      int      `json:"total"`
	NextCursor string   `json:"next_cursor"`
}

// availFixture runs the reservation, availability and movement routes.
type availFixture struct {
	*resFixture
	avail  *chi.Mux
	failed *chi.Mux
	// denied runs the routes for an organization without the feature
	// stock (E-44).
	denied *chi.Mux
}

func newAvailFixture(t *testing.T, f *scopetest.Fixture) *availFixture {
	t.Helper()
	x := newResFixture(t, f)
	movements := movement.NewService(movement.NewPGRepository(f.App))
	wire := func(repo *reservation.PGRepository, stock bool) *chi.Mux {
		mux := chi.NewRouter()
		reservation.NewHandler(repo).WithAvailability(reservation.NewPGAvailability(f.App)).
			WithEntitlements(stockFeature{stock: stock}).RegisterRoutes(mux)
		movement.NewHandler(movements).RegisterRoutes(mux)
		return mux
	}
	failing := reservation.NewPGRepository(f.App).WithEvents(events.NewOutbox()).WithMovements(refusingTaker{movements: movements})
	return &availFixture{resFixture: x, avail: wire(x.repo, true), failed: wire(failing, true), denied: wire(x.repo, false)}
}

// writer is the org-wide context of organization A with a principal that
// holds stock:write.
func (x *availFixture) writer() context.Context {
	return identity.WithPrincipal(x.org(), identity.Principal{Subject: x.actor, OrganizationID: x.f.OrgA,
		Permissions: []identity.Permission{identity.PermStockWrite, identity.PermAssignmentWrite}})
}

// bins reads GET /api/v1/consumables/{id}/availability with query.
func (x *availFixture) bins(ctx context.Context, cons string, query url.Values) (*httptest.ResponseRecorder, binPage) {
	x.t.Helper()
	path := "/api/v1/consumables/" + cons + "/availability"
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	w := x.call(x.avail, ctx, http.MethodGet, path, "")
	var page binPage
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			x.t.Fatalf("availability %s: %v", w.Body.String(), err)
		}
	}
	return w, page
}

// binOf returns the availability of the bin.
func (x *availFixture) binOf(cons, bin string) binRow {
	x.t.Helper()
	w, page := x.bins(x.org(), cons, url.Values{"location_id": {bin}})
	if w.Code != http.StatusOK || len(page.Data) != 1 || page.Total != 1 || page.Data[0].LocationID != bin {
		x.t.Fatalf("availability of bin %s: %d %s, want one row", bin, w.Code, w.Body.String())
	}
	return page.Data[0]
}

// reserveQty reserves quantity of cons in bin and returns the reservation id.
func (x *availFixture) reserveQty(ctx context.Context, cons, bin string, quantity float64) string {
	x.t.Helper()
	w := x.call(x.avail, ctx, http.MethodPost, "/api/v1/reservations", reserveBody(map[string]any{
		"consumable_id": cons, "location_id": bin, "quantity": quantity, "project_ref": "wp759"}))
	if w.Code != http.StatusCreated {
		x.t.Fatalf("reserve %v of %s: %d %s", quantity, cons, w.Code, w.Body.String())
	}
	return decodeID(x.t, w)
}

// stockedTotal is stocked with on_hand_total the sum of on_hand over the
// bins, as the bookings keep it (WP-285).
func stockedTotal(t *testing.T, f *scopetest.Fixture, clientID, name string, onHand float64, bins ...string) string {
	t.Helper()
	id := stocked(t, f, clientID, name, onHand, bins...)
	if _, err := f.Admin.Exec(context.Background(), `UPDATE consumable SET on_hand_total = $2 WHERE id = $1`,
		id, onHand*float64(len(bins))); err != nil {
		t.Fatalf("on_hand_total of %s: %v", name, err)
	}
	return id
}

// retrieve books a retrieval of quantity from bin over POST
// /api/v1/stock-movements.
func (x *availFixture) retrieve(cons, bin string, quantity float64) *httptest.ResponseRecorder {
	b, _ := json.Marshal(map[string]any{"item_kind": "consumable", "consumable_id": cons, "movement_type": "retrieval",
		"from_location_id": bin, "quantity": quantity})
	return x.call(x.avail, x.writer(), http.MethodPost, "/api/v1/stock-movements", string(b))
}

func requireBin(t *testing.T, got binRow, onHand, reserved float64) {
	t.Helper()
	if got.OnHand != onHand || got.Reserved != reserved || got.Available != onHand-reserved {
		t.Fatalf("bin %+v, want on_hand %v, reserved %v, available %v", got, onHand, reserved, onHand-reserved)
	}
}

func TestConsumableAvailability(t *testing.T) {
	f := scopetest.Seed(t, "2e")
	x := newAvailFixture(t, f)
	b := newBranch(t, f, f.Client1, "wp759")

	t.Run("1 availability per bin", func(t *testing.T) {
		second := addBin(t, f, b.shelf, "wp759-second")
		third := addBin(t, f, b.shelf, "wp759-third")
		cons := stockedTotal(t, f, f.Client1, "wp759-bins", 10, b.bin, second, third)
		requireBin(t, x.binOf(cons, b.bin), 10, 0)

		released := x.reserveQty(x.org(), cons, b.bin, 4)
		kept := x.reserveQty(x.org(), cons, b.bin, 3)
		requireBin(t, x.binOf(cons, b.bin), 10, 7)
		if w := x.call(x.avail, x.org(), http.MethodPost, "/api/v1/reservations/"+released+"/release", `{}`); w.Code != http.StatusOK {
			t.Fatalf("release: %d %s", w.Code, w.Body.String())
		}
		requireBin(t, x.binOf(cons, b.bin), 10, 3)
		if w := x.call(x.avail, x.writer(), http.MethodPost, "/api/v1/reservations/"+kept+"/convert", `{}`); w.Code != http.StatusOK {
			t.Fatalf("convert: %d %s", w.Code, w.Body.String())
		}
		requireBin(t, x.binOf(cons, b.bin), 7, 0)
		requireBin(t, x.binOf(cons, second), 10, 0)
		x.requireInvariant()

		// Without location_id every bin, ordered by bin, cursor paginated.
		want := []string{b.bin, second, third}
		slices.Sort(want)
		var got []string
		query := url.Values{"limit": {"2"}}
		for range 3 {
			w, page := x.bins(x.org(), cons, query)
			if w.Code != http.StatusOK || page.Total != 3 {
				t.Fatalf("page: %d %s, want total 3", w.Code, w.Body.String())
			}
			for _, row := range page.Data {
				got = append(got, row.LocationID)
			}
			if page.NextCursor == "" {
				break
			}
			query = url.Values{"limit": {"2"}, "cursor": {page.NextCursor}}
		}
		if !slices.Equal(got, want) {
			t.Fatalf("bins %v, want %v in bin order", got, want)
		}

		if w, _ := x.bins(x.org(), f.ID(), nil); w.Code != http.StatusNotFound {
			t.Fatalf("unknown consumable: %d %s, want 404", w.Code, w.Body.String())
		}
		if w, _ := x.bins(x.org(), cons, url.Values{"location_id": {"no-uuid"}}); w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("malformed location_id: %d %s, want 422", w.Code, w.Body.String())
		}
		if w, _ := x.bins(x.org(), cons, url.Values{"cursor": {"broken"}}); w.Code != http.StatusBadRequest {
			t.Fatalf("broken cursor: %d %s, want 400", w.Code, w.Body.String())
		}

		// A client-1 principal sees no bin of a client-2 article.
		other := newBranch(t, f, f.Client2, "wp759-c2")
		foreign := stockedTotal(t, f, f.Client2, "wp759-foreign", 5, other.bin)
		if w, _ := x.bins(x.client1(), foreign, nil); w.Code != http.StatusNotFound {
			t.Fatalf("client-2 article for client 1: %d %s, want 404", w.Code, w.Body.String())
		}
		if w, page := x.bins(x.client1(), cons, nil); w.Code != http.StatusOK || page.Total != 3 {
			t.Fatalf("own article for client 1: %d %s, want three bins", w.Code, w.Body.String())
		}
	})

	t.Run("2 conversion into a retrieval", func(t *testing.T) {
		cons := stockedTotal(t, f, f.Client1, "wp759-convert", 6, b.bin)
		id := x.reserveQty(x.org(), cons, b.bin, 4)

		for _, c := range []struct {
			name, body string
			status     int
		}{
			{"holder", `{"assignee_id":"` + x.actor + `"}`, http.StatusUnprocessableEntity},
			{"team", `{"team_id":"` + f.ID() + `"}`, http.StatusUnprocessableEntity},
		} {
			w := x.call(x.avail, x.writer(), http.MethodPost, "/api/v1/reservations/"+id+"/convert", c.body)
			if w.Code != c.status {
				t.Fatalf("%s: %d %s, want %d", c.name, w.Code, w.Body.String(), c.status)
			}
			requireNoRawText(t, w)
		}

		// A booking error rolls back: the reservation stays active, no
		// retrieval is written, the stock is unchanged.
		w := x.call(x.failed, x.writer(), http.MethodPost, "/api/v1/reservations/"+id+"/convert", `{}`)
		if w.Code < http.StatusInternalServerError {
			t.Fatalf("refused booking: %d %s, want 5xx", w.Code, w.Body.String())
		}
		requireNoRawText(t, w)
		if s := x.state(id); s != "active" {
			t.Fatalf("after the refused booking state %s, want active", s)
		}
		if m := x.consumableMovements(cons, "retrieval"); m != "0" {
			t.Fatalf("%s retrievals after the refused booking, want 0", m)
		}
		requireBin(t, x.binOf(cons, b.bin), 6, 4)

		w = x.call(x.avail, x.writer(), http.MethodPost, "/api/v1/reservations/"+id+"/convert", `{"notes":"wp759"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("convert: %d %s", w.Code, w.Body.String())
		}
		var out struct {
			Reservation reservation.Reservation `json:"reservation"`
			Movement    *movement.Movement      `json:"movement"`
			Assignment  json.RawMessage         `json:"assignment"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("convert answer %s: %v", w.Body.String(), err)
		}
		if out.Reservation.State != "consumed" || out.Movement == nil || out.Movement.MovementType != "retrieval" ||
			out.Movement.FromLocationID != b.bin || out.Movement.Quantity == nil || *out.Movement.Quantity != 4 || out.Assignment != nil {
			t.Fatalf("convert answer %s, want the consumed reservation and a retrieval of 4 from the bin", w.Body.String())
		}
		if s := x.state(id); s != "consumed" {
			t.Fatalf("state %s, want consumed", s)
		}
		if m := x.consumableMovements(cons, "retrieval"); m != "1" {
			t.Fatalf("%s retrievals, want 1", m)
		}
		if q := x.scalar(`SELECT quantity::float8::text FROM stock_movement WHERE consumable_id = $1 AND movement_type = 'retrieval'
			AND from_location_id = $2`, cons, b.bin); q != "4" {
			t.Fatalf("retrieval quantity %s, want 4", q)
		}
		requireBin(t, x.binOf(cons, b.bin), 2, 0)
		if total := x.scalar(`SELECT on_hand_total::float8::text FROM consumable WHERE id = $1`, cons); total != "2" {
			t.Fatalf("on_hand_total %s, want 2", total)
		}
		if c := x.eventClient(id, events.ReservationConverted); c != f.Client1 {
			t.Fatalf("reservation.converted client_id %q, want the client of the bin", c)
		}
		x.requireInvariant()

		// A reservation no longer active is 409.
		if w = x.call(x.avail, x.writer(), http.MethodPost, "/api/v1/reservations/"+id+"/convert", `{}`); w.Code != http.StatusConflict {
			t.Fatalf("second convert: %d %s, want 409", w.Code, w.Body.String())
		}
		requireNoRawText(t, w)
	})

	t.Run("3 reservations race retrievals", func(t *testing.T) {
		for round := range 4 {
			cons := stockedTotal(t, f, f.Client1, "wp759-race-"+string(rune('a'+round)), 5, b.bin)
			var wg sync.WaitGroup
			var reserved, retrieved *httptest.ResponseRecorder
			wg.Add(2)
			go func() {
				defer wg.Done()
				reserved = x.call(x.avail, x.org(), http.MethodPost, "/api/v1/reservations", reserveBody(map[string]any{
					"consumable_id": cons, "location_id": b.bin, "quantity": 5, "project_ref": "wp759"}))
			}()
			go func() {
				defer wg.Done()
				retrieved = x.retrieve(cons, b.bin, 5)
			}()
			wg.Wait()
			okReserve, okRetrieve := reserved.Code == http.StatusCreated, retrieved.Code == http.StatusCreated
			if okReserve == okRetrieve {
				t.Fatalf("reservation %d %s and retrieval %d %s: want exactly one to win", reserved.Code, reserved.Body.String(),
					retrieved.Code, retrieved.Body.String())
			}
			loser := reserved
			if !okRetrieve {
				loser = retrieved
			}
			if loser.Code != http.StatusConflict || problemType(loser) != problemInsufficientStock {
				t.Fatalf("loser %d %s, want 409 insufficient-stock", loser.Code, loser.Body.String())
			}
			if onHand, res := x.stockOf(cons, b.bin); res > onHand {
				t.Fatalf("reserved %v beyond on_hand %v", res, onHand)
			}
			x.requireInvariant()
		}

		// A retrieval without reservation takes only the available stock.
		cons := stockedTotal(t, f, f.Client1, "wp759-available", 10, b.bin)
		x.reserveQty(x.org(), cons, b.bin, 4)
		if w := x.retrieve(cons, b.bin, 7); w.Code != http.StatusConflict || problemType(w) != problemInsufficientStock {
			t.Fatalf("retrieval beyond available: %d %s, want 409 insufficient-stock", w.Code, w.Body.String())
		}
		if w := x.retrieve(cons, b.bin, 6); w.Code != http.StatusCreated {
			t.Fatalf("retrieval of the available stock: %d %s", w.Code, w.Body.String())
		}
		requireBin(t, x.binOf(cons, b.bin), 4, 4)

		// The conversion takes the reserved rest, parallel to a retrieval,
		// without deadlock.
		cons = stockedTotal(t, f, f.Client1, "wp759-convert-race", 5, b.bin)
		id := x.reserveQty(x.org(), cons, b.bin, 2)
		var wg sync.WaitGroup
		var converted, retrieved *httptest.ResponseRecorder
		wg.Add(2)
		go func() {
			defer wg.Done()
			converted = x.call(x.avail, x.writer(), http.MethodPost, "/api/v1/reservations/"+id+"/convert", `{}`)
		}()
		go func() {
			defer wg.Done()
			retrieved = x.retrieve(cons, b.bin, 3)
		}()
		wg.Wait()
		if converted.Code != http.StatusOK || retrieved.Code != http.StatusCreated {
			t.Fatalf("convert %d %s, retrieval %d %s: want both", converted.Code, converted.Body.String(),
				retrieved.Code, retrieved.Body.String())
		}
		requireBin(t, x.binOf(cons, b.bin), 0, 0)
		x.requireInvariant()
	})

	t.Run("4 alias inventory/availability", func(t *testing.T) {
		cons := stockedTotal(t, f, f.Client1, "wp759-alias", 8, b.bin)
		x.reserveQty(x.org(), cons, b.bin, 3)
		for _, kind := range []string{"consumable", "quantity_item"} {
			w := x.call(x.avail, x.org(), http.MethodGet, "/api/v1/inventory/availability?item_kind="+kind+"&item_id="+cons, "")
			var body struct {
				Data []reservation.Availability `json:"data"`
			}
			if w.Code != http.StatusOK {
				t.Fatalf("availability: %d %s", w.Code, w.Body.String())
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Data) != 1 {
				t.Fatalf("availability %s: %s (%v), want one row", kind, w.Body.String(), err)
			}
			if a := body.Data[0]; a.ItemKind != "consumable" || a.Total != 8 || a.Reserved != 3 || a.Available != 5 {
				t.Fatalf("availability %s: %+v, want consumable with total 8, reserved 3, available 5", kind, a)
			}
		}
	})

	t.Run("5 feature stock and stock:write", func(t *testing.T) {
		cons := stockedTotal(t, f, f.Client1, "wp759-perm", 5, b.bin)
		id := x.reserveQty(x.org(), cons, b.bin, 1)
		withoutStockWrite := identity.WithPrincipal(x.org(), identity.Principal{Subject: x.actor, OrganizationID: f.OrgA,
			Permissions: []identity.Permission{identity.PermAssignmentWrite}})
		w := x.call(x.avail, withoutStockWrite, http.MethodPost, "/api/v1/reservations/"+id+"/convert", `{}`)
		if w.Code != http.StatusForbidden {
			t.Fatalf("convert without stock:write: %d %s, want 403", w.Code, w.Body.String())
		}

		// Without the feature stock the quantity reservation and its
		// conversion are 403 feature-not-entitled; the asset reservation
		// passes.
		w = x.call(x.denied, x.org(), http.MethodPost, "/api/v1/reservations", reserveBody(map[string]any{
			"consumable_id": cons, "location_id": b.bin, "quantity": 1, "project_ref": "wp759"}))
		if w.Code != http.StatusForbidden || problemType(w) != problemFeatureNotEntitled {
			t.Fatalf("quantity reservation without stock: %d %s, want 403 feature-not-entitled", w.Code, w.Body.String())
		}
		w = x.call(x.denied, x.writer(), http.MethodPost, "/api/v1/reservations/"+id+"/convert", `{}`)
		if w.Code != http.StatusForbidden || problemType(w) != problemFeatureNotEntitled {
			t.Fatalf("conversion without stock: %d %s, want 403 feature-not-entitled", w.Code, w.Body.String())
		}
		if s := x.state(id); s != "active" {
			t.Fatalf("state %s after the refusals, want active", s)
		}
		requireBin(t, x.binOf(cons, b.bin), 5, 1)
		asset := f.Asset(t, f.OrgA, f.Client1, "wp759-asset")
		w = x.call(x.denied, x.org(), http.MethodPost, "/api/v1/reservations", reserveBody(map[string]any{
			"asset_id": asset, "project_ref": "wp759"}))
		if w.Code != http.StatusCreated {
			t.Fatalf("asset reservation without stock: %d %s, want 201", w.Code, w.Body.String())
		}
		x.requireInvariant()
	})
}
