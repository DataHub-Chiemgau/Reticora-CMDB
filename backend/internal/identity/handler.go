package identity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/go-chi/chi/v5"
)

// RefreshCookieName is the HttpOnly cookie that carries the refresh token
// (AUT-02). It is only sent to the auth endpoints.
const (
	RefreshCookieName = "reticora_refresh"
	refreshCookiePath = "/api/v1/auth"
)

// Handler provides auth-related HTTP endpoints.
type Handler struct {
	oidc     *OIDCProvider
	sessions *SessionIssuer
	// provisioner auto-creates the app_user on first login so downstream
	// foreign keys (ticket reporter, assignment, ...) resolve without manual
	// user setup. Nil disables provisioning (tests, --no-db without users).
	provisioner UserProvisioner
	// defaultRole is the standard role assigned on auto-provisioning
	// (e.g. "viewer"); empty assigns no role.
	defaultRole string
	// access resolves the role grants of the user from the database at login
	// and refresh (RBA-03). Nil keeps the IdP groups as the only grant
	// (tests, --no-db without users).
	access AccessResolver
	// refresh stores the rotating refresh tokens.
	refresh *RefreshSessions
}

// AccessResolver loads the role grants of a user: every role assignment and
// custom role assignment with its permissions and scope, and the org-wide
// grants of the standard roles the IdP roles map to (RBA-02).
type AccessResolver interface {
	AccessGrants(ctx context.Context, orgID, userID string) ([]Grant, error)
	// RoleGrants returns one org-wide grant per named standard role of the
	// organization, with the role's permissions from the database. Roles
	// valid only in a client scope grant nothing here.
	RoleGrants(ctx context.Context, orgID string, roleNames []string) ([]Grant, error)
}

// ErrUserInactive is returned when a deactivated user logs in or refreshes a
// session. A login never reactivates a deactivated account (TLC-04).
var ErrUserInactive = errors.New("identity: user is deactivated")

// ErrFirstLoginNotPermitted is returned when an unknown user logs in without
// an admission for organization and e-mail: a pending invitation or an
// account created by an administrator or SCIM (AUT-01).
var ErrFirstLoginNotPermitted = errors.New("identity: first login requires an invitation for this organization and e-mail")

// UserStatusChecker reports whether an app_user may hold a session. A
// provisioner implementing it makes refresh check the user status; a user
// that no longer exists is not active.
type UserStatusChecker interface {
	UserActive(ctx context.Context, orgID, userID string) (bool, error)
}

// UserProvisioner upserts the authenticated OIDC subject into app_user.
type UserProvisioner interface {
	// EnsureUser resolves the app_user of the OIDC subject in the
	// organization, or on first login by organization and verified e-mail
	// (empty when unverified). It creates or links a user only with an
	// admission and returns ErrFirstLoginNotPermitted otherwise, and
	// ErrUserInactive for a deactivated user.
	EnsureUser(ctx context.Context, orgID, oidcSubject, email, displayName string) (string, error)
	// EnsureRole assigns the named standard role when the user has none yet.
	EnsureRole(ctx context.Context, orgID, userID, roleName string) error
}

// NewHandler constructs a new identity handler. Refresh tokens live in a
// per-process store until WithRefreshSessions sets the shared one.
func NewHandler(oidc *OIDCProvider, sessions *SessionIssuer) *Handler {
	return &Handler{oidc: oidc, sessions: sessions,
		refresh: NewRefreshSessions(cache.NewMemoryStore(), sessions.Revocations())}
}

// WithRefreshSessions sets the refresh token store; production uses the Redis
// store so that every replica sees rotations and revocations.
func (h *Handler) WithRefreshSessions(r *RefreshSessions) *Handler {
	h.refresh = r
	return h
}

// WithProvisioning enables first-login app_user provisioning.
func (h *Handler) WithProvisioning(p UserProvisioner, defaultRole string) *Handler {
	h.provisioner = p
	h.defaultRole = defaultRole
	return h
}

// WithAccessResolver makes login and refresh read the role assignments from
// the database instead of relying on token claims only.
func (h *Handler) WithAccessResolver(resolver AccessResolver) *Handler {
	h.access = resolver
	return h
}

// RegisterRoutes registers authentication routes on the provided router.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/auth/config", h.Config)
	r.Post("/api/v1/auth/callback", h.Callback)
	r.Post("/api/v1/auth/refresh", h.Refresh)
	r.Post("/api/v1/auth/logout", h.Logout)
	r.Get("/api/v1/auth/me", h.Me)
}

// Config exposes the browser-facing OIDC parameters so that the single-page
// application can start an authorization code flow with PKCE. The endpoint is
// unauthenticated by design and never returns the client secret.
func (h *Handler) Config(w http.ResponseWriter, r *http.Request) {
	if err := r.Context().Err(); err != nil {
		writeContextError(w, err)
		return
	}
	if h == nil || h.oidc == nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", "identity: OIDC provider is not configured")
		return
	}

	cfg := h.oidc.PublicConfig()
	if cfg.Issuer == "" || cfg.ClientID == "" {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "identity: OIDC provider is not configured")
		return
	}

	api.WriteJSON(w, http.StatusOK, cfg)
}

// Callback handles the OIDC callback and issues an internal session token.
// The authorization code exchange is only accepted together with the PKCE
// code verifier and the transaction state, so an injected or stolen code
// cannot be redeemed without the browser-side secrets.
func (h *Handler) Callback(w http.ResponseWriter, r *http.Request) {
	if err := r.Context().Err(); err != nil {
		writeContextError(w, err)
		return
	}

	var req struct {
		Code         string `json:"code"`
		State        string `json:"state"`
		CodeVerifier string `json:"code_verifier"`
	}
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if strings.TrimSpace(req.State) == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "state is required")
		return
	}
	if strings.TrimSpace(req.CodeVerifier) == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "code_verifier is required")
		return
	}

	sessionToken, expiresAt, result, err := h.exchangeAndIssue(r.Context(), req.Code, req.CodeVerifier)
	if err != nil {
		writeIdentityError(w, err)
		return
	}
	refreshToken, err := h.refresh.Issue(r.Context(), &RefreshGrant{Claims: withoutTokenTimes(&result.claims)})
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", "store refresh token")
		return
	}
	setRefreshCookie(w, refreshToken)

	api.WriteJSON(w, http.StatusOK, map[string]any{
		"token": sessionToken,
		"user": map[string]any{
			"sub":         result.idToken.Subject,
			"email":       result.idToken.Email,
			"name":        result.idToken.Name,
			"groups":      append([]string(nil), result.idToken.Groups...),
			"org_id":      result.claims.OrganizationID,
			"permissions": append([]Permission(nil), result.claims.Permissions...),
		},
		"expires_at": expiresAt.Format(time.RFC3339),
	})
}

// Refresh issues a new access token for the refresh cookie and rotates the
// cookie (AUT-02). The refresh token is used up; presenting it again revokes
// the session. Roles and scopes are read again, and a deactivated user gets
// no token.
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	if err := r.Context().Err(); err != nil {
		writeContextError(w, err)
		return
	}
	if h == nil || h.sessions == nil || h.refresh == nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", "session issuer is not configured")
		return
	}
	cookie, err := r.Cookie(RefreshCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "refresh cookie is required")
		return
	}
	grant, err := h.refresh.Consume(r.Context(), cookie.Value)
	if err != nil {
		clearRefreshCookie(w)
		if errors.Is(err, ErrRefreshInvalid) || errors.Is(err, ErrRefreshReused) || errors.Is(err, ErrSessionRevoked) {
			api.WriteError(w, http.StatusUnauthorized, "Unauthorized", err.Error())
			return
		}
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", "check refresh token")
		return
	}

	// A deactivated or deleted user gets no new token, even when the
	// blacklist entry of the deactivation has expired or was never written.
	if checker, ok := h.provisioner.(UserStatusChecker); ok {
		active, statusErr := checker.UserActive(r.Context(), grant.Claims.OrganizationID, grant.Claims.Subject)
		if statusErr != nil {
			api.WriteError(w, http.StatusInternalServerError, "Internal Error", "check user status")
			return
		}
		if !active {
			clearRefreshCookie(w)
			api.WriteError(w, http.StatusUnauthorized, "Unauthorized", ErrUserInactive.Error())
			return
		}
	}

	refreshedClaims := cloneSessionClaims(&grant.Claims)
	// Roles and scopes are re-read on every refresh so revoked or narrowed
	// assignments take effect without a new login.
	if err = h.applyAccess(r.Context(), &refreshedClaims); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", "resolve role assignments")
		return
	}
	if err = stampToken(&refreshedClaims); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	token, err := h.sessions.Issue(refreshedClaims)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	grant.Claims = withoutTokenTimes(&refreshedClaims)
	refreshToken, err := h.refresh.Issue(r.Context(), grant)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", "store refresh token")
		return
	}
	setRefreshCookie(w, refreshToken)

	api.WriteJSON(w, http.StatusOK, map[string]string{
		"token":      token,
		"expires_at": refreshedClaims.ExpiresAt.Format(time.RFC3339),
	})
}

// Logout ends the session: the refresh family of the cookie is revoked, the
// presented access token is blacklisted until it expires (AUT-02) and the
// cookie is cleared. It needs no valid access token, so an expired session
// can still log out.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	if err := r.Context().Err(); err != nil {
		writeContextError(w, err)
		return
	}
	if h == nil || h.sessions == nil || h.refresh == nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", "session issuer is not configured")
		return
	}
	if cookie, err := r.Cookie(RefreshCookieName); err == nil {
		if err := h.refresh.Revoke(r.Context(), cookie.Value); err != nil {
			api.WriteError(w, http.StatusInternalServerError, "Internal Error", "revoke refresh token")
			return
		}
	}
	if token, err := bearerTokenFromRequest(r); err == nil {
		if claims, validErr := h.sessions.Validate(token); validErr == nil {
			if revocations := h.sessions.Revocations(); revocations != nil {
				if err := revocations.RevokeToken(r.Context(), claims.ID, claims.ExpiresAt); err != nil {
					api.WriteError(w, http.StatusInternalServerError, "Internal Error", "revoke access token")
					return
				}
			}
		}
	}
	clearRefreshCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// stampToken sets token id, issue and expiry time of a new access token.
func stampToken(claims *SessionClaims) error {
	id, err := newTokenID()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	claims.ID, claims.IssuedAt, claims.ExpiresAt = id, now, now.Add(sessionLifetime)
	return nil
}

// withoutTokenTimes returns the claims a refresh grant keeps: everything but
// token id, issue and expiry time.
func withoutTokenTimes(claims *SessionClaims) SessionClaims {
	c := cloneSessionClaims(claims)
	c.ID, c.IssuedAt, c.ExpiresAt = "", time.Time{}, time.Time{}
	return c
}

func setRefreshCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     RefreshCookieName,
		Value:    token,
		Path:     refreshCookiePath,
		MaxAge:   int(RefreshTokenLifetime.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

func clearRefreshCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     RefreshCookieName,
		Value:    "",
		Path:     refreshCookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

// Me returns information about the current authenticated user.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	if err := r.Context().Err(); err != nil {
		writeContextError(w, err)
		return
	}
	if h == nil || h.sessions == nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", "session issuer is not configured")
		return
	}

	token, err := bearerTokenFromRequest(r)
	if err != nil {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", err.Error())
		return
	}

	claims, err := h.sessions.Validate(token)
	if err != nil {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", err.Error())
		return
	}
	if !claims.ExpiresAt.IsZero() && time.Now().UTC().After(claims.ExpiresAt) {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "session token is expired")
		return
	}

	api.WriteJSON(w, http.StatusOK, claims)
}

type authResult struct {
	idToken *IDTokenClaims
	claims  SessionClaims
}

func (h *Handler) exchangeAndIssue(ctx context.Context, code, codeVerifier string) (string, time.Time, authResult, error) {
	if err := ctx.Err(); err != nil {
		return "", time.Time{}, authResult{}, err
	}
	if h == nil || h.oidc == nil {
		return "", time.Time{}, authResult{}, errors.New("identity: OIDC provider is not configured")
	}
	if h.sessions == nil {
		return "", time.Time{}, authResult{}, errors.New("identity: session issuer is not configured")
	}
	if strings.TrimSpace(code) == "" {
		return "", time.Time{}, authResult{}, errors.New("identity: authorization code is required")
	}

	tokenSet, err := h.oidc.ExchangeCodeWithVerifier(ctx, code, codeVerifier)
	if err != nil {
		return "", time.Time{}, authResult{}, err
	}
	idToken, err := h.oidc.ValidateIDToken(ctx, tokenSet.IDToken)
	if err != nil {
		return "", time.Time{}, authResult{}, err
	}
	if strings.TrimSpace(idToken.Subject) == "" {
		return "", time.Time{}, authResult{}, errors.New("identity: ID token subject is required")
	}

	// The organization is the realm's user attribute (CH26, AUT-09), never a
	// group name the user could be added to.
	orgID := idToken.OrganizationID
	if orgID == "" {
		return "", time.Time{}, authResult{}, errors.New("identity: no organization attribute in ID token")
	}

	// Provision the app_user for the OIDC subject on first login so FK-bound
	// resources (tickets, assignments) accept the acting user. The session
	// subject is the app_user id once provisioning ran, keeping token and
	// database identity aligned.
	subject := idToken.Subject
	if h.provisioner != nil {
		email := ""
		if idToken.EmailVerified {
			email = idToken.Email
		}
		userID, provisionErr := h.provisioner.EnsureUser(ctx, orgID, idToken.Subject, email, idToken.Name)
		if errors.Is(provisionErr, ErrUserInactive) || errors.Is(provisionErr, ErrFirstLoginNotPermitted) {
			return "", time.Time{}, authResult{}, provisionErr
		}
		if provisionErr != nil {
			return "", time.Time{}, authResult{}, fmt.Errorf("identity: provision user: %w", provisionErr)
		}
		if h.defaultRole != "" {
			// Best effort: role assignment must not block login.
			_ = h.provisioner.EnsureRole(ctx, orgID, userID, h.defaultRole)
		}
		subject = userID
	}

	claims := SessionClaims{
		Subject:        subject,
		OrganizationID: orgID,
		Groups:         append([]string(nil), idToken.Groups...),
		Name:           idToken.Name,
		Email:          idToken.Email,
	}
	if err = h.applyAccess(ctx, &claims); err != nil {
		return "", time.Time{}, authResult{}, fmt.Errorf("identity: resolve role assignments: %w", err)
	}
	if stampErr := stampToken(&claims); stampErr != nil {
		return "", time.Time{}, authResult{}, stampErr
	}

	sessionToken, err := h.sessions.Issue(claims)
	if err != nil {
		return "", time.Time{}, authResult{}, err
	}

	return sessionToken, claims.ExpiresAt, authResult{
		idToken: idToken,
		claims:  claims,
	}, nil
}

// applyAccess sets permissions and scopes of the session from the database
// only (RBA-02): the role assignments of the user and the standard roles its
// IdP roles map to (StandardRolesForGroups), both read from the roles of the
// organization. IdP roles are org-wide grants; database assignments carry
// their client and site scope. A failing lookup fails the login or refresh
// instead of issuing an unscoped session. Without a resolver (tests) the
// token's access stays unchanged.
func (h *Handler) applyAccess(ctx context.Context, claims *SessionClaims) error {
	if h.access == nil {
		return nil
	}
	grants := make([]Grant, 0, 4)
	if roles := StandardRolesForGroups(claims.Groups); len(roles) > 0 {
		mapped, err := h.access.RoleGrants(ctx, claims.OrganizationID, roles)
		if err != nil {
			return err
		}
		grants = append(grants, mapped...)
	}
	stored, err := h.access.AccessGrants(ctx, claims.OrganizationID, claims.Subject)
	if err != nil {
		return err
	}
	grants = append(grants, stored...)
	access := ResolveAccess(grants)
	scope := access.Scope
	claims.Permissions = access.Permissions
	claims.Scope = &scope
	claims.PermissionScopes = access.PermissionScopes
	claims.ClientScope = scope.LegacyClientScope()
	return nil
}

func bearerTokenFromRequest(r *http.Request) (string, error) {
	authz := strings.TrimSpace(r.Header.Get("Authorization"))
	if authz == "" {
		return "", errors.New("missing bearer token")
	}

	parts := strings.SplitN(authz, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
		return "", errors.New("invalid authorization header")
	}

	return strings.TrimSpace(parts[1]), nil
}

func isUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
				return false
			}
		}
	}
	return true
}

func cloneSessionClaims(claims *SessionClaims) SessionClaims {
	cloned := *claims
	cloned.Permissions = append([]Permission(nil), claims.Permissions...)
	cloned.Groups = append([]string(nil), claims.Groups...)
	if claims.Scope != nil {
		scope := *claims.Scope
		cloned.Scope = &scope
	}
	if claims.PermissionScopes != nil {
		cloned.PermissionScopes = make(map[Permission]Scope, len(claims.PermissionScopes))
		for k, v := range claims.PermissionScopes {
			cloned.PermissionScopes[k] = v
		}
	}
	return cloned
}

func writeIdentityError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeContextError(w, err)
	case errors.Is(err, ErrUserInactive), errors.Is(err, ErrFirstLoginNotPermitted):
		api.WriteError(w, http.StatusForbidden, "Forbidden", err.Error())
	case strings.Contains(err.Error(), "authorization code is required"),
		strings.Contains(err.Error(), "PKCE code verifier is required"),
		strings.Contains(err.Error(), "ID token is required"),
		strings.Contains(err.Error(), "groups claim"):
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
	case strings.Contains(err.Error(), "organization attribute"),
		strings.Contains(err.Error(), "unexpected ID token issuer"),
		strings.Contains(err.Error(), "ID token is expired"),
		strings.Contains(err.Error(), "ID token subject is required"),
		strings.Contains(err.Error(), "verify ID token signature"),
		strings.Contains(err.Error(), "ID token audience"),
		strings.Contains(err.Error(), "authorized party"),
		strings.Contains(err.Error(), "unexpected ID token nonce"),
		strings.Contains(err.Error(), "missing ID token"):
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", err.Error())
	case strings.Contains(err.Error(), "session issuer is not configured"),
		strings.Contains(err.Error(), "OIDC provider is not configured"),
		strings.Contains(err.Error(), "OIDC issuer URL is required"),
		strings.Contains(err.Error(), "OIDC client ID is required"):
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
	default:
		api.WriteError(w, http.StatusBadGateway, "Bad Gateway", err.Error())
	}
}

func writeContextError(w http.ResponseWriter, err error) {
	api.WriteError(w, http.StatusRequestTimeout, "Request Timeout", err.Error())
}
