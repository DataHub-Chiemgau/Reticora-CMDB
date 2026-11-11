package operator

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// addonBookRequest is the optional body of POST /addons/{key}.
type addonBookRequest struct {
	ValidUntil *time.Time `json:"valid_until"`
	Source     string     `json:"source"`
}

// errAddonNotBooked answers the cancellation of an add-on without an
// active row (404).
var errAddonNotBooked = errors.New("the add-on is not booked")

// ListAddonCatalog answers GET /api/v1/admin/addons: the bookable add-ons
// (PKG-03, CH14), derived from entitlement.Addons, without tenant data.
func (h *Handler) ListAddonCatalog(w http.ResponseWriter, _ *http.Request) {
	api.WriteJSON(w, http.StatusOK, map[string]any{"data": entitlement.AddonCatalog()})
}

// addonKey reads {key} and answers 422 (field key) when it is no add-on.
func addonKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := chi.URLParam(r, "key")
	if !slices.Contains(entitlement.Addons, key) {
		api.WriteFieldError(w, &api.FieldError{Field: "key", Status: http.StatusUnprocessableEntity,
			Message: "not an add-on (allowed: " + strings.Join(entitlement.Addons, ", ") + ")"})
		return "", false
	}
	return key, true
}

// BookAddon books an add-on for an organization (PKG-03, CH14, WP-883):
// the row of the key gets origin addon, enabled and the given valid_until
// and source (default manual); 201 for a new row, 200 for a booking again.
// The booking is recorded as addon.book with before and after in
// operator_audit and emits entitlement.changed with the add-on, in one
// transaction; a plan change never touches it.
func (h *Handler) BookAddon(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.targetOrganization(w, r)
	if !ok {
		return
	}
	key, ok := addonKey(w, r)
	if !ok {
		return
	}
	var req addonBookRequest
	if r.ContentLength != 0 {
		if err := api.ReadJSON(r, &req); err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
			return
		}
	}
	if req.Source == "" {
		req.Source = "manual"
	}
	if req.ValidUntil != nil && !req.ValidUntil.After(time.Now()) {
		api.WriteFieldError(w, &api.FieldError{Field: "valid_until", Status: http.StatusUnprocessableEntity,
			Message: "valid_until must lie in the future"})
		return
	}
	created := false
	var booked entitlement.Entitlement
	err := h.addonChange(r, orgID, key, "addon.book", func(ctx context.Context, before *entitlement.Entitlement) (*entitlement.Entitlement, error) {
		created = before == nil
		ent := entitlement.Entitlement{OrganizationID: orgID, FeatureKey: key, Enabled: true, Limits: map[string]int64{},
			ValidUntil: req.ValidUntil, Source: req.Source, Origin: entitlement.OriginAddon}
		if before != nil {
			ent.Plan, ent.Limits = before.Plan, before.Limits
		} else if plan, _, planErr := h.entitlements.OrganizationPlan(ctx, orgID); planErr == nil {
			ent.Plan = plan
		}
		stored, grantErr := h.entitlements.Grant(ctx, &ent)
		booked = stored
		return &stored, grantErr
	})
	if h.writeAddonError(w, err, "addon.book") {
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	api.WriteJSON(w, status, booked)
}

// CancelAddon cancels an add-on (PKG-03, WP-883): the row is disabled, row,
// limits and valid_until stay; 204. An add-on without an active row is 404.
func (h *Handler) CancelAddon(w http.ResponseWriter, r *http.Request) {
	orgID, ok := h.targetOrganization(w, r)
	if !ok {
		return
	}
	key, ok := addonKey(w, r)
	if !ok {
		return
	}
	err := h.addonChange(r, orgID, key, "addon.cancel", func(ctx context.Context, before *entitlement.Entitlement) (*entitlement.Entitlement, error) {
		if before == nil || !before.Enabled {
			return nil, errAddonNotBooked
		}
		ent := *before
		ent.Enabled = false
		stored, grantErr := h.entitlements.Grant(ctx, &ent)
		return &stored, grantErr
	})
	if h.writeAddonError(w, err, "addon.cancel") {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// addonChange runs write with the stored row of the add-on (nil without
// one) in the transaction of RecordChange, records action with before and
// after and emits entitlement.changed as the last write.
func (h *Handler) addonChange(r *http.Request, orgID, key, action string,
	write func(ctx context.Context, before *entitlement.Entitlement) (*entitlement.Entitlement, error)) error {
	op, _ := FromContext(r.Context())
	return h.audit.RecordChange(orgContext(r.Context(), orgID), orgID, func(ctx context.Context) (*AuditEntry, error) {
		rows, err := h.entitlements.Stored(ctx, orgID)
		if err != nil {
			return nil, err
		}
		var before *entitlement.Entitlement
		for i := range rows {
			if rows[i].FeatureKey == key {
				before = &rows[i]
			}
		}
		after, err := write(ctx, before)
		if err != nil {
			return nil, err
		}
		plan, _, err := h.entitlements.OrganizationPlan(ctx, orgID)
		if err != nil {
			return nil, err
		}
		data := events.EntitlementChangedData{ID: orgID, Plan: string(plan), Addon: key, Changed: []string{key}}
		if emitErr := h.emitEntitlementChanged(ctx, orgID, &data); emitErr != nil {
			return nil, emitErr
		}
		details := map[string]any{"remote": r.RemoteAddr, "feature_key": key, "after": after}
		if before != nil {
			details["before"] = before
		}
		return &AuditEntry{OperatorID: op.ID, OperatorKind: op.Kind, Action: action,
			TargetOrg: orgID, Status: http.StatusOK, Details: details}, nil
	})
}

// writeAddonError answers err of an add-on change; false when err is nil.
func (h *Handler) writeAddonError(w http.ResponseWriter, err error, action string) bool {
	var validation *entitlement.ValidationError
	switch {
	case err == nil:
		return false
	case errors.Is(err, errAddonNotBooked):
		api.WriteError(w, http.StatusNotFound, "Not Found", "the add-on is not booked")
	case errors.As(err, &validation):
		api.WriteFieldError(w, &api.FieldError{Field: validation.Field, Message: validation.Error(),
			Status: http.StatusUnprocessableEntity})
	case errors.Is(err, ErrAuditFailed):
		writeAuditFailure(w, &AuditEntry{Action: action}, err)
	default:
		api.WriteRepoError(w, err)
	}
	return true
}
