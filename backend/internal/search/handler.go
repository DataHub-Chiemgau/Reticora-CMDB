package search

import (
	"context"
	"log/slog"
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
		api.WriteRepoError(w, err)
		return
	}
	filtered, err := h.filterAllowed(r.Context(), t, res.Data)
	if err != nil {
		// A failing permission lookup must be visible, never silently
		// return an empty result set (audit finding H3b).
		slog.Error("search permission filter failed",
			"org_id", t.OrganizationID, "user_id", t.UserID, "error", err)
		api.WriteError(w, http.StatusInternalServerError, "Internal Error",
			"could not resolve search permissions")
		return
	}
	res.Data = filtered
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
		slog.Error("search reindex failed", "org_id", t.OrganizationID, "error", err)
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusAccepted, out)
}

// filterAllowed removes hits the user is not permitted to see. Permission
// lookups are cached per key; a lookup error is returned to the caller so it
// can surface the failure instead of silently dropping every hit.
func (h *Handler) filterAllowed(ctx context.Context, t tenant.TenantInfo, hits []Hit) ([]Hit, error) {
	if h.permissions == nil || t.UserID == "" {
		return hits, nil
	}
	allowed := map[string]bool{}
	out := hits[:0]
	for _, hit := range hits {
		key := permissionFor(hit.EntityType)
		if key == "" {
			continue
		}
		ok, cached := allowed[key]
		if !cached {
			var err error
			ok, err = h.permissions.HasPermission(ctx, t.OrganizationID, t.UserID, key)
			if err != nil {
				return nil, err
			}
			allowed[key] = ok
		}
		if ok {
			out = append(out, hit)
		}
	}
	return out, nil
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
	case "location", "reservation":
		// Locations and reservations are read through the asset scope
		// (see server/authz.go route mapping).
		return "asset:read"
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
