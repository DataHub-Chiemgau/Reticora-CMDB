package ipam_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
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

// --- Allocation, usage, origin AS and prefixes (IPM-16, WP-1067).

// newRange creates a range of the organization (client "" = org-wide).
func (e *asnEnv) newRange(name string, start, end int64, clientID string) ipam.ASNRange {
	e.t.Helper()
	body := fmt.Sprintf(`{"name":%q,"start":%d,"end":%d}`, name, start, end)
	if clientID != "" {
		body = fmt.Sprintf(`{"name":%q,"start":%d,"end":%d,"client_id":%q}`, name, start, end, clientID)
	}
	var rg ipam.ASNRange
	e.expect(e.org, http.MethodPost, "/api/v1/asn-ranges", body, http.StatusCreated, &rg)
	return rg
}

// allocate allocates from the range and returns the AS number.
func (e *asnEnv) allocate(ctx context.Context, rangeID, body string, want int) ipam.ASN {
	e.t.Helper()
	var a ipam.ASN
	if want == http.StatusCreated {
		e.expect(ctx, http.MethodPost, "/api/v1/asn-ranges/"+rangeID+"/allocate", body, want, &a)
	} else {
		e.expect(ctx, http.MethodPost, "/api/v1/asn-ranges/"+rangeID+"/allocate", body, want, nil)
	}
	return a
}

// TestASNAllocateConcurrent: 20 parallel allocations from 64512–64600
// yield 20 different numbers without gap from 64512 on; every allocation
// writes asn.changed.
func TestASNAllocateConcurrent(t *testing.T) {
	e := newASNEnv(t)
	rg := e.newRange("parallel", 64512, 64600, "")
	const n = 20
	numbers := make([]int64, n)
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			e.mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/asn-ranges/"+rg.ID+"/allocate",
				bytes.NewBufferString(fmt.Sprintf(`{"name":"cust-%d","kind":"customer"}`, i))).WithContext(e.org))
			codes[i] = w.Code
			var a ipam.ASN
			_ = json.Unmarshal(w.Body.Bytes(), &a)
			numbers[i] = a.Number
		}()
	}
	wg.Wait()
	for i, c := range codes {
		if c != http.StatusCreated {
			t.Fatalf("allocation %d: %d", i, c)
		}
	}
	slices.Sort(numbers)
	for i, got := range numbers {
		if want := int64(64512 + i); got != want {
			t.Fatalf("numbers %v, want 64512 to 64531 without gap", numbers)
		}
	}
	var events int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND type = 'asn.changed'`,
		e.f.OrgA).Scan(&events); err != nil || events != n {
		t.Errorf("asn.changed events %d (%v), want %d", events, err, n)
	}
}

// TestASNAllocateFillsGap: with 64513 created by hand, the allocations
// take 64512, then 64514; the reserved 65535 is skipped (to 65536) and a
// client principal allocates from an org-wide range for its client.
func TestASNAllocateFillsGap(t *testing.T) {
	e := newASNEnv(t)
	rg := e.newRange("gap", 64512, 64520, "")
	e.expect(e.org, http.MethodPost, "/api/v1/asns", `{"number":64513,"name":"manual","kind":"peer"}`, http.StatusCreated, nil)
	if a := e.allocate(e.org, rg.ID, `{"name":"first","kind":"customer"}`, http.StatusCreated); a.Number != 64512 || a.Name != "first" {
		t.Fatalf("first allocation %+v", a)
	}
	if a := e.allocate(e.org, rg.ID, `{"name":"second","kind":"customer","rir":"private"}`, http.StatusCreated); a.Number != 64514 ||
		a.RIR == nil || *a.RIR != "private" {
		t.Fatalf("second allocation %+v", a)
	}
	// A number of client 2 in the range is taken although client 1 does
	// not see it.
	e.expect(e.org, http.MethodPost, "/api/v1/asns", `{"number":64515,"name":"c2","kind":"customer","client_id":"`+e.f.Client2+`"}`,
		http.StatusCreated, nil)
	a := e.allocate(e.client1, rg.ID, `{"name":"c1","kind":"customer","client_id":"`+e.f.Client1+`"}`, http.StatusCreated)
	if a.Number != 64516 || a.ClientID == nil || *a.ClientID != e.f.Client1 {
		t.Errorf("client 1 allocation %+v", a)
	}
	// Across the reserved 65535.
	edge := e.newRange("edge", 65534, 65537, "")
	if a := e.allocate(e.org, edge.ID, `{"name":"e1","kind":"own"}`, http.StatusCreated); a.Number != 65534 {
		t.Errorf("edge 1: %+v", a)
	}
	if a := e.allocate(e.org, edge.ID, `{"name":"e2","kind":"own"}`, http.StatusCreated); a.Number != 65536 || a.ASDot != "1.0" {
		t.Errorf("edge 2 skips 65535: %+v", a)
	}
	// Field rules and unknown ranges.
	for body, f := range map[string]string{`{"name":" ","kind":"own"}`: "name", `{"name":"x","kind":"transit"}`: "kind",
		`{"name":"x","kind":"own","rir":"x"}`: "rir"} {
		if w := e.call(e.org, http.MethodPost, "/api/v1/asn-ranges/"+rg.ID+"/allocate", body); w.Code != http.StatusUnprocessableEntity ||
			field(t, w) != f {
			t.Errorf("POST %s: %d %s, want 422 %s", body, w.Code, w.Body.String(), f)
		}
	}
	e.allocate(e.org, e.f.ID(), `{"name":"x","kind":"own"}`, http.StatusNotFound)
	e.allocate(e.orgB, rg.ID, `{"name":"x","kind":"own"}`, http.StatusNotFound)
}

// TestASNAllocateFullRange409: a range without a free number is 409
// range; a client other than the client of the range is 422 client_id; an
// allocation without client from a client's range takes that client.
func TestASNAllocateFullRange409(t *testing.T) {
	e := newASNEnv(t)
	rg := e.newRange("tiny", 64700, 64701, "")
	e.allocate(e.org, rg.ID, `{"name":"a","kind":"own"}`, http.StatusCreated)
	e.allocate(e.org, rg.ID, `{"name":"b","kind":"own"}`, http.StatusCreated)
	w := e.call(e.org, http.MethodPost, "/api/v1/asn-ranges/"+rg.ID+"/allocate", `{"name":"c","kind":"own"}`)
	if w.Code != http.StatusConflict || field(t, w) != "range" {
		t.Errorf("full range: %d %s, want 409 range", w.Code, w.Body.String())
	}
	// Only a reserved number left (23456) is full as well.
	reserved := e.newRange("reserved", 23455, 23457, "")
	e.allocate(e.org, reserved.ID, `{"name":"r1","kind":"own"}`, http.StatusCreated)
	if a := e.allocate(e.org, reserved.ID, `{"name":"r2","kind":"own"}`, http.StatusCreated); a.Number != 23457 {
		t.Errorf("allocation after 23456: %+v", a)
	}
	if w = e.call(e.org, http.MethodPost, "/api/v1/asn-ranges/"+reserved.ID+"/allocate", `{"name":"r","kind":"own"}`); w.Code != http.StatusConflict {
		t.Errorf("reserved range: %d %s", w.Code, w.Body.String())
	}
	client := e.newRange("client-1", 64800, 64810, e.f.Client1)
	if w = e.call(e.org, http.MethodPost, "/api/v1/asn-ranges/"+client.ID+"/allocate",
		`{"name":"x","kind":"customer","client_id":"`+e.f.Client2+`"}`); w.Code != http.StatusUnprocessableEntity || field(t, w) != "client_id" {
		t.Errorf("foreign client: %d %s", w.Code, w.Body.String())
	}
	if a := e.allocate(e.org, client.ID, `{"name":"y","kind":"customer"}`, http.StatusCreated); a.ClientID == nil || *a.ClientID != e.f.Client1 ||
		a.Number != 64800 {
		t.Errorf("allocation of the client range %+v", a)
	}
	// Client 2 does not see the range of client 1.
	client2 := tenant.WithTenant(e.f.ClientCtx(e.f.Client2), tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: e.f.User})
	e.allocate(client2, client.ID, `{"name":"z","kind":"customer"}`, http.StatusNotFound)
}

// TestASNAllocate32Bit: a 32-bit range 4200000000–4200000002 allocates
// its numbers in order with their asdot form, then is full.
func TestASNAllocate32Bit(t *testing.T) {
	e := newASNEnv(t)
	rg := e.newRange("private-32", 4200000000, 4200000002, "")
	for i, want := range []string{"64086.59904", "64086.59905", "64086.59906"} {
		a := e.allocate(e.org, rg.ID, fmt.Sprintf(`{"name":"p%d","kind":"customer"}`, i), http.StatusCreated)
		if a.Number != 4200000000+int64(i) || a.ASDot != want {
			t.Errorf("allocation %d: %+v", i, a)
		}
	}
	e.allocate(e.org, rg.ID, `{"name":"p3","kind":"customer"}`, http.StatusConflict)
}

// TestASNRangeUsage: total counts the usable numbers (without 65535),
// used the AS numbers of the organization in the range (also manual
// ones), free and percent follow; a client principal counts only the rows
// it sees and gets scoped true.
func TestASNRangeUsage(t *testing.T) {
	e := newASNEnv(t)
	rg := e.newRange("usage", 65530, 65539, "")
	e.allocate(e.org, rg.ID, `{"name":"a","kind":"own"}`, http.StatusCreated)
	e.expect(e.org, http.MethodPost, "/api/v1/asns", `{"number":65532,"name":"manual","kind":"peer"}`, http.StatusCreated, nil)
	e.expect(e.org, http.MethodPost, "/api/v1/asns", `{"number":65533,"name":"c2","kind":"peer","client_id":"`+e.f.Client2+`"}`,
		http.StatusCreated, nil)
	e.expect(e.org, http.MethodPost, "/api/v1/asns", `{"number":64000,"name":"outside","kind":"peer"}`, http.StatusCreated, nil)
	var u ipam.ASNRangeUsage
	e.expect(e.org, http.MethodGet, "/api/v1/asn-ranges/"+rg.ID+"/usage", "", http.StatusOK, &u)
	if u.Total != 9 || u.Used != 3 || u.Free != 6 || u.Percent != 33.33 || u.Scoped || u.RangeID != rg.ID {
		t.Errorf("usage %+v", u)
	}
	e.expect(e.client1, http.MethodGet, "/api/v1/asn-ranges/"+rg.ID+"/usage", "", http.StatusOK, &u)
	if u.Used != 2 || !u.Scoped {
		t.Errorf("usage of client 1 %+v", u)
	}
	// A principal restricted to sites sees every AS number (the read
	// policy narrows by client only): complete count, not scoped.
	siteScope := database.OrgWideScope(e.f.OrgA, e.f.User)
	siteScope.Sites = database.ScopeIDs(e.f.ID())
	site := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &siteScope),
		tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: e.f.User})
	e.expect(site, http.MethodGet, "/api/v1/asn-ranges/"+rg.ID+"/usage", "", http.StatusOK, &u)
	if u.Used != 3 || u.Scoped {
		t.Errorf("usage of a site principal %+v, want used 3 and not scoped", u)
	}
	e.expect(e.org, http.MethodGet, "/api/v1/asn-ranges/"+e.f.ID()+"/usage", "", http.StatusNotFound, nil)
}

// subnet returns a subnet of client (empty: org-wide) created through
// the routes.
func (e *asnEnv) subnet(cidr, clientID, originASN string, want int) ipam.Subnet {
	e.t.Helper()
	body := fmt.Sprintf(`{"cidr":%q`, cidr)
	if clientID != "" {
		body += fmt.Sprintf(`,"client_id":%q`, clientID)
	}
	if originASN != "" {
		body += fmt.Sprintf(`,"origin_asn_id":%q`, originASN)
	}
	body += "}"
	var s ipam.Subnet
	if want == http.StatusCreated {
		e.expect(e.org, http.MethodPost, "/api/v1/subnets", body, want, &s)
	} else if w := e.expect(e.org, http.MethodPost, "/api/v1/subnets", body, want, nil); field(e.t, w) != "origin_asn_id" {
		e.t.Errorf("POST %s: %s, want field origin_asn_id", body, w.Body.String())
	}
	return s
}

// TestSubnetOriginASN: POST and PATCH set origin_asn_id, "" clears it; the
// subnet shows origin_asn {id, number, asdot, name}; an AS number of
// another client is 422 origin_asn_id, an org-wide one is allowed; an
// invisible or unknown one is 422; an AS number does not move to a client
// while a subnet of another client names it (422 client_id); the list
// filters by origin_asn_id; deleting the AS number sets NULL; the history
// records the change.
func TestSubnetOriginASN(t *testing.T) {
	e := newASNEnv(t)
	var own, c2 ipam.ASN
	e.expect(e.org, http.MethodPost, "/api/v1/asns", `{"number":"AS1.10","name":"own","kind":"own"}`, http.StatusCreated, &own)
	e.expect(e.org, http.MethodPost, "/api/v1/asns", `{"number":64999,"name":"c2","kind":"customer","client_id":"`+e.f.Client2+`"}`,
		http.StatusCreated, &c2)
	s := e.subnet("10.67.0.0/24", e.f.Client1, own.ID, http.StatusCreated)
	if s.OriginASNID != own.ID || s.OriginASN == nil || s.OriginASN.Number != 65546 || s.OriginASN.ASDot != "1.10" ||
		s.OriginASN.Name != "own" || s.OriginASN.ID != own.ID {
		t.Fatalf("subnet %+v origin %+v", s, s.OriginASN)
	}
	e.subnet("10.67.1.0/24", e.f.Client1, c2.ID, http.StatusUnprocessableEntity)
	e.subnet("10.67.2.0/24", e.f.Client1, e.f.ID(), http.StatusUnprocessableEntity)
	e.subnet("10.67.3.0/24", "", "not-a-uuid", http.StatusUnprocessableEntity)
	// Client 1 does not see the AS number of client 2: 422 as unknown.
	w := e.call(e.client1, http.MethodPatch, "/api/v1/subnets/"+s.ID, `{"origin_asn_id":"`+c2.ID+`"}`)
	if w.Code != http.StatusUnprocessableEntity || field(t, w) != "origin_asn_id" {
		t.Errorf("client 1 with the AS number of client 2: %d %s", w.Code, w.Body.String())
	}
	// Moving the subnet to client 2 with an AS number of client 2 is fine.
	var moved ipam.Subnet
	e.expect(e.org, http.MethodPatch, "/api/v1/subnets/"+s.ID, `{"client_id":"`+e.f.Client2+`","origin_asn_id":"`+c2.ID+`"}`,
		http.StatusOK, &moved)
	if moved.OriginASNID != c2.ID {
		t.Errorf("moved %+v", moved)
	}
	// Moving it back to client 1 with the origin AS of client 2 is refused.
	if w = e.call(e.org, http.MethodPatch, "/api/v1/subnets/"+s.ID, `{"client_id":"`+e.f.Client1+`"}`); w.Code != http.StatusUnprocessableEntity ||
		field(t, w) != "origin_asn_id" {
		t.Errorf("move with a foreign origin AS: %d %s", w.Code, w.Body.String())
	}
	var cleared ipam.Subnet
	e.expect(e.org, http.MethodPatch, "/api/v1/subnets/"+s.ID, `{"origin_asn_id":""}`, http.StatusOK, &cleared)
	if cleared.OriginASNID != "" || cleared.OriginASN != nil {
		t.Errorf("cleared %+v", cleared)
	}
	e.expect(e.org, http.MethodPatch, "/api/v1/subnets/"+s.ID, `{"origin_asn_id":"`+c2.ID+`"}`, http.StatusOK, nil)
	other := e.subnet("10.67.4.0/24", "", own.ID, http.StatusCreated)
	var page struct {
		Data []ipam.Subnet `json:"data"`
	}
	e.expect(e.org, http.MethodGet, "/api/v1/subnets?origin_asn_id="+own.ID, "", http.StatusOK, &page)
	if len(page.Data) != 1 || page.Data[0].ID != other.ID {
		t.Errorf("filter by origin AS: %+v", page.Data)
	}
	if w = e.call(e.org, http.MethodGet, "/api/v1/subnets?origin_asn_id=x", ""); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("malformed filter: %d", w.Code)
	}
	// Moving an org-wide AS number to a client while a subnet of another
	// client (or an org-wide subnet) has it as origin AS is 422 client_id;
	// once only subnets of that client remain, the move is fine.
	var shared ipam.ASN
	e.expect(e.org, http.MethodPost, "/api/v1/asns", `{"number":64998,"name":"shared","kind":"own"}`, http.StatusCreated, &shared)
	e.subnet("10.67.5.0/24", e.f.Client1, shared.ID, http.StatusCreated)
	y := e.subnet("10.67.6.0/24", e.f.Client2, shared.ID, http.StatusCreated)
	for _, client := range []string{e.f.Client1, e.f.Client2} {
		if w = e.call(e.org, http.MethodPatch, "/api/v1/asns/"+shared.ID, `{"client_id":"`+client+`"}`); w.Code != http.StatusUnprocessableEntity ||
			field(t, w) != "client_id" {
			t.Errorf("move the shared origin AS to %s: %d %s", client, w.Code, w.Body.String())
		}
	}
	e.expect(e.org, http.MethodPatch, "/api/v1/subnets/"+y.ID, `{"origin_asn_id":""}`, http.StatusOK, nil)
	var movedASN ipam.ASN
	e.expect(e.org, http.MethodPatch, "/api/v1/asns/"+shared.ID, `{"client_id":"`+e.f.Client1+`"}`, http.StatusOK, &movedASN)
	if movedASN.ClientID == nil || *movedASN.ClientID != e.f.Client1 {
		t.Errorf("moved AS number %+v", movedASN)
	}
	e.expect(e.org, http.MethodPatch, "/api/v1/asns/"+shared.ID, `{"client_id":""}`, http.StatusOK, nil)
	// Deleting the AS number clears the reference.
	e.expect(e.org, http.MethodDelete, "/api/v1/asns/"+c2.ID, "", http.StatusNoContent, nil)
	var after ipam.Subnet
	e.expect(e.org, http.MethodGet, "/api/v1/subnets/"+s.ID, "", http.StatusOK, &after)
	if after.OriginASNID != "" || after.OriginASN != nil {
		t.Errorf("after deleting the AS number: %+v", after)
	}
	var changes int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM ipam_change WHERE organization_id = $1 AND object_id = $2
		AND field_name = 'origin_asn_id'`, e.f.OrgA, s.ID).Scan(&changes); err != nil || changes == 0 {
		t.Errorf("history of origin_asn_id: %d %v", changes, err)
	}
	// --no-db: the memory repository passes origin_asn_id through.
	mem := ipam.NewMemoryRepository()
	ms := &ipam.Subnet{OrganizationID: e.f.OrgA, CIDR: "10.68.0.0/24", OriginASNID: own.ID}
	if err := mem.CreateSubnet(context.Background(), ms); err != nil || ms.OriginASNID != own.ID {
		t.Fatalf("memory create %+v %v", ms, err)
	}
	empty := ""
	if got, err := mem.UpdateSubnet(context.Background(), e.f.OrgA, ms.ID, &ipam.UpdateSubnetRequest{OriginASNID: &empty}); err != nil ||
		got.OriginASNID != "" {
		t.Errorf("memory clear %+v %v", got, err)
	}
}

// TestASNPrefixes: GET /asns/{id}/prefixes lists the visible subnets with
// the origin AS (id, CIDR, VRF, client), paginated; a client principal
// sees only those of its client; an unknown AS number is 404.
func TestASNPrefixes(t *testing.T) {
	e := newASNEnv(t)
	var own ipam.ASN
	e.expect(e.org, http.MethodPost, "/api/v1/asns", `{"number":64900,"name":"own","kind":"own"}`, http.StatusCreated, &own)
	a := e.subnet("10.69.0.0/24", e.f.Client1, own.ID, http.StatusCreated)
	b := e.subnet("10.69.1.0/24", e.f.Client2, own.ID, http.StatusCreated)
	e.subnet("10.69.2.0/24", "", "", http.StatusCreated)
	type page struct {
		Data       []ipam.ASNPrefix `json:"data"`
		Total      int              `json:"total"`
		HasMore    bool             `json:"has_more"`
		NextCursor string           `json:"next_cursor"`
	}
	var p page
	e.expect(e.org, http.MethodGet, "/api/v1/asns/"+own.ID+"/prefixes", "", http.StatusOK, &p)
	if p.Total != 2 || len(p.Data) != 2 || p.Data[0].ID != a.ID || p.Data[1].ID != b.ID || p.Data[0].CIDR != "10.69.0.0/24" ||
		p.Data[0].VRFID == "" || p.Data[0].ClientID == nil || *p.Data[0].ClientID != e.f.Client1 {
		t.Errorf("prefixes %+v", p)
	}
	e.expect(e.org, http.MethodGet, "/api/v1/asns/"+own.ID+"/prefixes?limit=1", "", http.StatusOK, &p)
	if len(p.Data) != 1 || !p.HasMore || p.Total != 2 {
		t.Errorf("first page %+v", p)
	}
	e.expect(e.client1, http.MethodGet, "/api/v1/asns/"+own.ID+"/prefixes", "", http.StatusOK, &p)
	if p.Total != 1 || p.Data[0].ID != a.ID {
		t.Errorf("prefixes of client 1 %+v", p)
	}
	e.expect(e.org, http.MethodGet, "/api/v1/asns/"+e.f.ID()+"/prefixes", "", http.StatusNotFound, nil)
}
