package ipam

import (
	"errors"
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// HTTP handlers of the atomic allocation (IPM-03, WP-904): POST
// /api/v1/subnets/{id}/allocate and /allocate-subnet, both ipam:write.

// writeAllocationError answers an allocation error: 409 subnet-exhausted
// without room, a field rule with its field, 404 for a subnet the caller
// cannot see.
func writeAllocationError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrSubnetExhausted) {
		api.WriteProblemBody(w, http.StatusConflict, api.ProblemDetail{Type: ProblemSubnetExhausted, Title: "Conflict",
			Status: http.StatusConflict, Detail: "the subnet has no free space for the allocation"})
		return
	}
	writeRepoError(w, err, "subnet not found")
}

// AllocateAddress handles POST /api/v1/subnets/{id}/allocate: the lowest
// free host address of the subnet as a new address (201).
func (h *Handler) AllocateAddress(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req AllocateAddressRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	ipType := req.Type
	if ipType == "" {
		ipType = IPTypeStatic
	}
	if ipType != IPTypeStatic && ipType != IPTypeReserved {
		api.WriteFieldError(w, errAllocateType)
		return
	}
	a := &IPAddress{
		OrganizationID: orgID,
		SubnetID:       chi.URLParam(r, "id"),
		InterfaceID:    req.InterfaceID,
		Type:           ipType,
		DNSName:        req.DNSName,
		Description:    req.Description,
	}
	if ipType == IPTypeReserved {
		// The reserving user as for a reservation (Konvention 16); empty
		// for a principal without user id, then NULL.
		a.ReservedBy = tenant.FromContext(r.Context()).UserID
	}
	if err := h.repo.AllocateAddress(r.Context(), a); err != nil {
		writeAllocationError(w, err)
		return
	}
	h.checkDuplicates(r.Context(), orgID, DuplicateSource{IPIDs: []string{a.ID}})
	api.WriteJSON(w, http.StatusCreated, a)
}

// AllocateSubnet handles POST /api/v1/subnets/{id}/allocate-subnet: the
// lowest aligned free prefix of prefix_length inside the subnet as a new
// child (201).
func (h *Handler) AllocateSubnet(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req AllocateSubnetRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.PrefixLength == nil {
		api.WriteFieldError(w, errPrefixLength)
		return
	}
	s := &Subnet{
		OrganizationID: orgID,
		ClientID:       req.ClientID,
		SectionID:      req.SectionID,
		Name:           req.Name,
		IsPool:         req.IsPool,
	}
	if err := h.repo.AllocateSubnet(r.Context(), chi.URLParam(r, "id"), *req.PrefixLength, s); err != nil {
		writeAllocationError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, s)
}
