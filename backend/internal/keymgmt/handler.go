package keymgmt

import (
	"net/http"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler provides HTTP handlers for key management.
type Handler struct {
	repo Repository
}

// NewHandler creates a new key management handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers key management routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/keys", h.List)
	r.Post("/api/v1/keys", h.Create)
	r.Get("/api/v1/keys/{id}", h.Get)
	r.Patch("/api/v1/keys/{id}", h.Update)
	r.Delete("/api/v1/keys/{id}", h.Delete)
	r.Post("/api/v1/keys/{id}/issue", h.Issue)
	r.Post("/api/v1/keys/{id}/return", h.Return)
	r.Get("/api/v1/keys/{id}/assignments", h.ListAssignments)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page := api.ParsePagination(r)
	filter := FilterParams{
		KeyType:  r.URL.Query().Get("key_type"),
		Status:   r.URL.Query().Get("status"),
		ClientID: r.URL.Query().Get("client_id"),
		Search:   r.URL.Query().Get("search"),
	}
	items, total, err := h.repo.List(r.Context(), t.OrganizationID, filter, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Item]{
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
		api.WriteError(w, http.StatusNotFound, "Not Found", "key item not found")
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
	var req CreateItemRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name is required")
		return
	}
	item := &Item{
		OrganizationID: t.OrganizationID,
		ClientID:       req.ClientID,
		Name:           req.Name,
		KeyType:        req.KeyType,
		Identifier:     req.Identifier,
		Location:       req.Location,
		Notes:          req.Notes,
	}
	if err := h.repo.Create(r.Context(), item); err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, item)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req UpdateItemRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	item, err := h.repo.Update(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "key item not found")
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
		api.WriteError(w, http.StatusNotFound, "Not Found", "key item not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Issue handles POST /api/v1/keys/{id}/issue — hands the key to a user.
func (h *Handler) Issue(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req IssueRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if strings.TrimSpace(req.AssignedTo) == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "assigned_to is required")
		return
	}
	a := &Assignment{
		OrganizationID: t.OrganizationID,
		KeyItemID:      chi.URLParam(r, "id"),
		AssignedTo:     req.AssignedTo,
		IssuedBy:       t.UserID,
		Notes:          req.Notes,
	}
	item, err := h.repo.Issue(r.Context(), a)
	if err != nil {
		if strings.Contains(err.Error(), "not available") {
			api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
			return
		}
		api.WriteError(w, http.StatusNotFound, "Not Found", "key item not found")
		return
	}
	api.WriteJSON(w, http.StatusCreated, map[string]any{"assignment": a, "key_item": item})
}

// Return handles POST /api/v1/keys/{id}/return — marks the key returned.
func (h *Handler) Return(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	item, err := h.repo.Return(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		if strings.Contains(err.Error(), "not issued") {
			api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
			return
		}
		api.WriteError(w, http.StatusNotFound, "Not Found", "key item not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

// ListAssignments handles GET /api/v1/keys/{id}/assignments.
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
