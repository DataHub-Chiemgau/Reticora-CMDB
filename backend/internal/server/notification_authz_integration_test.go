// Notification administration under notification:manage; webhook url and
// delivery contents redacted (WP-398; F-154).
//
//   - TestAuditDefectNotificationAdministrationNeedsNotificationManage –
//     WP-139 / WP-137 (RBA-01, RBA-02, NTF-01; moved from
//     audit_t6_router_auditdefect_test.go): viewer and engineer get 403 on
//     GET /notification-deliveries, /notification-channels and
//     /notification-channels/{id}; the org_admin reads the webhook channel
//     with 200 and without the token of its url.
//   - TestNotificationAdministrationShowsNoTokensOrContents – WP-398
//     (NTF-01, NTF-04, API-05, RBA-02): webhook:read and webhook:manage
//     reach no channel or delivery route (403, nothing created); the
//     org_admin sees the webhook url as scheme and host, secret and
//     password as the placeholder; a PATCH with the placeholders keeps the
//     stored url (same sealed length) and answers masked again; the
//     delivery list names recipient, category, status,
//     attempts and error, but neither body nor data, so no invitation link
//     and no webhook token; the own inbox and preferences stay 200 for
//     every standard role.
//
// The tests run the complete router of audit_t6_harness_test.go on
// PostgreSQL.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -v -p 1 -run 'TestAuditDefectNotificationAdministration|TestNotificationAdministration' ./internal/server/
package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

func TestAuditDefectNotificationAdministrationNeedsNotificationManage(t *testing.T) {
	e := t6NewEnv(t, "ca", nil)
	f := e.F
	admin := t6As{f.OrgA, f.AppUser(t, f.OrgA, "audit-d9-admin"), t6RolePerms(t, "org_admin")}
	const token = "AUDITD9TOKENXYZ"
	r := e.Call(t, admin, http.MethodPost, "/api/v1/notification-channels",
		`{"name":"audit-d9-chat","provider":"webhook","config":{"url":"https://hooks.example.invalid/services/T0/B0/`+token+`"},"enabled":true}`)
	if r.Code != http.StatusCreated {
		t.Fatalf("org_admin POST /notification-channels = %d %s", r.Code, t6Short(r.Body))
	}
	channel := t6ID(r.Body)

	for _, role := range []string{"viewer", "engineer"} {
		caller := t6As{f.OrgA, f.AppUser(t, f.OrgA, "audit-d9-"+role), t6RolePerms(t, role)}
		for _, path := range []string{"/api/v1/notification-deliveries", "/api/v1/notification-channels", "/api/v1/notification-channels/" + channel} {
			t.Run(role+" GET "+path, func(t *testing.T) {
				r := e.Call(t, caller, http.MethodGet, path, ``)
				if r.Code != http.StatusForbidden {
					t.Errorf("%s GET %s = %d, want 403 (RBA-02: notification:manage is org_admin only); token leaked: %v",
						role, path, r.Code, strings.Contains(string(r.Body), token))
				}
			})
		}
	}
	t.Run("org_admin GET channel does not reveal the webhook token", func(t *testing.T) {
		r := e.Call(t, admin, http.MethodGet, "/api/v1/notification-channels/"+channel, ``)
		if r.Code != http.StatusOK {
			t.Fatalf("org_admin GET channel = %d", r.Code)
		}
		if strings.Contains(string(r.Body), token) {
			t.Errorf("channel response contains the incoming-webhook token: %s", t6Short(r.Body))
		}
	})
}

func TestNotificationAdministrationShowsNoTokensOrContents(t *testing.T) {
	e := t6NewEnv(t, "ca", nil)
	f := e.F
	bg := context.Background()
	admin := t6As{f.OrgA, f.AppUser(t, f.OrgA, "ntf-admin"), t6RolePerms(t, "org_admin")}
	const hookToken, queryToken, inviteToken = "NTFCAH00K", "NTFCAQUERY", "NTFCAINV1TE"
	const hookURL = "https://hooks.example.invalid/services/T0/B0/" + hookToken + "?key=" + queryToken
	r := e.Call(t, admin, http.MethodPost, "/api/v1/notification-channels",
		`{"name":"ntf-ca-chat","provider":"webhook","config":{"url":"`+hookURL+`","secret":"ntf-ca-hmac"},"enabled":true}`)
	if r.Code != http.StatusCreated {
		t.Fatalf("org_admin POST webhook channel = %d %s", r.Code, t6Short(r.Body))
	}
	hook := t6ID(r.Body)
	r = e.Call(t, admin, http.MethodPost, "/api/v1/notification-channels",
		`{"name":"ntf-ca-mail","provider":"smtp","enabled":true,"config":{"host":"mail.example.invalid","port":587,"tls":"starttls",
		  "from":"cmdb@example.invalid","username":"svc-ca","password":"ntf-ca-password"}}`)
	if r.Code != http.StatusCreated {
		t.Fatalf("org_admin POST smtp channel = %d %s", r.Code, t6Short(r.Body))
	}
	mail := t6ID(r.Body)
	secrets := []string{hookToken, queryToken, "ntf-ca-hmac", "ntf-ca-password"}

	t.Run("webhook permissions reach no channel or delivery route", func(t *testing.T) {
		hooker := t6As{f.OrgA, f.AppUser(t, f.OrgA, "ntf-hooker"), []identity.Permission{
			identity.PermWebhookRead, identity.PermWebhookManage, identity.PermCIRead}}
		for _, tc := range []struct{ method, path, body string }{
			{http.MethodGet, "/api/v1/notification-channels", ``},
			{http.MethodGet, "/api/v1/notification-channels/" + hook, ``},
			{http.MethodPost, "/api/v1/notification-channels", `{"name":"ntf-ca-hijack","provider":"inapp"}`},
			{http.MethodPatch, "/api/v1/notification-channels/" + hook, `{"config":{"url":"https://attacker.example.invalid/x"}}`},
			{http.MethodDelete, "/api/v1/notification-channels/" + mail, ``},
			{http.MethodGet, "/api/v1/notification-deliveries", ``},
		} {
			if r := e.Call(t, hooker, tc.method, tc.path, tc.body); r.Code != http.StatusForbidden {
				t.Errorf("webhook:read+manage %s %s = %d, want 403", tc.method, tc.path, r.Code)
			}
		}
		if n := e.Count(t, `SELECT count(*) FROM notification_channel WHERE organization_id = $1`, f.OrgA); n != 2 {
			t.Errorf("channels of organization A = %d, want 2 (nothing created or deleted)", n)
		}
	})

	t.Run("org_admin reads channels redacted and keeps them with the placeholders", func(t *testing.T) {
		for _, path := range []string{"/api/v1/notification-channels", "/api/v1/notification-channels/" + hook} {
			r := e.Call(t, admin, http.MethodGet, path, ``)
			if r.Code != http.StatusOK {
				t.Fatalf("GET %s = %d %s", path, r.Code, t6Short(r.Body))
			}
			for _, s := range secrets {
				if strings.Contains(string(r.Body), s) {
					t.Errorf("GET %s reveals %q: %s", path, s, t6Short(r.Body))
				}
			}
		}
		var ch struct {
			Config map[string]any `json:"config"`
		}
		_ = json.Unmarshal(e.Call(t, admin, http.MethodGet, "/api/v1/notification-channels/"+hook, ``).Body, &ch)
		if ch.Config["url"] != "https://hooks.example.invalid/********" || ch.Config["secret"] != "********" {
			t.Errorf("webhook config = %v, want scheme and host with masked path and query, secret as placeholder", ch.Config)
		}
		// The configuration is sealed; its length shows whether the url
		// with the token was kept or replaced by the shorter placeholder.
		sealed := func() int {
			return e.Count(t, `SELECT octet_length(config_ciphertext) FROM notification_channel WHERE id = $1`, hook)
		}
		before := sealed()
		r := e.Call(t, admin, http.MethodPatch, "/api/v1/notification-channels/"+hook,
			`{"name":"ntf-ca-chat-2","config":{"url":"https://hooks.example.invalid/********","secret":"********"}}`)
		if r.Code != http.StatusOK || !strings.Contains(string(r.Body), `"url":"https://hooks.example.invalid/********"`) {
			t.Errorf("PATCH with the placeholders = %d %s", r.Code, t6Short(r.Body))
		}
		for _, s := range secrets {
			if strings.Contains(string(r.Body), s) {
				t.Errorf("PATCH response reveals %q", s)
			}
		}
		if after := sealed(); after != before {
			t.Errorf("sealed configuration %d bytes after the placeholder PATCH, %d before: the stored url was replaced", after, before)
		}
	})

	t.Run("delivery list without body, data, links and tokens", func(t *testing.T) {
		link := "https://cmdb.example.invalid/invite/" + inviteToken
		if _, err := f.Admin.Exec(bg, `INSERT INTO notification_delivery (organization_id, channel_id, recipient_email, recipient_key,
				category, critical, subject, body, data, status, attempts)
			VALUES ($1, $2, 'invitee@example.invalid', 'mail:invitee@example.invalid', 'invitation', true, 'Einladung zu Reticora',
				$3, jsonb_build_object('link', $4::text), 'sent', 1)`,
			f.OrgA, mail, "Sie wurden eingeladen:\n"+link, link); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Admin.Exec(bg, `INSERT INTO notification_delivery (organization_id, channel_id, recipient_key, recipients,
				category, subject, body, status, attempts, max_attempts, last_error)
			VALUES ($1, $2, '*', '[{"email":"invitee@example.invalid"}]', 'invitation', 'Einladung zu Reticora', $3, 'failed', 5, 5, $4)`,
			f.OrgA, hook, link, `webhook: Post "`+hookURL+`": dial tcp: connection refused`); err != nil {
			t.Fatal(err)
		}
		r := e.Call(t, admin, http.MethodGet, "/api/v1/notification-deliveries", ``)
		if r.Code != http.StatusOK {
			t.Fatalf("org_admin GET /notification-deliveries = %d %s", r.Code, t6Short(r.Body))
		}
		for _, leak := range append(secrets, inviteToken, "/invite/", "Sie wurden eingeladen") {
			if strings.Contains(string(r.Body), leak) {
				t.Errorf("delivery list contains %q: %s", leak, t6Short(r.Body))
			}
		}
		var list struct {
			Data []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(r.Body, &list); err != nil || len(list.Data) != 2 {
			t.Fatalf("deliveries: %v %s", err, t6Short(r.Body))
		}
		for _, d := range list.Data {
			for _, k := range []string{"body", "data"} {
				if _, ok := d[k]; ok {
					t.Errorf("delivery %v returns %q", d["id"], k)
				}
			}
			switch d["channel_id"] {
			case mail:
				if rcpt, _ := d["recipient"].(map[string]any); rcpt["email"] != "invitee@example.invalid" ||
					d["category"] != "invitation" || d["status"] != "sent" || d["attempts"] != float64(1) {
					t.Errorf("e-mail delivery = %v", d)
				}
			case hook:
				if d["status"] != "failed" || d["attempts"] != float64(5) ||
					d["last_error"] != `webhook: Post "https://hooks.example.invalid/********": dial tcp: connection refused` {
					t.Errorf("webhook delivery = %v", d)
				}
			default:
				t.Errorf("unexpected delivery %v", d)
			}
		}
	})

	t.Run("own inbox and preferences stay 200 for every role", func(t *testing.T) {
		for _, role := range []string{"org_admin", "engineer", "viewer", "client_technician"} {
			caller := t6As{f.OrgA, f.AppUser(t, f.OrgA, "ntf-inbox-"+role), t6RolePerms(t, role)}
			for _, path := range []string{"/api/v1/notifications", "/api/v1/notifications/preferences"} {
				if r := e.Call(t, caller, http.MethodGet, path, ``); r.Code != http.StatusOK {
					t.Errorf("%s GET %s = %d %s, want 200", role, path, r.Code, t6Short(r.Body))
				}
			}
			if r := e.Call(t, caller, http.MethodPut, "/api/v1/notifications/preferences",
				`{"time_zone":"Europe/Berlin","muted_categories":["review"]}`); r.Code != http.StatusOK {
				t.Errorf("%s PUT /notifications/preferences = %d %s, want 200", role, r.Code, t6Short(r.Body))
			}
		}
	})
}
