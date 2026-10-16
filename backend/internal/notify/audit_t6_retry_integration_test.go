// Phase-1 acceptance audit, group T6-epic-b – REGRESSION test (must pass).
//
// WP-139 (NTF-04: notification_delivery with status and retries; quiet hours
// per user; acceptance criterion "Notifier-Worker mit Retry, Dedup-Schlüssel,
// Zustellstatus")
// Gap (high) "Retry, backoff and final failure on the PG store (repo test)":
//   - an always-failing provider gives attempts 1..5 with next_attempt_at =
//     clock + 2^n minutes and status failed after the fifth attempt; a failed
//     delivery is not claimed again;
//   - a provider failing once and then succeeding gives sent with attempts 2;
//   - quiet hours on PostgreSQL do not count as an attempt and move
//     next_attempt_at to the end of the window; a critical notification
//     ignores them.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_11?sslmode=disable \
//	  go test -count=1 -v -run 'TestAuditT6NotifierRetry' ./internal/notify/
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

// flakyProvider fails the first failures sends (forever when failures < 0).
type flakyProvider struct {
	failures int
	calls    int
}

func (p *flakyProvider) Send(_ context.Context, _ *notify.Channel, msg *notify.Message) (*notify.Delivery, error) {
	p.calls++
	if p.failures < 0 || p.calls <= p.failures {
		return nil, errors.New("audit-e9: smtp relay unavailable")
	}
	return &notify.Delivery{ProviderID: "audit-e9", Recipients: len(msg.To)}, nil
}

func TestAuditT6NotifierRetryBackoffAndQuietHoursOnPostgres(t *testing.T) {
	f := scopetest.Seed(t, "e9")
	bg := context.Background()
	key, _ := crypto.GenerateMasterKey()
	enc, _ := crypto.NewEnvelopeEncryptor(key)
	channels := notify.NewPGRepository(f.App, notify.NewSealer(enc))
	store := notify.NewPGDeliveryStore(f.App)
	prefs := notify.NewPGPreferences(f.App)
	ch := &notify.Channel{OrganizationID: f.OrgA, Name: "audit-e9-mail", Provider: notify.ProviderCentralSMTP, Enabled: true}
	if err := channels.Create(bg, ch); err != nil {
		t.Fatal(err)
	}
	// A fixed clock in the past keeps deliveries of other tests out of reach.
	start := time.Date(2026, 3, 2, 23, 0, 0, 0, time.UTC)

	type env struct {
		n    *notify.Notifier
		now  *time.Time
		prov *flakyProvider
	}
	newNotifier := func(failures int) env {
		now := start
		prov := &flakyProvider{failures: failures}
		n := notify.NewNotifier(channels, store, prefs, notify.NewSender(map[string]notify.Provider{notify.ProviderCentralSMTP: prov})).
			WithClock(func() time.Time { return now })
		return env{n: n, now: &now, prov: prov}
	}
	delivery := func(subject string) notify.QueuedDelivery {
		t.Helper()
		all, err := store.List(bg, f.OrgA, 200, 0)
		if err != nil {
			t.Fatal(err)
		}
		for i := range all {
			if all[i].Subject == subject {
				return all[i]
			}
		}
		t.Fatalf("no delivery with subject %q", subject)
		return notify.QueuedDelivery{}
	}
	notifyOne := func(e env, req *notify.Request) {
		t.Helper()
		if queued, err := e.n.Notify(bg, f.OrgA, req); err != nil || queued != 1 {
			t.Fatalf("Notify %q queued %d (%v), want 1", req.Subject, queued, err)
		}
	}

	t.Run("always failing: exponential backoff, failed after five attempts", func(t *testing.T) {
		e := newNotifier(-1)
		const subject = "audit-e9-always-failing"
		notifyOne(e, &notify.Request{Category: "job", Subject: subject, DedupKey: "audit-e9:1", Recipients: []notify.Recipient{{Email: "ops@e9.example"}}})
		for attempt := 1; attempt <= 5; attempt++ {
			_, _ = e.n.ProcessDue(bg)
			d := delivery(subject)
			if d.Attempts != attempt || d.LastError == "" {
				t.Fatalf("after attempt %d: attempts %d last_error %q", attempt, d.Attempts, d.LastError)
			}
			if attempt < 5 {
				want := e.now.Add(time.Minute * time.Duration(1<<attempt))
				if d.Status != notify.StatusPending || !d.NextAttemptAt.Equal(want) {
					t.Fatalf("after attempt %d: status %s next_attempt_at %s, want pending at %s", attempt, d.Status, d.NextAttemptAt, want)
				}
				// Not due before the backoff ends.
				*e.now = want.Add(-time.Second)
				_, _ = e.n.ProcessDue(bg)
				if again := delivery(subject); again.Attempts != attempt {
					t.Fatalf("attempted before next_attempt_at: attempts %d", again.Attempts)
				}
				*e.now = want
				continue
			}
			if d.Status != notify.StatusFailed {
				t.Errorf("after the fifth attempt: status %s, want failed", d.Status)
			}
		}
		*e.now = e.now.Add(24 * time.Hour)
		_, _ = e.n.ProcessDue(bg)
		if d := delivery(subject); d.Attempts != 5 || d.Status != notify.StatusFailed {
			t.Errorf("failed delivery attempted again: %+v", d)
		}
		if e.prov.calls != 5 {
			t.Errorf("provider calls = %d, want 5", e.prov.calls)
		}
	})

	t.Run("failing once: sent on the second attempt", func(t *testing.T) {
		e := newNotifier(1)
		const subject = "audit-e9-failing-once"
		notifyOne(e, &notify.Request{Category: "job", Subject: subject, DedupKey: "audit-e9:2", Recipients: []notify.Recipient{{Email: "ops@e9.example"}}})
		_, _ = e.n.ProcessDue(bg)
		if d := delivery(subject); d.Status != notify.StatusPending || d.Attempts != 1 {
			t.Fatalf("after the failed attempt: %+v", d)
		}
		*e.now = e.now.Add(2 * time.Minute)
		if sent, err := e.n.ProcessDue(bg); err != nil || sent < 1 {
			t.Fatalf("second attempt sent %d (%v)", sent, err)
		}
		if d := delivery(subject); d.Status != notify.StatusSent || d.Attempts != 2 || d.SentAt == nil || d.LastError != "" {
			t.Errorf("after the second attempt: %+v", d)
		}
	})

	t.Run("quiet hours defer without an attempt; critical ignores them", func(t *testing.T) {
		user := f.AppUser(t, f.OrgA, "audit-e9-quiet")
		if err := prefs.SetPreference(bg, f.OrgA, user, &notify.Preference{QuietStart: "22:00", QuietEnd: "06:30", TimeZone: "UTC"}); err != nil {
			t.Fatal(err)
		}
		e := newNotifier(0)
		const quiet, critical = "audit-e9-quiet", "audit-e9-critical"
		recipient := []notify.Recipient{{UserID: user, Email: "quiet@e9.example"}}
		notifyOne(e, &notify.Request{Category: "export", Subject: quiet, DedupKey: "audit-e9:3", Recipients: recipient})
		notifyOne(e, &notify.Request{Category: "job", Critical: true, Subject: critical, DedupKey: "audit-e9:4", Recipients: recipient})
		_, _ = e.n.ProcessDue(bg)
		endOfWindow := time.Date(2026, 3, 3, 6, 30, 0, 0, time.UTC)
		if d := delivery(quiet); d.Status != notify.StatusPending || d.Attempts != 0 || !d.NextAttemptAt.Equal(endOfWindow) {
			t.Errorf("during quiet hours: status %s attempts %d next_attempt_at %s, want pending, 0, %s", d.Status, d.Attempts, d.NextAttemptAt, endOfWindow)
		}
		if d := delivery(critical); d.Status != notify.StatusSent || d.Attempts != 1 {
			t.Errorf("critical during quiet hours: status %s attempts %d, want sent, 1", d.Status, d.Attempts)
		}
		*e.now = endOfWindow
		_, _ = e.n.ProcessDue(bg)
		if d := delivery(quiet); d.Status != notify.StatusSent || d.Attempts != 1 {
			t.Errorf("after quiet hours: status %s attempts %d, want sent, 1", d.Status, d.Attempts)
		}
	})
}
