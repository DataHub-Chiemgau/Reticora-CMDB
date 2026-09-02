package override

import (
	"context"
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
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
func (h *Handler) ListForCI(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	items, err := h.repo.ListForCI(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"data": items})
}

// SetOverride handles PUT /api/v1/cis/{id}/fields/{name}/override
func (h *Handler) SetOverride(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
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
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	h.dispatch(r, t.OrganizationID, "override.changed", fv)
	api.WriteJSON(w, http.StatusOK, fv)
}

// ClearOverride handles DELETE /api/v1/cis/{id}/fields/{name}/override
func (h *Handler) ClearOverride(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	fv, err := h.repo.ClearOverride(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), chi.URLParam(r, "name"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "field value not found")
		return
	}
	h.dispatch(r, t.OrganizationID, "override.changed", fv)
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
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
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
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
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
	policy.OrganizationID = t.OrganizationID
	policy.IsDefault = true
	if err := h.repo.UpsertPolicy(r.Context(), &policy); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	h.dispatch(r, t.OrganizationID, "reconciliation.policy_changed", policy)
	api.WriteJSON(w, http.StatusOK, policy)
}
