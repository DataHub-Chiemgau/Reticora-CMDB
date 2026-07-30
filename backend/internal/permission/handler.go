package permission

import (
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
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
			allowed, err := repo.HasPermission(t.OrganizationID, t.UserID, key)
			if err != nil {
				api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
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
	items, err := h.repo.ListPermissions()
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, items)
}

func (h *Handler) ListRole(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	items, err := h.repo.ListRolePermissions(t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, items)
}

func (h *Handler) ReplaceRole(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	if t.UserID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing user context")
		return
	}
	allowed, err := h.repo.HasPermission(t.OrganizationID, t.UserID, "permission:write")
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	if !allowed {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "missing permission permission:write")
		return
	}
	var req ReplaceRolePermissionsRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	items, err := h.repo.ReplaceRolePermissions(t.OrganizationID, chi.URLParam(r, "id"), t.UserID, req.PermissionKeys)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, items)
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
	keys, err := h.repo.EffectivePermissions(t.OrganizationID, t.UserID)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, EffectivePermissionsResponse{UserID: t.UserID, Permissions: keys})
}
