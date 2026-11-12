package form

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

type Handler struct{ repo Repository }

func NewHandler(repo Repository) *Handler { return &Handler{repo: repo} }
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/forms", h.ListDefinitions)
	r.Post("/api/v1/forms", h.CreateDefinition)
	r.Get("/api/v1/forms/{id}", h.GetDefinition)
	r.Patch("/api/v1/forms/{id}", h.UpdateDefinition)
	r.Delete("/api/v1/forms/{id}", h.DeleteDefinition)
	r.Get("/api/v1/forms/{id}/versions", h.ListVersions)
	r.Get("/api/v1/forms/{id}/versions/{version}", h.GetVersion)
	r.Get("/api/v1/forms/{id}/submissions", h.ListFormSubmissions)
	r.Post("/api/v1/forms/{id}/submissions", h.CreateSubmission)
	r.Get("/api/v1/form-submissions", h.ListSubmissions)
	r.Get("/api/v1/form-submissions/{id}", h.GetSubmission)
}

func tenantInfo(w http.ResponseWriter, r *http.Request) (tenant.TenantInfo, bool) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return tenant.TenantInfo{}, false
	}
	return t, true
}
func activeOnly(r *http.Request) bool { return r.URL.Query().Get("active") == "true" }

// writeFormError answers a failed form request: a definition or submission
// that is unknown, of another organization or outside the caller's scope is
// 404 with a fixed text (TEN-04), a CI or ticket that is no UUID or not
// visible 422 on its field. Database errors go through api.WriteRepoError
// (a client outside the caller's scope: 403); no table, constraint, policy
// or SQLSTATE text reaches the client (API-03, F-047), the log keeps it.
func writeFormError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrNotFound.Error())
	case errors.Is(err, ErrSubmissionNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrSubmissionNotFound.Error())
	case errors.Is(err, ErrVersionNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrVersionNotFound.Error())
	case errors.Is(err, ErrPreconditionFailed):
		api.WritePreconditionFailed(w, ErrPreconditionFailed.Error())
	case errors.Is(err, ErrVersionChanged):
		api.WriteError(w, http.StatusConflict, "Conflict", ErrVersionChanged.Error())
	case errors.As(err, new(ValidationError)):
		var ve ValidationError
		errors.As(err, &ve)
		writeValidationProblem(w, ve)
	case api.WriteFieldError(w, err):
	default:
		slog.WarnContext(r.Context(), "form request failed", "error", err)
		api.WriteRepoError(w, err)
	}
}

// requireUUIDs answers 400 (query) or 422 (body) on the first field whose
// value is set but no UUID, so no malformed id reaches SQL (API-03), and
// reports whether all were valid. fields alternates names and values.
func requireUUIDs(w http.ResponseWriter, status int, fields ...string) bool {
	for i := 0; i+1 < len(fields); i += 2 {
		if fields[i+1] != "" && !api.IsUUID(fields[i+1]) {
			api.WriteFieldError(w, &api.FieldError{Field: fields[i], Message: "must be a UUID", Status: status})
			return false
		}
	}
	return true
}

func (h *Handler) ListDefinitions(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	clientID := r.URL.Query().Get("client_id")
	if !requireUUIDs(w, http.StatusBadRequest, "client_id", clientID) {
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.ListDefinitions(r.Context(), t.OrganizationID, clientID, activeOnly(r), page)
	if err != nil {
		writeFormError(w, r, err)
		return
	}
	api.WriteJSON(w, 200, api.ListResponse[Definition]{Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total})
}
func (h *Handler) GetDefinition(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	item, err := h.repo.GetDefinition(r.Context(), t.OrganizationID, id)
	if err != nil {
		writeFormError(w, r, err)
		return
	}
	w.Header().Set("ETag", api.ETag(item.Version))
	api.WriteJSON(w, 200, item)
}
func (h *Handler) CreateDefinition(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	var req CreateDefinitionRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, 400, "Bad Request", err.Error())
		return
	}
	if req.Name == "" || req.Schema == nil {
		api.WriteError(w, 400, "Bad Request", "name and schema are required")
		return
	}
	active := true
	if req.Active != nil {
		active = *req.Active
	}
	if !requireUUIDs(w, http.StatusUnprocessableEntity, "client_id", req.ClientID) {
		return
	}
	item := &Definition{OrganizationID: t.OrganizationID, ClientID: req.ClientID, Name: req.Name, Description: req.Description, Schema: req.Schema, UIHints: req.UIHints, Active: active}
	if item.UIHints == nil {
		item.UIHints = JSONMap{}
	}
	// A client outside the caller's scope is refused by the policy of
	// form_def (403, alike for a foreign and an unknown client), not with
	// the policy text (F-047).
	if err := h.repo.CreateDefinition(r.Context(), item); err != nil {
		writeFormError(w, r, err)
		return
	}
	api.WriteJSON(w, 201, item)
}
func (h *Handler) UpdateDefinition(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	// If-Match names the version the change is based on (API-07); the
	// repository checks it on the locked row. Without it the change is
	// applied as before.
	version, sent, parseErr := api.ParseIfMatch(r)
	if parseErr != nil {
		api.WritePreconditionFailed(w, parseErr.Error())
		return
	}
	var req UpdateDefinitionRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, 400, "Bad Request", err.Error())
		return
	}
	if req.ClientID != nil && !requireUUIDs(w, http.StatusUnprocessableEntity, "client_id", *req.ClientID) {
		return
	}
	if sent {
		req.IfMatch = &version
	}
	req.CreatedBy = t.UserID
	item, err := h.repo.UpdateDefinition(r.Context(), t.OrganizationID, id, req)
	if err != nil {
		writeFormError(w, r, err)
		return
	}
	w.Header().Set("ETag", api.ETag(item.Version))
	api.WriteJSON(w, 200, item)
}
func (h *Handler) DeleteDefinition(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	if err := h.repo.DeleteDefinition(r.Context(), t.OrganizationID, id); err != nil {
		writeFormError(w, r, err)
		return
	}
	w.WriteHeader(204)
}

// ListVersions handles GET /api/v1/forms/{id}/versions: the schema
// versions of a form, newest first (WFL-01, API-06). A form outside the
// caller's client scope is 404 like an unknown one (TEN-04).
func (h *Handler) ListVersions(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	items, total, err := h.repo.ListVersions(r.Context(), t.OrganizationID, id, page)
	if err != nil {
		writeFormError(w, r, err)
		return
	}
	api.WriteJSON(w, 200, api.ListResponse[DefinitionVersion]{Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+len(items) < total})
}

// GetVersion handles GET /api/v1/forms/{id}/versions/{version}: schema and
// UI hints of one version.
func (h *Handler) GetVersion(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	number, err := strconv.Atoi(chi.URLParam(r, "version"))
	if err != nil || number < 1 {
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrVersionNotFound.Error())
		return
	}
	item, err := h.repo.GetVersion(r.Context(), t.OrganizationID, id, number)
	if err != nil {
		writeFormError(w, r, err)
		return
	}
	api.WriteJSON(w, 200, item)
}

func (h *Handler) ListFormSubmissions(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	formID, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	filter := SubmissionFilter{FormID: formID, Status: r.URL.Query().Get("status")}
	items, total, err := h.repo.ListSubmissions(r.Context(), t.OrganizationID, filter, page)
	if err != nil {
		writeFormError(w, r, err)
		return
	}
	api.WriteJSON(w, 200, api.ListResponse[Submission]{Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total})
}
func (h *Handler) ListSubmissions(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	filter := SubmissionFilter{FormID: r.URL.Query().Get("form_id"), Status: r.URL.Query().Get("status"), TicketID: r.URL.Query().Get("ticket_id"), CIID: r.URL.Query().Get("ci_id")}
	if !requireUUIDs(w, http.StatusBadRequest, "form_id", filter.FormID, "ticket_id", filter.TicketID, "ci_id", filter.CIID) {
		return
	}
	items, total, err := h.repo.ListSubmissions(r.Context(), t.OrganizationID, filter, page)
	if err != nil {
		writeFormError(w, r, err)
		return
	}
	api.WriteJSON(w, 200, api.ListResponse[Submission]{Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total})
}
func (h *Handler) GetSubmission(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrSubmissionNotFound.Error())
	if !ok {
		return
	}
	item, err := h.repo.GetSubmission(r.Context(), t.OrganizationID, id)
	if err != nil {
		writeFormError(w, r, err)
		return
	}
	api.WriteJSON(w, 200, item)
}
func (h *Handler) CreateSubmission(w http.ResponseWriter, r *http.Request) {
	t, ok := tenantInfo(w, r)
	if !ok {
		return
	}
	formID, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	def, err := h.repo.GetDefinition(r.Context(), t.OrganizationID, formID)
	if err != nil {
		writeFormError(w, r, err)
		return
	}
	if !def.Active {
		api.WriteError(w, 400, "Bad Request", "form definition is inactive")
		return
	}
	var req CreateSubmissionRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, 400, "Bad Request", err.Error())
		return
	}
	if req.Values == nil {
		req.Values = JSONMap{}
	}
	if err := Validate(def.Schema, req.Values); err != nil {
		var ve ValidationError
		if errors.As(err, &ve) {
			writeValidationProblem(w, ve)
			return
		}
		api.WriteError(w, 400, "Bad Request", err.Error())
		return
	}
	status := req.Status
	if status == "" {
		status = "submitted"
	}
	if !requireUUIDs(w, http.StatusUnprocessableEntity, "ci_id", req.CIID, "ticket_id", req.TicketID) {
		return
	}
	// The values were validated against the schema of the current version;
	// the repository binds exactly that version or refuses with 409 when
	// the schema changed meanwhile (WFL-01).
	sub := &Submission{OrganizationID: t.OrganizationID, FormID: def.ID, Values: req.Values, SubmittedBy: t.UserID, CIID: req.CIID, TicketID: req.TicketID, Status: status,
		ExpectedVersionID: def.CurrentVersionID}
	// A CI or ticket outside the caller's scope is 422 on its field like an
	// unknown one (TEN-04, F-047).
	if err := h.repo.CreateSubmission(r.Context(), sub); err != nil {
		writeFormError(w, r, err)
		return
	}
	api.WriteJSON(w, 201, sub)
}

func writeValidationProblem(w http.ResponseWriter, ve ValidationError) {
	api.WriteJSON(w, http.StatusBadRequest, struct {
		Type   string       `json:"type"`
		Title  string       `json:"title"`
		Status int          `json:"status"`
		Detail string       `json:"detail"`
		Fields []FieldError `json:"fields"`
	}{Type: "https://reticora.io/problems/validation", Title: "Validation Failed", Status: http.StatusBadRequest, Detail: "submitted values do not match the form schema", Fields: ve.Fields})
}
