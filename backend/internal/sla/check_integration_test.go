package sla_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/sla"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
)

// standardSwitch is the entitlement.Checker of the tests: ticketing_standard
// per organization, switchable.
type standardSwitch struct {
	mu sync.Mutex
	on map[string]bool
}

func (s *standardSwitch) IsEnabled(_ context.Context, orgID, key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return key == entitlement.FeatureTicketingStandard && s.on[orgID]
}

func (s *standardSwitch) set(orgID string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.on[orgID] = on
}

// breachWorld is the PostgreSQL fixture of the sla.check tests (WP-769):
// the ticket and SLA handlers with the gated hooks, org-wide policies per
// priority in both organizations (elapsed time) and the run.
type breachWorld struct {
	f        *scopetest.Fixture
	mux      chi.Router
	features *standardSwitch
	check    *sla.BreachCheck
	users    map[string]string // reporter per organization
	policies map[string]map[string]*sla.Policy
	repo     *sla.PGRepository
	seen     map[string]bool
}

func newBreachWorld(t *testing.T) *breachWorld {
	t.Helper()
	f := scopetest.Seed(t, "39")
	w := &breachWorld{f: f, features: &standardSwitch{on: map[string]bool{f.OrgA: true, f.OrgB: true}},
		users: map[string]string{}, policies: map[string]map[string]*sla.Policy{}, repo: sla.NewPGRepository(f.App),
		seen: map[string]bool{}}
	tickets := ticket.NewPGRepository(f.App)
	w.mux = chi.NewRouter()
	ticket.NewHandler(ticket.NewService(tickets, sla.TicketHooks{Repo: w.repo, Features: w.features})).RegisterRoutes(w.mux)
	sla.NewHandler(w.repo, tickets).RegisterRoutes(w.mux)
	for _, org := range []string{f.OrgA, f.OrgB} {
		w.users[org] = f.AppUser(t, org, "breach-reporter-"+org[len(org)-2:])
		w.policies[org] = map[string]*sla.Policy{}
		for prio, minutes := range map[string][2]int{"low": {240, 960}, "medium": {60, 240}, "high": {15, 60}} {
			p := &sla.Policy{OrganizationID: org, Name: "breach " + prio, Priority: prio,
				ResponseTargetMinutes: minutes[0], ResolutionTargetMinutes: minutes[1]}
			if err := w.repo.CreatePolicy(f.OrgCtx(org), p); err != nil {
				t.Fatalf("policy %s: %v", prio, err)
			}
			w.policies[org][prio] = p
		}
	}
	w.check = sla.NewBreachCheck(f.App)
	w.check.Licensed = func(_ context.Context, orgID string) (bool, error) {
		return w.features.IsEnabled(context.Background(), orgID, entitlement.FeatureTicketingStandard), nil
	}
	return w
}

func (w *breachWorld) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := w.f.Admin.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// request sends an org-wide request of the organization's reporter.
func (w *breachWorld) request(t *testing.T, org, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	ctx := tenant.WithTenant(w.f.OrgCtx(org), tenant.TenantInfo{OrganizationID: org, UserID: w.users[org]})
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

func (w *breachWorld) create(t *testing.T, org string, body map[string]any) ticket.Ticket {
	t.Helper()
	rec := w.request(t, org, http.MethodPost, "/api/v1/tickets", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create ticket: %d %s", rec.Code, rec.Body)
	}
	var tk ticket.Ticket
	if err := json.Unmarshal(rec.Body.Bytes(), &tk); err != nil {
		t.Fatal(err)
	}
	return tk
}

func (w *breachWorld) patch(t *testing.T, org, id string, body map[string]any) {
	t.Helper()
	if rec := w.request(t, org, http.MethodPatch, "/api/v1/tickets/"+id, body); rec.Code != http.StatusOK {
		t.Fatalf("patch %v: %d %s", body, rec.Code, rec.Body)
	}
}

// overdue moves the due times of the ticket's SLA state into the past
// (response and/or resolution).
func (w *breachWorld) overdue(t *testing.T, ticketID string, response, resolution bool) {
	t.Helper()
	w.exec(t, `UPDATE ticket_sla SET
		response_due_at = CASE WHEN $2 THEN now() - interval '2 hours' ELSE now() + interval '2 days' END,
		resolution_due_at = CASE WHEN $3 THEN now() - interval '1 hour' ELSE now() + interval '3 days' END
		WHERE ticket_id = $1`, ticketID, response, resolution)
}

func (w *breachWorld) run(t *testing.T, org string) int {
	t.Helper()
	n, err := w.check.RunOrg(context.Background(), org)
	if err != nil {
		t.Fatalf("run %s: %v", org, err)
	}
	return n
}

// breach is one ticket.sla_breached event of the outbox.
type breach struct {
	org, entity string
	data        map[string]any
}

// fresh returns the ticket.sla_breached events of the organization written
// since the last call, in commit order.
func (w *breachWorld) fresh(t *testing.T, org string) []breach {
	t.Helper()
	rows, err := w.f.Admin.Query(context.Background(), `SELECT id::text, organization_id::text, COALESCE(entity_id::text, ''), data
		FROM event_outbox WHERE organization_id = $1 AND type = $2 ORDER BY sequence`, org, events.TicketSLABreached)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []breach
	for rows.Next() {
		var id string
		var b breach
		var raw []byte
		if err = rows.Scan(&id, &b.org, &b.entity, &raw); err != nil {
			t.Fatal(err)
		}
		if w.seen[id] {
			continue
		}
		w.seen[id] = true
		if err = json.Unmarshal(raw, &b.data); err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func kinds(bs []breach) string {
	var out []string
	for _, b := range bs {
		out = append(out, b.entity+":"+b.data["kind"].(string))
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// notified reads the breach notification times of the ticket's state.
func (w *breachWorld) notified(t *testing.T, ticketID string) (response, resolution *time.Time) {
	t.Helper()
	if err := w.f.Admin.QueryRow(context.Background(), `SELECT response_breach_notified_at, resolution_breach_notified_at
		FROM ticket_sla WHERE ticket_id = $1`, ticketID).Scan(&response, &resolution); err != nil {
		t.Fatal(err)
	}
	return response, resolution
}

// TestSLABreachCheck covers TKT-01 (Standard: SLA breach escalation, WP-769)
// against PostgreSQL:
//  1. an overdue response and resolution time each give exactly one
//     ticket.sla_breached with its kind and the fields of the frame, a
//     second run none;
//  2. a breach a read marked first (GET /api/v1/tickets/{id}/sla) is
//     reported once;
//  3. PATCH priority chooses the SLA again from the ticket's creation and
//     reports the new breach exactly once; a policy named with sla_id
//     stays;
//  4. a reopen clears resolved_at and resolution_breach_notified_at, the
//     next run reports the resolution once; new→open changes nothing;
//  5. without ticketing_standard the run skips the organization and POST
//     /api/v1/tickets attaches no SLA;
//  7. the organizations stay apart, more than 500 due rows are processed
//     in several batches, and a run whose events fail leaves neither marks
//     nor events.
//
// Case 6 (notification chain) is TestSLABreachNotifiesAssigneeAndLead.
func TestSLABreachCheck(t *testing.T) {
	w := newBreachWorld(t)
	f := w.f
	ci1 := f.CI(t, f.OrgA, f.Client1, "breach-c1")
	assignee := f.AppUser(t, f.OrgA, "breach-assignee")
	lead := f.AppUser(t, f.OrgA, "breach-lead")
	team := f.ID()
	w.exec(t, `INSERT INTO team (id, organization_id, name, lead_id) VALUES ($1, $2, 'breach team', $3)`, team, f.OrgA, lead)

	// (1) Both due times overdue: one event per kind, then none.
	t1 := w.create(t, f.OrgA, map[string]any{"title": "both", "priority": "medium", "assignee_id": assignee,
		"team_id": team, "related_ci_id": ci1})
	w.overdue(t, t1.ID, true, true)
	if n := w.run(t, f.OrgA); n != 2 {
		t.Fatalf("first run reported %d, want 2", n)
	}
	got := w.fresh(t, f.OrgA)
	if kinds(got) != t1.ID+":resolution,"+t1.ID+":response" {
		t.Fatalf("events %s, want response and resolution of %s", kinds(got), t1.ID)
	}
	d := got[0].data
	if d["id"] != t1.ID || d["ticket_number"] != float64(t1.TicketNumber) || d["priority"] != "medium" ||
		d["sla_id"] != w.policies[f.OrgA]["medium"].ID || d["assignee_id"] != assignee || d["team_id"] != team ||
		d["team_lead_id"] != lead || d["client_id"] != f.Client1 || d["due_at"] == "" {
		t.Fatalf("event data %v", d)
	}
	if _, present := d["site_id"]; present {
		t.Errorf("site_id %v of a ticket without site", d["site_id"])
	}
	if n := w.run(t, f.OrgA); n != 0 || len(w.fresh(t, f.OrgA)) != 0 {
		t.Fatalf("second run reported %d", n)
	}
	// new→open (no reopen) leaves the notifications.
	w.patch(t, f.OrgA, t1.ID, map[string]any{"status": "open"})
	if resp, res := w.notified(t, t1.ID); resp == nil || res == nil {
		t.Fatalf("new→open cleared the notifications: %v %v", resp, res)
	}
	if n := w.run(t, f.OrgA); n != 0 {
		t.Fatalf("run after new→open reported %d", n)
	}

	// (2) The read marks the breach first; the run still reports it once.
	t2 := w.create(t, f.OrgA, map[string]any{"title": "read first", "priority": "medium"})
	w.overdue(t, t2.ID, true, false)
	rec := w.request(t, f.OrgA, http.MethodGet, "/api/v1/tickets/"+t2.ID+"/sla", nil)
	var state sla.TicketSLA
	if err := json.Unmarshal(rec.Body.Bytes(), &state); rec.Code != http.StatusOK || err != nil || !state.ResponseBreached {
		t.Fatalf("GET sla: %d %s", rec.Code, rec.Body)
	}
	if n := w.run(t, f.OrgA); n != 1 || kinds(w.fresh(t, f.OrgA)) != t2.ID+":response" {
		t.Fatalf("after the read: %d reported", n)
	}
	if n := w.run(t, f.OrgA); n != 0 {
		t.Fatalf("second run after the read reported %d", n)
	}

	// (3) Reapplication: after the first response only the resolution
	// counts; the new priority computes from the creation three days ago.
	t3 := w.create(t, f.OrgA, map[string]any{"title": "reapply", "priority": "low"})
	w.patch(t, f.OrgA, t3.ID, map[string]any{"status": "in_progress"})
	w.exec(t, `UPDATE ticket SET created_at = now() - interval '3 days' WHERE id = $1`, t3.ID)
	w.overdue(t, t3.ID, false, true)
	if n := w.run(t, f.OrgA); n != 1 || kinds(w.fresh(t, f.OrgA)) != t3.ID+":resolution" {
		t.Fatalf("before the reapplication: %d reported", n)
	}
	w.patch(t, f.OrgA, t3.ID, map[string]any{"priority": "high"})
	var slaID string
	var resolutionDue, created time.Time
	var firstResponse *time.Time
	if err := f.Admin.QueryRow(context.Background(), `SELECT ts.sla_id::text, ts.resolution_due_at, t.created_at, ts.first_response_at
		FROM ticket_sla ts JOIN ticket t ON t.id = ts.ticket_id WHERE ts.ticket_id = $1`, t3.ID).Scan(&slaID, &resolutionDue, &created, &firstResponse); err != nil {
		t.Fatal(err)
	}
	if slaID != w.policies[f.OrgA]["high"].ID || !resolutionDue.Equal(created.Add(60*time.Minute)) || firstResponse == nil {
		t.Fatalf("after PATCH priority: sla %s due %s (created %s) first response %v", slaID, resolutionDue, created, firstResponse)
	}
	if resp, res := w.notified(t, t3.ID); resp != nil || res != nil {
		t.Fatalf("reapplication kept the notifications: %v %v", resp, res)
	}
	if n := w.run(t, f.OrgA); n != 1 || kinds(w.fresh(t, f.OrgA)) != t3.ID+":resolution" {
		t.Fatalf("after the reapplication: %d reported", n)
	}
	if n := w.run(t, f.OrgA); n != 0 {
		t.Fatalf("second run after the reapplication reported %d", n)
	}
	// A policy named with sla_id stays when the priority changes.
	t4 := w.create(t, f.OrgA, map[string]any{"title": "named", "priority": "medium"})
	if rec = w.request(t, f.OrgA, http.MethodPost, "/api/v1/tickets/"+t4.ID+"/sla",
		map[string]any{"sla_id": w.policies[f.OrgA]["low"].ID}); rec.Code != http.StatusOK {
		t.Fatalf("attach named policy: %d %s", rec.Code, rec.Body)
	}
	w.patch(t, f.OrgA, t4.ID, map[string]any{"priority": "high"})
	if state, err := w.repo.GetForTicket(f.OrgCtx(f.OrgA), f.OrgA, t4.ID); err != nil || state.SLAID != w.policies[f.OrgA]["low"].ID {
		t.Fatalf("named policy after PATCH priority: %+v %v", state, err)
	}

	// (4) Reopen: resolved without event, reopened reported once.
	t5 := w.create(t, f.OrgA, map[string]any{"title": "reopen", "priority": "medium"})
	w.patch(t, f.OrgA, t5.ID, map[string]any{"status": "open"})
	w.overdue(t, t5.ID, false, true)
	w.patch(t, f.OrgA, t5.ID, map[string]any{"status": "resolved"})
	if n := w.run(t, f.OrgA); n != 0 {
		t.Fatalf("resolved ticket reported %d", n)
	}
	w.patch(t, f.OrgA, t5.ID, map[string]any{"status": "open"})
	var resolvedAt *time.Time
	var breached bool
	if err := f.Admin.QueryRow(context.Background(), `SELECT resolved_at, resolution_breached FROM ticket_sla WHERE ticket_id = $1`,
		t5.ID).Scan(&resolvedAt, &breached); err != nil || resolvedAt != nil || !breached {
		t.Fatalf("after the reopen: resolved_at %v breached %v err %v", resolvedAt, breached, err)
	}
	if n := w.run(t, f.OrgA); n != 1 || kinds(w.fresh(t, f.OrgA)) != t5.ID+":resolution" {
		t.Fatalf("after the reopen: %d reported", n)
	}

	// (5) Without ticketing_standard: no SLA at creation, no run.
	b1 := w.create(t, f.OrgB, map[string]any{"title": "b standard", "priority": "medium"})
	w.overdue(t, b1.ID, true, true)
	w.features.set(f.OrgB, false)
	b2 := w.create(t, f.OrgB, map[string]any{"title": "b essential", "priority": "medium"})
	var rows int
	if err := f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM ticket_sla WHERE ticket_id = $1`, b2.ID).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("ticket without ticketing_standard has %d SLA rows (%v)", rows, err)
	}
	if n := w.run(t, f.OrgB); n != 0 || len(w.fresh(t, f.OrgB)) != 0 {
		t.Fatalf("unlicensed organization reported %d", n)
	}
	w.features.set(f.OrgB, true)

	// (7) A failing event write rolls the marks back.
	w.exec(t, `CREATE OR REPLACE FUNCTION wp769_refuse() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'wp769: outbox refused'; END $$`)
	w.exec(t, `CREATE TRIGGER wp769_refuse BEFORE INSERT ON event_outbox FOR EACH ROW
		WHEN (NEW.organization_id = '`+f.OrgB+`'::uuid) EXECUTE FUNCTION wp769_refuse()`)
	dropped := false
	drop := func() {
		if !dropped {
			dropped = true
			w.exec(t, `DROP TRIGGER IF EXISTS wp769_refuse ON event_outbox`)
			w.exec(t, `DROP FUNCTION IF EXISTS wp769_refuse()`)
		}
	}
	t.Cleanup(drop)
	if _, err := w.check.RunOrg(context.Background(), f.OrgB); err == nil {
		t.Fatal("run with a refused outbox succeeded")
	}
	if resp, res := w.notified(t, b1.ID); resp != nil || res != nil {
		t.Fatalf("rolled-back run left notifications: %v %v", resp, res)
	}
	drop()
	// Organization A is not touched by B's run; 501 more due rows in B
	// take two batches.
	w.exec(t, `INSERT INTO ticket (organization_id, title, status, priority, category, reporter_id)
		SELECT $1, 'bulk ' || g, 'open', 'medium', 'incident', $2 FROM generate_series(1, 501) g`, f.OrgB, w.users[f.OrgB])
	w.exec(t, `INSERT INTO ticket_sla (organization_id, ticket_id, sla_id, response_due_at, resolution_due_at, first_response_at)
		SELECT t.organization_id, t.id, $2, now() - interval '2 hours', now() - interval '1 hour', now() - interval '3 hours'
		  FROM ticket t WHERE t.organization_id = $1 AND t.title LIKE 'bulk %'`, f.OrgB, w.policies[f.OrgB]["medium"].ID)
	if n := w.run(t, f.OrgB); n != 503 {
		t.Fatalf("organization B reported %d, want 501 resolutions and the two of %s", n, b1.ID)
	}
	gotB := w.fresh(t, f.OrgB)
	perTicket := map[string]int{}
	for _, b := range gotB {
		if b.org != f.OrgB {
			t.Fatalf("event of organization %s in B's run", b.org)
		}
		perTicket[b.entity]++
	}
	if len(gotB) != 503 || len(perTicket) != 502 || perTicket[b1.ID] != 2 {
		t.Fatalf("organization B: %d events over %d tickets", len(gotB), len(perTicket))
	}
	if len(w.fresh(t, f.OrgA)) != 0 {
		t.Fatal("B's run wrote events of organization A")
	}
	if n := w.run(t, f.OrgB); n != 0 {
		t.Fatalf("second run of B reported %d", n)
	}
	// Run walks every organization; both are done.
	if n, err := w.check.Run(context.Background()); err != nil || n != 0 {
		t.Fatalf("Run over every organization: %d %v", n, err)
	}
}

// TestSLABreachNotifiesAssigneeAndLead covers TKT-01 case 6 of WP-769: the
// chain outbox → events.Publisher → notify.EventConsumer → notifier with
// the in-app provider gives the escalation to the assignee and the team
// lead, each with ticket:read in the ticket's client; a lead with
// ticket:read only in another client and a ticket reader who is neither
// get nothing.
func TestSLABreachNotifiesAssigneeAndLead(t *testing.T) {
	w := newBreachWorld(t)
	f := w.f
	bg := context.Background()
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	pii.SetDefault(pii.NewVault(f.App, enc))
	t.Cleanup(func() { pii.SetDefault(nil) })
	center := &notify.Center{Channels: notify.NewPGRepository(f.App, notify.NewSealer(enc)), Inbox: notify.NewPGInbox(f.App),
		Preferences: notify.NewPGPreferences(f.App), Deliveries: notify.NewPGDeliveryStore(f.App)}
	if err = center.Channels.Create(bg, &notify.Channel{OrganizationID: f.OrgA, Name: "center", Provider: notify.ProviderInApp, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	orgRole, clientRole := f.ID(), f.ID()
	w.exec(t, `INSERT INTO role (id, organization_id, name, scope, permissions) VALUES ($1, $2, 'wp769-org', 'org', '["ticket:read"]'::jsonb)`, orgRole, f.OrgA)
	w.exec(t, `INSERT INTO role (id, organization_id, name, scope, permissions) VALUES ($1, $2, 'wp769-client', 'client', '["ticket:read"]'::jsonb)`, clientRole, f.OrgA)
	assign := func(user, role string, client any) {
		t.Helper()
		w.exec(t, `INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id) VALUES ($1, $2, $3, $4)`, f.OrgA, user, role, client)
	}
	assignee := f.AppUser(t, f.OrgA, "ntf-assignee")
	lead := f.AppUser(t, f.OrgA, "ntf-lead")
	foreignLead := f.AppUser(t, f.OrgA, "ntf-foreign-lead")
	reader := f.AppUser(t, f.OrgA, "ntf-reader")
	assign(assignee, orgRole, nil)
	assign(lead, clientRole, f.Client1)
	assign(foreignLead, clientRole, f.Client2)
	assign(reader, orgRole, nil)
	team, foreignTeam := f.ID(), f.ID()
	w.exec(t, `INSERT INTO team (id, organization_id, name, lead_id) VALUES ($1, $2, 'ntf team', $3)`, team, f.OrgA, lead)
	w.exec(t, `INSERT INTO team (id, organization_id, name, lead_id) VALUES ($1, $2, 'ntf foreign team', $3)`, foreignTeam, f.OrgA, foreignLead)
	ci1 := f.CI(t, f.OrgA, f.Client1, "ntf-c1")
	own := w.create(t, f.OrgA, map[string]any{"title": "escalate", "priority": "high", "assignee_id": assignee,
		"team_id": team, "related_ci_id": ci1})
	foreign := w.create(t, f.OrgA, map[string]any{"title": "foreign lead", "priority": "high", "team_id": foreignTeam,
		"related_ci_id": ci1})
	w.overdue(t, own.ID, false, true)
	w.overdue(t, foreign.ID, false, true)
	if n := w.run(t, f.OrgA); n != 2 {
		t.Fatalf("run reported %d, want 2", n)
	}

	notifier := notify.NewNotifier(center.Channels, center.Deliveries, center.Preferences,
		notify.NewSender(map[string]notify.Provider{notify.ProviderInApp: notify.NewInAppProvider(center.Inbox)}))
	publisher := events.NewPublisher(f.App, nil, notify.NewEventConsumer(notify.NewPGResolver(f.App, permission.NewPGRepository(f.App)), notifier))
	if _, err = publisher.PublishPending(bg); err != nil {
		t.Fatal(err)
	}
	if _, err = notifier.ProcessDue(bg); err != nil {
		t.Fatal(err)
	}
	inbox := func(user string) []notify.InAppNotification {
		t.Helper()
		items, _, listErr := center.Inbox.List(bg, f.OrgA, user, false, 20, 0)
		if listErr != nil {
			t.Fatal(listErr)
		}
		return items
	}
	number := "#" + jsonNumber(own.TicketNumber)
	for name, user := range map[string]string{"assignee": assignee, "team lead": lead} {
		items := inbox(user)
		if len(items) != 1 || items[0].Category != "ticket" || !strings.Contains(items[0].Subject, number) {
			t.Errorf("%s: in-app %+v, want the escalation of ticket %s", name, items, number)
		}
	}
	for name, user := range map[string]string{"lead without ticket:read in the client": foreignLead, "other ticket reader": reader} {
		if items := inbox(user); len(items) != 0 {
			t.Errorf("%s got %+v", name, items)
		}
	}
}

func jsonNumber(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
