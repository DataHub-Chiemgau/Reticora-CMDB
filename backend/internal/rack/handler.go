package rack

import (
	"errors"
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler exposes HTTP endpoints for racks, mounts, and cables.
type Handler struct {
	repo Repository
}

// NewHandler creates a new rack handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers rack, mount, and cable routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/racks", h.ListRacks)
	r.Post("/api/v1/racks", h.CreateRack)
	r.Get("/api/v1/racks/{id}", h.GetRack)
	r.Patch("/api/v1/racks/{id}", h.UpdateRack)
	r.Delete("/api/v1/racks/{id}", h.DeleteRack)

	r.Get("/api/v1/racks/{id}/mounts", h.ListMounts)
	r.Post("/api/v1/racks/{id}/mounts", h.CreateMount)
	r.Get("/api/v1/rack-mounts/{id}", h.GetMount)
	r.Patch("/api/v1/rack-mounts/{id}", h.UpdateMount)
	r.Delete("/api/v1/rack-mounts/{id}", h.DeleteMount)

	r.Get("/api/v1/cables", h.ListCables)
	r.Post("/api/v1/cables", h.CreateCable)
	r.Get("/api/v1/cables/{id}", h.GetCable)
	r.Patch("/api/v1/cables/{id}", h.UpdateCable)
	r.Delete("/api/v1/cables/{id}", h.DeleteCable)
}

func org(w http.ResponseWriter, r *http.Request) (string, bool) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return "", false
	}
	return t.OrganizationID, true
}

// writeRepoError maps repository sentinel errors to HTTP responses.
func writeRepoError(w http.ResponseWriter, err error, notFoundMsg string) {
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrRackNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", notFoundMsg)
	case errors.Is(err, ErrValidation):
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
	default:
		api.WriteRepoError(w, err)
	}
}

// --- Racks ---

// ListRacks handles GET /api/v1/racks
func (h *Handler) ListRacks(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListRacks(r.Context(), orgID, r.URL.Query().Get("room_id"), page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Rack]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// CreateRack handles POST /api/v1/racks
func (h *Handler) CreateRack(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req CreateRackRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Name == "" || req.RoomID == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "room_id and name are required")
		return
	}
	height := req.HeightU
	if height == 0 {
		height = 42
	}
	if height < 1 || height > 60 {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "height_u must be between 1 and 60")
		return
	}
	width := req.WidthMM
	if width == 0 {
		width = 600
	}
	depth := req.DepthMM
	if depth == 0 {
		depth = 1000
	}
	rk := &Rack{
		OrganizationID: orgID,
		RoomID:         req.RoomID,
		Name:           req.Name,
		HeightU:        height,
		WidthMM:        width,
		DepthMM:        depth,
		Notes:          req.Notes,
	}
	if err := h.repo.CreateRack(r.Context(), rk); err != nil {
		writeRepoError(w, err, "rack not found")
		return
	}
	api.WriteJSON(w, http.StatusCreated, rk)
}

// GetRack handles GET /api/v1/racks/{id}
func (h *Handler) GetRack(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	rk, err := h.repo.GetRack(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "rack not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, rk)
}

// UpdateRack handles PATCH /api/v1/racks/{id}
func (h *Handler) UpdateRack(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req UpdateRackRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.HeightU != nil && (*req.HeightU < 1 || *req.HeightU > 60) {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "height_u must be between 1 and 60")
		return
	}
	rk, err := h.repo.UpdateRack(r.Context(), orgID, chi.URLParam(r, "id"), req)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "rack not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, rk)
}

// DeleteRack handles DELETE /api/v1/racks/{id}
func (h *Handler) DeleteRack(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	if err := h.repo.DeleteRack(r.Context(), orgID, chi.URLParam(r, "id")); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "rack not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Mounts ---

// ListMounts handles GET /api/v1/racks/{id}/mounts
func (h *Handler) ListMounts(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListMounts(r.Context(), orgID, chi.URLParam(r, "id"), page)
	if err != nil {
		writeRepoError(w, err, "rack not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[RackMount]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// CreateMount handles POST /api/v1/racks/{id}/mounts
func (h *Handler) CreateMount(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req CreateMountRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.CIID == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "ci_id is required")
		return
	}
	height := req.HeightU
	if height == 0 {
		height = 1
	}
	face := req.Face
	if face == "" {
		face = "front"
	}
	if !ValidFaces[face] {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid face")
		return
	}
	m := &RackMount{
		OrganizationID: orgID,
		RackID:         chi.URLParam(r, "id"),
		CIID:           req.CIID,
		PositionU:      req.PositionU,
		HeightU:        height,
		Face:           face,
	}
	if err := h.repo.CreateMount(r.Context(), m); err != nil {
		writeRepoError(w, err, "rack not found")
		return
	}
	api.WriteJSON(w, http.StatusCreated, m)
}

// GetMount handles GET /api/v1/rack-mounts/{id}
func (h *Handler) GetMount(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	m, err := h.repo.GetMount(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "rack mount not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, m)
}

// UpdateMount handles PATCH /api/v1/rack-mounts/{id}
func (h *Handler) UpdateMount(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req UpdateMountRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	m, err := h.repo.UpdateMount(r.Context(), orgID, chi.URLParam(r, "id"), req)
	if err != nil {
		writeRepoError(w, err, "rack mount not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, m)
}

// DeleteMount handles DELETE /api/v1/rack-mounts/{id}
func (h *Handler) DeleteMount(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	if err := h.repo.DeleteMount(r.Context(), orgID, chi.URLParam(r, "id")); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "rack mount not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Cables ---

// ListCables handles GET /api/v1/cables
func (h *Handler) ListCables(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListCables(r.Context(), orgID, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Cable]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// CreateCable handles POST /api/v1/cables
func (h *Handler) CreateCable(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req CreateCableRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	cableType := req.CableType
	if cableType == "" {
		cableType = "copper"
	}
	if !ValidCableTypes[cableType] {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid cable_type")
		return
	}
	status := req.Status
	if status == "" {
		status = "connected"
	}
	if !ValidCableStatuses[status] {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid status")
		return
	}
	c := &Cable{
		OrganizationID:    orgID,
		Label:             req.Label,
		CableType:         cableType,
		LengthM:           req.LengthM,
		Color:             req.Color,
		SourceInterfaceID: req.SourceInterfaceID,
		TargetInterfaceID: req.TargetInterfaceID,
		Status:            status,
		InstalledAt:       req.InstalledAt,
	}
	if err := h.repo.CreateCable(r.Context(), c); err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, c)
}

// GetCable handles GET /api/v1/cables/{id}
func (h *Handler) GetCable(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	c, err := h.repo.GetCable(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "cable not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, c)
}

// UpdateCable handles PATCH /api/v1/cables/{id}
func (h *Handler) UpdateCable(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req UpdateCableRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.CableType != nil && !ValidCableTypes[*req.CableType] {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid cable_type")
		return
	}
	if req.Status != nil && !ValidCableStatuses[*req.Status] {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid status")
		return
	}
	c, err := h.repo.UpdateCable(r.Context(), orgID, chi.URLParam(r, "id"), req)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "cable not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, c)
}

// DeleteCable handles DELETE /api/v1/cables/{id}
func (h *Handler) DeleteCable(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	if err := h.repo.DeleteCable(r.Context(), orgID, chi.URLParam(r, "id")); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "cable not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
