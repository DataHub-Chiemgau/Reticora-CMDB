package identity_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

// TestLoginTakesLocaleClaim covers NTF-03 (WP-653, F-383): the OIDC
// callback with a fake IdP and the provisioning of user.PGRepository, as
// router.go wires them, stores the language of the ID token's locale claim
// (de* → de-DE, en* → en-US) as the user's notification language
// (app_user.locale) — for a user the login creates (invitation), an
// account an administrator created (linked by the login) and a returning
// user without a choice (the column default de). A language chosen in the
// preferences, an explicit choice of the organization's locale (empty) and
// a language taken from an earlier login stay; a claim of another language,
// one that is no string and a token without the claim change nothing and
// do not fail the login. The stored language decides the language of the
// user's notifications (notify.TemplateLocale, organization de-DE).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestLoginTakesLocaleClaim(t *testing.T) {
	f := scopetest.Seed(t, "61")
	bg := context.Background()
	idp := newScopeIdP(t, f.OrgA)
	handler := identity.NewHandler(idp.provider(), newIssuer(t)).
		WithProvisioning(user.NewPGRepository(f.App), "viewer").
		WithAccessResolver(permission.NewPGRepository(f.App))

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	var viewer string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM role WHERE organization_id = $1 AND name = 'viewer'`, f.OrgA).Scan(&viewer); err != nil {
		t.Fatalf("standard role viewer: %v", err)
	}
	// existing creates a user with the subject scopetest-61-<name>, the
	// stored locale (nil: the column default) and the role viewer.
	existing := func(name string, locale *string) string {
		t.Helper()
		id := f.AppUser(t, f.OrgA, name)
		if locale != nil {
			exec(`UPDATE app_user SET locale = $2 WHERE id = $1`, id, *locale)
		}
		exec(`INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, id, viewer)
		return id
	}
	localeOf := func(email string) string {
		t.Helper()
		var got string
		if err := f.Admin.QueryRow(bg, `SELECT locale FROM app_user WHERE organization_id = $1 AND lower(email) = lower($2)`,
			f.OrgA, email).Scan(&got); err != nil {
			t.Fatalf("locale of %s: %v", email, err)
		}
		return got
	}
	login := func(subject, email string, claim any) {
		t.Helper()
		if claim != nil {
			idp.claims[subject] = map[string]any{"locale": claim}
		} else {
			delete(idp.claims, subject)
		}
		if rec := idp.login(handler, subject, email); rec.Code != http.StatusOK || !hasRefreshCookie(rec) {
			t.Fatalf("login of %s with locale claim %v: %d %s, want 200 with a session", subject, claim, rec.Code, rec.Body.String())
		}
	}
	str := func(s string) *string { return &s }

	// A user the login creates: an invitation of organization A.
	inviter := existing("inviter", nil)
	exec(`INSERT INTO user_invitation (organization_id, email, role_id, scope_type, invited_by, token_hash, expires_at)
		VALUES ($1, 'invited-en@scopetest.invalid', $2, 'org', $3, 'hash-653', now() + interval '7 days')`, f.OrgA, viewer, inviter)
	// An account an administrator created without subject.
	exec(`INSERT INTO app_user (organization_id, email, display_name) VALUES ($1, 'created-de@scopetest.invalid', 'Created')`, f.OrgA)
	// Returning users: without a choice, with a chosen language, with the
	// organization's locale chosen explicitly.
	existing("returning", nil)
	existing("chosen", str("de-DE"))
	existing("orgdefault", str(""))
	existing("french", nil)
	existing("number", nil)
	existing("none", nil)

	for _, tc := range []struct {
		name, subject, email string
		claim                any
		want                 string
	}{
		{"user created by the login (invitation)", "lc-invited", "invited-en@scopetest.invalid", "en", "en-US"},
		{"account created by an administrator", "lc-created", "created-de@scopetest.invalid", "de-AT", "de-DE"},
		{"returning user without a choice", "scopetest-61-returning", "scopetest-61-returning@scopetest.invalid", "en_GB", "en-US"},
		{"chosen language", "scopetest-61-chosen", "scopetest-61-chosen@scopetest.invalid", "en", "de-DE"},
		{"organization's locale chosen", "scopetest-61-orgdefault", "scopetest-61-orgdefault@scopetest.invalid", "en-US", ""},
		{"language without templates", "scopetest-61-french", "scopetest-61-french@scopetest.invalid", "fr-FR", "de"},
		{"claim that is no string", "scopetest-61-number", "scopetest-61-number@scopetest.invalid", 42, "de"},
		{"token without the claim", "scopetest-61-none", "scopetest-61-none@scopetest.invalid", nil, "de"},
		// A language taken from an earlier login counts as chosen.
		{"second login of the created user", "lc-invited", "invited-en@scopetest.invalid", "de", "en-US"},
	} {
		login(tc.subject, tc.email, tc.claim)
		if got := localeOf(tc.email); got != tc.want {
			t.Errorf("%s: app_user.locale %q after the login with locale claim %v, want %q (F-383)", tc.name, got, tc.claim, tc.want)
		}
	}

	// The notifications follow the stored language; the organization's
	// locale is de-DE.
	for email, want := range map[string]string{
		"invited-en@scopetest.invalid":              notify.LocaleEN,
		"created-de@scopetest.invalid":              notify.LocaleDE,
		"scopetest-61-returning@scopetest.invalid":  notify.LocaleEN,
		"scopetest-61-orgdefault@scopetest.invalid": notify.LocaleDE,
	} {
		if got := notify.TemplateLocale(localeOf(email), "de-DE"); got != want {
			t.Errorf("notification language of %s: %s, want %s", email, got, want)
		}
	}
}
