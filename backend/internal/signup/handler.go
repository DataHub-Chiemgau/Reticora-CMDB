package signup

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
)

// Handler serves the public signup endpoints.
type Handler struct {
	svc     *Service
	protect *Protection
}

// NewHandler creates the handler; a nil service answers 503.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// WithProtection sets the captcha, blocklist and per-IP limit (WP-112).
// Without it signups answer 503: the endpoint never runs unprotected.
func (h *Handler) WithProtection(p *Protection) *Handler {
	h.protect = p
	return h
}

func (h *Handler) available() bool { return h.protect != nil && h.svc.Available() }

// RegisterRoutes registers the signup routes. Both are unauthenticated: the
// request carries no credential, the verification token is the credential.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/signup/challenge", h.Challenge)
	r.Post("/api/v1/signup", h.Submit)
	r.Post("/api/v1/signup/verify", h.Verify)
}

// Challenge issues an ALTCHA challenge for the signup form.
func (h *Handler) Challenge(w http.ResponseWriter, r *http.Request) {
	if !h.available() {
		writeError(w, r, ErrUnavailable)
		return
	}
	c, err := h.protect.NewChallenge()
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	api.WriteJSON(w, http.StatusOK, c)
}

// Submit accepts a signup request after the protection (per-IP budget,
// captcha, disposable domains). The answer is 202 whether or not the
// address is known, so the endpoint reveals no accounts.
func (h *Handler) Submit(w http.ResponseWriter, r *http.Request) {
	if !h.available() {
		writeError(w, r, ErrUnavailable)
		return
	}
	var req Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid JSON body")
		return
	}
	if err := h.protect.Check(r.Context(), middleware.ClientIP(r), req.Altcha, req.Email); err != nil {
		if !writeProtectionError(w, r, err) {
			// The shared store failed: fail closed.
			slog.ErrorContext(r.Context(), "signup protection unavailable", "error", err)
			api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "signup protection is unavailable, try again later")
		}
		return
	}
	if err := h.svc.Submit(r.Context(), req); err != nil {
		writeError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "verification_sent"})
}

// Verify completes a signup and creates the organization.
func (h *Handler) Verify(w http.ResponseWriter, r *http.Request) {
	var v Verification
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&v); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid JSON body")
		return
	}
	res, err := h.svc.Verify(r.Context(), v)
	if err != nil {
		writeError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, res)
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrUnavailable):
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", err.Error())
	case errors.Is(err, ErrInvalidToken):
		api.WriteError(w, http.StatusNotFound, "Not Found", err.Error())
	case errors.Is(err, ErrExpired):
		api.WriteError(w, http.StatusGone, "Gone", err.Error())
	case errors.Is(err, ErrAccountExists):
		api.WriteFieldError(w, &api.ConflictError{Field: "email", Message: err.Error()})
	case api.WriteFieldError(w, err):
	default:
		slog.ErrorContext(r.Context(), "signup failed", "error", err)
		api.WriteError(w, http.StatusInternalServerError, "Internal Server Error", "signup failed")
	}
}
