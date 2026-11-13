package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
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
	// CriticalIf makes a single event critical by its data (an alarm of
	// severity critical, WP-987); nil: Critical alone decides.
	CriticalIf func(data map[string]any) bool
	// Personal notifications carry a credential of their recipients (the
	// invitation link): they reach the recipients only, no webhook channel.
	Personal bool
	// Audience names the recipients of an event; nil: the event notifies
	// nobody.
	Audience func(data map[string]any) *Audience
}

func str(data map[string]any, key string) string {
	v, _ := data[key].(string)
	return v
}

// CategoryAlarm is the category of the alarm and incident notifications
// (NTF-05 [P3], MON-04, ALR-02, WP-987).
const CategoryAlarm = "alarm"

// severityCritical makes an alarm or incident of severity critical a
// critical notification: it ignores quiet hours and muted categories.
func severityCritical(d map[string]any) bool { return str(d, "severity") == "critical" }

// alarmAudience is the audience of an alarm or incident (WP-987): the
// contacts of its CI in the roles operations and escalation, and every user
// with monitoring:read whose scope covers its client and site. A suppressed
// alarm (symptom, maintenance, flapping) has none: it is not delivered.
func alarmAudience(ciKey string) func(d map[string]any) *Audience {
	return func(d map[string]any) *Audience {
		if str(d, "suppressed_reason") != "" {
			return nil
		}
		a := &Audience{Permission: identity.PermMonitoringRead, AllWithPermission: true,
			ClientID: str(d, "client_id"), SiteID: str(d, "site_id")}
		if ci := str(d, ciKey); ci != "" {
			a.CIID, a.ContactRoles = ci, []string{"operations", "escalation"}
		}
		return a
	}
}

// escalationAudience is the audience of alarm.severity_changed: only an
// escalation to critical is delivered (critical, past quiet hours), so an
// alarm that opened as warning during quiet hours still reaches the
// on-call staff once it becomes critical (AK1 of WP-987); lower
// escalations wait for the next opening or resolving.
func escalationAudience(d map[string]any) *Audience {
	if !severityCritical(d) {
		return nil
	}
	return alarmAudience("ci_id")(d)
}

// Subscriptions are the phase-1 notification events of NTF-05.
var Subscriptions = map[string]Subscription{
	// Alarms and incidents (NTF-05 [P3], MON-04, ALR-02, E-161, WP-987):
	// opening and resolving; critical by severity.
	events.AlarmOpened: {Category: CategoryAlarm, CriticalIf: severityCritical, Audience: alarmAudience("ci_id")},
	events.AlarmResolved: {Category: CategoryAlarm, CriticalIf: severityCritical,
		Audience: alarmAudience("ci_id")},
	events.AlarmSeverityChanged: {Category: CategoryAlarm, CriticalIf: severityCritical,
		Audience: escalationAudience},
	events.IncidentOpened: {Category: CategoryAlarm, CriticalIf: severityCritical,
		Audience: alarmAudience("root_ci_id")},
	events.IncidentResolved: {Category: CategoryAlarm, CriticalIf: severityCritical,
		Audience: alarmAudience("root_ci_id")},
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
	events.MonitoringSuspensionChanged: {
		// Probes of a lost monitoring feature paused or resumed (PKG-09,
		// WP-942) go to the org_admins like entitlement.warning.
		Category: "entitlement", Critical: true,
		Audience: func(map[string]any) *Audience {
			return &Audience{Permission: identity.PermUserManage, AllWithPermission: true}
		},
	},
	events.EntitlementChanged: {
		// A change of plan, add-on or quota (PKG-02, WP-882) goes to the
		// org_admins who may read the entitlements.
		Category: "entitlement",
		Audience: func(map[string]any) *Audience {
			return &Audience{Permission: identity.PermEntitlementRead, Roles: []string{"org_admin"}}
		},
	},
	events.InvitationCreated: {
		// The link holds the acceptance token: whoever reads it can accept
		// the invitation (F-041), so it goes to the invitee's address only.
		// Where the acceptance creates a verified IdP account (IdP admin
		// client), the event carries no link: the issue or renewal already
		// sent the mail straight over the central SMTP server
		// (InvitationMailer), and no channel of the organization, not even
		// its own SMTP server, gets a copy.
		Category: "invitation", Critical: true, Personal: true,
		Audience: func(d map[string]any) *Audience {
			if str(d, "link") == "" {
				return nil
			}
			return &Audience{Direct: []Recipient{{Email: str(d, "email")}}}
		},
	},
	events.ExportCompleted: {
		Category: "export",
		Audience: func(d map[string]any) *Audience {
			return &Audience{DirectUserIDs: []string{str(d, "initiated_by")}}
		},
	},
	// The full export of the organization (TLC-05, WP-1151) reaches the
	// user who started it, personally: no webhook channel learns of the
	// unsealed export.
	events.OrgExportCompleted: {
		Category: "export", Personal: true,
		Audience: func(d map[string]any) *Audience {
			if by := str(d, "initiated_by"); by != "" {
				return &Audience{DirectUserIDs: []string{by}}
			}
			return nil
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
	events.TicketSLABreached: {
		// The escalation of a missed due time (the sla.check run, TKT-01,
		// WP-769) to the assignee and the lead of the ticket's team; they
		// must read tickets in the ticket's client and site. Not critical:
		// quiet hours, muted categories and the digest apply.
		Category: "ticket",
		Audience: func(d map[string]any) *Audience {
			var users []string
			for _, key := range []string{"assignee_id", "team_lead_id"} {
				if id := str(d, key); id != "" && !slices.Contains(users, id) {
					users = append(users, id)
				}
			}
			return &Audience{Permission: identity.PermTicketRead, UserIDs: users,
				ClientID: str(d, "client_id"), SiteID: str(d, "site_id")}
		},
	},
	events.ContractExpiring: {
		// A reached stage before the due date of a contract (the
		// expiry.check run, NTF-05 [P2], MGT-12, WP-270): the org_admins
		// who read contracts in the contract's client, organization-wide
		// for a contract without client. A contract has no owner (E-119).
		// Not critical: quiet hours, muted categories and the digest apply.
		Category: "contract",
		Audience: func(d map[string]any) *Audience {
			return &Audience{Permission: identity.PermContractRead, Roles: []string{"org_admin"}, ClientID: str(d, "client_id")}
		},
	},
	events.AssetWarrantyExpiring: {
		// A reached stage before the warranty end of an asset (WP-270):
		// its owning user and team and the org_admins, each with asset:read
		// in the asset's client and site, and the owner contacts of its
		// primary CI.
		Category: "warranty",
		Audience: func(d map[string]any) *Audience {
			a := &Audience{Permission: identity.PermAssetRead, Roles: []string{"org_admin"},
				ClientID: str(d, "client_id"), SiteID: str(d, "site_id")}
			if id := str(d, "owner_user_id"); id != "" {
				a.UserIDs = []string{id}
			}
			if id := str(d, "owner_team_id"); id != "" {
				a.TeamIDs = []string{id}
			}
			if id := str(d, "primary_ci_id"); id != "" {
				a.CIID, a.ContactRoles = id, []string{"owner"}
			}
			return a
		},
	},
	events.StockBelowMinimum: {
		// The total stock of a consumable fell below its minimum level
		// (STK-01, NTF-05 [P2], WP-285, WP-845): every active user with
		// stock:write in the article's client, organization-wide grants
		// only for an article without client. A consumable has no site (its
		// bins have one each): a grant for single sites does not cover it.
		// Not critical: quiet hours, muted categories and the digest apply.
		Category: "stock",
		Audience: func(d map[string]any) *Audience {
			return &Audience{Permission: identity.PermStockWrite, AllWithPermission: true, ClientID: str(d, "client_id")}
		},
	},
	events.MaintenanceScheduled: {
		// A maintenance window announced to one affected client (MGT-04,
		// NTF-05 [P3], WP-1034): the client's contacts in the roles of the
		// announcement (contact_roles; empty: every contact of the client)
		// and the users with maintenance:read in the client. Never another
		// client's contacts, never the organization-wide ones. Not
		// critical: quiet hours and muted categories apply.
		Category: "maintenance",
		Audience: func(d map[string]any) *Audience {
			client := str(d, "client_id")
			if client == "" {
				return nil
			}
			var roles []string
			if list, ok := d["contact_roles"].([]any); ok {
				for _, r := range list {
					if role, ok := r.(string); ok && role != "" {
						roles = append(roles, role)
					}
				}
			}
			return &Audience{Permission: identity.PermMaintenanceRead, AllWithPermission: true, ClientID: client,
				ClientContacts: &ClientContacts{ContactRoles: roles}}
		},
	},
	events.DocumentScanned: {
		// A finding quarantines the version (SEC-09, E-42, WP-734): its
		// uploader and the org_admins who read the document are told. A
		// clean or failed scan notifies nobody; the sweep retries a failed
		// one.
		Category: "security", Critical: true,
		Audience: func(d map[string]any) *Audience {
			if str(d, "result") != "infected" {
				return &Audience{}
			}
			var uploader []string
			if by := str(d, "uploaded_by"); by != "" {
				uploader = []string{by}
			}
			return &Audience{DirectUserIDs: uploader, Roles: []string{"org_admin"}, Permission: identity.PermDocumentRead,
				ClientID: str(d, "client_id"), SiteID: str(d, "site_id")}
		},
	},
}

// EventConsumer notifies of the subscribed events of the outbox
// (events.Consumer). An event notifies once: the event id is the dedup
// key, so a repeated delivery of the event queues nothing.
type EventConsumer struct {
	resolver Resolver
	notifier *Notifier
	// linkBase is the origin (scheme and host) of the links of the alarm
	// texts; empty: the texts carry no link.
	linkBase string
}

// CINamer names the CI of an event for the texts (ci_name); the PGResolver
// is one.
type CINamer interface {
	CIName(ctx context.Context, orgID, ciID string) (string, error)
}

// WithLinkBase sets the origin of the links in the alarm and incident texts
// from a URL of the frontend (config.InvitationAcceptURL): its scheme and
// host. A URL without both leaves the texts without link.
func (c *EventConsumer) WithLinkBase(frontendURL string) *EventConsumer {
	c.linkBase = LinkOrigin(frontendURL)
	return c
}

// LinkOrigin is scheme://host of a URL; empty for a URL without both.
func LinkOrigin(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// alarmLinks are the paths of the links of the alarm and incident texts.
var alarmLinks = map[string]string{
	events.AlarmOpened: "/alarms?id=", events.AlarmResolved: "/alarms?id=", events.AlarmSeverityChanged: "/alarms?id=",
	events.IncidentOpened: "/alarms?incident_id=", events.IncidentResolved: "/alarms?incident_id=",
}

// enrich adds what the texts of an alarm or incident name beyond the event
// data: the name of the CI and the link.
func (c *EventConsumer) enrich(ctx context.Context, env *events.Envelope, data map[string]any) error {
	path, ok := alarmLinks[env.Type]
	if !ok {
		return nil
	}
	if c.linkBase != "" && str(data, "id") != "" {
		data["link"] = c.linkBase + path + url.QueryEscape(str(data, "id"))
	}
	ci := str(data, "ci_id")
	if ci == "" {
		ci = str(data, "root_ci_id")
	}
	if namer, ok := c.resolver.(CINamer); ok && ci != "" {
		name, err := namer.CIName(ctx, env.OrgID, ci)
		if err != nil {
			return err
		}
		data["ci_name"] = name
	}
	return nil
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
	audience := sub.Audience(data)
	if audience == nil {
		return nil
	}
	if err := c.enrich(ctx, env, data); err != nil {
		return err
	}
	recipients, err := c.resolver.Resolve(ctx, env.OrgID, audience)
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
	critical := sub.Critical || (sub.CriticalIf != nil && sub.CriticalIf(data))
	_, err = c.notifier.Notify(ctx, env.OrgID, &Request{Category: sub.Category, Critical: critical, Personal: sub.Personal,
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

// CategoryWorkflow is the category of the notifications of the workflow
// action send_notification (NTF-05 [P3], WP-1095).
const CategoryWorkflow = "workflow"

// directCategories are the categories of notifications that no event
// subscription sends but an application path directly (Notifier.Notify):
// users mute and bundle them like the others. They are never critical by
// themselves; a single request may be (Request.Critical).
var directCategories = []string{CategoryWorkflow}

// Categories are the notification categories of Subscriptions and the
// direct ones (directCategories), sorted.
func Categories() []string {
	out := slices.Clone(directCategories)
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
