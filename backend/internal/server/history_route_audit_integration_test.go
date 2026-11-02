package server_test

// Audit part of the CI history (WP-681, k7n-ci-history-route-audit, F-410;
// WP-171 AK1, WP-475, CI-06, E-78): through the production router with the
// PostgreSQL route auditor, the writes on the sub-resources of a CI are
// audited by the route audit under the route segment "cis" (resource_id the
// CI) or, for its contact links and network interfaces, under
// "ci-contacts" and "network-interfaces" (resource_id the link or the
// interface, the CI in the values). GET /history/ci/{id} with audit:read
// shows every one of them next to the entries of the domain recorder
// ("ci"); a principal restricted to a client reads only the entries the
// audit list shows it (WP-660).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… TEST_APP_DATABASE_URL=… \
//	  go test -count=1 -v -run '^TestCIHistoryIncludesRouteAuditOfCISubresources$' ./internal/server/

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// TestCIHistoryIncludesRouteAuditOfCISubresources is the coding test of
// F-410: on e5c2d86 the unified history of a CI lists only the audit
// entries of resource_type ci (PATCH /cis/{id}) and misses the route
// entries of POST /cis/{id}/interfaces and /contacts (resource_type cis)
// and those of PATCH and DELETE /network-interfaces/{id} and DELETE
// /ci-contacts/{id}, which GET /audit-log lists. Now the org-wide reader
// sees all of them and none about another CI's interface; the client-1
// reader sees the entries about the CI that the audit list shows it, and
// the CI of client 2 stays 404.
func TestCIHistoryIncludesRouteAuditOfCISubresources(t *testing.T) {
	e := t6NewEnv(t, "6c", nil)
	f := e.F
	admin := t6As{f.OrgA, f.AppUser(t, f.OrgA, "hra-admin"), identity.AllPermissions()}
	call := func(method, path, body string, want int) string {
		t.Helper()
		r := e.Call(t, admin, method, path, body)
		if r.Code != want {
			t.Fatalf("%s %s = %d %s, want %d", method, path, r.Code, t6Short(r.Body), want)
		}
		return t6ID(r.Body)
	}
	id := f.CI(t, f.OrgA, f.Client1, "hra-ci")
	other := f.CI(t, f.OrgA, f.Client1, "hra-other-ci")
	foreign := f.CI(t, f.OrgA, f.Client2, "hra-ci-c2")
	contact := call(http.MethodPost, "/api/v1/contacts", fmt.Sprintf(`{"display_name":"HRA Contact","client_id":%q}`, f.Client1), http.StatusCreated)

	call(http.MethodPatch, "/api/v1/cis/"+id, `{"name":"hra-ci-renamed"}`, http.StatusOK)
	eth1 := call(http.MethodPost, "/api/v1/cis/"+id+"/interfaces", `{"name":"hra-eth1"}`, http.StatusCreated)
	eth2 := call(http.MethodPost, "/api/v1/cis/"+id+"/interfaces", `{"name":"hra-eth2"}`, http.StatusCreated)
	call(http.MethodPatch, "/api/v1/network-interfaces/"+eth1, `{"description":"hra uplink"}`, http.StatusOK)
	call(http.MethodDelete, "/api/v1/network-interfaces/"+eth2, "", http.StatusNoContent)
	call(http.MethodPost, "/api/v1/cis/"+id+"/contacts", fmt.Sprintf(`{"contact_id":%q,"relationship_type":"owner"}`, contact), http.StatusCreated)
	link := call(http.MethodPost, "/api/v1/cis/"+id+"/contacts", fmt.Sprintf(`{"contact_id":%q,"relationship_type":"escalation"}`, contact), http.StatusCreated)
	call(http.MethodDelete, "/api/v1/ci-contacts/"+link, "", http.StatusNoContent)
	// Control: an interface of another CI.
	otherIf := call(http.MethodPost, "/api/v1/cis/"+other+"/interfaces", `{"name":"hra-other-eth"}`, http.StatusCreated)
	call(http.MethodPatch, "/api/v1/network-interfaces/"+otherIf, `{"description":"other"}`, http.StatusOK)

	// The audit entries about the CI and its links as GET /audit-log
	// lists them to the org-wide reader.
	type entry struct {
		ID           string `json:"id"`
		ResourceType string `json:"resource_type"`
		ResourceID   string `json:"resource_id"`
		Action       string `json:"action"`
	}
	auditLog := func(scope identity.Scope, query string) []entry {
		t.Helper()
		var body struct {
			Data []entry `json:"data"`
		}
		hraGet(t, e, f.OrgA, admin.User, scope, "/api/v1/audit-log?limit=500&"+query, &body)
		return body.Data
	}
	all := identity.ScopeSet{All: true}
	orgWide := identity.Scope{Clients: all, Sites: all, Teams: all}
	client1 := identity.Scope{Clients: identity.ScopeSet{IDs: []string{f.Client1}}, Sites: all, Teams: all}
	want := map[string]entry{}
	for _, q := range []string{"resource_id=" + id, "resource_id=" + eth1, "resource_id=" + eth2, "resource_id=" + link} {
		for _, en := range auditLog(orgWide, q) {
			want[en.ID] = en
		}
	}
	actions := func(m map[string]entry) []string {
		out := make([]string, 0, len(m))
		for _, en := range m {
			out = append(out, en.ResourceType+"/"+en.Action)
		}
		sort.Strings(out)
		return out
	}
	for _, action := range []string{"ci/ci.updated", "cis/cis.interfaces", "cis/cis.contacts", "network-interfaces/network-interfaces.update",
		"network-interfaces/network-interfaces.delete", "ci-contacts/ci-contacts.delete"} {
		found := false
		for _, a := range actions(want) {
			found = found || a == action
		}
		if !found {
			t.Fatalf("precondition: GET /audit-log lists no entry %s about the CI or its links: %v", action, actions(want))
		}
	}
	otherEntries := map[string]bool{}
	for _, en := range auditLog(orgWide, "resource_id="+otherIf) {
		otherEntries[en.ID] = true
	}

	history := func(scope identity.Scope, ciID string) (int, map[string]entry) {
		t.Helper()
		var body struct {
			Data []struct {
				ID     string `json:"id"`
				Source string `json:"source"`
				Action string `json:"change_type"`
			} `json:"data"`
			Total         int  `json:"total"`
			AuditIncluded bool `json:"audit_included"`
		}
		code := hraGet(t, e, f.OrgA, admin.User, scope, "/api/v1/history/ci/"+ciID+"?limit=200", &body)
		if code != http.StatusOK {
			return code, nil
		}
		if !body.AuditIncluded || body.Total != len(body.Data) {
			t.Errorf("history of %s: audit_included=%v total=%d entries=%d", ciID, body.AuditIncluded, body.Total, len(body.Data))
		}
		out := map[string]entry{}
		for _, d := range body.Data {
			if d.Source == "audit" {
				out[d.ID] = entry{ID: d.ID, Action: d.Action}
			}
		}
		return code, out
	}

	// The org-wide reader: every entry about the CI, none about the other
	// CI's interface.
	_, got := history(orgWide, id)
	missing := []string{}
	for eid, en := range want {
		if _, ok := got[eid]; !ok {
			missing = append(missing, en.ResourceType+"/"+en.Action)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("audit entries of the CI missing in its history: %d of %d: %v", len(missing), len(want), missing)
	}
	for eid := range got {
		if otherEntries[eid] {
			t.Errorf("the history of the CI shows the entry %s about another CI's interface", eid)
		}
	}

	// The client-1 reader: the entries the audit list shows it, all of
	// those of the CI itself (ci and cis); not the delete of the link,
	// whose row is gone (WP-660: entries about deleted objects are hidden).
	visible := map[string]bool{}
	for _, en := range auditLog(client1, "") {
		visible[en.ID] = true
	}
	_, scoped := history(client1, id)
	for eid := range scoped {
		if !visible[eid] {
			t.Errorf("the client-1 reader sees the entry %s in the history, which the audit list does not show it", eid)
		}
	}
	for eid, en := range want {
		if (en.ResourceType == "ci" || en.ResourceType == "cis") && !visible[eid] {
			t.Errorf("the audit list does not show the client-1 reader the entry %s/%s of its CI", en.ResourceType, en.Action)
		}
		if visible[eid] {
			if _, ok := scoped[eid]; !ok {
				t.Errorf("the client-1 reader misses the entry %s/%s in the history of its CI", en.ResourceType, en.Action)
			}
		}
	}
	t.Logf("org-wide reader: %d of %d audit entries of the CI and its links in the history; client-1 reader: %d in the history",
		len(want)-len(missing), len(want), len(scoped))
	if code, _ := history(client1, foreign); code != http.StatusNotFound {
		t.Errorf("history of the CI of client 2 as the client-1 reader = %d, want 404", code)
	}
}

// hraGet sends GET path as user with the scope and all permissions and decodes
// a 200 answer into out.
func hraGet(t *testing.T, e *t6Env, org, user string, scope identity.Scope, path string, out any) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	p := identity.Principal{Subject: user, OrganizationID: org, Type: identity.PrincipalTypeUser, Scope: &scope,
		Permissions: identity.AllPermissions(), Credential: "hra"}
	ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), p.Permissions)
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, req.WithContext(ctx))
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
	}
	return w.Code
}
