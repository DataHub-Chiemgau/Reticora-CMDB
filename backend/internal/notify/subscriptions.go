package notify

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// The notification events of phase 1 (NTF-05): which domain event reaches
// whom with which text. A subscription turns the event of the outbox into
// a notification request; the notifier delivers it over the channels of
// the organization.

// Subscription maps one event type to a notification.
type Subscription struct {
	Category string
	// Critical notifications ignore quiet hours and muted categories.
	Critical bool
	Audience func(data map[string]any) *Audience
	// Render returns subject and body (German, then English).
	Render func(data map[string]any) (subject, body string)
}

func str(data map[string]any, key string) string {
	v, _ := data[key].(string)
	return v
}

func text(de, en string) string { return de + "\n\n" + en }

// Subscriptions are the phase-1 notification events of NTF-05.
var Subscriptions = map[string]Subscription{
	events.ReviewCreated: {
		Category: "review",
		Audience: func(d map[string]any) *Audience {
			return &Audience{Permission: identity.PermReviewResolve, AllWithPermission: true,
				ClientID: str(d, "client_id"), SiteID: str(d, "site_id")}
		},
		Render: func(d map[string]any) (string, string) {
			return "Neues Review-Item: " + str(d, "kind"),
				text("Ein neues Review-Item ("+str(d, "kind")+") wartet auf eine Entscheidung.",
					"A new review item ("+str(d, "kind")+") awaits a decision.")
		},
	},
	events.CollectorOffline: {
		Category: "collector", Critical: true,
		Audience: func(d map[string]any) *Audience {
			return &Audience{Permission: identity.PermDiscoveryManage, AllWithPermission: true,
				ClientID: str(d, "client_id"), SiteID: str(d, "site_id")}
		},
		Render: func(d map[string]any) (string, string) {
			return "Collector offline: " + str(d, "name"),
				text("Der Collector "+str(d, "name")+" meldet sich seit "+str(d, "last_seen_at")+" nicht mehr.",
					"Collector "+str(d, "name")+" has not reported since "+str(d, "last_seen_at")+".")
		},
	},
	events.EntitlementWarning: {
		Category: "entitlement",
		Audience: func(map[string]any) *Audience {
			return &Audience{Permission: identity.PermUserManage, AllWithPermission: true}
		},
		Render: func(d map[string]any) (string, string) {
			return "Lizenz-Warnung: " + str(d, "feature"),
				text("Die Lizenzgrenze für "+str(d, "feature")+" ist fast erreicht: "+fmt.Sprint(d["used"])+" von "+fmt.Sprint(d["limit"])+".",
					"The licence limit of "+str(d, "feature")+" is almost reached: "+fmt.Sprint(d["used"])+" of "+fmt.Sprint(d["limit"])+".")
		},
	},
	events.EntitlementExpired: {
		Category: "entitlement", Critical: true,
		Audience: func(map[string]any) *Audience {
			return &Audience{Permission: identity.PermUserManage, AllWithPermission: true}
		},
		Render: func(d map[string]any) (string, string) {
			return "Lizenz abgelaufen: " + str(d, "feature"),
				text("Die Lizenz für "+str(d, "feature")+" ist am "+str(d, "expired_at")+" abgelaufen.",
					"The licence of "+str(d, "feature")+" expired on "+str(d, "expired_at")+".")
		},
	},
	events.InvitationCreated: {
		Category: "invitation", Critical: true,
		Audience: func(d map[string]any) *Audience {
			return &Audience{Direct: []Recipient{{Email: str(d, "email")}}}
		},
		Render: func(d map[string]any) (string, string) {
			return "Einladung zu Reticora",
				text("Sie wurden zu "+str(d, "organization_name")+" eingeladen. Die Einladung gilt bis "+str(d, "expires_at")+":\n"+str(d, "link"),
					"You have been invited to "+str(d, "organization_name")+". The invitation is valid until "+str(d, "expires_at")+":\n"+str(d, "link"))
		},
	},
	events.ExportCompleted: {
		Category: "export",
		Audience: func(d map[string]any) *Audience {
			return &Audience{DirectUserIDs: []string{str(d, "initiated_by")}}
		},
		Render: func(d map[string]any) (string, string) {
			return "Export fertig (" + str(d, "format") + ")",
				text("Ihr Export ("+str(d, "format")+", "+fmt.Sprint(d["row_count"])+" Zeilen) steht zum Download bereit.",
					"Your export ("+str(d, "format")+", "+fmt.Sprint(d["row_count"])+" rows) is ready for download.")
		},
	},
	events.JobFailed: {
		Category: "job",
		Audience: func(d map[string]any) *Audience {
			if by := str(d, "created_by"); by != "" {
				return &Audience{DirectUserIDs: []string{by}}
			}
			return &Audience{Permission: identity.PermUserManage, AllWithPermission: true}
		},
		Render: func(d map[string]any) (string, string) {
			return "Job fehlgeschlagen: " + str(d, "kind"),
				text("Der Job "+str(d, "kind")+" ist endgültig fehlgeschlagen: "+str(d, "error"),
					"Job "+str(d, "kind")+" failed for good: "+str(d, "error"))
		},
	},
	events.WebhookDead: {
		Category: "webhook",
		Audience: func(map[string]any) *Audience {
			return &Audience{Permission: identity.PermWebhookManage, AllWithPermission: true}
		},
		Render: func(d map[string]any) (string, string) {
			return "Webhook-Zustellung aufgegeben: " + str(d, "event"),
				text("Die Zustellung von "+str(d, "event")+" an das Abonnement "+str(d, "subscription_id")+" ist nach "+
					fmt.Sprint(d["attempts"])+" Versuchen in der Dead-Letter-Queue: "+str(d, "last_error"),
					"The delivery of "+str(d, "event")+" to subscription "+str(d, "subscription_id")+" went to the dead-letter queue after "+
						fmt.Sprint(d["attempts"])+" attempts: "+str(d, "last_error"))
		},
	},
	events.CertificateExpiring: {
		Category: "certificate", Critical: true,
		Audience: func(d map[string]any) *Audience {
			return &Audience{Permission: identity.PermCIRead, AllWithPermission: true, ClientID: str(d, "client_id"),
				SiteID: str(d, "site_id"), CIID: str(d, "ci_id"), ContactRoles: []string{"owner", "responsible"}}
		},
		Render: func(d map[string]any) (string, string) {
			return "Zertifikat läuft ab: " + str(d, "name"),
				text("Das Zertifikat "+str(d, "name")+" läuft am "+str(d, "expires_at")+" ab.",
					"Certificate "+str(d, "name")+" expires on "+str(d, "expires_at")+".")
		},
	},
}

// EventConsumer notifies of the subscribed events of the outbox
// (events.Consumer). An event notifies once: the event id is the dedup
// key, so a repeated delivery of the event queues nothing.
type EventConsumer struct {
	resolver Resolver
	notifier *Notifier
}

// NewEventConsumer creates the consumer.
func NewEventConsumer(resolver Resolver, notifier *Notifier) *EventConsumer {
	return &EventConsumer{resolver: resolver, notifier: notifier}
}

// Consume implements events.Consumer.
func (c *EventConsumer) Consume(ctx context.Context, env *events.Envelope) error {
	sub, ok := Subscriptions[env.Type]
	if !ok {
		return nil
	}
	data := map[string]any{}
	if len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, &data); err != nil {
			return fmt.Errorf("decode %s: %w", env.Type, err)
		}
	}
	recipients, err := c.resolver.Resolve(ctx, env.OrgID, sub.Audience(data))
	if err != nil {
		return err
	}
	if len(recipients) == 0 {
		return nil
	}
	subject, body := sub.Render(data)
	data["event_id"], data["event_type"] = env.ID, env.Type
	_, err = c.notifier.Notify(ctx, env.OrgID, &Request{Category: sub.Category, Critical: sub.Critical, Subject: subject,
		Body: body, Data: data, DedupKey: env.Type + ":" + env.ID, Recipients: recipients})
	return err
}
