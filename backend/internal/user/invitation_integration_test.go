package user

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// stubInvitationAccounts records IdP accounts created on acceptance.
type stubInvitationAccounts struct {
	mu      sync.Mutex
	created []string
	deleted []string
	err     error
}

func (a *stubInvitationAccounts) CreateInvitedAccount(_ context.Context, email, _, _, _, _ string) (string, error) {
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

// testAcceptURL is the accept page of the invitation links in these tests
// (RETICORA_INVITATION_ACCEPT_URL).
const testAcceptURL = "https://cmdb.example.invalid/invitations/accept"

// withCaller returns base with the tenant info and the principal of userID in
// orgID holding every permission in scope (the issuer of the invitations).
func withCaller(base context.Context, orgID, userID string, scope identity.Scope) context.Context {
	p := identity.Principal{Subject: userID, OrganizationID: orgID, Type: identity.PrincipalTypeUser,
		Permissions: identity.AllPermissions(), Scope: &scope, ClientScope: scope.LegacyClientScope()}
	return tenant.WithTenant(identity.WithPrincipal(base, p), tenant.TenantInfo{OrganizationID: orgID, UserID: userID})
}

// TestInvitationsAgainstPostgres covers WP-114 (TLC-03, API-05): issue with
// a stored hash, seven days; accept atomically and once (parallel double
// acceptance), creating the user with the invited role and scope; renew an
// expired invitation; revoke; the issuer's scope bounds the invitable
// scopes (grant ceiling, WP-394); failures roll back.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestInvitationsAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "87")
	ctx := context.Background()
	repo := NewPGInvitations(f.App)
	h := NewInvitationHandler(repo).WithAcceptURL(testAcceptURL)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	var viewerRole, adminRole string
	if err := f.Admin.QueryRow(ctx, `SELECT id::text FROM role WHERE organization_id = $1 AND name = 'viewer'`, f.OrgA).Scan(&viewerRole); err != nil {
		t.Fatalf("viewer role: %v", err)
	}
	if err := f.Admin.QueryRow(ctx, `SELECT id::text FROM role WHERE organization_id = $1 AND name = 'org_admin'`, f.OrgA).Scan(&adminRole); err != nil {
		t.Fatalf("org_admin role: %v", err)
	}

	all := identity.ScopeSet{All: true}
	orgWide := identity.Scope{Clients: all, Sites: all, Teams: all}
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
	orgAdmin := withCaller(f.OrgCtx(f.OrgA), f.OrgA, f.User, orgWide)

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
	// client-restricted issuer cannot invite into another client (grant
	// ceiling, 403), an unknown client is 422.
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
	client2Admin := withCaller(f.ClientCtx(f.Client2), f.OrgA, f.User, identity.Scope{Clients: identity.ScopeSet{IDs: []string{f.Client2}}, Sites: all, Teams: all})
	if rec, _ := issue(client2Admin, `{"email":"x@invite.example","role_id":"`+viewerRole+`","scope_type":"client","scope_id":"`+f.Client1+`"}`); rec.Code != http.StatusForbidden {
		t.Errorf("invite into another client: %d, want 403", rec.Code)
	}
	if rec, _ := issue(orgAdmin, `{"email":"x@invite.example","role_id":"`+viewerRole+`","scope_type":"client","scope_id":"`+f.ID()+`"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("invite into an unknown client: %d, want 422", rec.Code)
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
	orgBAdmin := withCaller(f.OrgCtx(f.OrgB), f.OrgB, f.User, orgWide)
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

// TestAuditDefectInvitationCreatedEvent covers WP-421 (NTF-05, TLC-03,
// F-041; formerly a defect test of WP-140/WP-114): issue and renewal append
// exactly one invitation.created in their transaction, with the address (a
// vault surrogate in the outbox, SEC-05), the organization name, the expiry
// and the link RETICORA_INVITATION_ACCEPT_URL?token=<token>; the renewal's
// event carries the new token. Refused issues and renewals (409 duplicate
// or accepted, 403 grant ceiling, 422, 400, 404), the acceptance and
// rolled-back transactions (the event cannot be written: nothing is stored)
// append nothing. Without accept URL issue and renewal answer 503
// (invitations not configured) and change nothing. Organization B gets no
// event. Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestAuditDefectInvitationCreatedEvent' ./internal/user/
func TestAuditDefectInvitationCreatedEvent(t *testing.T) {
	f := scopetest.Seed(t, "d2")
	bg := context.Background()
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	// Personal event data travels as vault surrogates (SEC-05), as in the
	// server.
	vault := pii.NewVault(f.App, enc)
	pii.SetDefault(vault)
	t.Cleanup(func() { pii.SetDefault(nil) })

	viewerRole := auditRoleID(t, f, "viewer")
	mux := chi.NewRouter()
	NewInvitationHandler(NewPGInvitations(f.App)).WithAcceptURL(testAcceptURL).RegisterRoutes(mux)
	// An org-wide org_admin issues and renews (grant ceiling, WP-394); a
	// client-1 org_admin may not invite org-wide.
	admin := f.AppUser(t, f.OrgA, "audit-d2-orgadmin")
	delegate := f.AppUser(t, f.OrgA, "audit-d2-delegate")
	for _, stmt := range []struct {
		user   string
		client *string
	}{{admin, nil}, {delegate, &f.Client1}} {
		if _, err := f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id) VALUES ($1, $2, $3, $4)`,
			f.OrgA, stmt.user, auditRoleID(t, f, "org_admin"), stmt.client); err != nil {
			t.Fatal(err)
		}
	}
	adminCtx, delegateCtx := auditPrincipalCtx(t, f, admin), auditPrincipalCtx(t, f, delegate)
	var orgName string
	if err := f.Admin.QueryRow(bg, `SELECT name FROM organization WHERE id = $1`, f.OrgA).Scan(&orgName); err != nil {
		t.Fatal(err)
	}

	type event struct {
		EntityID string
		Raw      string         // data as stored
		RawEmail any            // email as stored
		Data     map[string]any // data with the surrogates resolved
	}
	outbox := func(orgID string) []event {
		t.Helper()
		rows, err := f.Admin.Query(bg, `SELECT COALESCE(entity_id, ''), data::text FROM event_outbox
			WHERE organization_id = $1 AND type = 'invitation.created' ORDER BY sequence`, orgID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []event
		for rows.Next() {
			var e event
			if err := rows.Scan(&e.EntityID, &e.Raw); err != nil {
				t.Fatal(err)
			}
			out = append(out, e)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		scope := database.OrgWideScope(orgID, "")
		for i := range out {
			var data map[string]any
			if err := json.Unmarshal([]byte(out[i].Raw), &data); err != nil {
				t.Fatalf("event data %s: %v", out[i].Raw, err)
			}
			out[i].RawEmail = data["email"]
			if err := database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
				revealed, rerr := pii.RevealTx(ctx, tx, orgID, data)
				out[i].Data, _ = revealed.(map[string]any)
				return rerr
			}); err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	checkEvent := func(t *testing.T, e event, inv Invitation, email string) {
		t.Helper()
		if ref, sealed := pii.RefID(e.RawEmail); !sealed || ref == "" || strings.Contains(strings.ToLower(e.Raw), strings.ToLower(email)) {
			t.Errorf("invitation.created stores the address in clear, want a vault surrogate (SEC-05): %s", e.Raw)
		}
		if got, _ := e.Data["email"].(string); !strings.EqualFold(got, email) {
			t.Errorf("invitation.created email = %q, want %q", got, email)
		}
		if got, _ := e.Data["organization_name"].(string); got != orgName {
			t.Errorf("invitation.created organization_name = %q, want %q", got, orgName)
		}
		if got, _ := e.Data["expires_at"].(string); got != inv.ExpiresAt.UTC().Format(time.RFC3339) {
			t.Errorf("invitation.created expires_at = %q, want %q", got, inv.ExpiresAt.UTC().Format(time.RFC3339))
		}
		if link, _ := e.Data["link"].(string); inv.Token == "" || link != testAcceptURL+"?token="+inv.Token {
			t.Errorf("invitation.created link = %q, want %q", link, testAcceptURL+"?token="+inv.Token)
		}
		if id, _ := e.Data["id"].(string); id != inv.ID || e.EntityID != inv.ID {
			t.Errorf("invitation.created id %q, entity %q, want %q", id, e.EntityID, inv.ID)
		}
	}
	decode := func(t *testing.T, w *httptest.ResponseRecorder) Invitation {
		t.Helper()
		var inv Invitation
		if err := json.Unmarshal(w.Body.Bytes(), &inv); err != nil {
			t.Fatalf("%s: %v", w.Body.String(), err)
		}
		return inv
	}
	stored := func(id string) (hash string) {
		t.Helper()
		if err := f.Admin.QueryRow(bg, `SELECT token_hash FROM user_invitation WHERE id = $1`, id).Scan(&hash); err != nil {
			t.Fatal(err)
		}
		return hash
	}
	rows := func(email string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM user_invitation WHERE organization_id = $1 AND lower(email) = lower($2)`,
			f.OrgA, email).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	email := "audit-d2-invitee@invite.example"
	w := auditPost(mux, adminCtx, "/api/v1/invitations", `{"email":"`+email+`","role_id":"`+viewerRole+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("issue = %d %s", w.Code, w.Body.String())
	}
	inv := decode(t, w)

	t.Run("issue emits exactly one invitation.created", func(t *testing.T) {
		got := outbox(f.OrgA)
		if len(got) != 1 {
			t.Fatalf("invitation.created events after issue = %d, want 1 (NTF-05: the invitee gets the link by mail)", len(got))
		}
		checkEvent(t, got[0], inv, email)
	})

	t.Run("refused issues emit nothing", func(t *testing.T) {
		before := len(outbox(f.OrgA))
		for _, tc := range []struct {
			name string
			ctx  context.Context
			body string
			want int
		}{
			{"duplicate pending invitation", adminCtx, `{"email":"` + email + `","role_id":"` + viewerRole + `"}`, http.StatusConflict},
			{"client-1 org_admin invites org-wide (grant ceiling)", delegateCtx, `{"email":"audit-d2-ceiling@invite.example","role_id":"` + viewerRole + `"}`, http.StatusForbidden},
			{"unknown role", adminCtx, `{"email":"audit-d2-unknown@invite.example","role_id":"` + f.ID() + `"}`, http.StatusUnprocessableEntity},
			{"invalid address", adminCtx, `{"email":"not an address","role_id":"` + viewerRole + `"}`, http.StatusBadRequest},
		} {
			if w := auditPost(mux, tc.ctx, "/api/v1/invitations", tc.body); w.Code != tc.want {
				t.Errorf("%s = %d %s, want %d", tc.name, w.Code, w.Body.String(), tc.want)
			}
		}
		if after := len(outbox(f.OrgA)); after != before {
			t.Errorf("events after refused issues = %d, want %d", after, before)
		}
	})

	t.Run("renew emits a new invitation.created with the new token", func(t *testing.T) {
		before := len(outbox(f.OrgA))
		w := auditPost(mux, adminCtx, "/api/v1/invitations/"+inv.ID+"/renew", ``)
		if w.Code != http.StatusOK {
			t.Fatalf("renew = %d %s", w.Code, w.Body.String())
		}
		renewed := decode(t, w)
		got := outbox(f.OrgA)
		if len(got) != before+1 {
			t.Fatalf("invitation.created events after renew = %d, want %d", len(got), before+1)
		}
		checkEvent(t, got[len(got)-1], renewed, email)
		if renewed.Token == inv.Token || strings.Contains(got[len(got)-1].Raw, inv.Token) {
			t.Errorf("the renewal's event carries the old token")
		}
	})

	t.Run("refused renewals and the acceptance emit nothing", func(t *testing.T) {
		accepted := decode(t, auditPost(mux, adminCtx, "/api/v1/invitations", `{"email":"audit-d2-accepted@invite.example","role_id":"`+viewerRole+`"}`))
		before := len(outbox(f.OrgA))
		if w := auditPost(mux, context.Background(), "/api/v1/invitations/accept", `{"token":"`+accepted.Token+`"}`); w.Code != http.StatusCreated {
			t.Fatalf("accept = %d %s", w.Code, w.Body.String())
		}
		for _, tc := range []struct {
			name, id string
			ctx      context.Context
			want     int
		}{
			{"accepted invitation", accepted.ID, adminCtx, http.StatusConflict},
			{"client-1 org_admin renews an org-wide invitation (grant ceiling)", inv.ID, delegateCtx, http.StatusForbidden},
			{"unknown invitation", f.ID(), adminCtx, http.StatusNotFound},
		} {
			if w := auditPost(mux, tc.ctx, "/api/v1/invitations/"+tc.id+"/renew", ``); w.Code != tc.want {
				t.Errorf("renew %s = %d %s, want %d", tc.name, w.Code, w.Body.String(), tc.want)
			}
		}
		if after := len(outbox(f.OrgA)); after != before {
			t.Errorf("events after the acceptance and refused renewals = %d, want %d", after, before)
		}
	})

	t.Run("a rolled-back issue or renewal emits nothing and stores nothing", func(t *testing.T) {
		before, hash := len(outbox(f.OrgA)), stored(inv.ID)
		// A vault without key cannot seal the address: the event cannot be
		// written, so the transaction of the issue or renewal rolls back.
		pii.SetDefault(pii.NewVault(f.App, nil))
		const lost = "audit-d2-rollback@invite.example"
		issued := auditPost(mux, adminCtx, "/api/v1/invitations", `{"email":"`+lost+`","role_id":"`+viewerRole+`"}`)
		renewed := auditPost(mux, adminCtx, "/api/v1/invitations/"+inv.ID+"/renew", ``)
		pii.SetDefault(vault)
		if issued.Code != http.StatusInternalServerError {
			t.Errorf("issue without event = %d %s, want 500", issued.Code, issued.Body.String())
		}
		if renewed.Code != http.StatusInternalServerError {
			t.Errorf("renewal without event = %d %s, want 500", renewed.Code, renewed.Body.String())
		}
		if n := rows(lost); n != 0 {
			t.Errorf("rolled-back issue stored %d invitations", n)
		}
		if stored(inv.ID) != hash {
			t.Error("rolled-back renewal replaced the token")
		}
		if after := len(outbox(f.OrgA)); after != before {
			t.Errorf("events after rolled-back transactions = %d, want %d", after, before)
		}
	})

	t.Run("without accept URL issue and renewal answer 503", func(t *testing.T) {
		unconfigured := chi.NewRouter()
		NewInvitationHandler(NewPGInvitations(f.App)).RegisterRoutes(unconfigured)
		before, hash := len(outbox(f.OrgA)), stored(inv.ID)
		const unsent = "audit-d2-unconfigured@invite.example"
		for _, path := range []string{"/api/v1/invitations", "/api/v1/invitations/" + inv.ID + "/renew"} {
			w := auditPost(unconfigured, adminCtx, path, `{"email":"`+unsent+`","role_id":"`+viewerRole+`"}`)
			var problem struct {
				Detail string `json:"detail"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &problem)
			if w.Code != http.StatusServiceUnavailable || problem.Detail != "invitations not configured" ||
				!strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") {
				t.Errorf("POST %s = %d %q %s, want 503 problem+json 'invitations not configured'", path, w.Code, w.Header().Get("Content-Type"), w.Body.String())
			}
		}
		if rows(unsent) != 0 || stored(inv.ID) != hash || len(outbox(f.OrgA)) != before {
			t.Errorf("unconfigured: %d invitations stored, token changed %v, %d new events; want nothing changed",
				rows(unsent), stored(inv.ID) != hash, len(outbox(f.OrgA))-before)
		}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/invitations", nil).WithContext(adminCtx)
		list := httptest.NewRecorder()
		unconfigured.ServeHTTP(list, req)
		if list.Code != http.StatusOK {
			t.Errorf("list without accept URL = %d, want 200", list.Code)
		}
	})

	t.Run("organization B sees no invitation event", func(t *testing.T) {
		if got := outbox(f.OrgB); len(got) != 0 {
			t.Errorf("organization B invitation events = %d, want 0", len(got))
		}
	})
}

// Grant ceiling of invitations and role assignments (WP-394, TLC-03, RBA-03,
// API-05; F-015, F-305). Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestAuditDefect(Invitation|RoleAssignment)GrantCeiling|TestRolesAssignSunsetPathGrantCeiling|TestInvitationRoleScopeMismatch|TestRoleAssignmentScopeIsNeverWidened|TestRoleAssignmentGrantCeilingTeamScope' ./internal/user/

// auditPrincipalCtx resolves userID through the production chain
// (AccessGrants → ResolveAccess → TenantScopeFor) and returns its request
// context with principal, tenant scope and tenant info.
func auditPrincipalCtx(t *testing.T, f *scopetest.Fixture, userID string) context.Context {
	t.Helper()
	bg := context.Background()
	grants, err := permission.NewPGRepository(f.App).AccessGrants(bg, f.OrgA, userID)
	if err != nil {
		t.Fatalf("access grants of %s: %v", userID, err)
	}
	acc := identity.ResolveAccess(grants)
	p := identity.Principal{Subject: userID, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser, Scope: &acc.Scope,
		PermissionScopes: acc.PermissionScopes, Permissions: acc.Permissions, ClientScope: acc.Scope.LegacyClientScope()}
	if !p.Has(identity.PermUserManage) {
		t.Fatalf("fixture: principal %s lacks user:manage", userID)
	}
	ts := middleware.TenantScopeFor(&p)
	ctx := database.ContextWithTenantScope(bg, &ts)
	ctx = identity.WithPrincipal(ctx, p)
	return tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: f.OrgA, UserID: userID, ClientID: p.ClientScope})
}

func auditRoleID(t *testing.T, f *scopetest.Fixture, name string) string {
	t.Helper()
	var id string
	if err := f.Admin.QueryRow(context.Background(), `SELECT id::text FROM role WHERE organization_id = $1 AND name = $2`, f.OrgA, name).Scan(&id); err != nil {
		t.Fatalf("role %s: %v", name, err)
	}
	return id
}

func auditPost(mux http.Handler, ctx context.Context, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// ceilingFixture is organization A of a scopetest fixture with a client-1
// delegate (org_admin with scope_client_id = client 1, user:manage and
// role:manage there only), an org-wide org_admin, a site of each client, and
// the request contexts of both administrators resolved like a session.
type ceilingFixture struct {
	*scopetest.Fixture
	t                        *testing.T
	delegate, orgAdmin       string
	delegateCtx, orgAdminCtx context.Context
	site1, site2             string
}

func newCeilingFixture(t *testing.T, tag string) *ceilingFixture {
	t.Helper()
	f := scopetest.Seed(t, tag)
	c := &ceilingFixture{Fixture: f, t: t}
	adminRole := auditRoleID(t, f, "org_admin")
	c.delegate = f.AppUser(t, f.OrgA, "ceiling-delegate")
	c.exec(`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id) VALUES ($1, $2, $3, $4)`,
		f.OrgA, c.delegate, adminRole, f.Client1)
	c.orgAdmin = f.AppUser(t, f.OrgA, "ceiling-orgadmin")
	c.exec(`INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, c.orgAdmin, adminRole)
	c.delegateCtx, c.orgAdminCtx = auditPrincipalCtx(t, f, c.delegate), auditPrincipalCtx(t, f, c.orgAdmin)
	c.site1, c.site2 = c.site(f.Client1, "ceiling-site-1"), c.site(f.Client2, "ceiling-site-2")
	return c
}

func (c *ceilingFixture) exec(sql string, args ...any) {
	c.t.Helper()
	if _, err := c.Admin.Exec(context.Background(), sql, args...); err != nil {
		c.t.Fatalf("%s: %v", sql, err)
	}
}

func (c *ceilingFixture) count(sql string, args ...any) int {
	c.t.Helper()
	var n int
	if err := c.Admin.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		c.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func (c *ceilingFixture) site(clientID, name string) string {
	c.t.Helper()
	var id string
	if err := c.Admin.QueryRow(context.Background(), `INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, $3) RETURNING id::text`,
		c.OrgA, clientID, name).Scan(&id); err != nil {
		c.t.Fatalf("site %s: %v", name, err)
	}
	return id
}

// assignments counts the standard and custom role assignments of a user.
func (c *ceilingFixture) assignments(userID string) int {
	c.t.Helper()
	return c.count(`SELECT (SELECT count(*) FROM role_assignment WHERE user_id = $1)::int
		+ (SELECT count(*) FROM user_custom_role WHERE user_id = $1)::int`, userID)
}

// audits counts the audit entries of organization A.
func (c *ceilingFixture) audits() int {
	c.t.Helper()
	return c.count(`SELECT count(*)::int FROM audit_log WHERE organization_id = $1`, c.OrgA)
}

// forbidden reports whether w is a 403 problem (RFC 7807).
func forbidden(w *httptest.ResponseRecorder) bool {
	return w.Code == http.StatusForbidden && strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json")
}

// TestAuditDefectInvitationGrantCeiling covers WP-394 (TLC-03, RBA-03,
// F-015; formerly a defect test of WP-114): issuing and renewing an
// invitation needs user:manage and every permission of the invited role in a
// scope covering the invitation's scope. A client-1 delegate (org_admin with
// scope_client_id = client 1) gets 403 (RFC 7807) without row and without
// audit for org_admin or viewer org-wide, for client 2 and a site of client
// 2; it invites viewer and client_technician into client 1 and viewer at a
// site of client 1 (a client covers its sites, site.client_id). The
// org-wide org_admin invites org_admin org-wide. Renewal checks the renewing
// principal: the delegate cannot renew an org-wide invitation (403, token and
// validity unchanged) but renews one of client 1.
func TestAuditDefectInvitationGrantCeiling(t *testing.T) {
	c := newCeilingFixture(t, "d1")
	adminRole := auditRoleID(t, c.Fixture, "org_admin")
	viewerRole := auditRoleID(t, c.Fixture, "viewer")
	techRole := auditRoleID(t, c.Fixture, "client_technician")

	mux := chi.NewRouter()
	NewInvitationHandler(NewPGInvitations(c.App)).WithAcceptURL(testAcceptURL).RegisterRoutes(mux)

	invitations := func(email string) int {
		t.Helper()
		return c.count(`SELECT count(*)::int FROM user_invitation WHERE organization_id = $1 AND lower(email) = lower($2)`, c.OrgA, email)
	}
	scoped := func(role, scopeType, scopeID string) string {
		return `{"role_id":"` + role + `","scope_type":"` + scopeType + `","scope_id":"` + scopeID + `"}`
	}

	for _, tc := range []struct {
		name    string
		ctx     context.Context
		email   string
		body    string
		allowed bool
	}{
		{"client-1 delegate invites org_admin org-wide (escalation)", c.delegateCtx, "audit-d1-esc-org@invite.example",
			`{"role_id":"` + adminRole + `","scope_type":"org"}`, false},
		{"client-1 delegate invites viewer org-wide (scope beyond own)", c.delegateCtx, "audit-d1-viewer-org@invite.example",
			`{"role_id":"` + viewerRole + `","scope_type":"org"}`, false},
		{"client-1 delegate invites viewer into client 2", c.delegateCtx, "audit-d1-viewer-c2@invite.example",
			scoped(viewerRole, "client", c.Client2), false},
		{"client-1 delegate invites org_admin into client 2", c.delegateCtx, "audit-d1-admin-c2@invite.example",
			scoped(adminRole, "client", c.Client2), false},
		{"client-1 delegate invites viewer at a site of client 2", c.delegateCtx, "audit-d1-viewer-s2@invite.example",
			scoped(viewerRole, "site", c.site2), false},
		{"client-1 delegate invites viewer into client 1", c.delegateCtx, "audit-d1-viewer-c1@invite.example",
			scoped(viewerRole, "client", c.Client1), true},
		{"client-1 delegate invites client_technician into client 1", c.delegateCtx, "audit-d1-tech-c1@invite.example",
			scoped(techRole, "client", c.Client1), true},
		{"client-1 delegate invites viewer at a site of client 1", c.delegateCtx, "audit-d1-viewer-s1@invite.example",
			scoped(viewerRole, "site", c.site1), true},
		{"org-wide org_admin invites org_admin org-wide", c.orgAdminCtx, "audit-d1-admin-org@invite.example",
			`{"role_id":"` + adminRole + `","scope_type":"org"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			audits := c.audits()
			body := `{"email":"` + tc.email + `",` + strings.TrimPrefix(tc.body, "{")
			w := auditPost(mux, tc.ctx, "/api/v1/invitations", body)
			n := invitations(tc.email)
			if tc.allowed {
				if w.Code != http.StatusCreated || n != 1 || c.audits() != audits+1 {
					t.Errorf("POST /invitations = %d (%d rows, %d new audit entries), want 201, 1 row and invitation.issued; body %.200s",
						w.Code, n, c.audits()-audits, w.Body.String())
				}
				return
			}
			if !forbidden(w) || n != 0 || c.audits() != audits {
				t.Errorf("POST /invitations = %d %q with %d stored invitation(s) and %d new audit entries, want 403 problem+json, none and no audit: a principal must not invite beyond its own role and scope; body %.200s",
					w.Code, w.Header().Get("Content-Type"), n, c.audits()-audits, w.Body.String())
			}
		})
	}

	// Renewal: the ceiling applies to the renewing principal.
	type stored struct {
		id, hash  string
		expiresAt time.Time
	}
	read := func(email string) stored {
		t.Helper()
		var s stored
		if err := c.Admin.QueryRow(context.Background(), `SELECT id::text, token_hash, expires_at FROM user_invitation
			WHERE organization_id = $1 AND lower(email) = lower($2)`, c.OrgA, email).Scan(&s.id, &s.hash, &s.expiresAt); err != nil {
			t.Fatalf("invitation of %s: %v", email, err)
		}
		return s
	}
	orgWideInv := "audit-d1-renew-org@invite.example"
	if w := auditPost(mux, c.orgAdminCtx, "/api/v1/invitations", `{"email":"`+orgWideInv+`","role_id":"`+viewerRole+`"}`); w.Code != http.StatusCreated {
		t.Fatalf("org-wide invitation: %d %s", w.Code, w.Body.String())
	}
	t.Run("client-1 delegate renews an org-wide invitation", func(t *testing.T) {
		before, audits := read(orgWideInv), c.audits()
		w := auditPost(mux, c.delegateCtx, "/api/v1/invitations/"+before.id+"/renew", "")
		after := read(orgWideInv)
		if !forbidden(w) || after != before || c.audits() != audits {
			t.Errorf("renew = %d (token changed %v, validity changed %v, %d new audit entries), want 403 and nothing changed; body %.200s",
				w.Code, after.hash != before.hash, !after.expiresAt.Equal(before.expiresAt), c.audits()-audits, w.Body.String())
		}
	})
	t.Run("client-1 delegate renews an invitation of client 1", func(t *testing.T) {
		before := read("audit-d1-viewer-c1@invite.example")
		w := auditPost(mux, c.delegateCtx, "/api/v1/invitations/"+before.id+"/renew", "")
		if w.Code != http.StatusOK || read("audit-d1-viewer-c1@invite.example").hash == before.hash {
			t.Errorf("renew = %d %.200s, want 200 with a new token", w.Code, w.Body.String())
		}
	})
	t.Run("org-wide org_admin renews the org-wide invitation", func(t *testing.T) {
		before := read(orgWideInv)
		w := auditPost(mux, c.orgAdminCtx, "/api/v1/invitations/"+before.id+"/renew", "")
		if w.Code != http.StatusOK || read(orgWideInv).hash == before.hash {
			t.Errorf("renew = %d %.200s, want 200 with a new token", w.Code, w.Body.String())
		}
	})
}

// TestAuditDefectRoleAssignmentGrantCeiling covers WP-394 (RBA-03, API-05,
// F-015; formerly a defect test of WP-114) for POST /role-assignments: the
// caller needs role:manage and every permission of the standard or custom
// role in a scope covering the assignment's scope. A client-1 delegate gets
// 403 (RFC 7807) without row and without audit for org_admin org-wide (to
// itself or another user), viewer org-wide, org_admin in client 2, viewer at
// a site of client 2 and a custom role org-wide; it assigns viewer,
// client_technician and the custom role in client 1 and viewer at a site of
// client 1 (201). The org-wide org_admin assigns org_admin org-wide.
func TestAuditDefectRoleAssignmentGrantCeiling(t *testing.T) {
	c := newCeilingFixture(t, "d3")
	adminRole := auditRoleID(t, c.Fixture, "org_admin")
	viewerRole := auditRoleID(t, c.Fixture, "viewer")
	techRole := auditRoleID(t, c.Fixture, "client_technician")
	customRole := c.ID()
	c.exec(`INSERT INTO custom_role (id, organization_id, name, permissions) VALUES ($1, $2, 'ceiling-custom', '["ci:read","ci:write"]')`, customRole, c.OrgA)
	t.Cleanup(func() {
		// custom_role has no ON DELETE CASCADE to organization.
		_, _ = c.Admin.Exec(context.Background(), `DELETE FROM user_custom_role WHERE custom_role_id = $1`, customRole)
		_, _ = c.Admin.Exec(context.Background(), `DELETE FROM custom_role WHERE id = $1`, customRole)
	})
	target := c.AppUser(t, c.OrgA, "audit-d3-target")
	target2 := c.AppUser(t, c.OrgA, "audit-d3-target2")

	mux := chi.NewRouter()
	NewHandler(NewPGRepository(c.App)).RegisterRoutes(mux)

	orgWideAdmin := func(userID string) bool {
		t.Helper()
		return c.count(`SELECT count(*)::int FROM role_assignment WHERE user_id = $1 AND role_id = $2
			AND scope_client_id IS NULL AND scope_site_id IS NULL AND scope_team_id IS NULL`, userID, adminRole) > 0
	}
	standard := func(user, role, scopeType, scopeID string) string {
		return `{"user_id":"` + user + `","role_id":"` + role + `","scope_type":"` + scopeType + `","scope_id":"` + scopeID + `"}`
	}

	for _, tc := range []struct {
		name    string
		ctx     context.Context
		user    string
		body    string
		allowed bool
	}{
		{"client-1 delegate grants itself org_admin org-wide", c.delegateCtx, c.delegate,
			`{"user_id":"` + c.delegate + `","role_id":"` + adminRole + `","scope_type":"organization"}`, false},
		{"client-1 delegate grants another user org_admin org-wide", c.delegateCtx, target,
			`{"user_id":"` + target + `","role_id":"` + adminRole + `","scope_type":"organization"}`, false},
		{"client-1 delegate grants viewer org-wide (scope beyond own)", c.delegateCtx, target,
			`{"user_id":"` + target + `","role_id":"` + viewerRole + `"}`, false},
		{"client-1 delegate grants org_admin in client 2", c.delegateCtx, target, standard(target, adminRole, "client", c.Client2), false},
		{"client-1 delegate grants viewer at a site of client 2", c.delegateCtx, target, standard(target, viewerRole, "site", c.site2), false},
		{"client-1 delegate grants the custom role org-wide", c.delegateCtx, target,
			`{"user_id":"` + target + `","custom_role_id":"` + customRole + `"}`, false},
		{"client-1 delegate grants viewer in client 1", c.delegateCtx, target, standard(target, viewerRole, "client", c.Client1), true},
		{"client-1 delegate grants client_technician in client 1", c.delegateCtx, target, standard(target, techRole, "client", c.Client1), true},
		{"client-1 delegate grants viewer at a site of client 1", c.delegateCtx, target, standard(target, viewerRole, "site", c.site1), true},
		{"client-1 delegate grants the custom role in client 1", c.delegateCtx, target,
			`{"user_id":"` + target + `","custom_role_id":"` + customRole + `","scope_type":"client","scope_id":"` + c.Client1 + `"}`, true},
		{"org-wide org_admin grants org_admin org-wide", c.orgAdminCtx, target2,
			`{"user_id":"` + target2 + `","role_id":"` + adminRole + `","scope_type":"organization"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, audits := c.assignments(tc.user), c.audits()
			w := auditPost(mux, tc.ctx, "/api/v1/role-assignments", tc.body)
			added := c.assignments(tc.user) - rows
			if tc.allowed {
				if w.Code != http.StatusCreated || added != 1 {
					t.Errorf("POST /role-assignments = %d (%d new assignments), want 201 and one; body %.200s", w.Code, added, w.Body.String())
				}
				return
			}
			if !forbidden(w) || added != 0 || c.audits() != audits {
				t.Errorf("POST /role-assignments = %d %q, %d new assignments, %d new audit entries; want 403 problem+json and nothing stored (grant ceiling); body %.200s",
					w.Code, w.Header().Get("Content-Type"), added, c.audits()-audits, w.Body.String())
			}
		})
	}
	if orgWideAdmin(c.delegate) || orgWideAdmin(target) || !orgWideAdmin(target2) {
		t.Errorf("org-wide org_admin: delegate %v, target %v, target2 %v; want only target2", orgWideAdmin(c.delegate), orgWideAdmin(target), orgWideAdmin(target2))
	}
}

// TestRolesAssignSunsetPathGrantCeiling covers WP-394 (RBA-03, F-015, E-25)
// for the sunset path POST /api/v1/roles/assign, the same handler as POST
// /role-assignments: the client-1 delegate's org-wide org_admin assignment is
// 403 without row; its viewer assignment in client 1 and the org-wide
// org_admin's org-wide assignment are 201.
func TestRolesAssignSunsetPathGrantCeiling(t *testing.T) {
	c := newCeilingFixture(t, "02")
	adminRole := auditRoleID(t, c.Fixture, "org_admin")
	viewerRole := auditRoleID(t, c.Fixture, "viewer")
	target := c.AppUser(t, c.OrgA, "sunset-target")
	mux := chi.NewRouter()
	NewHandler(NewPGRepository(c.App)).RegisterRoutes(mux)

	w := auditPost(mux, c.delegateCtx, "/api/v1/roles/assign", `{"user_id":"`+target+`","role_id":"`+adminRole+`"}`)
	if !forbidden(w) || c.assignments(target) != 0 {
		t.Errorf("delegate: org-wide org_admin via /roles/assign = %d, %d assignments; want 403 and none; body %.200s", w.Code, c.assignments(target), w.Body.String())
	}
	w = auditPost(mux, c.delegateCtx, "/api/v1/roles/assign",
		`{"user_id":"`+target+`","role_id":"`+viewerRole+`","scope_type":"client","scope_id":"`+c.Client1+`"}`)
	if w.Code != http.StatusCreated || c.assignments(target) != 1 {
		t.Errorf("delegate: viewer in client 1 via /roles/assign = %d %.200s, want 201", w.Code, w.Body.String())
	}
	w = auditPost(mux, c.orgAdminCtx, "/api/v1/roles/assign", `{"user_id":"`+target+`","role_id":"`+adminRole+`"}`)
	if w.Code != http.StatusCreated || c.assignments(target) != 2 {
		t.Errorf("org-wide org_admin via /roles/assign = %d %.200s, want 201", w.Code, w.Body.String())
	}
}

// TestInvitationRoleScopeMismatch covers WP-394 (TLC-03, RBA-02, F-305): a
// client role (role.scope client) is invited and assigned with scope_type
// client only, a site role with scope_type site only; an organization role
// fits every scope. A mismatch is 422 with field scope_type and stores
// nothing, for POST /invitations and POST /role-assignments alike.
func TestInvitationRoleScopeMismatch(t *testing.T) {
	c := newCeilingFixture(t, "03")
	viewerRole := auditRoleID(t, c.Fixture, "viewer")
	techRole := auditRoleID(t, c.Fixture, "client_technician")
	siteRole := c.ID()
	c.exec(`INSERT INTO role (id, organization_id, name, permissions, scope) VALUES ($1, $2, 'mismatch-site-reader', '["ci:read"]', 'site')`, siteRole, c.OrgA)
	var team string
	if err := c.Admin.QueryRow(context.Background(), `INSERT INTO team (organization_id, name) VALUES ($1, 'mismatch team') RETURNING id::text`, c.OrgA).Scan(&team); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// team has no ON DELETE CASCADE to organization.
		_, _ = c.Admin.Exec(context.Background(), `DELETE FROM team WHERE id = $1`, team)
	})

	invitations := chi.NewRouter()
	NewInvitationHandler(NewPGInvitations(c.App)).WithAcceptURL(testAcceptURL).RegisterRoutes(invitations)
	users := chi.NewRouter()
	NewHandler(NewPGRepository(c.App)).RegisterRoutes(users)
	field := func(w *httptest.ResponseRecorder) string {
		var problem struct {
			Field string `json:"field"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &problem)
		return problem.Field
	}

	t.Run("invitations", func(t *testing.T) {
		for i, tc := range []struct {
			name, role, scopeType, scopeID string
			allowed                        bool
		}{
			{"client_technician org-wide", techRole, "org", "", false},
			{"client_technician at a site", techRole, "site", c.site1, false},
			{"site role org-wide", siteRole, "org", "", false},
			{"site role in a client", siteRole, "client", c.Client1, false},
			{"client_technician in client 1", techRole, "client", c.Client1, true},
			{"viewer at site 1", viewerRole, "site", c.site1, true},
			{"site role at site 1", siteRole, "site", c.site1, true},
			{"viewer in client 1", viewerRole, "client", c.Client1, true},
		} {
			email := "mismatch-" + strconv.Itoa(i) + "@invite.example"
			body := `{"email":"` + email + `","role_id":"` + tc.role + `","scope_type":"` + tc.scopeType + `"`
			if tc.scopeID != "" {
				body += `,"scope_id":"` + tc.scopeID + `"`
			}
			w := auditPost(invitations, c.orgAdminCtx, "/api/v1/invitations", body+"}")
			n := c.count(`SELECT count(*)::int FROM user_invitation WHERE organization_id = $1 AND email = $2`, c.OrgA, email)
			if tc.allowed {
				if w.Code != http.StatusCreated || n != 1 {
					t.Errorf("%s: %d (%d rows) %.200s, want 201", tc.name, w.Code, n, w.Body.String())
				}
				continue
			}
			if w.Code != http.StatusUnprocessableEntity || field(w) != "scope_type" || n != 0 {
				t.Errorf("%s: %d field %q (%d rows) %.200s, want 422 scope_type and no row", tc.name, w.Code, field(w), n, w.Body.String())
			}
		}
	})

	t.Run("role assignments", func(t *testing.T) {
		for _, tc := range []struct {
			name, role, scopeType, scopeID string
			allowed                        bool
		}{
			{"client_technician org-wide", techRole, "organization", "", false},
			{"client_technician at a site", techRole, "site", c.site1, false},
			{"client_technician in a team", techRole, "team", team, false},
			{"site role in a team", siteRole, "team", team, false},
			{"site role in a client", siteRole, "client", c.Client1, false},
			{"client_technician in client 1", techRole, "client", c.Client1, true},
			{"viewer at site 1", viewerRole, "site", c.site1, true},
			{"site role at site 1", siteRole, "site", c.site1, true},
			{"viewer in a team", viewerRole, "team", team, true},
		} {
			user := c.AppUser(t, c.OrgA, "mismatch-"+strings.ReplaceAll(tc.name, " ", "-"))
			body := `{"user_id":"` + user + `","role_id":"` + tc.role + `","scope_type":"` + tc.scopeType + `"`
			if tc.scopeID != "" {
				body += `,"scope_id":"` + tc.scopeID + `"`
			}
			w := auditPost(users, c.orgAdminCtx, "/api/v1/role-assignments", body+"}")
			n := c.assignments(user)
			if tc.allowed {
				if w.Code != http.StatusCreated || n != 1 {
					t.Errorf("%s: %d (%d rows) %.200s, want 201", tc.name, w.Code, n, w.Body.String())
				}
				continue
			}
			if w.Code != http.StatusUnprocessableEntity || field(w) != "scope_type" || n != 0 {
				t.Errorf("%s: %d field %q (%d rows) %.200s, want 422 scope_type and no row", tc.name, w.Code, field(w), n, w.Body.String())
			}
		}
	})
}

// TestRoleAssignmentScopeIsNeverWidened covers WP-394 (RBA-03, API-05): a
// role assignment that names a client or site scope must name a client or
// site of the organization; without scope_id, with an unknown one or one of
// organization B it is 422 with field scope_id and stores nothing, in
// particular no org-wide assignment (before, POST /role-assignments with
// scope_type client and no scope_id stored an org-wide one). An org-wide
// assignment names no scope_id. The answers carry no repository or database
// text.
func TestRoleAssignmentScopeIsNeverWidened(t *testing.T) {
	c := newCeilingFixture(t, "6e")
	viewerRole := auditRoleID(t, c.Fixture, "viewer")
	customRole := c.ID()
	c.exec(`INSERT INTO custom_role (id, organization_id, name, permissions) VALUES ($1, $2, 'widen-custom', '["ci:read"]')`, customRole, c.OrgA)
	t.Cleanup(func() {
		_, _ = c.Admin.Exec(context.Background(), `DELETE FROM user_custom_role WHERE custom_role_id = $1`, customRole)
		_, _ = c.Admin.Exec(context.Background(), `DELETE FROM custom_role WHERE id = $1`, customRole)
	})
	var clientB, siteB string
	if err := c.Admin.QueryRow(context.Background(), `INSERT INTO client (organization_id, name, slug) VALUES ($1, 'widen client b', 'scopetest-6e-cb') RETURNING id::text`,
		c.OrgB).Scan(&clientB); err != nil {
		t.Fatal(err)
	}
	if err := c.Admin.QueryRow(context.Background(), `INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, 'widen site b') RETURNING id::text`,
		c.OrgB, clientB).Scan(&siteB); err != nil {
		t.Fatal(err)
	}
	target := c.AppUser(t, c.OrgA, "widen-target")
	mux := chi.NewRouter()
	NewHandler(NewPGRepository(c.App)).RegisterRoutes(mux)

	for _, role := range []string{`"role_id":"` + viewerRole + `"`, `"custom_role_id":"` + customRole + `"`} {
		for _, tc := range []struct{ name, scope, field string }{
			{"client without scope_id", `"scope_type":"client"`, "scope_id"},
			{"site without scope_id", `"scope_type":"site"`, "scope_id"},
			{"client with an invalid scope_id", `"scope_type":"client","scope_id":"not-a-client"`, "scope_id"},
			{"unknown client", `"scope_type":"client","scope_id":"` + c.ID() + `"`, "scope_id"},
			{"unknown site", `"scope_type":"site","scope_id":"` + c.ID() + `"`, "scope_id"},
			{"client of organization B", `"scope_type":"client","scope_id":"` + clientB + `"`, "scope_id"},
			{"site of organization B", `"scope_type":"site","scope_id":"` + siteB + `"`, "scope_id"},
			{"organization with a scope_id", `"scope_type":"organization","scope_id":"` + c.Client1 + `"`, "scope_id"},
			{"unknown scope_type", `"scope_type":"region","scope_id":"` + c.Client1 + `"`, "scope_type"},
		} {
			w := auditPost(mux, c.orgAdminCtx, "/api/v1/role-assignments", `{"user_id":"`+target+`",`+role+`,`+tc.scope+`}`)
			var problem struct {
				Field  string `json:"field"`
				Detail string `json:"detail"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &problem)
			if w.Code != http.StatusUnprocessableEntity || problem.Field != tc.field {
				t.Errorf("%s (%s): %d field %q %.200s, want 422 %s", tc.name, role, w.Code, problem.Field, w.Body.String(), tc.field)
			}
			for _, leak := range []string{"violates", "constraint", "SQLSTATE", "role_assignment", "user_custom_role", "pgx", "ERROR"} {
				if strings.Contains(w.Body.String(), leak) {
					t.Errorf("%s (%s): the answer discloses %q: %s", tc.name, role, leak, w.Body.String())
				}
			}
		}
	}
	if n := c.assignments(target); n != 0 {
		t.Fatalf("refused scopes stored %d assignments, want none (and never an org-wide one)", n)
	}
	// The valid client and site scopes are stored as such.
	for _, scope := range []string{`"scope_type":"client","scope_id":"` + c.Client1 + `"`, `"scope_type":"site","scope_id":"` + c.site1 + `"`} {
		if w := auditPost(mux, c.orgAdminCtx, "/api/v1/role-assignments", `{"user_id":"`+target+`","role_id":"`+viewerRole+`",`+scope+`}`); w.Code != http.StatusCreated {
			t.Errorf("POST %s: %d %.200s, want 201", scope, w.Code, w.Body.String())
		}
	}
	if n := c.count(`SELECT count(*)::int FROM role_assignment WHERE user_id = $1 AND scope_client_id IS NULL AND scope_site_id IS NULL AND scope_team_id IS NULL`, target); n != 0 {
		t.Errorf("%d org-wide assignments of the target, want none", n)
	}
}
