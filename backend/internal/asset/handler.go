package asset

import (
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler provides HTTP handlers for asset endpoints.
type Handler struct {
	repo Repository
}

// NewHandler creates a new asset handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers asset routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/assets", h.List)
	r.Post("/api/v1/assets", h.Create)
	r.Get("/api/v1/assets/{id}", h.Get)
	r.Patch("/api/v1/assets/{id}", h.Update)
	r.Delete("/api/v1/assets/{id}", h.Delete)
}

// List handles GET /api/v1/assets
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	page := api.ParsePagination(r)
	filter := FilterParams{
		Status:   r.URL.Query().Get("status"),
		Category: r.URL.Query().Get("category"),
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

	api.WriteJSON(w, http.StatusOK, api.ListResponse[Asset]{
		Data:    items,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// Get handles GET /api/v1/assets/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	item, err := h.repo.GetByID(t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "asset not found")
		return
	}

	api.WriteJSON(w, http.StatusOK, item)
}

// Create handles POST /api/v1/assets
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

	if req.Name == "" || req.AssetTag == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name and asset_tag are required")
		return
	}

	category := req.Category
	if category == "" {
		category = "hardware"
	}
	status := req.Status
	if status == "" {
		status = "in_stock"
	}
	currency := req.Currency
	if currency == "" {
		currency = "EUR"
	}

	a := &Asset{
		OrganizationID: t.OrganizationID,
		ClientID:       req.ClientID,
		CIID:           req.CIID,
		AssetTag:       req.AssetTag,
		Name:           req.Name,
		Category:       category,
		Status:         status,
		PurchaseDate:   req.PurchaseDate,
		PurchaseCost:   req.PurchaseCost,
		Currency:       currency,
		WarrantyEnd:    req.WarrantyEnd,
		Supplier:       req.Supplier,
		InvoiceNumber:  req.InvoiceNumber,
		SerialNumber:   req.SerialNumber,
		Location:       req.Location,
		Notes:          req.Notes,
		CustomFields:   req.CustomFields,
	}
	if a.CustomFields == nil {
		a.CustomFields = make(map[string]any)
	}

	if err := h.repo.Create(a); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusCreated, a)
}

// Update handles PATCH /api/v1/assets/{id}
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	var req UpdateRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	item, err := h.repo.Update(t.OrganizationID, id, req)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "asset not found")
		return
	}

	api.WriteJSON(w, http.StatusOK, item)
}

// Delete handles DELETE /api/v1/assets/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	if err := h.repo.Delete(t.OrganizationID, id); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "asset not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
