package operator

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// maxPlanChangeReason is the longest reason of a plan change (characters).
const maxPlanChangeReason = 500

// planChangeRequest is the body of PUT /api/v1/admin/orgs/{id}/plan.
type planChangeRequest struct {
	Plan   string `json:"plan"`
	Reason string `json:"reason"`
}

// PlanChangeResponse answers a plan change: the plan, the time of its last
// change and the rows the change wrote.
type PlanChangeResponse struct {
	Plan          entitlement.Plan          `json:"plan"`
	PlanBefore    entitlement.Plan          `json:"plan_before"`
	PlanChangedAt *time.Time                `json:"plan_changed_at"`
	Unchanged     bool                      `json:"unchanged"`
	Changed       []string                  `json:"changed"`
	Entitlements  []entitlement.Entitlement `json:"entitlements"`
}

// validate checks the body: plan one of the four plans, reason 1–500
// characters.
func (req *planChangeRequest) validate() *api.FieldError {
	plans := []string{string(entitlement.PlanEssential), string(entitlement.PlanStandard),
		string(entitlement.PlanPro), string(entitlement.PlanEnterprise)}
	if !slices.Contains(plans, req.Plan) {
		return &api.FieldError{Field: "plan", Message: "plan must be one of " + strings.Join(plans, ", "),
			Status: http.StatusUnprocessableEntity}
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" || utf8.RuneCountInString(req.Reason) > maxPlanChangeReason {
		return &api.FieldError{Field: "reason", Message: "reason is required (1–500 characters)",
			Status: http.StatusUnprocessableEntity}
	}
	return nil
}

// ChangePlan changes the plan of an organization (PKG-02, ENT-04, WP-882):
// in one transaction with its operator_audit entry plan.change it writes
// organization.plan and the rows of origin plan (entitlement.PlanTransition;
// add-on and manual rows stay, nothing is deleted) and, as the last write,
// the event entitlement.changed, which notifies the org_admins. A failed
// audit entry rolls everything back (500). The same plan again changes
// nothing, emits no event and is recorded with "unchanged": true.
func (h *Handler) ChangePlan(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.targetOrganization(w, r)
	if !ok {
		return
	}
	var req planChangeRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if fieldErr := req.validate(); fieldErr != nil {
		api.WriteFieldError(w, fieldErr)
		return
	}
	op, _ := FromContext(r.Context())
	var change entitlement.PlanChange
	var entry *AuditEntry
	err := h.audit.RecordChange(orgContext(r.Context(), orgID), orgID, func(ctx context.Context) (*AuditEntry, error) {
		var changeErr error
		change, changeErr = h.entitlements.ChangePlan(ctx, orgID, entitlement.Plan(req.Plan), entitlement.TransitionOptions{})
		if changeErr != nil {
			return nil, changeErr
		}
		details := map[string]any{"remote": r.RemoteAddr, "reason": req.Reason,
			"before": map[string]any{"plan": change.From, "entitlements": change.Before},
			"after":  map[string]any{"plan": change.To, "entitlements": change.Written}}
		if change.Unchanged {
			details["unchanged"] = true
		} else {
			details["changed"] = change.Changed
			if emitErr := h.emitEntitlementChanged(ctx, orgID, &events.EntitlementChangedData{ID: orgID, Plan: string(change.To),
				PlanBefore: string(change.From), Changed: change.Changed}); emitErr != nil {
				return nil, emitErr
			}
		}
		entry = &AuditEntry{OperatorID: op.ID, OperatorKind: op.Kind, Action: "plan.change",
			TargetOrg: orgID, Status: http.StatusOK, Details: details}
		return entry, nil
	})
	switch {
	case errors.Is(err, ErrAuditFailed):
		if entry == nil {
			entry = &AuditEntry{OperatorID: op.ID, Action: "plan.change"}
		}
		writeAuditFailure(w, entry, err)
	case errors.Is(err, entitlement.ErrPlanChangeUnavailable):
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "plan changes are not available")
	case err != nil:
		api.WriteRepoError(w, err)
	default:
		markAudited(r.Context())
		changed := change.Changed
		if changed == nil {
			changed = []string{}
		}
		written := change.Written
		if written == nil {
			written = []entitlement.Entitlement{}
		}
		at := change.ChangedAt
		if at == nil {
			if _, stored, planErr := h.entitlements.OrganizationPlan(orgContext(r.Context(), orgID), orgID); planErr == nil {
				at = stored
			}
		}
		api.WriteJSON(w, http.StatusOK, PlanChangeResponse{Plan: change.To, PlanBefore: change.From, PlanChangedAt: at,
			Unchanged: change.Unchanged, Changed: changed, Entitlements: written})
	}
}

// emitEntitlementChanged writes entitlement.changed into the outbox as the
// last write of the transaction (database.BeforeCommit, Konvention 20),
// organization-wide without scope; the entity is the organization.
func (h *Handler) emitEntitlementChanged(ctx context.Context, orgID string, data *events.EntitlementChangedData) error {
	if h.pool == nil {
		return nil
	}
	if data.Changed == nil {
		data.Changed = []string{}
	}
	return database.WithRequestTenant(ctx, h.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return database.BeforeCommit(ctx, func() error {
			_, err := events.NewOutbox().Append(ctx, tx, &events.Event{OrgID: orgID, Type: events.EntitlementChanged,
				EntityID: orgID, Data: *data})
			return err
		})
	})
}
