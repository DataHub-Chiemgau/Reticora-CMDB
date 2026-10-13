package user

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// stubInvitationAccounts records IdP accounts created on acceptance.
type stubInvitationAccounts struct {
	mu      sync.Mutex
	created []string
	deleted []string
	err     error
}

func (a *stubInvitationAccounts) CreateInvitedAccount(_ context.Context, email, _, _, _ string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return "", a.err
	}
	a.created = append(a.created, email)
	return "kc-" + email, nil
}

func (a *stubInvitationAccounts) DeleteAccount(_ context.Context, id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.deleted = append(a.deleted, id)
	return nil
}

// TestInvitationsAgainstPostgres covers WP-114 (TLC-03, API-05): issue with
// a stored hash, seven days; accept atomically and once (parallel double
// acceptance), creating the user with the invited role and scope; renew an
// expired invitation; revoke; the issuer's scope bounds the invitable
// scopes; failures roll back.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestInvitationsAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "87")
	ctx := context.Background()
	repo := NewPGInvitations(f.App)
	h := NewInvitationHandler(repo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	var viewerRole, adminRole string
	if err := f.Admin.QueryRow(ctx, `SELECT id::text FROM role WHERE organization_id = $1 AND name = 'viewer'`, f.OrgA).Scan(&viewerRole); err != nil {
		t.Fatalf("viewer role: %v", err)
	}
	if err := f.Admin.QueryRow(ctx, `SELECT id::text FROM role WHERE organization_id = $1 AND name = 'org_admin'`, f.OrgA).Scan(&adminRole); err != nil {
		t.Fatalf("org_admin role: %v", err)
	}

	asAdmin := func(base context.Context) context.Context {
		return tenant.WithTenant(base, tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	}
	call := func(reqCtx context.Context, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(reqCtx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	issue := func(reqCtx context.Context, body string) (*httptest.ResponseRecorder, Invitation) {
		t.Helper()
		w := call(reqCtx, http.MethodPost, "/api/v1/invitations", body)
		var inv Invitation
		_ = json.Unmarshal(w.Body.Bytes(), &inv)
		return w, inv
	}
	accept := func(token string) *httptest.ResponseRecorder {
		return call(ctx, http.MethodPost, "/api/v1/invitations/accept", `{"token":"`+token+`","display_name":"Invited"}`)
	}
	orgAdmin := asAdmin(f.OrgCtx(f.OrgA))

	// Issue: token once, hash stored, seven days.
	w, inv := issue(orgAdmin, `{"email":"new@invite.example","role_id":"`+viewerRole+`"}`)
	if w.Code != http.StatusCreated || inv.Token == "" || inv.Status != InvitationPending || inv.ScopeType != "org" {
		t.Fatalf("issue: %d %s", w.Code, w.Body.String())
	}
	if d := time.Until(inv.ExpiresAt); d < InvitationTTL-time.Minute || d > InvitationTTL+time.Minute {
		t.Errorf("expires in %v, want 7 days", d)
	}
	var stored string
	if err := f.Admin.QueryRow(ctx, `SELECT token_hash FROM user_invitation WHERE id = $1`, inv.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != HashInvitationToken(inv.Token) || strings.Contains(stored, inv.Token) {
		t.Error("the stored token is not the hash")
	}
	if rec, _ := issue(orgAdmin, `{"email":"NEW@invite.example","role_id":"`+viewerRole+`"}`); rec.Code != http.StatusConflict {
		t.Errorf("second pending invitation: %d, want 409", rec.Code)
	}

	// Parallel double acceptance: exactly one wins.
	const parallel = 6
	codes := make(chan int, parallel)
	var wg sync.WaitGroup
	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- accept(inv.Token).Code
		}()
	}
	wg.Wait()
	close(codes)
	created := 0
	for code := range codes {
		switch code {
		case http.StatusCreated:
			created++
		case http.StatusNotFound:
		default:
			t.Errorf("parallel acceptance answered %d", code)
		}
	}
	if created != 1 {
		t.Fatalf("parallel acceptances created %d users, want 1", created)
	}
	var (
		users       int
		role        string
		scopeClient *string
	)
	if err := f.Admin.QueryRow(ctx, `SELECT count(*) FROM app_user WHERE organization_id = $1 AND email = 'new@invite.example'`, f.OrgA).Scan(&users); err != nil || users != 1 {
		t.Fatalf("users = %d (%v)", users, err)
	}
	if err := f.Admin.QueryRow(ctx, `SELECT r.name, ra.scope_client_id::text FROM app_user u JOIN role_assignment ra ON ra.user_id = u.id
		JOIN role r ON r.id = ra.role_id WHERE u.organization_id = $1 AND u.email = 'new@invite.example'`, f.OrgA).Scan(&role, &scopeClient); err != nil {
		t.Fatal(err)
	}
	if role != "viewer" || scopeClient != nil {
		t.Errorf("assignment = %s scope %v", role, scopeClient)
	}
	if rec := accept(inv.Token); rec.Code != http.StatusNotFound {
		t.Errorf("reuse: %d, want 404", rec.Code)
	}
	if rec := call(orgAdmin, http.MethodDelete, "/api/v1/invitations/"+inv.ID, ""); rec.Code != http.StatusConflict {
		t.Errorf("revoke accepted: %d, want 409", rec.Code)
	}
	if rec := call(orgAdmin, http.MethodPost, "/api/v1/invitations/"+inv.ID+"/renew", ""); rec.Code != http.StatusConflict {
		t.Errorf("renew accepted: %d, want 409", rec.Code)
	}

	// Client scope: the user gets the role on that client only; a
	// client-restricted issuer cannot invite into another client.
	w, scoped := issue(orgAdmin, `{"email":"tech@invite.example","role_id":"`+viewerRole+`","scope_type":"client","scope_id":"`+f.Client1+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("scoped issue: %d %s", w.Code, w.Body.String())
	}
	if rec := accept(scoped.Token); rec.Code != http.StatusCreated {
		t.Fatalf("scoped accept: %d %s", rec.Code, rec.Body.String())
	}
	if err := f.Admin.QueryRow(ctx, `SELECT ra.scope_client_id::text FROM app_user u JOIN role_assignment ra ON ra.user_id = u.id
		WHERE u.organization_id = $1 AND u.email = 'tech@invite.example'`, f.OrgA).Scan(&scopeClient); err != nil || scopeClient == nil || *scopeClient != f.Client1 {
		t.Errorf("scoped assignment = %v (%v)", scopeClient, err)
	}
	client2Admin := asAdmin(f.ClientCtx(f.Client2))
	if rec, _ := issue(client2Admin, `{"email":"x@invite.example","role_id":"`+viewerRole+`","scope_type":"client","scope_id":"`+f.Client1+`"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("invite into an invisible client: %d, want 422", rec.Code)
	}
	if rec, _ := issue(orgAdmin, `{"email":"y@invite.example","role_id":"`+f.ID()+`"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown role: %d, want 422", rec.Code)
	}
	if rec, _ := issue(orgAdmin, `{"email":"y@invite.example","role_id":"`+viewerRole+`","scope_type":"org","scope_id":"`+f.Client1+`"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("org scope with scope_id: %d, want 400", rec.Code)
	}

	// Expired: 410; renewal issues a new token, the old one stops working.
	_, expiring := issue(orgAdmin, `{"email":"late@invite.example","role_id":"`+viewerRole+`"}`)
	if _, err := f.Admin.Exec(ctx, `UPDATE user_invitation SET expires_at = now() - interval '1 minute' WHERE id = $1`, expiring.ID); err != nil {
		t.Fatal(err)
	}
	if rec := accept(expiring.Token); rec.Code != http.StatusGone {
		t.Errorf("expired: %d, want 410", rec.Code)
	}
	w = call(orgAdmin, http.MethodGet, "/api/v1/invitations", "")
	if !strings.Contains(w.Body.String(), `"status":"expired"`) || strings.Contains(w.Body.String(), `"token"`) {
		t.Errorf("list: %s", w.Body.String())
	}
	w = call(orgAdmin, http.MethodPost, "/api/v1/invitations/"+expiring.ID+"/renew", "")
	var renewed Invitation
	if err := json.Unmarshal(w.Body.Bytes(), &renewed); w.Code != http.StatusOK || err != nil || renewed.Token == "" || renewed.Status != InvitationPending {
		t.Fatalf("renew: %d %s", w.Code, w.Body.String())
	}
	if rec := accept(expiring.Token); rec.Code != http.StatusNotFound {
		t.Errorf("old token after renewal: %d, want 404", rec.Code)
	}
	if rec := accept(renewed.Token); rec.Code != http.StatusCreated {
		t.Errorf("renewed token: %d %s", rec.Code, rec.Body.String())
	}

	// Revoke a pending invitation.
	_, revoked := issue(orgAdmin, `{"email":"gone@invite.example","role_id":"`+viewerRole+`"}`)
	if rec := call(orgAdmin, http.MethodDelete, "/api/v1/invitations/"+revoked.ID, ""); rec.Code != http.StatusNoContent {
		t.Errorf("revoke: %d", rec.Code)
	}
	if rec := accept(revoked.Token); rec.Code != http.StatusNotFound {
		t.Errorf("revoked token: %d, want 404", rec.Code)
	}

	// An existing user of the address: conflict, the invitation stays pending.
	existing := f.AppUser(t, f.OrgA, "taken")
	var takenEmail string
	if err := f.Admin.QueryRow(ctx, `SELECT email FROM app_user WHERE id = $1`, existing).Scan(&takenEmail); err != nil {
		t.Fatal(err)
	}
	_, dup := issue(orgAdmin, `{"email":"`+takenEmail+`","role_id":"`+adminRole+`"}`)
	if rec := accept(dup.Token); rec.Code != http.StatusConflict {
		t.Errorf("existing user: %d, want 409", rec.Code)
	}
	var acceptedAt *time.Time
	if err := f.Admin.QueryRow(ctx, `SELECT accepted_at FROM user_invitation WHERE id = $1`, dup.ID).Scan(&acceptedAt); err != nil || acceptedAt != nil {
		t.Errorf("invitation consumed by a failed acceptance: %v (%v)", acceptedAt, err)
	}

	// Organization B neither sees the invitations nor accepts them with its id.
	orgBAdmin := tenant.WithTenant(f.OrgCtx(f.OrgB), tenant.TenantInfo{OrganizationID: f.OrgB, UserID: f.User})
	w = call(orgBAdmin, http.MethodGet, "/api/v1/invitations", "")
	if strings.Contains(w.Body.String(), "@invite.example") {
		t.Errorf("organization B sees invitations of A: %s", w.Body.String())
	}
	_, pending := issue(orgAdmin, `{"email":"cross@invite.example","role_id":"`+viewerRole+`"}`)
	_, secret, _ := strings.Cut(pending.Token, ".")
	if rec := accept(f.OrgB + "." + secret); rec.Code != http.StatusNotFound {
		t.Errorf("token with another organization: %d, want 404", rec.Code)
	}

	// With the IdP client: the password creates the account; a failing IdP
	// rolls the acceptance back.
	accounts := &stubInvitationAccounts{}
	h.WithAccounts(accounts)
	if rec := call(ctx, http.MethodPost, "/api/v1/invitations/accept", `{"token":"`+pending.Token+`"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("missing password: %d, want 400", rec.Code)
	}
	accounts.err = errors.New("idp down")
	if rec := call(ctx, http.MethodPost, "/api/v1/invitations/accept", `{"token":"`+pending.Token+`","password":"correct horse battery"}`); rec.Code != http.StatusInternalServerError {
		t.Errorf("failing IdP: %d", rec.Code)
	}
	if err := f.Admin.QueryRow(ctx, `SELECT accepted_at FROM user_invitation WHERE id = $1`, pending.ID).Scan(&acceptedAt); err != nil || acceptedAt != nil {
		t.Errorf("failing IdP consumed the invitation: %v (%v)", acceptedAt, err)
	}
	accounts.err = nil
	w = call(ctx, http.MethodPost, "/api/v1/invitations/accept", `{"token":"`+pending.Token+`","password":"correct horse battery"}`)
	var res AcceptedInvitation
	if err := json.Unmarshal(w.Body.Bytes(), &res); w.Code != http.StatusCreated || err != nil || !res.AccountCreated || res.OrganizationID != f.OrgA {
		t.Errorf("accept with account: %d %s", w.Code, w.Body.String())
	}
	if len(accounts.created) != 1 || accounts.created[0] != "cross@invite.example" {
		t.Errorf("accounts = %v", accounts.created)
	}

	// Every step is audited.
	var audited int
	if err := f.Admin.QueryRow(ctx, `SELECT count(DISTINCT action) FROM audit_log WHERE organization_id = $1 AND resource_type = 'user_invitation'`, f.OrgA).Scan(&audited); err != nil || audited != 4 {
		t.Errorf("audited invitation actions = %d (%v), want issued, accepted, renewed, revoked", audited, err)
	}
}
