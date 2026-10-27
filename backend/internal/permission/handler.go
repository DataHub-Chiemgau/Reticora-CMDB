package permission

import (
	"errors"
	"log/slog"
	"net/http"
	"slices"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler exposes permission catalogue and grant endpoints.
type Handler struct{ repo Repository }

// NewHandler creates a permission handler.
func NewHandler(repo Repository) *Handler { return &Handler{repo: repo} }

// RegisterRoutes registers permission routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/permissions", h.List)
	r.Get("/api/v1/roles/{id}/permissions", h.ListRole)
	r.Put("/api/v1/roles/{id}/permissions", h.ReplaceRole)
	r.Get("/api/v1/me/permissions", h.Effective)
}

// Require returns middleware that rejects callers without key.
func Require(repo Repository, key string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t := tenant.FromContext(r.Context())
			if t.OrganizationID == "" || t.UserID == "" {
				api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant or user context")
				return
			}
			allowed, err := repo.HasPermission(r.Context(), t.OrganizationID, t.UserID, key)
			if err != nil {
				api.WriteRepoError(w, err)
				return
			}
			if !allowed {
				api.WriteError(w, http.StatusForbidden, "Forbidden", "missing permission "+key)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func tenantInfo(w http.ResponseWriter, r *http.Request) (tenant.TenantInfo, bool) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return tenant.TenantInfo{}, false
	}
	return t, true
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	if _, ok := tenantInfo(w, r); !ok {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	items, err := h.repo.ListPermissions(r.Context())
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.Page(items, page))
}

func (h *Handler) ListRole(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	roleID, ok := api.ParseUUIDParam(w, r, "id", "role not found")
	if !ok {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	items, err := h.repo.ListRolePermissions(r.Context(), t.OrganizationID, roleID)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.Page(items, page))
}

// ReplaceRole handles PUT /api/v1/roles/{id}/permissions. The route is gated
// by the role:manage authorization middleware. The role applies to all its
// assignments, so permissions it adds must be held by the caller
// organization-wide (403 otherwise, RBA-03, F-015). A role id that is no
// UUID or no role of the organization is 404, an unknown permission key
// 400 naming permission_keys; database errors go through api.WriteRepoError
// (F-047).
func (h *Handler) ReplaceRole(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	roleID, ok := api.ParseUUIDParam(w, r, "id", "role not found")
	if !ok {
		return
	}
	p, ok := identity.PrincipalFromContext(r.Context())
	if !ok {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "changing a role requires an authenticated principal")
		return
	}
	var req ReplaceRolePermissionsRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	items, err := h.repo.ReplaceRolePermissions(r.Context(), t.OrganizationID, roleID, t.UserID, &p, req.PermissionKeys)
	var widening *RoleWideningError
	switch {
	case err == nil:
		api.WriteJSON(w, http.StatusOK, items)
	case errors.As(err, &widening):
		api.WriteError(w, http.StatusForbidden, "Forbidden", widening.Error())
	case errors.Is(err, ErrRoleNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrRoleNotFound.Error())
	case api.WriteFieldError(w, err):
	default:
		slog.WarnContext(r.Context(), "replacing role permissions failed", "role_id", roleID, "error", err)
		api.WriteRepoError(w, err)
	}
}

func (h *Handler) Effective(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	if t.UserID == "" {
		api.WriteJSON(w, http.StatusOK, EffectivePermissionsResponse{Permissions: []string{}})
		return
	}
	keys, err := h.repo.EffectivePermissions(r.Context(), t.OrganizationID, t.UserID)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	// A user session also holds the rights of its IdP groups (RBA-02,
	// F-200): the identity module resolves them together with the role
	// assignments at login and refresh, and RequirePermission enforces that
	// set. Without them a member of reticora-viewer without a role
	// assignment would read no permission although the API grants the
	// viewer rights. API keys keep the rights of their owner's assignments.
	if p, ok := identity.PrincipalFromContext(r.Context()); ok && p.Type == identity.PrincipalTypeUser && p.Subject == t.UserID {
		keys = withSessionPermissions(keys, p.Permissions)
	}
	api.WriteJSON(w, http.StatusOK, EffectivePermissionsResponse{UserID: t.UserID, Permissions: keys})
}

// withSessionPermissions returns keys and the session's permissions, sorted
// and without duplicates.
func withSessionPermissions(keys []string, session []identity.Permission) []string {
	merged := append([]string{}, keys...)
	for _, p := range session {
		merged = append(merged, string(p))
	}
	slices.Sort(merged)
	return slices.Compact(merged)
}
