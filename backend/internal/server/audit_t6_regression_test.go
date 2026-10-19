// Phase-1 acceptance audit, group T6-epic-b – REGRESSION tests through the
// production router (must pass). Harness: audit_t6_harness_test.go.
//
//   - TestAuditT6AttributeDefinitionsAuthzAndIsolation
//     WP-116 (DB-03, MET-10, API-05; RBA-01, TEN-05, TEN-09)
//     Gaps (high): "Authz through the real middleware stack" – a principal
//     with only ci:read lists and reads definitions but every write is 403
//     and changes nothing; ci_attribute:manage may write (201/200/204).
//     "Cross-tenant isolation of attribute-definitions exists only as an
//     audit probe" – organization B gets 404 on A's definition, 422 when it
//     attaches a definition to A's type, does not list A's keys, and A's row
//     stays unchanged.
//   - TestAuditT6NotificationChannelsAndInbox
//     WP-139 (NTF-04, API-05), WP-137 (NTF-01: config encrypted)
//     Gap (high): "Router-level PG API test for channels, deliveries and
//     inbox incl. RBAC and isolation" – positive and isolation part:
//     org_admin CRUD 201/200/200/204 with a redacted response and only
//     ciphertext in the database; organization B gets 404 on A's channel;
//     GET /notifications lists only the caller's own items and marking
//     another user's item read is 404. (The viewer/engineer 403 part is a
//     defect: audit_t6_router_auditdefect_test.go
//     TestAuditDefectNotificationAdministrationNeedsNotificationManage.)
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_11?sslmode=disable \
//	  go test -count=1 -v -run 'TestAuditT6' ./internal/server/
package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

func TestAuditT6AttributeDefinitionsAuthzAndIsolation(t *testing.T) {
	e := t6NewEnv(t, "de", nil)
	f := e.F
	bg := context.Background()
	adminA := t6As{f.OrgA, f.AppUser(t, f.OrgA, "audit-de-admin"), identity.AllPermissions()}
	readerA := t6As{f.OrgA, f.AppUser(t, f.OrgA, "audit-de-reader"), []identity.Permission{identity.PermCIRead}}
	managerA := t6As{f.OrgA, f.AppUser(t, f.OrgA, "audit-de-manager"),
		[]identity.Permission{identity.PermCIRead, identity.PermCIAttributeManage}}
	adminB := t6As{f.OrgB, f.AppUser(t, f.OrgB, "audit-de-b"), identity.AllPermissions()}

	var typeA string
	if err := f.Admin.QueryRow(bg, `INSERT INTO ci_type (organization_id, key, name) VALUES ($1, 'audit-de-type', 'audit-de-type') RETURNING id::text`,
		f.OrgA).Scan(&typeA); err != nil {
		t.Fatal(err)
	}
	r := e.Call(t, adminA, http.MethodPost, "/api/v1/attribute-definitions",
		`{"ci_type_id":"`+typeA+`","key":"audit_de_ports","data_type":"integer","required":true,"validation":{"min":1,"max":96}}`)
	if r.Code != http.StatusCreated {
		t.Fatalf("org A admin POST /attribute-definitions = %d %s", r.Code, t6Short(r.Body))
	}
	def := t6ID(r.Body)
	type row struct {
		Required bool
		Updated  string
		Key      string
	}
	stored := func() row {
		t.Helper()
		var s row
		if err := f.Admin.QueryRow(bg, `SELECT required, updated_at::text, key FROM attribute_definition WHERE id = $1`, def).
			Scan(&s.Required, &s.Updated, &s.Key); err != nil {
			t.Fatalf("definition %s: %v", def, err)
		}
		return s
	}
	initial := stored()
	definitions := func() int {
		return e.Count(t, `SELECT count(*) FROM attribute_definition WHERE organization_id IN ($1, $2)`, f.OrgA, f.OrgB)
	}
	listKeys := func(as t6As) []string {
		t.Helper()
		r := e.Call(t, as, http.MethodGet, "/api/v1/attribute-definitions?limit=500", ``)
		if r.Code != http.StatusOK {
			t.Fatalf("GET /attribute-definitions = %d %s", r.Code, t6Short(r.Body))
		}
		var list struct {
			Data []struct {
				Key string `json:"key"`
			} `json:"data"`
		}
		if err := json.Unmarshal(r.Body, &list); err != nil {
			t.Fatalf("list body: %v", err)
		}
		keys := make([]string, 0, len(list.Data))
		for _, d := range list.Data {
			keys = append(keys, d.Key)
		}
		return keys
	}
	contains := func(keys []string, key string) bool {
		for _, k := range keys {
			if k == key {
				return true
			}
		}
		return false
	}

	t.Run("ci:read lists and reads", func(t *testing.T) {
		if !contains(listKeys(readerA), "audit_de_ports") {
			t.Errorf("reader list does not contain audit_de_ports")
		}
		if r := e.Call(t, readerA, http.MethodGet, "/api/v1/attribute-definitions/"+def, ``); r.Code != http.StatusOK {
			t.Errorf("reader GET definition = %d", r.Code)
		}
	})

	before := definitions()
	for _, tc := range []struct {
		name         string
		as           t6As
		method, path string
		body         string
		want         int
	}{
		{"ci:read POST", readerA, http.MethodPost, "/api/v1/attribute-definitions",
			`{"ci_type_id":"` + typeA + `","key":"audit_de_reader","data_type":"text"}`, http.StatusForbidden},
		{"ci:read PATCH", readerA, http.MethodPatch, "/api/v1/attribute-definitions/" + def, `{"required":false}`, http.StatusForbidden},
		{"ci:read DELETE", readerA, http.MethodDelete, "/api/v1/attribute-definitions/" + def, ``, http.StatusForbidden},
		{"org B GET", adminB, http.MethodGet, "/api/v1/attribute-definitions/" + def, ``, http.StatusNotFound},
		{"org B PATCH", adminB, http.MethodPatch, "/api/v1/attribute-definitions/" + def, `{"required":false}`, http.StatusNotFound},
		{"org B DELETE", adminB, http.MethodDelete, "/api/v1/attribute-definitions/" + def, ``, http.StatusNotFound},
		{"org B POST on org A's type", adminB, http.MethodPost, "/api/v1/attribute-definitions",
			`{"ci_type_id":"` + typeA + `","key":"audit_de_b","data_type":"text"}`, http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name+" is refused", func(t *testing.T) {
			if r := e.Call(t, tc.as, tc.method, tc.path, tc.body); r.Code != tc.want {
				t.Errorf("%s %s = %d, want %d: %s", tc.method, tc.path, r.Code, tc.want, t6Short(r.Body))
			}
		})
	}
	t.Run("refused writes changed nothing", func(t *testing.T) {
		if after := definitions(); after != before {
			t.Errorf("attribute definitions of A and B = %d after refused writes, want %d", after, before)
		}
		if got := stored(); got != initial {
			t.Errorf("org A definition after refused writes = %+v, want %+v", got, initial)
		}
		if contains(listKeys(adminB), "audit_de_ports") {
			t.Errorf("organization B lists org A's definition key audit_de_ports")
		}
	})

	t.Run("ci_attribute:manage writes", func(t *testing.T) {
		if r := e.Call(t, managerA, http.MethodPatch, "/api/v1/attribute-definitions/"+def, `{"required":false}`); r.Code != http.StatusOK {
			t.Fatalf("manager PATCH = %d %s", r.Code, t6Short(r.Body))
		}
		if got := stored(); got.Required {
			t.Errorf("required after manager PATCH = true")
		}
		r := e.Call(t, managerA, http.MethodPost, "/api/v1/attribute-definitions", `{"ci_type_id":"`+typeA+`","key":"audit_de_label","data_type":"text"}`)
		if r.Code != http.StatusCreated {
			t.Fatalf("manager POST = %d %s", r.Code, t6Short(r.Body))
		}
		if r := e.Call(t, managerA, http.MethodDelete, "/api/v1/attribute-definitions/"+def, ``); r.Code != http.StatusNoContent {
			t.Fatalf("manager DELETE = %d %s", r.Code, t6Short(r.Body))
		}
		if n := e.Count(t, `SELECT count(*) FROM attribute_definition WHERE id = $1`, def); n != 0 {
			t.Errorf("definition rows after DELETE = %d", n)
		}
	})
}

func TestAuditT6NotificationChannelsAndInbox(t *testing.T) {
	e := t6NewEnv(t, "df", nil)
	f := e.F
	bg := context.Background()
	adminA := t6As{f.OrgA, f.AppUser(t, f.OrgA, "audit-df-admin"), t6RolePerms(t, "org_admin")}
	adminB := t6As{f.OrgB, f.AppUser(t, f.OrgB, "audit-df-b"), t6RolePerms(t, "org_admin")}
	const secret = "audit-df-smtp-secret-1"

	r := e.Call(t, adminA, http.MethodPost, "/api/v1/notification-channels",
		`{"name":"audit-df-mail","provider":"smtp","enabled":true,
		  "config":{"host":"mail.audit-df.example.invalid","port":587,"tls":"starttls","from":"cmdb@audit-df.example.invalid",
		            "username":"svc-df","password":"`+secret+`"}}`)
	if r.Code != http.StatusCreated {
		t.Fatalf("org_admin POST /notification-channels = %d %s", r.Code, t6Short(r.Body))
	}
	channel := t6ID(r.Body)
	if strings.Contains(string(r.Body), secret) {
		t.Errorf("create response reveals the SMTP password")
	}

	t.Run("database holds only ciphertext", func(t *testing.T) {
		var cipher []byte
		var dek []byte
		if err := f.Admin.QueryRow(bg, `SELECT config_ciphertext, config_dek FROM notification_channel WHERE id = $1`, channel).Scan(&cipher, &dek); err != nil {
			t.Fatal(err)
		}
		if len(cipher) == 0 || len(dek) == 0 {
			t.Fatalf("config_ciphertext %d bytes, config_dek %d bytes: config not sealed", len(cipher), len(dek))
		}
		for _, plain := range []string{secret, "svc-df", "mail.audit-df.example.invalid"} {
			if strings.Contains(string(cipher), plain) {
				t.Errorf("config_ciphertext contains plaintext %q", plain)
			}
		}
		var leak int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM notification_channel c WHERE c.id = $1 AND row_to_json(c)::text LIKE '%' || $2 || '%'`,
			channel, secret).Scan(&leak); err != nil {
			t.Fatal(err)
		}
		if leak != 0 {
			t.Errorf("notification_channel row contains the SMTP password in plaintext")
		}
	})

	t.Run("org_admin reads and renames with redaction", func(t *testing.T) {
		for _, path := range []string{"/api/v1/notification-channels", "/api/v1/notification-channels/" + channel} {
			r := e.Call(t, adminA, http.MethodGet, path, ``)
			if r.Code != http.StatusOK {
				t.Fatalf("GET %s = %d", path, r.Code)
			}
			if !strings.Contains(string(r.Body), channel) {
				t.Errorf("GET %s does not return channel %s", path, channel)
			}
			if strings.Contains(string(r.Body), secret) {
				t.Errorf("GET %s reveals the SMTP password", path)
			}
		}
		r := e.Call(t, adminA, http.MethodPatch, "/api/v1/notification-channels/"+channel, `{"name":"audit-df-mail-renamed"}`)
		if r.Code != http.StatusOK || strings.Contains(string(r.Body), secret) {
			t.Errorf("PATCH name = %d (password revealed: %v)", r.Code, strings.Contains(string(r.Body), secret))
		}
		if n := e.Count(t, `SELECT count(*) FROM notification_channel WHERE id = $1 AND name = 'audit-df-mail-renamed'`, channel); n != 1 {
			t.Errorf("renamed channel rows = %d", n)
		}
	})

	t.Run("organization B cannot reach org A's channel", func(t *testing.T) {
		for _, tc := range []struct{ method, body string }{
			{http.MethodGet, ``}, {http.MethodPatch, `{"name":"audit-df-hijacked"}`}, {http.MethodDelete, ``},
		} {
			if r := e.Call(t, adminB, tc.method, "/api/v1/notification-channels/"+channel, tc.body); r.Code != http.StatusNotFound {
				t.Errorf("org B %s channel of org A = %d, want 404", tc.method, r.Code)
			}
		}
		r := e.Call(t, adminB, http.MethodGet, "/api/v1/notification-channels", ``)
		if r.Code != http.StatusOK || strings.Contains(string(r.Body), channel) {
			t.Errorf("org B list = %d, contains org A's channel: %v", r.Code, strings.Contains(string(r.Body), channel))
		}
		if n := e.Count(t, `SELECT count(*) FROM notification_channel WHERE id = $1 AND name = 'audit-df-mail-renamed'`, channel); n != 1 {
			t.Errorf("org A channel changed by org B")
		}
	})

	t.Run("inbox lists only own items", func(t *testing.T) {
		other := t6As{f.OrgA, f.AppUser(t, f.OrgA, "audit-df-other"), t6RolePerms(t, "viewer")}
		inbox := func(user, subject string) string {
			t.Helper()
			var id string
			if err := f.Admin.QueryRow(bg, `INSERT INTO notification_inbox (organization_id, user_id, category, subject) VALUES ($1, $2, 'export', $3) RETURNING id::text`,
				f.OrgA, user, subject).Scan(&id); err != nil {
				t.Fatal(err)
			}
			return id
		}
		inbox(adminA.User, "audit-df-own")
		foreign := inbox(other.User, "audit-df-foreign")

		r := e.Call(t, adminA, http.MethodGet, "/api/v1/notifications", ``)
		if r.Code != http.StatusOK {
			t.Fatalf("GET /notifications = %d %s", r.Code, t6Short(r.Body))
		}
		if !strings.Contains(string(r.Body), "audit-df-own") || strings.Contains(string(r.Body), "audit-df-foreign") {
			t.Errorf("GET /notifications = %s; want only the caller's own item", t6Short(r.Body))
		}
		if r := e.Call(t, adminA, http.MethodPost, "/api/v1/notifications/"+foreign+"/read", ``); r.Code != http.StatusNotFound {
			t.Errorf("marking another user's notification read = %d, want 404", r.Code)
		}
		if n := e.Count(t, `SELECT count(*) FROM notification_inbox WHERE id = $1 AND read_at IS NULL`, foreign); n != 1 {
			t.Errorf("other user's notification was marked read")
		}
		if r := e.Call(t, other, http.MethodPost, "/api/v1/notifications/"+foreign+"/read", ``); r.Code != http.StatusNoContent {
			t.Errorf("owner marks own notification read = %d, want 204", r.Code)
		}
	})

	t.Run("org_admin deletes the channel", func(t *testing.T) {
		if r := e.Call(t, adminA, http.MethodDelete, "/api/v1/notification-channels/"+channel, ``); r.Code != http.StatusNoContent {
			t.Fatalf("DELETE channel = %d %s", r.Code, t6Short(r.Body))
		}
		if n := e.Count(t, `SELECT count(*) FROM notification_channel WHERE id = $1`, channel); n != 0 {
			t.Errorf("channel rows after DELETE = %d", n)
		}
	})
}
