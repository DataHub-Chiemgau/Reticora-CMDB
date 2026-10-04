package identity_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/go-chi/chi/v5"
)

// TestDeactivationEndsAccess covers WP-042 (TLC-04, AUT-02, AUT-01): a login
// never reactivates a deactivated user; deactivation revokes the user's API
// keys and records an audit entry in the same transaction and blacklists the
// sessions; refresh checks the status and reloads the grants; a user owning
// open objects cannot be deleted.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDeactivationEndsAccess(t *testing.T) {
	f := scopetest.Seed(t, "53")
	bg := context.Background()
	users := user.NewPGRepository(f.App)
	admin := f.AppUser(t, f.OrgA, "deact-admin")

	// First login provisions the user.
	subject := "deact-oidc-subject-53"
	userID, err := users.EnsureUser(bg, f.OrgA, subject, "deact@example.invalid", "Deact")
	if err != nil {
		t.Fatalf("first login: %v", err)
	}

	// A role granting ci:read and an API key of the user.
	roleID := f.ID()
	if _, err = f.Admin.Exec(bg, `INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 'deact-reader', '["ci:read"]')`, roleID, f.OrgA); err != nil {
		t.Fatalf("seed role: %v", err)
	}
	if _, err = f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, userID, roleID); err != nil {
		t.Fatalf("assign role: %v", err)
	}
	keyID := f.ID()
	if _, err = f.Admin.Exec(bg, `INSERT INTO api_key (id, organization_id, name, key_hash, key_prefix, created_by) VALUES ($1, $2, 'deact', 'hash', 'deact53', $3)`,
		keyID, f.OrgA, userID); err != nil {
		t.Fatalf("seed api key: %v", err)
	}

	// Sessions: a blacklist on a cache store; the refresh handler reads the
	// user status and the grants from the database.
	revocations := identity.NewSessionRevocations(cache.NewMemoryStore())
	sessions := newIssuer(t).WithRevocations(revocations)
	plain := newIssuer(t) // without blacklist: isolates the refresh status check
	issue := func(issuer *identity.SessionIssuer) string {
		t.Helper()
		now := time.Now().UTC()
		token, issueErr := issuer.Issue(identity.SessionClaims{Subject: userID, OrganizationID: f.OrgA, IssuedAt: now, ExpiresAt: now.Add(time.Hour)})
		if issueErr != nil {
			t.Fatal(issueErr)
		}
		return token
	}
	refresh := func(issuer *identity.SessionIssuer, token string) (int, []identity.Permission) {
		t.Helper()
		h := identity.NewHandler(nil, issuer).WithProvisioning(users, "").WithAccessResolver(permission.NewPGRepository(f.App))
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", strings.NewReader(`{"token":"`+token+`"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.Refresh(w, req)
		if w.Code != http.StatusOK {
			return w.Code, nil
		}
		var out struct {
			Token string `json:"token"`
		}
		if decodeErr := json.Unmarshal(w.Body.Bytes(), &out); decodeErr != nil {
			t.Fatalf("decode refresh: %v", decodeErr)
		}
		claims, validateErr := issuer.Validate(out.Token)
		if validateErr != nil {
			t.Fatalf("refreshed token invalid: %v", validateErr)
		}
		return w.Code, claims.Permissions
	}
	hasCIRead := func(perms []identity.Permission) bool {
		for _, p := range perms {
			if p == identity.PermCIRead {
				return true
			}
		}
		return false
	}

	// Refresh reloads the grants: ci:read now, gone after the role is withdrawn.
	if code, perms := refresh(plain, issue(plain)); code != http.StatusOK || !hasCIRead(perms) {
		t.Fatalf("refresh with role: status %d permissions %v", code, perms)
	}
	if _, err = f.Admin.Exec(bg, `DELETE FROM role_assignment WHERE user_id = $1`, userID); err != nil {
		t.Fatalf("withdraw role: %v", err)
	}
	if code, perms := refresh(plain, issue(plain)); code != http.StatusOK || hasCIRead(perms) {
		t.Errorf("refresh after the role was withdrawn: status %d permissions %v, want no ci:read", code, perms)
	}

	// Deactivation through the API.
	mux := chi.NewRouter()
	user.NewHandler(users).WithSessionRevoker(revocations).RegisterRoutes(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		scope := database.OrgWideScope(f.OrgA, admin)
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: admin})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(database.ContextWithTenantScope(ctx, &scope)))
		return w
	}
	before := issue(sessions)
	time.Sleep(time.Millisecond)
	if w := call(http.MethodPatch, "/api/v1/users/"+userID, `{"status":"inactive"}`); w.Code != http.StatusOK {
		t.Fatalf("deactivate: status %d: %s", w.Code, w.Body.String())
	}

	// Sessions and refresh tokens end at once.
	if _, err = sessions.Validate(before); !errors.Is(err, identity.ErrSessionRevoked) {
		t.Errorf("session after deactivation: %v, want ErrSessionRevoked", err)
	}
	if code, _ := refresh(sessions, before); code != http.StatusUnauthorized {
		t.Errorf("refresh of a revoked session: status %d, want 401", code)
	}
	// Without the blacklist the refresh still fails on the user status.
	if code, _ := refresh(plain, issue(plain)); code != http.StatusUnauthorized {
		t.Errorf("refresh of a deactivated user: status %d, want 401", code)
	}

	// The API key is revoked and no longer found.
	var revoked bool
	if err = f.Admin.QueryRow(bg, `SELECT revoked_at IS NOT NULL FROM api_key WHERE id = $1`, keyID).Scan(&revoked); err != nil || !revoked {
		t.Errorf("api key after deactivation: revoked=%v, %v", revoked, err)
	}
	if key, lookupErr := identity.NewPGAPIKeyStore(f.Admin).LookupByPrefix(bg, "deact53"); lookupErr != nil || key != nil {
		t.Errorf("lookup of the revoked key: %+v, %v; want none", key, lookupErr)
	}

	// The deactivation is audited by the acting admin.
	var audited int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND action = 'user.deactivated' AND resource_id = $2 AND actor_id = $3`,
		f.OrgA, userID, admin).Scan(&audited); err != nil || audited != 1 {
		t.Errorf("audit entries of the deactivation: %d, %v; want 1", audited, err)
	}

	// A new login neither succeeds nor reactivates the user.
	if _, err = users.EnsureUser(bg, f.OrgA, subject, "deact@example.invalid", "Deact"); !errors.Is(err, identity.ErrUserInactive) {
		t.Errorf("login of a deactivated user: %v, want ErrUserInactive", err)
	}
	if active, statusErr := users.UserActive(bg, f.OrgA, userID); statusErr != nil || active {
		t.Errorf("user after login attempt: active=%v, %v; want inactive", active, statusErr)
	}

	// Reactivation allows a new login; the revoked key stays revoked.
	if w := call(http.MethodPatch, "/api/v1/users/"+userID, `{"status":"active"}`); w.Code != http.StatusOK {
		t.Fatalf("reactivate: status %d: %s", w.Code, w.Body.String())
	}
	if _, err = users.EnsureUser(bg, f.OrgA, subject, "deact@example.invalid", "Deact"); err != nil {
		t.Errorf("login after reactivation: %v", err)
	}
	if _, err = sessions.Validate(issue(sessions)); err != nil {
		t.Errorf("session issued after reactivation: %v", err)
	}
	if err = f.Admin.QueryRow(bg, `SELECT revoked_at IS NOT NULL FROM api_key WHERE id = $1`, keyID).Scan(&revoked); err != nil || !revoked {
		t.Errorf("api key after reactivation: revoked=%v, %v; want still revoked", revoked, err)
	}

	// Handover rule: an owner of an open ticket cannot be deleted; a user
	// still referenced by a closed ticket neither; an unreferenced one can.
	ticketID := f.ID()
	if _, err = f.Admin.Exec(bg, `INSERT INTO ticket (id, organization_id, title, reporter_id, assignee_id, status) VALUES ($1, $2, 'handover', $3, $4, 'open')`,
		ticketID, f.OrgA, admin, userID); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	if w := call(http.MethodDelete, "/api/v1/users/"+userID, ""); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "hand them over") {
		t.Errorf("delete an owner of an open ticket: status %d body %s, want 409", w.Code, w.Body.String())
	}
	if _, err = f.Admin.Exec(bg, `UPDATE ticket SET status = 'closed' WHERE id = $1`, ticketID); err != nil {
		t.Fatalf("close ticket: %v", err)
	}
	if w := call(http.MethodDelete, "/api/v1/users/"+userID, ""); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "anonymize") {
		t.Errorf("delete a user referenced by a closed ticket: status %d body %s, want 409", w.Code, w.Body.String())
	}
	spare := f.AppUser(t, f.OrgA, "deact-spare")
	if w := call(http.MethodDelete, "/api/v1/users/"+spare, ""); w.Code != http.StatusNoContent {
		t.Errorf("delete an unreferenced user: status %d body %s, want 204", w.Code, w.Body.String())
	}
}

func newIssuer(t *testing.T) *identity.SessionIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := identity.NewSessionIssuer(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	if err != nil {
		t.Fatal(err)
	}
	return issuer
}
