package order_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/order"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Retest (WP-286, STK-05):
//
//	cd backend && go test -count=1 -p 1 -run 'TestOrder' ./internal/order/

// orderWorld runs the order handler (RegisterRoutes) on PostgreSQL.
type orderWorld struct {
	f         *scopetest.Fixture
	mux       *chi.Mux
	requester string
	approver  string
}

func newOrderWorld(t *testing.T) *orderWorld {
	t.Helper()
	f := scopetest.Seed(t, "31")
	mux := chi.NewRouter()
	order.NewHandler(order.NewPGRepository(f.App)).RegisterRoutes(mux)
	return &orderWorld{f: f, mux: mux, requester: f.AppUser(t, f.OrgA, "requester"), approver: f.AppUser(t, f.OrgA, "approver")}
}

// call is one request of user with the tenant scope of ctx (org-wide when
// nil).
type call struct {
	ctx          context.Context
	user         string
	method, path string
	body         string
}

func (w *orderWorld) do(t *testing.T, c call, want int) *httptest.ResponseRecorder {
	t.Helper()
	ctx := c.ctx
	if ctx == nil {
		ctx = w.f.OrgCtx(w.f.OrgA)
	}
	req := httptest.NewRequest(c.method, c.path, bytes.NewBufferString(c.body))
	req = req.WithContext(tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: w.f.OrgA, UserID: c.user}))
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, req)
	if rec.Code != want {
		t.Fatalf("%s %s: got %d %s, want %d", c.method, c.path, rec.Code, rec.Body.String(), want)
	}
	if want >= 400 {
		var p struct {
			Status  int    `json:"status"`
			TraceID string `json:"trace_id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Status != want || p.TraceID == "" {
			t.Fatalf("%s %s: problem %s, want RFC 7807 with status %d and trace_id", c.method, c.path, rec.Body.String(), want)
		}
	}
	return rec
}

func decodeOrder(t *testing.T, rec *httptest.ResponseRecorder) order.Order {
	t.Helper()
	var o order.Order
	if err := json.Unmarshal(rec.Body.Bytes(), &o); err != nil {
		t.Fatalf("decode order %s: %v", rec.Body.String(), err)
	}
	return o
}

// problem decodes the detail and field of a problem response.
func problem(t *testing.T, rec *httptest.ResponseRecorder) (detail, field string) {
	t.Helper()
	var p struct {
		Detail string `json:"detail"`
		Field  string `json:"field"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode problem %s: %v", rec.Body.String(), err)
	}
	return p.Detail, p.Field
}

// create posts an order of the requester and returns it.
func (w *orderWorld) create(t *testing.T, body string) order.Order {
	t.Helper()
	return decodeOrder(t, w.do(t, call{user: w.requester, method: http.MethodPost, path: "/api/v1/orders", body: body}, http.StatusCreated))
}

// transition posts POST /orders/{id}/<action> as user.
func (w *orderWorld) transition(t *testing.T, id, action, user string, want int) *httptest.ResponseRecorder {
	t.Helper()
	return w.do(t, call{user: user, method: http.MethodPost, path: "/api/v1/orders/" + id + "/" + action}, want)
}

// setStatus moves an order into a state no route reaches (the goods
// receipt of WP-760) through the maintenance pool.
func (w *orderWorld) setStatus(t *testing.T, id, status string) {
	t.Helper()
	if _, err := w.f.Admin.Exec(context.Background(), `UPDATE internal_order SET status = $2 WHERE id = $1`, id, status); err != nil {
		t.Fatal(err)
	}
}

func (w *orderWorld) stored(t *testing.T, id string) (status, approvedBy string) {
	t.Helper()
	if err := w.f.Admin.QueryRow(context.Background(), `SELECT status, COALESCE(approved_by::text, '') FROM internal_order WHERE id = $1`, id).
		Scan(&status, &approvedBy); err != nil {
		t.Fatal(err)
	}
	return status, approvedBy
}

var actions = []string{"submit", "approve", "reject", "place", "cancel", "close"}

// TestOrderStateMachine walks every state through every route of the state
// machine (STK-05, WP-286): the allowed transitions succeed, all others are
// 409 and leave the order unchanged; terminal states allow nothing.
func TestOrderStateMachine(t *testing.T) {
	w := newOrderWorld(t)
	// The target state of each action as reached from its allowed sources.
	allowed := map[string]map[string]string{
		"draft":              {"submit": "submitted", "cancel": "cancelled"},
		"submitted":          {"approve": "approved", "reject": "rejected", "cancel": "cancelled"},
		"approved":           {"place": "ordered", "cancel": "cancelled"},
		"ordered":            {"cancel": "cancelled"},
		"partially_received": {"cancel": "cancelled"},
		"received":           {"close": "closed"},
		"rejected":           {},
		"closed":             {},
		"cancelled":          {},
	}
	for from, targets := range allowed {
		t.Run(from, func(t *testing.T) {
			for _, action := range actions {
				o := w.create(t, `{"title":"sm-`+from+`-`+action+`"}`)
				w.setStatus(t, o.ID, from)
				to, ok := targets[action]
				if !ok {
					w.transition(t, o.ID, action, w.approver, http.StatusConflict)
					if status, approvedBy := w.stored(t, o.ID); status != from || approvedBy != "" {
						t.Errorf("%s from %s refused but stored %s/%q", action, from, status, approvedBy)
					}
					continue
				}
				got := decodeOrder(t, w.transition(t, o.ID, action, w.approver, http.StatusOK))
				if got.Status != to {
					t.Errorf("%s from %s: status %s, want %s", action, from, got.Status, to)
				}
				wantApprover := ""
				if action == "approve" || action == "reject" {
					wantApprover = w.approver
				}
				if got.ApprovedBy != wantApprover {
					t.Errorf("%s from %s: approved_by %q, want %q", action, from, got.ApprovedBy, wantApprover)
				}
			}
		})
	}

	// The full route: draft → submitted → approved → ordered, then the
	// receipt (WP-760) and close.
	o := w.create(t, `{"title":"sm-route"}`)
	for _, step := range []struct{ action, user, status string }{
		{"submit", w.requester, "submitted"}, {"approve", w.approver, "approved"}, {"place", w.requester, "ordered"},
	} {
		if got := decodeOrder(t, w.transition(t, o.ID, step.action, step.user, http.StatusOK)); got.Status != step.status {
			t.Fatalf("%s: status %s, want %s", step.action, got.Status, step.status)
		}
	}
	w.setStatus(t, o.ID, "received")
	if got := decodeOrder(t, w.transition(t, o.ID, "close", w.requester, http.StatusOK)); got.Status != "closed" || got.ApprovedBy != w.approver {
		t.Fatalf("close: %+v", got)
	}

	// Unknown orders are 404, for every route.
	for _, id := range []string{w.f.ID(), "not-a-uuid"} {
		w.do(t, call{user: w.requester, method: http.MethodGet, path: "/api/v1/orders/" + id}, http.StatusNotFound)
		w.do(t, call{user: w.requester, method: http.MethodPatch, path: "/api/v1/orders/" + id, body: `{"title":"x"}`}, http.StatusNotFound)
		w.do(t, call{user: w.requester, method: http.MethodDelete, path: "/api/v1/orders/" + id}, http.StatusNotFound)
		w.do(t, call{user: w.requester, method: http.MethodPost, path: "/api/v1/orders/" + id + "/items", body: `{"description":"x","quantity":1}`}, http.StatusNotFound)
		for _, action := range actions {
			w.transition(t, id, action, w.approver, http.StatusNotFound)
		}
	}
}

// TestOrderFourEyes: the requester cannot approve his own order, and an
// order without requester cannot be approved (403, STK-05); approved_by
// stays empty until an approval succeeds.
func TestOrderFourEyes(t *testing.T) {
	w := newOrderWorld(t)
	o := w.create(t, `{"title":"four-eyes"}`)
	if o.RequestedBy != w.requester {
		t.Fatalf("requested_by %q, want the requester", o.RequestedBy)
	}
	w.transition(t, o.ID, "submit", w.requester, http.StatusOK)
	rec := w.transition(t, o.ID, "approve", w.requester, http.StatusForbidden)
	if detail, _ := problem(t, rec); !strings.Contains(detail, "four-eyes rule") {
		t.Errorf("403 detail %q does not name the four-eyes rule", detail)
	}
	if status, approvedBy := w.stored(t, o.ID); status != "submitted" || approvedBy != "" {
		t.Fatalf("refused approval stored %s/%q", status, approvedBy)
	}
	if got := decodeOrder(t, w.transition(t, o.ID, "approve", w.approver, http.StatusOK)); got.ApprovedBy != w.approver {
		t.Fatalf("approval by the second user: %+v", got)
	}

	// Without requested_by nobody can approve.
	anon := w.create(t, `{"title":"four-eyes-anonymous"}`)
	if _, err := w.f.Admin.Exec(context.Background(), `UPDATE internal_order SET status = 'submitted', requested_by = NULL WHERE id = $1`, anon.ID); err != nil {
		t.Fatal(err)
	}
	w.transition(t, anon.ID, "approve", w.approver, http.StatusForbidden)
	if status, approvedBy := w.stored(t, anon.ID); status != "submitted" || approvedBy != "" {
		t.Fatalf("refused approval without requester stored %s/%q", status, approvedBy)
	}
}

// TestOrderParallelApprove: of parallel approvals exactly one succeeds, the
// others are 409.
func TestOrderParallelApprove(t *testing.T) {
	w := newOrderWorld(t)
	o := w.create(t, `{"title":"parallel"}`)
	w.transition(t, o.ID, "submit", w.requester, http.StatusOK)
	const n = 8
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/orders/"+o.ID+"/approve", nil)
			req = req.WithContext(tenant.WithTenant(w.f.OrgCtx(w.f.OrgA), tenant.TenantInfo{OrganizationID: w.f.OrgA, UserID: w.approver}))
			rec := httptest.NewRecorder()
			w.mux.ServeHTTP(rec, req)
			codes[i] = rec.Code
		}(i)
	}
	wg.Wait()
	ok, conflict := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflict++
		}
	}
	if ok != 1 || conflict != n-1 {
		t.Fatalf("parallel approvals: %v, want one 200 and %d × 409", codes, n-1)
	}
}

// TestOrderDraftOnlyChanges: PATCH, DELETE and new positions need draft
// (409 otherwise); a position books no movement.
func TestOrderDraftOnlyChanges(t *testing.T) {
	w := newOrderWorld(t)
	ctx := context.Background()
	if _, err := w.f.Admin.Exec(ctx, `UPDATE organization SET currency = 'CHF' WHERE id = $1`, w.f.OrgA); err != nil {
		t.Fatal(err)
	}
	asset := w.f.Asset(t, w.f.OrgA, "", "order-asset")
	o := w.create(t, `{"title":"draft-only"}`)
	if o.Currency != "CHF" {
		t.Errorf("currency %q, want the organization's CHF", o.Currency)
	}
	path := "/api/v1/orders/" + o.ID
	got := decodeOrder(t, w.do(t, call{user: w.requester, method: http.MethodPost, path: path + "/items",
		body: `{"description":"switch","quantity":2,"asset_id":"` + asset + `"}`}, http.StatusCreated))
	if len(got.Items) != 1 || got.Items[0].AssetID != asset || got.Items[0].ReceivedQuantity != 0 {
		t.Fatalf("items %+v", got.Items)
	}
	var movements int
	if err := w.f.Admin.QueryRow(ctx, `SELECT count(*) FROM asset_movement WHERE organization_id = $1`, w.f.OrgA).Scan(&movements); err != nil {
		t.Fatal(err)
	}
	if movements != 0 {
		t.Errorf("adding a position booked %d movements, want none", movements)
	}
	w.do(t, call{user: w.requester, method: http.MethodPatch, path: path, body: `{"title":"draft-only renamed"}`}, http.StatusOK)

	w.transition(t, o.ID, "submit", w.requester, http.StatusOK)
	for _, c := range []call{
		{method: http.MethodPatch, path: path, body: `{"title":"late"}`},
		{method: http.MethodDelete, path: path},
		{method: http.MethodPost, path: path + "/items", body: `{"description":"late","quantity":1}`},
	} {
		c.user = w.requester
		w.do(t, c, http.StatusConflict)
	}
	var title string
	var items int
	if err := w.f.Admin.QueryRow(ctx, `SELECT title, (SELECT count(*) FROM internal_order_item WHERE order_id = $1) FROM internal_order WHERE id = $1`, o.ID).
		Scan(&title, &items); err != nil || title != "draft-only renamed" || items != 1 {
		t.Fatalf("order after refused changes: %q, %d items, err=%v", title, items, err)
	}

	// A draft is deleted.
	d := w.create(t, `{"title":"to delete"}`)
	w.do(t, call{user: w.requester, method: http.MethodDelete, path: "/api/v1/orders/" + d.ID}, http.StatusNoContent)
}

// TestOrderSupplierAndClient covers the supplier alias and the client
// consistency of suppliers and consumables (422).
func TestOrderSupplierAndClient(t *testing.T) {
	w := newOrderWorld(t)
	ctx := context.Background()
	insert := func(q string, args ...any) string {
		t.Helper()
		var id string
		if err := w.f.Admin.QueryRow(ctx, q, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	acme := insert(`INSERT INTO supplier (organization_id, name) VALUES ($1, 'Acme GmbH') RETURNING id::text`, w.f.OrgA)
	other := insert(`INSERT INTO supplier (organization_id, name) VALUES ($1, 'Other AG') RETURNING id::text`, w.f.OrgA)
	sup1 := insert(`INSERT INTO supplier (organization_id, client_id, name) VALUES ($1, $2, 'C1 Supply') RETURNING id::text`, w.f.OrgA, w.f.Client1)
	sup2 := insert(`INSERT INTO supplier (organization_id, client_id, name) VALUES ($1, $2, 'C2 Supply') RETURNING id::text`, w.f.OrgA, w.f.Client2)
	consOrg := insert(`INSERT INTO consumable (organization_id, name) VALUES ($1, 'org-wide toner') RETURNING id::text`, w.f.OrgA)
	cons1 := insert(`INSERT INTO consumable (organization_id, client_id, name) VALUES ($1, $2, 'c1 toner') RETURNING id::text`, w.f.OrgA, w.f.Client1)
	cons2 := insert(`INSERT INTO consumable (organization_id, client_id, name) VALUES ($1, $2, 'c2 toner') RETURNING id::text`, w.f.OrgA, w.f.Client2)
	asset := w.f.Asset(t, w.f.OrgA, "", "supplier-asset")
	post := func(body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		return w.do(t, call{user: w.requester, method: http.MethodPost, path: "/api/v1/orders", body: body}, want)
	}
	wantField := func(rec *httptest.ResponseRecorder, field string) {
		t.Helper()
		if _, got := problem(t, rec); got != field {
			t.Errorf("problem %s, want field %q", rec.Body.String(), field)
		}
	}

	// The alias resolves an existing name; an empty value is not set.
	o := decodeOrder(t, post(`{"title":"alias","supplier":"Acme GmbH"}`, http.StatusCreated))
	if o.SupplierID != acme || o.SupplierName != "Acme GmbH" || o.Supplier != "Acme GmbH" {
		t.Fatalf("alias order: %+v", o)
	}
	if o = decodeOrder(t, post(`{"title":"empty alias","supplier":""}`, http.StatusCreated)); o.SupplierID != "" {
		t.Fatalf("empty alias set supplier %q", o.SupplierID)
	}
	if o = decodeOrder(t, post(`{"title":"by id","supplier_id":"`+acme+`","supplier":"Acme GmbH"}`, http.StatusCreated)); o.SupplierID != acme {
		t.Fatalf("matching alias and id: %+v", o)
	}
	wantField(post(`{"title":"unknown","supplier":"Nobody Ltd"}`, http.StatusUnprocessableEntity), "supplier")
	wantField(post(`{"title":"contradiction","supplier_id":"`+other+`","supplier":"Acme GmbH"}`, http.StatusUnprocessableEntity), "supplier")
	wantField(post(`{"title":"unknown id","supplier_id":"`+w.f.ID()+`"}`, http.StatusUnprocessableEntity), "supplier_id")
	var suppliers int
	if err := w.f.Admin.QueryRow(ctx, `SELECT count(*) FROM supplier WHERE organization_id = $1`, w.f.OrgA).Scan(&suppliers); err != nil || suppliers != 4 {
		t.Fatalf("suppliers after the requests: %d err=%v, want the 4 seeded ones", suppliers, err)
	}

	// Client-bound suppliers fit only orders of their client.
	post(`{"title":"c1 own","client_id":"`+w.f.Client1+`","supplier_id":"`+sup1+`"}`, http.StatusCreated)
	wantField(post(`{"title":"c1 foreign","client_id":"`+w.f.Client1+`","supplier_id":"`+sup2+`"}`, http.StatusUnprocessableEntity), "supplier_id")
	wantField(post(`{"title":"org foreign","supplier_id":"`+sup1+`"}`, http.StatusUnprocessableEntity), "supplier_id")
	wantField(post(`{"title":"c1 foreign alias","client_id":"`+w.f.Client1+`","supplier":"C2 Supply"}`, http.StatusUnprocessableEntity), "supplier")
	c1 := decodeOrder(t, post(`{"title":"c1 order","client_id":"`+w.f.Client1+`","supplier":"Acme GmbH"}`, http.StatusCreated))
	path := "/api/v1/orders/" + c1.ID
	wantField(w.do(t, call{user: w.requester, method: http.MethodPatch, path: path, body: `{"supplier_id":"` + sup2 + `"}`}, http.StatusUnprocessableEntity), "supplier_id")
	if got := decodeOrder(t, w.do(t, call{user: w.requester, method: http.MethodPatch, path: path, body: `{"supplier_id":"` + sup1 + `"}`}, http.StatusOK)); got.SupplierID != sup1 {
		t.Fatalf("patch supplier: %+v", got)
	}
	wantField(w.do(t, call{user: w.requester, method: http.MethodPatch, path: path, body: `{"client_id":"` + w.f.Client2 + `"}`}, http.StatusUnprocessableEntity), "supplier_id")

	// Consumables: org-wide or of the order's client; a position is a
	// consumable or an asset position.
	items := path + "/items"
	item := func(body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		return w.do(t, call{user: w.requester, method: http.MethodPost, path: items, body: body}, want)
	}
	item(`{"description":"own toner","quantity":3,"consumable_id":"`+cons1+`"}`, http.StatusCreated)
	item(`{"description":"org toner","quantity":3,"consumable_id":"`+consOrg+`"}`, http.StatusCreated)
	wantField(item(`{"description":"foreign toner","quantity":3,"consumable_id":"`+cons2+`"}`, http.StatusUnprocessableEntity), "consumable_id")
	wantField(item(`{"description":"both","quantity":1,"consumable_id":"`+consOrg+`","asset_id":"`+asset+`"}`, http.StatusUnprocessableEntity), "asset_id")
	orgWide := decodeOrder(t, post(`{"title":"org order"}`, http.StatusCreated))
	wantField(w.do(t, call{user: w.requester, method: http.MethodPost, path: "/api/v1/orders/" + orgWide.ID + "/items",
		body: `{"description":"client toner","quantity":1,"consumable_id":"` + cons1 + `"}`}, http.StatusUnprocessableEntity), "consumable_id")
	// The client-1 consumable binds the order to client 1.
	wantField(w.do(t, call{user: w.requester, method: http.MethodPatch, path: path, body: `{"client_id":"` + w.f.Client2 + `","supplier_id":""}`},
		http.StatusUnprocessableEntity), "client_id")
}

// TestOrderItemClientPolicy: the positions carry the client of their order
// and internal_order_item_isolation filters them by it (E-09): a client-1
// principal reads no position of a client-2 order even by a direct query,
// and a client change of the order moves its positions along.
func TestOrderItemClientPolicy(t *testing.T) {
	w := newOrderWorld(t)
	ctx := context.Background()
	o := w.create(t, `{"title":"c2 order","client_id":"`+w.f.Client2+`"}`)
	w.do(t, call{user: w.requester, method: http.MethodPost, path: "/api/v1/orders/" + o.ID + "/items", body: `{"description":"cable","quantity":5}`}, http.StatusCreated)
	visible := func(scopeCtx context.Context) int {
		t.Helper()
		var n int
		if err := database.WithRequestTenant(scopeCtx, w.f.App, w.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM internal_order_item WHERE order_id = $1`, o.ID).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := visible(w.f.ClientCtx(w.f.Client1)); n != 0 {
		t.Errorf("client-1 principal reads %d positions of a client-2 order, want 0", n)
	}
	if n := visible(w.f.ClientCtx(w.f.Client2)); n != 1 {
		t.Errorf("client-2 principal reads %d positions of its order, want 1", n)
	}
	// A client-1 principal cannot add a position to it either.
	w.do(t, call{ctx: w.f.ClientCtx(w.f.Client1), user: w.requester, method: http.MethodPost, path: "/api/v1/orders/" + o.ID + "/items",
		body: `{"description":"intruder","quantity":1}`}, http.StatusNotFound)

	w.do(t, call{user: w.requester, method: http.MethodPatch, path: "/api/v1/orders/" + o.ID, body: `{"client_id":"` + w.f.Client1 + `"}`}, http.StatusOK)
	var client string
	if err := w.f.Admin.QueryRow(ctx, `SELECT client_id::text FROM internal_order_item WHERE order_id = $1`, o.ID).Scan(&client); err != nil || client != w.f.Client1 {
		t.Fatalf("position client after the order moved: %q err=%v, want client 1", client, err)
	}
	if n := visible(w.f.ClientCtx(w.f.Client1)); n != 1 {
		t.Errorf("client-1 principal reads %d positions after the move, want 1", n)
	}
}
