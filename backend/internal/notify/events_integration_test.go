package notify_test

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
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
		{events.EntitlementWarning, map[string]any{"feature": "max_cis", "used": 95, "limit": 100}, "entitlement", []string{adminMail}},
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
