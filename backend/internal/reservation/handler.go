package reservation

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
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

// BinAvailabilityProvider reads the availability of a consumable per bin
// (GET /api/v1/consumables/{id}/availability, WP-759); PGAvailability
// implements it.
type BinAvailabilityProvider interface {
	BinAvailability(ctx context.Context, orgID, consumableID, locationID string, page api.PaginationParams) ([]BinAvailability, int, error)
}

// Handler provides HTTP handlers for reservations and availability.
type Handler struct {
	repo         Repository
	dispatcher   EventDispatcher
	availability AvailabilityProvider
	entitlements entitlement.Checker
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

// WithEntitlements attaches the feature check of quantity reservations
// (E-44, WP-759): reserving and converting a consumable need the feature
// stock of the organization, beyond assets_basic of the route gate on
// /api/v1/reservations. Without a checker (nil) every organization is
// entitled.
func (h *Handler) WithEntitlements(c entitlement.Checker) *Handler {
	h.entitlements = c
	return h
}

// stockEntitled answers 403 feature-not-entitled and reports false when the
// organization is not entitled to the feature stock (E-44, WP-759).
func (h *Handler) stockEntitled(w http.ResponseWriter, r *http.Request, orgID string) bool {
	if h.entitlements == nil || h.entitlements.IsEnabled(r.Context(), orgID, entitlement.FeatureStock) {
		return true
	}
	refusal := &entitlement.FeatureNotEntitledError{FeatureKey: entitlement.FeatureStock}
	slog.WarnContext(r.Context(), "quantity reservation refused", "organization_id", orgID,
		"method", r.Method, "path", r.URL.Path, "feature", refusal.FeatureKey)
	api.WriteTypedProblem(w, http.StatusForbidden, refusal)
	return false
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
	r.Get("/api/v1/consumables/{id}/availability", h.ConsumableAvailability)
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
// asset, consumable or bin that is unknown, of another organization or outside
// the caller's scope, or an asset the caller may not lock, is 404 with a
// fixed text (TEN-04); a quantity beyond the available stock of the bin is
// 409 insufficient-stock (api.WriteTypedProblem, WP-284), a location that is
// no bin or a missing bin 422; an asset reserved already is 409 asset-reserved, a
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
	case errors.Is(err, ErrItemNotFound), errors.Is(err, consumable.ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrItemNotFound.Error())
	case errors.Is(err, ErrBinNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrBinNotFound.Error())
	case consumable.IsInsufficientStock(err):
		api.WriteTypedProblem(w, http.StatusConflict, consumable.ErrInsufficientStock)
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
// asset_id, or consumable_id (deprecated alias quantity_item_id) with
// location_id and quantity, in the body (frontend/src/api/cmdb.ts). An
// asset reservation runs through the same repository path as POST
// /api/v1/assets/{id}/reserve (lock, movement, event); a consumable
// reservation locks the stock row of its bin (WP-284).
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
	if req.AssetID == "" && req.ConsumableID == "" && req.QuantityItemID == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "asset_id or consumable_id is required")
		return
	}
	if err := normalizeCreate(&req); err != nil {
		api.WriteFieldError(w, err)
		return
	}
	h.create(w, r, t.OrganizationID, &req)
}

// normalizeCreate resolves the deprecated aliases of a reservation request
// (E-38, WP-284): quantity_item_id is consumable_id, item_kind
// quantity_item is consumable. Without item_kind a request with asset_id
// reserves the asset, otherwise the consumable. A request naming both
// objects, an asset reservation with consumable or bin, a consumable
// reservation with asset or an unknown item_kind is a 422 field error.
//
// validation: the texts name the field and the rule.
func normalizeCreate(req *CreateRequest) error {
	unprocessable := func(field, message string) error {
		return &api.FieldError{Field: field, Message: message, Status: http.StatusUnprocessableEntity}
	}
	if req.QuantityItemID != "" {
		if req.ConsumableID != "" && req.ConsumableID != req.QuantityItemID {
			return unprocessable("quantity_item_id", "quantity_item_id is the deprecated alias of consumable_id and must not differ from it")
		}
		req.ConsumableID, req.QuantityItemID = req.QuantityItemID, ""
	}
	switch req.ItemKind {
	case "quantity_item":
		req.ItemKind = "consumable"
	case "":
		req.ItemKind = "consumable"
		if req.AssetID != "" {
			req.ItemKind = "asset"
		}
	}
	switch req.ItemKind {
	case "asset":
		if req.AssetID == "" {
			return unprocessable("asset_id", "asset_id is required for an asset reservation")
		}
		if req.ConsumableID != "" || req.LocationID != "" {
			return unprocessable("consumable_id", "an asset reservation names no consumable_id and no location_id")
		}
	case "consumable":
		if req.ConsumableID == "" {
			return unprocessable("consumable_id", "consumable_id is required for a consumable reservation")
		}
		if req.AssetID != "" {
			return unprocessable("asset_id", "a consumable reservation names no asset_id")
		}
	default:
		return unprocessable("item_kind", "item_kind must be asset or consumable (quantity_item is the alias of consumable)")
	}
	return nil
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
// A quantity reservation needs the feature stock (E-44, WP-759).
func (h *Handler) create(w http.ResponseWriter, r *http.Request, orgID string, req *CreateRequest) {
	if req.ItemKind == "consumable" && !h.stockEntitled(w, r, orgID) {
		return
	}
	// Ids reach SQL only as UUIDs: a malformed one is a 422 field error,
	// not a driver message (API-03).
	for _, f := range [][2]string{{"asset_id", req.AssetID}, {"consumable_id", req.ConsumableID}, {"location_id", req.LocationID},
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
		ConsumableID:   req.ConsumableID,
		LocationID:     req.LocationID,
		Quantity:       1,
		AssigneeID:     req.AssigneeID,
		ProjectRef:     req.ProjectRef,
		OrderID:        req.OrderID,
		TicketID:       req.TicketID,
		Reason:         req.Reason,
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
// A quantity reservation becomes consumed and its whole quantity a
// retrieval from its bin (WP-759); 200 with the reservation and the
// movement. It needs the feature stock (403 feature-not-entitled, E-44)
// and, beyond assignment:write of the route, stock:write (403, E-138).
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
	if !h.quantityConvertAllowed(w, r, t.OrganizationID, id) {
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

// quantityConvertAllowed reads the reservation id and, for a quantity
// reservation, checks the feature stock (E-44) and the permission
// stock:write of the principal (E-138): a conversion takes stock out of the
// bin like POST /api/v1/stock-movements. It answers and reports false when
// the reservation is not visible or a check fails; an asset reservation
// passes.
func (h *Handler) quantityConvertAllowed(w http.ResponseWriter, r *http.Request, orgID, id string) bool {
	res, err := h.repo.GetByID(r.Context(), orgID, id)
	if err != nil {
		writeReservationError(w, r, err)
		return false
	}
	if res.ItemKind != "consumable" {
		return true
	}
	if !h.stockEntitled(w, r, orgID) {
		return false
	}
	if p, ok := identity.PrincipalFromContext(r.Context()); !ok || !p.Has(identity.PermStockWrite) {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "missing required permission: "+string(identity.PermStockWrite))
		return false
	}
	return true
}

// ConsumableAvailability handles GET /api/v1/consumables/{id}/availability
// (E-38, WP-759): the availability of the consumable per bin (on_hand,
// reserved, available = on_hand − reserved) from its stock rows the caller
// sees, with location_id only that bin, ordered by bin and cursor
// paginated. An unknown or invisible consumable is 404.
func (h *Handler) ConsumableAvailability(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	bins, ok := h.availability.(BinAvailabilityProvider)
	if !ok {
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "availability projection is not configured")
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrItemNotFound.Error())
	if !ok {
		return
	}
	locationID := r.URL.Query().Get("location_id")
	if locationID != "" {
		if err := api.RequireUUID("location_id", locationID); err != nil {
			api.WriteFieldError(w, err)
			return
		}
	}
	page := api.ParsePagination(r)
	if page.CursorError != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid cursor")
		return
	}
	rows, total, err := bins.BinAvailability(r.Context(), t.OrganizationID, id, locationID, page)
	switch {
	case errors.Is(err, ErrItemNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrItemNotFound.Error())
		return
	case errors.Is(err, api.ErrInvalidCursor):
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid cursor")
		return
	case err != nil:
		slog.WarnContext(r.Context(), "consumable availability failed", "error", err)
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, BinAvailabilityPage(rows, total, page))
}

// Availability handles GET /api/v1/inventory/availability: the org-wide
// projection, kept as alias until the web client no longer calls it; its
// successors are GET /api/v1/consumables/{id}/availability for consumables
// and active_reservation of GET /api/v1/assets/{id} for assets (E-38,
// WP-759).
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
