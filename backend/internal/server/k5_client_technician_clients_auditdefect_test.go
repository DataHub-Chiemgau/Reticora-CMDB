//go:build auditdefect

package server_test

// Defect test found by WP-556 (t7-ui-scope-lists, UI-01, RBA-02; F-373): the
// standard client-scoped role client_technician cannot list its clients.
// The route GET /api/v1/clients requires user:read (server/authz.go,
// readPermissions "clients"); the role matrix of migration 000147 does not
// give client_technician user:read (RBA-02 has no user:read for it). The
// request answers 403, and the client select of the scope switcher
// (frontend/src/components/ScopeSwitcher.tsx, disabled={!clients.isSuccess})
// stays disabled for every client technician.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=... go test -tags auditdefect -count=1 -v \
//	  -run '^TestAuditClientTechnicianListsOwnClients$' ./internal/server/

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// TestAuditClientTechnicianListsOwnClients: a principal with exactly the
// permissions of the standard role client_technician (read from the role
// row of the organization), bound to client 1, lists the clients through the
// router: 200 with client 1 and without client 2.
func TestAuditClientTechnicianListsOwnClients(t *testing.T) {
	r := newAuditT3Router(t, "f6")
	f := r.f
	var raw []byte
	if err := f.Admin.QueryRow(context.Background(),
		`SELECT permissions::text FROM role WHERE organization_id = $1 AND name = 'client_technician' AND is_builtin`, f.OrgA).Scan(&raw); err != nil {
		t.Fatalf("standard role client_technician: %v", err)
	}
	var keys []string
	if err := json.Unmarshal(raw, &keys); err != nil || len(keys) == 0 {
		t.Fatalf("permissions of client_technician %s: %v", raw, err)
	}
	perms := make([]identity.Permission, 0, len(keys))
	for _, k := range keys {
		perms = append(perms, identity.Permission(k))
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/clients?limit=200", bytes.NewReader(nil))
	principal := identity.Principal{Subject: r.other, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
		Scope: &identity.Scope{Clients: identity.ScopeSet{IDs: []string{f.Client1}}, Sites: identity.ScopeSet{All: true},
			Teams: identity.ScopeSet{All: true}},
		Permissions: perms, Credential: "k5-client-technician"}
	ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), principal), principal.Permissions)
	w := httptest.NewRecorder()
	r.router.ServeHTTP(w, req.WithContext(ctx))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/clients as client_technician of client 1 (permissions %v): %d %s; want 200 with the own client (scope switcher, UI-01)",
			keys, w.Code, w.Body.String())
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	own := false
	for _, c := range list.Data {
		if c.ID == f.Client2 {
			t.Errorf("client_technician of client 1 lists client 2")
		}
		own = own || c.ID == f.Client1
	}
	if !own {
		t.Errorf("client_technician of client 1 does not list client 1: %s", w.Body.String())
	}
}
