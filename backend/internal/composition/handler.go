package composition

import (
	"context"
	"net/http"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// EventDispatcher publishes composition events to webhook subscribers.
type EventDispatcher interface {
	Dispatch(ctx context.Context, orgID, event string, payload any)
}

// AssetLookup resolves the parent asset of a composition. It is declared here
// rather than importing the asset package because asset already imports
// composition (for parent-owned field rejection), so the dependency must not
// be reversed.
type AssetLookup interface {
	GetByID(ctx context.Context, orgID, id string) (*ParentAssetRef, error)
}

// ParentAssetRef is the read-only projection of the shared inventory identity
// that a parent asset owns (spec §5). Children display these values but never
// store them, so the parent stays the single source of truth and the data is
// not duplicated across the composition.
//
// It mirrors asset.CIRef, which projects the opposite direction (an asset
// reading its linked CI's technical identity, spec §4).
type ParentAssetRef struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	AssetTag      string  `json:"asset_tag,omitempty"`
	Status        string  `json:"status,omitempty"`
	SerialNumber  string  `json:"serial_number,omitempty"`
	Barcode       string  `json:"barcode,omitempty"`
	RFIDTag       string  `json:"rfid_tag,omitempty"`
	PurchaseDate  string  `json:"purchase_date,omitempty"`
	PurchaseCost  float64 `json:"purchase_cost,omitempty"`
	Currency      string  `json:"currency,omitempty"`
	Supplier      string  `json:"supplier,omitempty"`
	InvoiceNumber string  `json:"invoice_number,omitempty"`
	WarrantyEnd   string  `json:"warranty_end,omitempty"`
	Location      string  `json:"location,omitempty"`
}

// Handler provides HTTP handlers for parent-asset/child composition.
type Handler struct {
	repo       Repository
	assets     AssetLookup
	dispatcher EventDispatcher
}

// NewHandler creates a new composition handler.
func NewHandler(repo Repository, dispatcher ...EventDispatcher) *Handler {
	h := &Handler{repo: repo}
	if len(dispatcher) > 0 {
		h.dispatcher = dispatcher[0]
	}
	return h
}

// WithAssets attaches the parent asset lookup used to project inherited
// inventory data onto a child.
func (h *Handler) WithAssets(lookup AssetLookup) *Handler {
	h.assets = lookup
	return h
}

// RegisterRoutes registers composition routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/assets/{id}/children", h.ListChildren)
	r.Get("/api/v1/cis/{id}/parent", h.ParentOfCI)
	r.Get("/api/v1/compositions", h.List)
	r.Post("/api/v1/compositions", h.Create)
	r.Get("/api/v1/compositions/{id}", h.Get)
	r.Patch("/api/v1/compositions/{id}", h.Update)
	r.Delete("/api/v1/compositions/{id}", h.Delete)
}

// List handles GET /api/v1/compositions
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.List(r.Context(), t.OrganizationID, r.URL.Query().Get("parent_asset_id"), page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Composition]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

func (h *Handler) dispatch(r *http.Request, orgID, event string, payload any) {
	if h.dispatcher != nil {
		h.dispatcher.Dispatch(r.Context(), orgID, event, payload)
	}
}

// ListChildren handles GET /api/v1/assets/{id}/children
func (h *Handler) ListChildren(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page := api.ParsePagination(r)
	items, total, err := h.repo.List(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), page)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Composition]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// ParentOfCI handles GET /api/v1/cis/{id}/parent
//
// It answers "which parent asset owns this CI, and what shared inventory data
// does it contribute?". Without it a child CI is unreadable as part of its
// composition: the CI record deliberately stores no commercial data, so a
// client had no way to display the serial number, warranty or location that
// belong to the parent (spec §5).
//
// 404 is returned when the CI has no parent, which is the normal case for a
// standalone CI and for the simple 1 asset ↔ 1 CI workflow.
func (h *Handler) ParentOfCI(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	link, err := h.repo.ParentOf(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), "")
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	if link == nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "CI has no parent asset")
		return
	}

	resp := map[string]any{"composition": link}
	// The parent projection is best-effort: the link itself is still useful
	// even when the asset cannot be read, so a lookup failure must not turn a
	// valid composition into an error.
	if h.assets != nil {
		if parent, err := h.assets.GetByID(r.Context(), t.OrganizationID, link.ParentAssetID); err == nil && parent != nil {
			resp["parent_asset"] = parent
			resp["inherited_fields"] = ParentOwnedFields
		}
	}
	api.WriteJSON(w, http.StatusOK, resp)
}

// Create handles POST /api/v1/compositions
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
	if req.ParentAssetID == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "parent_asset_id is required")
		return
	}
	if (req.ChildCIID == "") == (req.ChildAssetID == "") {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "exactly one of child_ci_id or child_asset_id is required")
		return
	}
	configurationOnly := true
	if req.ConfigurationOnly != nil {
		configurationOnly = *req.ConfigurationOnly
	}
	c := &Composition{
		OrganizationID:                t.OrganizationID,
		ParentAssetID:                 req.ParentAssetID,
		ChildCIID:                     req.ChildCIID,
		ChildAssetID:                  req.ChildAssetID,
		Role:                          req.Role,
		Position:                      req.Position,
		ConfigurationOnly:             configurationOnly,
		IndependentlySerialized:       req.IndependentlySerialized,
		IndependentlyAssignable:       req.IndependentlyAssignable,
		IndependentlyLocatable:        req.IndependentlyLocatable,
		IndependentlyLifecycleManaged: req.IndependentlyLifecycleManaged,
	}
	if err := h.repo.Create(r.Context(), c); err != nil {
		msg := err.Error()
		if strings.HasPrefix(msg, "composition cycle") || strings.Contains(msg, "child already has a parent asset") ||
			strings.Contains(msg, "exactly one of") {
			api.WriteError(w, http.StatusConflict, "Conflict", msg)
			return
		}
		if api.WriteDBError(w, err) {
			return
		}
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", "could not create composition")
		return
	}
	h.dispatch(r, t.OrganizationID, "composition.created", c)
	api.WriteJSON(w, http.StatusCreated, c)
}

// Get handles GET /api/v1/compositions/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	item, err := h.repo.GetByID(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "composition not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, item)
}

// Update handles PATCH /api/v1/compositions/{id}
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
		api.WriteError(w, http.StatusNotFound, "Not Found", "composition not found")
		return
	}
	h.dispatch(r, t.OrganizationID, "composition.updated", item)
	api.WriteJSON(w, http.StatusOK, item)
}

// Delete handles DELETE /api/v1/compositions/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if err := h.repo.Delete(r.Context(), t.OrganizationID, chi.URLParam(r, "id")); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "composition not found")
		return
	}
	h.dispatch(r, t.OrganizationID, "composition.deleted", map[string]string{"id": chi.URLParam(r, "id")})
	w.WriteHeader(http.StatusNoContent)
}
