package alarm_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The REST routes of the alarms on PostgreSQL (ALR-01, WP-983): ack and
// un-ack write the transition with the actor, the audit entry
// alarm.acknowledged / alarm.unacknowledged and (ack only) the event in one
// transaction; the list and the detail follow the client scope of the
// caller; the detail holds at most 500 transitions.

type restEnv struct {
	t   *testing.T
	f   *scopetest.Fixture
	svc *alarm.Service
	mux http.Handler
	// client restricts the caller to one client; empty is org-wide.
	client string
	perms  []identity.Permission
}

func newRESTEnv(t *testing.T) *restEnv {
	t.Helper()
	f := scopetest.Seed(t, alarmTag)
	svc := alarm.NewPGService(f.App)
	return &restEnv{t: t, f: f, svc: svc, mux: alarmMux(svc), perms: operator}
}

// do sends a request as the fixture's user of organization A.
func (e *restEnv) do(method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	scope := database.OrgWideScope(e.f.OrgA, e.f.User)
	idScope := &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}}
	if e.client != "" {
		scope.Clients = database.ScopeIDs(e.client)
		idScope.Clients = identity.ScopeSet{IDs: []string{e.client}}
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	p := identity.Principal{Subject: e.f.User, OrganizationID: e.f.OrgA, Type: identity.PrincipalTypeUser,
		Scope: idScope, Permissions: e.perms}
	ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), e.perms)
	ctx = database.ContextWithTenantScope(ctx, &scope)
	ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: e.f.User})
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

// open raises an open alarm of series for a client (no CI).
func (e *restEnv) open(series, client string) *alarm.Alarm {
	e.t.Helper()
	in := raiseIn(series, alarm.SeverityWarning)
	in.SourceKind, in.ClientID = alarm.SourceCollector, &client
	a, err := e.svc.Raise(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, in)
	if err != nil {
		e.t.Fatal(err)
	}
	return a
}

type auditRow struct {
	Action, ResourceType, ResourceID, ActorID string
	Changes                                   map[string]any
}

// auditOf reads the audit entries of an alarm, oldest first.
func (e *restEnv) auditOf(id string) []auditRow {
	e.t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT action, resource_type, resource_id::text, COALESCE(actor_id::text, ''), changes
		FROM audit_log WHERE organization_id = $1 AND resource_id = $2 ORDER BY seq`, e.f.OrgA, id)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		var raw []byte
		if err := rows.Scan(&r.Action, &r.ResourceType, &r.ResourceID, &r.ActorID, &raw); err != nil {
			e.t.Fatal(err)
		}
		_ = json.Unmarshal(raw, &r.Changes)
		out = append(out, r)
	}
	return out
}

// lastTransition reads the newest transition of an alarm.
func (e *restEnv) lastTransition(id string) alarm.Transition {
	e.t.Helper()
	trs, err := e.svc.Store().Transitions(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, id)
	if err != nil || len(trs) == 0 {
		e.t.Fatalf("transitions of %s: %v, %v", id, trs, err)
	}
	return trs[len(trs)-1]
}

func TestAckWritesAuditAndTransition(t *testing.T) {
	e := newRESTEnv(t)
	a := e.open("ack", e.f.Client1)
	before := lastSequence(t, e.f, e.f.OrgA)
	rec := e.do(http.MethodPost, "/api/v1/monitoring/alarms/"+a.ID+"/ack", `{"comment":"looking into it"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"acknowledged"`) {
		t.Fatalf("ack: %d %s", rec.Code, rec.Body.String())
	}
	tr := e.lastTransition(a.ID)
	if tr.ToState != alarm.StateAcknowledged || tr.ActorID == nil || *tr.ActorID != e.f.User || tr.Comment == nil ||
		*tr.Comment != "looking into it" || tr.Reason != alarm.ReasonAcknowledged {
		t.Errorf("transition %+v: want acknowledged by the user with the comment", tr)
	}
	entries := e.auditOf(a.ID)
	if len(entries) != 1 || entries[0].Action != alarm.AuditAcknowledged || entries[0].ResourceType != "alarm" ||
		entries[0].ActorID != e.f.User {
		t.Fatalf("audit %+v: want one alarm.acknowledged entry of the user", entries)
	}
	if state, _ := entries[0].Changes["state"].(map[string]any); state["old"] != "open" || state["new"] != "acknowledged" {
		t.Errorf("audit changes %v: want state open → acknowledged", entries[0].Changes)
	}
	acks := 0
	for _, ev := range outboxOf(t, e.f, e.f.OrgA, before) {
		if ev.Type == events.AlarmAcknowledged && ev.EntityID == a.ID {
			acks++
		}
	}
	if acks != 1 {
		t.Errorf("%d alarm.acknowledged events, want 1", acks)
	}
	// A refused ack (already acknowledged) writes neither audit nor
	// transition.
	if rec = e.do(http.MethodPost, "/api/v1/monitoring/alarms/"+a.ID+"/ack", ""); rec.Code != http.StatusConflict {
		t.Errorf("second ack: %d, want 409", rec.Code)
	}
	if n := len(e.auditOf(a.ID)); n != 1 {
		t.Errorf("%d audit entries after a refused ack, want 1", n)
	}
}

func TestUnackWritesAuditAndTransition(t *testing.T) {
	e := newRESTEnv(t)
	a := e.open("unack", e.f.Client1)
	if rec := e.do(http.MethodPost, "/api/v1/monitoring/alarms/"+a.ID+"/ack", ""); rec.Code != http.StatusOK {
		t.Fatalf("ack: %d %s", rec.Code, rec.Body.String())
	}
	before := lastSequence(t, e.f, e.f.OrgA)
	rec := e.do(http.MethodPost, "/api/v1/monitoring/alarms/"+a.ID+"/unack", `{"comment":"wrong team"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"open"`) ||
		!strings.Contains(rec.Body.String(), `"acknowledged_by":null`) {
		t.Fatalf("unack: %d %s", rec.Code, rec.Body.String())
	}
	tr := e.lastTransition(a.ID)
	if tr.ToState != alarm.StateOpen || tr.FromState == nil || *tr.FromState != alarm.StateAcknowledged ||
		tr.ActorID == nil || *tr.ActorID != e.f.User || tr.Comment == nil || *tr.Comment != "wrong team" {
		t.Errorf("transition %+v: want acknowledged → open by the user with the comment", tr)
	}
	entries := e.auditOf(a.ID)
	if len(entries) != 2 || entries[1].Action != alarm.AuditUnacknowledged || entries[1].ResourceType != "alarm" {
		t.Fatalf("audit %+v: want alarm.acknowledged and alarm.unacknowledged", entries)
	}
	// E-161 has no event for the un-ack.
	for _, ev := range outboxOf(t, e.f, e.f.OrgA, before) {
		if ev.EntityID == a.ID {
			t.Errorf("event %s after un-ack, want none", ev.Type)
		}
	}
}

func TestAlarmListClientScope(t *testing.T) {
	e := newRESTEnv(t)
	own := e.open("client1", e.f.Client1)
	other := e.open("client2", e.f.Client2)
	e.client = e.f.Client1
	page := decodeList(t, e.do(http.MethodGet, "/api/v1/monitoring/alarms", ""))
	if page.Total != 1 || len(page.Data) != 1 || page.Data[0].ID != own.ID {
		t.Errorf("client 1 list %v (total %d), want only %s", ids(page.Data), page.Total, own.ID)
	}
	page = decodeList(t, e.do(http.MethodGet, "/api/v1/monitoring/alarms?client_id="+e.f.Client2, ""))
	if page.Total != 0 {
		t.Errorf("client 1 asking for client 2: %v, want none", ids(page.Data))
	}
	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/monitoring/alarms/" + other.ID},
		{http.MethodPost, "/api/v1/monitoring/alarms/" + other.ID + "/ack"},
	} {
		if rec := e.do(rt.method, rt.path, ""); rec.Code != http.StatusNotFound {
			t.Errorf("client 1 %s %s: %d, want 404", rt.method, rt.path, rec.Code)
		}
	}
	if got, err := e.svc.Store().Get(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, other.ID); err != nil || got.State != alarm.StateOpen {
		t.Errorf("alarm of client 2 after a refused ack: %v, %v", got, err)
	}
	e.client = ""
	page = decodeList(t, e.do(http.MethodGet, "/api/v1/monitoring/alarms?client_id="+e.f.Client2, ""))
	if page.Total != 1 || page.Data[0].ID != other.ID {
		t.Errorf("org-wide client_id filter: %v, want %s", ids(page.Data), other.ID)
	}
}

func TestAlarmDetailTransitionsTruncated(t *testing.T) {
	e := newRESTEnv(t)
	a := e.open("busy", e.f.Client1)
	// 520 more entries of the log (one is the raise): the log is append-only.
	if _, err := e.f.Admin.Exec(context.Background(), `INSERT INTO alarm_transition (organization_id, alarm_id, from_state, to_state, at, reason)
		SELECT $1, $2, 'open', 'open', now() + make_interval(secs => g), 'severity_changed' FROM generate_series(1, 520) AS g`,
		e.f.OrgA, a.ID); err != nil {
		t.Fatal(err)
	}
	rec := e.do(http.MethodGet, "/api/v1/monitoring/alarms/"+a.ID, "")
	var detail struct {
		Transitions []alarm.Transition `json:"transitions"`
		Truncated   bool               `json:"transitions_truncated"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("detail: %d %s", rec.Code, rec.Body.String())
	}
	if len(detail.Transitions) != alarm.MaxTransitions || !detail.Truncated {
		t.Errorf("%d transitions, truncated %v; want %d and true", len(detail.Transitions), detail.Truncated, alarm.MaxTransitions)
	}
	if detail.Transitions[0].ToState != alarm.StateOpen || detail.Transitions[0].FromState != nil {
		t.Errorf("first transition %+v: want the raise (oldest first)", detail.Transitions[0])
	}
}
