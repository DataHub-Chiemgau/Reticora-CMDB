package tenantapi

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
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
	r.Get("/api/v1/org", h.GetOrganization)
	r.Patch("/api/v1/org", h.UpdateOrganization)

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

// writeFieldError answers field errors (400) and conflicts (409) naming the
// field (LOC-01–LOC-06) and reports whether err was one of them.
func writeFieldError(w http.ResponseWriter, err error) bool { return api.WriteFieldError(w, err) }

// writeWriteError reports a failed create or update: field errors and
// conflicts with their field, "not found" as 404, the rest sanitized.
func writeWriteError(w http.ResponseWriter, err error, notFound string) {
	switch {
	case writeFieldError(w, err):
	case err.Error() == "not found":
		api.WriteError(w, http.StatusNotFound, "Not Found", notFound)
	default:
		api.WriteRepoError(w, err)
	}
}

// writeCreateError reports a failed create of a building or room: field
// errors and conflicts with their field (LOC-04, LOC-05), database errors
// sanitized, an unknown or invisible parent as 400. Other repository
// errors go to the log only (API-03).
func writeCreateError(w http.ResponseWriter, err error) {
	switch {
	case writeFieldError(w, err):
	case api.WriteDBError(w, err):
	case err.Error() == "not found", strings.HasSuffix(err.Error(), " not found"):
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "the parent site or building was not found")
	default:
		slog.Warn("create of a building or room failed", "error", err)
		api.WriteRepoError(w, err)
	}
}

// --- Organization ---

// GetOrganization handles GET /api/v1/org (API-05, LOC-01).
func (h *Handler) GetOrganization(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOrUnauthorized(w, r)
	if !ok {
		return
	}
	o, err := h.repo.GetOrganization(r.Context(), orgID)
	if err != nil {
		writeWriteError(w, err, "organization not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, o)
}

// UpdateOrganization handles PATCH /api/v1/org: name, settings, IANA time
// zone, ISO 4217 currency and BCP 47 locale (canonicalized), and the AI
// opt-in (AI-02), which only an org-wide holder of user:manage changes.
func (h *Handler) UpdateOrganization(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOrUnauthorized(w, r)
	if !ok {
		return
	}
	var req UpdateOrganizationRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if err := validateOrganization(&req); err != nil {
		writeFieldError(w, err)
		return
	}
	if req.AIOptIn != nil && !holdsOrgWide(r, identity.PermUserManage) {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "only a holder of user:manage for the whole organization may change ai_opt_in")
		return
	}
	o, err := h.repo.UpdateOrganization(r.Context(), orgID, req)
	if err != nil {
		writeWriteError(w, err, "organization not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, o)
}

// holdsOrgWide reports whether the request's principal holds perm for the
// whole organization, not only for some clients, sites or teams (RBA-03).
// Without a principal it is false (fail-closed).
func holdsOrgWide(r *http.Request, perm identity.Permission) bool {
	p, ok := identity.PrincipalFromContext(r.Context())
	return ok && len(identity.GrantCeiling(&p, []identity.Grant{identity.OrgWideGrant([]identity.Permission{perm})})) == 0
}

func validateOrganization(req *UpdateOrganizationRequest) error {
	if req.Name != nil && *req.Name == "" {
		return &FieldError{Field: "name", Message: "must not be empty"}
	}
	if req.Timezone != nil {
		if err := validTimezone("timezone", *req.Timezone); err != nil {
			return err
		}
	}
	if req.Currency != nil {
		if err := validCurrency(*req.Currency); err != nil {
			return err
		}
	}
	if req.Locale != nil {
		canonical, err := canonicalLocale(*req.Locale)
		if err != nil {
			return err
		}
		req.Locale = &canonical
	}
	return nil
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
		api.WriteRepoError(w, err)
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
	// Accept `code` as a slug alias when no explicit slug is given.
	if req.Slug == "" && req.Code != "" {
		req.Slug = req.Code
	}
	if req.Name == "" || req.Slug == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name and slug are required")
		return
	}
	if err := validOptionalTimezone("timezone", req.Timezone); err != nil {
		writeFieldError(w, err)
		return
	}
	c := &Client{OrganizationID: orgID, Name: req.Name, Slug: req.Slug, ExternalRef: emptyToNil(req.ExternalRef),
		Timezone: emptyToNil(req.Timezone), Settings: req.Settings}
	if err := h.repo.CreateClient(r.Context(), c); err != nil {
		writeWriteError(w, err, "client not found")
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
	if (req.Name != nil && *req.Name == "") || (req.Slug != nil && *req.Slug == "") {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name and slug must not be empty")
		return
	}
	if err := validOptionalTimezone("timezone", req.Timezone); err != nil {
		writeFieldError(w, err)
		return
	}
	c, err := h.repo.UpdateClient(r.Context(), orgID, chi.URLParam(r, "id"), req)
	if err != nil {
		writeWriteError(w, err, "client not found")
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
		api.WriteRepoError(w, err)
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
	if err := validOptionalTimezone("timezone", req.Timezone); err != nil {
		writeFieldError(w, err)
		return
	}
	s := &Site{
		Timezone:       emptyToNil(req.Timezone),
		OrganizationID: orgID,
		ClientID:       req.ClientID,
		Name:           req.Name,
		Address:        req.Address,
		GeoLat:         req.GeoLat,
		GeoLon:         req.GeoLon,
		Notes:          req.Notes,
	}
	if err := h.repo.CreateSite(r.Context(), s); err != nil {
		writeWriteError(w, err, "client not found")
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
	if req.Name != nil && *req.Name == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name must not be empty")
		return
	}
	if err := validOptionalTimezone("timezone", req.Timezone); err != nil {
		writeFieldError(w, err)
		return
	}
	s, err := h.repo.UpdateSite(r.Context(), orgID, chi.URLParam(r, "id"), req)
	if err != nil {
		writeWriteError(w, err, "site not found")
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
		if locations.WriteDependencyConflict(w, r, err) {
			return
		}
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
		api.WriteRepoError(w, err)
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
		writeCreateError(w, err)
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
		writeWriteError(w, err, "building not found")
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
		if locations.WriteDependencyConflict(w, r, err) {
			return
		}
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
		api.WriteRepoError(w, err)
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
		writeCreateError(w, err)
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
		writeWriteError(w, err, "room not found")
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
		if locations.WriteDependencyConflict(w, r, err) {
			return
		}
		api.WriteError(w, http.StatusNotFound, "Not Found", "room not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// emptyToNil drops an empty optional text.
func emptyToNil(v *string) *string {
	if v == nil || *v == "" {
		return nil
	}
	return v
}
