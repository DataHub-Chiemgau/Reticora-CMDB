package stocktake

import (
	"errors"
	"net/http"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
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
	r.Get("/api/v1/stocktakes/{id}/difference", h.Difference)
	r.Post("/api/v1/stocktakes/{id}/complete", h.Complete)
	r.Post("/api/v1/stocktakes/{id}/start", h.Start)
	r.Get("/api/v1/stocktakes/{id}/items", h.ListItems)
	r.Post("/api/v1/stocktakes/{id}/cancel", h.Cancel)
}

// orgWideOrForbidden answers 403 and reports false when the request's
// principal is restricted in the client or site scope (F-365): a stocktake
// without area is a row of the whole organization, so only an org-wide
// principal changes, starts, cancels, completes or deletes it (E-09); the
// repository refuses the write as well. Without a tenant scope the
// repository fails closed.
func orgWideOrForbidden(w http.ResponseWriter, r *http.Request) bool {
	if scope, ok := database.TenantScopeFromContext(r.Context()); ok && (!scope.Clients.IsAll() || !scope.Sites.IsAll()) {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "only a principal with an organization-wide scope may change a stocktake")
		return false
	}
	return true
}

// writable reads the stocktake id before a write and reports whether the
// write may go on (WP-848): a stocktake the principal cannot see answers
// 404; one without area answers 403 to a principal restricted in the client
// or site scope (orgWideOrForbidden). A stocktake with area is left to the
// repository and the restrictive policies of its client and site.
func (h *Handler) writable(w http.ResponseWriter, r *http.Request, orgID, id string) bool {
	st, err := h.repo.GetByID(r.Context(), orgID, id)
	if err != nil {
		writeStocktakeError(w, err)
		return false
	}
	if st.LocationID == "" {
		return orgWideOrForbidden(w, r)
	}
	return true
}

// writeStocktakeError answers the sentinel errors of the repository with
// their status and any other error through api.WriteRepoError.
func writeStocktakeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "stocktake not found")
	case errors.Is(err, ErrInvalidTransition):
		api.WriteError(w, http.StatusConflict, "Conflict", "the status of the stocktake does not allow this transition")
	case errors.Is(err, ErrAlreadyCompleted):
		api.WriteError(w, http.StatusConflict, "Conflict", "stocktake already completed or cancelled")
	case errors.Is(err, ErrNoArea):
		api.WriteFieldError(w, &api.FieldError{Field: "location_id", Message: "the stocktake has no area to start",
			Status: http.StatusUnprocessableEntity})
	case errors.Is(err, ErrSnapshotUnavailable):
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "the expected stock is not available without PostgreSQL")
	case errors.Is(err, api.ErrInvalidCursor):
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid cursor")
	default:
		api.WriteRepoError(w, err)
	}
}

// checkLocation answers 422 on location_id and reports false when value is
// set and no UUID.
func checkLocation(w http.ResponseWriter, value string) bool {
	if value == "" {
		return true
	}
	if err := api.RequireUUID("location_id", value); err != nil {
		api.WriteFieldError(w, err)
		return false
	}
	return true
}

// List handles GET /api/v1/stocktakes
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
		Status:  r.URL.Query().Get("status"),
		Scope:   r.URL.Query().Get("scope"),
		Search:  r.URL.Query().Get("search"),
		SortBy:  r.URL.Query().Get("sort_by"),
		SortDir: r.URL.Query().Get("sort_dir"),
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
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Stocktake]{
		Data:       items,
		Total:      total,
		Limit:      page.Limit,
		Offset:     page.Offset,
		HasMore:    hasMore,
		NextCursor: NextCursor(items, &filter, page.Limit),
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
	item, err := h.repo.GetByID(r.Context(), t.OrganizationID, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			api.WriteError(w, http.StatusNotFound, "Not Found", "stocktake not found")
			return
		}
		api.WriteRepoError(w, err)
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

	if !checkLocation(w, req.LocationID) {
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
		Status:         StatusPlanned,
		Scope:          scope,
		LocationID:     req.LocationID,
		DueDate:        req.DueDate,
	}

	if err := h.repo.Create(r.Context(), s); err != nil {
		api.WriteRepoError(w, err)
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
	if !h.writable(w, r, t.OrganizationID, id) {
		return
	}
	var req UpdateRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.LocationID != nil && !checkLocation(w, *req.LocationID) {
		return
	}

	item, err := h.repo.Update(r.Context(), t.OrganizationID, id, req)
	if err != nil {
		writeStocktakeError(w, err)
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
	if !h.writable(w, r, t.OrganizationID, id) {
		return
	}
	if err := h.repo.Delete(r.Context(), t.OrganizationID, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			api.WriteError(w, http.StatusNotFound, "Not Found", "stocktake not found")
			return
		}
		api.WriteRepoError(w, err)
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

	if err := h.repo.AddScan(r.Context(), scan); err != nil {
		if errors.Is(err, ErrNotFound) {
			api.WriteError(w, http.StatusNotFound, "Not Found", "stocktake not found")
			return
		}
		api.WriteRepoError(w, err)
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

	scans, total, err := h.repo.ListScans(r.Context(), t.OrganizationID, stocktakeID, page)
	if err != nil {
		api.WriteRepoError(w, err)
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

// Difference handles GET /api/v1/stocktakes/{id}/difference and returns the
// scans that deviate from the expected inventory, enriched with the affected
// asset where the scan resolved to one.
func (h *Handler) Difference(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	stocktakeID := chi.URLParam(r, "id")
	page := api.ParsePagination(r)

	entries, total, err := h.repo.Difference(r.Context(), t.OrganizationID, stocktakeID, page)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			api.WriteError(w, http.StatusNotFound, "Not Found", "stocktake not found")
			return
		}
		api.WriteRepoError(w, err)
		return
	}

	api.WriteJSON(w, http.StatusOK, api.ListResponse[DifferenceEntry]{
		Data:    entries,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// Complete handles POST /api/v1/stocktakes/{id}/complete. It finalizes the
// stocktake and, unless apply_corrections is false, applies the recorded
// differences to the inventory (missing → lost, surplus → in_stock,
// damaged → maintenance, wrong_location → location update).
func (h *Handler) Complete(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	stocktakeID := chi.URLParam(r, "id")
	if !h.writable(w, r, t.OrganizationID, stocktakeID) {
		return
	}
	applyCorrections := true
	if r.Body != nil && r.ContentLength != 0 {
		var req CompleteRequest
		if err := api.ReadJSON(r, &req); err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
			return
		}
		if req.ApplyCorrections != nil {
			applyCorrections = *req.ApplyCorrections
		}
	}

	completion, err := h.repo.Complete(r.Context(), t.OrganizationID, stocktakeID, applyCorrections)
	if err != nil {
		if errors.Is(err, ErrAlreadyCompleted) {
			api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
			return
		}
		if errors.Is(err, ErrNotFound) {
			api.WriteError(w, http.StatusNotFound, "Not Found", "stocktake not found")
			return
		}
		api.WriteRepoError(w, err)
		return
	}

	api.WriteJSON(w, http.StatusOK, completion)
}

// Start handles POST /api/v1/stocktakes/{id}/start (planned → in_progress,
// WP-848): it freezes the expected stock of the area. Without area it
// answers 422 on location_id, for another status 409; the in-memory
// repository answers 501.
func (h *Handler) Start(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	if !h.writable(w, r, t.OrganizationID, id) {
		return
	}
	item, err := h.repo.Start(r.Context(), t.OrganizationID, id, t.UserID)
	if err != nil {
		writeStocktakeError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

// Cancel handles POST /api/v1/stocktakes/{id}/cancel (planned or
// in_progress → cancelled, WP-848); for another status it answers 409.
func (h *Handler) Cancel(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	if !h.writable(w, r, t.OrganizationID, id) {
		return
	}
	item, err := h.repo.Cancel(r.Context(), t.OrganizationID, id)
	if err != nil {
		writeStocktakeError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

// ListItems handles GET /api/v1/stocktakes/{id}/items, the frozen expected
// stock (WP-848), cursor-paginated by id.
func (h *Handler) ListItems(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	page := api.ParsePagination(r)
	if page.CursorError != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid cursor")
		return
	}
	items, total, err := h.repo.ListItems(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), page)
	if err != nil {
		writeStocktakeError(w, err)
		return
	}
	next := ItemsNextCursor(items, page.Limit)
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Item]{
		Data:       items,
		Total:      total,
		Limit:      page.Limit,
		Offset:     page.Offset,
		HasMore:    next != "",
		NextCursor: next,
	})
}
