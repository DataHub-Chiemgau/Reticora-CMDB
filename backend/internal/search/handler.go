package search

import (
	"context"
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
	res, err := h.backend.Query(r.Context(), q)
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	res.Data = h.filterAllowed(r.Context(), t, res.Data)
	res.Total = len(res.Data)
	res.HasMore = false
	api.WriteJSON(w, http.StatusOK, res)
}

// Reindex handles POST /api/v1/search/reindex. The route is gated by the
// search:write permission in the router's authorization layer; the handler
// only needs the tenant context.
func (h *Handler) Reindex(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	out, err := h.backend.ReindexTenant(r.Context(), t.OrganizationID)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusAccepted, out)
}
// filterAllowed keeps the hits whose entity type the caller may read
// (SRC-01). It fails closed: without a permission repository or a user every
// hit is dropped, as are hits of types without a read permission.
func (h *Handler) filterAllowed(ctx context.Context, t tenant.TenantInfo, hits []Hit) []Hit {
	out := hits[:0]
	if h.permissions == nil || t.UserID == "" {
		return out
	}
	allowed := map[string]bool{}
	for _, hit := range hits {
		key, ok := ReadPermission[hit.EntityType]
		if !ok {
			continue
		}
		granted, seen := allowed[key]
		if !seen {
			ok, err := h.permissions.HasPermission(ctx, t.OrganizationID, t.UserID, key)
			granted = err == nil && ok
			allowed[key] = granted
		}
		if granted {
			out = append(out, hit)
		}
	}
	return out
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
