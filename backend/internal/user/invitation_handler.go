package user

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// minInvitedPasswordLength is checked before the IdP applies its policy.
const minInvitedPasswordLength = 12

// InvitationHandler serves /api/v1/invitations (TLC-03).
type InvitationHandler struct {
	repo     InvitationRepository
	accounts InvitationAccounts
	// quota checks max_users before an acceptance creates the user.
	quota QuotaGuard
	// acceptURL is the page the link of the invitation mail opens
	// (RETICORA_INVITATION_ACCEPT_URL); nil: issue and renew answer 503.
	acceptURL *url.URL
	now       func() time.Time
}

// NewInvitationHandler creates the handler; a nil repository answers 503.
// Issue and renew need WithAcceptURL as well.
func NewInvitationHandler(repo InvitationRepository) *InvitationHandler {
	return &InvitationHandler{repo: repo, now: time.Now}
}

// WithAcceptURL sets the page the link of the invitation mail opens; the
// link adds the token as query parameter. An empty value or one that is not
// an absolute http(s) URL (config.Validate) leaves invitations
// unconfigured: issue and renew answer 503, like the signup without
// verification page.
func (h *InvitationHandler) WithAcceptURL(raw string) *InvitationHandler {
	h.acceptURL = nil
	if u, err := url.Parse(raw); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil {
		h.acceptURL = u
	}
	return h
}

// WithAccounts lets the acceptance create the IdP account with the given
// password (Keycloak admin client).
func (h *InvitationHandler) WithAccounts(a InvitationAccounts) *InvitationHandler {
	h.accounts = a
	return h
}

// WithQuota enforces max_users on the acceptance (ENT-03): at the limit it
// answers 403 entitlement-limit, creates neither user nor IdP account and
// leaves the invitation open.
func (h *InvitationHandler) WithQuota(q QuotaGuard) *InvitationHandler {
	h.quota = q
	return h
}

// RegisterRoutes registers the invitation routes. Issue, list, renew and
// revoke need user:manage, issue and renew within the caller's rights (grant
// ceiling); accept is public, the token is the credential, and it runs
// through the pre-auth limit.
func (h *InvitationHandler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/invitations", h.List)
	r.Post("/api/v1/invitations", h.Issue)
	r.Post("/api/v1/invitations/accept", h.Accept)
	r.Post("/api/v1/invitations/{id}/renew", h.Renew)
	r.Delete("/api/v1/invitations/{id}", h.Revoke)
}

func (h *InvitationHandler) unavailable(w http.ResponseWriter) bool {
	if h.repo == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "invitations need the database")
		return true
	}
	return false
}

// unconfigured refuses issue and renewal without accept URL: the invitee
// would get no usable link (NTF-05).
func (h *InvitationHandler) unconfigured(w http.ResponseWriter) bool {
	if h.unavailable(w) {
		return true
	}
	if h.acceptURL == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "invitations not configured")
		return true
	}
	return false
}

func writeInvitationError(w http.ResponseWriter, r *http.Request, err error) {
	var ceiling *GrantCeilingError
	switch {
	case errors.As(err, &ceiling):
		api.WriteError(w, http.StatusForbidden, "Forbidden", ceiling.Error())
	case api.WriteEntitlementError(w, err):
	case errors.Is(err, ErrInvitationNotFound), errors.Is(err, ErrInvitationInvalid):
		api.WriteError(w, http.StatusNotFound, "Not Found", err.Error())
	case errors.Is(err, ErrInvitationExpired):
		api.WriteError(w, http.StatusGone, "Gone", err.Error())
	case errors.Is(err, ErrInvitationAccepted):
		api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
	case errors.Is(err, ErrInvitationUserExists):
		api.WriteFieldError(w, &api.ConflictError{Field: "email", Message: err.Error()})
	case errors.Is(err, ErrMFAUnavailable):
		// An org_admin account without MFA was deleted again (WP-358); the
		// cause names the admin API and goes to the log only.
		slog.ErrorContext(r.Context(), "invitation: org_admin account without enforceable MFA", "error", err)
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", ErrMFAUnavailable.Error())
	case api.WriteFieldError(w, err):
	default:
		slog.ErrorContext(r.Context(), "invitation failed", "error", err)
		api.WriteRepoError(w, err)
	}
}

// List returns the organization's invitations with their status.
func (h *InvitationHandler) List(w http.ResponseWriter, r *http.Request) {
	if h.unavailable(w) {
		return
	}
	items, err := h.repo.ListInvitations(r.Context(), tenant.FromContext(r.Context()).OrganizationID)
	if err != nil {
		writeInvitationError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Invitation]{Data: items, Total: len(items), Limit: len(items)})
}

// invitingPrincipal is the principal whose rights bound an issued or renewed
// invitation (grant ceiling); without one the request is refused.
func invitingPrincipal(w http.ResponseWriter, r *http.Request) (*identity.Principal, bool) {
	p, ok := identity.PrincipalFromContext(r.Context())
	if !ok {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "invitations are issued by an authenticated principal")
		return nil, false
	}
	return &p, true
}

// Issue creates an invitation valid for seven days. The response carries
// the token once; only its hash is stored. The invitee gets the link with
// the token by mail (invitation.created in the issue's transaction). The
// issuer must hold user:manage and the permissions of the role in a scope
// covering the invitation's scope (403 otherwise).
func (h *InvitationHandler) Issue(w http.ResponseWriter, r *http.Request) {
	if h.unconfigured(w) {
		return
	}
	t := tenant.FromContext(r.Context())
	if t.UserID == "" {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "invitations are issued by a user")
		return
	}
	caller, ok := invitingPrincipal(w, r)
	if !ok {
		return
	}
	var req struct {
		Email     string `json:"email"`
		RoleID    string `json:"role_id"`
		ScopeType string `json:"scope_type"`
		ScopeID   string `json:"scope_id"`
	}
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	token, hash, err := NewInvitationToken(t.OrganizationID)
	if err != nil {
		writeInvitationError(w, r, err)
		return
	}
	inv := &Invitation{OrganizationID: t.OrganizationID, Email: req.Email, RoleID: req.RoleID,
		ScopeType: req.ScopeType, ScopeID: req.ScopeID, InvitedBy: t.UserID, ExpiresAt: h.now().Add(InvitationTTL)}
	if err := h.repo.IssueInvitation(r.Context(), caller, inv, hash, InvitationLink(h.acceptURL, token)); err != nil {
		writeInvitationError(w, r, err)
		return
	}
	inv.Token = token
	api.WriteJSON(w, http.StatusCreated, inv)
}

// Renew gives a pending or expired invitation a new token and seven more
// days; the invitee gets a mail with the new link. The grant ceiling
// applies to the renewing principal, not to the original issuer.
func (h *InvitationHandler) Renew(w http.ResponseWriter, r *http.Request) {
	if h.unconfigured(w) {
		return
	}
	caller, ok := invitingPrincipal(w, r)
	if !ok {
		return
	}
	orgID := tenant.FromContext(r.Context()).OrganizationID
	token, hash, err := NewInvitationToken(orgID)
	if err != nil {
		writeInvitationError(w, r, err)
		return
	}
	inv, err := h.repo.RenewInvitation(r.Context(), orgID, chi.URLParam(r, "id"), caller, hash, InvitationLink(h.acceptURL, token), h.now().Add(InvitationTTL))
	if err != nil {
		writeInvitationError(w, r, err)
		return
	}
	inv.Token = token
	api.WriteJSON(w, http.StatusOK, inv)
}

// Revoke deletes a pending or expired invitation.
func (h *InvitationHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	if h.unavailable(w) {
		return
	}
	if err := h.repo.RevokeInvitation(r.Context(), tenant.FromContext(r.Context()).OrganizationID, chi.URLParam(r, "id")); err != nil {
		writeInvitationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Accept consumes an invitation once and creates the user with the invited
// role and scope. With the IdP admin client the password creates the IdP
// account in the same step; it is never stored by Reticora. An org_admin
// account is bound to MFA; when that fails, the acceptance answers 503 and
// the invitation stays open.
func (h *InvitationHandler) Accept(w http.ResponseWriter, r *http.Request) {
	if h.unavailable(w) {
		return
	}
	var req struct {
		Token       string `json:"token"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
	}
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	orgID, ok := InvitationOrg(req.Token)
	if !ok {
		writeInvitationError(w, r, ErrInvitationInvalid)
		return
	}
	if utf8.RuneCountInString(req.DisplayName) > 200 {
		writeInvitationError(w, r, &api.FieldError{Field: "display_name", Message: "display_name has at most 200 characters"})
		return
	}
	var (
		createAccount func(ctx context.Context, inv *Invitation, role string) error
		accountID     string
	)
	if h.accounts != nil {
		if utf8.RuneCountInString(req.Password) < minInvitedPasswordLength {
			writeInvitationError(w, r, &api.FieldError{Field: "password",
				Message: fmt.Sprintf("password must have at least %d characters", minInvitedPasswordLength)})
			return
		}
		createAccount = func(ctx context.Context, inv *Invitation, role string) error {
			display := req.DisplayName
			if display == "" {
				display = inv.Email
			}
			id, err := h.accounts.CreateInvitedAccount(ctx, inv.Email, display, req.Password, orgID, role)
			if err != nil {
				return err
			}
			accountID = id
			return nil
		}
	}
	accepted, err := h.repo.AcceptInvitation(r.Context(), orgID, HashInvitationToken(req.Token), req.DisplayName, h.quota, createAccount)
	if err != nil {
		if accountID != "" {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
			defer cancel()
			if derr := h.accounts.DeleteAccount(cleanup, accountID); derr != nil {
				slog.ErrorContext(r.Context(), "delete IdP account after failed acceptance", "error", derr)
			}
		}
		writeInvitationError(w, r, err)
		return
	}
	accepted.AccountCreated = accountID != ""
	api.WriteJSON(w, http.StatusCreated, accepted)
}
