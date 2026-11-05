package reservation_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reservation"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Tests of WP-284 (STK-03, E-40, E-09) against PostgreSQL
// (TEST_DATABASE_URL, scopetest.Seed):
//
//	cd backend && go test -count=1 -p 1 -run 'TestConsumableReservation' ./internal/reservation/
//
//   - TestConsumableReservation: cases 1 to 4 and 7 of the plan – parallel
//     reservations on one bin never beyond on_hand (409 insufficient-stock),
//     release and expiry with one movement release each and the events of
//     the bin's client, expiry parallel to a release without deadlock, the
//     error contract of the handler and the availability projection.
//   - TestConsumableReservationScope: cases 4 and 5 – client and site of
//     the bin decide visibility and search hits, an org-wide article in an
//     own bin is reservable, a moved bin takes its reservations along.
//   - TestConsumableReservationMigration: case 6 – the data migration of
//     000264 in a rolled-back transaction.

const (
	problemInsufficientStock = "https://reticora.io/problems/insufficient-stock"
	reservationMigration     = "../../migrations/000264_reservation_consumable_bin"
	// quantityItemDropMigration drops quantity_item (WP-758); the replay
	// of 000264 rebuilds it first with its down part.
	quantityItemDropMigration = "../../migrations/000269_quantity_item_drop"
)

// branch is a storage branch site > warehouse > zone > shelf > bin.
type branch struct {
	site, zone, shelf, bin string
}

// newBranch creates a storage branch of clientID through the location
// repository of an org-wide principal.
func newBranch(t *testing.T, f *scopetest.Fixture, clientID, name string) branch {
	t.Helper()
	repo := locations.NewPGRepository(f.App)
	ids := map[locations.Kind]string{}
	parent := ""
	for _, kind := range []locations.Kind{locations.KindSite, locations.KindWarehouse, locations.KindZone, locations.KindShelf, locations.KindBin} {
		req := locations.CreateRequest{Kind: kind, ParentID: parent, Name: name + "-" + string(kind)}
		if kind == locations.KindSite {
			req.ClientID = clientID
		}
		l, err := repo.Create(f.OrgCtx(f.OrgA), f.OrgA, req)
		if err != nil {
			t.Fatalf("create %s %s: %v", kind, name, err)
		}
		parent, ids[kind] = l.ID, l.ID
	}
	return branch{site: ids[locations.KindSite], zone: ids[locations.KindZone], shelf: ids[locations.KindShelf], bin: ids[locations.KindBin]}
}

// addBin creates one more bin on shelf.
func addBin(t *testing.T, f *scopetest.Fixture, shelf, name string) string {
	t.Helper()
	l, err := locations.NewPGRepository(f.App).Create(f.OrgCtx(f.OrgA), f.OrgA,
		locations.CreateRequest{Kind: locations.KindBin, ParentID: shelf, Name: name})
	if err != nil {
		t.Fatalf("create bin %s: %v", name, err)
	}
	return l.ID
}

// stocked creates a consumable of clientID (empty: org-wide) with on_hand
// in each of bins.
func stocked(t *testing.T, f *scopetest.Fixture, clientID, name string, onHand float64, bins ...string) string {
	t.Helper()
	c := &consumable.Consumable{OrganizationID: f.OrgA, ClientID: clientID, Name: name}
	if err := consumable.NewPGRepository(f.App).Create(f.OrgCtx(f.OrgA), c); err != nil {
		t.Fatalf("create consumable %s: %v", name, err)
	}
	for _, bin := range bins {
		if _, err := f.Admin.Exec(context.Background(), `INSERT INTO stock_level (organization_id, consumable_id, location_id, on_hand)
			VALUES ($1, $2, $3, $4)`, f.OrgA, c.ID, bin, onHand); err != nil {
			t.Fatalf("stock of %s: %v", name, err)
		}
	}
	return c.ID
}

// reserveBody is the request body of a consumable reservation.
func reserveBody(fields map[string]any) string {
	b, _ := json.Marshal(fields)
	return string(b)
}

// stockOf returns on_hand and reserved of the stock row.
func (x *resFixture) stockOf(cons, bin string) (onHand, reserved float64) {
	x.t.Helper()
	if err := x.f.Admin.QueryRow(context.Background(), `SELECT on_hand::float8, reserved::float8 FROM stock_level
		WHERE consumable_id = $1 AND location_id = $2`, cons, bin).Scan(&onHand, &reserved); err != nil {
		x.t.Fatalf("stock of %s in %s: %v", cons, bin, err)
	}
	return onHand, reserved
}

// consumableMovements counts the ledger rows typ of the consumable.
func (x *resFixture) consumableMovements(cons, typ string) string {
	return x.scalar(`SELECT count(*)::text FROM stock_movement WHERE consumable_id = $1 AND movement_type = $2`, cons, typ)
}

// requireInvariant fails when a stock row of organization A has reserved
// other than the sum of the active quantities of its reservations.
func (x *resFixture) requireInvariant() {
	x.t.Helper()
	if n := x.scalar(`SELECT count(*)::text FROM stock_level s
		WHERE s.organization_id = $1 AND s.reserved <> COALESCE((SELECT sum(r.quantity) FROM reservation r
			WHERE r.consumable_id = s.consumable_id AND r.location_id = s.location_id AND r.state = 'active'), 0)`, x.f.OrgA); n != "0" {
		x.t.Fatalf("%s stock rows with reserved other than the sum of their active reservations", n)
	}
}

// requireNoRawText fails when the answer carries database text.
func requireNoRawText(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	body := w.Body.String()
	for _, raw := range []string{"stock_level", "reservation_", "SQLSTATE", "violates", "ERROR:", "pgx"} {
		if strings.Contains(body, raw) {
			t.Fatalf("answer %d carries database text %q: %s", w.Code, raw, body)
		}
	}
}

// decodeReservation decodes the reservation of an answer.
func decodeReservation(t *testing.T, w *httptest.ResponseRecorder) reservation.Reservation {
	t.Helper()
	var res reservation.Reservation
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || res.ID == "" {
		t.Fatalf("answer %d %s is no reservation (%v)", w.Code, w.Body.String(), err)
	}
	return res
}

func TestConsumableReservation(t *testing.T) {
	f := scopetest.Seed(t, "2e")
	x := newResFixture(t, f)
	b := newBranch(t, f, f.Client1, "wp284")
	post := func(ctx context.Context, fields map[string]any) *httptest.ResponseRecorder {
		return x.call(x.mux, ctx, http.MethodPost, "/api/v1/reservations", reserveBody(fields))
	}

	t.Run("1 parallel reservations never beyond on_hand", func(t *testing.T) {
		cons := stocked(t, f, f.Client1, "wp284-parallel", 10, b.bin)
		const n = 8
		answers := make([]*httptest.ResponseRecorder, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				answers[i] = post(x.org(), map[string]any{"consumable_id": cons, "location_id": b.bin, "quantity": 3, "project_ref": "wp284"})
			}()
		}
		wg.Wait()
		created := 0
		for _, w := range answers {
			switch {
			case w.Code == http.StatusCreated:
				created++
			case w.Code == http.StatusConflict && problemType(w) == problemInsufficientStock:
				requireNoRawText(t, w)
			default:
				t.Fatalf("answer %d %s, want 201 or 409 insufficient-stock", w.Code, w.Body.String())
			}
		}
		if created != 3 {
			t.Fatalf("%d reservations of 3 on a stock of 10, want 3", created)
		}
		if active := x.scalar(`SELECT COALESCE(sum(quantity), 0)::float8::text FROM reservation WHERE consumable_id = $1 AND state = 'active'`, cons); active != "9" {
			t.Fatalf("active quantity %s, want 9", active)
		}
		if onHand, reserved := x.stockOf(cons, b.bin); onHand != 10 || reserved != 9 {
			t.Fatalf("stock on_hand %v reserved %v, want 10 and 9", onHand, reserved)
		}
		if m := x.consumableMovements(cons, "reservation"); m != "3" {
			t.Fatalf("%s reservation movements, want 3", m)
		}
		x.requireInvariant()
	})

	t.Run("2 release and expiry", func(t *testing.T) {
		cons := stocked(t, f, "", "wp284-release", 5, b.bin)
		w := post(x.org(), map[string]any{"consumable_id": cons, "location_id": b.bin, "quantity": 2, "project_ref": "wp284"})
		if w.Code != http.StatusCreated {
			t.Fatalf("reserve: %d %s", w.Code, w.Body.String())
		}
		res := decodeReservation(t, w)
		if res.ItemKind != "consumable" || res.ConsumableID != cons || res.LocationID != b.bin || res.ClientID != f.Client1 || res.SiteID != b.site {
			t.Fatalf("reservation %+v, want consumable %s in bin %s of client 1 and its site", res, cons, b.bin)
		}
		var payload struct {
			ConsumableID string  `json:"consumable_id"`
			LocationID   string  `json:"location_id"`
			Quantity     float64 `json:"quantity"`
			ClientID     string  `json:"client_id"`
			SiteID       string  `json:"site_id"`
		}
		raw := x.scalar(`SELECT data::text FROM event_outbox WHERE entity_id = $1 AND type = $2`, res.ID, events.ReservationCreated)
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			t.Fatalf("reservation.created: %v", err)
		}
		if payload.ConsumableID != cons || payload.LocationID != b.bin || payload.Quantity != 2 || payload.ClientID != f.Client1 || payload.SiteID != b.site {
			t.Fatalf("reservation.created payload %+v", payload)
		}
		if w = x.call(x.mux, x.org(), http.MethodPost, "/api/v1/reservations/"+res.ID+"/release", `{}`); w.Code != http.StatusOK {
			t.Fatalf("release: %d %s", w.Code, w.Body.String())
		}
		if m := x.consumableMovements(cons, "release"); m != "1" {
			t.Fatalf("%s release movements, want 1", m)
		}
		if onHand, reserved := x.stockOf(cons, b.bin); onHand != 5 || reserved != 0 {
			t.Fatalf("after the release on_hand %v reserved %v, want 5 and 0", onHand, reserved)
		}
		if c := x.eventClient(res.ID, events.ReservationReleased); c != f.Client1 {
			t.Fatalf("reservation.released client_id %q, want the client of the bin", c)
		}
		if w = x.call(x.mux, x.org(), http.MethodPost, "/api/v1/reservations/"+res.ID+"/release", `{}`); w.Code != http.StatusConflict {
			t.Fatalf("second release: %d %s, want 409", w.Code, w.Body.String())
		}
		requireNoRawText(t, w)
		x.requireInvariant()

		w = post(x.org(), map[string]any{"consumable_id": cons, "location_id": b.bin, "quantity": 4,
			"reserved_until": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), "project_ref": "wp284"})
		if w.Code != http.StatusCreated {
			t.Fatalf("reserve due: %d %s", w.Code, w.Body.String())
		}
		due := decodeID(t, w)
		if err := reservation.NewSweeper(x.repo, nil).SweepOnce(context.Background()); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if s := x.state(due); s != "expired" {
			t.Fatalf("due reservation %s, want expired", s)
		}
		if m := x.consumableMovements(cons, "release"); m != "2" {
			t.Fatalf("%s release movements after the expiry, want 2", m)
		}
		if onHand, reserved := x.stockOf(cons, b.bin); onHand != 5 || reserved != 0 {
			t.Fatalf("after the expiry on_hand %v reserved %v, want 5 and 0", onHand, reserved)
		}
		if c := x.eventClient(due, events.ReservationExpired); c != f.Client1 {
			t.Fatalf("reservation.expired client_id %q, want the client of the bin", c)
		}
		if n := x.scalar(`SELECT count(*)::text FROM audit_log WHERE organization_id = $1 AND resource_id = $2
				AND action = 'reservations.expire' AND source = 'system'`, f.OrgA, due); n != "1" {
			t.Fatalf("%s audit entries reservations.expire, want 1", n)
		}
		x.requireInvariant()
	})

	t.Run("2 expiry parallel to release", func(t *testing.T) {
		cons := stocked(t, f, f.Client1, "wp284-race", 50, b.bin)
		for i := range 5 {
			w := post(x.org(), map[string]any{"consumable_id": cons, "location_id": b.bin, "quantity": 1,
				"reserved_until": time.Now().Add(-time.Second).UTC().Format(time.RFC3339), "project_ref": "wp284-" + strconv.Itoa(i)})
			if w.Code != http.StatusCreated {
				t.Fatalf("reserve: %d %s", w.Code, w.Body.String())
			}
			id := decodeID(t, w)
			before := x.consumableMovements(cons, "release")
			var wg sync.WaitGroup
			var sweepErr error
			var released *httptest.ResponseRecorder
			wg.Add(2)
			go func() {
				defer wg.Done()
				sweepErr = reservation.NewSweeper(x.repo, nil).SweepOnce(context.Background())
			}()
			go func() {
				defer wg.Done()
				released = x.call(x.mux, x.org(), http.MethodPost, "/api/v1/reservations/"+id+"/release", `{}`)
			}()
			wg.Wait()
			if sweepErr != nil {
				t.Fatalf("sweep: %v", sweepErr)
			}
			switch s := x.state(id); {
			case s == "released" && released.Code == http.StatusOK:
			case s == "expired" && released.Code == http.StatusConflict:
			default:
				t.Fatalf("state %s with release %d %s: want released/200 or expired/409", s, released.Code, released.Body.String())
			}
			beforeN, _ := strconv.Atoi(before)
			if after := x.consumableMovements(cons, "release"); after != strconv.Itoa(beforeN+1) {
				t.Fatalf("%s release movements after %s, want one more", after, before)
			}
			x.requireInvariant()
		}
	})

	t.Run("3 error contract", func(t *testing.T) {
		cons := stocked(t, f, f.Client1, "wp284-errors", 2, b.bin)
		two := stocked(t, f, f.Client1, "wp284-two-bins", 2, b.bin, addBin(t, f, b.shelf, "wp284-second-bin"))
		for _, c := range []struct {
			name   string
			fields map[string]any
			status int
			field  string
			typ    string
		}{
			{"unknown consumable", map[string]any{"consumable_id": f.ID(), "location_id": b.bin}, http.StatusNotFound, "", ""},
			{"unknown consumable without bin", map[string]any{"consumable_id": f.ID()}, http.StatusNotFound, "", ""},
			{"unknown bin", map[string]any{"consumable_id": cons, "location_id": f.ID()}, http.StatusNotFound, "", ""},
			{"location is no bin", map[string]any{"consumable_id": cons, "location_id": b.shelf}, http.StatusUnprocessableEntity, "location_id", ""},
			{"shortfall", map[string]any{"consumable_id": cons, "location_id": b.bin, "quantity": 3}, http.StatusConflict, "", problemInsufficientStock},
			{"two bins without location_id", map[string]any{"quantity_item_id": two}, http.StatusUnprocessableEntity, "location_id", ""},
			{"asset and consumable", map[string]any{"asset_id": f.ID(), "consumable_id": cons}, http.StatusUnprocessableEntity, "consumable_id", ""},
		} {
			w := post(x.org(), c.fields)
			if w.Code != c.status {
				t.Fatalf("%s: %d %s, want %d", c.name, w.Code, w.Body.String(), c.status)
			}
			var p struct {
				Field string `json:"field"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &p)
			if c.field != "" && p.Field != c.field {
				t.Fatalf("%s: field %q, want %q", c.name, p.Field, c.field)
			}
			if c.typ != "" && problemType(w) != c.typ {
				t.Fatalf("%s: type %q, want %q", c.name, problemType(w), c.typ)
			}
			requireNoRawText(t, w)
		}
		if _, reserved := x.stockOf(cons, b.bin); reserved != 0 {
			t.Fatalf("refused reservations left reserved %v", reserved)
		}
		// The deprecated alias with the only bin of the article.
		w := post(x.org(), map[string]any{"item_kind": "quantity_item", "quantity_item_id": cons, "quantity": 2})
		if w.Code != http.StatusCreated {
			t.Fatalf("alias quantity_item_id: %d %s, want 201", w.Code, w.Body.String())
		}
		if res := decodeReservation(t, w); res.ItemKind != "consumable" || res.ConsumableID != cons || res.LocationID != b.bin {
			t.Fatalf("alias reservation %+v, want consumable %s in its only bin", res, cons)
		}
		x.requireInvariant()
	})

	t.Run("7 availability", func(t *testing.T) {
		second := addBin(t, f, b.shelf, "wp284-avail-bin")
		cons := stocked(t, f, f.Client1, "wp284-avail", 4, b.bin, second)
		if w := post(x.org(), map[string]any{"consumable_id": cons, "location_id": second, "quantity": 3}); w.Code != http.StatusCreated {
			t.Fatalf("reserve: %d %s", w.Code, w.Body.String())
		}
		mux := chi.NewRouter()
		reservation.NewHandler(x.repo).WithAvailability(reservation.NewPGAvailability(f.App)).RegisterRoutes(mux)
		for _, kind := range []string{"consumable", "quantity_item"} {
			w := x.call(mux, x.org(), http.MethodGet, "/api/v1/inventory/availability?item_kind="+kind+"&item_id="+cons, "")
			if w.Code != http.StatusOK {
				t.Fatalf("availability: %d %s", w.Code, w.Body.String())
			}
			var body struct {
				Data []reservation.Availability `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Data) != 1 {
				t.Fatalf("availability %s: %s (%v), want one row", kind, w.Body.String(), err)
			}
			if a := body.Data[0]; a.ItemKind != "consumable" || a.ItemID != cons || a.Total != 8 || a.Reserved != 3 || a.Available != 5 {
				t.Fatalf("availability %s: %+v, want consumable with total 8, reserved 3, available 5", kind, a)
			}
		}
	})
}

func TestConsumableReservationScope(t *testing.T) {
	f := scopetest.Seed(t, "2e")
	x := newResFixture(t, f)
	bg := context.Background()
	own := newBranch(t, f, f.Client1, "wp284-own")
	otherSite := newBranch(t, f, f.Client1, "wp284-other-site")
	foreign := newBranch(t, f, f.Client2, "wp284-foreign")
	post := func(ctx context.Context, fields map[string]any) *httptest.ResponseRecorder {
		return x.call(x.mux, ctx, http.MethodPost, "/api/v1/reservations", reserveBody(fields))
	}
	siteCtx := func(site string) context.Context {
		scope := database.OrgWideScope(f.OrgA, x.actor)
		scope.Sites = database.ScopeIDs(site)
		return tenant.WithTenant(database.ContextWithTenantScope(bg, &scope), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: x.actor})
	}
	visible := func(ctx context.Context) map[string]bool {
		t.Helper()
		list, _, err := x.repo.List(ctx, f.OrgA, "", api.PaginationParams{Limit: 200})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		out := map[string]bool{}
		for i := range list {
			out[list[i].ID] = true
		}
		return out
	}
	scopeOf := func(table, id string) (client, site string) {
		t.Helper()
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(client_id::text, ''), COALESCE(site_id::text, '') FROM `+table+
			` WHERE `+map[string]string{"reservation": "id", "search_document": "entity_id"}[table]+` = $1`, id).Scan(&client, &site); err != nil {
			t.Fatalf("scope of %s %s: %v", table, id, err)
		}
		return client, site
	}

	ownItem := stocked(t, f, f.Client1, "wp284-own-item", 10, own.bin, otherSite.bin)
	orgWide := stocked(t, f, "", "wp284-orgwide", 10, own.bin, foreign.bin)
	foreignItem := stocked(t, f, f.Client2, "wp284-foreign-item", 10, foreign.bin)

	// Case 5: client 1 reserves neither a client-2 article nor an org-wide
	// one in a client-2 bin; an org-wide article in its own bin it does.
	for name, fields := range map[string]map[string]any{
		"client-2 article":                {"consumable_id": foreignItem, "location_id": foreign.bin},
		"client-2 article without bin":    {"consumable_id": foreignItem},
		"org-wide article, client-2 bin":  {"consumable_id": orgWide, "location_id": foreign.bin},
		"own article in a client-2 bin":   {"consumable_id": ownItem, "location_id": foreign.bin},
		"client-2 article in its own bin": {"consumable_id": foreignItem, "location_id": own.bin},
	} {
		w := post(x.client1(), fields)
		if w.Code != http.StatusNotFound {
			t.Fatalf("client 1, %s: %d %s, want 404", name, w.Code, w.Body.String())
		}
		requireNoRawText(t, w)
	}
	w := post(x.client1(), map[string]any{"consumable_id": orgWide, "location_id": own.bin, "quantity": 2})
	if w.Code != http.StatusCreated {
		t.Fatalf("client 1, org-wide article in its own bin: %d %s, want 201", w.Code, w.Body.String())
	}
	orgWideOwn := decodeReservation(t, w)
	if orgWideOwn.ClientID != f.Client1 || orgWideOwn.SiteID != own.site {
		t.Fatalf("reservation of the org-wide article: client %q site %q, want client 1 and the bin's site", orgWideOwn.ClientID, orgWideOwn.SiteID)
	}
	reserveOrg := func(item, bin string) string {
		t.Helper()
		ow := post(x.org(), map[string]any{"consumable_id": item, "location_id": bin, "quantity": 1, "project_ref": "wp284-scope"})
		if ow.Code != http.StatusCreated {
			t.Fatalf("org reservation of %s in %s: %d %s", item, bin, ow.Code, ow.Body.String())
		}
		return decodeID(t, ow)
	}
	orgWideForeign := reserveOrg(orgWide, foreign.bin)
	foreignRes := reserveOrg(foreignItem, foreign.bin)
	ownRes := reserveOrg(ownItem, own.bin)
	otherSiteRes := reserveOrg(ownItem, otherSite.bin)
	if c, s := scopeOf("reservation", orgWideForeign); c != f.Client2 || s != foreign.site {
		t.Fatalf("org-wide article in the client-2 bin: client %q site %q, want client 2 and its site", c, s)
	}

	got := visible(x.client1())
	for id, want := range map[string]bool{orgWideOwn.ID: true, ownRes: true, otherSiteRes: true, orgWideForeign: false, foreignRes: false} {
		if got[id] != want {
			t.Errorf("client 1 sees reservation %s: %v, want %v", id, got[id], want)
		}
	}
	if w = x.call(x.mux, x.client1(), http.MethodPost, "/api/v1/reservations/"+foreignRes+"/release", `{}`); w.Code != http.StatusNotFound {
		t.Fatalf("client-1 release of a client-2 reservation: %d %s, want 404", w.Code, w.Body.String())
	}

	// Case 4: a site-scoped principal sees only the reservations in bins
	// of its site.
	got = visible(siteCtx(own.site))
	if !got[ownRes] || !got[orgWideOwn.ID] || got[otherSiteRes] || got[foreignRes] {
		t.Fatalf("site principal sees %v, want the reservations of its bins only", got)
	}
	if w = post(siteCtx(own.site), map[string]any{"consumable_id": ownItem, "location_id": otherSite.bin}); w.Code != http.StatusNotFound {
		t.Fatalf("site principal reserving in a bin of another site: %d %s, want 404", w.Code, w.Body.String())
	}

	// Search rows follow the client and site of the bin.
	if _, err := search.NewPGRepository(f.App).ReindexTenant(f.OrgCtx(f.OrgA), f.OrgA); err != nil {
		t.Fatalf("reindex: %v", err)
	}
	found := func(scope *database.TenantScope, ids ...string) map[string]bool {
		t.Helper()
		out := map[string]bool{}
		if err := database.WithTenant(bg, f.App, scope, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT entity_id::text FROM search_document WHERE entity_type = 'reservation' AND entity_id = ANY($1::uuid[])`, ids)
			if err != nil {
				return err
			}
			hits, err := pgx.CollectRows(rows, pgx.RowTo[string])
			for _, h := range hits {
				out[h] = true
			}
			return err
		}); err != nil {
			t.Fatalf("search rows: %v", err)
		}
		return out
	}
	client1 := database.OrgWideScope(f.OrgA, x.actor)
	client1.Clients = database.ScopeIDs(f.Client1)
	if hits := found(&client1, orgWideOwn.ID, orgWideForeign, foreignRes); !hits[orgWideOwn.ID] || hits[orgWideForeign] || hits[foreignRes] {
		t.Fatalf("client 1 finds %v, want only the reservation in its own bin", hits)
	}
	site := database.OrgWideScope(f.OrgA, x.actor)
	site.Sites = database.ScopeIDs(own.site)
	if hits := found(&site, ownRes, otherSiteRes); !hits[ownRes] || hits[otherSiteRes] {
		t.Fatalf("site principal finds %v, want only the reservation of its site", hits)
	}

	// Moved bins take their reservations and search rows along: the bin of
	// the other site moves into the own site (E-40), the own bin with the
	// org-wide article under a client-2 shelf.
	locs := locations.NewPGRepository(f.App)
	if _, err := locs.Move(f.OrgCtx(f.OrgA), f.OrgA, otherSite.bin, own.shelf); err != nil {
		t.Fatalf("move bin to the own site: %v", err)
	}
	if _, s := scopeOf("reservation", otherSiteRes); s != own.site {
		t.Fatalf("reservation site after the bin moved: %q, want the own site", s)
	}
	if _, s := scopeOf("search_document", otherSiteRes); s != own.site {
		t.Fatalf("search row site after the bin moved: %q, want the own site", s)
	}
	if !visible(siteCtx(own.site))[otherSiteRes] {
		t.Fatal("site principal does not see the reservation of the bin moved into its site")
	}
	orgWideBin := addBin(t, f, own.shelf, "wp284-orgwide-bin")
	if _, err := f.Admin.Exec(bg, `INSERT INTO stock_level (organization_id, consumable_id, location_id, on_hand) VALUES ($1, $2, $3, 3)`,
		f.OrgA, orgWide, orgWideBin); err != nil {
		t.Fatal(err)
	}
	movedRes := reserveOrg(orgWide, orgWideBin)
	if _, err := search.NewPGRepository(f.App).ReindexTenant(f.OrgCtx(f.OrgA), f.OrgA); err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if _, err := locs.Move(f.OrgCtx(f.OrgA), f.OrgA, orgWideBin, foreign.shelf); err != nil {
		t.Fatalf("move bin to client 2: %v", err)
	}
	if c, s := scopeOf("reservation", movedRes); c != f.Client2 || s != foreign.site {
		t.Fatalf("reservation after the bin moved to client 2: client %q site %q", c, s)
	}
	if c, _ := scopeOf("search_document", movedRes); c != f.Client2 {
		t.Fatalf("search row client after the bin moved to client 2: %q", c)
	}
	if visible(x.client1())[movedRes] {
		t.Fatal("client 1 still sees the reservation of the bin moved to client 2")
	}

	// A site change of a location runs without error
	// (location_refresh_asset_site without quantity_item since 000269).
	if _, err := locs.Move(f.OrgCtx(f.OrgA), f.OrgA, otherSite.shelf, own.zone); err != nil {
		t.Fatalf("site change of a location: %v", err)
	}
	x.requireInvariant()
}

func TestConsumableReservationMigration(t *testing.T) {
	f := scopetest.Seed(t, "2e")
	bg := context.Background()
	up, err := os.ReadFile(reservationMigration + ".up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile(reservationMigration + ".down.sql")
	if err != nil {
		t.Fatal(err)
	}
	dropDown, err := os.ReadFile(quantityItemDropMigration + ".down.sql")
	if err != nil {
		t.Fatal(err)
	}
	b := newBranch(t, f, f.Client1, "mig264")
	second := addBin(t, f, b.shelf, "mig264-second")

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
	scalar := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if scanErr := tx.QueryRow(bg, sql, args...).Scan(&s); scanErr != nil {
			t.Fatalf("%s: %v", sql, scanErr)
		}
		return s
	}
	exec(string(dropDown))
	exec(string(down))

	// Articles merged with the same id (000256): one bin, two bins, none.
	oneBin, twoBins, noBin := f.ID(), f.ID(), f.ID()
	for _, id := range []string{oneBin, twoBins, noBin} {
		exec(`INSERT INTO quantity_item (id, organization_id, client_id, name) VALUES ($1, $2, $3, $4)`, id, f.OrgA, f.Client1, "mig264-"+id)
		exec(`INSERT INTO consumable (id, organization_id, client_id, name) VALUES ($1, $2, $3, $4)`, id, f.OrgA, f.Client1, "mig264-"+id)
	}
	exec(`INSERT INTO stock_level (organization_id, consumable_id, location_id, on_hand) VALUES ($1, $2, $3, 5)`, f.OrgA, oneBin, b.bin)
	for _, bin := range []string{b.bin, second} {
		exec(`INSERT INTO stock_level (organization_id, consumable_id, location_id, on_hand) VALUES ($1, $2, $3, 5)`, f.OrgA, twoBins, bin)
	}
	older, younger, done, ambiguous, homeless := f.ID(), f.ID(), f.ID(), f.ID(), f.ID()
	start := time.Now().Add(-time.Hour)
	for i, r := range []struct {
		id, item, state string
		quantity        float64
	}{
		{older, oneBin, "active", 3}, {younger, oneBin, "active", 3}, {done, oneBin, "released", 4},
		{ambiguous, twoBins, "active", 1}, {homeless, noBin, "active", 1},
	} {
		exec(`INSERT INTO reservation (id, organization_id, item_kind, quantity_item_id, quantity, state, created_at)
			VALUES ($1, $2, 'quantity_item', $3, $4, $5, $6)`, r.id, f.OrgA, r.item, r.quantity, r.state, start.Add(time.Duration(i)*time.Minute))
	}

	exec(string(up))
	for _, c := range []struct{ id, state string }{{older, "active"}, {younger, "released"}, {done, "released"}} {
		got := scalar(`SELECT item_kind || '/' || consumable_id::text || '/' || location_id::text || '/' || state FROM reservation WHERE id = $1`, c.id)
		if want := "consumable/" + oneBin + "/" + b.bin + "/" + c.state; got != want {
			t.Errorf("reservation %s after up: %s, want %s", c.id, got, want)
		}
	}
	if client := scalar(`SELECT COALESCE(client_id::text, '') FROM reservation WHERE id = $1`, older); client != f.Client1 {
		t.Errorf("migrated reservation client %q, want the client of the bin", client)
	}
	reasons := map[string]string{}
	rows, err := tx.Query(bg, `SELECT row_data ->> 'id', reason FROM migration_quarantine WHERE migration = '000264' AND organization_id = $1`, f.OrgA)
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
	for id, reason := range map[string]string{younger: "over_reservation", ambiguous: "reservation_without_bin", homeless: "reservation_without_bin"} {
		if reasons[id] != reason {
			t.Errorf("quarantine of %s: %q, want %q", id, reasons[id], reason)
		}
	}
	if n := scalar(`SELECT count(*)::text FROM reservation WHERE id = ANY($1::uuid[])`, []string{ambiguous, homeless}); n != "0" {
		t.Errorf("%s reservations without bin left in reservation", n)
	}
	if reserved := scalar(`SELECT reserved::float8::text FROM stock_level WHERE consumable_id = $1 AND location_id = $2`, oneBin, b.bin); reserved != "3" {
		t.Errorf("reserved after up %s, want 3", reserved)
	}
	if n := scalar(`SELECT count(*)::text FROM stock_level s WHERE s.organization_id = $1 AND s.reserved <> COALESCE((SELECT sum(r.quantity)
		FROM reservation r WHERE r.consumable_id = s.consumable_id AND r.location_id = s.location_id AND r.state = 'active'), 0)`, f.OrgA); n != "0" {
		t.Errorf("%s stock rows break the invariant after up", n)
	}

	exec(string(down))
	for _, c := range []struct{ id, item, state string }{
		{older, oneBin, "active"}, {younger, oneBin, "active"}, {done, oneBin, "released"},
		{ambiguous, twoBins, "active"}, {homeless, noBin, "active"},
	} {
		got := scalar(`SELECT item_kind || '/' || COALESCE(quantity_item_id::text, '') || '/' || state FROM reservation WHERE id = $1`, c.id)
		if want := "quantity_item/" + c.item + "/" + c.state; got != want {
			t.Errorf("reservation %s after down: %s, want %s", c.id, got, want)
		}
	}
	if n := scalar(`SELECT count(*)::text FROM migration_quarantine WHERE migration = '000264' AND row_data ->> 'id' = ANY($1)`,
		[]string{older, younger, done, ambiguous, homeless}); n != "0" {
		t.Errorf("%s quarantine entries of 000264 after down", n)
	}
	if reserved := scalar(`SELECT reserved::float8::text FROM stock_level WHERE consumable_id = $1 AND location_id = $2`, oneBin, b.bin); reserved != "0" {
		t.Errorf("reserved after down %s, want 0", reserved)
	}
	exec(string(up))
}
