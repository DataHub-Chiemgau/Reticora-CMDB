package search

import (
	"net/http"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

type Handler struct {
	backend     Backend
	permissions permission.Repository
}

func NewHandler(backend Backend, permissions permission.Repository) *Handler {
	return &Handler{backend: backend, permissions: permissions}
}
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/search", h.Search)
	r.Post("/api/v1/search/reindex", h.Reindex)
}
func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page := api.ParsePagination(r)
	if page.CursorError != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", page.CursorError.Error())
		return
	}
	q := Query{OrganizationID: t.OrganizationID, UserID: t.UserID, Text: r.URL.Query().Get("q"), EntityTypes: splitTypes(r.URL.Query().Get("type")), Limit: page.Limit, Offset: page.Offset, Highlight: true}
	res, err := h.backend.Query(q)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	res.Data = h.filterAllowed(t, res.Data)
	res.Total = len(res.Data)
	res.HasMore = false
	api.WriteJSON(w, http.StatusOK, res)
}
func (h *Handler) Reindex(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if h.permissions != nil && t.UserID != "" {
		ok, err := h.permissions.HasPermission(t.OrganizationID, t.UserID, "search:write")
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
			return
		}
		if !ok {
			api.WriteError(w, http.StatusForbidden, "Forbidden", "missing permission search:write")
			return
		}
	}
	out, err := h.backend.ReindexTenant(t.OrganizationID)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusAccepted, out)
}
func (h *Handler) filterAllowed(t tenant.TenantInfo, hits []Hit) []Hit {
	if h.permissions == nil || t.UserID == "" {
		return hits
	}
	out := hits[:0]
	for _, hit := range hits {
		key := permissionFor(hit.EntityType)
		if key == "" {
			continue
		}
		ok, err := h.permissions.HasPermission(t.OrganizationID, t.UserID, key)
		if err == nil && ok {
			out = append(out, hit)
		}
	}
	return out
}
func permissionFor(entity string) string {
	switch entity {
	case "ci":
		return "ci:read"
	case "asset":
		return "asset:read"
	case "document":
		return "document:read"
	case "ticket":
		return "ticket:read"
	case "contact":
		return "contact:read"
	case "compliance":
		return "compliance:read"
	default:
		return ""
	}
}
func splitTypes(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
