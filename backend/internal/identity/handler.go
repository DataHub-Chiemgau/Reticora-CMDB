package identity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/go-chi/chi/v5"
)

const (
	sessionLifetime = time.Hour
	refreshWindow   = 24 * time.Hour
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
}

// AccessResolver loads the role grants of a user: every role assignment and
// custom role assignment with its permissions and scope.
type AccessResolver interface {
	AccessGrants(ctx context.Context, orgID, userID string) ([]Grant, error)
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

// NewHandler constructs a new identity handler.
func NewHandler(oidc *OIDCProvider, sessions *SessionIssuer) *Handler {
	return &Handler{oidc: oidc, sessions: sessions}
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

// Refresh refreshes a session token.
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	if err := r.Context().Err(); err != nil {
		writeContextError(w, err)
		return
	}
	if h == nil || h.sessions == nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", "session issuer is not configured")
		return
	}

	var req struct {
		Token string `json:"token"`
	}
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if strings.TrimSpace(req.Token) == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "token is required")
		return
	}

	claims, err := h.sessions.Validate(strings.TrimSpace(req.Token))
	if err != nil {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", err.Error())
		return
	}
	if claims.ExpiresAt.IsZero() {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "session token expiry is required")
		return
	}

	now := time.Now().UTC()
	if now.After(claims.ExpiresAt.Add(refreshWindow)) {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "session token refresh window has expired")
		return
	}

	// A deactivated or deleted user gets no new token, even when the
	// blacklist entry of the deactivation has expired or was never written.
	if checker, ok := h.provisioner.(UserStatusChecker); ok {
		active, statusErr := checker.UserActive(r.Context(), claims.OrganizationID, claims.Subject)
		if statusErr != nil {
			api.WriteError(w, http.StatusInternalServerError, "Internal Error", "check user status")
			return
		}
		if !active {
			api.WriteError(w, http.StatusUnauthorized, "Unauthorized", ErrUserInactive.Error())
			return
		}
	}

	refreshedClaims := cloneSessionClaims(claims)
	// Roles and scopes are re-read on every refresh so revoked or narrowed
	// assignments take effect without a new login.
	if err = h.applyAccess(r.Context(), &refreshedClaims); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", "resolve role assignments")
		return
	}
	refreshedClaims.IssuedAt = now
	refreshedClaims.ExpiresAt = now.Add(sessionLifetime)

	token, err := h.sessions.Issue(refreshedClaims)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusOK, map[string]string{
		"token":      token,
		"expires_at": refreshedClaims.ExpiresAt.Format(time.RFC3339),
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

	now := time.Now().UTC()
	claims := SessionClaims{
		Subject:        subject,
		OrganizationID: orgID,
		Groups:         append([]string(nil), idToken.Groups...),
		IssuedAt:       now,
		ExpiresAt:      now.Add(sessionLifetime),
	}
	if err = h.applyAccess(ctx, &claims); err != nil {
		return "", time.Time{}, authResult{}, fmt.Errorf("identity: resolve role assignments: %w", err)
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

// applyAccess sets permissions and scopes of the session from the IdP groups
// and the role assignments in the database. IdP group roles are org-wide
// grants; database assignments carry their client and site scope. A failing
// lookup fails the login or refresh instead of issuing an unscoped session.
func (h *Handler) applyAccess(ctx context.Context, claims *SessionClaims) error {
	if h.access == nil && len(claims.Groups) == 0 {
		// Nothing to resolve from: keep the token's access unchanged.
		return nil
	}
	grants := make([]Grant, 0, 4)
	if groupPermissions := permissionsFromGroups(claims.Groups); len(groupPermissions) > 0 {
		grants = append(grants, OrgWideGrant(groupPermissions))
	}
	if h.access != nil {
		stored, err := h.access.AccessGrants(ctx, claims.OrganizationID, claims.Subject)
		if err != nil {
			return err
		}
		grants = append(grants, stored...)
	}
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

func permissionsFromGroups(groups []string) []Permission {
	all := allPermissions()
	permissions := make([]Permission, 0)
	for _, group := range groups {
		normalized := normalizeGroup(group)
		switch {
		case strings.Contains(normalized, "admin"), strings.Contains(normalized, "owner"):
			permissions = append(permissions, all...)
			continue
		case strings.Contains(normalized, "editor"), strings.Contains(normalized, "writer"):
			permissions = append(permissions, editorPermissions()...)
		case strings.Contains(normalized, "viewer"), strings.Contains(normalized, "reader"), strings.Contains(normalized, "readonly"), strings.Contains(normalized, "read only"):
			permissions = append(permissions, readerPermissions()...)
		}

		for _, permission := range all {
			token := normalizeGroup(string(permission))
			if token != "" && strings.Contains(normalized, token) {
				permissions = append(permissions, permission)
			}
		}
	}
	return MergePermissions(permissions)
}

func readerPermissions() []Permission {
	readers := make([]Permission, 0)
	for _, permission := range allPermissions() {
		if strings.HasSuffix(string(permission), ":read") {
			readers = append(readers, permission)
		}
	}
	return readers
}

func editorPermissions() []Permission {
	return []Permission{
		PermCIRead,
		PermCIWrite,
		PermCIDelete,
		PermSiteRead,
		PermSiteWrite,
		PermRackRead,
		PermRackWrite,
		PermRelationshipRead,
		PermRelationshipWrite,
		PermContactRead,
		PermContactWrite,
		PermTopologyRead,
		PermDiscoveryRead,
		PermDiscoveryWrite,
		PermDiscoveryIngest,
		PermAssetRead,
		PermAssetWrite,
		PermAssignmentRead,
		PermAssignmentWrite,
		PermDocumentRead,
		PermDocumentWrite,
		PermStocktakeRead,
		PermStocktakeWrite,
		PermTicketRead,
		PermTicketWrite,
		PermSLARead,
		PermIPAMRead,
		PermIPAMWrite,
		PermFormRead,
		PermFormWrite,
		PermWorkflowRead,
		PermWorkflowWrite,
		PermComplianceRead,
		PermMonitoringRead,
		PermSearchRead,
		PermAIRead,
		PermExportRun,
	}
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

func normalizeGroup(value string) string {
	replacer := strings.NewReplacer(":", " ", "-", " ", "_", " ", "/", " ", ".", " ")
	return strings.Join(strings.Fields(strings.ToLower(replacer.Replace(value))), " ")
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
