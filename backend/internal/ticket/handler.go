package ticket

import (
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Handler provides HTTP handlers for ticket endpoints.
type Handler struct {
	repo Repository
}

// NewHandler creates a new ticket handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers ticket routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/tickets", h.List)
	mux.HandleFunc("POST /api/v1/tickets", h.Create)
	mux.HandleFunc("GET /api/v1/tickets/{id}", h.Get)
	mux.HandleFunc("PATCH /api/v1/tickets/{id}", h.Update)
	mux.HandleFunc("DELETE /api/v1/tickets/{id}", h.Delete)
	mux.HandleFunc("POST /api/v1/tickets/{id}/comments", h.AddComment)
	mux.HandleFunc("GET /api/v1/tickets/{id}/comments", h.ListComments)
}

// List handles GET /api/v1/tickets
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	page := api.ParsePagination(r)
	filter := FilterParams{
		Status:     r.URL.Query().Get("status"),
		Priority:   r.URL.Query().Get("priority"),
		Category:   r.URL.Query().Get("category"),
		AssigneeID: r.URL.Query().Get("assignee_id"),
		TeamID:     r.URL.Query().Get("team_id"),
		Search:     r.URL.Query().Get("search"),
		SortBy:     r.URL.Query().Get("sort_by"),
		SortDir:    r.URL.Query().Get("sort_dir"),
	}

	items, total, err := h.repo.List(t.OrganizationID, filter, page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusOK, api.ListResponse[Ticket]{
		Data:    items,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// Get handles GET /api/v1/tickets/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := r.PathValue("id")
	item, err := h.repo.GetByID(t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "ticket not found")
		return
	}

	api.WriteJSON(w, http.StatusOK, item)
}

// Create handles POST /api/v1/tickets
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

	if req.Title == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "title is required")
		return
	}

	priority := req.Priority
	if priority == "" {
		priority = "medium"
	}
	category := req.Category
	if category == "" {
		category = "incident"
	}

	ticket := &Ticket{
		OrganizationID: t.OrganizationID,
		Title:          req.Title,
		Description:    req.Description,
		Status:         "open",
		Priority:       priority,
		Category:       category,
		ReporterID:     t.UserID,
		AssigneeID:     req.AssigneeID,
		TeamID:         req.TeamID,
		RelatedCIID:    req.RelatedCIID,
		RelatedAssetID: req.RelatedAssetID,
		DueDate:        req.DueDate,
		Tags:           req.Tags,
	}
	if ticket.Tags == nil {
		ticket.Tags = []string{}
	}

	if err := h.repo.Create(ticket); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusCreated, ticket)
}

// Update handles PATCH /api/v1/tickets/{id}
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
		api.WriteError(w, http.StatusNotFound, "Not Found", "ticket not found")
		return
	}

	api.WriteJSON(w, http.StatusOK, item)
}

// Delete handles DELETE /api/v1/tickets/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := r.PathValue("id")
	if err := h.repo.Delete(t.OrganizationID, id); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "ticket not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// AddComment handles POST /api/v1/tickets/{id}/comments
func (h *Handler) AddComment(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	ticketID := r.PathValue("id")
	var req CommentRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	if req.Content == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "content is required")
		return
	}

	comment := &Comment{
		OrganizationID: t.OrganizationID,
		TicketID:       ticketID,
		AuthorID:       t.UserID,
		Content:        req.Content,
		IsInternal:     req.IsInternal,
	}

	if err := h.repo.AddComment(comment); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusCreated, comment)
}

// ListComments handles GET /api/v1/tickets/{id}/comments
func (h *Handler) ListComments(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	ticketID := r.PathValue("id")
	page := api.ParsePagination(r)

	comments, total, err := h.repo.ListComments(t.OrganizationID, ticketID, page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusOK, api.ListResponse[Comment]{
		Data:    comments,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}
