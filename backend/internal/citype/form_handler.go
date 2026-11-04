package citype

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// registerFormRoutes registers the resolved forms (WP-719, MET-50,
// MET-52, API-06). They lie below the core prefixes cis and ci-types and
// need ci:read; a preview or another role needs citype:manage in addition
// (checked here, authz.go maps the path only).
func (h *Handler) registerFormRoutes(r chi.Router) {
	r.Get("/api/v1/cis/{id}/form", h.GetCIForm)
	r.Get("/api/v1/ci-types/{id}/form", h.GetCITypeForm)
}

// formSource returns the form source of the repository or answers 503
// (in-memory mode has no layouts and no rule catalog, like
// attributeStore).
func (h *Handler) formSource(w http.ResponseWriter) (FormSource, bool) {
	source, ok := h.repo.(FormSource)
	if !ok {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "resolved forms need the database")
		return nil, false
	}
	return source, true
}

// FormPolicySubject names the form a field policy is made for (WP-727):
// a stored CI (CIID) or a new CI of a type (CITypeID); PreviewRoleID is
// the role of a preview, whose policy then applies instead of the
// caller's.
type FormPolicySubject struct {
	CIID          string
	CITypeID      string
	PreviewRoleID string
}

// FormPolicyFactory makes the FieldPolicy of a resolved form from the field
// permissions (RBA-07, E-43, WP-727): the policy of the caller of ctx (or
// of the preview role) for the type and scope of the CI. A field it does
// not let read is left out of the layout, one it does not let change is
// read only. citype does not know the field permissions; the server
// builds the factory, and the BFF field ciForm (WP-311) takes the same.
// An error answers the form with 500 (fail-closed); a CI the factory
// does not find is ErrFormCINotFound.
type FormPolicyFactory func(ctx context.Context, orgID string, subject FormPolicySubject) (FieldPolicy, error)

// WithFormPolicy sets the factory of the field policy of the resolved
// forms; without it every field is readable and editable (NoopFieldPolicy).
func (h *Handler) WithFormPolicy(f FormPolicyFactory) *Handler {
	h.formPolicy = f
	return h
}

// formFieldPolicy returns the field policy of the form of req: the
// policy of the preview role with preview and role_id, otherwise the
// caller's. A CI or role that is no UUID gets NoopFieldPolicy: the
// resolution answers 404 before it arranges a field.
func (h *Handler) formFieldPolicy(ctx context.Context, orgID string, req *FormRequest) (FieldPolicy, error) {
	if h.formPolicy == nil || (req.CIID != "" && !api.IsUUID(req.CIID)) ||
		(req.CIID == "" && !api.IsUUID(req.CITypeID)) || (req.Preview && req.RoleID != "" && !api.IsUUID(req.RoleID)) {
		return NoopFieldPolicy{}, nil
	}
	subject := FormPolicySubject{CIID: req.CIID, CITypeID: req.CITypeID}
	if req.Preview {
		subject.PreviewRoleID = req.RoleID
	}
	return h.formPolicy(ctx, orgID, subject)
}

// GetCIForm handles GET /api/v1/cis/{id}/form (?preview=, role_id=,
// template_id=): the resolved form of a CI the caller sees.
func (h *Handler) GetCIForm(w http.ResponseWriter, r *http.Request) {
	h.serveForm(w, r, FormRequest{CIID: chi.URLParam(r, "id")})
}

// GetCITypeForm handles GET /api/v1/ci-types/{id}/form (?template_id=,
// preview=, role_id=, draft=): the form for creating a CI of the type;
// with draft the preview of that metamodel draft (E-113, WP-720), which
// needs citype:manage like preview=true.
func (h *Handler) GetCITypeForm(w http.ResponseWriter, r *http.Request) {
	h.serveForm(w, r, FormRequest{CITypeID: chi.URLParam(r, "id")})
}

// serveForm reads the query, checks the right to preview and resolves.
// preview=true needs citype:manage; so does a role_id that is not a role
// of the caller, which is then resolved as a preview of that role.
func (h *Handler) serveForm(w http.ResponseWriter, r *http.Request, req FormRequest) {
	t, ok := tenantOr401(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	preview := false
	if raw := q.Get("preview"); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			api.WriteFieldError(w, &api.FieldError{Field: "preview", Message: "preview must be true or false"})
			return
		}
		preview = parsed
	}
	req.Preview = preview
	req.RoleID = strings.TrimSpace(q.Get("role_id"))
	req.TemplateID = strings.TrimSpace(q.Get("template_id"))
	p, hasPrincipal := identity.PrincipalFromContext(r.Context())
	manage := hasPrincipal && p.Has(identity.PermCITypeManage)
	if preview && !manage {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "a form preview requires citype:manage")
		return
	}
	// The preview of a draft (E-113, WP-720): the form of a new CI of a
	// type only; GET /api/v1/cis/{id}/form does not read the parameter.
	draftID, withDraft := "", req.CITypeID != "" && q.Has("draft")
	if withDraft {
		if !manage {
			api.WriteError(w, http.StatusForbidden, "Forbidden", "the preview of a draft requires citype:manage")
			return
		}
		if draftID = strings.TrimSpace(q.Get("draft")); !api.IsUUID(draftID) {
			api.WriteFieldError(w, &api.FieldError{Field: "draft", Message: "draft must be the id of a metamodel draft"})
			return
		}
	}
	source, ok := h.formSource(w)
	if !ok {
		return
	}
	// The field policy (WP-727) follows the resolved request: a role
	// other than the caller's is resolved as a preview of that role.
	resolve := func(req FormRequest) (*ResolvedForm, error) {
		policy, perr := h.formFieldPolicy(r.Context(), t.OrganizationID, &req)
		if perr != nil {
			return nil, perr
		}
		resolver := NewFormResolver(source, policy)
		if withDraft {
			drafts, _ := DraftFormsFromContext(r.Context())
			resolver.WithDraft(drafts, draftID)
		}
		return resolver.ResolveForm(r.Context(), t.OrganizationID, req)
	}
	form, err := resolve(req)
	if errors.Is(err, ErrFormRoleNotOwn) {
		if !manage {
			api.WriteError(w, http.StatusForbidden, "Forbidden", "another role than the caller's requires citype:manage")
			return
		}
		req.Preview = true
		form, err = resolve(req)
	}
	if err != nil {
		writeFormError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, form)
}

// writeFormError reports a failed resolution (RFC 7807).
func writeFormError(w http.ResponseWriter, err error) {
	for _, notFound := range []error{ErrFormCINotFound, ErrFormTypeNotFound, ErrFormTemplateNotFound, ErrFormRoleNotFound,
		ErrFormDraftNotFound} {
		if errors.Is(err, notFound) {
			api.WriteError(w, http.StatusNotFound, "Not Found", notFound.Error())
			return
		}
	}
	switch {
	case errors.Is(err, ErrFormDraftFinal):
		api.WriteProblem(w, http.StatusConflict, "urn:reticora:problem:conflict", "Conflict", ErrFormDraftFinal.Error())
	case errors.Is(err, ErrFormDraftsUnavailable):
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", ErrFormDraftsUnavailable.Error())
	default:
		api.WriteRepoError(w, err)
	}
}
