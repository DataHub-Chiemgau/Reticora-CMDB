package notify_test

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

// TestRecipientsAgainstPostgres covers NTF-03 (WP-138) against
// PostgreSQL: recipients come from roles, teams, subscriptions and the
// contact roles of a CI; a user receives only what they may read — the
// read permission in the object's client and site; inactive users and
// other organizations never; every address once.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRecipientsAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "9a")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	role := func(org, name, scope, perms string) string {
		t.Helper()
		id := f.ID()
		exec(`INSERT INTO role (id, organization_id, name, scope, permissions) VALUES ($1, $2, $3, $4, $5::jsonb)`, id, org, name, scope, perms)
		return id
	}
	assign := func(org, user, roleID string, client any) {
		t.Helper()
		exec(`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id) VALUES ($1, $2, $3, $4)`, org, user, roleID, client)
	}
	ops := role(f.OrgA, "ntf-ops", "org", `["ci:read"]`)
	clientRole := role(f.OrgA, "ntf-client", "client", `["ci:read"]`)
	assetOnly := role(f.OrgA, "ntf-asset", "org", `["asset:read"]`)
	orgUser := f.AppUser(t, f.OrgA, "org")
	c1User := f.AppUser(t, f.OrgA, "c1")
	c2User := f.AppUser(t, f.OrgA, "c2")
	teamUser := f.AppUser(t, f.OrgA, "team")
	noPerm := f.AppUser(t, f.OrgA, "noperm")
	inactive := f.AppUser(t, f.OrgA, "inactive")
	assign(f.OrgA, orgUser, ops, nil)
	assign(f.OrgA, c1User, clientRole, f.Client1)
	assign(f.OrgA, c2User, clientRole, f.Client2)
	assign(f.OrgA, teamUser, clientRole, f.Client1)
	assign(f.OrgA, noPerm, assetOnly, nil)
	assign(f.OrgA, inactive, ops, nil)
	exec(`UPDATE app_user SET is_active = false WHERE id = $1`, inactive)
	team := f.ID()
	exec(`INSERT INTO team (id, organization_id, name) VALUES ($1, $2, 'ntf-team')`, team, f.OrgA)
	exec(`INSERT INTO team_member (organization_id, team_id, user_id) VALUES ($1, $2, $3)`, f.OrgA, team, teamUser)
	bRole := role(f.OrgB, "ntf-ops", "org", `["ci:read"]`)
	assign(f.OrgB, f.AppUser(t, f.OrgB, "b"), bRole, nil)

	ciID := f.CI(t, f.OrgA, f.Client1, "ntf-ci")
	contact := func(name, email, rel string) {
		t.Helper()
		id := f.ID()
		exec(`INSERT INTO contact (id, organization_id, client_id, display_name, email) VALUES ($1, $2, $3, $4, $5)`, id, f.OrgA, f.Client1, name, email)
		exec(`INSERT INTO ci_contact (organization_id, ci_id, contact_id, relationship_type, client_id) VALUES ($1, $2, $3, $4, $5)`,
			f.OrgA, ciID, id, rel, f.Client1)
	}
	contact("Owner", "owner@acme.example", "owner")
	contact("Vendor", "vendor@acme.example", "vendor")
	contact("Org User Again", "scopetest-9a-org@scopetest.invalid", "escalation")

	resolver := notify.NewPGResolver(f.App, permission.NewPGRepository(f.App))
	names := func(a *notify.Audience) string {
		t.Helper()
		got, err := resolver.Resolve(bg, f.OrgA, a)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(got))
		for i, r := range got {
			if r.UserID != "" {
				out[i] = strings.TrimPrefix(r.Name, "scopetest-9a-")
			} else {
				out[i] = "contact:" + r.Name
			}
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	for name, tc := range map[string]struct {
		audience notify.Audience
		want     string
	}{
		"roles, object of client 1": {notify.Audience{Permission: identity.PermCIRead, ClientID: f.Client1,
			Roles: []string{"ntf-ops", "ntf-client"}}, "c1,org,team"},
		"roles, organization-wide object": {notify.Audience{Permission: identity.PermCIRead,
			Roles: []string{"ntf-ops", "ntf-client"}}, "org"},
		"everyone with the permission, client 2": {notify.Audience{Permission: identity.PermCIRead, ClientID: f.Client2,
			AllWithPermission: true}, "c2,org"},
		"subscriptions without the scope": {notify.Audience{Permission: identity.PermCIRead, ClientID: f.Client1,
			UserIDs: []string{noPerm, c2User, inactive}}, ""},
		"team": {notify.Audience{Permission: identity.PermCIRead, ClientID: f.Client1, TeamIDs: []string{team}}, "team"},
		"CI contacts and a user": {notify.Audience{Permission: identity.PermCIRead, ClientID: f.Client1, UserIDs: []string{orgUser},
			CIID: ciID, ContactRoles: []string{"owner", "escalation"}}, "contact:Owner,org"},
		"other permission": {notify.Audience{Permission: identity.PermAssetRead, AllWithPermission: true}, "noperm"},
	} {
		if got := names(&tc.audience); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
	if _, err := resolver.Resolve(bg, f.OrgA, &notify.Audience{Roles: []string{"ntf-ops"}}); err == nil {
		t.Error("an audience of users without permission was resolved")
	}
}
