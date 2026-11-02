// Notification administration only for org-wide holders of
// notification:manage (WP-689, F-418).
//
//   - TestNotificationAdministrationRequiresOrgWideScope – WP-689 (NTF-03,
//     NTF-04, TEN-06, RBA-03, API-05): notification channels and their
//     deliveries belong to the organization, not to a client; a channel
//     receives the notifications of every client and a delivery names
//     recipients of every client. A holder of notification:manage bound to
//     a client or a site, resolved from the database like the login
//     (permission.PGRepository.AccessGrants, identity.ResolveAccess), gets
//     403 on list, create, detail, update and delete of channels and on the
//     delivery list; nothing is created, changed or deleted, so no webhook
//     channel of such a holder receives the notifications of another client.
//     The same custom role assigned org-wide keeps 200/201/204; a user
//     without notification:manage gets 403.
//
// The test runs the complete router of audit_t6_harness_test.go on
// PostgreSQL.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -v -p 1 -run 'TestNotificationAdministrationRequiresOrgWideScope' ./internal/server/
package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

func TestNotificationAdministrationRequiresOrgWideScope(t *testing.T) {
	e := t6NewEnv(t, "ca", nil)
	f := e.F
	bg := context.Background()
	admin := t6As{f.OrgA, f.AppUser(t, f.OrgA, "wp689-admin"), t6RolePerms(t, "org_admin")}
	perms := permission.NewPGRepository(f.App)

	// fromDB resolves the principal of user like the login
	// (identity.Handler.applyAccess).
	fromDB := func(user string) identity.Principal {
		t.Helper()
		grants, err := perms.AccessGrants(bg, f.OrgA, user)
		if err != nil {
			t.Fatal(err)
		}
		acc := identity.ResolveAccess(grants)
		scope := acc.Scope
		return identity.Principal{Subject: user, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser, Scope: &scope,
			PermissionScopes: acc.PermissionScopes, Permissions: acc.Permissions, ClientScope: scope.LegacyClientScope(), Credential: "wp689"}
	}
	call := func(p identity.Principal, method, path, body string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), p.Permissions)
		w := httptest.NewRecorder()
		e.handler.ServeHTTP(w, req.WithContext(ctx))
		return w.Code, strings.TrimSpace(w.Body.String())
	}
	mustCreate := func(path, body string) string {
		t.Helper()
		r := e.Call(t, admin, http.MethodPost, path, body)
		if r.Code != http.StatusCreated {
			t.Fatalf("org_admin POST %s = %d %s", path, r.Code, t6Short(r.Body))
		}
		return t6ID(r.Body)
	}

	// The in-app channel of the organization and a delivery naming a
	// recipient of client 2.
	center := mustCreate("/api/v1/notification-channels", `{"name":"wp689-center","provider":"inapp","enabled":true}`)
	c2User := f.AppUser(t, f.OrgA, "wp689-c2-reviewer")
	if _, err := f.Admin.Exec(bg, `INSERT INTO notification_delivery (organization_id, channel_id, recipient_user_id, recipient_email,
		recipient_name, recipient_key, category, subject, status) VALUES ($1, $2, $3::uuid, 'wp689-c2@example.invalid', 'wp689-c2-reviewer',
		'user:'||$3::text, 'review', 'wp689 review in client 2', 'sent')`, f.OrgA, center, c2User); err != nil {
		t.Fatal(err)
	}

	// The custom role {notification:manage, webhook:manage} through the API,
	// assigned in client 1, in site 1 of client 1 and org-wide.
	role := mustCreate("/api/v1/roles", `{"name":"wp689-ntf-mgr","permissions":["notification:manage","webhook:manage"]}`)
	var siteID string
	room := f.Room(t, f.Client1, "wp689-s1")
	if err := f.Admin.QueryRow(bg, `SELECT b.site_id::text FROM room r JOIN building b ON b.id = r.building_id WHERE r.id = $1`, room).Scan(&siteID); err != nil {
		t.Fatal(err)
	}
	assign := func(name, scope string) identity.Principal {
		t.Helper()
		user := f.AppUser(t, f.OrgA, name)
		mustCreate("/api/v1/role-assignments", `{"user_id":"`+user+`","custom_role_id":"`+role+`"`+scope+`}`)
		p := fromDB(user)
		if !p.Has(identity.PermNotificationManage) {
			t.Fatalf("%s: the resolved principal lacks notification:manage: %v", name, p.Permissions)
		}
		return p
	}
	clientMgr := assign("wp689-c1-mgr", `,"scope_type":"client","scope_id":"`+f.Client1+`"`)
	siteMgr := assign("wp689-s1-mgr", `,"scope_type":"site","scope_id":"`+siteID+`"`)
	orgMgr := assign("wp689-org-mgr", ``)
	if clientMgr.Scope.Clients.All || siteMgr.Scope.Sites.All || !orgMgr.Scope.OrgWide() {
		t.Fatalf("resolved scopes: client %+v, site %+v, org %+v", clientMgr.Scope, siteMgr.Scope, orgMgr.Scope)
	}
	viewer := t6As{f.OrgA, f.AppUser(t, f.OrgA, "wp689-viewer"), t6RolePerms(t, "viewer")}

	channels := func() int {
		return e.Count(t, `SELECT count(*) FROM notification_channel WHERE organization_id = $1`, f.OrgA)
	}
	centerEnabled := func() int {
		return e.Count(t, `SELECT count(*) FROM notification_channel WHERE id = $1 AND enabled AND name = 'wp689-center'`, center)
	}
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/notification-deliveries", ""},
		{http.MethodGet, "/api/v1/notification-channels", ""},
		{http.MethodPost, "/api/v1/notification-channels", `{"name":"wp689-hook","provider":"webhook","enabled":true,"config":{"url":"https://93.184.216.34/wp689"}}`},
		{http.MethodGet, "/api/v1/notification-channels/" + center, ""},
		{http.MethodPatch, "/api/v1/notification-channels/" + center, `{"enabled":false,"name":"wp689-renamed"}`},
		{http.MethodDelete, "/api/v1/notification-channels/" + center, ""},
	}

	// Scope-bound holders and the viewer: 403 on every route, nothing
	// created, changed or deleted, no recipient of client 2 disclosed.
	refused := map[string]identity.Principal{"client-1 holder": clientMgr, "site-1 holder": siteMgr}
	for name, p := range refused {
		for _, r := range routes {
			t.Run(name+" "+r.method+" "+r.path, func(t *testing.T) {
				before := channels()
				code, body := call(p, r.method, r.path, r.body)
				if code != http.StatusForbidden {
					t.Errorf("%s %s as the %s (notification:manage in a scope) = %d, want 403 (only an org-wide holder administers channels and reads deliveries): %s",
						r.method, r.path, name, code, t6ShortS(body))
				}
				if strings.Contains(body, "wp689-c2") || strings.Contains(body, "wp689-center") {
					t.Errorf("%s %s as the %s discloses channel or recipient: %s", r.method, r.path, name, t6ShortS(body))
				}
				if got := channels(); got != before {
					t.Errorf("%s %s as the %s: %d channels, before %d", r.method, r.path, name, got, before)
				}
				if centerEnabled() != 1 {
					t.Errorf("%s %s as the %s changed or deleted the in-app channel of the organization", r.method, r.path, name)
				}
			})
		}
	}
	for _, r := range routes {
		if rr := e.Call(t, viewer, r.method, r.path, r.body); rr.Code != http.StatusForbidden {
			t.Errorf("control: %s %s as viewer (without notification:manage) = %d, want 403", r.method, r.path, rr.Code)
		}
	}
	if n := e.Count(t, `SELECT count(*) FROM notification_channel WHERE organization_id = $1 AND name = 'wp689-hook'`, f.OrgA); n != 0 {
		t.Fatalf("refused holders created %d webhook channels", n)
	}

	// Positive control: the same role assigned org-wide administers the
	// channels and reads the deliveries.
	want := []int{http.StatusOK, http.StatusOK, http.StatusCreated, http.StatusOK, http.StatusOK, http.StatusNoContent}
	for i, r := range routes {
		code, body := call(orgMgr, r.method, r.path, r.body)
		if code != want[i] {
			t.Errorf("control: %s %s as the org-wide holder = %d, want %d: %s", r.method, r.path, code, want[i], t6ShortS(body))
		}
		if i == 0 && !strings.Contains(body, "wp689-c2@example.invalid") {
			t.Errorf("control: the org-wide holder does not read the delivery to the client-2 recipient: %s", t6ShortS(body))
		}
		if i == 1 && !strings.Contains(body, center) {
			t.Errorf("control: the org-wide holder does not list the in-app channel: %s", t6ShortS(body))
		}
	}
	if n := e.Count(t, `SELECT count(*) FROM notification_channel WHERE organization_id = $1 AND name = 'wp689-hook'`, f.OrgA); n != 1 {
		t.Errorf("the org-wide holder's webhook channel: %d rows, want 1", n)
	}
	if n := e.Count(t, `SELECT count(*) FROM notification_channel WHERE id = $1`, center); n != 0 {
		t.Errorf("the org-wide holder's DELETE left the in-app channel")
	}
}

// t6ShortS shortens a response body string for messages.
func t6ShortS(s string) string { return t6Short([]byte(s)) }
