package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
)

// The action send_webhook on PostgreSQL (WFL-02, WP-1096, E-231 option 2):
// the event workflow.webhook is written to the outbox in the transaction of
// the succeeded step and reaches only the webhook subscriptions of the
// organization through Dispatcher.Consume (signature, egress policy); a
// definition that names a target of its own is refused.
//
// They run only with TEST_DATABASE_URL (scopetest.Seed); the retry case
// also with TEST_NATS_URL.
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestWebhookAction' ./internal/workflow/

// receiver is the fake receiver of the subscriptions: deliveries by path.
type receiver struct {
	mu     sync.Mutex
	got    map[string][]map[string]any
	signed atomic.Int32
	srv    *httptest.Server
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()
	rc := &receiver{got: map[string][]map[string]any{}}
	rc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.Header.Get("X-Reticora-Signature") != "" && r.Header.Get("X-Reticora-Event") == events.WorkflowWebhook {
			rc.signed.Add(1)
		}
		rc.mu.Lock()
		rc.got[strings.TrimPrefix(r.URL.Path, "/")] = append(rc.got[strings.TrimPrefix(r.URL.Path, "/")], body)
		rc.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(rc.srv.Close)
	return rc
}

func (rc *receiver) count(name string) int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return len(rc.got[name])
}

type webhookEnv struct {
	f    *scopetest.Fixture
	repo *PGRepository
	exec *Executor
	ctx  context.Context
	rc   *receiver
	subs *webhook.PGRepository
}

func newWebhookEnv(t *testing.T) *webhookEnv {
	t.Helper()
	f := scopetest.Seed(t, "3f")
	bg := context.Background()
	user := f.AppUser(t, f.OrgA, "wf-webhook-actor")
	scope := database.OrgWideScope(f.OrgA, user)
	e := &webhookEnv{f: f, repo: NewPGRepository(f.App), rc: newReceiver(t), subs: webhook.NewPGRepository(f.App),
		ctx: tenant.WithTenant(database.ContextWithTenantScope(bg, &scope), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})}
	e.exec = NewExecutor(e.repo, nil, ci.NewPGRepository(f.App), nil, nil).WithEventOutbox()
	// A subscription of organization A and one of organization B, both on
	// workflow.webhook.
	for _, s := range []struct{ org, name string }{{f.OrgA, "own"}, {f.OrgB, "foreign"}} {
		sub := &webhook.Subscription{OrganizationID: s.org, Name: "wf-webhook-" + s.name, URL: e.rc.srv.URL + "/" + s.name,
			Secret: "wf-secret", Events: []string{events.WorkflowWebhook}, IsActive: true}
		if err := e.subs.Create(f.OrgCtx(s.org), sub); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

// definition stores an active definition with actions.
func (e *webhookEnv) definition(t *testing.T, actions ...JSONMap) *Definition {
	t.Helper()
	def := &Definition{OrganizationID: e.f.OrgA, Name: fmt.Sprintf("wf-webhook-%d", time.Now().UnixNano()), Trigger: JSONMap{"type": "manual"},
		Actions: actions, Active: true}
	if err := e.repo.CreateDefinition(e.ctx, def); err != nil {
		t.Fatal(err)
	}
	return def
}

// outbox returns the events workflow.webhook of a run.
func outboxOf(t *testing.T, f *scopetest.Fixture, runID string) []events.Envelope {
	t.Helper()
	rows, err := f.Admin.Query(context.Background(), `SELECT id::text, organization_id::text, type, COALESCE(entity_id, ''), sequence,
		COALESCE(entity_seq, 0), data FROM event_outbox WHERE type = $1 AND entity_id = $2 ORDER BY sequence`, events.WorkflowWebhook, runID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []events.Envelope
	for rows.Next() {
		var env events.Envelope
		if err := rows.Scan(&env.ID, &env.OrgID, &env.Type, &env.EntityID, &env.Sequence, &env.EntitySeq, &env.Data); err != nil {
			t.Fatal(err)
		}
		out = append(out, env)
	}
	return out
}

// deliver hands the envelopes to a dispatcher as the publisher does after
// the commit (twice: at least once) and waits for the deliveries.
func (e *webhookEnv) deliver(t *testing.T, envs []events.Envelope) {
	t.Helper()
	d := webhook.NewDispatcher(e.subs, egress.NewClient(egress.Options{AllowPrivate: true}),
		webhook.DispatcherOptions{Deliveries: webhook.NewPGDeliveryStore(e.f.App), PollInterval: -1})
	for range 2 {
		for i := range envs {
			if err := d.Consume(context.Background(), &envs[i]); err != nil {
				t.Fatalf("consume %s: %v", envs[i].ID, err)
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := d.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestWebhookActionWritesOutboxInStepTx: the succeeded step has exactly
// one outbox row workflow.webhook with run, step, the CI and the rendered
// template; the step output names topic and event; the subscription of
// the organization receives it signed.
func TestWebhookActionWritesOutboxInStepTx(t *testing.T) {
	e := newWebhookEnv(t)
	ciID := e.f.CI(t, e.f.OrgA, e.f.Client1, "wf-webhook-ci")
	def := e.definition(t, JSONMap{"type": "noop"}, JSONMap{"type": "send_webhook", "topic": "server.down",
		"payload": map[string]any{"host": "${data.host}", "missing": "${data.nothing}", "fixed": "host ${data.host}", "n": 3.0}})
	run, err := e.exec.Trigger(e.ctx, e.f.OrgA, def, "manual", JSONMap{"ci_id": ciID, "data": map[string]any{"host": "srv-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusSucceeded || len(run.Steps) != 2 || run.Steps[1].Status != StatusSucceeded {
		t.Fatalf("run %s steps %+v, want succeeded", run.Status, run.Steps)
	}
	envs := outboxOf(t, e.f, run.ID)
	if len(envs) != 1 {
		t.Fatalf("%d outbox rows of the run, want 1", len(envs))
	}
	var data map[string]any
	if err := json.Unmarshal(envs[0].Data, &data); err != nil {
		t.Fatal(err)
	}
	payload, _ := data["payload"].(map[string]any)
	if data["run_id"] != run.ID || data["id"] != run.ID || data["workflow_id"] != def.ID || data["definition_version_id"] != run.DefinitionVersionID ||
		data["step_index"] != 1.0 || data["topic"] != "server.down" || data["ci_id"] != ciID || data["client_id"] != e.f.Client1 {
		t.Errorf("event data %v", data)
	}
	if payload["host"] != "srv-1" || payload["missing"] != nil || payload["fixed"] != "host ${data.host}" || payload["n"] != 3.0 {
		t.Errorf("payload %v, want the rendered template", payload)
	}
	if _, has := payload["missing"]; !has {
		t.Errorf("payload %v: the unknown path is null, not absent", payload)
	}
	if out := run.Steps[1].Output; out["topic"] != "server.down" || out["event_id"] != envs[0].ID {
		t.Errorf("step output %v, want topic and event_id %s", out, envs[0].ID)
	}
	e.deliver(t, envs)
	if e.rc.count("own") != 1 || e.rc.signed.Load() != 1 {
		t.Errorf("deliveries to the own subscription: %d (signed %d), want 1", e.rc.count("own"), e.rc.signed.Load())
	}
}

// TestWebhookActionRollbackWritesNothing: an error after the event in the
// same transaction rolls it back: no outbox row, no delivery, the step is
// failed.
func TestWebhookActionRollbackWritesNothing(t *testing.T) {
	e := newWebhookEnv(t)
	e.repo.afterAppend = func() error { return errors.New("failure after the event") }
	def := e.definition(t, JSONMap{"type": "send_webhook", "topic": "rolled.back"})
	run, err := e.exec.Trigger(e.ctx, e.f.OrgA, def, "manual", JSONMap{})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusFailed || len(run.Steps) != 1 || run.Steps[0].Status != StatusFailed {
		t.Fatalf("run %s steps %+v, want the step failed", run.Status, run.Steps)
	}
	if _, has := run.Steps[0].Output["event_id"]; has {
		t.Errorf("step output %v names an event", run.Steps[0].Output)
	}
	if envs := outboxOf(t, e.f, run.ID); len(envs) != 0 {
		t.Fatalf("%d outbox rows after the rollback, want none", len(envs))
	}
	e.deliver(t, nil)
	if n := e.rc.count("own"); n != 0 {
		t.Errorf("%d deliveries after the rollback", n)
	}
}

// TestWebhookActionRetryDeliversOnce: a run whose later step fails in the
// first attempt is retried by the job (WP-1088); the succeeded webhook
// step is not executed again: one outbox row, one delivery per
// subscription.
func TestWebhookActionRetryDeliversOnce(t *testing.T) {
	w := newWorkerEnv(t, 5, true)
	w.exec.WithEventOutbox()
	subs := webhook.NewPGRepository(w.f.App)
	rc := newReceiver(t)
	for _, name := range []string{"first", "second"} {
		if err := subs.Create(w.f.OrgCtx(w.f.OrgA), &webhook.Subscription{OrganizationID: w.f.OrgA, Name: "wf-retry-" + name,
			URL: rc.srv.URL + "/" + name, Secret: "s", Events: []string{events.WorkflowWebhook}, IsActive: true}); err != nil {
			t.Fatal(err)
		}
	}
	d := w.form(t, w.ctx, "")
	w.forms.fail = 1
	run := w.start(t, w.ctx, JSONMap{"type": "send_webhook", "topic": "retry.once"},
		JSONMap{"type": "submit_form", "form_id": d.ID, "values": map[string]any{"hostname": "srv-r"}})
	if _, err := w.exec.Execute(w.ctx, w.f.OrgA, nil, run); err != nil {
		t.Fatal(err)
	}
	done := w.await(t, run.ID)
	if done.Status != StatusSucceeded || done.Attempts != 2 {
		t.Fatalf("run %s attempts %d %q, want succeeded after 2 attempts", done.Status, done.Attempts, done.LastError)
	}
	envs := outboxOf(t, w.f, run.ID)
	if len(envs) != 1 {
		t.Fatalf("%d outbox rows after the retry, want 1", len(envs))
	}
	e := &webhookEnv{f: w.f, subs: subs, rc: rc}
	e.deliver(t, envs)
	if rc.count("first") != 1 || rc.count("second") != 1 {
		t.Errorf("deliveries first %d second %d, want one each", rc.count("first"), rc.count("second"))
	}
}

// TestWebhookActionOnlyOwnSubscriptions: the subscription of another
// organization on workflow.webhook receives nothing.
func TestWebhookActionOnlyOwnSubscriptions(t *testing.T) {
	e := newWebhookEnv(t)
	def := e.definition(t, JSONMap{"type": "send_webhook", "topic": "own.only", "payload": map[string]any{"k": "v"}})
	run, err := e.exec.Trigger(e.ctx, e.f.OrgA, def, "manual", JSONMap{})
	if err != nil || run.Status != StatusSucceeded {
		t.Fatalf("run %v: %v", run, err)
	}
	e.deliver(t, outboxOf(t, e.f, run.ID))
	if e.rc.count("own") != 1 || e.rc.count("foreign") != 0 {
		t.Errorf("deliveries own %d foreign %d, want 1 and 0", e.rc.count("own"), e.rc.count("foreign"))
	}
}

// TestWebhookActionRejectsURL: a definition whose send_webhook names url
// (or headers, secret, event) or lacks a valid topic is 422 on the path
// of the field, on POST and PATCH.
func TestWebhookActionRejectsURL(t *testing.T) {
	e := newWebhookEnv(t)
	mux := chi.NewRouter()
	NewHandler(e.repo, e.exec).RegisterRoutes(mux)
	do := func(method, path string, body any) (int, map[string]any) {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(e.ctx)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	create := func(action map[string]any) (int, map[string]any) {
		return do(http.MethodPost, "/api/v1/workflows", map[string]any{"name": fmt.Sprintf("wf-reject-%d", time.Now().UnixNano()),
			"trigger": map[string]any{"type": "manual"}, "actions": []any{action}})
	}
	code, body := create(map[string]any{"type": "send_webhook", "topic": "x", "url": "https://evil.example"})
	if code != http.StatusUnprocessableEntity || body["field"] != "actions[0].url" {
		t.Errorf("url: %d %v, want 422 on actions[0].url", code, body)
	}
	for field, value := range map[string]any{"headers": map[string]any{"a": "b"}, "secret": "s", "event": "ci.changed"} {
		code, body = create(map[string]any{"type": "send_webhook", "topic": "x", field: value})
		if code != http.StatusUnprocessableEntity || body["field"] != "actions[0]."+field {
			t.Errorf("%s: %d %v, want 422 on actions[0].%s", field, code, body, field)
		}
	}
	for name, action := range map[string]map[string]any{
		"no topic":    {"type": "send_webhook"},
		"bad topic":   {"type": "send_webhook", "topic": "Not Valid"},
		"long topic":  {"type": "send_webhook", "topic": strings.Repeat("a", 65)},
		"bad payload": {"type": "send_webhook", "topic": "x", "payload": []any{1}},
	} {
		code, body = create(action)
		want := "actions[0].topic"
		if name == "bad payload" {
			want = "actions[0].payload"
		}
		if code != http.StatusUnprocessableEntity || body["field"] != want {
			t.Errorf("%s: %d %v, want 422 on %s", name, code, body, want)
		}
	}
	code, body = create(map[string]any{"type": "send_webhook", "topic": "ok.topic", "payload": map[string]any{"a": "${data.a}"}})
	if code != http.StatusCreated {
		t.Fatalf("valid action: %d %v", code, body)
	}
	id, _ := body["id"].(string)
	code, body = do(http.MethodPatch, "/api/v1/workflows/"+id, map[string]any{"actions": []any{map[string]any{"type": "noop"},
		map[string]any{"type": "send_webhook", "topic": "x", "url": "https://evil.example"}}})
	if code != http.StatusUnprocessableEntity || body["field"] != "actions[1].url" {
		t.Errorf("PATCH with url: %d %v, want 422 on actions[1].url", code, body)
	}
}

// TestWebhookActionStoredEventIsTopic: a definition stored before WP-1096
// with event keeps running, the event as topic; the data are too large
// above 64 KiB.
func TestWebhookActionStoredEventIsTopic(t *testing.T) {
	e := newWebhookEnv(t)
	def := e.definition(t, JSONMap{"type": "send_webhook", "event": "legacy.event"})
	run, err := e.exec.Trigger(e.ctx, e.f.OrgA, def, "manual", JSONMap{})
	if err != nil || run.Status != StatusSucceeded || run.Steps[0].Output["topic"] != "legacy.event" {
		t.Fatalf("stored event: %v %+v, want succeeded with topic legacy.event", err, run)
	}
	// A free event name of the time before WP-1096 is brought into the
	// form of a topic instead of failing the step.
	for event, topic := range map[string]string{"Server-Down": "server-down", "host down": "host_down",
		strings.Repeat("Ä", 70): strings.Repeat("_", 64)} {
		legacy := e.definition(t, JSONMap{"type": "send_webhook", "event": event})
		run, err = e.exec.Trigger(e.ctx, e.f.OrgA, legacy, "manual", JSONMap{})
		if err != nil || run.Status != StatusSucceeded || run.Steps[0].Output["topic"] != topic {
			t.Fatalf("stored event %q: %v %+v, want succeeded with topic %s", event, err, run, topic)
		}
	}
	big := e.definition(t, JSONMap{"type": "send_webhook", "topic": "big", "payload": map[string]any{"blob": strings.Repeat("x", maxWebhookData)}})
	run, err = e.exec.Trigger(e.ctx, e.f.OrgA, big, "manual", JSONMap{})
	if err != nil || run.Status != StatusFailed || run.Steps[0].Error != errWebhookTooLarge.Error() {
		t.Fatalf("large payload: %v %+v, want failed with %q", err, run, errWebhookTooLarge)
	}
	if envs := outboxOf(t, e.f, run.ID); len(envs) != 0 {
		t.Errorf("%d outbox rows of the failed step", len(envs))
	}
}

// TestWebhookActionAppendEventOnceForParallelAttempts: two attempts that execute
// the same send_webhook step at once (a redelivery while the first still
// runs) write one event: the second finds the step succeeded.
func TestWebhookActionAppendEventOnceForParallelAttempts(t *testing.T) {
	e := newWebhookEnv(t)
	action := JSONMap{"type": "send_webhook", "topic": "parallel.once"}
	def := e.definition(t, action)
	run, err := e.exec.Start(e.ctx, e.f.OrgA, def, "manual", JSONMap{})
	if err != nil {
		t.Fatal(err)
	}
	step := &Step{OrganizationID: e.f.OrgA, RunID: run.ID, StepIndex: 0, ActionType: webhookActionType, Status: StatusRunning}
	if err = e.repo.AppendStep(e.ctx, step); err != nil {
		t.Fatal(err)
	}
	event, _, err := e.exec.webhookEvent(e.ctx, e.f.OrgA, run, 0, action)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = e.repo.AppendEvent(e.ctx, e.f.OrgA, step.ID, JSONMap{"topic": "parallel.once"}, event)
		}()
	}
	close(start)
	wg.Wait()
	ok, already := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, errStepSucceeded):
			already++
		default:
			t.Errorf("AppendEvent: %v", err)
		}
	}
	if ok != 1 || already != 1 {
		t.Errorf("AppendEvent results %v, want one success and one errStepSucceeded", errs)
	}
	if envs := outboxOf(t, e.f, run.ID); len(envs) != 1 {
		t.Errorf("%d outbox rows of the step, want 1", len(envs))
	}
}
