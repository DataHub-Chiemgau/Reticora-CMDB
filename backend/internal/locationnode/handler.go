package locationnode

import (
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Handler provides HTTP handlers for the generalized location hierarchy.
type Handler struct {
	repo Repository
}

// NewHandler creates a new location node handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers location node routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/locations", h.List)
	r.Post("/api/v1/locations", h.Create)
	r.Get("/api/v1/locations/tree", h.Tree)
	r.Get("/api/v1/locations/{id}", h.Get)
	r.Patch("/api/v1/locations/{id}", h.Update)
	r.Delete("/api/v1/locations/{id}", h.Delete)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	filter := FilterParams{
		ParentID: r.URL.Query().Get("parent_id"),
		NodeType: r.URL.Query().Get("node_type"),
		RootOnly: r.URL.Query().Get("root_only") == "true",
		Search:   r.URL.Query().Get("search"),
	}
	items, err := h.repo.List(r.Context(), t.OrganizationID, filter)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"data": items, "total": len(items)})
}

// Tree handles GET /api/v1/locations/tree
func (h *Handler) Tree(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	items, err := h.repo.Tree(r.Context(), t.OrganizationID)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"data": items})
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	item, err := h.repo.GetByID(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "location not found")
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
	var req CreateRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Name == "" || req.NodeType == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name and node_type are required")
		return
	}
	if !NodeTypes[req.NodeType] {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid node_type")
		return
	}
	node := &Node{
		OrganizationID: t.OrganizationID,
		ClientID:       req.ClientID,
		ParentID:       req.ParentID,
		NodeType:       req.NodeType,
		Name:           req.Name,
		SiteID:         req.SiteID,
		BuildingID:     req.BuildingID,
		RoomID:         req.RoomID,
		RackID:         req.RackID,
		Barcode:        req.Barcode,
		Attributes:     req.Attributes,
		SortOrder:      req.SortOrder,
	}
	if err := h.repo.Create(r.Context(), node); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusCreated, node)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req UpdateRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	item, err := h.repo.Update(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), req)
	if err != nil {
		if err.Error() == "not found" {
			api.WriteError(w, http.StatusNotFound, "Not Found", "location not found")
			return
		}
		api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
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
	if err := h.repo.Delete(r.Context(), t.OrganizationID, chi.URLParam(r, "id")); err != nil {
		if err.Error() == "not found" {
			api.WriteError(w, http.StatusNotFound, "Not Found", "location not found")
			return
		}
		api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
