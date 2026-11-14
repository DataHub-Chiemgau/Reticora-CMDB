package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
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
	case c.TLS == SMTPNone && c.Username != "" && !smtpLocalhost(c.Host):
		// smtp.PlainAuth refuses an unencrypted connection to any other
		// host: every send would fail at AUTH.
		return fmt.Errorf("%w: smtp authentication needs tls or starttls", ErrValidation)
	}
	if _, err := parseAddress(c.From); err != nil {
		return fmt.Errorf("%w: smtp from: %v", ErrValidation, err)
	}
	return nil
}

// smtpLocalhost reports whether net/smtp's PlainAuth sends credentials to
// host over an unencrypted connection: only to these literal names.
func smtpLocalhost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// smtpPorts are the ports an organization's own server may use unless
// private destinations are allowed: relay, implicit TLS and submission.
var smtpPorts = map[int]bool{25: true, 465: true, 587: true}

// allowPrivate is the setting the errors of CheckEgress name for internal
// relays.
const allowPrivate = "RETICORA_EGRESS_ALLOW_PRIVATE=true"

// DestinationError rejects the server of an organization's own SMTP
// channel or the url of a webhook channel under the egress policy
// (SEC-08): the request field and the reason, which wraps
// egress.ErrBlockedDestination.
type DestinationError struct {
	Field string
	Err   error
	// Status is the HTTP status; zero means 422.
	Status int
}

func (e *DestinationError) Error() string { return e.Err.Error() }

func (e *DestinationError) Unwrap() error { return e.Err }

// CheckEgress checks an organization's own server (provider smtp) against
// the egress policy when it is stored and before every send: no IP literal
// or name of the internal network or a cloud metadata endpoint, and only
// the ports 25, 465 and 587, unless private destinations are allowed. The
// address the name resolves to is checked on every connection (DNS
// rebinding). The configuration must be valid.
func (c *SMTPConfig) CheckEgress(opts egress.Options) error {
	if err := opts.ValidateHost(c.Host); err != nil {
		// Metadata endpoints stay blocked with the setting as well.
		if (egress.Options{AllowPrivate: true}).ValidateHost(c.Host) == nil {
			err = fmt.Errorf("%w; an SMTP relay in the internal network needs %s", err, allowPrivate)
		}
		return &DestinationError{Field: "host", Err: err}
	}
	if !opts.AllowPrivate && !smtpPorts[c.Port] {
		return &DestinationError{Field: "port", Err: fmt.Errorf("%w: port %d (25, 465 or 587; other ports need %s)",
			egress.ErrBlockedDestination, c.Port, allowPrivate)}
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
	// connect opens the connection to the server; nil uses a plain
	// net.Dialer (the central server of the operator), organization
	// servers get the egress dialer.
	connect func(ctx context.Context, network, address string) (net.Conn, error)
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
	// Attachments are sent as parts of a multipart/mixed message after
	// the text (the table counts of the deletion protocol, WP-1156).
	Attachments []Attachment
}

// Attachment is a file part of a mail: name, media type and content.
type Attachment struct {
	Name        string
	ContentType string
	Data        []byte
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
	connect := s.connect
	if connect == nil {
		connect = (&net.Dialer{}).DialContext
	}
	conn, err := connect(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("smtp connect %s: %w", addr, err)
	}
	if s.cfg.TLS == SMTPTLS {
		// TLS over the connection the dialer checked, verified for the
		// configured host name.
		cfg := s.tlsConfig()
		if cfg.ServerName == "" {
			cfg = cfg.Clone()
			cfg.ServerName = s.cfg.Host
		}
		tlsConn := tls.Client(conn, cfg)
		if err = tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("smtp connect %s: %w", addr, err)
		}
		conn = tlsConn
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
	if len(m.Attachments) > 0 {
		if err := writeMultipart(&b, header, m); err != nil {
			return "", nil, err
		}
		return msgID, b.Bytes(), nil
	}
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")
	if err := writeQuotedText(&b, m.Body); err != nil {
		return "", nil, err
	}
	return msgID, b.Bytes(), nil
}

// writeQuotedText writes body as quoted-printable with CRLF line ends.
func writeQuotedText(b *bytes.Buffer, body string) error {
	qp := quotedprintable.NewWriter(b)
	if _, err := qp.Write([]byte(strings.ReplaceAll(body, "\n", "\r\n"))); err != nil {
		return err
	}
	return qp.Close()
}

// writeMultipart writes the text and the attachments of m as
// multipart/mixed; the attachments in base64 with their file name.
func writeMultipart(b *bytes.Buffer, header func(k, v string), m *Mail) error {
	mw := multipart.NewWriter(b)
	header("Content-Type", mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": mw.Boundary()}))
	b.WriteString("\r\n")
	text, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {"text/plain; charset=utf-8"},
		"Content-Transfer-Encoding": {"quoted-printable"},
	})
	if err != nil {
		return err
	}
	qp := quotedprintable.NewWriter(text)
	if _, err = qp.Write([]byte(strings.ReplaceAll(m.Body, "\n", "\r\n"))); err != nil {
		return err
	}
	if cerr := qp.Close(); cerr != nil {
		return cerr
	}
	for _, a := range m.Attachments {
		if strings.ContainsAny(a.Name, "\r\n\"") {
			return fmt.Errorf("attachment name with a line break or quote")
		}
		contentType := a.ContentType
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		part, perr := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {contentType},
			"Content-Transfer-Encoding": {"base64"},
			"Content-Disposition":       {mime.FormatMediaType("attachment", map[string]string{"filename": a.Name})},
		})
		if perr != nil {
			return perr
		}
		encoded := base64.StdEncoding.EncodeToString(a.Data)
		for len(encoded) > 76 {
			if _, perr = part.Write([]byte(encoded[:76] + "\r\n")); perr != nil {
				return perr
			}
			encoded = encoded[76:]
		}
		if _, perr = part.Write([]byte(encoded + "\r\n")); perr != nil {
			return perr
		}
	}
	return mw.Close()
}

// EmailProvider delivers e-mail channels: central_smtp over the
// installation's server with the organization as sender name (the central
// relay signs with DKIM for its domain, SPF covers it), smtp over the
// organization's own server under the egress policy (SEC-08).
type EmailProvider struct {
	central *SMTPSender
	// egress is the destination policy of organization servers; the
	// central server is the operator's configuration and exempt.
	egress egress.Options
	dialer *egress.Dialer
	// TLSConfig overrides the TLS settings of organization servers (tests).
	TLSConfig *tls.Config
}

// NewEmailProvider creates the provider; central may be nil (no central
// SMTP configured: central_smtp channels fail). Organization servers are
// held to the default egress policy, which blocks every internal
// destination.
func NewEmailProvider(central *SMTPSender) *EmailProvider {
	return (&EmailProvider{central: central}).WithEgress(egress.Options{})
}

// WithEgress sets the destination policy of organization servers
// (RETICORA_EGRESS_ALLOW_PRIVATE).
func (p *EmailProvider) WithEgress(opts egress.Options) *EmailProvider {
	p.egress, p.dialer = opts, egress.NewDialer(opts)
	return p
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
		// The channel's sender name, else the organization's (NTF-03).
		m.FromName, _ = ch.Config["sender_name"].(string)
		if m.FromName == "" {
			m.FromName = msg.SenderName
		}
		m.ReplyTo, _ = ch.Config["reply_to"].(string)
	case ProviderSMTP:
		cfg, err := SMTPConfigFrom(ch.Config)
		if err != nil {
			return nil, err
		}
		if sender, err = NewSMTPSender(&cfg); err != nil {
			return nil, err
		}
		// Checked again on every send: the channel may have been stored
		// under another policy.
		if err := cfg.CheckEgress(p.egress); err != nil {
			return nil, err
		}
		sender.TLSConfig = p.TLSConfig
		sender.connect = p.dialer.DialContext
		// A sender address without a display name gets the
		// organization's sender name (NTF-03).
		if from, _ := parseAddress(cfg.From); from != nil && from.Name == "" {
			m.FromName = msg.SenderName
		}
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

// InvitationMail is the invitation mail of an issue or renewal (TLC-03,
// NTF-05): the acceptance link for the invited address and its expiry, with
// the organization's name, locale (LOC-01) and settings.branding.sender_name
// (NTF-03).
type InvitationMail struct {
	To               string
	Link             string
	ExpiresAt        time.Time
	OrganizationName string
	Locale           string
	SenderName       string
}

// InvitationMailer sends the invitation mail straight to the invited
// address over the central SMTP server of the installation, like the
// verification e-mail of the self-signup (user.InvitationMailer). Its link
// holds the acceptance token, and with the IdP admin client the acceptance
// creates a verified IdP account of the address: the mail therefore never
// goes over a channel of the organization, whose own SMTP server (provider
// smtp) the organization's administrators choose and could read. It is no
// notification: channels, preferences, digests and notifications_email do
// not apply. The text is the template of invitation.created in the
// organization's language (the invitee has no account, so no language of
// their own), branded like the organization's e-mails: sender name, subject
// prefix and footer.
type InvitationMailer struct {
	Sender *SMTPSender
}

// SendInvitation implements user.InvitationMailer.
func (m InvitationMailer) SendInvitation(ctx context.Context, inv *InvitationMail) error {
	brand := newBranding(inv.OrganizationName, inv.Locale, inv.SenderName)
	locale := TemplateLocale("", brand.Locale)
	text := brand.Mail(locale, Render(events.InvitationCreated, locale, map[string]any{
		"organization_name": inv.OrganizationName, "expires_at": inv.ExpiresAt.UTC().Format(time.RFC3339), "link": inv.Link}))
	_, err := m.Sender.Send(ctx, &Mail{FromName: brand.SenderName, To: []string{inv.To}, Subject: text.Subject, Body: text.Body})
	return err
}

// ErrNoCentralSMTP: the installation has no central SMTP server; mails
// that only it may send (the deletion protocol) cannot be delivered.
var ErrNoCentralSMTP = errors.New("no central SMTP server is configured")

// PurgeProtocolMail is the deletion protocol of an organization (TLC-06,
// WP-1156) for the address of its requester: the times, the total hash
// over the protocol and the id of its operator_audit entry; the counts per
// table and object kind are the text attachment. It holds no personal
// value of the organization.
type PurgeProtocolMail struct {
	To     string
	Locale string
	// AuditID is the id of the operator_audit entry org.purged.
	AuditID      int64
	Hash         string
	RequestedAt  time.Time
	DueAt        time.Time
	PurgeStarted time.Time
	PurgeEnded   time.Time
	Rows         int64
	Objects      int64
	// Counts is the text attachment: one line per table and object kind.
	Counts string
}

// OrgPurgeProtocolMailer sends the deletion protocol over the central SMTP
// server of the installation (like SignupMailer): the organization, its
// users and its channels are gone, so no channel of the organization can
// carry it. A nil Sender is ErrNoCentralSMTP.
type OrgPurgeProtocolMailer struct {
	Sender *SMTPSender
}

// purgeProtocolTexts are subject, body and attachment name per language.
var purgeProtocolTexts = map[string]struct{ subject, body, file string }{
	LocaleDE: {
		subject: "Reticora: Löschprotokoll Ihrer Organisation",
		body: "Die von Ihnen beantragte Löschung der Organisation ist abgeschlossen.\n\n" +
			"Antrag: %s\nFällig: %s\nBeginn der Löschung: %s\nEnde der Löschung: %s\n" +
			"Gelöschte Zeilen: %d\nGelöschte Objekte: %d\n\n" +
			"Gesamthash (SHA-256): %s\nEintrag im Betreiberprotokoll (operator_audit): %d\n\n" +
			"Die Zählungen und Hashes je Tabelle und Objektart stehen im Anhang. " +
			"Sicherungen (Backups) enthalten die Daten noch, bis ihre Aufbewahrungsfrist abläuft; danach sind sie dort ebenfalls gelöscht.\n" +
			"Diese Adresse wurde nur für den Versand dieses Protokolls aufbewahrt und ist jetzt gelöscht.\n",
		file: "loeschprotokoll.txt",
	},
	LocaleEN: {
		subject: "Reticora: deletion protocol of your organization",
		body: "The deletion of the organization you requested is complete.\n\n" +
			"Requested: %s\nDue: %s\nPurge started: %s\nPurge ended: %s\n" +
			"Rows deleted: %d\nObjects deleted: %d\n\n" +
			"Total hash (SHA-256): %s\nEntry of the operator audit (operator_audit): %d\n\n" +
			"The counts and hashes per table and object kind are attached. " +
			"Backups still hold the data until their retention ends; then it is gone there as well.\n" +
			"This address was kept only to send this protocol and is deleted now.\n",
		file: "deletion-protocol.txt",
	},
}

// SendPurgeProtocol sends p to its address in its language (German unless
// it is English).
func (m OrgPurgeProtocolMailer) SendPurgeProtocol(ctx context.Context, p *PurgeProtocolMail) error {
	if m.Sender == nil {
		return ErrNoCentralSMTP
	}
	locale := LocaleDE
	if strings.HasPrefix(strings.ToLower(p.Locale), "en") {
		locale = LocaleEN
	}
	text := purgeProtocolTexts[locale]
	stamp := func(t time.Time) string { return t.UTC().Format(time.RFC3339) }
	body := fmt.Sprintf(text.body, stamp(p.RequestedAt), stamp(p.DueAt), stamp(p.PurgeStarted), stamp(p.PurgeEnded),
		p.Rows, p.Objects, p.Hash, p.AuditID)
	_, err := m.Sender.Send(ctx, &Mail{To: []string{p.To}, Subject: text.subject, Body: body,
		Attachments: []Attachment{{Name: text.file, ContentType: "text/plain; charset=utf-8", Data: []byte(p.Counts)}}})
	return err
}
