package asset

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// ParentLookup reports whether an asset is a composition child (spec §5:
// children must not duplicate the parent's shared inventory properties).
type ParentLookup interface {
	// ParentOfAsset returns true when the asset participates as a child in a
	// composition.
	ParentOfAsset(ctx context.Context, orgID, assetID string) (bool, error)
}

// CILookup resolves the technical identity of a linked CI (spec §4: the CI
// owns the technical/configuration view; the asset reads it through the
// optional 1:1 association instead of duplicating fields).
type CILookup interface {
	GetByID(ctx context.Context, orgID, id string) (*CIRef, error)
}

// CIRef is the read-only technical identity projection of the linked CI.
type CIRef struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	Hostname        string `json:"hostname,omitempty"`
	FQDN            string `json:"fqdn,omitempty"`
	ManagementIP    string `json:"management_ip,omitempty"`
	Manufacturer    string `json:"manufacturer,omitempty"`
	Model           string `json:"model,omitempty"`
	SerialNumber    string `json:"serial_number,omitempty"`
	OSName          string `json:"os_name,omitempty"`
	OSVersion       string `json:"os_version,omitempty"`
	DiscoverySource string `json:"discovery_source,omitempty"`
}

// Handler provides HTTP handlers for asset endpoints.
type Handler struct {
	repo        Repository
	composition ParentLookup
	cis         CILookup
}

// NewHandler creates a new asset handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// WithComposition attaches the composition lookup used to reject
// parent-owned shared fields on child assets.
func (h *Handler) WithComposition(lookup ParentLookup) *Handler {
	h.composition = lookup
	return h
}

// WithCIs attaches the CI lookup used to enrich an asset with the linked
// CI's read-only technical identity (spec §4).
func (h *Handler) WithCIs(lookup CILookup) *Handler {
	h.cis = lookup
	return h
}

// parentOwnedColumns are the shared inventory properties owned by the parent
// asset in a composition (spec §5). Children inherit them read-only.
var parentOwnedColumns = map[string]string{
	"serial_number":  "SerialNumber",
	"barcode":        "Barcode",
	"rfid_tag":       "RFIDTag",
	"purchase_date":  "PurchaseDate",
	"purchase_cost":  "PurchaseCost",
	"supplier":       "Supplier",
	"invoice_number": "InvoiceNumber",
	"warranty_end":   "WarrantyEnd",
	"location":       "Location",
}

// RegisterRoutes registers asset routes on the given mux.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/assets", h.List)
	r.Post("/api/v1/assets", h.Create)
	r.Get("/api/v1/assets/{id}", h.Get)
	r.Patch("/api/v1/assets/{id}", h.Update)
	r.Delete("/api/v1/assets/{id}", h.Delete)
	// Direct-access label (spec §13): printable SVG label with asset tag,
	// barcode/RFID marker and CMDB deep link.
	r.Get("/api/v1/assets/{id}/label.svg", h.Label)
}

// List handles GET /api/v1/assets
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	page := api.ParsePagination(r)
	if page.CursorError != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", page.CursorError.Error())
		return
	}
	filter := FilterParams{
		Status:   r.URL.Query().Get("status"),
		Category: r.URL.Query().Get("category"),
		ClientID: r.URL.Query().Get("client_id"),
		Search:   r.URL.Query().Get("search"),
		SortBy:   r.URL.Query().Get("sort_by"),
		SortDir:  r.URL.Query().Get("sort_dir"),
	}

	items, total, err := h.repo.List(r.Context(), t.OrganizationID, filter, page)
	if err != nil {
		if errors.Is(err, api.ErrInvalidCursor) {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
			return
		}
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	hasMore := page.Offset+page.Limit < total
	if page.Cursor != nil {
		hasMore = len(items) == page.Limit
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Asset]{
		Data:       items,
		Total:      total,
		Limit:      page.Limit,
		Offset:     page.Offset,
		HasMore:    hasMore,
		NextCursor: NextCursor(items, filter, page.Limit),
	})
}

// Get handles GET /api/v1/assets/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	item, err := h.repo.GetByID(r.Context(), t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "asset not found")
		return
	}

	// Enrich with the linked CI's read-only technical identity (spec §4); the
	// CI stays the source of truth for technical data.
	if h.cis != nil && item.CIID != "" {
		if linked, err := h.cis.GetByID(r.Context(), t.OrganizationID, item.CIID); err == nil && linked != nil {
			api.WriteJSON(w, http.StatusOK, map[string]any{
				"asset": item,
				"ci":    linked,
			})
			return
		}
	}

	api.WriteJSON(w, http.StatusOK, item)
}

// Create handles POST /api/v1/assets
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

	if req.Name == "" || req.AssetTag == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name and asset_tag are required")
		return
	}

	category := req.Category
	if category == "" {
		category = "hardware"
	}
	status := req.Status
	if status == "" {
		status = "in_stock"
	}
	currency := req.Currency
	if currency == "" {
		currency = "EUR"
	}

	a := &Asset{
		OrganizationID: t.OrganizationID,
		ClientID:       req.ClientID,
		CIID:           req.CIID,
		AssetTag:       req.AssetTag,
		Name:           req.Name,
		Category:       category,
		Status:         status,
		PurchaseDate:   req.PurchaseDate,
		PurchaseCost:   req.PurchaseCost,
		Currency:       currency,
		WarrantyEnd:    req.WarrantyEnd,
		Supplier:       req.Supplier,
		InvoiceNumber:  req.InvoiceNumber,
		SerialNumber:   req.SerialNumber,
		RFIDTag:        req.RFIDTag,
		Barcode:        req.Barcode,
		Location:       req.Location,
		Notes:          req.Notes,
		CustomFields:   req.CustomFields,
	}
	if a.CustomFields == nil {
		a.CustomFields = make(map[string]any)
	}

	if err := h.repo.Create(r.Context(), a); err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	api.WriteJSON(w, http.StatusCreated, a)
}

// Update handles PATCH /api/v1/assets/{id}
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	var req UpdateRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	// Children must not duplicate the parent asset's shared inventory
	// properties (spec §5). Reject writes to parent-owned fields when the
	// asset participates as a composition child.
	if err := h.rejectParentOwnedFields(r.Context(), t.OrganizationID, id, &req); err != nil {
		api.WriteError(w, http.StatusConflict, "Conflict", err.Error())
		return
	}

	item, err := h.repo.Update(r.Context(), t.OrganizationID, id, req)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "asset not found")
		return
	}

	api.WriteJSON(w, http.StatusOK, item)
}

// rejectParentOwnedFields returns an error when the request writes a
// parent-owned shared inventory property on a composition child.
func (h *Handler) rejectParentOwnedFields(ctx context.Context, orgID, id string, req *UpdateRequest) error {
	if h.composition == nil {
		return nil
	}
	isChild, err := h.composition.ParentOfAsset(ctx, orgID, id)
	if err != nil || !isChild {
		return err
	}
	v := reflect.ValueOf(req).Elem()
	for column, field := range parentOwnedColumns {
		f := v.FieldByName(field)
		if !f.IsValid() || f.IsNil() {
			continue
		}
		// A nil-to-nil clear is allowed; only setting a value conflicts.
		empty := reflect.Zero(f.Type().Elem())
		if !reflect.DeepEqual(f.Elem().Interface(), empty.Interface()) {
			return fmt.Errorf("field %q is owned by the parent asset and is read-only on composition children", column)
		}
	}
	return nil
}

// Label handles GET /api/v1/assets/{id}/label.svg — renders the printable
// direct-access label for the asset.
func (h *Handler) Label(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	item, err := h.repo.GetByID(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "asset not found")
		return
	}
	baseURL := r.URL.Query().Get("base_url")
	if baseURL == "" {
		baseURL = "https://" + r.Host
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(RenderLabelSVG(item, baseURL)))
}

// Delete handles DELETE /api/v1/assets/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	if err := h.repo.Delete(r.Context(), t.OrganizationID, id); err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "asset not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
