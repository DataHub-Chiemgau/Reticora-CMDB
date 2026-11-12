package maintenance_test

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

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/maintenance"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/topology"
)

// Recipients of a maintenance window over the graph impact (MGT-04, NTF-05
// [P3], WP-1034): a window on a router reaches the client of a CPE behind
// it, heuristic paths only on request, a restricted caller only its
// clients; the notification is announced as maintenance.scheduled and its
// status follows the deliveries.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).

// recipientsEnv is a window on a router of client 1 with a CPE of client 2
// behind it (uplink_to), a monitor of client 3 connected_to the router
// (heuristic) and a server of client 3 without any path to the router.
type recipientsEnv struct {
	f        *scopetest.Fixture
	mux      chi.Router
	user     string
	window   string
	client3  string
	orgWide  database.TenantScope
	client1  database.TenantScope
	client23 database.TenantScope
}

func newRecipientsEnv(t *testing.T) *recipientsEnv {
	t.Helper()
	f := scopetest.Seed(t, "3a")
	bg := context.Background()
	client3 := f.ID()
	if _, err := f.Admin.Exec(bg, `INSERT INTO client (id, organization_id, name, slug) VALUES ($1, $2, 'Client 3', 'scopetest-3a-c3')`,
		client3, f.OrgA); err != nil {
		t.Fatal(err)
	}
	router := f.CI(t, f.OrgA, f.Client1, "rcp-router")
	cpe := f.CI(t, f.OrgA, f.Client2, "rcp-cpe")
	monitor := f.CI(t, f.OrgA, client3, "rcp-monitor")
	f.CI(t, f.OrgA, client3, "rcp-unrelated")
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES
		($1, $2, $3, 'uplink_to'), ($1, $4, $3, 'connected_to')`, f.OrgA, cpe, router, monitor); err != nil {
		t.Fatal(err)
	}
	repo := maintenance.NewPGRepository(f.App)
	start := time.Now().UTC().Add(24 * time.Hour)
	w := &maintenance.Window{OrganizationID: f.OrgA, Title: "Router upgrade", StartsAt: start, EndsAt: start.Add(time.Hour),
		CIIDs: []string{router}}
	if err := repo.Create(f.OrgCtx(f.OrgA), w); err != nil {
		t.Fatal(err)
	}
	mux := chi.NewRouter()
	analyzer := topology.NewAnalyzer(ci.NewPGRepository(f.App), relationship.NewPGRepository(f.App)).
		WithRelationshipTypes(relationshiptype.NewPGRepository(f.App))
	maintenance.NewHandler(repo).WithAnalyzer(analyzer).RegisterRoutes(mux)
	user := f.AppUser(t, f.OrgA, "rcp-user")
	e := &recipientsEnv{f: f, mux: mux, user: user, window: w.ID, client3: client3, orgWide: database.OrgWideScope(f.OrgA, user)}
	e.client1 = database.OrgWideScope(f.OrgA, user)
	e.client1.Clients = database.ScopeIDs(f.Client1)
	e.client23 = database.OrgWideScope(f.OrgA, user)
	e.client23.Clients = database.ScopeIDs(f.Client2, client3)
	return e
}

func (e *recipientsEnv) call(t *testing.T, scope *database.TenantScope, method, path, body string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: e.user})
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req.WithContext(database.ContextWithTenantScope(ctx, scope)))
	return w.Code, w.Body.Bytes()
}

func (e *recipientsEnv) preview(t *testing.T, scope *database.TenantScope, query string) map[string]maintenance.ClientImpact {
	t.Helper()
	code, body := e.call(t, scope, http.MethodGet, "/api/v1/maintenance-windows/"+e.window+"/impact"+query, "")
	if code != http.StatusOK {
		t.Fatalf("impact%s: %d %s", query, code, body)
	}
	var p maintenance.ImpactPreview
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	out := map[string]maintenance.ClientImpact{}
	for _, c := range p.Clients {
		out[c.ClientID] = c
	}
	return out
}

type notifyResult struct {
	Notified      int                        `json:"notified"`
	Queued        int                        `json:"queued"`
	Notifications []maintenance.Notification `json:"notifications"`
	Data          []maintenance.Notification `json:"data"`
}

func (e *recipientsEnv) notify(t *testing.T, scope *database.TenantScope, body string) notifyResult {
	t.Helper()
	code, raw := e.call(t, scope, http.MethodPost, "/api/v1/maintenance-windows/"+e.window+"/notify", body)
	if code != http.StatusOK {
		t.Fatalf("notify: %d %s", code, raw)
	}
	var out notifyResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func byClient(ns []maintenance.Notification) map[string]maintenance.Notification {
	out := map[string]maintenance.Notification{}
	for _, n := range ns {
		out[n.ClientID] = n
	}
	return out
}

// events reads the maintenance.scheduled events of the window.
func (e *recipientsEnv) events(t *testing.T) []map[string]any {
	t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT data FROM event_outbox
		WHERE organization_id = $1 AND type = 'maintenance.scheduled' AND entity_id = $2 ORDER BY sequence`, e.f.OrgA, e.window)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var d map[string]any
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}

func TestRecipientsIncludeIndirectClient(t *testing.T) {
	e := newRecipientsEnv(t)
	got := e.preview(t, &e.orgWide, "")
	if c1, c2 := got[e.f.Client1], got[e.f.Client2]; len(got) != 2 || c1.WindowCIs != 1 || c2.CertainCIs != 1 || c2.WindowCIs != 0 {
		t.Fatalf("preview %+v, want client 1 (window CI) and client 2 (CPE behind the router)", got)
	}
	if _, ok := got[e.client3]; ok {
		t.Error("client 3 without a certain path is a recipient")
	}
	res := e.notify(t, &e.orgWide, `{"contact_roles":["technical"]}`)
	notes := byClient(res.Notifications)
	if len(notes) != 2 || notes[e.f.Client2].Channel != maintenance.ChannelNotify || notes[e.f.Client2].Status != maintenance.NotificationPending {
		t.Fatalf("notifications %+v, want pending notify rows for clients 1 and 2", res.Notifications)
	}
	if res.Notified != 0 || res.Queued != 2 {
		t.Errorf("notified %d, queued %d; want 0 and 2 (nothing delivered yet)", res.Notified, res.Queued)
	}
	evs := e.events(t)
	if len(evs) != 2 {
		t.Fatalf("%d maintenance.scheduled events, want one per client", len(evs))
	}
	for _, d := range evs {
		n, ok := notes[d["client_id"].(string)]
		if !ok || d["maintenance_notification_id"] != n.ID || d["id"] != e.window || d["starts_at"] == nil ||
			d["affected_cis"] != 1.0 || d["title"] != "Router upgrade" {
			t.Errorf("event data %v", d)
		}
		if roles, _ := d["contact_roles"].([]any); len(roles) != 1 || roles[0] != "technical" {
			t.Errorf("event contact roles %v", d["contact_roles"])
		}
	}
}

func TestRecipientsHeuristicOptIn(t *testing.T) {
	e := newRecipientsEnv(t)
	if got := e.preview(t, &e.orgWide, ""); len(got) != 2 {
		t.Errorf("without opt-in: %+v, want clients 1 and 2", got)
	}
	got := e.preview(t, &e.orgWide, "?include_heuristic=true")
	if c3, ok := got[e.client3]; !ok || c3.HeuristicCIs != 1 || c3.CertainCIs != 0 || len(got) != 3 {
		t.Errorf("with opt-in: %+v, want client 3 with one heuristic CI", got)
	}
	// Categories narrow the walk: power only reaches nobody behind the
	// router.
	if got = e.preview(t, &e.orgWide, "?categories=power"); len(got) != 1 {
		t.Errorf("categories=power: %+v, want only the window's client", got)
	}
	code, body := e.call(t, &e.orgWide, http.MethodGet, "/api/v1/maintenance-windows/"+e.window+"/impact?include_heuristic=maybe", "")
	if code != http.StatusUnprocessableEntity || !strings.Contains(string(body), "include_heuristic") {
		t.Errorf("invalid include_heuristic: %d %s, want 422 naming the field", code, body)
	}
	// An unknown category is 422, not a walk that silently reaches nobody.
	code, body = e.call(t, &e.orgWide, http.MethodGet, "/api/v1/maintenance-windows/"+e.window+"/impact?categories=powr", "")
	if code != http.StatusUnprocessableEntity || !strings.Contains(string(body), "categories") {
		t.Errorf("unknown category in the preview: %d %s, want 422 naming the field", code, body)
	}
	code, body = e.call(t, &e.orgWide, http.MethodPost, "/api/v1/maintenance-windows/"+e.window+"/notify", `{"categories":["unknown"]}`)
	if code != http.StatusUnprocessableEntity || !strings.Contains(string(body), "categories") {
		t.Errorf("unknown category in notify: %d %s, want 422 naming the field", code, body)
	}
	if n := len(e.events(t)); n != 0 {
		t.Errorf("%d events after the refused notify, want none", n)
	}
	res := e.notify(t, &e.orgWide, `{"include_heuristic":true}`)
	if notes := byClient(res.Notifications); len(notes) != 3 {
		t.Errorf("notify with opt-in: %+v, want three clients", res.Notifications)
	}
}

func TestRecipientsScopeOfCaller(t *testing.T) {
	e := newRecipientsEnv(t)
	// Client 2 and 3 see the CPE but not the window's router: the window
	// itself is invisible to them, so 404.
	if code, _ := e.call(t, &e.client23, http.MethodGet, "/api/v1/maintenance-windows/"+e.window+"/impact", ""); code != http.StatusNotFound {
		t.Errorf("impact for a caller without the window's CIs: %d, want 404", code)
	}
	got := e.preview(t, &e.client1, "?include_heuristic=true")
	if len(got) != 1 || got[e.f.Client1].WindowCIs != 1 {
		t.Errorf("preview for client 1: %+v, want only client 1", got)
	}
	res := e.notify(t, &e.client1, "")
	if notes := byClient(res.Notifications); len(notes) != 1 || notes[e.f.Client1].ID == "" {
		t.Errorf("notify by client 1: %+v, want only client 1", res.Notifications)
	}
	if evs := e.events(t); len(evs) != 1 || evs[0]["client_id"] != e.f.Client1 {
		t.Errorf("events %v, want one for client 1", evs)
	}
}

// delivery records a delivery of the notification n in status status.
func (e *recipientsEnv) delivery(t *testing.T, n maintenance.Notification, status string) {
	t.Helper()
	bg := context.Background()
	var channel string
	if err := e.f.Admin.QueryRow(bg, `INSERT INTO notification_channel (organization_id, name, kind, provider)
		VALUES ($1, 'rcp-'||gen_random_uuid()::text, 'inapp', 'inapp') RETURNING id::text`, e.f.OrgA).Scan(&channel); err != nil {
		t.Fatal(err)
	}
	if _, err := e.f.Admin.Exec(bg, `INSERT INTO notification_delivery (organization_id, channel_id, recipient_key, category, subject,
		data, status, sent_at) VALUES ($1, $2, gen_random_uuid()::text, 'maintenance', 'x', jsonb_build_object('maintenance_notification_id', $3::text),
		$4, CASE WHEN $4 = 'sent' THEN now() END)`, e.f.OrgA, channel, n.ID, status); err != nil {
		t.Fatal(err)
	}
}

func (e *recipientsEnv) listed(t *testing.T) map[string]maintenance.Notification {
	t.Helper()
	code, raw := e.call(t, &e.orgWide, http.MethodGet, "/api/v1/maintenance-windows/"+e.window+"/notifications", "")
	if code != http.StatusOK {
		t.Fatalf("notifications: %d %s", code, raw)
	}
	var out notifyResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return byClient(out.Data)
}

func TestNotifyStatusFollowsDelivery(t *testing.T) {
	e := newRecipientsEnv(t)
	notes := byClient(e.notify(t, &e.orgWide, "").Notifications)
	c1, c2 := notes[e.f.Client1], notes[e.f.Client2]
	if got := e.listed(t); got[e.f.Client1].Status != maintenance.NotificationPending || got[e.f.Client1].SentAt != nil {
		t.Errorf("before any delivery: %+v, want pending without sent_at", got[e.f.Client1])
	}
	e.delivery(t, c1, "pending")
	if got := e.listed(t); got[e.f.Client1].Status != maintenance.NotificationPending {
		t.Errorf("delivery pending: %+v", got[e.f.Client1])
	}
	e.delivery(t, c1, "sent")
	e.delivery(t, c2, "failed")
	got := e.listed(t)
	if got[e.f.Client1].Status != maintenance.NotificationSent || got[e.f.Client1].SentAt == nil {
		t.Errorf("one delivery sent: %+v, want sent with sent_at", got[e.f.Client1])
	}
	if got[e.f.Client2].Status != maintenance.NotificationFailed || got[e.f.Client2].SentAt != nil {
		t.Errorf("all deliveries failed: %+v, want failed", got[e.f.Client2])
	}
	// The answer of notify reports the same.
	res := e.notify(t, &e.orgWide, "")
	if res.Notified != 1 {
		t.Errorf("notify after the deliveries: notified %d, want 1", res.Notified)
	}
}

// TestNotifyStatusFollowsDigestDelivery: a recipient who bundles the
// category maintenance gets the announcement in a digest (NTF-04); the
// notification is named in the data of a digest entry, and the sent digest
// makes it sent.
func TestNotifyStatusFollowsDigestDelivery(t *testing.T) {
	e := newRecipientsEnv(t)
	notes := byClient(e.notify(t, &e.orgWide, "").Notifications)
	c1 := notes[e.f.Client1]
	bg := context.Background()
	var channel string
	if err := e.f.Admin.QueryRow(bg, `INSERT INTO notification_channel (organization_id, name, kind, provider)
		VALUES ($1, 'rcp-digest', 'inapp', 'inapp') RETURNING id::text`, e.f.OrgA).Scan(&channel); err != nil {
		t.Fatal(err)
	}
	if _, err := e.f.Admin.Exec(bg, `INSERT INTO notification_delivery (organization_id, channel_id, recipient_key, category, subject,
		data, status, sent_at) VALUES ($1, $2, gen_random_uuid()::text, 'maintenance', 'digest',
		jsonb_build_object('digest', true, 'category', 'maintenance', 'count', 2, 'entries', jsonb_build_array(
			jsonb_build_object('subject', 'other', 'data', jsonb_build_object('id', 'x')),
			jsonb_build_object('subject', 'maintenance', 'data', jsonb_build_object('maintenance_notification_id', $3::text)))),
		'sent', now())`, e.f.OrgA, channel, c1.ID); err != nil {
		t.Fatal(err)
	}
	got := e.listed(t)
	if got[e.f.Client1].Status != maintenance.NotificationSent || got[e.f.Client1].SentAt == nil {
		t.Errorf("digest delivery sent: %+v, want sent with sent_at", got[e.f.Client1])
	}
	if got[e.f.Client2].Status != maintenance.NotificationPending {
		t.Errorf("client 2 without a delivery: %+v, want pending", got[e.f.Client2])
	}
}

// TestNotifyConcurrentAnnouncesOnce: parallel notify calls for one window
// announce every client once (one event per client).
func TestNotifyConcurrentAnnouncesOnce(t *testing.T) {
	e := newRecipientsEnv(t)
	const calls = 6
	var wg sync.WaitGroup
	codes := make([]int, calls)
	for i := range calls {
		wg.Go(func() {
			codes[i], _ = e.call(t, &e.orgWide, http.MethodPost, "/api/v1/maintenance-windows/"+e.window+"/notify", "")
		})
	}
	wg.Wait()
	for i, code := range codes {
		if code != http.StatusOK {
			t.Errorf("call %d: %d, want 200", i, code)
		}
	}
	perClient := map[string]int{}
	for _, d := range e.events(t) {
		perClient[d["client_id"].(string)]++
	}
	if len(perClient) != 2 || perClient[e.f.Client1] != 1 || perClient[e.f.Client2] != 1 {
		t.Errorf("events per client %v, want one each for clients 1 and 2", perClient)
	}
}

func TestNotifyAgainOnlyNewOrFailed(t *testing.T) {
	e := newRecipientsEnv(t)
	first := byClient(e.notify(t, &e.orgWide, "").Notifications)
	if len(e.events(t)) != 2 {
		t.Fatalf("first notify: %d events, want 2", len(e.events(t)))
	}
	// Nothing new, nothing failed: no new announcement.
	again := byClient(e.notify(t, &e.orgWide, "").Notifications)
	if len(e.events(t)) != 2 || again[e.f.Client1].ID != first[e.f.Client1].ID {
		t.Errorf("repeated notify announced again (%d events) or changed rows", len(e.events(t)))
	}
	// Client 2's delivery failed: it alone is announced again, on the
	// same notification, pending again.
	e.delivery(t, first[e.f.Client2], "failed")
	// A pre-WP-1034 webhook row of client 3 stays readable.
	if _, err := e.f.Admin.Exec(context.Background(), `INSERT INTO maintenance_notification (organization_id, maintenance_window_id, client_id, channel, status)
		VALUES ($1, $2, $3, 'webhook', 'pending')`, e.f.OrgA, e.window, e.client3); err != nil {
		t.Fatal(err)
	}
	if got := e.listed(t); got[e.f.Client2].Status != maintenance.NotificationFailed {
		t.Fatalf("client 2 before the new announcement: %+v, want failed", got[e.f.Client2])
	}
	third := byClient(e.notify(t, &e.orgWide, "").Notifications)
	evs := e.events(t)
	renewed := third[e.f.Client2]
	if len(evs) != 3 || evs[2]["client_id"] != e.f.Client2 || evs[2]["maintenance_notification_id"] != renewed.ID {
		t.Errorf("events %v, want a third one for client 2 only, naming its new notification", evs)
	}
	// The new announcement has its own id: the failed deliveries of the
	// old one no longer count.
	if renewed.Status != maintenance.NotificationPending || renewed.ID == first[e.f.Client2].ID {
		t.Errorf("client 2 after the new announcement: %+v, want pending under a new id", renewed)
	}
	if got := e.listed(t); got[e.f.Client2].Status != maintenance.NotificationPending || got[e.f.Client2].ID != renewed.ID {
		t.Errorf("listed client 2 after the new announcement: %+v, want pending", got[e.f.Client2])
	}
	// Pending again: a fourth notify announces nothing.
	if fourth := byClient(e.notify(t, &e.orgWide, "").Notifications); fourth[e.f.Client2].ID != renewed.ID {
		t.Errorf("fourth notify changed client 2: %+v", fourth[e.f.Client2])
	}
	if n := len(e.events(t)); n != 3 {
		t.Errorf("%d events after a fourth notify, want still 3", n)
	}
	// A delivery of the new announcement sets its status.
	e.delivery(t, renewed, "sent")
	if got := e.listed(t); got[e.f.Client2].Status != maintenance.NotificationSent {
		t.Errorf("client 2 after the delivery of the new announcement: %+v, want sent", got[e.f.Client2])
	}
	if got := e.listed(t); got[e.client3].Channel != maintenance.ChannelWebhook || got[e.client3].Status != maintenance.NotificationPending {
		t.Errorf("legacy webhook row: %+v", got[e.client3])
	}
}

func TestNotifyUnknownWindow404RepoError500(t *testing.T) {
	e := newRecipientsEnv(t)
	unknown := "00000000-0000-4000-8000-000000000404"
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/maintenance-windows/" + unknown + "/notify"},
		{http.MethodGet, "/api/v1/maintenance-windows/" + unknown + "/impact"},
		{http.MethodGet, "/api/v1/maintenance-windows/" + unknown + "/notifications"},
		{http.MethodGet, "/api/v1/maintenance-windows/" + unknown},
	} {
		if code, body := e.call(t, &e.orgWide, c.method, c.path, ""); code != http.StatusNotFound {
			t.Errorf("%s %s: %d %s, want 404", c.method, c.path, code, body)
		}
	}
	// A repository failure (an id that is no uuid fails in PostgreSQL) is
	// 500, not 404, and names no internals.
	broken := database.OrgWideScope(e.f.OrgA, e.user)
	broken.OrgID = "not-a-uuid"
	code, body := e.call(t, &broken, http.MethodPost, "/api/v1/maintenance-windows/"+e.window+"/notify", "")
	if code != http.StatusInternalServerError || strings.Contains(string(body), "SQLSTATE") {
		t.Errorf("repository failure: %d %s, want 500 without internals", code, body)
	}
}
