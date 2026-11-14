package ipam

import (
	"errors"
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/go-chi/chi/v5"
)

// HTTP handlers of the IPAM history (IPM-08, WP-908): GET
// /api/v1/subnets/{id}/history and /api/v1/ips/{id}/history (also under
// /ip-addresses), ipam:read, paged, the newest first; 501 without
// PostgreSQL.

// listHistory answers the history of the object kind of the path's id.
func (h *Handler) listHistory(w http.ResponseWriter, r *http.Request, kind, notFound string) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	repo, ok := h.repo.(HistoryRepository)
	if !ok {
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "the IPAM history is not available")
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	items, total, err := repo.ListChanges(r.Context(), orgID, kind, chi.URLParam(r, "id"), page)
	if errors.Is(err, ErrNotFound) {
		api.WriteError(w, http.StatusNotFound, "Not Found", notFound)
		return
	}
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	if items == nil {
		items = []Change{}
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Change]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total,
	})
}

// SubnetHistory handles GET /api/v1/subnets/{id}/history
func (h *Handler) SubnetHistory(w http.ResponseWriter, r *http.Request) {
	h.listHistory(w, r, ChangeKindSubnet, "subnet not found")
}

// IPAddressHistory handles GET /api/v1/ips/{id}/history
func (h *Handler) IPAddressHistory(w http.ResponseWriter, r *http.Request) {
	h.listHistory(w, r, ChangeKindIPAddress, "ip address not found")
}
