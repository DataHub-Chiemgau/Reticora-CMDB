package ipam

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// HTTP handlers of the subnet scan (IPM-11, WP-1062):
//
//	GET   /api/v1/subnets/{id}/scan  configuration, collector group members, target numbers
//	PATCH /api/v1/subnets/{id}/scan  change the configuration (ipam:write)
//
// The feature rule /api/v1/subnets + /scan needs ipam and monitoring
// (WP-865). Without PostgreSQL and the probe engine both answer 501.

// WithScanProbes attaches the managed probes of the subnet scan
// (probe.ScanProbes), also to a repository that synchronizes the scan on
// its own writes (the delete of a subnet, a new client or CIDR).
func (h *Handler) WithScanProbes(p ScanProbes) *Handler {
	h.scanProbes = p
	if s, ok := h.repo.(interface{ SetScanProbes(ScanProbes) }); ok && p != nil {
		s.SetScanProbes(p)
	}
	return h
}

// registerScanRoutes registers the routes of scan_handler.go.
func (h *Handler) registerScanRoutes(r chi.Router) {
	r.Get("/api/v1/subnets/{id}/scan", h.GetSubnetScan)
	r.Patch("/api/v1/subnets/{id}/scan", h.UpdateSubnetScan)
}

// scanStore returns the scan store; 501 without PostgreSQL or probe engine.
func (h *Handler) scanStore(w http.ResponseWriter, r *http.Request) (ScanStore, string, bool) {
	orgID, ok := org(w, r)
	if !ok {
		return nil, "", false
	}
	s, ok := h.repo.(ScanStore)
	if !ok || h.scanProbes == nil {
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "the subnet scan needs PostgreSQL")
		return nil, "", false
	}
	return s, orgID, true
}

func writeScanError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "subnet not found")
	case api.WriteEntitlementError(w, err):
	case api.WriteFieldError(w, err):
	default:
		api.WriteRepoError(w, err)
	}
}

// GetSubnetScan handles GET /api/v1/subnets/{id}/scan.
func (h *Handler) GetSubnetScan(w http.ResponseWriter, r *http.Request) {
	s, orgID, ok := h.scanStore(w, r)
	if !ok {
		return
	}
	scan, err := s.GetSubnetScan(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		writeScanError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, scan)
}

// UpdateSubnetScan handles PATCH /api/v1/subnets/{id}/scan.
func (h *Handler) UpdateSubnetScan(w http.ResponseWriter, r *http.Request) {
	s, orgID, ok := h.scanStore(w, r)
	if !ok {
		return
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body == nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid JSON body")
		return
	}
	u, err := ParseScanUpdate(body)
	if err != nil {
		writeScanError(w, err)
		return
	}
	scan, err := s.UpdateSubnetScan(r.Context(), orgID, chi.URLParam(r, "id"), u, h.scanProbes)
	if err != nil {
		writeScanError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, scan)
}
