package notify_test

import (
	"context"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

// TestStockBelowMinimumNotifies covers STK-01 and NTF-05 [P2] (WP-845)
// against PostgreSQL: stock.below_minimum goes from the outbox through the
// publisher and the event consumer to the notifier. The users with
// stock:write in the article's client receive it, by e-mail and in-app;
// users of another client, users with a grant for single sites only, users
// with stock:read only and users of another organization do not. An
// article without client reaches the organization-wide grants only. A user
// who muted the category stock receives nothing, a user with a digest for
// stock gets one digest e-mail with both events; a repeated delivery of the
// same event id notifies nobody twice.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStockBelowMinimumNotifies(t *testing.T) {
	f := scopetest.Seed(t, "9c")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	key, _ := crypto.GenerateMasterKey()
	enc, _ := crypto.NewEnvelopeEncryptor(key)
	pii.SetDefault(pii.NewVault(f.App, enc))
	t.Cleanup(func() { pii.SetDefault(nil) })

	role := func(org, name, scope, perms string) string {
		t.Helper()
		id := f.ID()
		exec(`INSERT INTO role (id, organization_id, name, scope, permissions) VALUES ($1, $2, $3, $4, $5::jsonb)`, id, org, name, scope, perms)
		return id
	}
	site := f.ID()
	exec(`INSERT INTO site (id, organization_id, client_id, name) VALUES ($1, $2, $3, 'stk-s1')`, site, f.OrgA, f.Client1)
	orgRole := role(f.OrgA, "stk-org", "org", `["stock:write"]`)
	clientRole := role(f.OrgA, "stk-client", "client", `["stock:write"]`)
	siteRole := role(f.OrgA, "stk-site", "site", `["stock:write"]`)
	readRole := role(f.OrgA, "stk-read", "org", `["stock:read"]`)
	orgRoleB := role(f.OrgB, "stk-org-b", "org", `["stock:write"]`)
	users := map[string]string{}
	for _, u := range []struct {
		org, name, role string
		client, site    any
	}{
		{f.OrgA, "org", orgRole, nil, nil},
		{f.OrgA, "muted", orgRole, nil, nil},
		{f.OrgA, "digest", orgRole, nil, nil},
		{f.OrgA, "c1", clientRole, f.Client1, nil},
		{f.OrgA, "c2", clientRole, f.Client2, nil},
		{f.OrgA, "s1", siteRole, nil, site},
		{f.OrgA, "reader", readRole, nil, nil},
		{f.OrgB, "borg", orgRoleB, nil, nil},
	} {
		users[u.name] = f.AppUser(t, u.org, u.name)
		exec(`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id, scope_site_id) VALUES ($1, $2, $3, $4, $5)`,
			u.org, users[u.name], u.role, u.client, u.site)
	}
	mailOf := func(name string) string { return "scopetest-9c-" + name + "@scopetest.invalid" }

	center := &notify.Center{Channels: notify.NewPGRepository(f.App, notify.NewSealer(enc)), Inbox: notify.NewPGInbox(f.App),
		Preferences: notify.NewPGPreferences(f.App), Deliveries: notify.NewPGDeliveryStore(f.App)}
	for _, ch := range []*notify.Channel{
		{OrganizationID: f.OrgA, Name: "mail", Provider: notify.ProviderCentralSMTP, Enabled: true},
		{OrganizationID: f.OrgA, Name: "center", Provider: notify.ProviderInApp, Enabled: true},
		{OrganizationID: f.OrgB, Name: "mail", Provider: notify.ProviderCentralSMTP, Enabled: true},
	} {
		if err := center.Channels.Create(bg, ch); err != nil {
			t.Fatal(err)
		}
	}
	if err := center.Preferences.SetPreference(bg, f.OrgA, users["muted"], &notify.Preference{MutedCategories: []string{"stock"}}); err != nil {
		t.Fatal(err)
	}
	if err := center.Preferences.SetPreference(bg, f.OrgA, users["digest"],
		&notify.Preference{Digest: map[string]string{"stock": notify.DigestHourly}}); err != nil {
		t.Fatal(err)
	}

	// A clock in the past: the run never claims deliveries of other tests.
	now := time.Date(2020, 1, 6, 10, 5, 0, 0, time.UTC)
	mail := &recordingProvider{}
	notifier := notify.NewNotifier(center.Channels, center.Deliveries, center.Preferences, notify.NewSender(map[string]notify.Provider{
		notify.ProviderCentralSMTP: mail, notify.ProviderInApp: notify.NewInAppProvider(center.Inbox),
	})).WithClock(func() time.Time { return now }).WithDigest(notify.NewPGDigestStore(f.App))
	consumer := notify.NewEventConsumer(notify.NewPGResolver(f.App, permission.NewPGRepository(f.App)), notifier)
	publisher := events.NewPublisher(f.App, nil, consumer)
	emitter := events.NewPGEmitter(f.App)
	process := func() {
		t.Helper()
		if _, err := publisher.PublishPending(bg); err != nil {
			t.Fatal(err)
		}
		for range 3 {
			if _, err := notifier.ProcessDue(bg); err != nil {
				t.Fatal(err)
			}
		}
	}

	// The events as consumable.ApplyBooking writes them (WP-285): one of an
	// article of client 1, one without client, one in organization B.
	inClient := events.StockBelowMinimumData{ID: f.ID(), ClientID: f.Client1, SKU: "TON-415", Name: "Toner HP 415A", Unit: "Stk",
		OnHandTotal: 2, MinLevel: 5}
	orgWide := events.StockBelowMinimumData{ID: f.ID(), Name: "Kabelbinder", Unit: "Pack", OnHandTotal: 0.5, MinLevel: 3}
	inB := events.StockBelowMinimumData{ID: f.ID(), Name: "Papier A4", Unit: "Pack", OnHandTotal: 1, MinLevel: 10}
	for _, ev := range []*events.Event{
		{OrgID: f.OrgA, Type: events.StockBelowMinimum, EntityID: inClient.ID, Data: inClient},
		{OrgID: f.OrgA, Type: events.StockBelowMinimum, EntityID: orgWide.ID, Data: orgWide},
		{OrgID: f.OrgB, Type: events.StockBelowMinimum, EntityID: inB.ID, Data: inB},
	} {
		if err := emitter.Emit(bg, ev); err != nil {
			t.Fatal(err)
		}
	}
	process()

	// mailsOf returns the sorted e-mail recipients of the stock mails of
	// the article id.
	mailsOf := func(id string) []string {
		var out []string
		for _, m := range mail.sent {
			if m.Category == "stock" && m.Data["id"] == id {
				for _, r := range m.To {
					out = append(out, r.Email)
				}
			}
		}
		sort.Strings(out)
		return out
	}
	for name, tc := range map[string]struct {
		id   string
		want []string
	}{
		"article of client 1":    {inClient.ID, []string{mailOf("c1"), mailOf("org")}},
		"article without client": {orgWide.ID, []string{mailOf("org")}},
		"article of B":           {inB.ID, []string{mailOf("borg")}},
	} {
		if got := mailsOf(tc.id); !slices.Equal(got, tc.want) {
			t.Errorf("%s: mail to %v, want %v", name, got, tc.want)
		}
	}
	for _, m := range mail.sent {
		if m.Category != "stock" || m.Data["id"] != inClient.ID || m.To[0].Email != mailOf("c1") {
			continue
		}
		if m.Subject != "[scopetest-9c-a] Mindestbestand unterschritten: Toner HP 415A" ||
			!strings.Contains(m.Body, "TON-415") || !strings.Contains(m.Body, "2 Stk") || !strings.Contains(m.Body, "5 Stk") {
			t.Errorf("mail of the article of client 1: subject %q, body %q", m.Subject, m.Body)
		}
	}

	// In-app: per event, also for the user with a digest, never for the
	// users outside the article's scope.
	for name, want := range map[string]int{"org": 2, "digest": 2, "c1": 1, "c2": 0, "s1": 0, "reader": 0} {
		items, _, err := center.Inbox.List(bg, f.OrgA, users[name], false, 20, 0)
		if err != nil || countCategory(items, "stock") != want {
			t.Errorf("in-app stock notifications of %s: %d (%v), want %d", name, countCategory(items, "stock"), err, want)
		}
	}
	var muted int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM notification_delivery WHERE organization_id = $1 AND recipient_user_id = $2
		AND category = 'stock' AND status <> $3`, f.OrgA, users["muted"], notify.StatusSuppressed).Scan(&muted); err != nil || muted != 0 {
		t.Errorf("unsuppressed stock deliveries of the user who muted stock: %d (%v), want 0", muted, err)
	}

	// After the hour: one digest e-mail with both events of A.
	now = time.Date(2020, 1, 6, 11, 0, 30, 0, time.UTC)
	if queued, err := notifier.RunDigest(bg); err != nil || queued != 1 {
		t.Fatalf("digest run: %d (%v), want 1 (the user with a stock digest)", queued, err)
	}
	process()
	var digests []notify.Message
	for _, m := range mail.sent {
		if m.Category == "stock" && len(m.To) == 1 && m.To[0].Email == mailOf("digest") {
			digests = append(digests, m)
		}
	}
	if len(digests) != 1 {
		t.Fatalf("stock mails to the user with a digest: %d, want 1 digest", len(digests))
	}
	if entries, _ := digests[0].Data["entries"].([]any); len(entries) != 2 || digests[0].Data["digest"] != true ||
		strings.Count(digests[0].Body, "Mindestbestand unterschritten") != 2 {
		t.Errorf("digest: %d entries, data %v, body %q; want both events", len(entries), digests[0].Data, digests[0].Body)
	}

	// A repeated delivery of the events notifies nobody twice.
	exec(`UPDATE event_outbox SET published_at = NULL WHERE organization_id IN ($1, $2) AND type = $3`, f.OrgA, f.OrgB, events.StockBelowMinimum)
	before := len(mail.sent)
	process()
	now = time.Date(2020, 1, 6, 12, 0, 30, 0, time.UTC)
	if queued, err := notifier.RunDigest(bg); err != nil || queued != 0 {
		t.Errorf("digest run after the repeated events: %d (%v), want 0", queued, err)
	}
	process()
	if len(mail.sent) != before {
		t.Errorf("repeated events sent %d more mails", len(mail.sent)-before)
	}
	items, _, err := center.Inbox.List(bg, f.OrgA, users["org"], false, 20, 0)
	if err != nil || countCategory(items, "stock") != 2 {
		t.Errorf("in-app stock notifications of org after the repetition: %d (%v), want 2", countCategory(items, "stock"), err)
	}
}
