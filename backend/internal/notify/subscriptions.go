package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
)

// The notification events of phase 1 (NTF-05): which domain event reaches
// whom with which text. A subscription turns the event of the outbox into
// a notification request; the notifier delivers it over the channels of
// the organization.

// Subscription maps one event type to a notification. Its texts are the
// Templates of the event type.
type Subscription struct {
	Category string
	// Critical notifications ignore quiet hours and muted categories.
	Critical bool
	// Personal notifications carry a credential of their recipients (the
	// invitation link): they reach the recipients only, no webhook channel.
	Personal bool
	Audience func(data map[string]any) *Audience
}

func str(data map[string]any, key string) string {
	v, _ := data[key].(string)
	return v
}

// Subscriptions are the phase-1 notification events of NTF-05.
var Subscriptions = map[string]Subscription{
	events.ReviewCreated: {
		Category: "review",
		Audience: func(d map[string]any) *Audience {
			if str(d, "kind") == reviews.KindUnlicensedCI {
				// A device held because max_cis is reached is a licensing
				// matter: it goes to the org_admins only (ENT-03, CH21).
				return &Audience{Permission: identity.PermReviewResolve, Roles: []string{"org_admin"},
					ClientID: str(d, "client_id"), SiteID: str(d, "site_id")}
			}
			return &Audience{Permission: identity.PermReviewResolve, AllWithPermission: true,
				ClientID: str(d, "client_id"), SiteID: str(d, "site_id")}
		},
	},
	events.CollectorOffline: {
		Category: "collector", Critical: true,
		Audience: func(d map[string]any) *Audience {
			return &Audience{Permission: identity.PermDiscoveryManage, AllWithPermission: true,
				ClientID: str(d, "client_id"), SiteID: str(d, "site_id")}
		},
	},
	events.EntitlementWarning: {
		// The expiry warning of the entitlement.notices run, 14, 7 and 1
		// day before valid_until (ENT-07, WP-425).
		Category: "entitlement",
		Audience: func(map[string]any) *Audience {
			return &Audience{Permission: identity.PermUserManage, AllWithPermission: true}
		},
	},
	events.EntitlementExpired: {
		Category: "entitlement", Critical: true,
		Audience: func(map[string]any) *Audience {
			return &Audience{Permission: identity.PermUserManage, AllWithPermission: true}
		},
	},
	events.InvitationCreated: {
		// The link holds the acceptance token: whoever reads it can accept
		// the invitation (F-041), so it goes to the invitee's address only.
		Category: "invitation", Critical: true, Personal: true,
		Audience: func(d map[string]any) *Audience {
			return &Audience{Direct: []Recipient{{Email: str(d, "email")}}}
		},
	},
	events.ExportCompleted: {
		Category: "export",
		Audience: func(d map[string]any) *Audience {
			return &Audience{DirectUserIDs: []string{str(d, "initiated_by")}}
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
	},
	events.WebhookDead: {
		Category: "webhook",
		Audience: func(map[string]any) *Audience {
			return &Audience{Permission: identity.PermWebhookManage, AllWithPermission: true}
		},
	},
	events.CertificateExpiring: {
		Category: "certificate", Critical: true,
		Audience: func(d map[string]any) *Audience {
			// The certificate of a collector and the collector CA (the
			// certificate.notices run, COL-04, WP-425) concern whoever
			// manages discovery in the collector's client and site, like
			// collector.offline; the certificate of a CI its readers and
			// contacts.
			if str(d, "ci_id") == "" {
				return &Audience{Permission: identity.PermDiscoveryManage, AllWithPermission: true,
					ClientID: str(d, "client_id"), SiteID: str(d, "site_id")}
			}
			return &Audience{Permission: identity.PermCIRead, AllWithPermission: true, ClientID: str(d, "client_id"),
				SiteID: str(d, "site_id"), CIID: str(d, "ci_id"), ContactRoles: []string{"owner", "operations"}}
		},
	},
	events.APIKeyCreatorDeactivated: {
		// The service-account keys of a deactivated user stay valid (E-64,
		// option 2), but the user saw their secrets when creating them: the
		// org_admins who may manage API keys are asked to rotate them.
		Category: "apikey", Critical: true,
		Audience: func(map[string]any) *Audience {
			return &Audience{Permission: identity.PermAPIKeyManage, Roles: []string{"org_admin"}}
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
	var brand *Branding
	if source, ok := c.resolver.(BrandingSource); ok {
		if brand, err = source.Branding(ctx, env.OrgID); err != nil {
			return err
		}
	}
	// Each recipient reads the event in their language (NTF-03); webhook
	// channels and recipients without an account get the organization's.
	orgLocale := TemplateLocale("", brand.locale())
	texts := map[string]Text{orgLocale: Render(env.Type, orgLocale, data)}
	for i := range recipients {
		locale := TemplateLocale(recipients[i].Locale, brand.locale())
		if _, ok := texts[locale]; !ok {
			texts[locale] = Render(env.Type, locale, data)
		}
	}
	data["event_id"], data["event_type"] = env.ID, env.Type
	_, err = c.notifier.Notify(ctx, env.OrgID, &Request{Category: sub.Category, Critical: sub.Critical, Personal: sub.Personal,
		Subject: texts[orgLocale].Subject, Body: texts[orgLocale].Body, Texts: texts, Branding: brand, Data: data,
		DedupKey: env.Type + ":" + env.ID, Recipients: recipients})
	return err
}

// Category subscriptions (NTF-03, E-91 option 2): a user subscribes to the
// notification categories through the NTF-04 preference. Every category is
// subscribed unless the preference mutes it (Preference.MutedCategories),
// also without a stored preference, so the users of a role audience receive
// it by default; a muted category reaches the user only with a critical
// notification. Client contacts are addressed only by an audience that
// names them (Audience.ClientContacts), never by default.
const (
	SubscriptionSubscribed = "subscribed"
	SubscriptionMuted      = "muted"
)

// Categories are the notification categories of Subscriptions, sorted.
func Categories() []string {
	var out []string
	for _, sub := range Subscriptions {
		if !slices.Contains(out, sub.Category) {
			out = append(out, sub.Category)
		}
	}
	sort.Strings(out)
	return out
}

// applySubscriptions folds the named category subscriptions into the muted
// categories: muted adds the category, subscribed removes it.
func (p *Preference) applySubscriptions() error {
	categories := Categories()
	for _, category := range slices.Sorted(maps.Keys(p.Subscriptions)) {
		state := p.Subscriptions[category]
		if !slices.Contains(categories, category) {
			return fmt.Errorf("%w: subscriptions: unknown category %q (known: %s)", ErrValidation, category,
				strings.Join(categories, ", "))
		}
		switch state {
		case SubscriptionMuted:
			if !slices.Contains(p.MutedCategories, category) {
				p.MutedCategories = append(p.MutedCategories, category)
			}
		case SubscriptionSubscribed:
			p.MutedCategories = slices.DeleteFunc(p.MutedCategories, func(c string) bool { return c == category })
		default:
			return fmt.Errorf("%w: subscriptions: unknown state %q for %s (subscribed or muted)", ErrValidation, state, category)
		}
	}
	return nil
}

// FillSubscriptions sets the subscription of every category from the
// muted categories.
func (p *Preference) FillSubscriptions() {
	p.Subscriptions = make(map[string]string, len(Subscriptions))
	for _, category := range Categories() {
		p.Subscriptions[category] = SubscriptionSubscribed
		if slices.Contains(p.MutedCategories, category) {
			p.Subscriptions[category] = SubscriptionMuted
		}
	}
}
