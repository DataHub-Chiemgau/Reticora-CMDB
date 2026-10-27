package server_test

// Reading clients with the standard roles (WP-644, k5n-clients-read; RBA-02,
// UI-01; closes F-373). GET /api/v1/clients and GET /api/v1/clients/{id}
// require location:read, which every standard role with a client reference
// holds; RLS limits the list to the clients of the scope. Before WP-644 the
// routes required user:read, which client_technician does not hold (RBA-02),
// so the client select of the scope switcher stayed disabled for every
// client technician. Writes stay user:manage.
//
// Runs the complete router (authorization, OpenAPI validation) against
// PostgreSQL with the permissions of the standard roles read from the role
// rows of the organization.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=... go test -count=1 -v \
//	  -run 'TestAuditClientTechnicianListsOwnClients|TestClientsReadStandardRoles' ./internal/server/

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// standardRolePermissions reads the permissions of the built-in role name of
// organization A.
func standardRolePermissions(t *testing.T, r *auditT3Router, name string) []identity.Permission {
	t.Helper()
	var raw []byte
	if err := r.f.Admin.QueryRow(context.Background(),
		`SELECT permissions::text FROM role WHERE organization_id = $1 AND name = $2 AND is_builtin`, r.f.OrgA, name).Scan(&raw); err != nil {
		t.Fatalf("standard role %s: %v", name, err)
	}
	var keys []string
	if err := json.Unmarshal(raw, &keys); err != nil || len(keys) == 0 {
		t.Fatalf("permissions of %s %s: %v", name, raw, err)
	}
	perms := make([]identity.Permission, 0, len(keys))
	for _, k := range keys {
		perms = append(perms, identity.Permission(k))
	}
	return perms
}

// clientsCall sends one request through the router as the second user of
// organization A with perms, bound to the given clients (nil: all clients).
func clientsCall(r *auditT3Router, perms []identity.Permission, clients []string, method, path, body string) *httptest.ResponseRecorder {
	r.t.Helper()
	clientSet := identity.ScopeSet{All: true}
	if clients != nil {
		clientSet = identity.ScopeSet{IDs: clients}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	principal := identity.Principal{Subject: r.other, OrganizationID: r.f.OrgA, Type: identity.PrincipalTypeUser,
		Scope:       &identity.Scope{Clients: clientSet, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}},
		Permissions: perms, Credential: "k5-clients-read"}
	ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), principal), principal.Permissions)
	w := httptest.NewRecorder()
	r.router.ServeHTTP(w, req.WithContext(ctx))
	return w
}

// listedClients decodes the ids of a client list response.
func listedClients(t *testing.T, w *httptest.ResponseRecorder) map[string]bool {
	t.Helper()
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("client list %s: %v", w.Body.String(), err)
	}
	ids := map[string]bool{}
	for _, c := range list.Data {
		ids[c.ID] = true
	}
	return ids
}

// TestAuditClientTechnicianListsOwnClients: a principal with exactly the
// permissions of the standard role client_technician (read from the role
// row of the organization), bound to client 1, lists the clients through the
// router: 200 with client 1 and without client 2 (F-373).
func TestAuditClientTechnicianListsOwnClients(t *testing.T) {
	r := newAuditT3Router(t, "f6")
	f := r.f
	perms := standardRolePermissions(t, r, "client_technician")

	w := clientsCall(r, perms, []string{f.Client1}, http.MethodGet, "/api/v1/clients?limit=200", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/clients as client_technician of client 1 (permissions %v): %d %s; want 200 with the own client (scope switcher, UI-01)",
			perms, w.Code, w.Body.String())
	}
	ids := listedClients(t, w)
	if ids[f.Client2] {
		t.Errorf("client_technician of client 1 lists client 2")
	}
	if !ids[f.Client1] {
		t.Errorf("client_technician of client 1 does not list client 1: %s", w.Body.String())
	}
}

// TestClientsReadStandardRoles covers the other three standard roles
// (org-wide: both clients), the detail route for client_technician (own
// client 200, foreign client 404), a principal without location:read (403 on
// list and detail) and the write routes, which stay user:manage.
func TestClientsReadStandardRoles(t *testing.T) {
	r := newAuditT3Router(t, "f6")
	f := r.f

	for _, role := range []string{"org_admin", "engineer", "viewer"} {
		t.Run(role, func(t *testing.T) {
			perms := standardRolePermissions(t, r, role)
			w := clientsCall(r, perms, nil, http.MethodGet, "/api/v1/clients?limit=200", "")
			if w.Code != http.StatusOK {
				t.Fatalf("GET /api/v1/clients as %s: %d %s; want 200", role, w.Code, w.Body.String())
			}
			ids := listedClients(t, w)
			if !ids[f.Client1] || !ids[f.Client2] {
				t.Errorf("%s (org-wide) lists %v; want client 1 and client 2", role, ids)
			}
			if w := clientsCall(r, perms, nil, http.MethodGet, "/api/v1/clients/"+f.Client2, ""); w.Code != http.StatusOK {
				t.Errorf("GET /api/v1/clients/{client 2} as %s: %d %s; want 200", role, w.Code, w.Body.String())
			}
		})
	}

	t.Run("client_technician detail", func(t *testing.T) {
		perms := standardRolePermissions(t, r, "client_technician")
		if w := clientsCall(r, perms, []string{f.Client1}, http.MethodGet, "/api/v1/clients/"+f.Client1, ""); w.Code != http.StatusOK {
			t.Errorf("GET /api/v1/clients/{client 1} as client_technician of client 1: %d %s; want 200", w.Code, w.Body.String())
		}
		if w := clientsCall(r, perms, []string{f.Client1}, http.MethodGet, "/api/v1/clients/"+f.Client2, ""); w.Code != http.StatusNotFound {
			t.Errorf("GET /api/v1/clients/{client 2} as client_technician of client 1: %d %s; want 404 (RLS)", w.Code, w.Body.String())
		}
	})

	t.Run("client_technician writes", func(t *testing.T) {
		perms := standardRolePermissions(t, r, "client_technician")
		writes := []struct{ method, path, body string }{
			{http.MethodPost, "/api/v1/clients", `{"name":"k5 client","slug":"k5-client"}`},
			{http.MethodPatch, "/api/v1/clients/" + f.Client1, `{"name":"renamed"}`},
			{http.MethodDelete, "/api/v1/clients/" + f.Client1, ""},
		}
		for _, wr := range writes {
			if w := clientsCall(r, perms, []string{f.Client1}, wr.method, wr.path, wr.body); w.Code != http.StatusForbidden {
				t.Errorf("%s %s as client_technician: %d %s; want 403 (user:manage)", wr.method, wr.path, w.Code, w.Body.String())
			}
		}
	})

	t.Run("without location:read", func(t *testing.T) {
		perms := auditT3Without(identity.PermLocationRead)
		for _, path := range []string{"/api/v1/clients?limit=200", "/api/v1/clients/" + f.Client1} {
			if w := clientsCall(r, perms, nil, http.MethodGet, path, ""); w.Code != http.StatusForbidden {
				t.Errorf("GET %s without location:read (all other permissions): %d %s; want 403", path, w.Code, w.Body.String())
			}
		}
	})
}
