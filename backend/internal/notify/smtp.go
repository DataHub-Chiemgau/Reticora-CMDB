package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// TLS modes of an SMTP server.
const (
	SMTPStartTLS = "starttls"
	SMTPTLS      = "tls"
	// SMTPNone sends unencrypted; only for a relay on the same host.
	SMTPNone = "none"
)

// SMTPConfig is an SMTP server (TEC-13): the central one of the
// installation or an organization's own.
type SMTPConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	// From is the envelope and header sender address.
	From string `json:"from"`
	TLS  string `json:"tls"`
}

// SMTPConfigFrom reads an SMTP configuration from a channel configuration.
func SMTPConfigFrom(cfg map[string]any) (SMTPConfig, error) {
	var out SMTPConfig
	str := func(k string) string { v, _ := cfg[k].(string); return v }
	out.Host, out.Username, out.Password, out.From, out.TLS = str("host"), str("username"), str("password"), str("from"), str("tls")
	switch p := cfg["port"].(type) {
	case float64:
		out.Port = int(p)
	case int:
		out.Port = p
	case string:
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, fmt.Errorf("%w: port must be a number", ErrValidation)
		}
		out.Port = n
	}
	return out, nil
}

// Validate checks the configuration; the TLS mode defaults to STARTTLS on
// port 587.
func (c *SMTPConfig) Validate() error {
	if c.TLS == "" {
		c.TLS = SMTPStartTLS
	}
	if c.Port == 0 {
		c.Port = 587
		if c.TLS == SMTPTLS {
			c.Port = 465
		}
	}
	switch {
	case strings.TrimSpace(c.Host) == "":
		return fmt.Errorf("%w: smtp host is required", ErrValidation)
	case c.Port < 1 || c.Port > 65535:
		return fmt.Errorf("%w: smtp port %d is out of range", ErrValidation, c.Port)
	case c.TLS != SMTPStartTLS && c.TLS != SMTPTLS && c.TLS != SMTPNone:
		return fmt.Errorf("%w: smtp tls must be starttls, tls or none", ErrValidation)
	case c.Password != "" && c.Username == "":
		return fmt.Errorf("%w: smtp password without username", ErrValidation)
	}
	if _, err := parseAddress(c.From); err != nil {
		return fmt.Errorf("%w: smtp from: %v", ErrValidation, err)
	}
	return nil
}

// parseAddress accepts one address without line breaks (header injection).
func parseAddress(s string) (*mail.Address, error) {
	if strings.ContainsAny(s, "\r\n") {
		return nil, fmt.Errorf("line break in address")
	}
	return mail.ParseAddress(s)
}

// SMTPSender sends e-mail over one SMTP server.
type SMTPSender struct {
	cfg     SMTPConfig
	Timeout time.Duration
	// TLSConfig overrides the TLS settings (tests with their own CA).
	TLSConfig *tls.Config
	now       func() time.Time
}

// NewSMTPSender creates a sender; the configuration must be valid.
func NewSMTPSender(cfg *SMTPConfig) (*SMTPSender, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &SMTPSender{cfg: *cfg, Timeout: 30 * time.Second, now: time.Now}, nil
}

// Mail is one e-mail.
type Mail struct {
	FromName string
	ReplyTo  string
	To       []string
	Subject  string
	Body     string
}

// Send delivers m and returns its Message-ID.
func (s *SMTPSender) Send(ctx context.Context, m *Mail) (string, error) {
	if len(m.To) == 0 {
		return "", fmt.Errorf("e-mail without recipients")
	}
	from, _ := parseAddress(s.cfg.From)
	if m.FromName != "" {
		from.Name = m.FromName
	}
	to := make([]*mail.Address, 0, len(m.To))
	for _, rcpt := range m.To {
		a, err := parseAddress(rcpt)
		if err != nil {
			return "", fmt.Errorf("recipient %q: %w", rcpt, err)
		}
		to = append(to, a)
	}
	if strings.ContainsAny(m.Subject, "\r\n") || strings.ContainsAny(m.FromName, "\r\n") {
		return "", fmt.Errorf("line break in a header")
	}
	msgID, body, err := s.compose(from, to, m)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	client, err := s.dial(ctx)
	if err != nil {
		return "", err
	}
	defer client.Close()
	if s.cfg.Username != "" {
		if err = client.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
			return "", fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err = client.Mail(from.Address); err != nil {
		return "", fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	for _, a := range to {
		if err = client.Rcpt(a.Address); err != nil {
			return "", fmt.Errorf("smtp RCPT TO %s: %w", a.Address, err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return "", fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err = w.Write(body); err != nil {
		return "", fmt.Errorf("smtp write: %w", err)
	}
	if err = w.Close(); err != nil {
		return "", fmt.Errorf("smtp end of data: %w", err)
	}
	_ = client.Quit()
	return msgID, nil
}

func (s *SMTPSender) tlsConfig() *tls.Config {
	if s.TLSConfig != nil {
		return s.TLSConfig
	}
	return &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}
}

// dial opens the session with the TLS mode of the configuration; STARTTLS
// is required when configured.
func (s *SMTPSender) dial(ctx context.Context) (*smtp.Client, error) {
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	dialer := &net.Dialer{}
	var conn net.Conn
	var err error
	if s.cfg.TLS == SMTPTLS {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: s.tlsConfig()}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("smtp connect %s: %w", addr, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	client, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("smtp greeting: %w", err)
	}
	if s.cfg.TLS == SMTPStartTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			_ = client.Close()
			return nil, fmt.Errorf("smtp server %s offers no STARTTLS", addr)
		}
		if err = client.StartTLS(s.tlsConfig()); err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("smtp STARTTLS: %w", err)
		}
	}
	return client, nil
}

// compose builds the RFC 5322 message: UTF-8 text in quoted-printable.
func (s *SMTPSender) compose(from *mail.Address, to []*mail.Address, m *Mail) (msgID string, body []byte, err error) {
	idBytes := make([]byte, 12)
	if _, err := rand.Read(idBytes); err != nil {
		return "", nil, err
	}
	domain := "reticora.local"
	if at := strings.LastIndex(from.Address, "@"); at >= 0 {
		domain = from.Address[at+1:]
	}
	msgID = "<" + hex.EncodeToString(idBytes) + "@" + domain + ">"
	recipients := make([]string, len(to))
	for i, a := range to {
		recipients[i] = a.String()
	}
	var b bytes.Buffer
	header := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	header("From", from.String())
	header("To", strings.Join(recipients, ", "))
	if m.ReplyTo != "" {
		reply, err := parseAddress(m.ReplyTo)
		if err != nil {
			return "", nil, fmt.Errorf("reply-to: %w", err)
		}
		header("Reply-To", reply.String())
	}
	header("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	header("Date", s.now().UTC().Format(time.RFC1123Z))
	header("Message-ID", msgID)
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&b)
	if _, err := qp.Write([]byte(strings.ReplaceAll(m.Body, "\n", "\r\n"))); err != nil {
		return "", nil, err
	}
	if err := qp.Close(); err != nil {
		return "", nil, err
	}
	return msgID, b.Bytes(), nil
}

// EmailProvider delivers e-mail channels: central_smtp over the
// installation's server with the organization as sender name (the central
// relay signs with DKIM for its domain, SPF covers it), smtp over the
// organization's own server.
type EmailProvider struct {
	central *SMTPSender
	// TLSConfig overrides the TLS settings of organization servers (tests).
	TLSConfig *tls.Config
}

// NewEmailProvider creates the provider; central may be nil (no central
// SMTP configured: central_smtp channels fail).
func NewEmailProvider(central *SMTPSender) *EmailProvider {
	return &EmailProvider{central: central}
}

// Send implements Provider.
func (p *EmailProvider) Send(ctx context.Context, ch *Channel, msg *Message) (*Delivery, error) {
	var sender *SMTPSender
	m := &Mail{Subject: msg.Subject, Body: msg.Body}
	switch ch.Provider {
	case ProviderCentralSMTP:
		if p.central == nil {
			return nil, fmt.Errorf("no central SMTP server is configured")
		}
		sender = p.central
		m.FromName, _ = ch.Config["sender_name"].(string)
		m.ReplyTo, _ = ch.Config["reply_to"].(string)
	case ProviderSMTP:
		cfg, err := SMTPConfigFrom(ch.Config)
		if err != nil {
			return nil, err
		}
		if sender, err = NewSMTPSender(&cfg); err != nil {
			return nil, err
		}
		sender.TLSConfig = p.TLSConfig
	default:
		return nil, fmt.Errorf("provider %s is no e-mail provider", ch.Provider)
	}
	for _, r := range msg.To {
		if r.Email == "" {
			continue
		}
		addr := r.Email
		if r.Name != "" {
			addr = (&mail.Address{Name: r.Name, Address: r.Email}).String()
		}
		m.To = append(m.To, addr)
	}
	if len(m.To) == 0 {
		return nil, fmt.Errorf("no recipient has an e-mail address")
	}
	id, err := sender.Send(ctx, m)
	if err != nil {
		return nil, err
	}
	return &Delivery{ProviderID: id, Recipients: len(m.To), SentAt: time.Now().UTC()}, nil
}

// SignupMailer sends the verification e-mail of the self-signup (TLC-01)
// over the central SMTP server.
type SignupMailer struct {
	Sender *SMTPSender
}

// SendSignupVerification implements signup.Mailer.
func (s SignupMailer) SendSignupVerification(ctx context.Context, to, organizationName, link string) error {
	_, err := s.Sender.Send(ctx, &Mail{
		To:      []string{to},
		Subject: "Reticora: E-Mail-Adresse bestätigen / confirm your e-mail address",
		Body: "Bitte bestätigen Sie die Registrierung von " + organizationName + ":\n" + link +
			"\n\nPlease confirm the registration of " + organizationName + ":\n" + link + "\n",
	})
	return err
}
