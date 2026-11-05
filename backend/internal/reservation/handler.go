package reservation

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// EventDispatcher publishes reservation events to webhook subscribers.
type EventDispatcher interface {
	Dispatch(ctx context.Context, orgID, event string, payload any)
}

// AvailabilityProvider computes the availability projection for an item.
type AvailabilityProvider interface {
	Availability(ctx context.Context, orgID string, filter AvailabilityFilter) ([]Availability, error)
}

// Handler provides HTTP handlers for reservations and availability.
type Handler struct {
	repo         Repository
	dispatcher   EventDispatcher
	availability AvailabilityProvider
}

// NewHandler creates a new reservation handler.
func NewHandler(repo Repository, dispatcher ...EventDispatcher) *Handler {
	h := &Handler{repo: repo}
	if len(dispatcher) > 0 {
		h.dispatcher = dispatcher[0]
	}
	return h
}

// WithAvailability attaches the availability projection provider.
func (h *Handler) WithAvailability(p AvailabilityProvider) *Handler {
	h.availability = p
	return h
}

// RegisterRoutes registers reservation routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/reservations", h.List)
	r.Post("/api/v1/reservations", h.Create)
	r.Get("/api/v1/reservations/{id}", h.Get)
	r.Post("/api/v1/reservations/{id}/release", h.Release)
	r.Post("/api/v1/reservations/{id}/convert", h.Convert)
	r.Post("/api/v1/assets/{id}/reserve", h.ReserveAsset)
	r.Get("/api/v1/inventory/availability", h.Availability)
}

// dispatch hands an event to the webhook dispatcher directly; only the
// profile without outbox (--no-db) passes one (router.go), otherwise the
// PostgreSQL repository writes the events in the transaction (WP-277).
func (h *Handler) dispatch(r *http.Request, orgID, event string, payload any) {
	if h.dispatcher != nil {
		h.dispatcher.Dispatch(r.Context(), orgID, event, payload)
	}
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	items, total, err := h.repo.List(r.Context(), t.OrganizationID, r.URL.Query().Get("state"), page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Reservation]{
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
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	item, err := h.repo.GetByID(r.Context(), t.OrganizationID, id)
	if err != nil {
		writeReservationError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

// writeReservationError answers a failed reservation request: a reservation,
// asset or quantity item that is unknown, of another organization or outside
// the caller's scope, or an asset the caller may not lock, is 404 with a
// fixed text (TEN-04); an asset reserved already is 409 asset-reserved, a
// reservation no longer active 409 with its domain message. The conversion
// adds the refusals of the assignment service (WP-275): an asset with a
// holder 409 asset-assigned, a holder of another organization 422. Database
// errors go through api.WriteRepoError; no table, constraint, policy or
// SQLSTATE text reaches the client (API-03, F-047), the log keeps it.
func writeReservationError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrNotFound.Error())
	case errors.Is(err, ErrAssetNotFound), errors.Is(err, assignment.ErrAssetNotFound), errors.Is(err, movement.ErrAssetNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrAssetNotFound.Error())
	case errors.Is(err, ErrItemNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrItemNotFound.Error())
	case errors.Is(err, ErrConflict):
		api.WriteProblem(w, http.StatusConflict, assignment.ProblemAssetReserved, "Conflict", ErrConflict.Error())
	case errors.Is(err, assignment.ErrAssetReserved):
		api.WriteProblem(w, http.StatusConflict, assignment.ProblemAssetReserved, "Conflict", assignment.ErrAssetReserved.Error())
	case errors.Is(err, ErrNotActive):
		api.WriteError(w, http.StatusConflict, "Conflict", ErrNotActive.Error())
	case errors.Is(err, assignment.ErrAlreadyAssigned):
		api.WriteProblem(w, http.StatusConflict, assignment.ProblemAssetAssigned, "Conflict", assignment.ErrAlreadyAssigned.Error())
	case errors.Is(err, ErrConvertUnavailable):
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", ErrConvertUnavailable.Error())
	case api.WriteFieldError(w, err):
	default:
		slog.WarnContext(r.Context(), "reservation request failed", "error", err)
		api.WriteRepoError(w, err)
	}
}

// Create handles POST /api/v1/reservations: the existing path with
// asset_id or quantity_item_id in the body (frontend/src/api/cmdb.ts). An
// asset reservation runs through the same repository path as POST
// /api/v1/assets/{id}/reserve (lock, movement, event).
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
	if req.AssetID == "" && req.QuantityItemID == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "asset_id or quantity_item_id is required")
		return
	}
	h.create(w, r, t.OrganizationID, &req)
}

// ReserveAsset handles POST /api/v1/assets/{id}/reserve (AST-06, WP-277):
// 201 with the reservation of the asset for a user (assignee_id) or a
// project (project_ref) until reserved_until.
func (h *Handler) ReserveAsset(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	assetID, ok := api.ParseUUIDParam(w, r, "id", ErrAssetNotFound.Error())
	if !ok {
		return
	}
	var req ReserveRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	switch {
	case req.ReservedUntil == "":
		api.WriteFieldError(w, &api.FieldError{Field: "reserved_until", Message: "reserved_until is required", Status: http.StatusUnprocessableEntity})
		return
	case (req.AssigneeID == "") == (req.ProjectRef == ""):
		api.WriteFieldError(w, &api.FieldError{Field: "assignee_id", Message: "exactly one of assignee_id and project_ref is required",
			Status: http.StatusUnprocessableEntity})
		return
	}
	h.create(w, r, t.OrganizationID, &CreateRequest{ItemKind: "asset", AssetID: assetID, ReservedUntil: req.ReservedUntil,
		AssigneeID: req.AssigneeID, ProjectRef: req.ProjectRef, Reason: req.Reason, ExpiresAt: req.ExpiresAt})
}

// parseTime reads the RFC 3339 time of field; empty is nil.
//
// validation: the texts name the field and its format.
func parseTime(field, value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	ts, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, &api.FieldError{Field: field, Message: "invalid " + field}
	}
	return &ts, nil
}

// create validates req and creates the reservation (Create, ReserveAsset).
func (h *Handler) create(w http.ResponseWriter, r *http.Request, orgID string, req *CreateRequest) {
	// Ids reach SQL only as UUIDs: a malformed one is a 422 field error,
	// not a driver message (API-03).
	for _, f := range [][2]string{{"asset_id", req.AssetID}, {"quantity_item_id", req.QuantityItemID},
		{"assignee_id", req.AssigneeID}, {"order_id", req.OrderID}, {"ticket_id", req.TicketID}} {
		if f[1] == "" {
			continue
		}
		if err := api.RequireUUID(f[0], f[1]); err != nil {
			api.WriteFieldError(w, err)
			return
		}
	}
	res := &Reservation{
		OrganizationID: orgID,
		ItemKind:       req.ItemKind,
		AssetID:        req.AssetID,
		QuantityItemID: req.QuantityItemID,
		Quantity:       1,
		AssigneeID:     req.AssigneeID,
		ProjectRef:     req.ProjectRef,
		OrderID:        req.OrderID,
		TicketID:       req.TicketID,
		Reason:         req.Reason,
	}
	if res.ItemKind == "" {
		if req.AssetID != "" {
			res.ItemKind = "asset"
		} else {
			res.ItemKind = "quantity_item"
		}
	}
	if req.Quantity != nil {
		if *req.Quantity <= 0 {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "quantity must be positive")
			return
		}
		res.Quantity = *req.Quantity
	}
	from, err := parseTime("reserved_from", req.ReservedFrom)
	if err != nil {
		api.WriteFieldError(w, err)
		return
	}
	if from != nil {
		res.ReservedFrom = *from
	}
	if res.ReservedUntil, err = parseTime("reserved_until", req.ReservedUntil); err != nil {
		api.WriteFieldError(w, err)
		return
	}
	if res.ExpiresAt, err = parseTime("expires_at", req.ExpiresAt); err != nil {
		api.WriteFieldError(w, err)
		return
	}
	if p, ok := identity.PrincipalFromContext(r.Context()); ok {
		res.CreatedBy = p.Subject
	}
	if err = h.repo.Create(r.Context(), res); err != nil {
		writeReservationError(w, r, err)
		return
	}
	h.dispatch(r, orgID, events.ReservationCreated, res)
	api.WriteJSON(w, http.StatusCreated, res)
}

// Release handles POST /api/v1/reservations/{id}/release
func (h *Handler) Release(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	// A reservation outside the caller's scope is 404 like an unknown one,
	// one released already 409 (F-047).
	item, err := h.repo.Transition(r.Context(), t.OrganizationID, id, "released")
	if err != nil {
		writeReservationError(w, r, err)
		return
	}
	h.dispatch(r, t.OrganizationID, events.ReservationReleased, item)
	api.WriteJSON(w, http.StatusOK, item)
}

// Convert handles POST /api/v1/reservations/{id}/convert (WP-277): the
// active asset reservation becomes consumed and its asset is assigned in
// the same transaction, to assignee_id or team_id of the body, else to the
// user of the reservation; 200 with the reservation and the assignment.
func (h *Handler) Convert(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	var req ConvertRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	switch {
	case req.AssigneeID != "" && req.TeamID != "":
		api.WriteFieldError(w, &api.FieldError{Field: "assignee_id", Message: "at most one of assignee_id and team_id is allowed",
			Status: http.StatusUnprocessableEntity})
		return
	case req.AssigneeID != "" && !api.IsUUID(req.AssigneeID):
		api.WriteFieldError(w, &api.FieldError{Field: "assignee_id", Message: "must be a UUID", Status: http.StatusUnprocessableEntity})
		return
	case req.TeamID != "" && !api.IsUUID(req.TeamID):
		api.WriteFieldError(w, &api.FieldError{Field: "team_id", Message: "must be a UUID", Status: http.StatusUnprocessableEntity})
		return
	}
	out, err := h.repo.Convert(r.Context(), t.OrganizationID, id, &ConvertInput{
		Holder: assignment.Holder{UserID: req.AssigneeID, TeamID: req.TeamID}, DueAt: req.DueAt, Notes: req.Notes})
	if err != nil {
		writeReservationError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// Availability handles GET /api/v1/inventory/availability
func (h *Handler) Availability(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if h.availability == nil {
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "availability projection is not configured")
		return
	}
	items, err := h.availability.Availability(r.Context(), t.OrganizationID, AvailabilityFilter{
		ItemKind: r.URL.Query().Get("item_kind"),
		ItemID:   r.URL.Query().Get("item_id"),
	})
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"data": api.NonNilSlice(items)})
}
