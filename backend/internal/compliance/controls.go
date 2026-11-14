package compliance

import (
	"fmt"
	"net/http"
	"slices"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// The control catalog of ISO/IEC 27001:2022, NIS2 and KRITIS and the
// assignment of rules to controls (SEC-04, WP-1128, E-238 option 1): the
// global catalog compliance_control holds the identifiers with short titles
// of our own (no normative text); compliance_rule_control assigns the rules
// of an organization. The report (WP-1129) shows the coverage per control.

// standardParam reads the required query parameter standard (400 when it
// is missing or unknown).
func standardParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	s := r.URL.Query().Get("standard")
	if !slices.Contains(Standards, s) {
		api.WriteFieldError(w, &api.FieldError{Field: "standard", Message: "must be one of iso27001, nis2, kritis"})
		return "", false
	}
	return s, true
}

// ListControls handles GET /api/v1/compliance/controls?standard=….
func (h *Handler) ListControls(w http.ResponseWriter, r *http.Request) {
	h.listControls(w, r, false)
}

// ControlCoverage handles GET /api/v1/compliance/controls/coverage: the
// controls of the standard without an active rule.
func (h *Handler) ControlCoverage(w http.ResponseWriter, r *http.Request) {
	h.listControls(w, r, true)
}

func (h *Handler) listControls(w http.ResponseWriter, r *http.Request, gaps bool) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	standard, ok := standardParam(w, r)
	if !ok {
		return
	}
	items, err := h.repo.ListControls(r.Context(), t.OrganizationID, standard, gaps)
	if err != nil {
		writeComplianceError(w, r, err)
		return
	}
	api.WriteJSON(w, 200, api.CompleteList(items))
}

// PutRuleControls handles PUT /api/v1/compliance/rules/{id}/controls.
func (h *Handler) PutRuleControls(w http.ResponseWriter, r *http.Request) {
	t, ok := org(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	// control_ids is required: a missing or null field (a misnamed key)
	// never clears the assignment, only an explicit [] does.
	var req struct {
		ControlIDs *[]string `json:"control_ids"`
	}
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, 400, "Bad Request", "the body must be {\"control_ids\": [...]}")
		return
	}
	if req.ControlIDs == nil {
		api.WriteFieldError(w, &api.FieldError{Field: "control_ids", Message: "is required (an empty list removes every control)",
			Status: http.StatusUnprocessableEntity})
		return
	}
	controlIDs := *req.ControlIDs
	if len(controlIDs) > maxRuleControls {
		api.WriteFieldError(w, &api.FieldError{Field: "control_ids", Message: fmt.Sprintf("at most %d controls", maxRuleControls),
			Status: http.StatusUnprocessableEntity})
		return
	}
	for i, c := range controlIDs {
		if !api.IsUUID(c) {
			api.WriteFieldError(w, controlFieldError(i))
			return
		}
	}
	if err := h.repo.ReplaceRuleControls(r.Context(), t.OrganizationID, id, controlIDs); err != nil {
		writeComplianceError(w, r, err)
		return
	}
	ids, err := h.repo.RuleControlIDs(r.Context(), t.OrganizationID, id)
	if err != nil {
		writeComplianceError(w, r, err)
		return
	}
	api.WriteJSON(w, 200, RuleControls{RuleID: id, ControlIDs: ids})
}

// controlFieldError is the 422 of the control at index i.
func controlFieldError(i int) *api.FieldError {
	return &api.FieldError{Field: fmt.Sprintf("control_ids[%d]", i), Message: "names no control of the catalog",
		Status: http.StatusUnprocessableEntity}
}
