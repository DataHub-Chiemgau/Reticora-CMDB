package ipam

import (
	"errors"
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler exposes HTTP endpoints for subnets, IP addresses, and interfaces.
type Handler struct {
	repo Repository
}

// NewHandler creates a new ipam handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers IPAM routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/subnets", h.ListSubnets)
	r.Post("/api/v1/subnets", h.CreateSubnet)
	r.Get("/api/v1/subnets/{id}", h.GetSubnet)
	r.Patch("/api/v1/subnets/{id}", h.UpdateSubnet)
	r.Delete("/api/v1/subnets/{id}", h.DeleteSubnet)
	r.Get("/api/v1/subnets/{id}/addresses", h.ListSubnetAddresses)

	// ips is the catalog path (API-05); ip-addresses stays until its
	// sunset (API-01).
	for _, base := range []string{"/api/v1/ips", "/api/v1/ip-addresses"} {
		r.Get(base, h.ListIPAddresses)
		r.Post(base, h.CreateIPAddress)
		r.Get(base+"/{id}", h.GetIPAddress)
		r.Patch(base+"/{id}", h.UpdateIPAddress)
		r.Delete(base+"/{id}", h.DeleteIPAddress)
	}

	r.Get("/api/v1/cis/{id}/interfaces", h.ListInterfaces)
	r.Post("/api/v1/cis/{id}/interfaces", h.CreateInterface)
	r.Get("/api/v1/network-interfaces/{id}", h.GetInterface)
	r.Patch("/api/v1/network-interfaces/{id}", h.UpdateInterface)
	r.Delete("/api/v1/network-interfaces/{id}", h.DeleteInterface)

	r.Get("/api/v1/vlans", h.ListVLANs)
	r.Post("/api/v1/vlans", h.CreateVLAN)
	r.Get("/api/v1/vlans/{id}", h.GetVLAN)
	r.Patch("/api/v1/vlans/{id}", h.UpdateVLAN)
	r.Delete("/api/v1/vlans/{id}", h.DeleteVLAN)

	r.Get("/api/v1/vrfs", h.ListVRFs)
	r.Post("/api/v1/vrfs", h.CreateVRF)
	r.Get("/api/v1/vrfs/{id}", h.GetVRF)
	r.Patch("/api/v1/vrfs/{id}", h.UpdateVRF)
	r.Delete("/api/v1/vrfs/{id}", h.DeleteVRF)
}

func org(w http.ResponseWriter, r *http.Request) (string, bool) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return "", false
	}
	return t.OrganizationID, true
}

func writeRepoError(w http.ResponseWriter, err error, notFoundMsg string) {
	// A refused field, e.g. a VLAN of another client (422 vlan_id).
	if api.WriteFieldError(w, err) {
		return
	}
	switch {
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", notFoundMsg)
	case errors.Is(err, ErrValidation):
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
	case errors.Is(err, ErrConflict):
		api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
	case errors.Is(err, ErrInUse):
		api.WriteError(w, http.StatusUnprocessableEntity, "Unprocessable Entity", err.Error())
	default:
		api.WriteRepoError(w, err)
	}
}

// --- Subnets ---

// ListSubnets handles GET /api/v1/subnets
func (h *Handler) ListSubnets(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	f := SubnetFilter{
		ClientID: r.URL.Query().Get("client_id"),
		SiteID:   r.URL.Query().Get("site_id"),
	}
	items, total, err := h.repo.ListSubnets(r.Context(), orgID, f, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Subnet]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// CreateSubnet handles POST /api/v1/subnets
func (h *Handler) CreateSubnet(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req CreateSubnetRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.CIDR == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "cidr is required")
		return
	}
	normalized, err := validateCIDR(req.CIDR)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Gateway != "" {
		if _, err := validateIP(req.Gateway); err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
			return
		}
	}
	for _, dns := range req.DNSServers {
		if _, err := validateIP(dns); err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid dns server: "+err.Error())
			return
		}
	}
	s := &Subnet{
		OrganizationID: orgID,
		ClientID:       req.ClientID,
		SiteID:         req.SiteID,
		CIDR:           normalized,
		VRFID:          req.VRFID,
		Name:           req.Name,
		VLANID:         req.VLANID,
		Gateway:        req.Gateway,
		DNSServers:     req.DNSServers,
		Description:    req.Description,
		IsManagement:   req.IsManagement,
	}
	if err := h.repo.CreateSubnet(r.Context(), s); err != nil {
		writeRepoError(w, err, "subnet not found")
		return
	}
	api.WriteJSON(w, http.StatusCreated, s)
}

// GetSubnet handles GET /api/v1/subnets/{id}
func (h *Handler) GetSubnet(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	s, err := h.repo.GetSubnet(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "subnet not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, s)
}

// UpdateSubnet handles PATCH /api/v1/subnets/{id}
func (h *Handler) UpdateSubnet(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req UpdateSubnetRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Gateway != nil && *req.Gateway != "" {
		if _, err := validateIP(*req.Gateway); err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
			return
		}
	}
	for _, dns := range req.DNSServers {
		if _, err := validateIP(dns); err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid dns server: "+err.Error())
			return
		}
	}
	s, err := h.repo.UpdateSubnet(r.Context(), orgID, chi.URLParam(r, "id"), req)
	if err != nil {
		if !api.WriteFieldError(w, err) {
			api.WriteError(w, http.StatusNotFound, "Not Found", "subnet not found")
		}
		return
	}
	api.WriteJSON(w, http.StatusOK, s)
}

// DeleteSubnet handles DELETE /api/v1/subnets/{id}
func (h *Handler) DeleteSubnet(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	if err := h.repo.DeleteSubnet(r.Context(), orgID, chi.URLParam(r, "id")); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "subnet not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListSubnetAddresses handles GET /api/v1/subnets/{id}/addresses
func (h *Handler) ListSubnetAddresses(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	subnetID := chi.URLParam(r, "id")
	if _, err := h.repo.GetSubnet(r.Context(), orgID, subnetID); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "subnet not found")
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListIPAddresses(r.Context(), orgID, subnetID, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[IPAddress]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// --- IP Addresses ---

// ListIPAddresses handles GET /api/v1/ip-addresses
func (h *Handler) ListIPAddresses(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListIPAddresses(r.Context(), orgID, r.URL.Query().Get("subnet_id"), page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[IPAddress]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// CreateIPAddress handles POST /api/v1/ip-addresses
func (h *Handler) CreateIPAddress(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req CreateIPAddressRequest
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
	ipType := req.Type
	if ipType == "" {
		ipType = IPTypeStatic
	}
	if !ValidIPTypes[ipType] {
		api.WriteFieldError(w, &api.FieldError{Field: "type", Message: "invalid ip address type"})
		return
	}
	if req.SubnetID != "" {
		subnet, err := h.repo.GetSubnet(r.Context(), orgID, req.SubnetID)
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "subnet not found")
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
	}
	a := &IPAddress{
		OrganizationID: orgID,
		SubnetID:       req.SubnetID,
		InterfaceID:    req.InterfaceID,
		Address:        normalized,
		Type:           ipType,
		DNSName:        req.DNSName,
		Description:    req.Description,
	}
	if err := h.repo.CreateIPAddress(r.Context(), a); err != nil {
		writeRepoError(w, err, "ip address not found")
		return
	}
	api.WriteJSON(w, http.StatusCreated, a)
}

// GetIPAddress handles GET /api/v1/ip-addresses/{id}
func (h *Handler) GetIPAddress(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	a, err := h.repo.GetIPAddress(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "ip address not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, a)
}

// UpdateIPAddress handles PATCH /api/v1/ip-addresses/{id}
func (h *Handler) UpdateIPAddress(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req UpdateIPAddressRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Type != nil && !ValidIPTypes[*req.Type] {
		api.WriteFieldError(w, &api.FieldError{Field: "type", Message: "invalid ip address type"})
		return
	}
	// Validate address still belongs to a newly-assigned subnet.
	if req.SubnetID != nil && *req.SubnetID != "" {
		current, err := h.repo.GetIPAddress(r.Context(), orgID, chi.URLParam(r, "id"))
		if err != nil {
			api.WriteError(w, http.StatusNotFound, "Not Found", "ip address not found")
			return
		}
		subnet, err := h.repo.GetSubnet(r.Context(), orgID, *req.SubnetID)
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "subnet not found")
			return
		}
		inSubnet, err := ipInSubnet(current.Address, subnet.CIDR)
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
			return
		}
		if !inSubnet {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "address does not belong to subnet "+subnet.CIDR)
			return
		}
	}
	a, err := h.repo.UpdateIPAddress(r.Context(), orgID, chi.URLParam(r, "id"), req)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "ip address not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, a)
}

// DeleteIPAddress handles DELETE /api/v1/ip-addresses/{id}
func (h *Handler) DeleteIPAddress(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	if err := h.repo.DeleteIPAddress(r.Context(), orgID, chi.URLParam(r, "id")); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "ip address not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Network Interfaces ---

// ListInterfaces handles GET /api/v1/cis/{id}/interfaces
func (h *Handler) ListInterfaces(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListInterfacesForCI(r.Context(), orgID, chi.URLParam(r, "id"), page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[NetworkInterface]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// CreateInterface handles POST /api/v1/cis/{id}/interfaces
func (h *Handler) CreateInterface(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req CreateInterfaceRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Name == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name is required")
		return
	}
	ifType := req.IfType
	if ifType == "" {
		ifType = "ethernet"
	}
	if !ValidInterfaceTypes[ifType] {
		api.WriteFieldError(w, &api.FieldError{Field: "if_type", Message: "invalid interface type"})
		return
	}
	adminStatus := req.AdminStatus
	if adminStatus == "" {
		adminStatus = "up"
	}
	operStatus := req.OperStatus
	if operStatus == "" {
		operStatus = "unknown"
	}
	ni := &NetworkInterface{
		OrganizationID: orgID,
		CIID:           chi.URLParam(r, "id"),
		Name:           req.Name,
		IfIndex:        req.IfIndex,
		MAC:            req.MAC,
		IfType:         ifType,
		SpeedMbps:      req.SpeedMbps,
		MTU:            req.MTU,
		VLANID:         req.VLANID,
		IsManagement:   req.IsManagement,
		IsUplink:       req.IsUplink,
		AdminStatus:    adminStatus,
		OperStatus:     operStatus,
		Description:    req.Description,
	}
	if err := h.repo.CreateInterface(r.Context(), ni); err != nil {
		switch {
		case api.WriteFieldError(w, err):
		case errors.Is(err, ErrNotFound):
			// An unknown CI or one the caller cannot see, e.g. of
			// another organization (F-311).
			api.WriteError(w, http.StatusNotFound, "Not Found", "CI not found")
		default:
			api.WriteRepoError(w, err)
		}
		return
	}
	api.WriteJSON(w, http.StatusCreated, ni)
}

// GetInterface handles GET /api/v1/network-interfaces/{id}
func (h *Handler) GetInterface(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	ni, err := h.repo.GetInterface(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "network interface not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, ni)
}

// UpdateInterface handles PATCH /api/v1/network-interfaces/{id}
func (h *Handler) UpdateInterface(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req UpdateInterfaceRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.IfType != nil && !ValidInterfaceTypes[*req.IfType] {
		api.WriteFieldError(w, &api.FieldError{Field: "if_type", Message: "invalid interface type"})
		return
	}
	ni, err := h.repo.UpdateInterface(r.Context(), orgID, chi.URLParam(r, "id"), req)
	if err != nil {
		if !api.WriteFieldError(w, err) {
			api.WriteError(w, http.StatusNotFound, "Not Found", "network interface not found")
		}
		return
	}
	api.WriteJSON(w, http.StatusOK, ni)
}

// interfaceInUseProblem is the 409 problem of an interface that edges keep
// (REL-05).
type interfaceInUseProblem struct {
	api.ProblemDetail
	RelationshipIDs []string `json:"relationship_ids"`
}

// DeleteInterface handles DELETE /api/v1/network-interfaces/{id}: 409 while
// an edge other than an unverified discovery edge names the interface, 404
// for an interface the caller cannot see; unverified discovery edges go
// with the interface.
func (h *Handler) DeleteInterface(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	err := h.repo.DeleteInterface(r.Context(), orgID, chi.URLParam(r, "id"))
	var inUse *InterfaceInUseError
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.As(err, &inUse):
		api.WriteProblemBody(w, http.StatusConflict, interfaceInUseProblem{
			ProblemDetail: api.ProblemDetail{Type: "https://reticora.io/problems/409", Title: "Conflict",
				Status: http.StatusConflict, Detail: inUse.Error()},
			RelationshipIDs: append([]string{}, inUse.RelationshipIDs...),
		})
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "network interface not found")
	default:
		api.WriteRepoError(w, err)
	}
}
