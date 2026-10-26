package notify_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

// TestDigestBundlesReviews covers NTF-04 (WP-508, F-156) against
// PostgreSQL: five review.created within one hour reach a reviewer with the
// default preference as one digest e-mail with five entries after the
// hour, while the in-app center keeps one notification per event;
// collector.offline (critical) is sent at once; a user who muted reviews
// receives nothing; a user with an empty digest receives each review at
// once; organization B gets its own digest and never an entry of A, and the
// digest items are isolated by RLS; another run, and a repeated event,
// queue no second delivery.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDigestBundlesReviews(t *testing.T) {
	f := scopetest.Seed(t, "9b")
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

	users := map[string]string{}
	for _, u := range []struct{ org, name string }{
		{f.OrgA, "reviewer"}, {f.OrgA, "muted"}, {f.OrgA, "instant"}, {f.OrgB, "breviewer"},
	} {
		role := f.ID()
		exec(`INSERT INTO role (id, organization_id, name, scope, permissions) VALUES ($1, $2, $3, 'org', $4::jsonb)`,
			role, u.org, "ntf4-"+u.name, `["review:resolve", "discovery:manage"]`)
		users[u.name] = f.AppUser(t, u.org, u.name)
		exec(`INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, u.org, users[u.name], role)
	}
	mailOf := func(name string) string { return "scopetest-9b-" + name + "@scopetest.invalid" }

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
	// Opt-out of reviews; the default digest stays.
	if err := center.Preferences.SetPreference(bg, f.OrgA, users["muted"], &notify.Preference{MutedCategories: []string{"review"}}); err != nil {
		t.Fatal(err)
	}
	// An empty digest: every e-mail on its own.
	if err := center.Preferences.SetPreference(bg, f.OrgA, users["instant"], &notify.Preference{Digest: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	if p, err := center.Preferences.Preference(bg, f.OrgA, users["muted"]); err != nil || p.Digest["review"] != notify.DigestHourly {
		t.Fatalf("stored default digest: %+v %v", p, err)
	}

	// A clock in the past: the run never claims deliveries of other tests.
	now := time.Date(2020, 1, 6, 10, 5, 0, 0, time.UTC)
	mail := &recordingProvider{}
	notifier := notify.NewNotifier(center.Channels, center.Deliveries, center.Preferences, notify.NewSender(map[string]notify.Provider{
		notify.ProviderCentralSMTP: mail, notify.ProviderInApp: notify.NewInAppProvider(center.Inbox),
	})).WithClock(func() time.Time { return now }).WithDigest(notify.NewPGDigestStore(f.App))
	consumer := notify.NewEventConsumer(notify.NewPGResolver(f.App, permission.NewPGRepository(f.App)), notifier)
	seq := 0
	emit := func(orgID, typ string, data map[string]any) *events.Envelope {
		t.Helper()
		seq++
		raw, _ := json.Marshal(data)
		env := &events.Envelope{ID: f.ID(), Type: typ, OrgID: orgID, OccurredAt: now, Sequence: int64(seq), Data: raw}
		if err := consumer.Consume(bg, env); err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		return env
	}
	process := func() {
		t.Helper()
		for range 3 {
			if _, err := notifier.ProcessDue(bg); err != nil {
				t.Fatal(err)
			}
		}
	}
	// sentTo returns the mails of category sent to address.
	sentTo := func(address, category string) []notify.Message {
		var out []notify.Message
		for _, m := range mail.sent {
			if m.Category == category && len(m.To) == 1 && m.To[0].Email == address {
				out = append(out, m)
			}
		}
		return out
	}

	var first *events.Envelope
	for i := range 5 {
		now = time.Date(2020, 1, 6, 10, 5+10*i, 0, 0, time.UTC)
		env := emit(f.OrgA, events.ReviewCreated, map[string]any{"id": "r-" + string(rune('1'+i)), "kind": "probable_duplicate"})
		if first == nil {
			first = env
		}
		if i == 1 {
			emit(f.OrgA, events.CollectorOffline, map[string]any{"id": "c-1", "name": "dc1-collector", "last_seen_at": "2020-01-06T10:00:00Z"})
			emit(f.OrgB, events.ReviewCreated, map[string]any{"id": "rb-1", "kind": "probable_duplicate"})
		}
		process()
	}

	// Within the hour: the critical collector.offline and the reviews of
	// the user without a digest are sent, no bundled review.
	if got := sentTo(mailOf("reviewer"), "collector"); len(got) != 1 {
		t.Errorf("collector.offline to the reviewer: %d mails, want 1 at once", len(got))
	}
	if got := sentTo(mailOf("muted"), "collector"); len(got) != 1 {
		t.Errorf("collector.offline to the user who muted reviews: %d mails, want 1 (critical)", len(got))
	}
	if got := sentTo(mailOf("instant"), "review"); len(got) != 5 {
		t.Errorf("reviews to the user with an empty digest: %d mails, want 5 at once", len(got))
	}
	for _, name := range []string{"reviewer", "muted", "breviewer"} {
		if got := sentTo(mailOf(name), "review"); len(got) != 0 {
			t.Errorf("review mails to %s within the window: %d, want 0", name, len(got))
		}
	}
	if queued, err := notifier.RunDigest(bg); err != nil || queued != 0 {
		t.Errorf("digest run within the window: %d (%v), want 0", queued, err)
	}
	// The in-app center stays per event.
	if items, _, err := center.Inbox.List(bg, f.OrgA, users["reviewer"], false, 50, 0); err != nil || countCategory(items, "review") != 5 {
		t.Errorf("in-app reviews of the reviewer: %d (%v), want 5", countCategory(items, "review"), err)
	}

	// RLS: in organization A's scope no digest item of B is visible, and
	// none can be written for B.
	scopeA := database.OrgWideScope(f.OrgA, "")
	err := database.WithTenant(bg, f.App, &scopeA, func(ctx context.Context, tx pgx.Tx) error {
		var foreign, own int
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE organization_id = $1), count(*) FILTER (WHERE organization_id = $2)
			FROM notification_digest_item`, f.OrgB, f.OrgA).Scan(&foreign, &own); err != nil {
			return err
		}
		if foreign != 0 || own != 10 {
			t.Errorf("digest items in A's scope: %d of B, %d of A, want 0 and 10 (reviewer, muted)", foreign, own)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = database.WithTenant(bg, f.App, &scopeA, func(ctx context.Context, tx pgx.Tx) error {
		_, xerr := tx.Exec(ctx, `INSERT INTO notification_digest_item (organization_id, channel_id, recipient_user_id, recipient_email,
				recipient_key, category, digest_window, window_end, subject)
			SELECT $1, channel_id, recipient_user_id, recipient_email, recipient_key, category, digest_window, window_end, 'x'
			FROM notification_digest_item LIMIT 1`, f.OrgB)
		return xerr
	})
	if err == nil || !strings.Contains(err.Error(), "row-level security") {
		t.Errorf("digest item for B written in A's scope: %v, want a row-level security error", err)
	}

	// After the hour: one digest per user, channel, category and window.
	now = time.Date(2020, 1, 6, 11, 0, 30, 0, time.UTC)
	if queued, rerr := notifier.RunDigest(bg); rerr != nil || queued != 3 {
		t.Fatalf("digest run after the window: %d (%v), want 3 (reviewer, muted, B)", queued, rerr)
	}
	process()
	digests := sentTo(mailOf("reviewer"), "review")
	if len(digests) != 1 {
		t.Fatalf("review mails to the reviewer: %d, want 1 digest", len(digests))
	}
	d := digests[0]
	entries, _ := d.Data["entries"].([]any)
	if len(entries) != 5 || d.Data["digest"] != true || d.Data["window"] != notify.DigestHourly ||
		strings.Count(d.Body, "Neues Review-Item") != 10 || !strings.Contains(d.Subject, "5") {
		t.Errorf("digest: subject %q, %d entries, data %v, body %q; want 5 entries listed in German and English",
			d.Subject, len(entries), d.Data, d.Body)
	}
	if got := sentTo(mailOf("muted"), "review"); len(got) != 0 {
		t.Errorf("review mails to the user who muted reviews: %d, want 0", len(got))
	}
	bDigests := sentTo(mailOf("breviewer"), "review")
	if len(bDigests) != 1 {
		t.Fatalf("review mails in B: %d, want 1 digest", len(bDigests))
	}
	if bEntries, _ := bDigests[0].Data["entries"].([]any); len(bEntries) != 1 {
		t.Errorf("digest of B: %d entries, want 1 (its own)", len(bEntries))
	}
	for _, m := range mail.sent {
		if raw, _ := json.Marshal(m.Data); strings.Contains(string(raw), "rb-1") && m.To[0].Email != mailOf("breviewer") {
			t.Errorf("B's review reached %s", m.To[0].Email)
		}
	}
	var muted string
	if err = f.Admin.QueryRow(bg, `SELECT status FROM notification_delivery WHERE organization_id = $1 AND recipient_user_id = $2
		AND category = 'review' AND recipient_key LIKE 'mail:%'`, f.OrgA, users["muted"]).Scan(&muted); err != nil || muted != notify.StatusSuppressed {
		t.Errorf("digest of the user who muted reviews: %q (%v), want suppressed", muted, err)
	}

	// Another run and a repeated event queue nothing twice.
	before := len(mail.sent)
	if queued, rerr := notifier.RunDigest(bg); rerr != nil || queued != 0 {
		t.Errorf("second digest run: %d (%v), want 0", queued, rerr)
	}
	if err = consumer.Consume(bg, first); err != nil {
		t.Fatal(err)
	}
	now = time.Date(2020, 1, 6, 12, 0, 30, 0, time.UTC)
	if queued, rerr := notifier.RunDigest(bg); rerr != nil || queued != 0 {
		t.Errorf("digest run after a repeated event: %d (%v), want 0", queued, rerr)
	}
	process()
	if len(mail.sent) != before {
		t.Errorf("second run sent %d more mails", len(mail.sent)-before)
	}
	var total int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM notification_delivery WHERE organization_id = $1 AND recipient_user_id = $2
		AND category = 'review' AND recipient_key LIKE 'mail:%'`, f.OrgA, users["reviewer"]).Scan(&total); err != nil || total != 1 {
		t.Errorf("review e-mail deliveries of the reviewer: %d (%v), want 1", total, err)
	}
}

func countCategory(items []notify.InAppNotification, category string) int {
	n := 0
	for i := range items {
		if items[i].Category == category {
			n++
		}
	}
	return n
}
