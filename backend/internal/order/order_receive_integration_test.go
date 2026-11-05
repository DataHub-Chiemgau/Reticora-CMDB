package order_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/order"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Retest (WP-760, STK-05):
//
//	cd backend && go test -count=1 -p 1 -run 'TestOrderReceive' ./internal/order/

// receiveWorld runs the order handler with the movement service as its
// booking service, behind the idempotency middleware (API-04).
type receiveWorld struct {
	*orderWorld
	handler http.Handler
}

func newReceiveWorld(t *testing.T) *receiveWorld {
	t.Helper()
	f := scopetest.Seed(t, "31")
	mux := chi.NewRouter()
	order.NewHandler(order.NewPGRepository(f.App)).
		WithBookings(movement.NewService(movement.NewPGRepository(f.App))).
		RegisterRoutes(mux)
	w := &orderWorld{f: f, mux: mux, requester: f.AppUser(t, f.OrgA, "requester"), approver: f.AppUser(t, f.OrgA, "approver")}
	return &receiveWorld{orderWorld: w, handler: middleware.IdempotencyWithStore(cache.NewMemoryStore())(mux)}
}

// post sends POST /orders/{id}/receive with body and an optional
// Idempotency-Key, without checking the answer.
func (w *receiveWorld) post(id, body, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders/"+id+"/receive", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	req = req.WithContext(tenant.WithTenant(w.f.OrgCtx(w.f.OrgA), tenant.TenantInfo{OrganizationID: w.f.OrgA, UserID: w.requester}))
	rec := httptest.NewRecorder()
	w.handler.ServeHTTP(rec, req)
	return rec
}

// receive posts the goods receipt and checks the status; a refusal must be
// RFC 7807 with trace_id, and its field is returned with the detail.
func (w *receiveWorld) receive(t *testing.T, id, body string, want int) (rec *httptest.ResponseRecorder, detail, field string) {
	t.Helper()
	rec = w.post(id, body, "")
	if rec.Code != want {
		t.Fatalf("receive %s: got %d %s, want %d", body, rec.Code, rec.Body.String(), want)
	}
	if want >= 400 {
		var p struct {
			Status  int    `json:"status"`
			TraceID string `json:"trace_id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Status != want || p.TraceID == "" {
			t.Fatalf("receive %s: problem %s, want RFC 7807 with status %d and trace_id", body, rec.Body.String(), want)
		}
		detail, field = problem(t, rec)
	}
	return rec, detail, field
}

// addItem adds a position to an order in draft and returns its id.
func (w *receiveWorld) addItem(t *testing.T, orderID, body string) string {
	t.Helper()
	o := decodeOrder(t, w.do(t, call{user: w.requester, method: http.MethodPost, path: "/api/v1/orders/" + orderID + "/items", body: body}, http.StatusCreated))
	return o.Items[len(o.Items)-1].ID
}

func (w *receiveWorld) scalar(t *testing.T, query string, args ...any) float64 {
	t.Helper()
	var v float64
	if err := w.f.Admin.QueryRow(context.Background(), query, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return v
}

func (w *receiveWorld) text(t *testing.T, query string, args ...any) string {
	t.Helper()
	var v string
	if err := w.f.Admin.QueryRow(context.Background(), query, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return v
}

func (w *receiveWorld) consumable(t *testing.T, clientID, name string) string {
	t.Helper()
	return w.text(t, `INSERT INTO consumable (organization_id, client_id, name) VALUES ($1, $2, $3) RETURNING id::text`, w.f.OrgA, clientID, name)
}

func (w *receiveWorld) movements(t *testing.T, orderID string) float64 {
	t.Helper()
	return w.scalar(t, `SELECT count(*)::float8 FROM stock_movement WHERE order_id = $1`, orderID)
}

func (w *receiveWorld) received(t *testing.T, itemID string) float64 {
	t.Helper()
	return w.scalar(t, `SELECT received_quantity::float8 FROM internal_order_item WHERE id = $1`, itemID)
}

func (w *receiveWorld) onHand(t *testing.T, consumableID, bin string) float64 {
	t.Helper()
	return w.scalar(t, `SELECT COALESCE(sum(on_hand), 0)::float8 FROM stock_level WHERE consumable_id = $1 AND location_id = $2`, consumableID, bin)
}

func itemOf(o *order.Order, id string) order.Item {
	for _, it := range o.Items {
		if it.ID == id {
			return it
		}
	}
	return order.Item{}
}

// TestOrderReceiveBooksStockMovements covers the goods receipt (STK-05,
// WP-760) through POST /orders/{id}/receive on PostgreSQL: a receipt
// outside ordered and partially_received is 409; an unknown second position
// rolls the first back (no movement, received_quantity unchanged); a bin
// of another client is 422 with the answer of an unknown bin; an
// overdelivery is 422; an asset position needs as many fitting asset_ids
// as it receives. A partial receipt books a consumable receipt into
// stock_movement with order_id, carries the stock_level of the target bin
// forward, emits stock.movement and moves the order to partially_received;
// a repetition with the same Idempotency-Key replays the answer without a
// second booking. The rest, into another bin and with the assets, moves
// the order to received and the assets into the bin; a further receipt is
// 409. Without a bin of the position or the order the receipt is 422.
func TestOrderReceiveBooksStockMovements(t *testing.T) {
	w := newReceiveWorld(t)
	f := w.f
	_, bin1 := binTree(t, f, f.OrgA, f.Client1, "rc1")
	_, bin1b := binTree(t, f, f.OrgA, f.Client1, "rc1b")
	_, bin2 := binTree(t, f, f.OrgA, f.Client2, "rc2")
	cons := w.consumable(t, f.Client1, "rc-cable")
	asset1 := f.Asset(t, f.OrgA, f.Client1, "rc-asset1")
	asset2 := f.Asset(t, f.OrgA, f.Client1, "rc-asset2")
	asset3 := f.Asset(t, f.OrgA, f.Client2, "rc-asset3")

	o := w.create(t, `{"title":"receipt","client_id":"`+f.Client1+`","target_location_id":"`+bin1+`"}`)
	consItem := w.addItem(t, o.ID, `{"description":"cable","quantity":10,"consumable_id":"`+cons+`"}`)
	assetItem := w.addItem(t, o.ID, `{"description":"laptops","quantity":2}`)
	line := func(item string, qty string, extra string) string {
		return `{"item_id":"` + item + `","quantity":` + qty + extra + `}`
	}
	body := func(lines ...string) string {
		out := `{"items":[`
		for i, l := range lines {
			if i > 0 {
				out += ","
			}
			out += l
		}
		return out + `]}`
	}

	// Approved is not yet ordered: 409.
	w.setStatus(t, o.ID, order.StatusApproved)
	w.receive(t, o.ID, body(line(consItem, "1", "")), http.StatusConflict)
	w.setStatus(t, o.ID, order.StatusOrdered)

	// The first error rolls the whole receipt back.
	if _, _, field := w.receive(t, o.ID, body(line(consItem, "4", ""), line(f.ID(), "1", "")), http.StatusUnprocessableEntity); field != "items[1].item_id" {
		t.Errorf("unknown position: field %q, want items[1].item_id", field)
	}
	if n, rcv, stock := w.movements(t, o.ID), w.received(t, consItem), w.onHand(t, cons, bin1); n != 0 || rcv != 0 || stock != 0 {
		t.Errorf("after a refused receipt: %v movements, received %v, on_hand %v, want 0 0 0", n, rcv, stock)
	}
	// A bin of another client answers like an unknown one.
	_, unknown, field := w.receive(t, o.ID, body(line(consItem, "1", `,"location_id":"`+f.ID()+`"`)), http.StatusUnprocessableEntity)
	if field != "items[0].location_id" {
		t.Errorf("unknown bin: field %q", field)
	}
	if _, detail, field2 := w.receive(t, o.ID, body(line(consItem, "1", `,"location_id":"`+bin2+`"`)), http.StatusUnprocessableEntity); field2 != "items[0].location_id" || detail != unknown {
		t.Errorf("bin of client 2: %q on %q, want %q on items[0].location_id", detail, field2, unknown)
	}
	// Overdelivery.
	if _, _, field3 := w.receive(t, o.ID, body(line(consItem, "11", "")), http.StatusUnprocessableEntity); field3 != "items[0].quantity" {
		t.Errorf("overdelivery: field %q, want items[0].quantity", field3)
	}
	// Asset positions: the count must match, the assets must fit.
	for name, ids := range map[string]string{
		"missing asset_ids": ``,
		"one of two":        `,"asset_ids":["` + asset1 + `"]`,
		"asset of client 2": `,"asset_ids":["` + asset1 + `","` + asset3 + `"]`,
		"unknown asset":     `,"asset_ids":["` + asset1 + `","` + f.ID() + `"]`,
	} {
		if _, _, fld := w.receive(t, o.ID, body(line(assetItem, "2", ids)), http.StatusUnprocessableEntity); fld != "items[0].asset_ids" {
			t.Errorf("%s: field %q, want items[0].asset_ids", name, fld)
		}
	}
	if _, _, fld := w.receive(t, o.ID, body(line(consItem, "1", `,"asset_ids":["`+asset1+`"]`)), http.StatusUnprocessableEntity); fld != "items[0].asset_ids" {
		t.Errorf("asset_ids on a consumable position: field %q", fld)
	}
	if n := w.movements(t, o.ID); n != 0 {
		t.Fatalf("refused receipts booked %v movements", n)
	}

	// Partial receipt into the target bin.
	first := w.post(o.ID, body(line(consItem, "4", "")), "rc-key-1")
	if first.Code != http.StatusOK {
		t.Fatalf("partial receipt: %d %s", first.Code, first.Body.String())
	}
	got := decodeOrder(t, first)
	if got.Status != order.StatusPartiallyReceived || itemOf(&got, consItem).ReceivedQuantity != 4 || itemOf(&got, assetItem).ReceivedQuantity != 0 {
		t.Errorf("after the partial receipt: %+v", got)
	}
	var kind, consumableID, to, typ string
	var qty float64
	if err := f.Admin.QueryRow(context.Background(), `SELECT item_kind, consumable_id::text, to_location_id::text, movement_type, quantity::float8
		FROM stock_movement WHERE order_id = $1`, o.ID).Scan(&kind, &consumableID, &to, &typ, &qty); err != nil {
		t.Fatalf("movement of the receipt: %v", err)
	}
	if kind != "consumable" || consumableID != cons || to != bin1 || typ != "receipt" || qty != 4 {
		t.Errorf("movement %s %s %s %s %v, want consumable %s %s receipt 4", kind, consumableID, to, typ, qty, cons, bin1)
	}
	if stock := w.onHand(t, cons, bin1); stock != 4 {
		t.Errorf("on_hand of the target bin %v, want 4", stock)
	}
	if n := w.scalar(t, `SELECT count(*)::float8 FROM event_outbox WHERE type = 'stock.movement' AND entity_id = $1`, cons); n != 1 {
		t.Errorf("%v stock.movement events, want 1", n)
	}
	// The repetition with the same key replays and books nothing.
	replay := w.post(o.ID, body(line(consItem, "4", "")), "rc-key-1")
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Errorf("replay: %d replayed=%q", replay.Code, replay.Header().Get("Idempotency-Replayed"))
	}
	if n, rcv := w.movements(t, o.ID), w.received(t, consItem); n != 1 || rcv != 4 {
		t.Errorf("after the replay: %v movements, received %v, want 1 and 4", n, rcv)
	}

	// The rest: consumables into another bin, the assets into the target.
	rec, _, _ := w.receive(t, o.ID, body(line(consItem, "6", `,"location_id":"`+bin1b+`"`),
		line(assetItem, "2", `,"asset_ids":["`+asset1+`","`+asset2+`"]`)), http.StatusOK)
	got = decodeOrder(t, rec)
	if got.Status != order.StatusReceived || itemOf(&got, consItem).ReceivedQuantity != 10 || itemOf(&got, assetItem).ReceivedQuantity != 2 {
		t.Errorf("after the full receipt: %+v", got)
	}
	if stock := w.onHand(t, cons, bin1b); stock != 6 {
		t.Errorf("on_hand of the second bin %v, want 6", stock)
	}
	for _, a := range []string{asset1, asset2} {
		if n := w.scalar(t, `SELECT count(*)::float8 FROM stock_movement WHERE order_id = $1 AND asset_id = $2 AND item_kind = 'asset'
				AND movement_type = 'receipt' AND to_location_id = $3`, o.ID, a, bin1); n != 1 {
			t.Errorf("asset %s: %v receipts into the target bin, want 1", a, n)
		}
		if loc := w.text(t, `SELECT COALESCE(location_id::text, '') FROM asset WHERE id = $1`, a); loc != bin1 {
			t.Errorf("asset %s in %q, want %s", a, loc, bin1)
		}
	}
	if n := w.movements(t, o.ID); n != 4 {
		t.Errorf("%v movements of the order, want 4", n)
	}
	w.receive(t, o.ID, body(line(consItem, "1", "")), http.StatusConflict)

	// Without a bin of the position or the order: 422.
	noBin := w.create(t, `{"title":"no bin","client_id":"`+f.Client1+`"}`)
	noBinItem := w.addItem(t, noBin.ID, `{"description":"cable","quantity":1,"consumable_id":"`+cons+`"}`)
	w.setStatus(t, noBin.ID, order.StatusOrdered)
	if _, _, fld := w.receive(t, noBin.ID, body(line(noBinItem, "1", "")), http.StatusUnprocessableEntity); fld != "items[0].location_id" {
		t.Errorf("no bin: field %q, want items[0].location_id", fld)
	}
	// Malformed requests.
	w.receive(t, noBin.ID, `{"items":[]}`, http.StatusUnprocessableEntity)
	w.receive(t, noBin.ID, body(line(noBinItem, "0", "")), http.StatusUnprocessableEntity)
	w.receive(t, f.ID(), body(line(noBinItem, "1", "")), http.StatusNotFound)
}

// TestOrderReceiveConcurrentLastQuantity: of two simultaneous receipts of
// the last open quantity exactly one succeeds (the order lock serializes
// them); the stock grows once (WP-760).
func TestOrderReceiveConcurrentLastQuantity(t *testing.T) {
	w := newReceiveWorld(t)
	f := w.f
	_, bin := binTree(t, f, f.OrgA, f.Client1, "rcc")
	cons := w.consumable(t, f.Client1, "rcc-toner")
	o := w.create(t, `{"title":"race","client_id":"`+f.Client1+`","target_location_id":"`+bin+`"}`)
	item := w.addItem(t, o.ID, `{"description":"toner","quantity":5,"consumable_id":"`+cons+`"}`)
	w.setStatus(t, o.ID, order.StatusOrdered)
	w.receive(t, o.ID, `{"items":[{"item_id":"`+item+`","quantity":3}]}`, http.StatusOK)

	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = w.post(o.ID, `{"items":[{"item_id":"`+item+`","quantity":2}]}`, "").Code
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusConflict, http.StatusUnprocessableEntity:
		default:
			t.Errorf("concurrent receipt answered %d", c)
		}
	}
	if ok != 1 {
		t.Errorf("concurrent receipts %v: %d succeeded, want exactly 1", codes, ok)
	}
	if rcv, stock, n := w.received(t, item), w.onHand(t, cons, bin), w.movements(t, o.ID); rcv != 5 || stock != 5 || n != 2 {
		t.Errorf("after the race: received %v, on_hand %v, %v movements, want 5 5 2", rcv, stock, n)
	}
	if status, _ := w.stored(t, o.ID); status != order.StatusReceived {
		t.Errorf("status %s, want received", status)
	}
}
