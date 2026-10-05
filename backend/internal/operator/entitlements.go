package operator

import (
	"context"
	"errors"
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// WithEntitlements serves /api/v1/admin/orgs/{id}/entitlements: the only
// path that writes entitlements (ENT-04, E-12).
func (h *Handler) WithEntitlements(svc *entitlement.Service) *Handler {
	h.entitlements = svc
	return h
}

// orgContext is the tenant context in which the operator reads and writes
// the entitlements of an organization.
func orgContext(ctx context.Context, orgID string) context.Context {
	scope := database.OrgWideScope(orgID, "")
	return database.ContextWithTenantScope(ctx, &scope)
}

// organizationExists checks the target organization (system read).
func (h *Handler) organizationExists(ctx context.Context, orgID string) (bool, error) {
	if h.pool == nil {
		return true, nil
	}
	var exists bool
	err := database.WithSystem(ctx, h.pool, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM organization WHERE id::text = $1)`, orgID).Scan(&exists)
	})
	return exists, err
}

// targetOrganization resolves {id} and writes 404/503 when it cannot be
// served.
func (h *Handler) targetOrganization(w http.ResponseWriter, r *http.Request) (string, bool) {
	if h.entitlements == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "entitlements are not configured")
		return "", false
	}
	orgID := chi.URLParam(r, "id")
	exists, err := h.organizationExists(r.Context(), orgID)
	if err != nil {
		api.WriteRepoError(w, err)
		return "", false
	}
	if !exists {
		api.WriteError(w, http.StatusNotFound, "Not Found", "organization not found")
		return "", false
	}
	return orgID, true
}

// ListEntitlements lists the entitlements of an organization.
func (h *Handler) ListEntitlements(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.targetOrganization(w, r)
	if !ok {
		return
	}
	items, err := h.entitlements.List(orgContext(r.Context(), orgID), orgID)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[entitlement.Entitlement]{Data: items, Total: len(items), Limit: len(items)})
}

// GrantEntitlement stores (or updates) an entitlement of an organization.
// The change, with the previous and the new state, is recorded in
// operator_audit (ENT-04).
func (h *Handler) GrantEntitlement(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.targetOrganization(w, r)
	if !ok {
		return
	}
	var req entitlement.GrantRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	ent, err := req.Entitlement(orgID)
	ctx := orgContext(r.Context(), orgID)
	var before []entitlement.Entitlement
	if err == nil {
		before, err = h.entitlements.List(ctx, orgID)
	}
	var granted entitlement.Entitlement
	if err == nil {
		granted, err = h.entitlements.Grant(ctx, ent)
	}
	var validation *entitlement.ValidationError
	if errors.As(err, &validation) {
		api.WriteError(w, http.StatusUnprocessableEntity, "Unprocessable Entity", validation.Error())
		return
	}
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}

	op, _ := FromContext(r.Context())
	details := map[string]any{"remote": r.RemoteAddr, "feature_key": granted.FeatureKey, "after": granted}
	for i := range before {
		if before[i].FeatureKey == granted.FeatureKey {
			details["before"] = before[i]
		}
	}
	h.record(r.Context(), &AuditEntry{OperatorID: op.ID, OperatorKind: op.Kind, Action: "entitlement.grant",
		TargetOrg: orgID, Status: http.StatusOK, Details: details})
	api.WriteJSON(w, http.StatusOK, granted)
}
