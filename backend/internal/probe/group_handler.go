package probe

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The routes of the monitoring groups (device groups of PRB-01 and CRD-04,
// E-178, WP-946); the rules are described in profile_handler.go.

// checkGroupName checks the name of a monitoring group.
func checkGroupName(name string) error {
	if strings.TrimSpace(name) == "" || len(name) > maxGroupName {
		return unprocessable("name", "name must not be blank and at most 200 characters")
	}
	return nil
}

func (h *Handler) listGroups(w http.ResponseWriter, r *http.Request) {
	ps, ok := h.profiles(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	client := r.URL.Query().Get("client_id")
	if client != "" && !api.IsUUID(client) {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "client_id must be a UUID")
		return
	}
	items, total, err := ps.ListGroups(r.Context(), client, page.Limit, page.Offset)
	if err != nil {
		writeMonitoringError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[MonitoringGroup]{Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+len(items) < total})
}

func (h *Handler) getGroup(w http.ResponseWriter, r *http.Request) {
	ps, ok := h.profiles(w, r)
	if !ok {
		return
	}
	g, err := ps.GetGroup(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeMonitoringError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, g)
}

func (h *Handler) createGroup(w http.ResponseWriter, r *http.Request) {
	ps, ok := h.profiles(w, r)
	if !ok {
		return
	}
	var req struct {
		Name     string `json:"name"`
		ClientID string `json:"client_id"`
		Priority int    `json:"priority"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid request body")
		return
	}
	if err := checkGroupName(req.Name); err != nil {
		writeMonitoringError(w, err)
		return
	}
	if req.ClientID != "" && !api.IsUUID(req.ClientID) {
		writeMonitoringError(w, unprocessable("client_id", "client not found"))
		return
	}
	g := &MonitoringGroup{OrganizationID: tenant.FromContext(r.Context()).OrganizationID, ClientID: req.ClientID,
		Name: strings.TrimSpace(req.Name), Priority: req.Priority}
	created, err := ps.CreateGroup(r.Context(), g)
	if err != nil {
		writeMonitoringError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, created)
}

func (h *Handler) updateGroup(w http.ResponseWriter, r *http.Request) {
	ps, ok := h.profiles(w, r)
	if !ok {
		return
	}
	var req struct {
		Name     *string `json:"name"`
		Priority *int    `json:"priority"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid request body; only name and priority can be changed")
		return
	}
	if req.Name == nil && req.Priority == nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name at least one field to change")
		return
	}
	if req.Name != nil {
		if err := checkGroupName(*req.Name); err != nil {
			writeMonitoringError(w, err)
			return
		}
		trimmed := strings.TrimSpace(*req.Name)
		req.Name = &trimmed
	}
	g, err := ps.UpdateGroup(r.Context(), chi.URLParam(r, "id"), &GroupUpdate{Name: req.Name, Priority: req.Priority})
	if err != nil {
		writeMonitoringError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, g)
}

func (h *Handler) deleteGroup(w http.ResponseWriter, r *http.Request) {
	ps, ok := h.profiles(w, r)
	if !ok {
		return
	}
	n, err := ps.DeleteGroup(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeMonitoringError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]int{"deleted_profiles": n})
}

func (h *Handler) listGroupMembers(w http.ResponseWriter, r *http.Request) {
	ps, ok := h.profiles(w, r)
	if !ok {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	items, total, err := ps.ListGroupMembers(r.Context(), chi.URLParam(r, "id"), page.Limit, page.Offset)
	if err != nil {
		writeMonitoringError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[MonitoringGroupMember]{Data: items, Total: total, Limit: page.Limit,
		Offset: page.Offset, HasMore: page.Offset+len(items) < total})
}

func (h *Handler) addGroupMembers(w http.ResponseWriter, r *http.Request) {
	ps, ok := h.profiles(w, r)
	if !ok {
		return
	}
	var req struct {
		CIIDs []string `json:"ci_ids"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid request body")
		return
	}
	switch {
	case len(req.CIIDs) == 0:
		writeMonitoringError(w, unprocessable("ci_ids", "ci_ids must name at least one CI"))
		return
	case len(req.CIIDs) > MaxGroupMembersPerRequest:
		writeMonitoringError(w, unprocessable("ci_ids", "ci_ids must name at most 500 CIs"))
		return
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(req.CIIDs))
	for _, id := range req.CIIDs {
		if !api.IsUUID(id) {
			writeMonitoringError(w, ErrCINotFound)
			return
		}
		if id = strings.ToLower(id); !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	members, err := ps.AddGroupMembers(r.Context(), chi.URLParam(r, "id"), ids)
	if err != nil {
		writeMonitoringError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string][]MonitoringGroupMember{"data": members})
}

func (h *Handler) removeGroupMember(w http.ResponseWriter, r *http.Request) {
	ps, ok := h.profiles(w, r)
	if !ok {
		return
	}
	if err := ps.RemoveGroupMember(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "ci_id")); err != nil {
		writeMonitoringError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
