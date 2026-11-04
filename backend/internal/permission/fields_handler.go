package permission

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// FieldGrantHandler serves the administration of the field permissions
// under /api/v1/field-permissions (RBA-07, API-06, E-38, WP-726): the field
// grants, the object grants (/object-grants) and the sensitive fields
// (/sensitive-fields). The router requires field_permission:manage for
// every route, reads included; the handler requires it for the whole
// organization as well (fail-closed): the field permissions apply to every
// client, site and team, so a holder restricted to some of them must not
// read or change them. After every successful write the cached snapshot of
// the organization is dropped once the transaction has committed.
type FieldGrantHandler struct {
	repo     FieldGrantRepository
	resolver *FieldResolver
}

// NewFieldGrantHandler creates the handler; resolver (nil: none) is the
// resolver whose cache a write invalidates.
func NewFieldGrantHandler(repo FieldGrantRepository, resolver *FieldResolver) *FieldGrantHandler {
	return &FieldGrantHandler{repo: repo, resolver: resolver}
}

// The paths of the field permission administration.
const (
	fieldPermissionsPath = "/api/v1/field-permissions"
	objectGrantsPath     = fieldPermissionsPath + "/object-grants"
	sensitiveFieldsPath  = fieldPermissionsPath + "/sensitive-fields"
)

// RegisterRoutes registers the ten operations of the field permissions.
func (h *FieldGrantHandler) RegisterRoutes(r chi.Router) {
	r.Get(fieldPermissionsPath, h.ListFieldGrants)
	r.Post(fieldPermissionsPath, h.CreateFieldGrant)
	r.Patch(fieldPermissionsPath+"/{id}", h.UpdateFieldGrant)
	r.Delete(fieldPermissionsPath+"/{id}", h.DeleteFieldGrant)
	r.Get(objectGrantsPath, h.ListObjectGrants)
	r.Post(objectGrantsPath, h.CreateObjectGrant)
	r.Delete(objectGrantsPath+"/{id}", h.DeleteObjectGrant)
	r.Get(sensitiveFieldsPath, h.ListSensitiveFields)
	r.Post(sensitiveFieldsPath, h.CreateSensitiveField)
	r.Delete(sensitiveFieldsPath+"/{id}", h.DeleteSensitiveField)
}

// CreateFieldGrantRequest is the body of POST /api/v1/field-permissions:
// exactly one of role_id and custom_role_id, at most one of field_key,
// relationship_type_id and action.
type CreateFieldGrantRequest struct {
	RoleID             *string    `json:"role_id"`
	CustomRoleID       *string    `json:"custom_role_id"`
	ObjectKind         ObjectKind `json:"object_kind"`
	CITypeID           *string    `json:"ci_type_id"`
	FieldKey           *string    `json:"field_key"`
	RelationshipTypeID *string    `json:"relationship_type_id"`
	Action             *string    `json:"action"`
	Level              Level      `json:"level"`
}

// UpdateFieldGrantRequest is the body of PATCH
// /api/v1/field-permissions/{id}: only the level changes.
type UpdateFieldGrantRequest struct {
	Level Level `json:"level"`
}

// CreateObjectGrantRequest is the body of POST
// /api/v1/field-permissions/object-grants: exactly one grantee.
type CreateObjectGrantRequest struct {
	GranteeRoleID       *string    `json:"grantee_role_id"`
	GranteeCustomRoleID *string    `json:"grantee_custom_role_id"`
	GranteeTeamID       *string    `json:"grantee_team_id"`
	ObjectKind          ObjectKind `json:"object_kind"`
	ObjectID            string     `json:"object_id"`
	Level               Level      `json:"level"`
}

// CreateSensitiveFieldRequest is the body of POST
// /api/v1/field-permissions/sensitive-fields.
type CreateSensitiveFieldRequest struct {
	ObjectKind ObjectKind `json:"object_kind"`
	CITypeID   *string    `json:"ci_type_id"`
	FieldKey   string     `json:"field_key"`
}

// FieldPermissionPage is a cursor page of a field permission list: the
// configuration of the organization, paged by (created_at, id) without a
// count (API-02).
type FieldPermissionPage[T any] struct {
	Data       []T    `json:"data"`
	Limit      int    `json:"limit"`
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor,omitempty"`
}

func newFieldPermissionPage[T any](items []T, next string, page api.PaginationParams) FieldPermissionPage[T] {
	if items == nil {
		items = []T{}
	}
	return FieldPermissionPage[T]{Data: items, Limit: pageLimit(page), HasMore: next != "", NextCursor: next}
}

// Fixed rule texts of the answers.
const (
	msgOrgWide          = "field permissions apply to the whole organization; field_permission:manage must be held organization-wide"
	msgUnknownFieldKey  = "the field key is unknown, not grantable or does not fit the object kind"
	msgUnknownReference = "the ci type, relationship type, action or object is unknown or not visible"
	msgUnknownKind      = "object_kind must be one of ci, asset, contract, ticket, document, relationship"
	msgGrantNotFound    = "field permission not found"
)

// orgWideHolder reports whether the principal holds perm for the whole
// organization: in every dimension of the scope of its grant, for an API
// key without resolved scope without a client scope (identity's
// principalScopeFor).
func orgWideHolder(p *identity.Principal, perm identity.Permission) bool {
	if !p.Has(perm) {
		return false
	}
	if p.Scope == nil {
		_, restricted := p.PermissionScopes[perm]
		return !restricted && strings.TrimSpace(p.ClientScope) == ""
	}
	scope, ok := p.ScopeFor(perm)
	return ok && scope.OrgWide()
}

// authorize returns the organization of the request when its principal
// manages the field permissions; otherwise it answers 401 or 403.
func (h *FieldGrantHandler) authorize(w http.ResponseWriter, r *http.Request) (tenant.TenantInfo, bool) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return tenant.TenantInfo{}, false
	}
	p, ok := identity.PrincipalFromContext(r.Context())
	if !ok || p.OrganizationID != t.OrganizationID || !orgWideHolder(&p, identity.PermFieldPermissionManage) {
		api.WriteError(w, http.StatusForbidden, "Forbidden", msgOrgWide)
		return tenant.TenantInfo{}, false
	}
	return t, true
}

// creator is the acting user as created_by, nil when it is no user id.
func creator(t *tenant.TenantInfo) *string {
	if !isUUID(t.UserID) {
		return nil
	}
	id := t.UserID
	return &id
}

// invalidate drops the cached snapshot of the organization once the
// request's transaction has committed (WP-332); with Redis every instance
// reads the change.
func (h *FieldGrantHandler) invalidate(ctx context.Context, orgID string) {
	if h.resolver == nil {
		return
	}
	database.AfterCommit(ctx, func(ctx context.Context) { h.resolver.Invalidate(ctx, orgID) })
}

// writeError answers a repository error: an unknown field key and an
// unknown or invisible reference are 422 with a fixed text, a foreign
// cursor 400, the rest api.WriteRepoError (field errors with their rule
// text, 409 for a duplicate, 422 for an unknown or foreign role, custom
// role or team, 404 for a missing row).
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrUnknownFieldKey):
		api.WriteFieldError(w, &api.FieldError{Field: "field_key", Message: msgUnknownFieldKey, Status: http.StatusUnprocessableEntity})
	case errors.Is(err, ErrUnknownReference):
		api.WriteError(w, http.StatusUnprocessableEntity, "Unprocessable Entity", msgUnknownReference)
	case errors.Is(err, api.ErrInvalidCursor):
		api.WriteError(w, http.StatusBadRequest, "Bad Request", api.ErrInvalidCursor.Error())
	case errors.Is(err, api.ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", msgGrantNotFound)
	default:
		var fe *api.FieldError
		if !errors.As(err, &fe) {
			slog.WarnContext(r.Context(), "field permission request failed", "error", err)
		}
		api.WriteRepoError(w, err)
	}
}

// listPage reads the pagination and the object_kind filter of a list; an
// unknown kind or a malformed cursor is 400.
func listPage(w http.ResponseWriter, r *http.Request) (api.PaginationParams, ObjectKind, bool) {
	page := api.ParsePagination(r)
	if page.CursorError != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", api.ErrInvalidCursor.Error())
		return page, "", false
	}
	kind := ObjectKind(r.URL.Query().Get("object_kind"))
	if kind != "" && !kind.Valid() {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", msgUnknownKind)
		return page, "", false
	}
	return page, kind, true
}

// ListFieldGrants handles GET /api/v1/field-permissions with the filters
// role_id, custom_role_id, object_kind and ci_type_id.
func (h *FieldGrantHandler) ListFieldGrants(w http.ResponseWriter, r *http.Request) {
	t, ok := h.authorize(w, r)
	if !ok {
		return
	}
	page, kind, ok := listPage(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	items, next, err := h.repo.ListFieldGrants(r.Context(), t.OrganizationID, &FieldGrantFilter{
		RoleID: q.Get("role_id"), CustomRoleID: q.Get("custom_role_id"), ObjectKind: kind, CITypeID: q.Get("ci_type_id"),
	}, page)
	if err != nil {
		writeError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, newFieldPermissionPage(items, next, page))
}

// CreateFieldGrant handles POST /api/v1/field-permissions (201).
func (h *FieldGrantHandler) CreateFieldGrant(w http.ResponseWriter, r *http.Request) {
	t, ok := h.authorize(w, r)
	if !ok {
		return
	}
	var req CreateFieldGrantRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	item, err := h.repo.CreateFieldGrant(r.Context(), t.OrganizationID, &FieldGrant{
		RoleID: req.RoleID, CustomRoleID: req.CustomRoleID, ObjectKind: req.ObjectKind, CITypeID: req.CITypeID,
		FieldKey: req.FieldKey, RelationshipTypeID: req.RelationshipTypeID, Action: req.Action, Level: req.Level,
		CreatedBy: creator(&t),
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	h.invalidate(r.Context(), t.OrganizationID)
	api.WriteJSON(w, http.StatusCreated, item)
}

// UpdateFieldGrant handles PATCH /api/v1/field-permissions/{id}: the level.
func (h *FieldGrantHandler) UpdateFieldGrant(w http.ResponseWriter, r *http.Request) {
	t, ok := h.authorize(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", msgGrantNotFound)
	if !ok {
		return
	}
	var req UpdateFieldGrantRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	item, err := h.repo.UpdateFieldGrantLevel(r.Context(), t.OrganizationID, id, req.Level)
	if err != nil {
		writeError(w, r, err)
		return
	}
	h.invalidate(r.Context(), t.OrganizationID)
	api.WriteJSON(w, http.StatusOK, item)
}

// deleteRow answers DELETE …/{id} with 204 after del removed the row.
func (h *FieldGrantHandler) deleteRow(w http.ResponseWriter, r *http.Request, del func(ctx context.Context, orgID, id string) error) {
	t, ok := h.authorize(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", msgGrantNotFound)
	if !ok {
		return
	}
	if err := del(r.Context(), t.OrganizationID, id); err != nil {
		writeError(w, r, err)
		return
	}
	h.invalidate(r.Context(), t.OrganizationID)
	w.WriteHeader(http.StatusNoContent)
}

// DeleteFieldGrant handles DELETE /api/v1/field-permissions/{id} (204).
func (h *FieldGrantHandler) DeleteFieldGrant(w http.ResponseWriter, r *http.Request) {
	h.deleteRow(w, r, h.repo.DeleteFieldGrant)
}

// ListObjectGrants handles GET /api/v1/field-permissions/object-grants with
// the filters object_kind, object_id, grantee_role_id,
// grantee_custom_role_id and grantee_team_id.
func (h *FieldGrantHandler) ListObjectGrants(w http.ResponseWriter, r *http.Request) {
	t, ok := h.authorize(w, r)
	if !ok {
		return
	}
	page, kind, ok := listPage(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	items, next, err := h.repo.ListObjectGrants(r.Context(), t.OrganizationID, &ObjectGrantFilter{
		ObjectKind: kind, ObjectID: q.Get("object_id"), GranteeRoleID: q.Get("grantee_role_id"),
		GranteeCustomRoleID: q.Get("grantee_custom_role_id"), GranteeTeamID: q.Get("grantee_team_id"),
	}, page)
	if err != nil {
		writeError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, newFieldPermissionPage(items, next, page))
}

// CreateObjectGrant handles POST /api/v1/field-permissions/object-grants
// (201); the object must be visible to the caller.
func (h *FieldGrantHandler) CreateObjectGrant(w http.ResponseWriter, r *http.Request) {
	t, ok := h.authorize(w, r)
	if !ok {
		return
	}
	var req CreateObjectGrantRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	item, err := h.repo.CreateObjectGrant(r.Context(), t.OrganizationID, &ObjectGrant{
		GranteeRoleID: req.GranteeRoleID, GranteeCustomRoleID: req.GranteeCustomRoleID, GranteeTeamID: req.GranteeTeamID,
		ObjectKind: req.ObjectKind, ObjectID: req.ObjectID, Level: req.Level, CreatedBy: creator(&t),
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	h.invalidate(r.Context(), t.OrganizationID)
	api.WriteJSON(w, http.StatusCreated, item)
}

// DeleteObjectGrant handles DELETE
// /api/v1/field-permissions/object-grants/{id} (204).
func (h *FieldGrantHandler) DeleteObjectGrant(w http.ResponseWriter, r *http.Request) {
	h.deleteRow(w, r, h.repo.DeleteObjectGrant)
}

// ListSensitiveFields handles GET
// /api/v1/field-permissions/sensitive-fields with the filters object_kind
// and ci_type_id.
func (h *FieldGrantHandler) ListSensitiveFields(w http.ResponseWriter, r *http.Request) {
	t, ok := h.authorize(w, r)
	if !ok {
		return
	}
	page, kind, ok := listPage(w, r)
	if !ok {
		return
	}
	items, next, err := h.repo.ListSensitiveFields(r.Context(), t.OrganizationID, &SensitiveFieldFilter{
		ObjectKind: kind, CITypeID: r.URL.Query().Get("ci_type_id"),
	}, page)
	if err != nil {
		writeError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, newFieldPermissionPage(items, next, page))
}

// CreateSensitiveField handles POST
// /api/v1/field-permissions/sensitive-fields (201).
func (h *FieldGrantHandler) CreateSensitiveField(w http.ResponseWriter, r *http.Request) {
	t, ok := h.authorize(w, r)
	if !ok {
		return
	}
	var req CreateSensitiveFieldRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	item, err := h.repo.CreateSensitiveField(r.Context(), t.OrganizationID, &SensitiveField{
		ObjectKind: req.ObjectKind, CITypeID: req.CITypeID, FieldKey: req.FieldKey, CreatedBy: creator(&t),
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	h.invalidate(r.Context(), t.OrganizationID)
	api.WriteJSON(w, http.StatusCreated, item)
}

// DeleteSensitiveField handles DELETE
// /api/v1/field-permissions/sensitive-fields/{id} (204).
func (h *FieldGrantHandler) DeleteSensitiveField(w http.ResponseWriter, r *http.Request) {
	h.deleteRow(w, r, h.repo.DeleteSensitiveField)
}
