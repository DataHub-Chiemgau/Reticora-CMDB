package order

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler provides HTTP handlers for the internal ordering module.
type Handler struct {
	repo Repository
	// booker books the goods receipt (WP-760); nil answers 503.
	booker Booker
	// runs starts the approval run of a submitted order (WithRunStarter,
	// WP-1108); nil: approve and reject are the direct routes only.
	runs RunStarter
}

// SubmittedEvent is the trigger of the approval workflow of an order
// (WFL-04, WP-1108).
const SubmittedEvent = "order_submitted"

// RunStarter starts the approval workflow of a submitted order (WFL-04,
// WP-1108) without this package importing the workflow engine; the router
// wires the adapter of the executor.
type RunStarter interface {
	// StartApproval starts, in the transaction of ctx, the run of the
	// active definition with the trigger order_submitted over payload; runID
	// is empty without such a definition. after executes the run once the
	// transaction committed.
	StartApproval(ctx context.Context, orgID string, payload map[string]any) (runID string, after func(ctx context.Context), err error)
	// RunOpen reports whether the run has not ended yet.
	RunOpen(ctx context.Context, orgID, runID string) (bool, error)
}

// WithRunStarter makes the submit start the approval workflow of the order
// in its transaction (WP-1108).
func (h *Handler) WithRunStarter(runs RunStarter) *Handler {
	h.runs = runs
	return h
}

// ErrApprovalRunOpen: approve or reject while the approval workflow of the
// order runs (409, WP-1108).
var ErrApprovalRunOpen = errors.New("the order is decided by its approval workflow run")

// NewHandler creates a new order handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
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
	r.Post("/api/v1/orders/{id}/place", h.Place)
	r.Post("/api/v1/orders/{id}/cancel", h.Cancel)
	r.Post("/api/v1/orders/{id}/close", h.Close)
	r.Post("/api/v1/orders/{id}/items", h.AddItem)
	r.Post("/api/v1/orders/{id}/receive", h.Receive)
}

// writeError maps the repository errors: a refused transition or a change
// outside draft is 409, the four-eyes rule 403; everything else goes
// through api.WriteRepoError (404, 422 field problems, sanitized database
// errors).
func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidTransition):
		api.WriteError(w, http.StatusConflict, "Conflict", ErrInvalidTransition.Error())
	case errors.Is(err, ErrNotReceivable):
		api.WriteError(w, http.StatusConflict, "Conflict", ErrNotReceivable.Error())
	case errors.Is(err, ErrNotDraft):
		api.WriteError(w, http.StatusConflict, "Conflict", ErrNotDraft.Error())
	case errors.Is(err, ErrFourEyes):
		api.WriteError(w, http.StatusForbidden, "Forbidden", ErrFourEyes.Error())
	default:
		api.WriteRepoError(w, err)
	}
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
		api.WriteRepoError(w, err)
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
		writeError(w, err)
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
		OrganizationID:   t.OrganizationID,
		ClientID:         req.ClientID,
		OrderNumber:      req.OrderNumber,
		Title:            req.Title,
		Status:           StatusDraft,
		RequestedBy:      t.UserID,
		SupplierID:       req.SupplierID,
		Supplier:         strings.TrimSpace(req.Supplier),
		TargetLocationID: strings.TrimSpace(req.TargetLocationID),
		TotalCost:        req.TotalCost,
		Currency:         req.Currency,
		Notes:            req.Notes,
	}
	if err := h.repo.Create(r.Context(), o); err != nil {
		writeError(w, err)
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
		writeError(w, err)
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
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// setStatus is the shared transition handler; the repository checks the
// state machine and, for an approval, the four-eyes rule.
func (h *Handler) setStatus(w http.ResponseWriter, r *http.Request, status string) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	item, err := h.repo.SetStatus(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), status, t.UserID)
	if err != nil {
		writeError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

// Submit handles POST /api/v1/orders/{id}/submit (draft → submitted). With
// an active definition of the trigger order_submitted the run starts in
// the transaction of the status change (WP-1108): a start that fails
// leaves the order in draft.
func (h *Handler) Submit(w http.ResponseWriter, r *http.Request) {
	if h.runs == nil {
		h.setStatus(w, r, StatusSubmitted)
		return
	}
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	id := chi.URLParam(r, "id")
	var item *Order
	var after func(ctx context.Context)
	err := h.repo.RunInTx(r.Context(), t.OrganizationID, func(ctx context.Context) error {
		var serr error
		if item, serr = h.repo.SetStatus(ctx, t.OrganizationID, id, StatusSubmitted, t.UserID); serr != nil {
			return serr
		}
		runID, run, rerr := h.runs.StartApproval(ctx, t.OrganizationID, approvalPayload(item))
		if rerr != nil {
			return fmt.Errorf("start the approval workflow: %w", rerr)
		}
		if runID == "" {
			return nil
		}
		if aerr := h.repo.SetApprovalRun(ctx, t.OrganizationID, id, runID); aerr != nil {
			return aerr
		}
		item.ApprovalRunID, after = runID, run
		return nil
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if after != nil {
		after(context.WithoutCancel(r.Context()))
	}
	api.WriteJSON(w, http.StatusOK, item)
}

// approvalPayload is the run context of the approval workflow of an order.
func approvalPayload(o *Order) map[string]any {
	items := make([]any, 0, len(o.Items))
	for i := range o.Items {
		it := &o.Items[i]
		items = append(items, map[string]any{"id": it.ID, "description": it.Description, "quantity": it.Quantity,
			"unit_price": it.UnitPrice, "consumable_id": it.ConsumableID, "asset_id": it.AssetID})
	}
	return map[string]any{"order_id": o.ID, "order_number": o.OrderNumber, "title": o.Title, "requested_by": o.RequestedBy,
		"total_cost": o.TotalCost, "currency": o.Currency, "client_id": o.ClientID, "items": items}
}

// Approve handles POST /api/v1/orders/{id}/approve (submitted → approved;
// four-eyes rule, records the approver); 409 while the approval workflow
// of the order runs (WP-1108).
func (h *Handler) Approve(w http.ResponseWriter, r *http.Request) {
	if h.runOpen(w, r) {
		return
	}
	h.setStatus(w, r, StatusApproved)
}

// Reject handles POST /api/v1/orders/{id}/reject (submitted → rejected;
// records the actor); 409 while the approval workflow runs (WP-1108).
func (h *Handler) Reject(w http.ResponseWriter, r *http.Request) {
	if h.runOpen(w, r) {
		return
	}
	h.setStatus(w, r, StatusRejected)
}

// runOpen answers 409 with the run id when the approval workflow of the
// order has not ended and reports whether it answered.
func (h *Handler) runOpen(w http.ResponseWriter, r *http.Request) bool {
	if h.runs == nil {
		return false
	}
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		return false
	}
	o, err := h.repo.GetByID(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, err)
		return true
	}
	if o.ApprovalRunID == "" {
		return false
	}
	open, err := h.runs.RunOpen(r.Context(), t.OrganizationID, o.ApprovalRunID)
	if err != nil {
		writeError(w, err)
		return true
	}
	if !open {
		return false
	}
	api.WriteError(w, http.StatusConflict, "Conflict", ErrApprovalRunOpen.Error()+" "+o.ApprovalRunID)
	return true
}

// Place handles POST /api/v1/orders/{id}/place (approved → ordered).
func (h *Handler) Place(w http.ResponseWriter, r *http.Request) { h.setStatus(w, r, StatusOrdered) }

// Cancel handles POST /api/v1/orders/{id}/cancel (draft, submitted,
// approved, ordered or partially_received → cancelled).
func (h *Handler) Cancel(w http.ResponseWriter, r *http.Request) { h.setStatus(w, r, StatusCancelled) }

// Close handles POST /api/v1/orders/{id}/close (received → closed).
func (h *Handler) Close(w http.ResponseWriter, r *http.Request) { h.setStatus(w, r, StatusClosed) }

// AddItem handles POST /api/v1/orders/{id}/items: a position of an order
// in draft. It books no movement; the goods receipt does (WP-760).
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
		ConsumableID:   strings.TrimSpace(req.ConsumableID),
		AssetID:        strings.TrimSpace(req.AssetID),
	}
	o, err := h.repo.AddItem(r.Context(), item)
	if err != nil {
		writeError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, o)
}
