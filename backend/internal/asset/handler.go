package asset

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"regexp"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenantapi"
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
	// links writes the asset↔CI links through the CI domain (WP-272).
	links CILinker
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
// asset in a composition (spec §5). Children inherit them read-only. The key
// is the API name of the field (AST-01, WP-739), the value the member of
// UpdateRequest.
var parentOwnedColumns = map[string]string{
	"serial_number":  "SerialNumber",
	"barcode":        "Barcode",
	"rfid_tag":       "RFIDTag",
	"purchase_date":  "PurchaseDate",
	"purchase_price": "PurchaseCost",
	"supplier_id":    "SupplierID",
	"invoice_number": "InvoiceNumber",
	"warranty_until": "WarrantyEnd",
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
	// Asset↔CI links (AST-04, WP-272); the CIs of an asset are listed by
	// GET /api/v1/cis?filter[asset_id]=.
	r.Post("/api/v1/assets/{id}/cis", h.LinkCI)
	r.Delete("/api/v1/assets/{id}/cis/{ci_id}", h.UnlinkCI)
}

// requestContext returns the organization and the field policy of the
// request (permission.PolicyFrom), or answers 401 or 500 (policy not
// resolvable, fail-closed).
func requestContext(w http.ResponseWriter, r *http.Request) (string, *permission.FieldPolicy, bool) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return "", nil, false
	}
	policy, err := permission.PolicyFrom(r.Context())
	if err != nil {
		slog.WarnContext(r.Context(), "asset field policy failed", "error", err)
		api.WriteRepoError(w, err)
		return "", nil, false
	}
	return t.OrganizationID, policy, true
}

// assetRef is the object reference of an asset for its field permissions:
// the asset, its client and its site.
func assetRef(a *Asset) permission.ObjectRef {
	return permission.ObjectRef{ID: a.ID, ClientID: a.ClientID, SiteID: a.SiteID}
}

// project returns the asset as the policy lets the principal read it
// (RBA-07, WP-265, Konvention 11): the asset itself without a restriction,
// otherwise without the fields it may not read and with the readable
// custom fields only.
func project(policy *permission.FieldPolicy, a *Asset) (any, error) {
	return permission.ProjectValue(policy, permission.KindAsset, a.AssetTypeID, assetRef(a), a)
}

// projectCIRef returns the linked CI's identity for the policy: when the
// CI policy is restricted only id, name and status, the identity without
// technical fields (projecting them needs the CI's type and scope, which
// the identity does not carry; fail-closed).
func projectCIRef(policy *permission.FieldPolicy, ref *CIRef) any {
	if !permission.NeedsProjection(policy, permission.KindCI) {
		return ref
	}
	return map[string]string{"id": ref.ID, "name": ref.Name, "status": ref.Status}
}

// List handles GET /api/v1/assets. A filter, search or sort on a field the
// principal cannot read in every scope is 403 with the fields.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	orgID, policy, ok := requestContext(w, r)
	if !ok {
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
	if filterErr := permission.CheckListFilter(policy, permission.KindAsset, nil, listFieldPaths(&filter)); filterErr != nil {
		api.WriteFieldError(w, filterErr)
		return
	}

	items, total, err := h.repo.List(r.Context(), orgID, &filter, page)
	if err != nil {
		if errors.Is(err, api.ErrInvalidCursor) {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
			return
		}
		api.WriteRepoError(w, err)
		return
	}

	hasMore := page.Offset+page.Limit < total
	if page.Cursor != nil {
		hasMore = len(items) == page.Limit
	}
	nextCursor := NextCursor(items, &filter, page.Limit)
	if !permission.NeedsProjection(policy, permission.KindAsset) {
		api.WriteJSON(w, http.StatusOK, api.ListResponse[Asset]{
			Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: hasMore, NextCursor: nextCursor,
		})
		return
	}
	data := make([]any, len(items))
	for i := range items {
		if data[i], err = project(policy, &items[i]); err != nil {
			api.WriteRepoError(w, err)
			return
		}
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[any]{
		Data: data, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: hasMore, NextCursor: nextCursor,
	})
}

// listFieldPaths are the DTO paths the list reads: its filters, the search
// over the inventory number and a sort on a field column.
func listFieldPaths(filter *FilterParams) []string {
	var paths []string
	if filter.Status != "" {
		paths = append(paths, "status")
	}
	if filter.Category != "" {
		paths = append(paths, "category")
	}
	if filter.Search != "" {
		paths = append(paths, "inventory_no")
	}
	if column, _ := NormalizeSort(filter); column == "inventory_no" || column == "category" || column == "status" {
		paths = append(paths, column)
	}
	return paths
}

// Get handles GET /api/v1/assets/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	orgID, policy, ok := requestContext(w, r)
	if !ok {
		return
	}

	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	item, err := h.repo.GetByID(r.Context(), orgID, id)
	if err != nil {
		writeAssetError(w, r, err)
		return
	}
	body, err := project(policy, item)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}

	// Enrich with the linked CI's read-only technical identity (spec §4); the
	// CI stays the source of truth for technical data.
	if h.cis != nil && item.CIID != "" {
		if linked, err := h.cis.GetByID(r.Context(), orgID, item.CIID); err == nil && linked != nil {
			api.WriteJSON(w, http.StatusOK, map[string]any{
				"asset": body,
				"ci":    projectCIRef(policy, linked),
			})
			return
		}
	}

	api.WriteJSON(w, http.StatusOK, body)
}

// Create handles POST /api/v1/assets. A missing or empty currency is the
// currency of the organization; every sent field must be editable for the
// principal (403 with the fields).
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	orgID, policy, ok := requestContext(w, r)
	if !ok {
		return
	}

	var req CreateRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}

	if req.Name == "" || req.AssetTag == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name and inventory_no are required")
		return
	}
	if err := validCurrency(req.Currency); err != nil {
		api.WriteFieldError(w, err)
		return
	}
	price, err := purchasePrice(req.PurchaseCost)
	if err != nil {
		api.WriteFieldError(w, err)
		return
	}
	req.PurchaseCost = price

	category := req.Category
	if category == "" {
		category = "hardware"
	}
	status := req.Status
	if status == "" {
		status = "in_stock"
	}

	a := &Asset{
		OrganizationID: orgID,
		ClientID:       req.ClientID,
		CIID:           req.CIID,
		AssetTag:       req.AssetTag,
		Name:           req.Name,
		Category:       category,
		Status:         status,
		Manufacturer:   req.Manufacturer,
		Model:          req.Model,
		PurchaseDate:   req.PurchaseDate,
		PurchaseCost:   req.PurchaseCost,
		Currency:       strings.TrimSpace(req.Currency),
		WarrantyEnd:    req.WarrantyEnd,
		CostCenter:     req.CostCenter,
		SupplierID:     req.SupplierID,
		ContractID:     req.ContractID,
		OwnerUserID:    req.OwnerUserID,
		OwnerTeamID:    req.OwnerTeamID,
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
	if err = permission.CheckWrite(policy, permission.KindAsset, "", assetRef(a), req.SentFields()); err != nil {
		api.WriteFieldError(w, err)
		return
	}

	// A primary CI is linked through the CI domain (WP-272), which needs
	// ci:write as PATCH /cis/{id} does.
	if store, linked := h.linkStore(); linked && a.CIID != "" {
		if !requirePermission(w, r, identity.PermCIWrite) {
			return
		}
		err = h.createLinked(r.Context(), store, a)
	} else {
		err = h.repo.Create(r.Context(), a)
	}
	if err != nil {
		writeAssetError(w, r, err)
		return
	}

	body, err := project(policy, a)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, body)
}

// validCurrency checks a sent currency (ISO 4217, WP-107): empty passes
// (the organization's currency applies), anything else must be an active
// code (400 on currency).
func validCurrency(code string) error {
	if strings.TrimSpace(code) == "" {
		return nil
	}
	return tenantapi.ValidCurrency(code)
}

// purchasePriceFormat is a purchase price of NUMERIC(14,2): up to twelve
// integer digits and two decimals, not negative.
var purchasePriceFormat = regexp.MustCompile(`^\d{1,12}(\.\d{1,2})?$`)

// purchasePrice checks a sent purchase price (a decimal string, AST-01) and
// returns it with two decimals ("12.5" → "12.50"), so it compares equal to
// the stored value; empty passes (no price). Anything else is 400 on
// purchase_price.
func purchasePrice(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if !purchasePriceFormat.MatchString(value) {
		return "", &api.FieldError{Field: "purchase_price",
			Message: "must be a decimal string with up to 12 integer digits and 2 decimals, e.g. \"1234.50\""}
	}
	whole, frac, _ := strings.Cut(value, ".")
	whole = strings.TrimLeft(whole, "0")
	if whole == "" {
		whole = "0"
	}
	return whole + "." + frac + strings.Repeat("0", 2-len(frac)), nil
}

// ParentOwnedFieldError refuses a write to a shared inventory property of a
// composition child (spec §5, 409): the parent asset owns Field.
type ParentOwnedFieldError struct{ Field string }

// validation: a fixed rule text with the field name of the request.
func (e *ParentOwnedFieldError) Error() string {
	return fmt.Sprintf("field %q is owned by the parent asset and is read-only on composition children", e.Field)
}

// writeAssetError answers a failed asset request: an asset that is unknown,
// of another organization or outside the caller's scope is 404 (TEN-04), a
// parent-owned field of a composition child 409 with its domain message, a
// linked CI that is no UUID or not visible 422 on primary_ci_id (on the link
// routes 404), an asset the caller may not change 403, a reference that is
// not usable 422 on its field, a taken inventory number, barcode or RFID tag
// 409 on its field, a field the principal may not edit 403 with the fields.
// Database errors go through api.WriteRepoError; no table, constraint,
// policy or SQLSTATE text reaches the client (API-03, F-047), the log keeps
// it.
func writeAssetError(w http.ResponseWriter, r *http.Request, err error) {
	var owned *ParentOwnedFieldError
	switch {
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrNotFound.Error())
	case errors.Is(err, ErrCINotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", ErrCINotFound.Error())
	case errors.Is(err, ErrNotWritable):
		api.WriteError(w, http.StatusForbidden, "Forbidden", ErrNotWritable.Error())
	case errors.As(err, &owned):
		api.WriteError(w, http.StatusConflict, "Conflict", owned.Error())
	case api.WriteFieldError(w, err):
	default:
		slog.WarnContext(r.Context(), "asset request failed", "error", err)
		api.WriteRepoError(w, err)
	}
}

// readOnlyPaths name the canonical write path of each read-only member of
// the asset (E-120).
var readOnlyPaths = []struct {
	field, message string
	sent           func(*UpdateRequest) bool
}{
	{"location_id", "is read-only; the location changes through stock movements (POST /api/v1/stock-movements) or rack placement",
		func(req *UpdateRequest) bool { return req.LocationID.Set }},
	{"lifecycle_state", "is read-only; the state changes through POST /api/v1/assets/{id}/lifecycle-transitions",
		func(req *UpdateRequest) bool { return req.LifecycleState.Set }},
}

// rejectReadOnly returns the 422 field error of the first read-only member
// the request sends.
func rejectReadOnly(req *UpdateRequest) error {
	for _, ro := range readOnlyPaths {
		if ro.sent(req) {
			return &api.FieldError{Field: ro.field, Message: ro.message, Status: http.StatusUnprocessableEntity}
		}
	}
	return nil
}

// Update handles PATCH /api/v1/assets/{id}
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	orgID, policy, ok := requestContext(w, r)
	if !ok {
		return
	}

	// The id reaches the composition lookup and SQL only as a UUID (F-047).
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	var req UpdateRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if err := rejectReadOnly(&req); err != nil {
		api.WriteFieldError(w, err)
		return
	}
	if req.Currency != nil {
		if err := validCurrency(*req.Currency); err != nil {
			api.WriteFieldError(w, err)
			return
		}
	}
	if req.AssetTag != nil && strings.TrimSpace(*req.AssetTag) == "" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "inventory_no must not be empty")
		return
	}
	if req.PurchaseCost != nil {
		price, err := purchasePrice(*req.PurchaseCost)
		if err != nil {
			api.WriteFieldError(w, err)
			return
		}
		req.PurchaseCost = &price
	}

	// Children must not duplicate the parent asset's shared inventory
	// properties (spec §5). Reject writes to parent-owned fields when the
	// asset participates as a composition child.
	if err := h.rejectParentOwnedFields(r.Context(), orgID, id, &req); err != nil {
		writeAssetError(w, r, err)
		return
	}

	// A primary CI is linked through the CI domain (WP-272), which needs
	// ci:write as PATCH /cis/{id} does; clearing it writes no CI.
	if _, linked := h.linkStore(); linked && req.CIID != nil && strings.TrimSpace(*req.CIID) != "" &&
		!requirePermission(w, r, identity.PermCIWrite) {
		return
	}
	req.Check = checkUpdate(policy, &req)
	item, err := h.update(r.Context(), orgID, id, &req)
	if err != nil {
		writeAssetError(w, r, err)
		return
	}

	body, err := project(policy, item)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, body)
}

// checkUpdate returns the check of a PATCH against the field permissions
// (WP-265): every sent field the principal may not read counts, whatever
// its value (no equality oracle), and every readable field whose value
// changes; each must be editable.
func checkUpdate(policy *permission.FieldPolicy, req *UpdateRequest) func(*Asset) error {
	return func(current *Asset) error {
		if !policy.Restricted(permission.KindAsset) {
			return nil
		}
		ref := assetRef(current)
		before, err := fieldValues(current)
		if err != nil {
			return err
		}
		after, err := fieldValues(req)
		if err != nil {
			return err
		}
		var changed []string
		for _, path := range req.SentFields() {
			if !permission.Readable(policy, permission.KindAsset, current.AssetTypeID, ref, path) ||
				!bytes.Equal(normalizedValue(before[path]), normalizedValue(after[path])) {
				changed = append(changed, path)
			}
		}
		return permission.CheckWrite(policy, permission.KindAsset, current.AssetTypeID, ref, changed)
	}
}

// fieldValues encodes v (an asset or an update request) as its DTO members,
// with each custom field as custom_fields.<key>.
func fieldValues(v any) (map[string]json.RawMessage, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]json.RawMessage
	if decodeErr := json.Unmarshal(raw, &out); decodeErr != nil {
		return nil, decodeErr
	}
	if custom, ok := out["custom_fields"]; ok {
		var fields map[string]json.RawMessage
		if decodeErr := json.Unmarshal(custom, &fields); decodeErr == nil {
			for k, value := range fields {
				out[customFieldsPrefix+k] = value
			}
		}
	}
	return out, nil
}

// normalizedValue treats an absent member, null, "" and 0 alike: the
// repository stores an empty text as NULL and the DTO omits empty members.
func normalizedValue(raw json.RawMessage) []byte {
	switch s := strings.TrimSpace(string(raw)); s {
	case "", "null", `""`, "0":
		return nil
	default:
		return []byte(s)
	}
}

// rejectParentOwnedFields returns a *ParentOwnedFieldError when the request
// writes a parent-owned shared inventory property on a composition child,
// the error of the composition lookup when it fails.
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
			return &ParentOwnedFieldError{Field: column}
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
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	item, err := h.repo.GetByID(r.Context(), t.OrganizationID, id)
	if err != nil {
		writeAssetError(w, r, err)
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

	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	if err := h.repo.Delete(r.Context(), t.OrganizationID, id); err != nil {
		writeAssetError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
