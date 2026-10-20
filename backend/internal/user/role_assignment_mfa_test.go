package user

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// MFA binding of every org_admin grant (WP-358, AUT-09, CH26, E-14, finding
// F-005): role assignment, invitation acceptance, first login and the
// reconciliation of existing org_admins map the Keycloak realm role
// mfa_required to the account before the grant commits; a failing binding
// fails the grant.
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -v -run 'MFA' ./internal/user/

// fakeMFA records the bindings; fail makes RequireMFA fail for the listed
// subjects (all when it contains "*"), accounts answers AccountByEmail.
type fakeMFA struct {
	mu       sync.Mutex
	required []string
	lookups  []string
	accounts map[string]string
	fail     []string
}

func (m *fakeMFA) RequireMFA(_ context.Context, subject string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if slices.Contains(m.fail, "*") || slices.Contains(m.fail, subject) {
		return errors.New("keycloak: role mapping: status 403")
	}
	m.required = append(m.required, subject)
	return nil
}

func (m *fakeMFA) AccountByEmail(_ context.Context, email string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lookups = append(m.lookups, email)
	return m.accounts[strings.ToLower(email)], nil
}

func (m *fakeMFA) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.required, m.lookups, m.fail = nil, nil, nil
}

func (m *fakeMFA) bound() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.required)
}

func TestBindMFAWithoutEnforcerBindsNothing(t *testing.T) {
	if err := bindMFA(context.Background(), nil, "subject", "a@b.example"); err != nil {
		t.Fatalf("bindMFA without enforcer: %v", err)
	}
}

func TestUnavailableMFAFailsClosed(t *testing.T) {
	err := bindMFA(context.Background(), UnavailableMFA{}, "subject", "")
	if !errors.Is(err, ErrMFAUnavailable) || !errors.Is(err, identity.ErrMFAUnavailable) {
		t.Errorf("bindMFA(UnavailableMFA) = %v, want ErrMFAUnavailable", err)
	}
	if err = bindMFA(context.Background(), UnavailableMFA{}, "", "a@b.example"); !errors.Is(err, ErrMFAUnavailable) {
		t.Errorf("bindMFA(UnavailableMFA) by e-mail = %v, want ErrMFAUnavailable", err)
	}
}

// mfaFixture is a seeded database with the user handler on the PostgreSQL
// repository bound to a fake enforcer.
type mfaFixture struct {
	*scopetest.Fixture
	t    *testing.T
	mfa  *fakeMFA
	repo *PGRepository
	mux  *chi.Mux
}

func newMFAFixture(t *testing.T, tag string) *mfaFixture {
	f := scopetest.Seed(t, tag)
	m := &mfaFixture{Fixture: f, t: t, mfa: &fakeMFA{accounts: map[string]string{}}}
	m.repo = NewPGRepository(f.App).WithMFA(m.mfa)
	m.mux = chi.NewRouter()
	NewHandler(m.repo).RegisterRoutes(m.mux)
	return m
}

func (m *mfaFixture) role(orgID, name string) string {
	m.t.Helper()
	var id string
	if err := m.Admin.QueryRow(context.Background(), `SELECT id::text FROM role WHERE organization_id = $1 AND name = $2`, orgID, name).Scan(&id); err != nil {
		m.t.Fatalf("role %s: %v", name, err)
	}
	return id
}

// userWithoutSubject inserts a user as POST /users creates it: no IdP
// account linked yet.
func (m *mfaFixture) userWithoutSubject(orgID, email string) string {
	m.t.Helper()
	id := m.ID()
	if _, err := m.Admin.Exec(context.Background(), `INSERT INTO app_user (id, organization_id, email, display_name) VALUES ($1, $2, $3, $3)`,
		id, orgID, email); err != nil {
		m.t.Fatalf("insert user %s: %v", email, err)
	}
	return id
}

func (m *mfaFixture) subject(userID string) string {
	m.t.Helper()
	var s string
	if err := m.Admin.QueryRow(context.Background(), `SELECT COALESCE(oidc_subject, '') FROM app_user WHERE id = $1`, userID).Scan(&s); err != nil {
		m.t.Fatalf("subject of %s: %v", userID, err)
	}
	return s
}

func (m *mfaFixture) assignments(userID, roleID string) int {
	m.t.Helper()
	var n int
	if err := m.Admin.QueryRow(context.Background(), `SELECT count(*) FROM role_assignment WHERE user_id = $1 AND role_id = $2`, userID, roleID).Scan(&n); err != nil {
		m.t.Fatalf("count assignments: %v", err)
	}
	return n
}

func (m *mfaFixture) grant(orgID, userID, roleID string) {
	m.t.Helper()
	if _, err := m.Admin.Exec(context.Background(), `INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, orgID, userID, roleID); err != nil {
		m.t.Fatalf("grant: %v", err)
	}
}

// assign posts a role assignment as an org-wide administrator of OrgA.
func (m *mfaFixture) assign(path, userID, roleID string) *httptest.ResponseRecorder {
	ctx := tenant.WithTenant(m.OrgCtx(m.OrgA), tenant.TenantInfo{OrganizationID: m.OrgA, UserID: m.User})
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"user_id":"`+userID+`","role_id":"`+roleID+`"}`)).WithContext(ctx)
	w := httptest.NewRecorder()
	m.mux.ServeHTTP(w, req)
	return w
}

// invite stores a pending invitation of OrgA for email with the role.
func (m *mfaFixture) invite(email, roleID string) string {
	m.t.Helper()
	var id string
	if err := m.Admin.QueryRow(context.Background(), `INSERT INTO user_invitation (organization_id, email, role_id, invited_by, token_hash, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`, m.OrgA, email, roleID, m.User, "hash-"+email, time.Now().Add(time.Hour)).Scan(&id); err != nil {
		m.t.Fatalf("invite %s: %v", email, err)
	}
	return id
}

func (m *mfaFixture) invitationOpen(id string) bool {
	m.t.Helper()
	var open bool
	if err := m.Admin.QueryRow(context.Background(), `SELECT accepted_at IS NULL FROM user_invitation WHERE id = $1`, id).Scan(&open); err != nil {
		m.t.Fatalf("read invitation: %v", err)
	}
	return open
}

func (m *mfaFixture) userOfEmail(orgID, email string) bool {
	m.t.Helper()
	var exists bool
	if err := m.Admin.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM app_user WHERE organization_id = $1 AND lower(email) = lower($2))`, orgID, email).Scan(&exists); err != nil {
		m.t.Fatalf("read user: %v", err)
	}
	return exists
}

// TestRoleAssignmentOrgAdminRequiresMFA (WP-358): assigning the standard
// role org_admin binds the account of the target user exactly once before
// the commit (201); other roles bind nothing; a failing binding answers 503
// problem+json and assigns nothing; a user of another organization is 404
// without binding. A user without oidc subject is bound through the account
// of its address; without such an account the first login binds the
// account it links.
func TestRoleAssignmentOrgAdminRequiresMFA(t *testing.T) {
	m := newMFAFixture(t, "7a")
	ctx := context.Background()
	orgAdmin, engineer := m.role(m.OrgA, "org_admin"), m.role(m.OrgA, "engineer")

	for _, path := range []string{"/api/v1/role-assignments", "/api/v1/roles/assign"} {
		m.mfa.reset()
		target := m.AppUser(t, m.OrgA, "target"+strings.ReplaceAll(path, "/", "-"))
		if w := m.assign(path, target, orgAdmin); w.Code != http.StatusCreated {
			t.Fatalf("%s org_admin: %d %s", path, w.Code, w.Body.String())
		}
		if got := m.mfa.bound(); !slices.Equal(got, []string{m.subject(target)}) {
			t.Errorf("%s org_admin: bound %v, want exactly [%s]", path, got, m.subject(target))
		}
		if n := m.assignments(target, orgAdmin); n != 1 {
			t.Errorf("%s org_admin: %d assignments, want 1", path, n)
		}
	}

	// Another role binds nothing.
	m.mfa.reset()
	eng := m.AppUser(t, m.OrgA, "engineer")
	if w := m.assign("/api/v1/role-assignments", eng, engineer); w.Code != http.StatusCreated {
		t.Fatalf("engineer: %d %s", w.Code, w.Body.String())
	}
	if got := m.mfa.bound(); len(got) != 0 || len(m.mfa.lookups) != 0 {
		t.Errorf("engineer: bound %v, lookups %v; want none", got, m.mfa.lookups)
	}

	// A failing binding: 503, nothing assigned.
	m.mfa.reset()
	m.mfa.fail = []string{"*"}
	refused := m.AppUser(t, m.OrgA, "refused")
	w := m.assign("/api/v1/role-assignments", refused, orgAdmin)
	if w.Code != http.StatusServiceUnavailable || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") {
		t.Fatalf("failing binding: %d %q %s, want 503 problem+json", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
	if strings.Contains(w.Body.String(), "403") || strings.Contains(w.Body.String(), "keycloak") {
		t.Errorf("503 body names the admin API: %s", w.Body.String())
	}
	if n := m.assignments(refused, orgAdmin); n != 0 {
		t.Errorf("failing binding: %d assignments, want none", n)
	}

	// A user of another organization: 404, no binding, nothing stored.
	m.mfa.reset()
	foreign := m.AppUser(t, m.OrgB, "foreign")
	if w := m.assign("/api/v1/role-assignments", foreign, orgAdmin); w.Code != http.StatusNotFound {
		t.Errorf("foreign user: %d %s, want 404", w.Code, w.Body.String())
	}
	if got := m.mfa.bound(); len(got) != 0 || len(m.mfa.lookups) != 0 {
		t.Errorf("foreign user: bound %v, lookups %v; want none", got, m.mfa.lookups)
	}
	if n := m.assignments(foreign, orgAdmin); n != 0 {
		t.Errorf("foreign user: %d assignments, want none", n)
	}

	// Without oidc subject, an account of the address exists: it is bound.
	m.mfa.reset()
	m.mfa.accounts["known-7a@mfa.example"] = "kc-known"
	known := m.userWithoutSubject(m.OrgA, "Known-7a@mfa.example")
	if w := m.assign("/api/v1/role-assignments", known, orgAdmin); w.Code != http.StatusCreated {
		t.Fatalf("user without subject, known account: %d %s", w.Code, w.Body.String())
	}
	if got := m.mfa.bound(); !slices.Equal(got, []string{"kc-known"}) || !slices.Equal(m.mfa.lookups, []string{"Known-7a@mfa.example"}) {
		t.Errorf("user without subject, known account: bound %v, lookups %v", got, m.mfa.lookups)
	}

	// Without oidc subject and without account: assigned, bound at the
	// first login, which links the subject.
	m.mfa.reset()
	later := m.userWithoutSubject(m.OrgA, "later-7a@mfa.example")
	if w := m.assign("/api/v1/role-assignments", later, orgAdmin); w.Code != http.StatusCreated {
		t.Fatalf("user without account: %d %s", w.Code, w.Body.String())
	}
	if got := m.mfa.bound(); len(got) != 0 {
		t.Errorf("user without account: bound %v before any account exists", got)
	}
	// The first login fails while the binding fails, and links nothing.
	m.mfa.fail = []string{"*"}
	if _, err := m.repo.EnsureUser(ctx, m.OrgA, "kc-later", "later-7a@mfa.example", "Later"); !errors.Is(err, identity.ErrMFAUnavailable) {
		t.Fatalf("first login with failing binding: %v, want identity.ErrMFAUnavailable", err)
	}
	if s := m.subject(later); s != "" {
		t.Errorf("failed first login linked the subject %q", s)
	}
	m.mfa.fail = nil
	id, err := m.repo.EnsureUser(ctx, m.OrgA, "kc-later", "later-7a@mfa.example", "Later")
	if err != nil || id != later {
		t.Fatalf("first login: %q, %v; want %s", id, err, later)
	}
	if got := m.mfa.bound(); !slices.Equal(got, []string{"kc-later"}) {
		t.Errorf("first login of the org_admin: bound %v, want [kc-later]", got)
	}
	// Later logins of the returning user bind nothing more.
	if _, err = m.repo.EnsureUser(ctx, m.OrgA, "kc-later", "later-7a@mfa.example", "Later"); err != nil {
		t.Fatal(err)
	}
	if got := m.mfa.bound(); len(got) != 1 {
		t.Errorf("returning login: bound %v, want no new binding", got)
	}

	// A linked account without org_admin binds nothing at its first login.
	m.mfa.reset()
	m.userWithoutSubject(m.OrgA, "plain-7a@mfa.example")
	if _, err = m.repo.EnsureUser(ctx, m.OrgA, "kc-plain", "plain-7a@mfa.example", "Plain"); err != nil {
		t.Fatal(err)
	}
	if got := m.mfa.bound(); len(got) != 0 {
		t.Errorf("first login without org_admin: bound %v", got)
	}

	// A repository without enforcer (handler and server tests) binds
	// nothing and still assigns.
	plain := chi.NewRouter()
	NewHandler(NewPGRepository(m.App)).RegisterRoutes(plain)
	m.mux = plain
	m.mfa.reset()
	unbound := m.AppUser(t, m.OrgA, "unbound")
	if w := m.assign("/api/v1/role-assignments", unbound, orgAdmin); w.Code != http.StatusCreated {
		t.Errorf("without enforcer: %d %s", w.Code, w.Body.String())
	}
	if got := m.mfa.bound(); len(got) != 0 {
		t.Errorf("without enforcer: bound %v", got)
	}
}

// TestFirstLoginWithOrgAdminInvitationRequiresMFA (WP-358): the first login
// that accepts a pending org_admin invitation binds the subject exactly
// once before the commit; when the binding fails, the login fails, nothing
// is assigned and the invitation stays open. A viewer invitation binds
// nothing.
func TestFirstLoginWithOrgAdminInvitationRequiresMFA(t *testing.T) {
	m := newMFAFixture(t, "7b")
	ctx := context.Background()
	orgAdmin, viewer := m.role(m.OrgA, "org_admin"), m.role(m.OrgA, "viewer")

	inv := m.invite("admin-7b@mfa.example", orgAdmin)
	m.mfa.fail = []string{"kc-admin-7b"}
	if _, err := m.repo.EnsureUser(ctx, m.OrgA, "kc-admin-7b", "admin-7b@mfa.example", "Admin"); !errors.Is(err, identity.ErrMFAUnavailable) {
		t.Fatalf("first login with failing binding: %v, want identity.ErrMFAUnavailable", err)
	}
	if !m.invitationOpen(inv) || m.userOfEmail(m.OrgA, "admin-7b@mfa.example") {
		t.Fatalf("failed login: invitation open %v, user created %v; want open and no user",
			m.invitationOpen(inv), m.userOfEmail(m.OrgA, "admin-7b@mfa.example"))
	}

	m.mfa.reset()
	id, err := m.repo.EnsureUser(ctx, m.OrgA, "kc-admin-7b", "admin-7b@mfa.example", "Admin")
	if err != nil {
		t.Fatalf("first login: %v", err)
	}
	if got := m.mfa.bound(); !slices.Equal(got, []string{"kc-admin-7b"}) {
		t.Errorf("first login with org_admin invitation: bound %v, want exactly [kc-admin-7b]", got)
	}
	if m.invitationOpen(inv) || m.assignments(id, orgAdmin) != 1 {
		t.Errorf("first login: invitation open %v, %d org_admin assignments", m.invitationOpen(inv), m.assignments(id, orgAdmin))
	}

	m.mfa.reset()
	m.invite("viewer-7b@mfa.example", viewer)
	if _, err = m.repo.EnsureUser(ctx, m.OrgA, "kc-viewer-7b", "viewer-7b@mfa.example", "Viewer"); err != nil {
		t.Fatalf("viewer first login: %v", err)
	}
	if got := m.mfa.bound(); len(got) != 0 {
		t.Errorf("viewer first login: bound %v", got)
	}
}

// recordingAccounts records the invited role of each IdP account and can
// fail like an org_admin account whose MFA binding failed.
type recordingAccounts struct {
	mu    sync.Mutex
	roles []string
	err   error
}

func (a *recordingAccounts) CreateInvitedAccount(_ context.Context, _, _, _, _, role string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.roles = append(a.roles, role)
	if a.err != nil {
		return "", a.err
	}
	return "kc-invited", nil
}

func (a *recordingAccounts) DeleteAccount(context.Context, string) error { return nil }

// TestInvitationAcceptanceOrgAdminRequiresMFA (WP-358): the acceptance
// hands the invited role to CreateInvitedAccount, which binds an org_admin
// account to MFA; when that fails (account deleted, ErrMFAUnavailable), the
// acceptance answers 503, creates no user and the invitation stays open.
func TestInvitationAcceptanceOrgAdminRequiresMFA(t *testing.T) {
	m := newMFAFixture(t, "7d")
	accounts := &recordingAccounts{}
	mux := chi.NewRouter()
	NewInvitationHandler(NewPGInvitations(m.App)).WithAccounts(accounts).RegisterRoutes(mux)
	accept := func(email, roleID string) (*httptest.ResponseRecorder, string) {
		token, hash, err := NewInvitationToken(m.OrgA)
		if err != nil {
			t.Fatal(err)
		}
		var id string
		if err = m.Admin.QueryRow(context.Background(), `INSERT INTO user_invitation (organization_id, email, role_id, invited_by, token_hash, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`, m.OrgA, email, roleID, m.User, hash, time.Now().Add(time.Hour)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/invitations/accept",
			strings.NewReader(`{"token":"`+token+`","password":"correct horse battery staple"}`))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w, id
	}

	accounts.err = errors.Join(identity.ErrMFAUnavailable, errors.New("keycloak: role mapping: status 403"))
	w, inv := accept("admin-7d@mfa.example", m.role(m.OrgA, "org_admin"))
	if w.Code != http.StatusServiceUnavailable || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") {
		t.Fatalf("org_admin acceptance with failing binding: %d %s, want 503 problem+json", w.Code, w.Body.String())
	}
	if !m.invitationOpen(inv) || m.userOfEmail(m.OrgA, "admin-7d@mfa.example") {
		t.Errorf("failed acceptance: invitation open %v, user created %v", m.invitationOpen(inv), m.userOfEmail(m.OrgA, "admin-7d@mfa.example"))
	}

	accounts.err = nil
	if w, _ = accept("admin2-7d@mfa.example", m.role(m.OrgA, "org_admin")); w.Code != http.StatusCreated {
		t.Fatalf("org_admin acceptance: %d %s", w.Code, w.Body.String())
	}
	var accepted AcceptedInvitation
	_ = json.Unmarshal(w.Body.Bytes(), &accepted)
	if !accepted.AccountCreated {
		t.Errorf("acceptance did not create the account: %s", w.Body.String())
	}
	if w, _ = accept("viewer-7d@mfa.example", m.role(m.OrgA, "viewer")); w.Code != http.StatusCreated {
		t.Fatalf("viewer acceptance: %d %s", w.Code, w.Body.String())
	}
	if !slices.Equal(accounts.roles, []string{"org_admin", "org_admin", "viewer"}) {
		t.Errorf("invited roles handed to the IdP = %v", accounts.roles)
	}
}

// TestReconcileMFABindsExistingOrgAdmins (WP-358): the reconciliation at
// start binds exactly the active org_admins with oidc subject of every
// organization (two here), not inactive ones, not users without subject
// and not other roles; a failing binding is counted and does not stop the
// run; a second run binds the same accounts again (the enforcer is
// idempotent).
func TestReconcileMFABindsExistingOrgAdmins(t *testing.T) {
	m := newMFAFixture(t, "7c")
	ctx := context.Background()
	prefix := "scopetest-7c-"

	adminA := m.AppUser(t, m.OrgA, "admin-a")
	m.grant(m.OrgA, adminA, m.role(m.OrgA, "org_admin"))
	adminB := m.AppUser(t, m.OrgB, "admin-b")
	m.grant(m.OrgB, adminB, m.role(m.OrgB, "org_admin"))
	// org_admin in a client scope is org_admin as well.
	scoped := m.AppUser(t, m.OrgA, "admin-a-client")
	if _, err := m.Admin.Exec(ctx, `INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id) VALUES ($1, $2, $3, $4)`,
		m.OrgA, scoped, m.role(m.OrgA, "org_admin"), m.Client1); err != nil {
		t.Fatal(err)
	}
	inactive := m.AppUser(t, m.OrgA, "admin-inactive")
	m.grant(m.OrgA, inactive, m.role(m.OrgA, "org_admin"))
	if _, err := m.Admin.Exec(ctx, `UPDATE app_user SET is_active = false WHERE id = $1`, inactive); err != nil {
		t.Fatal(err)
	}
	noSubject := m.userWithoutSubject(m.OrgA, "nosubject-7c@mfa.example")
	m.grant(m.OrgA, noSubject, m.role(m.OrgA, "org_admin"))
	engineer := m.AppUser(t, m.OrgA, "engineer")
	m.grant(m.OrgA, engineer, m.role(m.OrgA, "engineer"))
	// An org_admin with two assignments is bound once.
	m.grant(m.OrgA, adminA, m.role(m.OrgA, "engineer"))

	own := func() []string {
		var out []string
		for _, s := range m.mfa.bound() {
			if strings.HasPrefix(s, prefix) {
				out = append(out, s)
			}
		}
		slices.Sort(out)
		return out
	}
	want := []string{prefix + "admin-a", prefix + "admin-a-client", prefix + "admin-b"}

	bound, failed, err := m.repo.ReconcileMFA(ctx)
	if err != nil {
		t.Fatalf("ReconcileMFA: %v", err)
	}
	if got := own(); !slices.Equal(got, want) {
		t.Errorf("bound %v, want exactly %v", got, want)
	}
	if bound < len(want) {
		t.Errorf("bound count %d < %d", bound, len(want))
	}
	_ = failed // other packages' organizations may share the database

	// A failing binding is counted, the others are still bound.
	m.mfa.reset()
	m.mfa.fail = []string{prefix + "admin-a"}
	_, failed, err = m.repo.ReconcileMFA(ctx)
	if err != nil {
		t.Fatalf("ReconcileMFA with a failing binding: %v", err)
	}
	if failed < 1 {
		t.Errorf("failed = %d, want the failing org_admin counted", failed)
	}
	if got := own(); !slices.Equal(got, want[1:]) {
		t.Errorf("with a failing binding: bound %v, want %v", got, want[1:])
	}

	// Without enforcer the reconciliation binds nothing.
	if b, f, rerr := NewPGRepository(m.App).ReconcileMFA(ctx); b != 0 || f != 0 || rerr != nil {
		t.Errorf("without enforcer: %d bound, %d failed, %v", b, f, rerr)
	}
}

// TestEnsureRoleReportsHeldDefaultRole (WP-367): EnsureRole reports whether
// the user holds the default role afterwards, so the login binds a default
// role org_admin to MFA: assigned now or at an earlier login is held, a
// user with another role keeps it and does not hold org_admin, an unknown
// role is never held. Assigning org_admin binds the subject exactly once
// before the commit; a failing binding assigns nothing (a session of the
// user that refreshes meanwhile gets no org_admin) and wraps
// ErrMFAUnavailable. Another default role binds nothing.
func TestEnsureRoleReportsHeldDefaultRole(t *testing.T) {
	m := newMFAFixture(t, "7f")
	ctx := context.Background()
	orgAdmin, viewer := m.role(m.OrgA, "org_admin"), m.role(m.OrgA, "viewer")

	fresh := m.AppUser(t, m.OrgA, "fresh")
	m.mfa.fail = []string{"*"}
	if held, err := m.repo.EnsureRole(ctx, m.OrgA, fresh, "org_admin"); !errors.Is(err, identity.ErrMFAUnavailable) || held {
		t.Fatalf("failing binding: held %v, err %v; want ErrMFAUnavailable", held, err)
	}
	if n := m.assignments(fresh, orgAdmin); n != 0 {
		t.Fatalf("failing binding assigned org_admin (%d)", n)
	}
	m.mfa.reset()
	for _, login := range []string{"first", "second"} {
		held, err := m.repo.EnsureRole(ctx, m.OrgA, fresh, "org_admin")
		if err != nil || !held {
			t.Fatalf("%s login: held %v, err %v; want the default role held", login, held, err)
		}
	}
	if n := m.assignments(fresh, orgAdmin); n != 1 {
		t.Errorf("default role assigned %d times, want once", n)
	}
	if got := m.mfa.bound(); !slices.Equal(got, []string{"scopetest-7f-fresh"}) {
		t.Errorf("assigning org_admin bound %v, want exactly [scopetest-7f-fresh]", got)
	}

	m.mfa.reset()
	other := m.AppUser(t, m.OrgA, "viewer")
	m.grant(m.OrgA, other, viewer)
	if held, err := m.repo.EnsureRole(ctx, m.OrgA, other, "org_admin"); err != nil || held {
		t.Errorf("user with another role: held %v, err %v; want not held", held, err)
	}
	if n := m.assignments(other, orgAdmin); n != 0 {
		t.Errorf("user with another role got org_admin (%d)", n)
	}
	plain := m.AppUser(t, m.OrgA, "plain")
	if held, err := m.repo.EnsureRole(ctx, m.OrgA, plain, "viewer"); err != nil || !held {
		t.Errorf("default role viewer: held %v, err %v; want held", held, err)
	}
	none := m.AppUser(t, m.OrgA, "none")
	if held, err := m.repo.EnsureRole(ctx, m.OrgA, none, "no-such-role"); err != nil || held {
		t.Errorf("unknown default role: held %v, err %v; want not held", held, err)
	}
	if got := m.mfa.bound(); len(got) != 0 {
		t.Errorf("other roles bound %v", got)
	}
}
