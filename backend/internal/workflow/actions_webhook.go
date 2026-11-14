package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// The action send_webhook (WFL-02, WP-1096, E-231 option 2) hands an event
// workflow.webhook to the webhook subscriptions of the organization; it
// names no target of its own:
//
//	{"type": "send_webhook", "topic": "server.down",
//	 "payload": {"host": "${data.host}", "fixed": 1}}
//
// With PostgreSQL the event is written to the event outbox in the
// transaction that sets the step succeeded (Repository.AppendEvent): a
// rolled-back step writes nothing, a succeeded step is not executed again
// (WP-1088), so there is one event per step. The publisher hands it to
// Dispatcher.Consume after the commit (signature, egress policy, retries
// and dead letters). Without the outbox (--no-db) the event goes to
// Dispatcher.Dispatch once the step is stored succeeded.

// webhookActionType is the type of the action.
const webhookActionType = "send_webhook"

// legacyWebhookEvent was the default event name of send_webhook before
// WP-1096; a stored definition without topic and event keeps it as topic.
const legacyWebhookEvent = "workflow.event"

// maxWebhookData bounds the serialized data of the event.
const maxWebhookData = 64 << 10

// topicPattern is the form of a topic.
var topicPattern = regexp.MustCompile(`^[a-z0-9_.-]{1,64}$`)

// webhookTargetFields name a target of the action itself, which E-231
// refuses: the target is a subscription of the organization.
var webhookTargetFields = []string{"url", "headers", "secret", "event"}

// Fixed texts of a failed send_webhook step.
var (
	errWebhookTooLarge  = errors.New("webhook payload exceeds 64 KiB")
	errWebhookAction    = errors.New("invalid send_webhook action")
	errWebhookNoOutbox  = errors.New("webhook events unavailable")
	errWebhookNoPayload = errors.New("webhook payload must be an object")
)

// errStepSucceeded: AppendEvent found the step succeeded already, another
// attempt wrote its event; this attempt writes none.
var errStepSucceeded = errors.New("workflow step succeeded already")

// WithEventOutbox makes send_webhook write workflow.webhook to the event
// outbox of the step's transaction (Repository.AppendEvent, WP-1096 AK3);
// the router sets it with the PostgreSQL repositories.
func (e *Executor) WithEventOutbox() *Executor {
	e.eventOutbox = true
	return e
}

// validateWebhookAction checks action i of a definition that is written
// (POST and PATCH /api/v1/workflows): a send_webhook action names a topic
// of topicPattern, an optional payload object and no target of its own
// (url, headers, secret, event: 422 on actions[i].<field>, E-231).
func validateWebhookAction(i int, action JSONMap) error {
	if typ, _ := action["type"].(string); typ != webhookActionType {
		return nil
	}
	fieldErr := func(field, message string) error {
		return &api.FieldError{Field: fmt.Sprintf("actions[%d].%s", i, field), Message: message, Status: http.StatusUnprocessableEntity}
	}
	for _, field := range webhookTargetFields {
		if _, has := action[field]; has {
			return fieldErr(field, "is not allowed: send_webhook delivers to the webhook subscriptions of the organization")
		}
	}
	topic, ok := action["topic"].(string)
	if !ok || !topicPattern.MatchString(topic) {
		return fieldErr("topic", "is required and must match ^[a-z0-9_.-]{1,64}$")
	}
	if payload, has := action["payload"]; has && payload != nil {
		if _, isObject := payload.(map[string]any); !isObject {
			return fieldErr("payload", "must be an object")
		}
	}
	return nil
}

// validateActions checks the actions of a definition that is written.
func validateActions(actions []JSONMap) error {
	for i, action := range actions {
		if err := validateWebhookAction(i, action); err != nil {
			return err
		}
	}
	return nil
}

// webhookTopic returns the topic of a stored action: topic, else the event
// of a definition stored before WP-1096 in the form of topicPattern
// (legacyTopic), else the former default.
func webhookTopic(action JSONMap) (string, error) {
	topic, _ := action["topic"].(string)
	if topic == "" {
		if event, _ := action["event"].(string); event != "" {
			return legacyTopic(event), nil
		}
		topic = legacyWebhookEvent
	}
	if !topicPattern.MatchString(topic) {
		return "", errWebhookAction
	}
	return topic, nil
}

// legacyTopic brings the free event name of a definition stored before
// WP-1096 into the form of topicPattern: lower case, every other character
// becomes "_", at most 64 characters. "Server-Down" is server-down, "host
// down" host_down.
func legacyTopic(event string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(event) {
		if b.Len() == 64 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// webhookPlaceholder is a string value of the payload that names a path of
// the run context: exactly ${<path>}.
var webhookPlaceholder = regexp.MustCompile(`^\$\{([A-Za-z0-9_.\-]+)\}$`)

// renderPayload replaces each string value of v that is exactly ${<path>}
// by the value of the flat run context under that dot path (null for an
// unknown one); every other value stays as it is.
func renderPayload(v any, values map[string]any) any {
	switch t := v.(type) {
	case string:
		m := webhookPlaceholder.FindStringSubmatch(t)
		if m == nil {
			return t
		}
		if value, ok := values[m[1]]; ok {
			return value
		}
		return nil
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, item := range t {
			out[k] = renderPayload(item, values)
		}
		return out
	case JSONMap:
		return renderPayload(map[string]any(t), values)
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = renderPayload(item, values)
		}
		return out
	}
	return v
}

// webhookEvent builds the event workflow.webhook of step index of run: run,
// workflow, version, step, topic, the CI with client and site (the scope
// rule of the type, WP-868) and the rendered payload.
func (e *Executor) webhookEvent(ctx context.Context, orgID string, run *Run, index int, action JSONMap) (*events.Event, string, error) {
	topic, err := webhookTopic(action)
	if err != nil {
		return nil, "", err
	}
	payload := map[string]any{}
	if raw, has := action["payload"]; has && raw != nil {
		object, isObject := raw.(map[string]any)
		if !isObject {
			return nil, "", errWebhookNoPayload
		}
		payload = renderPayload(object, EvaluationValues(run.Context)).(map[string]any)
	}
	data := map[string]any{"id": run.ID, "run_id": run.ID, "workflow_id": run.WorkflowID,
		"definition_version_id": run.DefinitionVersionID, "step_index": index, "topic": topic, "payload": payload}
	if run.CIID != "" {
		data["ci_id"] = run.CIID
		if e.cis != nil {
			item, cerr := e.cis.GetByID(ctx, orgID, run.CIID)
			if cerr != nil {
				return nil, "", fmt.Errorf("read the CI of the run: %w", cerr)
			}
			if item.ClientID != "" {
				data["client_id"] = item.ClientID
			}
			if item.SiteID != "" {
				data["site_id"] = item.SiteID
			}
		}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, "", errWebhookAction
	}
	if len(raw) > maxWebhookData {
		return nil, "", errWebhookTooLarge
	}
	return &events.Event{OrgID: orgID, Type: events.WorkflowWebhook, EntityID: run.ID, Data: data}, topic, nil
}

// sendWebhook executes the send_webhook action of step and stores the step
// succeeded together with its event (WP-1096 AK3, AK5).
func (e *Executor) sendWebhook(ctx context.Context, orgID string, run *Run, step *Step, action JSONMap) (JSONMap, error) {
	event, topic, err := e.webhookEvent(ctx, orgID, run, step.StepIndex, action)
	if err != nil {
		return nil, err
	}
	output := JSONMap{"topic": topic}
	if e.eventOutbox {
		eventID, aerr := e.repo.AppendEvent(ctx, orgID, step.ID, output, event)
		if errors.Is(aerr, errStepSucceeded) {
			// Another attempt executed the step at the same time and
			// wrote its event; the step stays as that attempt stored it.
			return output, nil
		}
		if aerr != nil {
			return nil, aerr
		}
		output["event_id"] = eventID
		return output, nil
	}
	if e.dispatcher == nil {
		return nil, errWebhookNoOutbox
	}
	if _, err := e.repo.UpdateStep(ctx, orgID, step.ID, StatusSucceeded, output, ""); err != nil {
		return nil, fmt.Errorf("update workflow step: %w", err)
	}
	e.dispatcher.Dispatch(ctx, orgID, events.WorkflowWebhook, event.Data)
	return output, nil
}
