package workflow_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/workflow"
)

// Lifecycle transitions start workflows (LCY-04, WP-1104): a definition
// with the trigger shorthand of lifecycle.changed (stored as its filter
// through the API) is started by the event of a real transition with the
// transition and its CI or asset in the run context; another transition
// and a transition by a run (transition_lifecycle, loop guard of WP-1090)
// start none.
//
// They run only with TEST_DATABASE_URL (scopetest.Seed).
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestLifecycleTransitionStartsRun|TestLifecycleOtherTransitionIgnored|TestLifecycleAssetContext|TestLifecycleWorkflowTransitionNoLoop' ./internal/workflow/

// lifecycleTriggerEnv has a CI type and an asset model with the states
// in_stock (initial), deployed and retired, the lifecycle service, the
// workflow API and the trigger consumer (runs executed in the consumer).
type lifecycleTriggerEnv struct {
	f        *scopetest.Fixture
	ctx      context.Context
	runs     *workflow.PGRepository
	exec     *workflow.Executor
	consumer *workflow.TriggerConsumer
	api      chi.Router
	svc      *lifecycle.Service
	model    *lifecycle.Definition
	assets   *lifecycle.Definition
	typeID   string
}

func newLifecycleTriggerEnv(t *testing.T) *lifecycleTriggerEnv {
	t.Helper()
	f := scopetest.Seed(t, "3f")
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	models := lifecycle.NewPGRepository(f.App)
	tag := time.Now().UnixNano()
	states := []lifecycle.State{{Key: "in_stock", Label: "In stock", IsInitial: true}, {Key: "deployed", Label: "Deployed", SortOrder: 1},
		{Key: "retired", Label: "Retired", IsTerminal: true, SortOrder: 2}}
	edges := []lifecycle.Transition{{FromStateKey: "in_stock", ToStateKey: "deployed", Key: "deploy", Label: "Deploy"},
		{FromStateKey: "deployed", ToStateKey: "retired", Key: "retire", Label: "Retire"}}
	e := &lifecycleTriggerEnv{f: f, ctx: ctx}
	e.model = &lifecycle.Definition{OrganizationID: f.OrgA, Key: fmt.Sprintf("wf1104-%d", tag), Name: "wf1104", AppliesTo: "ci", States: states, Transitions: edges}
	if err := models.Create(ctx, e.model); err != nil {
		t.Fatalf("lifecycle model: %v", err)
	}
	// The asset model avoids the states with a system guard of LCY-03
	// (in_stock, deployed): an asset enters its initial state ordered.
	e.assets = &lifecycle.Definition{OrganizationID: f.OrgA, Key: fmt.Sprintf("wf1104-asset-%d", tag), Name: "wf1104 assets", AppliesTo: "asset",
		States:      []lifecycle.State{{Key: "ordered", Label: "Ordered", IsInitial: true}, {Key: "retired", Label: "Retired", IsTerminal: true, SortOrder: 1}},
		Transitions: []lifecycle.Transition{{FromStateKey: "ordered", ToStateKey: "retired", Key: "retire", Label: "Retire"}}}
	if err := models.Create(ctx, e.assets); err != nil {
		t.Fatalf("asset lifecycle model: %v", err)
	}
	typ := &citype.Type{OrganizationID: f.OrgA, Key: fmt.Sprintf("wf1104-lc-%d", tag), Name: "wf1104-lc", IsObservable: true, LifecycleModelID: e.model.ID}
	if err := citype.NewPGRepository(f.App).Create(ctx, typ); err != nil {
		t.Fatalf("type: %v", err)
	}
	e.typeID = typ.ID
	e.svc = lifecycle.NewService(models, lifecycle.NewPGStateStore(f.App), lifecycle.NewHistoryRecorder(history.NewPGRepository(f.App)))
	e.runs = workflow.NewPGRepository(f.App)
	e.exec = workflow.NewExecutor(e.runs, nil, ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()), nil, nil).
		WithLifecycle(workflow.NewLifecycleTransitioner(e.svc, lifecycle.NewResolver(f.App))).
		WithPolicies(permission.NewFieldResolver(permission.NewPGRepository(f.App), nil, permission.NewPGFieldGrants(f.App), nil), user.NewPGRepository(f.App)).
		WithAssets(asset.NewPGRepository(f.App))
	e.consumer = workflow.NewTriggerConsumer(e.exec, e.runs, nil, func(context.Context, string) bool { return true })
	e.api = chi.NewRouter()
	workflow.NewHandler(e.runs, e.exec).RegisterRoutes(e.api)
	return e
}

// define stores a definition through POST /api/v1/workflows and returns it
// as stored.
func (e *lifecycleTriggerEnv) define(t *testing.T, trigger, action map[string]any) *workflow.Definition {
	t.Helper()
	body, err := json.Marshal(map[string]any{"name": fmt.Sprintf("wf1104-%d", time.Now().UnixNano()), "trigger": trigger, "actions": []any{action}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflows", bytes.NewReader(body)).WithContext(e.ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.api.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create definition: %d %s", rec.Code, rec.Body.String())
	}
	var def workflow.Definition
	if err := json.Unmarshal(rec.Body.Bytes(), &def); err != nil {
		t.Fatal(err)
	}
	if _, has := def.Trigger["to_state"]; has || def.Trigger["filter"] == nil {
		t.Fatalf("stored trigger %v, want the shorthand as filter", def.Trigger)
	}
	return &def
}

func (e *lifecycleTriggerEnv) ciInState(t *testing.T, name, state string) string {
	t.Helper()
	id := e.f.CI(t, e.f.OrgA, e.f.Client1, name)
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE ci SET ci_type_id = $2, lifecycle_state = $3 WHERE id = $1`, id, e.typeID, state); err != nil {
		t.Fatal(err)
	}
	return id
}

// transition moves the entity to state through the lifecycle service.
func (e *lifecycleTriggerEnv) transition(t *testing.T, entityType, id, modelID, state string) {
	t.Helper()
	if _, err := e.svc.Transition(e.ctx, e.f.OrgA, entityType, id, modelID, lifecycle.TransitionRequest{ToState: state, Reason: "wp1104"}); err != nil {
		t.Fatalf("transition %s %s → %s: %v", entityType, id, state, err)
	}
}

// outboxEnvelopes returns every lifecycle.changed of entity in the outbox
// as the publisher sends it, with its origin.
func (e *lifecycleTriggerEnv) outboxEnvelopes(t *testing.T, entity string) []events.Envelope {
	t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT id::text, type, organization_id::text, occurred_at, sequence,
		COALESCE(entity_id, ''), COALESCE(entity_seq, 0), data, origin FROM event_outbox
		WHERE organization_id = $1 AND entity_id = $2 AND type = 'lifecycle.changed' ORDER BY sequence`, e.f.OrgA, entity)
	if err != nil {
		t.Fatal(err)
	}
	var envs []events.Envelope
	for rows.Next() {
		var env events.Envelope
		var origin []byte
		if err := rows.Scan(&env.ID, &env.Type, &env.OrgID, &env.OccurredAt, &env.Sequence, &env.EntityID, &env.EntitySeq, &env.Data, &origin); err != nil {
			t.Fatal(err)
		}
		if len(origin) > 0 {
			env.Origin = &events.Origin{}
			if err := json.Unmarshal(origin, env.Origin); err != nil {
				t.Fatal(err)
			}
		}
		envs = append(envs, env)
	}
	rows.Close()
	return envs
}

// deliver hands every lifecycle.changed of entity in the outbox to the
// consumer directly, as the durable consumer would.
func (e *lifecycleTriggerEnv) deliver(t *testing.T, entity string) int {
	t.Helper()
	envs := e.outboxEnvelopes(t, entity)
	for i := range envs {
		if err := e.consumer.Handle(context.Background(), &envs[i]); err != nil {
			t.Fatalf("handle %s: %v", envs[i].ID, err)
		}
	}
	return len(envs)
}

func (e *lifecycleTriggerEnv) runsOf(t *testing.T, def *workflow.Definition) []workflow.Run {
	t.Helper()
	list, _, err := e.runs.ListRuns(e.ctx, e.f.OrgA, &workflow.RunFilter{WorkflowID: def.ID}, api.PaginationParams{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return list
}

var retireTrigger = map[string]any{"type": "event", "event": "lifecycle.changed", "from_state": "deployed", "to_state": "retired", "entity_type": "ci"}

// deliverOverNATS publishes every lifecycle.changed of entity in the
// outbox twice to a JetStream stream of its own (as the outbox publisher
// does, a second time as after a publisher retry) and runs the durable
// consumer workflow-triggers on it until both deliveries are acknowledged.
func (e *lifecycleTriggerEnv) deliverOverNATS(t *testing.T, entity string) int {
	t.Helper()
	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Skip("TEST_NATS_URL not set")
	}
	nc, err := nats.Connect(url, nats.Name("wp1104"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	bg := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	stream := "WP1104_" + suffix
	prefix := "wp1104_" + suffix + "."
	if _, err = js.CreateStream(bg, jetstream.StreamConfig{Name: stream, Subjects: []string{prefix + ">"}, Storage: jetstream.MemoryStorage}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = js.DeleteStream(context.Background(), stream) })
	envs := e.outboxEnvelopes(t, entity)
	published := uint64(0)
	for i := range envs {
		body, merr := json.Marshal(&envs[i])
		if merr != nil {
			t.Fatal(merr)
		}
		for range 2 {
			if _, err = js.Publish(bg, prefix+envs[i].OrgID+"."+envs[i].Type, body); err != nil {
				t.Fatal(err)
			}
			published++
		}
	}
	consumer := workflow.NewTriggerConsumer(e.exec, e.runs, js, func(context.Context, string) bool { return true })
	consumer.Stream, consumer.Durable, consumer.Filter = stream, "wp1104-"+suffix, prefix+"*.>"
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	go consumer.Run(ctx)
	deadline := time.Now().Add(15 * time.Second)
	for {
		info, ierr := js.Stream(bg, stream)
		if ierr != nil {
			t.Fatal(ierr)
		}
		c, cerr := info.Consumer(bg, consumer.Durable)
		if cerr == nil {
			if ci, _ := c.Info(bg); ci != nil && ci.AckFloor.Stream >= published {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the durable consumer did not acknowledge the deliveries")
		}
		time.Sleep(100 * time.Millisecond)
	}
	return len(envs)
}

// TestLifecycleTransitionStartsRun: deployed → retired of a CI, delivered
// twice over NATS JetStream to the durable consumer, starts exactly one run
// with from, to, model_key and ci in its context, on the CI (serial key
// ci:<id>, WP-1089).
func TestLifecycleTransitionStartsRun(t *testing.T) {
	e := newLifecycleTriggerEnv(t)
	def := e.define(t, retireTrigger, map[string]any{"type": "noop"})
	device := e.ciInState(t, "wf1104-retire", "deployed")
	e.transition(t, "ci", device, e.model.ID, "retired")
	if n := e.deliverOverNATS(t, device); n != 1 {
		t.Fatalf("lifecycle.changed events %d, want 1", n)
	}
	runs := e.runsOf(t, def)
	if len(runs) != 1 {
		t.Fatalf("runs %d, want exactly one", len(runs))
	}
	run, err := e.runs.GetRun(e.ctx, e.f.OrgA, runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	c := run.Context
	if c["from"] != "deployed" || c["to"] != "retired" || c["model_key"] != e.model.Key || c["entity_type"] != "ci" || c["entity_id"] != device ||
		c["reason"] != "wp1104" {
		t.Errorf("context %v, want the transition deployed → retired of the model", c)
	}
	snapshot, _ := c["ci"].(map[string]any)
	if snapshot["id"] != device || c["ci_id"] != device || run.CIID != device {
		t.Errorf("run CI %q, context ci %v ci_id %v; want the CI %s", run.CIID, snapshot, c["ci_id"], device)
	}
	var key string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT COALESCE(serial_key, '') FROM workflow_run WHERE id = $1`, run.ID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key != "ci:"+device {
		t.Errorf("serial key %q, want ci:%s", key, device)
	}
	if run.Status != workflow.StatusSucceeded {
		t.Errorf("run %s, want succeeded", run.Status)
	}
}

// TestLifecycleOtherTransitionIgnored: in_stock → deployed starts no run.
func TestLifecycleOtherTransitionIgnored(t *testing.T) {
	e := newLifecycleTriggerEnv(t)
	def := e.define(t, retireTrigger, map[string]any{"type": "noop"})
	device := e.ciInState(t, "wf1104-deploy", "in_stock")
	e.transition(t, "ci", device, e.model.ID, "deployed")
	if n := e.deliver(t, device); n != 1 {
		t.Fatalf("lifecycle.changed events %d, want 1", n)
	}
	if runs := e.runsOf(t, def); len(runs) != 0 {
		t.Errorf("runs %d after in_stock → deployed, want none", len(runs))
	}
}

// TestLifecycleAssetContext: the transition of an asset puts the asset
// into the context and serializes over the asset, not over a CI.
func TestLifecycleAssetContext(t *testing.T) {
	e := newLifecycleTriggerEnv(t)
	def := e.define(t, map[string]any{"type": "event", "event": "lifecycle.changed", "to_state": "ordered", "entity_type": "asset"},
		map[string]any{"type": "noop"})
	item := e.f.Asset(t, e.f.OrgA, e.f.Client1, fmt.Sprintf("WF1104-%d", time.Now().UnixNano()))
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE asset SET purchase_price = 1234.50, invoice_number = 'INV-1104',
		notes = 'secret', custom_fields = '{"budget": 9}' WHERE id = $1`, item); err != nil {
		t.Fatal(err)
	}
	e.transition(t, "asset", item, e.assets.ID, "ordered")
	if n := e.deliver(t, item); n != 1 {
		t.Fatalf("lifecycle.changed events %d, want 1", n)
	}
	runs := e.runsOf(t, def)
	if len(runs) != 1 {
		t.Fatalf("runs %d, want one", len(runs))
	}
	run, err := e.runs.GetRun(e.ctx, e.f.OrgA, runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := run.Context["asset"].(map[string]any)
	if snapshot["id"] != item || snapshot["name"] == nil || snapshot["client_id"] != e.f.Client1 || run.Context["entity_type"] != "asset" || run.Context["to"] != "ordered" {
		t.Errorf("context %v, want the asset %s with its identifying fields", run.Context, item)
	}
	// The run context has no field projection: fields under field policy
	// stay out of the snapshot (convention 11).
	for _, field := range []string{"purchase_price", "invoice_number", "cost_center", "custom_fields", "notes", "supplier_id", "contract_id", "inventory_no"} {
		if _, has := snapshot[field]; has {
			t.Errorf("asset snapshot has %s: %v", field, snapshot)
		}
	}
	if run.CIID != "" || run.Context["ci"] != nil {
		t.Errorf("asset run on CI %q / %v, want none", run.CIID, run.Context["ci"])
	}
	var key string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT COALESCE(serial_key, '') FROM workflow_run WHERE id = $1`, run.ID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if key != "asset:"+item {
		t.Errorf("serial key %q, want asset:%s", key, item)
	}
}

// TestLifecycleWorkflowTransitionNoLoop: a transition by transition_lifecycle
// carries the origin of its run and starts no further run (WP-1090 loop
// guard; the definition does not allow workflow origins).
func TestLifecycleWorkflowTransitionNoLoop(t *testing.T) {
	e := newLifecycleTriggerEnv(t)
	def := e.define(t, retireTrigger, map[string]any{"type": "noop"})
	device := e.ciInState(t, "wf1104-loop", "deployed")
	mover := &workflow.Definition{OrganizationID: e.f.OrgA, Name: fmt.Sprintf("wf1104-mover-%d", time.Now().UnixNano()),
		Trigger: workflow.JSONMap{"type": "manual"}, Active: true,
		Actions: []workflow.JSONMap{{"type": "transition_lifecycle", "ci_id": device, "to_state": "retired"}}}
	if err := e.runs.CreateDefinition(e.ctx, mover); err != nil {
		t.Fatal(err)
	}
	run, err := e.exec.Trigger(e.ctx, e.f.OrgA, mover, "manual", workflow.JSONMap{})
	if err != nil || run.Status != workflow.StatusSucceeded {
		t.Fatalf("transition_lifecycle run: %v %+v", err, run)
	}
	if n := e.deliver(t, device); n != 1 {
		t.Fatalf("lifecycle.changed events %d, want 1", n)
	}
	if runs := e.runsOf(t, def); len(runs) != 0 {
		t.Errorf("runs %d after a transition by a workflow, want none", len(runs))
	}
}
