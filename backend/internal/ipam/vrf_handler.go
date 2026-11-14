package ipam

import (
	"net/http"
	"slices"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/go-chi/chi/v5"
)

// HTTP handlers of VRFs (TEN-10, NET-05): /api/v1/vrfs; writes need vrf:manage.

// ListVRFs handles GET /api/v1/vrfs
func (h *Handler) ListVRFs(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	// section_id lists the VRFs assigned to a section (IPM-07).
	f := VRFFilter{SectionID: r.URL.Query().Get("section_id")}
	items, total, err := h.repo.ListVRFs(r.Context(), orgID, f, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	if items == nil {
		items = []VRF{}
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[VRF]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total,
	})
}

// CreateVRF handles POST /api/v1/vrfs
func (h *Handler) CreateVRF(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req CreateVRFRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name is required")
		return
	}
	v := &VRF{OrganizationID: orgID, Name: strings.TrimSpace(req.Name), Description: req.Description}
	var err error
	if v.RouteDistinguisher, v.ImportTargets, v.ExportTargets, v.SectionIDs, err = vrfAttributes(&req.RouteDistinguisher,
		&req.ImportTargets, &req.ExportTargets, &req.SectionIDs); err != nil {
		api.WriteFieldError(w, err)
		return
	}
	if err = h.repo.CreateVRF(r.Context(), v); err != nil {
		writeRepoError(w, err, "vrf not found")
		return
	}
	api.WriteJSON(w, http.StatusCreated, v)
}

// GetVRF handles GET /api/v1/vrfs/{id}
func (h *Handler) GetVRF(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	v, err := h.repo.GetVRF(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		writeRepoError(w, err, "vrf not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, v)
}

// UpdateVRF handles PATCH /api/v1/vrfs/{id}
func (h *Handler) UpdateVRF(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	var req UpdateVRFRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name must not be empty")
		return
	}
	if req.RouteDistinguisher != nil || req.ImportTargets != nil || req.ExportTargets != nil || req.SectionIDs != nil {
		rd, imports, exports, sections, err := vrfAttributes(req.RouteDistinguisher, req.ImportTargets, req.ExportTargets, req.SectionIDs)
		if err != nil {
			api.WriteFieldError(w, err)
			return
		}
		if req.RouteDistinguisher != nil {
			req.RouteDistinguisher = &rd
		}
		if req.ImportTargets != nil {
			req.ImportTargets = &imports
		}
		if req.ExportTargets != nil {
			req.ExportTargets = &exports
		}
		if req.SectionIDs != nil {
			req.SectionIDs = &sections
		}
	}
	v, err := h.repo.UpdateVRF(r.Context(), orgID, chi.URLParam(r, "id"), req)
	if err != nil {
		writeRepoError(w, err, "vrf not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, v)
}

// DeleteVRF handles DELETE /api/v1/vrfs/{id}
func (h *Handler) DeleteVRF(w http.ResponseWriter, r *http.Request) {
	orgID, ok := org(w, r)
	if !ok {
		return
	}
	if err := h.repo.DeleteVRF(r.Context(), orgID, chi.URLParam(r, "id")); err != nil {
		writeRepoError(w, err, "vrf not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// vrfAttributes checks and normalizes the routing attributes and sections
// of a VRF write (IPM-07): the route distinguisher in a form of RFC 4364
// ("" clears it), the route target lists and the section ids (UUIDs,
// duplicates dropped). A nil argument stays empty.
func vrfAttributes(rd *string, imports, exports, sections *[]string) (
	outRD string, outImports, outExports, outSections []string, err error,
) {
	if rd != nil && strings.TrimSpace(*rd) != "" {
		canonical, ok := normalizeRouteDistinguisher(*rd)
		if !ok {
			return "", nil, nil, nil, errRouteDistinguisher
		}
		outRD = canonical
	}
	outImports, outExports = []string{}, []string{}
	if imports != nil {
		if outImports, err = normalizeRouteTargets("import_targets", *imports); err != nil {
			return "", nil, nil, nil, err
		}
	}
	if exports != nil {
		if outExports, err = normalizeRouteTargets("export_targets", *exports); err != nil {
			return "", nil, nil, nil, err
		}
	}
	outSections = []string{}
	if sections != nil {
		for _, id := range *sections {
			// The id format belongs to the repository (UUIDs in PostgreSQL,
			// "section-N" in memory); an unknown id is 422 section_ids there.
			id = strings.TrimSpace(id)
			if id == "" {
				return "", nil, nil, nil, errVRFSections
			}
			if !slices.Contains(outSections, id) {
				outSections = append(outSections, id)
			}
		}
	}
	return outRD, outImports, outExports, outSections, nil
}
