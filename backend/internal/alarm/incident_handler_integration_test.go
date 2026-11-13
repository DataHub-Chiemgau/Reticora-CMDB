package alarm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The incident routes on PostgreSQL (ALR-03, ALR-10, WP-1036): a root set
// by hand survives the correlation, the client scope of the caller, the
// right alert:acknowledge, incident.updated in the outbox and the latency
// of the list.

type incidentRESTEnv struct {
	t     *testing.T
	f     *scopetest.Fixture
	svc   *alarm.Service
	store alarm.Store
	mux   http.Handler
}

func newIncidentRESTEnv(t *testing.T) *incidentRESTEnv {
	t.Helper()
	f := scopetest.Seed(t, alarmTag)
	svc := alarm.NewPGService(f.App)
	inner := chi.NewRouter()
	alarm.NewHandler(svc).RegisterRoutes(inner)
	mux := chi.NewRouter()
	for _, rt := range incidentRoutes {
		mux.With(server.AuthorizeRoute(rt.method, rt.pattern)).Method(rt.method, rt.pattern, inner)
	}
	return &incidentRESTEnv{t: t, f: f, svc: svc, store: svc.Store(), mux: mux}
}

// do sends a request as the fixture's user of organization A with perms,
// org-wide or restricted to client.
func (e *incidentRESTEnv) do(client string, perms []identity.Permission, method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	scope := database.OrgWideScope(e.f.OrgA, e.f.User)
	idScope := &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}}
	if client != "" {
		scope.Clients = database.ScopeIDs(client)
		idScope.Clients = identity.ScopeSet{IDs: []string{client}}
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	p := identity.Principal{Subject: e.f.User, OrganizationID: e.f.OrgA, Type: identity.PrincipalTypeUser, Scope: idScope, Permissions: perms}
	ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), perms)
	ctx = database.ContextWithTenantScope(ctx, &scope)
	ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: e.f.User})
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

// incident opens an incident of the client with a root and n-1 symptoms
// on CIs of the client.
func (e *incidentRESTEnv) incident(client, prefix string, n int) (*alarm.Incident, []string) {
	e.t.Helper()
	ctx := e.f.OrgCtx(e.f.OrgA)
	var ids []string
	for i := range n {
		ci := e.f.CI(e.t, e.f.OrgA, client, prefix+"-"+string(rune('a'+i)))
		a, _, err := e.store.UpsertOpen(ctx, e.f.OrgA, &alarm.UpsertInput{SourceKind: alarm.SourceCheck, SourceID: e.f.ID(),
			SeriesKey: prefix, CIID: &ci, Severity: alarm.SeverityCritical, State: alarm.StateOpen, Summary: "down"})
		if err != nil {
			e.t.Fatal(err)
		}
		ids = append(ids, a.ID)
	}
	root := ids[0]
	inc, _, err := e.store.UpsertIncident(ctx, e.f.OrgA, &alarm.IncidentInput{CauseClass: alarm.CauseTopology, Confidence: 70})
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err = e.store.AttachAlarms(ctx, e.f.OrgA, inc.ID, []string{root}, alarm.RoleRoot); err != nil {
		e.t.Fatal(err)
	}
	if n > 1 {
		if _, err = e.store.AttachAlarms(ctx, e.f.OrgA, inc.ID, ids[1:], alarm.RoleSymptom); err != nil {
			e.t.Fatal(err)
		}
	}
	inc, err = e.store.GetIncident(ctx, e.f.OrgA, inc.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return inc, ids
}

// updatedEvents counts incident.updated of the incident in the outbox.
func (e *incidentRESTEnv) updatedEvents(id string) int {
	e.t.Helper()
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM event_outbox
		WHERE organization_id = $1 AND type = 'incident.updated' AND entity_id = $2`, e.f.OrgA, id).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestIncidentManualRootSurvivesRecorrelation(t *testing.T) {
	e := newIncidentRESTEnv(t)
	inc, ids := e.incident(e.f.Client1, "wp1036-root", 3)
	rec := e.do("", incidentOperator, http.MethodPost, "/api/v1/monitoring/incidents/"+inc.ID+"/root",
		`{"alarm_id":"`+ids[2]+`","reason":"manual analysis"}`)
	d := decodeIncident(t, rec)
	if d.RootAlarmID != ids[2] || !d.ManualRoot {
		t.Fatalf("manual root: %+v", d)
	}
	ctx := e.f.OrgCtx(e.f.OrgA)
	// The correlation of WP-1028 sets the root without manual: refused.
	if _, err := e.store.SetRoot(ctx, e.f.OrgA, inc.ID, ids[0], false); err == nil {
		t.Error("an automatic SetRoot replaced the manual root")
	}
	// A merge keeps the root of the incident that keeps it by hand.
	other, otherIDs := e.incident(e.f.Client1, "wp1036-merge", 1)
	if _, err := e.store.MergeIncidents(ctx, e.f.OrgA, inc.ID, other.ID, otherIDs[0]); err != nil {
		t.Fatal(err)
	}
	got, err := e.store.GetIncident(ctx, e.f.OrgA, inc.ID)
	if err != nil || got.RootAlarmID == nil || *got.RootAlarmID != ids[2] || !got.ManualRoot {
		t.Errorf("after the merge: %+v %v, want the manual root %s", got, err, ids[2])
	}
	// The transition root_changed names actor and reason.
	trs, err := e.store.IncidentTransitions(ctx, e.f.OrgA, inc.ID)
	if err != nil {
		t.Fatal(err)
	}
	var named bool
	for _, tr := range trs {
		if tr.Reason == alarm.IncidentReasonRootChanged && tr.ActorID != nil && *tr.ActorID == e.f.User &&
			tr.Details["reason"] == "manual analysis" {
			named = true
		}
	}
	if !named {
		t.Errorf("no root_changed with actor and reason in %+v", trs)
	}
}

func TestIncidentClientScope(t *testing.T) {
	e := newIncidentRESTEnv(t)
	own, _ := e.incident(e.f.Client1, "wp1036-own", 2)
	foreign, _ := e.incident(e.f.Client2, "wp1036-foreign", 2)
	rec := e.do(e.f.Client1, incidentOperator, http.MethodGet, "/api/v1/monitoring/incidents?limit=200", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), own.ID) || strings.Contains(rec.Body.String(), foreign.ID) {
		t.Errorf("client 1 lists %s", rec.Body.String())
	}
	if rec := e.do(e.f.Client1, incidentOperator, http.MethodGet, "/api/v1/monitoring/incidents/"+foreign.ID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("foreign incident: %d, want 404", rec.Code)
	}
	if rec := e.do(e.f.Client1, incidentOperator, http.MethodPost, "/api/v1/monitoring/incidents/"+foreign.ID+"/ack", ""); rec.Code != http.StatusNotFound {
		t.Errorf("ack of a foreign incident: %d, want 404", rec.Code)
	}
	d := decodeIncident(t, e.do(e.f.Client1, incidentOperator, http.MethodGet, "/api/v1/monitoring/incidents/"+own.ID, ""))
	if len(d.Members) != 2 || d.Root.AlarmID == "" {
		t.Errorf("own incident %+v", d)
	}
	if !strings.Contains(e.do(e.f.Client1, incidentOperator, http.MethodGet, "/api/v1/monitoring/incidents/"+own.ID, "").Body.String(),
		`"ci_name":"wp1036-own-a"`) {
		t.Error("the root names no CI name")
	}
}

// TestIncidentSymptomCountWithInvisibleRoot: a root alarm outside the
// caller's scope does not lower the symptom count (the symptoms are
// counted without the root, not the visible members minus one).
func TestIncidentSymptomCountWithInvisibleRoot(t *testing.T) {
	e := newIncidentRESTEnv(t)
	inc, ids := e.incident(e.f.Client1, "wp1036-hidden-root", 2)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client2, "wp1036-hidden-root-ci")
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE alarm SET ci_id = $3, client_id = $4, site_id = NULL
		WHERE organization_id = $1 AND id = $2`, e.f.OrgA, ids[0], ci, e.f.Client2); err != nil {
		t.Fatal(err)
	}
	d := decodeIncident(t, e.do(e.f.Client1, incidentOperator, http.MethodGet, "/api/v1/monitoring/incidents/"+inc.ID, ""))
	if len(d.Members) != 1 || d.Members[0].ID != ids[1] || d.SymptomCount != 1 {
		t.Errorf("client 1 sees members %+v and symptom_count %d, want the one symptom", d.Members, d.SymptomCount)
	}
	if d := decodeIncident(t, e.do("", incidentOperator, http.MethodGet, "/api/v1/monitoring/incidents/"+inc.ID, "")); d.SymptomCount != 1 || len(d.Members) != 2 {
		t.Errorf("organization-wide: members %d and symptom_count %d, want 2 and 1", len(d.Members), d.SymptomCount)
	}
}

func TestIncidentAckNeedsAlertAcknowledge(t *testing.T) {
	e := newIncidentRESTEnv(t)
	inc, ids := e.incident(e.f.Client1, "wp1036-ack", 2)
	path := "/api/v1/monitoring/incidents/" + inc.ID + "/ack"
	if rec := e.do("", []identity.Permission{identity.PermMonitoringRead, identity.PermMonitoringWrite}, http.MethodPost, path, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("ack without alert:acknowledge: %d, want 403", rec.Code)
	}
	d := decodeIncident(t, e.do("", incidentOperator, http.MethodPost, path, `{"comment":"checking"}`))
	if d.State != alarm.IncidentAcknowledged {
		t.Errorf("state %s", d.State)
	}
	root, err := e.store.Get(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, ids[0])
	if err != nil || root.State != alarm.StateAcknowledged || root.AcknowledgedBy == nil || *root.AcknowledgedBy != e.f.User {
		t.Errorf("root alarm %+v %v, want acknowledged by the user", root, err)
	}
}

func TestIncidentUpdatedEmitted(t *testing.T) {
	e := newIncidentRESTEnv(t)
	inc, ids := e.incident(e.f.Client1, "wp1036-evt", 3)
	base := "/api/v1/monitoring/incidents/" + inc.ID
	decodeIncident(t, e.do("", incidentOperator, http.MethodPost, base+"/ack", ""))
	if n := e.updatedEvents(inc.ID); n != 1 {
		t.Fatalf("after ack: %d incident.updated, want 1", n)
	}
	if rec := e.do("", incidentOperator, http.MethodPost, base+"/ack", ""); rec.Code != http.StatusConflict {
		t.Errorf("second ack: %d, want 409", rec.Code)
	}
	stray, _ := e.incident(e.f.Client1, "wp1036-stray", 1)
	strayRoot := *stray.RootAlarmID
	if rec := e.do("", incidentOperator, http.MethodPost, base+"/root", `{"alarm_id":"`+strayRoot+`","reason":"x"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("root of another incident: %d, want 422", rec.Code)
	}
	if n := e.updatedEvents(inc.ID); n != 1 {
		t.Fatalf("after the refused calls: %d incident.updated, want 1", n)
	}
	decodeIncident(t, e.do("", incidentOperator, http.MethodPost, base+"/root", `{"alarm_id":"`+ids[1]+`","reason":"y"}`))
	if n := e.updatedEvents(inc.ID); n != 2 {
		t.Fatalf("after the root: %d incident.updated, want 2", n)
	}
	var change, root, actor string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT data->>'change', data->>'root_alarm_id',
		data->>'actor_id' FROM event_outbox WHERE organization_id = $1 AND type = 'incident.updated'
		AND entity_id = $2 ORDER BY sequence DESC LIMIT 1`, e.f.OrgA, inc.ID).Scan(&change, &root, &actor); err != nil {
		t.Fatal(err)
	}
	if change != alarm.IncidentChangeRootChanged || root != ids[1] || actor != e.f.User {
		t.Errorf("incident.updated: change %s root %s actor %s", change, root, actor)
	}
}

func TestIncidentListLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("10,000 incidents")
	}
	e := newIncidentRESTEnv(t)
	if _, err := e.f.Admin.Exec(context.Background(), `INSERT INTO incident (organization_id, client_id, cause_class, opened_at)
		SELECT $1, $2, 'topology', now() - (g || ' seconds')::interval FROM generate_series(1, 10000) AS g`, e.f.OrgA, e.f.Client1); err != nil {
		t.Fatal(err)
	}
	var cursor string
	var durations []time.Duration
	for i := range 40 {
		path := "/api/v1/monitoring/incidents?limit=50"
		if i%4 != 0 && cursor != "" {
			path += "&cursor=" + cursor
		}
		start := time.Now()
		rec := e.do("", incidentOperator, http.MethodGet, path, "")
		durations = append(durations, time.Since(start))
		if rec.Code != http.StatusOK {
			t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
		}
		if i := strings.Index(rec.Body.String(), `"next_cursor":"`); i >= 0 {
			rest := rec.Body.String()[i+len(`"next_cursor":"`):]
			cursor = rest[:strings.Index(rest, `"`)]
		}
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	if p95 := durations[len(durations)*95/100]; p95 >= 300*time.Millisecond {
		t.Errorf("p95 of the list over 10,000 incidents: %v, want < 300 ms", p95)
	}
}
