package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
)

// TestTriggerMatchesTypeListAndFilter covers WP-1090 AK2: trigger.event is
// a type name or a list of names; trigger.filter (MET-45) is evaluated over
// the run context.
func TestTriggerMatchesTypeListAndFilter(t *testing.T) {
	single := JSONMap{"type": "event", "event": "ci.changed"}
	list := JSONMap{"type": "event", "event": []any{"ci.created", "ci.changed"}}
	for _, c := range []struct {
		trigger JSONMap
		event   string
		want    bool
	}{
		{single, "ci.changed", true}, {single, "ci.created", false},
		{list, "ci.created", true}, {list, "ci.changed", true}, {list, "ci.deleted", false},
		{JSONMap{"type": "event"}, "ci.changed", false},
	} {
		if got := eventMatches(c.trigger, c.event); got != c.want {
			t.Errorf("eventMatches(%v, %s) = %v, want %v", c.trigger, c.event, got, c.want)
		}
	}
	if !TriggerMatches(&Definition{Trigger: list}, "ci.created") {
		t.Error("TriggerMatches does not know the list form")
	}
	payload := JSONMap{"event": map[string]any{"type": "ci.changed"}, "ci": map[string]any{"status": "maintenance"}}
	for _, c := range []struct {
		filter any
		want   bool
	}{
		{nil, true},
		{map[string]any{"op": "eq", "field": "ci.status", "value": "maintenance"}, true},
		{map[string]any{"op": "eq", "field": "ci.status", "value": "active"}, false},
		{map[string]any{"op": "all", "rules": []any{map[string]any{"op": "eq", "field": "event.type", "value": "ci.changed"}}}, true},
	} {
		trigger := JSONMap{"type": "event", "event": "ci.changed"}
		if c.filter != nil {
			trigger["filter"] = c.filter
		}
		got, err := filterHolds(trigger, payload)
		if err != nil || got != c.want {
			t.Errorf("filter %v: %v %v, want %v", c.filter, got, err, c.want)
		}
	}
	if _, err := filterHolds(JSONMap{"filter": map[string]any{"op": "bogus"}}, payload); err == nil {
		t.Error("an invalid stored filter holds")
	}
}

// TestOriginDepthLimit covers WP-1090 AK5: an event with the origin of a
// run starts no run of a definition without allow_workflow_origin; with
// it the run gets the depth of the origin plus one, and an event of a run
// at depth 3 starts nothing.
func TestOriginDepthLimit(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	exec := NewExecutor(repo, nil, nil, nil, nil)
	plain := &Definition{OrganizationID: "org-1", Name: "plain", Trigger: JSONMap{"type": "event", "event": "ticket.updated"},
		Actions: []JSONMap{{"type": "noop"}}, Active: true}
	allowing := &Definition{OrganizationID: "org-1", Name: "allowing", Trigger: JSONMap{"type": "event", "event": "ticket.updated",
		"allow_workflow_origin": true}, Actions: []JSONMap{{"type": "noop"}}, Active: true}
	for _, d := range []*Definition{plain, allowing} {
		if err := repo.CreateDefinition(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	consumer := NewTriggerConsumer(exec, repo, nil, func(context.Context, string) bool { return true })
	runsOf := func(def *Definition) []Run {
		t.Helper()
		list, _, err := repo.ListRuns(ctx, "org-1", &RunFilter{WorkflowID: def.ID}, pageAll)
		if err != nil {
			t.Fatal(err)
		}
		return list
	}
	n := 0
	envelope := func(origin *events.Origin) *events.Envelope {
		n++
		return &events.Envelope{ID: fmt.Sprintf("00000000-0000-4000-9000-%012d", n),
			Type: "ticket.updated", OrgID: "org-1", OccurredAt: time.Now(), Data: json.RawMessage(`{}`), Origin: origin}
	}
	if err := consumer.Handle(ctx, envelope(&events.Origin{Kind: events.OriginWorkflowRun, RunID: "r", Depth: 0})); err != nil {
		t.Fatal(err)
	}
	if got := runsOf(plain); len(got) != 0 {
		t.Errorf("runs of the definition without allow_workflow_origin: %d, want 0", len(got))
	}
	got := runsOf(allowing)
	if len(got) != 1 || got[0].TriggerDepth != 1 {
		t.Fatalf("runs of the allowing definition: %+v, want one at depth 1", got)
	}
	if err := consumer.Handle(ctx, envelope(&events.Origin{Kind: events.OriginWorkflowRun, RunID: "r", Depth: 2})); err != nil {
		t.Fatal(err)
	}
	if got = runsOf(allowing); len(got) != 2 {
		t.Fatalf("runs after an event at depth 2: %d, want 2", len(got))
	}
	loops := testutil.ToFloat64(triggerLoop)
	if err := consumer.Handle(ctx, envelope(&events.Origin{Kind: events.OriginWorkflowRun, RunID: "r", Depth: triggerMaxDepth})); err != nil {
		t.Fatal(err)
	}
	if got = runsOf(allowing); len(got) != 2 {
		t.Errorf("runs after an event at depth %d: %d, want still 2", triggerMaxDepth, len(got))
	}
	if got := testutil.ToFloat64(triggerLoop) - loops; got != 1 {
		t.Errorf("reticora_workflow_trigger_loop_total rose by %v, want 1 (AK5)", got)
	}
	if err := consumer.Handle(ctx, envelope(nil)); err != nil {
		t.Fatal(err)
	}
	if got := runsOf(plain); len(got) != 1 || got[0].TriggerDepth != 0 {
		t.Errorf("runs of an event without origin: %+v, want one at depth 0", got)
	}
}

// failingDefinitions is an event store whose definitions cannot be read:
// Handle fails every delivery.
type failingDefinitions struct{ *MemoryRepository }

func (failingDefinitions) EventDefinitions(context.Context, string) ([]Definition, error) {
	return nil, errors.New("definitions unavailable")
}

// fakeTriggerMsg is a JetStream message of the trigger consumer that
// records how it was settled.
type fakeTriggerMsg struct {
	jetstream.Msg
	data                 []byte
	delivered            uint64
	acked, naked, termed bool
}

func (m *fakeTriggerMsg) Data() []byte    { return m.data }
func (m *fakeTriggerMsg) Subject() string { return "events.ticket.updated" }
func (m *fakeTriggerMsg) Metadata() (*jetstream.MsgMetadata, error) {
	return &jetstream.MsgMetadata{NumDelivered: m.delivered}, nil
}
func (m *fakeTriggerMsg) Ack() error  { m.acked = true; return nil }
func (m *fakeTriggerMsg) Nak() error  { m.naked = true; return nil }
func (m *fakeTriggerMsg) Term() error { m.termed = true; return nil }

// TestTriggerDeadAfterMaxDeliver covers WP-1090 AK1: a delivery that fails
// is delivered again (Nak) until MaxDeliver; the last one is terminated and
// counted in reticora_workflow_trigger_dead_total, as a malformed event is.
func TestTriggerDeadAfterMaxDeliver(t *testing.T) {
	ctx := context.Background()
	consumer := NewTriggerConsumer(NewExecutor(NewMemoryRepository(), nil, nil, nil, nil), failingDefinitions{NewMemoryRepository()}, nil,
		func(context.Context, string) bool { return true })
	data, err := json.Marshal(events.Envelope{ID: "00000000-0000-4000-9000-000000000001", Type: "ticket.updated", OrgID: "org-1",
		OccurredAt: time.Now(), Data: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	dead := testutil.ToFloat64(triggerDead)
	early := &fakeTriggerMsg{data: data, delivered: 1}
	consumer.process(ctx, early)
	if !early.naked || early.termed || early.acked {
		t.Errorf("first failed delivery: %+v, want Nak", early)
	}
	if got := testutil.ToFloat64(triggerDead) - dead; got != 0 {
		t.Errorf("dead deliveries after a Nak: %v, want 0", got)
	}
	last := &fakeTriggerMsg{data: data, delivered: triggerMaxDeliver}
	consumer.process(ctx, last)
	if !last.termed || last.naked || last.acked {
		t.Errorf("delivery %d: %+v, want Term", triggerMaxDeliver, last)
	}
	if got := testutil.ToFloat64(triggerDead) - dead; got != 1 {
		t.Errorf("reticora_workflow_trigger_dead_total rose by %v, want 1", got)
	}
	malformed := &fakeTriggerMsg{data: []byte(`{`), delivered: 1}
	consumer.process(ctx, malformed)
	if !malformed.termed || testutil.ToFloat64(triggerDead)-dead != 2 {
		t.Errorf("malformed event: %+v, dead %v, want Term and counted", malformed, testutil.ToFloat64(triggerDead)-dead)
	}
}

// flakySubmit is a run queue whose first submit fails.
type flakySubmit struct {
	calls int
	keys  []string
}

func (q *flakySubmit) Submit(_ context.Context, _, _ string, _ any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error) {
	q.calls++
	if q.calls == 1 {
		return nil, false, errors.New("nats unavailable")
	}
	q.keys = append(q.keys, opts.IdempotencyKey)
	return &jobs.Job{}, true, nil
}

// TestEventRunQueuedAgainOnRedelivery: the submit of the job of an event
// run fails on the first delivery; the run stays pending and the delivery
// again (after the Nak) queues it under the idempotency key of its enqueue
// instead of skipping the stored run (WFL-02, WP-1090).
func TestEventRunQueuedAgainOnRedelivery(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	queue := &flakySubmit{}
	exec := NewExecutor(repo, nil, nil, nil, nil).WithQueue(queue)
	def := &Definition{OrganizationID: "org-1", Name: "queued", Trigger: JSONMap{"type": "event", "event": "ticket.updated"},
		Actions: []JSONMap{{"type": "noop"}}, Active: true}
	if err := repo.CreateDefinition(ctx, def); err != nil {
		t.Fatal(err)
	}
	consumer := NewTriggerConsumer(exec, repo, nil, func(context.Context, string) bool { return true })
	env := &events.Envelope{ID: "00000000-0000-4000-9000-000000000099", Type: "ticket.updated", OrgID: "org-1", OccurredAt: time.Now(),
		Data: json.RawMessage(`{}`)}
	if err := consumer.Handle(ctx, env); err == nil {
		t.Fatal("the first delivery succeeded although the submit failed")
	}
	runs, _, err := repo.ListRuns(ctx, "org-1", &RunFilter{WorkflowID: def.ID}, pageAll)
	if err != nil || len(runs) != 1 || runs[0].Status != StatusPending {
		t.Fatalf("runs after the failed submit: %+v %v, want one pending", runs, err)
	}
	if err = consumer.Handle(ctx, env); err != nil {
		t.Fatalf("delivery again: %v", err)
	}
	if want := RunJobKey(runs[0].ID, runs[0].Enqueues); len(queue.keys) != 1 || queue.keys[0] != want {
		t.Errorf("submitted jobs %v, want one with key %s", queue.keys, want)
	}
	if runs, _, _ = repo.ListRuns(ctx, "org-1", &RunFilter{WorkflowID: def.ID}, pageAll); len(runs) != 1 {
		t.Errorf("runs after the delivery again: %d, want still 1", len(runs))
	}
}
