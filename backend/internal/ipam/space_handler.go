package ipam

import (
	"context"
	"net/http"
	"strconv"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/go-chi/chi/v5"
)

// HTTP handlers of usage and free space of a subnet (IPM-03, WP-903):
// GET /api/v1/subnets/{id}/usage, /free-addresses and /free-subnets; all
// need ipam:read.

// SpaceRepository delivers the occupancy of a subnet: all its addresses
// and children, also those outside the caller's scope, once the caller
// sees the subnet (ErrNotFound otherwise).
type SpaceRepository interface {
	SubnetOccupancy(ctx context.Context, orgID, subnetID string) (*Occupancy, error)
}

// SubnetOccupancy implements SpaceRepository on the maps.
func (r *MemoryRepository) SubnetOccupancy(_ context.Context, orgID, subnetID string) (*Occupancy, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.occupancyLocked(orgID, subnetID)
}

// occupancy answers 404 for a subnet the caller cannot see.
func (h *Handler) occupancy(w http.ResponseWriter, r *http.Request, orgID string) (*Occupancy, bool) {
	o, err := h.repo.SubnetOccupancy(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		writeRepoError(w, err, "subnet not found")
		return nil, false
	}
	return o, true
}

// freeDefaultLimit is the number of free addresses or subnets answered
// without limit (IPM-03, WP-903 AK2).
const freeDefaultLimit = 20

// freeLimit reads limit through api.ParseLimit: empty, malformed or not
// positive is the default 20, a larger value is capped at 200. The
// free-space lists are not paged; offset and cursor do not apply.
func freeLimit(r *http.Request) int {
	return api.ParseLimit(r, freeDefaultLimit)
}

// SubnetUsage handles GET /api/v1/subnets/{id}/usage
func (h *Handler) SubnetUsage(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	o, ok := h.occupancy(w, r, orgID)
	if !ok {
		return
	}
	u, err := ComputeUsage(o)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	u.SubnetID = chi.URLParam(r, "id")
	api.WriteJSON(w, http.StatusOK, u)
}

// SubnetFreeAddresses handles GET /api/v1/subnets/{id}/free-addresses
func (h *Handler) SubnetFreeAddresses(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	o, ok := h.occupancy(w, r, orgID)
	if !ok {
		return
	}
	free, err := ComputeFreeAddresses(o, freeLimit(r))
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, FreeAddresses{SubnetID: chi.URLParam(r, "id"), Addresses: free})
}

// SubnetFreeSubnets handles GET /api/v1/subnets/{id}/free-subnets
func (h *Handler) SubnetFreeSubnets(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	prefixLen, err := strconv.Atoi(r.URL.Query().Get("prefix_length"))
	if err != nil {
		api.WriteFieldError(w, errPrefixLength)
		return
	}
	o, ok := h.occupancy(w, r, orgID)
	if !ok {
		return
	}
	free, err := ComputeFreeSubnets(o, prefixLen, freeLimit(r))
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, FreeSubnets{SubnetID: chi.URLParam(r, "id"), PrefixLength: prefixLen, Subnets: free})
}
