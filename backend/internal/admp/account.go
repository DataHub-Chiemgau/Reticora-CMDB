// Package admp holds the read-only connection to ADMP, the leading external
// system of the customer for connections and services (ADM-01 ff., E-222,
// E-223, E-224). This file holds the accounts (ADM-01, WP-925): local
// configuration of where and as whom Reticora reads ADMP, never a call to
// ADMP. The login is a credential of kind api_token (stored, or vault_ref
// of a connection resolved on the server, E-220); an account stores no
// secret and no answer, log or audit entry names one.
package admp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Defaults and bounds of an account (admp_account checks).
const (
	DefaultRateLimitRPS = 2.0
	MaxRateLimitRPS     = 50.0
	DefaultTimeoutMS    = 10000
	MinTimeoutMS        = 1000
	MaxTimeoutMS        = 60000
	maxNameLength       = 200
)

// ErrorCredentialMissing is the last_error_class of an account whose
// credential was deleted.
const ErrorCredentialMissing = "credential_missing"

// Errors of the account store.
var (
	ErrAccountNotFound = errors.New("admp account not found")
	// ErrNoAccount: neither the client nor the organization has an enabled
	// account (AccountFor).
	ErrNoAccount = errors.New("no enabled admp account")
	// ErrOutsideScope: a principal restricted to clients writes an
	// account outside them or an organization-wide one (E-09; 403).
	ErrOutsideScope = errors.New("the account is outside the caller's client scope")
)

// CredentialRef names the login of an account: id, name and source of the
// credential, never a value of it.
type CredentialRef struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Source string `json:"source"`
}

// Account is an ADMP account of the organization (ClientID empty) or of a
// client. LastOKAt and LastErrorClass are written by the refresh (WP-929).
type Account struct {
	ID             string         `json:"id"`
	OrganizationID string         `json:"organization_id"`
	ClientID       string         `json:"client_id,omitempty"`
	Name           string         `json:"name"`
	BaseURL        string         `json:"base_url"`
	AuthURL        string         `json:"auth_url,omitempty"`
	CredentialID   string         `json:"credential_id,omitempty"`
	Credential     *CredentialRef `json:"credential,omitempty"`
	RateLimitRPS   float64        `json:"rate_limit_rps"`
	TimeoutMS      int            `json:"timeout_ms"`
	Enabled        bool           `json:"enabled"`
	LastOKAt       *time.Time     `json:"last_ok_at"`
	LastErrorClass *string        `json:"last_error_class"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

// AccountRepository stores the accounts in the caller's scope;
// PGAccountRepository and MemoryAccountRepository implement it.
type AccountRepository interface {
	CreateAccount(ctx context.Context, a *Account) error
	ListAccounts(ctx context.Context, orgID string) ([]Account, error)
	GetAccount(ctx context.Context, orgID, id string) (*Account, error)
	// UpdateAccount stores every member of a but the status fields.
	UpdateAccount(ctx context.Context, a *Account) error
	DeleteAccount(ctx context.Context, orgID, id string) error
}

// Credentials is what the accounts read of the credential service: a
// credential visible to the caller and the place a vault connection is
// resolved at.
type Credentials interface {
	Get(ctx context.Context, orgID, id string) (*credential.Credential, error)
	List(ctx context.Context, orgID string) ([]credential.Credential, error)
	VaultResolveAt(ctx context.Context, orgID, connectionID string) (string, error)
}

// credentialService adapts *credential.Service to Credentials.
type credentialService struct{ svc *credential.Service }

func (c credentialService) Get(ctx context.Context, orgID, id string) (*credential.Credential, error) {
	return c.svc.Get(ctx, orgID, id)
}

func (c credentialService) List(ctx context.Context, orgID string) ([]credential.Credential, error) {
	return c.svc.List(ctx, orgID)
}

func (c credentialService) VaultResolveAt(ctx context.Context, orgID, connectionID string) (string, error) {
	conn, err := c.svc.Vaults().Get(ctx, orgID, connectionID)
	if err != nil {
		return "", err
	}
	return conn.ResolveAt, nil
}

// CredentialsOf adapts the credential service for the accounts.
func CredentialsOf(svc *credential.Service) Credentials { return credentialService{svc: svc} }

// AccountService validates and stores accounts.
type AccountService struct {
	repo   AccountRepository
	creds  Credentials
	egress egress.Options
}

// NewAccountService creates the service; egress is the destination policy
// of the router (opts.Egress, SEC-08).
func NewAccountService(repo AccountRepository, creds Credentials, policy egress.Options) *AccountService {
	return &AccountService{repo: repo, creds: creds, egress: policy}
}

func fieldError(field, message string) error {
	return &api.FieldError{Field: field, Status: http.StatusUnprocessableEntity, Message: message}
}

// checkURL requires an absolute https URL with a host, without user
// information, query and fragment, whose destination the egress policy
// allows (AK4).
func (s *AccountService) checkURL(field, raw string) error {
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Scheme != "https" || u.Hostname() == "" || u.Opaque != "":
		return fieldError(field, field+" must be an absolute https URL with a host")
	case u.User != nil:
		return fieldError(field, field+" must not carry user information")
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		return fieldError(field, field+" must not carry a query or fragment")
	}
	if err = s.egress.ValidateURL(raw); err != nil {
		return fieldError(field, field+" names a private or internal destination")
	}
	return nil
}

// inScope reports whether the principal of ctx may write an account of
// clientID (E-09: an organization-wide account only an org-wide principal).
func inScope(ctx context.Context, clientID string) bool {
	scope, ok := database.TenantScopeFromContext(ctx)
	if !ok || scope.Clients.IsAll() {
		return true
	}
	return clientID != "" && slices.Contains(scope.Clients.IDs(), clientID)
}

// validate checks an account completely (create and the merged PATCH).
func (s *AccountService) validate(ctx context.Context, orgID string, a *Account) error {
	a.Name = strings.TrimSpace(a.Name)
	a.BaseURL = strings.TrimSpace(a.BaseURL)
	a.AuthURL = strings.TrimSpace(a.AuthURL)
	switch {
	case a.Name == "" || len(a.Name) > maxNameLength:
		return fieldError("name", fmt.Sprintf("name is required, at most %d characters", maxNameLength))
	case a.ClientID != "" && !api.IsUUID(a.ClientID):
		return fieldError("client_id", "client_id must be a UUID")
	case math.IsNaN(a.RateLimitRPS) || a.RateLimitRPS <= 0 || a.RateLimitRPS > MaxRateLimitRPS:
		return fieldError("rate_limit_rps", "rate_limit_rps must be greater than 0 and at most 50")
	case a.TimeoutMS < MinTimeoutMS || a.TimeoutMS > MaxTimeoutMS:
		return fieldError("timeout_ms", "timeout_ms must be from 1000 to 60000")
	}
	if err := s.checkURL("base_url", a.BaseURL); err != nil {
		return err
	}
	if a.AuthURL != "" {
		if err := s.checkURL("auth_url", a.AuthURL); err != nil {
			return err
		}
	}
	if !inScope(ctx, a.ClientID) {
		return ErrOutsideScope
	}
	return s.checkCredential(ctx, orgID, a)
}

// checkCredential applies the rules of AK3: a credential of the
// organization of kind api_token, of the account's client or org-wide;
// a vault_ref only of a connection resolved on the server (E-220).
func (s *AccountService) checkCredential(ctx context.Context, orgID string, a *Account) error {
	if a.CredentialID == "" {
		return nil
	}
	if s.creds == nil {
		return fieldError("credential_id", "credentials are not available")
	}
	const message = "credential_id must name a credential of kind api_token of the account's client or org-wide"
	if !api.IsUUID(a.CredentialID) {
		return fieldError("credential_id", message)
	}
	cred, err := s.creds.Get(ctx, orgID, a.CredentialID)
	if errors.Is(err, credential.ErrNotFound) {
		return fieldError("credential_id", message)
	}
	if err != nil {
		return err
	}
	if cred.Kind != credential.KindAPIToken || (cred.ClientID != "" && cred.ClientID != a.ClientID) {
		return fieldError("credential_id", message)
	}
	if cred.Source == credential.SourceVaultRef {
		if cred.VaultRef == nil {
			return fieldError("credential_id", "the vault reference of the credential is incomplete")
		}
		at, rerr := s.creds.VaultResolveAt(ctx, orgID, cred.VaultRef.ConnectionID)
		if rerr != nil && !errors.Is(rerr, credential.ErrVaultConnectionNotFound) {
			return rerr
		}
		if at != credential.ResolveAtServer {
			return fieldError("credential_id", "a vault reference of an ADMP account needs a vault connection with resolve_at server")
		}
	}
	return nil
}

// Create stores an account.
func (s *AccountService) Create(ctx context.Context, orgID string, a *Account) (*Account, error) {
	a.OrganizationID = orgID
	if err := s.validate(ctx, orgID, a); err != nil {
		return nil, err
	}
	if err := s.repo.CreateAccount(ctx, a); err != nil {
		return nil, err
	}
	return s.withCredential(ctx, orgID, a), nil
}

// credentialsUnchecked marks a store that keeps the credential reference
// without foreign key (the memory store of --no-db): the service notices a
// deleted credential itself.
type credentialsUnchecked interface{ UncheckedCredentials() }

// missingCredential applies the rule of the foreign key to an account of a
// store without one (AK3, AK7): a credential that no longer exists leaves
// the account without credential_id and with last_error_class
// credential_missing, as ON DELETE SET NULL and the trigger make it on
// PostgreSQL. A failed lookup changes nothing.
func (s *AccountService) missingCredential(ctx context.Context, orgID string, list []Account) {
	if _, unchecked := s.repo.(credentialsUnchecked); !unchecked || s.creds == nil {
		return
	}
	for i := range list {
		a := &list[i]
		if a.CredentialID == "" {
			continue
		}
		if _, err := s.creds.Get(ctx, orgID, a.CredentialID); errors.Is(err, credential.ErrNotFound) {
			missing := ErrorCredentialMissing
			a.CredentialID, a.LastErrorClass = "", &missing
		}
	}
}

// getAccount reads an account with the rule of missingCredential.
func (s *AccountService) getAccount(ctx context.Context, orgID, id string) (*Account, error) {
	a, err := s.repo.GetAccount(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	one := []Account{*a}
	s.missingCredential(ctx, orgID, one)
	return &one[0], nil
}

// List returns the accounts of the caller's scope with their credential
// references.
func (s *AccountService) List(ctx context.Context, orgID string) ([]Account, error) {
	list, err := s.repo.ListAccounts(ctx, orgID)
	if err != nil {
		return nil, err
	}
	s.missingCredential(ctx, orgID, list)
	refs := map[string]*CredentialRef{}
	if s.creds == nil {
		return list, nil
	}
	if creds, cerr := s.creds.List(ctx, orgID); cerr == nil {
		for i := range creds {
			refs[creds[i].ID] = &CredentialRef{ID: creds[i].ID, Name: creds[i].Name, Source: creds[i].Source}
		}
	}
	for i := range list {
		list[i].Credential = refs[list[i].CredentialID]
	}
	return list, nil
}

// Get returns one account.
func (s *AccountService) Get(ctx context.Context, orgID, id string) (*Account, error) {
	a, err := s.getAccount(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	return s.withCredential(ctx, orgID, a), nil
}

// Update applies a PATCH: change merges the sent members into the stored
// account, which is then validated as a whole.
func (s *AccountService) Update(ctx context.Context, orgID, id string, change func(a *Account) error) (*Account, error) {
	current, err := s.getAccount(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if !inScope(ctx, current.ClientID) {
		return nil, ErrOutsideScope
	}
	next := *current
	if changeErr := change(&next); changeErr != nil {
		return nil, changeErr
	}
	if validErr := s.validate(ctx, orgID, &next); validErr != nil {
		return nil, validErr
	}
	if updateErr := s.repo.UpdateAccount(ctx, &next); updateErr != nil {
		return nil, updateErr
	}
	return s.withCredential(ctx, orgID, &next), nil
}

// Delete removes an account and with it its cache (WP-928).
func (s *AccountService) Delete(ctx context.Context, orgID, id string) error {
	current, err := s.repo.GetAccount(ctx, orgID, id)
	if err != nil {
		return err
	}
	if !inScope(ctx, current.ClientID) {
		return ErrOutsideScope
	}
	return s.repo.DeleteAccount(ctx, orgID, id)
}

// AccountFor returns the account the data of a client is read with: the
// enabled account of the client, else the enabled organization-wide one,
// else ErrNoAccount (AK2). It reads in the organization-wide scope: a
// principal restricted to the client does not see the organization-wide
// account (AK6), but its client's data is read with it. The account is for
// the ADMP client (WP-926), never an answer to that principal.
func (s *AccountService) AccountFor(ctx context.Context, orgID, clientID string) (*Account, error) {
	scope := database.OrgWideScope(orgID, tenant.FromContext(ctx).UserID)
	ctx = database.ContextWithTenantScope(database.WithoutTx(ctx), &scope)
	list, err := s.repo.ListAccounts(ctx, orgID)
	if err != nil {
		return nil, err
	}
	s.missingCredential(ctx, orgID, list)
	var org *Account
	for i := range list {
		a := &list[i]
		if !a.Enabled {
			continue
		}
		if clientID != "" && a.ClientID == clientID {
			return a, nil
		}
		if a.ClientID == "" && org == nil {
			org = a
		}
	}
	if org == nil {
		return nil, ErrNoAccount
	}
	return org, nil
}

// withCredential adds the reference of the account's credential when the
// caller sees it.
func (s *AccountService) withCredential(ctx context.Context, orgID string, a *Account) *Account {
	a.Credential = nil
	if a.CredentialID == "" || s.creds == nil {
		return a
	}
	if cred, err := s.creds.Get(ctx, orgID, a.CredentialID); err == nil {
		a.Credential = &CredentialRef{ID: cred.ID, Name: cred.Name, Source: cred.Source}
	}
	return a
}

// --- HTTP -------------------------------------------------------------------

// Handler serves /api/v1/admp/accounts (admp:manage through routeRules;
// feature and module admp).
type Handler struct {
	svc *AccountService
}

// NewHandler creates the account handler.
func NewHandler(svc *AccountService) *Handler { return &Handler{svc: svc} }

// RegisterRoutes mounts the account routes. They configure Reticora and
// never reach ADMP (TestADMPPathsAreReadOnly exempts them).
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/api/v1/admp/accounts", func(r chi.Router) {
		r.Post("/", h.create)
		r.Get("/", h.list)
		r.Get("/{id}", h.get)
		r.Patch("/{id}", h.update)
		r.Delete("/{id}", h.delete)
	})
}

// accountRequest is the body of POST /admp/accounts.
type accountRequest struct {
	ClientID     string   `json:"client_id"`
	Name         string   `json:"name"`
	BaseURL      string   `json:"base_url"`
	AuthURL      string   `json:"auth_url"`
	CredentialID string   `json:"credential_id"`
	RateLimitRPS *float64 `json:"rate_limit_rps"`
	TimeoutMS    *int     `json:"timeout_ms"`
	Enabled      *bool    `json:"enabled"`
}

func organizationID(w http.ResponseWriter, r *http.Request) (string, bool) {
	orgID := tenant.FromContext(r.Context()).OrganizationID
	if orgID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return "", false
	}
	return orgID, true
}

// writeAccountError maps the errors of the account service.
func writeAccountError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrAccountNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "admp account not found")
	case errors.Is(err, ErrOutsideScope):
		api.WriteError(w, http.StatusForbidden, "Forbidden", "the account is outside the caller's client scope")
	default:
		// Field and conflict errors carry their field (422, 409); a write
		// the row level security refuses 403; anything else 500 (API-03).
		api.WriteRepoError(w, err)
	}
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}
	var req accountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid request body")
		return
	}
	a := Account{ClientID: req.ClientID, Name: req.Name, BaseURL: req.BaseURL, AuthURL: req.AuthURL,
		CredentialID: req.CredentialID, RateLimitRPS: DefaultRateLimitRPS, TimeoutMS: DefaultTimeoutMS,
		Enabled: req.Enabled == nil || *req.Enabled}
	if req.RateLimitRPS != nil {
		a.RateLimitRPS = *req.RateLimitRPS
	}
	if req.TimeoutMS != nil {
		a.TimeoutMS = *req.TimeoutMS
	}
	created, err := h.svc.Create(r.Context(), orgID, &a)
	if err != nil {
		writeAccountError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, created)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	list, err := h.svc.List(r.Context(), orgID)
	if err != nil {
		writeAccountError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.Page(list, page))
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}
	a, err := h.svc.Get(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		writeAccountError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, a)
}

// accountPatch decodes one member of a PATCH into the account.
func accountPatch(a *Account, field string, raw json.RawMessage) error {
	null := string(raw) == "null"
	text := func(dst *string, nullable bool) error {
		if nullable && null {
			*dst = ""
			return nil
		}
		if err := json.Unmarshal(raw, dst); err != nil || null {
			return fieldError(field, field+" must be a string")
		}
		return nil
	}
	switch field {
	case "name":
		return text(&a.Name, false)
	case "base_url":
		return text(&a.BaseURL, false)
	case "auth_url":
		return text(&a.AuthURL, true)
	case "credential_id":
		return text(&a.CredentialID, true)
	case "rate_limit_rps":
		if err := json.Unmarshal(raw, &a.RateLimitRPS); err != nil || null {
			return fieldError(field, "rate_limit_rps must be a number")
		}
		return nil
	case "timeout_ms":
		if err := json.Unmarshal(raw, &a.TimeoutMS); err != nil || null {
			return fieldError(field, "timeout_ms must be an integer")
		}
		return nil
	case "enabled":
		if err := json.Unmarshal(raw, &a.Enabled); err != nil || null {
			return fieldError(field, "enabled must be a boolean")
		}
		return nil
	case "client_id":
		return fieldError(field, "client_id cannot be changed; create an account for the other client")
	case "last_ok_at", "last_error_class":
		return fieldError(field, field+" is written by the refresh only")
	}
	return fieldError(field, "unknown field")
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid request body")
		return
	}
	a, err := h.svc.Update(r.Context(), orgID, chi.URLParam(r, "id"), func(a *Account) error {
		fields := make([]string, 0, len(body))
		for field := range body {
			fields = append(fields, field)
		}
		slices.Sort(fields)
		for _, field := range fields {
			if patchErr := accountPatch(a, field, body[field]); patchErr != nil {
				return patchErr
			}
		}
		return nil
	})
	if err != nil {
		writeAccountError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, a)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}
	if err := h.svc.Delete(r.Context(), orgID, chi.URLParam(r, "id")); err != nil {
		writeAccountError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
