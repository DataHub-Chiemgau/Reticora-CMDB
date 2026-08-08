package identity

import (
	"context"
	"errors"
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
}

// NewHandler constructs a new identity handler.
func NewHandler(oidc *OIDCProvider, sessions *SessionIssuer) *Handler {
	return &Handler{oidc: oidc, sessions: sessions}
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

	refreshedClaims := cloneSessionClaims(claims)
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

	orgID := firstUUIDGroup(idToken.Groups)
	if orgID == "" {
		return "", time.Time{}, authResult{}, errors.New("identity: no organization group found in ID token")
	}

	now := time.Now().UTC()
	claims := SessionClaims{
		Subject:        idToken.Subject,
		OrganizationID: orgID,
		Permissions:    permissionsFromGroups(idToken.Groups),
		IssuedAt:       now,
		ExpiresAt:      now.Add(sessionLifetime),
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
	return []Permission{
		PermCIRead,
		PermSiteRead,
		PermTopologyRead,
		PermAuditRead,
	}
}

func editorPermissions() []Permission {
	return []Permission{
		PermCIRead,
		PermCIWrite,
		PermCIDelete,
		PermSiteRead,
		PermSiteWrite,
		PermRackWrite,
		PermRelationshipWrite,
		PermContactWrite,
		PermTopologyRead,
		PermDiscoveryIngest,
		PermExportRun,
	}
}

func allPermissions() []Permission {
	return []Permission{
		PermCIRead,
		PermCIWrite,
		PermCIDelete,
		PermCITypeManage,
		PermSiteRead,
		PermSiteWrite,
		PermRackWrite,
		PermRelationshipWrite,
		PermContactWrite,
		PermTopologyRead,
		PermDiscoveryIngest,
		PermCollectorManage,
		PermCredentialManage,
		PermWebhookManage,
		PermExportRun,
		PermUserManage,
		PermRoleManage,
		PermEntitlementManage,
		PermAuditRead,
		PermAPIKeyManage,
	}
}

func firstUUIDGroup(groups []string) string {
	for _, group := range groups {
		candidate := strings.TrimSpace(group)
		if isUUID(candidate) {
			return candidate
		}
	}
	return ""
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
	return cloned
}

func writeIdentityError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeContextError(w, err)
	case strings.Contains(err.Error(), "authorization code is required"),
		strings.Contains(err.Error(), "PKCE code verifier is required"),
		strings.Contains(err.Error(), "ID token is required"),
		strings.Contains(err.Error(), "groups claim"):
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
	case strings.Contains(err.Error(), "organization group"),
		strings.Contains(err.Error(), "unexpected ID token issuer"),
		strings.Contains(err.Error(), "ID token is expired"),
		strings.Contains(err.Error(), "ID token subject is required"),
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
