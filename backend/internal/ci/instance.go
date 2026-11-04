package ci

import (
	"context"
	"errors"
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Instance attribute values (MET-14, WP-256). The definitions of the
// attributes of a single CI stay under /cis/{id}/field-definitions
// (WP-045); their values have an API of their own, written with
// ci_instance_attribute:manage through Service.Update like a PATCH.

// InstanceFieldLister lists the instance attribute definitions of a CI.
// The field resolver of the service (citype.PGRepository,
// citype.MemoryRepository) implements it next to FieldResolver.
type InstanceFieldLister interface {
	ListInstanceFields(ctx context.Context, orgID, ciID string) ([]citype.InstanceField, error)
}

// InstanceAttribute is the value of one instance attribute of a CI with its
// definition; Value is null while the CI holds none.
type InstanceAttribute struct {
	Key        string               `json:"key"`
	Definition citype.InstanceField `json:"definition"`
	Value      any                  `json:"value"`
}

// SetInstanceAttributeRequest is the body of
// PUT /api/v1/cis/{id}/instance-attributes/{key}.
type SetInstanceAttributeRequest struct {
	Value any `json:"value"`
}

// instanceForbiddenDetail is the detail of the 403 of
// ErrInstanceAttributeForbidden.
const instanceForbiddenDetail = "changing an instance attribute requires ci_instance_attribute:manage"

// ErrInstanceAttributeForbidden refuses a manual write that changes an
// instance attribute without ci_instance_attribute:manage (MET-14); the
// handlers answer 403.
var ErrInstanceAttributeForbidden = errors.New(instanceForbiddenDetail)

// Details of a refused instance attribute write (422).
const (
	instanceKeyUndefined = "the CI has no instance attribute with this key"
	instanceValueMissing = "value is required; DELETE removes the value"
)

// instanceFields lists the instance attribute definitions of a CI; without
// a resolver that lists them the CI has none.
func (s *Service) instanceFields(ctx context.Context, orgID, ciID string) ([]citype.InstanceField, error) {
	lister, ok := s.fields.(InstanceFieldLister)
	if !ok {
		return nil, nil
	}
	return lister.ListInstanceFields(ctx, orgID, ciID)
}

// instanceField returns the definition of the instance attribute key of a
// CI, or a *ValidationError when the CI has none.
func (s *Service) instanceField(ctx context.Context, orgID, ciID, key string) (*citype.InstanceField, error) {
	defs, err := s.instanceFields(ctx, orgID, ciID)
	if err != nil {
		return nil, err
	}
	for i := range defs {
		if defs[i].Name == key {
			return &defs[i], nil
		}
	}
	return nil, &ValidationError{Violations: []Violation{{Field: key, Detail: instanceKeyUndefined}}}
}

// ListInstanceAttributes lists every instance attribute definition of the
// CI with its value (MET-14). The CI must be visible (ErrNotFound).
func (s *Service) ListInstanceAttributes(ctx context.Context, orgID, ciID string) ([]InstanceAttribute, error) {
	item, err := s.repo.GetByID(ctx, orgID, ciID)
	if err != nil {
		return nil, err
	}
	defs, err := s.instanceFields(ctx, orgID, ciID)
	if err != nil {
		return nil, err
	}
	// The entries of instance attributes the principal may not read are
	// left out (_instance.<key>, RBA-07, WP-265).
	policy, err := permission.PolicyFrom(ctx)
	if err != nil {
		return nil, err
	}
	keys := make([]string, len(defs))
	for i := range defs {
		keys[i] = defs[i].Name
	}
	ref, instance := ciRef(item), permission.WithInstanceKeys(keys)
	out := make([]InstanceAttribute, 0, len(defs))
	for i := range defs {
		if !permission.Readable(policy, permission.KindCI, item.CITypeID, ref, "attributes."+defs[i].Name, instance) {
			continue
		}
		out = append(out, InstanceAttribute{Key: defs[i].Name, Definition: defs[i], Value: item.Attributes[defs[i].Name]})
	}
	return out, nil
}

// SetInstanceAttribute replaces the value of the instance attribute key of
// a CI: a manual write through Update (If-Match, version + 1, history,
// audit ci.updated, event ci.changed); the value is validated against the
// definition and stored under _instance. A key without a definition or a
// null value is a *ValidationError.
func (s *Service) SetInstanceAttribute(ctx context.Context, orgID, ciID, key string, value any, ifMatch *int64, author string) (*InstanceAttribute, *Item, error) {
	ctx = permission.WithAction(ctx, identity.PermCIInstanceAttributeManage)
	before, err := s.repo.GetByID(ctx, orgID, ciID)
	if err != nil {
		return nil, nil, err
	}
	if actionErr := checkInstanceAction(ctx, before); actionErr != nil {
		return nil, nil, actionErr
	}
	def, err := s.instanceField(ctx, orgID, ciID, key)
	if err != nil {
		return nil, nil, err
	}
	if value == nil {
		return nil, nil, &ValidationError{Violations: []Violation{{Field: key, Detail: instanceValueMissing}}}
	}
	item, err := s.Update(ctx, orgID, ciID, UpdateRequest{
		Attributes: map[string]any{key: replacePatch(before.Attributes[key], value)},
		IfMatch:    ifMatch,
		Manual:     &ManualChange{Author: author},
	})
	if err != nil {
		return nil, nil, err
	}
	return &InstanceAttribute{Key: key, Definition: *def, Value: item.Attributes[key]}, item, nil
}

// DeleteInstanceAttribute removes the value of the instance attribute key
// from _instance of a CI through Update; the definition stays. A key
// without a definition is a *ValidationError.
func (s *Service) DeleteInstanceAttribute(ctx context.Context, orgID, ciID, key string, ifMatch *int64, author string) (*Item, error) {
	ctx = permission.WithAction(ctx, identity.PermCIInstanceAttributeManage)
	before, err := s.repo.GetByID(ctx, orgID, ciID)
	if err != nil {
		return nil, err
	}
	if actionErr := checkInstanceAction(ctx, before); actionErr != nil {
		return nil, actionErr
	}
	if _, err := s.instanceField(ctx, orgID, ciID, key); err != nil {
		return nil, err
	}
	return s.Update(ctx, orgID, ciID, UpdateRequest{
		Attributes: map[string]any{key: nil},
		IfMatch:    ifMatch,
		Manual:     &ManualChange{Author: author},
	})
}

// checkInstanceAction checks the action of the instance attribute routes
// (ci_instance_attribute:manage, not ci:write; WP-728) on the CI before
// its key is validated; Update checks it again with the action of ctx.
func checkInstanceAction(ctx context.Context, item *Item) error {
	return permission.CheckActionOn(ctx, permission.KindCI, permission.ActionFrom(ctx),
		func(context.Context) ([]permission.ActionTarget, error) {
			return []permission.ActionTarget{ciTarget(item)}, nil
		})
}

// replacePatch is the merge patch member that replaces old with value
// (RFC 7396 merges objects): the members of an old object that value
// lacks are removed.
func replacePatch(old, value any) any {
	next, ok := value.(map[string]any)
	if !ok {
		return value
	}
	prev, _ := old.(map[string]any)
	out := make(map[string]any, len(next)+len(prev))
	for k := range prev {
		if _, kept := next[k]; !kept {
			out[k] = nil
		}
	}
	for k, v := range next {
		out[k] = replacePatch(prev[k], v)
	}
	return out
}

// checkInstanceWrite refuses a manual write that changes an instance
// attribute of the CI without ci_instance_attribute:manage (MET-14,
// WP-256): PATCH /cis/{id} needs it for every changed instance key, an
// instance value sent unchanged (the form sends the whole attribute map)
// stays allowed. A write without a principal is refused as well.
func (s *Service) checkInstanceWrite(ctx context.Context, orgID string, before *Item, req *UpdateRequest) error {
	if req.Manual == nil || len(req.Attributes) == 0 {
		return nil
	}
	if p, ok := identity.PrincipalFromContext(ctx); ok && p.Has(identity.PermCIInstanceAttributeManage) {
		return nil
	}
	defs, err := s.instanceFields(ctx, orgID, before.ID)
	if err != nil || len(defs) == 0 {
		return err
	}
	after := MergePatch(before.Attributes, req.Attributes)
	for i := range defs {
		key := defs[i].Name
		if _, sent := req.Attributes[key]; !sent {
			continue
		}
		old, had := before.Attributes[key]
		now, has := after[key]
		if had != has || !sameValue(old, now) {
			return ErrInstanceAttributeForbidden
		}
	}
	return nil
}

// ListInstanceAttributes handles GET /api/v1/cis/{id}/instance-attributes:
// the complete, bounded list of the CI (api.CompleteList).
func (h *Handler) ListInstanceAttributes(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	items, err := h.svc.ListInstanceAttributes(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		writeInstanceError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.CompleteList(items))
}

// SetInstanceAttribute handles PUT /api/v1/cis/{id}/instance-attributes/{key}.
func (h *Handler) SetInstanceAttribute(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req SetInstanceAttributeRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	ifMatch, ok := instanceIfMatch(w, r)
	if !ok {
		return
	}
	attr, item, err := h.svc.SetInstanceAttribute(r.Context(), t.OrganizationID, chi.URLParam(r, "id"),
		chi.URLParam(r, "key"), req.Value, ifMatch, t.UserID)
	if err != nil {
		writeInstanceError(w, err)
		return
	}
	if h.dispatcher != nil {
		h.dispatcher.Dispatch(r.Context(), t.OrganizationID, "ci.updated", item)
	}
	w.Header().Set("ETag", api.ETag(item.Version))
	api.WriteJSON(w, http.StatusOK, attr)
}

// DeleteInstanceAttribute handles DELETE /api/v1/cis/{id}/instance-attributes/{key}.
func (h *Handler) DeleteInstanceAttribute(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	ifMatch, ok := instanceIfMatch(w, r)
	if !ok {
		return
	}
	item, err := h.svc.DeleteInstanceAttribute(r.Context(), t.OrganizationID, chi.URLParam(r, "id"),
		chi.URLParam(r, "key"), ifMatch, t.UserID)
	if err != nil {
		writeInstanceError(w, err)
		return
	}
	if h.dispatcher != nil {
		h.dispatcher.Dispatch(r.Context(), t.OrganizationID, "ci.updated", item)
	}
	w.Header().Set("ETag", api.ETag(item.Version))
	w.WriteHeader(http.StatusNoContent)
}

// instanceIfMatch reads If-Match (API-07); a malformed one is answered 412.
func instanceIfMatch(w http.ResponseWriter, r *http.Request) (*int64, bool) {
	version, present, err := api.ParseIfMatch(r)
	if err != nil {
		api.WritePreconditionFailed(w, err.Error())
		return nil, false
	}
	if !present {
		return nil, true
	}
	return &version, true
}

// writeInstanceError maps the errors of the instance attribute routes as
// PATCH /cis/{id} does: 412/409, 400/409/422 with violations, 403, 404.
func writeInstanceError(w http.ResponseWriter, err error) {
	switch {
	case writeFieldForbidden(w, err), writeConcurrencyError(w, err), writeValidationError(w, err):
	case errors.Is(err, ErrInstanceAttributeForbidden):
		api.WriteError(w, http.StatusForbidden, "Forbidden", instanceForbiddenDetail)
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "CI not found")
	default:
		api.WriteRepoError(w, err)
	}
}
