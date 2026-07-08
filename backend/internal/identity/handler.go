package identity

import (
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/go-chi/chi/v5"
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
	r.Post("/api/v1/auth/login", h.Login)
	r.Post("/api/v1/auth/callback", h.Callback)
	r.Post("/api/v1/auth/refresh", h.Refresh)
	r.Get("/api/v1/auth/me", h.Me)
}

// Login initiates the OIDC authentication flow.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "OIDC login flow not implemented")
}

// Callback handles the OIDC callback and issues an internal session token.
func (h *Handler) Callback(w http.ResponseWriter, r *http.Request) {
	api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "OIDC callback flow not implemented")
}

// Refresh refreshes a session token.
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "session refresh flow not implemented")
}

// Me returns information about the current authenticated user.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "current user endpoint not implemented")
}
