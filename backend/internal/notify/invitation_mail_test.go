package notify

import (
	"context"
	"encoding/json"
	"io"
	"mime/quotedprintable"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// mailParts splits a recorded SMTP message into its header and its
// quoted-printable decoded body.
func mailParts(t *testing.T, data string) (header, body string) {
	t.Helper()
	header, raw, ok := strings.Cut(data, "\r\n\r\n")
	if !ok {
		t.Fatalf("message without body:\n%s", data)
	}
	decoded, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(raw)))
	if err != nil {
		t.Fatalf("quoted-printable body: %v", err)
	}
	return header, strings.ReplaceAll(string(decoded), "\r\n", "\n")
}

// TestInvitationMailerSendsStraightToTheInvitee covers TLC-03 and NTF-05:
// the invitation mail of an issue or renewal goes over the central SMTP
// server to the invited address only (one session, one RCPT TO), with the
// template of invitation.created in the organization's language, the
// accept link with its token and the expiry, branded like the
// organization's e-mails (sender name as display name and subject prefix,
// the organization in the footer; without a sender name the organization's
// name). A name with a line break injects no header. A server that does
// not take the mail is an error.
func TestInvitationMailerSendsStraightToTheInvitee(t *testing.T) {
	ctx := context.Background()
	srv := newFakeSMTP(t, false)
	central, err := NewSMTPSender(&SMTPConfig{Host: "127.0.0.1", Port: srv.port(), TLS: SMTPNone, From: "noreply@reticora.example"})
	if err != nil {
		t.Fatal(err)
	}
	mailer := InvitationMailer{Sender: central}
	expires := time.Date(2026, 11, 9, 10, 30, 0, 0, time.FixedZone("CET", 3600))
	link := "https://cmdb.example/invitations/accept?token=0b9a51a2-55a4-4c7c-9a4f-6f4f6f4f6f4f.c2VjcmV0LXBhcnQtb2YtdGhlLXRva2Vu"
	for _, m := range []*InvitationMail{
		{To: "victim@bigcorp.example", Link: link, ExpiresAt: expires, OrganizationName: "ACME", Locale: "en-US", SenderName: "ACME IT"},
		{To: "neu@bigcorp.example", Link: link, ExpiresAt: expires, OrganizationName: "ACME", Locale: "de-DE"},
		{To: "inject@bigcorp.example", Link: link, ExpiresAt: expires, OrganizationName: "ACME\r\nBcc: spy@attacker.example", Locale: "de"},
	} {
		if err = mailer.SendInvitation(ctx, m); err != nil {
			t.Fatalf("send to %s: %v", m.To, err)
		}
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.data) != 3 {
		t.Fatalf("%d SMTP messages, want 3", len(srv.data))
	}
	for i, want := range []struct {
		rcpt, from, subject string
		body                []string
	}{
		{"victim@bigcorp.example", `From: "ACME IT" <noreply@reticora.example>`, "Subject: [ACME IT] Invitation to Reticora",
			[]string{"You have been invited to ACME. The invitation is valid until 2026-11-09T09:30:00Z:\n" + link,
				"This notification was sent by Reticora on behalf of ACME."}},
		{"neu@bigcorp.example", `From: "ACME" <noreply@reticora.example>`, "Subject: [ACME] Einladung zu Reticora",
			[]string{"Sie wurden zu ACME eingeladen. Die Einladung gilt bis 2026-11-09T09:30:00Z:\n" + link,
				"Diese Benachrichtigung wurde von Reticora im Auftrag von ACME versendet."}},
		{"inject@bigcorp.example", `From: "ACME Bcc: spy@attacker.example" <noreply@reticora.example>`, "Subject: [ACME Bcc: spy@attacker.example] Einladung zu Reticora",
			[]string{link}},
	} {
		if got := strings.Join(srv.rcpt[i], ","); got != "<"+want.rcpt+">" {
			t.Errorf("mail %d: RCPT TO %s, want only <%s>", i, got, want.rcpt)
		}
		if srv.from[i] != "<noreply@reticora.example>" {
			t.Errorf("mail %d: MAIL FROM %s, want the central server's sender", i, srv.from[i])
		}
		header, body := mailParts(t, srv.data[i])
		for _, h := range []string{want.from, want.subject, "To: <" + want.rcpt + ">"} {
			if !strings.Contains(header, h+"\r\n") {
				t.Errorf("mail %d lacks the header %q:\n%s", i, h, header)
			}
		}
		if strings.Contains(header, "\r\nBcc:") {
			t.Errorf("mail %d: the organization name injected a header:\n%s", i, header)
		}
		for _, b := range want.body {
			if !strings.Contains(body, b) {
				t.Errorf("mail %d: body lacks %q:\n%s", i, b, body)
			}
		}
	}

	closed, err := NewSMTPSender(&SMTPConfig{Host: "127.0.0.1", Port: 1, TLS: SMTPNone, From: "noreply@reticora.example"})
	if err != nil {
		t.Fatal(err)
	}
	if err = (InvitationMailer{Sender: closed}).SendInvitation(ctx, &InvitationMail{To: "x@bigcorp.example", Link: link,
		ExpiresAt: expires, OrganizationName: "ACME"}); err == nil {
		t.Error("an unreachable SMTP server took the invitation mail")
	}
}

// TestInvitationEventWithoutLinkNotifiesNobody covers TLC-03 and NTF-05:
// where the acceptance creates a verified IdP account, the issue mails the
// invitee straight over the central SMTP server and invitation.created
// carries no link. Such an event queues no delivery on any channel of the
// organization: not on its own SMTP server (which its administrators
// choose and could read), not on a central_smtp channel (no second mail),
// not in-app and not on a webhook. The event with link (no IdP admin
// client) still reaches the invitee over the e-mail channels.
func TestInvitationEventWithoutLinkNotifiesNobody(t *testing.T) {
	ctx := context.Background()
	env := newNotifierEnv(t)
	if err := env.repo.Create(ctx, &Channel{OrganizationID: "org-1", Name: "relay", Provider: ProviderSMTP, Enabled: true,
		Config: map[string]any{"host": "relay.attacker.example", "port": float64(25), "tls": "none", "from": "x@attacker.example"}}); err != nil {
		t.Fatal(err)
	}
	consumer := NewEventConsumer(audienceResolver{}, env.n)
	consume := func(id string, data map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(data)
		if err := consumer.Consume(ctx, &events.Envelope{ID: id, OrgID: "org-1", Type: events.InvitationCreated, Data: raw}); err != nil {
			t.Fatal(err)
		}
	}
	consume("e-direct", map[string]any{"id": "inv-1", "email": "victim@bigcorp.example", "organization_name": "ACME",
		"expires_at": "2026-11-09T09:30:00Z"})
	if rows, _ := env.store.List(ctx, "org-1", 100, 0); len(rows) != 0 {
		t.Fatalf("an invitation.created without link queued %d deliveries: %+v", len(rows), rows)
	}

	consume("e-channels", map[string]any{"id": "inv-2", "email": "new@x.example", "organization_name": "ACME",
		"expires_at": "2026-11-09T09:30:00Z", "link": "https://cmdb.example/invitations/accept?token=org-1.secret"})
	rows, _ := env.store.List(ctx, "org-1", 100, 0)
	if len(rows) != 2 {
		t.Fatalf("the invitation.created with link queued %d deliveries, want one per e-mail channel: %+v", len(rows), rows)
	}
	for _, r := range rows {
		ch, err := env.repo.Get(ctx, "org-1", r.ChannelID)
		if err != nil || ch.Kind != KindEmail || r.Recipient.Email != "new@x.example" {
			t.Errorf("delivery %+v over %+v (%v), want an e-mail channel to the invitee", r, ch, err)
		}
	}
}
