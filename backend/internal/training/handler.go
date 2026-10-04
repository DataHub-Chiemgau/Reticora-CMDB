package training

import (
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler provides HTTP handlers for training management.
type Handler struct {
	repo Repository
}

// NewHandler creates a new training handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers training routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/trainings", h.List)
	r.Post("/api/v1/trainings", h.Create)
	r.Get("/api/v1/trainings/{id}", h.Get)
	r.Patch("/api/v1/trainings/{id}", h.Update)
	r.Delete("/api/v1/trainings/{id}", h.Delete)
	r.Get("/api/v1/trainings/{id}/assignments", h.ListAssignments)
	r.Post("/api/v1/trainings/{id}/assignments", h.Assign)
	r.Post("/api/v1/training-assignments/{id}/complete", h.Complete)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page := api.ParsePagination(r)
	filter := FilterParams{Category: r.URL.Query().Get("category"), Search: r.URL.Query().Get("search")}
	items, total, err := h.repo.List(r.Context(), t.OrganizationID, filter, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Course]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	item, err := h.repo.GetByID(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "training not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req CreateCourseRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "title is required")
		return
	}
	c := &Course{
		OrganizationID: t.OrganizationID,
		Title:          req.Title,
		Description:    req.Description,
		Category:       req.Category,
		ValidityMonths: req.ValidityMonths,
	}
	if err := h.repo.Create(r.Context(), c); err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, c)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req UpdateCourseRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	item, err := h.repo.Update(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "training not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if err := h.repo.Delete(r.Context(), t.OrganizationID, chi.URLParam(r, "id")); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "training not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ListAssignments(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	items, err := h.repo.ListAssignments(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"data": items})
}

// Assign handles POST /api/v1/trainings/{id}/assignments.
func (h *Handler) Assign(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req AssignRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if strings.TrimSpace(req.UserID) == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "user_id is required")
		return
	}
	a := &Assignment{
		OrganizationID: t.OrganizationID,
		TrainingID:     chi.URLParam(r, "id"),
		UserID:         req.UserID,
		Status:         "assigned",
	}
	if req.DueAt != "" {
		parsed, err := time.Parse(time.RFC3339, req.DueAt)
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "due_at must be RFC3339")
			return
		}
		a.DueAt = &parsed
	}
	created, err := h.repo.Assign(r.Context(), a)
	if err != nil {
		if strings.Contains(err.Error(), "already assigned") {
			api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
			return
		}
		api.WriteError(w, http.StatusNotFound, "Not Found", "training not found")
		return
	}
	api.WriteJSON(w, http.StatusCreated, created)
}

// Complete handles POST /api/v1/training-assignments/{id}/complete.
func (h *Handler) Complete(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req CompleteRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	item, err := h.repo.Complete(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req.ProofObjectKey)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "training assignment not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}
