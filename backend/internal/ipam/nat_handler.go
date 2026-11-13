package ipam

import (
	"errors"
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// HTTP handlers of the NAT translations (IPM-15, WP-1071):
// /api/v1/nat-translations; reads need ipam:read, writes ipam:write (authz,
// WP-866), the feature ipam gates both (WP-865).

// registerNATRoutes registers the routes of nat_handler.go.
func (h *Handler) registerNATRoutes(r chi.Router) {
	r.Get("/api/v1/nat-translations", h.ListNATTranslations)
	r.Post("/api/v1/nat-translations", h.CreateNATTranslation)
	r.Get("/api/v1/nat-translations/{id}", h.GetNATTranslation)
	r.Patch("/api/v1/nat-translations/{id}", h.UpdateNATTranslation)
	r.Delete("/api/v1/nat-translations/{id}", h.DeleteNATTranslation)
}

// writeNATError answers a failed NAT request: 404 for an unknown or
// invisible translation, 422 per field, else the repository error.
func writeNATError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "NAT translation not found")
	case api.WriteFieldError(w, err):
	default:
		api.WriteRepoError(w, err)
	}
}

// ListNATTranslations handles GET /api/v1/nat-translations with the
// filters ci_id, kind, address_id and subnet_id.
func (h *Handler) ListNATTranslations(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.natRepo(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := &NATFilter{CIID: q.Get("ci_id"), Kind: q.Get("kind"), AddressID: q.Get("address_id"), SubnetID: q.Get("subnet_id")}
	if f.Kind != "" && !slices.Contains(natKinds, f.Kind) {
		api.WriteFieldError(w, errNATKind)
		return
	}
	items, total, err := repo.ListNATTranslations(r.Context(), orgID, f, page)
	if err != nil {
		writeNATError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[NATTranslation]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+len(items) < total,
	})
}

// CreateNATTranslation handles POST /api/v1/nat-translations.
func (h *Handler) CreateNATTranslation(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.natRepo(w, r)
	if !ok {
		return
	}
	var req CreateNATTranslationRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid JSON body")
		return
	}
	n, err := repo.CreateNATTranslation(r.Context(), orgID, &req)
	if err != nil {
		writeNATError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, n)
}

// GetNATTranslation handles GET /api/v1/nat-translations/{id}.
func (h *Handler) GetNATTranslation(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.natRepo(w, r)
	if !ok {
		return
	}
	n, err := repo.GetNATTranslation(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		writeNATError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, n)
}

// UpdateNATTranslation handles PATCH /api/v1/nat-translations/{id}.
func (h *Handler) UpdateNATTranslation(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.natRepo(w, r)
	if !ok {
		return
	}
	var req UpdateNATTranslationRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid JSON body")
		return
	}
	n, err := repo.UpdateNATTranslation(r.Context(), orgID, chi.URLParam(r, "id"), &req)
	if err != nil {
		writeNATError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, n)
}

// DeleteNATTranslation handles DELETE /api/v1/nat-translations/{id}.
func (h *Handler) DeleteNATTranslation(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.natRepo(w, r)
	if !ok {
		return
	}
	if err := repo.DeleteNATTranslation(r.Context(), orgID, chi.URLParam(r, "id")); err != nil {
		writeNATError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
