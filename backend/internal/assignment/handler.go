package assignment

import (
	"errors"
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler provides HTTP handlers for assignment endpoints. Reads use the
// repository; every write goes through the assignment service (WP-275),
// without it (profile without database) they answer 501.
type Handler struct {
	repo    Repository
	service *Service
}

// NewHandler creates a new assignment handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// WithService attaches the assignment service (nil: writes answer 501).
func (h *Handler) WithService(service *Service) *Handler {
	h.service = service
	return h
}

// RegisterRoutes registers assignment routes on the given mux. Below
// /api/v1/assignments only the reads remain (E-38, WP-746); every write
// goes through /api/v1/assets/{id}/assign|return|transfer.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/assignments", h.List)
	r.Get("/api/v1/assignments/{id}", h.Get)
	r.Post("/api/v1/assets/{id}/assign", h.AssignAsset)
	r.Post("/api/v1/assets/{id}/return", h.ReturnAsset)
	r.Post("/api/v1/assets/{id}/transfer", h.TransferAsset)
}

// writable answers 401 without tenant and 501 without service, and
// reports whether the write may go on.
func (h *Handler) writable(w http.ResponseWriter, t tenant.TenantInfo) bool {
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return false
	}
	if h.service == nil {
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "assignments need the database")
		return false
	}
	return true
}

// writeServiceError answers a failed write of the assignment service: an
// asset that is not visible or not lockable is 404, a holder conflict 409
// with its problem type, a field error 422 on its field; database errors
// (a holder of another organization: 422) go through api.WriteRepoError.
func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrAssetNotFound), errors.Is(err, movement.ErrAssetNotFound), errors.Is(err, api.ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "asset not found")
	case errors.Is(err, ErrHolderMismatch):
		api.WriteProblem(w, http.StatusConflict, ProblemHolderMismatch, "Conflict",
			"the asset has another holder than expected_holder")
	case errors.Is(err, ErrAlreadyAssigned):
		api.WriteProblem(w, http.StatusConflict, ProblemAssetAssigned, "Conflict",
			"the asset is assigned already; return or transfer it")
	case errors.Is(err, ErrNotAssigned):
		api.WriteProblem(w, http.StatusConflict, ProblemAssetNotAssigned, "Conflict", "the asset is not assigned")
	case errors.Is(err, ErrAssetReserved):
		api.WriteProblem(w, http.StatusConflict, ProblemAssetReserved, "Conflict", ErrAssetReserved.Error())
	default:
		api.WriteRepoError(w, err)
	}
}

// AssignAsset handles POST /api/v1/assets/{id}/assign: 201 with the
// assignment, whose id is the id of the assignment movement.
func (h *Handler) AssignAsset(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if !h.writable(w, t) {
		return
	}
	var req AssignAssetRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	a, err := h.service.Assign(r.Context(), t.OrganizationID, &AssignInput{AssetID: chi.URLParam(r, "id"),
		Holder: Holder{UserID: req.AssignedUserID, TeamID: req.AssignedTeamID}, DueAt: req.DueAt, Notes: req.Notes})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, a)
}

// ReturnAsset handles POST /api/v1/assets/{id}/return: 200 with the
// booked return movement.
func (h *Handler) ReturnAsset(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if !h.writable(w, t) {
		return
	}
	var req ReturnAssetRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	m, err := h.service.Return(r.Context(), t.OrganizationID, &ReturnInput{AssetID: chi.URLParam(r, "id"),
		ExpectedHolder: req.ExpectedHolder, ReturnCondition: req.ReturnCondition, Notes: req.Notes})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, m)
}

// TransferAsset handles POST /api/v1/assets/{id}/transfer: 201 with the
// new assignment.
func (h *Handler) TransferAsset(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if !h.writable(w, t) {
		return
	}
	var req TransferAssetRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	a, err := h.service.Transfer(r.Context(), t.OrganizationID, &TransferInput{AssetID: chi.URLParam(r, "id"),
		ExpectedHolder: req.ExpectedHolder, Holder: Holder{UserID: req.AssignedUserID, TeamID: req.AssignedTeamID},
		DueAt: req.DueAt, Notes: req.Notes})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, a)
}

// List handles GET /api/v1/assignments
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	page := api.ParsePagination(r)
	if page.CursorError != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", page.CursorError.Error())
		return
	}
	filter := FilterParams{
		Status:     r.URL.Query().Get("status"),
		AssignedTo: r.URL.Query().Get("assigned_to"),
		AssetID:    r.URL.Query().Get("asset_id"),
		Search:     r.URL.Query().Get("search"),
		SortBy:     r.URL.Query().Get("sort_by"),
		SortDir:    r.URL.Query().Get("sort_dir"),
	}

	items, total, err := h.repo.List(r.Context(), t.OrganizationID, &filter, page)
	if err != nil {
		if errors.Is(err, api.ErrInvalidCursor) {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
			return
		}
		api.WriteRepoError(w, err)
		return
	}

	hasMore := page.Offset+page.Limit < total
	if page.Cursor != nil {
		hasMore = len(items) == page.Limit
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Assignment]{
		Data:       items,
		Total:      total,
		Limit:      page.Limit,
		Offset:     page.Offset,
		HasMore:    hasMore,
		NextCursor: NextCursor(items, &filter, page.Limit),
	})
}

// Get handles GET /api/v1/assignments/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	item, err := h.repo.GetByID(r.Context(), t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "assignment not found")
		return
	}

	api.WriteJSON(w, http.StatusOK, item)
}
