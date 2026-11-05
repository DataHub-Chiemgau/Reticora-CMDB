package movement

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Recorder is the minimal interface other modules (assignment, disposal,
// order, stocktake) use to emit auditable movements into the ledger (spec §9:
// every inventory movement is a transaction). The Service satisfies it, and
// so do the PG and in-memory repositories.
type Recorder interface {
	Record(ctx context.Context, m *Movement) error
}

// Handler provides HTTP handlers for the movement ledger and the deprecated
// alias inventory/items on consumable (E-40, WP-757).
// Bookings go through the movement service, which writes stock.movement to
// the outbox (WP-274).
type Handler struct {
	repo   Repository
	assets AssetCreator
}

// NewHandler creates a new movement handler over the movement service (or
// a repository).
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers movement routes on the given mux. The ledger is
// append-only: no update or delete routes exist by design (spec §9).
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/stock-movements", h.ListMovements)
	r.Post("/api/v1/stock-movements", h.Record)
	r.Get("/api/v1/assets/{id}/movements", h.ListAssetMovements)
	r.Get("/api/v1/consumables/{id}/movements", h.ListConsumableMovements)
	r.Post("/api/v1/consumables/{id}/movements", h.RecordConsumableMovement)
	r.Get("/api/v1/inventory/items", h.ListItems)
	r.Post("/api/v1/inventory/items", h.CreateItem)
	r.Get("/api/v1/inventory/items/{id}", h.GetItem)
	r.Patch("/api/v1/inventory/items/{id}", h.UpdateItem)
	r.Delete("/api/v1/inventory/items/{id}", h.DeleteItem)
	r.Post("/api/v1/inventory/items/{id}/convert", h.ConvertToAsset)
}

// dependencyProblem is the 409 of a delete refused by dependent objects
// (pattern of locations.WriteDependencyConflict).
type dependencyProblem struct {
	api.ProblemDetail
	Dependencies []string `json:"dependencies"`
}

// WriteLedgerDependency answers the delete of an object the ledger keeps
// with 409 and dependencies ["stock_movement"] (WP-274).
func WriteLedgerDependency(w http.ResponseWriter, r *http.Request) {
	api.WriteProblemBody(w, http.StatusConflict, dependencyProblem{
		ProblemDetail: api.ProblemDetail{
			Type:     "https://reticora.io/problems/409",
			Title:    "Conflict",
			Status:   http.StatusConflict,
			Detail:   "the object has stock movements, which are kept; it cannot be deleted",
			Instance: r.URL.Path,
		},
		Dependencies: []string{"stock_movement"},
	})
}

// writeMovementError answers a failed movement or inventory-item request:
// an asset, consumable or article of the alias that is unknown, of another
// organization or outside the caller's scope is 404 with a fixed text
// (TEN-04), a location or reference likewise 422 on its field, a taken SKU
// 409, a shortfall 409 insufficient-stock (api.WriteTypedProblem), a
// delete kept by movements 409 with dependencies, one kept by stock in a
// bin 409.
// Database errors go through api.WriteRepoError; no table, constraint,
// policy or SQLSTATE text reaches the client (API-03, F-047), the log keeps
// it.
func writeMovementError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrItemNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrItemNotFound.Error())
	case errors.Is(err, ErrAssetNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrAssetNotFound.Error())
	case errors.Is(err, consumable.ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", consumable.ErrNotFound.Error())
	case errors.Is(err, ErrConflict):
		api.WriteError(w, http.StatusConflict, "Conflict", ErrConflict.Error())
	case errors.Is(err, ErrInsufficientStock):
		api.WriteTypedProblem(w, http.StatusConflict, err)
	case errors.Is(err, ErrHasMovements):
		WriteLedgerDependency(w, r)
	case errors.Is(err, consumable.ErrHasStock):
		api.WriteError(w, http.StatusConflict, "Conflict", consumable.ErrHasStock.Error())
	case errors.Is(err, api.ErrInvalidCursor):
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid cursor")
	case api.WriteFieldError(w, err):
	default:
		slog.WarnContext(r.Context(), "inventory movement request failed", "error", err)
		api.WriteRepoError(w, err)
	}
}

// requireUUIDs answers 422 on the first field whose value is set but no
// UUID, so no malformed id reaches SQL (API-03), and reports whether all
// were valid. fields alternates field names and values.
func requireUUIDs(w http.ResponseWriter, fields ...string) bool {
	for i := 0; i+1 < len(fields); i += 2 {
		if fields[i+1] == "" {
			continue
		}
		if err := api.RequireUUID(fields[i], fields[i+1]); err != nil {
			api.WriteFieldError(w, err)
			return false
		}
	}
	return true
}

// writeField answers 422 with field and its fixed message.
func writeField(w http.ResponseWriter, field, message string) {
	api.WriteFieldError(w, &api.FieldError{Field: field, Message: message, Status: http.StatusUnprocessableEntity})
}

// parseOccurred reads the RFC 3339 time of the query parameter name; an
// invalid value is answered with 422 and ok false.
func parseOccurred(w http.ResponseWriter, r *http.Request, name string) (at *time.Time, ok bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return nil, true
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		writeField(w, name, "must be an RFC 3339 date-time")
		return nil, false
	}
	return &parsed, true
}

// parseMovementPage reads limit, offset and cursor of a movement list; a
// malformed cursor or one of another order is answered with 400.
func parseMovementPage(w http.ResponseWriter, r *http.Request) (api.PaginationParams, bool) {
	page := api.ParsePagination(r)
	err := page.CursorError
	if err == nil && page.Cursor != nil {
		_, _, err = movementPosition(*page.Cursor)
	}
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid cursor")
		return page, false
	}
	return page, true
}

// listMovements answers a movement list with filter.
func (h *Handler) listMovements(w http.ResponseWriter, r *http.Request, orgID string, filter *MovementFilter, page api.PaginationParams) {
	rows, total, err := h.repo.ListMovements(r.Context(), orgID, filter, page)
	if err != nil {
		writeMovementError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, MovementPage(rows, total, page))
}

// ListMovements handles GET /api/v1/stock-movements: the movements of
// every object kind, filtered by asset_id, consumable_id,
// quantity_item_id (deprecated alias of consumable_id), movement_type (STK-04 type, else 422), location_id
// (source or target) and occurred_from/occurred_to (RFC 3339, else 422),
// newest first on (occurred_at, id) with a keyset cursor.
func (h *Handler) ListMovements(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	q := r.URL.Query()
	filter := MovementFilter{
		AssetID:        q.Get("asset_id"),
		ConsumableID:   q.Get("consumable_id"),
		QuantityItemID: q.Get("quantity_item_id"),
		MovementType:   q.Get("movement_type"),
		LocationID:     q.Get("location_id"),
	}
	if !requireUUIDs(w, "asset_id", filter.AssetID, "consumable_id", filter.ConsumableID,
		"quantity_item_id", filter.QuantityItemID, "location_id", filter.LocationID) {
		return
	}
	if filter.MovementType != "" && !MovementTypes[filter.MovementType] {
		writeField(w, "movement_type", "unknown movement_type")
		return
	}
	var ok bool
	if filter.OccurredFrom, ok = parseOccurred(w, r, "occurred_from"); !ok {
		return
	}
	if filter.OccurredTo, ok = parseOccurred(w, r, "occurred_to"); !ok {
		return
	}
	page, ok := parseMovementPage(w, r)
	if !ok {
		return
	}
	h.listMovements(w, r, t.OrganizationID, &filter, page)
}

// ListAssetMovements handles GET /api/v1/assets/{id}/movements, the
// deprecated reading of GET /api/v1/stock-movements?asset_id= (WP-247).
func (h *Handler) ListAssetMovements(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	assetID, ok := api.ParseUUIDParam(w, r, "id", ErrAssetNotFound.Error())
	if !ok {
		return
	}
	page, ok := parseMovementPage(w, r)
	if !ok {
		return
	}
	h.listMovements(w, r, t.OrganizationID, &MovementFilter{AssetID: assetID}, page)
}

// checkCreateRequest answers a booking POST /api/v1/stock-movements does
// not take and reports whether req passed (E-38, E-142, WP-756): for a
// consumable (quantity_item_id is its deprecated alias since the merge,
// E-40, WP-757) receipt, retrieval, correction and transfer with quantity
// > 0; for an
// asset only transfer to to_location_id, without quantity. The types of
// the domain paths are 422 movement-type-reserved; legacy names, unknown
// types and every other combination are 422 on their field. The rules of
// the locations per type of a consumable are those of
// consumable.ApplyBooking.
func checkCreateRequest(w http.ResponseWriter, req *CreateMovementRequest) bool {
	objects := 0
	for _, id := range []string{req.AssetID, req.ConsumableID, req.QuantityItemID} {
		if id != "" {
			objects++
		}
	}
	switch {
	case ReservedMovementTypes[req.MovementType]:
		api.WriteProblem(w, http.StatusUnprocessableEntity, ProblemMovementTypeReserved, "Movement Type Reserved",
			"this movement type is booked only by its domain operation")
	case !MovementTypes[req.MovementType]:
		writeField(w, "movement_type", "unknown movement_type")
	case objects != 1:
		writeField(w, "asset_id", "exactly one of asset_id, consumable_id and quantity_item_id is required")
	case req.AssetID != "" && req.ItemKind != "" && req.ItemKind != "asset",
		req.ConsumableID != "" && req.ItemKind != "" && req.ItemKind != "consumable",
		req.QuantityItemID != "" && req.ItemKind != "" && req.ItemKind != "quantity_item" && req.ItemKind != "consumable":
		writeField(w, "item_kind", "item_kind does not match the moved object")
	case req.AssetID != "" && !AssetMovementTypes[req.MovementType]:
		writeField(w, "movement_type", "an asset moves through this route only by transfer")
	case req.AssetID != "" && req.Quantity != nil:
		writeField(w, "quantity", "an asset movement has no quantity")
	case req.AssetID != "" && req.ToLocationID == "":
		writeField(w, "to_location_id", "an asset transfer needs to_location_id")
	case (req.ConsumableID != "" || req.QuantityItemID != "") && !ConsumableMovementTypes[req.MovementType]:
		writeField(w, "movement_type", "movement_type is not booked for a consumable")
	case req.AssetID == "" && (req.Quantity == nil || *req.Quantity <= 0):
		writeField(w, "quantity", "quantity must be greater than 0")
	default:
		return true
	}
	return false
}

// mayMoveAssets reports whether the caller may move an asset: the
// transfer of an asset needs asset:move besides stock:write of the route
// (E-142, WP-756), else 403. The route authorization always sets the
// principal; without one (handler tests) the route check is all there is.
func mayMoveAssets(w http.ResponseWriter, r *http.Request) bool {
	if p, ok := identity.PrincipalFromContext(r.Context()); ok && !p.Has(identity.PermAssetMove) {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "missing required permission: "+string(identity.PermAssetMove))
		return false
	}
	return true
}

// Record handles POST /api/v1/stock-movements: it books a quantity
// booking of a consumable (also named by the deprecated alias
// quantity_item_id, WP-757), or the transfer of an asset,
// through the movement service (lock, checks, stock per bin,
// stock.movement in the outbox, all in one transaction).
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
	if !checkCreateRequest(w, &req) {
		return
	}
	if req.AssetID != "" && !mayMoveAssets(w, r) {
		return
	}
	// consumable_id is checked by the service (no UUID: not found); the
	// in-memory store has ids of its own.
	if !requireUUIDs(w, "asset_id", req.AssetID, "quantity_item_id", req.QuantityItemID, "from_location_id", req.FromLocationID,
		"to_location_id", req.ToLocationID, "ticket_id", req.TicketID, "order_id", req.OrderID, "workflow_run_id", req.WorkflowRunID,
		"document_id", req.DocumentID) {
		return
	}
	m := &Movement{
		OrganizationID: t.OrganizationID,
		ItemKind:       req.ItemKind,
		AssetID:        req.AssetID,
		ConsumableID:   req.ConsumableID,
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
	// An object outside the caller's scope is 404 like an unknown one, and
	// nothing is recorded (F-047, TEN-04).
	if err := h.repo.Record(r.Context(), m); err != nil {
		writeMovementError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, m)
}

// ListConsumableMovements handles GET /api/v1/consumables/{id}/movements,
// the deprecated reading of GET /api/v1/stock-movements?consumable_id=
// (E-38, WP-756): the same rows and page, each with direction, location_id
// and reference of the former form.
func (h *Handler) ListConsumableMovements(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page, ok := parseMovementPage(w, r)
	if !ok {
		return
	}
	rows, total, err := h.repo.ListMovements(r.Context(), t.OrganizationID, &MovementFilter{ConsumableID: chi.URLParam(r, "id")}, page)
	if err != nil {
		writeMovementError(w, r, err)
		return
	}
	list := MovementPage(rows, total, page)
	legacy := make([]ConsumableMovement, 0, len(list.Data))
	for i := range list.Data {
		legacy = append(legacy, LegacyConsumableMovement(&list.Data[i]))
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[ConsumableMovement]{Data: legacy, Total: list.Total, Limit: list.Limit,
		Offset: list.Offset, HasMore: list.HasMore, NextCursor: list.NextCursor})
}

// RecordConsumableMovement handles POST /api/v1/consumables/{id}/movements
// (E-38, WP-756): in books a receipt into the bin location_id, out a
// retrieval from it, through the movement service like POST
// /api/v1/stock-movements. Without location_id the consumable must have
// exactly one bin, else 422 on location_id; a location that is no visible
// bin of the consumable's client is 422 on location_id, a shortfall 409
// insufficient-stock.
func (h *Handler) RecordConsumableMovement(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req consumable.CreateMovementRequest
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
	if !requireUUIDs(w, "location_id", req.LocationID) {
		return
	}
	quantity := req.Quantity
	m := &Movement{
		OrganizationID: t.OrganizationID,
		ItemKind:       "consumable",
		ConsumableID:   chi.URLParam(r, "id"),
		MovementType:   "receipt",
		ToLocationID:   req.LocationID,
		Quantity:       &quantity,
		Reason:         req.Reason,
		Notes:          req.Reference,
		ActorID:        t.UserID,
		onlyBin:        req.LocationID == "",
	}
	if req.Direction == "out" {
		m.MovementType, m.FromLocationID, m.ToLocationID = "retrieval", req.LocationID, ""
	}
	if err := h.repo.Record(r.Context(), m); err != nil {
		var field *api.FieldError
		if errors.As(err, &field) && (field.Field == "to_location_id" || field.Field == "from_location_id") {
			// The bin of this path is location_id.
			err = &api.FieldError{Field: "location_id", Message: field.Message, Status: field.Status}
		}
		writeMovementError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, map[string]any{"movement": LegacyConsumableMovement(m)})
}

// ListItems handles GET /api/v1/inventory/items, the alias of GET
// /api/v1/consumables in the answer form QuantityItem (E-40, WP-757).
func (h *Handler) ListItems(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListItems(r.Context(), t.OrganizationID, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[QuantityItem]{
		Data: api.NonNilSlice(items), Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// GetItem handles GET /api/v1/inventory/items/{id}, the deprecated alias of
// GET /api/v1/consumables/{id} (WP-757).
func (h *Handler) GetItem(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrItemNotFound.Error())
	if !ok {
		return
	}
	item, err := h.repo.GetItem(r.Context(), t.OrganizationID, id)
	if err != nil {
		writeMovementError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

// CreateItem handles POST /api/v1/inventory/items, the alias of POST
// /api/v1/consumables (WP-757): a stock_level > 0 is booked as a receipt
// into the bin location_id through the movement service.
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
	if !requireUUIDs(w, "client_id", req.ClientID, "location_id", req.LocationID) {
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
		actorID:        t.UserID,
	}
	if err := h.repo.CreateItem(r.Context(), item); err != nil {
		writeMovementError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, item)
}

// UpdateItem handles PATCH /api/v1/inventory/items/{id}, the deprecated
// alias of PATCH /api/v1/consumables/{id} (WP-757); location_id and
// attributes are 422.
func (h *Handler) UpdateItem(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrItemNotFound.Error())
	if !ok {
		return
	}
	var req UpdateItemRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	item, err := h.repo.UpdateItem(r.Context(), t.OrganizationID, id, req)
	if err != nil {
		writeMovementError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

// AssetCreator creates a serialized asset from a quantity item conversion
// (spec §6). Satisfied by the asset repository; with a context that carries
// a transaction (database.ContextWithTx) it creates the asset in it.
type AssetCreator interface {
	Create(ctx context.Context, a *AssetRef) (string, error)
}

// AssetRef is the minimal asset payload needed for the conversion flow; the
// asset package provides the concrete type via an adapter in the server
// wiring (avoids an import cycle between movement and asset).
type AssetRef struct {
	OrganizationID string
	ClientID       string
	AssetTag       string
	Name           string
	Category       string
	LocationID     string
	SerialNumber   string
}

// WithAssets attaches the asset creator used by the conversion endpoint.
func (h *Handler) WithAssets(creator AssetCreator) *Handler {
	h.assets = creator
	return h
}

// ConvertToAsset handles POST /api/v1/inventory/items/{id}/convert: one
// unit of the article is retrieved from the bin location_id (the only bin
// of the article when absent, else 422 on location_id) and becomes a
// serialized asset in that bin, both in one transaction (Repository.
// ConvertItem, WP-757). A shortfall is 409 insufficient-stock and leaves
// neither asset nor movement.
func (h *Handler) ConvertToAsset(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if h.assets == nil {
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "asset conversion is not configured")
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrItemNotFound.Error())
	if !ok {
		return
	}
	var req struct {
		AssetTag     string `json:"asset_tag"`
		SerialNumber string `json:"serial_number,omitempty"`
		LocationID   string `json:"location_id,omitempty"`
	}
	if readErr := api.ReadJSON(r, &req); readErr != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", readErr.Error())
		return
	}
	if req.AssetTag == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "asset_tag is required")
		return
	}
	if !requireUUIDs(w, "location_id", req.LocationID) {
		return
	}
	conv := &Conversion{OrganizationID: t.OrganizationID, ItemID: id, LocationID: req.LocationID, AssetTag: req.AssetTag,
		SerialNumber: req.SerialNumber, ActorID: t.UserID}
	if err := h.repo.ConvertItem(r.Context(), conv, h.assets); err != nil {
		writeMovementError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, map[string]any{
		"asset_id":         conv.AssetID,
		"quantity_item_id": id,
		"movement_id":      conv.Movement.ID,
	})
}

// DeleteItem handles DELETE /api/v1/inventory/items/{id}, the deprecated
// alias of DELETE /api/v1/consumables/{id} (WP-757).
func (h *Handler) DeleteItem(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrItemNotFound.Error())
	if !ok {
		return
	}
	if err := h.repo.DeleteItem(r.Context(), t.OrganizationID, id); err != nil {
		writeMovementError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
