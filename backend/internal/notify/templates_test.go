package notify

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
)

// templateData is a payload of each notification event that fills every
// field its texts name.
var templateData = map[string]map[string]any{
	events.ReviewCreated:      {"kind": "probable_duplicate"},
	events.CollectorOffline:   {"name": "dc1-collector", "last_seen_at": "2026-10-13T10:00:00Z"},
	events.EntitlementWarning: {"feature": "discovery", "valid_until": "2026-11-08T04:13:00Z", "days_left": float64(7)},
	events.EntitlementExpired: {"feature": "discovery", "expired_at": "2026-11-08T04:13:00Z"},
	events.InvitationCreated: {"organization_name": "ACME", "expires_at": "2026-10-20T12:00:00Z",
		"link": "https://cmdb.example/invitations/accept?token=t"},
	events.ExportCompleted: {"format": "csv", "row_count": float64(3)},
	events.JobFailed:       {"kind": "export", "error": "disk full"},
	events.WebhookDead: {"event": "ci.updated", "subscription_id": "s-1", "attempts": float64(5),
		"last_error": "timeout"},
	events.CertificateExpiring: {"name": "www.acme.example", "expires_at": "2026-12-18T09:30:00Z"},
	events.APIKeyCreatorDeactivated: {"user_id": "u-9", "keys": []any{map[string]any{"key_prefix": "rk_ab",
		"name": "ci-sync", "service_account_name": "sync", "service_account_id": "sa-1"}}},
}

// germanWords and englishWords are words only a text of that language
// contains; a text naming words of the other language is bilingual.
var (
	germanWords  = []string{" der ", " die ", " das ", " ist ", " wurde ", "Ihr ", "Sie ", " für ", " nicht ", " läuft ", " wartet ", " Zeilen", " bleiben "}
	englishWords = []string{" the ", " has ", " was ", " is ", " your ", "Your ", "You ", " awaits ", " expires ", " rows", " stay ", " failed "}
)

func namesAny(s string, words []string) string {
	s = " " + s + " "
	for _, w := range words {
		if strings.Contains(s, w) {
			return w
		}
	}
	return ""
}

// TestTemplatesCoverEveryEvent covers NTF-03 (WP-588, F-322): every event
// of Subscriptions has a template in de-DE and en-US; each renders a
// subject and a text in its language only — the bilingual text is gone —
// without placeholders of missing values. An unknown locale gets the
// German text.
func TestTemplatesCoverEveryEvent(t *testing.T) {
	for typ := range Subscriptions {
		data, ok := templateData[typ]
		if !ok {
			t.Errorf("%s: no test payload", typ)
			continue
		}
		for _, locale := range []string{LocaleDE, LocaleEN} {
			if _, ok := Templates[typ][locale]; !ok {
				t.Errorf("%s: no %s template", typ, locale)
				continue
			}
			text := Render(typ, locale, data)
			if text.Subject == "" || text.Body == "" {
				t.Errorf("%s in %s: empty text %+v", typ, locale, text)
			}
			if strings.Contains(text.Subject+text.Body, "<nil>") || strings.Contains(text.Subject+text.Body, "%!") {
				t.Errorf("%s in %s: placeholder of a missing value: %+v", typ, locale, text)
			}
			other := englishWords
			if locale == LocaleEN {
				other = germanWords
			}
			if w := namesAny(text.Body, other); w != "" {
				t.Errorf("%s in %s: the text names %q of the other language: %q", typ, locale, w, text.Body)
			}
		}
		de, en := Render(typ, LocaleDE, data), Render(typ, LocaleEN, data)
		if de.Body == en.Body || strings.Contains(de.Body, en.Body) || strings.Contains(en.Body, de.Body) {
			t.Errorf("%s: German and English text are not distinct: %q / %q", typ, de.Body, en.Body)
		}
		if fr := Render(typ, "fr-FR", data); fr != de {
			t.Errorf("%s: an unknown locale renders %+v, want the German text", typ, fr)
		}
	}
	for typ := range Templates {
		if _, ok := Subscriptions[typ]; !ok {
			t.Errorf("template of %s, which no subscription notifies", typ)
		}
	}
	// The texts are per language: a review in English names it in English.
	if got := Render(events.ReviewCreated, LocaleEN, templateData[events.ReviewCreated]).Subject; got != "New review item: probable_duplicate" {
		t.Errorf("English review subject %q", got)
	}
	if got := Render(events.ReviewCreated, LocaleDE, templateData[events.ReviewCreated]).Subject; got != "Neues Review-Item: probable_duplicate" {
		t.Errorf("German review subject %q", got)
	}
}

// TestTemplatesLocale covers the language of a recipient (NTF-03): the
// user's locale (app_user.locale), else the organization's (LOC-01), else
// de-DE. The column default "de" of every account counts as no choice.
func TestTemplatesLocale(t *testing.T) {
	for _, tc := range []struct{ user, org, want string }{
		{"en-US", "de-DE", LocaleEN},
		{"de-DE", "en-US", LocaleDE},
		{"en-GB", "", LocaleEN},
		{"en_US", "de-DE", LocaleEN},
		{"DE-at", "en-US", LocaleDE},
		{"", "en-US", LocaleEN},
		{"de", "en-US", LocaleEN},
		{"de", "", LocaleDE},
		{"fr-FR", "en-GB", LocaleEN},
		{"fr-FR", "it-IT", LocaleDE},
		{"", "", LocaleDE},
	} {
		if got := TemplateLocale(tc.user, tc.org); got != tc.want {
			t.Errorf("user %q, organization %q: %s, want %s", tc.user, tc.org, got, tc.want)
		}
	}
}

// TestTemplatesBranding covers the organization's branding of e-mails
// (NTF-03, E-91 option 2), in each language: the sender name — the
// organization's name or settings.branding.sender_name — as subject
// prefix, the organization's name in a footer in the recipient's language.
// Names go into mail headers: line breaks and control characters are
// removed, the length is bounded. Without branding the text stays.
func TestTemplatesBranding(t *testing.T) {
	text := Text{Subject: "Neues Review-Item: x", Body: "Ein neues Review-Item (x) wartet auf eine Entscheidung."}
	brand := newBranding("ACME GmbH", "de-DE", "")
	if brand.SenderName != "ACME GmbH" {
		t.Errorf("sender name without settings.branding: %q, want the organization's name", brand.SenderName)
	}
	de := brand.Mail(LocaleDE, text)
	if de.Subject != "[ACME GmbH] Neues Review-Item: x" || !strings.HasPrefix(de.Body, text.Body+"\n\n-- \n") ||
		!strings.HasSuffix(de.Body, "Diese Benachrichtigung wurde von Reticora im Auftrag von ACME GmbH versendet.") {
		t.Errorf("German branding: %+v", de)
	}
	en := newBranding("ACME GmbH", "en-US", "ACME Service Desk").Mail(LocaleEN,
		Text{Subject: "New review item: x", Body: "A new review item (x) awaits a decision."})
	if en.Subject != "[ACME Service Desk] New review item: x" ||
		!strings.HasSuffix(en.Body, "\n\n-- \nThis notification was sent by Reticora on behalf of ACME GmbH.") {
		t.Errorf("English branding with settings.branding.sender_name: %+v", en)
	}
	if got := (*Branding)(nil).Mail(LocaleDE, text); got != text {
		t.Errorf("without branding: %+v, want the text unchanged", got)
	}
	hostile := newBranding("ACME\r\nBcc: x@evil.example", "de-DE", "ACME\tIT\x00\nX-Header: 1")
	if strings.ContainsAny(hostile.SenderName+hostile.OrganizationName, "\r\n\t\x00") ||
		hostile.SenderName != "ACME IT X-Header: 1" || hostile.OrganizationName != "ACME Bcc: x@evil.example" {
		t.Errorf("names with control characters: %q / %q", hostile.SenderName, hostile.OrganizationName)
	}
	if long := newBranding(strings.Repeat("ä", 300), "", ""); len([]rune(long.SenderName)) != 100 {
		t.Errorf("long name: %d characters, want 100", len([]rune(long.SenderName)))
	}
}

// brandedResolver resolves every audience to fixed recipients and brands
// them as an organization with locale en-US.
type brandedResolver struct {
	recipients []Recipient
	brand      *Branding
}

func (r brandedResolver) Resolve(context.Context, string, *Audience) ([]Recipient, error) {
	return r.recipients, nil
}

func (r brandedResolver) Branding(context.Context, string) (*Branding, error) { return r.brand, nil }

// TestTemplatesPerRecipient covers NTF-03 (WP-588) for the event consumer:
// a user with locale en-US receives the English text, one with de-DE the
// German, a user with the account default and an address without an
// account (a contact) the organization's (en-US). E-mails carry the
// subject prefix and the footer and the sender name reaches the e-mail
// provider; the in-app center gets the plain text in the user's language,
// the webhook channel the organization's text, both unbranded.
func TestTemplatesPerRecipient(t *testing.T) {
	ctx := context.Background()
	env := newNotifierEnv(t)
	brand := newBranding("ACME GmbH", "en-US", "")
	consumer := NewEventConsumer(brandedResolver{brand: brand, recipients: []Recipient{
		{UserID: "u-en", Email: "en@x.example", Locale: "en-US"},
		{UserID: "u-de", Email: "de@x.example", Locale: "de-DE"},
		{UserID: "u-default", Email: "default@x.example", Locale: "de"},
		{Email: "contact@x.example"},
	}}, env.n)
	raw, _ := json.Marshal(map[string]any{"kind": "probable_duplicate"})
	if err := consumer.Consume(ctx, &events.Envelope{ID: "e-1", OrgID: "org-1", Type: events.ReviewCreated, Data: raw}); err != nil {
		t.Fatal(err)
	}
	// The memory store has no branding of the organization: the sender
	// name is set as the PostgreSQL store does when claiming.
	rows, _ := env.store.List(ctx, "org-1", 100, 0)
	for _, r := range rows {
		r.SenderName = brand.SenderName
		if err := env.store.Finish(ctx, &r); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := env.n.ProcessDue(ctx); err != nil {
		t.Fatal(err)
	}
	const en, de = "New review item: probable_duplicate", "Neues Review-Item: probable_duplicate"
	want := map[string]string{"en@x.example": en, "de@x.example": de, "default@x.example": en, "contact@x.example": en}
	if len(env.mail.sent) != len(want) {
		t.Fatalf("mails: %d, want %d", len(env.mail.sent), len(want))
	}
	for _, m := range env.mail.sent {
		subject := want[m.To[0].Email]
		if m.Subject != "[ACME GmbH] "+subject {
			t.Errorf("mail to %s: subject %q, want %q", m.To[0].Email, m.Subject, "[ACME GmbH] "+subject)
		}
		text, footer := "A new review item (probable_duplicate) awaits a decision.", "This notification was sent by Reticora on behalf of ACME GmbH."
		if subject == de {
			text, footer = "Ein neues Review-Item (probable_duplicate) wartet auf eine Entscheidung.",
				"Diese Benachrichtigung wurde von Reticora im Auftrag von ACME GmbH versendet."
		}
		if m.Body != text+"\n\n-- \n"+footer {
			t.Errorf("mail to %s: body %q, want %q with the footer %q", m.To[0].Email, m.Body, text, footer)
		}
		if m.SenderName != "ACME GmbH" {
			t.Errorf("mail to %s: sender name %q, want ACME GmbH", m.To[0].Email, m.SenderName)
		}
	}
	for user, subject := range map[string]string{"u-en": en, "u-de": de, "u-default": en} {
		items := env.inbox.For("org-1", user)
		if len(items) != 1 || items[0].Subject != subject || strings.Contains(items[0].Body, "ACME") {
			t.Errorf("in-app of %s: %+v, want %q unbranded", user, items, subject)
		}
	}
	if len(env.hook.sent) != 1 || env.hook.sent[0].Subject != en || strings.Contains(env.hook.sent[0].Body, "ACME") {
		t.Fatalf("webhook: %+v, want the organization's text unbranded", env.hook.sent)
	}
	// The locale and the sender name never reach a webhook payload.
	payload, _ := json.Marshal(env.hook.sent[0])
	if strings.Contains(string(payload), "en-US") || strings.Contains(string(payload), "sender") {
		t.Errorf("webhook payload names locale or sender: %s", payload)
	}
}

// TestTemplatesDigest covers the digest of NTF-04 in the recipient's
// language (NTF-03): an English user's digest is English, a German user's
// German, each branded; neither is bilingual.
func TestTemplatesDigest(t *testing.T) {
	n := NewNotifier(nil, nil, nil, nil)
	brand := newBranding("ACME GmbH", "de-DE", "")
	end := time.Date(2026, 10, 13, 13, 0, 0, 0, time.UTC)
	item := func(locale, subject string) DigestItem {
		return DigestItem{OrganizationID: "org-1", ChannelID: "c-1", Recipient: Recipient{UserID: "u-1", Email: "u@x.example", Locale: locale},
			Category: "review", Window: DigestHourly, WindowEnd: end, Subject: subject, CreatedAt: end.Add(-time.Minute), Branding: brand}
	}
	en := n.buildDigest([]DigestItem{item("en-US", "New review item: a"), item("en-US", "New review item: b")})
	if en.Subject != "[ACME GmbH] Digest review: 2 notifications" || !strings.Contains(en.Body, "2 notifications (review) of the last hour:") ||
		strings.Contains(en.Body, "Benachrichtigungen") || !strings.HasSuffix(en.Body, "on behalf of ACME GmbH.") {
		t.Errorf("English digest: %q\n%s", en.Subject, en.Body)
	}
	de := n.buildDigest([]DigestItem{item("", "Neues Review-Item: a")})
	if de.Subject != "[ACME GmbH] Zusammenfassung review: 1 Benachrichtigungen" ||
		!strings.Contains(de.Body, "1 Benachrichtigungen (review) der letzten Stunde:") || strings.Contains(de.Body, "notifications") ||
		!strings.HasSuffix(de.Body, "im Auftrag von ACME GmbH versendet.") {
		t.Errorf("German digest: %q\n%s", de.Subject, de.Body)
	}
}

// TestTemplatesSubscriptionsPreference covers the category subscriptions
// of NTF-03 (E-91 option 2) in the NTF-04 preference: every category is
// subscribed by default; muting one through subscriptions or
// muted_categories shows it as muted; subscribing again removes it from
// the muted categories; an unknown category or state is refused.
func TestTemplatesSubscriptionsPreference(t *testing.T) {
	p := &Preference{}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(p.Subscriptions) != len(Categories()) {
		t.Fatalf("default subscriptions %v, want every category of %v", p.Subscriptions, Categories())
	}
	for category, state := range p.Subscriptions {
		if state != SubscriptionSubscribed {
			t.Errorf("default subscription of %s: %s, want subscribed", category, state)
		}
	}
	p = &Preference{MutedCategories: []string{"job"}, Subscriptions: map[string]string{"review": SubscriptionMuted, "job": SubscriptionSubscribed}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.MutedCategories, ",") != "review" || p.Subscriptions["review"] != SubscriptionMuted ||
		p.Subscriptions["job"] != SubscriptionSubscribed || p.Subscriptions["export"] != SubscriptionSubscribed {
		t.Errorf("after subscriptions: muted %v, subscriptions %v", p.MutedCategories, p.Subscriptions)
	}
	if err := p.Validate(); err != nil || strings.Join(p.MutedCategories, ",") != "review" {
		t.Errorf("validating twice: %v, muted %v", err, p.MutedCategories)
	}
	for name, subs := range map[string]map[string]string{
		"unknown category": {"newsletter": SubscriptionMuted},
		"unknown state":    {"review": "sometimes"},
	} {
		if err := (&Preference{Subscriptions: subs}).Validate(); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: %v, want a validation error", name, err)
		}
	}
	// A muted category is not delivered, a critical notification of it is.
	env := newNotifierEnv(t)
	ctx := context.Background()
	if err := env.store.SetPreference(ctx, "org-1", "u-1", &Preference{Subscriptions: map[string]string{"collector": SubscriptionMuted,
		"review": SubscriptionMuted}}); err != nil {
		t.Fatal(err)
	}
	to := []Recipient{{UserID: "u-1", Email: "u1@x.example"}}
	for _, req := range []*Request{
		{Category: "review", Subject: "review", DedupKey: "r", Recipients: to, ChannelIDs: []string{env.mailCh.ID}},
		{Category: "collector", Critical: true, Subject: "collector", DedupKey: "c", Recipients: to, ChannelIDs: []string{env.mailCh.ID}},
	} {
		if _, err := env.n.Notify(ctx, "org-1", req); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := env.n.ProcessDue(ctx); err != nil {
		t.Fatal(err)
	}
	if len(env.mail.sent) != 1 || env.mail.sent[0].Subject != "collector" {
		t.Errorf("mails %+v, want only the critical collector notification", env.mail.sent)
	}
}

// TestTemplatesSenderName covers the sender name of NTF-03 at the e-mail
// providers: the central server sends with the organization's sender name
// unless the channel names one; the organization's own server keeps the
// display name of its sender address and uses the organization's only for
// an address without one.
func TestTemplatesSenderName(t *testing.T) {
	ctx := context.Background()
	srv := newFakeSMTP(t, false)
	central, err := NewSMTPSender(&SMTPConfig{Host: "127.0.0.1", Port: srv.port(), TLS: SMTPNone, From: "noreply@reticora.example"})
	if err != nil {
		t.Fatal(err)
	}
	own := func(from string) *Channel {
		return &Channel{ID: "c1", OrganizationID: "org-1", Name: "own", Provider: ProviderSMTP, Enabled: true,
			Config: map[string]any{"host": "127.0.0.1", "port": float64(srv.port()), "tls": "none", "from": from}}
	}
	sender := NewSender(map[string]Provider{ProviderSMTP: NewEmailProvider(central).WithEgress(egress.Options{AllowPrivate: true}),
		ProviderCentralSMTP: NewEmailProvider(central)})
	msg := &Message{Category: "export", Subject: "s", Body: "b", To: []Recipient{{Email: "ops@acme.example"}}, SenderName: "ACME GmbH"}
	for _, ch := range []*Channel{
		{OrganizationID: "org-1", Name: "central", Provider: ProviderCentralSMTP, Enabled: true},
		{OrganizationID: "org-1", Name: "central named", Provider: ProviderCentralSMTP, Enabled: true, Config: map[string]any{"sender_name": "ACME IT"}},
		own("CMDB <cmdb@acme.example>"),
		own("cmdb@acme.example"),
	} {
		if _, err := sender.Send(ctx, ch, msg); err != nil {
			t.Fatalf("%s: %v", ch.Name, err)
		}
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	for i, want := range []string{`From: "ACME GmbH" <noreply@reticora.example>`, `From: "ACME IT" <noreply@reticora.example>`,
		`From: "CMDB" <cmdb@acme.example>`, `From: "ACME GmbH" <cmdb@acme.example>`} {
		if i >= len(srv.data) || !strings.Contains(srv.data[i], want) {
			t.Errorf("mail %d lacks %q", i, want)
		}
	}
}
