package ipam

import (
	"net/http"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/go-chi/chi/v5"
)

// HTTP handlers of VRFs (TEN-10, NET-05): /api/v1/vrfs; writes need vrf:manage.

// ListVRFs handles GET /api/v1/vrfs
func (h *Handler) ListVRFs(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListVRFs(r.Context(), orgID, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	if items == nil {
		items = []VRF{}
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[VRF]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total,
	})
}

// CreateVRF handles POST /api/v1/vrfs
func (h *Handler) CreateVRF(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req CreateVRFRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name is required")
		return
	}
	v := &VRF{OrganizationID: orgID, Name: strings.TrimSpace(req.Name), Description: req.Description}
	if err := h.repo.CreateVRF(r.Context(), v); err != nil {
		writeRepoError(w, err, "vrf not found")
		return
	}
	api.WriteJSON(w, http.StatusCreated, v)
}

// GetVRF handles GET /api/v1/vrfs/{id}
func (h *Handler) GetVRF(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	v, err := h.repo.GetVRF(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		writeRepoError(w, err, "vrf not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, v)
}

// UpdateVRF handles PATCH /api/v1/vrfs/{id}
func (h *Handler) UpdateVRF(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req UpdateVRFRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name must not be empty")
		return
	}
	v, err := h.repo.UpdateVRF(r.Context(), orgID, chi.URLParam(r, "id"), req)
	if err != nil {
		writeRepoError(w, err, "vrf not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, v)
}

// DeleteVRF handles DELETE /api/v1/vrfs/{id}
func (h *Handler) DeleteVRF(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	if err := h.repo.DeleteVRF(r.Context(), orgID, chi.URLParam(r, "id")); err != nil {
		writeRepoError(w, err, "vrf not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
