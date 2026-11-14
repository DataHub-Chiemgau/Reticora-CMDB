package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// EntityResolver resolves the lifecycle model applicable to an entity
// (asset: the effective asset model of its type chain or the default
// physical_asset; ci: the effective ci model of its type chain).
type EntityResolver interface {
	// LifecycleModelFor returns the id of the effective lifecycle model of
	// an entity; an entity outside the scope is "not found", a CI without
	// a model ErrNoLifecycleModel.
	LifecycleModelFor(r *http.Request, orgID, entityType, entityID string) (string, error)
}

// CIScopeFunc reads the type and scope of a visible CI for the action
// grant of its transition (RBA-07, WP-728); an error "not found" for a CI
// that is unknown, not visible or deleted.
type CIScopeFunc func(ctx context.Context, orgID, ciID string) (typeID string, ref permission.ObjectRef, err error)

// Handler provides HTTP handlers for lifecycle definitions and transitions.
type Handler struct {
	repo     Repository
	service  *Service
	resolver EntityResolver
	ciScope  CIScopeFunc
}

// WithCIScope sets the CI lookup of the action grant lifecycle:transition
// (WP-728). Without it a transition of a CI is refused with 403 whenever
// the policy depends on the type of the CI (fail-closed).
func (h *Handler) WithCIScope(fn CIScopeFunc) *Handler {
	h.ciScope = fn
	return h
}

// NewHandler creates a lifecycle handler. resolver may be nil when transition
// endpoints are not served. A MemoryStateStore resolver is linked to a
// MemoryRepository repo, so it resolves model ids (--no-db).
func NewHandler(repo Repository, service *Service, resolver EntityResolver) *Handler {
	if store, ok := resolver.(*MemoryStateStore); ok {
		if defs, ok := repo.(*MemoryRepository); ok {
			store.linkDefinitions(defs)
		}
	}
	return &Handler{repo: repo, service: service, resolver: resolver}
}

// lifecycleBases are the collection paths of lifecycle models:
// lifecycle-models is the catalog path (E-38, API-06), lifecycle-definitions
// the deprecated G1 alias that answers until its sunset (API-01).
var lifecycleBases = []string{"/api/v1/lifecycle-models", "/api/v1/lifecycle-definitions"}

// RegisterRoutes registers lifecycle routes on the given mux. PATCH exists
// only under lifecycle-models.
func (h *Handler) RegisterRoutes(r chi.Router) {
	for _, base := range lifecycleBases {
		r.Get(base, h.List)
		r.Post(base, h.Create)
		r.Get(base+"/{id}", h.Get)
		r.Delete(base+"/{id}", h.Delete)
	}
	r.Patch("/api/v1/lifecycle-models/{id}", h.Update)
	r.Get("/api/v1/assets/{id}/lifecycle-transitions", h.OptionsAsset)
	r.Get("/api/v1/cis/{id}/lifecycle-transitions", h.OptionsCI)
	r.Post("/api/v1/assets/{id}/lifecycle-transitions", h.TransitionAsset)
	r.Post("/api/v1/cis/{id}/lifecycle-transitions", h.TransitionCI)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	items, total, err := h.repo.List(r.Context(), t.OrganizationID, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	if items == nil {
		items = []Definition{}
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Definition]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+len(items) < total,
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
		writeModelError(w, err)
		return
	}
	w.Header().Set("ETag", api.ETag(item.Version))
	api.WriteJSON(w, http.StatusOK, item)
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var req CreateDefinitionRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Key == "" || req.Name == "" || len(req.States) == 0 {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "key, name and at least one state are required")
		return
	}
	if !validAppliesTo[req.AppliesTo] {
		api.WriteFieldError(w, &api.FieldError{Field: "applies_to", Message: "applies_to must be asset or ci",
			Status: http.StatusUnprocessableEntity})
		return
	}
	appliesTo := req.AppliesTo
	if appliesTo == "" {
		appliesTo = "asset"
	}
	def := &Definition{
		OrganizationID: t.OrganizationID,
		Key:            req.Key,
		Name:           req.Name,
		AppliesTo:      appliesTo,
		Description:    req.Description,
	}
	states, transitions, err := buildModel(appliesTo, req.States, req.Transitions)
	if err != nil {
		api.WriteFieldError(w, err)
		return
	}
	def.States, def.Transitions = states, transitions
	if err := h.repo.Create(r.Context(), def); err != nil {
		// A taken key is 409 (in memory ErrConflict, in PostgreSQL 23505);
		// no SQLSTATE or constraint text reaches the client (F-047).
		if errors.Is(err, ErrConflict) {
			api.WriteError(w, http.StatusConflict, "Conflict", ErrConflict.Error())
			return
		}
		api.WriteRepoError(w, err)
		return
	}
	w.Header().Set("ETag", api.ETag(def.Version))
	api.WriteJSON(w, http.StatusCreated, def)
}

// Update handles PATCH /api/v1/lifecycle-models/{id}: name, description,
// states (matched by key, a kept state keeps its id) and transitions
// (replaced; without transitions the existing ones between kept states
// stay). key and applies_to cannot change (422). If-Match is optional
// (API-07); an older version is 412. A system model is 403, the model of
// another organization 404, a removed state with entities 409.
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	expected, _, err := api.ParseIfMatch(r)
	if err != nil {
		api.WritePreconditionFailed(w, err.Error())
		return
	}
	var req UpdateDefinitionRequest
	if err = api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	current, err := h.repo.GetByID(r.Context(), t.OrganizationID, chi.URLParam(r, "id"))
	if err != nil {
		writeModelError(w, err)
		return
	}
	if current.IsSystem || current.OrganizationID == "" {
		writeModelError(w, ErrSystemModel)
		return
	}
	if req.Key != nil && *req.Key != current.Key {
		api.WriteFieldError(w, &api.FieldError{Field: "key", Message: "the key of a lifecycle model cannot change",
			Status: http.StatusUnprocessableEntity})
		return
	}
	if req.AppliesTo != nil && *req.AppliesTo != current.AppliesTo {
		api.WriteFieldError(w, &api.FieldError{Field: "applies_to", Message: "applies_to of a lifecycle model cannot change",
			Status: http.StatusUnprocessableEntity})
		return
	}
	def := &Definition{ID: current.ID, OrganizationID: t.OrganizationID, Name: current.Name, Description: current.Description}
	if req.Name != nil {
		if strings.TrimSpace(*req.Name) == "" {
			api.WriteFieldError(w, &api.FieldError{Field: "name", Message: "name is required",
				Status: http.StatusUnprocessableEntity})
			return
		}
		def.Name = *req.Name
	}
	if req.Description != nil {
		def.Description = *req.Description
	}
	stateSpecs := specsOfStates(current.States)
	if req.States != nil {
		stateSpecs = *req.States
	}
	var transitionSpecs []TransitionSpec
	if req.Transitions != nil {
		transitionSpecs = *req.Transitions
	} else {
		transitionSpecs = keptTransitions(current.Transitions, stateSpecs)
	}
	if len(stateSpecs) == 0 {
		api.WriteFieldError(w, &api.FieldError{Field: "states", Message: "at least one state is required",
			Status: http.StatusUnprocessableEntity})
		return
	}
	states, transitions, err := buildModel(current.AppliesTo, stateSpecs, transitionSpecs)
	if err != nil {
		api.WriteFieldError(w, err)
		return
	}
	def.States, def.Transitions = states, transitions
	if err := h.repo.Update(r.Context(), def, expected); err != nil {
		writeModelError(w, err)
		return
	}
	w.Header().Set("ETag", api.ETag(def.Version))
	api.WriteJSON(w, http.StatusOK, def)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if err := h.repo.Delete(r.Context(), t.OrganizationID, chi.URLParam(r, "id")); err != nil {
		writeModelError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeModelError answers a refused read or write of a lifecycle model: an
// unknown or foreign model 404, a system model 403, an older If-Match
// version 412, a used model or removed states with entities 409 with their
// counts; everything else goes through api.WriteRepoError (F-047).
func writeModelError(w http.ResponseWriter, err error) {
	var inUse *ModelInUseConflict
	var statesInUse *StatesInUseConflict
	switch {
	case errors.Is(err, api.ErrNotFound) || err.Error() == "not found":
		api.WriteError(w, http.StatusNotFound, "Not Found", "lifecycle model not found")
	case errors.Is(err, ErrSystemModel):
		api.WriteError(w, http.StatusForbidden, "Forbidden", ErrSystemModel.Error())
	case errors.Is(err, ErrVersionMismatch):
		api.WritePreconditionFailed(w, ErrVersionMismatch.Error())
	case errors.As(err, &inUse):
		api.WriteProblemBody(w, http.StatusConflict, struct {
			api.ProblemDetail
			*ModelInUseConflict
		}{
			ProblemDetail: api.ProblemDetail{Type: "urn:reticora:problem:conflict", Title: "Conflict",
				Status: http.StatusConflict, Detail: inUse.Error()},
			ModelInUseConflict: inUse,
		})
	case errors.As(err, &statesInUse):
		api.WriteProblemBody(w, http.StatusConflict, struct {
			api.FieldProblem
			StateCounts []StateCount `json:"state_counts"`
		}{
			FieldProblem: api.FieldProblem{
				ProblemDetail: api.ProblemDetail{Type: "urn:reticora:problem:conflict", Title: "Conflict",
					Status: http.StatusConflict, Detail: statesInUse.Error()},
				Field: "states",
			},
			StateCounts: statesInUse.Counts,
		})
	default:
		api.WriteRepoError(w, err)
	}
}

// requiredAssetStates are the states an organization asset model must
// contain: reservation/availability.go and savedview/filter.go evaluate
// these keys, the system checks of WP-279 hang on in_stock, deployed and
// disposed.
var requiredAssetStates = []string{"in_stock", "reserved", "deployed", "repair", "retired", "disposed"}

// buildModel checks the states and transitions of a POST or PATCH and
// returns them: state keys are set and unique, an asset model has the
// required asset states, transitions name known states and have a key,
// and every guard rule passes rules.Parse (422 with the path, e.g.
// transitions[2].guard.fields.serial_number).
func buildModel(appliesTo string, stateSpecs []StateSpec, transitionSpecs []TransitionSpec) ([]State, []Transition, error) {
	unprocessable := func(field, format string, args ...any) error {
		return &api.FieldError{Field: field, Message: fmt.Sprintf(format, args...), Status: http.StatusUnprocessableEntity}
	}
	known := map[string]bool{}
	states := make([]State, 0, len(stateSpecs))
	for i, s := range stateSpecs {
		if strings.TrimSpace(s.Key) == "" {
			return nil, nil, unprocessable(fmt.Sprintf("states[%d].key", i), "the state key is required")
		}
		if known[s.Key] {
			return nil, nil, unprocessable(fmt.Sprintf("states[%d].key", i), "the state key %q is taken", s.Key)
		}
		known[s.Key] = true
		// Only the state of a CI locks fields (ci.Service, the catalog
		// rules of citype): an asset model with locks would answer 200
		// and protect nothing.
		if appliesTo == "asset" && len(s.LockedFields) > 0 {
			return nil, nil, unprocessable(fmt.Sprintf("states[%d].locked_fields", i), "only the states of a CI model lock fields")
		}
		if err := checkLockedFields(i, s.LockedFields); err != nil {
			return nil, nil, err
		}
		states = append(states, State{
			Key: s.Key, Label: s.Label, IsInitial: s.IsInitial,
			IsTerminal: s.IsTerminal, SortOrder: s.SortOrder, RequiredFields: s.RequiredFields,
			LockedFields: s.LockedFields,
		})
	}
	if appliesTo == "asset" {
		var missing []string
		for _, key := range requiredAssetStates {
			if !known[key] {
				missing = append(missing, key)
			}
		}
		if len(missing) > 0 {
			return nil, nil, unprocessable("states", "an asset lifecycle model needs the states %s; missing: %s",
				strings.Join(requiredAssetStates, ", "), strings.Join(missing, ", "))
		}
	}
	transitions := make([]Transition, 0, len(transitionSpecs))
	for i, tr := range transitionSpecs {
		path := fmt.Sprintf("transitions[%d]", i)
		if strings.TrimSpace(tr.Key) == "" {
			return nil, nil, unprocessable(path+".key", "the transition key is required")
		}
		if !known[tr.ToStateKey] {
			return nil, nil, unprocessable(path+".to_state_key", "unknown state %q", tr.ToStateKey)
		}
		if tr.FromStateKey != "" && !known[tr.FromStateKey] {
			return nil, nil, unprocessable(path+".from_state_key", "unknown state %q", tr.FromStateKey)
		}
		guard, err := parseGuard(path+".guard", tr.Guard)
		if err != nil {
			return nil, nil, err
		}
		transitions = append(transitions, Transition{
			FromStateKey: tr.FromStateKey, ToStateKey: tr.ToStateKey,
			Key: tr.Key, Label: tr.Label, RequiredFields: tr.RequiredFields,
			Guard: guard, Validation: guardValidation(guard),
		})
	}
	return states, transitions, nil
}

// lockedFieldOfState is the column of the state itself: a state never
// locks it (a transition changes it, WP-1103).
const lockedFieldOfState = "lifecycle_state"

// checkLockedFields checks the locked fields of state i (WP-1103 AK1):
// names are set and unique, no reserved attribute namespace (_overrides,
// _observed, _provenance, _instance) and not lifecycle_state; a refusal is
// 422 on states[i].locked_fields[j].
func checkLockedFields(i int, fields []string) error {
	seen := map[string]bool{}
	for j, name := range fields {
		path := fmt.Sprintf("states[%d].locked_fields[%d]", i, j)
		refuse := func(message string) error {
			return &api.FieldError{Field: path, Message: message, Status: http.StatusUnprocessableEntity}
		}
		switch {
		case strings.TrimSpace(name) == "" || name != strings.TrimSpace(name):
			return refuse("a locked field needs a name without surrounding spaces")
		case fieldmeta.ReservedNames[name]:
			return refuse("a reserved attribute namespace cannot be locked")
		case name == lockedFieldOfState:
			return refuse("lifecycle_state changes by transitions and cannot be locked")
		case seen[name]:
			return refuse("the field is locked twice")
		}
		seen[name] = true
	}
	return nil
}

// parseGuard checks every rule of a guard with rules.Parse; a refused rule
// is 422 on path.fields.<field>.
func parseGuard(path string, spec *GuardSpec) (*Guard, error) {
	if spec == nil {
		return nil, nil
	}
	if spec.Fields == nil {
		return nil, &api.FieldError{Field: path + ".fields", Message: "the guard needs fields",
			Status: http.StatusUnprocessableEntity}
	}
	fields := make([]string, 0, len(spec.Fields))
	for field := range spec.Fields {
		fields = append(fields, field)
	}
	slices.Sort(fields)
	guard := &Guard{Fields: map[string]rules.Rule{}}
	for _, field := range fields {
		if strings.TrimSpace(field) == "" {
			return nil, &api.FieldError{Field: path + ".fields", Message: "a guard field needs a name",
				Status: http.StatusUnprocessableEntity}
		}
		rule, err := parseRule(spec.Fields[field])
		if err != nil {
			return nil, &api.FieldError{Field: path + ".fields." + field, Message: err.Error(),
				Status: http.StatusUnprocessableEntity}
		}
		guard.Fields[field] = rule
	}
	return guard, nil
}

// parseRule checks one guard rule strictly (rules.Parse).
// validation: the texts of rules.Parse name the operator and the property
// of the rule that fail, or the JSON decoding error of the request value.
func parseRule(raw []byte) (rules.Rule, error) {
	return rules.Parse(raw)
}

// specsOfStates returns the stored states as request specs (a PATCH
// without states keeps them).
func specsOfStates(states []State) []StateSpec {
	out := make([]StateSpec, 0, len(states))
	for i := range states {
		s := &states[i]
		out = append(out, StateSpec{Key: s.Key, Label: s.Label, IsInitial: s.IsInitial,
			IsTerminal: s.IsTerminal, SortOrder: s.SortOrder, RequiredFields: s.RequiredFields, LockedFields: s.LockedFields})
	}
	return out
}

// keptTransitions returns the stored transitions whose states the PATCH
// keeps, as request specs with their guard.
func keptTransitions(transitions []Transition, states []StateSpec) []TransitionSpec {
	kept := map[string]bool{}
	for _, s := range states {
		kept[s.Key] = true
	}
	out := make([]TransitionSpec, 0, len(transitions))
	for i := range transitions {
		tr := &transitions[i]
		if !kept[tr.ToStateKey] || (tr.FromStateKey != "" && !kept[tr.FromStateKey]) {
			continue
		}
		spec := TransitionSpec{FromStateKey: tr.FromStateKey, ToStateKey: tr.ToStateKey,
			Key: tr.Key, Label: tr.Label, RequiredFields: tr.RequiredFields}
		if tr.Guard != nil {
			spec.Guard = guardSpecOf(tr.Guard)
		}
		out = append(out, spec)
	}
	return out
}

// OptionsAsset handles GET /api/v1/assets/{id}/lifecycle-transitions.
func (h *Handler) OptionsAsset(w http.ResponseWriter, r *http.Request) {
	h.options(w, r, "asset")
}

// OptionsCI handles GET /api/v1/cis/{id}/lifecycle-transitions.
func (h *Handler) OptionsCI(w http.ResponseWriter, r *http.Request) {
	h.options(w, r, "ci")
}

// options answers the transitions the effective model of the entity
// allows from its current state (LifecycleTransitionOptions): a CI without
// a model is 422 (ErrNoLifecycleModel), an entity outside the scope 404.
func (h *Handler) options(w http.ResponseWriter, r *http.Request, entityType string) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if h.service == nil || h.resolver == nil {
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "lifecycle transitions are not configured")
		return
	}
	entityID, ok := api.ParseUUIDParam(w, r, "id", "entity not found")
	if !ok {
		return
	}
	modelID, err := h.resolver.LifecycleModelFor(r, t.OrganizationID, entityType, entityID)
	if errors.Is(err, ErrNoLifecycleModel) {
		writeNoLifecycleModel(w)
		return
	}
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "entity not found")
		return
	}
	def, err := h.repo.GetByID(r.Context(), t.OrganizationID, modelID)
	if err != nil {
		writeTransitionError(w, err)
		return
	}
	current, err := h.service.states.CurrentState(r.Context(), t.OrganizationID, entityType, entityID)
	if err != nil {
		writeTransitionError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, transitionOptions(def, current))
}

// transitionOptions lists the transitions of def from current; a
// transition without from state applies from every state. An entity
// without a state may enter the initial state as well (Service.Transition).
func transitionOptions(def *Definition, current string) TransitionOptions {
	stateByKey := map[string]State{}
	for i := range def.States {
		stateByKey[def.States[i].Key] = def.States[i]
	}
	out := TransitionOptions{
		Model:        ModelRef{ID: def.ID, Key: def.Key, Name: def.Name},
		CurrentState: current,
		Transitions:  []TransitionOption{},
	}
	targets := map[string]bool{}
	for i := range def.Transitions {
		tr := &def.Transitions[i]
		if tr.FromStateKey != "" && tr.FromStateKey != current {
			continue
		}
		label := tr.Label
		if label == "" {
			label = tr.Key
		}
		required := append(slices.Clone(api.NonNilSlice(stateByKey[tr.ToStateKey].RequiredFields)), tr.RequiredFields...)
		out.Transitions = append(out.Transitions, TransitionOption{
			ToState: tr.ToStateKey, Key: tr.Key, Label: label,
			RequiredFields: required, Guard: tr.Guard,
		})
		targets[tr.ToStateKey] = true
	}
	if initial := initialKey(def); current == "" && initial != "" && !targets[initial] {
		st := stateByKey[initial]
		label := st.Label
		if label == "" {
			label = initial
		}
		out.Transitions = append(out.Transitions, TransitionOption{
			ToState: initial, Key: "initial", Label: label,
			RequiredFields: slices.Clone(api.NonNilSlice(st.RequiredFields)),
		})
	}
	return out
}

// TransitionAsset handles POST /api/v1/assets/{id}/lifecycle-transitions
func (h *Handler) TransitionAsset(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, "asset")
}

// TransitionCI handles POST /api/v1/cis/{id}/lifecycle-transitions
func (h *Handler) TransitionCI(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, "ci")
}

func (h *Handler) transition(w http.ResponseWriter, r *http.Request, entityType string) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	if h.service == nil || h.resolver == nil {
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "lifecycle transitions are not configured")
		return
	}
	var req TransitionRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	entityID, ok := api.ParseUUIDParam(w, r, "id", "entity not found")
	if !ok {
		return
	}
	// lifecycle:transition after the action grants on the type and scope
	// of the CI, before the model and the guards (WP-728). Assets follow
	// with the action grants of their object kind (convention 11).
	if entityType == "ci" && !h.allowCIAction(w, r, t.OrganizationID, entityID) {
		return
	}
	modelID, err := h.resolver.LifecycleModelFor(r, t.OrganizationID, entityType, entityID)
	if errors.Is(err, ErrNoLifecycleModel) {
		writeNoLifecycleModel(w)
		return
	}
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "entity not found")
		return
	}
	result, err := h.service.Transition(r.Context(), t.OrganizationID, entityType, entityID, modelID, req)
	if err != nil {
		writeTransitionError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, result)
}

// allowCIAction checks lifecycle:transition against the action grants of
// the CI and reports whether the transition may go on: 403 with the action,
// 404 for a CI that is not visible, 500 otherwise.
func (h *Handler) allowCIAction(w http.ResponseWriter, r *http.Request, orgID, ciID string) bool {
	err := permission.CheckActionOn(r.Context(), permission.KindCI, identity.PermLifecycleTransition,
		func(ctx context.Context) ([]permission.ActionTarget, error) {
			if h.ciScope == nil {
				return nil, &api.FieldForbiddenError{Fields: []string{}, Action: string(identity.PermLifecycleTransition)}
			}
			typeID, ref, err := h.ciScope(ctx, orgID, ciID)
			if err != nil {
				return nil, err
			}
			return []permission.ActionTarget{{TypeID: typeID, Ref: ref}}, nil
		})
	switch {
	case err == nil:
		return true
	case api.WriteFieldError(w, err):
	case err.Error() == "not found":
		api.WriteError(w, http.StatusNotFound, "Not Found", "entity not found")
	default:
		api.WriteRepoError(w, err)
	}
	return false
}

// validAppliesTo are the entity kinds of a lifecycle model (the CHECK of
// lifecycle_definition.applies_to since 000201); empty means asset. "both"
// is a 422 on applies_to, not a check violation of the database.
var validAppliesTo = map[string]bool{"": true, "asset": true, "ci": true}

// writeTransitionError reports a failed transition (WP-724): failed
// required fields or guard rules are 422 lifecycle-guard with the
// violations, a failed system guard (WP-279) also with guard and missing;
// a missing edge or an unknown target state 422 invalid-transition, a from_state that is not the current state 409 with
// current_state, a refused field 422 with the field, a model that cannot
// be resolved 422, an entity that vanished 404; state store errors go
// through api.WriteRepoError (F-047).
func writeTransitionError(w http.ResponseWriter, err error) {
	var guard *GuardError
	var invalid *InvalidTransitionError
	var conflict *StateConflictError
	switch {
	case errors.As(err, &guard):
		api.WriteProblemBody(w, http.StatusUnprocessableEntity, struct {
			api.ProblemDetail
			Guard      string           `json:"guard,omitempty"`
			Missing    []string         `json:"missing,omitempty"`
			Violations []GuardViolation `json:"violations"`
		}{
			ProblemDetail: api.ProblemDetail{Type: ProblemLifecycleGuard, Title: "Unprocessable Entity",
				Status: http.StatusUnprocessableEntity, Detail: guard.Error()},
			Guard: guard.Guard, Missing: guard.Missing, Violations: guard.Violations,
		})
	case errors.As(err, &invalid):
		api.WriteProblemBody(w, http.StatusUnprocessableEntity, struct {
			api.ProblemDetail
			Field     string `json:"field"`
			FromState string `json:"from_state"`
			ToState   string `json:"to_state"`
		}{
			ProblemDetail: api.ProblemDetail{Type: ProblemInvalidTransition, Title: "Unprocessable Entity",
				Status: http.StatusUnprocessableEntity, Detail: invalid.Error()},
			Field: "to_state", FromState: invalid.From, ToState: invalid.To,
		})
	case errors.As(err, &conflict):
		api.WriteProblemBody(w, http.StatusConflict, struct {
			api.ProblemDetail
			Field        string `json:"field"`
			CurrentState string `json:"current_state"`
		}{
			ProblemDetail: api.ProblemDetail{Type: ProblemConflict, Title: "Conflict",
				Status: http.StatusConflict, Detail: conflict.Error()},
			Field: "from_state", CurrentState: conflict.CurrentState,
		})
	case api.WriteFieldError(w, err):
	case errors.Is(err, ErrNoLifecycleModel):
		writeNoLifecycleModel(w)
	case err.Error() == "not found":
		api.WriteError(w, http.StatusNotFound, "Not Found", "entity not found")
	default:
		api.WriteRepoError(w, err)
	}
}

// writeNoLifecycleModel answers a transition of an entity whose type has no
// lifecycle model: 422 on the field to_state, since such an entity has no
// lifecycle state to set (DB-05).
func writeNoLifecycleModel(w http.ResponseWriter) {
	api.WriteFieldError(w, &api.FieldError{Field: "to_state", Message: ErrNoLifecycleModel.Error(),
		Status: http.StatusUnprocessableEntity})
}
