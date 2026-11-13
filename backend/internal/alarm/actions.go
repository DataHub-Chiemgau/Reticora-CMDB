package alarm

import (
	"encoding/json"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// Actions are the follow-up actions of an alarm (MON-04, WP-988): those of
// its alert rule, the defaults for the alarms of the other sources (check,
// collector, …). notify.enabled false notifies nobody (the subscription
// of WP-987 yields no audience), webhook.enabled false reaches no webhook
// subscription; the event stays in the outbox and the stream for the
// audit and the later consumers (workflow, correlation). The type is the
// payload frame events.AlarmActions: package monitoring (the rule API)
// cannot import this package (alarm imports monitoring for the rule
// sink), so type, defaults and checks live with the payload.
type Actions = events.AlarmActions

// DetailsActions is the key of the actions in the details of an alarm.
const DetailsActions = "actions"

// DefaultActions are notify and webhook on.
func DefaultActions() Actions { return events.DefaultAlarmActions() }

// ParseActions reads the actions object of a rule (events.ParseAlarmActions):
// absent, null and {} are the defaults; the reserved keys ticket (WP-1040)
// and workflow (WP-1110) and every unknown key are 422 with the field.
func ParseActions(raw json.RawMessage) (Actions, error) { return events.ParseAlarmActions(raw) }

// actionsOf reads the actions from the details of an alarm: they come back
// from the database as decoded JSON, from the memory store as Actions;
// without them the defaults.
func actionsOf(details map[string]any) Actions {
	switch v := details[DetailsActions].(type) {
	case Actions:
		return v
	case *Actions:
		if v != nil {
			return *v
		}
	case map[string]any:
		return events.AlarmActionsOf(map[string]any{DetailsActions: v})
	}
	return DefaultActions()
}

// withActions is details with the actions under DetailsActions, a copy so
// the caller's map stays as it is; nil actions are the defaults.
func withActions(details map[string]any, actions *Actions) map[string]any {
	out := make(map[string]any, len(details)+1)
	for k, v := range details {
		out[k] = v
	}
	a := DefaultActions()
	if actions != nil {
		a = *actions
	}
	out[DetailsActions] = a
	return out
}
