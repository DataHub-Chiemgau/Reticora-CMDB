package signup

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
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

	// A failing invitation mail removes the account again.
	inviteStatus = http.StatusInternalServerError
	if _, err := kc.InviteAccount(context.Background(), Account{Email: "b@acme.example"}); err == nil {
		t.Fatal("failed invitation accepted")
	}
	if deleted != "inv-1" {
		t.Errorf("deleted = %q", deleted)
	}
}
