package signup

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
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

// fakeMFAAdmin is a Keycloak admin API that keeps the realm role mappings
// of its users: the available roles exclude the effective ones, and the
// effective (composite) roles contain the mapped roles and those of the
// user's groups.
type fakeMFAAdmin struct {
	mu        sync.Mutex
	created   map[string]keycloakUser
	mapped    map[string][]string // user id -> directly mapped realm roles
	group     map[string][]string // user id -> realm roles through groups
	deleted   []string
	mappings  int
	mapStatus int
	users     []map[string]string // answer of GET /users
	query     string
}

func (f *fakeMFAAdmin) server(t *testing.T) *httptest.Server {
	t.Helper()
	f.created, f.mapped = map[string]keycloakUser{}, map[string][]string{}
	if f.group == nil {
		f.group = map[string][]string{}
	}
	roles := []fakeRole{{ID: "role-viewer", Name: "viewer"}, {ID: "role-mfa", Name: "mfa_required"}}
	effective := func(id string) []string { return append(slices.Clone(f.mapped[id]), f.group[id]...) }
	mux := http.NewServeMux()
	mux.HandleFunc("POST /realms/reticora/protocol/openid-connect/token", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "admin-token"})
	})
	mux.HandleFunc("POST /admin/realms/reticora/users", func(w http.ResponseWriter, r *http.Request) {
		var u keycloakUser
		_ = json.NewDecoder(r.Body).Decode(&u)
		f.mu.Lock()
		id := "kc-" + strconv.Itoa(len(f.created)+1)
		f.created[id] = u
		f.mu.Unlock()
		w.Header().Set("Location", "http://"+r.Host+"/admin/realms/reticora/users/"+id)
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("GET /admin/realms/reticora/users", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.query = r.URL.RawQuery
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(f.users)
	})
	mux.HandleFunc("GET /admin/realms/reticora/users/{id}/role-mappings/realm/composite", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []fakeRole{}
		for _, name := range effective(r.PathValue("id")) {
			out = append(out, fakeRole{ID: "role-" + name, Name: name})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("GET /admin/realms/reticora/users/{id}/role-mappings/realm/available", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []fakeRole{}
		for _, role := range roles {
			if !slices.Contains(effective(r.PathValue("id")), role.Name) {
				out = append(out, role)
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("POST /admin/realms/reticora/users/{id}/role-mappings/realm", func(w http.ResponseWriter, r *http.Request) {
		var body []fakeRole
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.mappings++
		if f.mapStatus != 0 {
			w.WriteHeader(f.mapStatus)
			return
		}
		for _, role := range body {
			if role.ID == "" {
				t.Errorf("role mapping %+v without id", role)
			}
			f.mapped[r.PathValue("id")] = append(f.mapped[r.PathValue("id")], role.Name)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /admin/realms/reticora/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.deleted = append(f.deleted, r.PathValue("id"))
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestKeycloakInvitedAccountMFAFollowsRole (WP-357, WP-358, AUT-09): the
// account of an accepted invitation (CreateInvitedAccount) is bound to MFA
// exactly when the invited role is org_admin: realm role mfa_required and
// CONFIGURE_TOTP like a signup account. Other roles get neither. A failing
// mapping deletes the account and reports identity.ErrMFAUnavailable.
func TestKeycloakInvitedAccountMFAFollowsRole(t *testing.T) {
	for _, c := range []struct {
		role    string
		mfa     bool
		actions []string
	}{
		{"viewer", false, nil},
		{"engineer", false, nil},
		{"org_admin", true, []string{"CONFIGURE_TOTP"}},
	} {
		t.Run(c.role, func(t *testing.T) {
			f := &fakeMFAAdmin{}
			srv := f.server(t)
			kc, _ := NewKeycloakAccounts(srv.URL+"/realms/reticora", "signup", "s3cret", srv.Client())
			id, err := kc.CreateInvitedAccount(context.Background(), "invitee@acme.example", "Ina Invitee", "correct horse battery", "org-1", c.role)
			if err != nil || id != "kc-1" {
				t.Fatalf("CreateInvitedAccount = %q, %v", id, err)
			}
			if got := slices.Contains(f.mapped[id], "mfa_required"); got != c.mfa {
				t.Errorf("realm roles %v; mfa_required mapped = %v, want %v", f.mapped[id], got, c.mfa)
			}
			if !slices.Equal(f.created[id].RequiredActions, c.actions) {
				t.Errorf("required actions = %v, want %v", f.created[id].RequiredActions, c.actions)
			}
			if u := f.created[id]; len(u.Credentials) != 1 || u.Credentials[0].Value != "correct horse battery" || !u.EmailVerified {
				t.Errorf("invited user = %+v", u)
			}
			if len(f.deleted) != 0 {
				t.Errorf("deleted %v", f.deleted)
			}
		})
	}
	t.Run("org_admin, mapping refused", func(t *testing.T) {
		f := &fakeMFAAdmin{mapStatus: http.StatusForbidden}
		srv := f.server(t)
		kc, _ := NewKeycloakAccounts(srv.URL+"/realms/reticora", "signup", "s3cret", srv.Client())
		id, err := kc.CreateInvitedAccount(context.Background(), "invitee@acme.example", "Ina Invitee", "correct horse battery", "org-1", "org_admin")
		if id != "" || !errors.Is(err, identity.ErrMFAUnavailable) {
			t.Fatalf("CreateInvitedAccount = %q, %v; want identity.ErrMFAUnavailable", id, err)
		}
		if !slices.Equal(f.deleted, []string{"kc-1"}) {
			t.Errorf("deleted = %v, want the created account", f.deleted)
		}
	})
}

// TestKeycloakRequireMFAIsIdempotent (WP-358): RequireMFA binds an existing
// account (role assignment, first login, reconciliation): the first call
// maps mfa_required, a second call finds it among the effective roles and
// maps nothing. An account holding it through a group (reticora-admin) is
// not mapped again either. A refused lookup or mapping reports
// identity.ErrMFAUnavailable.
func TestKeycloakRequireMFAIsIdempotent(t *testing.T) {
	f := &fakeMFAAdmin{group: map[string][]string{"grouped": {"mfa_required"}}}
	srv := f.server(t)
	kc, _ := NewKeycloakAccounts(srv.URL+"/realms/reticora", "signup", "s3cret", srv.Client())
	ctx := context.Background()
	if err := kc.RequireMFA(ctx, "promoted"); err != nil {
		t.Fatalf("first RequireMFA: %v", err)
	}
	if !slices.Equal(f.mapped["promoted"], []string{"mfa_required"}) || f.mappings != 1 {
		t.Fatalf("after the first call: roles %v, %d mappings; want [mfa_required], 1", f.mapped["promoted"], f.mappings)
	}
	if err := kc.RequireMFA(ctx, "promoted"); err != nil {
		t.Fatalf("second RequireMFA: %v", err)
	}
	if !slices.Equal(f.mapped["promoted"], []string{"mfa_required"}) || f.mappings != 1 {
		t.Errorf("after the second call: roles %v, %d mappings; want no new mapping", f.mapped["promoted"], f.mappings)
	}
	if err := kc.RequireMFA(ctx, "grouped"); err != nil || len(f.mapped["grouped"]) != 0 || f.mappings != 1 {
		t.Errorf("group member: err %v, mapped %v, %d mappings; want nothing mapped", err, f.mapped["grouped"], f.mappings)
	}

	refused := &fakeMFAAdmin{mapStatus: http.StatusForbidden}
	rsrv := refused.server(t)
	rkc, _ := NewKeycloakAccounts(rsrv.URL+"/realms/reticora", "signup", "s3cret", rsrv.Client())
	if err := rkc.RequireMFA(ctx, "promoted"); !errors.Is(err, identity.ErrMFAUnavailable) {
		t.Errorf("refused mapping: %v, want identity.ErrMFAUnavailable", err)
	}
	unreachable, _ := NewKeycloakAccounts("http://127.0.0.1:1/realms/reticora", "signup", "s3cret", nil)
	if err := unreachable.RequireMFA(ctx, "promoted"); !errors.Is(err, identity.ErrMFAUnavailable) {
		t.Errorf("unreachable admin API: %v, want identity.ErrMFAUnavailable", err)
	}
}

// TestKeycloakAccountByEmail (WP-358): the account of a user without oidc
// subject is found by its exact address; none is "", several are an error.
func TestKeycloakAccountByEmail(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name    string
		users   []map[string]string
		want    string
		wantErr bool
	}{
		{"found", []map[string]string{{"id": "kc-7", "email": "ada@acme.example"}}, "kc-7", false},
		{"found, other case", []map[string]string{{"id": "kc-7", "email": "Ada@Acme.example"}}, "kc-7", false},
		{"none", []map[string]string{}, "", false},
		{"other address only", []map[string]string{{"id": "kc-8", "email": "ada.lovelace@acme.example"}}, "", false},
		{"two accounts", []map[string]string{{"id": "kc-7", "email": "ada@acme.example"}, {"id": "kc-9", "email": "ada@acme.example"}}, "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeMFAAdmin{users: c.users}
			srv := f.server(t)
			kc, _ := NewKeycloakAccounts(srv.URL+"/realms/reticora", "signup", "s3cret", srv.Client())
			id, err := kc.AccountByEmail(ctx, "ada@acme.example")
			if id != c.want || (err != nil) != c.wantErr {
				t.Fatalf("AccountByEmail = %q, %v; want %q, error %v", id, err, c.want, c.wantErr)
			}
			if c.wantErr && !errors.Is(err, identity.ErrMFAUnavailable) {
				t.Errorf("error %v does not wrap identity.ErrMFAUnavailable", err)
			}
			q, _ := url.ParseQuery(f.query)
			if q.Get("email") != "ada@acme.example" || q.Get("exact") != "true" {
				t.Errorf("query %q, want email and exact=true", f.query)
			}
		})
	}
}
