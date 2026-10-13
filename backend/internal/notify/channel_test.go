package notify

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

// fakeSMTP is a minimal SMTP server that records one session per mail.
type fakeSMTP struct {
	ln       net.Listener
	startTLS bool
	mu       sync.Mutex
	auth     []string
	from     []string
	rcpt     [][]string
	data     []string
}

func newFakeSMTP(t *testing.T, startTLS bool) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeSMTP{ln: ln, startTLS: startTLS}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *fakeSMTP) port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *fakeSMTP) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.session(conn)
	}
}

func (s *fakeSMTP) session(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	reply := func(line string) { _, _ = io.WriteString(conn, line+"\r\n") }
	reply("220 fake ESMTP")
	var rcpts []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(upper, "EHLO"):
			if s.startTLS {
				reply("250-fake")
				reply("250-STARTTLS")
			} else {
				reply("250-fake")
			}
			reply("250 AUTH PLAIN")
		case strings.HasPrefix(upper, "AUTH PLAIN"):
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(cmd[len("AUTH PLAIN"):]))
			s.mu.Lock()
			s.auth = append(s.auth, strings.ReplaceAll(string(raw), "\x00", "|"))
			s.mu.Unlock()
			reply("235 ok")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			s.mu.Lock()
			s.from = append(s.from, cmd[len("MAIL FROM:"):])
			s.mu.Unlock()
			reply("250 ok")
		case strings.HasPrefix(upper, "RCPT TO:"):
			rcpts = append(rcpts, cmd[len("RCPT TO:"):])
			reply("250 ok")
		case upper == "DATA":
			reply("354 go ahead")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			s.mu.Lock()
			s.rcpt = append(s.rcpt, rcpts)
			s.data = append(s.data, b.String())
			s.mu.Unlock()
			rcpts = nil
			reply("250 queued")
		case upper == "QUIT":
			reply("221 bye")
			return
		default:
			reply("250 ok")
		}
	}
}

// TestChannelValidation covers NTF-01 (WP-137): provider and kind belong
// together, an SMTP channel needs a valid server and sender, a webhook a
// URL; addresses with line breaks are refused.
func TestChannelValidation(t *testing.T) {
	smtpCfg := map[string]any{"host": "mail.example.org", "from": "cmdb@example.org"}
	for name, tc := range map[string]struct {
		ch Channel
		ok bool
	}{
		"central":           {Channel{Name: "mail", Provider: ProviderCentralSMTP, Config: map[string]any{"sender_name": "ACME"}}, true},
		"own smtp":          {Channel{Name: "mail", Provider: ProviderSMTP, Config: smtpCfg}, true},
		"in-app":            {Channel{Name: "center", Provider: ProviderInApp}, true},
		"webhook":           {Channel{Name: "hook", Provider: ProviderWebhook, Config: map[string]any{"url": "https://hooks.example.org/x"}}, true},
		"no name":           {Channel{Provider: ProviderInApp}, false},
		"unknown provider":  {Channel{Name: "x", Provider: "twilio"}, false},
		"kind mismatch":     {Channel{Name: "x", Kind: KindSMS, Provider: ProviderSMTP, Config: smtpCfg}, false},
		"smtp without host": {Channel{Name: "x", Provider: ProviderSMTP, Config: map[string]any{"from": "a@b.org"}}, false},
		"smtp bad tls":      {Channel{Name: "x", Provider: ProviderSMTP, Config: map[string]any{"host": "h", "from": "a@b.org", "tls": "ssl"}}, false},
		"smtp bad sender":   {Channel{Name: "x", Provider: ProviderSMTP, Config: map[string]any{"host": "h", "from": "a@b.org\r\nBcc: x@y.org"}}, false},
		"reply-to injected": {Channel{Name: "x", Provider: ProviderCentralSMTP, Config: map[string]any{"reply_to": "a@b.org\nBcc: c@d.org"}}, false},
		"webhook no url":    {Channel{Name: "x", Provider: ProviderWebhook}, false},
	} {
		err := tc.ch.Validate()
		if (err == nil) != tc.ok {
			t.Errorf("%s: %v", name, err)
		}
		if tc.ok && tc.ch.Kind == "" {
			t.Errorf("%s: kind not derived", name)
		}
	}
	cfg := SMTPConfig{Host: "h", From: "a@b.org", TLS: SMTPTLS}
	if err := cfg.Validate(); err != nil || cfg.Port != 465 {
		t.Errorf("implicit TLS default port: %d %v", cfg.Port, err)
	}
}

// TestSealer covers the encrypted channel configuration: a roundtrip, a
// fresh data key per configuration, and a refused tampered ciphertext.
func TestSealer(t *testing.T) {
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	s := NewSealer(enc)
	cfg := map[string]any{"password": "s3cret", "port": float64(587)}
	ct, dek, err := s.Seal(cfg)
	if err != nil || strings.Contains(string(ct), "s3cret") {
		t.Fatalf("seal: %v", err)
	}
	ct2, dek2, _ := s.Seal(cfg)
	if string(dek) == string(dek2) || string(ct) == string(ct2) {
		t.Error("two configurations share a data key")
	}
	got, err := s.Open(ct, dek)
	if err != nil || got["password"] != "s3cret" || got["port"] != float64(587) {
		t.Errorf("open: %v %v", got, err)
	}
	ct[len(ct)-1] ^= 0xff
	if _, err = s.Open(ct, dek); err == nil {
		t.Error("a tampered configuration was opened")
	}
	if ct, dek, err = s.Seal(nil); ct != nil || dek != nil || err != nil {
		t.Error("an empty configuration was stored")
	}
}

// TestEmailOverSMTP covers TEC-13: e-mail over the organization's own SMTP
// with authentication, over the central SMTP with the organization as
// sender name and reply-to, UTF-8 subject and body; a server without the
// required STARTTLS is refused; the signup mail goes over the central
// server.
func TestEmailOverSMTP(t *testing.T) {
	ctx := context.Background()
	srv := newFakeSMTP(t, false)
	own := &Channel{ID: "c1", OrganizationID: "org-1", Name: "own", Provider: ProviderSMTP, Enabled: true,
		Config: map[string]any{"host": "127.0.0.1", "port": float64(srv.port()), "tls": "none",
			"username": "cmdb", "password": "pw", "from": "CMDB <cmdb@acme.example>"}}
	if err := own.Validate(); err != nil {
		t.Fatal(err)
	}
	central, err := NewSMTPSender(&SMTPConfig{Host: "127.0.0.1", Port: srv.port(), TLS: SMTPNone, From: "noreply@reticora.example"})
	if err != nil {
		t.Fatal(err)
	}
	sender := NewSender(map[string]Provider{ProviderSMTP: NewEmailProvider(central), ProviderCentralSMTP: NewEmailProvider(central)})
	msg := &Message{Category: "export", Subject: "Export fertig – Größe 2 MB", Body: "Der Export ist bereit.\nGrüße",
		To: []Recipient{{Email: "ops@acme.example", Name: "Ops Team"}, {UserID: "u-1"}, {Email: "admin@acme.example"}}}
	d, err := sender.Send(ctx, own, msg)
	if err != nil {
		t.Fatal(err)
	}
	if d.Recipients != 2 || d.ProviderID == "" || d.Provider != ProviderSMTP || d.ChannelID != "c1" {
		t.Errorf("delivery: %+v", d)
	}
	centralCh := &Channel{OrganizationID: "org-1", Name: "central", Provider: ProviderCentralSMTP, Enabled: true,
		Config: map[string]any{"sender_name": "ACME IT", "reply_to": "it@acme.example"}}
	if _, err = sender.Send(ctx, centralCh, msg); err != nil {
		t.Fatal(err)
	}
	if err = (SignupMailer{Sender: central}).SendSignupVerification(ctx, "new@acme.example", "ACME", "https://app/verify?t=1"); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.data) != 3 {
		t.Fatalf("mails: %d", len(srv.data))
	}
	if len(srv.auth) != 1 || srv.auth[0] != "|cmdb|pw" {
		t.Errorf("auth: %v", srv.auth)
	}
	if srv.from[0] != "<cmdb@acme.example>" || strings.Join(srv.rcpt[0], ",") != "<ops@acme.example>,<admin@acme.example>" {
		t.Errorf("envelope: %v %v", srv.from, srv.rcpt)
	}
	first := srv.data[0]
	for _, want := range []string{`From: "CMDB" <cmdb@acme.example>`, `To: "Ops Team" <ops@acme.example>, <admin@acme.example>`,
		"Subject: =?utf-8?q?", "Content-Transfer-Encoding: quoted-printable", "Gr=C3=BC=C3=9Fe", "Message-ID: <"} {
		if !strings.Contains(first, want) {
			t.Errorf("own SMTP mail lacks %q:\n%s", want, first)
		}
	}
	second := srv.data[1]
	if srv.from[1] != "<noreply@reticora.example>" || !strings.Contains(second, `From: "ACME IT" <noreply@reticora.example>`) ||
		!strings.Contains(second, "Reply-To: <it@acme.example>") {
		t.Errorf("central mail:\n%s", second)
	}
	if !strings.Contains(srv.data[2], "https://app/verify?t=3D1") || strings.Join(srv.rcpt[2], ",") != "<new@acme.example>" {
		t.Errorf("signup mail:\n%s", srv.data[2])
	}

	// STARTTLS is required when configured.
	plain := newFakeSMTP(t, false)
	strict, _ := NewSMTPSender(&SMTPConfig{Host: "127.0.0.1", Port: plain.port(), From: "a@b.example"})
	if _, err = strict.Send(ctx, &Mail{To: []string{"x@y.example"}, Subject: "s", Body: "b"}); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("server without STARTTLS: %v", err)
	}
	if _, err = central.Send(ctx, &Mail{To: []string{"x@y.example"}, Subject: "a\r\nBcc: z@y.example", Body: "b"}); err == nil {
		t.Error("a subject with a line break was sent")
	}
	noCentral := NewSender(map[string]Provider{ProviderCentralSMTP: NewEmailProvider(nil)})
	if _, err = noCentral.Send(ctx, centralCh, msg); err == nil {
		t.Error("central channel without central SMTP")
	}
}

// TestInAppAndWebhookChannels covers the in-app and webhook providers and
// the routing of the sender.
func TestInAppAndWebhookChannels(t *testing.T) {
	ctx := context.Background()
	inbox := NewMemoryInbox()
	var gotBody []byte
	var gotSig string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotSig = r.Header.Get("X-Reticora-Signature")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer hook.Close()
	sender := NewSender(map[string]Provider{ProviderInApp: NewInAppProvider(inbox), ProviderWebhook: NewWebhookProvider(hook.Client())})
	msg := &Message{Category: "review", Subject: "Neues Review-Item", Body: "Bitte prüfen",
		To: []Recipient{{UserID: "u-1"}, {UserID: "u-2", Email: "b@x.example"}, {Email: "c@x.example"}}, Data: map[string]any{"item": "r-1"}}

	inApp := &Channel{OrganizationID: "org-1", Name: "center", Provider: ProviderInApp, Kind: KindInApp, Enabled: true}
	if d, err := sender.Send(ctx, inApp, msg); err != nil || d.Recipients != 2 {
		t.Fatalf("in-app: %+v %v", d, err)
	}
	if got := inbox.For("org-1", "u-2"); len(got) != 1 || got[0].Subject != "Neues Review-Item" || got[0].Data["item"] != "r-1" {
		t.Errorf("inbox of u-2: %+v", got)
	}
	if got := inbox.For("org-2", "u-1"); len(got) != 0 {
		t.Error("in-app notification in another organization")
	}

	webhookCh := &Channel{OrganizationID: "org-1", Name: "hook", Provider: ProviderWebhook, Kind: KindWebhook, Enabled: true,
		Config: map[string]any{"url": hook.URL, "secret": "k"}}
	if _, err := sender.Send(ctx, webhookCh, msg); err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte("k"))
	mac.Write(gotBody)
	if gotSig != "sha256="+hex.EncodeToString(mac.Sum(nil)) || !strings.Contains(string(gotBody), `"category":"review"`) {
		t.Errorf("webhook: %s %s", gotSig, gotBody)
	}

	webhookCh.Enabled = false
	if _, err := sender.Send(ctx, webhookCh, msg); !errors.Is(err, ErrDisabled) {
		t.Errorf("disabled channel: %v", err)
	}
	if _, err := sender.Send(ctx, &Channel{Provider: ProviderSMTP, Enabled: true}, msg); err == nil {
		t.Error("a provider without implementation sent")
	}
	if _, err := sender.Send(ctx, inApp, &Message{Subject: "x"}); err == nil {
		t.Error("a message without recipients was sent")
	}
	repo := NewMemoryRepository()
	ch := &Channel{OrganizationID: "org-1", Name: "center", Provider: ProviderInApp, Enabled: true}
	if err := repo.Create(ctx, ch); err != nil {
		t.Fatal(err)
	}
	if err := repo.Create(ctx, &Channel{OrganizationID: "org-1", Name: "center", Provider: ProviderInApp}); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate name: %v", err)
	}
	if _, err := repo.Get(ctx, "org-2", ch.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("another organization's channel: %v", err)
	}
}
