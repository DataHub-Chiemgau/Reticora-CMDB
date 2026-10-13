package ci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

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
	includes   map[string]IncludeFunc
}

// IncludeFunc loads one related collection of a CI for ?include= (API-05).
type IncludeFunc func(ctx context.Context, orgID, ciID string) (any, error)

// IncludeAsset is always available: the canonical asset facts of the CI.
const IncludeAsset = "asset"

// WithIncludes adds the related collections GET /cis/{id}?include= can
// embed (relationships, contacts …) besides the asset.
func (h *Handler) WithIncludes(includes map[string]IncludeFunc) *Handler {
	h.includes = includes
	return h
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
	r.Post("/api/v1/cis/{id}/restore", h.Restore)
	r.Post("/api/v1/cis/{id}/type", h.ChangeType)
	r.Post("/api/v1/cis/{id}/merge", h.Merge)
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
	// filter[field][op], sort, fields and q (API-02); the former single
	// query parameters stay as shorthands.
	lq, err := api.ParseListQuery(r, ListFields)
	if err != nil {
		api.WriteFieldError(w, err)
		return
	}
	filter := FilterParams{
		Status:   r.URL.Query().Get("status"),
		TypeID:   r.URL.Query().Get("ci_type_id"),
		ClientID: r.URL.Query().Get("client_id"),
		SiteID:   r.URL.Query().Get("site_id"),
		RoomID:   r.URL.Query().Get("room_id"),
		Search:   r.URL.Query().Get("search"),
		SortBy:   lq.SortBy,
		SortDir:  lq.SortDir,
		Filters:  lq.Filters,
	}
	if lq.Q != "" {
		filter.Search = lq.Q
	}

	items, total, err := h.svc.List(r.Context(), t.OrganizationID, filter, page)
	if err != nil {
		if errors.Is(err, api.ErrInvalidCursor) {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
			return
		}
		if api.WriteFieldError(w, err) {
			return
		}
		api.WriteRepoError(w, err)
		return
	}

	hasMore := page.Offset+page.Limit < total
	if page.Cursor != nil {
		hasMore = len(items) == page.Limit
	}
	next := NextCursor(items, filter, page.Limit)
	if len(lq.Fields) > 0 {
		data, perr := api.SelectFields(items, lq.Fields)
		if perr != nil {
			api.WriteRepoError(w, perr)
			return
		}
		api.WriteJSON(w, http.StatusOK, api.ListResponse[map[string]json.RawMessage]{Data: data, Total: total, Limit: page.Limit,
			Offset: page.Offset, HasMore: hasMore, NextCursor: next})
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Item]{
		Data:       items,
		Total:      total,
		Limit:      page.Limit,
		Offset:     page.Offset,
		HasMore:    hasMore,
		NextCursor: next,
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
		// A merged CI points to its survivor for 12 months (CI-12).
		if survivor, ok, merr := h.svc.MergedInto(r.Context(), t.OrganizationID, id); merr == nil && ok {
			w.Header().Set("Location", "/api/v1/cis/"+survivor)
			api.WriteError(w, http.StatusMovedPermanently, "Moved Permanently", "the CI was merged into "+survivor)
			return
		}
		api.WriteError(w, http.StatusNotFound, "Not Found", "CI not found")
		return
	}

	w.Header().Set("ETag", api.ETag(item.Version))
	if raw := strings.TrimSpace(r.URL.Query().Get("include")); raw != "" {
		body, ierr := h.withIncludes(r.Context(), t.OrganizationID, item, raw)
		if ierr != nil {
			if !api.WriteFieldError(w, ierr) {
				api.WriteRepoError(w, ierr)
			}
			return
		}
		api.WriteJSON(w, http.StatusOK, body)
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

// withIncludes embeds the requested related collections into the CI
// (API-05): include=relationships,contacts,asset. An unknown name is 422.
func (h *Handler) withIncludes(ctx context.Context, orgID string, item *Item, raw string) (map[string]json.RawMessage, error) {
	encoded, err := json.Marshal(item)
	if err != nil {
		return nil, err
	}
	var body map[string]json.RawMessage
	if uerr := json.Unmarshal(encoded, &body); uerr != nil {
		return nil, uerr
	}
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		var value any
		switch load, ok := h.includes[name]; {
		case name == IncludeAsset:
			value = item.Asset
		case ok:
			if value, err = load(ctx, orgID, item.ID); err != nil {
				return nil, err
			}
		default:
			known := []string{IncludeAsset}
			for k := range h.includes {
				known = append(known, k)
			}
			sort.Strings(known)
			return nil, &api.FieldError{Field: "include", Status: http.StatusUnprocessableEntity,
				Message: fmt.Sprintf("unknown include %q; allowed: %s", name, strings.Join(known, ", "))}
		}
		if body[name], err = json.Marshal(value); err != nil {
			return nil, err
		}
	}
	return body, nil
}

// Restore handles POST /api/v1/cis/{id}/restore: a deleted CI comes back
// within the retention (CI-05); after it the answer is 410.
func (h *Handler) Restore(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	item, err := h.svc.Restore(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	switch {
	case errors.Is(err, ErrRetentionExpired):
		api.WriteError(w, http.StatusGone, "Gone", err.Error())
		return
	case errors.Is(err, ErrMergeConflict):
		api.WriteError(w, http.StatusConflict, "Conflict", "the CI was merged; use the unmerge")
		return
	case err != nil && err.Error() == "not found":
		api.WriteError(w, http.StatusNotFound, "Not Found", "no deleted CI with this id")
		return
	case err != nil:
		if api.WriteFieldError(w, err) {
			return
		}
		api.WriteRepoError(w, err)
		return
	}
	w.Header().Set("ETag", api.ETag(item.Version))
	api.WriteJSON(w, http.StatusOK, item)
}

// ChangeType handles POST /api/v1/cis/{id}/type: the manual type change
// (CI-11). Missing required attributes of the target type are findings.
func (h *Handler) ChangeType(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var body struct {
		CITypeID string `json:"ci_type_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.CITypeID) == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "ci_type_id is required")
		return
	}
	result, err := h.svc.ChangeType(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), strings.TrimSpace(body.CITypeID))
	switch {
	case errors.Is(err, ErrUnknownType):
		api.WriteError(w, http.StatusUnprocessableEntity, "Unprocessable Entity", err.Error())
		return
	case err != nil && err.Error() == "not found":
		api.WriteError(w, http.StatusNotFound, "Not Found", "CI not found")
		return
	case err != nil:
		api.WriteRepoError(w, err)
		return
	}
	w.Header().Set("ETag", api.ETag(result.CI.Version))
	api.WriteJSON(w, http.StatusOK, result)
}

// Merge handles POST /api/v1/cis/{id}/merge {victim_id}: the victim is
// merged into the CI of the path (CI-12).
func (h *Handler) Merge(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var body struct {
		VictimID string `json:"victim_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.VictimID) == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "victim_id is required")
		return
	}
	result, err := h.svc.Merge(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), strings.TrimSpace(body.VictimID))
	switch {
	case errors.Is(err, ErrMergeConflict):
		api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
		return
	case errors.Is(err, ErrMergeSelf):
		api.WriteError(w, http.StatusUnprocessableEntity, "Unprocessable Entity", err.Error())
		return
	case errors.Is(err, ErrMergeUnsupported):
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", err.Error())
		return
	case err != nil && err.Error() == "not found":
		api.WriteError(w, http.StatusNotFound, "Not Found", "survivor or victim not found")
		return
	case err != nil:
		if api.WriteFieldError(w, err) {
			return
		}
		api.WriteRepoError(w, err)
		return
	}
	w.Header().Set("ETag", api.ETag(result.Survivor.Version))
	api.WriteJSON(w, http.StatusOK, result)
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
		AssetID:         req.AssetID,
		ParentCIID:      req.ParentCIID,
		CITypeID:        req.CITypeID,
		Name:            req.Name,
		Status:          status,
		Manufacturer:    req.Manufacturer,
		Model:           req.Model,
		SerialNumber:    req.SerialNumber,
		HardwareUUID:    req.HardwareUUID,
		ManagementIP:    req.ManagementIP,
		PrimaryMAC:      req.PrimaryMAC,
		Hostname:        req.Hostname,
		FQDN:            req.FQDN,
		OSName:          req.OSName,
		OSVersion:       req.OSVersion,
		FirmwareVersion: req.FirmwareVersion,
		SysObjectID:     req.SysObjectID,
		Attributes:      req.Attributes,
		DiscoverySource: req.DiscoverySource,
	}
	if item.Attributes == nil {
		item.Attributes = make(map[string]any)
	}
	if item.DiscoverySource == "" {
		item.DiscoverySource = SourceManual
	}
	if !IsValidDiscoverySource(item.DiscoverySource) {
		api.WriteFieldError(w, &api.FieldError{Field: "discovery_source", Message: "unknown discovery source"})
		return
	}
	// A CI a person creates — by hand or by import — is manual (CI-01).
	item.IsManual = IsManualSource(item.DiscoverySource)

	if err := h.svc.Create(r.Context(), item); err != nil {
		if isEntitlementError(err) {
			if !api.WriteTypedProblem(w, http.StatusForbidden, err) {
				api.WriteError(w, http.StatusForbidden, "Forbidden", err.Error())
			}
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
	// overrides (OVR-01) and its If-Match is checked (API-07).
	req.Manual = &ManualChange{Author: t.UserID}
	version, present, err := api.ParseIfMatch(r)
	if err != nil {
		api.WritePreconditionFailed(w, err.Error())
		return
	}
	if present {
		req.IfMatch = &version
	}

	item, err := h.svc.Update(r.Context(), t.OrganizationID, id, req)
	if err != nil {
		if writeConcurrencyError(w, err) {
			return
		}
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

	w.Header().Set("ETag", api.ETag(item.Version))
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
	// Reserved attribute keys are a malformed request (GLO-13): 400 with the
	// fields.
	var reserved *ReservedKeyError
	if errors.As(err, &reserved) {
		violations := make([]Violation, len(reserved.Fields))
		for i, f := range reserved.Fields {
			violations[i] = Violation{Field: f, Detail: "reserved namespace, not allowed as an attribute key"}
		}
		api.WriteJSON(w, http.StatusBadRequest, struct {
			api.ProblemDetail
			Violations []Violation `json:"violations"`
		}{
			ProblemDetail: api.ProblemDetail{Type: "https://reticora.io/problems/400", Title: "Bad Request",
				Status: http.StatusBadRequest, Detail: reserved.Error()},
			Violations: violations,
		})
		return true
	}
	// A value of a unique attribute another CI holds (MET-15): 409 with the
	// attributes.
	var taken *UniqueError
	if errors.As(err, &taken) {
		violations := make([]Violation, len(taken.Fields))
		for i, f := range taken.Fields {
			violations[i] = Violation{Field: f, Detail: "another CI already holds this value of the unique attribute"}
		}
		api.WriteJSON(w, http.StatusConflict, struct {
			api.ProblemDetail
			Violations []Violation `json:"violations"`
		}{
			ProblemDetail: api.ProblemDetail{Type: "urn:reticora:problem:conflict", Title: "Conflict",
				Status: http.StatusConflict, Detail: taken.Error()},
			Violations: violations,
		})
		return true
	}
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

// writeConcurrencyError maps the If-Match outcomes of API-07/API-03: 412
// for a missing (when required) or unknown version, 409 with the fields
// changed since. It reports whether err was one of them.
func writeConcurrencyError(w http.ResponseWriter, err error) bool {
	var conflict *ConflictError
	switch {
	case errors.Is(err, ErrPreconditionRequired), errors.Is(err, ErrPreconditionFailed):
		api.WritePreconditionFailed(w, err.Error())
	case errors.As(err, &conflict):
		w.Header().Set("ETag", api.ETag(conflict.Current))
		api.WriteJSON(w, http.StatusConflict, struct {
			api.ProblemDetail
			Fields         []string `json:"fields"`
			CurrentVersion int64    `json:"current_version"`
		}{
			ProblemDetail: api.ProblemDetail{
				Type: "urn:reticora:problem:conflict", Title: "Conflict", Status: http.StatusConflict,
				Detail: conflict.Error(),
			},
			Fields: conflict.Fields, CurrentVersion: conflict.Current,
		})
	default:
		return false
	}
	return true
}
