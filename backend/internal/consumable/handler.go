package consumable

import (
	"errors"
	"net/http"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler provides HTTP handlers for the consumable stock module.
type Handler struct {
	repo Repository
}

// NewHandler creates a new consumable handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers consumable routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/consumables", h.List)
	r.Post("/api/v1/consumables", h.Create)
	r.Get("/api/v1/consumables/{id}", h.Get)
	r.Patch("/api/v1/consumables/{id}", h.Update)
	r.Delete("/api/v1/consumables/{id}", h.Delete)
	r.Get("/api/v1/consumables/{id}/stock", h.ListStock)
	r.Get("/api/v1/consumables/{id}/movements", h.ListMovements)
	r.Post("/api/v1/consumables/{id}/movements", h.AddMovement)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page := api.ParsePagination(r)
	filter := FilterParams{
		Category: r.URL.Query().Get("category"),
		ClientID: r.URL.Query().Get("client_id"),
		LowStock: r.URL.Query().Get("low_stock") == "true",
		Search:   r.URL.Query().Get("search"),
	}
	items, total, err := h.repo.List(r.Context(), t.OrganizationID, filter, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Consumable]{
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
		api.WriteError(w, http.StatusNotFound, "Not Found", "consumable not found")
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
	var req CreateConsumableRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name is required")
		return
	}
	c := &Consumable{
		OrganizationID: t.OrganizationID,
		ClientID:       req.ClientID,
		Name:           req.Name,
		SKU:            req.SKU,
		Barcode:        req.Barcode,
		SupplierID:     req.SupplierID,
		Category:       req.Category,
		Unit:           req.Unit,
		MinLevel:       req.MinLevel,
		Notes:          req.Notes,
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
	var req UpdateConsumableRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	item, err := h.repo.Update(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req)
	if err != nil {
		// A client that does not match the bins of the stock (23514), a
		// taken barcode (23505) or an unknown supplier (23503) is answered
		// by api.WriteDBError; everything else reads as not found.
		if !errors.Is(err, ErrNotFound) && api.WriteDBError(w, err) {
			return
		}
		api.WriteError(w, http.StatusNotFound, "Not Found", "consumable not found")
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
		// Movements of the ledger keep the consumable (RESTRICT, WP-744):
		// 409 with dependencies ["stock_movement"] (WP-274).
		if movement.IsLedgerReference(err) {
			movement.WriteLedgerDependency(w, r)
			return
		}
		// Stock in a bin keeps the consumable (WP-283), also a row of a
		// bin the caller cannot see (RESTRICT of stock_level).
		if errors.Is(err, ErrHasStock) || isStockReference(err) {
			api.WriteError(w, http.StatusConflict, "Conflict", "the consumable still has stock in a bin")
			return
		}
		api.WriteError(w, http.StatusNotFound, "Not Found", "consumable not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ListMovements(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListMovements(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Movement]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// ListStock answers GET /api/v1/consumables/{id}/stock: the stock per bin
// visible to the caller, cursor-paginated (API-02).
func (h *Handler) ListStock(w http.ResponseWriter, r *http.Request) {
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
	rows, total, err := h.repo.ListStock(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), page)
	switch {
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "consumable not found")
		return
	case errors.Is(err, api.ErrInvalidCursor):
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid cursor")
		return
	case err != nil:
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, StockPage(rows, total, page))
}

// AddMovement answers POST /api/v1/consumables/{id}/movements, the
// transitional path until WP-756: in is a receipt into the bin location_id,
// out a retrieval from it; without location_id the only bin of the
// consumable. A shortfall is 409 insufficient-stock, a missing, foreign or
// non-bin location 422 on location_id.
func (h *Handler) AddMovement(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req CreateMovementRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Direction != "in" && req.Direction != "out" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "direction must be in or out")
		return
	}
	if req.Quantity <= 0 {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "quantity must be positive")
		return
	}
	m := &Movement{
		OrganizationID: t.OrganizationID,
		ConsumableID:   chi.URLParam(r, "id"),
		Direction:      req.Direction,
		LocationID:     req.LocationID,
		Quantity:       req.Quantity,
		Reason:         req.Reason,
		Reference:      req.Reference,
		ActorID:        t.UserID,
	}
	c, err := h.repo.AddMovement(r.Context(), m)
	if err != nil {
		var field *api.FieldError
		switch {
		case IsInsufficientStock(err):
			api.WriteTypedProblem(w, http.StatusConflict, err)
		case errors.Is(err, ErrNotFound):
			api.WriteError(w, http.StatusNotFound, "Not Found", "consumable not found")
		case errors.As(err, &field) && (field.Field == "to_location_id" || field.Field == "from_location_id"):
			// The bin of the transitional path is location_id.
			api.WriteFieldError(w, &api.FieldError{Field: "location_id", Message: field.Message, Status: field.Status})
		default:
			api.WriteRepoError(w, err)
		}
		return
	}
	api.WriteJSON(w, http.StatusCreated, map[string]any{"movement": m, "consumable": c})
}
