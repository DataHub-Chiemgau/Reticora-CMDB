package tenantapi

import (
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler exposes HTTP endpoints for the location hierarchy.
type Handler struct {
	repo Repository
}

// NewHandler creates a new tenantapi handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers all location-hierarchy routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/clients", h.ListClients)
	r.Post("/api/v1/clients", h.CreateClient)
	r.Get("/api/v1/clients/{id}", h.GetClient)
	r.Patch("/api/v1/clients/{id}", h.UpdateClient)
	r.Delete("/api/v1/clients/{id}", h.DeleteClient)

	r.Get("/api/v1/sites", h.ListSites)
	r.Post("/api/v1/sites", h.CreateSite)
	r.Get("/api/v1/sites/{id}", h.GetSite)
	r.Patch("/api/v1/sites/{id}", h.UpdateSite)
	r.Delete("/api/v1/sites/{id}", h.DeleteSite)

	r.Get("/api/v1/buildings", h.ListBuildings)
	r.Post("/api/v1/buildings", h.CreateBuilding)
	r.Get("/api/v1/buildings/{id}", h.GetBuilding)
	r.Patch("/api/v1/buildings/{id}", h.UpdateBuilding)
	r.Delete("/api/v1/buildings/{id}", h.DeleteBuilding)

	r.Get("/api/v1/rooms", h.ListRooms)
	r.Post("/api/v1/rooms", h.CreateRoom)
	r.Get("/api/v1/rooms/{id}", h.GetRoom)
	r.Patch("/api/v1/rooms/{id}", h.UpdateRoom)
	r.Delete("/api/v1/rooms/{id}", h.DeleteRoom)
}

func orgOrUnauthorized(w http.ResponseWriter, r *http.Request) (string, bool) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return "", false
	}
	return t.OrganizationID, true
}

func writeList[T any](w http.ResponseWriter, items []T, total int, page api.PaginationParams) {
	api.WriteJSON(w, http.StatusOK, api.ListResponse[T]{
		Data:    items,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// --- Clients ---

// ListClients handles GET /api/v1/clients
func (h *Handler) ListClients(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOrUnauthorized(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListClients(r.Context(), orgID, page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	writeList(w, items, total, page)
}

// CreateClient handles POST /api/v1/clients
func (h *Handler) CreateClient(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOrUnauthorized(w, r)
	if !ok {
		return
	}
	var req CreateClientRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Name == "" || req.Slug == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name and slug are required")
		return
	}
	c := &Client{OrganizationID: orgID, Name: req.Name, Slug: req.Slug, Settings: req.Settings}
	if err := h.repo.CreateClient(r.Context(), c); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusCreated, c)
}

// GetClient handles GET /api/v1/clients/{id}
func (h *Handler) GetClient(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOrUnauthorized(w, r)
	if !ok {
		return
	}
	c, err := h.repo.GetClient(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "client not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, c)
}

// UpdateClient handles PATCH /api/v1/clients/{id}
func (h *Handler) UpdateClient(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOrUnauthorized(w, r)
	if !ok {
		return
	}
	var req UpdateClientRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	c, err := h.repo.UpdateClient(r.Context(), orgID, chi.URLParam(r, "id"), req)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "client not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, c)
}

// DeleteClient handles DELETE /api/v1/clients/{id}
func (h *Handler) DeleteClient(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOrUnauthorized(w, r)
	if !ok {
		return
	}
	if err := h.repo.DeleteClient(r.Context(), orgID, chi.URLParam(r, "id")); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "client not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Sites ---

// ListSites handles GET /api/v1/sites
func (h *Handler) ListSites(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOrUnauthorized(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListSites(r.Context(), orgID, r.URL.Query().Get("client_id"), page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	writeList(w, items, total, page)
}

// CreateSite handles POST /api/v1/sites
func (h *Handler) CreateSite(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOrUnauthorized(w, r)
	if !ok {
		return
	}
	var req CreateSiteRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Name == "" || req.ClientID == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "client_id and name are required")
		return
	}
	s := &Site{
		OrganizationID: orgID,
		ClientID:       req.ClientID,
		Name:           req.Name,
		Address:        req.Address,
		GeoLat:         req.GeoLat,
		GeoLon:         req.GeoLon,
		Notes:          req.Notes,
	}
	if err := h.repo.CreateSite(r.Context(), s); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusCreated, s)
}

// GetSite handles GET /api/v1/sites/{id}
func (h *Handler) GetSite(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOrUnauthorized(w, r)
	if !ok {
		return
	}
	s, err := h.repo.GetSite(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "site not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, s)
}

// UpdateSite handles PATCH /api/v1/sites/{id}
func (h *Handler) UpdateSite(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOrUnauthorized(w, r)
	if !ok {
		return
	}
	var req UpdateSiteRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	s, err := h.repo.UpdateSite(r.Context(), orgID, chi.URLParam(r, "id"), req)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "site not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, s)
}

// DeleteSite handles DELETE /api/v1/sites/{id}
func (h *Handler) DeleteSite(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOrUnauthorized(w, r)
	if !ok {
		return
	}
	if err := h.repo.DeleteSite(r.Context(), orgID, chi.URLParam(r, "id")); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "site not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Buildings ---

// ListBuildings handles GET /api/v1/buildings
func (h *Handler) ListBuildings(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOrUnauthorized(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListBuildings(r.Context(), orgID, r.URL.Query().Get("site_id"), page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	writeList(w, items, total, page)
}

// CreateBuilding handles POST /api/v1/buildings
func (h *Handler) CreateBuilding(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOrUnauthorized(w, r)
	if !ok {
		return
	}
	var req CreateBuildingRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Name == "" || req.SiteID == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "site_id and name are required")
		return
	}
	b := &Building{
		OrganizationID:     orgID,
		SiteID:             req.SiteID,
		Name:               req.Name,
		Floors:             req.Floors,
		FloorplanObjectKey: req.FloorplanObjectKey,
	}
	if err := h.repo.CreateBuilding(r.Context(), b); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusCreated, b)
}

// GetBuilding handles GET /api/v1/buildings/{id}
func (h *Handler) GetBuilding(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOrUnauthorized(w, r)
	if !ok {
		return
	}
	b, err := h.repo.GetBuilding(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "building not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, b)
}

// UpdateBuilding handles PATCH /api/v1/buildings/{id}
func (h *Handler) UpdateBuilding(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOrUnauthorized(w, r)
	if !ok {
		return
	}
	var req UpdateBuildingRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	b, err := h.repo.UpdateBuilding(r.Context(), orgID, chi.URLParam(r, "id"), req)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "building not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, b)
}

// DeleteBuilding handles DELETE /api/v1/buildings/{id}
func (h *Handler) DeleteBuilding(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOrUnauthorized(w, r)
	if !ok {
		return
	}
	if err := h.repo.DeleteBuilding(r.Context(), orgID, chi.URLParam(r, "id")); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "building not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Rooms ---

// ListRooms handles GET /api/v1/rooms
func (h *Handler) ListRooms(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOrUnauthorized(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListRooms(r.Context(), orgID, r.URL.Query().Get("building_id"), page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	writeList(w, items, total, page)
}

// CreateRoom handles POST /api/v1/rooms
func (h *Handler) CreateRoom(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOrUnauthorized(w, r)
	if !ok {
		return
	}
	var req CreateRoomRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Name == "" || req.BuildingID == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "building_id and name are required")
		return
	}
	rm := &Room{
		OrganizationID: orgID,
		BuildingID:     req.BuildingID,
		Name:           req.Name,
		Floor:          req.Floor,
		RoomType:       req.RoomType,
	}
	if err := h.repo.CreateRoom(r.Context(), rm); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusCreated, rm)
}

// GetRoom handles GET /api/v1/rooms/{id}
func (h *Handler) GetRoom(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOrUnauthorized(w, r)
	if !ok {
		return
	}
	rm, err := h.repo.GetRoom(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "room not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, rm)
}

// UpdateRoom handles PATCH /api/v1/rooms/{id}
func (h *Handler) UpdateRoom(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOrUnauthorized(w, r)
	if !ok {
		return
	}
	var req UpdateRoomRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	rm, err := h.repo.UpdateRoom(r.Context(), orgID, chi.URLParam(r, "id"), req)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "room not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, rm)
}

// DeleteRoom handles DELETE /api/v1/rooms/{id}
func (h *Handler) DeleteRoom(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOrUnauthorized(w, r)
	if !ok {
		return
	}
	if err := h.repo.DeleteRoom(r.Context(), orgID, chi.URLParam(r, "id")); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "room not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
