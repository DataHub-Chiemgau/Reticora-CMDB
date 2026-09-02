package movement

import (
	"context"
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// EventDispatcher publishes movement events to webhook subscribers.
type EventDispatcher interface {
	Dispatch(ctx context.Context, orgID, event string, payload any)
}

// Handler provides HTTP handlers for the movement ledger and quantity items.
type Handler struct {
	repo       Repository
	dispatcher EventDispatcher
}

// NewHandler creates a new movement handler.
func NewHandler(repo Repository, dispatcher ...EventDispatcher) *Handler {
	h := &Handler{repo: repo}
	if len(dispatcher) > 0 {
		h.dispatcher = dispatcher[0]
	}
	return h
}

// RegisterRoutes registers movement routes on the given mux. The ledger is
// append-only: no update or delete routes exist by design (spec §9).
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/stock-movements", h.ListMovements)
	r.Post("/api/v1/stock-movements", h.Record)
	r.Get("/api/v1/assets/{id}/movements", h.ListAssetMovements)
	r.Get("/api/v1/inventory/items", h.ListItems)
	r.Post("/api/v1/inventory/items", h.CreateItem)
	r.Get("/api/v1/inventory/items/{id}", h.GetItem)
	r.Patch("/api/v1/inventory/items/{id}", h.UpdateItem)
	r.Delete("/api/v1/inventory/items/{id}", h.DeleteItem)
}

func (h *Handler) dispatch(r *http.Request, orgID, event string, payload any) {
	if h.dispatcher != nil {
		h.dispatcher.Dispatch(r.Context(), orgID, event, payload)
	}
}

// ListMovements handles GET /api/v1/stock-movements
func (h *Handler) ListMovements(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page := api.ParsePagination(r)
	filter := MovementFilter{
		AssetID:        r.URL.Query().Get("asset_id"),
		QuantityItemID: r.URL.Query().Get("quantity_item_id"),
		MovementType:   r.URL.Query().Get("movement_type"),
		LocationID:     r.URL.Query().Get("location_id"),
	}
	items, total, err := h.repo.ListMovements(r.Context(), t.OrganizationID, filter, page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Movement]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// ListAssetMovements handles GET /api/v1/assets/{id}/movements
func (h *Handler) ListAssetMovements(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListMovements(r.Context(), t.OrganizationID,
		MovementFilter{AssetID: chi.URLParam(r, "id")}, page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Movement]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// Record handles POST /api/v1/stock-movements
func (h *Handler) Record(w http.ResponseWriter, r *http.Request) {
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
	if !MovementTypes[req.MovementType] {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid movement_type")
		return
	}
	if req.AssetID == "" && req.QuantityItemID == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "asset_id or quantity_item_id is required")
		return
	}
	if req.QuantityItemID != "" && req.Quantity == nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "quantity is required for quantity items")
		return
	}
	m := &Movement{
		OrganizationID: t.OrganizationID,
		ItemKind:       req.ItemKind,
		AssetID:        req.AssetID,
		QuantityItemID: req.QuantityItemID,
		MovementType:   req.MovementType,
		FromLocationID: req.FromLocationID,
		ToLocationID:   req.ToLocationID,
		Quantity:       req.Quantity,
		Reason:         req.Reason,
		TicketID:       req.TicketID,
		OrderID:        req.OrderID,
		WorkflowRunID:  req.WorkflowRunID,
		DocumentID:     req.DocumentID,
		Notes:          req.Notes,
	}
	if p, ok := identity.PrincipalFromContext(r.Context()); ok {
		m.ActorID = p.Subject
	}
	if err := h.repo.Record(r.Context(), m); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	h.dispatch(r, t.OrganizationID, "inventory.movement", m)
	api.WriteJSON(w, http.StatusCreated, m)
}

// ListItems handles GET /api/v1/inventory/items
func (h *Handler) ListItems(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListItems(r.Context(), t.OrganizationID, page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[QuantityItem]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// GetItem handles GET /api/v1/inventory/items/{id}
func (h *Handler) GetItem(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	item, err := h.repo.GetItem(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "inventory item not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

// CreateItem handles POST /api/v1/inventory/items
func (h *Handler) CreateItem(w http.ResponseWriter, r *http.Request) {
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
	if req.Name == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name is required")
		return
	}
	item := &QuantityItem{
		OrganizationID: t.OrganizationID,
		ClientID:       req.ClientID,
		SKU:            req.SKU,
		Name:           req.Name,
		Category:       req.Category,
		Unit:           req.Unit,
		StockLevel:     req.StockLevel,
		MinLevel:       req.MinLevel,
		LocationID:     req.LocationID,
		Notes:          req.Notes,
		Attributes:     req.Attributes,
	}
	if err := h.repo.CreateItem(r.Context(), item); err != nil {
		api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusCreated, item)
}

// UpdateItem handles PATCH /api/v1/inventory/items/{id}
func (h *Handler) UpdateItem(w http.ResponseWriter, r *http.Request) {
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
	item, err := h.repo.UpdateItem(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "inventory item not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

// DeleteItem handles DELETE /api/v1/inventory/items/{id}
func (h *Handler) DeleteItem(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if err := h.repo.DeleteItem(r.Context(), t.OrganizationID, chi.URLParam(r, "id")); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "inventory item not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
