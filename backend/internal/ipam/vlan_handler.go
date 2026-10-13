package ipam

import (
	"net/http"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/go-chi/chi/v5"
)

// HTTP handlers of the VLAN catalog (NET-08, NET-05): /api/v1/vlans.

// ListVLANs handles GET /api/v1/vlans
func (h *Handler) ListVLANs(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListVLANs(r.Context(), orgID, r.URL.Query().Get("site_id"), page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	if items == nil {
		items = []VLAN{}
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[VLAN]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total,
	})
}

// CreateVLAN handles POST /api/v1/vlans
func (h *Handler) CreateVLAN(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req CreateVLANRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if err := validateVLAN(req.VLANID, req.Name); err != nil {
		writeRepoError(w, err, "")
		return
	}
	v := &VLAN{OrganizationID: orgID, SiteID: req.SiteID, VLANID: req.VLANID, Name: strings.TrimSpace(req.Name),
		Description: req.Description}
	if err := h.repo.CreateVLAN(r.Context(), v); err != nil {
		writeRepoError(w, err, "vlan not found")
		return
	}
	api.WriteJSON(w, http.StatusCreated, v)
}

// GetVLAN handles GET /api/v1/vlans/{id}
func (h *Handler) GetVLAN(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	v, err := h.repo.GetVLAN(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		writeRepoError(w, err, "vlan not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, v)
}

// UpdateVLAN handles PATCH /api/v1/vlans/{id}
func (h *Handler) UpdateVLAN(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req UpdateVLANRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	number, name := 1, "x"
	if req.VLANID != nil {
		number = *req.VLANID
	}
	if req.Name != nil {
		name = *req.Name
	}
	if err := validateVLAN(number, name); err != nil {
		writeRepoError(w, err, "")
		return
	}
	v, err := h.repo.UpdateVLAN(r.Context(), orgID, chi.URLParam(r, "id"), req)
	if err != nil {
		writeRepoError(w, err, "vlan not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, v)
}

// DeleteVLAN handles DELETE /api/v1/vlans/{id}
func (h *Handler) DeleteVLAN(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	if err := h.repo.DeleteVLAN(r.Context(), orgID, chi.URLParam(r, "id")); err != nil {
		writeRepoError(w, err, "vlan not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
