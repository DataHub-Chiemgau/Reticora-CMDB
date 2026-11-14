package sla

import (
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// REST of the availability targets (ALR-06, WP-1048, E-160): under
// /api/v1/monitoring/sla, gated with monitoring_analytics (WP-865); reading
// with monitoring:read, writing with monitoring:write. A target outside the
// caller's scope is 404, a write on a CI or client outside it 403.
//
//	GET    /api/v1/monitoring/sla/targets       list (filters scope_kind, ci_id, client_id; cursor)
//	POST   /api/v1/monitoring/sla/targets       create
//	GET    /api/v1/monitoring/sla/targets/{id}  one target
//	PATCH  /api/v1/monitoring/sla/targets/{id}  change name, percent, period, warning share, calendar, enabled
//	DELETE /api/v1/monitoring/sla/targets/{id}  delete

const (
	targetsPath = "/api/v1/monitoring/sla/targets"
	targetPath  = targetsPath + "/{id}"
)

// AvailabilityHandler serves the availability targets.
type AvailabilityHandler struct {
	repo AvailabilityRepository
}

// NewAvailabilityHandler returns the handler on repo.
func NewAvailabilityHandler(repo AvailabilityRepository) *AvailabilityHandler {
	return &AvailabilityHandler{repo: repo}
}

// RegisterRoutes mounts the routes.
func (h *AvailabilityHandler) RegisterRoutes(r chi.Router) {
	r.Get(targetsPath, h.list)
	r.Post(targetsPath, h.create)
	r.Get(targetPath, h.get)
	r.Patch(targetPath, h.update)
	r.Delete(targetPath, h.delete)
}

// writeTargetError maps the errors of the target routes.
func writeTargetError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrTargetNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrTargetNotFound.Error())
	case errors.Is(err, ErrTargetExists):
		api.WriteError(w, http.StatusConflict, "Conflict", ErrTargetExists.Error())
	case errors.Is(err, ErrTargetOutOfScope):
		api.WriteError(w, http.StatusForbidden, "Forbidden", ErrTargetOutOfScope.Error())
	case api.WriteFieldError(w, err):
	default:
		slog.WarnContext(r.Context(), "availability target request failed", "error", err)
		api.WriteRepoError(w, err)
	}
}

func (h *AvailabilityHandler) list(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := AvailabilityFilter{ScopeKind: q.Get("scope_kind"), CIID: strings.ToLower(q.Get("ci_id")),
		ClientID: strings.ToLower(q.Get("client_id"))}
	if f.ScopeKind != "" && !slices.Contains(ScopeKinds, f.ScopeKind) {
		api.WriteFieldError(w, unprocessable("scope_kind", "scope_kind must be one of "+strings.Join(ScopeKinds, ", ")))
		return
	}
	for name, v := range map[string]string{"ci_id": f.CIID, "client_id": f.ClientID} {
		if v != "" && !api.IsUUID(v) {
			api.WriteFieldError(w, unprocessable(name, name+" must be a UUID"))
			return
		}
	}
	items, err := h.repo.ListTargets(r.Context(), t.OrganizationID, f)
	if err != nil {
		writeTargetError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.Page(items, page))
}

func (h *AvailabilityHandler) create(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	var req CreateAvailabilityTargetRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "request body must be a JSON object of an availability target")
		return
	}
	if req.TargetPercent == nil {
		api.WriteFieldError(w, unprocessable("target_percent", "target_percent is required"))
		return
	}
	target := targetOf(t.OrganizationID, &req)
	if err := h.repo.CreateTarget(r.Context(), target); err != nil {
		writeTargetError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, target)
}

func (h *AvailabilityHandler) get(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrTargetNotFound.Error())
	if !ok {
		return
	}
	target, err := h.repo.GetTarget(r.Context(), t.OrganizationID, id)
	if err != nil {
		writeTargetError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, target)
}

func (h *AvailabilityHandler) update(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrTargetNotFound.Error())
	if !ok {
		return
	}
	var req UpdateAvailabilityTargetRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "request body must be a JSON object of target fields")
		return
	}
	target, err := h.repo.UpdateTarget(r.Context(), t.OrganizationID, id, &req)
	if err != nil {
		writeTargetError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, target)
}

func (h *AvailabilityHandler) delete(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrTargetNotFound.Error())
	if !ok {
		return
	}
	if err := h.repo.DeleteTarget(r.Context(), t.OrganizationID, id); err != nil {
		writeTargetError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
