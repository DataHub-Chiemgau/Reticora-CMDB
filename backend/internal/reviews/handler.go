package reviews

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
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
	// Tickets from items (WP-291); no alias under /discovery.
	r.Post("/api/v1/review-items/{id}/ticket", h.Ticket)
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

// List handles GET /api/v1/review-items and its deprecated alias. A status
// or kind outside the documented enums is 400 with the field (API-01,
// F-426), as rel_type of /relationships, not an empty list.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	q := r.URL.Query()
	filter := Filter{Status: q.Get("status"), Kind: q.Get("kind")}
	if filter.Status != "" && !slices.Contains(Statuses, filter.Status) {
		api.WriteFieldError(w, &api.FieldError{Field: "status", Message: "must be one of " + strings.Join(Statuses, ", ")})
		return
	}
	if filter.Kind != "" && !slices.Contains(Kinds, filter.Kind) {
		api.WriteFieldError(w, &api.FieldError{Field: "kind", Message: "must be a review type: " + strings.Join(Kinds, ", ")})
		return
	}
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
	if err == nil {
		// One query for the tickets of the page (WP-291).
		err = h.svc.attachTickets(r.Context(), t.OrganizationID, items)
	}
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
	one := []Item{*item}
	if err = h.svc.attachTickets(r.Context(), t.OrganizationID, one); err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, &one[0])
}

// Ticket handles POST /api/v1/review-items/{id}/ticket (WP-291): the route
// requires ticket:write, the handler also review:resolve (403). The body
// is optional.
func (h *Handler) Ticket(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	// An item outside the caller's scope is not found before the
	// permission is checked (404 before 403, like mayControl of jobs).
	if _, err := h.svc.Get(r.Context(), t.OrganizationID, chi.URLParam(r, "id")); err != nil {
		writeError(w, err)
		return
	}
	if p, ok := identity.PrincipalFromContext(r.Context()); !ok || !p.Has(identity.PermReviewResolve) {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "opening a ticket from a review item requires "+string(identity.PermReviewResolve))
		return
	}
	var body TicketRequest
	if err := api.ReadJSON(r, &body); err != nil && !errors.Is(err, io.EOF) {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	created, err := h.svc.CreateTicket(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), &body)
	if err != nil {
		writeError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, created)
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
		// A refusal of the effect as the resource it writes reports it
		// (e.g. the scope of a scope_suggested item: 409 on target, 422 on
		// collector_id, as POST /discovery-scopes answers, F-405).
		if api.WriteFieldError(w, err) {
			return
		}
		var typed api.TypedProblem
		if errors.As(err, &typed) && typed.ProblemType() != "" {
			api.WriteTypedProblem(w, http.StatusForbidden, err)
			return
		}
		api.WriteRepoError(w, err)
	}
}
