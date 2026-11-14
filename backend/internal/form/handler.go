package form

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

type Handler struct {
	repo Repository
	// quota admits a form definition (max_form_definitions, WithQuota,
	// WFL-08); nil admits all.
	quota DefinitionQuota
	// docs reads the documents of signature fields (WithDocuments,
	// WP-1101); nil without the module documents.
	docs DocumentReader
	// hook connects submissions with workflow runs (WithSubmissionHook,
	// WP-1102); nil while the module workflows is off.
	hook SubmissionHook
}

// SubmissionHook connects the submissions of a form with workflow runs
// (WFL-01, WFL-02, WP-1102); the router wires the workflow executor. Both
// calls run in the transaction of the submission (the context carries it):
// a refused or rolled-back submission leaves no run and continues none.
type SubmissionHook interface {
	// PrepareResume locks the run runID and its step that waits for form
	// formID (FOR UPDATE) and checks that userID is its recipient. It
	// returns the values the form was requested with (readonly_when
	// compares against them). A run that is unknown, of another
	// organization, outside the scope or not waiting for this form is
	// ErrRunNotWaiting, a caller who is not the recipient
	// ErrNotFormRecipient.
	PrepareResume(ctx context.Context, orgID, runID, formID, userID string) (prefill JSONMap, err error)
	// Submitted is called once the submission is stored: it starts the
	// runs of the active definitions with the trigger form_submitted of
	// the form and completes the waiting step of sub.RunID. after, if not
	// nil, executes or queues the runs once the transaction committed.
	Submitted(ctx context.Context, sub *Submission) (after func(ctx context.Context), err error)
}

// TxRunner runs fn in one transaction of the context's tenant scope: the
// repository calls fn makes with its context join it. The PG repository
// implements it.
type TxRunner interface {
	RunInTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error
}

// WithSubmissionHook makes POST /forms/{id}/submissions start the runs of
// form_submitted definitions and continue the run a submission names
// (run_id) in the transaction of the submission (WP-1102).
func (h *Handler) WithSubmissionHook(hook SubmissionHook) *Handler {
	h.hook = hook
	return h
}

// inTx runs fn in one transaction when the repository offers one.
func (h *Handler) inTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error {
	if tx, ok := h.repo.(TxRunner); ok {
		return tx.RunInTx(ctx, orgID, fn)
	}
	return fn(ctx)
}

// DefinitionQuota admits one more form definition of the organization
// (WFL-08, WP-1112).
type DefinitionQuota interface {
	AllowFormDefinition(ctx context.Context, orgID string) error
}

func NewHandler(repo Repository) *Handler { return &Handler{repo: repo} }

// WithQuota checks max_form_definitions on POST /api/v1/forms: above the
// limit 403 entitlement-limit.
func (h *Handler) WithQuota(q DefinitionQuota) *Handler {
	h.quota = q
	return h
}

// WithDocuments lets signature fields name documents (WFL-01, WP-1101);
// the router sets it with the module documents. Without it a definition
// with a signature field is refused with 422.
func (h *Handler) WithDocuments(docs DocumentReader) *Handler {
	h.docs = docs
	return h
}

// validateDefinition checks the conditional logic of hints and the
// signature fields of schema of a definition that is written (WP-1101
// AK1, AK5); a refusal is answered with 422 on its field.
func (h *Handler) validateDefinition(w http.ResponseWriter, schema, hints JSONMap) bool {
	if err := ValidateHints(schema, hints); err != nil {
		api.WriteFieldError(w, err)
		return false
	}
	if err := ValidateSignatureFields(schema, h.docs); err != nil {
		api.WriteFieldError(w, err)
		return false
	}
	return true
}

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
	case errors.Is(err, ErrRunNotWaiting):
		api.WriteProblem(w, http.StatusConflict, ProblemRunNotWaiting, "Conflict", ErrRunNotWaiting.Error())
	case errors.Is(err, ErrNotFormRecipient):
		api.WriteError(w, http.StatusForbidden, "Forbidden", ErrNotFormRecipient.Error())
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
	if !h.validateDefinition(w, req.Schema, req.UIHints) {
		return
	}
	if h.quota != nil {
		if err := h.quota.AllowFormDefinition(r.Context(), t.OrganizationID); err != nil {
			if !api.WriteEntitlementError(w, err) {
				writeFormError(w, r, err)
			}
			return
		}
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
	// repository checks it on the locked row. Without it a change of name,
	// description, client or active is applied as before; a change of
	// schema or hints is bound to the version it was checked against.
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
	// A change of schema or hints is checked as the version it creates:
	// the new part with the unchanged other part (WP-1101 AK1).
	if req.Schema != nil || req.UIHints != nil {
		current, gerr := h.repo.GetDefinition(r.Context(), t.OrganizationID, id)
		if gerr != nil {
			writeFormError(w, r, gerr)
			return
		}
		schema, hints := current.Schema, current.UIHints
		if req.Schema != nil {
			schema = req.Schema
		}
		if req.UIHints != nil {
			hints = req.UIHints
		}
		if !h.validateDefinition(w, schema, hints) {
			return
		}
		// The check holds for the version it read: without If-Match the
		// change is bound to it, so a concurrent change of schema or hints
		// is 412 instead of an unchecked combination of both (AK1).
		if req.IfMatch == nil {
			v := current.Version
			req.IfMatch = &v
		}
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
	if rerr := api.ReadJSON(r, &req); rerr != nil {
		api.WriteError(w, 400, "Bad Request", rerr.Error())
		return
	}
	if req.Values == nil {
		req.Values = JSONMap{}
	}
	status := req.Status
	if status == "" {
		status = "submitted"
	}
	if !requireUUIDs(w, http.StatusUnprocessableEntity, "ci_id", req.CIID, "ticket_id", req.TicketID, "run_id", req.RunID) {
		return
	}
	if req.RunID != "" && h.hook == nil {
		// Without the module workflows no run waits for a form.
		writeFormError(w, r, ErrRunNotWaiting)
		return
	}
	// The values were validated against the schema of the current version;
	// the repository binds exactly that version or refuses with 409 when
	// the schema changed meanwhile (WFL-01).
	sub := &Submission{OrganizationID: t.OrganizationID, FormID: def.ID, SubmittedBy: t.UserID, CIID: req.CIID, TicketID: req.TicketID, Status: status,
		RunID: req.RunID, ExpectedVersionID: def.CurrentVersionID}
	// The submission, the runs it starts and the run it continues are
	// stored in one transaction (WP-1102): a refused submission leaves no
	// run, and the waiting step stays locked until the commit.
	var after func(ctx context.Context)
	err = h.inTx(r.Context(), t.OrganizationID, func(ctx context.Context) error {
		var prefill JSONMap
		if req.RunID != "" {
			var perr error
			if prefill, perr = h.hook.PrepareResume(ctx, t.OrganizationID, req.RunID, def.ID, t.UserID); perr != nil {
				return perr
			}
		}
		// Schema and conditional logic of the current version, which the
		// repository binds (WP-1101 AK2, AK3): hidden fields are dropped, a
		// signature names a readable PNG or JPEG document, a readonly_when
		// field keeps the value the form was requested with.
		values, verr := ValidateSubmission(ctx, &SubmissionCheck{OrganizationID: t.OrganizationID, Schema: def.Schema, UIHints: def.UIHints,
			Values: req.Values, Prefill: prefill, Documents: h.docs})
		if verr != nil {
			return verr
		}
		sub.Values = values
		// A CI or ticket outside the caller's scope is 422 on its field like
		// an unknown one (TEN-04, F-047).
		if cerr := h.repo.CreateSubmission(ctx, sub); cerr != nil {
			return cerr
		}
		if h.hook == nil {
			return nil
		}
		var herr error
		after, herr = h.hook.Submitted(ctx, sub)
		return herr
	})
	if err != nil {
		writeFormError(w, r, err)
		return
	}
	if after != nil {
		// The committed runs execute even when the client goes away.
		after(context.WithoutCancel(r.Context()))
	}
	api.WriteJSON(w, 201, sub)
}

// ProblemRunNotWaiting is the problem type of a submission to a run that
// waits for no input of the form (409, WP-1102 AK4).
const ProblemRunNotWaiting = "https://reticora.io/problems/run-not-waiting"

func writeValidationProblem(w http.ResponseWriter, ve ValidationError) {
	api.WriteJSON(w, http.StatusBadRequest, struct {
		Type   string       `json:"type"`
		Title  string       `json:"title"`
		Status int          `json:"status"`
		Detail string       `json:"detail"`
		Fields []FieldError `json:"fields"`
	}{Type: "https://reticora.io/problems/validation", Title: "Validation Failed", Status: http.StatusBadRequest, Detail: "submitted values do not match the form schema", Fields: ve.Fields})
}
