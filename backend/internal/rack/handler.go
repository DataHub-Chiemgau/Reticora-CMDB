package rack

import (
	"errors"
	"net/http"
	"sort"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
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

	// layout and PUT/DELETE mounts/{ci_id} are the rack contract of RCK-02;
	// the list and POST on racks/{id}/mounts stay until their sunset.
	r.Get("/api/v1/racks/{id}/layout", h.GetLayout)
	r.Put("/api/v1/racks/{id}/mounts/{ci_id}", h.PutMount)
	r.Delete("/api/v1/racks/{id}/mounts/{ci_id}", h.UnmountCI)
	r.Get("/api/v1/racks/{id}/mounts", h.ListMounts)
	r.Post("/api/v1/racks/{id}/mounts", h.CreateMount)
	// mounts is the catalog path (API-05): the rack is a query parameter
	// or part of the body; rack-mounts stays until its sunset (API-01).
	r.Get("/api/v1/mounts", h.ListMountsByQuery)
	r.Post("/api/v1/mounts", h.CreateMount)
	for _, base := range []string{"/api/v1/mounts", "/api/v1/rack-mounts"} {
		r.Get(base+"/{id}", h.GetMount)
		r.Patch(base+"/{id}", h.UpdateMount)
		r.Delete(base+"/{id}", h.DeleteMount)
	}

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
	case errors.Is(err, ErrConflict):
		api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
	case api.WriteFieldError(w, err):
	default:
		api.WriteRepoError(w, err)
	}
}

// --- Racks ---

// DefaultHeightUnits is the height of a rack created without one (LOC-06).
const DefaultHeightUnits = 42

// validHeightUnits accepts 1–60 height units; anything else, an explicit 0
// included, is 422 naming height_units (LOC-06).
func validHeightUnits(h int) error {
	if h < 1 || h > 60 {
		return &api.FieldError{Field: "height_units", Message: "must be between 1 and 60", Status: http.StatusUnprocessableEntity}
	}
	return nil
}

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
	height := DefaultHeightUnits
	if req.HeightUnits != nil {
		height = *req.HeightUnits
	}
	if err := validHeightUnits(height); err != nil {
		api.WriteFieldError(w, err)
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
		HeightUnits:    height,
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
	if req.HeightUnits != nil {
		if err := validHeightUnits(*req.HeightUnits); err != nil {
			api.WriteFieldError(w, err)
			return
		}
	}
	if req.Name != nil && *req.Name == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name must not be empty")
		return
	}
	rk, err := h.repo.UpdateRack(r.Context(), orgID, chi.URLParam(r, "id"), req)
	if err != nil {
		writeRepoError(w, err, "rack not found")
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
		if locations.WriteDependencyConflict(w, r, err) {
			return
		}
		api.WriteError(w, http.StatusNotFound, "Not Found", "rack not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Mounts ---

// Layout is the occupancy of a rack (RCK-02): its mounts ordered by
// position and the free units of each face.
type Layout struct {
	Rack   *Rack            `json:"rack"`
	Mounts []RackMount      `json:"mounts"`
	Free   map[string][]int `json:"free"`
}

// maxLayoutMounts bounds the mounts of a layout: a rack has at most 60
// units on two faces.
const maxLayoutMounts = 200

// GetLayout handles GET /api/v1/racks/{id}/layout.
func (h *Handler) GetLayout(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	rk, err := h.repo.GetRack(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		writeRepoError(w, err, "rack not found")
		return
	}
	mounts, _, err := h.repo.ListMounts(r.Context(), orgID, rk.ID, api.PaginationParams{Limit: maxLayoutMounts})
	if err != nil {
		writeRepoError(w, err, "rack not found")
		return
	}
	sort.Slice(mounts, func(i, j int) bool { return mounts[i].PositionU < mounts[j].PositionU })
	api.WriteJSON(w, http.StatusOK, Layout{Rack: rk, Mounts: mounts, Free: freeUnits(rk.HeightUnits, mounts)})
}

// freeUnits lists the units of each face no mount occupies; a mount on
// both faces occupies front and rear.
func freeUnits(height int, mounts []RackMount) map[string][]int {
	free := map[string][]int{}
	for _, face := range []string{"front", "rear"} {
		taken := map[int]bool{}
		for i := range mounts {
			if facesOverlap(face, mounts[i].Face) {
				for u := mounts[i].PositionU; u < mounts[i].PositionU+mounts[i].HeightU; u++ {
					taken[u] = true
				}
			}
		}
		units := []int{}
		for u := 1; u <= height; u++ {
			if !taken[u] {
				units = append(units, u)
			}
		}
		free[face] = units
	}
	return free
}

// PutMountRequest places a CI in a rack (RCK-02).
type PutMountRequest struct {
	PositionU int    `json:"position_u"`
	HeightU   int    `json:"height_u,omitempty"`
	Face      string `json:"face,omitempty"`
}

// PutMount handles PUT /api/v1/racks/{id}/mounts/{ci_id}: it mounts the CI
// at the position (201) or moves its mount there (200). A collision or a
// position beyond the rack height is 409.
func (h *Handler) PutMount(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req PutMountRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.HeightU == 0 {
		req.HeightU = 1
	}
	if req.Face == "" {
		req.Face = "front"
	}
	if !ValidFaces[req.Face] {
		api.WriteFieldError(w, &api.FieldError{Field: "face", Message: "must be front, rear or both"})
		return
	}
	rackID, ciID := chi.URLParam(r, "id"), chi.URLParam(r, "ci_id")
	current, err := h.repo.MountByCI(r.Context(), orgID, ciID)
	switch {
	case errors.Is(err, ErrNotFound):
		m := &RackMount{OrganizationID: orgID, RackID: rackID, CIID: ciID, PositionU: req.PositionU, HeightU: req.HeightU, Face: req.Face}
		if cerr := h.repo.CreateMount(r.Context(), m); cerr != nil {
			writeRepoError(w, cerr, "rack not found")
			return
		}
		api.WriteJSON(w, http.StatusCreated, m)
		return
	case err != nil:
		writeRepoError(w, err, "rack not found")
		return
	}
	m, err := h.repo.UpdateMount(r.Context(), orgID, current.ID, UpdateMountRequest{
		RackID: &rackID, PositionU: &req.PositionU, HeightU: &req.HeightU, Face: &req.Face,
	})
	if err != nil {
		writeRepoError(w, err, "rack not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, m)
}

// UnmountCI handles DELETE /api/v1/racks/{id}/mounts/{ci_id}.
func (h *Handler) UnmountCI(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	m, err := h.repo.MountByCI(r.Context(), orgID, chi.URLParam(r, "ci_id"))
	if err != nil || m.RackID != chi.URLParam(r, "id") {
		api.WriteError(w, http.StatusNotFound, "Not Found", "the CI is not mounted in this rack")
		return
	}
	if err = h.repo.DeleteMount(r.Context(), orgID, m.ID); err != nil {
		writeRepoError(w, err, "rack mount not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

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

// ListMountsByQuery handles GET /api/v1/mounts?rack_id=…
func (h *Handler) ListMountsByQuery(w http.ResponseWriter, r *http.Request) {
	rackID := r.URL.Query().Get("rack_id")
	if rackID == "" {
		api.WriteFieldError(w, &api.FieldError{Field: "rack_id", Message: "is required"})
		return
	}
	chi.RouteContext(r.Context()).URLParams.Add("id", rackID)
	h.ListMounts(w, r)
}

// CreateMount handles POST /api/v1/racks/{id}/mounts and POST
// /api/v1/mounts (rack_id in the body)
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
	rackID := chi.URLParam(r, "id")
	if rackID == "" {
		rackID = req.RackID
	}
	if rackID == "" {
		api.WriteFieldError(w, &api.FieldError{Field: "rack_id", Message: "is required"})
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
		RackID:         rackID,
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
		writeRepoError(w, err, "rack mount not found")
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
