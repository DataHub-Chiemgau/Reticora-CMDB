package ipam

import (
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// HTTP handlers of the IP reservations of a subnet (NET-09, WP-299):
// /api/v1/subnets/{id}/reservations; reads need ipam:read, writes
// ipam:write.

// subnetOf answers 404 when the caller does not see the subnet of the path.
func (h *Handler) subnetOf(w http.ResponseWriter, r *http.Request, orgID string) (*Subnet, bool) {
	subnet, err := h.repo.GetSubnet(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		writeRepoError(w, err, "subnet not found")
		return nil, false
	}
	return subnet, true
}

// ListReservations handles GET /api/v1/subnets/{id}/reservations
func (h *Handler) ListReservations(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	subnet, ok := h.subnetOf(w, r, orgID)
	if !ok {
		return
	}
	items, total, err := h.repo.ListReservations(r.Context(), orgID, subnet.ID, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	if items == nil {
		items = []IPAddress{}
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[IPAddress]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total,
	})
}

// CreateReservation handles POST /api/v1/subnets/{id}/reservations
func (h *Handler) CreateReservation(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req CreateReservationRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Address == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "address is required")
		return
	}
	normalized, err := validateIP(req.Address)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	subnet, ok := h.subnetOf(w, r, orgID)
	if !ok {
		return
	}
	inSubnet, err := ipInSubnet(normalized, subnet.CIDR)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if !inSubnet {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "address does not belong to subnet "+subnet.CIDR)
		return
	}
	a := &IPAddress{
		OrganizationID: orgID,
		SubnetID:       subnet.ID,
		Address:        normalized,
		Type:           IPTypeReserved,
		Description:    req.Description,
		ReservedUntil:  req.ReservedUntil,
		// The reserving user (convention 16); empty for a principal without
		// user id, then NULL.
		ReservedBy: tenant.FromContext(r.Context()).UserID,
	}
	if a.ReservedUntil != nil {
		until := a.ReservedUntil.UTC()
		a.ReservedUntil = &until
	}
	if err := h.repo.CreateReservation(r.Context(), a); err != nil {
		writeRepoError(w, err, "subnet not found")
		return
	}
	api.WriteJSON(w, http.StatusCreated, a)
}

// DeleteReservation handles DELETE /api/v1/subnets/{id}/reservations/{ip_id}
func (h *Handler) DeleteReservation(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	subnet, ok := h.subnetOf(w, r, orgID)
	if !ok {
		return
	}
	if err := h.repo.DeleteReservation(r.Context(), orgID, subnet.ID, chi.URLParam(r, "ip_id")); err != nil {
		writeRepoError(w, err, "reservation not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
