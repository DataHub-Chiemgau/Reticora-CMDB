package alarm_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

// Tickets from monitoring without a second ticket on PostgreSQL (TKT-01
// Monitoring [P3], MON-04, WFL-06, WP-1040): the ticket action of a rule,
// the dedupe by root cause under the advisory lock of the exclusive target,
// symptoms at the ticket of their incident, the comment and the
// resolution, the manual ticket of an incident and the tickets of an
// alarm detail.
//
// Retest: cd backend && go test -count=1 -p 1 -run 'TestRuleTicket|TestIncidentSymptoms|TestIncidentFormed|TestIncidentRoot|TestIncidentResolution|TestResolveWrites|TestResolveShared|TestSuppressedAlarm|TestManualIncidentTicket|TestAlarmDetailListsTickets' ./internal/alarm/

// ruleSource is the alert rule of the ticket cases.
const ruleSource = "22222222-2222-4222-8222-0000000010a0"

// writerEnv is the ticket environment with the ticket writer on the
// service and the manual ticket route.
type writerEnv struct {
	*ticketEnv
	writer alarm.TicketWriter
	mux    http.Handler
}

func newWriterEnv(t *testing.T, features featureSet) *writerEnv {
	t.Helper()
	e := newTicketEnv(t, features)
	writer := server.NewAlarmTickets(e.tickets, nil)
	e.svc.WithTicketWriter(entitledWriter{TicketWriter: writer, features: features})
	inner := chi.NewRouter()
	alarm.NewHandler(e.svc).WithTickets(e.port, features).WithTicketWriter(writer).RegisterRoutes(inner)
	mux := chi.NewRouter()
	for _, rt := range []struct{ method, pattern string }{
		{http.MethodPost, "/api/v1/monitoring/incidents/{id}/ticket"},
		{http.MethodGet, "/api/v1/monitoring/alarms/{id}"},
	} {
		mux.With(server.AuthorizeRoute(rt.method, rt.pattern)).Method(rt.method, rt.pattern, inner)
	}
	return &writerEnv{ticketEnv: e, writer: writer, mux: mux}
}

// entitledWriter answers Entitled from the feature set.
type entitledWriter struct {
	alarm.TicketWriter
	features featureSet
}

func (w entitledWriter) Entitled(_ context.Context, _ string) bool {
	return w.features[entitlement.FeatureTicketingEssential]
}

var withTicketing = featureSet{entitlement.FeatureTicketingEssential: true}

// serve answers a request of the reporter (an app user) with perms on the
// routes of the environment.
func (e *writerEnv) serve(perms []identity.Permission, method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.doRaw(e.mux, perms, method, path, body)
}

// asReporter is a request context of the reporter with perms and an
// unrestricted field policy.
func (e *writerEnv) asReporter(perms []identity.Permission) context.Context {
	return permission.WithPolicy(e.asReporterWithoutPolicy(perms), permission.UnrestrictedPolicy(perms))
}

// asReporterWithoutPolicy is asReporter without field policy.
func (e *writerEnv) asReporterWithoutPolicy(perms []identity.Permission) context.Context {
	scope := database.OrgWideScope(e.f.OrgA, e.reporter)
	idScope := &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}}
	p := identity.Principal{Subject: e.reporter, OrganizationID: e.f.OrgA, Type: identity.PrincipalTypeUser, Scope: idScope, Permissions: perms}
	ctx := identity.WithPermissions(identity.WithPrincipal(context.Background(), p), perms)
	ctx = database.ContextWithTenantScope(ctx, &scope)
	return tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: e.reporter})
}

// doRaw answers a request of the fixture's user with perms on mux behind
// the route permissions.
func (e *writerEnv) doRaw(mux http.Handler, perms []identity.Permission, method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req.WithContext(e.asReporter(perms)))
	return rec
}

// raiseTicketed raises an alarm of the rule on the CI with the ticket
// action on.
func (e *writerEnv) raiseTicketed(ciID, series string, closeOnResolve bool) *alarm.Alarm {
	e.t.Helper()
	ci := ciID
	actions := alarm.DefaultActions()
	actions.Ticket = &events.AlarmTicketAction{Enabled: true, ReporterID: e.reporter, CloseOnResolve: closeOnResolve}
	a, err := e.svc.Raise(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, &alarm.RaiseInput{SourceKind: alarm.SourceRule, SourceID: ruleSource,
		SeriesKey: series, CIID: &ci, Severity: alarm.SeverityCritical, Summary: "cpu high " + series, Actions: &actions})
	if err != nil {
		e.t.Fatal(err)
	}
	return a
}

func (e *writerEnv) countTickets(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM `+sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// ticketOf returns the ticket linked to the alarm ("" for none).
func (e *writerEnv) ticketOf(alarmID string) string {
	e.t.Helper()
	var id string
	err := e.f.Admin.QueryRow(context.Background(), `SELECT ticket_id::text FROM ticket_link WHERE entity_type = 'alarm' AND alarm_id = $1`,
		alarmID).Scan(&id)
	if err != nil {
		return ""
	}
	return id
}

func (e *writerEnv) systemComments(ticketID string) int {
	e.t.Helper()
	return e.countTickets(`ticket_comment WHERE ticket_id = $1`, ticketID)
}

// TestRuleTicketTwoFiringsOneTicket: an alarm of the rule opens one ticket
// (priority from the severity, the rule's reporter, links to alarm and CI);
// the next firing of the rule on the same CI (a new alarm after the first
// resolved) links and comments the open ticket instead of a second one.
func TestRuleTicketTwoFiringsOneTicket(t *testing.T) {
	e := newWriterEnv(t, withTicketing)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp1040-router")
	first := e.raiseTicketed(ci, "cpu", false)
	tid := e.ticketOf(first.ID)
	if tid == "" {
		t.Fatal("the first firing opened no ticket")
	}
	var prio, reporter string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT priority, reporter_id::text FROM ticket WHERE id = $1`, tid).
		Scan(&prio, &reporter); err != nil {
		t.Fatal(err)
	}
	if prio != "critical" || reporter != e.reporter {
		t.Errorf("ticket priority %s reporter %s, want critical and the rule's reporter", prio, reporter)
	}
	if n := e.countTickets(`ticket_link WHERE ticket_id = $1 AND entity_type = 'ci' AND ci_id = $2`, tid, ci); n != 1 {
		t.Errorf("%d links to the CI, want 1", n)
	}
	a, err := e.store.Get(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Details[alarm.DetailsTicketID] != tid || a.Details[alarm.DetailsTicketExisting] != false {
		t.Errorf("alarm details %v, want ticket_id and ticket_existing false", a.Details)
	}
	if _, err = e.svc.Clear(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, first.DedupKey, first.LastSeenAt, ""); err != nil {
		t.Fatal(err)
	}
	second := e.raiseTicketed(ci, "cpu", false)
	if second.ID == first.ID {
		t.Fatal("the second firing reused the resolved alarm")
	}
	if got := e.ticketOf(second.ID); got != tid {
		t.Errorf("second firing on ticket %q, want the open %s", got, tid)
	}
	if n := e.countTickets(`ticket WHERE organization_id = $1 AND reporter_id = $2`, e.f.OrgA, e.reporter); n != 1 {
		t.Errorf("%d tickets, want one", n)
	}
	if n := e.systemComments(tid); n < 1 {
		t.Errorf("%d comments at the ticket, want the system comment of the second firing", n)
	}
	b, err := e.store.Get(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if b.Details[alarm.DetailsTicketExisting] != true {
		t.Errorf("second alarm details %v, want ticket_existing true", b.Details)
	}
}

// TestRuleTicketParallelFiringsOneTicket: alarms of two series on one CI
// raised at the same time by two workers open exactly one ticket (the
// advisory lock of the exclusive CI).
func TestRuleTicketParallelFiringsOneTicket(t *testing.T) {
	e := newWriterEnv(t, withTicketing)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp1040-parallel")
	var wg sync.WaitGroup
	alarms := make([]*alarm.Alarm, 2)
	for i := range alarms {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ciID := ci
			actions := alarm.DefaultActions()
			actions.Ticket = &events.AlarmTicketAction{Enabled: true, ReporterID: e.reporter}
			a, err := e.svc.Raise(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, &alarm.RaiseInput{SourceKind: alarm.SourceRule, SourceID: ruleSource,
				SeriesKey: []string{"cpu", "mem"}[i], CIID: &ciID, Severity: alarm.SeverityWarning, Summary: "parallel", Actions: &actions})
			if err != nil {
				t.Error(err)
				return
			}
			alarms[i] = a
		}(i)
	}
	wg.Wait()
	if n := e.countTickets(`ticket WHERE organization_id = $1 AND reporter_id = $2`, e.f.OrgA, e.reporter); n != 1 {
		t.Errorf("%d tickets of two parallel firings, want exactly one", n)
	}
	if alarms[0] == nil || alarms[1] == nil || e.ticketOf(alarms[0].ID) == "" || e.ticketOf(alarms[0].ID) != e.ticketOf(alarms[1].ID) {
		t.Error("both alarms are not linked to the one ticket")
	}
}

// TestRuleTicketLinksExistingRootCITicket: an open ticket at the CI of the
// alarm is linked and commented; no new ticket.
func TestRuleTicketLinksExistingRootCITicket(t *testing.T) {
	e := newWriterEnv(t, withTicketing)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp1040-existing")
	existing := e.ticketAt("open at the CI", ci)
	e.link(existing, "ci", ci)
	a := e.raiseTicketed(ci, "cpu", false)
	if got := e.ticketOf(a.ID); got != existing {
		t.Errorf("alarm linked to %q, want the existing ticket %s", got, existing)
	}
	if n := e.countTickets(`ticket WHERE organization_id = $1 AND reporter_id = $2 AND id <> $3`, e.f.OrgA, e.reporter, existing); n != 0 {
		t.Errorf("%d new tickets, want none", n)
	}
	if e.systemComments(existing) < 1 {
		t.Error("no system comment at the existing ticket")
	}
}

// TestIncidentSymptomsAppendToRootTicket: the root alarm of an incident
// opens the ticket of the incident (exclusive incident); its symptoms open
// none but are linked to it with a system comment.
func TestIncidentSymptomsAppendToRootTicket(t *testing.T) {
	e := newWriterEnv(t, withTicketing)
	inc, cis := e.incident("wp1040-inc", 3)
	ctx := e.f.OrgCtx(e.f.OrgA)
	page, err := e.store.List(ctx, e.f.OrgA, &alarm.ListFilter{IncidentID: inc.ID, States: alarm.States})
	if err != nil {
		t.Fatal(err)
	}
	actions := alarm.DefaultActions()
	actions.Ticket = &events.AlarmTicketAction{Enabled: true, ReporterID: e.reporter}
	for i := range page.Items {
		a := &page.Items[i]
		u := &alarm.StateUpdate{State: a.State, Severity: a.Severity, SuppressedReason: a.SuppressedReason, IncidentID: a.IncidentID,
			OpenedAt: a.OpenedAt, Details: map[string]any{alarm.DetailsActions: actions}}
		if _, err = e.store.UpdateState(ctx, e.f.OrgA, a.ID, u); err != nil {
			t.Fatal(err)
		}
	}
	root, err := e.svc.OpenAlarmTicket(ctx, e.f.OrgA, *inc.RootAlarmID)
	if err != nil || root == nil || root.Existing {
		t.Fatalf("root ticket %+v (%v), want a new one", root, err)
	}
	if n := e.countTickets(`ticket_link WHERE ticket_id = $1 AND entity_type = 'incident' AND incident_id = $2`, root.Ticket.TicketID, inc.ID); n != 1 {
		t.Errorf("%d links of the root ticket to the incident, want 1", n)
	}
	for i := range page.Items {
		if page.Items[i].ID == *inc.RootAlarmID {
			continue
		}
		got, err := e.svc.OpenAlarmTicket(ctx, e.f.OrgA, page.Items[i].ID)
		if err != nil || got == nil || got.Ticket.TicketID != root.Ticket.TicketID || !got.Existing {
			t.Errorf("symptom %s: %+v (%v), want the incident's ticket", page.Items[i].ID, got, err)
		}
	}
	if n := e.countTickets(`ticket WHERE organization_id = $1 AND reporter_id = $2`, e.f.OrgA, e.reporter); n != 1 {
		t.Errorf("%d tickets for an incident of %d CIs, want one", n, len(cis))
	}
	if e.systemComments(root.Ticket.TicketID) < 2 {
		t.Error("the symptoms wrote no system comment at the incident's ticket")
	}
}

// TestResolveWritesCommentAndClosesOnResolve: resolving the alarm comments
// its ticket; with close_on_resolve the ticket becomes resolved, without
// it keeps its state.
func TestResolveWritesCommentAndClosesOnResolve(t *testing.T) {
	for _, closeOnResolve := range []bool{true, false} {
		e := newWriterEnv(t, withTicketing)
		ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp1040-resolve")
		a := e.raiseTicketed(ci, "cpu", closeOnResolve)
		tid := e.ticketOf(a.ID)
		if tid == "" {
			t.Fatal("no ticket")
		}
		// A new ticket cannot go to resolved directly (new → open first):
		// the ticket is in work.
		if _, err := e.f.Admin.Exec(context.Background(), `UPDATE ticket SET status = 'open' WHERE id = $1`, tid); err != nil {
			t.Fatal(err)
		}
		before := e.systemComments(tid)
		if _, err := e.svc.Clear(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, a.DedupKey, a.LastSeenAt, ""); err != nil {
			t.Fatal(err)
		}
		if e.systemComments(tid) != before+1 {
			t.Errorf("close_on_resolve %v: %d comments after the resolution, want %d", closeOnResolve, e.systemComments(tid), before+1)
		}
		var status string
		if err := e.f.Admin.QueryRow(context.Background(), `SELECT status FROM ticket WHERE id = $1`, tid).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if closeOnResolve != (status == "resolved") {
			t.Errorf("close_on_resolve %v: ticket status %s", closeOnResolve, status)
		}
	}
}

// TestManualIncidentTicket409WithExistingTicket: the manual ticket of an
// incident is created with the caller as reporter and the incident's
// defaults; a second one is 409 naming the open ticket.
func TestManualIncidentTicket409WithExistingTicket(t *testing.T) {
	e := newWriterEnv(t, withTicketing)
	inc, _ := e.incident("wp1040-manual", 2)
	perms := []identity.Permission{identity.PermMonitoringRead, identity.PermTicketRead, identity.PermTicketWrite}
	path := "/api/v1/monitoring/incidents/" + inc.ID + "/ticket"
	rec := e.serve(perms, http.MethodPost, path, `{"priority":"high"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST %s = %d %s", path, rec.Code, rec.Body)
	}
	var out alarm.IncidentTicketView
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.TicketID == "" {
		t.Fatalf("answer %s (%v)", rec.Body, err)
	}
	var title, prio, reporter string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT title, priority, reporter_id::text FROM ticket WHERE id = $1`, out.TicketID).
		Scan(&title, &prio, &reporter); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(title, "topology") || prio != "high" || reporter != e.reporter {
		t.Errorf("ticket %q %s %s, want the incident's title, high and the caller", title, prio, reporter)
	}
	rec = e.serve(perms, http.MethodPost, path, `{}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), out.TicketID) || !strings.Contains(rec.Body.String(), `"ticket_number"`) {
		t.Errorf("second ticket = %d %s, want 409 with ticket_id and ticket_number", rec.Code, rec.Body)
	}
	if rec := e.serve([]identity.Permission{identity.PermMonitoringRead}, http.MethodPost, path, `{}`); rec.Code != http.StatusForbidden {
		t.Errorf("without ticket:write = %d, want 403", rec.Code)
	}
}

// TestManualIncidentTicketNeedsTicketingEssential: without the feature the
// route answers 403 feature-not-entitled.
func TestManualIncidentTicketNeedsTicketingEssential(t *testing.T) {
	e := newWriterEnv(t, featureSet{})
	inc, _ := e.incident("wp1040-noent", 1)
	rec := e.serve(ticketPerms, http.MethodPost, "/api/v1/monitoring/incidents/"+inc.ID+"/ticket", `{}`)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "feature-not-entitled") {
		t.Errorf("without ticketing_essential = %d %s, want 403 feature-not-entitled", rec.Code, rec.Body)
	}
	// The ticket action skips the alarm as well.
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp1040-noent-ci")
	a := e.raiseTicketed(ci, "cpu", false)
	if e.ticketOf(a.ID) != "" {
		t.Error("an alarm without ticketing_essential opened a ticket")
	}
}

// TestAlarmDetailListsTickets: GET /monitoring/alarms/{id} names the
// tickets of the alarm with ticket:read and none without.
func TestAlarmDetailListsTickets(t *testing.T) {
	e := newWriterEnv(t, withTicketing)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp1040-detail")
	a := e.raiseTicketed(ci, "cpu", false)
	tid := e.ticketOf(a.ID)
	var view struct {
		Tickets []alarm.TicketRef `json:"tickets"`
	}
	rec := e.serve(ticketPerms, http.MethodGet, "/api/v1/monitoring/alarms/"+a.ID, "")
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &view) != nil {
		t.Fatalf("detail = %d %s", rec.Code, rec.Body)
	}
	if len(view.Tickets) != 1 || view.Tickets[0].TicketID != tid || view.Tickets[0].TicketNumber == 0 {
		t.Errorf("tickets %+v, want the alarm's ticket", view.Tickets)
	}
	rec = e.serve([]identity.Permission{identity.PermMonitoringRead}, http.MethodGet, "/api/v1/monitoring/alarms/"+a.ID, "")
	view.Tickets = nil
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &view) != nil || len(view.Tickets) != 0 {
		t.Errorf("without ticket:read: %d %s, want no tickets", rec.Code, rec.Body)
	}
}

// TestRuleTicketReporterValidation: the rule API sets the saving user as
// reporter and refuses an inactive user or one without ticket:write with
// 422 on actions.ticket.reporter_id.
func TestRuleTicketReporterValidation(t *testing.T) {
	e := newWriterEnv(t, withTicketing)
	inactive := e.f.AppUser(t, e.f.OrgA, "wp1040-inactive")
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE app_user SET is_active = false WHERE id = $1`, inactive); err != nil {
		t.Fatal(err)
	}
	h := monitoring.NewHandler(monitoring.NewPGMetricStore(e.f.App)).
		WithTicketReporters(server.NewTicketReporters(user.NewPGRepository(e.f.App), permission.NewPGRepository(e.f.App)))
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	body := func(reporter string) string {
		r := ""
		if reporter != "" {
			r = `,"reporter_id":"` + reporter + `"`
		}
		return `{"name":"wp1040 cpu","metric_name":"cpu","condition":"gt","threshold":90,"severity":"critical",
			"actions":{"ticket":{"enabled":true` + r + `}}}`
	}
	// Only the saving user (or the reporter the rule has already) may be
	// the reporter: a foreign, even active, user is 422.
	other := e.f.AppUser(t, e.f.OrgA, "wp1040-other")
	for name, reporter := range map[string]string{"inactive": inactive, "foreign": other} {
		rec := e.doRaw(mux, ticketPerms, http.MethodPost, "/api/v1/monitoring/alerts", body(reporter))
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "actions.ticket.reporter_id") {
			t.Errorf("%s reporter = %d %s, want 422 on actions.ticket.reporter_id", name, rec.Code, rec.Body)
		}
	}
}

// restrictedTicketPolicy is the policy of a principal with ticket:write
// whose field grant leaves assignee_id of tickets read only.
func restrictedTicketPolicy(t *testing.T, orgID string, perms []identity.Permission) *permission.FieldPolicy {
	t.Helper()
	ctx := context.Background()
	repo := permission.NewMemoryFieldGrants()
	role := "44444444-4444-4444-8444-0000000010a4"
	key := "core.assignee_id"
	if _, err := repo.CreateFieldGrant(ctx, orgID, &permission.FieldGrant{RoleID: &role, ObjectKind: permission.KindTicket,
		FieldKey: &key, Level: permission.LevelRead}); err != nil {
		t.Fatal(err)
	}
	p, err := permission.NewFieldResolver(nil, nil, repo, nil).PolicyForGrants(ctx, orgID, []identity.Grant{
		{RoleID: role, Permissions: perms},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Restricted(permission.KindTicket) {
		t.Fatal("policy not restricted")
	}
	return p
}

// TestManualIncidentTicketFieldRights covers AK4: the manual ticket follows
// the field rights of the caller like the review tickets: assignee_id a
// field grant leaves read only is 403 naming the field, no ticket; without
// it the ticket is created.
func TestManualIncidentTicketFieldRights(t *testing.T) {
	e := newWriterEnv(t, withTicketing)
	inc, _ := e.incident("wp1040-fields", 1)
	perms := []identity.Permission{identity.PermMonitoringRead, identity.PermTicketRead, identity.PermTicketWrite}
	path := "/api/v1/monitoring/incidents/" + inc.ID + "/ticket"
	ctx := permission.WithPolicy(e.asReporterWithoutPolicy(perms), restrictedTicketPolicy(t, e.f.OrgA, perms))
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"assignee_id":"`+e.reporter+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "assignee_id") {
		t.Fatalf("assignee_id without the field right = %d %s, want 403 naming the field", rec.Code, rec.Body)
	}
	if n := e.countTickets(`ticket_link WHERE entity_type = 'incident' AND incident_id = $1`, inc.ID); n != 0 {
		t.Errorf("%d tickets of the incident after the refusal, want none", n)
	}
	req = httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"priority":"high"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req.WithContext(ctx))
	if rec.Code != http.StatusCreated {
		t.Errorf("without the restricted field = %d %s, want 201", rec.Code, rec.Body)
	}
}

// ticketActions is the actions object with the ticket action of the rule.
func (e *writerEnv) ticketActions(closeOnResolve bool) alarm.Actions {
	actions := alarm.DefaultActions()
	actions.Ticket = &events.AlarmTicketAction{Enabled: true, ReporterID: e.reporter, CloseOnResolve: closeOnResolve}
	return actions
}

// TestIncidentFormedAfterRaiseOneTicket covers AK2-AK4 in the real order:
// the alarms of three CIs open (each with the ticket action: the root
// alarm's own ticket at its CI, and one per symptom CI as long as no
// incident exists), then the correlation forms the incident
// (Service.AttachAlarms): the root's ticket is linked to the incident, the
// symptoms are linked to it; the manual POST is 409 naming that ticket.
func TestIncidentFormedAfterRaiseOneTicket(t *testing.T) {
	e := newWriterEnv(t, withTicketing)
	ctx := e.f.OrgCtx(e.f.OrgA)
	var ids []string
	for i := range 3 {
		ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp1040-seq-"+string(rune('a'+i)))
		ids = append(ids, e.raiseTicketed(ci, "down", false).ID)
	}
	rootTicket := e.ticketOf(ids[0])
	if rootTicket == "" {
		t.Fatal("the root alarm opened no ticket")
	}
	inc, _, err := e.store.UpsertIncident(ctx, e.f.OrgA, &alarm.IncidentInput{CauseClass: alarm.CauseTopology, Confidence: 70})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.svc.AttachAlarms(ctx, e.f.OrgA, inc.ID, ids[:1], alarm.RoleRoot); err != nil {
		t.Fatal(err)
	}
	if _, err = e.svc.AttachAlarms(ctx, e.f.OrgA, inc.ID, ids[1:], alarm.RoleSymptom); err != nil {
		t.Fatal(err)
	}
	if n := e.countTickets(`ticket_link WHERE entity_type = 'incident' AND incident_id = $1`, inc.ID); n != 1 {
		t.Fatalf("%d tickets linked to the incident, want the root's one", n)
	}
	if n := e.countTickets(`ticket_link WHERE ticket_id = $1 AND entity_type = 'incident' AND incident_id = $2`, rootTicket, inc.ID); n != 1 {
		t.Errorf("the root alarm's ticket is not linked to the incident")
	}
	for _, id := range ids[1:] {
		if n := e.countTickets(`ticket_link WHERE ticket_id = $1 AND entity_type = 'alarm' AND alarm_id = $2`, rootTicket, id); n != 1 {
			t.Errorf("symptom %s not linked to the incident's ticket", id)
		}
	}
	rec := e.serve(ticketPerms, http.MethodPost, "/api/v1/monitoring/incidents/"+inc.ID+"/ticket", `{}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), rootTicket) {
		t.Errorf("manual ticket after the correlation = %d %s, want 409 naming %s", rec.Code, rec.Body, rootTicket)
	}
}

// TestIncidentRootLinksTicketAtRootCI covers AK3: an open ticket at the
// root CI of an incident is linked to the incident (and the root alarm)
// instead of a new ticket.
func TestIncidentRootLinksTicketAtRootCI(t *testing.T) {
	e := newWriterEnv(t, withTicketing)
	inc, cis := e.incident("wp1040-rootci", 2)
	existing := e.ticketAt("open at the root CI", cis[0])
	e.link(existing, "ci", cis[0])
	ctx := e.f.OrgCtx(e.f.OrgA)
	root, err := e.store.Get(ctx, e.f.OrgA, *inc.RootAlarmID)
	if err != nil {
		t.Fatal(err)
	}
	actions := e.ticketActions(false)
	if _, err = e.store.UpdateState(ctx, e.f.OrgA, root.ID, &alarm.StateUpdate{State: root.State, Severity: root.Severity,
		IncidentID: root.IncidentID, OpenedAt: root.OpenedAt, Details: map[string]any{alarm.DetailsActions: actions}}); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.OpenAlarmTicket(ctx, e.f.OrgA, root.ID)
	if err != nil || got == nil || got.Ticket.TicketID != existing || !got.Existing {
		t.Fatalf("root ticket %+v (%v), want the existing ticket at the root CI", got, err)
	}
	if n := e.countTickets(`ticket_link WHERE ticket_id = $1 AND entity_type = 'incident' AND incident_id = $2`, existing, inc.ID); n != 1 {
		t.Errorf("the ticket at the root CI is not linked to the incident")
	}
	if n := e.countTickets(`ticket WHERE organization_id = $1 AND reporter_id = $2 AND id <> $3`, e.f.OrgA, e.reporter, existing); n != 0 {
		t.Errorf("%d new tickets, want none", n)
	}
}

// TestResolveSharedTicketWaitsForLastAlarm covers AK5: two alarms of one
// CI share the ticket; with close_on_resolve the first that clears only
// comments it, the last resolves it.
func TestResolveSharedTicketWaitsForLastAlarm(t *testing.T) {
	e := newWriterEnv(t, withTicketing)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp1040-shared")
	cpu := e.raiseTicketed(ci, "cpu", true)
	mem := e.raiseTicketed(ci, "mem", true)
	tid := e.ticketOf(cpu.ID)
	if tid == "" || e.ticketOf(mem.ID) != tid {
		t.Fatal("the two alarms of the CI do not share one ticket")
	}
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE ticket SET status = 'open' WHERE id = $1`, tid); err != nil {
		t.Fatal(err)
	}
	status := func() string {
		var s string
		if err := e.f.Admin.QueryRow(context.Background(), `SELECT status FROM ticket WHERE id = $1`, tid).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := e.systemComments(tid)
	if _, err := e.svc.Clear(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, cpu.DedupKey, cpu.LastSeenAt, ""); err != nil {
		t.Fatal(err)
	}
	if s := status(); s != "open" || e.systemComments(tid) != before+1 {
		t.Errorf("after the first alarm: ticket %s with %d new comments, want open with one comment", s, e.systemComments(tid)-before)
	}
	if _, err := e.svc.Clear(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, mem.DedupKey, mem.LastSeenAt, ""); err != nil {
		t.Fatal(err)
	}
	if s := status(); s != "resolved" {
		t.Errorf("after the last alarm: ticket %s, want resolved", s)
	}
}

// TestIncidentResolutionCommentsManualTicket covers AK5: the manual
// ticket of an incident gets a system comment when the incident resolves
// (Service.TransitionIncident); a ticket a person opened stays open.
func TestIncidentResolutionCommentsManualTicket(t *testing.T) {
	e := newWriterEnv(t, withTicketing)
	inc, _ := e.incident("wp1040-incres", 1)
	rec := e.serve(ticketPerms, http.MethodPost, "/api/v1/monitoring/incidents/"+inc.ID+"/ticket", `{}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("manual ticket = %d %s", rec.Code, rec.Body)
	}
	var out alarm.IncidentTicketView
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	before := e.systemComments(out.TicketID)
	if _, err := e.svc.TransitionIncident(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, inc.ID, alarm.IncidentResolved, nil); err != nil {
		t.Fatal(err)
	}
	if n := e.systemComments(out.TicketID); n != before+1 {
		t.Errorf("%d comments after the resolution of the incident, want %d", n, before+1)
	}
	var status string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT status FROM ticket WHERE id = $1`, out.TicketID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status == "resolved" || status == "closed" {
		t.Errorf("manual ticket %s after the incident resolved, want it left to its owner", status)
	}
}

// TestSuppressedAlarmOpensTicketOnRelease: an alarm suppressed in a
// maintenance window opens no ticket; the release of the window opens it.
func TestSuppressedAlarmOpensTicketOnRelease(t *testing.T) {
	e := newWriterEnv(t, withTicketing)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp1040-maint")
	window := "33333333-3333-4333-8333-0000000010d4"
	checker := &toggleWindow{ci: ci, window: window, active: true}
	e.svc.WithMaintenance(checker)
	a := e.raiseTicketed(ci, "cpu", false)
	if a.SuppressedReason == nil || e.ticketOf(a.ID) != "" {
		t.Fatalf("suppressed alarm %+v with ticket %q, want suppressed without ticket", a, e.ticketOf(a.ID))
	}
	checker.active = false
	if n, err := e.svc.ReleaseMaintenance(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, window); err != nil || n != 1 {
		t.Fatalf("release: %d, %v", n, err)
	}
	if e.ticketOf(a.ID) == "" {
		t.Error("the released alarm opened no ticket")
	}
}

// toggleWindow holds one CI in a window while active.
type toggleWindow struct {
	ci, window string
	active     bool
}

func (w *toggleWindow) MaintenanceOf(_ context.Context, _, ciID string, _ time.Time) (alarm.MaintenanceHit, error) {
	if w.active && ciID == w.ci {
		return alarm.MaintenanceHit{WindowID: w.window}, nil
	}
	return alarm.MaintenanceHit{}, nil
}
