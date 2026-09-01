package disposal

import (
	"net/http"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

var validMethods = map[string]bool{
	"reuse": true, "recycling": true, "destruction": true,
	"secure_erasure": true, "physical_destruction": true, "return_to_vendor": true,
}

// Handler provides HTTP handlers for disposal records (append-only).
type Handler struct {
	repo Repository
}

// NewHandler creates a new disposal handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes registers disposal routes. There are deliberately no
// PATCH/DELETE routes: disposal records are revision-safe (append-only).
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/disposal-records", h.List)
	r.Post("/api/v1/disposal-records", h.Create)
	r.Get("/api/v1/disposal-records/{id}", h.Get)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page := api.ParsePagination(r)
	filter := FilterParams{
		Method:  r.URL.Query().Get("method"),
		AssetID: r.URL.Query().Get("asset_id"),
		CIID:    r.URL.Query().Get("ci_id"),
	}
	items, total, err := h.repo.List(r.Context(), t.OrganizationID, filter, page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Record]{
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
	item, err := h.repo.GetByID(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "disposal record not found")
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
	var req CreateRecordRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if !validMethods[req.Method] {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "method must be one of reuse, recycling, destruction, secure_erasure, physical_destruction, return_to_vendor")
		return
	}
	if req.AssetID == "" && req.CIID == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "asset_id or ci_id is required")
		return
	}
	var performedAt time.Time
	if req.PerformedAt != "" {
		parsed, err := time.Parse(time.RFC3339, req.PerformedAt)
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "performed_at must be RFC3339")
			return
		}
		performedAt = parsed
	}
	rec := &Record{
		OrganizationID: t.OrganizationID,
		AssetID:        req.AssetID,
		CIID:           req.CIID,
		Method:         req.Method,
		CertificateRef: req.CertificateRef,
		DataCarrier:    req.DataCarrier,
		PerformedBy:    req.PerformedBy,
		PerformedAt:    performedAt,
		Notes:          req.Notes,
	}
	if err := h.repo.Create(r.Context(), rec); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusCreated, rec)
}
