package citype

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
)

// Computed attributes over asset fields (SR-58, WP-871): their values
// inherit the field rules of the asset fields they read, so defining one
// is a field permission decision. Creating or changing a definition with
// asset sources needs field_permission:manage besides ci_attribute:manage;
// a definition without asset sources stays at ci_attribute:manage. Every
// change of a definition drops the cached field policy snapshot of the
// organization, which carries the asset sources.

// FieldPolicyInvalidator drops the cached field policy snapshot of an
// organization (permission.FieldResolver.Invalidate).
type FieldPolicyInvalidator interface {
	Invalidate(ctx context.Context, orgID string)
}

// WithFieldPolicyInvalidator sets the invalidator called after a change of
// an attribute definition (SR-58).
func (h *Handler) WithFieldPolicyInvalidator(inv FieldPolicyInvalidator) *Handler {
	h.fieldPolicy = inv
	return h
}

func (h *Handler) invalidateFieldPolicy(ctx context.Context, orgID string) {
	if h.fieldPolicy != nil {
		h.fieldPolicy.Invalidate(ctx, orgID)
	}
}

// ComputedReadsAsset reports whether a computed expression as JSON reads a
// field of the linked asset; false for null, an empty value or an
// expression that does not parse (validation refuses it with 422).
func ComputedReadsAsset(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return false
	}
	expr, err := rules.ParseExpr(raw)
	if err != nil {
		return false
	}
	return exprReadsAsset(&expr)
}

func exprReadsAsset(e *rules.Expr) bool {
	if e == nil {
		return false
	}
	for _, f := range e.Fields() {
		if strings.HasPrefix(f, rules.AssetPrefix) {
			return true
		}
	}
	return false
}

// assetSourceRight answers 403 when the caller lacks
// field_permission:manage and reports whether it holds it.
func assetSourceRight(w http.ResponseWriter, r *http.Request) bool {
	perms, _ := identity.PermissionsFromContext(r.Context())
	if identity.HasPermission(perms, identity.PermFieldPermissionManage) {
		return true
	}
	api.WriteError(w, http.StatusForbidden, "Forbidden",
		"a computed attribute that reads asset fields needs the permission "+string(identity.PermFieldPermissionManage))
	return false
}
