package user_test

// Self-lockout guard of the user administration against PostgreSQL and Redis
// (WP-648, TLC-04; finding F-377): PATCH /users/{id} with a non-active
// status and DELETE /users/{id} on the caller's own account answer 400
// problem+json with a fixed text, before anything is written, revoked or
// audited, like POST /users/{id}/anonymize. Before the fix an org_admin could
// deactivate (and revoke the sessions of) or delete their own account, so the
// last admin could lock the organization out. Other fields of the own
// account and status active stay allowed; other users and principals without
// a user id behave as before.
//
// Retest (needs a migrated database and Redis):
//
//	cd backend && TEST_DATABASE_URL=… TEST_REDIS_URL=redis://localhost:56379/0 go test -count=1 -v -run '^TestOwnAccount' ./internal/user/

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// as sends the request as an org-wide principal of organization A whose
// tenant context carries callerID as its user id ("" for a principal
// without one).
func (d *deactivationFixture) as(callerID, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	scope := database.OrgWideScope(d.f.OrgA, callerID)
	ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: d.f.OrgA, UserID: callerID})
	w := httptest.NewRecorder()
	d.mux.ServeHTTP(w, req.WithContext(database.ContextWithTenantScope(ctx, &scope)))
	return w
}

// userRow is the stored state of an app_user row.
type userRow struct {
	exists      bool
	active      bool
	displayName string
	updatedAt   time.Time
}

func (d *deactivationFixture) userRow(t *testing.T, id string) userRow {
	t.Helper()
	var row userRow
	if err := d.f.Admin.QueryRow(context.Background(),
		`SELECT count(*) > 0, coalesce(bool_or(is_active), false), coalesce(max(display_name), ''), coalesce(max(updated_at), 'epoch') FROM app_user WHERE id = $1`, id).
		Scan(&row.exists, &row.active, &row.displayName, &row.updatedAt); err != nil {
		t.Fatalf("read user: %v", err)
	}
	return row
}

// orgAudits counts the audit entries of organization A (the audit chain).
func (d *deactivationFixture) orgAudits(t *testing.T) int {
	t.Helper()
	var n int
	if err := d.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE organization_id = $1`, d.f.OrgA).Scan(&n); err != nil {
		t.Fatalf("count audit entries: %v", err)
	}
	return n
}

// wantProblem checks a 400 problem+json answer with the fixed detail text.
func wantProblem(t *testing.T, w *httptest.ResponseRecorder, detail string) {
	t.Helper()
	if w.Code != http.StatusBadRequest {
		t.Errorf("status %d %s, want 400", w.Code, w.Body.String())
		return
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("Content-Type %q, want application/problem+json", ct)
	}
	var problem struct {
		Status int    `json:"status"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil || problem.Status != http.StatusBadRequest || problem.Detail != detail {
		t.Errorf("problem %s (err %v), want status 400 and detail %q", w.Body.String(), err, detail)
	}
}

// TestOwnAccountDeactivationRefused: an org_admin who sends PATCH
// /users/{own id} with a non-active status gets 400 "you cannot deactivate
// your own account"; the account stays active and unchanged, its session
// stays valid, no revocation is attempted, its API key stays valid and no
// audit entry is written. A differently spelled own id (upper case, without
// hyphens) is refused too, also by POST /users/{id}/anonymize. Changing the
// own display name and status active stay allowed, and deactivating another
// user works as before.
func TestOwnAccountDeactivationRefused(t *testing.T) {
	d := newDeactivationFixture(t)
	bg := context.Background()
	own := d.admin
	keyID := d.apiKey(t, d.f.OrgA, own, "slk-own")
	token := d.session(t, own, d.f.OrgA)
	before := d.userRow(t, own)
	auditsBefore := d.orgAudits(t)

	for _, c := range []struct{ name, id, body string }{
		{"status inactive", own, `{"status":"inactive"}`},
		{"status suspended with a display name", own, `{"display_name":"Renamed while locking out","status":"suspended"}`},
		{"status inactive on the upper-case id", strings.ToUpper(own), `{"status":"inactive"}`},
		{"status inactive on the id without hyphens", strings.ReplaceAll(own, "-", ""), `{"status":"INACTIVE"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			wantProblem(t, d.as(own, http.MethodPatch, "/api/v1/users/"+c.id, c.body), "you cannot deactivate your own account")
			if after := d.userRow(t, own); after != before {
				t.Errorf("own account after the refused PATCH: %+v, want unchanged %+v", after, before)
			}
			if n := d.revoker.calls[own]; n != 0 {
				t.Errorf("refused self-deactivation called the session revocation %d times; want 0", n)
			}
			if at, err := d.revocations.RevokedAt(bg, own); err != nil || !at.IsZero() {
				t.Errorf("refused self-deactivation wrote a blacklist entry (revoked at %v, err %v); want none", at, err)
			}
			if _, err := d.issuer.Validate(token); err != nil {
				t.Errorf("own session after the refused self-deactivation: %v; want still valid", err)
			}
			if _, _, keyRevoked, _ := d.state(t, own, keyID); keyRevoked {
				t.Error("refused self-deactivation revoked the own API key")
			}
			if n := d.orgAudits(t); n != auditsBefore {
				t.Errorf("audit entries of the organization: %d, want unchanged %d", n, auditsBefore)
			}
		})
	}

	t.Run("anonymize on the upper-case own id", func(t *testing.T) {
		wantProblem(t, d.as(own, http.MethodPost, "/api/v1/users/"+strings.ToUpper(own)+"/anonymize", ""), "you cannot anonymize your own account")
		if after := d.userRow(t, own); after != before || d.revoker.calls[own] != 0 || d.orgAudits(t) != auditsBefore {
			t.Errorf("own account after the refused anonymization: %+v revocations=%d; want unchanged %+v, 0", after, d.revoker.calls[own], before)
		}
	})

	t.Run("own display name stays changeable", func(t *testing.T) {
		w := d.as(own, http.MethodPatch, "/api/v1/users/"+own, `{"display_name":"Own new name"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("PATCH own display name: %d %s, want 200", w.Code, w.Body.String())
		}
		if row := d.userRow(t, own); !row.active || row.displayName != "Own new name" {
			t.Errorf("own account after the rename: %+v, want active with the new name", row)
		}
	})

	t.Run("own status active stays allowed", func(t *testing.T) {
		w := d.as(own, http.MethodPatch, "/api/v1/users/"+own, `{"status":"active"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("PATCH own status active: %d %s, want 200", w.Code, w.Body.String())
		}
		if !d.userRow(t, own).active || d.revoker.calls[own] != 0 {
			t.Errorf("own account after status active: active=%v revocations=%d; want active, 0", d.userRow(t, own).active, d.revoker.calls[own])
		}
		if _, err := d.issuer.Validate(token); err != nil {
			t.Errorf("own session after status active: %v; want still valid", err)
		}
	})

	t.Run("control: deactivating another user", func(t *testing.T) {
		member := d.f.AppUser(t, d.f.OrgA, "slk-member")
		memberToken := d.session(t, member, d.f.OrgA)
		if w := d.as(own, http.MethodPatch, "/api/v1/users/"+member, `{"status":"inactive"}`); w.Code != http.StatusOK {
			t.Fatalf("PATCH another user status inactive: %d %s, want 200", w.Code, w.Body.String())
		}
		if d.userRow(t, member).active {
			t.Error("other user still active after the deactivation")
		}
		if n := d.revoker.calls[member]; n != 2 {
			t.Errorf("session revocations of the other user: %d, want 2", n)
		}
		if _, err := d.issuer.Validate(memberToken); err == nil {
			t.Error("session of the deactivated user still valid")
		}
		if w := d.as(own, http.MethodPatch, "/api/v1/users/"+member, `{"status":"active"}`); w.Code != http.StatusOK || !d.userRow(t, member).active {
			t.Errorf("reactivating the other user: %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("control: principal without a user id", func(t *testing.T) {
		member := d.f.AppUser(t, d.f.OrgA, "slk-nouser")
		for _, caller := range []string{"", d.f.ID()} {
			if w := d.as(caller, http.MethodPatch, "/api/v1/users/"+member, `{"status":"inactive"}`); w.Code != http.StatusOK {
				t.Errorf("PATCH status inactive by a principal with user id %q: %d %s, want 200", caller, w.Code, w.Body.String())
			}
			if w := d.as(caller, http.MethodPatch, "/api/v1/users/"+member, `{"status":"active"}`); w.Code != http.StatusOK {
				t.Errorf("PATCH status active by a principal with user id %q: %d %s, want 200", caller, w.Code, w.Body.String())
			}
		}
	})
}

// TestOwnAccountDeletionRefused: DELETE /users/{own id} answers 400 "you
// cannot delete your own account" and the account stays; deleting another
// user works as before (204, and 409 for a user who owns open objects), also
// for a principal without a user id.
func TestOwnAccountDeletionRefused(t *testing.T) {
	d := newDeactivationFixture(t)
	own := d.f.AppUser(t, d.f.OrgA, "slk-self-delete")
	before := d.userRow(t, own)
	auditsBefore := d.orgAudits(t)

	for _, c := range []struct{ name, id string }{
		{"own id", own},
		{"own upper-case id", strings.ToUpper(own)},
	} {
		t.Run(c.name, func(t *testing.T) {
			wantProblem(t, d.as(own, http.MethodDelete, "/api/v1/users/"+c.id, ""), "you cannot delete your own account")
			if after := d.userRow(t, own); after != before {
				t.Errorf("own account after the refused DELETE: %+v, want unchanged %+v", after, before)
			}
			if n := d.orgAudits(t); n != auditsBefore {
				t.Errorf("audit entries of the organization: %d, want unchanged %d", n, auditsBefore)
			}
		})
	}

	t.Run("control: deleting another user", func(t *testing.T) {
		member := d.f.AppUser(t, d.f.OrgA, "slk-delete-member")
		if w := d.as(own, http.MethodDelete, "/api/v1/users/"+member, ""); w.Code != http.StatusNoContent {
			t.Fatalf("DELETE another user: %d %s, want 204", w.Code, w.Body.String())
		}
		if d.userRow(t, member).exists {
			t.Error("other user still exists after the DELETE")
		}
	})

	t.Run("control: deleting a user who owns open objects", func(t *testing.T) {
		member := d.f.AppUser(t, d.f.OrgA, "slk-delete-owner")
		if _, err := d.f.Admin.Exec(context.Background(), `INSERT INTO ticket (id, organization_id, title, reporter_id, assignee_id, status) VALUES ($1, $2, 'handover', $3, $4, 'open')`,
			d.f.ID(), d.f.OrgA, own, member); err != nil {
			t.Fatalf("seed ticket: %v", err)
		}
		if w := d.as(own, http.MethodDelete, "/api/v1/users/"+member, ""); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "hand them over") {
			t.Errorf("DELETE the assignee of an open ticket: %d %s, want 409", w.Code, w.Body.String())
		}
		if !d.userRow(t, member).exists {
			t.Error("assignee of an open ticket deleted")
		}
	})

	t.Run("control: principal without a user id", func(t *testing.T) {
		for i, caller := range []string{"", d.f.ID()} {
			member := d.f.AppUser(t, d.f.OrgA, "slk-delete-nouser-"+string(rune('a'+i)))
			if w := d.as(caller, http.MethodDelete, "/api/v1/users/"+member, ""); w.Code != http.StatusNoContent {
				t.Errorf("DELETE by a principal with user id %q: %d %s, want 204", caller, w.Code, w.Body.String())
			}
		}
	})
}
