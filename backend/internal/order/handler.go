package order

import (
	"context"
	"net/http"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// MovementRecorder records order receipts into the inventory movement ledger
// (spec §9: receipt is an auditable movement). Satisfied by the movement
// repository.
type MovementRecorder interface {
	Record(ctx context.Context, m *movement.Movement) error
}

// Handler provides HTTP handlers for the internal ordering module.
type Handler struct {
	repo      Repository
	movements MovementRecorder
}

// NewHandler creates a new order handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// WithMovements attaches the movement ledger so order items received into
// inventory emit receipt movements. Movement failures never fail the order.
func (h *Handler) WithMovements(recorder MovementRecorder) *Handler {
	h.movements = recorder
	return h
}

// RegisterRoutes registers order routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/orders", h.List)
	r.Post("/api/v1/orders", h.Create)
	r.Get("/api/v1/orders/{id}", h.Get)
	r.Patch("/api/v1/orders/{id}", h.Update)
	r.Delete("/api/v1/orders/{id}", h.Delete)
	r.Post("/api/v1/orders/{id}/submit", h.Submit)
	r.Post("/api/v1/orders/{id}/approve", h.Approve)
	r.Post("/api/v1/orders/{id}/reject", h.Reject)
	r.Post("/api/v1/orders/{id}/items", h.AddItem)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page := api.ParsePagination(r)
	filter := FilterParams{
		Status:   r.URL.Query().Get("status"),
		ClientID: r.URL.Query().Get("client_id"),
		Search:   r.URL.Query().Get("search"),
	}
	items, total, err := h.repo.List(r.Context(), t.OrganizationID, filter, page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Order]{
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
		api.WriteError(w, http.StatusNotFound, "Not Found", "order not found")
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
	var req CreateOrderRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "title is required")
		return
	}
	o := &Order{
		OrganizationID: t.OrganizationID,
		ClientID:       req.ClientID,
		OrderNumber:    req.OrderNumber,
		Title:          req.Title,
		Status:         "draft",
		RequestedBy:    t.UserID,
		Supplier:       req.Supplier,
		TotalCost:      req.TotalCost,
		Currency:       req.Currency,
		Notes:          req.Notes,
	}
	if err := h.repo.Create(r.Context(), o); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusCreated, o)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req UpdateOrderRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	item, err := h.repo.Update(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "order not found")
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
		api.WriteError(w, http.StatusNotFound, "Not Found", "order not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// setStatus is the shared transition handler for submit/approve/reject.
func (h *Handler) setStatus(w http.ResponseWriter, r *http.Request, status string) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	item, err := h.repo.SetStatus(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), status, t.UserID)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "order not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

// Submit handles POST /api/v1/orders/{id}/submit (draft → submitted).
func (h *Handler) Submit(w http.ResponseWriter, r *http.Request) { h.setStatus(w, r, "submitted") }

// Approve handles POST /api/v1/orders/{id}/approve (→ approved; records actor).
func (h *Handler) Approve(w http.ResponseWriter, r *http.Request) { h.setStatus(w, r, "approved") }

// Reject handles POST /api/v1/orders/{id}/reject (→ rejected; records actor).
func (h *Handler) Reject(w http.ResponseWriter, r *http.Request) { h.setStatus(w, r, "rejected") }

func (h *Handler) AddItem(w http.ResponseWriter, r *http.Request) {
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
	if strings.TrimSpace(req.Description) == "" || req.Quantity <= 0 {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "description and positive quantity are required")
		return
	}
	item := &Item{
		OrganizationID: t.OrganizationID,
		OrderID:        chi.URLParam(r, "id"),
		Description:    req.Description,
		Quantity:       req.Quantity,
		UnitPrice:      req.UnitPrice,
		ConsumableID:   req.ConsumableID,
		AssetID:        req.AssetID,
	}
	o, err := h.repo.AddItem(r.Context(), item)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "order not found")
		return
	}
	// Receiving an ordered item into inventory is an auditable receipt
	// movement (spec §9). Assets move as serialized units; consumable-linked
	// positions move as quantities.
	if h.movements != nil {
		m := &movement.Movement{
			OrganizationID: t.OrganizationID,
			MovementType:   "receipt",
			OrderID:        item.OrderID,
			Notes:          item.Description,
		}
		if item.AssetID != "" {
			m.ItemKind = "asset"
			m.AssetID = item.AssetID
			_ = h.movements.Record(r.Context(), m)
		} else if item.ConsumableID != "" {
			m.ItemKind = "quantity_item"
			m.QuantityItemID = item.ConsumableID
			qty := item.Quantity
			m.Quantity = &qty
			_ = h.movements.Record(r.Context(), m)
		}
	}
	api.WriteJSON(w, http.StatusCreated, o)
}
