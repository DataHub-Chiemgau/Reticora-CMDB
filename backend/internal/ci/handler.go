package ci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
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
	provenance ProvenanceSource
	// parentAssets projects the asset of GET /cis/{id}/parent (WP-742).
	parentAssets ParentAssetLookup
}

// IncludeFunc loads one related collection of a CI for ?include= (API-05);
// truncated reports that the collection holds more rows the caller may see
// than it returns.
type IncludeFunc func(ctx context.Context, orgID, ciID string) (items any, truncated bool, err error)

// IncludeTruncated is the member of a CI with includes that names the
// embedded collections capped by the server (truncated.<name> = true).
const IncludeTruncated = "truncated"

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
	r.Get("/api/v1/cis/{id}/provenance", h.Provenance)
	r.Patch("/api/v1/cis/{id}", h.Update)
	r.Delete("/api/v1/cis/{id}", h.Delete)
	r.Post("/api/v1/cis/{id}/restore", h.Restore)
	r.Post("/api/v1/cis/{id}/type", h.ChangeType)
	r.Post("/api/v1/cis/{id}/merge", h.Merge)
	r.Post("/api/v1/cis/bulk", h.Bulk)
	r.Post("/api/v1/cis/{id}/unmerge", h.Unmerge)
	// Instance attribute values (MET-14, WP-256).
	r.Get("/api/v1/cis/{id}/instance-attributes", h.ListInstanceAttributes)
	r.Put("/api/v1/cis/{id}/instance-attributes/{key}", h.SetInstanceAttribute)
	r.Delete("/api/v1/cis/{id}/instance-attributes/{key}", h.DeleteInstanceAttribute)
	// Templates of a CI (MET-16, WP-710).
	r.Get("/api/v1/cis/{id}/templates", h.ListTemplates)
	r.Put("/api/v1/cis/{id}/templates", h.SetTemplates)
	// Tag catalog and tags of a CI (CI-08, WP-753).
	h.registerTagRoutes(r)
	// The legacy composition paths on parent_ci_id (WP-742).
	r.Get("/api/v1/cis/{id}/parent", h.Parent)
	r.Get("/api/v1/assets/{id}/children", h.AssetChildren)
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
		// total after E-104: exact up to api.TotalCap, beyond it capped.
		CappedTotal: true,
	}
	if lq.Q != "" {
		filter.Search = lq.Q
	}
	// A filter on a field the principal cannot read in every scope is an
	// oracle of its values: 403 with the fields, before the list and its
	// total (RBA-07, WP-265).
	policy, ok := requestPolicy(w, r)
	if !ok {
		return
	}
	if ferr := checkListFilters(policy, &filter); ferr != nil {
		api.WriteFieldError(w, ferr)
		return
	}

	items, counted, err := h.svc.List(r.Context(), t.OrganizationID, &filter, page)
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

	// The count reaches past the page when more CIs follow it, also when
	// it was capped; total shows at most api.TotalCap (E-104).
	hasMore := page.Offset+page.Limit < counted
	if page.Cursor != nil {
		hasMore = len(items) == page.Limit
	}
	total, capped := api.CapTotal(counted)
	next := NextCursor(items, &filter, page.Limit)
	if permission.NeedsProjection(policy, permission.KindCI) {
		// The projection first, then the selection: a field the principal
		// cannot read is missing per CI, also when fields= names it.
		data := make([]map[string]json.RawMessage, len(items))
		for i := range items {
			projected, perr := permission.Project(policy, permission.KindCI, items[i].CITypeID, ciRef(&items[i]), &items[i],
				ciProjectOptions(&items[i])...)
			if perr != nil {
				api.WriteRepoError(w, perr)
				return
			}
			data[i] = projected
		}
		if len(lq.Fields) > 0 {
			selected, serr := api.SelectFields(data, lq.Fields)
			if serr != nil {
				api.WriteRepoError(w, serr)
				return
			}
			data = selected
		}
		api.WriteJSON(w, http.StatusOK, api.ListResponse[map[string]json.RawMessage]{Data: data, Total: total, TotalCapped: capped,
			Limit: page.Limit, Offset: page.Offset, HasMore: hasMore, NextCursor: next})
		return
	}
	if len(lq.Fields) > 0 {
		data, perr := api.SelectFields(items, lq.Fields)
		if perr != nil {
			api.WriteRepoError(w, perr)
			return
		}
		api.WriteJSON(w, http.StatusOK, api.ListResponse[map[string]json.RawMessage]{Data: data, Total: total, TotalCapped: capped, Limit: page.Limit,
			Offset: page.Offset, HasMore: hasMore, NextCursor: next})
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Item]{
		Data:        items,
		Total:       total,
		TotalCapped: capped,
		Limit:       page.Limit,
		Offset:      page.Offset,
		HasMore:     hasMore,
		NextCursor:  next,
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

	policy, ok := requestPolicy(w, r)
	if !ok {
		return
	}
	w.Header().Set("ETag", api.ETag(item.Version))
	if raw := strings.TrimSpace(r.URL.Query().Get("include")); raw != "" {
		body, ierr := h.withIncludes(r.Context(), t.OrganizationID, policy, item, raw)
		if ierr != nil {
			if !api.WriteFieldError(w, ierr) {
				api.WriteRepoError(w, ierr)
			}
			return
		}
		api.WriteJSON(w, http.StatusOK, body)
		return
	}
	writeCI(w, http.StatusOK, policy, item)
}

// withIncludes embeds the requested related collections into the CI
// (API-05): include=relationships,contacts,asset. An unknown name is 422.
// A collection the server capped is named in truncated (F-223).
// The CI and its asset facts are projected by the field permissions, the
// references like the attributes (RBA-07, WP-265).
func (h *Handler) withIncludes(ctx context.Context, orgID string, policy *permission.FieldPolicy, item *Item, raw string) (map[string]json.RawMessage, error) {
	projecting := permission.NeedsProjection(policy, permission.KindCI)
	var body map[string]json.RawMessage
	var err error
	if projecting {
		body, err = permission.Project(policy, permission.KindCI, item.CITypeID, ciRef(item), item, ciProjectOptions(item)...)
		if err != nil {
			return nil, err
		}
	} else {
		encoded, merr := json.Marshal(item)
		if merr != nil {
			return nil, merr
		}
		if uerr := json.Unmarshal(encoded, &body); uerr != nil {
			return nil, uerr
		}
	}
	truncated := map[string]bool{}
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		var value any
		switch load, ok := h.includes[name]; {
		case name == IncludeAsset && projecting:
			// The projected asset facts of the CI, null without them.
			if body[name] == nil {
				body[name] = json.RawMessage("null")
			}
			continue
		case name == IncludeAsset:
			value = item.Asset
		case ok:
			var capped bool
			if value, capped, err = load(ctx, orgID, item.ID); err != nil {
				return nil, err
			}
			if capped {
				truncated[name] = true
			}
			if refs, isRefs := value.(map[string]any); isRefs && name == IncludeReferences {
				value = permission.ProjectAttributes(policy, item.CITypeID, ciRef(item), refs, ciProjectOptions(item)...)
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
	if len(truncated) > 0 {
		if body[IncludeTruncated], err = json.Marshal(truncated); err != nil {
			return nil, err
		}
	}
	return body, nil
}

// Restore handles POST /api/v1/cis/{id}/restore: a deleted CI comes back
// within the retention (CI-05); after it the answer is 410. A CI that is not
// deleted or was merged is 409.
func (h *Handler) Restore(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	// The field policy is resolved before the write (fail-closed, WP-265).
	policy, ok := requestPolicy(w, r)
	if !ok {
		return
	}
	// ci:delete after the action grants on the type and scope of the
	// deleted CI (WP-728).
	if !h.allowAction(w, r, identity.PermCIDelete, "no deleted CI with this id", chi.URLParam(r, "id")) {
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
	case errors.Is(err, ErrNotDeleted):
		api.WriteError(w, http.StatusConflict, "Conflict", "the CI is not deleted")
		return
	case errors.Is(err, ErrConfigurationParentDeleted):
		api.WriteError(w, http.StatusConflict, "Conflict", ErrConfigurationParentDeleted.Error())
		return
	case err != nil && err.Error() == "not found":
		api.WriteError(w, http.StatusNotFound, "Not Found", "no deleted CI with this id")
		return
	case api.WriteEntitlementError(w, err):
		// max_cis is reached: the CI stays deleted (ENT-03).
		return
	case err != nil:
		if api.WriteFieldError(w, err) {
			return
		}
		api.WriteRepoError(w, err)
		return
	}
	w.Header().Set("ETag", api.ETag(item.Version))
	writeCI(w, http.StatusOK, policy, item)
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
	// Values the principal cannot read in the source type do not move
	// into another type's grants (RBA-07, WP-265).
	policy, ok := requestPolicy(w, r)
	if !ok {
		return
	}
	// The action grant ci:write on the current type before the field
	// permissions (WP-728).
	if !h.allowAction(w, r, identity.PermCIWrite, "CI not found", chi.URLParam(r, "id")) {
		return
	}
	if !h.refuseUnreadableValues(w, r, policy, chi.URLParam(r, "id")) {
		return
	}
	result, err := h.svc.ChangeType(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), strings.TrimSpace(body.CITypeID))
	switch {
	case errors.Is(err, ErrUnknownType):
		// Unknown and of another organization alike (TEN-09).
		writeValidationError(w, ciTypeViolation(ciTypeNotFound))
		return
	case err != nil && err.Error() == "not found":
		api.WriteError(w, http.StatusNotFound, "Not Found", "CI not found")
		return
	case err != nil:
		if !writeValidationError(w, err) {
			api.WriteRepoError(w, err)
		}
		return
	}
	w.Header().Set("ETag", api.ETag(result.CI.Version))
	if permission.NeedsProjection(policy, permission.KindCI) && result.CI != nil {
		// The CI after the change is projected by its new type; findings
		// name only readable fields.
		findings := make([]TypeFinding, 0, len(result.Findings))
		for _, f := range result.Findings {
			if ciFieldReadable(policy, result.CI, f.Field) {
				findings = append(findings, f)
			}
		}
		projected := *result
		projected.Findings = findings
		writeWithCIs(w, http.StatusOK, policy, &projected, map[string]*Item{"ci": result.CI})
		return
	}
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
	// Survivor and victim carry no value the principal cannot read in
	// their type: the merge would move or reveal it (RBA-07, WP-265).
	policy, ok := requestPolicy(w, r)
	if !ok {
		return
	}
	// ci:delete on survivor and victim after the action grants, before the
	// field permissions (WP-728).
	if !h.allowAction(w, r, identity.PermCIDelete, "survivor or victim not found", chi.URLParam(r, "id"), strings.TrimSpace(body.VictimID)) {
		return
	}
	if !h.refuseUnreadableValues(w, r, policy, chi.URLParam(r, "id"), strings.TrimSpace(body.VictimID)) {
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
	if permission.NeedsProjection(policy, permission.KindCI) && result.Survivor != nil {
		projected := *result
		projected.OverrideConflicts = make([]string, 0, len(result.OverrideConflicts))
		for _, field := range result.OverrideConflicts {
			if ciFieldReadable(policy, result.Survivor, field) {
				projected.OverrideConflicts = append(projected.OverrideConflicts, field)
			}
		}
		writeWithCIs(w, http.StatusOK, policy, &projected, map[string]*Item{"survivor": result.Survivor})
		return
	}
	api.WriteJSON(w, http.StatusOK, result)
}

// ProblemUnmergeLaterMerge is the problem type of an unmerge a later open
// merge of the survivor blocks (409, F-416).
const ProblemUnmergeLaterMerge = "urn:reticora:problem:unmerge-later-merge"

// Unmerge handles POST /api/v1/cis/{id}/unmerge: the merged CI of the path
// and its survivor return to their state before the merge, within 30 days
// (CI-12); afterwards 410. A survivor changed after the merge answers 409
// with the changed fields; ?force=true restores the snapshot over them
// (E-80). A survivor that is part of a later open merge answers 409
// unmerge-later-merge naming that merge, also with force (F-416).
func (h *Handler) Unmerge(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	// The field policy is resolved before the write (fail-closed, WP-265).
	policy, ok := requestPolicy(w, r)
	if !ok {
		return
	}
	force := false
	if raw := r.URL.Query().Get("force"); raw != "" {
		parsed, perr := strconv.ParseBool(raw)
		if perr != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "force must be true or false")
			return
		}
		force = parsed
	}
	result, err := h.svc.Unmerge(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), force)
	var changed *UnmergeConflictError
	var later *UnmergeLaterMergeError
	switch {
	case errors.As(err, &later):
		// A later open merge of the survivor is unmerged first; force does
		// not override it (F-416).
		api.WriteProblemBody(w, http.StatusConflict, struct {
			api.ProblemDetail
			LaterMergeID         string `json:"later_merge_id,omitempty"`
			LaterMergeVictimID   string `json:"later_merge_victim_id"`
			LaterMergeSurvivorID string `json:"later_merge_survivor_id,omitempty"`
		}{
			ProblemDetail: api.ProblemDetail{
				Type: ProblemUnmergeLaterMerge, Title: "Conflict", Status: http.StatusConflict,
				Detail: later.Error(),
			},
			LaterMergeID: later.MergeID, LaterMergeVictimID: later.VictimID, LaterMergeSurvivorID: later.SurvivorID,
		})
		return
	case errors.As(err, &changed):
		w.Header().Set("ETag", api.ETag(changed.Current))
		api.WriteProblemBody(w, http.StatusConflict, struct {
			api.ProblemDetail
			Fields         []string `json:"fields"`
			CurrentVersion int64    `json:"current_version"`
		}{
			ProblemDetail: api.ProblemDetail{
				Type: "urn:reticora:problem:conflict", Title: "Conflict", Status: http.StatusConflict,
				Detail: changed.Error(),
			},
			Fields: changed.Fields, CurrentVersion: changed.Current,
		})
		return
	case errors.Is(err, ErrSnapshotExpired):
		api.WriteError(w, http.StatusGone, "Gone", err.Error())
		return
	case errors.Is(err, ErrMergeConflict):
		api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
		return
	case errors.Is(err, ErrMergeUnsupported):
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", err.Error())
		return
	case err != nil && err.Error() == "not found":
		api.WriteError(w, http.StatusNotFound, "Not Found", "no merge of this CI to revert")
		return
	case api.WriteEntitlementError(w, err):
		// max_cis is reached: the merge stays (ENT-03).
		return
	case err != nil:
		if api.WriteFieldError(w, err) {
			return
		}
		api.WriteRepoError(w, err)
		return
	}
	if permission.NeedsProjection(policy, permission.KindCI) {
		writeWithCIs(w, http.StatusOK, policy, result, map[string]*Item{"survivor": result.Survivor, "victim": result.Victim})
		return
	}
	api.WriteJSON(w, http.StatusOK, result)
}

// Bulk handles POST /api/v1/cis/bulk (API-08, BLK-01): below 100 ids the
// answer is the report per id (200); from 100 ids on 202 with the job
// whose result is the report.
func (h *Handler) Bulk(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req BulkRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	result, job, err := h.svc.Bulk(r.Context(), t.OrganizationID, &req)
	switch {
	case errors.Is(err, ErrBulkActionUnavailable):
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", ErrBulkActionUnavailable.Error())
	case err != nil:
		// A missing entitlement of export is 403 feature-not-entitled
		// (WP-780); a missing right 403 with the action.
		switch {
		case api.WriteFieldError(w, err):
		case IsFeatureNotEntitled(err):
			api.WriteTypedProblem(w, http.StatusForbidden, err)
		default:
			api.WriteRepoError(w, err)
		}
	case job != nil:
		// The job ci.bulk, or the export job of export (WP-780).
		w.Header().Set("Location", BulkJobLocation(job))
		api.WriteJSON(w, http.StatusAccepted, map[string]any{"job_id": job.ID, "status": job.Status, "kind": job.Kind})
	default:
		api.WriteJSON(w, http.StatusOK, result)
	}
}

// The lifecycle of the deprecated write attributes.tags (E-125, API-01):
// deprecated with WP-281, switched off at the sunset, at least 12 months
// after the G2 release. Then attributes.tags answers 400.
var (
	legacyTagsDeprecatedAt = time.Date(2026, time.November, 3, 0, 0, 0, 0, time.UTC)
	legacyTagsSunset       = time.Date(2028, time.June, 30, 0, 0, 0, 0, time.UTC)
)

// markLegacyTags sets Deprecation and Sunset (RFC 9745, RFC 8594, in the
// form of the deprecated operations, middleware.writeDeprecation) on the
// answer of a write that carries attributes.tags (E-125).
func markLegacyTags(w http.ResponseWriter, attributes map[string]any) {
	if _, ok := attributes[LegacyTagsKey]; !ok {
		return
	}
	w.Header().Set("Deprecation", "@"+strconv.FormatInt(legacyTagsDeprecatedAt.Unix(), 10))
	w.Header().Set("Sunset", legacyTagsSunset.Format(http.TimeFormat))
}

// Create handles POST /api/v1/cis
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	// The field policy is resolved before the write (fail-closed, WP-265).
	policy, ok := requestPolicy(w, r)
	if !ok {
		return
	}

	var req CreateRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	markLegacyTags(w, req.Attributes)
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
		ChildMode:       req.ChildMode,
		OwnerUserID:     req.OwnerUserID,
		OwnerTeamID:     req.OwnerTeamID,
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
		Templates:       req.Templates,
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
		// Fields the principal may not write (RBA-07, WP-265): 403 with
		// the fields, before the validation.
		if writeFieldForbidden(w, err) {
			return
		}
		if isEntitlementError(err) {
			if !api.WriteTypedProblem(w, http.StatusForbidden, err) {
				api.WriteError(w, http.StatusForbidden, "Forbidden", err.Error())
			}
			return
		}
		if writeValidationError(w, err) {
			return
		}
		if writeTemplatesUnavailable(w, err) {
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

	writeCI(w, http.StatusCreated, policy, item)
}

// Update handles PATCH /api/v1/cis/{id}
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	// The field policy is resolved before the write (fail-closed, WP-265).
	policy, ok := requestPolicy(w, r)
	if !ok {
		return
	}

	id := chi.URLParam(r, "id")
	var req UpdateRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	markLegacyTags(w, req.Attributes)
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
		// Fields the principal may not write (RBA-07, WP-265): 403 with
		// the fields, checked before the validation and If-Match.
		if writeFieldForbidden(w, err) {
			return
		}
		if writeConcurrencyError(w, err) {
			return
		}
		if writeValidationError(w, err) {
			return
		}
		// A changed instance attribute without its right (MET-14, WP-256).
		if errors.Is(err, ErrInstanceAttributeForbidden) {
			api.WriteError(w, http.StatusForbidden, "Forbidden", instanceForbiddenDetail)
			return
		}
		// Only a missing CI is 404; any other failure is not hidden as one
		// (F-229).
		if errors.Is(err, ErrNotFound) {
			api.WriteError(w, http.StatusNotFound, "Not Found", "CI not found")
			return
		}
		api.WriteRepoError(w, err)
		return
	}
	if h.dispatcher != nil {
		h.dispatcher.Dispatch(r.Context(), t.OrganizationID, "ci.updated", item)
	}

	w.Header().Set("ETag", api.ETag(item.Version))
	writeCI(w, http.StatusOK, policy, item)
}

// Delete handles DELETE /api/v1/cis/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	// ci:delete after the action grants on the type and scope of the CI
	// (WP-728).
	if !h.allowAction(w, r, identity.PermCIDelete, "CI not found", id) {
		return
	}
	item, err := h.svc.Delete(r.Context(), t.OrganizationID, id)
	if errors.Is(err, ErrConfigurationChildren) {
		// E-123 (b): no cascade to the configuration children.
		api.WriteError(w, http.StatusConflict, "Conflict", ErrConfigurationChildren.Error())
		return
	}
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
	policy, ok := requestPolicy(w, r)
	if !ok {
		return
	}
	if permission.NeedsProjection(policy, permission.KindCI) {
		if changes, err = h.projectChanges(r.Context(), t.OrganizationID, id, policy, changes); err != nil {
			api.WriteRepoError(w, err)
			return
		}
	}

	api.WriteJSON(w, http.StatusOK, api.ListResponse[Change]{
		Data:    changes,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// writeTemplatesUnavailable answers 503 for ErrTemplatesUnavailable
// (WP-710: template assignments need the database). It reports whether err
// was it.
func writeTemplatesUnavailable(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, ErrTemplatesUnavailable) {
		return false
	}
	api.WriteUnavailable(w, "template assignments need the database")
	return true
}

// ListTemplates handles GET /api/v1/cis/{id}/templates: the templates of
// the CI in position order, a complete, bounded list (api.CompleteList).
func (h *Handler) ListTemplates(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	items, err := h.svc.ListTemplates(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	switch {
	case writeTemplatesUnavailable(w, err):
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "CI not found")
	case err != nil:
		api.WriteRepoError(w, err)
	default:
		api.WriteJSON(w, http.StatusOK, api.CompleteList(items))
	}
}

// SetTemplatesRequest is the body of PUT /api/v1/cis/{id}/templates: the
// ordered list [{template_id, version}].
type SetTemplatesRequest struct {
	Templates []citype.CITemplate `json:"templates"`
}

// SetTemplates handles PUT /api/v1/cis/{id}/templates: replaces the ordered
// template list of the CI (If-Match, API-07) and answers the CI with its
// new ETag.
func (h *Handler) SetTemplates(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	// The field policy is resolved before the write (fail-closed, WP-265).
	policy, ok := requestPolicy(w, r)
	if !ok {
		return
	}
	if !h.svc.TemplatesAvailable() {
		writeTemplatesUnavailable(w, ErrTemplatesUnavailable)
		return
	}
	var req SetTemplatesRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Templates == nil {
		api.WriteFieldError(w, &api.FieldError{Field: "templates", Message: "templates is required (an empty list removes every template)"})
		return
	}
	version, present, err := api.ParseIfMatch(r)
	if err != nil {
		api.WritePreconditionFailed(w, err.Error())
		return
	}
	var ifMatch *int64
	if present {
		ifMatch = &version
	}
	item, err := h.svc.SetTemplates(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req.Templates, ifMatch, t.UserID)
	switch {
	case err == nil:
	case writeFieldForbidden(w, err), writeConcurrencyError(w, err), writeValidationError(w, err), writeTemplatesUnavailable(w, err):
		return
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "CI not found")
		return
	default:
		api.WriteRepoError(w, err)
		return
	}
	if h.dispatcher != nil {
		h.dispatcher.Dispatch(r.Context(), t.OrganizationID, "ci.updated", item)
	}
	w.Header().Set("ETag", api.ETag(item.Version))
	writeCI(w, http.StatusOK, policy, item)
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
	// attributes.tags that is no array of strings is a malformed request
	// (E-125): 400 on the field.
	var legacyTags *LegacyTagsError
	if errors.As(err, &legacyTags) {
		api.WriteJSON(w, http.StatusBadRequest, struct {
			api.ProblemDetail
			Violations []Violation `json:"violations"`
		}{
			ProblemDetail: api.ProblemDetail{Type: "https://reticora.io/problems/400", Title: "Bad Request",
				Status: http.StatusBadRequest, Detail: legacyTags.Error()},
			Violations: []Violation{{Field: "attributes.tags", Detail: "must be an array of strings"}},
		})
		return true
	}
	// A value of a unique attribute another CI holds (MET-15): 409 with the
	// attributes (UniqueError.ConflictViolations), as every handler that
	// passes the error to api.WriteFieldError answers it.
	var taken *UniqueError
	if errors.As(err, &taken) {
		return api.WriteFieldError(w, taken)
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

// Field permissions of the CI REST API (RBA-07, E-43, WP-265): the answers
// carry only the fields the principal may read (permission.Project), the
// writes are checked by the service (Service.Create, Service.Update).

// requestPolicy returns the field policy of the request; when it cannot be
// resolved it answers 500 and reports false (fail-closed).
func requestPolicy(w http.ResponseWriter, r *http.Request) (*permission.FieldPolicy, bool) {
	policy, err := permission.PolicyFrom(r.Context())
	if err != nil {
		api.WriteRepoError(w, err)
		return nil, false
	}
	return policy, true
}

// allowAction checks the action grant of the route on the CIs (RBA-07,
// WP-728) and reports whether the request may go on: 403 with the action
// when the grants refuse it, 404 with notFound for a CI the check could
// not read, 500 otherwise (fail-closed).
func (h *Handler) allowAction(w http.ResponseWriter, r *http.Request, action identity.Permission, notFound string, ids ...string) bool {
	err := h.svc.CheckAction(r.Context(), tenant.FromContext(r.Context()).OrganizationID, action, ids...)
	switch {
	case err == nil:
		return true
	case writeFieldForbidden(w, err):
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", notFound)
	default:
		api.WriteRepoError(w, err)
	}
	return false
}

// ciRef is the object reference of a CI for its field permissions.
func ciRef(item *Item) permission.ObjectRef {
	return permission.ObjectRef{ID: item.ID, ClientID: item.ClientID, SiteID: item.SiteID}
}

// ciProjectOptions name the instance attributes of the CI and the type and
// scope of its asset.
func ciProjectOptions(item *Item) []permission.ProjectOption {
	opts := []permission.ProjectOption{permission.WithInstanceKeys(item.InstanceKeys)}
	if item.Asset != nil {
		opts = append(opts, permission.WithAsset(item.Asset.AssetTypeID,
			permission.ObjectRef{ID: item.Asset.ID, ClientID: item.Asset.ClientID, SiteID: item.Asset.SiteID}))
	}
	return opts
}

// projectCI returns the CI as the policy lets the principal read it: the
// item itself without a restriction.
func projectCI(policy *permission.FieldPolicy, item *Item) (any, error) {
	return permission.ProjectValue(policy, permission.KindCI, item.CITypeID, ciRef(item), item, ciProjectOptions(item)...)
}

// writeCI answers the projected CI.
func writeCI(w http.ResponseWriter, status int, policy *permission.FieldPolicy, item *Item) {
	body, err := projectCI(policy, item)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, status, body)
}

// writeWithCIs answers result with its CI members (by JSON name) replaced
// by their projection.
func writeWithCIs(w http.ResponseWriter, status int, policy *permission.FieldPolicy, result any, members map[string]*Item) {
	encoded, err := json.Marshal(result)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	var body map[string]json.RawMessage
	if err = json.Unmarshal(encoded, &body); err != nil {
		api.WriteRepoError(w, err)
		return
	}
	for name, item := range members {
		if item == nil {
			continue
		}
		projected, perr := projectCI(policy, item)
		if perr != nil {
			api.WriteRepoError(w, perr)
			return
		}
		if body[name], err = json.Marshal(projected); err != nil {
			api.WriteRepoError(w, err)
			return
		}
	}
	api.WriteJSON(w, status, body)
}

// writeFieldForbidden answers a *api.FieldForbiddenError (403 with the
// fields) and reports whether err was one.
func writeFieldForbidden(w http.ResponseWriter, err error) bool {
	var forbidden *api.FieldForbiddenError
	if !errors.As(err, &forbidden) {
		return false
	}
	return api.WriteFieldError(w, forbidden)
}

// ciFieldReadable reports whether the principal may read the field of the
// CI named as provenance, overrides and findings name it (bare name).
func ciFieldReadable(policy *permission.FieldPolicy, item *Item, name string) bool {
	return permission.Readable(policy, permission.KindCI, item.CITypeID, ciRef(item), permission.CIFieldPath(name),
		ciProjectOptions(item)...)
}

// unreadableValues returns the DTO paths of the projected fields with a
// value the principal may not read in the CI's type.
func unreadableValues(policy *permission.FieldPolicy, item *Item) []string {
	var out []string
	ref, opts := ciRef(item), ciProjectOptions(item)
	check := func(path string) {
		if !permission.Readable(policy, permission.KindCI, item.CITypeID, ref, path, opts...) {
			out = append(out, path)
		}
	}
	for name := range typedValues(item) {
		if permission.IsProjectedCIField(name) {
			check(name)
		}
	}
	for name := range ownerValues(item) {
		check(name)
	}
	for key, value := range item.Attributes {
		if value != nil {
			check("attributes." + key)
		}
	}
	return out
}

// refuseUnreadableValues answers 403 with the fields when one of the CIs
// carries a value the principal may not read in its type (type change,
// merge) and reports whether the request may go on. A CI that is not
// visible is 404, as the service would answer.
func (h *Handler) refuseUnreadableValues(w http.ResponseWriter, r *http.Request, policy *permission.FieldPolicy, ids ...string) bool {
	if !policy.Restricted(permission.KindCI) {
		return true
	}
	orgID := tenant.FromContext(r.Context()).OrganizationID
	var refused []string
	for _, id := range ids {
		item, err := h.svc.GetByID(r.Context(), orgID, id)
		switch {
		case errors.Is(err, ErrNotFound):
			api.WriteError(w, http.StatusNotFound, "Not Found", "CI not found")
			return false
		case err != nil:
			api.WriteRepoError(w, err)
			return false
		}
		refused = append(refused, unreadableValues(policy, item)...)
	}
	if len(refused) == 0 {
		return true
	}
	sort.Strings(refused)
	api.WriteFieldError(w, &api.FieldForbiddenError{Fields: slices.Compact(refused)})
	return false
}

// checkListFilters refuses filters on projected fields the principal
// cannot read in every scope for the queried types (WP-265).
func checkListFilters(policy *permission.FieldPolicy, filter *FilterParams) error {
	if len(filter.Filters) == 0 || !policy.Restricted(permission.KindCI) {
		return nil
	}
	paths := make([]string, 0, len(filter.Filters))
	for i := range filter.Filters {
		paths = append(paths, filter.Filters[i].Field)
	}
	var types []string
	if groups, named := filter.namedTypes(); named {
		for _, group := range groups {
			types = append(types, group...)
		}
	}
	return permission.CheckListFilter(policy, permission.KindCI, types, paths)
}

// projectChanges projects the history of a CI (WP-265): entries of a
// field the principal may not read are left out, the attributes of an
// attributes entry are projected per key and the whole CI of a create or
// delete entry like a CI. A CI that cannot be read (deleted) is projected
// with the policy that reads nothing (fail-closed).
func (h *Handler) projectChanges(ctx context.Context, orgID, ciID string, policy *permission.FieldPolicy, changes []Change) ([]Change, error) {
	item, err := h.svc.GetByID(ctx, orgID, ciID)
	switch {
	case errors.Is(err, ErrNotFound):
		item, policy = &Item{ID: ciID}, nil
	case err != nil:
		return nil, err
	}
	ref, opts := ciRef(item), ciProjectOptions(item)
	out := make([]Change, 0, len(changes))
	for i := range changes {
		c := changes[i]
		switch c.FieldName {
		case "":
			if c.OldValue, err = projectChangeCI(policy, item, c.OldValue); err != nil {
				return nil, err
			}
			if c.NewValue, err = projectChangeCI(policy, item, c.NewValue); err != nil {
				return nil, err
			}
		case "attributes":
			c.OldValue = projectChangeAttributes(policy, item, c.OldValue)
			c.NewValue = projectChangeAttributes(policy, item, c.NewValue)
		default:
			if !permission.Readable(policy, permission.KindCI, item.CITypeID, ref, permission.CIFieldPath(c.FieldName), opts...) {
				continue
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// projectChangeCI projects the whole CI of a create or delete entry; its
// asset facts are left out when the asset policy is restricted (the asset
// of the entry is not known).
func projectChangeCI(policy *permission.FieldPolicy, item *Item, value any) (any, error) {
	if _, isObject := value.(map[string]any); !isObject {
		return value, nil
	}
	return permission.Project(policy, permission.KindCI, item.CITypeID, ciRef(item), value,
		permission.WithInstanceKeys(item.InstanceKeys))
}

// projectChangeAttributes projects the attributes of an attributes entry
// per key; a value that is no object is left out (fail-closed).
func projectChangeAttributes(policy *permission.FieldPolicy, item *Item, value any) any {
	attrs, isObject := value.(map[string]any)
	if !isObject {
		return nil
	}
	return permission.ProjectAttributes(policy, item.CITypeID, ciRef(item), attrs, permission.WithInstanceKeys(item.InstanceKeys))
}

// ParentAssetRef is the read-only projection of the asset whose shared
// inventory identity a child CI shows (GET /cis/{id}/parent, spec §5). The
// fields and names are those of the former composition handler (API-01).
type ParentAssetRef struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	AssetTag      string  `json:"asset_tag,omitempty"`
	Status        string  `json:"status,omitempty"`
	SerialNumber  string  `json:"serial_number,omitempty"`
	Barcode       string  `json:"barcode,omitempty"`
	RFIDTag       string  `json:"rfid_tag,omitempty"`
	PurchaseDate  string  `json:"purchase_date,omitempty"`
	PurchaseCost  float64 `json:"purchase_cost,omitempty"`
	Currency      string  `json:"currency,omitempty"`
	Supplier      string  `json:"supplier,omitempty"`
	InvoiceNumber string  `json:"invoice_number,omitempty"`
	WarrantyEnd   string  `json:"warranty_end,omitempty"`
	Location      string  `json:"location,omitempty"`
}

// ParentAssetLookup reads the asset GET /cis/{id}/parent projects; the
// server adapts the asset repository.
type ParentAssetLookup interface {
	GetByID(ctx context.Context, orgID, id string) (*ParentAssetRef, error)
}

// WithParentAssets attaches the asset lookup of GET /cis/{id}/parent.
func (h *Handler) WithParentAssets(lookup ParentAssetLookup) *Handler {
	h.parentAssets = lookup
	return h
}

// legacyInheritedFields are the fields a configuration child inherits from
// its parent asset, as the composition handler named them (API-01).
var legacyInheritedFields = []string{
	"asset_tag", "serial_number", "barcode", "rfid_tag",
	"purchase_date", "purchase_cost", "supplier", "invoice_number",
	"warranty_end", "location", "lifecycle_state",
}

// legacyComposition is the composition member of GET /cis/{id}/parent in
// the former format (API-01): the link is the child CI, its flags follow
// from child_mode.
type legacyComposition struct {
	ID                            string `json:"id"`
	OrganizationID                string `json:"organization_id"`
	ParentAssetID                 string `json:"parent_asset_id"`
	ChildCIID                     string `json:"child_ci_id"`
	ConfigurationOnly             bool   `json:"configuration_only"`
	IndependentlySerialized       bool   `json:"independently_serialized"`
	IndependentlyAssignable       bool   `json:"independently_assignable"`
	IndependentlyLocatable        bool   `json:"independently_locatable"`
	IndependentlyLifecycleManaged bool   `json:"independently_lifecycle_managed"`
	CreatedAt                     string `json:"created_at"`
	UpdatedAt                     string `json:"updated_at"`
}

const noParentAsset = "CI has no parent asset"

// Parent handles GET /api/v1/cis/{id}/parent, deprecated in favour of GET
// /cis/{id} (E-25, API-01; WP-742): the asset of the parent CI, its own or
// the one it derives as a configuration child (WP-273), in the format of
// the former composition handler. A CI without a parent, or whose parent
// has no asset, is 404 as before.
func (h *Handler) Parent(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	child, err := h.svc.GetByID(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil || child.ParentCIID == "" {
		api.WriteError(w, http.StatusNotFound, "Not Found", noParentAsset)
		return
	}
	parent, err := h.svc.GetByID(r.Context(), t.OrganizationID, child.ParentCIID)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", noParentAsset)
		return
	}
	assetID := parent.AssetID
	if assetID == "" && parent.Asset != nil {
		assetID = parent.Asset.ID
	}
	if assetID == "" {
		api.WriteError(w, http.StatusNotFound, "Not Found", noParentAsset)
		return
	}
	configuration := child.ChildMode == ChildModeConfiguration
	link := legacyComposition{
		ID: child.ID, OrganizationID: child.OrganizationID, ParentAssetID: assetID, ChildCIID: child.ID,
		ConfigurationOnly: configuration, IndependentlySerialized: !configuration, IndependentlyAssignable: !configuration,
		IndependentlyLocatable: !configuration, IndependentlyLifecycleManaged: !configuration,
		CreatedAt: child.CreatedAt, UpdatedAt: child.UpdatedAt,
	}
	inherited := []string{}
	if configuration {
		inherited = legacyInheritedFields
	}
	resp := map[string]any{"composition": link, "inherited_fields": inherited}
	// The asset projection is best-effort as before: the link stays useful
	// when the asset cannot be read.
	if h.parentAssets != nil {
		if asset, aerr := h.parentAssets.GetByID(r.Context(), t.OrganizationID, assetID); aerr == nil && asset != nil {
			resp["parent_asset"] = asset
		}
	}
	api.WriteJSON(w, http.StatusOK, resp)
}

// AssetChildren handles GET /api/v1/assets/{id}/children (WP-742): the
// child CIs (parent_ci_id) of the CIs linked to the asset (ci.asset_id),
// with child_mode, cursor-paginated (API-02). An asset without CIs or one
// the principal cannot see has no children.
func (h *Handler) AssetChildren(w http.ResponseWriter, r *http.Request) {
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
	policy, ok := requestPolicy(w, r)
	if !ok {
		return
	}
	empty := api.ListResponse[any]{Data: []any{}, Limit: page.Limit, Offset: page.Offset}
	assetID := chi.URLParam(r, "id")
	if !api.IsUUID(assetID) {
		api.WriteJSON(w, http.StatusOK, empty)
		return
	}
	linked, _, err := h.svc.List(r.Context(), t.OrganizationID, &FilterParams{Filters: []api.Filter{
		{Field: "asset_id", Op: api.OpExact, Kind: api.KindUUID, Values: []string{assetID}}}},
		api.PaginationParams{Limit: api.MaxPageLimit})
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	if len(linked) == 0 {
		api.WriteJSON(w, http.StatusOK, empty)
		return
	}
	parents := make([]string, len(linked))
	for i := range linked {
		parents[i] = linked[i].ID
	}
	filter := FilterParams{Filters: []api.Filter{{Field: "parent_ci_id", Op: api.OpIn, Kind: api.KindUUID, Values: parents}}}
	items, counted, err := h.svc.List(r.Context(), t.OrganizationID, &filter, page)
	if err != nil {
		if errors.Is(err, api.ErrInvalidCursor) {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", api.ErrInvalidCursor.Error())
			return
		}
		api.WriteRepoError(w, err)
		return
	}
	hasMore := page.Offset+page.Limit < counted
	if page.Cursor != nil {
		hasMore = len(items) == page.Limit
	}
	data := make([]any, len(items))
	for i := range items {
		if data[i], err = projectCI(policy, &items[i]); err != nil {
			api.WriteRepoError(w, err)
			return
		}
	}
	total, capped := api.CapTotal(counted)
	api.WriteJSON(w, http.StatusOK, api.ListResponse[any]{Data: data, Total: total, TotalCapped: capped,
		Limit: page.Limit, Offset: page.Offset, HasMore: hasMore, NextCursor: NextCursor(items, &filter, page.Limit)})
}
