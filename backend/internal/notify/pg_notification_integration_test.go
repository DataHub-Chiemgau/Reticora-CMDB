package notify_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

// recordingProvider records the messages it sends.
type recordingProvider struct{ sent []notify.Message }

func (p *recordingProvider) Send(_ context.Context, _ *notify.Channel, msg *notify.Message) (*notify.Delivery, error) {
	p.sent = append(p.sent, *msg)
	return &notify.Delivery{ProviderID: "id-1", Recipients: len(msg.To)}, nil
}

// TestNotificationsAgainstPostgres covers NTF-04 (WP-139) against
// PostgreSQL: deliveries are queued once per dedup key, claimed by one
// worker, sent and recorded; the in-app inbox lists and marks per user;
// preferences round-trip; a delivery left in sending is claimed again
// after the lease.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestNotificationsAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "9b")
	bg := context.Background()
	key, _ := crypto.GenerateMasterKey()
	enc, _ := crypto.NewEnvelopeEncryptor(key)
	center := &notify.Center{
		Channels: notify.NewPGRepository(f.App, notify.NewSealer(enc)), Inbox: notify.NewPGInbox(f.App),
		Preferences: notify.NewPGPreferences(f.App), Deliveries: notify.NewPGDeliveryStore(f.App),
	}
	mailCh := &notify.Channel{OrganizationID: f.OrgA, Name: "mail", Provider: notify.ProviderCentralSMTP, Enabled: true}
	inAppCh := &notify.Channel{OrganizationID: f.OrgA, Name: "center", Provider: notify.ProviderInApp, Enabled: true}
	for _, ch := range []*notify.Channel{mailCh, inAppCh} {
		if err := center.Channels.Create(bg, ch); err != nil {
			t.Fatal(err)
		}
	}
	userA := f.AppUser(t, f.OrgA, "ntf-a")
	userB := f.AppUser(t, f.OrgA, "ntf-b")
	mail := &recordingProvider{}
	now := time.Now().UTC()
	n := notify.NewNotifier(center.Channels, center.Deliveries, center.Preferences, notify.NewSender(map[string]notify.Provider{
		notify.ProviderCentralSMTP: mail, notify.ProviderInApp: notify.NewInAppProvider(center.Inbox),
	})).WithClock(func() time.Time { return now })

	req := &notify.Request{Category: "export", Subject: "Export fertig", Body: "bereit", DedupKey: "export:1",
		Data:       map[string]any{"job": "1"},
		Recipients: []notify.Recipient{{UserID: userA, Email: "a@acme.example"}, {UserID: userB}}}
	if queued, err := n.Notify(bg, f.OrgA, req); err != nil || queued != 3 {
		t.Fatalf("queued %d (%v), want 3", queued, err)
	}
	if queued, _ := n.Notify(bg, f.OrgA, req); queued != 0 {
		t.Errorf("dedup: %d", queued)
	}
	if sent, err := n.ProcessDue(bg); err != nil || sent != 3 {
		t.Fatalf("sent %d (%v)", sent, err)
	}
	if sent, _ := n.ProcessDue(bg); sent != 0 {
		t.Errorf("sent twice: %d", sent)
	}
	if len(mail.sent) != 1 || mail.sent[0].To[0].Email != "a@acme.example" {
		t.Errorf("mail: %+v", mail.sent)
	}
	deliveries, err := center.Deliveries.List(bg, f.OrgA, 10, 0)
	if err != nil || len(deliveries) != 3 {
		t.Fatalf("deliveries: %+v %v", deliveries, err)
	}
	for _, d := range deliveries {
		if d.Status != notify.StatusSent || d.Attempts != 1 || d.SentAt == nil || d.Data["job"] != "1" {
			t.Errorf("delivery: %+v", d)
		}
	}

	items, total, err := center.Inbox.List(bg, f.OrgA, userB, true, 10, 0)
	if err != nil || total != 1 || items[0].Subject != "Export fertig" || items[0].Data["job"] != "1" {
		t.Fatalf("inbox of B: %+v %d %v", items, total, err)
	}
	if err = center.Inbox.MarkRead(bg, f.OrgA, userA, items[0].ID); !errors.Is(err, notify.ErrNotFound) {
		t.Errorf("A marked B's notification: %v", err)
	}
	if err = center.Inbox.MarkRead(bg, f.OrgA, userB, items[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, total, _ = center.Inbox.List(bg, f.OrgA, userB, true, 10, 0); total != 0 {
		t.Errorf("unread after marking: %d", total)
	}
	if marked, _ := center.Inbox.MarkAllRead(bg, f.OrgA, userA); marked != 1 {
		t.Errorf("mark all of A: %d", marked)
	}
	if _, total, _ = center.Inbox.List(bg, f.OrgB, userB, false, 10, 0); total != 0 {
		t.Error("the inbox shows across organizations")
	}

	if p, _ := center.Preferences.Preference(bg, f.OrgA, userA); p != nil {
		t.Errorf("preference before setting: %+v", p)
	}
	if err = center.Preferences.SetPreference(bg, f.OrgA, userA, &notify.Preference{QuietStart: "22:00", QuietEnd: "06:30",
		TimeZone: "Europe/Berlin", MutedCategories: []string{"review"}}); err != nil {
		t.Fatal(err)
	}
	if p, _ := center.Preferences.Preference(bg, f.OrgA, userA); p == nil || p.QuietStart != "22:00" || p.QuietEnd != "06:30" ||
		len(p.MutedCategories) != 1 {
		t.Errorf("preference: %+v", p)
	}

	// A worker died while sending: after the lease another one takes over.
	if _, err = n.Notify(bg, f.OrgA, &notify.Request{Category: "job", Subject: "Job fehlgeschlagen",
		Recipients: []notify.Recipient{{Email: "ops@acme.example"}}}); err != nil {
		t.Fatal(err)
	}
	claimed, err := center.Deliveries.ClaimDue(bg, now, 10, 10*time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	if again, _ := center.Deliveries.ClaimDue(bg, now, 10, 10*time.Minute); len(again) != 0 {
		t.Errorf("claimed twice within the lease: %d", len(again))
	}
	if again, _ := center.Deliveries.ClaimDue(bg, now.Add(time.Hour), 10, 10*time.Minute); len(again) != 1 || again[0].Attempts != 2 {
		t.Errorf("reclaim after the lease: %+v", again)
	}
}
