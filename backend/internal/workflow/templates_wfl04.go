package workflow

import "fmt"

// The templates of the use cases of WFL-04 (WP-1108, WP-1109). They belong
// to the template catalog of WP-1106 (templates.go); until that catalog is
// merged they stand here as Go data of the same shape: key, catalog
// version, name and description in de-DE and en-US, trigger, conditions
// and actions. A definition is made of one with Definition (inactive, like
// the instantiation of WP-1106).

// UseCaseTemplate is one template of a WFL-04 use case.
type UseCaseTemplate struct {
	Key            string            `json:"key"`
	CatalogVersion int               `json:"catalog_version"`
	Name           map[string]string `json:"name"`
	Description    map[string]string `json:"description"`
	Trigger        JSONMap           `json:"trigger"`
	Conditions     []JSONMap         `json:"conditions"`
	Actions        []JSONMap         `json:"actions"`
}

// Definition is a new inactive definition of the template named name.
func (t *UseCaseTemplate) Definition(orgID, name string) *Definition {
	meta := map[string]any{"template_key": t.Key, "template_version": t.CatalogVersion}
	trigger := JSONMap{}
	for k, v := range t.Trigger {
		trigger[k] = v
	}
	trigger["meta"] = meta
	conditions := t.Conditions
	if conditions == nil {
		conditions = []JSONMap{}
	}
	return &Definition{OrganizationID: orgID, Name: name, Description: t.Description["en-US"], Trigger: trigger,
		Conditions: conditions, Actions: t.Actions, Active: false}
}

// orderApprovalThreshold is the total cost above which an order needs two
// approvals (AK4 of WP-1108).
const orderApprovalThreshold = 1000

// UseCaseTemplates are the templates of WFL-04.
var UseCaseTemplates = []UseCaseTemplate{
	{
		// Order approval (WP-1108 AK4): above 1000 two approvals, otherwise
		// one, never by the initiator; then the decision on the order and
		// the notification of the requester.
		Key: "order_approval", CatalogVersion: 1,
		Name: map[string]string{"de-DE": "Bestellgenehmigung", "en-US": "Order approval"},
		Description: map[string]string{
			"de-DE": "Eine eingereichte Bestellung wird genehmigt: über 1000 von zwei Genehmigern, sonst von einem; danach wird der Anforderer benachrichtigt.",
			"en-US": "A submitted order is approved: above 1000 by two approvers, otherwise by one; then the requester is notified.",
		},
		Trigger: JSONMap{"type": TriggerOrderSubmitted},
		Actions: []JSONMap{
			{"type": approvalActionType, "message": "Bestellung genehmigen (über 1000)", "quorum": float64(2), "exclude_initiator": true,
				"when": map[string]any{"op": "gt", "field": "total_cost", "value": orderApprovalThreshold}},
			{"type": approvalActionType, "message": "Bestellung genehmigen", "quorum": float64(1), "exclude_initiator": true,
				"when": map[string]any{"op": "lte", "field": "total_cost", "value": orderApprovalThreshold}},
			{"type": orderDecisionActionType},
			// The requester learns the decision either way (AK4): the text
			// follows the status order_decision set (order_status).
			{"type": "send_notification", "recipients": map[string]any{"users": []any{"{{requested_by}}"}},
				"when":    map[string]any{"op": "eq", "field": "order_status", "value": "approved"},
				"subject": map[string]any{"de": "Bestellung {{order_number}} genehmigt", "en": "Order {{order_number}} approved"},
				"body":    map[string]any{"de": "Ihre Bestellung {{title}} wurde genehmigt.", "en": "Your order {{title}} was approved."}},
			{"type": "send_notification", "recipients": map[string]any{"users": []any{"{{requested_by}}"}},
				"when":    map[string]any{"op": "eq", "field": "order_status", "value": "rejected"},
				"subject": map[string]any{"de": "Bestellung {{order_number}} abgelehnt", "en": "Order {{order_number}} rejected"},
				"body":    map[string]any{"de": "Ihre Bestellung {{title}} wurde abgelehnt.", "en": "Your order {{title}} was rejected."}},
		},
	},
	// Maintenance notification (WP-1109 AK4).
	MaintenanceNotifyTemplate(MaintenanceAnnounceHours, MaintenanceRemindHours),
}

// Defaults of the template maintenance_notify (WP-1109 AK4): the
// announcement 72 hours, the reminder 2 hours before the start.
const (
	MaintenanceAnnounceHours = 72
	MaintenanceRemindHours   = 2
)

// maintenanceText is a text of the template in German and English.
func maintenanceText(de, en string) map[string]any { return map[string]any{"de": de, "en": en} }

// MaintenanceNotifyTemplate is the template maintenance_notify with the
// hours of the announcement and of the reminder before starts_at (AK4):
// on maintenance.scheduled the affected client contacts get the
// announcement, the reminder and, after ends_at, the completion; a window
// moved or cancelled meanwhile ends the run without sending
// (maintenance_affected).
func MaintenanceNotifyTemplate(announceHours, remindHours int) UseCaseTemplate {
	recipients := map[string]any{"maintenance_affected": map[string]any{"window_id": "window.id", "include_heuristic": false}}
	wait := func(at string) JSONMap { return JSONMap{"type": waitUntilActionType, "at": at} }
	send := func(subject, body map[string]any) JSONMap {
		return JSONMap{"type": "send_notification", "recipients": recipients, "subject": subject, "body": body}
	}
	return UseCaseTemplate{
		Key: "maintenance_notify", CatalogVersion: 1,
		Name: map[string]string{"de-DE": "Wartungsbenachrichtigung", "en-US": "Maintenance notification"},
		Description: map[string]string{
			"de-DE": "Die betroffenen Kunden eines Wartungsfensters erhalten Ankündigung, Erinnerung und Abschlussmeldung.",
			"en-US": "The affected customers of a maintenance window get the announcement, a reminder and the completion.",
		},
		Trigger: JSONMap{"type": TriggerEvent, "event": "maintenance.scheduled"},
		Actions: []JSONMap{
			wait(fmt.Sprintf("window.starts_at - %dh", announceHours)),
			send(maintenanceText("Geplante Wartung: {{window.title}}", "Scheduled maintenance: {{window.title}}"),
				maintenanceText("Wartung von {{window.starts_at}} bis {{window.ends_at}}.", "Maintenance from {{window.starts_at}} to {{window.ends_at}}.")),
			wait(fmt.Sprintf("window.starts_at - %dh", remindHours)),
			send(maintenanceText("Erinnerung: Wartung {{window.title}}", "Reminder: maintenance {{window.title}}"),
				maintenanceText("Die Wartung beginnt um {{window.starts_at}}.", "The maintenance starts at {{window.starts_at}}.")),
			wait("window.ends_at"),
			send(maintenanceText("Wartung abgeschlossen: {{window.title}}", "Maintenance completed: {{window.title}}"),
				maintenanceText("Die Wartung bis {{window.ends_at}} ist abgeschlossen.", "The maintenance until {{window.ends_at}} is completed.")),
		},
	}
}

// UseCaseTemplateByKey returns the template of key; nil when unknown.
func UseCaseTemplateByKey(key string) *UseCaseTemplate {
	for i := range UseCaseTemplates {
		if UseCaseTemplates[i].Key == key {
			return &UseCaseTemplates[i]
		}
	}
	return nil
}
