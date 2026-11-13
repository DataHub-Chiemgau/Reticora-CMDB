package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
)

// The CI actions of a run (WFL-02, LCY-04, WP-1097): set_ci_field writes
// one or more fields of a CI as source workflow (rank 92) through the CI
// service with the field rights of the initiator, the attribute and catalog
// rules and one version step; transition_lifecycle moves a CI or an asset
// through the lifecycle service with its guards. Both write as the
// initiator of the run (workflow_run.initiated_by; none for event,
// schedule and alarm triggers) with source system and the run id as
// correlation id of their audit entries and history rows.

// setFieldMax is the largest number of fields one set_ci_field writes.
const setFieldMax = 50

// Fixed texts of the CI actions; the step stores them as its error.
var (
	errSetFieldNoFields   = errors.New("set_ci_field needs a field or fields")
	errSetFieldTooMany    = fmt.Errorf("set_ci_field writes at most %d fields", setFieldMax)
	errSetFieldNoCI       = errors.New("set_ci_field needs a ci_id")
	errSetFieldText       = errors.New("set_ci_field: this field takes a text value")
	errFieldStateUnread   = errors.New("set_ci_field: override state unreadable, nothing written")
	errLifecycleNoState   = errors.New("transition_lifecycle needs a to_state")
	errLifecycleNoTarget  = errors.New("transition_lifecycle needs a ci_id or an asset_id")
	errLifecycleEntity    = errors.New("transition_lifecycle entity must be ci or asset")
	errLifecycleContext   = errors.New("transition_lifecycle context must be an object")
	errLifecycleUnwired   = errors.New("lifecycle transitions unavailable")
	errLifecycleNotFound  = errors.New("the object of the lifecycle transition was not found")
	errLifecycleConflicts = errors.New("the lifecycle state changed: from_state is not the current state")
	errInitiatorInactive  = errors.New("the initiator of the run is no active user: nothing written")
)

// LifecycleTransitioner moves an entity (ci or asset) of the organization to
// another lifecycle state with the guards of its model (lifecycle.Service
// with its model resolver, NewLifecycleTransitioner).
type LifecycleTransitioner interface {
	Transition(ctx context.Context, orgID, entityType, entityID string, req lifecycle.TransitionRequest) (*lifecycle.TransitionResult, error)
}

// lifecycleTransitioner resolves the model of the entity like the bulk
// lifecycle action (server.bulkLifecycle) and transitions it.
type lifecycleTransitioner struct {
	svc      *lifecycle.Service
	resolver lifecycle.EntityResolver
}

// NewLifecycleTransitioner returns the transitioner of transition_lifecycle
// on the lifecycle service and its model resolver.
func NewLifecycleTransitioner(svc *lifecycle.Service, resolver lifecycle.EntityResolver) LifecycleTransitioner {
	return lifecycleTransitioner{svc: svc, resolver: resolver}
}

func (l lifecycleTransitioner) Transition(ctx context.Context, orgID, entityType, entityID string, req lifecycle.TransitionRequest) (*lifecycle.TransitionResult, error) {
	// The resolver reads the scope of a request; the route of the single
	// transition names the entity.
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/"+entityType+"s/"+entityID+"/lifecycle-transitions", http.NoBody)
	if err != nil {
		return nil, err
	}
	modelID, err := l.resolver.LifecycleModelFor(r, orgID, entityType, entityID)
	switch {
	case errors.Is(err, lifecycle.ErrNoLifecycleModel):
		return nil, lifecycle.ErrNoLifecycleModel
	case err != nil:
		return nil, errLifecycleNotFound
	}
	return l.svc.Transition(ctx, orgID, entityType, entityID, modelID, req)
}

// CIWriter reads and writes CIs for set_ci_field: the CI service
// (ci.Service) with field rights and rules, else the repository.
type CIWriter interface {
	GetByID(ctx context.Context, orgID, id string) (*ci.Item, error)
	Update(ctx context.Context, orgID, id string, req ci.UpdateRequest) (*ci.Item, error) //nolint:gocritic // signature of ci.Repository
}

// WithCIService makes set_ci_field write through the CI service: action
// and field rights, attribute and catalog rules (RBA-07, MET-45, WP-1097).
// Without it the CI repository of the executor writes.
func (e *Executor) WithCIService(w CIWriter) *Executor {
	e.ciWriter = w
	return e
}

// InitiatorPolicies resolves the policy of the initiator of a run from the
// user's current role assignments, narrowed to a scope
// (permission.FieldResolver.PolicyForCreator, as a queue job resolves its
// creator).
type InitiatorPolicies interface {
	PolicyForCreator(ctx context.Context, users permission.CreatorStatus, orgID, userID string, scope *permission.CreatorScope) (*permission.FieldPolicy, error)
}

// WithPolicies sets the resolver of the initiator's rights and the user
// status: set_ci_field and transition_lifecycle then write with the action
// and field rights the initiator holds now, within the scope of the run's
// context (RBA-07, WP-1097); an initiator who is no active user writes
// nothing. Without it a run with initiator writes under the policy of its
// context.
func (e *Executor) WithPolicies(p InitiatorPolicies, users permission.CreatorStatus) *Executor {
	e.policies, e.users = p, users
	return e
}

// WithLifecycle sets the transitioner of transition_lifecycle (LCY-04,
// WP-1097); without it the action fails with a fixed text.
func (e *Executor) WithLifecycle(t LifecycleTransitioner) *Executor {
	e.lifecycle = t
	return e
}

// actionContext is the context the CI actions of run write in: the user
// is the initiator (none without one), the audit entries carry source
// system and the run id as correlation id (AUD-07). With an initiator and a
// policy resolver the initiator's action and field rights apply; without
// an initiator the run writes as the system, without field rights.
func (e *Executor) actionContext(ctx context.Context, orgID string, run *Run) (context.Context, error) {
	ctx = asInitiator(ctx, orgID, run.InitiatedBy)
	ctx = audit.WithSource(audit.WithCorrelationID(ctx, run.ID), audit.SourceSystem)
	switch {
	case run.InitiatedBy == "":
		return permission.WithPolicy(ctx, permission.UnrestrictedPolicy(identity.AllPermissions())), nil
	case e.policies != nil:
		policy, err := e.policies.PolicyForCreator(ctx, e.users, orgID, run.InitiatedBy, initiatorScope(ctx))
		if errors.Is(err, permission.ErrCreatorInactive) {
			return nil, errInitiatorInactive
		}
		if err != nil {
			return nil, fmt.Errorf("resolve the rights of the initiator: %w", err)
		}
		return permission.WithPolicy(ctx, policy), nil
	}
	return ctx, nil
}

// initiatorScope is the scope of ctx (the request or the scope snapshot of
// the run's job) as the narrowing of the initiator's role assignments: an
// org-wide dimension is nil (unrestricted), a restricted one its ids;
// without a scope nothing is narrowed.
func initiatorScope(ctx context.Context) *permission.CreatorScope {
	scope, ok := database.TenantScopeFromContext(ctx)
	if !ok {
		return &permission.CreatorScope{}
	}
	dim := func(s database.ScopeSet) []string {
		if s.IsAll() {
			return nil
		}
		if ids := s.IDs(); ids != nil {
			return ids
		}
		return []string{}
	}
	return &permission.CreatorScope{Clients: dim(scope.Clients), Sites: dim(scope.Sites), Teams: dim(scope.Teams)}
}

// fieldDecision is the decision about one field of set_ci_field.
type fieldDecision struct {
	write    *override.Write
	decision override.Decision
}

// setCIFields implements set_ci_field: fields {<field>: <value>, …} (at
// most setFieldMax) or the short form field/value on ci_id of the action,
// else the CI of the run. Each field is decided on its own by the central
// write decision (OVR-02) with source workflow (rank 92); the admitted
// fields go in one authoritative update of the CI service (one audit
// entry, one version step, API-07). A manual override is never replaced; a
// differing value opens the override_conflict review (REC-12). The output
// names written and reason per field; a refused write of the CI service
// fails the step with its text, rule violations stand in violations.
func (e *Executor) setCIFields(ctx context.Context, orgID string, run *Run, action JSONMap) (JSONMap, bool, error) {
	cis := e.ciWriter
	if cis == nil && e.cis != nil {
		cis = e.cis
	}
	if cis == nil {
		return nil, false, fmt.Errorf("ci repository unavailable")
	}
	fields, short, err := setFields(action)
	if err != nil {
		return nil, false, err
	}
	ciID := str(action, "ci_id", run.CIID)
	if ciID == "" {
		return nil, false, errSetFieldNoCI
	}
	ctx, err = e.actionContext(ctx, orgID, run)
	if err != nil {
		return nil, false, err
	}
	item, err := cis.GetByID(ctx, orgID, ciID)
	if err != nil {
		return nil, false, err
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	req := ci.UpdateRequest{Authoritative: true}
	decisions := make([]fieldDecision, 0, len(names))
	written := false
	observed := time.Now().UTC()
	for _, name := range names {
		value := fields[name]
		current, text, typed := typedField(item, name)
		if typed {
			s, ok := value.(string)
			if !ok {
				return nil, false, fmt.Errorf("%w: %s", errSetFieldText, name)
			}
			value = s
		} else {
			current = item.Attributes[name]
		}
		w := &override.Write{OrganizationID: orgID, CIID: item.ID, Field: name, Value: value, Source: sourceWorkflow,
			ObservedAt: observed, Current: current, FallbackSource: item.DiscoverySource}
		d := e.guard.Decide(ctx, w)
		if d.Reason == override.ReasonError {
			return nil, false, fmt.Errorf("%w (%s)", errFieldStateUnread, name)
		}
		if d.Write {
			written = true
			if typed {
				v := value.(string)
				*text(&req) = &v
			} else {
				if req.Attributes == nil {
					req.Attributes = map[string]any{}
				}
				req.Attributes[name] = value
			}
		}
		decisions = append(decisions, fieldDecision{write: w, decision: d})
	}
	if written {
		if _, err = cis.Update(ctx, orgID, item.ID, req); err != nil {
			var ve *ci.ValidationError
			if errors.As(err, &ve) {
				return JSONMap{"ci_id": item.ID, "violations": ve.Violations}, false, err
			}
			return nil, false, err
		}
	}
	out := JSONMap{"ci_id": item.ID}
	perField := JSONMap{}
	var unrecorded []string
	for _, fd := range decisions {
		d, w := fd.decision, fd.write
		if d.Write || d.Conflict || d.Reason == override.ReasonOverride {
			// The update is committed: a failed provenance record must not
			// fail the step, whose retry would write the CI a second time
			// (one version step per action, AK4). The output names it.
			if rerr := e.guard.Record(ctx, w); rerr != nil {
				slog.WarnContext(ctx, "workflow: provenance of set_ci_field not recorded", "ci_id", item.ID, "field", w.Field, "error", rerr)
				unrecorded = append(unrecorded, w.Field)
			}
		}
		if d.Conflict && e.conflicts != nil {
			// The override stays; the value goes to the review queue like a
			// discovered one (REC-12). A failed review does not fail the
			// step.
			previous, hasPrevious := item.ObservedUnderOverride(w.Field)
			if _, rerr := e.conflicts.OpenOverrideConflict(ctx, &reviews.OverrideConflict{
				OrganizationID: orgID, CIID: item.ID, Field: w.Field, Value: w.Value, Source: sourceWorkflow,
				Rank: override.SourceRank(sourceWorkflow), Previous: previous, HasPrevious: hasPrevious,
			}); rerr != nil {
				slog.WarnContext(ctx, "override conflict review not created", "ci_id", item.ID, "field", w.Field, "error", rerr)
			}
		}
		perField[w.Field] = JSONMap{"written": d.Write, "reason": d.Reason}
		if short {
			out["field"], out["written"], out["reason"] = w.Field, d.Write, d.Reason
		}
	}
	out["fields"] = perField
	if len(unrecorded) > 0 {
		out["provenance_unrecorded"] = unrecorded
	}
	return out, false, nil
}

// setFields returns the fields of a set_ci_field action: fields, or the
// short form field/value (short true).
func setFields(action JSONMap) (fields map[string]any, short bool, err error) {
	if raw, has := action["fields"]; has && raw != nil {
		m, ok := raw.(map[string]any)
		if !ok || len(m) == 0 {
			return nil, false, errSetFieldNoFields
		}
		if len(m) > setFieldMax {
			return nil, false, errSetFieldTooMany
		}
		for name := range m {
			if name == "" {
				return nil, false, errSetFieldNoFields
			}
		}
		return m, false, nil
	}
	field := str(action, "field", "")
	if field == "" {
		return nil, false, errSetFieldNoFields
	}
	return map[string]any{field: action["value"]}, true, nil
}

// typedField reports whether name is status, name or a typed column of a
// CI (ci.UpdateRequest has a field of its own): its current value and the
// field of the request it is written through.
func typedField(item *ci.Item, name string) (current any, field func(*ci.UpdateRequest) **string, typed bool) {
	pick := func(v string, f func(*ci.UpdateRequest) **string) (any, func(*ci.UpdateRequest) **string, bool) {
		return v, f, true
	}
	switch name {
	case "status":
		return pick(item.Status, func(r *ci.UpdateRequest) **string { return &r.Status })
	case "name":
		return pick(item.Name, func(r *ci.UpdateRequest) **string { return &r.Name })
	case "manufacturer":
		return pick(item.Manufacturer, func(r *ci.UpdateRequest) **string { return &r.Manufacturer })
	case "model":
		return pick(item.Model, func(r *ci.UpdateRequest) **string { return &r.Model })
	case "serial_number":
		return pick(item.SerialNumber, func(r *ci.UpdateRequest) **string { return &r.SerialNumber })
	case "hardware_uuid":
		return pick(item.HardwareUUID, func(r *ci.UpdateRequest) **string { return &r.HardwareUUID })
	case "management_ip":
		return pick(item.ManagementIP, func(r *ci.UpdateRequest) **string { return &r.ManagementIP })
	case "primary_mac":
		return pick(item.PrimaryMAC, func(r *ci.UpdateRequest) **string { return &r.PrimaryMAC })
	case "hostname":
		return pick(item.Hostname, func(r *ci.UpdateRequest) **string { return &r.Hostname })
	case "fqdn":
		return pick(item.FQDN, func(r *ci.UpdateRequest) **string { return &r.FQDN })
	case "os_name":
		return pick(item.OSName, func(r *ci.UpdateRequest) **string { return &r.OSName })
	case "os_version":
		return pick(item.OSVersion, func(r *ci.UpdateRequest) **string { return &r.OSVersion })
	case "firmware_version":
		return pick(item.FirmwareVersion, func(r *ci.UpdateRequest) **string { return &r.FirmwareVersion })
	case "sys_object_id":
		return pick(item.SysObjectID, func(r *ci.UpdateRequest) **string { return &r.SysObjectID })
	}
	return nil, nil, false
}

// transitionLifecycle implements transition_lifecycle: to_state (required),
// reason, context (object), from_state (optional) and entity (ci, the
// default, on ci_id of the action, else the CI of the run; asset on
// asset_id) through the lifecycle service with its guards (LCY-04). A
// guard violation fails the step with violations, guard and missing in its
// output; a type without model and a changed state fail it with a fixed
// text.
func (e *Executor) transitionLifecycle(ctx context.Context, orgID string, run *Run, action JSONMap) (JSONMap, bool, error) {
	if e.lifecycle == nil {
		return nil, false, errLifecycleUnwired
	}
	req := lifecycle.TransitionRequest{ToState: str(action, "to_state", ""), Reason: str(action, "reason", "")}
	if req.ToState == "" {
		return nil, false, errLifecycleNoState
	}
	if raw, has := action["from_state"]; has && raw != nil {
		s, ok := raw.(string)
		if !ok {
			return nil, false, errLifecycleNoState
		}
		req.FromState = &s
	}
	if raw, has := action["context"]; has && raw != nil {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, false, errLifecycleContext
		}
		req.Context = m
	}
	entity := str(action, "entity", "ci")
	var id string
	switch entity {
	case "ci":
		id = str(action, "ci_id", run.CIID)
	case "asset":
		id = str(action, "asset_id", "")
	default:
		return nil, false, errLifecycleEntity
	}
	if !api.IsUUID(id) {
		return nil, false, errLifecycleNoTarget
	}
	ctx, err := e.actionContext(ctx, orgID, run)
	if err != nil {
		return nil, false, err
	}
	// lifecycle:transition of the initiator on the type and scope of the
	// object, as the route of the single transition checks it (WP-728).
	if aerr := e.allowTransition(ctx, orgID, entity, id); aerr != nil {
		return nil, false, aerr
	}
	res, err := e.lifecycle.Transition(ctx, orgID, entity, id, req)
	var guard *lifecycle.GuardError
	var conflict *lifecycle.StateConflictError
	switch {
	case errors.As(err, &guard):
		violations := guard.Violations
		if violations == nil {
			violations = []lifecycle.GuardViolation{}
		}
		missing := guard.Missing
		if missing == nil {
			missing = []string{}
		}
		return JSONMap{"entity": entity, "entity_id": id, "violations": violations, "guard": guard.Guard, "missing": missing}, false, err
	case errors.As(err, &conflict):
		return JSONMap{"entity": entity, "entity_id": id, "current_state": conflict.CurrentState}, false, errLifecycleConflicts
	case err != nil:
		return nil, false, err
	}
	return JSONMap{"entity": res.EntityType, "entity_id": res.EntityID, "from_state": res.FromState, "to_state": res.ToState}, false, nil
}

// allowTransition checks lifecycle:transition with the policy of ctx (the
// initiator's, actionContext): for a CI on its type and scope, read in the
// scope of the run; an asset on itself without type. A refusal fails the
// step with the text of the check.
func (e *Executor) allowTransition(ctx context.Context, orgID, entity, id string) error {
	kind := permission.KindAsset
	load := func(context.Context) ([]permission.ActionTarget, error) {
		return []permission.ActionTarget{{Ref: permission.ObjectRef{ID: id}}}, nil
	}
	if entity == "ci" {
		kind = permission.KindCI
		load = func(ctx context.Context) ([]permission.ActionTarget, error) {
			if e.cis == nil {
				return nil, permission.ErrNoActionTarget
			}
			item, err := e.cis.GetByID(ctx, orgID, id)
			if errors.Is(err, ci.ErrNotFound) {
				return nil, errLifecycleNotFound
			}
			if err != nil {
				return nil, err
			}
			return []permission.ActionTarget{{TypeID: item.CITypeID, Ref: permission.ObjectRef{ID: item.ID, ClientID: item.ClientID, SiteID: item.SiteID}}}, nil
		}
	}
	return permission.CheckActionOn(ctx, kind, identity.PermLifecycleTransition, load)
}
