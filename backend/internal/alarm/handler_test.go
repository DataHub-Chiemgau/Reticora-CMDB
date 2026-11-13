package alarm_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The REST routes of the alarms (ALR-01, MON-04, WP-983) on the
// MemoryStore, behind the permission of each route as the server wires it.
// The PostgreSQL cases (audit, scope, truncated transitions) are in
// handler_integration_test.go.

const (
	memUser  = "00000000-0000-4000-8000-0000000000b1"
	otherOrg = "00000000-0000-4000-8000-0000000000a2"
)

// alarmRoutes are the routes of the handler.
var alarmRoutes = []struct{ method, pattern string }{
	{http.MethodGet, "/api/v1/monitoring/alarms"},
	{http.MethodGet, "/api/v1/monitoring/alarms/{id}"},
	{http.MethodPost, "/api/v1/monitoring/alarms/{id}/ack"},
	{http.MethodPost, "/api/v1/monitoring/alarms/{id}/unack"},
}

// alarmMux mounts the handler on svc behind the permission of every route.
func alarmMux(svc *alarm.Service) http.Handler {
	inner := chi.NewRouter()
	alarm.NewHandler(svc).RegisterRoutes(inner)
	mux := chi.NewRouter()
	for _, rt := range alarmRoutes {
		mux.With(server.AuthorizeRoute(rt.method, rt.pattern)).Method(rt.method, rt.pattern, inner)
	}
	return mux
}

// call sends a request as a user of org with perms.
func call(t *testing.T, mux http.Handler, org string, perms []identity.Permission, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	p := identity.Principal{Subject: memUser, OrganizationID: org, Type: identity.PrincipalTypeUser, Permissions: perms}
	ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), perms)
	ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: org, UserID: memUser})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

var operator = []identity.Permission{identity.PermMonitoringRead, identity.PermAlertAcknowledge}

type listBody struct {
	Data        []alarm.Alarm `json:"data"`
	Total       int           `json:"total"`
	HasMore     bool          `json:"has_more"`
	NextCursor  string        `json:"next_cursor"`
	TotalCapped bool          `json:"total_capped"`
}

func decodeList(t *testing.T, rec *httptest.ResponseRecorder) listBody {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	var out listBody
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// raiseAt opens an alarm of series in org, first seen at at.
func raiseAt(t *testing.T, svc *alarm.Service, org, series, severity string, at time.Time) *alarm.Alarm {
	t.Helper()
	in := raiseIn(series, severity)
	in.ObservedAt = at
	in.Details = map[string]any{"value": 97.5, "threshold": 90, "rule_id": ruleID, "hostname": "secret-attr"}
	a, err := svc.Raise(context.Background(), org, in)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func ids(items []alarm.Alarm) []string {
	out := make([]string, 0, len(items))
	for i := range items {
		out = append(out, items[i].ID)
	}
	return out
}

func TestAlarmListFiltersAndCursor(t *testing.T) {
	svc, _ := memService()
	mux := alarmMux(svc)
	base := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	var want []string
	for i := range 5 {
		sev := alarm.SeverityWarning
		if i%2 == 0 {
			sev = alarm.SeverityCritical
		}
		a := raiseAt(t, svc, memOrg, "s"+string(rune('a'+i)), sev, base.Add(time.Duration(i)*time.Minute))
		want = append([]string{a.ID}, want...) // newest first
	}
	// Pages of two, newest first, through next_cursor.
	var got []string
	path := "/api/v1/monitoring/alarms?limit=2"
	for range 4 {
		page := decodeList(t, call(t, mux, memOrg, operator, http.MethodGet, path, ""))
		if page.Total != 5 {
			t.Errorf("total %d, want 5", page.Total)
		}
		got = append(got, ids(page.Data)...)
		if !page.HasMore {
			if page.NextCursor != "" {
				t.Error("next_cursor on the last page")
			}
			break
		}
		path = "/api/v1/monitoring/alarms?limit=2&cursor=" + url.QueryEscape(page.NextCursor)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("paged ids %v, want %v", got, want)
	}
	// Details keep the value, the threshold and the rule, nothing else.
	page := decodeList(t, call(t, mux, memOrg, operator, http.MethodGet, "/api/v1/monitoring/alarms?severity=critical", ""))
	if page.Total != 3 || len(page.Data) != 3 {
		t.Fatalf("critical: %d of total %d, want 3", len(page.Data), page.Total)
	}
	for _, a := range page.Data {
		if a.Severity != alarm.SeverityCritical {
			t.Errorf("severity filter returned %s", a.Severity)
		}
		if _, leaked := a.Details["hostname"]; leaked || a.Details["threshold"] == nil || a.Details["rule_id"] != ruleID {
			t.Errorf("details %v: want value, threshold and rule_id only", a.Details)
		}
	}
	// from/to select on opened_at.
	from := base.Add(90 * time.Second).Format(time.RFC3339)
	to := base.Add(210 * time.Second).Format(time.RFC3339)
	page = decodeList(t, call(t, mux, memOrg, operator, http.MethodGet, "/api/v1/monitoring/alarms?from="+from+"&to="+to, ""))
	if page.Total != 2 {
		t.Errorf("opened between 1:30 and 3:30: %d, want 2", page.Total)
	}
	page = decodeList(t, call(t, mux, memOrg, operator, http.MethodGet,
		"/api/v1/monitoring/alarms?source_kind=rule&suppressed=false&state=open,acknowledged", ""))
	if page.Total != 5 {
		t.Errorf("source_kind, suppressed and state: %d, want 5", page.Total)
	}
	page = decodeList(t, call(t, mux, memOrg, operator, http.MethodGet, "/api/v1/monitoring/alarms?source_kind=check", ""))
	if page.Total != 0 {
		t.Errorf("source_kind check: %d, want 0", page.Total)
	}
	// Refused values: 422 with field; a forged cursor 400.
	for q, field := range map[string]string{"state=closed": "state", "severity=fatal": "severity", "ci_id=x": "ci_id",
		"suppressed=maybe": "suppressed", "from=yesterday": "from"} {
		rec := call(t, mux, memOrg, operator, http.MethodGet, "/api/v1/monitoring/alarms?"+q, "")
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"field":"`+field+`"`) {
			t.Errorf("%s: %d %s, want 422 %s", q, rec.Code, rec.Body.String(), field)
		}
	}
	if rec := call(t, mux, memOrg, operator, http.MethodGet, "/api/v1/monitoring/alarms?cursor=forged.x", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("forged cursor: %d, want 400", rec.Code)
	}

	// The list sorts on opened_at, not on first_seen_at: an alarm pending
	// since long before the others that opened last comes first; a pending
	// alarm sorts on its first_seen_at. The cursor pages on the same key.
	svc2, _ := memService()
	mux2 := alarmMux(svc2)
	early := raiseAt(t, svc2, memOrg, "early", alarm.SeverityWarning, base)
	middle := raiseAt(t, svc2, memOrg, "middle", alarm.SeverityWarning, base.Add(5*time.Minute))
	lateIn := raiseIn("late", alarm.SeverityWarning)
	lateIn.ObservedAt = base.Add(-time.Hour)
	if _, err := svc2.Pend(context.Background(), memOrg, lateIn); err != nil {
		t.Fatal(err)
	}
	late := raiseAt(t, svc2, memOrg, "late", alarm.SeverityWarning, base.Add(10*time.Minute))
	pendIn := raiseIn("pending", alarm.SeverityWarning)
	pendIn.ObservedAt = base.Add(2 * time.Minute)
	pending, err := svc2.Pend(context.Background(), memOrg, pendIn)
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := strings.Join([]string{late.ID, middle.ID, pending.ID, early.ID}, ",")
	got = nil
	path = "/api/v1/monitoring/alarms?limit=1"
	for range 5 {
		page := decodeList(t, call(t, mux2, memOrg, operator, http.MethodGet, path, ""))
		got = append(got, ids(page.Data)...)
		if !page.HasMore {
			break
		}
		path = "/api/v1/monitoring/alarms?limit=1&cursor=" + url.QueryEscape(page.NextCursor)
	}
	if strings.Join(got, ",") != wantOrder {
		t.Errorf("order %v, want %s (opened_at DESC, pending on first_seen_at)", got, wantOrder)
	}
}

func TestAlarmListDefaultsToUnresolved(t *testing.T) {
	svc, _ := memService()
	mux := alarmMux(svc)
	open := raiseAt(t, svc, memOrg, "open", alarm.SeverityWarning, time.Now().Add(-time.Hour))
	gone := raiseAt(t, svc, memOrg, "gone", alarm.SeverityWarning, time.Now().Add(-time.Hour))
	if _, err := svc.Clear(context.Background(), memOrg, gone.DedupKey, time.Time{}, ""); err != nil {
		t.Fatal(err)
	}
	pending, err := svc.Pend(context.Background(), memOrg, raiseIn("pending", alarm.SeverityInfo))
	if err != nil {
		t.Fatal(err)
	}
	page := decodeList(t, call(t, mux, memOrg, operator, http.MethodGet, "/api/v1/monitoring/alarms", ""))
	got := strings.Join(ids(page.Data), ",")
	if page.Total != 2 || strings.Contains(got, gone.ID) || !strings.Contains(got, open.ID) || !strings.Contains(got, pending.ID) {
		t.Errorf("default list %v (total %d): want the open and the pending alarm, not the resolved one", got, page.Total)
	}
	page = decodeList(t, call(t, mux, memOrg, operator, http.MethodGet, "/api/v1/monitoring/alarms?state=resolved", ""))
	if len(page.Data) != 1 || page.Data[0].ID != gone.ID {
		t.Errorf("state=resolved: %v, want %s", ids(page.Data), gone.ID)
	}
}

func TestAlarmForeignOrgIs404(t *testing.T) {
	svc, _ := memService()
	mux := alarmMux(svc)
	foreign := raiseAt(t, svc, otherOrg, "foreign", alarm.SeverityCritical, time.Now())
	own := raiseAt(t, svc, memOrg, "own", alarm.SeverityCritical, time.Now())
	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/monitoring/alarms/" + foreign.ID},
		{http.MethodPost, "/api/v1/monitoring/alarms/" + foreign.ID + "/ack"},
		{http.MethodPost, "/api/v1/monitoring/alarms/" + foreign.ID + "/unack"},
		{http.MethodGet, "/api/v1/monitoring/alarms/not-a-uuid"},
	} {
		if rec := call(t, mux, memOrg, operator, rt.method, rt.path, ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: %d, want 404", rt.method, rt.path, rec.Code)
		}
	}
	page := decodeList(t, call(t, mux, memOrg, operator, http.MethodGet, "/api/v1/monitoring/alarms", ""))
	if len(page.Data) != 1 || page.Data[0].ID != own.ID {
		t.Errorf("list of org A: %v, want only %s", ids(page.Data), own.ID)
	}
	rec := call(t, mux, memOrg, operator, http.MethodGet, "/api/v1/monitoring/alarms/"+own.ID, "")
	var detail struct {
		alarm.Alarm
		Transitions []alarm.Transition `json:"transitions"`
		Truncated   *bool              `json:"transitions_truncated"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("detail: %d %s", rec.Code, rec.Body.String())
	}
	if len(detail.Transitions) != 1 || detail.Transitions[0].ToState != alarm.StateOpen || detail.Truncated == nil || *detail.Truncated {
		t.Errorf("detail transitions %+v truncated %v: want the raise only", detail.Transitions, detail.Truncated)
	}
}

func TestAckOnResolvedIs409(t *testing.T) {
	svc, sink := memService()
	mux := alarmMux(svc)
	a := raiseAt(t, svc, memOrg, "ack", alarm.SeverityWarning, time.Now())
	rec := call(t, mux, memOrg, operator, http.MethodPost, "/api/v1/monitoring/alarms/"+a.ID+"/ack", `{"comment":"on it"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"acknowledged"`) ||
		!strings.Contains(rec.Body.String(), `"acknowledged_by":"`+memUser+`"`) {
		t.Fatalf("ack: %d %s", rec.Code, rec.Body.String())
	}
	// A second ack is no transition.
	if rec = call(t, mux, memOrg, operator, http.MethodPost, "/api/v1/monitoring/alarms/"+a.ID+"/ack", ""); rec.Code != http.StatusConflict {
		t.Errorf("second ack: %d, want 409", rec.Code)
	}
	if rec = call(t, mux, memOrg, operator, http.MethodPost, "/api/v1/monitoring/alarms/"+a.ID+"/unack", `{}`); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"state":"open"`) {
		t.Fatalf("unack: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := svc.Clear(context.Background(), memOrg, a.DedupKey, time.Time{}, ""); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"ack", "unack"} {
		rec = call(t, mux, memOrg, operator, http.MethodPost, "/api/v1/monitoring/alarms/"+a.ID+"/"+op, "")
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), alarm.ProblemInvalidTransition) ||
			!strings.Contains(rec.Body.String(), "in state resolved") {
			t.Errorf("%s on resolved: %d %s, want 409 invalid-transition", op, rec.Code, rec.Body.String())
		}
	}
	// A comment over 1,000 characters is 422 comment; an unknown member 400.
	b := raiseAt(t, svc, memOrg, "long", alarm.SeverityWarning, time.Now())
	long, _ := json.Marshal(map[string]string{"comment": strings.Repeat("x", 1001)})
	if rec = call(t, mux, memOrg, operator, http.MethodPost, "/api/v1/monitoring/alarms/"+b.ID+"/ack", string(long)); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), `"field":"comment"`) {
		t.Errorf("long comment: %d %s, want 422 comment", rec.Code, rec.Body.String())
	}
	if rec = call(t, mux, memOrg, operator, http.MethodPost, "/api/v1/monitoring/alarms/"+b.ID+"/ack", `{"note":"x"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown member: %d, want 400", rec.Code)
	}
	acks := 0
	for _, e := range sink.Events() {
		if e.Type == "alarm.acknowledged" {
			acks++
		}
	}
	if acks != 1 {
		t.Errorf("%d alarm.acknowledged events, want 1", acks)
	}
}

// TestAckOutsideAcknowledgeScopeIs403: a caller who reads the alarms of
// every client but holds alert:acknowledge only for client A may not
// acknowledge or un-acknowledge an alarm of client B.
func TestAckOutsideAcknowledgeScopeIs403(t *testing.T) {
	const clientA, clientB = "00000000-0000-4000-8000-0000000000c1", "00000000-0000-4000-8000-0000000000c2"
	svc, _ := memService()
	mux := alarmMux(svc)
	raise := func(series, client string) *alarm.Alarm {
		in := raiseIn(series, alarm.SeverityWarning)
		in.ClientID = &client
		a, err := svc.Raise(context.Background(), memOrg, in)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	ofA, ofB := raise("a", clientA), raise("b", clientB)
	all := identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}}
	p := identity.Principal{Subject: memUser, OrganizationID: memOrg, Type: identity.PrincipalTypeUser, Permissions: operator,
		Scope: &all, PermissionScopes: map[identity.Permission]identity.Scope{identity.PermAlertAcknowledge: {
			Clients: identity.ScopeSet{IDs: []string{clientA}}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}}}}
	send := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(""))
		ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), operator)
		ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: memOrg, UserID: memUser})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req.WithContext(ctx))
		return rec
	}
	if rec := send("/api/v1/monitoring/alarms/" + ofB.ID + "/ack"); rec.Code != http.StatusForbidden {
		t.Errorf("ack of client B: %d %s, want 403", rec.Code, rec.Body.String())
	}
	if got, err := svc.Store().Get(context.Background(), memOrg, ofB.ID); err != nil || got.State != alarm.StateOpen {
		t.Errorf("alarm of client B after refused ack: %v %v, want open", got, err)
	}
	if rec := send("/api/v1/monitoring/alarms/" + ofA.ID + "/ack"); rec.Code != http.StatusOK {
		t.Errorf("ack of client A: %d %s, want 200", rec.Code, rec.Body.String())
	}
	// Un-ack follows the same scope: acknowledged by someone else, B stays.
	if _, err := svc.Acknowledge(context.Background(), memOrg, ofB.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	if rec := send("/api/v1/monitoring/alarms/" + ofB.ID + "/unack"); rec.Code != http.StatusForbidden {
		t.Errorf("unack of client B: %d, want 403", rec.Code)
	}
}

func TestAckRequiresAlertAcknowledge(t *testing.T) {
	svc, _ := memService()
	mux := alarmMux(svc)
	a := raiseAt(t, svc, memOrg, "viewer", alarm.SeverityWarning, time.Now())
	viewer := []identity.Permission{identity.PermMonitoringRead}
	if rec := call(t, mux, memOrg, viewer, http.MethodGet, "/api/v1/monitoring/alarms/"+a.ID, ""); rec.Code != http.StatusOK {
		t.Errorf("viewer reads: %d, want 200", rec.Code)
	}
	for _, op := range []string{"ack", "unack"} {
		if rec := call(t, mux, memOrg, viewer, http.MethodPost, "/api/v1/monitoring/alarms/"+a.ID+"/"+op, ""); rec.Code != http.StatusForbidden {
			t.Errorf("viewer %s: %d, want 403", op, rec.Code)
		}
	}
	// monitoring:write does not stand in for alert:acknowledge (E-159).
	writer := []identity.Permission{identity.PermMonitoringRead, identity.PermMonitoringWrite}
	if rec := call(t, mux, memOrg, writer, http.MethodPost, "/api/v1/monitoring/alarms/"+a.ID+"/ack", ""); rec.Code != http.StatusForbidden {
		t.Errorf("monitoring:write ack: %d, want 403", rec.Code)
	}
	if got, err := svc.Store().Get(context.Background(), memOrg, a.ID); err != nil || got.State != alarm.StateOpen {
		t.Errorf("alarm after refused acks: %v %v, want open", got, err)
	}
}
