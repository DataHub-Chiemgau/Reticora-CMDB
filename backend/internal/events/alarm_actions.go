package events

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// AlarmActions are the follow-up actions of an alert rule (MON-04,
// WP-988): whether the alarms of the rule notify (notify, WP-987) and
// whether alarm.* reach the webhook subscriptions (webhook.Dispatcher).
// The alarm carries them in details.actions and every alarm.* event in
// its data, so the consumers decide without reading the rule. The type
// lives here because package monitoring (the rule API), alarm (the
// alarm) and the consumers notify and webhook all read it; alarm.Actions
// names it in the alarm domain.
//
// ticket (WP-1040, E-205 option 2) opens a ticket for an alarm of the rule
// (off by default). The key workflow (WP-1110) is reserved: alarms as
// workflow triggers bind at the workflow definition (WP-1090/WP-1110), not
// at the rule, so a second path rule → workflow would start the same runs
// twice.
type AlarmActions struct {
	Notify  AlarmAction `json:"notify"`
	Webhook AlarmAction `json:"webhook"`
	// Ticket is the ticket action; nil is off.
	Ticket *AlarmTicketAction `json:"ticket,omitempty"`
}

// AlarmTicketAction opens a ticket for an alarm of the rule (WP-1040):
// priority (low, medium, high, critical; empty: from the severity),
// category, the reporter (the user who saved the rule; Konvention 46) and
// whether the resolution of the alarm resolves the ticket.
type AlarmTicketAction struct {
	Enabled        bool   `json:"enabled"`
	Priority       string `json:"priority,omitempty"`
	Category       string `json:"category,omitempty"`
	ReporterID     string `json:"reporter_id,omitempty"`
	CloseOnResolve bool   `json:"close_on_resolve"`
}

// TicketPriorities are the priorities of a ticket action (ticket.Priorities).
var TicketPriorities = []string{"low", "medium", "high", "critical"}

// maxTicketCategory bounds the category of a ticket action.
const maxTicketCategory = 100

// AlarmAction switches one follow-up action.
type AlarmAction struct {
	Enabled bool `json:"enabled"`
}

// DefaultAlarmActions are the actions of a rule without its own and of
// the alarms of the other sources (check, collector, …): notify and
// webhook on.
func DefaultAlarmActions() AlarmActions {
	return AlarmActions{Notify: AlarmAction{Enabled: true}, Webhook: AlarmAction{Enabled: true}}
}

// Action keys of an actions object.
const (
	AlarmActionNotify   = "notify"
	AlarmActionWebhook  = "webhook"
	AlarmActionTicket   = "ticket"
	AlarmActionWorkflow = "workflow"
)

// reservedAlarmActions are the keys that later work packages fill, with
// the hint of the 422.
var reservedAlarmActions = map[string]string{
	AlarmActionWorkflow: "reserved: alarms start workflows through an event trigger of the workflow definition (WP-1110)",
}

// ParseAlarmActions reads an actions object: absent, null and {} are the
// defaults, a missing key keeps its default. ticket is the ticket action
// (parseTicketAction); workflow is 422 with the field actions.workflow and
// a hint, another
// key, a value that is no object, an unknown key in an action and an
// enabled that is no boolean are 422 with the field.
// validation: fixed texts naming the field.
func ParseAlarmActions(raw json.RawMessage) (AlarmActions, error) {
	out := DefaultAlarmActions()
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return out, nil
	}
	var keys map[string]json.RawMessage
	if trimmed[0] != '{' || json.Unmarshal(trimmed, &keys) != nil {
		return out, actionsField("actions", "must be an object")
	}
	// The reserved key first, then the unknown ones.
	if _, ok := keys[AlarmActionWorkflow]; ok {
		return out, actionsField("actions."+AlarmActionWorkflow, reservedAlarmActions[AlarmActionWorkflow])
	}
	for key := range keys {
		if key != AlarmActionNotify && key != AlarmActionWebhook && key != AlarmActionTicket {
			return out, actionsField("actions."+key, "unknown action; known are notify, webhook and ticket")
		}
	}
	if raw, ok := keys[AlarmActionTicket]; ok {
		t, err := parseTicketAction(raw)
		if err != nil {
			return out, err
		}
		out.Ticket = t
	}
	for key, target := range map[string]*AlarmAction{AlarmActionNotify: &out.Notify, AlarmActionWebhook: &out.Webhook} {
		value, ok := keys[key]
		if !ok {
			continue
		}
		if err := parseAlarmAction("actions."+key, value, target); err != nil {
			return out, err
		}
	}
	return out, nil
}

// parseAlarmAction reads {"enabled": bool} into target; null keeps the
// default.
func parseAlarmAction(field string, raw json.RawMessage, target *AlarmAction) error {
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var keys map[string]json.RawMessage
	if len(trimmed) == 0 || trimmed[0] != '{' || json.Unmarshal(trimmed, &keys) != nil {
		return actionsField(field, "must be an object such as {\"enabled\": true}")
	}
	for key, value := range keys {
		if key != "enabled" {
			return actionsField(field+"."+key, "unknown key; known is enabled")
		}
		// json.Unmarshal leaves a bool unchanged on null, so null is
		// checked here: enabled is a required boolean.
		var enabled bool
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, &enabled) != nil {
			return actionsField(field+".enabled", "must be a boolean")
		}
		target.Enabled = enabled
	}
	return nil
}

// parseTicketAction reads {"enabled", "priority", "category",
// "reporter_id", "close_on_resolve"}; null is off.
func parseTicketAction(raw json.RawMessage) (*AlarmTicketAction, error) {
	const field = "actions.ticket"
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var keys map[string]json.RawMessage
	if len(trimmed) == 0 || trimmed[0] != '{' || json.Unmarshal(trimmed, &keys) != nil {
		return nil, actionsField(field, "must be an object such as {\"enabled\": true}")
	}
	out := &AlarmTicketAction{}
	for key, value := range keys {
		value = bytes.TrimSpace(value)
		switch key {
		case "enabled", "close_on_resolve":
			var b bool
			if bytes.Equal(value, []byte("null")) || json.Unmarshal(value, &b) != nil {
				return nil, actionsField(field+"."+key, "must be a boolean")
			}
			if key == "enabled" {
				out.Enabled = b
			} else {
				out.CloseOnResolve = b
			}
		case "priority", "category", "reporter_id":
			var v string
			if bytes.Equal(value, []byte("null")) {
				continue
			}
			if json.Unmarshal(value, &v) != nil {
				return nil, actionsField(field+"."+key, "must be a string")
			}
			switch key {
			case "priority":
				if v != "" && !slices.Contains(TicketPriorities, v) {
					return nil, actionsField(field+".priority", "must be one of low, medium, high, critical")
				}
				out.Priority = v
			case "category":
				if len(v) > maxTicketCategory {
					return nil, actionsField(field+".category", "must be at most 100 characters")
				}
				out.Category = v
			default:
				if v != "" && !api.IsUUID(v) {
					return nil, actionsField(field+".reporter_id", "must be a user id")
				}
				out.ReporterID = v
			}
		default:
			return nil, actionsField(field+"."+key, "unknown key; known are enabled, priority, category, reporter_id and close_on_resolve")
		}
	}
	return out, nil
}

func actionsField(field, message string) error {
	return &api.FieldError{Field: field, Message: message, Status: http.StatusUnprocessableEntity}
}

// ParseStoredAlarmActions reads the stored actions of an alert_rule row
// leniently: unknown and reserved keys and unreadable values keep the
// defaults instead of failing, so one row written by a newer release
// (ticket, workflow) or by hand does not stop the evaluator of all
// organizations. The strict ParseAlarmActions checks on write.
func ParseStoredAlarmActions(raw json.RawMessage) AlarmActions {
	var decoded map[string]any
	if json.Unmarshal(bytes.TrimSpace(raw), &decoded) != nil {
		return DefaultAlarmActions()
	}
	return AlarmActionsOf(map[string]any{"actions": decoded})
}

// AlarmActionsOf reads the actions of alarm.* data (decoded JSON): absent
// or unreadable actions are the defaults, so an event written before
// WP-988 still notifies and reaches the webhooks.
func AlarmActionsOf(data map[string]any) AlarmActions {
	out := DefaultAlarmActions()
	actions, ok := data["actions"].(map[string]any)
	if !ok {
		return out
	}
	for key, target := range map[string]*AlarmAction{AlarmActionNotify: &out.Notify, AlarmActionWebhook: &out.Webhook} {
		if action, ok := actions[key].(map[string]any); ok {
			if enabled, ok := action["enabled"].(bool); ok {
				target.Enabled = enabled
			}
		}
	}
	if t, ok := actions[AlarmActionTicket].(map[string]any); ok {
		ta := &AlarmTicketAction{}
		ta.Enabled, _ = t["enabled"].(bool)
		ta.CloseOnResolve, _ = t["close_on_resolve"].(bool)
		if v, ok := t["priority"].(string); ok && slices.Contains(TicketPriorities, v) {
			ta.Priority = v
		}
		ta.Category, _ = t["category"].(string)
		if v, ok := t["reporter_id"].(string); ok && api.IsUUID(v) {
			ta.ReporterID = v
		}
		out.Ticket = ta
	}
	return out
}
