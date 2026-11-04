package override

import (
	"context"
	"errors"
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// EventDispatcher publishes override events to webhook subscribers.
type EventDispatcher interface {
	Dispatch(ctx context.Context, orgID, event string, payload any)
}

// Handler provides HTTP handlers for field provenance and manual overrides.
type Handler struct {
	repo       Repository
	dispatcher EventDispatcher
	cis        CILookup
}

// CIScope is the type and scope of a CI and the keys of its instance
// attributes: the field permissions of its overrides follow them (RBA-07,
// WP-265).
type CIScope struct {
	TypeID       string
	ClientID     string
	SiteID       string
	InstanceKeys []string
}

// CILookup reads the CIScope of a visible CI; ErrNotFound when the CI is
// unknown, not visible or deleted.
type CILookup interface {
	FieldScope(ctx context.Context, orgID, ciID string) (*CIScope, error)
}

// WithCIs sets the CI lookup of the field permissions. Without it a
// principal whose policy restricts CIs sees no field value and writes no
// override (fail-closed).
func (h *Handler) WithCIs(lookup CILookup) *Handler {
	h.cis = lookup
	return h
}

// fieldAccess is the field policy of a request on one CI.
type fieldAccess struct {
	policy *permission.FieldPolicy
	scope  *CIScope
}

// restricted reports whether the policy restricts CI fields.
func (a *fieldAccess) restricted() bool { return a.policy.Restricted(permission.KindCI) }

// options name the instance attributes of the CI: with them the field
// name maps to its key (permission.CIFieldPath): a typed column is
// core.<name>, an instance attribute of the CI _instance.<name>, anything
// else attributes.<name>; name and status are structural.
func (a *fieldAccess) options() []permission.ProjectOption {
	if a.scope == nil {
		return nil
	}
	return []permission.ProjectOption{permission.WithInstanceKeys(a.scope.InstanceKeys)}
}

func (a *fieldAccess) ref() (typeID string, ref permission.ObjectRef) {
	if a.scope == nil {
		return "", permission.ObjectRef{}
	}
	return a.scope.TypeID, permission.ObjectRef{ClientID: a.scope.ClientID, SiteID: a.scope.SiteID}
}

// readable reports whether the principal may read the field.
func (a *fieldAccess) readable(ciID, name string) bool {
	if !a.restricted() {
		return true
	}
	if a.scope == nil {
		_, structural, _ := permission.FieldKeyOf(permission.KindCI, permission.CIFieldPath(name))
		return structural
	}
	typeID, ref := a.ref()
	ref.ID = ciID
	return permission.Readable(a.policy, permission.KindCI, typeID, ref, permission.CIFieldPath(name), a.options()...)
}

// checkWrite refuses an override of a field the principal may not write
// (*api.FieldForbiddenError).
func (a *fieldAccess) checkWrite(ciID, name string) error {
	if !a.restricted() {
		return nil
	}
	path := permission.CIFieldPath(name)
	if a.scope == nil {
		if _, structural, _ := permission.FieldKeyOf(permission.KindCI, path); structural {
			return nil
		}
		return &api.FieldForbiddenError{Fields: []string{path}}
	}
	typeID, ref := a.ref()
	ref.ID = ciID
	return permission.CheckWrite(a.policy, permission.KindCI, typeID, ref, []string{path}, a.options()...)
}

// access resolves the field policy of the request and, when it restricts
// CIs, the scope of the CI. It answers the failure and reports false: 404
// for a CI that is not visible, 500 otherwise (fail-closed).
func (h *Handler) access(w http.ResponseWriter, r *http.Request, orgID, ciID string) (*fieldAccess, bool) {
	policy, err := permission.PolicyFrom(r.Context())
	if err != nil {
		api.WriteRepoError(w, err)
		return nil, false
	}
	a := &fieldAccess{policy: policy}
	if !a.restricted() || h.cis == nil {
		return a, true
	}
	a.scope, err = h.cis.FieldScope(r.Context(), orgID, ciID)
	if errors.Is(err, ErrNotFound) {
		api.WriteError(w, http.StatusNotFound, "Not Found", "ci not found")
		return nil, false
	}
	if err != nil {
		api.WriteRepoError(w, err)
		return nil, false
	}
	return a, true
}

// NewHandler creates a new override handler.
func NewHandler(repo Repository, dispatcher ...EventDispatcher) *Handler {
	h := &Handler{repo: repo}
	if len(dispatcher) > 0 {
		h.dispatcher = dispatcher[0]
	}
	return h
}

// RegisterRoutes registers override routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/cis/{id}/fields", h.ListForCI)
	r.Put("/api/v1/cis/{id}/fields/{name}/override", h.SetOverride)
	r.Delete("/api/v1/cis/{id}/fields/{name}/override", h.ClearOverride)
	r.Get("/api/v1/reconciliation/conflicts", h.Conflicts)
	r.Get("/api/v1/reconciliation/source-policy", h.GetPolicy)
	r.Put("/api/v1/reconciliation/source-policy", h.UpsertPolicy)
}

func (h *Handler) dispatch(r *http.Request, orgID, event string, payload any) {
	if h.dispatcher != nil {
		h.dispatcher.Dispatch(r.Context(), orgID, event, payload)
	}
}

// ListForCI handles GET /api/v1/cis/{id}/fields — the provenance view of
// every tracked field with discovered/override/effective values (spec §13).
// A CI that is unknown, not visible or soft-deleted is 404 (F-422).
func (h *Handler) ListForCI(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	ciID := chi.URLParam(r, "id")
	items, err := h.repo.ListForCI(r.Context(), t.OrganizationID, ciID)
	if errors.Is(err, ErrNotFound) {
		api.WriteError(w, http.StatusNotFound, "Not Found", "ci not found")
		return
	}
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	// Entries of fields the principal may not read are left out (RBA-07,
	// WP-265).
	access, ok := h.access(w, r, t.OrganizationID, ciID)
	if !ok {
		return
	}
	if access.restricted() {
		visible := make([]FieldValue, 0, len(items))
		for i := range items {
			if access.readable(ciID, items[i].FieldName) {
				visible = append(visible, items[i])
			}
		}
		items = visible
	}
	api.WriteJSON(w, http.StatusOK, api.CompleteList(items))
}

// refuseReservedName answers 400 with the field "name" when the path names
// a reserved attribute namespace (GLO-13, E-23) and reports whether it did.
func refuseReservedName(w http.ResponseWriter, r *http.Request) bool {
	if !IsReservedName(chi.URLParam(r, "name")) {
		return false
	}
	writeReservedName(w)
	return true
}

func writeReservedName(w http.ResponseWriter) {
	api.WriteFieldError(w, &api.FieldError{Field: "name",
		Message: "reserved attribute namespace (_overrides, _observed, _provenance, _instance), not a field (GLO-13)"})
}

// SetOverride handles PUT /api/v1/cis/{id}/fields/{name}/override. A
// reserved namespace as name is 400 (GLO-13).
func (h *Handler) SetOverride(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if refuseReservedName(w, r) {
		return
	}
	// An override writes the field: it needs the write level of the field
	// (RBA-07, WP-265), before the body is validated.
	if !h.allowWrite(w, r, t.OrganizationID) {
		return
	}
	var req SetOverrideRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Reason == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "reason is required for a manual override")
		return
	}
	protected := true
	if req.Protected != nil {
		protected = *req.Protected
	}
	author := ""
	if p, ok := identity.PrincipalFromContext(r.Context()); ok {
		author = p.Subject
	}
	fv, err := h.repo.SetOverride(r.Context(), t.OrganizationID, chi.URLParam(r, "id"),
		chi.URLParam(r, "name"), req.Value, author, req.Reason, protected)
	if err != nil {
		if err.Error() == "not found" {
			api.WriteError(w, http.StatusNotFound, "Not Found", "ci not found")
			return
		}
		if errors.Is(err, ErrNotOverridable) {
			api.WriteError(w, http.StatusUnprocessableEntity, "Unprocessable Entity", err.Error())
			return
		}
		if errors.Is(err, ErrReservedName) {
			writeReservedName(w)
			return
		}
		api.WriteRepoError(w, err)
		return
	}
	h.dispatch(r, t.OrganizationID, "override.changed", fv)
	writeOverride(w, fv)
}

// ClearOverride handles DELETE /api/v1/cis/{id}/fields/{name}/override. An
// unknown CI or field is 404, a reserved namespace as name 400 (GLO-13), a
// computed attribute 422 (ErrNotOverridable, WP-717), any other failure
// goes through api.WriteRepoError (F-232).
func (h *Handler) ClearOverride(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if refuseReservedName(w, r) {
		return
	}
	if !h.allowWrite(w, r, t.OrganizationID) {
		return
	}
	fv, err := h.repo.ClearOverride(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), chi.URLParam(r, "name"))
	if errors.Is(err, ErrNotFound) {
		api.WriteError(w, http.StatusNotFound, "Not Found", "field value not found")
		return
	}
	if errors.Is(err, ErrReservedName) {
		writeReservedName(w)
		return
	}
	// A computed attribute has no override to clear (WP-717): 422 as for
	// PUT.
	if errors.Is(err, ErrNotOverridable) {
		api.WriteError(w, http.StatusUnprocessableEntity, "Unprocessable Entity", "the field is a computed attribute and has no override")
		return
	}
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	h.dispatch(r, t.OrganizationID, "override.changed", fv)
	writeOverride(w, fv)
}

// allowWrite answers 403 with the field when the principal may not write
// the field of the path and reports whether the override may go on.
func (h *Handler) allowWrite(w http.ResponseWriter, r *http.Request, orgID string) bool {
	ciID := chi.URLParam(r, "id")
	access, ok := h.access(w, r, orgID, ciID)
	if !ok {
		return false
	}
	if err := access.checkWrite(ciID, chi.URLParam(r, "name")); err != nil {
		api.WriteFieldError(w, err)
		return false
	}
	return true
}

// writeOverride answers an override change with the field value and the
// CI's new version as ETag (API-07): the next PATCH of the CI sends it as
// If-Match.
func writeOverride(w http.ResponseWriter, fv *FieldValue) {
	if fv.CIVersion > 0 {
		w.Header().Set("ETag", api.ETag(fv.CIVersion))
	}
	api.WriteJSON(w, http.StatusOK, fv)
}

// Conflicts handles GET /api/v1/reconciliation/conflicts — field values where
// discovered and effective values diverge.
func (h *Handler) Conflicts(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.Conflicts(r.Context(), t.OrganizationID, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[FieldValue]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// GetPolicy handles GET /api/v1/reconciliation/source-policy
func (h *Handler) GetPolicy(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	policy, err := h.repo.Policy(r.Context(), t.OrganizationID)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, policy)
}

// UpsertPolicy handles PUT /api/v1/reconciliation/source-policy
func (h *Handler) UpsertPolicy(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var policy SourcePolicy
	if err := api.ReadJSON(r, &policy); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if len(policy.Priorities) == 0 {
		api.WriteFieldError(w, &api.FieldError{Field: "priorities", Message: "priorities must not be empty"})
		return
	}
	// PostgreSQL stores no NUL in text or jsonb; reject it before SQL (F-047).
	if err := api.RejectNUL("name", policy.Name); err != nil {
		api.WriteFieldError(w, err)
		return
	}
	if err := api.RejectNUL("priorities", policy.Priorities...); err != nil {
		api.WriteFieldError(w, err)
		return
	}
	policy.OrganizationID = t.OrganizationID
	policy.IsDefault = true
	if err := h.repo.UpsertPolicy(r.Context(), &policy); err != nil {
		api.WriteRepoError(w, err)
		return
	}
	h.dispatch(r, t.OrganizationID, "reconciliation.policy_changed", policy)
	api.WriteJSON(w, http.StatusOK, policy)
}
