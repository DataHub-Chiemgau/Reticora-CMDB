package ci

import (
	"context"
	"errors"
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// isEntitlementError reports whether the error was raised by the entitlement
// service because a licensed limit or feature restriction was violated.
func isEntitlementError(err error) bool {
	var entitlementErr interface{ LimitExceeded() bool }
	return errors.As(err, &entitlementErr) && entitlementErr.LimitExceeded()
}

// EventDispatcher publishes CI lifecycle events.
type EventDispatcher interface {
	Dispatch(ctx context.Context, orgID, event string, payload any)
}

// Handler provides HTTP handlers for CI endpoints.
type Handler struct {
	svc        *Service
	dispatcher EventDispatcher
}

// NewHandler creates a new CI handler.
func NewHandler(svc *Service, dispatcher ...EventDispatcher) *Handler {
	h := &Handler{svc: svc}
	if len(dispatcher) > 0 {
		h.dispatcher = dispatcher[0]
	}
	return h
}

// RegisterRoutes registers CI routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/cis", h.List)
	r.Post("/api/v1/cis", h.Create)
	r.Get("/api/v1/cis/{id}", h.Get)
	r.Get("/api/v1/cis/{id}/changes", h.ListChanges)
	r.Patch("/api/v1/cis/{id}", h.Update)
	r.Delete("/api/v1/cis/{id}", h.Delete)
}

// List handles GET /api/v1/cis
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
		Status:   r.URL.Query().Get("status"),
		TypeID:   r.URL.Query().Get("ci_type_id"),
		ClientID: r.URL.Query().Get("client_id"),
		SiteID:   r.URL.Query().Get("site_id"),
		RoomID:   r.URL.Query().Get("room_id"),
		Search:   r.URL.Query().Get("search"),
		SortBy:   r.URL.Query().Get("sort_by"),
		SortDir:  r.URL.Query().Get("sort_dir"),
	}

	items, total, err := h.svc.List(r.Context(), t.OrganizationID, filter, page)
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
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Item]{
		Data:       items,
		Total:      total,
		Limit:      page.Limit,
		Offset:     page.Offset,
		HasMore:    hasMore,
		NextCursor: NextCursor(items, filter, page.Limit),
	})
}

// Get handles GET /api/v1/cis/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	item, err := h.svc.GetByID(r.Context(), t.OrganizationID, id)
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
		LocationID:      req.LocationID,
		CITypeID:        req.CITypeID,
		Name:            req.Name,
		Status:          status,
		Manufacturer:    req.Manufacturer,
		Model:           req.Model,
		SerialNumber:    req.SerialNumber,
		ManagementIP:    req.ManagementIP,
		FirmwareVersion: req.FirmwareVersion,
		Attributes:      req.Attributes,
		DiscoverySource: req.DiscoverySource,
	}
	if item.Attributes == nil {
		item.Attributes = make(map[string]any)
	}
	if item.DiscoverySource == "" {
		item.DiscoverySource = SourceManual
	}

	if err := h.svc.Create(r.Context(), item); err != nil {
		if isEntitlementError(err) {
			api.WriteError(w, http.StatusForbidden, "Forbidden", err.Error())
			return
		}
		if writeValidationError(w, err) {
			return
		}
		if api.WriteDBError(w, err) {
			return
		}
		api.WriteRepoError(w, err)
		return
	}
	if h.dispatcher != nil {
		h.dispatcher.Dispatch(r.Context(), t.OrganizationID, "ci.created", item)
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

	id := chi.URLParam(r, "id")
	var req UpdateRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	// A PATCH through the API is a manual change: its fields get protected
	// overrides (OVR-01).
	req.Manual = &ManualChange{Author: t.UserID}

	item, err := h.svc.Update(r.Context(), t.OrganizationID, id, req)
	if err != nil {
		if writeValidationError(w, err) {
			return
		}
		if api.WriteDBError(w, err) {
			return
		}
		api.WriteError(w, http.StatusNotFound, "Not Found", "CI not found")
		return
	}
	if h.dispatcher != nil {
		h.dispatcher.Dispatch(r.Context(), t.OrganizationID, "ci.updated", item)
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

	id := chi.URLParam(r, "id")
	item, err := h.svc.Delete(r.Context(), t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "CI not found")
		return
	}
	if h.dispatcher != nil {
		h.dispatcher.Dispatch(r.Context(), t.OrganizationID, "ci.deleted", item)
	}

	w.WriteHeader(http.StatusNoContent)
}

// ListChanges handles GET /api/v1/cis/{id}/changes.
func (h *Handler) ListChanges(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	page := api.ParsePagination(r)
	changes, total, err := h.svc.ListChanges(r.Context(), t.OrganizationID, id, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}

	api.WriteJSON(w, http.StatusOK, api.ListResponse[Change]{
		Data:    changes,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// writeValidationError renders a field-metadata validation failure as an
// RFC 7807 problem with status 422 and the per-field violations, so clients can
// map the response back onto their form fields. It reports whether err was a
// validation error.
func writeValidationError(w http.ResponseWriter, err error) bool {
	ve, ok := AsValidationError(err)
	if !ok {
		return false
	}
	api.WriteJSON(w, http.StatusUnprocessableEntity, struct {
		api.ProblemDetail
		Violations []Violation `json:"violations"`
	}{
		ProblemDetail: api.ProblemDetail{
			Type:   "https://reticora.io/problems/422",
			Title:  "Unprocessable Entity",
			Status: http.StatusUnprocessableEntity,
			Detail: ve.Error(),
		},
		Violations: ve.Violations,
	})
	return true
}
