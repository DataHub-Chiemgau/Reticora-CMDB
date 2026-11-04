package disposal

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
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

// MovementRecorder books the disposal movement into the ledger
// stock_movement (spec §9, WP-274). Satisfied by movement.Service; with the
// context of the disposal transaction it joins it.
type MovementRecorder interface {
	Record(ctx context.Context, m *movement.Movement) error
}

// Transitioner applies a lifecycle transition (WP-724). Satisfied by
// lifecycle.Service; with the context of the disposal transaction it joins
// it.
type Transitioner interface {
	Transition(ctx context.Context, orgID, entityType, entityID, modelID string, req lifecycle.TransitionRequest) (*lifecycle.TransitionResult, error)
}

// ModelReader loads a lifecycle model by its id; it names the states a
// disposal may start from when the current state has no edge to disposed.
// Satisfied by lifecycle.Repository.
type ModelReader interface {
	GetByID(ctx context.Context, orgID, id string) (*lifecycle.Definition, error)
}

// TxRunner runs fn in one transaction of the context's tenant scope: the
// repository calls fn makes with its context join it
// (database.ContextWithTx, WP-332). The disposal PG repository implements
// it.
type TxRunner interface {
	RunInTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error
}

// DisposedState is the lifecycle state a disposal sets (seed model
// physical_asset, migration 000055).
const DisposedState = "disposed"

// SourceStateError refuses a disposal whose asset or CI has no lifecycle
// transition to disposed from its current state, or no state at all (422
// invalid-transition with the allowed source states of the model).
type SourceStateError struct {
	EntityType string
	From       string
	Allowed    []string
}

// validation: the state keys of the entity and of its lifecycle model.
func (e *SourceStateError) Error() string {
	from := e.From
	if from == "" {
		from = "(none)"
	}
	return fmt.Sprintf("the %s cannot be disposed from lifecycle state %s; allowed source states: %s",
		e.EntityType, from, strings.Join(e.Allowed, ", "))
}

// Handler provides HTTP handlers for disposal records (append-only).
type Handler struct {
	repo        Repository
	tx          TxRunner
	movements   MovementRecorder
	transitions Transitioner
	resolver    lifecycle.EntityResolver
	models      ModelReader
}

// NewHandler creates a new disposal handler. A repository that is a
// TxRunner records a disposal in one transaction (NFR-03, WP-278).
func NewHandler(repo Repository) *Handler {
	h := &Handler{repo: repo}
	if runner, ok := repo.(TxRunner); ok {
		h.tx = runner
	}
	return h
}

// WithLifecycle attaches the movement service and the lifecycle transition
// of a disposal (MGT-02, WP-278): in the transaction of the record the
// asset books the movement disposal, and the asset or CI enters the
// lifecycle state disposed. resolver resolves the lifecycle model of the
// entity, models reads it for the allowed source states. A failure of
// either fails the disposal and rolls the record back.
func (h *Handler) WithLifecycle(movements MovementRecorder, transitions Transitioner, resolver lifecycle.EntityResolver, models ModelReader) *Handler {
	h.movements = movements
	h.transitions = transitions
	h.resolver = resolver
	h.models = models
	return h
}

// inTx runs fn in one transaction, or as it is without a TxRunner (e.g.
// the MemoryRepository).
func (h *Handler) inTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error {
	if h.tx == nil {
		return fn(ctx)
	}
	return h.tx.RunInTx(ctx, orgID, fn)
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
	err := h.inTx(r.Context(), t.OrganizationID, func(ctx context.Context) error {
		if createErr := h.repo.Create(ctx, rec); createErr != nil {
			return createErr
		}
		return h.dispose(r.WithContext(ctx), rec)
	})
	if err != nil {
		writeCreateError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, rec)
}

// dispose books the movement disposal of the asset of rec and moves its
// asset and CI into the lifecycle state disposed, with the context of the
// disposal transaction (req.Context()). The transition runs after the
// insert, so a guard on disposal_record sees the new record (WP-279). A CI
// whose type has no lifecycle model keeps no state.
func (h *Handler) dispose(req *http.Request, rec *Record) error {
	ctx := req.Context()
	if h.movements != nil && rec.AssetID != "" {
		if err := h.movements.Record(ctx, &movement.Movement{
			OrganizationID: rec.OrganizationID,
			ItemKind:       "asset",
			AssetID:        rec.AssetID,
			MovementType:   "disposal",
			Reason:         rec.Method,
			DocumentID:     rec.EvidenceDocumentID,
			Notes:          rec.Notes,
		}); err != nil {
			return err
		}
	}
	if h.transitions == nil || h.resolver == nil {
		return nil
	}
	for _, entity := range []struct{ kind, id string }{{"asset", rec.AssetID}, {"ci", rec.CIID}} {
		if entity.id == "" {
			continue
		}
		modelID, err := h.resolver.LifecycleModelFor(req, rec.OrganizationID, entity.kind, entity.id)
		if errors.Is(err, lifecycle.ErrNoLifecycleModel) {
			continue
		}
		if err != nil {
			return err
		}
		_, err = h.transitions.Transition(ctx, rec.OrganizationID, entity.kind, entity.id, modelID, lifecycle.TransitionRequest{
			ToState: DisposedState,
			Reason:  "disposal: " + rec.Method,
			Context: map[string]any{
				"disposal_record": rec.ID,
				"disposal_date":   rec.PerformedAt.UTC().Format(time.RFC3339),
			},
		})
		var invalid *lifecycle.InvalidTransitionError
		if errors.As(err, &invalid) {
			return h.sourceStateError(ctx, rec.OrganizationID, entity.kind, modelID, invalid.From)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// sourceStateError is the SourceStateError of an entity in state from: the
// allowed source states are the from states of the transitions of model
// modelID into disposed.
func (h *Handler) sourceStateError(ctx context.Context, orgID, entityType, modelID, from string) error {
	allowed := []string{}
	if h.models != nil {
		def, err := h.models.GetByID(ctx, orgID, modelID)
		if err != nil {
			return err
		}
		for i := range def.Transitions {
			if tr := &def.Transitions[i]; tr.ToStateKey == DisposedState && tr.FromStateKey != "" &&
				!slices.Contains(allowed, tr.FromStateKey) {
				allowed = append(allowed, tr.FromStateKey)
			}
		}
		slices.Sort(allowed)
	}
	return &SourceStateError{EntityType: entityType, From: from, Allowed: allowed}
}

// writeCreateError reports a failed disposal: a refused source state is 422
// invalid-transition with from_state, to_state and allowed_from_states, a
// failed guard of the transition 422 lifecycle-guard with the violations,
// an asset or CI that is not visible 404; other errors go through
// api.WriteRepoError.
func writeCreateError(w http.ResponseWriter, err error) {
	var source *SourceStateError
	var guard *lifecycle.GuardError
	switch {
	case errors.As(err, &source):
		api.WriteProblemBody(w, http.StatusUnprocessableEntity, struct {
			api.ProblemDetail
			Field             string   `json:"field"`
			FromState         string   `json:"from_state"`
			ToState           string   `json:"to_state"`
			AllowedFromStates []string `json:"allowed_from_states"`
		}{
			ProblemDetail: api.ProblemDetail{Type: lifecycle.ProblemInvalidTransition, Title: "Unprocessable Entity",
				Status: http.StatusUnprocessableEntity, Detail: source.Error()},
			Field: source.EntityType + "_id", FromState: source.From, ToState: DisposedState,
			AllowedFromStates: source.Allowed,
		})
	case errors.As(err, &guard):
		api.WriteProblemBody(w, http.StatusUnprocessableEntity, struct {
			api.ProblemDetail
			Violations []lifecycle.GuardViolation `json:"violations"`
		}{
			ProblemDetail: api.ProblemDetail{Type: lifecycle.ProblemLifecycleGuard, Title: "Unprocessable Entity",
				Status: http.StatusUnprocessableEntity, Detail: guard.Error()},
			Violations: guard.Violations,
		})
	case errors.Is(err, movement.ErrAssetNotFound), err.Error() == "not found":
		api.WriteError(w, http.StatusNotFound, "Not Found", "the requested resource was not found")
	default:
		api.WriteRepoError(w, err)
	}
}
