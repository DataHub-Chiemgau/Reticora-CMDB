package citype

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// registerRuleRoutes registers the rule catalog (WP-716, MET-45, API-06;
// module metamodel, E-105). Reading requires ci:read, writing
// citype:manage (E-106, server/authz.go).
func (h *Handler) registerRuleRoutes(r chi.Router) {
	r.Get("/api/v1/rules", h.ListRules)
	r.Post("/api/v1/rules", h.CreateRule)
	r.Get("/api/v1/rules/{id}", h.GetRule)
	r.Patch("/api/v1/rules/{id}", h.UpdateRule)
	r.Delete("/api/v1/rules/{id}", h.DeleteRule)
}

// ruleStore returns the rule catalog or answers 503 (in-memory mode has
// none, like attributeStore).
func (h *Handler) ruleStore(w http.ResponseWriter, r *http.Request) (RuleStore, tenant.TenantInfo, bool) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return nil, t, false
	}
	store, ok := h.repo.(RuleStore)
	if !ok {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "the rule catalog needs the database")
	}
	return store, t, ok
}

// writeRuleError reports a failed rule request (RFC 7807).
func writeRuleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrRuleNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrRuleNotFound.Error())
	case errors.Is(err, ErrRuleReadOnly):
		api.WriteError(w, http.StatusForbidden, "Forbidden", ErrRuleReadOnly.Error())
	case errors.Is(err, ErrTemplateVersionImmutable):
		api.WriteProblem(w, http.StatusConflict, "urn:reticora:problem:conflict", "Conflict",
			"the rules of a published or retired template version are immutable")
	default:
		api.WriteRepoError(w, err)
	}
}

// ListRules handles GET /api/v1/rules (?ci_type_id=, attribute_set_id=,
// template_version_id=, attribute_key=, kind=): the organization's and the
// system rules newest first on (created_at, id) with limit and the signed
// cursor of API-02.
func (h *Handler) ListRules(w http.ResponseWriter, r *http.Request) {
	store, t, ok := h.ruleStore(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseNewestFirstPage(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	items, total, err := store.ListRulePage(r.Context(), t.OrganizationID, &RuleFilter{
		CITypeID: q.Get("ci_type_id"), AttributeSetID: q.Get("attribute_set_id"),
		TemplateVersionID: q.Get("template_version_id"), AttributeKey: q.Get("attribute_key"), Kind: q.Get("kind"),
	}, page)
	if err != nil {
		writeRuleError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.NewestFirstPage(items, total, page, func(rule *MetamodelRule) (time.Time, string) {
		return rule.CreatedAt, rule.ID
	}))
}

// GetRule handles GET /api/v1/rules/{id}.
func (h *Handler) GetRule(w http.ResponseWriter, r *http.Request) {
	store, t, ok := h.ruleStore(w, r)
	if !ok {
		return
	}
	rule, err := store.GetRule(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		writeRuleError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, rule)
}

// CreateRule handles POST /api/v1/rules.
func (h *Handler) CreateRule(w http.ResponseWriter, r *http.Request) {
	store, t, ok := h.ruleStore(w, r)
	if !ok {
		return
	}
	var in RuleInput
	if err := api.ReadJSON(r, &in); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	rule, err := store.CreateRule(r.Context(), t.OrganizationID, &in)
	if err != nil {
		writeRuleError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, rule)
}

// UpdateRule handles PATCH /api/v1/rules/{id}.
func (h *Handler) UpdateRule(w http.ResponseWriter, r *http.Request) {
	store, t, ok := h.ruleStore(w, r)
	if !ok {
		return
	}
	var patch RulePatch
	if err := api.ReadJSON(r, &patch); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	rule, err := store.UpdateRule(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), &patch)
	if err != nil {
		writeRuleError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, rule)
}

// DeleteRule handles DELETE /api/v1/rules/{id}.
func (h *Handler) DeleteRule(w http.ResponseWriter, r *http.Request) {
	store, t, ok := h.ruleStore(w, r)
	if !ok {
		return
	}
	if err := store.DeleteRule(r.Context(), t.OrganizationID, chi.URLParam(r, "id")); err != nil {
		writeRuleError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
