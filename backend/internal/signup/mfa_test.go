package signup

// MFA binding of organization administrators (WP-357, AUT-09, CH26, E-14,
// finding F-005). The realm requires OTP for users holding the realm role
// mfa_required (browser flow browser-mfa). The accounts the backend creates
// for the first org_admin of an organization (self-service signup: Verify ->
// CreateAccount; operator path: CreateOrganization -> InviteAccount) must
// hold that role and set up OTP at their first login, and the realm group
// reticora-admin, which identity/rbac.go maps to the app role org_admin,
// must carry it in both shipped realm exports. Moved from the auditdefect
// file t2_mfa_auditdefect_test.go.
//
// Retest:
//
//	cd backend && go test -count=1 -v -run 'TestAuditT2FirstOrgAdminAccountRequiresMFA|TestAuditT2RealmAdminGroupRequiresMFA' ./internal/signup/

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
)

// mfaRecorder is a fake Keycloak admin API that records every created user
// and every realm role mapping. mapStatus, when set, is the answer of the
// role mapping request.
type mfaRecorder struct {
	mu        sync.Mutex
	created   map[string]keycloakUser // user id -> created user
	mappings  map[string][]string     // user id -> realm role names
	deleted   []string
	mapStatus int
	next      int
}

func (m *mfaRecorder) server(t *testing.T) *httptest.Server {
	t.Helper()
	m.created = map[string]keycloakUser{}
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
		m.next++
		id := "kc-user-" + strconv.Itoa(m.next)
		m.created[id] = u
		m.mu.Unlock()
		w.Header().Set("Location", "http://"+r.Host+"/admin/realms/reticora/users/"+id)
		w.WriteHeader(http.StatusCreated)
	})
	// The realm roles a user may still be given, as Keycloak lists them to a
	// client holding manage-users (GET /roles/{name} would need view-realm).
	mux.HandleFunc("GET /admin/realms/reticora/users/{id}/role-mappings/realm/available", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": "role-viewer", "name": "viewer"},
			{"id": "role-mfa_required", "name": "mfa_required"},
			{"id": "role-org_admin", "name": "org_admin"},
		})
	})
	mux.HandleFunc("POST /admin/realms/reticora/users/{id}/role-mappings/realm", func(w http.ResponseWriter, r *http.Request) {
		var roles []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&roles); err != nil {
			t.Errorf("decode role mapping: %v", err)
		}
		if m.mapStatus != 0 {
			w.WriteHeader(m.mapStatus)
			return
		}
		m.mu.Lock()
		for _, role := range roles {
			if role.ID != "role-"+role.Name {
				t.Errorf("role mapping %+v without the role id Keycloak requires", role)
			}
			m.mappings[r.PathValue("id")] = append(m.mappings[r.PathValue("id")], role.Name)
		}
		m.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("PUT /admin/realms/reticora/users/{id}/execute-actions-email", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /admin/realms/reticora/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.deleted = append(m.deleted, r.PathValue("id"))
		m.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// mfaBound reports whether the created account is bound to MFA: it holds
// the realm role mfa_required and must set up OTP at its first login
// (required action CONFIGURE_TOTP).
func (m *mfaRecorder) mfaBound(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Contains(m.mappings[id], "mfa_required") && slices.Contains(m.created[id].RequiredActions, "CONFIGURE_TOTP")
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
				t.Errorf("%s: account %s has realm roles %v and required actions %v; want the realm role mfa_required and CONFIGURE_TOTP (AUT-09: MFA mandatory for org_admin)",
					c.name, id, rec.mappings[id], rec.created[id].RequiredActions)
			}
			if slices.Contains(rec.mappings[id], "org_admin") {
				t.Errorf("%s: realm role org_admin mapped; the app role comes from the database, the realm only marks MFA", c.name)
			}
			if len(rec.deleted) != 0 {
				t.Errorf("%s: accounts deleted %v", c.name, rec.deleted)
			}
		})
	}
}

// TestAuditT2RealmAdminGroupRequiresMFA: identity/rbac.go maps the realm
// group reticora-admin to the app role org_admin. Both shipped realm exports
// must therefore give that group the realm role mfa_required, otherwise its
// members are org_admins without the second factor. Deliberately not
// org_admin: the realm must not become a second source of app rights.
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
				if !slices.Equal(g.RealmRoles, []string{"mfa_required"}) {
					t.Errorf("%s: group reticora-admin (app role org_admin) has realm roles %v; want [mfa_required] (AUT-09)", file, g.RealmRoles)
				}
			}
			if !found {
				t.Errorf("%s: group reticora-admin missing", file)
			}
		})
	}
}
