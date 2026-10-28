package reviews

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// legacyDeprecation is the Deprecation header (RFC 9745) of the
// /api/v1/discovery/review-items paths, replaced by review-items (API-05,
// E-25) on 2026-10-12.
const legacyDeprecation = "@1791763200"

// Handler serves the review-items resource.
type Handler struct {
	svc *Service
}

// NewHandler returns the handler of svc.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes registers /api/v1/review-items and the deprecated
// /api/v1/discovery/review-items aliases.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/review-items", h.List)
	r.Get("/api/v1/review-items/{id}", h.Get)
	r.Post("/api/v1/review-items/{id}/resolve", h.Resolve)
	r.Get("/api/v1/discovery/review-items", deprecated("/api/v1/review-items", h.List))
	r.Post("/api/v1/discovery/review-items/{id}/resolve", deprecated("/api/v1/review-items/{id}/resolve", h.Resolve))
}

// deprecated marks a legacy path; the successor link names the item of the
// request, not the template (F-219).
func deprecated(successor string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Deprecation", legacyDeprecation)
		link := successor
		if id, err := url.PathUnescape(chi.URLParam(r, "id")); err == nil && id != "" {
			link = strings.ReplaceAll(link, "{id}", url.PathEscape(id))
		}
		w.Header().Set("Link", "<"+link+`>; rel="successor-version"`)
		next(w, r)
	}
}

// List handles GET /api/v1/review-items.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	q := r.URL.Query()
	filter := Filter{Status: q.Get("status"), Kind: q.Get("kind")}
	if v := q.Get("escalated"); v != "" {
		escalated, err := strconv.ParseBool(v)
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "escalated must be true or false")
			return
		}
		filter.Escalated = escalated
	}
	page := api.ParsePagination(r)
	items, total, err := h.svc.List(r.Context(), t.OrganizationID, filter, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Item]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total,
	})
}

// Get handles GET /api/v1/review-items/{id}.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	item, err := h.svc.Get(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

// Resolve handles POST /api/v1/review-items/{id}/resolve (review:resolve).
func (h *Handler) Resolve(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var body ResolveRequest
	if err := api.ReadJSON(r, &body); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	// The resolver is always the principal of the request (F-091).
	res := Resolution{Action: body.Action, CIID: body.CIID, CITypeID: body.CITypeID, Note: body.Note, ResolvedBy: t.UserID}
	item, err := h.svc.Resolve(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), &res)
	if err != nil {
		writeError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

func writeError(w http.ResponseWriter, err error) {
	var refused *Error
	switch {
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", err.Error())
	case errors.Is(err, ErrAlreadyResolved):
		api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
	case errors.As(err, &refused) && refused.Field != "":
		api.WriteFieldError(w, &api.FieldError{Field: refused.Field, Message: refused.Message, Status: refused.Status})
	case errors.As(err, &refused) && refused.Problem != "":
		api.WriteProblem(w, refused.Status, refused.Problem, http.StatusText(refused.Status), refused.Message)
	case errors.As(err, &refused):
		api.WriteError(w, refused.Status, http.StatusText(refused.Status), refused.Message)
	default:
		var typed api.TypedProblem
		if errors.As(err, &typed) && typed.ProblemType() != "" {
			api.WriteTypedProblem(w, http.StatusForbidden, err)
			return
		}
		api.WriteRepoError(w, err)
	}
}
