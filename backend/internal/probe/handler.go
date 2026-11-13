package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// REST of the probe definitions (PRB-01, PRT-01, WP-939, E-160):
//
//	GET    /api/v1/monitoring/probes             list (filters ci_id, type, collector_group_id, client_id, enabled)
//	POST   /api/v1/monitoring/probes             create
//	GET    /api/v1/monitoring/probes/{id}        read
//	PATCH  /api/v1/monitoring/probes/{id}        change target, params, enabled, parent, collector group
//	DELETE /api/v1/monitoring/probes/{id}        delete
//	GET    /api/v1/monitoring/probe-types        the register api/probe-types.json (WP-934)
//	GET    /api/v1/monitoring/probe-types/{key}  one entry of the register
//
// A write checks in its transaction: the references (CI, client, site,
// collector group, parent probe) in the caller's scope, unknown and
// invisible alike 422; the type known and available, the override set of
// the level probe and the effective values against the type limits
// (probewire.Validate, timeout below interval), every violation 422 with
// field; the target fitting the type; the parent chain without cycle (409)
// and at most MaxProbeDepth levels deep (422), under the advisory lock
// probe_parent:<org>. A create, a change and an enable are admitted against
// the probe budget of the organization (PKG-04, WP-940; BudgetGate): the
// first lock of the transaction is the budget lock database.LockQuota(ctx,
// org, "probe_budget"), exclusive, before every row lock; the budget before
// and after the write is counted in the same transaction over every active
// probe of the organization (a create before its insert, a change after the
// row lock of the changed probe only, before its update); a raise above a
// quota is 403 entitlement-limit, an interval below min_probe_interval_s
// 422 on interval_s, also on a disabled probe. A delete or a disable raises
// nothing. The last write of the
// transaction is the event
// config.changed (WP-867, Konvention 20); the route audit writes the entry
// with the snapshot of the probe row (audit.routeSnapshots).

// MaxProbeDepth is the deepest chain of parent probes (levels, the root
// included) a write may build.
const MaxProbeDepth = 8

// Errors of the probe writes; the handler maps them.
var (
	// ErrParentCycle: the parent would make the probe its own ancestor
	// (also the probe itself as parent), 409.
	ErrParentCycle = &api.ConflictError{Field: "parent_probe_id", Message: "the parent probe would create a cycle"}
	// ErrTargetTaken: the client, CI, type and target are taken by another
	// probe (probe_target_key), 409.
	ErrTargetTaken = &api.ConflictError{Field: "target", Message: "a probe of this type for this target already exists"}
)

// unprocessable returns the 422 of a field.
func unprocessable(field, message string) *api.FieldError {
	return &api.FieldError{Field: field, Message: message, Status: http.StatusUnprocessableEntity}
}

// ValidationError is the 422 of a write whose values break the type limits
// or the parameter schema: every violation with its field.
type ValidationError struct {
	Violations []probewire.FieldError
}

// validation: the first violation, field and message of probewire.Validate.
func (e *ValidationError) Error() string {
	if len(e.Violations) == 0 {
		return "invalid probe"
	}
	return e.Violations[0].Error()
}

// WriteTx is the view of a write transaction the checks of a write read
// through: everything in the caller's scope (RLS) unless said otherwise.
type WriteTx interface {
	// CI returns client and site of a visible CI; ok false when the CI is
	// missing or invisible.
	CI(ctx context.Context, id string) (clientID, siteID string, ok bool, err error)
	// ClientVisible reports whether the client exists in the caller's
	// scope.
	ClientVisible(ctx context.Context, id string) (bool, error)
	// Site returns the client of a visible site (empty for an org-wide
	// site); ok false when the site is missing or invisible.
	Site(ctx context.Context, id string) (clientID string, ok bool, err error)
	// CollectorGroup returns the client of a visible collector group
	// (empty for an org-wide group).
	CollectorGroup(ctx context.Context, id string) (clientID string, ok bool, err error)
	// CollectorGroupsOfClient lists the visible collector groups whose
	// client is clientID (org-wide groups for an empty clientID).
	CollectorGroupsOfClient(ctx context.Context, clientID string) ([]string, error)
	// ProbeVisible reports whether the probe exists in the caller's scope.
	ProbeVisible(ctx context.Context, id string) (bool, error)
	// Ancestors returns the chain of parents from id upwards (id first),
	// read across the client and site scope (only ids leave it), at most
	// limit entries; a repeated id ends it.
	Ancestors(ctx context.Context, id string, limit int) ([]string, error)
	// SubtreeHeight returns how many levels of children lie below id (0
	// without children), read across the client and site scope, at most
	// limit.
	SubtreeHeight(ctx context.Context, id string, limit int) (int, error)
	// Layers returns the override levels above the probe for its type.
	Layers(ctx context.Context, p *Probe) (Layers, error)
}

// Check is the check of a write in its transaction: next is the probe to
// store (complete; the check may fill client, site and collector group),
// current the stored one (nil for a create).
type Check func(ctx context.Context, tx WriteTx, next, current *Probe) error

// Store writes probe definitions; PGRepository and MemoryRepository
// implement it with the same rules.
type Store interface {
	Repository
	// Count counts the probes matching f (Limit and Offset ignored).
	Count(ctx context.Context, f *ListFilter) (int, error)
	// Create stores p after check, in one transaction with the event
	// config.changed.
	Create(ctx context.Context, p *Probe, check Check) (*Probe, error)
	// Update changes the probe: change derives the next state from the
	// current one and names the changed fields; check runs on it.
	Update(ctx context.Context, id string, change func(current *Probe) (*Probe, []string, error), check Check) (*Probe, error)
	// Delete removes the probe (ErrNotFound when missing or invisible).
	Delete(ctx context.Context, id string) error
	// SetBudget makes Create and Update admit the probe budget with gate
	// and the resolution raise intervals to the plan minimum (WP-940);
	// nil admits every write.
	SetBudget(gate BudgetGate)
}

// BudgetGate admits the probe budget of an organization
// (entitlement.Service, PKG-04, WP-940).
type BudgetGate interface {
	// AllowProbeChange refuses a raise of the budget above a quota (403
	// entitlement-limit) or an interval below min_probe_interval_s (422
	// on interval_s).
	AllowProbeChange(ctx context.Context, orgID string, before, after entitlement.ProbeBudget) error
	// MinProbeIntervalS returns min_probe_interval_s (0 without one).
	MinProbeIntervalS(ctx context.Context, orgID string) (int64, error)
	// CheckInterval refuses an interval below min_probe_interval_s (422
	// on interval_s; a monitoring profile, WP-946 AK6).
	CheckInterval(ctx context.Context, orgID string, seconds int) error
}

// Handler serves the probe routes.
type Handler struct {
	store Store
}

// NewHandler returns the handler on store.
func NewHandler(store Store) *Handler { return &Handler{store: store} }

// WithBudget admits the writes against the probe budget of gate (WP-940).
func (h *Handler) WithBudget(gate BudgetGate) *Handler {
	h.store.SetBudget(gate)
	return h
}

// RegisterRoutes mounts the routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/monitoring/probes", h.list)
	r.Post("/api/v1/monitoring/probes", h.create)
	r.Get("/api/v1/monitoring/probes/{id}", h.get)
	r.Patch("/api/v1/monitoring/probes/{id}", h.update)
	r.Delete("/api/v1/monitoring/probes/{id}", h.delete)
	r.Get("/api/v1/monitoring/probe-types", h.listTypes)
	r.Get("/api/v1/monitoring/probe-types/{key}", h.getType)
	h.registerProfileRoutes(r)
}

// requireOrg answers 401 without tenant.
func requireOrg(w http.ResponseWriter, r *http.Request) bool {
	if tenant.FromContext(r.Context()).OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return false
	}
	return true
}

// writeError maps the errors of the probe store.
func writeError(w http.ResponseWriter, err error) {
	var ve *ValidationError
	switch {
	case api.WriteEntitlementError(w, err):
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "probe not found")
	case errors.As(err, &ve):
		violations := make([]api.Violation, 0, len(ve.Violations))
		for _, v := range ve.Violations {
			violations = append(violations, api.Violation{Field: v.Field, Detail: v.Message})
		}
		field := ""
		if len(violations) > 0 {
			field = violations[0].Field
		}
		api.WriteProblemBody(w, http.StatusUnprocessableEntity, struct {
			api.FieldProblem
			Violations []api.Violation `json:"violations"`
		}{
			FieldProblem: api.FieldProblem{ProblemDetail: api.ProblemDetail{Type: "urn:reticora:problem:invalid-field",
				Title: http.StatusText(http.StatusUnprocessableEntity), Status: http.StatusUnprocessableEntity, Detail: ve.Error()},
				Field: field},
			Violations: violations,
		})
	default:
		api.WriteRepoError(w, err)
	}
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	if !requireOrg(w, r) {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := &ListFilter{CIID: q.Get("ci_id"), Type: q.Get("type"), CollectorGroupID: q.Get("collector_group_id"), ClientID: q.Get("client_id")}
	if v := q.Get("enabled"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "enabled must be true or false")
			return
		}
		f.Enabled = &b
	}
	total, err := h.store.Count(r.Context(), f)
	if err != nil {
		writeError(w, err)
		return
	}
	f.Limit, f.Offset = page.Limit, page.Offset
	items, err := h.store.List(r.Context(), f)
	if err != nil {
		writeError(w, err)
		return
	}
	for i := range items {
		items[i].Params = normalizeParams(items[i].Params)
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Probe]{Data: items, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+len(items) < total})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	if !requireOrg(w, r) {
		return
	}
	p, err := h.store.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, err)
		return
	}
	p.Params = normalizeParams(p.Params)
	api.WriteJSON(w, http.StatusOK, p)
}

// createRequest is the body of POST /monitoring/probes.
type createRequest struct {
	Type             string          `json:"type"`
	Target           string          `json:"target"`
	CIID             string          `json:"ci_id"`
	ClientID         string          `json:"client_id"`
	SiteID           string          `json:"site_id"`
	CollectorGroupID string          `json:"collector_group_id"`
	Params           json.RawMessage `json:"params"`
	Enabled          *bool           `json:"enabled"`
	ParentProbeID    string          `json:"parent_probe_id"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	if !requireOrg(w, r) {
		return
	}
	var req createRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid request body")
		return
	}
	info := tenant.FromContext(r.Context())
	p := &Probe{
		OrganizationID: info.OrganizationID, Type: strings.TrimSpace(req.Type), Target: strings.TrimSpace(req.Target),
		CIID: req.CIID, ClientID: req.ClientID, SiteID: req.SiteID, CollectorGroupID: req.CollectorGroupID,
		Params: req.Params, Enabled: req.Enabled == nil || *req.Enabled, ParentProbeID: req.ParentProbeID,
	}
	if api.IsUUID(info.UserID) {
		p.CreatedBySurrogate = info.UserID
	}
	if err := checkStatic(p); err != nil {
		writeError(w, err)
		return
	}
	created, err := h.store.Create(r.Context(), p, checkWrite)
	if err != nil {
		writeError(w, err)
		return
	}
	created.Params = normalizeParams(created.Params)
	api.WriteJSON(w, http.StatusCreated, created)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	if !requireOrg(w, r) {
		return
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body == nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid request body")
		return
	}
	if len(body) == 0 {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "name at least one field to change")
		return
	}
	change, err := parseUpdate(body)
	if err != nil {
		writeError(w, err)
		return
	}
	updated, err := h.store.Update(r.Context(), chi.URLParam(r, "id"), change, checkWrite)
	if err != nil {
		writeError(w, err)
		return
	}
	updated.Params = normalizeParams(updated.Params)
	api.WriteJSON(w, http.StatusOK, updated)
}

// parseUpdate reads the fields of a PATCH (target, params, enabled,
// parent_probe_id, collector_group_id) into a change of the stored probe.
func parseUpdate(body map[string]json.RawMessage) (func(current *Probe) (*Probe, []string, error), error) {
	var (
		target, group, parent *string
		params                json.RawMessage
		enabled               *bool
	)
	str := func(field string, raw json.RawMessage, nullable bool) (*string, error) {
		if nullable && string(bytes.TrimSpace(raw)) == "null" {
			empty := ""
			return &empty, nil
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, unprocessable(field, field+" must be a string")
		}
		return &s, nil
	}
	for field, raw := range body {
		var err error
		switch field {
		case "target":
			target, err = str(field, raw, false)
		case "collector_group_id":
			group, err = str(field, raw, false)
		case "parent_probe_id":
			parent, err = str(field, raw, true)
		case "params":
			params = raw
		case "enabled":
			var b bool
			if err = json.Unmarshal(raw, &b); err != nil {
				err = unprocessable(field, "enabled must be a boolean")
			}
			enabled = &b
		default:
			err = unprocessable(field, "only target, params, enabled, parent_probe_id and collector_group_id can be changed")
		}
		if err != nil {
			return nil, err
		}
	}
	return func(current *Probe) (*Probe, []string, error) {
		next := *current
		var changed []string
		if target != nil && strings.TrimSpace(*target) != current.Target {
			next.Target = strings.TrimSpace(*target)
			changed = append(changed, "target")
		}
		if group != nil && *group != current.CollectorGroupID {
			next.CollectorGroupID = *group
			changed = append(changed, "collector_group_id")
			if next.CollectorGroupID == "" {
				return nil, nil, unprocessable("collector_group_id", "collector_group_id must name a collector group")
			}
		}
		if parent != nil && *parent != current.ParentProbeID {
			next.ParentProbeID = *parent
			changed = append(changed, "parent_probe_id")
		}
		if enabled != nil && *enabled != current.Enabled {
			next.Enabled = *enabled
			changed = append(changed, "enabled")
		}
		if params != nil {
			next.Params = params
			changed = append(changed, changedParams(current.Params, params)...)
		}
		if err := checkStatic(&next); err != nil {
			return nil, nil, err
		}
		return &next, changed, nil
	}, nil
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	if !requireOrg(w, r) {
		return
	}
	if err := h.store.Delete(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// TypeEntry is an entry of the register as GET /probe-types returns it.
type TypeEntry struct {
	Key string `json:"key"`
	probewire.Type
}

func (h *Handler) listTypes(w http.ResponseWriter, r *http.Request) {
	if !requireOrg(w, r) {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	types := probewire.Types()
	out := make([]TypeEntry, 0, len(types))
	for i := range types {
		out = append(out, TypeEntry{Key: types[i].Key, Type: types[i]})
	}
	api.WriteJSON(w, http.StatusOK, api.Page(out, page))
}

func (h *Handler) getType(w http.ResponseWriter, r *http.Request) {
	if !requireOrg(w, r) {
		return
	}
	t, ok := probewire.Lookup(chi.URLParam(r, "key"))
	if !ok {
		api.WriteError(w, http.StatusNotFound, "Not Found", "probe type not found")
		return
	}
	api.WriteJSON(w, http.StatusOK, TypeEntry{Key: t.Key, Type: t})
}

// normalizeParams answers {} for a probe without overrides.
func normalizeParams(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return json.RawMessage(`{}`)
	}
	return raw
}

// changedParams names the override keys whose value differs between two
// override sets.
func changedParams(before, after json.RawMessage) []string {
	a, b := ParseOverrides(before), ParseOverrides(after)
	var out []string
	for k, v := range b {
		if old, ok := a[k]; !ok || !jsonEqual(old, v) {
			out = append(out, k)
		}
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func jsonEqual(a, b any) bool {
	x, errA := json.Marshal(a)
	y, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(x, y)
}

// createdFields names the settings of a new probe for config.changed.
func createdFields(p *Probe) []string {
	out := []string{"type", "target", "enabled", "collector_group_id"}
	if p.CIID != "" {
		out = append(out, "ci_id")
	}
	if p.ParentProbeID != "" {
		out = append(out, "parent_probe_id")
	}
	return append(out, changedParams(nil, p.Params)...)
}

// DeletedField is the changed field config.changed names for a deleted
// probe.
const DeletedField = "deleted"

// budgetTarget is the monitored target of a probe for the budget: the CI,
// or without one the host of the target within the probe's client (an
// icmp and a tcp probe of one host are one target).
func budgetTarget(p *Probe) string {
	if p.CIID != "" {
		return "ci:" + p.CIID
	}
	host := p.Target
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return "host:" + p.ClientID + ":" + strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
}

// budgetSpec is the part of the effective values the budget counts.
func budgetSpec(p *Probe, e *EffectiveSpec) entitlement.EffectiveSpec {
	return entitlement.EffectiveSpec{Type: p.Type, Target: budgetTarget(p), IntervalS: e.IntervalS.Int(), Active: p.Enabled}
}

// admitBudget admits a write against the budget (WP-940 AK2, AK3): active
// are the active probes of the organization as stored, read in the write
// transaction after the budget lock, layersOf their override levels with
// the plan minimum; current is the stored probe (nil for a create), next
// the probe to store with its levels nextLayers (plan minimum set). The
// stored probes run at their effective interval, raised to the plan
// minimum; next runs at the interval the write asks for, unless the write
// leaves the interval as it was on a running probe (stock below the plan
// minimum stays changeable). Enabling a probe or asking for another
// interval checks the interval the write asks for, also on a disabled
// probe (PKG-04: an interval below min_probe_interval_s is 422 on
// interval_s, so a disabled probe cannot carry one past the check); a
// disabled next counts nothing else (active may be nil).
func admitBudget(ctx context.Context, gate BudgetGate, orgID string, active []Probe, layersOf func(*Probe) Layers,
	current, next *Probe, nextLayers Layers) error {
	raw := nextLayers
	raw.PlanMinIntervalS = 0
	e := Resolve(next, raw)
	asked := current == nil || !current.Enabled || Resolve(current, raw).IntervalS.Int() != e.IntervalS.Int()
	if !next.Enabled {
		if !asked || nextLayers.PlanMinIntervalS <= 0 || e.IntervalS.Int() >= nextLayers.PlanMinIntervalS {
			return nil
		}
		// Only the interval rises: AllowProbeChange checks it (422).
		return gate.AllowProbeChange(ctx, orgID, entitlement.ProbeBudget{}, entitlement.ProbeBudget{MinIntervalS: e.IntervalS.Int()})
	}
	if !asked {
		e = Resolve(next, nextLayers)
	}
	before := make([]entitlement.EffectiveSpec, 0, len(active))
	after := make([]entitlement.EffectiveSpec, 0, len(active)+1)
	for i := range active {
		p := &active[i]
		stored := Resolve(p, layersOf(p))
		spec := budgetSpec(p, &stored)
		before = append(before, spec)
		if current == nil || p.ID != current.ID {
			after = append(after, spec)
		}
	}
	after = append(after, budgetSpec(next, &e))
	return gate.AllowProbeChange(ctx, orgID, entitlement.BudgetOf(before), entitlement.BudgetOf(after))
}

// checkStatic checks what needs no read: the reference ids are UUIDs, the
// type is known and available, params is an object and the target is
// plain text of at most 1024 characters.
func checkStatic(p *Probe) error {
	for _, ref := range []struct{ field, id string }{
		{"ci_id", p.CIID}, {"client_id", p.ClientID}, {"site_id", p.SiteID},
		{"collector_group_id", p.CollectorGroupID}, {"parent_probe_id", p.ParentProbeID},
	} {
		if ref.id != "" && !api.IsUUID(ref.id) {
			return unprocessable(ref.field, ref.field+" not found")
		}
	}
	if p.ID != "" && p.ParentProbeID == p.ID {
		return ErrParentCycle
	}
	t, ok := probewire.Lookup(p.Type)
	switch {
	case p.Type == "":
		return &ValidationError{Violations: []probewire.FieldError{{Field: "type", Message: "type is required"}}}
	case !ok:
		return &ValidationError{Violations: []probewire.FieldError{{Field: "type", Message: fmt.Sprintf("unknown probe type %q", p.Type)}}}
	case !t.Available():
		return &ValidationError{Violations: []probewire.FieldError{{Field: "type",
			Message: fmt.Sprintf("probe type %s is planned and cannot be used yet", p.Type)}}}
	}
	if len(bytes.TrimSpace(p.Params)) == 0 || string(bytes.TrimSpace(p.Params)) == "null" {
		p.Params = json.RawMessage(`{}`)
	}
	if fe := targetText(p.Target); fe != nil {
		return &ValidationError{Violations: []probewire.FieldError{*fe}}
	}
	if errs := ValidateLayer(p.Type, p.Params); len(errs) > 0 {
		return &ValidationError{Violations: errs}
	}
	return nil
}

// targetText checks the target as text.
func targetText(target string) *probewire.FieldError {
	switch {
	case target == "":
		return &probewire.FieldError{Field: "target", Message: "target is required"}
	case len(target) > 1024:
		return &probewire.FieldError{Field: "target", Message: "target must be at most 1024 characters"}
	}
	for _, r := range target {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return &probewire.FieldError{Field: "target", Message: "target must not contain spaces or control characters"}
		}
	}
	return nil
}

// checkWrite is the check of a create and an update in the write
// transaction.
func checkWrite(ctx context.Context, tx WriteTx, next, current *Probe) error {
	if current == nil {
		if err := checkScope(ctx, tx, next); err != nil {
			return err
		}
	}
	if current == nil || next.CollectorGroupID != current.CollectorGroupID {
		if err := checkCollectorGroup(ctx, tx, next); err != nil {
			return err
		}
	}
	if next.ParentProbeID != "" && (current == nil || next.ParentProbeID != current.ParentProbeID) {
		if err := checkParent(ctx, tx, next, current != nil); err != nil {
			return err
		}
	}
	layers, err := tx.Layers(ctx, next)
	if err != nil {
		return err
	}
	return validateEffective(next, layers)
}

// checkScope takes client and site from the CI, or checks client and site
// of a probe without CI.
func checkScope(ctx context.Context, tx WriteTx, p *Probe) error {
	if p.CIID != "" {
		client, site, ok, err := tx.CI(ctx, p.CIID)
		if err != nil {
			return err
		}
		if !ok {
			return unprocessable("ci_id", "ci not found")
		}
		if p.ClientID != "" && p.ClientID != client {
			return unprocessable("client_id", "client_id must be the client of the CI")
		}
		if p.SiteID != "" && p.SiteID != site {
			return unprocessable("site_id", "site_id must be the site of the CI")
		}
		p.ClientID, p.SiteID = client, site
		return nil
	}
	if p.ClientID == "" {
		return unprocessable("client_id", "client_id is required without ci_id")
	}
	ok, err := tx.ClientVisible(ctx, p.ClientID)
	if err != nil {
		return err
	}
	if !ok {
		return unprocessable("client_id", "client not found")
	}
	if p.SiteID != "" {
		client, ok, err := tx.Site(ctx, p.SiteID)
		if err != nil {
			return err
		}
		if !ok || (client != "" && client != p.ClientID) {
			return unprocessable("site_id", "site not found or not a site of the client")
		}
	}
	return nil
}

// checkCollectorGroup checks the named group (the probe's client or
// org-wide) or takes the only group of the probe's client.
func checkCollectorGroup(ctx context.Context, tx WriteTx, p *Probe) error {
	if p.CollectorGroupID == "" {
		groups, err := tx.CollectorGroupsOfClient(ctx, p.ClientID)
		if err != nil {
			return err
		}
		if len(groups) != 1 {
			return unprocessable("collector_group_id",
				fmt.Sprintf("the client has %d collector groups; name the collector group", len(groups)))
		}
		p.CollectorGroupID = groups[0]
		return nil
	}
	client, ok, err := tx.CollectorGroup(ctx, p.CollectorGroupID)
	if err != nil {
		return err
	}
	if !ok || (client != "" && client != p.ClientID) {
		return unprocessable("collector_group_id", "collector group not found or not one of the probe's client")
	}
	return nil
}

// checkParent checks the parent: visible, no cycle, chain at most
// MaxProbeDepth levels with the probe and its children.
func checkParent(ctx context.Context, tx WriteTx, p *Probe, stored bool) error {
	if p.ParentProbeID == p.ID {
		return ErrParentCycle
	}
	ok, err := tx.ProbeVisible(ctx, p.ParentProbeID)
	if err != nil {
		return err
	}
	if !ok {
		return unprocessable("parent_probe_id", "parent probe not found")
	}
	ancestors, err := tx.Ancestors(ctx, p.ParentProbeID, MaxProbeDepth+1)
	if err != nil {
		return err
	}
	for _, a := range ancestors {
		if stored && a == p.ID {
			return ErrParentCycle
		}
	}
	height := 0
	if stored {
		if height, err = tx.SubtreeHeight(ctx, p.ID, MaxProbeDepth); err != nil {
			return err
		}
	}
	if len(ancestors)+1+height > MaxProbeDepth {
		return unprocessable("parent_probe_id", fmt.Sprintf("the chain of parent probes must be at most %d levels deep", MaxProbeDepth))
	}
	return nil
}

// validateEffective checks the effective values of the probe (WP-937)
// against the type limits with probewire.Validate, the timeout before the
// resolution lowers it below the interval, and the target against the
// type.
func validateEffective(p *Probe, layers Layers) error {
	t, ok := probewire.Lookup(p.Type)
	if !ok {
		return &ValidationError{Violations: []probewire.FieldError{{Field: "type", Message: fmt.Sprintf("unknown probe type %q", p.Type)}}}
	}
	own := ParseOverrides(p.Params)
	e := resolveWith(&t, p, own, layers)
	spec := e.ProbeSpec(0)
	r := resolver{own: own, layers: layers, groups: rankedGroups(layers.Groups)}
	spec.TimeoutMS = r.intField(KeyTimeoutS, t.DefaultTimeoutS, t.MinTimeoutS, t.MaxTimeoutS).Int() * 1000
	errs := probewire.Validate(&spec)
	if fe := checkTarget(p.Type, p.Target, e.Params); fe != nil {
		errs = append(errs, *fe)
	}
	if len(errs) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]probewire.FieldError, 0, len(errs))
	for _, fe := range errs {
		if !seen[fe.Field] {
			seen[fe.Field] = true
			out = append(out, fe)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Field < out[j].Field })
	return &ValidationError{Violations: out}
}

// checkTarget checks the target against the type: icmp an IP address or a
// host name, tcp a host with a port 1-65535 (in the target or as the
// parameter port); the other types are planned and keep the text check.
func checkTarget(typ, target string, params map[string]Field) *probewire.FieldError {
	if fe := targetText(target); fe != nil {
		return fe
	}
	switch typ {
	case "icmp":
		if !validHost(target) {
			return &probewire.FieldError{Field: "target", Message: "target must be an IP address or a host name"}
		}
	case "tcp":
		host, port, err := net.SplitHostPort(target)
		if err != nil {
			// No port in the target: the parameter port must give one.
			host, port = strings.TrimSuffix(strings.TrimPrefix(target, "["), "]"), ""
			if f, ok := params["port"]; ok && f.Value != nil {
				port = fmt.Sprint(f.Value)
			}
		}
		if !validHost(host) {
			return &probewire.FieldError{Field: "target", Message: "target must be host:port with an IP address or a host name"}
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return &probewire.FieldError{Field: "target", Message: "target needs a port between 1 and 65535 (host:port or params.port)"}
		}
	}
	return nil
}

// validHost reports whether s is an IP address or a host name after RFC
// 1123 (labels of letters, digits and hyphens, at most 253 characters).
func validHost(s string) bool {
	if _, err := netip.ParseAddr(s); err == nil {
		return true
	}
	s = strings.TrimSuffix(s, ".")
	if s == "" || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}
