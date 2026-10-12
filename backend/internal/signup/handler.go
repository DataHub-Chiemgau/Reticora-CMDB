package signup

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Handler serves the public signup endpoints.
type Handler struct {
	svc *Service
}

// NewHandler creates the handler; a nil service answers 503.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// RegisterRoutes registers the signup routes. Both are unauthenticated: the
// request carries no credential, the verification token is the credential.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Post("/api/v1/signup", h.Submit)
	r.Post("/api/v1/signup/verify", h.Verify)
}

// Submit accepts a signup request. The answer is 202 whether or not the
// address is known, so the endpoint reveals no accounts.
func (h *Handler) Submit(w http.ResponseWriter, r *http.Request) {
	var req Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid JSON body")
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
