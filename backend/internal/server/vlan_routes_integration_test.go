package server_test

// PG-HTTP test of the VLAN catalog and of the VLAN references of subnets and
// interfaces (WP-371; NET-08, NET-01, NET-02, DB-05, TEN-05; F-088) through
// the complete router of audit_t3_harness_test.go (authorization, OpenAPI
// validation, tenant scope) as a client-1 principal:
//   - POST/GET/PATCH/DELETE /api/v1/vlans of a client-1 VLAN → 201/200/200/204,
//     the list holds no client-2 VLAN;
//   - GET, PATCH and DELETE of a client-2 VLAN → 404, the VLAN is unchanged;
//   - POST and PATCH of a subnet and of an interface with the client-2 VLAN →
//     422 with field vlan_id, nothing is stored; with the client-1 VLAN →
//     201/200.
//
// Retest (needs a migrated database):
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_03?sslmode=disable \
//     go test -count=1 -v -run 'TestVLANRoutesStayWithinTheClient' ./internal/server/

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

func TestVLANRoutesStayWithinTheClient(t *testing.T) {
	r := newAuditT3Router(t, "0e")
	f := r.f
	client1 := &identity.Scope{Clients: identity.ScopeSet{IDs: []string{f.Client1}}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}}
	call := func(method, path, body string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		p := identity.Principal{Subject: r.user, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
			Scope: client1, Permissions: identity.AllPermissions(), Credential: "vlan-routes"}
		ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), p.Permissions)
		w := httptest.NewRecorder()
		r.router.ServeHTTP(w, req.WithContext(ctx))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	// refused requires 422 with field vlan_id.
	refused := func(what string, code int, body map[string]any) {
		t.Helper()
		if code != http.StatusUnprocessableEntity || body["field"] != "vlan_id" {
			t.Errorf("%s with the client-2 VLAN: %d %v, want 422 with field vlan_id", what, code, body)
		}
	}
	siteA := r.str(`INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, 'vlan-routes-a') RETURNING id::text`, f.OrgA, f.Client1)
	siteB := r.str(`INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, 'vlan-routes-b') RETURNING id::text`, f.OrgA, f.Client2)
	foreign := r.str(`INSERT INTO vlan (organization_id, site_id, vlan_id, name) VALUES ($1, $2, 20, 'client-2') RETURNING id::text`, f.OrgA, siteB)

	// The catalog as client 1.
	code, created := call(http.MethodPost, "/api/v1/vlans", `{"site_id":"`+siteA+`","vlan_id":10,"name":"users"}`)
	if code != http.StatusCreated || created["client_id"] != f.Client1 {
		t.Fatalf("POST /api/v1/vlans: %d %v, want 201 with client %s", code, created, f.Client1)
	}
	own, _ := created["id"].(string)
	if status, got := call(http.MethodGet, "/api/v1/vlans/"+own, ""); status != http.StatusOK || got["name"] != "users" {
		t.Errorf("GET own VLAN: %d %v, want 200", status, got)
	}
	if status, got := call(http.MethodPatch, "/api/v1/vlans/"+own, `{"name":"staff"}`); status != http.StatusOK || got["name"] != "staff" {
		t.Errorf("PATCH own VLAN: %d %v, want 200 with name staff", status, got)
	}
	code, list := call(http.MethodGet, "/api/v1/vlans", "")
	if data, _ := list["data"].([]any); code != http.StatusOK || len(data) != 1 || data[0].(map[string]any)["id"] != own {
		t.Errorf("GET /api/v1/vlans: %d %v, want only the client-1 VLAN", code, list)
	}
	for _, tc := range []struct{ method, body string }{{http.MethodGet, ""}, {http.MethodPatch, `{"name":"renamed-by-client-1"}`}, {http.MethodDelete, ""}} {
		if status, got := call(tc.method, "/api/v1/vlans/"+foreign, tc.body); status != http.StatusNotFound {
			t.Errorf("%s client-2 VLAN: %d %v, want 404", tc.method, status, got)
		}
	}
	if got := r.str(`SELECT name FROM vlan WHERE id = $1`, foreign); got != "client-2" {
		t.Errorf("client-2 VLAN after the refused requests: name %q, want client-2 and not deleted", got)
	}

	// Subnets.
	code, body := call(http.MethodPost, "/api/v1/subnets", `{"cidr":"10.14.1.0/24","client_id":"`+f.Client1+`","vlan_id":"`+foreign+`"}`)
	refused("POST /api/v1/subnets", code, body)
	if n := r.str(`SELECT count(*)::text FROM subnet WHERE organization_id = $1 AND cidr = '10.14.1.0/24'`, f.OrgA); n != "0" {
		t.Errorf("subnets stored by the refused POST: %s, want 0", n)
	}
	code, body = call(http.MethodPost, "/api/v1/subnets", `{"cidr":"10.14.0.0/24","client_id":"`+f.Client1+`","vlan_id":"`+own+`"}`)
	subnet, _ := body["id"].(string)
	if code != http.StatusCreated || body["vlan_id"] != own {
		t.Fatalf("POST /api/v1/subnets with the client-1 VLAN: %d %v, want 201", code, body)
	}
	code, body = call(http.MethodPatch, "/api/v1/subnets/"+subnet, `{"vlan_id":"`+foreign+`"}`)
	refused("PATCH /api/v1/subnets/{id}", code, body)
	if got := r.str(`SELECT coalesce(vlan_id::text, '') FROM subnet WHERE id = $1`, subnet); got != own {
		t.Errorf("subnet VLAN after the refused PATCH: %s, want %s", got, own)
	}

	// Interfaces of a client-1 CI.
	ciID := f.CI(t, f.OrgA, f.Client1, "vlan-routes-ci")
	code, body = call(http.MethodPost, "/api/v1/cis/"+ciID+"/interfaces", `{"name":"eth1","vlan_id":"`+foreign+`"}`)
	refused("POST /api/v1/cis/{id}/interfaces", code, body)
	if n := r.str(`SELECT count(*)::text FROM network_interface WHERE ci_id = $1`, ciID); n != "0" {
		t.Errorf("interfaces stored by the refused POST: %s, want 0", n)
	}
	code, body = call(http.MethodPost, "/api/v1/cis/"+ciID+"/interfaces", `{"name":"eth0","vlan_id":"`+own+`"}`)
	nic, _ := body["id"].(string)
	if code != http.StatusCreated || body["vlan_id"] != own {
		t.Fatalf("POST interface with the client-1 VLAN: %d %v, want 201", code, body)
	}
	code, body = call(http.MethodPatch, "/api/v1/network-interfaces/"+nic, `{"vlan_id":"`+foreign+`"}`)
	refused("PATCH /api/v1/network-interfaces/{id}", code, body)
	if got := r.str(`SELECT coalesce(vlan_id::text, '') FROM network_interface WHERE id = $1`, nic); got != own {
		t.Errorf("interface VLAN after the refused PATCH: %s, want %s", got, own)
	}
	if detail, _ := body["detail"].(string); strings.Contains(detail, "client-2") || strings.Contains(detail, foreign) {
		t.Errorf("422 detail %q discloses the client-2 VLAN", detail)
	}

	// Deleting the VLAN clears the references.
	if status, got := call(http.MethodDelete, "/api/v1/vlans/"+own, ""); status != http.StatusNoContent {
		t.Errorf("DELETE own VLAN: %d %v, want 204", status, got)
	}
	if got := r.str(`SELECT coalesce((SELECT vlan_id::text FROM subnet WHERE id = $1), '') || coalesce((SELECT vlan_id::text FROM network_interface WHERE id = $2), '')`,
		subnet, nic); got != "" {
		t.Errorf("references after deleting the VLAN: %q, want none", got)
	}
	if status, _ := call(http.MethodGet, "/api/v1/vlans/"+own, ""); status != http.StatusNotFound {
		t.Errorf("GET deleted VLAN: %d, want 404", status)
	}
}
