package ci

import (
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// EventDispatcher publishes CI lifecycle events.
type EventDispatcher interface {
	Dispatch(orgID, event string, payload any)
}

// Repository defines persistence operations for CIs.
type Repository interface {
	List(orgID string, filter FilterParams, page api.PaginationParams) ([]Item, int, error)
	GetByID(orgID, id string) (*Item, error)
	Create(item *Item) error
	Update(orgID, id string, req UpdateRequest) (*Item, error)
	Delete(orgID, id string) error
}

// Handler provides HTTP handlers for CI endpoints.
type Handler struct {
	repo       Repository
	dispatcher EventDispatcher
}

// NewHandler creates a new CI handler.
func NewHandler(repo Repository, dispatcher ...EventDispatcher) *Handler {
	h := &Handler{repo: repo}
	if len(dispatcher) > 0 {
		h.dispatcher = dispatcher[0]
	}
	return h
}

// RegisterRoutes registers CI routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/cis", h.List)
	mux.HandleFunc("POST /api/v1/cis", h.Create)
	mux.HandleFunc("GET /api/v1/cis/{id}", h.Get)
	mux.HandleFunc("PATCH /api/v1/cis/{id}", h.Update)
	mux.HandleFunc("DELETE /api/v1/cis/{id}", h.Delete)
}

// List handles GET /api/v1/cis
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	page := api.ParsePagination(r)
	filter := FilterParams{
		Status:   r.URL.Query().Get("status"),
		TypeID:   r.URL.Query().Get("ci_type_id"),
		ClientID: r.URL.Query().Get("client_id"),
		Search:   r.URL.Query().Get("search"),
		SortBy:   r.URL.Query().Get("sort_by"),
		SortDir:  r.URL.Query().Get("sort_dir"),
	}

	items, total, err := h.repo.List(t.OrganizationID, filter, page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusOK, api.ListResponse[Item]{
		Data:    items,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// Get handles GET /api/v1/cis/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := r.PathValue("id")
	item, err := h.repo.GetByID(t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "CI not found")
		return
	}

	api.WriteJSON(w, http.StatusOK, item)
}

// Create handles POST /api/v1/cis
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

	if req.Name == "" || req.CITypeID == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name and ci_type_id are required")
		return
	}

	status := req.Status
	if status == "" {
		status = "active"
	}

	item := &Item{
		OrganizationID:  t.OrganizationID,
		ClientID:        req.ClientID,
		CITypeID:        req.CITypeID,
		Name:            req.Name,
		Status:          status,
		Manufacturer:    req.Manufacturer,
		Model:           req.Model,
		SerialNumber:    req.SerialNumber,
		ManagementIP:    req.ManagementIP,
		FirmwareVersion: req.FirmwareVersion,
		Attributes:      req.Attributes,
		Source:          req.Source,
	}
	if item.Attributes == nil {
		item.Attributes = make(map[string]any)
	}
	if item.Source == "" {
		item.Source = "manual"
	}

	if err := h.repo.Create(item); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	if h.dispatcher != nil {
		h.dispatcher.Dispatch(t.OrganizationID, "ci.created", item)
	}

	api.WriteJSON(w, http.StatusCreated, item)
}

// Update handles PATCH /api/v1/cis/{id}
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
		api.WriteError(w, http.StatusNotFound, "Not Found", "CI not found")
		return
	}
	if h.dispatcher != nil {
		h.dispatcher.Dispatch(t.OrganizationID, "ci.updated", item)
	}

	api.WriteJSON(w, http.StatusOK, item)
}

// Delete handles DELETE /api/v1/cis/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := r.PathValue("id")
	item, err := h.repo.GetByID(t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "CI not found")
		return
	}
	if err := h.repo.Delete(t.OrganizationID, id); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "CI not found")
		return
	}
	if h.dispatcher != nil {
		h.dispatcher.Dispatch(t.OrganizationID, "ci.deleted", item)
	}

	w.WriteHeader(http.StatusNoContent)
}
