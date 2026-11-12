package ipam

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// HTTP handlers of the AS numbers and ranges (IPM-16, WP-1066, WP-1067):
// /api/v1/asns and /api/v1/asn-ranges with the allocation of the next free
// number, the usage of a range and the prefixes of an AS number; reads
// need ipam:read, writes ipam:write (authz, WP-866), the feature ipam gates
// both (WP-865).

// registerASNRoutes registers the routes of asn_handler.go.
func (h *Handler) registerASNRoutes(r chi.Router) {
	r.Get("/api/v1/asns", h.ListASNs)
	r.Post("/api/v1/asns", h.CreateASN)
	r.Get("/api/v1/asns/{id}", h.GetASN)
	r.Patch("/api/v1/asns/{id}", h.UpdateASN)
	r.Delete("/api/v1/asns/{id}", h.DeleteASN)
	r.Get("/api/v1/asn-ranges", h.ListASNRanges)
	r.Post("/api/v1/asn-ranges", h.CreateASNRange)
	r.Get("/api/v1/asn-ranges/{id}", h.GetASNRange)
	r.Patch("/api/v1/asn-ranges/{id}", h.UpdateASNRange)
	r.Delete("/api/v1/asn-ranges/{id}", h.DeleteASNRange)
	// WP-1067: allocation, usage and the prefixes of an AS number.
	r.Post("/api/v1/asn-ranges/{id}/allocate", h.AllocateASN)
	r.Get("/api/v1/asn-ranges/{id}/usage", h.ASNRangeUsage)
	r.Get("/api/v1/asns/{id}/prefixes", h.ListASNPrefixes)
}

// asnRepo returns the repository of the AS numbers, or answers 501 when the
// repository has none (no database).
func (h *Handler) asnRepo(w http.ResponseWriter, r *http.Request) (ASNRepository, string, bool) {
	repo, ok := h.repo.(ASNRepository)
	if !ok {
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "AS numbers are not available")
		return nil, "", false
	}
	orgID, ok := org(w, r)
	return repo, orgID, ok
}

// writeASNError answers a failed AS number request: 404 for an unknown or
// unwritable row, else the field problem or repository error.
func writeASNError(w http.ResponseWriter, err error, what string) {
	if errors.Is(err, ErrNotFound) {
		api.WriteError(w, http.StatusNotFound, "Not Found", what+" not found")
		return
	}
	api.WriteRepoError(w, err)
}

// unprocessable is a 422 problem on field.
func unprocessable(field, message string) *api.FieldError {
	return &api.FieldError{Field: field, Message: message, Status: http.StatusUnprocessableEntity}
}

// checkASNText validates name, kind and rir of a request.
func checkASNText(name, kind, rir *string) error {
	switch {
	case name != nil && strings.TrimSpace(*name) == "":
		return unprocessable("name", "name is required")
	case name != nil && len([]rune(*name)) > 200:
		return unprocessable("name", "name is longer than 200 characters")
	case kind != nil && !slices.Contains(asnKinds, *kind):
		return errASNKind
	case rir != nil && *rir != "" && !slices.Contains(asnRIRs, *rir):
		return errASNRIR
	}
	return nil
}

// ListASNs handles GET /api/v1/asns with the filters kind, client_id,
// range_id and q.
func (h *Handler) ListASNs(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.asnRepo(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := &ASNFilter{Kind: q.Get("kind"), ClientID: q.Get("client_id"), RangeID: q.Get("range_id"), Q: q.Get("q")}
	if f.Kind != "" && !slices.Contains(asnKinds, f.Kind) {
		api.WriteFieldError(w, errASNKind)
		return
	}
	items, total, err := repo.ListASNs(r.Context(), orgID, f, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[ASN]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+len(items) < total,
	})
}

// CreateASN handles POST /api/v1/asns.
func (h *Handler) CreateASN(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.asnRepo(w, r)
	if !ok {
		return
	}
	var req CreateASNRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	number, err := ParseASN(string(req.Number))
	if err != nil {
		api.WriteFieldError(w, errASNNumber)
		return
	}
	name := strings.TrimSpace(req.Name)
	if err := checkASNText(&name, &req.Kind, &req.RIR); err != nil {
		api.WriteFieldError(w, err)
		return
	}
	a := &ASN{OrganizationID: orgID, Number: number, Name: name, Kind: req.Kind, Description: req.Description}
	if req.ClientID != "" {
		a.ClientID = &req.ClientID
	}
	if req.RIR != "" {
		a.RIR = &req.RIR
	}
	if err := repo.CreateASN(r.Context(), a); err != nil {
		writeASNError(w, err, "AS number")
		return
	}
	api.WriteJSON(w, http.StatusCreated, a)
}

// GetASN handles GET /api/v1/asns/{id}.
func (h *Handler) GetASN(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.asnRepo(w, r)
	if !ok {
		return
	}
	a, err := repo.GetASN(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		writeASNError(w, err, "AS number")
		return
	}
	api.WriteJSON(w, http.StatusOK, a)
}

// UpdateASN handles PATCH /api/v1/asns/{id}.
func (h *Handler) UpdateASN(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.asnRepo(w, r)
	if !ok {
		return
	}
	var req UpdateASNRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Number != nil {
		n, err := ParseASN(string(*req.Number))
		if err != nil {
			api.WriteFieldError(w, errASNNumber)
			return
		}
		req.number = n
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		req.Name = &name
	}
	if err := checkASNText(req.Name, req.Kind, req.RIR); err != nil {
		api.WriteFieldError(w, err)
		return
	}
	a, err := repo.UpdateASN(r.Context(), orgID, chi.URLParam(r, "id"), &req)
	if err != nil {
		writeASNError(w, err, "AS number")
		return
	}
	api.WriteJSON(w, http.StatusOK, a)
}

// DeleteASN handles DELETE /api/v1/asns/{id}.
func (h *Handler) DeleteASN(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.asnRepo(w, r)
	if !ok {
		return
	}
	if err := repo.DeleteASN(r.Context(), orgID, chi.URLParam(r, "id")); err != nil {
		writeASNError(w, err, "AS number")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListASNRanges handles GET /api/v1/asn-ranges.
func (h *Handler) ListASNRanges(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.asnRepo(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	items, total, err := repo.ListASNRanges(r.Context(), orgID, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[ASNRange]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+len(items) < total,
	})
}

// rangeBound reads a bound of a range or answers the field error.
func rangeBound(v ASNumber, field *api.FieldError) (int64, error) {
	n, err := ParseASN(string(v))
	if err != nil {
		return 0, field
	}
	return n, nil
}

// CreateASNRange handles POST /api/v1/asn-ranges.
func (h *Handler) CreateASNRange(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.asnRepo(w, r)
	if !ok {
		return
	}
	var req CreateASNRangeRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if err := checkASNText(&name, nil, nil); err != nil {
		api.WriteFieldError(w, err)
		return
	}
	start, err := rangeBound(req.Start, errASNRangeStart)
	if err != nil {
		api.WriteFieldError(w, err)
		return
	}
	end, err := rangeBound(req.End, errASNRangeEnd)
	if err != nil || start > end {
		api.WriteFieldError(w, errASNRangeEnd)
		return
	}
	rg := &ASNRange{OrganizationID: orgID, Name: name, Start: start, End: end, Purpose: req.Purpose}
	if req.ClientID != "" {
		rg.ClientID = &req.ClientID
	}
	if err := repo.CreateASNRange(r.Context(), rg); err != nil {
		writeASNError(w, err, "AS number range")
		return
	}
	api.WriteJSON(w, http.StatusCreated, rg)
}

// GetASNRange handles GET /api/v1/asn-ranges/{id}.
func (h *Handler) GetASNRange(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.asnRepo(w, r)
	if !ok {
		return
	}
	rg, err := repo.GetASNRange(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		writeASNError(w, err, "AS number range")
		return
	}
	api.WriteJSON(w, http.StatusOK, rg)
}

// UpdateASNRange handles PATCH /api/v1/asn-ranges/{id}; a start above the
// end (also against the stored bound) is 422 end.
func (h *Handler) UpdateASNRange(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.asnRepo(w, r)
	if !ok {
		return
	}
	var req UpdateASNRangeRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		req.Name = &name
		if err := checkASNText(req.Name, nil, nil); err != nil {
			api.WriteFieldError(w, err)
			return
		}
	}
	if req.Start != nil {
		n, err := rangeBound(*req.Start, errASNRangeStart)
		if err != nil {
			api.WriteFieldError(w, err)
			return
		}
		req.start = &n
	}
	if req.End != nil {
		n, err := rangeBound(*req.End, errASNRangeEnd)
		if err != nil {
			api.WriteFieldError(w, err)
			return
		}
		req.end = &n
	}
	if req.start != nil && req.end != nil && *req.start > *req.end {
		api.WriteFieldError(w, errASNRangeEnd)
		return
	}
	rg, err := repo.UpdateASNRange(r.Context(), orgID, chi.URLParam(r, "id"), &req)
	if err != nil {
		writeASNError(w, err, "AS number range")
		return
	}
	api.WriteJSON(w, http.StatusOK, rg)
}

// DeleteASNRange handles DELETE /api/v1/asn-ranges/{id}.
func (h *Handler) DeleteASNRange(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.asnRepo(w, r)
	if !ok {
		return
	}
	if err := repo.DeleteASNRange(r.Context(), orgID, chi.URLParam(r, "id")); err != nil {
		writeASNError(w, err, "AS number range")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AllocateASN handles POST /api/v1/asn-ranges/{id}/allocate (WP-1067): the
// AS number gets the lowest free number of the range, atomically; 201 with
// the AS number, 409 range for a full range, 422 client_id for a client
// other than the range's.
func (h *Handler) AllocateASN(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.asnRepo(w, r)
	if !ok {
		return
	}
	var req AllocateASNRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if err := checkASNText(&name, &req.Kind, &req.RIR); err != nil {
		api.WriteFieldError(w, err)
		return
	}
	a := &ASN{OrganizationID: orgID, Name: name, Kind: req.Kind, Description: req.Description}
	if req.ClientID != "" {
		a.ClientID = &req.ClientID
	}
	if req.RIR != "" {
		a.RIR = &req.RIR
	}
	if err := repo.AllocateASN(r.Context(), orgID, chi.URLParam(r, "id"), a); err != nil {
		writeASNError(w, err, "AS number range")
		return
	}
	api.WriteJSON(w, http.StatusCreated, a)
}

// ASNRangeUsage handles GET /api/v1/asn-ranges/{id}/usage (WP-1067).
func (h *Handler) ASNRangeUsage(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.asnRepo(w, r)
	if !ok {
		return
	}
	u, err := repo.ASNRangeUsage(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		writeASNError(w, err, "AS number range")
		return
	}
	api.WriteJSON(w, http.StatusOK, u)
}

// ListASNPrefixes handles GET /api/v1/asns/{id}/prefixes (WP-1067): the
// visible subnets with the AS number as origin AS, paginated.
func (h *Handler) ListASNPrefixes(w http.ResponseWriter, r *http.Request) {
	repo, orgID, ok := h.asnRepo(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	items, total, err := repo.ListASNPrefixes(r.Context(), orgID, chi.URLParam(r, "id"), page)
	if err != nil {
		writeASNError(w, err, "AS number")
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[ASNPrefix]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+len(items) < total,
	})
}
