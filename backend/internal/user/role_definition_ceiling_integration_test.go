// Ceiling of narrowing a role definition (WP-667, RBA-03, TEN-09; finding
// F-396): whoever may not revoke an assignment (DELETE
// /role-assignments/{id}, WP-395) may not reach the same effect over the
// role definition. Removing permissions from a role (PATCH /roles/{id},
// PUT /roles/{id}/permissions) or deleting it (DELETE /roles/{id}) takes
// them from every assignment of the role, so the caller must pass the
// revocation ceiling of each one: role:manage and every permission of the
// role in a scope covering the assignment's scope. Otherwise 403 and nothing
// changes.
//
// The test runs through the production router behind the production
// authentication and tenant middleware (revokeStack); sessions come from the
// stored assignments via POST /api/v1/auth/refresh.
//
// Retest (needs a migrated database):
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -v -run '^TestRoleDefinitionChangeNeverReachesBeyondCallerScope$' ./internal/user/
package user_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// TestRoleDefinitionChangeNeverReachesBeyondCallerScope covers WP-667
// (F-396). A client-1 delegate (org_admin with scope_client_id = client 1)
// cannot revoke the org-wide assignment of a custom role (403, the WP-395
// symmetry control) and therefore gets 403 with everything unchanged for:
// PATCH /roles/{id} removing all or one permission of a custom role assigned
// org-wide, in client 2 or in a site of client 2; DELETE /roles/{id} of such
// a role (the user_custom_role rows stay); PUT /roles/{id}/permissions
// removing a key the org-wide admin added to the standard role viewer
// (assigned org-wide to a user) or to engineer (assigned org-wide to a
// service account only). The victim keeps ci:read at the next refresh. The
// delegate may still rename such a role, narrow and delete a custom role
// whose assignments lie in client 1, and delete an unassigned one; the
// org-wide org_admin narrows and deletes every role (200/204).
func TestRoleDefinitionChangeNeverReachesBeyondCallerScope(t *testing.T) {
	s := newRevokeStack(t, "7a")
	delegate := s.AppUser(t, s.OrgA, "narrow-delegate")
	s.grant(s.OrgA, delegate, "org_admin", s.Client1)
	victim := s.AppUser(t, s.OrgA, "narrow-victim")
	adminToken, _ := s.login(s.OrgA, s.admin)
	delegateToken, _ := s.login(s.OrgA, delegate)
	site2 := s.site(s.Client2, "narrow-site-c2")

	both := `["ci:read", "ci:write"]`
	orgWide := s.customRole("narrow-org", both)
	orgWideDel := s.customRole("narrow-org-delete", both)
	client2 := s.customRole("narrow-client2", both)
	siteC2 := s.customRole("narrow-site2", both)
	client1 := s.customRole("narrow-client1", both)
	unassigned := s.customRole("narrow-unassigned", both)
	orgAssignment := s.assign(adminToken, `{"user_id":"`+victim+`","custom_role_id":"`+orgWide+`","scope_type":"organization"}`)
	s.assign(adminToken, `{"user_id":"`+victim+`","custom_role_id":"`+orgWideDel+`","scope_type":"organization"}`)
	s.assign(adminToken, `{"user_id":"`+victim+`","custom_role_id":"`+client2+`","scope_type":"client","scope_id":"`+s.Client2+`"}`)
	s.assign(adminToken, `{"user_id":"`+victim+`","custom_role_id":"`+siteC2+`","scope_type":"site","scope_id":"`+site2+`"}`)
	s.assign(adminToken, `{"user_id":"`+victim+`","custom_role_id":"`+client1+`","scope_type":"client","scope_id":"`+s.Client1+`"}`)

	// Standard roles: the org-wide admin adds a key to viewer (assigned
	// org-wide to the victim) and to engineer (assigned org-wide to a
	// service account only).
	viewer, engineer := s.role(s.OrgA, "viewer"), s.role(s.OrgA, "engineer")
	s.grant(s.OrgA, victim, "viewer", "")
	account := s.str(`INSERT INTO service_account (organization_id, name) VALUES ($1, 'narrow-sa') RETURNING id::text`, s.OrgA)
	s.str(`INSERT INTO service_account_role (organization_id, service_account_id, role_id) VALUES ($1, $2, $3) RETURNING id::text`,
		s.OrgA, account, engineer)
	standardKeys := func(role string) []string {
		var keys []string
		if err := s.Admin.QueryRow(t.Context(), `SELECT ARRAY(SELECT permission_key FROM role_permission WHERE role_id = $1 ORDER BY 1)`,
			role).Scan(&keys); err != nil {
			t.Fatal(err)
		}
		return keys
	}
	keysBody := func(keys []string) string {
		b, err := json.Marshal(map[string]any{"permission_keys": keys})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	viewerSeed, engineerSeed := standardKeys(viewer), standardKeys(engineer)
	viewerExtra := append(slices.Clone(viewerSeed), "location:write")
	engineerExtra := append(slices.Clone(engineerSeed), "webhook:manage")
	for role, keys := range map[string][]string{viewer: viewerExtra, engineer: engineerExtra} {
		if w := s.do(adminToken, http.MethodPut, "/api/v1/roles/"+role+"/permissions", keysBody(keys)); w.Code != http.StatusOK {
			t.Fatalf("fixture: admin PUT /roles/%s/permissions = %d %.200s", role, w.Code, w.Body.String())
		}
	}

	customKeys := func(role string) string {
		return s.str(`SELECT COALESCE((SELECT permissions::text FROM custom_role WHERE id = $1), 'deleted')`, role)
	}
	assignments := func(role string) int {
		return s.count(`SELECT count(*)::int FROM user_custom_role WHERE custom_role_id = $1`, role)
	}
	refused := func(name string, w *httptest.ResponseRecorder, unchanged bool, state string) {
		t.Helper()
		if !problem(w, http.StatusForbidden) || !unchanged {
			t.Errorf("%s = %d %.220s; state afterwards %s; want 403 (problem+json) and the role and its assignments unchanged",
				name, w.Code, w.Body.String(), state)
		}
	}

	// Symmetry control (WP-395): the delegate cannot revoke the org-wide
	// assignment itself.
	if w := s.revoke(delegateToken, orgAssignment); w.Code != http.StatusForbidden || !s.stored(orgAssignment) {
		t.Fatalf("control: delegate DELETE /role-assignments/{org-wide} = %d %.200s, want 403 and the row kept", w.Code, w.Body.String())
	}

	// The same effect over the role definition is refused.
	for _, c := range []struct{ name, role, body string }{
		{"delegate PATCH org-wide role {permissions:[]}", orgWide, `{"permissions":[]}`},
		{"delegate PATCH org-wide role {permissions:[ci:read]}", orgWide, `{"permissions":["ci:read"]}`},
		{"delegate PATCH client-2 role {permissions:[]}", client2, `{"permissions":[]}`},
		{"delegate PATCH site-of-client-2 role {permissions:[ci:read]}", siteC2, `{"permissions":["ci:read"]}`},
	} {
		w := s.do(delegateToken, http.MethodPatch, "/api/v1/roles/"+c.role, c.body)
		refused(c.name, w, customKeys(c.role) == `["ci:read", "ci:write"]`, customKeys(c.role))
	}
	for _, role := range []string{orgWideDel, client2, siteC2} {
		w := s.do(delegateToken, http.MethodDelete, "/api/v1/roles/"+role, "")
		refused("delegate DELETE /roles/{"+role+"}", w, customKeys(role) != "deleted" && assignments(role) == 1, customKeys(role))
	}
	for _, c := range []struct {
		name, role string
		keys, want []string
	}{
		{"delegate PUT viewer without the added location:write", viewer, viewerSeed, viewerExtra},
		{"delegate PUT engineer (service account org-wide) without the added webhook:manage", engineer, engineerSeed, engineerExtra},
	} {
		w := s.do(delegateToken, http.MethodPut, "/api/v1/roles/"+c.role+"/permissions", keysBody(c.keys))
		got := standardKeys(c.role)
		slices.Sort(c.want)
		refused(c.name, w, slices.Equal(got, c.want), strings.Join(got, ","))
	}

	// The victim keeps the rights at the next refresh (ci:write comes from
	// the org-wide custom roles only, location:write from viewer's added key).
	victimToken, _ := s.login(s.OrgA, victim)
	if !s.holds(victimToken, identity.PermCIWrite) || !s.holds(victimToken, identity.Permission("location:write")) {
		t.Errorf("victim lost ci:write or location:write at the next refresh after the refused changes")
	}

	// What the delegate may still do.
	if w := s.do(delegateToken, http.MethodPatch, "/api/v1/roles/"+orgWide, `{"name":"narrow-org-renamed"}`); w.Code != http.StatusOK {
		t.Errorf("delegate renames the org-wide role = %d %.200s, want 200 (no permission removed)", w.Code, w.Body.String())
	}
	if w := s.do(delegateToken, http.MethodPatch, "/api/v1/roles/"+client1, `{"permissions":["ci:read"]}`); w.Code != http.StatusOK ||
		customKeys(client1) != `["ci:read"]` {
		t.Errorf("delegate narrows the client-1 role = %d %.200s (permissions %s), want 200 with [ci:read]", w.Code, w.Body.String(), customKeys(client1))
	}
	if w := s.do(delegateToken, http.MethodDelete, "/api/v1/roles/"+client1, ""); w.Code != http.StatusNoContent || customKeys(client1) != "deleted" {
		t.Errorf("delegate deletes the client-1 role = %d %.200s, want 204", w.Code, w.Body.String())
	}
	if w := s.do(delegateToken, http.MethodDelete, "/api/v1/roles/"+unassigned, ""); w.Code != http.StatusNoContent {
		t.Errorf("delegate deletes an unassigned role = %d %.200s, want 204", w.Code, w.Body.String())
	}

	// The org-wide org_admin keeps 200/204.
	if w := s.do(adminToken, http.MethodPatch, "/api/v1/roles/"+orgWide, `{"permissions":[]}`); w.Code != http.StatusOK || customKeys(orgWide) != `[]` {
		t.Errorf("admin PATCH org-wide role {permissions:[]} = %d %.200s, want 200", w.Code, w.Body.String())
	}
	for _, role := range []string{orgWideDel, client2, siteC2} {
		if w := s.do(adminToken, http.MethodDelete, "/api/v1/roles/"+role, ""); w.Code != http.StatusNoContent || assignments(role) != 0 {
			t.Errorf("admin DELETE /roles/{%s} = %d %.200s, want 204 without assignments", role, w.Code, w.Body.String())
		}
	}
	for role, keys := range map[string][]string{viewer: viewerSeed, engineer: engineerSeed} {
		if w := s.do(adminToken, http.MethodPut, "/api/v1/roles/"+role+"/permissions", keysBody(keys)); w.Code != http.StatusOK ||
			!slices.Equal(standardKeys(role), keys) {
			t.Errorf("admin PUT /roles/%s/permissions back to the seed = %d %.200s, want 200", role, w.Code, w.Body.String())
		}
	}
}
