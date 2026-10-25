package signup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// KeycloakAccounts creates and deletes realm users through the Keycloak
// admin REST API. It authenticates with the client-credentials grant of a
// confidential client whose service account holds the realm-management role
// manage-users.
type KeycloakAccounts struct {
	issuer       string // https://host/realms/<realm>
	adminRealm   string // https://host/admin/realms/<realm>
	clientID     string
	clientSecret string
	client       *http.Client
}

// NewKeycloakAccounts derives the admin endpoint from the realm's issuer
// URL (…/realms/<realm>). client may be nil for http.DefaultClient with a
// timeout.
func NewKeycloakAccounts(issuerURL, clientID, clientSecret string, client *http.Client) (*KeycloakAccounts, error) {
	issuer := strings.TrimRight(issuerURL, "/")
	base, realm, ok := strings.Cut(issuer, "/realms/")
	if !ok || realm == "" || strings.Contains(realm, "/") {
		return nil, fmt.Errorf("keycloak: issuer %q is not of the form <base>/realms/<realm>", issuerURL)
	}
	if clientID == "" || clientSecret == "" {
		return nil, errors.New("keycloak: admin client id and secret are required")
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &KeycloakAccounts{issuer: issuer, adminRealm: base + "/admin/realms/" + realm,
		clientID: clientID, clientSecret: clientSecret, client: client}, nil
}

type keycloakCredential struct {
	Type      string `json:"type"`
	Value     string `json:"value"`
	Temporary bool   `json:"temporary"`
}

type keycloakUser struct {
	Username        string               `json:"username"`
	Email           string               `json:"email"`
	FirstName       string               `json:"firstName,omitempty"`
	LastName        string               `json:"lastName,omitempty"`
	Enabled         bool                 `json:"enabled"`
	EmailVerified   bool                 `json:"emailVerified"`
	Attributes      map[string][]string  `json:"attributes"`
	Credentials     []keycloakCredential `json:"credentials,omitempty"`
	RequiredActions []string             `json:"requiredActions,omitempty"`
}

// keycloakRole is a realm role as the admin API lists it.
type keycloakRole struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// splitName splits a display name into first and last name: the realm's
// user profile requires both, and an account without them is sent to the
// profile form after its first login instead of into the application. A
// single word stays the first name.
func splitName(display string) (first, last string) {
	fields := strings.Fields(display)
	switch len(fields) {
	case 0:
		return "", ""
	case 1:
		return fields[0], ""
	default:
		return strings.Join(fields[:len(fields)-1], " "), fields[len(fields)-1]
	}
}

// inviteActions are the actions the invited first org_admin completes.
var inviteActions = []string{"UPDATE_PASSWORD", "VERIFY_EMAIL"}

// mfaRole is the realm role whose holders the browser flow browser-mfa asks
// for a second factor (deploy/keycloak, AUT-09, CH26); configureTOTP makes
// them set up OTP at their first login.
const (
	mfaRole       = "mfa_required"
	configureTOTP = "CONFIGURE_TOTP"
)

// orgAdminRole is the app role whose accounts are bound to MFA (AUT-09).
const orgAdminRole = "org_admin"

// CreateAccount creates the account of the first org_admin of a self-signup:
// verified address, the organization_id attribute the login reads, the
// password as permanent credential, and the MFA binding of AUT-09 (realm
// role mfa_required, required action CONFIGURE_TOTP). Without the role the
// account is deleted again: no org_admin without MFA.
func (k *KeycloakAccounts) CreateAccount(ctx context.Context, a Account) (string, error) {
	return k.createAccount(ctx, a, true)
}

// CreateInvitedAccount creates the account of an accepted invitation
// (user.InvitationAccounts, TLC-03): like a signup, with the password the
// invited person chose; an existing account is a conflict on email. The
// invited role decides the MFA binding (WP-358): an org_admin account gets
// mfa_required and CONFIGURE_TOTP like a signup account and is deleted again
// when the role cannot be mapped; other roles get neither.
func (k *KeycloakAccounts) CreateInvitedAccount(ctx context.Context, email, displayName, password, orgID, role string) (string, error) {
	id, err := k.createAccount(ctx, Account{Email: email, DisplayName: displayName, Password: password, OrganizationID: orgID}, role == orgAdminRole)
	if errors.Is(err, ErrAccountExists) {
		return "", &api.ConflictError{Field: "email", Message: ErrAccountExists.Error()}
	}
	return id, err
}

func (k *KeycloakAccounts) createAccount(ctx context.Context, a Account, mfa bool) (string, error) {
	first, last := splitName(a.DisplayName)
	user := &keycloakUser{
		Username: a.Email, Email: a.Email, FirstName: first, LastName: last, Enabled: true, EmailVerified: true,
		Attributes:  map[string][]string{"organization_id": {a.OrganizationID}},
		Credentials: []keycloakCredential{{Type: "password", Value: a.Password}},
	}
	if mfa {
		user.RequiredActions = []string{configureTOTP}
	}
	id, err := k.createUser(ctx, user)
	if err != nil || !mfa {
		return id, err
	}
	if err = k.requireMFA(ctx, id); err != nil {
		return "", k.rollback(ctx, id, err)
	}
	return id, nil
}

// InviteAccount creates the user without a password and lets Keycloak mail
// the link to set one and verify the address (execute-actions-email; the
// realm's SMTP settings apply). Used by the operator path (TLC-02): the
// account is the first org_admin and bound to MFA like a signup account.
func (k *KeycloakAccounts) InviteAccount(ctx context.Context, a Account) (string, error) {
	first, last := splitName(a.DisplayName)
	id, err := k.createUser(ctx, &keycloakUser{
		Username: a.Email, Email: a.Email, FirstName: first, LastName: last, Enabled: true,
		Attributes:      map[string][]string{"organization_id": {a.OrganizationID}},
		RequiredActions: append(slices.Clone(inviteActions), configureTOTP),
	})
	if err != nil {
		return "", err
	}
	if err = k.requireMFA(ctx, id); err != nil {
		return "", k.rollback(ctx, id, err)
	}
	if err = k.executeActionsEmail(ctx, id); err != nil {
		return "", k.rollback(ctx, id, err)
	}
	return id, nil
}

// rollback deletes the half-created account and returns err, joined with a
// failure of the deletion.
func (k *KeycloakAccounts) rollback(ctx context.Context, id string, err error) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if derr := k.DeleteAccount(cleanup, id); derr != nil {
		err = errors.Join(err, derr)
	}
	return err
}

// RequireMFA binds an existing account to the second factor
// (user.MFAEnforcer, WP-358): it maps mfa_required unless the account holds
// it already, directly, through a composite role or through a group
// (reticora-admin). It is idempotent. Errors wrap identity.ErrMFAUnavailable.
func (k *KeycloakAccounts) RequireMFA(ctx context.Context, id string) error {
	held, err := k.holdsMFA(ctx, id)
	if err != nil {
		return err
	}
	if held {
		return nil
	}
	return k.requireMFA(ctx, id)
}

// holdsMFA reports whether the user's effective realm roles contain
// mfa_required.
func (k *KeycloakAccounts) holdsMFA(ctx context.Context, id string) (bool, error) {
	var roles []keycloakRole
	if err := k.getJSON(ctx, "/users/"+url.PathEscape(id)+"/role-mappings/realm/composite", &roles); err != nil {
		return false, fmt.Errorf("%w: keycloak: effective realm roles: %w", identity.ErrMFAUnavailable, err)
	}
	return slices.ContainsFunc(roles, func(r keycloakRole) bool { return r.Name == mfaRole }), nil
}

// AccountByEmail returns the id of the account whose e-mail address is
// exactly email, "" when there is none (user.MFAEnforcer). More than one
// account of the address is an error: the binding must not guess.
func (k *KeycloakAccounts) AccountByEmail(ctx context.Context, email string) (string, error) {
	var users []struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}
	q := url.Values{"email": {email}, "exact": {"true"}, "briefRepresentation": {"true"}}
	if err := k.getJSON(ctx, "/users?"+q.Encode(), &users); err != nil {
		return "", fmt.Errorf("%w: keycloak: find user by e-mail: %w", identity.ErrMFAUnavailable, err)
	}
	var ids []string
	for _, u := range users {
		if u.ID != "" && strings.EqualFold(u.Email, email) {
			ids = append(ids, u.ID)
		}
	}
	switch len(ids) {
	case 0:
		return "", nil
	case 1:
		return ids[0], nil
	default:
		return "", fmt.Errorf("%w: keycloak: %d accounts with the e-mail address", identity.ErrMFAUnavailable, len(ids))
	}
}

// getJSON reads an admin API resource below the realm into v.
func (k *KeycloakAccounts) getJSON(ctx context.Context, resource string, v any) error {
	token, err := k.token(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.adminRealm+resource, http.NoBody)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := k.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v)
}

// requireMFA maps the realm role mfa_required to the user; its errors wrap
// identity.ErrMFAUnavailable.
func (k *KeycloakAccounts) requireMFA(ctx context.Context, id string) error {
	if err := k.mapMFARole(ctx, id); err != nil {
		return fmt.Errorf("%w: %w", identity.ErrMFAUnavailable, err)
	}
	return nil
}

// mapMFARole maps mfa_required. The role is looked up among the roles
// available to the user, which manage-users may read (GET /roles/{name}
// needs view-realm); the mapping needs its id.
func (k *KeycloakAccounts) mapMFARole(ctx context.Context, id string) error {
	token, err := k.token(ctx)
	if err != nil {
		return err
	}
	mappings := k.adminRealm + "/users/" + url.PathEscape(id) + "/role-mappings/realm"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mappings+"/available", http.NoBody)
	if err != nil {
		return fmt.Errorf("keycloak: realm roles request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := k.client.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: realm roles: %w", err)
	}
	var roles []keycloakRole
	derr := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&roles)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("keycloak: realm roles: status %d", resp.StatusCode)
	}
	if derr != nil {
		return fmt.Errorf("keycloak: realm roles: %w", derr)
	}
	i := slices.IndexFunc(roles, func(r keycloakRole) bool { return r.Name == mfaRole })
	if i < 0 || roles[i].ID == "" {
		return fmt.Errorf("keycloak: realm role %s not available", mfaRole)
	}
	body, _ := json.Marshal(roles[i : i+1])
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, mappings, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("keycloak: role mapping request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err = k.client.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: role mapping: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("keycloak: role mapping: status %d", resp.StatusCode)
	}
	return nil
}

func (k *KeycloakAccounts) executeActionsEmail(ctx context.Context, id string) error {
	token, err := k.token(ctx)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(inviteActions)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		k.adminRealm+"/users/"+url.PathEscape(id)+"/execute-actions-email", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("keycloak: invite request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := k.client.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: invite: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("keycloak: invite: status %d", resp.StatusCode)
	}
	return nil
}

// createUser posts a realm user and returns its id.
func (k *KeycloakAccounts) createUser(ctx context.Context, user *keycloakUser) (string, error) {
	token, err := k.token(ctx)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(user)
	if err != nil {
		return "", fmt.Errorf("keycloak: encode user: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, k.adminRealm+"/users", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("keycloak: create user request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := k.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("keycloak: create user: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusConflict:
		return "", ErrAccountExists
	case resp.StatusCode == http.StatusBadRequest:
		// Keycloak rejects passwords that violate the realm's policy with
		// 400 and an error message naming the policy.
		var kcErr struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
			ErrorMessage     string `json:"errorMessage"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&kcErr)
		msg := firstNonEmpty(kcErr.ErrorMessage, kcErr.ErrorDescription, kcErr.Error, "the account was rejected by the identity provider")
		return "", &api.FieldError{Field: "password", Message: msg, Status: http.StatusUnprocessableEntity}
	case resp.StatusCode != http.StatusCreated:
		return "", fmt.Errorf("keycloak: create user: status %d", resp.StatusCode)
	}
	id := path.Base(resp.Header.Get("Location"))
	if id == "" || id == "." || id == "/" {
		return "", errors.New("keycloak: create user: response has no Location")
	}
	return id, nil
}

// DeleteAccount removes the user; an already missing user is no error.
func (k *KeycloakAccounts) DeleteAccount(ctx context.Context, id string) error {
	token, err := k.token(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, k.adminRealm+"/users/"+url.PathEscape(id), http.NoBody)
	if err != nil {
		return fmt.Errorf("keycloak: delete user request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := k.client.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: delete user: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("keycloak: delete user: status %d", resp.StatusCode)
	}
	return nil
}

// token obtains an admin access token (client-credentials grant).
func (k *KeycloakAccounts) token(ctx context.Context) (string, error) {
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {k.clientID}, "client_secret": {k.clientSecret}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, k.issuer+"/protocol/openid-connect/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("keycloak: token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := k.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("keycloak: token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("keycloak: token: status %d", resp.StatusCode)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok); err != nil || tok.AccessToken == "" {
		return "", errors.New("keycloak: token: no access token in response")
	}
	return tok.AccessToken, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
