package alarm_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
)

// The tickets of an incident on PostgreSQL (ALR-09 d, TKT-01 Monitoring
// [P3], WP-1039): the link types incident and alarm, the open tickets at
// the incident, its root CI and its affected CIs under ticket RLS, linking
// a ticket with its system comment and the ticket of the incident list.

// ticketPerms read and link tickets besides the monitoring.
var ticketPerms = []identity.Permission{identity.PermMonitoringRead, identity.PermTicketRead, identity.PermTicketWrite}

// featureSet is an entitlement.Checker with fixed answers.
type featureSet map[string]bool

func (f featureSet) IsEnabled(_ context.Context, _, key string) bool { return f[key] }

type ticketEnv struct {
	t       *testing.T
	f       *scopetest.Fixture
	svc     *alarm.Service
	store   alarm.Store
	tickets *ticket.Service
	port    alarm.TicketPort
	mux     http.Handler
	teamA   string
	teamB   string
	// reporter is the app user the tickets are reported by.
	reporter string
}

func newTicketEnv(t *testing.T, features featureSet) *ticketEnv {
	t.Helper()
	f := scopetest.Seed(t, alarmTag)
	svc := alarm.NewPGService(f.App)
	tickets := ticket.NewService(ticket.NewPGRepository(f.App).WithEvents(events.NewOutbox()), nil)
	port := server.NewIncidentTickets(tickets, nil)
	inner := chi.NewRouter()
	alarm.NewHandler(svc).WithTickets(port, features).RegisterRoutes(inner)
	ticket.NewHandler(tickets).RegisterRoutes(inner)
	mux := chi.NewRouter()
	routes := append(slices.Clone(incidentRoutes),
		struct{ method, pattern string }{http.MethodPost, "/api/v1/monitoring/incidents/{id}/tickets/{ticket_id}"},
		struct{ method, pattern string }{http.MethodPost, "/api/v1/tickets/{id}/links"})
	for _, rt := range routes {
		mux.With(server.AuthorizeRoute(rt.method, rt.pattern)).Method(rt.method, rt.pattern, inner)
	}
	e := &ticketEnv{t: t, f: f, svc: svc, store: svc.Store(), tickets: tickets, port: port, mux: mux,
		reporter: f.AppUser(t, f.OrgA, "wp1039-reporter")}
	for _, team := range []*string{&e.teamA, &e.teamB} {
		*team = f.ID()
		if _, err := f.Admin.Exec(context.Background(), `INSERT INTO team (id, organization_id, name) VALUES ($1, $2, $3)`,
			*team, f.OrgA, "wp1039-"+(*team)[len(*team)-6:]); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

// ctx is a request context of the fixture's user with perms, org-wide or
// bound to team.
func (e *ticketEnv) ctx(team string, perms []identity.Permission) context.Context {
	scope := database.OrgWideScope(e.f.OrgA, e.f.User)
	idScope := &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}}
	if team != "" {
		scope.Teams = database.ScopeIDs(team)
		idScope.Teams = identity.ScopeSet{IDs: []string{team}}
	}
	p := identity.Principal{Subject: e.f.User, OrganizationID: e.f.OrgA, Type: identity.PrincipalTypeUser, Scope: idScope, Permissions: perms}
	ctx := identity.WithPermissions(identity.WithPrincipal(context.Background(), p), perms)
	ctx = database.ContextWithTenantScope(ctx, &scope)
	return tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: e.f.User})
}

func (e *ticketEnv) do(perms []identity.Permission, method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.doCtx(e.ctx("", perms), method, path, body)
}

// clientCtx is a request context of the fixture's user with perms, bound
// to the client.
func (e *ticketEnv) clientCtx(client string, perms []identity.Permission) context.Context {
	scope := database.OrgWideScope(e.f.OrgA, e.f.User)
	scope.Clients = database.ScopeIDs(client)
	idScope := &identity.Scope{Clients: identity.ScopeSet{IDs: []string{client}}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}}
	p := identity.Principal{Subject: e.f.User, OrganizationID: e.f.OrgA, Type: identity.PrincipalTypeUser, Scope: idScope, Permissions: perms}
	ctx := identity.WithPermissions(identity.WithPrincipal(context.Background(), p), perms)
	ctx = database.ContextWithTenantScope(ctx, &scope)
	return tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: e.f.User})
}

func (e *ticketEnv) doCtx(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

// ticket inserts a ticket of the team (empty: none) with the status and
// returns its id and number.
func (e *ticketEnv) ticket(title, team, status string) (string, int) {
	e.t.Helper()
	var id string
	var number int
	var teamArg any
	if team != "" {
		teamArg = team
	}
	if err := e.f.Admin.QueryRow(context.Background(), `INSERT INTO ticket (organization_id, title, reporter_id, team_id, status)
		VALUES ($1, $2, $3, $4, $5) RETURNING id::text, ticket_number`, e.f.OrgA, title, e.reporter, teamArg, status).Scan(&id, &number); err != nil {
		e.t.Fatal(err)
	}
	return id, number
}

// ticketAt inserts an open ticket related to the CI: it takes the CI's
// client and site (derive_ticket_scope).
func (e *ticketEnv) ticketAt(title, ci string) string {
	e.t.Helper()
	var id string
	if err := e.f.Admin.QueryRow(context.Background(), `INSERT INTO ticket (organization_id, title, reporter_id, related_ci_id, status)
		VALUES ($1, $2, $3, $4, 'open') RETURNING id::text`, e.f.OrgA, title, e.reporter, ci).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// link links the ticket to the target as the org-wide user.
func (e *ticketEnv) link(ticketID, entityType, entityID string) {
	e.t.Helper()
	if err := e.tickets.CreateLink(e.ctx("", ticketPerms), &ticket.Link{OrganizationID: e.f.OrgA, TicketID: ticketID,
		EntityType: entityType, EntityID: entityID}); err != nil {
		e.t.Fatalf("link %s %s: %v", entityType, entityID, err)
	}
}

// incident opens an incident of client 1 with a root and n-1 symptoms on
// CIs of their own; it returns the incident and the CIs (root first).
func (e *ticketEnv) incident(prefix string, n int) (*alarm.Incident, []string) {
	e.t.Helper()
	return e.clientIncident(e.f.Client1, prefix, n)
}

// clientIncident is incident on CIs of the client.
func (e *ticketEnv) clientIncident(client, prefix string, n int) (*alarm.Incident, []string) {
	e.t.Helper()
	ctx := e.f.OrgCtx(e.f.OrgA)
	var ids, cis []string
	for i := range n {
		ci := e.f.CI(e.t, e.f.OrgA, client, prefix+"-"+string(rune('a'+i)))
		cis = append(cis, ci)
		a, _, err := e.store.UpsertOpen(ctx, e.f.OrgA, &alarm.UpsertInput{SourceKind: alarm.SourceCheck, SourceID: e.f.ID(),
			SeriesKey: prefix, CIID: &ci, Severity: alarm.SeverityCritical, State: alarm.StateOpen, Summary: "down"})
		if err != nil {
			e.t.Fatal(err)
		}
		ids = append(ids, a.ID)
	}
	inc, _, err := e.store.UpsertIncident(ctx, e.f.OrgA, &alarm.IncidentInput{CauseClass: alarm.CauseTopology, Confidence: 70})
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err = e.store.AttachAlarms(ctx, e.f.OrgA, inc.ID, ids[:1], alarm.RoleRoot); err != nil {
		e.t.Fatal(err)
	}
	if n > 1 {
		if _, err = e.store.AttachAlarms(ctx, e.f.OrgA, inc.ID, ids[1:], alarm.RoleSymptom); err != nil {
			e.t.Fatal(err)
		}
	}
	if inc, err = e.store.GetIncident(ctx, e.f.OrgA, inc.ID); err != nil {
		e.t.Fatal(err)
	}
	return inc, cis
}

// open runs OpenTickets in one transaction of the context.
func (e *ticketEnv) open(ctx context.Context, lookup *alarm.TicketLookup, inc *alarm.Incident) *alarm.OpenTickets {
	e.t.Helper()
	var out *alarm.OpenTickets
	if err := e.svc.InTx(ctx, e.f.OrgA, func(ctx context.Context) error {
		var err error
		out, err = lookup.OpenTickets(ctx, inc)
		return err
	}); err != nil {
		e.t.Fatal(err)
	}
	return out
}

func TestOpenTicketsFindsRootCITicket(t *testing.T) {
	e := newTicketEnv(t, featureSet{"ticketing_essential": true})
	inc, cis := e.incident("wp1039-root", 3)
	atIncident, _ := e.ticket("at incident", "", ticket.StatusOpen)
	atRoot, _ := e.ticket("at root", "", ticket.StatusNew)
	atAffected, _ := e.ticket("at affected", "", ticket.StatusInProgress)
	e.link(atIncident, ticket.LinkIncident, inc.ID)
	e.link(atRoot, ticket.LinkCI, cis[0])
	e.link(atAffected, ticket.LinkCI, cis[2])
	// Linked at the incident and at the root CI: named once, by the incident.
	e.link(atIncident, ticket.LinkCI, cis[0])

	got := e.open(e.ctx("", ticketPerms), alarm.NewTicketLookup(e.port, e.store, nil), inc)
	var via []string
	for _, tk := range got.Tickets {
		via = append(via, tk.TicketID+"/"+tk.LinkedVia)
	}
	want := []string{atIncident + "/incident", atRoot + "/root_ci", atAffected + "/affected_ci"}
	if !slices.Equal(via, want) || got.Truncated {
		t.Fatalf("open tickets %v (truncated %v), want %v", via, got.Truncated, want)
	}
	if got.Tickets[1].CIID == nil || *got.Tickets[1].CIID != cis[0] || got.Tickets[2].CIID == nil || *got.Tickets[2].CIID != cis[2] {
		t.Errorf("ci ids of the CI tickets: %+v", got.Tickets)
	}
	if got.Tickets[1].Status != ticket.StatusNew || got.Tickets[1].TicketNumber == 0 {
		t.Errorf("root ticket %+v", got.Tickets[1])
	}
	// Without ticket:read none.
	if none := e.open(e.ctx("", []identity.Permission{identity.PermMonitoringRead}), alarm.NewTicketLookup(e.port, e.store, nil), inc); len(none.Tickets) != 0 {
		t.Errorf("without ticket:read: %+v", none.Tickets)
	}
}

func TestOpenTicketsHidesInvisibleTicket(t *testing.T) {
	e := newTicketEnv(t, featureSet{"ticketing_essential": true})
	inc, cis := e.incident("wp1039-team", 1)
	own, _ := e.ticket("own team", e.teamA, ticket.StatusOpen)
	foreign, _ := e.ticket("foreign team", e.teamB, ticket.StatusOpen)
	e.link(own, ticket.LinkCI, cis[0])
	e.link(foreign, ticket.LinkCI, cis[0])
	got := e.open(e.ctx(e.teamA, ticketPerms), alarm.NewTicketLookup(e.port, e.store, nil), inc)
	if len(got.Tickets) != 1 || got.Tickets[0].TicketID != own {
		t.Fatalf("team A sees %+v, want only %s", got.Tickets, own)
	}
}

func TestOpenTicketsSkipsClosed(t *testing.T) {
	e := newTicketEnv(t, featureSet{"ticketing_essential": true})
	inc, cis := e.incident("wp1039-closed", 1)
	closed, _ := e.ticket("closed", "", ticket.StatusClosed)
	resolved, _ := e.ticket("resolved", "", ticket.StatusResolved)
	e.link(closed, ticket.LinkCI, cis[0])
	e.link(resolved, ticket.LinkIncident, inc.ID)
	got := e.open(e.ctx("", ticketPerms), alarm.NewTicketLookup(e.port, e.store, nil), inc)
	if len(got.Tickets) != 1 || got.Tickets[0].TicketID != resolved {
		t.Fatalf("open tickets %+v, want only the resolved one", got.Tickets)
	}
}

func TestOpenTicketsLimit200(t *testing.T) {
	e := newTicketEnv(t, featureSet{"ticketing_essential": true})
	inc, cis := e.incident("wp1039-limit", 3)
	first, _ := e.ticket("at affected 1", "", ticket.StatusOpen)
	beyond, _ := e.ticket("beyond the limit", "", ticket.StatusOpen)
	e.link(first, ticket.LinkCI, cis[1])
	e.link(beyond, ticket.LinkCI, cis[2])
	// 199 further CIs without tickets between the first affected CI and
	// the one beyond the limit.
	var asked int
	affected := func(_ context.Context, _ *alarm.Incident, limit int) ([]string, error) {
		asked = limit
		out := []string{cis[1]}
		for range alarm.MaxAffectedTicketCIs - 1 {
			out = append(out, e.f.ID())
		}
		return append(out, cis[2]), nil
	}
	got := e.open(e.ctx("", ticketPerms), alarm.NewTicketLookup(e.port, e.store, affected), inc)
	if asked != alarm.MaxAffectedTicketCIs || !got.Truncated {
		t.Fatalf("limit %d, truncated %v", asked, got.Truncated)
	}
	if len(got.Tickets) != 1 || got.Tickets[0].TicketID != first {
		t.Errorf("tickets %+v, want only the one of the first 200 CIs", got.Tickets)
	}
	// The default reads the CIs of the members: two, not truncated.
	if def := e.open(e.ctx("", ticketPerms), alarm.NewTicketLookup(e.port, e.store, nil), inc); def.Truncated || len(def.Tickets) != 2 {
		t.Errorf("member CIs: %+v", def)
	}
}

// systemComments counts the system comments of the ticket.
func (e *ticketEnv) systemComments(ticketID string) []string {
	e.t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT content FROM ticket_comment
		WHERE ticket_id = $1 AND author_type = 'system' ORDER BY created_at`, ticketID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

func (e *ticketEnv) incidentLinks(ticketID, incidentID string) int {
	e.t.Helper()
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM ticket_link
		WHERE ticket_id = $1 AND entity_type = 'incident' AND incident_id = $2`, ticketID, incidentID).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestLinkIncidentTicketWritesSystemComment(t *testing.T) {
	e := newTicketEnv(t, featureSet{"ticketing_essential": true})
	inc, cis := e.incident("wp1039-link", 2)
	// A ticket of the incident's client (at its root CI) reads the root
	// CI's name in the comment.
	tk := e.ticketAt("router down", cis[0])
	var number int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT ticket_number FROM ticket WHERE id = $1`, tk).Scan(&number); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/monitoring/incidents/" + inc.ID + "/tickets/" + tk
	rec := e.do(ticketPerms, http.MethodPost, path, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("link: %d %s", rec.Code, rec.Body)
	}
	var d struct {
		ID     string `json:"id"`
		Ticket *struct {
			TicketID     string `json:"ticket_id"`
			TicketNumber int    `json:"ticket_number"`
			Status       string `json:"status"`
		} `json:"ticket"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.ID != inc.ID || d.Ticket == nil || d.Ticket.TicketID != tk || d.Ticket.TicketNumber != number {
		t.Errorf("answer %s", rec.Body)
	}
	if n := e.incidentLinks(tk, inc.ID); n != 1 {
		t.Errorf("links of type incident: %d", n)
	}
	comments := e.systemComments(tk)
	if len(comments) != 1 || !strings.Contains(comments[0], inc.ID) || !strings.Contains(comments[0], "wp1039-link-a") ||
		!strings.Contains(comments[0], "topology") {
		t.Errorf("system comments %q", comments)
	}
	if rec := e.do(ticketPerms, http.MethodPost, path, ""); rec.Code != http.StatusConflict {
		t.Errorf("second link: %d %s, want 409", rec.Code, rec.Body)
	}
	if n := len(e.systemComments(tk)); n != 1 {
		t.Errorf("system comments after the 409: %d, want 1", n)
	}
	// An org-wide ticket is read beyond the incident's client: its comment
	// names the root CI by id, not by name.
	wide, _ := e.ticket("org-wide", "", ticket.StatusOpen)
	if rec := e.do(ticketPerms, http.MethodPost, "/api/v1/monitoring/incidents/"+inc.ID+"/tickets/"+wide, ""); rec.Code != http.StatusCreated {
		t.Fatalf("link org-wide ticket: %d %s", rec.Code, rec.Body)
	}
	if c := e.systemComments(wide); len(c) != 1 || strings.Contains(c[0], "wp1039-link-a") || !strings.Contains(c[0], cis[0]) ||
		!strings.Contains(c[0], inc.ID) {
		t.Errorf("system comments of the org-wide ticket %q, want the root by id only", c)
	}
	// An unknown incident and an unknown ticket are 404.
	for _, p := range []string{
		"/api/v1/monitoring/incidents/" + e.f.ID() + "/tickets/" + tk,
		"/api/v1/monitoring/incidents/" + inc.ID + "/tickets/" + e.f.ID(),
	} {
		if rec := e.do(ticketPerms, http.MethodPost, p, ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d %s, want 404", p, rec.Code, rec.Body)
		}
	}
	// An existing incident outside the caller's scope (client 2 for a
	// principal bound to client 1) is 404 for a ticket the caller sees,
	// without link and comment.
	foreign, _ := e.clientIncident(e.f.Client2, "wp1039-link-foreign", 1)
	own := e.ticketAt("client 1 ticket", cis[1])
	invisible := e.doCtx(e.clientCtx(e.f.Client1, ticketPerms), http.MethodPost,
		"/api/v1/monitoring/incidents/"+foreign.ID+"/tickets/"+own, "")
	if invisible.Code != http.StatusNotFound {
		t.Errorf("invisible incident: %d %s, want 404", invisible.Code, invisible.Body)
	}
	if n := e.incidentLinks(own, foreign.ID); n != 0 || len(e.systemComments(own)) != 0 {
		t.Errorf("invisible incident: %d links, comments %q", n, e.systemComments(own))
	}
	// Without ticket:write the route refuses.
	if rec := e.do([]identity.Permission{identity.PermMonitoringRead, identity.PermMonitoringWrite}, http.MethodPost, path, ""); rec.Code != http.StatusForbidden {
		t.Errorf("without ticket:write: %d", rec.Code)
	}
	// ticket:write without monitoring:read reads no incident: 404 like an
	// invisible one, no link.
	other, _ := e.ticket("helpdesk", "", ticket.StatusOpen)
	helpdesk := e.do([]identity.Permission{identity.PermTicketRead, identity.PermTicketWrite}, http.MethodPost,
		"/api/v1/monitoring/incidents/"+inc.ID+"/tickets/"+other, "")
	if helpdesk.Code != http.StatusNotFound {
		t.Errorf("without monitoring:read: %d %s, want 404", helpdesk.Code, helpdesk.Body)
	}
	if n := e.incidentLinks(other, inc.ID); n != 0 {
		t.Errorf("link written without monitoring:read: %d", n)
	}
}

func TestTicketLinkTypesIncidentAndAlarm(t *testing.T) {
	e := newTicketEnv(t, featureSet{"ticketing_essential": true})
	inc, _ := e.incident("wp1039-types", 1)
	tk, _ := e.ticket("typed links", "", ticket.StatusOpen)
	for _, target := range []struct{ typ, id string }{{"incident", inc.ID}, {"alarm", *inc.RootAlarmID}} {
		rec := e.do(ticketPerms, http.MethodPost, "/api/v1/tickets/"+tk+"/links",
			`{"entity_type":"`+target.typ+`","entity_id":"`+target.id+`"}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("link %s: %d %s", target.typ, rec.Code, rec.Body)
		}
		var l ticket.Link
		if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil || l.EntityType != target.typ || l.EntityID != target.id {
			t.Errorf("link %s: %s %v", target.typ, rec.Body, err)
		}
	}
	// An unknown or invisible target is refused as for the other types
	// (422 entity_id, ticket.handler).
	if rec := e.do(ticketPerms, http.MethodPost, "/api/v1/tickets/"+tk+"/links",
		`{"entity_type":"incident","entity_id":"`+e.f.ID()+`"}`); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), `"field":"entity_id"`) {
		t.Errorf("unknown incident: %d %s", rec.Code, rec.Body)
	}
	// An existing incident and alarm outside the caller's scope (client 2
	// for a principal bound to client 1) are refused alike.
	foreign, _ := e.clientIncident(e.f.Client2, "wp1039-types-foreign", 1)
	own := e.ticketAt("client 1 ticket", e.f.CI(t, e.f.OrgA, e.f.Client1, "wp1039-types-own"))
	for _, target := range []struct{ typ, id string }{{"incident", foreign.ID}, {"alarm", *foreign.RootAlarmID}} {
		rec := e.doCtx(e.clientCtx(e.f.Client1, ticketPerms), http.MethodPost, "/api/v1/tickets/"+own+"/links",
			`{"entity_type":"`+target.typ+`","entity_id":"`+target.id+`"}`)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"field":"entity_id"`) {
			t.Errorf("invisible %s: %d %s, want 422 entity_id", target.typ, rec.Code, rec.Body)
		}
	}
	// Deleting the incident (only through the retention) deletes its link.
	tx, err := e.f.Admin.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(context.Background(), `SET LOCAL app.alarm_retention = 'on'`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(context.Background(), `DELETE FROM incident WHERE id = $1`, inc.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := e.incidentLinks(tk, inc.ID); n != 0 {
		t.Errorf("links of the deleted incident: %d", n)
	}
}

func TestLinkIncidentTicketNeedsTicketingEssential(t *testing.T) {
	e := newTicketEnv(t, featureSet{})
	inc, _ := e.incident("wp1039-feature", 1)
	tk, _ := e.ticket("no feature", "", ticket.StatusOpen)
	rec := e.do(ticketPerms, http.MethodPost, "/api/v1/monitoring/incidents/"+inc.ID+"/tickets/"+tk, "")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "feature-not-entitled") {
		t.Fatalf("without ticketing_essential: %d %s", rec.Code, rec.Body)
	}
	if n := e.incidentLinks(tk, inc.ID); n != 0 {
		t.Errorf("link written without the feature: %d", n)
	}
}

func TestIncidentListCarriesTicket(t *testing.T) {
	e := newTicketEnv(t, featureSet{"ticketing_essential": true})
	inc, _ := e.incident("wp1039-list", 1)
	other, _ := e.incident("wp1039-list-other", 1)
	closed, _ := e.ticket("closed first", "", ticket.StatusClosed)
	open, number := e.ticket("open second", "", ticket.StatusInProgress)
	e.link(closed, ticket.LinkIncident, inc.ID)
	e.link(open, ticket.LinkIncident, inc.ID)

	type view struct {
		ID     string           `json:"id"`
		Ticket *alarm.TicketRef `json:"ticket"`
	}
	list := func(perms []identity.Permission) map[string]*alarm.TicketRef {
		t.Helper()
		rec := e.do(perms, http.MethodGet, "/api/v1/monitoring/incidents", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("list: %d %s", rec.Code, rec.Body)
		}
		var page struct {
			Data []view `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		out := map[string]*alarm.TicketRef{}
		for _, v := range page.Data {
			out[v.ID] = v.Ticket
		}
		return out
	}
	got := list(ticketPerms)
	if ref := got[inc.ID]; ref == nil || ref.TicketID != open || ref.TicketNumber != number || ref.Status != ticket.StatusInProgress {
		t.Errorf("ticket of the incident %+v, want %s #%d in_progress", ref, open, number)
	}
	if _, ok := got[other.ID]; !ok || got[other.ID] != nil {
		t.Errorf("the other incident: %+v (present %v), want no ticket", got[other.ID], ok)
	}
	rec := e.do(ticketPerms, http.MethodGet, "/api/v1/monitoring/incidents/"+inc.ID, "")
	var d view
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil || d.Ticket == nil || d.Ticket.TicketID != open {
		t.Errorf("detail: %s %v", rec.Body, err)
	}
	// Without ticket:read the field is absent.
	if ref := list([]identity.Permission{identity.PermMonitoringRead})[inc.ID]; ref != nil {
		t.Errorf("without ticket:read: %+v", ref)
	}
	rec = e.do([]identity.Permission{identity.PermMonitoringRead}, http.MethodGet, "/api/v1/monitoring/incidents/"+inc.ID, "")
	if strings.Contains(rec.Body.String(), `"ticket"`) {
		t.Errorf("detail without ticket:read names a ticket: %s", rec.Body)
	}
}
