package document

import (
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Handler provides HTTP handlers for document endpoints.
type Handler struct {
	repo Repository
}

// NewHandler creates a new document handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers document routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/documents", h.List)
	mux.HandleFunc("POST /api/v1/documents", h.Create)
	mux.HandleFunc("GET /api/v1/documents/{id}", h.Get)
	mux.HandleFunc("PATCH /api/v1/documents/{id}", h.Update)
	mux.HandleFunc("DELETE /api/v1/documents/{id}", h.Delete)
	mux.HandleFunc("POST /api/v1/documents/{id}/links", h.Link)
	mux.HandleFunc("GET /api/v1/documents/{id}/links", h.GetLinks)
}

// List handles GET /api/v1/documents
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	page := api.ParsePagination(r)
	filter := FilterParams{
		Category: r.URL.Query().Get("category"),
		Search:   r.URL.Query().Get("search"),
		SortBy:   r.URL.Query().Get("sort_by"),
		SortDir:  r.URL.Query().Get("sort_dir"),
	}

	items, total, err := h.repo.List(t.OrganizationID, filter, page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusOK, api.ListResponse[Document]{
		Data:    items,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// Get handles GET /api/v1/documents/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := r.PathValue("id")
	item, err := h.repo.GetByID(t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "document not found")
		return
	}

	api.WriteJSON(w, http.StatusOK, item)
}

// Create handles POST /api/v1/documents
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

	if req.Title == "" || req.FileName == "" || req.StorageKey == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "title, file_name, and storage_key are required")
		return
	}

	category := req.Category
	if category == "" {
		category = "general"
	}
	mimeType := req.MimeType
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	d := &Document{
		OrganizationID: t.OrganizationID,
		Title:          req.Title,
		Description:    req.Description,
		FileName:       req.FileName,
		FileSize:       req.FileSize,
		MimeType:       mimeType,
		StorageKey:     req.StorageKey,
		Category:       category,
		Tags:           req.Tags,
		UploadedBy:     t.UserID,
	}
	if d.Tags == nil {
		d.Tags = []string{}
	}

	if err := h.repo.Create(d); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusCreated, d)
}

// Update handles PATCH /api/v1/documents/{id}
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := r.PathValue("id")
	var req UpdateRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	item, err := h.repo.Update(t.OrganizationID, id, req)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "document not found")
		return
	}

	api.WriteJSON(w, http.StatusOK, item)
}

// Delete handles DELETE /api/v1/documents/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := r.PathValue("id")
	if err := h.repo.Delete(t.OrganizationID, id); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "document not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// Link handles POST /api/v1/documents/{id}/links
func (h *Handler) Link(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	docID := r.PathValue("id")
	// Verify document exists
	if _, err := h.repo.GetByID(t.OrganizationID, docID); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "document not found")
		return
	}

	var req LinkRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	if req.EntityType == "" || req.EntityID == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "entity_type and entity_id are required")
		return
	}

	link := &DocumentLink{
		OrganizationID: t.OrganizationID,
		DocumentID:     docID,
		EntityType:     req.EntityType,
		EntityID:       req.EntityID,
	}

	if err := h.repo.LinkDocument(link); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusCreated, link)
}

// GetLinks handles GET /api/v1/documents/{id}/links
func (h *Handler) GetLinks(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	docID := r.PathValue("id")
	links, err := h.repo.GetLinks(t.OrganizationID, docID)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusOK, links)
}
