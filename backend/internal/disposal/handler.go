package disposal

import (
	"context"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Methods are the disposal methods (migration 000049).
var Methods = []string{"reuse", "recycling", "destruction", "secure_erasure", "physical_destruction", "return_to_vendor"}

// CommonRequiredFields are required for every method (E-55). An entry
// "a|b" is satisfied by either field.
var CommonRequiredFields = []string{"method", "performed_at", "performed_by", "asset_id|ci_id"}

// RequiredFields lists per method the fields required in addition to
// CommonRequiredFields after DIN 66399 and the BSI recommendation for data
// carriers (E-55, WP-751). An entry "a|b" is satisfied by either field.
// OpenAPI describes the same table at CreateDisposalRecordRequest
// (x-required-fields-by-method); the form of WP-815 mirrors it.
var RequiredFields = map[string][]string{
	"destruction":          {"protection_class", "security_level", "data_carrier", "supplier_id|witness", "evidence_document_id"},
	"physical_destruction": {"protection_class", "security_level", "data_carrier", "supplier_id|witness", "evidence_document_id"},
	"secure_erasure":       {"data_carrier", "certificate_ref|notes", "evidence_document_id"},
	"reuse":                {},
	"recycling":            {},
	"return_to_vendor":     {},
}

// securityLevelPattern is a DIN 66399 security level: protection target
// class letter and level 1-7, e.g. P-4.
var securityLevelPattern = regexp.MustCompile(`^[POTEFH]-[1-7]$`)

// MovementRecorder records the disposal movement into the inventory ledger
// (spec §9). Satisfied by the movement repository.
type MovementRecorder interface {
	Record(ctx context.Context, m *movement.Movement) error
}

// Handler provides HTTP handlers for disposal records (append-only).
type Handler struct {
	repo      Repository
	movements MovementRecorder
}

// NewHandler creates a new disposal handler.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// WithMovements attaches the movement ledger so disposal records emit an
// auditable disposal movement. Movement failures never fail the disposal
// record itself.
func (h *Handler) WithMovements(recorder MovementRecorder) *Handler {
	h.movements = recorder
	return h
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
		api.WriteRepoError(w, err)
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

// requestValues are the values of the request fields the required-field
// table names; empty means missing.
func requestValues(req *CreateRecordRequest) map[string]string {
	v := map[string]string{
		"method":               req.Method,
		"performed_at":         req.PerformedAt,
		"performed_by":         strings.TrimSpace(req.PerformedBy),
		"witness":              strings.TrimSpace(req.Witness),
		"asset_id":             req.AssetID,
		"ci_id":                req.CIID,
		"security_level":       req.SecurityLevel,
		"data_carrier":         strings.TrimSpace(req.DataCarrier),
		"supplier_id":          req.SupplierID,
		"evidence_document_id": req.EvidenceDocumentID,
		"certificate_ref":      strings.TrimSpace(req.CertificateRef),
		"notes":                strings.TrimSpace(req.Notes),
	}
	if req.ProtectionClass != nil {
		v["protection_class"] = "set"
	}
	return v
}

// Validate checks req against the required-field table of its method and
// the value ranges (E-55); the error is a *api.FieldError naming the field
// (400).
func Validate(req *CreateRecordRequest) error {
	if req.Method == "" {
		return &api.FieldError{Field: "method", Message: "is required"}
	}
	extra, ok := RequiredFields[req.Method]
	if !ok {
		return &api.FieldError{Field: "method", Message: "must be one of " + strings.Join(Methods, ", ")}
	}
	values := requestValues(req)
	for _, entry := range slices.Concat(CommonRequiredFields, extra) {
		alternatives := strings.Split(entry, "|")
		if slices.ContainsFunc(alternatives, func(f string) bool { return values[f] != "" }) {
			continue
		}
		msg := "is required"
		if len(alternatives) > 1 {
			msg = "is required (alternatively " + strings.Join(alternatives[1:], ", ") + ")"
		}
		if !slices.Contains(CommonRequiredFields, entry) {
			msg += " for method " + req.Method
		}
		return &api.FieldError{Field: alternatives[0], Message: msg}
	}
	if req.ProtectionClass != nil && (*req.ProtectionClass < 1 || *req.ProtectionClass > 3) {
		return &api.FieldError{Field: "protection_class", Message: "must be 1, 2 or 3 (DIN 66399)"}
	}
	if req.SecurityLevel != "" && !securityLevelPattern.MatchString(req.SecurityLevel) {
		return &api.FieldError{Field: "security_level", Message: "must match ^[POTEFH]-[1-7]$ (DIN 66399, e.g. P-4)"}
	}
	for _, f := range []string{"asset_id", "ci_id", "supplier_id", "evidence_document_id"} {
		if values[f] != "" && !api.IsUUID(values[f]) {
			return &api.FieldError{Field: f, Message: "must be a UUID"}
		}
	}
	if _, err := time.Parse(time.RFC3339, req.PerformedAt); err != nil {
		return &api.FieldError{Field: "performed_at", Message: "must be RFC3339"}
	}
	return nil
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
	if err := Validate(&req); err != nil {
		api.WriteRepoError(w, err)
		return
	}
	performedAt, _ := time.Parse(time.RFC3339, req.PerformedAt)
	rec := &Record{
		OrganizationID:     t.OrganizationID,
		AssetID:            req.AssetID,
		CIID:               req.CIID,
		Method:             req.Method,
		ProtectionClass:    req.ProtectionClass,
		SecurityLevel:      req.SecurityLevel,
		DataCarrier:        strings.TrimSpace(req.DataCarrier),
		SupplierID:         req.SupplierID,
		EvidenceDocumentID: req.EvidenceDocumentID,
		CertificateRef:     strings.TrimSpace(req.CertificateRef),
		PerformedBy:        strings.TrimSpace(req.PerformedBy),
		Witness:            strings.TrimSpace(req.Witness),
		PerformedAt:        performedAt,
		Notes:              strings.TrimSpace(req.Notes),
	}
	if err := h.repo.Create(r.Context(), rec); err != nil {
		api.WriteRepoError(w, err)
		return
	}
	if h.movements != nil && rec.AssetID != "" {
		_ = h.movements.Record(r.Context(), &movement.Movement{
			OrganizationID: t.OrganizationID,
			ItemKind:       "asset",
			AssetID:        rec.AssetID,
			MovementType:   "disposal",
			Reason:         rec.Method,
			Notes:          rec.Notes,
		})
	}
	api.WriteJSON(w, http.StatusCreated, rec)
}
