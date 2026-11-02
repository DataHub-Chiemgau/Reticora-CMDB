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
// operator_audit in the same transaction (ENT-04, F-208): when the entry
// cannot be written the change is rolled back and the answer is 500. An
// unknown feature or quota key is 422 with the field (F-209).
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
	op, _ := FromContext(r.Context())
	var granted entitlement.Entitlement
	var entry *AuditEntry
	ent, err := req.Entitlement(orgID)
	if err == nil {
		err = h.audit.RecordChange(orgContext(r.Context(), orgID), orgID, func(ctx context.Context) (*AuditEntry, error) {
			before, listErr := h.entitlements.List(ctx, orgID)
			if listErr != nil {
				return nil, listErr
			}
			stored, grantErr := h.entitlements.Grant(ctx, ent)
			if grantErr != nil {
				return nil, grantErr
			}
			granted = stored
			details := map[string]any{"remote": r.RemoteAddr, "feature_key": stored.FeatureKey, "after": stored}
			for i := range before {
				if before[i].FeatureKey == stored.FeatureKey {
					details["before"] = before[i]
				}
			}
			entry = &AuditEntry{OperatorID: op.ID, OperatorKind: op.Kind, Action: "entitlement.grant",
				TargetOrg: orgID, Status: http.StatusOK, Details: details}
			return entry, nil
		})
	}
	var validation *entitlement.ValidationError
	switch {
	case errors.As(err, &validation):
		api.WriteFieldError(w, &api.FieldError{Field: validation.Field, Message: validation.Error(),
			Status: http.StatusUnprocessableEntity})
	case errors.Is(err, ErrAuditFailed):
		if entry == nil {
			entry = &AuditEntry{OperatorID: op.ID, Action: "entitlement.grant"}
		}
		writeAuditFailure(w, entry, err)
	case err != nil:
		api.WriteRepoError(w, err)
	default:
		markAudited(r.Context())
		api.WriteJSON(w, http.StatusOK, granted)
	}
}
