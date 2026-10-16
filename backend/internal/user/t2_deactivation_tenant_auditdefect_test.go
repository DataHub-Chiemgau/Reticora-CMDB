//go:build auditdefect

// Phase-1 acceptance audit, group T2-auth-authz: defect test of the user
// deactivation and anonymization against PostgreSQL. It encodes a
// requirement the product currently violates and fails on an assertion until
// the finding is fixed.
//
//   - WP-042 (TLC-04, AUT-02, AUT-01; TEN tenant isolation), finding P2
//     "Deactivation request blacklists sessions of users in other
//     organizations (cross-tenant forced logout)"; gap (critical)
//     "Cross-tenant regression test for deactivation and anonymization".
//     user/handler.go calls revokeSessions(id) before the org-scoped update
//     finds the user, and the blacklist is keyed by the user id only.
//
// Retest (needs a migrated database):
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_02?sslmode=disable go test -tags auditdefect -count=1 -v -run 'TestAuditT2' ./internal/user/
package user_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/go-chi/chi/v5"
)

// TestAuditT2ForeignDeactivationRevokesNothing: an organization A admin who
// sends PATCH /users/{id} status=inactive or POST /users/{id}/anonymize for a
// user of organization B gets 404, and the organization B user's sessions stay
// valid (tenant isolation). Same-organization deactivation still ends the
// session at once (control, TLC-04).
func TestAuditT2ForeignDeactivationRevokesNothing(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Fatal("TEST_DATABASE_URL is required for this audit test")
	}
	f := scopetest.Seed(t, "eb")
	bg := context.Background()
	adminA := f.AppUser(t, f.OrgA, "t2-deact-admin-a")

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := identity.NewSessionIssuer(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	if err != nil {
		t.Fatal(err)
	}
	revocations := identity.NewSessionRevocations(cache.NewMemoryStore())
	issuer = issuer.WithRevocations(revocations)
	session := func(subject, org string) string {
		t.Helper()
		issued := time.Now().UTC().Add(-2 * time.Second)
		token, err := issuer.Issue(identity.SessionClaims{Subject: subject, OrganizationID: org, ID: "t2-" + subject, IssuedAt: issued, ExpiresAt: issued.Add(15 * time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := issuer.Validate(token); err != nil {
			t.Fatalf("token of %s before the request: %v", subject, err)
		}
		return token
	}

	mux := chi.NewRouter()
	user.NewHandler(user.NewPGRepository(f.App)).WithSessionRevoker(revocations).RegisterRoutes(mux)
	asAdminA := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		scope := database.OrgWideScope(f.OrgA, adminA)
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: adminA})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(database.ContextWithTenantScope(ctx, &scope)))
		return w
	}

	for _, c := range []struct {
		name, method, suffix, body string
	}{
		{"PATCH status inactive", http.MethodPatch, "", `{"status":"inactive"}`},
		{"POST anonymize", http.MethodPost, "/anonymize", ""},
	} {
		t.Run(c.name+" on an organization B user", func(t *testing.T) {
			victim := f.AppUser(t, f.OrgB, "t2-deact-victim-"+strings.ReplaceAll(strings.ToLower(c.name), " ", "-"))
			token := session(victim, f.OrgB)
			w := asAdminA(c.method, "/api/v1/users/"+victim+c.suffix, c.body)
			if w.Code != http.StatusNotFound {
				t.Errorf("%s by an org A admin on an org B user: %d, want 404", c.name, w.Code)
			}
			if at, err := revocations.RevokedAt(bg, victim); err != nil || !at.IsZero() {
				t.Errorf("%s by org A wrote a session blacklist entry for the org B user (revoked at %v, err %v); want none", c.name, at, err)
			}
			if _, err := issuer.Validate(token); err != nil {
				t.Errorf("org B user's session after the foreign %s: %v; want still valid", c.name, err)
			}
			var active bool
			if err := f.Admin.QueryRow(bg, `SELECT is_active FROM app_user WHERE id = $1`, victim).Scan(&active); err != nil || !active {
				t.Errorf("org B user active=%v (err %v), want unchanged", active, err)
			}
		})
	}

	t.Run("control: same organization deactivation ends the session", func(t *testing.T) {
		member := f.AppUser(t, f.OrgA, "t2-deact-member-a")
		token := session(member, f.OrgA)
		if w := asAdminA(http.MethodPatch, "/api/v1/users/"+member, `{"status":"inactive"}`); w.Code != http.StatusOK {
			t.Fatalf("deactivate org A user: %d %s", w.Code, w.Body.String())
		}
		if _, err := issuer.Validate(token); err == nil {
			t.Errorf("session of the deactivated org A user is still valid")
		}
	})
}
