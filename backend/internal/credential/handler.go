package credential

import (
	"encoding/json"
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler provides HTTP handlers for credential management.
type Handler struct {
	svc *Service
}

// NewHandler creates a new credential HTTP handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes mounts credential routes on the given router.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/api/v1/credentials", func(r chi.Router) {
		r.Post("/", h.create)
		r.Get("/", h.list)
		r.Get("/{id}", h.get)
		r.Get("/{id}/decrypt", h.decrypt)
		r.Delete("/{id}", h.delete)
	})
}

// organizationID resolves the tenant from the authenticated request context.
// Credentials hold decryptable secret material, so the organization must never
// be taken from client-supplied input such as a header or the request body —
// that would let any authenticated caller read another tenant's secrets.
func organizationID(w http.ResponseWriter, r *http.Request) (string, bool) {
	orgID := tenant.FromContext(r.Context()).OrganizationID
	if orgID == "" {
		writeError(w, http.StatusUnauthorized, "missing tenant context")
		return "", false
	}
	return orgID, true
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}

	var req CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.OrganizationID = orgID

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Secret == nil {
		writeError(w, http.StatusBadRequest, "secret is required")
		return
	}

	cred, err := h.svc.Create(r.Context(), req)
	if err == ErrInvalidKind {
		writeError(w, http.StatusBadRequest, "invalid credential kind")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create credential")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(cred)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}

	creds, err := h.svc.List(r.Context(), orgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list credentials")
		return
	}

	if creds == nil {
		creds = []Credential{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(creds)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")

	cred, err := h.svc.Get(r.Context(), orgID, id)
	if err == ErrNotFound {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get credential")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cred)
}

func (h *Handler) decrypt(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")

	secret, err := h.svc.Decrypt(r.Context(), orgID, id)
	if err == ErrNotFound {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to decrypt credential")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(secret)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organizationID(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")

	err := h.svc.Delete(r.Context(), orgID, id)
	if err == ErrNotFound {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete credential")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// writeError writes an RFC 7807 problem response.
func writeError(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"type":   "about:blank",
		"title":  http.StatusText(status),
		"status": status,
		"detail": detail,
	})
}
