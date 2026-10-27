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
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

// TestRecipientsAgainstPostgres covers NTF-03 (WP-138, WP-507) against
// PostgreSQL: recipients come from roles, teams, subscriptions, the
// contact roles of a CI and the contacts of the object's client (resolved
// through the PII vault); a user receives only what they may read — the
// read permission in the object's client and site; a contact of another
// client never, an organization-wide contact only on the explicit
// organization target; inactive users and other organizations never;
// every address once.
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

	// Contacts of the clients keep name and e-mail only in the PII vault
	// (AUD-01): the plaintext columns stay empty.
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	vault := pii.NewVault(f.App, enc)
	pii.SetDefault(vault)
	t.Cleanup(func() { pii.SetDefault(nil) })
	vaultContact := func(org string, client any, name, email, role string) {
		t.Helper()
		subject, err := vault.Create(f.OrgCtx(org), org, pii.KindContact, pii.Data{"name": name, "email": email})
		if err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO contact (id, organization_id, client_id, role, pii_subject_id) VALUES ($1, $2, $3, $4, $5)`,
			f.ID(), org, client, role, subject)
	}
	vaultContact(f.OrgA, f.Client1, "C1 Lead", "lead@c1.example", "IT-Leitung")
	vaultContact(f.OrgA, f.Client1, "C1 Tech", "tech@c1.example", "Technik")
	vaultContact(f.OrgA, f.Client2, "C2 Lead", "lead@c2.example", "IT-Leitung")
	vaultContact(f.OrgA, nil, "Org Desk", "desk@acme.example", "it-leitung")
	vaultContact(f.OrgB, nil, "B Desk", "desk@b.example", "IT-Leitung")

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
		"contacts of client 1": {notify.Audience{ClientID: f.Client1, ClientContacts: &notify.ClientContacts{}},
			"contact:C1 Lead,contact:C1 Tech,contact:Org User Again,contact:Owner,contact:Vendor"},
		"contacts of client 1 in a role": {notify.Audience{ClientID: f.Client1,
			ClientContacts: &notify.ClientContacts{ContactRoles: []string{" it-leitung"}}}, "contact:C1 Lead"},
		"contacts of client 2": {notify.Audience{ClientID: f.Client2, ClientContacts: &notify.ClientContacts{}}, "contact:C2 Lead"},
		"client contacts and CI contacts, each once": {notify.Audience{ClientID: f.Client1, CIID: ciID, ContactRoles: []string{"owner"},
			ClientContacts: &notify.ClientContacts{}}, "contact:C1 Lead,contact:C1 Tech,contact:Org User Again,contact:Owner,contact:Vendor"},
		"client contacts in roles and CI contacts": {notify.Audience{ClientID: f.Client1, CIID: ciID, ContactRoles: []string{"owner"},
			ClientContacts: &notify.ClientContacts{ContactRoles: []string{"IT-Leitung", "Technik"}}}, "contact:C1 Lead,contact:C1 Tech,contact:Owner"},
		"client contacts of an organization-wide object": {notify.Audience{ClientContacts: &notify.ClientContacts{}}, ""},
		"explicit organization target":                   {notify.Audience{ClientContacts: &notify.ClientContacts{Organization: true}}, "contact:Org Desk"},
		"client 2 and the organization in a role": {notify.Audience{ClientID: f.Client2,
			ClientContacts: &notify.ClientContacts{ContactRoles: []string{"IT-Leitung"}, Organization: true}}, "contact:C2 Lead,contact:Org Desk"},
		"client contacts and a user without the scope": {notify.Audience{Permission: identity.PermCIRead, ClientID: f.Client2,
			UserIDs: []string{c1User}, ClientContacts: &notify.ClientContacts{}}, "contact:C2 Lead"},
	} {
		if got := names(&tc.audience); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
	if _, err := resolver.Resolve(bg, f.OrgA, &notify.Audience{Roles: []string{"ntf-ops"}}); err == nil {
		t.Error("an audience of users without permission was resolved")
	}
}

// TestRecipientsRespectSiteScope covers the site scope of NTF-03 (WP-507)
// against PostgreSQL: a user whose role is granted at site s1 receives a
// notification about an object at s1, but not about one at s1b of the
// same client nor about an object of the client without a site; org-wide
// and client-wide holders receive both.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRecipientsRespectSiteScope(t *testing.T) {
	f := scopetest.Seed(t, "9a")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	site := func(name string) string {
		t.Helper()
		id := f.ID()
		exec(`INSERT INTO site (id, organization_id, client_id, name) VALUES ($1, $2, $3, $4)`, id, f.OrgA, f.Client1, name)
		return id
	}
	s1, s1b := site("ntf-s1"), site("ntf-s1b")
	role := func(name, scope string) string {
		t.Helper()
		id := f.ID()
		exec(`INSERT INTO role (id, organization_id, name, scope, permissions) VALUES ($1, $2, $3, $4, '["ci:read"]'::jsonb)`, id, f.OrgA, name, scope)
		return id
	}
	orgRole, clientRole, siteRole := role("ntf-site-ops", "org"), role("ntf-site-client", "client"), role("ntf-site-tech", "site")
	assign := func(user, roleID string, client, site any) {
		t.Helper()
		exec(`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id, scope_site_id) VALUES ($1, $2, $3, $4, $5)`,
			f.OrgA, user, roleID, client, site)
	}
	orgUser, clientUser := f.AppUser(t, f.OrgA, "org"), f.AppUser(t, f.OrgA, "c1")
	s1User, s1bUser := f.AppUser(t, f.OrgA, "s1"), f.AppUser(t, f.OrgA, "s1b")
	assign(orgUser, orgRole, nil, nil)
	assign(clientUser, clientRole, f.Client1, nil)
	assign(s1User, siteRole, nil, s1)
	assign(s1bUser, siteRole, nil, s1b)

	resolver := notify.NewPGResolver(f.App, permission.NewPGRepository(f.App))
	for name, tc := range map[string]struct {
		site string
		want string
	}{
		"object at s1":                  {s1, "c1,org,s1"},
		"object at s1b":                 {s1b, "c1,org,s1b"},
		"object of the client, no site": {"", "c1,org"},
		"object at another site of c1":  {f.ID(), "c1,org"},
	} {
		for _, a := range []notify.Audience{
			{Permission: identity.PermCIRead, ClientID: f.Client1, SiteID: tc.site, AllWithPermission: true},
			{Permission: identity.PermCIRead, ClientID: f.Client1, SiteID: tc.site, UserIDs: []string{orgUser, clientUser, s1User, s1bUser}},
		} {
			got, err := resolver.Resolve(bg, f.OrgA, &a)
			if err != nil {
				t.Fatal(err)
			}
			names := make([]string, len(got))
			for i, r := range got {
				names[i] = strings.TrimPrefix(r.Name, "scopetest-9a-")
			}
			sort.Strings(names)
			if got := strings.Join(names, ","); got != tc.want {
				t.Errorf("%s (all users: %v): %q, want %q", name, a.AllWithPermission, got, tc.want)
			}
		}
	}
}
