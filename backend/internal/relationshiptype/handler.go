package relationshiptype

import (
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler provides HTTP handlers for relationship type metadata.
type Handler struct {
	repo Repository
}

// NewHandler creates a new relationship type handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers relationship type routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/relationship-types", h.List)
	r.Post("/api/v1/relationship-types", h.Create)
	r.Get("/api/v1/relationship-types/{key}", h.Get)
	r.Patch("/api/v1/relationship-types/{key}", h.Update)
	r.Delete("/api/v1/relationship-types/{key}", h.Delete)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.List(r.Context(), t.OrganizationID, page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Type]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	item, err := h.repo.GetByKey(r.Context(), t.OrganizationID, chi.URLParam(r, "key"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "relationship type not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req UpsertRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Key == "" || req.ForwardLabel == "" || req.ReverseLabel == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "key, forward_label and reverse_label are required")
		return
	}
	impact := true
	if req.ImpactParticipation != nil {
		impact = *req.ImpactParticipation
	}
	typ := &Type{
		OrganizationID:      t.OrganizationID,
		Key:                 req.Key,
		ForwardLabel:        req.ForwardLabel,
		ReverseLabel:        req.ReverseLabel,
		SourceCITypes:       req.SourceCITypes,
		TargetCITypes:       req.TargetCITypes,
		Direction:           req.Direction,
		Cardinality:         req.Cardinality,
		Category:            req.Category,
		ImpactParticipation: impact,
		Description:         req.Description,
	}
	if err := h.repo.Create(r.Context(), typ); err != nil {
		api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusCreated, typ)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req UpsertRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	item, err := h.repo.Update(r.Context(), t.OrganizationID, chi.URLParam(r, "key"), req)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "relationship type not found or protected")
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if err := h.repo.Delete(r.Context(), t.OrganizationID, chi.URLParam(r, "key")); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "relationship type not found or protected")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
