package notify

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// flakyProvider fails a number of times, then records.
type flakyProvider struct {
	mu    sync.Mutex
	fails int
	sent  []Message
}

func (p *flakyProvider) Send(_ context.Context, _ *Channel, msg *Message) (*Delivery, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fails > 0 {
		p.fails--
		return nil, errors.New("relay unavailable")
	}
	p.sent = append(p.sent, *msg)
	return &Delivery{ProviderID: "m-1", Recipients: len(msg.To)}, nil
}

type notifierEnv struct {
	n       *Notifier
	store   *MemoryStore
	repo    *MemoryRepository
	mail    *flakyProvider
	hook    *flakyProvider
	clock   *time.Time
	mailCh  *Channel
	inAppCh *Channel
	inbox   *MemoryInbox
}

func newNotifierEnv(t *testing.T) *notifierEnv {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC) // 14:00 in Berlin
	env := &notifierEnv{store: NewMemoryStore(), repo: NewMemoryRepository(), mail: &flakyProvider{}, hook: &flakyProvider{},
		clock: &now, inbox: NewMemoryInbox()}
	env.mailCh = &Channel{OrganizationID: "org-1", Name: "mail", Provider: ProviderCentralSMTP, Enabled: true}
	env.inAppCh = &Channel{OrganizationID: "org-1", Name: "center", Provider: ProviderInApp, Enabled: true}
	for _, ch := range []*Channel{env.mailCh, env.inAppCh,
		{OrganizationID: "org-1", Name: "hook", Provider: ProviderWebhook, Enabled: true, Config: map[string]any{"url": "https://h.example"}},
		{OrganizationID: "org-1", Name: "off", Provider: ProviderInApp, Enabled: false},
		{OrganizationID: "org-2", Name: "other", Provider: ProviderInApp, Enabled: true}} {
		if err := env.repo.Create(ctx, ch); err != nil {
			t.Fatal(err)
		}
	}
	sender := NewSender(map[string]Provider{ProviderCentralSMTP: env.mail, ProviderInApp: NewInAppProvider(env.inbox), ProviderWebhook: env.hook})
	env.n = NewNotifier(env.repo, env.store, env.store, sender).WithClock(func() time.Time { return *env.clock })
	env.n.Backoff = func(int) time.Duration { return time.Minute }
	return env
}

func (e *notifierEnv) statuses(t *testing.T) map[string]int {
	t.Helper()
	rows, _ := e.store.List(context.Background(), "org-1", 100, 0)
	out := map[string]int{}
	for _, r := range rows {
		out[r.Status]++
	}
	return out
}

// TestNotifierDeliversWithRetryAndDedup covers NTF-04 and API-05 (WP-139):
// a request is queued per channel and recipient (e-mail with address,
// in-app for users, once per webhook; disabled channels and other
// organizations not), a repeated dedup key queues nothing, a failed
// attempt is retried after the backoff, the last failure is final, and
// each delivery records its status.
func TestNotifierDeliversWithRetryAndDedup(t *testing.T) {
	ctx := context.Background()
	env := newNotifierEnv(t)
	req := &Request{Category: "export", Subject: "Export fertig", Body: "bereit", DedupKey: "export:e-1",
		Recipients: []Recipient{{UserID: "u-1", Email: "a@x.example"}, {UserID: "u-2"}, {Email: "c@x.example"}}}
	queued, err := env.n.Notify(ctx, "org-1", req)
	if err != nil || queued != 5 { // mail a, mail c, in-app u-1, in-app u-2, webhook
		t.Fatalf("queued %d (%v), want 5", queued, err)
	}
	if again, _ := env.n.Notify(ctx, "org-1", req); again != 0 {
		t.Errorf("the same dedup key queued %d more", again)
	}
	if _, err = env.n.Notify(ctx, "org-1", &Request{Subject: "x"}); err == nil {
		t.Error("a request without category was queued")
	}

	env.mail.fails = 1
	sent, err := env.n.ProcessDue(ctx)
	if sent != 4 || err == nil {
		t.Errorf("first pass: sent %d, err %v", sent, err)
	}
	if st := env.statuses(t); st[StatusSent] != 4 || st[StatusPending] != 1 {
		t.Errorf("after the first pass: %v", st)
	}
	// Not due before the backoff.
	if sent, _ = env.n.ProcessDue(ctx); sent != 0 {
		t.Errorf("retried before the backoff: %d", sent)
	}
	*env.clock = env.clock.Add(time.Minute)
	if sent, err = env.n.ProcessDue(ctx); sent != 1 || err != nil {
		t.Errorf("retry: %d %v", sent, err)
	}
	if len(env.mail.sent) != 2 || len(env.hook.sent) != 1 || len(env.hook.sent[0].To) != 3 {
		t.Errorf("mail %d, webhook %+v", len(env.mail.sent), env.hook.sent)
	}
	if got := env.inbox.For("org-1", "u-2"); len(got) != 1 || got[0].Category != "export" {
		t.Errorf("in-app of u-2: %+v", got)
	}

	// The last attempt fails for good.
	env.n.MaxAttempts = 2
	env.mail.fails = 5
	if _, err = env.n.Notify(ctx, "org-1", &Request{Category: "job", Subject: "Job fehlgeschlagen",
		Recipients: []Recipient{{Email: "ops@x.example"}}, ChannelIDs: []string{env.mailCh.ID}}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		_, _ = env.n.ProcessDue(ctx)
		*env.clock = env.clock.Add(time.Minute)
	}
	rows, _ := env.store.List(ctx, "org-1", 1, 0)
	if rows[0].Status != StatusFailed || rows[0].Attempts != 2 || rows[0].LastError == "" {
		t.Errorf("final failure: %+v", rows[0])
	}

	// A channel disabled after queueing suppresses its delivery.
	if _, err = env.n.Notify(ctx, "org-1", &Request{Category: "review", Subject: "x", Recipients: []Recipient{{UserID: "u-1"}},
		ChannelIDs: []string{env.inAppCh.ID}}); err != nil {
		t.Fatal(err)
	}
	env.inAppCh.Enabled = false
	if err = env.repo.Update(ctx, env.inAppCh); err != nil {
		t.Fatal(err)
	}
	_, _ = env.n.ProcessDue(ctx)
	if rows, _ = env.store.List(ctx, "org-1", 1, 0); rows[0].Status != StatusSuppressed {
		t.Errorf("disabled channel: %+v", rows[0])
	}
}

// TestNotifierHonoursPreferences covers the quiet hours and opt-out of
// NTF-04: a muted category is suppressed, a delivery in the quiet hours
// waits until they end, critical notifications go through both.
func TestNotifierHonoursPreferences(t *testing.T) {
	ctx := context.Background()
	env := newNotifierEnv(t)
	if err := env.store.SetPreference(ctx, "org-1", "u-1", &Preference{QuietStart: "13:30", QuietEnd: "15:00",
		TimeZone: "Europe/Berlin", MutedCategories: []string{"review"}}); err != nil {
		t.Fatal(err)
	}
	only := []string{env.inAppCh.ID}
	for _, req := range []*Request{
		{Category: "review", Subject: "muted", Recipients: []Recipient{{UserID: "u-1"}}, ChannelIDs: only},
		{Category: "export", Subject: "quiet", Recipients: []Recipient{{UserID: "u-1"}}, ChannelIDs: only},
		{Category: "review", Subject: "critical", Critical: true, Recipients: []Recipient{{UserID: "u-1"}}, ChannelIDs: only},
		{Category: "export", Subject: "other user", Recipients: []Recipient{{UserID: "u-2"}}, ChannelIDs: only},
	} {
		if _, err := env.n.Notify(ctx, "org-1", req); err != nil {
			t.Fatal(err)
		}
	}
	if sent, _ := env.n.ProcessDue(ctx); sent != 2 {
		t.Errorf("sent during the quiet hours: %d, want critical and the other user", sent)
	}
	if st := env.statuses(t); st[StatusSuppressed] != 1 || st[StatusPending] != 1 {
		t.Errorf("statuses: %v", st)
	}
	*env.clock = time.Date(2026, 10, 13, 13, 0, 0, 0, time.UTC) // 15:00 in Berlin
	if sent, _ := env.n.ProcessDue(ctx); sent != 1 {
		t.Errorf("after the quiet hours: %d", sent)
	}
	rows, _ := env.store.List(ctx, "org-1", 10, 0)
	for _, r := range rows {
		if r.Subject == "quiet" && (r.Status != StatusSent || r.Attempts != 1) {
			t.Errorf("waiting is no attempt: %+v", r)
		}
	}
	if err := (&Preference{QuietStart: "22:00"}).Validate(); err == nil {
		t.Error("quiet hours without end")
	}
	if err := (&Preference{TimeZone: "Mars/Base"}).Validate(); err == nil {
		t.Error("unknown time zone")
	}
	overnight := &Preference{QuietStart: "22:00", QuietEnd: "06:00", TimeZone: "UTC"}
	if until := overnight.QuietUntil(time.Date(2026, 1, 1, 23, 0, 0, 0, time.UTC)); !until.Equal(time.Date(2026, 1, 2, 6, 0, 0, 0, time.UTC)) {
		t.Errorf("overnight quiet hours: %s", until)
	}
	if until := overnight.QuietUntil(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)); !until.IsZero() {
		t.Errorf("noon is not quiet: %s", until)
	}
}

// audienceResolver resolves an audience to its direct recipients and the
// users it names directly.
type audienceResolver struct{}

func (audienceResolver) Resolve(_ context.Context, _ string, a *Audience) ([]Recipient, error) {
	out := append([]Recipient(nil), a.Direct...)
	for _, id := range a.DirectUserIDs {
		out = append(out, Recipient{UserID: id, Email: id + "@x.example"})
	}
	return out, nil
}

// TestInvitationNotificationSkipsWebhookChannels covers WP-421 (NTF-05,
// F-041): the invitation mail carries the acceptance link, so
// invitation.created reaches the invitee's address over the e-mail channel
// only; the organization's webhook channel gets neither a delivery nor the
// link. Another event (export.completed) still reaches the webhook channel.
func TestInvitationNotificationSkipsWebhookChannels(t *testing.T) {
	ctx := context.Background()
	env := newNotifierEnv(t)
	consumer := NewEventConsumer(audienceResolver{}, env.n)
	link := "https://cmdb.example/invitations/accept?token=org-1.secret"
	invitation, _ := json.Marshal(map[string]any{"email": "new@x.example", "organization_name": "ACME",
		"expires_at": "2026-10-20T12:00:00Z", "link": link})
	if err := consumer.Consume(ctx, &events.Envelope{ID: "e-1", OrgID: "org-1", Type: events.InvitationCreated, Data: invitation}); err != nil {
		t.Fatal(err)
	}
	export, _ := json.Marshal(map[string]any{"format": "csv", "row_count": 3, "initiated_by": "u-1"})
	if err := consumer.Consume(ctx, &events.Envelope{ID: "e-2", OrgID: "org-1", Type: events.ExportCompleted, Data: export}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.n.ProcessDue(ctx); err != nil {
		t.Fatal(err)
	}
	var invitationMails []string
	for _, m := range env.mail.sent {
		if m.Category == "invitation" {
			invitationMails = append(invitationMails, m.To[0].Email)
			if !strings.Contains(m.Body, link) {
				t.Errorf("invitation mail without the link: %q", m.Body)
			}
		}
	}
	if len(invitationMails) != 1 || invitationMails[0] != "new@x.example" {
		t.Errorf("invitation mails to %v, want only the invitee", invitationMails)
	}
	if len(env.hook.sent) != 1 || env.hook.sent[0].Category != "export" {
		t.Fatalf("webhook channel deliveries %+v, want only export.completed", env.hook.sent)
	}
	rows, _ := env.store.List(ctx, "org-1", 100, 0)
	for _, r := range rows {
		if r.Category == "invitation" && r.ChannelID != env.mailCh.ID {
			t.Errorf("invitation delivery over channel %s, want the e-mail channel only", r.ChannelID)
		}
	}
}
