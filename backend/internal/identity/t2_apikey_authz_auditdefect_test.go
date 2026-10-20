//go:build auditdefect

// Phase-1 acceptance audit, group T2-auth-authz: defect test of the
// service-account role assignment against PostgreSQL. It encodes a
// requirement the product currently violates and fails on an assertion until
// the finding is fixed.
//
//   - WP-069 (RBA-08, RBA-03, API-05), finding P1 "Service-account binding
//     and role assignment are not authorized against the caller's rights and
//     scope (escalation)": handler part of the role assignment
//     (TestAuditT2ServiceAccountRolesWithinCallerRights, WP-355; the route
//     part, user:manage instead of role:manage, is in
//     internal/server/t2_auth_chain_auditdefect_test.go); gap (critical)
//     "Authorization of SA role assignment". The binding part and the
//     rotate/revoke tests moved to apikey_authz_integration_test.go
//     (WP-354), whose fixture newT2KeyFixture this test uses.
//
// Retest (needs a migrated database):
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_02?sslmode=disable go test -tags auditdefect -count=1 -v -run '^TestAuditT2ServiceAccountRolesWithinCallerRights$' ./internal/identity/
package identity_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

// TestAuditT2ServiceAccountRolesWithinCallerRights: service accounts are on a
// par with users (RBA-08); whoever assigns their roles may grant only rights
// they hold, in their own scope (RBA-03). A client-1 scoped caller holding
// user:manage and role:manage (so the route check passes even after the
// route fix) must not give a service account an org-wide role with
// apikey:manage and webhook:manage, nor that role limited to client 1. The
// org-wide administrator assigning a role within its rights is the control.
func TestAuditT2ServiceAccountRolesWithinCallerRights(t *testing.T) {
	k := newT2KeyFixture(t)
	bg := context.Background()
	callerID := k.f.AppUser(t, k.f.OrgA, "t2-sa-role-scoped")
	callerRole, powerful := k.f.ID(), k.f.ID()
	k.exec(`INSERT INTO role (id, organization_id, name, permissions, scope) VALUES ($1, $2, 't2-sa-role-scoped', '["user:read","user:manage","role:manage"]', 'client')`, callerRole, k.f.OrgA)
	k.exec(`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id) VALUES ($1, $2, $3, $4)`, k.f.OrgA, callerID, callerRole, k.f.Client1)
	k.exec(`INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 't2-sa-powerful', '["ci:read","user:manage","role:manage","apikey:manage","webhook:manage"]')`, powerful, k.f.OrgA)
	caller := k.resolve(k.f.OrgA, callerID)
	resetScope := database.OrgWideScope(k.f.OrgA, "")
	resetCtx := database.ContextWithTenantScope(bg, &resetScope)

	w := k.call(caller, http.MethodPost, "/api/v1/service-accounts", `{"name":"t2-sa-minted-by-scoped"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create service account as the scoped caller: %d %s", w.Code, w.Body.String())
	}
	var sa user.ServiceAccount
	if err := json.Unmarshal(w.Body.Bytes(), &sa); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, body string }{
		{"org-wide powerful role", `{"roles":[{"role_id":"` + powerful + `"}]}`},
		{"powerful role limited to client 1", `{"roles":[{"role_id":"` + powerful + `","scope_client_id":"` + k.f.Client1 + `"}]}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			rw := k.call(caller, http.MethodPut, "/api/v1/service-accounts/"+sa.ID+"/roles", c.body)
			grants, err := k.saRepo.ServiceAccountGrants(bg, k.f.OrgA, sa.ID)
			if err != nil {
				t.Fatalf("service account grants: %v", err)
			}
			if rw.Code != http.StatusForbidden || len(grants) != 0 {
				var granted []string
				for _, g := range grants {
					granted = append(granted, permStrings(g.Permissions)...)
				}
				t.Errorf("client-1 scoped caller {user:read,user:manage,role:manage} assigns the %s to a service account: %d, account now grants %v; want 403 and no grants (never grant more than the caller holds)",
					c.name, rw.Code, granted)
			}
			// Reset for the next case.
			if _, err := k.saRepo.SetServiceAccountRoles(resetCtx, k.f.OrgA, sa.ID, nil); err != nil {
				t.Fatalf("reset roles: %v", err)
			}
		})
	}

	// Control: the org-wide admin assigns a role within its rights.
	w = k.call(k.admin, http.MethodPost, "/api/v1/service-accounts", `{"name":"t2-sa-by-admin"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("control: create service account: %d %s", w.Code, w.Body.String())
	}
	var adminSA user.ServiceAccount
	_ = json.Unmarshal(w.Body.Bytes(), &adminSA)
	if w := k.call(k.admin, http.MethodPut, "/api/v1/service-accounts/"+adminSA.ID+"/roles", `{"roles":[{"role_id":"`+k.saRole+`"}]}`); w.Code != http.StatusOK {
		t.Errorf("control: org-wide admin assigns {ci:read} to a service account: %d %s, want 200", w.Code, w.Body.String())
	}
}
