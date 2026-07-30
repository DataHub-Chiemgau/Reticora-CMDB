package assignment

import (
	"errors"
	"net/http"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler provides HTTP handlers for assignment endpoints.
type Handler struct {
	repo Repository
}

// NewHandler creates a new assignment handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers assignment routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/assignments", h.List)
	r.Post("/api/v1/assignments", h.Create)
	r.Get("/api/v1/assignments/{id}", h.Get)
	r.Post("/api/v1/assignments/{id}/return", h.Return)
	r.Post("/api/v1/assignments/{id}/transfer", h.Transfer)
	r.Delete("/api/v1/assignments/{id}", h.Delete)
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

	items, total, err := h.repo.List(t.OrganizationID, filter, page)
	if err != nil {
		if errors.Is(err, api.ErrInvalidCursor) {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
			return
		}
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
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
		NextCursor: NextCursor(items, filter, page.Limit),
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
	item, err := h.repo.GetByID(t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "assignment not found")
		return
	}

	api.WriteJSON(w, http.StatusOK, item)
}

// Create handles POST /api/v1/assignments
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
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

	assignmentType := req.AssignmentType
	if assignmentType == "" {
		assignmentType = "assignment"
	}

	a := &Assignment{
		OrganizationID: t.OrganizationID,
		AssetID:        req.AssetID,
		CIID:           req.CIID,
		AssignedTo:     req.AssignedTo,
		AssignedBy:     t.UserID,
		AssignmentType: assignmentType,
		Status:         "active",
		DueDate:        req.DueDate,
		Notes:          req.Notes,
	}

	if err := h.repo.Create(a); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusCreated, a)
}

// Return handles POST /api/v1/assignments/{id}/return
func (h *Handler) Return(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	existing, err := h.repo.GetByID(t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "assignment not found")
		return
	}

	var req ReturnRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	updated := *existing
	updated.Status = "returned"
	updated.ReturnedAt = time.Now().UTC().Format(time.RFC3339)
	updated.ReturnCondition = req.ReturnCondition
	if req.Notes != "" {
		updated.Notes = req.Notes
	}

	if err := h.repo.Update(t.OrganizationID, id, &updated); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusOK, &updated)
}

// Transfer handles POST /api/v1/assignments/{id}/transfer
func (h *Handler) Transfer(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	existing, err := h.repo.GetByID(t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "assignment not found")
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

	// Mark old assignment as transferred
	transferred := *existing
	transferred.Status = "transferred"
	if err := h.repo.Update(t.OrganizationID, id, &transferred); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	// Create new assignment for new assignee
	newAssignment := &Assignment{
		OrganizationID: t.OrganizationID,
		AssetID:        existing.AssetID,
		CIID:           existing.CIID,
		AssignedTo:     req.NewAssignee,
		AssignedBy:     t.UserID,
		AssignmentType: "transfer",
		Status:         "active",
		Notes:          req.Notes,
	}

	if err := h.repo.Create(newAssignment); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusCreated, newAssignment)
}

// Delete handles DELETE /api/v1/assignments/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	if err := h.repo.Delete(t.OrganizationID, id); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "assignment not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
