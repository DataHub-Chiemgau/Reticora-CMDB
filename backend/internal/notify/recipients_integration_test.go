package notify_test

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
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

// TestNotificationLocaleAndSubscription covers NTF-03 (WP-588, F-322)
// against PostgreSQL: each recipient reads an event in their language —
// app_user.locale (en-US English, de-DE German), else the organization's
// locale (the account default and an invitee without an account) — with
// the organization's branding in e-mails: settings.branding.sender_name as
// sender and subject prefix, the organization's name in the footer; the
// in-app center gets the plain text. A user who unsubscribed a
// non-critical category (E-91 option 2) receives nothing of it, neither
// e-mail nor in-app (negative); a critical notification of an
// unsubscribed category still reaches them.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestNotificationLocaleAndSubscription(t *testing.T) {
	f := scopetest.Seed(t, "9a")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`UPDATE organization SET name = 'ACME GmbH', locale = 'en-US',
		settings = settings || '{"branding": {"sender_name": "ACME Service Desk"}}'::jsonb WHERE id = $1`, f.OrgA)
	role := f.ID()
	exec(`INSERT INTO role (id, organization_id, name, scope, permissions)
		VALUES ($1, $2, 'ntf3-ops', 'org', '["review:resolve", "discovery:manage"]'::jsonb)`, role, f.OrgA)
	users := map[string]string{}
	for name, locale := range map[string]string{"en": "en-US", "de": "de-DE", "default": "", "muted": "de-DE"} {
		users[name] = f.AppUser(t, f.OrgA, name)
		if locale != "" {
			exec(`UPDATE app_user SET locale = $2 WHERE id = $1`, users[name], locale)
		}
		exec(`INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, users[name], role)
	}
	mailOf := func(name string) string { return "scopetest-9a-" + name + "@scopetest.invalid" }

	key, _ := crypto.GenerateMasterKey()
	enc, _ := crypto.NewEnvelopeEncryptor(key)
	center := &notify.Center{Channels: notify.NewPGRepository(f.App, notify.NewSealer(enc)), Inbox: notify.NewPGInbox(f.App),
		Preferences: notify.NewPGPreferences(f.App), Deliveries: notify.NewPGDeliveryStore(f.App)}
	for _, ch := range []*notify.Channel{
		{OrganizationID: f.OrgA, Name: "mail", Provider: notify.ProviderCentralSMTP, Enabled: true},
		{OrganizationID: f.OrgA, Name: "center", Provider: notify.ProviderInApp, Enabled: true},
	} {
		if err := center.Channels.Create(bg, ch); err != nil {
			t.Fatal(err)
		}
	}
	// The muted user unsubscribes reviews and collectors.
	if err := center.Preferences.SetPreference(bg, f.OrgA, users["muted"], &notify.Preference{
		Subscriptions: map[string]string{"review": notify.SubscriptionMuted, "collector": notify.SubscriptionMuted}}); err != nil {
		t.Fatal(err)
	}
	if p, err := center.Preferences.Preference(bg, f.OrgA, users["muted"]); err != nil || p == nil ||
		strings.Join(p.MutedCategories, ",") != "collector,review" {
		t.Fatalf("stored subscriptions: %+v %v", p, err)
	}

	// A clock in the past: the run never claims deliveries of other tests.
	now := time.Date(2020, 1, 7, 10, 0, 0, 0, time.UTC)
	mail := &recordingProvider{}
	notifier := notify.NewNotifier(center.Channels, center.Deliveries, center.Preferences, notify.NewSender(map[string]notify.Provider{
		notify.ProviderCentralSMTP: mail, notify.ProviderInApp: notify.NewInAppProvider(center.Inbox),
	})).WithClock(func() time.Time { return now })
	consumer := notify.NewEventConsumer(notify.NewPGResolver(f.App, permission.NewPGRepository(f.App)), notifier)
	for typ, data := range map[string]map[string]any{
		events.ReviewCreated:    {"id": "r-1", "kind": "probable_duplicate"},
		events.CollectorOffline: {"id": "c-1", "name": "dc1-collector", "last_seen_at": "2020-01-07T09:00:00Z"},
		events.InvitationCreated: {"email": "invitee@acme.example", "organization_name": "ACME GmbH",
			"expires_at": "2020-01-14T10:00:00Z", "link": "https://cmdb.example/i"},
	} {
		raw, _ := json.Marshal(data)
		if err := consumer.Consume(bg, &events.Envelope{ID: f.ID(), Type: typ, OrgID: f.OrgA, OccurredAt: now, Data: raw}); err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
	}
	for range 3 {
		if _, err := notifier.ProcessDue(bg); err != nil {
			t.Fatal(err)
		}
	}
	sentTo := func(address, category string) []notify.Message {
		var out []notify.Message
		for _, m := range mail.sent {
			if m.Category == category && len(m.To) == 1 && m.To[0].Email == address {
				out = append(out, m)
			}
		}
		return out
	}
	const (
		enReview = "A new review item (probable_duplicate) awaits a decision."
		enFooter = "\n\n-- \nThis notification was sent by Reticora on behalf of ACME GmbH."
		deFooter = "\n\n-- \nDiese Benachrichtigung wurde von Reticora im Auftrag von ACME GmbH versendet."
	)
	for _, tc := range []struct{ address, category, subject, body, footer string }{
		{mailOf("en"), "review", "New review item: probable_duplicate", enReview, enFooter},
		{mailOf("de"), "review", "Neues Review-Item: probable_duplicate",
			"Ein neues Review-Item (probable_duplicate) wartet auf eine Entscheidung.", deFooter},
		{mailOf("default"), "review", "New review item: probable_duplicate", enReview, enFooter},
		{mailOf("en"), "collector", "Collector offline: dc1-collector",
			"Collector dc1-collector has not reported since 2020-01-07T09:00:00Z.", enFooter},
		{mailOf("muted"), "collector", "Collector offline: dc1-collector",
			"Der Collector dc1-collector meldet sich seit 2020-01-07T09:00:00Z nicht mehr.", deFooter},
		{"invitee@acme.example", "invitation", "Invitation to Reticora",
			"You have been invited to ACME GmbH. The invitation is valid until 2020-01-14T10:00:00Z:\nhttps://cmdb.example/i", enFooter},
	} {
		got := sentTo(tc.address, tc.category)
		if len(got) != 1 {
			t.Errorf("%s mails to %s: %d, want 1", tc.category, tc.address, len(got))
			continue
		}
		if m := got[0]; m.Subject != "[ACME Service Desk] "+tc.subject || m.Body != tc.body+tc.footer || m.SenderName != "ACME Service Desk" {
			t.Errorf("%s mail to %s: subject %q, body %q, sender %q; want %q, %q, ACME Service Desk",
				tc.category, tc.address, m.Subject, m.Body, m.SenderName, "[ACME Service Desk] "+tc.subject, tc.body+tc.footer)
		}
	}
	for name, subject := range map[string]string{"en": "New review item: probable_duplicate", "de": "Neues Review-Item: probable_duplicate"} {
		items, _, err := center.Inbox.List(bg, f.OrgA, users[name], false, 50, 0)
		if err != nil || countCategory(items, "review") != 1 {
			t.Fatalf("in-app reviews of %s: %d (%v), want 1", name, countCategory(items, "review"), err)
		}
		for _, it := range items {
			if it.Category == "review" && (it.Subject != subject || strings.Contains(it.Body, "ACME")) {
				t.Errorf("in-app review of %s: %q %q, want %q without branding", name, it.Subject, it.Body, subject)
			}
		}
	}

	// Negative: the unsubscribed non-critical category reaches the muted
	// user neither by e-mail nor in-app; its deliveries are suppressed.
	if got := sentTo(mailOf("muted"), "review"); len(got) != 0 {
		t.Errorf("review mails to the user who unsubscribed reviews: %d, want 0", len(got))
	}
	items, _, err := center.Inbox.List(bg, f.OrgA, users["muted"], false, 50, 0)
	if err != nil || countCategory(items, "review") != 0 || countCategory(items, "collector") != 1 {
		t.Errorf("in-app of the muted user: %d reviews, %d collector (%v), want 0 and 1", countCategory(items, "review"),
			countCategory(items, "collector"), err)
	}
	var sent, suppressed int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FILTER (WHERE status = 'sent'), count(*) FILTER (WHERE status = 'suppressed')
		FROM notification_delivery WHERE organization_id = $1 AND recipient_user_id = $2 AND category = 'review'`,
		f.OrgA, users["muted"]).Scan(&sent, &suppressed); err != nil || sent != 0 || suppressed != 2 {
		t.Errorf("review deliveries of the muted user: %d sent, %d suppressed (%v), want 0 and 2", sent, suppressed, err)
	}

	// The branding of each organization is its own.
	resolver := notify.NewPGResolver(f.App, permission.NewPGRepository(f.App))
	if b, err := resolver.Branding(bg, f.OrgB); err != nil || b == nil || b.SenderName != "scopetest-9a-b" || b.Locale != "de-DE" {
		t.Errorf("branding of B: %+v %v, want its own name and locale", b, err)
	}
}
