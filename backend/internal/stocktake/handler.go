package stocktake

import (
	"net/http"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler provides HTTP handlers for stocktake endpoints.
type Handler struct {
	repo Repository
}

// NewHandler creates a new stocktake handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers stocktake routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/stocktakes", h.List)
	r.Post("/api/v1/stocktakes", h.Create)
	r.Get("/api/v1/stocktakes/{id}", h.Get)
	r.Patch("/api/v1/stocktakes/{id}", h.Update)
	r.Delete("/api/v1/stocktakes/{id}", h.Delete)
	r.Post("/api/v1/stocktakes/{id}/scans", h.AddScan)
	r.Get("/api/v1/stocktakes/{id}/scans", h.ListScans)
}

// List handles GET /api/v1/stocktakes
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	page := api.ParsePagination(r)
	filter := FilterParams{
		Status:  r.URL.Query().Get("status"),
		Scope:   r.URL.Query().Get("scope"),
		Search:  r.URL.Query().Get("search"),
		SortBy:  r.URL.Query().Get("sort_by"),
		SortDir: r.URL.Query().Get("sort_dir"),
	}

	items, total, err := h.repo.List(t.OrganizationID, filter, page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusOK, api.ListResponse[Stocktake]{
		Data:    items,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// Get handles GET /api/v1/stocktakes/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	item, err := h.repo.GetByID(t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "stocktake not found")
		return
	}

	api.WriteJSON(w, http.StatusOK, item)
}

// Create handles POST /api/v1/stocktakes
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

	scope := req.Scope
	if scope == "" {
		scope = "full"
	}

	s := &Stocktake{
		OrganizationID: t.OrganizationID,
		Title:          req.Title,
		Description:    req.Description,
		Status:         "planned",
		Scope:          scope,
		StartedBy:      t.UserID,
		DueDate:        req.DueDate,
		TotalExpected:  req.TotalExpected,
	}

	if err := h.repo.Create(s); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusCreated, s)
}

// Update handles PATCH /api/v1/stocktakes/{id}
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
		api.WriteError(w, http.StatusNotFound, "Not Found", "stocktake not found")
		return
	}

	api.WriteJSON(w, http.StatusOK, item)
}

// Delete handles DELETE /api/v1/stocktakes/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	if err := h.repo.Delete(t.OrganizationID, id); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "stocktake not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// AddScan handles POST /api/v1/stocktakes/{id}/scans
func (h *Handler) AddScan(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	stocktakeID := chi.URLParam(r, "id")
	var req ScanRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	if req.ScanResult == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "scan_result is required")
		return
	}

	scanMethod := req.ScanMethod
	if scanMethod == "" {
		scanMethod = "manual"
	}

	scan := &StockScan{
		OrganizationID: t.OrganizationID,
		StocktakeID:    stocktakeID,
		AssetID:        req.AssetID,
		CIID:           req.CIID,
		ScannedBy:      t.UserID,
		ScanMethod:     scanMethod,
		ScanResult:     req.ScanResult,
		LocationFound:  req.LocationFound,
		Notes:          req.Notes,
		ScannedAt:      time.Now().UTC(),
	}

	if err := h.repo.AddScan(scan); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusCreated, scan)
}

// ListScans handles GET /api/v1/stocktakes/{id}/scans
func (h *Handler) ListScans(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	stocktakeID := chi.URLParam(r, "id")
	page := api.ParsePagination(r)

	scans, total, err := h.repo.ListScans(t.OrganizationID, stocktakeID, page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusOK, api.ListResponse[StockScan]{
		Data:    scans,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}
