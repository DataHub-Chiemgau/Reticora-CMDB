package alarm_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// The follow-up actions of an alert rule (MON-04, WP-988): notify and
// webhook, each on by default; ticket (WP-1040) and workflow (WP-1110) are
// reserved, every other key is refused. The alarm carries them in
// details.actions and its events in the data.

func TestActionsDefaults(t *testing.T) {
	for _, raw := range []string{"", "null", "{}", `{"notify":{}}`, `{"webhook":null}`} {
		got, err := alarm.ParseActions(json.RawMessage(raw))
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if !got.Notify.Enabled || !got.Webhook.Enabled {
			t.Errorf("%q = %+v, want notify and webhook on", raw, got)
		}
	}
	if d := alarm.DefaultActions(); !d.Notify.Enabled || !d.Webhook.Enabled {
		t.Errorf("DefaultActions = %+v", d)
	}
	got, err := alarm.ParseActions(json.RawMessage(`{"notify":{"enabled":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Notify.Enabled || !got.Webhook.Enabled {
		t.Errorf("notify off = %+v, want notify off and webhook on (the missing key keeps its default)", got)
	}
}

func wantField(t *testing.T, raw, field string) {
	t.Helper()
	_, err := alarm.ParseActions(json.RawMessage(raw))
	var fe *api.FieldError
	if !errors.As(err, &fe) {
		t.Fatalf("%s: error %v, want a field error", raw, err)
	}
	if fe.Field != field || fe.Status != http.StatusUnprocessableEntity {
		t.Errorf("%s: field %q status %d, want %q 422", raw, fe.Field, fe.Status, field)
	}
}

func TestActionsRejectReservedKeys(t *testing.T) {
	wantField(t, `{"workflow":{"workflow_id":"x"}}`, "actions.workflow")
	wantField(t, `{"notify":{"enabled":true},"workflow":{}}`, "actions.workflow")
	_, err := alarm.ParseActions(json.RawMessage(`{"workflow":{}}`))
	var fe *api.FieldError
	if errors.As(err, &fe) && fe.Message == "" {
		t.Error("reserved key without hint")
	}
}

// TestActionsTicket covers WP-1040 AK1: the ticket action is off by
// default, takes priority, category, reporter_id and close_on_resolve and
// refuses unknown keys and invalid values with the field.
func TestActionsTicket(t *testing.T) {
	a, err := alarm.ParseActions(json.RawMessage(`{"ticket":{"enabled":true,"priority":"high","category":"network",
		"reporter_id":"11111111-1111-4111-8111-111111111111","close_on_resolve":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if a.Ticket == nil || !a.Ticket.Enabled || a.Ticket.Priority != "high" || a.Ticket.Category != "network" ||
		a.Ticket.ReporterID != "11111111-1111-4111-8111-111111111111" || !a.Ticket.CloseOnResolve || !a.Notify.Enabled {
		t.Errorf("ticket action %+v", a.Ticket)
	}
	if d := alarm.DefaultActions(); d.Ticket != nil {
		t.Error("the ticket action is on by default")
	}
	wantField(t, `{"ticket":{"enabled":"yes"}}`, "actions.ticket.enabled")
	wantField(t, `{"ticket":{"priority":"urgent"}}`, "actions.ticket.priority")
	wantField(t, `{"ticket":{"reporter_id":"me"}}`, "actions.ticket.reporter_id")
	wantField(t, `{"ticket":{"team":"x"}}`, "actions.ticket.team")
	wantField(t, `{"ticket":true}`, "actions.ticket")
	stored := events.ParseStoredAlarmActions(json.RawMessage(`{"ticket":{"enabled":true,"priority":"low","close_on_resolve":true}}`))
	if stored.Ticket == nil || !stored.Ticket.Enabled || stored.Ticket.Priority != "low" || !stored.Ticket.CloseOnResolve {
		t.Errorf("stored ticket action %+v", stored.Ticket)
	}
}

func TestActionsRejectUnknownKey(t *testing.T) {
	wantField(t, `{"sms":{"enabled":true}}`, "actions.sms")
	wantField(t, `{"notify":{"enabled":true,"channel":"mail"}}`, "actions.notify.channel")
	wantField(t, `{"notify":{"enabled":"yes"}}`, "actions.notify.enabled")
	// null is no boolean: json.Unmarshal would leave false and switch the
	// action off silently.
	wantField(t, `{"notify":{"enabled":null}}`, "actions.notify.enabled")
	wantField(t, `{"webhook":{"enabled":null}}`, "actions.webhook.enabled")
	wantField(t, `{"webhook":true}`, "actions.webhook")
	wantField(t, `[]`, "actions")
}

// TestStoredActionsAreLenient: the stored object of a rule is read
// leniently, so a reserved or unknown key (a newer release, a manual fix)
// keeps the defaults instead of failing the scan of every rule.
func TestStoredActionsAreLenient(t *testing.T) {
	got := events.ParseStoredAlarmActions(json.RawMessage(`{"workflow":{"enabled":true},"notify":{"enabled":false},"x":1}`))
	if got.Notify.Enabled || !got.Webhook.Enabled {
		t.Errorf("stored = %+v, want notify off and webhook on", got)
	}
	for _, raw := range []string{"", "{}", "[]", "garbage"} {
		if got := events.ParseStoredAlarmActions(json.RawMessage(raw)); got != events.DefaultAlarmActions() {
			t.Errorf("%q = %+v, want the defaults", raw, got)
		}
	}
}

// TestActionsInDetailsAndEvents: a raise carries the actions of the rule
// into details.actions and the data of alarm.opened and alarm.resolved;
// without actions (a check alarm) the defaults.
func TestActionsInDetailsAndEvents(t *testing.T) {
	svc, sink := memService()
	ctx := context.Background()
	off := alarm.Actions{Notify: events.AlarmAction{Enabled: false}, Webhook: events.AlarmAction{Enabled: true}}
	in := raiseIn("s1", alarm.SeverityCritical)
	in.Actions = &off
	in.Details = map[string]any{"value": 1.0}
	a, err := svc.Raise(ctx, memOrg, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := in.Details[alarm.DetailsActions]; ok {
		t.Error("Raise changed the details of the caller")
	}
	if got, ok := a.Details[alarm.DetailsActions].(alarm.Actions); !ok || got != off {
		t.Errorf("details.actions = %#v, want %+v", a.Details[alarm.DetailsActions], off)
	}
	if _, err := svc.Clear(ctx, memOrg, a.DedupKey, a.FirstSeenAt.Add(1), ""); err != nil {
		t.Fatal(err)
	}
	check := &alarm.RaiseInput{SourceKind: alarm.SourceCheck, SourceID: ruleID, SeriesKey: "c", Severity: alarm.SeverityWarning,
		Summary: "down"}
	if _, err := svc.Raise(ctx, memOrg, check); err != nil {
		t.Fatal(err)
	}
	evs := sink.Events()
	if len(evs) != 3 {
		t.Fatalf("events %v", eventTypes(sink))
	}
	for i, want := range []alarm.Actions{off, off, alarm.DefaultActions()} {
		d, _ := evs[i].Data.(events.AlarmData)
		if d.Actions == nil || *d.Actions != want {
			t.Errorf("%s: actions %+v, want %+v", evs[i].Type, d.Actions, want)
		}
	}
}
