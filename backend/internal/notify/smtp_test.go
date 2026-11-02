package notify

import (
	"errors"
	"net/smtp"
	"strings"
	"testing"
)

// TestSMTPLoginNeedsTLS covers TEC-13: with TLS mode none a login fails on
// every send, because net/smtp's PlainAuth refuses an unencrypted connection
// to any host but localhost, while /readyz (EHLO only) stays green. Validate
// refuses that configuration by the same rule, and config.Validate does so
// even earlier, so the server stops at its start ("invalid configuration",
// "RETICORA_SMTP_USERNAME needs RETICORA_SMTP_TLS=starttls or tls") and an
// organization's own channel is refused when stored. A relay without login (the mailpit of the
// E2E overlay) and a login over starttls or tls pass.
func TestSMTPLoginNeedsTLS(t *testing.T) {
	for _, host := range []string{"smtp.example.org", "mailpit", "172.17.0.1", "host.docker.internal", "LOCALHOST",
		"localhost", "127.0.0.1", "::1"} {
		_, _, authErr := smtp.PlainAuth("", "mailer", "pw", host).Start(&smtp.ServerInfo{Name: host, Auth: []string{"PLAIN"}})
		cfg := SMTPConfig{Host: host, Port: 25, TLS: SMTPNone, Username: "mailer", Password: "pw", From: "noreply@example.org"}
		err := cfg.Validate()
		if (err == nil) != (authErr == nil) {
			t.Errorf("tls none with login to %s: Validate() = %v, net/smtp PlainAuth = %v; want both to accept or both to refuse",
				host, err, authErr)
		}
		if err != nil && (!errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "smtp authentication needs tls or starttls")) {
			t.Errorf("tls none with login to %s: Validate() = %v", host, err)
		}
	}
	for name, cfg := range map[string]SMTPConfig{
		"none without login (E2E mailpit)": {Host: "mailpit", Port: 1025, TLS: SMTPNone, From: "Reticora <noreply@reticora-e2e.de>"},
		"starttls with login":              {Host: "smtp.example.org", TLS: SMTPStartTLS, Username: "mailer", Password: "pw", From: "noreply@example.org"},
		"tls with login":                   {Host: "smtp.example.org", TLS: SMTPTLS, Username: "mailer", Password: "pw", From: "noreply@example.org"},
		"default mode with login":          {Host: "smtp.example.org", Username: "mailer", Password: "pw", From: "noreply@example.org"},
	} {
		if err := cfg.Validate(); err != nil {
			t.Errorf("%s: Validate() = %v", name, err)
		}
	}
	own := Channel{Name: "mail", Provider: ProviderSMTP, Config: map[string]any{"host": "smtp.example.org", "port": float64(25),
		"tls": "none", "username": "mailer", "password": "pw", "from": "cmdb@example.org"}}
	if err := own.Validate(); !errors.Is(err, ErrValidation) {
		t.Errorf("own SMTP channel with tls none and a login: Validate() = %v, want %v", err, ErrValidation)
	}
}
