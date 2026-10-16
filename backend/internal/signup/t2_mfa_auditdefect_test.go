//go:build auditdefect

// Phase-1 acceptance audit, group T2-auth-authz: defect tests of the MFA
// binding of organization administrators.
//
//   - WP-044 (AUT-09, CH26, E-14), finding P1 "MFA not enforced for
//     org_admins created by signup, invitation, operator path or the
//     reticora-admin group". AUT-09: "MFA fuer org_admin Pflicht". The realm
//     requires OTP for users holding the realm role mfa_required (contained
//     in org_admin), but the accounts the backend creates for the first
//     org_admin of an organization (self-service signup: Verify ->
//     CreateAccount; operator path: CreateOrganization -> InviteAccount) get
//     no realm role and no CONFIGURE_TOTP action, and the realm group
//     reticora-admin, which identity/rbac.go maps to the app role org_admin,
//     carries no realm role. Gap (high) "Backend/Keycloak role sync unit
//     test".
//
// Retest:
//
//	cd backend && go test -tags auditdefect -count=1 -v -run 'TestAuditT2' ./internal/signup/
package signup

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
)

// mfaRecorder is a fake Keycloak admin API that records every created user
// and every realm role mapping.
type mfaRecorder struct {
	mu       sync.Mutex
	created  []keycloakUser
	mappings map[string][]string // user id -> realm role names
}

func (m *mfaRecorder) server(t *testing.T) *httptest.Server {
	t.Helper()
	m.mappings = map[string][]string{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /realms/reticora/protocol/openid-connect/token", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "admin-token"})
	})
	mux.HandleFunc("POST /admin/realms/reticora/users", func(w http.ResponseWriter, r *http.Request) {
		var u keycloakUser
		if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
			t.Errorf("decode user: %v", err)
		}
		m.mu.Lock()
		m.created = append(m.created, u)
		m.mu.Unlock()
		w.Header().Set("Location", "http://"+r.Host+"/admin/realms/reticora/users/kc-user-1")
		w.WriteHeader(http.StatusCreated)
	})
	// Realm role lookups by name (GET /roles/{name}) for a later mapping.
	mux.HandleFunc("GET /admin/realms/reticora/roles/{name}", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "role-" + r.PathValue("name"), "name": r.PathValue("name")})
	})
	mux.HandleFunc("POST /admin/realms/reticora/users/{id}/role-mappings/realm", func(w http.ResponseWriter, r *http.Request) {
		var roles []struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&roles); err != nil {
			t.Errorf("decode role mapping: %v", err)
		}
		m.mu.Lock()
		for _, role := range roles {
			m.mappings[r.PathValue("id")] = append(m.mappings[r.PathValue("id")], role.Name)
		}
		m.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("PUT /admin/realms/reticora/users/{id}/execute-actions-email", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// mfaBound reports whether the created account is bound to MFA: it holds
// the realm role mfa_required (directly or through org_admin), or it must
// set up OTP at its first login (required action CONFIGURE_TOTP).
func (m *mfaRecorder) mfaBound(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if slices.Contains(m.mappings[id], "mfa_required") || slices.Contains(m.mappings[id], "org_admin") {
		return true
	}
	for _, u := range m.created {
		if slices.Contains(u.RequiredActions, "CONFIGURE_TOTP") {
			return true
		}
	}
	return false
}

// TestAuditT2FirstOrgAdminAccountRequiresMFA: the first org_admin of an
// organization, created by the self-service signup (CreateAccount) or the
// operator path (InviteAccount), must be bound to the mandatory second
// factor of AUT-09.
func TestAuditT2FirstOrgAdminAccountRequiresMFA(t *testing.T) {
	for _, c := range []struct {
		name   string
		create func(*KeycloakAccounts) (string, error)
	}{
		{"signup: CreateAccount of the first org_admin", func(k *KeycloakAccounts) (string, error) {
			return k.CreateAccount(context.Background(), Account{Email: "admin@t2-signup.example", DisplayName: "T2 Admin",
				Password: "a-long-signup-password-1", OrganizationID: "7a2a0000-0000-4000-8000-000000000001"})
		}},
		{"operator path: InviteAccount of the first org_admin", func(k *KeycloakAccounts) (string, error) {
			return k.InviteAccount(context.Background(), Account{Email: "admin@t2-operator.example", DisplayName: "T2 Admin",
				OrganizationID: "7a2a0000-0000-4000-8000-000000000002"})
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := &mfaRecorder{}
			srv := rec.server(t)
			kc, err := NewKeycloakAccounts(srv.URL+"/realms/reticora", "signup", "s3cret", srv.Client())
			if err != nil {
				t.Fatal(err)
			}
			id, err := c.create(kc)
			if err != nil {
				t.Fatalf("create account: %v", err)
			}
			if !rec.mfaBound(id) {
				t.Errorf("%s: account %s has realm roles %v and required actions %v; want the realm role mfa_required/org_admin or CONFIGURE_TOTP (AUT-09: MFA mandatory for org_admin)",
					c.name, id, rec.mappings[id], rec.created[0].RequiredActions)
			}
		})
	}
}

// TestAuditT2RealmAdminGroupRequiresMFA: identity/rbac.go maps the realm
// group reticora-admin to the app role org_admin. Both shipped realm exports
// must therefore give that group the realm role org_admin (or mfa_required),
// otherwise its members are org_admins without the second factor.
func TestAuditT2RealmAdminGroupRequiresMFA(t *testing.T) {
	for _, file := range []string{"realm-reticora.json", "realm-reticora-dev.json"} {
		t.Run(file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "keycloak", file))
			if err != nil {
				t.Fatalf("read realm export: %v", err)
			}
			var realm struct {
				Groups []struct {
					Name       string   `json:"name"`
					RealmRoles []string `json:"realmRoles"`
				} `json:"groups"`
			}
			if err := json.Unmarshal(raw, &realm); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, g := range realm.Groups {
				if g.Name != "reticora-admin" {
					continue
				}
				found = true
				if !slices.Contains(g.RealmRoles, "org_admin") && !slices.Contains(g.RealmRoles, "mfa_required") {
					t.Errorf("%s: group reticora-admin (app role org_admin) has realm roles %v; want org_admin or mfa_required (AUT-09)", file, g.RealmRoles)
				}
			}
			if !found {
				t.Errorf("%s: group reticora-admin missing", file)
			}
		})
	}
}
