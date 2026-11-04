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

// RegisterRoutes registers assignment routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/assignments", h.List)
	r.Post("/api/v1/assignments", h.Create)
	r.Get("/api/v1/assignments/{id}", h.Get)
	r.Post("/api/v1/assignments/{id}/return", h.Return)
	r.Post("/api/v1/assignments/{id}/transfer", h.Transfer)
	r.Delete("/api/v1/assignments/{id}", h.Delete)
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

// assetOf is the asset a legacy assignment row books on: its asset_id,
// else the asset of its CI (ci.asset_id); a CI without asset is 422.
func (h *Handler) assetOf(r *http.Request, orgID, assetID, ciID string) (string, error) {
	if assetID != "" {
		return assetID, nil
	}
	id, err := h.service.AssetOfCI(r.Context(), orgID, ciID)
	if errors.Is(err, api.ErrNotFound) || (err == nil && id == "") {
		return "", &api.FieldError{Field: "ci_id", Message: "the CI has no asset to assign", Status: http.StatusUnprocessableEntity}
	}
	return id, err
}

// openRow loads the legacy assignment row id; it answers 404 when it is
// not visible and 409 when it is closed already, and reports whether the
// request may go on.
func (h *Handler) openRow(w http.ResponseWriter, r *http.Request, orgID string) (*Assignment, bool) {
	existing, err := h.repo.GetByID(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "assignment not found")
		return nil, false
	}
	if existing.Status != "active" && existing.Status != "overdue" {
		writeServiceError(w, ErrNotAssigned)
		return nil, false
	}
	return existing, true
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

// Create handles POST /api/v1/assignments (until WP-746): the assignment
// service assigns the asset of asset_id, or of the CI of ci_id, to the
// user assigned_to.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if !h.writable(w, t) {
		return
	}

	var req CreateRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	if req.AssignedTo == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "assigned_to is required")
		return
	}
	if req.AssetID == "" && req.CIID == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "asset_id or ci_id is required")
		return
	}
	assetID, err := h.assetOf(r, t.OrganizationID, req.AssetID, req.CIID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	a, err := h.service.Assign(r.Context(), t.OrganizationID, &AssignInput{AssetID: assetID,
		Holder: Holder{UserID: req.AssignedTo}, DueDate: req.DueDate, Notes: req.Notes,
		CIID: req.CIID, Type: req.AssignmentType, Signature: req.Signature})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, a)
}

// Return handles POST /api/v1/assignments/{id}/return (until WP-746): the
// assignment service takes the asset back from the holder of the row; the
// return signature lands on the closed rows.
func (h *Handler) Return(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if !h.writable(w, t) {
		return
	}
	existing, ok := h.openRow(w, r, t.OrganizationID)
	if !ok {
		return
	}

	var req ReturnRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	assetID, err := h.assetOf(r, t.OrganizationID, existing.AssetID, existing.CIID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if _, err = h.service.Return(r.Context(), t.OrganizationID, &ReturnInput{AssetID: assetID, ExpectedHolder: existing.AssignedTo,
		ReturnCondition: req.ReturnCondition, Notes: req.Notes, Signature: req.Signature}); err != nil {
		writeServiceError(w, err)
		return
	}
	updated, err := h.repo.GetByID(r.Context(), t.OrganizationID, existing.ID)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, updated)
}

// Transfer handles POST /api/v1/assignments/{id}/transfer (until WP-746):
// the assignment service transfers the asset from the holder of the row to
// the user new_assignee.
func (h *Handler) Transfer(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if !h.writable(w, t) {
		return
	}
	existing, ok := h.openRow(w, r, t.OrganizationID)
	if !ok {
		return
	}

	var req TransferRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	if req.NewAssignee == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "new_assignee is required")
		return
	}
	assetID, err := h.assetOf(r, t.OrganizationID, existing.AssetID, existing.CIID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	a, err := h.service.Transfer(r.Context(), t.OrganizationID, &TransferInput{AssetID: assetID, ExpectedHolder: existing.AssignedTo,
		Holder: Holder{UserID: req.NewAssignee}, Notes: req.Notes})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, a)
}

// Delete handles DELETE /api/v1/assignments/{id}: 409, the history of
// assignments is append-only (return or transfer the asset instead).
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if !h.writable(w, t) {
		return
	}
	if _, err := h.repo.GetByID(r.Context(), t.OrganizationID, chi.URLParam(r, "id")); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "assignment not found")
		return
	}
	api.WriteProblem(w, http.StatusConflict, ProblemAppendOnly, "Conflict",
		"assignments are append-only history; return or transfer the asset instead")
}
