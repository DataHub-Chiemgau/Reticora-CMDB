package stocktake

import (
	"errors"
	"net/http"
	"slices"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler provides HTTP handlers for stocktake endpoints.
type Handler struct {
	repo    Repository
	service *Service
}

// NewHandler creates a new stocktake handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// WithService sets the completion service with the movement service
// (WP-761); without one (profile --no-db, NewService returns nil) POST
// …/complete answers 501 like start.
func (h *Handler) WithService(svc *Service) *Handler {
	h.service = svc
	return h
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
	// The alias of the earlier release answers until its x-sunset (E-38).
	r.Get("/api/v1/stocktakes/{id}/difference", h.Differences)
	r.Get("/api/v1/stocktakes/{id}/differences", h.Differences)
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
	case errors.Is(err, ErrNoSnapshot):
		api.WriteError(w, http.StatusConflict, "Conflict", "the stocktake has no frozen expected stock")
	case errors.Is(err, ErrObjectNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "scanned object not found")
	case errors.Is(err, ErrPlaceFound):
		api.WriteFieldError(w, &api.FieldError{Field: "location_id", Message: "the place found is no location of the area (a bin for a consumable)",
			Status: http.StatusUnprocessableEntity})
	case errors.Is(err, ErrSnapshotUnavailable):
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "the expected stock is not available without PostgreSQL")
	case errors.Is(err, api.ErrInvalidCursor):
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid cursor")
	case errors.Is(err, consumable.ErrInsufficientStock):
		// A correction would lower the stock of a bin below on_hand −
		// reserved (WP-761); the completion rolled back.
		api.WriteTypedProblem(w, http.StatusConflict, err)
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
		writeStocktakeError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// AddScan handles POST /api/v1/stocktakes/{id}/scans (WP-287): exactly one
// of asset_id and consumable_id (then with quantity > 0) at the place found
// location_id; the server classifies the scan. Scans need an in-progress
// stocktake with snapshot (409).
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
	if err := validateScan(&req); err != nil {
		api.WriteFieldError(w, err)
		return
	}

	scan := &StockScan{
		OrganizationID: t.OrganizationID,
		StocktakeID:    stocktakeID,
		AssetID:        req.AssetID,
		ConsumableID:   req.ConsumableID,
		Quantity:       req.Quantity,
		LocationID:     req.LocationID,
		ScannedBy:      t.UserID,
		ScanMethod:     req.ScanMethod,
		Code:           req.Code,
		Notes:          req.Notes,
	}
	if err := h.repo.AddScan(r.Context(), scan); err != nil {
		writeStocktakeError(w, err)
		return
	}

	api.WriteJSON(w, http.StatusCreated, scan)
}

// validateScan checks the input of a scan and defaults scan_method to
// manual; a violation is a 422 *api.FieldError, a NUL character a 400 one.
func validateScan(req *ScanRequest) error {
	unprocessable := func(field, message string) error {
		return &api.FieldError{Field: field, Message: message, Status: http.StatusUnprocessableEntity}
	}
	switch {
	case (req.AssetID == "") == (req.ConsumableID == ""):
		return unprocessable("asset_id", "exactly one of asset_id and consumable_id is required")
	case req.AssetID != "":
		if err := api.RequireUUID("asset_id", req.AssetID); err != nil {
			return err
		}
		if req.Quantity != nil {
			return unprocessable("quantity", "a scan of an asset takes no quantity")
		}
	default:
		if err := api.RequireUUID("consumable_id", req.ConsumableID); err != nil {
			return err
		}
		if req.Quantity == nil || *req.Quantity <= 0 || *req.Quantity >= 1e10 {
			return unprocessable("quantity", "a scan of a consumable needs a quantity greater than 0")
		}
	}
	if req.LocationID == "" {
		return unprocessable("location_id", "the place found is required")
	}
	if err := api.RequireUUID("location_id", req.LocationID); err != nil {
		return err
	}
	if req.ScanMethod == "" {
		req.ScanMethod = "manual"
	}
	if !slices.Contains(scanMethods, req.ScanMethod) {
		return unprocessable("scan_method", "must be one of manual, barcode, rfid, qrcode")
	}
	if err := api.RejectNUL("code", req.Code); err != nil {
		return err
	}
	return api.RejectNUL("notes", req.Notes)
}

// ListScans handles GET /api/v1/stocktakes/{id}/scans, newest first with a
// keyset cursor over scanned_at and id (WP-287).
func (h *Handler) ListScans(w http.ResponseWriter, r *http.Request) {
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
	scans, total, err := h.repo.ListScans(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), page)
	if err != nil {
		writeStocktakeError(w, err)
		return
	}
	next := ScansNextCursor(scans, page.Limit)
	api.WriteJSON(w, http.StatusOK, api.ListResponse[StockScan]{
		Data:       scans,
		Total:      total,
		Limit:      page.Limit,
		Offset:     page.Offset,
		HasMore:    next != "",
		NextCursor: next,
	})
}

// Differences handles GET /api/v1/stocktakes/{id}/differences and its
// deprecated alias …/difference (E-38, WP-287): the difference list from
// the snapshot and the scans, cursor-paginated. The embedded asset carries
// only the fields the caller may read (permission.Project, Konvention 11).
func (h *Handler) Differences(w http.ResponseWriter, r *http.Request) {
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
	policy, err := permission.PolicyFrom(r.Context())
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	entries, total, err := h.repo.Differences(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), page)
	if err != nil {
		writeStocktakeError(w, err)
		return
	}
	for i := range entries {
		a, ok := entries[i].Asset.(*DifferenceAsset)
		if !ok || a == nil {
			continue
		}
		projected, perr := permission.ProjectValue(policy, permission.KindAsset, a.AssetTypeID,
			permission.ObjectRef{ID: a.ID, ClientID: a.ClientID, SiteID: a.SiteID}, a)
		if perr != nil {
			api.WriteRepoError(w, perr)
			return
		}
		entries[i].Asset = projected
	}
	next := DifferencesNextCursor(entries, page.Limit)
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Difference]{
		Data:       entries,
		Total:      total,
		Limit:      page.Limit,
		Offset:     page.Offset,
		HasMore:    next != "",
		NextCursor: next,
	})
}

// Complete handles POST /api/v1/stocktakes/{id}/complete (in_progress →
// completed, WP-761): in one transaction it fixes the result in the
// snapshot and the totals and books the corrections through the movement
// service (Service.Complete); apply_corrections is deprecated and ignored.
// A stocktake without snapshot (an earlier release without area included)
// answers 409, it can only be cancelled; a refused booking answers 409
// insufficient-stock or 422 and rolls the completion back. Without a
// service (profile --no-db) it follows the status rules and answers 501.
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
	if r.Body != nil && r.ContentLength != 0 {
		var req CompleteRequest
		if err := api.ReadJSON(r, &req); err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
			return
		}
	}
	if h.service == nil {
		writeStocktakeError(w, h.completeUnavailable(r, t.OrganizationID, stocktakeID))
		return
	}

	completion, err := h.service.Complete(r.Context(), t.OrganizationID, stocktakeID, t.UserID)
	if err != nil {
		writeStocktakeError(w, err)
		return
	}

	api.WriteJSON(w, http.StatusOK, completion)
}

// completeUnavailable is the answer of a completion without service: the
// status rule (ErrAlreadyCompleted, ErrInvalidTransition) and then
// ErrSnapshotUnavailable (501), since only PostgreSQL freezes and books.
func (h *Handler) completeUnavailable(r *http.Request, orgID, id string) error {
	st, err := h.repo.GetByID(r.Context(), orgID, id)
	if err != nil {
		return err
	}
	if ruleErr := completeRule(st); ruleErr != nil && !errors.Is(ruleErr, ErrNoSnapshot) {
		return ruleErr
	}
	return ErrSnapshotUnavailable
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
