package notify_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// TestPhaseOneEventsNotify covers NTF-05 and OPS-05 (WP-140) against
// PostgreSQL: each of the phase-1 events — review created, collector
// offline, entitlement warning and expiry, invitation, export completed,
// job failed, webhook dead, certificate expiring — goes from the outbox
// through the publisher to the notifier and reaches its audience; a
// repeated delivery of the event notifies nobody twice; a finally failed
// job reports itself.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestPhaseOneEventsNotify(t *testing.T) {
	f := scopetest.Seed(t, "9c")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	adminRole := f.ID()
	exec(`INSERT INTO role (id, organization_id, name, scope, permissions) VALUES ($1, $2, 'ntf5-admin', 'org', $3::jsonb)`, adminRole, f.OrgA,
		`["user:manage", "webhook:manage", "discovery:manage", "review:resolve", "ci:read"]`)
	admin := f.AppUser(t, f.OrgA, "admin")
	initiator := f.AppUser(t, f.OrgA, "initiator")
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, admin, adminRole)
	ciID := f.CI(t, f.OrgA, f.Client1, "ntf5-cert")
	contact := f.ID()
	exec(`INSERT INTO contact (id, organization_id, client_id, display_name, email) VALUES ($1, $2, $3, 'Cert Owner', 'owner@acme.example')`,
		contact, f.OrgA, f.Client1)
	exec(`INSERT INTO ci_contact (organization_id, ci_id, contact_id, relationship_type, client_id) VALUES ($1, $2, $3, 'owner', $4)`,
		f.OrgA, ciID, contact, f.Client1)

	key, _ := crypto.GenerateMasterKey()
	enc, _ := crypto.NewEnvelopeEncryptor(key)
	// Personal event data travels as vault surrogates (SEC-05).
	pii.SetDefault(pii.NewVault(f.App, enc))
	t.Cleanup(func() { pii.SetDefault(nil) })
	center := &notify.Center{Channels: notify.NewPGRepository(f.App, notify.NewSealer(enc)), Inbox: notify.NewPGInbox(f.App),
		Preferences: notify.NewPGPreferences(f.App), Deliveries: notify.NewPGDeliveryStore(f.App)}
	for _, ch := range []*notify.Channel{
		{OrganizationID: f.OrgA, Name: "mail", Provider: notify.ProviderCentralSMTP, Enabled: true},
		{OrganizationID: f.OrgA, Name: "center", Provider: notify.ProviderInApp, Enabled: true},
	} {
		if err := center.Channels.Create(bg, ch); err != nil {
			t.Fatal(err)
		}
	}
	mail := &recordingProvider{}
	notifier := notify.NewNotifier(center.Channels, center.Deliveries, center.Preferences, notify.NewSender(map[string]notify.Provider{
		notify.ProviderCentralSMTP: mail, notify.ProviderInApp: notify.NewInAppProvider(center.Inbox),
	}))
	consumer := notify.NewEventConsumer(notify.NewPGResolver(f.App, permission.NewPGRepository(f.App)), notifier)
	publisher := events.NewPublisher(f.App, nil, consumer)
	emitter := events.NewPGEmitter(f.App)

	adminMail := "scopetest-9c-admin@scopetest.invalid"
	initiatorMail := "scopetest-9c-initiator@scopetest.invalid"
	cases := []struct {
		typ      string
		data     map[string]any
		category string
		want     []string // e-mail recipients
	}{
		{events.ReviewCreated, map[string]any{"id": "r-1", "kind": "probable_duplicate"}, "review", []string{adminMail}},
		{events.CollectorOffline, map[string]any{"id": "c-1", "name": "dc1-collector", "last_seen_at": "2026-10-13T10:00:00Z"}, "collector", []string{adminMail}},
		{events.EntitlementWarning, map[string]any{"feature": "discovery", "valid_until": "2026-10-20T00:00:00Z", "days_left": 7}, "entitlement", []string{adminMail}},
		{events.EntitlementExpired, map[string]any{"feature": "discovery", "expired_at": "2026-10-01"}, "entitlement", []string{adminMail}},
		{events.InvitationCreated, map[string]any{"email": "new@acme.example", "organization_name": "ACME", "expires_at": "2026-10-20",
			"link": "https://app/invite"}, "invitation", []string{"new@acme.example"}},
		{events.ExportCompleted, map[string]any{"id": "e-1", "format": "csv", "row_count": 12, "initiated_by": initiator}, "export", []string{initiatorMail}},
		{events.JobFailed, map[string]any{"id": "j-1", "kind": "import", "error": "boom", "created_by": initiator}, "job", []string{initiatorMail}},
		{events.WebhookDead, map[string]any{"subscription_id": "s-1", "event": "ci.created", "attempts": 5, "last_error": "502"}, "webhook", []string{adminMail}},
		{events.CertificateExpiring, map[string]any{"ci_id": ciID, "name": "www.acme.example", "expires_at": "2026-11-01",
			"client_id": f.Client1}, "certificate", []string{adminMail, "owner@acme.example"}},
	}
	for _, c := range cases {
		if err := emitter.Emit(bg, &events.Event{OrgID: f.OrgA, Type: c.typ, Data: c.data}); err != nil {
			t.Fatalf("%s: %v", c.typ, err)
		}
	}
	if _, err := publisher.PublishPending(bg); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := notifier.ProcessDue(bg); err != nil {
			t.Fatal(err)
		}
	}
	got := map[string][]string{}
	for _, m := range mail.sent {
		for _, r := range m.To {
			got[m.Category] = append(got[m.Category], r.Email)
		}
	}
	for _, c := range cases {
		var mails []string
		for _, m := range mail.sent {
			if m.Data["event_type"] == c.typ {
				mails = append(mails, m.To[0].Email)
			}
		}
		sort.Strings(mails)
		want := append([]string(nil), c.want...)
		sort.Strings(want)
		if strings.Join(mails, ",") != strings.Join(want, ",") {
			t.Errorf("%s: mail to %v, want %v", c.typ, mails, want)
		}
	}
	// In-app for the users among them.
	items, _, err := center.Inbox.List(bg, f.OrgA, initiator, false, 20, 0)
	if err != nil || len(items) != 2 {
		t.Errorf("in-app of the initiator: %+v %v", items, err)
	}

	// A repeated delivery of the events notifies nobody twice.
	if _, err = f.Admin.Exec(bg, `UPDATE event_outbox SET published_at = NULL WHERE organization_id = $1`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	before := len(mail.sent)
	if _, err = publisher.PublishPending(bg); err != nil {
		t.Fatal(err)
	}
	if _, err = notifier.ProcessDue(bg); err != nil {
		t.Fatal(err)
	}
	if len(mail.sent) != before {
		t.Errorf("repeated events sent %d more mails", len(mail.sent)-before)
	}

	// A producer: the job that fails for good reports job.failed.
	svc := jobs.NewService(jobs.NewPGRepository(f.App)).WithEvents(emitter)
	svc.Register(jobs.Kind{Name: "ntf5.sync", MaxAttempts: 1, Timeout: time.Minute})
	job, _, err := svc.Enqueue(bg, f.OrgA, "ntf5.sync", nil, jobs.EnqueueOptions{CreatedBy: initiator})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ClaimJob(bg, f.OrgA, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Fail(bg, f.OrgA, job.ID, errorString("upstream gone")); err != nil {
		t.Fatal(err)
	}
	var reported int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND type = 'job.failed' AND entity_id = $2`,
		f.OrgA, job.ID).Scan(&reported); err != nil || reported != 1 {
		t.Errorf("job.failed of the failed job: %d %v", reported, err)
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }

// reviewNotifyEnv is the server as cmd/server wires it for review events
// (router with the PostgreSQL event emitter and notification stores, CI
// service with the event outbox) plus the delivery chain outbox →
// publisher → notifier → in-app inbox, and three reviewers of organization
// A: one org-wide, one with a grant on client 1, one with a grant on client
// 2 (WP-549, NTF-05, EVT-01).
type reviewNotifyEnv struct {
	t         *testing.T
	f         *scopetest.Fixture
	router    http.Handler
	center    *notify.Center
	publisher *events.Publisher
	notifier  *notify.Notifier
	user      string
	orgWide   string
	c1, c2    string
}

func newReviewNotifyEnv(t *testing.T) *reviewNotifyEnv {
	t.Helper()
	f := scopetest.Seed(t, "9c")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	e := &reviewNotifyEnv{t: t, f: f}
	orgRole, clientRole := f.ID(), f.ID()
	exec(`INSERT INTO role (id, organization_id, name, scope, permissions) VALUES ($1, $2, 'wp549-org', 'org', '["review:resolve", "ci:read"]'::jsonb),
		($3, $2, 'wp549-client', 'client', '["review:resolve", "ci:read"]'::jsonb)`, orgRole, f.OrgA, clientRole)
	e.user = f.AppUser(t, f.OrgA, "wp549-actor")
	e.orgWide = f.AppUser(t, f.OrgA, "wp549-org")
	e.c1 = f.AppUser(t, f.OrgA, "wp549-c1")
	e.c2 = f.AppUser(t, f.OrgA, "wp549-c2")
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, e.orgWide, orgRole)
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id) VALUES ($1, $2, $3, $4), ($1, $5, $3, $6)`,
		f.OrgA, e.c1, clientRole, f.Client1, e.c2, f.Client2)

	key, _ := crypto.GenerateMasterKey()
	enc, _ := crypto.NewEnvelopeEncryptor(key)
	e.center = &notify.Center{Channels: notify.NewPGRepository(f.App, notify.NewSealer(enc)), Inbox: notify.NewPGInbox(f.App),
		Preferences: notify.NewPGPreferences(f.App), Deliveries: notify.NewPGDeliveryStore(f.App)}
	if err := e.center.Channels.Create(bg, &notify.Channel{OrganizationID: f.OrgA, Name: "center", Provider: notify.ProviderInApp, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	e.notifier = notify.NewNotifier(e.center.Channels, e.center.Deliveries, e.center.Preferences, notify.NewSender(map[string]notify.Provider{
		notify.ProviderInApp: notify.NewInAppProvider(e.center.Inbox),
	}))
	e.publisher = events.NewPublisher(f.App, nil, notify.NewEventConsumer(notify.NewPGResolver(f.App, permission.NewPGRepository(f.App)), e.notifier))

	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{Enforce: false})
	mux, _, err := server.NewRouter(repos, server.Options{
		Version:       "wp549",
		Entitlements:  entitlements,
		CIService:     ci.NewServiceWithLimits(repos.CI, entitlements),
		Credentials:   credential.NewService(repos.Credential, enc),
		OIDC:          identity.NewOIDCProvider(identity.OIDCConfig{}),
		Events:        events.NewPGEmitter(f.App),
		Notifications: e.center,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.router = middleware.TenantMiddleware(mux)
	return e
}

// do sends one request as the org-wide actor of organization A through the
// complete router.
func (e *reviewNotifyEnv) do(method, path string, body any, header map[string]string) (int, string) {
	e.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		e.t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	all := identity.ScopeSet{All: true}
	principal := identity.Principal{Subject: e.user, OrganizationID: e.f.OrgA, Scope: &identity.Scope{Clients: all, Sites: all, Teams: all},
		Type: identity.PrincipalTypeUser, Permissions: identity.AllPermissions(), Credential: "wp549"}
	ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), principal), principal.Permissions)
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req.WithContext(ctx))
	return w.Code, w.Body.String()
}

// deliver publishes the pending events and delivers the notifications.
func (e *reviewNotifyEnv) deliver() {
	e.t.Helper()
	bg := context.Background()
	if _, err := e.publisher.PublishPending(bg); err != nil {
		e.t.Fatal(err)
	}
	for range 3 {
		if _, err := e.notifier.ProcessDue(bg); err != nil {
			e.t.Fatal(err)
		}
	}
}

// notified counts the in-app notifications of user about review item id.
func (e *reviewNotifyEnv) notified(user, id string) int {
	e.t.Helper()
	list, _, err := e.center.Inbox.List(context.Background(), e.f.OrgA, user, false, 100, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	n := 0
	for _, item := range list {
		if item.Category == "review" && item.Data["event_type"] == events.ReviewCreated && item.Data["id"] == id {
			n++
		}
	}
	return n
}

// reviewCreated counts the review.created rows of the outbox for review item
// id and those of organization A in all; data is the event data of the
// item's row when there is exactly one.
func (e *reviewNotifyEnv) reviewCreated(id string) (rows, total int, data map[string]any) {
	e.t.Helper()
	bg := context.Background()
	if err := e.f.Admin.QueryRow(bg, `SELECT count(*) FILTER (WHERE entity_id = $2), count(*) FROM event_outbox
		WHERE organization_id = $1 AND type = 'review.created'`, e.f.OrgA, id).Scan(&rows, &total); err != nil {
		e.t.Fatal(err)
	}
	if rows == 1 {
		if err := e.f.Admin.QueryRow(bg, `SELECT data FROM event_outbox WHERE organization_id = $1 AND type = 'review.created' AND entity_id = $2`,
			e.f.OrgA, id).Scan(&data); err != nil {
			e.t.Fatal(err)
		}
	}
	return rows, total, data
}

// TestReviewCreatedReachesClientReviewers covers WP-549 (NTF-05, EVT-01;
// F-323) against PostgreSQL through the server as cmd/server wires it: a
// review item the ingest opens writes review.created in the transaction
// that inserts it, with the client and site of the item
// (review_item.client_id/site_id). The reviewer with a grant on client 1
// gets an in-app notification, the reviewer with a grant on client 2 none,
// the org-wide reviewer one. Two items: a probable_duplicate whose
// candidate CIs belong to client 1, and an unclassified_device item without
// candidates from the sighting of a client-1 collector (client from the
// collector).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestReviewCreatedReachesClientReviewers|TestMergeConflictReviewNotifies' ./internal/notify/
func TestReviewCreatedReachesClientReviewers(t *testing.T) {
	e := newReviewNotifyEnv(t)
	f := e.f
	bg := context.Background()
	collector1 := ""
	if err := f.Admin.QueryRow(bg, `INSERT INTO collector (organization_id, client_id, name, status) VALUES ($1, $2, 'wp549-collector-c1', 'active')
		RETURNING id::text`, f.OrgA, f.Client1).Scan(&collector1); err != nil {
		t.Fatal(err)
	}
	ingest := func(header map[string]string, recs ...plugins.DeviceRecord) {
		t.Helper()
		if code, body := e.do(http.MethodPost, "/api/v1/ingest/bulk", plugins.DeviceRecordBatch{Records: recs}, header); code != http.StatusAccepted {
			t.Fatalf("ingest: %d %s", code, body)
		}
	}
	itemOf := func(kind, marker string) (id, clientID string, candidates int) {
		t.Helper()
		if err := f.Admin.QueryRow(bg, `SELECT id::text, COALESCE(client_id::text, ''), cardinality(COALESCE(candidate_ci_ids, '{}'))
			FROM review_item WHERE organization_id = $1 AND kind = $2 AND payload::text LIKE '%' || $3 || '%'`, f.OrgA, kind, marker).
			Scan(&id, &clientID, &candidates); err != nil {
			t.Fatalf("%s item of %s: %v", kind, marker, err)
		}
		return id, clientID, candidates
	}

	// probable_duplicate: two devices of the client-1 discovery scope with
	// the same hostname and different serial numbers (CH29).
	first := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: "WP549-DUP-1", IP: "10.54.9.1",
		Manufacturer: "Reticora Labs", Model: "M1", Serial: "WP549-DUP-1"}, time.Now().Add(-2*time.Minute))
	first.Identity.Hostname = "wp549-dup-host"
	first.ClientID = f.Client1
	ingest(nil, first)
	second := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: "WP549-DUP-2", IP: "10.54.9.2",
		Manufacturer: "Reticora Labs", Model: "M1", Serial: "WP549-DUP-2"}, time.Now().Add(-time.Minute))
	second.Identity.Hostname = "wp549-dup-host"
	second.ClientID = f.Client1
	ingest(nil, second)
	dup, dupClient, dupCandidates := itemOf("probable_duplicate", "WP549-DUP-2")
	if dupClient != f.Client1 || dupCandidates == 0 {
		t.Fatalf("setup: probable_duplicate item client %q with %d candidates, want client 1 with candidates", dupClient, dupCandidates)
	}

	// unclassified_device without candidates: a client-1 collector sights a
	// device whose CI type the server does not know; the record carries no
	// client, the item takes the collector's.
	unknown := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "wp549-unknown-type", Name: "wp549-c1-appliance", IP: "10.54.9.3",
		Serial: "WP549-UNCL-1"}, time.Now().Add(-time.Minute))
	ingest(map[string]string{"X-Collector-ID": collector1}, unknown)
	uncl, unclClient, unclCandidates := itemOf("unclassified_device", "WP549-UNCL-1")
	if unclClient != f.Client1 || unclCandidates != 0 {
		t.Fatalf("setup: unclassified_device item client %q with %d candidates, want client 1 without candidates", unclClient, unclCandidates)
	}

	items := map[string]string{"probable_duplicate": dup, "unclassified_device": uncl}
	for name, id := range items {
		rows, _, data := e.reviewCreated(id)
		if rows != 1 || data["client_id"] != f.Client1 {
			t.Errorf("%s: %d review.created rows, data %v; want exactly one with the client_id of client 1", name, rows, data)
		}
	}
	e.deliver()
	for name, id := range items {
		if n := e.notified(e.c1, id); n != 1 {
			t.Errorf("%s: reviewer with a grant on client 1 got %d notifications, want 1 (F-323)", name, n)
		}
		if n := e.notified(e.c2, id); n != 0 {
			t.Errorf("%s: reviewer with a grant on client 2 got %d notifications, want none", name, n)
		}
		if n := e.notified(e.orgWide, id); n != 1 {
			t.Errorf("%s: org-wide reviewer got %d notifications, want 1", name, n)
		}
	}
}

// TestMergeConflictReviewNotifies covers WP-549 (NTF-05, EVT-01; F-324)
// against PostgreSQL: a merge with contradicting overrides (CI-12, REC-12)
// opens an override_conflict review and writes exactly one review.created
// in the merge transaction, which notifies the reviewers of the CI's client;
// a further merge that meets the open conflict review of the same
// object_key writes no second event; a merge whose transaction rolls back
// leaves neither the item nor the event.
func TestMergeConflictReviewNotifies(t *testing.T) {
	e := newReviewNotifyEnv(t)
	f := e.f
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	override := func(ciID, value string) {
		t.Helper()
		exec(`INSERT INTO ci_field_value (organization_id, ci_id, field_name, override_value, override_reason, override_at, protected)
			VALUES ($1, $2, 'model', to_jsonb($3::text), 'wp549', now(), true)`, f.OrgA, ciID, value)
	}
	merge := func(survivor, victim string) {
		t.Helper()
		code, body := e.do(http.MethodPost, "/api/v1/cis/"+survivor+"/merge", map[string]string{"victim_id": victim}, nil)
		var res ci.MergeResult
		if code != http.StatusOK || json.Unmarshal([]byte(body), &res) != nil || len(res.OverrideConflicts) != 1 || res.OverrideConflicts[0] != "model" {
			t.Fatalf("merge %s into %s: %d %s, want 200 with the model conflict", victim, survivor, code, body)
		}
	}
	conflictItem := func(survivor string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `SELECT id::text FROM review_item WHERE organization_id = $1 AND kind = 'override_conflict'
			AND object_key = 'ci:' || $2 || '/model' AND status = 'open'`, f.OrgA, survivor).Scan(&id); err != nil {
			t.Fatalf("open conflict review of %s: %v", survivor, err)
		}
		return id
	}

	survivor := f.CI(t, f.OrgA, f.Client1, "wp549-survivor")
	victim := f.CI(t, f.OrgA, f.Client1, "wp549-victim")
	override(survivor, "model-s")
	override(victim, "model-v")
	merge(survivor, victim)
	item := conflictItem(survivor)
	rows, total, data := e.reviewCreated(item)
	if rows != 1 || total != 1 || data["kind"] != "override_conflict" || data["client_id"] != f.Client1 {
		t.Errorf("merge conflict: %d review.created rows for the item (%d in all), data %v; want exactly one with client 1", rows, total, data)
	}
	e.deliver()
	if n := e.notified(e.c1, item); n != 1 {
		t.Errorf("merge conflict: reviewer with a grant on client 1 got %d notifications, want 1 (F-324)", n)
	}
	if n := e.notified(e.c2, item); n != 0 {
		t.Errorf("merge conflict: reviewer with a grant on client 2 got %d notifications, want none", n)
	}

	// A second victim with another contradicting override meets the open
	// conflict review of the same field: no second item, no second event.
	victim2 := f.CI(t, f.OrgA, f.Client1, "wp549-victim-2")
	override(victim2, "model-v2")
	merge(survivor, victim2)
	if again := conflictItem(survivor); again != item {
		t.Errorf("second conflict: open item %s, want the open item %s", again, item)
	}
	if rows, total, _ = e.reviewCreated(item); rows != 1 || total != 1 {
		t.Errorf("second conflict on the open review: %d review.created rows for the item, %d in all; want 1 and 1", rows, total)
	}

	// Negative: the merge runs in a transaction that rolls back afterwards;
	// the conflict review it opened and its event go with it.
	survivor3 := f.CI(t, f.OrgA, f.Client1, "wp549-survivor-3")
	victim3 := f.CI(t, f.OrgA, f.Client1, "wp549-victim-3")
	override(survivor3, "model-s3")
	override(victim3, "model-v3")
	repo := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox())
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: e.user})
	errRollback := errors.New("roll back the merge")
	var opened []string
	err := database.WithRequestTenantTx(ctx, f.App, f.OrgA, func(ctx context.Context) error {
		res, merr := repo.Merge(ctx, f.OrgA, survivor3, victim3, "")
		if merr != nil {
			return merr
		}
		opened = res.OverrideConflicts
		return errRollback
	})
	if !errors.Is(err, errRollback) || len(opened) != 1 {
		t.Fatalf("merge in the rolled back transaction: %v, conflicts %v; want the conflict and the rollback", err, opened)
	}
	var items int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM review_item WHERE organization_id = $1 AND object_key = 'ci:' || $2 || '/model'`,
		f.OrgA, survivor3).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if _, total, _ = e.reviewCreated(""); items != 0 || total != 1 {
		t.Errorf("rolled back merge: %d conflict items, %d review.created rows in all; want 0 and 1", items, total)
	}
}
