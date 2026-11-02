package user_test

// User deletion against PostgreSQL and Redis (WP-664, TLC-04, AUT-02,
// TEN-04; finding F-393): DELETE /users/{id} revokes the sessions of the
// user inside the transaction of the deletion, after the row of the
// caller's organization is deleted, and once more after the commit. A
// failing blacklist rolls the deletion back (503): the user, its API key
// and the audit trail stay as they were. A deletion that is refused (user of
// another organization, unknown id, open objects) never reaches the
// blacklist.
//
// Retest (needs a migrated database and Redis):
//
//	cd backend && TEST_DATABASE_URL=… TEST_APP_DATABASE_URL=… TEST_REDIS_URL=redis://localhost:56379/0 go test -count=1 -v -run '^TestUserDeletion' ./internal/user/

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// deletionState reads whether the user exists, whether the key is revoked
// and the number of user.deleted entries of the user.
func (d *deactivationFixture) deletionState(t *testing.T, userID, keyID string) (exists, keyRevoked bool, audits int) {
	t.Helper()
	bg := context.Background()
	if err := d.f.Admin.QueryRow(bg, `SELECT EXISTS (SELECT 1 FROM app_user WHERE id = $1)`, userID).Scan(&exists); err != nil {
		t.Fatalf("read user: %v", err)
	}
	if err := d.f.Admin.QueryRow(bg, `SELECT revoked_at IS NOT NULL FROM api_key WHERE id = $1`, keyID).Scan(&keyRevoked); err != nil {
		t.Fatalf("read api key: %v", err)
	}
	if err := d.f.Admin.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE resource_id = $1 AND action = 'user.deleted'`, userID).Scan(&audits); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	return exists, keyRevoked, audits
}

// TestUserDeletionRevocationFailureRollsBack: when the session blacklist
// cannot be written, DELETE /users/{id} answers 503 and the transaction is
// rolled back: the user still exists, its API key stays valid and no
// user.deleted entry is written.
func TestUserDeletionRevocationFailureRollsBack(t *testing.T) {
	d := newDeactivationFixture(t)
	d.revoker.fail = true

	member := d.f.AppUser(t, d.f.OrgA, "del-fail-member")
	keyID := d.apiKey(t, d.f.OrgA, member, "delf")
	w := d.asAdminA(http.MethodDelete, "/api/v1/users/"+member, "")
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("DELETE with an unavailable blacklist: %d %s, want 503", w.Code, w.Body.String())
	}
	if n := d.revoker.calls[member]; n != 1 {
		t.Errorf("session revocation attempts: %d, want 1 (inside the transaction)", n)
	}
	if exists, keyRevoked, audits := d.deletionState(t, member, keyID); !exists || keyRevoked || audits != 0 {
		t.Errorf("user after the failed DELETE: exists=%v key revoked=%v user.deleted entries=%d; want unchanged", exists, keyRevoked, audits)
	}
}

// TestUserDeletionRevokesOnlyOnDeletion: a DELETE of an organization A
// admin for a user of organization B, an unknown or invalid id or a user
// who owns open objects ends nothing (404, 404, 409; no revocation, the
// session stays valid); the deletion of a user of organization A ends the
// session at once, revoking in the transaction and after the commit, and
// revokes the user's API key.
func TestUserDeletionRevokesOnlyOnDeletion(t *testing.T) {
	d := newDeactivationFixture(t)
	bg := context.Background()

	t.Run("organization B user", func(t *testing.T) {
		victim := d.f.AppUser(t, d.f.OrgB, "del-foreign-victim")
		keyID := d.apiKey(t, d.f.OrgB, victim, "delv")
		token := d.session(t, victim, d.f.OrgB)
		if w := d.asAdminA(http.MethodDelete, "/api/v1/users/"+victim, ""); w.Code != http.StatusNotFound {
			t.Errorf("DELETE by an org A admin on an org B user: %d, want 404", w.Code)
		}
		if n := d.revoker.calls[victim]; n != 0 {
			t.Errorf("session revocations for the org B user: %d, want 0", n)
		}
		if at, err := d.revocations.RevokedAt(bg, victim); err != nil || !at.IsZero() {
			t.Errorf("blacklist entry of the org B user at %v (err %v), want none", at, err)
		}
		if _, err := d.issuer.Validate(token); err != nil {
			t.Errorf("org B user's session after the foreign DELETE: %v; want still valid", err)
		}
		if exists, keyRevoked, audits := d.deletionState(t, victim, keyID); !exists || keyRevoked || audits != 0 {
			t.Errorf("org B user after the foreign DELETE: exists=%v key revoked=%v user.deleted entries=%d; want unchanged", exists, keyRevoked, audits)
		}
	})

	for _, id := range []string{d.f.ID(), "not-a-uuid"} {
		t.Run("unknown id "+id, func(t *testing.T) {
			if w := d.asAdminA(http.MethodDelete, "/api/v1/users/"+id, ""); w.Code != http.StatusNotFound {
				t.Errorf("DELETE of an unknown user: %d, want 404", w.Code)
			}
			if n := d.revoker.calls[id]; n != 0 {
				t.Errorf("session revocations for an unknown user: %d, want 0", n)
			}
		})
	}

	t.Run("user who owns open objects", func(t *testing.T) {
		owner := d.f.AppUser(t, d.f.OrgA, "del-open-owner")
		keyID := d.apiKey(t, d.f.OrgA, owner, "delo")
		token := d.session(t, owner, d.f.OrgA)
		if _, err := d.f.Admin.Exec(bg, `INSERT INTO ticket (id, organization_id, title, reporter_id, assignee_id, status) VALUES ($1, $2, 'handover', $3, $4, 'open')`,
			d.f.ID(), d.f.OrgA, d.admin, owner); err != nil {
			t.Fatalf("seed ticket: %v", err)
		}
		if w := d.asAdminA(http.MethodDelete, "/api/v1/users/"+owner, ""); w.Code != http.StatusConflict {
			t.Errorf("DELETE the assignee of an open ticket: %d %s, want 409", w.Code, w.Body.String())
		}
		if n := d.revoker.calls[owner]; n != 0 {
			t.Errorf("session revocations for the owner of open objects: %d, want 0", n)
		}
		if _, err := d.issuer.Validate(token); err != nil {
			t.Errorf("owner's session after the refused DELETE: %v; want still valid", err)
		}
		if exists, keyRevoked, audits := d.deletionState(t, owner, keyID); !exists || keyRevoked || audits != 0 {
			t.Errorf("owner after the refused DELETE: exists=%v key revoked=%v user.deleted entries=%d; want unchanged", exists, keyRevoked, audits)
		}
	})

	t.Run("control: same organization", func(t *testing.T) {
		member := d.f.AppUser(t, d.f.OrgA, "del-member")
		keyID := d.apiKey(t, d.f.OrgA, member, "delm")
		token := d.session(t, member, d.f.OrgA)
		if w := d.asAdminA(http.MethodDelete, "/api/v1/users/"+member, ""); w.Code != http.StatusNoContent {
			t.Fatalf("DELETE an org A user: %d %s, want 204", w.Code, w.Body.String())
		}
		if _, err := d.issuer.Validate(token); !errors.Is(err, identity.ErrSessionRevoked) {
			t.Errorf("session of the deleted user: %v, want ErrSessionRevoked", err)
		}
		if n := d.revoker.calls[member]; n != 2 {
			t.Errorf("session revocations for the deleted user: %d, want 2 (in the transaction and after the commit)", n)
		}
		if exists, keyRevoked, audits := d.deletionState(t, member, keyID); exists || !keyRevoked || audits != 1 {
			t.Errorf("user after the DELETE: exists=%v key revoked=%v user.deleted entries=%d; want deleted, key revoked, 1 entry", exists, keyRevoked, audits)
		}
	})
}
