package ipam_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// AS numbers and AS number ranges (IPM-16, WP-1066, migration 000316)
// against PostgreSQL as the restricted runtime login (scopetest.AppDSN),
// through the routes of the ipam handler.

type asnEnv struct {
	t       *testing.T
	f       *scopetest.Fixture
	mux     *chi.Mux
	org     context.Context
	orgB    context.Context
	client1 context.Context
}

func newASNEnv(t *testing.T) *asnEnv {
	t.Helper()
	f := scopetest.Seed(t, "d8")
	mux := chi.NewRouter()
	ipam.NewHandler(ipam.NewPGRepository(f.App).WithEvents(events.NewOutbox())).RegisterRoutes(mux)
	return &asnEnv{
		t: t, f: f, mux: mux,
		org:     tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User}),
		orgB:    tenant.WithTenant(f.OrgCtx(f.OrgB), tenant.TenantInfo{OrganizationID: f.OrgB, UserID: f.User}),
		client1: tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User}),
	}
}

func (e *asnEnv) call(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx))
	return w
}

// expect sends a request, checks its status and decodes the body into v
// (optional).
func (e *asnEnv) expect(ctx context.Context, method, path, body string, want int, v any) *httptest.ResponseRecorder {
	e.t.Helper()
	w := e.call(ctx, method, path, body)
	if w.Code != want {
		e.t.Fatalf("%s %s %s = %d %s, want %d", method, path, body, w.Code, w.Body.String(), want)
	}
	if v != nil {
		if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
			e.t.Fatalf("decode %s: %v", w.Body.String(), err)
		}
	}
	return w
}

// field returns the field of a problem answer.
func field(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var p struct {
		Field string `json:"field"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	return p.Field
}

func TestASNUniquePerOrg(t *testing.T) {
	e := newASNEnv(t)
	var a ipam.ASN
	e.expect(e.org, http.MethodPost, "/api/v1/asns", `{"number":"AS1.10","name":"own","kind":"own","rir":"ripe"}`, http.StatusCreated, &a)
	if a.Number != 65546 || a.ASDot != "1.10" || a.RIR == nil || *a.RIR != "ripe" || a.ClientID != nil {
		t.Errorf("created %+v", a)
	}
	w := e.expect(e.org, http.MethodPost, "/api/v1/asns", `{"number":65546,"name":"again","kind":"peer"}`, http.StatusConflict, nil)
	if field(t, w) != "number" {
		t.Errorf("duplicate: %s, want field number", w.Body.String())
	}
	// The same number in another organization is allowed.
	e.expect(e.orgB, http.MethodPost, "/api/v1/asns", `{"number":"1.10","name":"own-b","kind":"own"}`, http.StatusCreated, nil)
	// Invalid and reserved numbers, kind and rir are 422 on their field.
	for body, f := range map[string]string{
		`{"number":0,"name":"x","kind":"own"}`:                                                        "number",
		`{"number":23456,"name":"x","kind":"own"}`:                                                    "number",
		`{"number":65535,"name":"x","kind":"own"}`:                                                    "number",
		`{"number":4294967295,"name":"x","kind":"own"}`:                                               "number",
		`{"number":"65535.65535","name":"x","kind":"own"}`:                                            "number",
		`{"number":"AS1.70000","name":"x","kind":"own"}`:                                              "number",
		`{"number":64512,"name":"x","kind":"transit"}`:                                                "kind",
		`{"number":64512,"name":"x","kind":"own","rir":"x"}`:                                          "rir",
		`{"number":64512,"name":" ","kind":"own"}`:                                                    "name",
		`{"number":64512,"name":"x","kind":"own","client_id":"c0de0000-0000-4000-8000-000000000000"}`: "client_id",
	} {
		resp := e.expect(e.org, http.MethodPost, "/api/v1/asns", body, http.StatusUnprocessableEntity, nil)
		if got := field(t, resp); got != f {
			t.Errorf("POST %s: field %q, want %q", body, got, f)
		}
	}
	// A client of organization B is unknown in A.
	var clientB string
	if err := e.f.Admin.QueryRow(context.Background(), `INSERT INTO client (organization_id, name, slug) VALUES ($1, 'd8-b', 'd8-b') RETURNING id::text`,
		e.f.OrgB).Scan(&clientB); err != nil {
		t.Fatal(err)
	}
	w = e.expect(e.org, http.MethodPost, "/api/v1/asns", `{"number":64512,"name":"x","kind":"own","client_id":"`+clientB+`"}`,
		http.StatusUnprocessableEntity, nil)
	if field(t, w) != "client_id" {
		t.Errorf("foreign client: %s", w.Body.String())
	}
	// PATCH and DELETE.
	var upd ipam.ASN
	e.expect(e.org, http.MethodPatch, "/api/v1/asns/"+a.ID, `{"name":"renamed","rir":"","number":"AS4200000000"}`, http.StatusOK, &upd)
	if upd.Name != "renamed" || upd.RIR != nil || upd.Number != 4200000000 || upd.ASDot != "64086.59904" {
		t.Errorf("patched %+v", upd)
	}
	e.expect(e.org, http.MethodDelete, "/api/v1/asns/"+a.ID, "", http.StatusNoContent, nil)
	e.expect(e.org, http.MethodGet, "/api/v1/asns/"+a.ID, "", http.StatusNotFound, nil)
}

func TestASNRangeOverlap409(t *testing.T) {
	e := newASNEnv(t)
	var rg ipam.ASNRange
	e.expect(e.org, http.MethodPost, "/api/v1/asn-ranges", `{"name":"private","start":"64512","end":"AS65534","purpose":"customers"}`,
		http.StatusCreated, &rg)
	if rg.Start != 64512 || rg.End != 65534 || rg.StartASDot != "64512" {
		t.Errorf("created %+v", rg)
	}
	for body, f := range map[string]string{
		`{"name":"overlap","start":65000,"end":65100}`:     "start",
		`{"name":"inside-edge","start":65534,"end":"1.0"}`: "start",
		`{"name":"private","start":"1.0","end":"1.100"}`:   "name",
	} {
		w := e.expect(e.org, http.MethodPost, "/api/v1/asn-ranges", body, http.StatusConflict, nil)
		if got := field(t, w); got != f {
			t.Errorf("POST %s: field %q, want %q", body, got, f)
		}
	}
	// Adjacent ranges do not overlap; another organization has its own.
	e.expect(e.org, http.MethodPost, "/api/v1/asn-ranges", `{"name":"adjacent","start":"1.0","end":"1.100"}`, http.StatusCreated, nil)
	e.expect(e.orgB, http.MethodPost, "/api/v1/asn-ranges", `{"name":"private","start":64512,"end":65534}`, http.StatusCreated, nil)
	// A PATCH into the neighbour is 409 start as well.
	w := e.expect(e.org, http.MethodPatch, "/api/v1/asn-ranges/"+rg.ID, `{"end":"1.5"}`, http.StatusConflict, nil)
	if field(t, w) != "start" {
		t.Errorf("PATCH overlap: %s", w.Body.String())
	}
	e.expect(e.org, http.MethodDelete, "/api/v1/asn-ranges/"+rg.ID, "", http.StatusNoContent, nil)
}

func TestASNRangeStartAfterEnd422(t *testing.T) {
	e := newASNEnv(t)
	for body, f := range map[string]string{
		`{"name":"r","start":65000,"end":64999}`:   "end",
		`{"name":"r","start":"1.1","end":"1.0"}`:   "end",
		`{"name":"r","start":0,"end":10}`:          "start",
		`{"name":"r","start":10,"end":4294967295}`: "end",
		`{"name":"r","start":23456,"end":23460}`:   "start",
		`{"name":"r","start":"x","end":10}`:        "start",
	} {
		w := e.expect(e.org, http.MethodPost, "/api/v1/asn-ranges", body, http.StatusUnprocessableEntity, nil)
		if got := field(t, w); got != f {
			t.Errorf("POST %s: field %q, want %q", body, got, f)
		}
	}
	var rg ipam.ASNRange
	e.expect(e.org, http.MethodPost, "/api/v1/asn-ranges", `{"name":"r","start":100,"end":200}`, http.StatusCreated, &rg)
	// Against the stored bound: a start above the stored end.
	w := e.expect(e.org, http.MethodPatch, "/api/v1/asn-ranges/"+rg.ID, `{"start":300}`, http.StatusUnprocessableEntity, nil)
	if field(t, w) != "end" {
		t.Errorf("PATCH start above the stored end: %s", w.Body.String())
	}
}

func TestASNClientPrincipal(t *testing.T) {
	e := newASNEnv(t)
	var orgWide, own, foreign ipam.ASN
	e.expect(e.org, http.MethodPost, "/api/v1/asns", `{"number":64600,"name":"org","kind":"own"}`, http.StatusCreated, &orgWide)
	e.expect(e.org, http.MethodPost, "/api/v1/asns", `{"number":64601,"name":"c1","kind":"customer","client_id":"`+e.f.Client1+`"}`,
		http.StatusCreated, &own)
	e.expect(e.org, http.MethodPost, "/api/v1/asns", `{"number":64602,"name":"c2","kind":"customer","client_id":"`+e.f.Client2+`"}`,
		http.StatusCreated, &foreign)
	var orgRange ipam.ASNRange
	e.expect(e.org, http.MethodPost, "/api/v1/asn-ranges", `{"name":"org","start":64600,"end":64699}`, http.StatusCreated, &orgRange)

	var list struct {
		Data  []ipam.ASN `json:"data"`
		Total int        `json:"total"`
	}
	e.expect(e.client1, http.MethodGet, "/api/v1/asns", "", http.StatusOK, &list)
	ids := map[string]bool{}
	for _, a := range list.Data {
		ids[a.ID] = true
	}
	if !ids[orgWide.ID] || !ids[own.ID] || ids[foreign.ID] || list.Total != 2 {
		t.Errorf("client 1 sees %v (total %d); want the org-wide and its own, not client 2's", ids, list.Total)
	}
	e.expect(e.client1, http.MethodGet, "/api/v1/asns/"+foreign.ID, "", http.StatusNotFound, nil)
	e.expect(e.client1, http.MethodGet, "/api/v1/asn-ranges/"+orgRange.ID, "", http.StatusOK, nil)
	// Org-wide rows are read-only for the client principal: POST 403,
	// PATCH and DELETE 404.
	e.expect(e.client1, http.MethodPost, "/api/v1/asns", `{"number":64610,"name":"x","kind":"own"}`, http.StatusForbidden, nil)
	e.expect(e.client1, http.MethodPatch, "/api/v1/asns/"+orgWide.ID, `{"name":"x"}`, http.StatusNotFound, nil)
	e.expect(e.client1, http.MethodDelete, "/api/v1/asns/"+orgWide.ID, "", http.StatusNotFound, nil)
	e.expect(e.client1, http.MethodPost, "/api/v1/asn-ranges", `{"name":"c1-free","start":64700,"end":64710}`, http.StatusForbidden, nil)
	e.expect(e.client1, http.MethodPatch, "/api/v1/asn-ranges/"+orgRange.ID, `{"name":"x"}`, http.StatusNotFound, nil)
	// Its own client's rows it writes; another client is 422 client_id.
	e.expect(e.client1, http.MethodPost, "/api/v1/asns", `{"number":64611,"name":"c1b","kind":"customer","client_id":"`+e.f.Client1+`"}`,
		http.StatusCreated, nil)
	e.expect(e.client1, http.MethodPatch, "/api/v1/asns/"+own.ID, `{"name":"c1-renamed"}`, http.StatusOK, nil)
	w := e.expect(e.client1, http.MethodPost, "/api/v1/asns", `{"number":64612,"name":"x","kind":"customer","client_id":"`+e.f.Client2+`"}`,
		http.StatusUnprocessableEntity, nil)
	if field(t, w) != "client_id" {
		t.Errorf("foreign client: %s", w.Body.String())
	}
	e.expect(e.client1, http.MethodPost, "/api/v1/asn-ranges", `{"name":"c1-range","start":64800,"end":64810,"client_id":"`+e.f.Client1+`"}`,
		http.StatusCreated, nil)
	e.expect(e.client1, http.MethodDelete, "/api/v1/asns/"+foreign.ID, "", http.StatusNotFound, nil)
}

func TestASNChangedEvent(t *testing.T) {
	e := newASNEnv(t)
	var a ipam.ASN
	e.expect(e.org, http.MethodPost, "/api/v1/asns", `{"number":"AS4200000001","name":"ev","kind":"customer","client_id":"`+e.f.Client1+`"}`,
		http.StatusCreated, &a)
	e.expect(e.org, http.MethodPatch, "/api/v1/asns/"+a.ID, `{"name":"ev2"}`, http.StatusOK, nil)
	e.expect(e.org, http.MethodDelete, "/api/v1/asns/"+a.ID, "", http.StatusNoContent, nil)
	// A range writes no asn.changed.
	e.expect(e.org, http.MethodPost, "/api/v1/asn-ranges", `{"name":"ev","start":10,"end":20}`, http.StatusCreated, nil)
	rows, err := e.f.Admin.Query(context.Background(), `SELECT type, data FROM event_outbox WHERE organization_id = $1 AND type = 'asn.changed'
		ORDER BY sequence`, e.f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var changes []string
	for rows.Next() {
		var typ string
		var raw []byte
		if err := rows.Scan(&typ, &raw); err != nil {
			t.Fatal(err)
		}
		var d map[string]any
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		if d["id"] != a.ID || d["number"] != float64(4200000001) || d["asn"] != float64(4200000001) || d["client_id"] != e.f.Client1 {
			t.Errorf("asn.changed data %v", d)
		}
		changes = append(changes, d["change"].(string))
	}
	if len(changes) != 3 || changes[0] != "create" || changes[1] != "update" || changes[2] != "delete" {
		t.Errorf("asn.changed changes %v, want create, update, delete", changes)
	}
}

func TestASNListFilterRangeAndQuery(t *testing.T) {
	e := newASNEnv(t)
	for _, body := range []string{
		`{"number":64512,"name":"Transit Alpha","kind":"upstream"}`,
		`{"number":64513,"name":"Peer Beta","kind":"peer"}`,
		`{"number":"1.10","name":"Customer Gamma","kind":"customer","client_id":"` + e.f.Client1 + `"}`,
		`{"number":4200000000,"name":"IXP Delta","kind":"ixp"}`,
	} {
		e.expect(e.org, http.MethodPost, "/api/v1/asns", body, http.StatusCreated, nil)
	}
	var rg ipam.ASNRange
	e.expect(e.org, http.MethodPost, "/api/v1/asn-ranges", `{"name":"low","start":64512,"end":64512}`, http.StatusCreated, &rg)
	count := func(query string) int {
		t.Helper()
		var list struct {
			Data  []ipam.ASN `json:"data"`
			Total int        `json:"total"`
		}
		e.expect(e.org, http.MethodGet, "/api/v1/asns"+query, "", http.StatusOK, &list)
		if len(list.Data) != min(list.Total, 50) {
			t.Errorf("%s: %d items, total %d", query, len(list.Data), list.Total)
		}
		return list.Total
	}
	for query, want := range map[string]int{
		"":                          4,
		"?kind=peer":                1,
		"?client_id=" + e.f.Client1: 1,
		"?range_id=" + rg.ID:        1,
		"?q=beta":                   1,
		"?q=1.10":                   1,
		"?q=AS65546":                1,
		"?q=4200000000":             1,
		"?q=64086.59904":            1,
		"?q=%25":                    0,
		"?range_id=" + e.f.Client1:  0,
		"?kind=peer&q=Alpha":        0,
	} {
		if got := count(query); got != want {
			t.Errorf("GET /asns%s: %d, want %d", query, got, want)
		}
	}
	e.expect(e.org, http.MethodGet, "/api/v1/asns?kind=transit", "", http.StatusUnprocessableEntity, nil)
	// Paging: two pages of two with a next cursor.
	var page struct {
		Data       []ipam.ASN `json:"data"`
		HasMore    bool       `json:"has_more"`
		NextCursor string     `json:"next_cursor"`
	}
	e.expect(e.org, http.MethodGet, "/api/v1/asns?limit=2", "", http.StatusOK, &page)
	if len(page.Data) != 2 || !page.HasMore || page.NextCursor == "" || page.Data[0].Number != 64512 {
		t.Fatalf("first page %+v", page)
	}
	e.expect(e.org, http.MethodGet, "/api/v1/asns?limit=2&cursor="+page.NextCursor, "", http.StatusOK, &page)
	if len(page.Data) != 2 || page.HasMore || page.Data[1].Number != 4200000000 {
		t.Errorf("second page %+v", page)
	}
	var ranges struct {
		Total int `json:"total"`
	}
	e.expect(e.org, http.MethodGet, "/api/v1/asn-ranges", "", http.StatusOK, &ranges)
	if ranges.Total != 1 {
		t.Errorf("ranges total %d", ranges.Total)
	}
}
