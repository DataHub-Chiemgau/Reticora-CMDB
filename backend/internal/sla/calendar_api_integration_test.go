package sla_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/sla"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Retest (WP-768, TKT-02):
//
//	cd backend && go test -count=1 -p 1 -run 'TestBusinessCalendarAPI' ./internal/sla/

// doAs runs a request in the request context ctx of the principal of org.
func (w *slaWorld) doAs(t *testing.T, ctx context.Context, org, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req = req.WithContext(tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: org, UserID: w.f.User}))
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, req)
	return rec
}

func decodeCalendar(t *testing.T, step string, rec *httptest.ResponseRecorder, want int) sla.BusinessCalendar {
	t.Helper()
	var c sla.BusinessCalendar
	if rec.Code != want || json.Unmarshal(rec.Body.Bytes(), &c) != nil {
		t.Fatalf("%s: got %d %s, want %d", step, rec.Code, rec.Body.String(), want)
	}
	return c
}

func expectProblem(t *testing.T, step string, rec *httptest.ResponseRecorder, status int, field string) {
	t.Helper()
	var p api.FieldProblem
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if rec.Code != status || p.Field != field {
		t.Fatalf("%s: got %d %s, want %d on %q", step, rec.Code, rec.Body.String(), status, field)
	}
}

// TestBusinessCalendarAPICRUD covers the REST maintenance of business
// calendars over PostgreSQL (TKT-02, WP-768): create, read, list, update
// and delete; each validation rule of Calendar.Validate is 400 with its
// field, client_id in PATCH is 400, a second default per organization and
// client is 409 on is_default, another organization gets 404. Deleting a
// calendar clears sla.calendar_id; a due time computed afterwards follows
// the org default.
func TestBusinessCalendarAPICRUD(t *testing.T) {
	w := newSLAWorld(t)
	ctx := context.Background()
	const base = "/api/v1/business-calendars"

	for _, c := range []struct{ step, body, field string }{
		{"empty name", `{"name":""}`, "name"},
		{"time zone", `{"name":"x","timezone":"Mars/Olympus"}`, "timezone"},
		{"weekday", `{"name":"x","weekly_hours":[{"weekday":0,"start":"08:00","end":"10:00"}]}`, "weekly_hours"},
		{"start not before end", `{"name":"x","weekly_hours":[{"weekday":1,"start":"10:00","end":"10:00"}]}`, "weekly_hours"},
		{"overlap", `{"name":"x","weekly_hours":[{"weekday":2,"start":"08:00","end":"12:00"},{"weekday":2,"start":"11:59","end":"13:00"}]}`, "weekly_hours"},
		{"empty windows", `{"name":"x","weekly_hours":[]}`, "weekly_hours"},
		{"duplicate holiday", `{"name":"x","holidays":[{"date":"2026-12-25"},{"date":"2026-12-25"}]}`, "holidays"},
		{"unknown client", `{"name":"x","client_id":"` + w.f.ID() + `"}`, "client_id"},
		{"malformed client", `{"name":"x","client_id":"c1"}`, "client_id"},
	} {
		expectProblem(t, c.step, w.do(t, http.MethodPost, base, c.body), http.StatusBadRequest, c.field)
	}

	orgDefault := decodeCalendar(t, "create org default", w.do(t, http.MethodPost, base,
		`{"name":"Org","is_default":true,"weekly_hours":`+weekdays+`,"holidays":[{"date":"2026-08-10","name":"Betriebsruhe"}]}`), http.StatusCreated)
	if orgDefault.ClientID != nil || orgDefault.Timezone != nil || len(orgDefault.WeeklyHours) != 5 || len(orgDefault.Holidays) != 1 || !orgDefault.IsDefault {
		t.Fatalf("org default: %+v", orgDefault)
	}
	expectProblem(t, "second org default", w.do(t, http.MethodPost, base, `{"name":"Org 2","is_default":true}`), http.StatusConflict, "is_default")
	c1 := decodeCalendar(t, "create client-1 default", w.do(t, http.MethodPost, base,
		`{"name":"C1","client_id":"`+w.f.Client1+`","timezone":"Europe/Vienna","is_default":true}`), http.StatusCreated)
	if c1.ClientID == nil || *c1.ClientID != w.f.Client1 || c1.Timezone == nil || len(c1.WeeklyHours) != 5 {
		t.Fatalf("client-1 calendar: %+v", c1)
	}
	expectProblem(t, "second client-1 default", w.do(t, http.MethodPost, base, `{"name":"C1 b","client_id":"`+w.f.Client1+`","is_default":true}`), http.StatusConflict, "is_default")

	path := base + "/" + c1.ID
	if got := decodeCalendar(t, "get", w.do(t, http.MethodGet, path, ""), http.StatusOK); got.Name != "C1" {
		t.Fatalf("get: %+v", got)
	}
	expectProblem(t, "patch client_id", w.do(t, http.MethodPatch, path, `{"client_id":"`+w.f.Client2+`"}`), http.StatusBadRequest, "client_id")
	expectProblem(t, "patch overlap", w.do(t, http.MethodPatch, path, `{"weekly_hours":[{"weekday":1,"start":"08:00","end":"12:00"},{"weekday":1,"start":"09:00","end":"10:00"}]}`), http.StatusBadRequest, "weekly_hours")
	expectProblem(t, "patch time zone", w.do(t, http.MethodPatch, path, `{"timezone":"EST"}`), http.StatusBadRequest, "timezone")
	expectProblem(t, "patch empty windows", w.do(t, http.MethodPatch, path, `{"weekly_hours":[]}`), http.StatusBadRequest, "weekly_hours")
	expectProblem(t, "patch duplicate holiday", w.do(t, http.MethodPatch, path, `{"holidays":[{"date":"2026-01-06"},{"date":"2026-01-06"}]}`), http.StatusBadRequest, "holidays")
	patched := decodeCalendar(t, "patch", w.do(t, http.MethodPatch, path, `{"name":"C1 Wien","timezone":null,"holidays":[{"date":"2026-01-06","name":"Dreikönig"}]}`), http.StatusOK)
	if patched.Name != "C1 Wien" || patched.Timezone != nil || len(patched.Holidays) != 1 || patched.ClientID == nil || *patched.ClientID != w.f.Client1 || !patched.IsDefault {
		t.Fatalf("patched: %+v", patched)
	}

	// Another organization sees nothing of it.
	for _, m := range []string{http.MethodGet, http.MethodPatch, http.MethodDelete} {
		if rec := w.doAs(t, w.f.OrgCtx(w.f.OrgB), w.f.OrgB, m, path, `{"name":"stolen"}`); rec.Code != http.StatusNotFound {
			t.Fatalf("org B %s: %d %s, want 404", m, rec.Code, rec.Body.String())
		}
	}
	if rec := w.doAs(t, w.f.OrgCtx(w.f.OrgB), w.f.OrgB, http.MethodGet, base, ""); rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"data":[]`)) {
		t.Fatalf("org B list: %d %s", rec.Code, rec.Body.String())
	}

	// A policy with its own calendar; deleting the calendar clears
	// sla.calendar_id and the next computation follows the org default.
	own := decodeCalendar(t, "create policy calendar", w.do(t, http.MethodPost, base, `{"name":"Always","weekly_hours":[{"weekday":1,"start":"00:00","end":"24:00"},{"weekday":2,"start":"00:00","end":"24:00"},{"weekday":3,"start":"00:00","end":"24:00"},{"weekday":4,"start":"00:00","end":"24:00"},{"weekday":5,"start":"00:00","end":"24:00"},{"weekday":6,"start":"00:00","end":"24:00"},{"weekday":7,"start":"00:00","end":"24:00"}],"timezone":"UTC"}`), http.StatusCreated)
	policy := w.policy(t, &sla.Policy{Name: "org", Priority: "medium", BusinessCalendar: true, CalendarID: &own.ID})
	// Friday, 07.08.2026, 15:00 UTC (17:00 CEST).
	tk := w.ticket(t, "medium", "", mustTime(t, "2026-08-07T15:00:00Z"))
	if s := w.apply(t, tk, ""); calendarOf(s) != own.ID || !s.ResponseDueAt.Equal(mustTime(t, "2026-08-07T17:00:00Z")) {
		t.Fatalf("with own calendar: calendar %q due %s", calendarOf(s), s.ResponseDueAt)
	}
	if rec := w.do(t, http.MethodDelete, base+"/"+own.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.do(t, http.MethodGet, base+"/"+own.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: %d", rec.Code)
	}
	var policyCalendar, stateCalendar *string
	if err := w.f.Admin.QueryRow(ctx, `SELECT s.calendar_id::text, ts.calendar_id::text FROM sla s JOIN ticket_sla ts ON ts.sla_id = s.id WHERE s.id = $1`, policy.ID).Scan(&policyCalendar, &stateCalendar); err != nil {
		t.Fatal(err)
	}
	if policyCalendar != nil || stateCalendar != nil {
		t.Fatalf("after delete: sla.calendar_id %v, ticket_sla.calendar_id %v, want NULL", policyCalendar, stateCalendar)
	}
	// Org default in Berlin with Monday 10.08. as holiday: Friday 17–18,
	// Tuesday 08–09 CEST.
	if s := w.apply(t, tk, ""); calendarOf(s) != orgDefault.ID || !s.ResponseDueAt.Equal(mustTime(t, "2026-08-11T07:00:00Z")) {
		t.Fatalf("after delete: calendar %q due %s, want org default and 2026-08-11T07:00:00Z", calendarOf(s), s.ResponseDueAt)
	}
}

// TestBusinessCalendarAPIClientScope (E-09): a principal restricted to
// client 1 sees the org-wide and its own calendars, not those of client 2;
// it cannot create an org-wide calendar (403) nor one of client 2 (400 on
// client_id); PATCH and DELETE of an org-wide calendar are 404 and leave it
// unchanged.
func TestBusinessCalendarAPIClientScope(t *testing.T) {
	w := newSLAWorld(t)
	const base = "/api/v1/business-calendars"
	orgCal := decodeCalendar(t, "org calendar", w.do(t, http.MethodPost, base, `{"name":"Org"}`), http.StatusCreated)
	c2Cal := decodeCalendar(t, "client-2 calendar", w.do(t, http.MethodPost, base, `{"name":"C2","client_id":"`+w.f.Client2+`"}`), http.StatusCreated)

	scoped := w.f.ClientCtx(w.f.Client1)
	as := func(method, path, body string) *httptest.ResponseRecorder {
		return w.doAs(t, scoped, w.f.OrgA, method, path, body)
	}
	if rec := as(http.MethodPost, base, `{"name":"Org by c1"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("org-wide create by client-1 principal: %d %s, want 403", rec.Code, rec.Body.String())
	}
	expectProblem(t, "client-2 create by client-1 principal", as(http.MethodPost, base, `{"name":"C2 by c1","client_id":"`+w.f.Client2+`"}`), http.StatusBadRequest, "client_id")
	c1Cal := decodeCalendar(t, "own create", as(http.MethodPost, base, `{"name":"C1","client_id":"`+w.f.Client1+`","is_default":true}`), http.StatusCreated)

	rec := as(http.MethodGet, base, "")
	var list sla.BusinessCalendarListResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	seen := map[string]bool{}
	for _, c := range list.Data {
		seen[c.ID] = true
	}
	if !seen[orgCal.ID] || !seen[c1Cal.ID] || seen[c2Cal.ID] || list.Total != 2 {
		t.Fatalf("client-1 list: %+v, want the org-wide and the own calendar", list)
	}
	if rec = as(http.MethodGet, base+"/"+c2Cal.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("client-2 calendar for client-1 principal: %d", rec.Code)
	}
	if rec = as(http.MethodGet, base+"/"+orgCal.ID, ""); rec.Code != http.StatusOK {
		t.Fatalf("org calendar for client-1 principal: %d", rec.Code)
	}
	for _, m := range []string{http.MethodPatch, http.MethodDelete} {
		if rec = as(m, base+"/"+orgCal.ID, `{"name":"changed"}`); rec.Code != http.StatusNotFound {
			t.Fatalf("%s org calendar by client-1 principal: %d %s, want 404", m, rec.Code, rec.Body.String())
		}
		if rec = as(m, base+"/"+c2Cal.ID, `{"name":"changed"}`); rec.Code != http.StatusNotFound {
			t.Fatalf("%s client-2 calendar by client-1 principal: %d, want 404", m, rec.Code)
		}
	}
	if got := decodeCalendar(t, "org calendar after", w.do(t, http.MethodGet, base+"/"+orgCal.ID, ""), http.StatusOK); got.Name != "Org" {
		t.Fatalf("org calendar changed: %+v", got)
	}
	if got := decodeCalendar(t, "own patch", as(http.MethodPatch, base+"/"+c1Cal.ID, `{"name":"C1 neu"}`), http.StatusOK); got.Name != "C1 neu" {
		t.Fatalf("own patch: %+v", got)
	}
	if rec = as(http.MethodDelete, base+"/"+c1Cal.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("own delete: %d", rec.Code)
	}
}

// TestBusinessCalendarAPIPaging: the list is sorted by name and id and
// paged with the signed keyset cursor; the client_id filter narrows it, a
// forged cursor is 400 (API-02).
func TestBusinessCalendarAPIPaging(t *testing.T) {
	w := newSLAWorld(t)
	const base = "/api/v1/business-calendars"
	for _, name := range []string{"Echo", "Alpha", "Delta", "Bravo", "Charlie", "Alpha"} {
		decodeCalendar(t, "create "+name, w.do(t, http.MethodPost, base, `{"name":"`+name+`"}`), http.StatusCreated)
	}
	decodeCalendar(t, "client calendar", w.do(t, http.MethodPost, base, `{"name":"Zulu","client_id":"`+w.f.Client1+`"}`), http.StatusCreated)

	var names []string
	ids := map[string]bool{}
	next := ""
	for pages := 0; pages < 10; pages++ {
		rec := w.do(t, http.MethodGet, base+"?limit=2&cursor="+url.QueryEscape(next), "")
		var list sla.BusinessCalendarListResponse
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil || list.Total != 7 || len(list.Data) > 2 {
			t.Fatalf("page %d: %d %s", pages, rec.Code, rec.Body.String())
		}
		for _, c := range list.Data {
			names = append(names, c.Name)
			ids[c.ID] = true
		}
		if next = list.NextCursor; next == "" {
			break
		}
	}
	want := []string{"Alpha", "Alpha", "Bravo", "Charlie", "Delta", "Echo", "Zulu"}
	if len(names) != len(want) || len(ids) != len(want) {
		t.Fatalf("pages: %v", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("pages: %v, want %v", names, want)
		}
	}
	rec := w.do(t, http.MethodGet, base+"?client_id="+w.f.Client1, "")
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"name":"Zulu"`)) || bytes.Contains(rec.Body.Bytes(), []byte(`"name":"Alpha"`)) {
		t.Fatalf("client filter: %d %s", rec.Code, rec.Body.String())
	}
	if rec = w.do(t, http.MethodGet, base+"?cursor=eyJ2IjoiQSJ9.forged", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("forged cursor: %d", rec.Code)
	}
}
