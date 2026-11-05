package savedview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// PermissionChecker answers whether a user holds a permission key.
type PermissionChecker interface {
	HasPermission(ctx context.Context, orgID, userID, key string) (bool, error)
}

// EntitlementChecker reports whether an organization may use a feature
// (*entitlement.Service).
type EntitlementChecker interface {
	IsEnabled(ctx context.Context, orgID, featureKey string) bool
}

// Handler provides HTTP handlers for saved views.
type Handler struct {
	repo         Repository
	query        QueryEngine
	permissions  PermissionChecker
	entitlements EntitlementChecker
	inventory    bool
}

// NewHandler creates a new saved view handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// WithQueryEngine attaches the filter-DSL query engine (spec §17). Without
// one, the query endpoint returns 501.
func (h *Handler) WithQueryEngine(engine QueryEngine) *Handler {
	h.query = engine
	return h
}

// WithPermissions attaches the permission check of the query endpoint: the
// caller must hold the read permission of the queried entity kind (SRC-03).
// Without a checker the endpoint answers 403 (fail closed).
func (h *Handler) WithPermissions(permissions PermissionChecker) *Handler {
	h.permissions = permissions
	return h
}

// WithEntitlements attaches the entitlement check of asset references
// (WP-309, E-44): an asset query and the asset fields of a CI query need
// assets_basic. Without a checker they are refused (fail closed).
func (h *Handler) WithEntitlements(entitlements EntitlementChecker) *Handler {
	h.entitlements = entitlements
	return h
}

// WithInventory sets whether the module inventory is enabled (LateModules):
// asset references need it (WP-309). Off by default (fail closed).
func (h *Handler) WithInventory(enabled bool) *Handler {
	h.inventory = enabled
	return h
}

// RegisterRoutes registers saved view routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/saved-views", h.List)
	r.Post("/api/v1/saved-views", h.Create)
	r.Get("/api/v1/saved-views/presets", h.PresetList)
	r.Get("/api/v1/saved-views/{id}", h.Get)
	r.Patch("/api/v1/saved-views/{id}", h.Update)
	r.Delete("/api/v1/saved-views/{id}", h.Delete)
	r.Post("/api/v1/search/query", h.Query)
}

func ownerID(r *http.Request) string {
	if p, ok := identity.PrincipalFromContext(r.Context()); ok {
		return p.Subject
	}
	return ""
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.List(r.Context(), t.OrganizationID, ownerID(r), page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[View]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// PresetList handles GET /api/v1/saved-views/presets
func (h *Handler) PresetList(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	api.WriteJSON(w, http.StatusOK, api.CompleteList(Presets()))
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
	item, err := h.repo.GetByID(r.Context(), t.OrganizationID, id, ownerID(r))
	if err != nil {
		writeViewError(w, r, err)
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
	var req UpsertRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Name == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name is required")
		return
	}
	if err := ValidateView(req.EntityKind, &req); err != nil {
		api.WriteFieldError(w, err)
		return
	}
	if !h.checkViewFilter(w, r, t, req.EntityKind, req.FilterSpec) {
		return
	}
	view := &View{
		OrganizationID:  t.OrganizationID,
		OwnerID:         ownerID(r),
		Name:            req.Name,
		EntityKind:      req.EntityKind,
		FilterSpec:      req.FilterSpec,
		Sort:            req.Sort,
		Columns:         req.Columns,
		Shared:          req.Shared != nil && *req.Shared,
		SharedWithRoles: req.SharedWithRoles,
	}
	if err := h.repo.Create(r.Context(), view); err != nil {
		writeViewError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, view)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	var req UpsertRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	current, err := h.repo.GetByID(r.Context(), t.OrganizationID, id, ownerID(r))
	if err != nil {
		writeViewError(w, r, err)
		return
	}
	// Only the owner changes a view (E-86, option 1): a view shared with the
	// caller is read-only and answers 404 like a view the caller cannot
	// see, before any validation tells them more about it.
	if user := ownerID(r); user == "" || current.OwnerID != user {
		writeViewError(w, r, ErrNotFound)
		return
	}
	kind := req.EntityKind
	if kind == "" {
		kind = current.EntityKind
	}
	if verr := ValidateView(kind, &req); verr != nil {
		api.WriteFieldError(w, verr)
		return
	}
	if !h.checkViewFilter(w, r, t, kind, req.FilterSpec) {
		return
	}
	item, err := h.repo.Update(r.Context(), t.OrganizationID, id, ownerID(r), &req)
	if err != nil {
		// A name the owner uses for another view is 409, not 404 (F-047).
		writeViewError(w, r, err)
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
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	if err := h.repo.Delete(r.Context(), t.OrganizationID, id, ownerID(r)); err != nil {
		writeViewError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeViewError answers a failed saved-view request: a view that is
// unknown, of another organization or not the caller's to read or change is
// 404 (VIE-01), a role id unknown to the organization 422 naming the
// field, a name the owner uses for another view 409 (in memory
// ErrConflict, in PostgreSQL 23505). Database errors go through
// api.WriteRepoError like the engine errors of Query: no constraint name or
// SQLSTATE reaches the client (API-03, F-047), the log keeps the text.
func writeViewError(w http.ResponseWriter, r *http.Request, err error) {
	var fe *api.FieldError
	switch {
	case errors.As(err, &fe):
		// A role id that is not a role of the organization (F-337).
		api.WriteFieldError(w, err)
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrNotFound.Error())
	case errors.Is(err, ErrConflict):
		api.WriteError(w, http.StatusConflict, "Conflict", ErrConflict.Error())
	default:
		slog.WarnContext(r.Context(), "saved view request failed", "error", err)
		api.WriteRepoError(w, err)
	}
}

// Query handles POST /api/v1/search/query — executes a structured filter spec
// (the saved-view DSL, spec §17) against CIs or assets. The body is the
// filter spec itself; entity_kind selects the entity ("ci" default, "asset").
func (h *Handler) Query(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if h.query == nil {
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "filter query engine is not configured")
		return
	}
	var spec FilterSpec
	if err := api.ReadJSON(r, &spec); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	key, ok := readPermissions[spec.entityKind()]
	if !ok {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", fmt.Sprintf("unsupported entity_kind %q", spec.entityKind()))
		return
	}
	if !h.mayRead(r.Context(), t, key) {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "missing permission "+key)
		return
	}
	// Asset references need asset:read, the module inventory and
	// assets_basic (WP-309).
	if !h.assetAccess(w, r, t, &spec) {
		return
	}
	page := api.ParsePagination(r)
	// Values are checked before any SQL runs: 422 naming the field.
	if err := spec.Validate(spec.entityKind()); err != nil {
		api.WriteFieldError(w, err)
		return
	}
	// Relationship filters need relationship:read (WP-789); like saving a
	// view, the static check of the spec answers first.
	if !h.relationshipAccess(w, r, t, &spec) {
		return
	}
	// Field permissions (RBA-07, WP-729): every field the spec reads must be
	// readable for the queried types before any SQL runs, otherwise 403
	// with the fields and without a total; the rows are projected.
	policy, err := permission.PolicyFrom(r.Context())
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	if ferr := h.checkFields(r.Context(), t.OrganizationID, policy, &spec); ferr != nil {
		if !api.WriteFieldError(w, ferr) {
			api.WriteRepoError(w, ferr)
		}
		return
	}
	items, total, err := h.query.Query(r.Context(), t.OrganizationID, &spec, page)
	if err != nil {
		// An attribute or tag the catalog refuses is a field error: 422
		// (unknown) or 400 (not searchable) naming the field (WP-309).
		if api.WriteFieldError(w, err) {
			return
		}
		// A validated spec compiles to SQL whose casts cannot fail; any
		// error left is internal and its text goes to the log only (API-03).
		slog.ErrorContext(r.Context(), "structured search failed", "error", err)
		api.WriteRepoError(w, err)
		return
	}
	ProjectResults(policy, spec.entityKind(), items)
	api.WriteJSON(w, http.StatusOK, api.ListResponse[QueryResult]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// referencesAssets reports whether the spec reads assets: an asset query or
// a field of the linked asset of a CI (asset.<field>, asset.attributes.<key>).
func referencesAssets(spec *FilterSpec) bool {
	if spec.entityKind() == "asset" {
		return true
	}
	found := false
	if spec.Where != nil {
		spec.Where.walk(func(e *Expr) {
			found = found || strings.HasPrefix(e.Field, "asset.")
		})
	}
	return found
}

// errInventoryDisabled refuses asset references while the module inventory
// is off; like entitlement.FeatureNotEntitledError it is a 403 with the
// problem type feature-not-entitled.
type errInventoryDisabled struct{}

func (errInventoryDisabled) Error() string       { return "module inventory is not enabled" }
func (errInventoryDisabled) LimitExceeded() bool { return true }
func (errInventoryDisabled) ProblemType() string { return api.ProblemFeatureNotEntitled }

// assetAccess answers whether the spec may read assets and otherwise
// writes the refusal (WP-309): an asset query and the asset fields of a CI
// query need asset:read (403 missing permission asset:read, also for a CI
// query), the module inventory and the entitlement assets_basic of the
// organization (403 feature-not-entitled; without an entitlement checker
// refused). The route itself stays ungated (search:read).
func (h *Handler) assetAccess(w http.ResponseWriter, r *http.Request, t tenant.TenantInfo, spec *FilterSpec) bool {
	if !referencesAssets(spec) {
		return true
	}
	if key := readPermissions["asset"]; !h.mayRead(r.Context(), t, key) {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "missing permission "+key)
		return false
	}
	if !h.inventory {
		api.WriteEntitlementError(w, errInventoryDisabled{})
		return false
	}
	if h.entitlements == nil || !h.entitlements.IsEnabled(r.Context(), t.OrganizationID, entitlement.FeatureAssetsBasic) {
		api.WriteEntitlementError(w, &entitlement.FeatureNotEntitledError{FeatureKey: entitlement.FeatureAssetsBasic})
		return false
	}
	return true
}

// usesRelationships reports whether the spec filters by relationships: a
// related node of the AST or one of the flat fields has_relationship,
// lacks_relationship, upstream_of and downstream_of (WP-789).
func usesRelationships(spec *FilterSpec) bool {
	found := spec.HasRelationship != "" || spec.LacksRelationship != "" || spec.UpstreamOf != "" || spec.DownstreamOf != ""
	if spec.Where != nil {
		spec.Where.walk(func(e *Expr) {
			found = found || e.Related != nil
		})
	}
	return found
}

// relationshipAccess answers whether the spec may filter by relationships
// and otherwise writes the refusal: a relationship filter (usesRelationships)
// needs relationship:read, the permission of the relationship routes (403
// missing permission relationship:read, WP-789).
func (h *Handler) relationshipAccess(w http.ResponseWriter, r *http.Request, t tenant.TenantInfo, spec *FilterSpec) bool {
	if !usesRelationships(spec) {
		return true
	}
	if key := string(identity.PermRelationshipRead); !h.mayRead(r.Context(), t, key) {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "missing permission "+key)
		return false
	}
	return true
}

// checkViewFilter checks the filter of a saved view on POST and PATCH by
// the rules of the query (WP-309): asset references (assetAccess),
// relationship filters (relationshipAccess, WP-789), field
// permissions (checkFields) and, through the query engine, the attribute
// definitions (searchable) and the tag catalog. A field error names
// filter_spec.<field>. Without an engine that checks specs (memory
// profile) only the static check of ValidateView applies. It answers
// whether the request may go on and otherwise writes the refusal.
func (h *Handler) checkViewFilter(w http.ResponseWriter, r *http.Request, t tenant.TenantInfo, entityKind string, filter map[string]any) bool {
	checker, ok := h.query.(SpecChecker)
	if !ok || filter == nil {
		return true
	}
	raw, err := json.Marshal(filter)
	if err != nil {
		api.WriteFieldError(w, &api.FieldError{Field: "filter_spec", Message: "not a filter spec", Status: http.StatusUnprocessableEntity})
		return false
	}
	var spec FilterSpec
	if err = json.Unmarshal(raw, &spec); err != nil {
		api.WriteFieldError(w, &api.FieldError{Field: "filter_spec", Message: "not a filter spec", Status: http.StatusUnprocessableEntity})
		return false
	}
	if entityKind == "" {
		entityKind = "ci"
	}
	spec.EntityKind = entityKind
	if !h.assetAccess(w, r, t, &spec) || !h.relationshipAccess(w, r, t, &spec) {
		return false
	}
	policy, err := permission.PolicyFrom(r.Context())
	if err != nil {
		api.WriteRepoError(w, err)
		return false
	}
	if ferr := h.checkFields(r.Context(), t.OrganizationID, policy, &spec); ferr != nil {
		if !api.WriteFieldError(w, ferr) {
			api.WriteRepoError(w, ferr)
		}
		return false
	}
	if cerr := checker.CheckSpec(r.Context(), t.OrganizationID, &spec); cerr != nil {
		var fe *api.FieldError
		if errors.As(cerr, &fe) {
			api.WriteFieldError(w, &api.FieldError{Field: "filter_spec." + fe.Field, Message: fe.Message, Status: fe.Status})
			return false
		}
		slog.WarnContext(r.Context(), "saved view filter check failed", "error", cerr)
		api.WriteRepoError(w, cerr)
		return false
	}
	return true
}

// TypeResolver resolves ci_type of a filter spec (a type key or name) to
// type ids (*PGQueryEngine).
type TypeResolver interface {
	TypeIDs(ctx context.Context, orgID, keyOrName string) ([]string, error)
}

// checkFields refuses a spec that reads a field the principal may not read
// for one of the queried types in every scope of its role assignments
// (RBA-07, WP-729; the rule of the list filters of WP-265, which sort keys
// and columns share: a sort is an order oracle, a column returns values).
// The fields are those of FilterSpec.ReferencedFields; structural fields
// pass. The queried types are the types of ci_type for a CI query (without
// it, or when the engine cannot resolve it, every type), for asset.<field>
// and for an asset query every asset type (ci_type of an asset query also
// matches assets through their CI), for the fields of related CIs every CI
// type (WP-789). The error is an *api.FieldForbiddenError
// naming the fields as the request names them.
func (h *Handler) checkFields(ctx context.Context, orgID string, policy *permission.FieldPolicy, spec *FilterSpec) error {
	entityKind := spec.entityKind()
	refs := spec.ReferencedFields(entityKind)
	if len(refs) == 0 || (!policy.Restricted(permission.KindCI) && !policy.Restricted(permission.KindAsset)) {
		return nil
	}
	var ciTypes []string
	if resolver, ok := h.query.(TypeResolver); ok && entityKind == "ci" && spec.CIType != "" {
		ids, err := resolver.TypeIDs(ctx, orgID, spec.CIType)
		if err != nil {
			return err
		}
		ciTypes = ids
	}
	// The names by group and field key; the fields of related CIs (WP-789)
	// are checked for every type.
	type group struct {
		entity  string
		related bool
	}
	names := map[group]map[string][]string{}
	var refused []string
	for _, ref := range refs {
		key, checked, known := fieldKey(ref.EntityKind, ref.Path)
		switch {
		case !known:
			if policy.Restricted(objectKinds[ref.EntityKind]) {
				refused = append(refused, ref.Name)
			}
		case checked:
			g := group{ref.EntityKind, ref.Related}
			if names[g] == nil {
				names[g] = map[string][]string{}
			}
			names[g][key] = append(names[g][key], ref.Name)
		}
	}
	for g, byKey := range names {
		keys := make([]string, 0, len(byKey))
		for key := range byKey {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var types []string
		if g.entity == "ci" && !g.related {
			types = ciTypes
		}
		for _, key := range permission.UnreadableKeys(policy, objectKinds[g.entity], types, keys) {
			refused = append(refused, byKey[key]...)
		}
	}
	if len(refused) == 0 {
		return nil
	}
	sort.Strings(refused)
	return &api.FieldForbiddenError{Fields: slices.Compact(refused)}
}

// readPermissions maps each queryable entity kind to its read permission.
var readPermissions = map[string]string{
	"ci":    "ci:read",
	"asset": "asset:read",
}

// mayRead reports whether the caller holds key; errors and a missing checker
// or user deny. The route needs only search:read (F-055), so the request's
// own permissions must hold key as well when the authentication set them: a
// token narrowed below its holder's roles reads no other entity kind.
func (h *Handler) mayRead(ctx context.Context, t tenant.TenantInfo, key string) bool {
	if h.permissions == nil || t.UserID == "" {
		return false
	}
	if perms, ok := identity.PermissionsFromContext(ctx); ok && !identity.HasPermission(perms, identity.Permission(key)) {
		return false
	}
	ok, err := h.permissions.HasPermission(ctx, t.OrganizationID, t.UserID, key)
	return err == nil && ok
}
