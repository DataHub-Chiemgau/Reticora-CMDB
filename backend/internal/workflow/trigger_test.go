package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
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

// TestLifecycleShorthandToFilter covers WP-1104 AK1: the shorthand of a
// lifecycle trigger is stored as trigger.filter, an all of eq (one value)
// and in (a list) over data.from, data.to, data.model_key and
// data.entity_type; the shorthand keys are dropped.
func TestLifecycleShorthandToFilter(t *testing.T) {
	base := func(extra JSONMap) JSONMap {
		trigger := JSONMap{"type": "event", "event": "lifecycle.changed"}
		for k, v := range extra {
			trigger[k] = v
		}
		return trigger
	}
	for name, c := range map[string]struct {
		shorthand JSONMap
		want      []any
	}{
		"single": {JSONMap{"from_state": "deployed", "to_state": "retired", "model": "hardware", "entity_type": "ci"}, []any{
			map[string]any{"op": "eq", "field": "data.from", "value": "deployed"},
			map[string]any{"op": "eq", "field": "data.to", "value": "retired"},
			map[string]any{"op": "eq", "field": "data.model_key", "value": "hardware"},
			map[string]any{"op": "eq", "field": "data.entity_type", "value": "ci"},
		}},
		"list": {JSONMap{"from_state": []any{"deployed", "in_repair"}, "to_state": "retired"}, []any{
			map[string]any{"op": "in", "field": "data.from", "value": []any{"deployed", "in_repair"}},
			map[string]any{"op": "eq", "field": "data.to", "value": "retired"},
		}},
		"only to_state": {JSONMap{"to_state": "retired"}, []any{
			map[string]any{"op": "eq", "field": "data.to", "value": "retired"},
		}},
	} {
		got, err := normalizeTrigger(base(c.shorthand))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, key := range []string{"from_state", "to_state", "model", "entity_type"} {
			if _, has := got[key]; has {
				t.Errorf("%s: %s is stored, want only the filter", name, key)
			}
		}
		want := map[string]any{"op": "all", "rules": c.want}
		if gb, _ := json.Marshal(got["filter"]); string(gb) != mustJSON(t, want) {
			t.Errorf("%s: filter %s, want %s", name, gb, mustJSON(t, want))
		}
		if err := ValidateTrigger(got); err != nil {
			t.Errorf("%s: the stored filter is no valid MET-45 node: %v", name, err)
		}
		payload := JSONMap{"data": map[string]any{"from": "deployed", "to": "retired", "model_key": "hardware", "entity_type": "ci"}}
		if hold, ferr := filterHolds(got, payload); ferr != nil || !hold {
			t.Errorf("%s: filter on deployed → retired: %v %v, want it to hold", name, hold, ferr)
		}
		payload["data"] = map[string]any{"from": "in_stock", "to": "deployed", "model_key": "hardware", "entity_type": "ci"}
		if hold, _ := filterHolds(got, payload); hold {
			t.Errorf("%s: filter holds on in_stock → deployed", name)
		}
	}
	plain := JSONMap{"type": "event", "event": "ci.changed"}
	if got, err := normalizeTrigger(plain); err != nil || len(got) != 2 {
		t.Errorf("trigger without shorthand: %v %v, want it unchanged", got, err)
	}
	for field, trigger := range map[string]JSONMap{
		"trigger.entity_type": base(JSONMap{"entity_type": "rack"}),
		"trigger.to_state":    base(JSONMap{"to_state": []any{}}),
		"trigger.model":       base(JSONMap{"model": []any{"a", "b"}}),
		"trigger.from_state":  {"type": "event", "event": "ci.changed", "from_state": "deployed"},
	} {
		var fe *api.FieldError
		if _, err := normalizeTrigger(trigger); !errors.As(err, &fe) || fe.Field != field || fe.Status != http.StatusUnprocessableEntity {
			t.Errorf("%v: %v, want 422 on %s", trigger, err, field)
		}
	}
}

// TestLifecycleShorthandWithFilterRejected: the shorthand and a filter of
// its own are 422 on trigger.filter.
func TestLifecycleShorthandWithFilterRejected(t *testing.T) {
	_, err := normalizeTrigger(JSONMap{"type": "event", "event": "lifecycle.changed", "to_state": "retired",
		"filter": map[string]any{"op": "eq", "field": "data.reason", "value": "x"}})
	var fe *api.FieldError
	if !errors.As(err, &fe) || fe.Field != "trigger.filter" || fe.Status != http.StatusUnprocessableEntity {
		t.Fatalf("shorthand with filter: %v, want 422 on trigger.filter", err)
	}
}

// TestLegacyLifecycleEventRejected: the former name lifecycle.transitioned
// as event, alone or in a list, is 422 on trigger.event naming
// lifecycle.changed.
func TestLegacyLifecycleEventRejected(t *testing.T) {
	for _, event := range []any{"lifecycle.transitioned", []any{"ci.changed", "lifecycle.transitioned"}} {
		_, err := normalizeTrigger(JSONMap{"type": "event", "event": event})
		var fe *api.FieldError
		if !errors.As(err, &fe) || fe.Field != "trigger.event" || !strings.Contains(fe.Message, "lifecycle.changed") {
			t.Errorf("event %v: %v, want 422 on trigger.event with lifecycle.changed", event, err)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
