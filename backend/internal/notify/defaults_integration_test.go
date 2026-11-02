package notify_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/signup"
)

// TestNewOrganizationReceivesNotifications covers WP-509 (NTF-01, NTF-04,
// UI-16; F-157) against PostgreSQL with the application role: an
// organization created through the self-signup has its in-app channel
// without any setup, so export.completed reaches the initiator's
// notification center exactly once; without a central SMTP server it has
// no e-mail channel; with its channel disabled the next export notifies
// nobody; the other organization gets nothing.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL).
func TestNewOrganizationReceivesNotifications(t *testing.T) {
	appDSN := scopetest.AppDSN(t)
	ctx := context.Background()
	app, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	// Closed after the cleanup below, which needs the admin pool.
	t.Cleanup(app.Close)
	admin, err := database.NewMaintenancePool(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)

	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	tag := hex.EncodeToString(suffix)
	var orgs []string
	t.Cleanup(func() {
		for _, id := range orgs {
			_, _ = admin.Exec(context.Background(), `DELETE FROM organization WHERE id = $1`, id)
		}
		_, _ = admin.Exec(context.Background(), `DELETE FROM signup_request WHERE organization_name LIKE $1`, "Default Channels % "+tag)
	})

	// Two organizations through the self-signup, without a central SMTP
	// server: nothing about notifications is set up by hand.
	mailer := &linkMailer{}
	cfg := signup.DefaultConfig()
	cfg.VerifyURL = "https://app.example/signup/verify"
	svc := signup.NewService(signup.NewPGRepository(app), mailer, acceptingAccounts{}, cfg)
	create := func(name, email string) (orgID, userID string) {
		t.Helper()
		if serr := svc.Submit(ctx, signup.Request{OrganizationName: name + " " + tag, Email: email, DisplayName: name}); serr != nil {
			t.Fatalf("Submit %s: %v", name, serr)
		}
		link, perr := url.Parse(mailer.link)
		if perr != nil {
			t.Fatal(perr)
		}
		res, verr := svc.Verify(ctx, signup.Verification{Token: link.Query().Get("token"), Password: "correct horse battery"})
		if verr != nil {
			t.Fatalf("Verify %s: %v", name, verr)
		}
		orgs = append(orgs, res.OrganizationID)
		if qerr := admin.QueryRow(ctx, `SELECT id::text FROM app_user WHERE organization_id = $1`, res.OrganizationID).Scan(&userID); qerr != nil {
			t.Fatalf("first user of %s: %v", name, qerr)
		}
		return res.OrganizationID, userID
	}
	orgA, userA := create("Default Channels A", "ada-"+tag+"@defaults.example")
	orgB, userB := create("Default Channels B", "bob-"+tag+"@defaults.example")

	key, _ := crypto.GenerateMasterKey()
	enc, _ := crypto.NewEnvelopeEncryptor(key)
	pii.SetDefault(pii.NewVault(app, enc))
	t.Cleanup(func() { pii.SetDefault(nil) })
	center := &notify.Center{Channels: notify.NewPGRepository(app, notify.NewSealer(enc)), Inbox: notify.NewPGInbox(app),
		Preferences: notify.NewPGPreferences(app), Deliveries: notify.NewPGDeliveryStore(app)}

	// The default channels: one enabled in-app channel, no e-mail channel
	// without a central SMTP server.
	channels, err := center.Channels.List(ctx, orgA)
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 || channels[0].Kind != notify.KindInApp || channels[0].Provider != notify.ProviderInApp ||
		!channels[0].Enabled {
		t.Fatalf("channels of a new organization = %+v, want one enabled in-app channel", channels)
	}

	mail := &recordingProvider{}
	notifier := notify.NewNotifier(center.Channels, center.Deliveries, center.Preferences, notify.NewSender(map[string]notify.Provider{
		notify.ProviderCentralSMTP: mail, notify.ProviderInApp: notify.NewInAppProvider(center.Inbox),
	}))
	consumer := notify.NewEventConsumer(notify.NewPGResolver(app, permission.NewPGRepository(app)), notifier)
	publisher := events.NewPublisher(app, nil, consumer)
	emitter := events.NewPGEmitter(app)
	exportDone := func(id string) {
		t.Helper()
		if eerr := emitter.Emit(ctx, &events.Event{OrgID: orgA, Type: events.ExportCompleted,
			Data: map[string]any{"id": id, "format": "csv", "row_count": 3, "initiated_by": userA}}); eerr != nil {
			t.Fatal(eerr)
		}
		if _, perr := publisher.PublishPending(ctx); perr != nil {
			t.Fatal(perr)
		}
		for range 3 {
			if _, nerr := notifier.ProcessDue(ctx); nerr != nil {
				t.Fatal(nerr)
			}
		}
	}
	inbox := func(orgID, userID string) []notify.InAppNotification {
		t.Helper()
		items, _, lerr := center.Inbox.List(ctx, orgID, userID, false, 50, 0)
		if lerr != nil {
			t.Fatal(lerr)
		}
		return items
	}

	exportDone("wp509-export-1")
	items := inbox(orgA, userA)
	if len(items) != 1 || items[0].Category != "export" || items[0].Data["id"] != "wp509-export-1" {
		t.Fatalf("inbox of the initiator after export.completed = %+v, want exactly one export notification", items)
	}
	if len(mail.sent) != 0 {
		t.Errorf("mails without an e-mail channel: %d", len(mail.sent))
	}

	// Negative: with the in-app channel disabled the next export reaches
	// nobody.
	ch := channels[0]
	ch.Enabled = false
	if err = center.Channels.Update(ctx, &ch); err != nil {
		t.Fatal(err)
	}
	exportDone("wp509-export-2")
	if items = inbox(orgA, userA); len(items) != 1 {
		t.Errorf("inbox after an export with the channel disabled = %d entries, want 1", len(items))
	}

	// Organization B: its own default channel, nothing of A's events.
	if items = inbox(orgB, userB); len(items) != 0 {
		t.Errorf("inbox of organization B = %+v, want none", items)
	}
	deliveries, err := center.Deliveries.List(ctx, orgB, 50, 0)
	if err != nil || len(deliveries) != 0 {
		t.Errorf("deliveries of organization B = %+v (%v), want none", deliveries, err)
	}
	if channels, err = center.Channels.List(ctx, orgB); err != nil || len(channels) != 1 || channels[0].Kind != notify.KindInApp {
		t.Errorf("channels of organization B = %+v (%v)", channels, err)
	}
}

// linkMailer keeps the verification link of the last signup.
type linkMailer struct{ link string }

func (m *linkMailer) SendSignupVerification(_ context.Context, _, _, link string) error {
	m.link = link
	return nil
}

// acceptingAccounts creates every IdP account.
type acceptingAccounts struct{}

func (acceptingAccounts) CreateAccount(context.Context, signup.Account) (string, error) {
	return "kc-wp509", nil
}
func (acceptingAccounts) DeleteAccount(context.Context, string) error { return nil }
