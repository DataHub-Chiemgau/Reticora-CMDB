package rls_test

// Phase-1 acceptance audit, group T1-tenancy: REGRESSION test (passes).
//
// WP-008 (TEN-04, TEN-05, TEN-06, CH25) gap "Real token chain for a
// CLIENT-scoped user (cls=[C1]) through AuthMiddleware -> TenantMiddleware
// -> repo -> RLS is not exercised" and WP-009 (RBA-03, TEN-04; AC3 "Session-
// Erzeugung und Refresh lesen Rollen aus der DB", AC4 "Ein ausschließlich
// client-zugewiesener Nutzer erhält keinen org-weiten Scope") gap
// "Refresh/login scope derivation with the real PG resolver is shown only by
// a verifier scratch probe".
//
// The session token comes from the real refresh endpoint (identity.Handler
// with permission.PGRepository as access resolver and a real RSA
// SessionIssuer); requests carry it as a bearer token through
// middleware.AuthMiddlewareWithVerifier -> TenantMiddleware -> the complete
// PostgreSQL router -> RLS, as in cmd/server. Each step changes the user's
// role_assignment in the database and refreshes: client 1 only, org-wide,
// narrowed to client 2, no assignment; a user of organization B never sees
// organization A.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -count=1 -v -run 'TestAuditT1SessionTokenScopeChain' ./internal/tenant/rls/

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

func TestAuditT1SessionTokenScopeChain(t *testing.T) {
	w := newT1World(t, "eb")
	f := w.f
	bg := context.Background()
	ci1 := f.CI(t, f.OrgA, f.Client1, "t1-chain-c1")
	ci2 := f.CI(t, f.OrgA, f.Client2, "t1-chain-c2")
	ciB := f.CI(t, f.OrgB, "", "t1-chain-b")

	users := user.NewPGRepository(f.App)
	subject := f.AppUser(t, f.OrgA, "t1-chain-subject")
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(`DELETE FROM role_assignment WHERE user_id = $1`, subject)
	engineer := w.scalar(`SELECT id::text FROM role WHERE organization_id = $1 AND name = 'engineer'`, f.OrgA)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := identity.NewSessionIssuer(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	if err != nil {
		t.Fatal(err)
	}
	refreshSessions := identity.NewRefreshSessions(cache.NewMemoryStore(), nil)
	auth := identity.NewHandler(nil, issuer).WithProvisioning(users, "").
		WithAccessResolver(permission.NewPGRepository(f.App)).WithRefreshSessions(refreshSessions)
	stack := middleware.AuthMiddlewareWithVerifier(issuer)(w.env.router)

	// refresh runs POST /api/v1/auth/refresh for the user and returns the
	// new access token and its verified claims.
	refresh := func(org, userID string) (string, *identity.SessionClaims) {
		t.Helper()
		now := time.Now().UTC()
		cookie, err := refreshSessions.Issue(bg, &identity.RefreshGrant{Claims: identity.SessionClaims{
			Subject: userID, OrganizationID: org, IssuedAt: now, ExpiresAt: now.Add(time.Hour)}})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
		req.AddCookie(&http.Cookie{Name: identity.RefreshCookieName, Value: cookie})
		rec := httptest.NewRecorder()
		auth.Refresh(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("refresh: %d %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Token string `json:"token"`
		}
		if uerr := json.Unmarshal(rec.Body.Bytes(), &out); uerr != nil || out.Token == "" {
			t.Fatalf("refresh response: %v %s", uerr, rec.Body.String())
		}
		claims, err := issuer.Validate(out.Token)
		if err != nil {
			t.Fatal(err)
		}
		return out.Token, claims
	}
	call := func(token, method, path string, body string) (int, string) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		stack.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	clientsOf := func(c *identity.SessionClaims) identity.ScopeSet {
		if c.Scope == nil {
			return identity.ScopeSet{}
		}
		return c.Scope.Clients
	}

	steps := []struct {
		name       string
		assign     func()
		wantScope  func(identity.ScopeSet) bool
		visible    map[string]bool
		wantListed int // HTTP status of GET /cis
	}{
		{
			name: "client-1 assignment",
			assign: func() {
				exec(`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id) VALUES ($1, $2, $3, $4)`, f.OrgA, subject, engineer, f.Client1)
			},
			wantScope: func(s identity.ScopeSet) bool { return !s.All && len(s.IDs) == 1 && s.IDs[0] == f.Client1 },
			visible:   map[string]bool{ci1: true, ci2: false}, wantListed: http.StatusOK,
		},
		{
			name:      "org-wide assignment",
			assign:    func() { exec(`UPDATE role_assignment SET scope_client_id = NULL WHERE user_id = $1`, subject) },
			wantScope: func(s identity.ScopeSet) bool { return s.All },
			visible:   map[string]bool{ci1: true, ci2: true}, wantListed: http.StatusOK,
		},
		{
			name:      "narrowed to client 2",
			assign:    func() { exec(`UPDATE role_assignment SET scope_client_id = $2 WHERE user_id = $1`, subject, f.Client2) },
			wantScope: func(s identity.ScopeSet) bool { return !s.All && len(s.IDs) == 1 && s.IDs[0] == f.Client2 },
			visible:   map[string]bool{ci1: false, ci2: true}, wantListed: http.StatusOK,
		},
		{
			name:      "no assignment",
			assign:    func() { exec(`DELETE FROM role_assignment WHERE user_id = $1`, subject) },
			wantScope: func(s identity.ScopeSet) bool { return !s.All && len(s.IDs) == 0 },
			visible:   map[string]bool{ci1: false, ci2: false}, wantListed: http.StatusForbidden,
		},
	}
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			s.assign()
			token, claims := refresh(f.OrgA, subject)
			if !s.wantScope(clientsOf(claims)) {
				t.Errorf("token client scope %+v does not match the role assignment", clientsOf(claims))
			}
			if s.name == "client-1 assignment" && (claims.Scope == nil || !claims.Scope.Sites.All) {
				t.Errorf("client assignment: site scope %+v, want unrestricted within the client", claims.Scope)
			}
			code, list := call(token, http.MethodGet, "/api/v1/cis?limit=200", "")
			if code != s.wantListed {
				t.Fatalf("GET /cis: %d %s, want %d", code, trim(list), s.wantListed)
			}
			for id, want := range s.visible {
				if got := strings.Contains(list, id); got != want {
					t.Errorf("GET /cis lists CI %s: %v, want %v", id, got, want)
				}
				if code != http.StatusOK {
					continue
				}
				wantDetail := http.StatusNotFound
				if want {
					wantDetail = http.StatusOK
				}
				if got, _ := call(token, http.MethodGet, "/api/v1/cis/"+id, ""); got != wantDetail {
					t.Errorf("GET /cis/%s: %d, want %d", id, got, wantDetail)
				}
				if !want {
					if got, body := call(token, http.MethodPatch, "/api/v1/cis/"+id, `{"name":"t1-chain-hijack"}`); got != http.StatusNotFound {
						t.Errorf("PATCH invisible CI %s: %d %s, want 404", id, got, trim(body))
					}
					if name := w.scalar(`SELECT name FROM ci WHERE id = $1`, id); name == "t1-chain-hijack" {
						t.Errorf("invisible CI %s was renamed through the token chain", id)
					}
				}
			}
			if strings.Contains(list, ciB) {
				t.Errorf("GET /cis as an organization-A user lists the organization-B CI")
			}
		})
	}

	t.Run("user of organization B", func(t *testing.T) {
		token, claims := refresh(f.OrgB, w.userB)
		if claims.OrganizationID != f.OrgB {
			t.Fatalf("token organization %s, want B", claims.OrganizationID)
		}
		code, list := call(token, http.MethodGet, "/api/v1/cis?limit=200", "")
		if code != http.StatusOK || !strings.Contains(list, ciB) {
			t.Fatalf("GET /cis as org B: %d, own CI listed %v", code, strings.Contains(list, ciB))
		}
		for _, id := range []string{ci1, ci2} {
			if strings.Contains(list, id) {
				t.Errorf("GET /cis as org B lists the organization-A CI %s", id)
			}
			if got, _ := call(token, http.MethodGet, "/api/v1/cis/"+id, ""); got != http.StatusNotFound {
				t.Errorf("GET /cis/{org-A CI} as org B: %d, want 404", got)
			}
		}
	})

	t.Run("a token the issuer did not sign is refused", func(t *testing.T) {
		if code, _ := call("eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0.", http.MethodGet, "/api/v1/cis", ""); code != http.StatusUnauthorized {
			t.Errorf("unsigned token: %d, want 401", code)
		}
	})
}
