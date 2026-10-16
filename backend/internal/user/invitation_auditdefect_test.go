//go:build auditdefect

// Phase-1 acceptance audit, group T6-epic-b – DEFECT tests (must fail until fixed).
//
// WP-114 (TLC-03, RBA-02, RBA-03, API-05) and WP-140 (NTF-05)
//
//   - TestAuditDefectInvitationGrantCeiling / TestAuditDefectRoleAssignmentGrantCeiling
//     P1 "No grant ceiling: a client-scoped user:manage holder issues org-wide
//     org_admin invitations (privilege escalation)". user/invitation.go
//     checkInvitationTargets returns nil for scope_type org and never compares
//     the invited role/scope with the issuer's grants; POST /role-assignments
//     (handler.go AssignRole) has the same unchecked pattern.
//     Gap (critical): grant ceiling on invitation issue and role assignment.
//
//   - TestAuditDefectInvitationCreatedEvent
//     WP-140 P1 / WP-114 P2 "Invitation is never delivered: invitation.created is
//     not emitted, so no invitation e-mail is sent" (NTF-05 Phase-1 event
//     "Einladung"; notify/subscriptions.go renders email, organization_name,
//     expires_at and link). Gap (critical/high): invitation delivery event.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_11?sslmode=disable \
//	  go test -tags auditdefect -count=1 -v -run 'TestAuditDefect(Invitation|RoleAssignment)' ./internal/user/
package user

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// auditPrincipalCtx resolves userID through the production chain
// (AccessGrants → ResolveAccess → TenantScopeFor) and returns its request
// context with principal, tenant scope and tenant info.
func auditPrincipalCtx(t *testing.T, f *scopetest.Fixture, userID string) context.Context {
	t.Helper()
	bg := context.Background()
	grants, err := permission.NewPGRepository(f.App).AccessGrants(bg, f.OrgA, userID)
	if err != nil {
		t.Fatalf("access grants of %s: %v", userID, err)
	}
	acc := identity.ResolveAccess(grants)
	p := identity.Principal{Subject: userID, OrganizationID: f.OrgA, Scope: &acc.Scope,
		PermissionScopes: acc.PermissionScopes, Permissions: acc.Permissions}
	if !p.Has(identity.PermUserManage) {
		t.Fatalf("fixture: principal %s lacks user:manage", userID)
	}
	ts := middleware.TenantScopeFor(&p)
	ctx := database.ContextWithTenantScope(bg, &ts)
	ctx = identity.WithPrincipal(ctx, p)
	return tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: f.OrgA, UserID: userID})
}

func auditRoleID(t *testing.T, f *scopetest.Fixture, name string) string {
	t.Helper()
	var id string
	if err := f.Admin.QueryRow(context.Background(), `SELECT id::text FROM role WHERE organization_id = $1 AND name = $2`, f.OrgA, name).Scan(&id); err != nil {
		t.Fatalf("role %s: %v", name, err)
	}
	return id
}

func auditPost(mux http.Handler, ctx context.Context, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// refused reports whether status is a refusal of an out-of-ceiling grant.
func refused(status int) bool {
	return status == http.StatusForbidden || status == http.StatusUnprocessableEntity
}

func TestAuditDefectInvitationGrantCeiling(t *testing.T) {
	f := scopetest.Seed(t, "d1")
	bg := context.Background()
	adminRole := auditRoleID(t, f, "org_admin")
	viewerRole := auditRoleID(t, f, "viewer")

	// The delegate holds org_admin restricted to client 1 (user:manage there).
	delegate := f.AppUser(t, f.OrgA, "audit-d1-delegate")
	if _, err := f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id) VALUES ($1, $2, $3, $4)`,
		f.OrgA, delegate, adminRole, f.Client1); err != nil {
		t.Fatal(err)
	}
	// An org-wide org_admin is the positive control.
	orgAdmin := f.AppUser(t, f.OrgA, "audit-d1-orgadmin")
	if _, err := f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`,
		f.OrgA, orgAdmin, adminRole); err != nil {
		t.Fatal(err)
	}
	delegateCtx := auditPrincipalCtx(t, f, delegate)
	orgAdminCtx := auditPrincipalCtx(t, f, orgAdmin)

	mux := chi.NewRouter()
	NewInvitationHandler(NewPGInvitations(f.App)).RegisterRoutes(mux)

	invitations := func(email string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM user_invitation WHERE organization_id = $1 AND lower(email) = lower($2)`, f.OrgA, email).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	for _, tc := range []struct {
		name    string
		ctx     context.Context
		email   string
		body    string
		allowed bool
	}{
		{"client-1 delegate invites org_admin org-wide (escalation)", delegateCtx, "audit-d1-esc-org@invite.example",
			`{"role_id":"` + adminRole + `","scope_type":"org"}`, false},
		{"client-1 delegate invites viewer org-wide (scope beyond own)", delegateCtx, "audit-d1-viewer-org@invite.example",
			`{"role_id":"` + viewerRole + `","scope_type":"org"}`, false},
		{"client-1 delegate invites viewer into client 2", delegateCtx, "audit-d1-viewer-c2@invite.example",
			`{"role_id":"` + viewerRole + `","scope_type":"client","scope_id":"` + f.Client2 + `"}`, false},
		{"client-1 delegate invites viewer into client 1", delegateCtx, "audit-d1-viewer-c1@invite.example",
			`{"role_id":"` + viewerRole + `","scope_type":"client","scope_id":"` + f.Client1 + `"}`, true},
		{"org-wide org_admin invites org_admin org-wide", orgAdminCtx, "audit-d1-admin-org@invite.example",
			`{"role_id":"` + adminRole + `","scope_type":"org"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"email":"` + tc.email + `",` + strings.TrimPrefix(tc.body, "{")
			w := auditPost(mux, tc.ctx, "/api/v1/invitations", body)
			n := invitations(tc.email)
			if tc.allowed {
				if w.Code != http.StatusCreated || n != 1 {
					t.Errorf("POST /invitations = %d (%d rows), want 201 and 1 row; body %.200s", w.Code, n, w.Body.String())
				}
				return
			}
			if !refused(w.Code) || n != 0 {
				t.Errorf("POST /invitations = %d with %d stored invitation(s), want 403/422 and none: a principal must not invite beyond its own role and scope; body %.200s",
					w.Code, n, w.Body.String())
			}
		})
	}
}

func TestAuditDefectRoleAssignmentGrantCeiling(t *testing.T) {
	f := scopetest.Seed(t, "d3")
	bg := context.Background()
	adminRole := auditRoleID(t, f, "org_admin")

	delegate := f.AppUser(t, f.OrgA, "audit-d3-delegate")
	if _, err := f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id) VALUES ($1, $2, $3, $4)`,
		f.OrgA, delegate, adminRole, f.Client1); err != nil {
		t.Fatal(err)
	}
	orgAdmin := f.AppUser(t, f.OrgA, "audit-d3-orgadmin")
	if _, err := f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`,
		f.OrgA, orgAdmin, adminRole); err != nil {
		t.Fatal(err)
	}
	target := f.AppUser(t, f.OrgA, "audit-d3-target")
	target2 := f.AppUser(t, f.OrgA, "audit-d3-target2")
	delegateCtx := auditPrincipalCtx(t, f, delegate)
	orgAdminCtx := auditPrincipalCtx(t, f, orgAdmin)

	mux := chi.NewRouter()
	NewHandler(NewPGRepository(f.App)).RegisterRoutes(mux)

	orgWideAdmin := func(userID string) bool {
		t.Helper()
		var ok bool
		if err := f.Admin.QueryRow(bg, `SELECT EXISTS (SELECT 1 FROM role_assignment WHERE user_id = $1 AND role_id = $2
			AND scope_client_id IS NULL AND scope_site_id IS NULL)`, userID, adminRole).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}

	for _, tc := range []struct {
		name    string
		ctx     context.Context
		user    string
		allowed bool
	}{
		{"client-1 delegate grants itself org_admin org-wide", delegateCtx, delegate, false},
		{"client-1 delegate grants another user org_admin org-wide", delegateCtx, target, false},
		{"org-wide org_admin grants org_admin org-wide", orgAdminCtx, target2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := auditPost(mux, tc.ctx, "/api/v1/role-assignments",
				`{"user_id":"`+tc.user+`","role_id":"`+adminRole+`","scope_type":"organization"}`)
			got := orgWideAdmin(tc.user)
			if tc.allowed {
				if w.Code != http.StatusCreated || !got {
					t.Errorf("POST /role-assignments = %d (org-wide org_admin stored %v), want 201 and stored; body %.200s", w.Code, got, w.Body.String())
				}
				return
			}
			if !refused(w.Code) || got {
				t.Errorf("POST /role-assignments = %d, org-wide org_admin stored = %v; want 403/422 and nothing stored (grant ceiling); body %.200s",
					w.Code, got, w.Body.String())
			}
		})
	}
}

func TestAuditDefectInvitationCreatedEvent(t *testing.T) {
	f := scopetest.Seed(t, "d2")
	bg := context.Background()
	viewerRole := auditRoleID(t, f, "viewer")
	mux := chi.NewRouter()
	NewInvitationHandler(NewPGInvitations(f.App)).RegisterRoutes(mux)
	adminCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})

	type event struct {
		Type string
		Data map[string]any
	}
	outbox := func(orgID string) []event {
		t.Helper()
		rows, err := f.Admin.Query(bg, `SELECT type, data FROM event_outbox WHERE organization_id = $1 AND type = 'invitation.created' ORDER BY sequence`, orgID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []event
		for rows.Next() {
			var e event
			var raw []byte
			if err := rows.Scan(&e.Type, &raw); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &e.Data); err != nil {
				t.Fatalf("event data %s: %v", raw, err)
			}
			out = append(out, e)
		}
		return out
	}
	checkEvent := func(t *testing.T, e event, email, token string) {
		t.Helper()
		if got, _ := e.Data["email"].(string); !strings.EqualFold(got, email) {
			t.Errorf("invitation.created email = %q, want %q", got, email)
		}
		for _, key := range []string{"organization_name", "expires_at"} {
			if s, _ := e.Data[key].(string); s == "" {
				t.Errorf("invitation.created lacks %s: %v", key, e.Data)
			}
		}
		if link, _ := e.Data["link"].(string); token == "" || !strings.Contains(link, token) {
			t.Errorf("invitation.created link %q does not carry the acceptance token", link)
		}
	}

	email := "audit-d2-invitee@invite.example"
	w := auditPost(mux, adminCtx, "/api/v1/invitations", `{"email":"`+email+`","role_id":"`+viewerRole+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("issue = %d %s", w.Code, w.Body.String())
	}
	var inv Invitation
	if err := json.Unmarshal(w.Body.Bytes(), &inv); err != nil {
		t.Fatal(err)
	}

	t.Run("issue emits exactly one invitation.created", func(t *testing.T) {
		got := outbox(f.OrgA)
		if len(got) != 1 {
			t.Fatalf("invitation.created events after issue = %d, want 1 (NTF-05: the invitee gets the link by mail)", len(got))
		}
		checkEvent(t, got[0], email, inv.Token)
	})

	t.Run("duplicate pending invitation (409) emits nothing", func(t *testing.T) {
		before := len(outbox(f.OrgA))
		w := auditPost(mux, adminCtx, "/api/v1/invitations", `{"email":"`+email+`","role_id":"`+viewerRole+`"}`)
		if w.Code != http.StatusConflict {
			t.Fatalf("duplicate issue = %d, want 409", w.Code)
		}
		if after := len(outbox(f.OrgA)); after != before {
			t.Errorf("events after refused duplicate = %d, want %d", after, before)
		}
	})

	t.Run("renew emits a new invitation.created with the new token", func(t *testing.T) {
		before := len(outbox(f.OrgA))
		w := auditPost(mux, adminCtx, "/api/v1/invitations/"+inv.ID+"/renew", ``)
		if w.Code != http.StatusOK {
			t.Fatalf("renew = %d %s", w.Code, w.Body.String())
		}
		var renewed Invitation
		if err := json.Unmarshal(w.Body.Bytes(), &renewed); err != nil {
			t.Fatal(err)
		}
		got := outbox(f.OrgA)
		if len(got) != before+1 {
			t.Fatalf("invitation.created events after renew = %d, want %d", len(got), before+1)
		}
		checkEvent(t, got[len(got)-1], email, renewed.Token)
	})

	t.Run("organization B sees no invitation event", func(t *testing.T) {
		if got := outbox(f.OrgB); len(got) != 0 {
			t.Errorf("organization B invitation events = %d, want 0", len(got))
		}
	})
}
