package contact

import (
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler exposes HTTP endpoints for contacts and CI-contact links.
type Handler struct {
	repo Repository
}

// NewHandler creates a new contact handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers contact routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/contacts", h.List)
	r.Post("/api/v1/contacts", h.Create)
	r.Get("/api/v1/contacts/{id}", h.Get)
	r.Patch("/api/v1/contacts/{id}", h.Update)
	r.Delete("/api/v1/contacts/{id}", h.Delete)

	r.Get("/api/v1/cis/{id}/contacts", h.ListForCI)
	r.Post("/api/v1/cis/{id}/contacts", h.Link)
	r.Delete("/api/v1/ci-contacts/{id}", h.Unlink)
}

func org(w http.ResponseWriter, r *http.Request) (string, bool) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return "", false
	}
	return t.OrganizationID, true
}

// List handles GET /api/v1/contacts
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.List(r.Context(), orgID, r.URL.Query().Get("client_id"), page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Contact]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// Create handles POST /api/v1/contacts
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req CreateContactRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.DisplayName == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "display_name is required")
		return
	}
	c := &Contact{
		OrganizationID: orgID,
		ClientID:       req.ClientID,
		DisplayName:    req.DisplayName,
		Email:          req.Email,
		Phone:          req.Phone,
		Role:           req.Role,
		Department:     req.Department,
		Notes:          req.Notes,
	}
	if err := h.repo.Create(r.Context(), c); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusCreated, c)
}

// Get handles GET /api/v1/contacts/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	c, err := h.repo.GetByID(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "contact not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, c)
}

// Update handles PATCH /api/v1/contacts/{id}
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req UpdateContactRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	c, err := h.repo.Update(r.Context(), orgID, chi.URLParam(r, "id"), req)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "contact not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, c)
}

// Delete handles DELETE /api/v1/contacts/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	if err := h.repo.Delete(r.Context(), orgID, chi.URLParam(r, "id")); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "contact not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListForCI handles GET /api/v1/cis/{id}/contacts
func (h *Handler) ListForCI(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListForCI(r.Context(), orgID, chi.URLParam(r, "id"), page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[CIContact]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// Link handles POST /api/v1/cis/{id}/contacts
func (h *Handler) Link(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req LinkContactRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.ContactID == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "contact_id is required")
		return
	}
	relType := req.RelationshipType
	if relType == "" {
		relType = "responsible"
	}
	if !ValidRelationshipTypes[relType] {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid relationship_type")
		return
	}
	link := &CIContact{
		OrganizationID:   orgID,
		CIID:             chi.URLParam(r, "id"),
		ContactID:        req.ContactID,
		RelationshipType: relType,
	}
	if err := h.repo.Link(r.Context(), link); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusCreated, link)
}

// Unlink handles DELETE /api/v1/ci-contacts/{id}
func (h *Handler) Unlink(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	if err := h.repo.Unlink(r.Context(), orgID, chi.URLParam(r, "id")); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "ci-contact link not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
