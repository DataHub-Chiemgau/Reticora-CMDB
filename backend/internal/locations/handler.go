package locations

import (
	"errors"
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/httpx"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler serves /api/v1/locations on the canonical location tree (LOC-10).
type Handler struct {
	repo Repository
}

// NewHandler creates the location tree handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers the location routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/locations", h.List)
	r.Post("/api/v1/locations", h.Create)
	r.Get("/api/v1/locations/tree", h.Tree)
	r.Get("/api/v1/locations/{id}", h.Get)
	r.Patch("/api/v1/locations/{id}", h.Update)
	r.Delete("/api/v1/locations/{id}", h.Delete)
}

// List handles GET /api/v1/locations.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organization(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	filter := Filter{
		ParentID: q.Get("parent_id"),
		Kind:     Kind(q.Get("kind")),
		RootOnly: q.Get("root_only") == "true",
		Search:   q.Get("search"),
	}
	if filter.Kind != "" && !filter.Kind.Valid() {
		writeFieldProblem(w, r, &FieldError{Field: "kind", Message: "unknown kind " + string(filter.Kind), Err: ErrInvalidKind})
		return
	}
	items, err := h.repo.List(r.Context(), orgID, filter)
	if err != nil {
		writeError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"data": items, "total": len(items)})
}

// Tree handles GET /api/v1/locations/tree: the visible nodes as a forest.
func (h *Handler) Tree(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organization(w, r)
	if !ok {
		return
	}
	items, err := h.repo.List(r.Context(), orgID, Filter{})
	if err != nil {
		writeError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"data": BuildTree(items)})
}

// Get handles GET /api/v1/locations/{id}.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organization(w, r)
	if !ok {
		return
	}
	item, err := h.repo.Get(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

// Create handles POST /api/v1/locations.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organization(w, r)
	if !ok {
		return
	}
	var req CreateRequest
	if err := api.ReadJSON(r, &req); err != nil {
		httpx.ValidationError(w, r, "invalid request body: "+err.Error())
		return
	}
	item, err := h.repo.Create(r.Context(), orgID, req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, item)
}

// Update handles PATCH /api/v1/locations/{id}: rename and/or move.
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organization(w, r)
	if !ok {
		return
	}
	var req UpdateRequest
	if err := api.ReadJSON(r, &req); err != nil {
		httpx.ValidationError(w, r, "invalid request body: "+err.Error())
		return
	}
	item, err := h.repo.Update(r.Context(), orgID, chi.URLParam(r, "id"), req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

// Delete handles DELETE /api/v1/locations/{id}.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	orgID, ok := organization(w, r)
	if !ok {
		return
	}
	if err := h.repo.Delete(r.Context(), orgID, chi.URLParam(r, "id")); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func organization(w http.ResponseWriter, r *http.Request) (string, bool) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		httpx.Unauthorized(w, r, "missing tenant context")
		return "", false
	}
	return t.OrganizationID, true
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	var fe *FieldError
	switch {
	case errors.As(err, &fe):
		writeFieldProblem(w, r, fe)
	case WriteDependencyConflict(w, r, err):
	case errors.Is(err, ErrNotFound):
		httpx.NotFound(w, r, "location not found")
	case errors.Is(err, ErrInvalidKind):
		writeFieldProblem(w, r, &FieldError{Field: "kind", Message: err.Error(), Err: err})
	case errors.Is(err, ErrInvalidParent), errors.Is(err, ErrCycle):
		writeFieldProblem(w, r, &FieldError{Field: "parent_id", Message: err.Error(), Err: err})
	case errors.Is(err, ErrInvalidInput):
		httpx.ValidationError(w, r, err.Error())
	default:
		api.WriteRepoError(w, err)
	}
}

// violationProblem is the ViolationProblem of the API: an RFC 7807 problem
// with the offending fields, as the CI handler writes it and the frontend
// maps onto form inputs.
type violationProblem struct {
	httpx.ProblemDetail
	Violations []violation `json:"violations"`
}

type violation struct {
	Field  string `json:"field"`
	Detail string `json:"detail"`
}

// writeFieldProblem writes a 422 problem naming the field; a cycle is
// reported the same way, as its field is parent_id.
func writeFieldProblem(w http.ResponseWriter, r *http.Request, fe *FieldError) {
	respondProblem(w, http.StatusUnprocessableEntity, violationProblem{
		ProblemDetail: httpx.ProblemDetail{
			Type:     httpx.TypeValidationError,
			Title:    "Validation Error",
			Status:   http.StatusUnprocessableEntity,
			Detail:   fe.Error(),
			Instance: r.URL.Path,
			TraceID:  r.Header.Get("X-Request-ID"),
		},
		Violations: []violation{{Field: fe.Field, Detail: fe.Message}},
	})
}
