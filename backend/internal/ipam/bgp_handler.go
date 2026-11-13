package ipam

import (
	"errors"
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// HTTP handlers of the BGP peerings (IPM-17, WP-1068):
// /api/v1/bgp-peerings; reads need ipam:read, writes ipam:write (authz,
// WP-866), the feature ipam gates both (WP-865). The peerings of a device
// are the list with ci_id.

// registerBGPRoutes registers the routes of bgp_handler.go.
func (h *Handler) registerBGPRoutes(r chi.Router) {
	r.Get("/api/v1/bgp-peerings", h.ListBGPPeerings)
	r.Post("/api/v1/bgp-peerings", h.CreateBGPPeering)
	r.Get("/api/v1/bgp-peerings/{id}", h.GetBGPPeering)
	r.Patch("/api/v1/bgp-peerings/{id}", h.UpdateBGPPeering)
	r.Delete("/api/v1/bgp-peerings/{id}", h.DeleteBGPPeering)
}

// bgpRepo returns the repository of the peerings, or answers 501 without
// one (no database).
func (h *Handler) bgpRepo(w http.ResponseWriter, r *http.Request) (BGPRepository, string, bool) {
	repo, ok := h.repo.(BGPRepository)
	if !ok {
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "BGP peerings are not available")
		return nil, "", false
	}
	orgID, ok := org(w, r)
	return repo, orgID, ok
}

// writeBGPError answers a failed peering request: 404 for an unknown or
// invisible peering, 422 per field, else the repository error (409 for a
// duplicate).
func writeBGPError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "BGP peering not found")
	case api.WriteFieldError(w, err):
	default:
		api.WriteRepoError(w, err)
	}
}

// ListBGPPeerings handles GET /api/v1/bgp-peerings with the filters ci_id,
// vrf_id, local_asn_id, remote_asn_number and expected_state.
func (h *Handler) ListBGPPeerings(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.bgpRepo(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := &BGPFilter{CIID: q.Get("ci_id"), VRFID: q.Get("vrf_id"), LocalASNID: q.Get("local_asn_id"), ExpectedState: q.Get("expected_state")}
	if v := q.Get("remote_asn_number"); v != "" {
		n, err := ParseASN(v)
		if err != nil {
			api.WriteFieldError(w, errBGPRemoteASN)
			return
		}
		f.RemoteASNNumber = n
	}
	if f.ExpectedState != "" && !slices.Contains(bgpExpectedStates, f.ExpectedState) {
		api.WriteFieldError(w, errBGPState)
		return
	}
	items, total, err := repo.ListBGPPeerings(r.Context(), orgID, f, page)
	if err != nil {
		writeBGPError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[BGPPeering]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+len(items) < total,
	})
}

// CreateBGPPeering handles POST /api/v1/bgp-peerings.
func (h *Handler) CreateBGPPeering(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.bgpRepo(w, r)
	if !ok {
		return
	}
	var req CreateBGPPeeringRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid JSON body")
		return
	}
	p, err := repo.CreateBGPPeering(r.Context(), orgID, &req)
	if err != nil {
		writeBGPError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, p)
}

// GetBGPPeering handles GET /api/v1/bgp-peerings/{id}.
func (h *Handler) GetBGPPeering(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.bgpRepo(w, r)
	if !ok {
		return
	}
	p, err := repo.GetBGPPeering(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		writeBGPError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, p)
}

// UpdateBGPPeering handles PATCH /api/v1/bgp-peerings/{id}.
func (h *Handler) UpdateBGPPeering(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.bgpRepo(w, r)
	if !ok {
		return
	}
	var req UpdateBGPPeeringRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid JSON body")
		return
	}
	p, err := repo.UpdateBGPPeering(r.Context(), orgID, chi.URLParam(r, "id"), &req)
	if err != nil {
		writeBGPError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, p)
}

// DeleteBGPPeering handles DELETE /api/v1/bgp-peerings/{id}.
func (h *Handler) DeleteBGPPeering(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.bgpRepo(w, r)
	if !ok {
		return
	}
	if err := repo.DeleteBGPPeering(r.Context(), orgID, chi.URLParam(r, "id")); err != nil {
		writeBGPError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
