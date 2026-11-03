package citype

import (
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

// formResolver returns the resolver over the repository or answers 503
// (in-memory mode has no layouts and no rule catalog, like
// attributeStore).
func (h *Handler) formResolver(w http.ResponseWriter) (*FormResolver, bool) {
	source, ok := h.repo.(FormSource)
	if !ok {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "resolved forms need the database")
		return nil, false
	}
	return NewFormResolver(source, nil), true
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
	resolver, ok := h.formResolver(w)
	if !ok {
		return
	}
	if withDraft {
		drafts, _ := DraftFormsFromContext(r.Context())
		resolver.WithDraft(drafts, draftID)
	}
	form, err := resolver.ResolveForm(r.Context(), t.OrganizationID, req)
	if errors.Is(err, ErrFormRoleNotOwn) {
		if !manage {
			api.WriteError(w, http.StatusForbidden, "Forbidden", "another role than the caller's requires citype:manage")
			return
		}
		req.Preview = true
		form, err = resolver.ResolveForm(r.Context(), t.OrganizationID, req)
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
