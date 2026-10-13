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
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
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
	Enabled         bool                 `json:"enabled"`
	EmailVerified   bool                 `json:"emailVerified"`
	Attributes      map[string][]string  `json:"attributes"`
	Credentials     []keycloakCredential `json:"credentials,omitempty"`
	RequiredActions []string             `json:"requiredActions,omitempty"`
}

// inviteActions are the actions the invited first org_admin completes.
var inviteActions = []string{"UPDATE_PASSWORD", "VERIFY_EMAIL"}

// CreateAccount creates the user with verified address, the organization_id
// attribute the login reads, and the password as permanent credential.
func (k *KeycloakAccounts) CreateAccount(ctx context.Context, a Account) (string, error) {
	return k.createUser(ctx, &keycloakUser{
		Username: a.Email, Email: a.Email, FirstName: a.DisplayName, Enabled: true, EmailVerified: true,
		Attributes:  map[string][]string{"organization_id": {a.OrganizationID}},
		Credentials: []keycloakCredential{{Type: "password", Value: a.Password}},
	})
}

// CreateInvitedAccount creates the account of an accepted invitation
// (user.InvitationAccounts, TLC-03): like a signup, with the password the
// invited person chose; an existing account is a conflict on email.
func (k *KeycloakAccounts) CreateInvitedAccount(ctx context.Context, email, displayName, password, orgID string) (string, error) {
	id, err := k.CreateAccount(ctx, Account{Email: email, DisplayName: displayName, Password: password, OrganizationID: orgID})
	if errors.Is(err, ErrAccountExists) {
		return "", &api.ConflictError{Field: "email", Message: err.Error()}
	}
	return id, err
}

// InviteAccount creates the user without a password and lets Keycloak mail
// the link to set one and verify the address (execute-actions-email; the
// realm's SMTP settings apply). Used by the operator path (TLC-02).
func (k *KeycloakAccounts) InviteAccount(ctx context.Context, a Account) (string, error) {
	id, err := k.createUser(ctx, &keycloakUser{
		Username: a.Email, Email: a.Email, FirstName: a.DisplayName, Enabled: true,
		Attributes:      map[string][]string{"organization_id": {a.OrganizationID}},
		RequiredActions: inviteActions,
	})
	if err != nil {
		return "", err
	}
	if err = k.executeActionsEmail(ctx, id); err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		if derr := k.DeleteAccount(cleanup, id); derr != nil {
			err = errors.Join(err, derr)
		}
		return "", err
	}
	return id, nil
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
