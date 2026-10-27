package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
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

// noticeData is the data of a notice as the event consumer reads it from
// the outbox: the JSON object the run wrote.
func noticeData(t *testing.T, notice any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(notice)
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]any{}
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	return data
}

// renderProblems lists what is wrong with a rendered notice: the subject
// does not name subjectWant, the German or the English text does not name
// every value of bodyWant, or a placeholder of a missing or mistyped value
// ("<nil>", "%!") appears.
func renderProblems(subject, body, subjectWant string, bodyWant ...string) []string {
	var problems []string
	for _, bad := range []string{"<nil>", "%!"} {
		if strings.Contains(subject+body, bad) {
			problems = append(problems, "contains "+bad)
		}
	}
	if !strings.Contains(subject, subjectWant) {
		problems = append(problems, "subject does not name "+subjectWant)
	}
	de, en, ok := strings.Cut(body, "\n\n")
	if !ok {
		problems = append(problems, "body has no German and English part")
	}
	for _, want := range bodyWant {
		if !strings.Contains(de, want) || !strings.Contains(en, want) {
			problems = append(problems, "text does not name "+want+" in German and English")
		}
	}
	return problems
}

// TestNoticesOfTheJOB02RunsRender covers WP-425 (NTF-05, ENT-07, COL-04):
// with the payloads of the entitlement.notices and certificate.notices runs
// (jobs.EntitlementNotice and jobs.CertificateNotice, as written in
// TestEntitlementNotices and TestCertificateNotices) subject and text of
// entitlement.warning name feature, valid_until and days_left,
// entitlement.expired feature and expired_at, certificate.expiring the
// certificate's name and end — in German and English, without "<nil>" or
// "%!". The check reports a warning without valid_until or days_left
// (negative cases). Collector certificates and the collector CA reach
// discovery:manage in the collector's client and site, a CI certificate
// still its readers and contacts.
func TestNoticesOfTheJOB02RunsRender(t *testing.T) {
	const validUntil, ended = "2026-11-08T04:13:00Z", "2026-12-18T09:30:00Z"
	for _, tc := range []struct {
		name, typ, subject string
		notice             any
		body               []string
	}{
		{"warning 7 days ahead", events.EntitlementWarning, "discovery",
			&jobs.EntitlementNotice{Feature: "discovery", ValidUntil: validUntil, DaysLeft: 7}, []string{"discovery", validUntil, "7"}},
		{"warning 14 days ahead", events.EntitlementWarning, "webhooks",
			&jobs.EntitlementNotice{Feature: "webhooks", ValidUntil: validUntil, DaysLeft: 14}, []string{"webhooks", validUntil, "14"}},
		{"warning 1 day ahead", events.EntitlementWarning, "discovery",
			&jobs.EntitlementNotice{Feature: "discovery", ValidUntil: validUntil, DaysLeft: 1}, []string{"discovery", validUntil, "1"}},
		{"expired", events.EntitlementExpired, "discovery",
			&jobs.EntitlementNotice{Feature: "discovery", ExpiredAt: validUntil}, []string{"discovery", validUntil}},
		{"collector certificate", events.CertificateExpiring, "dc1-collector",
			&jobs.CertificateNotice{Name: "dc1-collector", ExpiresAt: ended, Fingerprint: "9f86d081884c7d65",
				CollectorID: "c-1", ClientID: "client-1", SiteID: "site-1"}, []string{"dc1-collector", ended}},
		{"collector CA", events.CertificateExpiring, "Reticora Collector CA 2019-10-25 0a1b2c3d",
			&jobs.CertificateNotice{Name: "Reticora Collector CA 2019-10-25 0a1b2c3d", ExpiresAt: ended, Fingerprint: "60303ae22b998861"},
			[]string{"Reticora Collector CA 2019-10-25 0a1b2c3d", ended}},
	} {
		subject, body := Subscriptions[tc.typ].Render(noticeData(t, tc.notice))
		if problems := renderProblems(subject, body, tc.subject, tc.body...); len(problems) > 0 {
			t.Errorf("%s: %v\nsubject %q\nbody %q", tc.name, problems, subject, body)
		}
	}
	for days, want := range map[int][]string{1: {"noch 1 Tag)", "(1 day left)"}, 7: {"noch 7 Tage)", "(7 days left)"}} {
		_, body := Subscriptions[events.EntitlementWarning].Render(noticeData(t,
			&jobs.EntitlementNotice{Feature: "discovery", ValidUntil: validUntil, DaysLeft: days}))
		for _, w := range want {
			if !strings.Contains(body, w) {
				t.Errorf("warning with days_left %d: %q does not contain %q", days, body, w)
			}
		}
	}

	// Negative cases: the check reports a warning without valid_until and
	// one without days_left.
	for name, data := range map[string]map[string]any{
		"without valid_until": noticeData(t, &jobs.EntitlementNotice{Feature: "discovery", DaysLeft: 7}),
		"without days_left":   {"feature": "discovery", "valid_until": validUntil},
	} {
		subject, body := Subscriptions[events.EntitlementWarning].Render(data)
		if problems := renderProblems(subject, body, "discovery", "discovery", validUntil, "7"); len(problems) == 0 {
			t.Errorf("a warning %s passes the check: %q %q", name, subject, body)
		}
	}

	// Audiences.
	for _, tc := range []struct {
		name       string
		typ        string
		data       map[string]any
		permission identity.Permission
		client     string
		site       string
		ci         string
	}{
		{"collector certificate", events.CertificateExpiring, noticeData(t, &jobs.CertificateNotice{Name: "dc1-collector", ExpiresAt: ended,
			Fingerprint: "9f86d081884c7d65", CollectorID: "c-1", ClientID: "client-1", SiteID: "site-1"}),
			identity.PermDiscoveryManage, "client-1", "site-1", ""},
		{"collector CA", events.CertificateExpiring, noticeData(t, &jobs.CertificateNotice{Name: "CA", ExpiresAt: ended, Fingerprint: "60303ae22b998861"}),
			identity.PermDiscoveryManage, "", "", ""},
		{"CI certificate", events.CertificateExpiring, map[string]any{"ci_id": "ci-1", "name": "www.acme.example", "expires_at": ended, "client_id": "client-1"},
			identity.PermCIRead, "client-1", "", "ci-1"},
		{"entitlement warning", events.EntitlementWarning, noticeData(t, &jobs.EntitlementNotice{Feature: "discovery", ValidUntil: validUntil, DaysLeft: 7}),
			identity.PermUserManage, "", "", ""},
	} {
		a := Subscriptions[tc.typ].Audience(tc.data)
		if a.Permission != tc.permission || !a.AllWithPermission || a.ClientID != tc.client || a.SiteID != tc.site || a.CIID != tc.ci {
			t.Errorf("%s: audience %+v, want %s in client %q, site %q, CI %q", tc.name, a, tc.permission, tc.client, tc.site, tc.ci)
		}
	}
}

// emailFeature answers the notifications_email lookup of the notifier and
// records the tenant scope of each lookup.
type emailFeature struct {
	enabled bool
	err     error
	scopes  []string
}

func (e *emailFeature) Check(ctx context.Context, orgID, featureKey string) (entitlement.Entitlement, bool, error) {
	scope, _ := database.TenantScopeFromContext(ctx)
	e.scopes = append(e.scopes, orgID+"|"+scope.OrgID+"|"+featureKey)
	if e.err != nil {
		return entitlement.Entitlement{}, false, e.err
	}
	return entitlement.Entitlement{OrganizationID: orgID, FeatureKey: featureKey, Enabled: e.enabled}, e.enabled, nil
}

// TestNotifierEmailNeedsNotificationsEmail covers WP-471 (ENT-05, F-070):
// the e-mail channels (central_smtp and an own smtp server) send only while
// the organization holds notifications_email. Without it each e-mail
// delivery is skipped with the reason feature-not-entitled, stored on the
// delivery and logged; the in-app delivery of the same request is sent. A
// failed lookup is retried, not skipped. The lookup runs in the scope of
// the delivery's organization (the worker has no request scope).
func TestNotifierEmailNeedsNotificationsEmail(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
	}{{"entitled", true}, {"not entitled", false}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			var logs bytes.Buffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(prev) })

			repo, store, inbox := NewMemoryRepository(), NewMemoryStore(), NewMemoryInbox()
			central, own := &flakyProvider{}, &flakyProvider{}
			centralCh := &Channel{OrganizationID: "org-1", Name: "central", Provider: ProviderCentralSMTP, Enabled: true}
			ownCh := &Channel{OrganizationID: "org-1", Name: "own", Provider: ProviderSMTP, Enabled: true,
				Config: map[string]any{"host": "mail.example.org", "from": "cmdb@example.org"}}
			for _, ch := range []*Channel{centralCh, ownCh,
				{OrganizationID: "org-1", Name: "center", Provider: ProviderInApp, Enabled: true}} {
				if err := repo.Create(ctx, ch); err != nil {
					t.Fatal(err)
				}
			}
			features := &emailFeature{enabled: tc.enabled}
			n := NewNotifier(repo, store, store, NewSender(map[string]Provider{
				ProviderCentralSMTP: central, ProviderSMTP: own, ProviderInApp: NewInAppProvider(inbox)})).
				WithEntitlements(features)
			if queued, err := n.Notify(ctx, "org-1", &Request{Category: "export", Subject: "Export fertig",
				Recipients: []Recipient{{UserID: "u-1", Email: "a@x.example"}}}); err != nil || queued != 3 {
				t.Fatalf("queued %d (%v), want 3", queued, err)
			}
			sent, err := n.ProcessDue(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got := inbox.For("org-1", "u-1"); len(got) != 1 {
				t.Errorf("in-app deliveries: %+v, want 1 regardless of notifications_email", got)
			}
			if len(features.scopes) != 2 {
				t.Errorf("notifications_email lookups: %v, want one per e-mail delivery", features.scopes)
			}
			for _, s := range features.scopes {
				if s != "org-1|org-1|"+entitlement.FeatureNotificationsEmail {
					t.Errorf("lookup %q, want notifications_email in the scope of org-1", s)
				}
			}
			rows, _ := store.List(ctx, "org-1", 10, 0)
			if tc.enabled {
				if sent != 3 || len(central.sent) != 1 || len(own.sent) != 1 {
					t.Errorf("with notifications_email: sent %d, central %d, own smtp %d, want 3, 1, 1", sent, len(central.sent), len(own.sent))
				}
				for _, r := range rows {
					if r.Status != StatusSent {
						t.Errorf("delivery %s over %s: %s %q, want sent", r.ID, r.ChannelID, r.Status, r.LastError)
					}
				}
				if strings.Contains(logs.String(), ReasonFeatureNotEntitled) {
					t.Errorf("skip logged with notifications_email: %s", logs.String())
				}
				return
			}
			if sent != 1 || len(central.sent) != 0 || len(own.sent) != 0 {
				t.Errorf("without notifications_email: sent %d, central %d, own smtp %d, want 1 (in-app), 0, 0", sent, len(central.sent), len(own.sent))
			}
			skipped := 0
			for _, r := range rows {
				switch {
				case r.ChannelID == centralCh.ID || r.ChannelID == ownCh.ID:
					if r.Status != StatusSuppressed || r.LastError != ReasonFeatureNotEntitled {
						t.Errorf("e-mail delivery %s: %s %q, want suppressed feature-not-entitled", r.ID, r.Status, r.LastError)
					}
					skipped++
				case r.Status != StatusSent:
					t.Errorf("in-app delivery %s: %s %q, want sent", r.ID, r.Status, r.LastError)
				}
			}
			if skipped != 2 {
				t.Errorf("skipped e-mail deliveries: %d, want 2", skipped)
			}
			if got := strings.Count(logs.String(), "reason="+ReasonFeatureNotEntitled); got != 2 {
				t.Errorf("logged skips: %d, want 2; log: %s", got, logs.String())
			}

			// A failed lookup is retried later, not skipped for good.
			features.err = errors.New("entitlement store unavailable")
			if _, err = n.Notify(ctx, "org-1", &Request{Category: "export", Subject: "Zweiter Export",
				Recipients: []Recipient{{Email: "b@x.example"}}, ChannelIDs: []string{centralCh.ID}}); err != nil {
				t.Fatal(err)
			}
			_, _ = n.ProcessDue(ctx)
			if last, _ := store.List(ctx, "org-1", 1, 0); last[0].Status != StatusPending || last[0].LastError == ReasonFeatureNotEntitled {
				t.Errorf("failed lookup: %s %q, want pending for a retry", last[0].Status, last[0].LastError)
			}
		})
	}
}

// TestDigestWindowsAndCategories covers the digest rules of NTF-04
// (WP-508): an hourly window ends at the next full hour, a daily one at the
// next midnight in the recipient's time zone; only the categories with a
// non-critical event can be bundled; a request marked critical, an in-app
// or a webhook channel and an address without a user are never bundled.
func TestDigestWindowsAndCategories(t *testing.T) {
	at := time.Date(2026, 3, 28, 23, 30, 0, 0, time.UTC) // 00:30 on the 29th in Berlin (CET)
	if got := windowEnd(DigestHourly, "Europe/Berlin", at); !got.Equal(time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("hourly window end: %v", got)
	}
	// Midnight of the 30th in Berlin is 22:00 UTC (CEST from the 29th).
	if got := windowEnd(DigestDaily, "Europe/Berlin", at); !got.Equal(time.Date(2026, 3, 29, 22, 0, 0, 0, time.UTC)) {
		t.Errorf("daily window end in Berlin: %v", got)
	}
	if got := windowEnd(DigestDaily, "", at); !got.Equal(time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("daily window end without a time zone: %v", got)
	}
	if got := strings.Join(DigestCategories(), ","); got != "entitlement,export,job,review,webhook" {
		t.Errorf("digest categories: %s", got)
	}
	if digestWindow(nil, "review") != DigestHourly || digestWindow(nil, "export") != "" ||
		digestWindow(&Preference{Digest: map[string]string{}}, "review") != "" {
		t.Error("default digest: review hourly only; an empty digest bundles nothing")
	}

	ctx := context.Background()
	n := &Notifier{digest: digestRecorder{}}
	user := Recipient{UserID: "u-1", Email: "a@x.example"}
	for _, tc := range []struct {
		name string
		req  Request
		kind string
		r    Recipient
	}{
		{"critical", Request{Category: "review", Critical: true}, KindEmail, user},
		{"in-app", Request{Category: "review"}, KindInApp, user},
		{"webhook", Request{Category: "review"}, KindWebhook, user},
		{"address without a user", Request{Category: "review"}, KindEmail, Recipient{Email: "a@x.example"}},
	} {
		if window, _, err := n.bundled(ctx, "org-1", &tc.req, tc.kind, tc.r, map[string]*Preference{}); err != nil || window != "" {
			t.Errorf("%s: bundled %q (%v), want delivered on its own", tc.name, window, err)
		}
	}
	if window, _, _ := n.bundled(ctx, "org-1", &Request{Category: "review"}, KindEmail, user, map[string]*Preference{}); window != DigestHourly {
		t.Errorf("review e-mail with the default preference: %q, want hourly", window)
	}
}

// digestRecorder is a DigestStore that keeps nothing.
type digestRecorder struct{}

func (digestRecorder) AddItems(_ context.Context, _ string, items []DigestItem) (int, error) {
	return len(items), nil
}

func (digestRecorder) Bundle(context.Context, time.Time, func([]DigestItem) QueuedDelivery) (int, error) {
	return 0, nil
}
