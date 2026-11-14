package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Event triggers of workflows (WFL-02, WFL-03, WP-1090, E-226 option 2):
// the durable JetStream consumer workflow-triggers reads the domain events
// of the stream RETICORA_EVENTS; every replica pulls from the same
// durable. An event starts one run per active definition whose trigger is
// {"type": "event", "event": <type or list>} and whose trigger.filter
// (MET-45) holds over the run context. A repeated delivery starts no
// second run (uq_workflow_run_trigger_event). An event a workflow run wrote
// (Envelope.Origin) starts runs only of definitions with
// trigger.allow_workflow_origin, up to triggerMaxDepth. lifecycle.changed
// has a shorthand with state filters (normalizeTrigger, WP-1104) and puts
// the transition and its CI or asset into the run context.

// TriggerConsumerName is the durable consumer of the event triggers.
const TriggerConsumerName = "workflow-triggers"

// Settings of the durable consumer (AK1).
const (
	triggerMaxDeliver = 10
	triggerAckWait    = time.Minute
)

// triggerMaxDepth is the deepest run an event of a run starts (AK5).
const triggerMaxDepth = 3

// Cache periods of the definitions and of the feature per organization.
const (
	triggerDefinitionsTTL = 15 * time.Second
	triggerFeatureTTL     = 60 * time.Second
)

// TriggerEvent is the trigger of a run started by an event; the run's
// trigger column holds the event type.
const TriggerEvent = "event"

var (
	triggerDead = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "reticora_workflow_trigger_dead_total",
		Help: "Events the workflow trigger consumer terminated after the last delivery attempt.",
	})
	triggerLoop = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "reticora_workflow_trigger_loop_total",
		Help: "Workflow runs not started because the event of a run exceeded the trigger depth.",
	})
)

// EventStore reads the event definitions and stores the runs of events.
type EventStore interface {
	// EventDefinitions lists the active definitions whose trigger type is
	// event.
	EventDefinitions(ctx context.Context, orgID string) ([]Definition, error)
	// CreateEventRun stores run unless a run of its definition version for
	// its event exists (created false).
	CreateEventRun(ctx context.Context, run *Run) (bool, error)
	// EventRun returns the run of the definition version for the event,
	// ErrRunNotFound without one.
	EventRun(ctx context.Context, orgID, versionID, eventID string) (*Run, error)
}

// TriggerConsumer starts the runs of the event triggers.
type TriggerConsumer struct {
	exec    *Executor
	store   EventStore
	js      jetstream.JetStream
	enabled func(ctx context.Context, orgID string) bool
	now     func() time.Time
	// Stream is the stream the consumer reads (events.StreamName);
	// Durable and Filter name its consumer (TriggerConsumerConfig; tests
	// use a stream of their own).
	Stream, Durable, Filter string

	mu       sync.Mutex
	defs     map[string]cachedDefinitions
	features map[string]cachedFeature
}

type cachedDefinitions struct {
	defs []Definition
	at   time.Time
}

type cachedFeature struct {
	on bool
	at time.Time
}

// NewTriggerConsumer returns the consumer of the event triggers: runs of
// exec (with its queue) for the definitions of store in organizations
// with the workflow feature (enabled).
func NewTriggerConsumer(exec *Executor, store EventStore, js jetstream.JetStream, enabled func(ctx context.Context, orgID string) bool) *TriggerConsumer {
	cfg := TriggerConsumerConfig()
	return &TriggerConsumer{exec: exec, store: store, js: js, enabled: enabled, now: time.Now, Stream: events.StreamName,
		Durable: cfg.Durable, Filter: cfg.FilterSubject, defs: map[string]cachedDefinitions{}, features: map[string]cachedFeature{}}
}

// TriggerConsumerConfig is the durable consumer of the event triggers on
// the stream RETICORA_EVENTS (AK1).
func TriggerConsumerConfig() jetstream.ConsumerConfig {
	return jetstream.ConsumerConfig{
		Durable:       TriggerConsumerName,
		FilterSubject: events.SubjectPrefix + "*.>",
		AckPolicy:     jetstream.AckExplicitPolicy,
		MaxDeliver:    triggerMaxDeliver,
		AckWait:       triggerAckWait,
	}
}

// Run pulls the events until ctx ends; a failed setup is retried every
// ten seconds (the stream may not exist yet).
func (c *TriggerConsumer) Run(ctx context.Context) {
	for {
		err := c.consume(ctx)
		if ctx.Err() != nil {
			return
		}
		slog.Warn("workflow trigger consumer", "error", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

func (c *TriggerConsumer) consume(ctx context.Context) error {
	if c.js == nil {
		return errors.New("no JetStream connection")
	}
	cfg := TriggerConsumerConfig()
	cfg.Durable, cfg.FilterSubject = c.Durable, c.Filter
	consumer, err := c.js.CreateOrUpdateConsumer(ctx, c.Stream, cfg)
	if err != nil {
		return fmt.Errorf("ensure consumer %s: %w", TriggerConsumerName, err)
	}
	cc, err := consumer.Consume(func(msg jetstream.Msg) { c.process(ctx, msg) },
		jetstream.ConsumeErrHandler(func(_ jetstream.ConsumeContext, cerr error) {
			slog.Warn("consume workflow triggers", "error", cerr)
		}))
	if err != nil {
		return fmt.Errorf("consume %s: %w", TriggerConsumerName, err)
	}
	<-ctx.Done()
	cc.Stop()
	return nil
}

// process handles one message: acknowledged when handled, a malformed one
// terminated at once; a failure is delivered again until MaxDeliver, then
// terminated and counted.
func (c *TriggerConsumer) process(ctx context.Context, msg jetstream.Msg) {
	var env events.Envelope
	if err := json.Unmarshal(msg.Data(), &env); err != nil || env.OrgID == "" || env.ID == "" {
		slog.Warn("workflow trigger: malformed event", "subject", msg.Subject())
		_ = msg.Term()
		triggerDead.Inc()
		return
	}
	if err := c.Handle(ctx, &env); err != nil {
		slog.Warn("workflow trigger", "event_id", env.ID, "type", env.Type, "error", err)
		if meta, merr := msg.Metadata(); merr == nil && meta.NumDelivered >= triggerMaxDeliver {
			_ = msg.Term()
			triggerDead.Inc()
			return
		}
		_ = msg.Nak()
		return
	}
	_ = msg.Ack()
}

// Handle starts the runs of one event (AK2 to AK5) and returns an error
// only when the delivery should be repeated.
func (c *TriggerConsumer) Handle(ctx context.Context, env *events.Envelope) error {
	scope := database.OrgWideScope(env.OrgID, "")
	octx := tenant.WithTenant(database.ContextWithTenantScope(database.WithoutTx(ctx), &scope), tenant.TenantInfo{OrganizationID: env.OrgID})
	defs, err := c.definitions(octx, env.OrgID)
	if err != nil {
		return err
	}
	var matching []Definition
	for i := range defs {
		if eventMatches(defs[i].Trigger, env.Type) {
			matching = append(matching, defs[i])
		}
	}
	if len(matching) == 0 || !c.featureOn(octx, env.OrgID) {
		return nil
	}
	fromRun := env.Origin != nil && env.Origin.Kind == events.OriginWorkflowRun
	depth := 0
	if fromRun {
		depth = env.Origin.Depth + 1
	}
	payload, ciID, err := c.runContext(octx, env)
	if err != nil {
		return err
	}
	// A run on a CI serializes per CI (ci:<id>), a run on an asset of a
	// lifecycle.changed per asset (asset:<id>, WP-1104 AK2).
	serialKey := serialKeyOf(ciID)
	if ciID == "" {
		if a, ok := payload["asset"].(map[string]any); ok {
			if id, _ := a["id"].(string); id != "" {
				serialKey = "asset:" + id
			}
		}
	}
	var errs []error
	for i := range matching {
		def := &matching[i]
		if fromRun {
			if allow, _ := def.Trigger["allow_workflow_origin"].(bool); !allow {
				continue
			}
			if depth > triggerMaxDepth {
				triggerLoop.Inc()
				continue
			}
		}
		hold, ferr := filterHolds(def.Trigger, payload)
		if ferr != nil {
			slog.WarnContext(ctx, "workflow trigger: invalid stored filter", "workflow_id", def.ID)
			continue
		}
		if !hold {
			continue
		}
		run := &Run{OrganizationID: env.OrgID, WorkflowID: def.ID, DefinitionVersionID: def.CurrentVersionID, Status: StatusPending,
			Trigger: env.Type, Context: payload, CIID: ciID, SerialKey: serialKey, TriggerEventID: env.ID, TriggerDepth: depth}
		if c.exec.queue == nil {
			run.StartedAt = time.Now().UTC()
		}
		if ciID != "" && env.EntityID == ciID && env.EntitySeq > 0 {
			seq := env.EntitySeq
			run.EntitySeq = &seq
		}
		created, cerr := c.store.CreateEventRun(octx, run)
		if cerr != nil {
			errs = append(errs, fmt.Errorf("store the run of %s: %w", def.ID, cerr))
			continue
		}
		if !created {
			// A delivery again: the run exists. With the job queue a run
			// still pending is submitted again under the idempotency key of
			// its enqueue, so a queueing that failed on the first delivery
			// is caught up and a job that exists is not doubled.
			if c.exec.queue == nil {
				continue
			}
			existing, gerr := c.store.EventRun(octx, env.OrgID, def.CurrentVersionID, env.ID)
			if gerr != nil {
				errs = append(errs, fmt.Errorf("read the run of %s: %w", def.ID, gerr))
				continue
			}
			if existing.Status != StatusPending {
				continue
			}
			run = existing
		}
		if xerr := c.exec.queueEventRun(octx, env.OrgID, def, run); xerr != nil {
			errs = append(errs, fmt.Errorf("queue the run %s: %w", run.ID, xerr))
		}
	}
	return errors.Join(errs...)
}

// runContext builds the context of the runs of env (AK3): event, data and,
// for an event about a CI, ci (status, type, attributes read
// organization-wide). The CI is the entity of a ci.* event, otherwise
// data.ci_id; an unknown or deleted CI binds none.
func (c *TriggerConsumer) runContext(ctx context.Context, env *events.Envelope) (payload JSONMap, ciID string, err error) {
	data := map[string]any{}
	if len(env.Data) > 0 {
		if uerr := json.Unmarshal(env.Data, &data); uerr != nil || data == nil {
			data = map[string]any{}
		}
	}
	event := map[string]any{"id": env.ID, "type": env.Type, "occurred_at": env.OccurredAt.UTC().Format(time.RFC3339Nano)}
	if env.EntityID != "" {
		event["entity_id"], event["entity_seq"] = env.EntityID, env.EntitySeq
	}
	payload = JSONMap{"event": event, "data": data}
	candidate := ""
	switch {
	case env.Type == events.LifecycleChanged:
		// The transition (WP-1104 AK2): entity, states, model and reason
		// at the top of the context, the entity as ci or asset.
		for _, key := range []string{"entity_type", "entity_id", "from", "to", "model_key", "reason"} {
			if v, ok := data[key]; ok {
				payload[key] = v
			}
		}
		entityID, _ := data["entity_id"].(string)
		switch data["entity_type"] {
		case lifecycleEntityCI:
			candidate = entityID
		case lifecycleEntityAsset:
			if serr := c.assetSnapshot(ctx, env.OrgID, entityID, payload); serr != nil {
				return nil, "", serr
			}
			return payload, "", nil
		}
	case strings.HasPrefix(env.Type, "ci."):
		candidate = env.EntityID
	default:
		if s, ok := data["ci_id"].(string); ok {
			candidate = s
		}
	}
	if candidate == "" || !api.IsUUID(candidate) || c.exec.cis == nil {
		return payload, "", nil
	}
	item, gerr := c.exec.cis.GetByID(ctx, env.OrgID, candidate)
	if errors.Is(gerr, ci.ErrNotFound) {
		return payload, "", nil
	}
	if gerr != nil {
		return nil, "", fmt.Errorf("read the CI of the event: %w", gerr)
	}
	attrs := item.Attributes
	if attrs == nil {
		attrs = map[string]any{}
	}
	payload["ci_id"] = item.ID
	payload["ci"] = map[string]any{"id": item.ID, "name": item.Name, "status": item.Status, "ci_type_id": item.CITypeID, "attributes": attrs}
	return payload, item.ID, nil
}

// filterHolds evaluates trigger.filter (WP-1087) over the run context; no
// filter always holds.
func filterHolds(trigger, payload JSONMap) (bool, error) {
	raw, has := trigger["filter"]
	if !has || raw == nil {
		return true, nil
	}
	cond, err := parseCondition(raw)
	if err != nil {
		return false, errConditionInvalid
	}
	return rules.EvaluateCondition(&cond, EvaluationValues(payload)), nil
}

// definitions returns the event definitions of the organization, cached
// for triggerDefinitionsTTL.
func (c *TriggerConsumer) definitions(ctx context.Context, orgID string) ([]Definition, error) {
	now := c.now()
	c.mu.Lock()
	cached, ok := c.defs[orgID]
	c.mu.Unlock()
	if ok && now.Sub(cached.at) < triggerDefinitionsTTL {
		return cached.defs, nil
	}
	defs, err := c.store.EventDefinitions(ctx, orgID)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.defs[orgID] = cachedDefinitions{defs: defs, at: now}
	c.mu.Unlock()
	return defs, nil
}

// featureOn reports whether the organization has the workflow feature,
// cached for triggerFeatureTTL.
func (c *TriggerConsumer) featureOn(ctx context.Context, orgID string) bool {
	if c.enabled == nil {
		return false
	}
	now := c.now()
	c.mu.Lock()
	cached, ok := c.features[orgID]
	c.mu.Unlock()
	if ok && now.Sub(cached.at) < triggerFeatureTTL {
		return cached.on
	}
	on := c.enabled(ctx, orgID)
	c.mu.Lock()
	c.features[orgID] = cachedFeature{on: on, at: now}
	c.mu.Unlock()
	return on
}

// EventDefinitions implements EventStore.
func (r *PGRepository) EventDefinitions(ctx context.Context, orgID string) ([]Definition, error) {
	out := []Definition{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT "+defCols+" FROM workflow_def WHERE organization_id=$1 AND active AND trigger->>'type' = $2 ORDER BY id", orgID, TriggerEvent)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDef(rows)
			if err != nil {
				return err
			}
			out = append(out, *d)
		}
		return rows.Err()
	})
	return out, err
}

// CreateEventRun implements EventStore with INSERT … ON CONFLICT DO
// NOTHING on uq_workflow_run_trigger_event (AK3).
func (r *PGRepository) CreateEventRun(ctx context.Context, run *Run) (bool, error) {
	created := false
	if run.Context == nil {
		run.Context = JSONMap{}
	}
	if run.SerialKey == "" {
		run.SerialKey = serialKeyOf(run.CIID)
	}
	err := database.WithRequestTenant(ctx, r.pool, run.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO workflow_run (organization_id, workflow_id, definition_version_id, status, trigger, context, started_at,
				ci_id, serial_key, entity_seq, trigger_event_id, trigger_depth)
			VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, '')::uuid, NULLIF($9, ''), $10, $11, $12)
			ON CONFLICT (organization_id, definition_version_id, trigger_event_id) WHERE trigger_event_id IS NOT NULL DO NOTHING
			RETURNING id::text, created_at, updated_at`,
			run.OrganizationID, run.WorkflowID, run.DefinitionVersionID, run.Status, run.Trigger, run.Context, nullTime(run.StartedAt),
			run.CIID, run.SerialKey, run.EntitySeq, run.TriggerEventID, run.TriggerDepth).Scan(&run.ID, &run.CreatedAt, &run.UpdatedAt)
		if err == pgx.ErrNoRows {
			return nil
		}
		created = err == nil
		return err
	})
	return created, err
}

// EventRun implements EventStore.
func (r *PGRepository) EventRun(ctx context.Context, orgID, versionID, eventID string) (*Run, error) {
	var run *Run
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		run, err = scanRun(tx.QueryRow(ctx, "SELECT "+runCols+" FROM workflow_run WHERE organization_id=$1 AND definition_version_id=$2 AND trigger_event_id=$3",
			orgID, versionID, eventID))
		if err == pgx.ErrNoRows {
			return ErrRunNotFound
		}
		return err
	})
	return run, err
}

// queueEventRun executes the stored run of an event trigger like Execute;
// with the job queue a failed submit leaves the run pending instead of
// failing it, so the delivery again (Nak) queues it (WP-1090).
func (e *Executor) queueEventRun(ctx context.Context, orgID string, def *Definition, run *Run) error {
	if e.queue == nil {
		_, err := e.Execute(ctx, orgID, def, run)
		return err
	}
	_, _, err := e.queue.Submit(ctx, orgID, RunJobKind, RunJobParams{RunID: run.ID, Enqueue: run.Enqueues},
		jobs.EnqueueOptions{IdempotencyKey: RunJobKey(run.ID, run.Enqueues), CreatedBy: run.InitiatedBy})
	return err
}

// EventRun implements EventStore.
func (r *MemoryRepository) EventRun(_ context.Context, orgID, versionID, eventID string) (*Run, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, existing := range r.runs {
		if existing.OrganizationID == orgID && existing.DefinitionVersionID == versionID && existing.TriggerEventID == eventID {
			cp := *existing
			return &cp, nil
		}
	}
	return nil, ErrRunNotFound
}

// EventDefinitions implements EventStore.
func (r *MemoryRepository) EventDefinitions(_ context.Context, orgID string) ([]Definition, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []Definition{}
	for _, d := range r.definitions {
		if d.OrganizationID == orgID && d.Active && str(d.Trigger, "type", "") == TriggerEvent {
			out = append(out, *d)
		}
	}
	return out, nil
}

// CreateEventRun implements EventStore.
func (r *MemoryRepository) CreateEventRun(ctx context.Context, run *Run) (bool, error) {
	r.mu.RLock()
	for _, existing := range r.runs {
		if existing.OrganizationID == run.OrganizationID && existing.DefinitionVersionID == run.DefinitionVersionID &&
			existing.TriggerEventID != "" && existing.TriggerEventID == run.TriggerEventID {
			r.mu.RUnlock()
			return false, nil
		}
	}
	r.mu.RUnlock()
	key := run.SerialKey
	if err := r.CreateRun(ctx, run); err != nil {
		return false, err
	}
	if key != "" {
		// An asset run serializes over asset:<id> (WP-1104).
		r.mu.Lock()
		run.SerialKey = key
		if stored := r.runs[run.ID]; stored != nil {
			stored.SerialKey = key
		}
		r.mu.Unlock()
	}
	return true, nil
}

// ─── lifecycle triggers (LCY-04, WP-1104) ─────────────────────────────────────

// Entity types of lifecycle.changed.
const (
	lifecycleEntityCI    = "ci"
	lifecycleEntityAsset = "asset"
)

// legacyLifecycleEvent is the former name of lifecycle.changed; webhook
// subscriptions still receive it (webhook legacyNames), triggers refuse it.
const legacyLifecycleEvent = "lifecycle.transitioned"

// lifecycleShorthand are the keys of the trigger shorthand of
// lifecycle.changed and the data field each filters.
var lifecycleShorthand = []struct{ key, field string }{
	{"from_state", "data.from"}, {"to_state", "data.to"}, {"model", "data.model_key"}, {"entity_type", "data.entity_type"},
}

// AssetReader reads the asset of a lifecycle.changed for the run context
// (WP-1104 AK2); the router and the worker set the asset repository.
type AssetReader interface {
	GetByID(ctx context.Context, orgID, id string) (*asset.Asset, error)
}

// WithAssets sets the reader of the asset snapshot of lifecycle runs; nil
// puts only the asset id into the context.
func (e *Executor) WithAssets(assets AssetReader) *Executor {
	e.assets = assets
	return e
}

// assetSnapshot puts the asset id into payload (asset) with a fixed set of
// identifying fields, like the CI snapshot: id, name, status,
// lifecycle_state, asset_type_id, client_id and site_id. The run context
// has no field projection (GET of a run and placeholders of
// send_notification show it as stored), so financial and contract fields,
// owners, holders, notes and custom_fields stay out (field policy,
// convention 11). An unknown or deleted asset puts only its id.
func (c *TriggerConsumer) assetSnapshot(ctx context.Context, orgID, id string, payload JSONMap) error {
	if id == "" || !api.IsUUID(id) {
		return nil
	}
	payload["asset"] = map[string]any{"id": id}
	if c.exec.assets == nil {
		return nil
	}
	item, err := c.exec.assets.GetByID(ctx, orgID, id)
	if errors.Is(err, asset.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the asset of the event: %w", err)
	}
	snap := map[string]any{"id": id, "name": item.Name, "status": item.Status}
	for k, v := range map[string]string{"lifecycle_state": item.LifecycleState, "asset_type_id": item.AssetTypeID,
		"client_id": item.ClientID, "site_id": item.SiteID} {
		if v != "" {
			snap[k] = v
		}
	}
	payload["asset"] = snap
	return nil
}

// normalizeTrigger turns the shorthand of a lifecycle trigger (AK1)
//
//	{"type": "event", "event": "lifecycle.changed", "from_state": "<key>|[…]",
//	 "to_state": "<key>|[…]", "model": "<key>", "entity_type": "ci|asset"}
//
// into trigger.filter, a MET-45 all over data.from, data.to, data.model_key
// and data.entity_type (eq for a value, in for a list); the shorthand keys
// are not stored. The shorthand with a filter of its own, on another event
// or with an invalid value is 422 on trigger.<key>; the former name
// lifecycle.transitioned as event is 422 on trigger.event. A trigger
// without shorthand is returned as it is.
func normalizeTrigger(trigger JSONMap) (JSONMap, error) {
	if trigger == nil {
		return nil, nil
	}
	if legacyEvent(trigger["event"]) {
		return nil, triggerFieldError("trigger.event", legacyLifecycleEvent+" is the former name; use "+events.LifecycleChanged)
	}
	var present []string
	for _, sh := range lifecycleShorthand {
		if _, has := trigger[sh.key]; has {
			present = append(present, sh.key)
		}
	}
	if len(present) == 0 {
		return trigger, nil
	}
	if ev, _ := trigger["event"].(string); ev != events.LifecycleChanged || str(trigger, "type", "") != TriggerEvent {
		return nil, triggerFieldError("trigger."+present[0], "is a shorthand of the event trigger "+events.LifecycleChanged)
	}
	if f, has := trigger["filter"]; has && f != nil {
		return nil, triggerFieldError("trigger.filter", "cannot be combined with from_state, to_state, model or entity_type")
	}
	out := JSONMap{}
	for k, v := range trigger {
		out[k] = v
	}
	conds := []any{}
	for _, sh := range lifecycleShorthand {
		raw, has := trigger[sh.key]
		if !has {
			continue
		}
		delete(out, sh.key)
		values, err := shorthandValues(raw, sh.key == "from_state" || sh.key == "to_state")
		if err != nil {
			return nil, triggerFieldError("trigger."+sh.key, err.Error())
		}
		if sh.key == "entity_type" && values[0] != lifecycleEntityCI && values[0] != lifecycleEntityAsset {
			return nil, triggerFieldError("trigger.entity_type", "must be ci or asset")
		}
		if len(values) == 1 {
			conds = append(conds, map[string]any{"op": "eq", "field": sh.field, "value": values[0]})
			continue
		}
		list := make([]any, len(values))
		for i, v := range values {
			list[i] = v
		}
		conds = append(conds, map[string]any{"op": "in", "field": sh.field, "value": list})
	}
	out["filter"] = map[string]any{"op": "all", "rules": conds}
	return out, nil
}

// shorthandValues reads a shorthand value: a non-empty string or, where
// lists are allowed, a non-empty list of distinct non-empty strings.
// validation: fixed texts about the shorthand value (and the state key the
// caller sent twice).
func shorthandValues(raw any, listAllowed bool) ([]string, error) {
	switch v := raw.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, errors.New("must not be empty")
		}
		return []string{v}, nil
	case []any:
		if !listAllowed {
			return nil, errors.New("must be a string")
		}
		if len(v) == 0 {
			return nil, errors.New("must name at least one state")
		}
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok || strings.TrimSpace(s) == "" {
				return nil, errors.New("must be a list of state keys")
			}
			if slices.Contains(out, s) {
				return nil, fmt.Errorf("names %q twice", s)
			}
			out = append(out, s)
		}
		return out, nil
	}
	if listAllowed {
		return nil, errors.New("must be a state key or a list of state keys")
	}
	return nil, errors.New("must be a string")
}

// legacyEvent reports whether event names lifecycle.transitioned.
func legacyEvent(event any) bool {
	switch v := event.(type) {
	case string:
		return v == legacyLifecycleEvent
	case []any:
		for _, item := range v {
			if s, _ := item.(string); s == legacyLifecycleEvent {
				return true
			}
		}
	}
	return false
}

func triggerFieldError(field, message string) *api.FieldError {
	return &api.FieldError{Field: field, Message: message, Status: http.StatusUnprocessableEntity}
}
