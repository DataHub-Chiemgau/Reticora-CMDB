package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Event triggers of workflows over the durable JetStream consumer
// (WFL-02, WFL-03, WP-1090): a repeated delivery starts one run, the filter
// and the feature gate decide, an event a run wrote starts runs only where
// allowed and at most to depth 3, and a run on a CI carries the CI and the
// entity sequence of its event.
//
// They run only with TEST_DATABASE_URL (scopetest.Seed); the delivery test
// also with TEST_NATS_URL.
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestCIChangedDeliveredTwiceStartsOneRun|TestFilterFalseStartsNoRun|TestNoRunWithoutWorkflowFormsFeature|TestWorkflowOriginEventStartsNoRun|TestQueuedRunEventsCarryRunOrigin|TestEventRunCarriesCIAndEntitySeq' ./internal/workflow/

// triggerEnv is the trigger consumer of one organization on PostgreSQL with
// runs executed in the consumer (no queue) and CI writes that emit events.
type triggerEnv struct {
	f        *scopetest.Fixture
	ctx      context.Context
	repo     *PGRepository
	exec     *Executor
	consumer *TriggerConsumer
	feature  bool
}

func newTriggerEnv(t *testing.T) *triggerEnv {
	t.Helper()
	f := scopetest.Seed(t, "3f")
	scope := database.OrgWideScope(f.OrgA, "")
	e := &triggerEnv{f: f, repo: NewPGRepository(f.App), feature: true,
		ctx: tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope), tenant.TenantInfo{OrganizationID: f.OrgA})}
	cis := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox())
	e.exec = NewExecutor(e.repo, nil, cis, nil, nil).WithFieldGuard(override.NewGuard(override.NewPGRepository(f.App)))
	e.consumer = NewTriggerConsumer(e.exec, e.repo, nil, func(context.Context, string) bool { return e.feature })
	return e
}

// definition stores an active event definition with trigger and actions.
func (e *triggerEnv) definition(t *testing.T, trigger JSONMap, actions ...JSONMap) *Definition {
	t.Helper()
	if len(actions) == 0 {
		actions = []JSONMap{{"type": "noop"}}
	}
	def := &Definition{OrganizationID: e.f.OrgA, Name: fmt.Sprintf("wf-trigger-%d", time.Now().UnixNano()), Trigger: trigger, Actions: actions, Active: true}
	if err := e.repo.CreateDefinition(e.ctx, def); err != nil {
		t.Fatal(err)
	}
	return def
}

func (e *triggerEnv) runs(t *testing.T, def *Definition) []Run {
	t.Helper()
	list, _, err := e.repo.ListRuns(e.ctx, e.f.OrgA, &RunFilter{WorkflowID: def.ID}, pageAll)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// outboxEvents reads the envelopes of entity of type typ from the outbox
// as the publisher does, with their origin.
func (e *triggerEnv) outboxEvents(t *testing.T, entity, typ string) []events.Envelope {
	t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT id::text, type, organization_id::text, occurred_at, sequence,
		COALESCE(entity_id, ''), COALESCE(entity_seq, 0), data, origin FROM event_outbox
		WHERE organization_id = $1 AND entity_id = $2 AND type = $3 ORDER BY sequence`, e.f.OrgA, entity, typ)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []events.Envelope
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
		out = append(out, env)
	}
	return out
}

// ciChanged is an envelope ci.changed of ciID with entity_seq seq.
func (e *triggerEnv) ciChanged(t *testing.T, ciID string, seq int64) *events.Envelope {
	t.Helper()
	var id string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return &events.Envelope{ID: id, Type: "ci.changed", OrgID: e.f.OrgA, OccurredAt: time.Now().UTC(), Sequence: seq,
		EntityID: ciID, EntitySeq: seq, Data: json.RawMessage(`{"id":"` + ciID + `","changed_fields":["status"]}`)}
}

// TestCIChangedDeliveredTwiceStartsOneRun: the same ci.changed delivered
// twice over the durable consumer (and once more directly) starts exactly
// one run.
func TestCIChangedDeliveredTwiceStartsOneRun(t *testing.T) {
	e := newTriggerEnv(t)
	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Skip("TEST_NATS_URL not set")
	}
	nc, err := nats.Connect(url, nats.Name("wp1090"))
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
	stream := "WP1090_" + suffix
	prefix := "wp1090_" + suffix + "."
	if _, err = js.CreateStream(bg, jetstream.StreamConfig{Name: stream, Subjects: []string{prefix + ">"}, Storage: jetstream.MemoryStorage}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = js.DeleteStream(context.Background(), stream) })
	def := e.definition(t, JSONMap{"type": "event", "event": "ci.changed"})
	device := e.f.CI(t, e.f.OrgA, "", "wf-trigger-twice")
	env := e.ciChanged(t, device, 1)
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	// Two deliveries of the same event (no message id: the stream keeps
	// both, as after a publisher retry beyond the duplicate window).
	for range 2 {
		if _, err = js.Publish(bg, prefix+e.f.OrgA+".ci.changed", body); err != nil {
			t.Fatal(err)
		}
	}
	consumer := NewTriggerConsumer(e.exec, e.repo, js, func(context.Context, string) bool { return true })
	consumer.Stream, consumer.Durable, consumer.Filter = stream, "wp1090-"+suffix, prefix+"*.>"
	ctx, cancel := context.WithCancel(bg)
	t.Cleanup(cancel)
	go consumer.Run(ctx)
	deadline := time.Now().Add(15 * time.Second)
	for {
		info, ierr := js.Stream(bg, stream)
		if ierr != nil {
			t.Fatal(ierr)
		}
		c, cerr := info.Consumer(bg, consumer.Durable)
		if cerr == nil {
			if ci, _ := c.Info(bg); ci != nil && ci.AckFloor.Stream >= 2 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the consumer did not acknowledge both deliveries")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err = e.consumer.Handle(e.ctx, env); err != nil {
		t.Fatal(err)
	}
	runs := e.runs(t, def)
	if len(runs) != 1 || runs[0].TriggerEventID != env.ID || runs[0].Trigger != "ci.changed" {
		t.Fatalf("runs of the event: %+v, want one", runs)
	}
	if runs[0].Status != StatusSucceeded {
		t.Errorf("run %s, want succeeded", runs[0].Status)
	}
}

// TestFilterFalseStartsNoRun: a filter that does not hold over the run
// context starts no run; one that holds starts one.
func TestFilterFalseStartsNoRun(t *testing.T) {
	e := newTriggerEnv(t)
	device := e.f.CI(t, e.f.OrgA, "", "wf-trigger-filter")
	miss := e.definition(t, JSONMap{"type": "event", "event": "ci.changed", "filter": map[string]any{"op": "eq", "field": "ci.status", "value": "retired"}})
	hit := e.definition(t, JSONMap{"type": "event", "event": "ci.changed", "filter": map[string]any{"op": "eq", "field": "ci.status", "value": "active"}})
	if err := e.consumer.Handle(e.ctx, e.ciChanged(t, device, 1)); err != nil {
		t.Fatal(err)
	}
	if got := e.runs(t, miss); len(got) != 0 {
		t.Errorf("runs of the false filter: %d, want 0", len(got))
	}
	if got := e.runs(t, hit); len(got) != 1 {
		t.Errorf("runs of the true filter: %d, want 1", len(got))
	}
}

// TestNoRunWithoutWorkflowFormsFeature: without the workflow feature an
// event starts nothing.
func TestNoRunWithoutWorkflowFormsFeature(t *testing.T) {
	e := newTriggerEnv(t)
	e.feature = false
	def := e.definition(t, JSONMap{"type": "event", "event": "ci.changed"})
	device := e.f.CI(t, e.f.OrgA, "", "wf-trigger-feature")
	if err := e.consumer.Handle(e.ctx, e.ciChanged(t, device, 1)); err != nil {
		t.Fatal(err)
	}
	if got := e.runs(t, def); len(got) != 0 {
		t.Errorf("runs without the feature: %d, want 0", len(got))
	}
}

// TestWorkflowOriginEventStartsNoRun: set_ci_field writes ci.changed with
// the origin of its run; the definition of the run is not started again by
// it. With allow_workflow_origin the chain of runs ends at depth 3.
func TestWorkflowOriginEventStartsNoRun(t *testing.T) {
	e := newTriggerEnv(t)
	device := e.f.CI(t, e.f.OrgA, "", "wf-trigger-origin")
	def := e.definition(t, JSONMap{"type": "event", "event": "ci.changed"},
		JSONMap{"type": "set_ci_field", "ci_id": device, "field": "rack", "value": "R-origin"})
	if err := e.consumer.Handle(e.ctx, e.ciChanged(t, device, 1)); err != nil {
		t.Fatal(err)
	}
	runs := e.runs(t, def)
	if len(runs) != 1 || runs[0].Status != StatusSucceeded {
		t.Fatalf("runs of the external event: %+v, want one succeeded", runs)
	}
	written := e.outboxEvents(t, device, "ci.changed")
	if len(written) != 1 || written[0].Origin == nil || written[0].Origin.Kind != events.OriginWorkflowRun ||
		written[0].Origin.RunID != runs[0].ID || written[0].Origin.Depth != 0 {
		t.Fatalf("ci.changed of the run: %+v, want one with the origin of run %s", written, runs[0].ID)
	}
	if err := e.consumer.Handle(e.ctx, &written[0]); err != nil {
		t.Fatal(err)
	}
	if got := e.runs(t, def); len(got) != 1 {
		t.Errorf("runs after the event of the run: %d, want still 1", len(got))
	}

	// With allow_workflow_origin each event a run writes starts the next
	// run, one deeper, until depth 3: the chain follows the events the runs
	// write to the outbox (workflow.run_succeeded), each with the origin of
	// its run at the run's depth; after depth 3 no run follows and the loop
	// is counted.
	chain := e.definition(t, JSONMap{"type": "event", "event": []any{"ticket.updated", "workflow.run_succeeded"}, "allow_workflow_origin": true})
	// The definitions of the organization are cached for 15 s.
	e.consumer.defs = map[string]cachedDefinitions{}
	var id string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := e.consumer.Handle(e.ctx, &events.Envelope{ID: id, Type: "ticket.updated", OrgID: e.f.OrgA, OccurredAt: time.Now().UTC(),
		Data: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	loops := testutil.ToFloat64(triggerLoop)
	for depth := 0; depth <= triggerMaxDepth; depth++ {
		got := e.runs(t, chain)
		if len(got) != depth+1 || got[0].TriggerDepth != depth || got[0].Status != StatusSucceeded {
			t.Fatalf("runs of the chain at depth %d: %+v, want %d, the newest succeeded at depth %d", depth, got, depth+1, depth)
		}
		written := e.outboxEvents(t, got[0].ID, "workflow.run_succeeded")
		if len(written) != 1 || written[0].Origin == nil || written[0].Origin.Kind != events.OriginWorkflowRun ||
			written[0].Origin.RunID != got[0].ID || written[0].Origin.Depth != got[0].TriggerDepth {
			t.Fatalf("workflow.run_succeeded of the run at depth %d: %+v, want one with the origin of its run", depth, written)
		}
		if err := e.consumer.Handle(e.ctx, &written[0]); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.runs(t, chain); len(got) != triggerMaxDepth+1 {
		t.Fatalf("runs of the chain: %d, want %d (depths 0 to %d)", len(got), triggerMaxDepth+1, triggerMaxDepth)
	}
	if got := testutil.ToFloat64(triggerLoop) - loops; got != 1 {
		t.Errorf("reticora_workflow_trigger_loop_total rose by %v, want 1", got)
	}
}

// TestQueuedRunEventsCarryRunOrigin: with the job queue the worker's claim
// starts the run, outside the executor; its workflow.run_started and
// workflow.run_succeeded name the run as origin all the same, so a
// definition on workflow.run_started without allow_workflow_origin does not
// start itself again (WP-1090 AK5).
func TestQueuedRunEventsCarryRunOrigin(t *testing.T) {
	e := newWorkerEnv(t, 5, true)
	tc := &triggerEnv{f: e.f, ctx: e.ctx, repo: e.repo, exec: e.exec, feature: true}
	tc.consumer = NewTriggerConsumer(e.exec, e.repo, nil, func(context.Context, string) bool { return true })
	self := tc.definition(t, JSONMap{"type": "event", "event": "workflow.run_started"})
	run := e.start(t, e.ctx, JSONMap{"type": "noop"})
	if _, err := e.exec.Execute(e.ctx, e.f.OrgA, nil, run); err != nil {
		t.Fatal(err)
	}
	if done := e.await(t, run.ID); done.Status != StatusSucceeded {
		t.Fatalf("queued run: %s %q", done.Status, done.LastError)
	}
	for _, typ := range []string{"workflow.run_started", "workflow.run_succeeded"} {
		written := tc.outboxEvents(t, run.ID, typ)
		if len(written) != 1 || written[0].Origin == nil || written[0].Origin.Kind != events.OriginWorkflowRun || written[0].Origin.RunID != run.ID {
			t.Fatalf("%s of the queued run: %+v, want one with the origin of the run", typ, written)
		}
		if typ == "workflow.run_started" {
			if err := tc.consumer.Handle(e.ctx, &written[0]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got := tc.runs(t, self); len(got) != 0 {
		t.Errorf("runs of the definition on workflow.run_started: %d, want 0", len(got))
	}
}

// TestEventRunCarriesCIAndEntitySeq: a run of ci.changed refers to the CI
// of the event with its entity sequence; its context holds event, data and
// the CI read organization-wide.
func TestEventRunCarriesCIAndEntitySeq(t *testing.T) {
	e := newTriggerEnv(t)
	def := e.definition(t, JSONMap{"type": "event", "event": []any{"ci.created", "ci.changed"}})
	device := e.f.CI(t, e.f.OrgA, e.f.Client1, "wf-trigger-ci")
	env := e.ciChanged(t, device, 7)
	if err := e.consumer.Handle(e.ctx, env); err != nil {
		t.Fatal(err)
	}
	runs := e.runs(t, def)
	if len(runs) != 1 {
		t.Fatalf("runs: %+v, want one", runs)
	}
	run := runs[0]
	if run.CIID != device || run.SerialKey != "ci:"+device || run.EntitySeq == nil || *run.EntitySeq != 7 || run.TriggerDepth != 0 {
		t.Errorf("run: ci %q key %q seq %v depth %d, want the CI with entity_seq 7", run.CIID, run.SerialKey, run.EntitySeq, run.TriggerDepth)
	}
	event, _ := run.Context["event"].(map[string]any)
	ciCtx, _ := run.Context["ci"].(map[string]any)
	data, _ := run.Context["data"].(map[string]any)
	if event["id"] != env.ID || event["type"] != "ci.changed" || event["entity_id"] != device || ciCtx["status"] != "active" || data["id"] != device {
		t.Errorf("context %v, want event, data and the CI", run.Context)
	}
	var client string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT COALESCE(client_id::text, '') FROM workflow_run WHERE id = $1`, run.ID).Scan(&client); err != nil ||
		client != e.f.Client1 {
		t.Errorf("client of the run: %q %v, want the client of the CI", client, err)
	}
}
