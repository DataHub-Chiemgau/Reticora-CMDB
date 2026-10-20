package signup

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// fakeKeycloak serves the token endpoint and the admin users endpoint of
// realm "reticora".
func fakeKeycloak(t *testing.T, createStatus int, created *keycloakUser, deleted *string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /realms/reticora/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("grant_type") != "client_credentials" || r.FormValue("client_id") != "signup" || r.FormValue("client_secret") != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "admin-token"})
	})
	mux.HandleFunc("POST /admin/realms/reticora/users", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer admin-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(created); err != nil {
			t.Errorf("decode user: %v", err)
		}
		switch createStatus {
		case http.StatusCreated:
			w.Header().Set("Location", "http://"+r.Host+"/admin/realms/reticora/users/0a1b2c")
			w.WriteHeader(http.StatusCreated)
		case http.StatusBadRequest:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalidPasswordMinLengthMessage","error_description":"Invalid password: minimum length 14."}`))
		default:
			w.WriteHeader(createStatus)
		}
	})
	mux.HandleFunc("DELETE /admin/realms/reticora/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		*deleted = r.PathValue("id")
		w.WriteHeader(http.StatusNoContent)
	})
	fakeRealmRoles(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// fakeRole is a realm role as the Keycloak admin API lists it.
type fakeRole struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// fakeRealmRoles serves the realm roles available to a user and accepts
// their mapping (the MFA binding of the first org_admin, WP-357).
func fakeRealmRoles(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/realms/reticora/users/{id}/role-mappings/realm/available", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]fakeRole{{ID: "role-mfa", Name: "mfa_required"}})
	})
	mux.HandleFunc("POST /admin/realms/reticora/users/{id}/role-mappings/realm", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
}

func TestKeycloakCreateAccount(t *testing.T) {
	var (
		user    keycloakUser
		deleted string
	)
	srv := fakeKeycloak(t, http.StatusCreated, &user, &deleted)
	kc, err := NewKeycloakAccounts(srv.URL+"/realms/reticora", "signup", "s3cret", srv.Client())
	if err != nil {
		t.Fatalf("NewKeycloakAccounts: %v", err)
	}
	id, err := kc.CreateAccount(context.Background(), Account{Email: "a@example.com", DisplayName: "Ada",
		Password: "correct horse battery", OrganizationID: "org-1"})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if id != "0a1b2c" {
		t.Errorf("id = %q", id)
	}
	if user.Username != "a@example.com" || user.Email != "a@example.com" || !user.Enabled || !user.EmailVerified {
		t.Errorf("user = %+v", user)
	}
	if got := user.Attributes["organization_id"]; len(got) != 1 || got[0] != "org-1" {
		t.Errorf("organization_id attribute = %v", got)
	}
	if len(user.Credentials) != 1 || user.Credentials[0].Value != "correct horse battery" || user.Credentials[0].Temporary {
		t.Errorf("credentials = %+v", user.Credentials)
	}
	if err := kc.DeleteAccount(context.Background(), id); err != nil || deleted != "0a1b2c" {
		t.Errorf("DeleteAccount: %v, deleted %q", err, deleted)
	}
}

func TestKeycloakCreateAccountErrors(t *testing.T) {
	var (
		user    keycloakUser
		deleted string
	)
	conflict := fakeKeycloak(t, http.StatusConflict, &user, &deleted)
	kc, _ := NewKeycloakAccounts(conflict.URL+"/realms/reticora", "signup", "s3cret", conflict.Client())
	if _, err := kc.CreateAccount(context.Background(), Account{Email: "a@example.com"}); !errors.Is(err, ErrAccountExists) {
		t.Errorf("409: %v, want ErrAccountExists", err)
	}

	policy := fakeKeycloak(t, http.StatusBadRequest, &user, &deleted)
	kc, _ = NewKeycloakAccounts(policy.URL+"/realms/reticora", "signup", "s3cret", policy.Client())
	var fe *api.FieldError
	if _, err := kc.CreateAccount(context.Background(), Account{Email: "a@example.com"}); !errors.As(err, &fe) ||
		fe.Field != "password" || fe.Status != http.StatusUnprocessableEntity {
		t.Errorf("400: %v, want 422 password field error", err)
	}

	kc, _ = NewKeycloakAccounts(policy.URL+"/realms/reticora", "signup", "wrong", policy.Client())
	if _, err := kc.CreateAccount(context.Background(), Account{Email: "a@example.com"}); err == nil {
		t.Error("wrong client secret accepted")
	}

	if _, err := NewKeycloakAccounts("https://idp.example/auth", "signup", "s3cret", nil); err == nil {
		t.Error("issuer without /realms/ accepted")
	}
}

func TestKeycloakInviteAccount(t *testing.T) {
	var (
		user    keycloakUser
		actions []string
		deleted string
	)
	inviteStatus := http.StatusNoContent
	mux := http.NewServeMux()
	mux.HandleFunc("POST /realms/reticora/protocol/openid-connect/token", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "admin-token"})
	})
	mux.HandleFunc("POST /admin/realms/reticora/users", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&user)
		w.Header().Set("Location", "http://"+r.Host+"/admin/realms/reticora/users/inv-1")
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("PUT /admin/realms/reticora/users/{id}/execute-actions-email", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&actions)
		w.WriteHeader(inviteStatus)
	})
	mux.HandleFunc("DELETE /admin/realms/reticora/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		deleted = r.PathValue("id")
		w.WriteHeader(http.StatusNoContent)
	})
	fakeRealmRoles(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	kc, _ := NewKeycloakAccounts(srv.URL+"/realms/reticora", "signup", "s3cret", srv.Client())

	id, err := kc.InviteAccount(context.Background(), Account{Email: "admin@acme.example", OrganizationID: "org-9"})
	if err != nil || id != "inv-1" {
		t.Fatalf("InviteAccount = %q, %v", id, err)
	}
	if len(user.Credentials) != 0 || user.EmailVerified || user.Attributes["organization_id"][0] != "org-9" {
		t.Errorf("invited user = %+v", user)
	}
	if len(actions) != 2 || actions[0] != "UPDATE_PASSWORD" || actions[1] != "VERIFY_EMAIL" {
		t.Errorf("actions = %v", actions)
	}
	if !slices.Equal(user.RequiredActions, []string{"UPDATE_PASSWORD", "VERIFY_EMAIL", "CONFIGURE_TOTP"}) {
		t.Errorf("required actions = %v", user.RequiredActions)
	}

	// A failing invitation mail removes the account again.
	inviteStatus = http.StatusInternalServerError
	if _, err := kc.InviteAccount(context.Background(), Account{Email: "b@acme.example"}); err == nil {
		t.Fatal("failed invitation accepted")
	}
	if deleted != "inv-1" {
		t.Errorf("deleted = %q", deleted)
	}
}

func TestKeycloakAccountNames(t *testing.T) {
	cases := map[string][2]string{
		"":                    {"", ""},
		"Ada":                 {"Ada", ""},
		"Ada Lovelace":        {"Ada", "Lovelace"},
		" Anna  Maria  Weiß ": {"Anna Maria", "Weiß"},
	}
	for display, want := range cases {
		if first, last := splitName(display); first != want[0] || last != want[1] {
			t.Errorf("splitName(%q) = %q, %q; want %q, %q", display, first, last, want[0], want[1])
		}
	}
	var user keycloakUser
	var deleted string
	srv := fakeKeycloak(t, http.StatusCreated, &user, &deleted)
	kc, err := NewKeycloakAccounts(srv.URL+"/realms/reticora", "signup", "s3cret", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	// The realm's user profile requires first and last name: both are set,
	// so the first login does not stop at the profile form.
	if _, err = kc.CreateAccount(context.Background(), Account{Email: "a@example.com", DisplayName: "Ada Lovelace",
		Password: "correct horse battery", OrganizationID: "org-1"}); err != nil {
		t.Fatal(err)
	}
	if user.FirstName != "Ada" || user.LastName != "Lovelace" {
		t.Errorf("names = %q %q", user.FirstName, user.LastName)
	}
}

// TestKeycloakMFARoleFailureDeletesAccount (WP-357, AUT-09): when the realm
// role mfa_required cannot be mapped to the first org_admin (mapping refused,
// role missing from the realm, lookup failing), the account is deleted again
// and the error reaches the caller: no org_admin without MFA. The invitation
// mail of the operator path is not sent.
func TestKeycloakMFARoleFailureDeletesAccount(t *testing.T) {
	for _, c := range []struct {
		name                     string
		availableStatus, mapping int
		available                []fakeRole
		mapAttempts              int
	}{
		{"mapping refused", http.StatusOK, http.StatusForbidden, []fakeRole{{ID: "role-mfa", Name: "mfa_required"}}, 1},
		{"role missing in the realm", http.StatusOK, http.StatusNoContent, []fakeRole{{ID: "role-viewer", Name: "viewer"}}, 0},
		{"role lookup refused", http.StatusForbidden, http.StatusNoContent, nil, 0},
	} {
		for _, path := range []string{"signup", "operator"} {
			t.Run(c.name+"/"+path, func(t *testing.T) {
				var deleted, mailed []string
				mapped := 0
				mux := http.NewServeMux()
				mux.HandleFunc("POST /realms/reticora/protocol/openid-connect/token", func(w http.ResponseWriter, _ *http.Request) {
					_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "admin-token"})
				})
				mux.HandleFunc("POST /admin/realms/reticora/users", func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Location", "http://"+r.Host+"/admin/realms/reticora/users/adm-1")
					w.WriteHeader(http.StatusCreated)
				})
				mux.HandleFunc("GET /admin/realms/reticora/users/{id}/role-mappings/realm/available", func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(c.availableStatus)
					_ = json.NewEncoder(w).Encode(c.available)
				})
				mux.HandleFunc("POST /admin/realms/reticora/users/{id}/role-mappings/realm", func(w http.ResponseWriter, _ *http.Request) {
					mapped++
					w.WriteHeader(c.mapping)
				})
				mux.HandleFunc("PUT /admin/realms/reticora/users/{id}/execute-actions-email", func(w http.ResponseWriter, r *http.Request) {
					mailed = append(mailed, r.PathValue("id"))
					w.WriteHeader(http.StatusNoContent)
				})
				mux.HandleFunc("DELETE /admin/realms/reticora/users/{id}", func(w http.ResponseWriter, r *http.Request) {
					deleted = append(deleted, r.PathValue("id"))
					w.WriteHeader(http.StatusNoContent)
				})
				srv := httptest.NewServer(mux)
				defer srv.Close()
				kc, _ := NewKeycloakAccounts(srv.URL+"/realms/reticora", "signup", "s3cret", srv.Client())
				a := Account{Email: "admin@acme.example", DisplayName: "Ada Admin", OrganizationID: "org-1"}
				var (
					id  string
					err error
				)
				if path == "signup" {
					a.Password = "correct horse battery"
					id, err = kc.CreateAccount(context.Background(), a)
				} else {
					id, err = kc.InviteAccount(context.Background(), a)
				}
				if err == nil || id != "" {
					t.Fatalf("account without MFA role accepted: id %q, err %v", id, err)
				}
				if !slices.Equal(deleted, []string{"adm-1"}) {
					t.Errorf("deleted = %v, want the created account adm-1", deleted)
				}
				if len(mailed) != 0 {
					t.Errorf("invitation mail sent for an account without MFA: %v", mailed)
				}
				if mapped != c.mapAttempts {
					t.Errorf("role mapping requested %d times, want %d", mapped, c.mapAttempts)
				}
			})
		}
	}
}

// TestKeycloakInvitedAccountWithoutMFARole (WP-357): the account of an
// accepted invitation (CreateInvitedAccount) gets no realm role and no
// CONFIGURE_TOTP; whether it needs MFA follows from the invited role
// (WP-358).
func TestKeycloakInvitedAccountWithoutMFARole(t *testing.T) {
	var (
		user    keycloakUser
		lookups int
		mapped  int
	)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /realms/reticora/protocol/openid-connect/token", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "admin-token"})
	})
	mux.HandleFunc("POST /admin/realms/reticora/users", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&user)
		w.Header().Set("Location", "http://"+r.Host+"/admin/realms/reticora/users/inv-2")
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("GET /admin/realms/reticora/users/{id}/role-mappings/realm/available", func(w http.ResponseWriter, _ *http.Request) {
		lookups++
		_ = json.NewEncoder(w).Encode([]fakeRole{{ID: "role-mfa", Name: "mfa_required"}})
	})
	mux.HandleFunc("POST /admin/realms/reticora/users/{id}/role-mappings/realm", func(w http.ResponseWriter, _ *http.Request) {
		mapped++
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	kc, _ := NewKeycloakAccounts(srv.URL+"/realms/reticora", "signup", "s3cret", srv.Client())
	id, err := kc.CreateInvitedAccount(context.Background(), "viewer@acme.example", "Vera Viewer", "correct horse battery", "org-1")
	if err != nil || id != "inv-2" {
		t.Fatalf("CreateInvitedAccount = %q, %v", id, err)
	}
	if lookups != 0 || mapped != 0 {
		t.Errorf("invited account: %d role lookups, %d role mappings; want none", lookups, mapped)
	}
	if len(user.RequiredActions) != 0 {
		t.Errorf("invited account required actions = %v, want none", user.RequiredActions)
	}
	if len(user.Credentials) != 1 || user.Credentials[0].Value != "correct horse battery" || !user.EmailVerified {
		t.Errorf("invited user = %+v", user)
	}
}
