//go:build auditdefect

// Phase-1 acceptance audit, group T6-epic-b – DEFECT tests (must fail until fixed).
//
// WP-114 (TLC-03) and WP-140 (NTF-05). The grant ceiling tests
// TestAuditDefectInvitationGrantCeiling and
// TestAuditDefectRoleAssignmentGrantCeiling moved to
// invitation_integration_test.go (WP-394); this file uses their helpers.
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
//	  go test -tags auditdefect -count=1 -v -run 'TestAuditDefectInvitationCreatedEvent' ./internal/user/
package user

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

func TestAuditDefectInvitationCreatedEvent(t *testing.T) {
	f := scopetest.Seed(t, "d2")
	bg := context.Background()
	viewerRole := auditRoleID(t, f, "viewer")
	mux := chi.NewRouter()
	NewInvitationHandler(NewPGInvitations(f.App)).RegisterRoutes(mux)
	// An org-wide org_admin issues and renews (grant ceiling, WP-394).
	admin := f.AppUser(t, f.OrgA, "audit-d2-orgadmin")
	if _, err := f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`,
		f.OrgA, admin, auditRoleID(t, f, "org_admin")); err != nil {
		t.Fatal(err)
	}
	adminCtx := auditPrincipalCtx(t, f, admin)

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
