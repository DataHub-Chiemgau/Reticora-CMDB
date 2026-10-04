package identity_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

// TestFirstLoginRequiresAdmission covers WP-043 (AUT-01, AUT-09): the login
// resolves the app_user by subject, or by organization and verified e-mail,
// and creates or links a user only with an admission: the first user of an
// empty organization, an account created without subject, or a pending
// invitation, whose role and scope the user receives.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestFirstLoginRequiresAdmission(t *testing.T) {
	f := scopetest.Seed(t, "54")
	bg := context.Background()
	users := user.NewPGRepository(f.App)
	notPermitted := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, identity.ErrFirstLoginNotPermitted) {
			t.Errorf("%s: %v, want ErrFirstLoginNotPermitted", name, err)
		}
	}

	// Organization B has no user: its first login bootstraps it; the second
	// unknown user needs an admission. The returning user is recognized.
	first, err := users.EnsureUser(bg, f.OrgB, "fl54-b-first", "first@b.invalid", "First")
	if err != nil {
		t.Fatalf("bootstrap login: %v", err)
	}
	_, err = users.EnsureUser(bg, f.OrgB, "fl54-b-second", "second@b.invalid", "Second")
	notPermitted("second user without admission", err)
	if again, againErr := users.EnsureUser(bg, f.OrgB, "fl54-b-first", "first@b.invalid", "First"); againErr != nil || again != first {
		t.Errorf("returning user: %s, %v; want %s", again, againErr, first)
	}

	// Organization A has a user already.
	inviter := f.AppUser(t, f.OrgA, "fl54-inviter")

	// An unverified address (empty) admits nothing.
	_, err = users.EnsureUser(bg, f.OrgA, "fl54-unverified", "", "Unverified")
	notPermitted("unverified e-mail", err)

	// An account created by an administrator is linked on first login.
	var precreated string
	if err = f.Admin.QueryRow(bg, `INSERT INTO app_user (organization_id, email, display_name) VALUES ($1, 'Pre@A.invalid', 'Pre') RETURNING id::text`, f.OrgA).Scan(&precreated); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if id, linkErr := users.EnsureUser(bg, f.OrgA, "fl54-pre", "pre@a.invalid", "Pre"); linkErr != nil || id != precreated {
		t.Errorf("login of a created account: %s, %v; want %s", id, linkErr, precreated)
	}
	// The linked account is not taken over by another subject with the address.
	_, err = users.EnsureUser(bg, f.OrgA, "fl54-pre-other", "pre@a.invalid", "Pre")
	notPermitted("second subject for a linked address", err)

	// A pending invitation with client scope: the user gets the role in the
	// client and the invitation is accepted.
	roleID := f.ID()
	if _, err = f.Admin.Exec(bg, `INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 'fl54-reader', '["ci:read"]')`, roleID, f.OrgA); err != nil {
		t.Fatalf("seed role: %v", err)
	}
	invite := func(email, expires string) string {
		t.Helper()
		var id string
		if seedErr := f.Admin.QueryRow(bg, `
			INSERT INTO user_invitation (organization_id, email, role_id, scope_type, scope_id, invited_by, token_hash, expires_at)
			VALUES ($1, $2, $3, 'client', $4, $5, 'hash', now() + $6::interval) RETURNING id::text`,
			f.OrgA, email, roleID, f.Client1, inviter, expires).Scan(&id); seedErr != nil {
			t.Fatalf("seed invitation: %v", seedErr)
		}
		return id
	}
	invitation := invite("invited@a.invalid", "7 days")
	invited, err := users.EnsureUser(bg, f.OrgA, "fl54-invited", "Invited@A.invalid", "Invited")
	if err != nil {
		t.Fatalf("login with invitation: %v", err)
	}
	var accepted bool
	if err = f.Admin.QueryRow(bg, `SELECT accepted_at IS NOT NULL FROM user_invitation WHERE id = $1`, invitation).Scan(&accepted); err != nil || !accepted {
		t.Errorf("invitation after login: accepted=%v, %v", accepted, err)
	}
	var client string
	if err = f.Admin.QueryRow(bg, `SELECT COALESCE(scope_client_id::text, '') FROM role_assignment WHERE user_id = $1 AND role_id = $2`, invited, roleID).Scan(&client); err != nil || client != f.Client1 {
		t.Errorf("invited role assignment: client %q, %v; want client 1", client, err)
	}

	// An expired invitation admits nothing; neither does an invitation of
	// another organization's address.
	invite("late@a.invalid", "-1 minute")
	_, err = users.EnsureUser(bg, f.OrgA, "fl54-late", "late@a.invalid", "Late")
	notPermitted("expired invitation", err)
	_, err = users.EnsureUser(bg, f.OrgA, "fl54-stranger", "second@b.invalid", "Stranger")
	notPermitted("address without invitation", err)

	// The subject of an organization B user cannot enter organization A, even
	// with an invitation for its address.
	invite("first@b.invalid", "7 days")
	_, err = users.EnsureUser(bg, f.OrgA, "fl54-b-first", "first@b.invalid", "First")
	notPermitted("subject of another organization", err)
}
