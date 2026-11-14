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
	"time"

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

// The categories of the [P3] groups of NTF-05 (WP-1147): Alerts are alarm
// (alarm.*, incident.*, sla.violated) and config (ci.config_changed);
// maintenance windows are maintenance (maintenance.scheduled); workflow is
// workflow.run_failed and the action send_notification (WP-1095). The
// escalation steps of NTF-02 (WP-1145) are no subscribable category: they
// address the targets of the policy, never a muted or bundled preference.
const (
	// CategoryAlarm is the category of the alarm and incident
	// notifications (NTF-05 [P3], MON-04, ALR-02, WP-987) and of
	// sla.violated.
	CategoryAlarm = "alarm"
	// CategoryConfig is the category of ci.config_changed (ALR-07).
	CategoryConfig = "config"
	// CategoryMaintenance is the category of maintenance.scheduled
	// (MGT-04, WP-1034).
	CategoryMaintenance = "maintenance"
)

// renamedCategories maps an earlier name of a category to its current one
// (WP-1147 AK1): a stored preference that mutes or bundles the earlier
// name keeps muting or bundling the category when it is read.
var renamedCategories = map[string]string{"maintenance_window": CategoryMaintenance}

// renameCategories maps the earlier category names of a stored preference
// to the current ones, each category once.
func (p *Preference) renameCategories() {
	muted := make([]string, 0, len(p.MutedCategories))
	for _, c := range p.MutedCategories {
		if to, ok := renamedCategories[c]; ok {
			c = to
		}
		if !slices.Contains(muted, c) {
			muted = append(muted, c)
		}
	}
	p.MutedCategories = muted
	for from, to := range renamedCategories {
		if window, ok := p.Digest[from]; ok {
			delete(p.Digest, from)
			if _, kept := p.Digest[to]; !kept {
				p.Digest[to] = window
			}
		}
	}
}

// severityCritical makes an alarm or incident of severity critical a
// critical notification: it ignores quiet hours and muted categories.
func severityCritical(d map[string]any) bool { return str(d, "severity") == "critical" }

// alarmAudience is the audience of an alarm or incident (WP-987): the
// contacts of its CI in the roles operations and escalation, and every user
// with monitoring:read whose scope covers its client and site. A suppressed
// alarm (symptom, maintenance, flapping) has none: it is not delivered.
// Neither has an alarm whose rule switched the notification off
// (actions.notify.enabled false, WP-988); the event stays in the outbox
// and the stream for the other consumers.
func alarmAudience(ciKey string) func(d map[string]any) *Audience {
	return func(d map[string]any) *Audience {
		if str(d, "suppressed_reason") != "" || !events.AlarmActionsOf(d).Notify.Enabled {
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
	// An availability target missed (ALR-06, WP-1049) is an alert: the
	// contacts of its CI in the roles operations and escalation and the
	// users with monitoring:read in its client and site. Not critical.
	events.SLAViolated: {Category: CategoryAlarm, Audience: func(d map[string]any) *Audience {
		a := &Audience{Permission: identity.PermMonitoringRead, AllWithPermission: true,
			ClientID: str(d, "client_id"), SiteID: str(d, "site_id")}
		if ci := str(d, "ci_id"); ci != "" {
			a.CIID, a.ContactRoles = ci, []string{"operations", "escalation"}
		}
		return a
	}},
	// A new configuration version of a device (ALR-07, WP-1054): only the
	// operations contacts of the CI and the users with monitoring:read in
	// its client and site. The text names no configuration content.
	events.CIConfigChanged: {Category: CategoryConfig, Audience: func(d map[string]any) *Audience {
		ci := str(d, "ci_id")
		if ci == "" {
			return nil
		}
		return &Audience{Permission: identity.PermMonitoringRead, AllWithPermission: true,
			ClientID: str(d, "client_id"), SiteID: str(d, "site_id"), CIID: ci, ContactRoles: []string{"operations"}}
	}},
	// A failed workflow run (WFL-02, WP-1092): its initiator and the
	// org_admins with workflow:read in the client and site of the run.
	events.WorkflowRunFailed: {Category: CategoryWorkflow, Audience: func(d map[string]any) *Audience {
		a := &Audience{Permission: identity.PermWorkflowRead, Roles: []string{"org_admin"},
			ClientID: str(d, "client_id"), SiteID: str(d, "site_id")}
		if by := str(d, "initiated_by"); by != "" {
			a.DirectUserIDs = []string{by}
		}
		return a
	}},
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
	// The deletion request of the organization and its cancel (TLC-06,
	// WP-1152, WP-1154) reach every org_admin, personally and critical.
	events.OrgDeletionRequested: {
		Category: "org_deletion", Critical: true, Personal: true,
		Audience: func(map[string]any) *Audience {
			return &Audience{Permission: identity.PermLocationRead, Roles: []string{"org_admin"}}
		},
	},
	events.OrgDeletionCancelled: {
		Category: "org_deletion", Critical: true, Personal: true,
		Audience: func(map[string]any) *Audience {
			return &Audience{Permission: identity.PermLocationRead, Roles: []string{"org_admin"}}
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
		Category: CategoryMaintenance,
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

// alarmLinks are the paths of the links of the alarm and incident texts
// and of the other alerts (WP-1147): the target on the availability tab of
// the SLA page (WP-1051), the configuration diff of a CI (its id follows
// the path, WP-1058 AK6), the run view of a failed workflow (WP-1132).
var alarmLinks = map[string]string{
	events.AlarmOpened: "/alarms?id=", events.AlarmResolved: "/alarms?id=", events.AlarmSeverityChanged: "/alarms?id=",
	events.IncidentOpened: "/alarms?incident_id=", events.IncidentResolved: "/alarms?incident_id=",
	events.SLAViolated: "/slas?tab=availability&target_id=", events.CIConfigChanged: "/cmdb/",
	events.WorkflowRunFailed: "/workflows/runs/",
}

// SLATarget is what the text and the audience of sla.violated name beyond
// the event data (events.SLAViolatedData): the name of the availability
// target and its CI (empty for a target of a client).
type SLATarget struct {
	Name string
	CIID string
}

// ConfigChange is what the text of ci.config_changed names beyond the
// event data (events.CIConfigChangedData): when the version was collected
// and how many lines differ from its predecessor. Never the content.
type ConfigChange struct {
	CollectedAt  time.Time
	PreviousID   string
	ChangedLines int
	HasPrevious  bool
}

// AlertDescriber describes the objects of sla.violated and
// ci.config_changed; the PGResolver is one. found is false for an object
// that no longer exists.
type AlertDescriber interface {
	SLATarget(ctx context.Context, orgID, targetID string) (t SLATarget, found bool, err error)
	ConfigChange(ctx context.Context, orgID, snapshotID, previousID string) (c ConfigChange, found bool, err error)
}

// configContentKeys are the keys of ci.config_changed that identify the
// configuration itself (its hashes, WP-1054 AK3): they leave the consumer
// in no text, no in-app notification and no webhook payload.
var configContentKeys = []string{"hash", "sha256", "previous_sha256"}

// describeAlert adds what sla.violated and ci.config_changed name beyond
// their event contract: the target's name and CI (the audience needs the
// CI, so it runs before the audience is named), the time of the version
// and its number of changed lines. It drops the configuration hashes.
func (c *EventConsumer) describeAlert(ctx context.Context, env *events.Envelope, data map[string]any) error {
	switch env.Type {
	case events.SLAViolated:
		describer, ok := c.resolver.(AlertDescriber)
		if !ok || str(data, "id") == "" {
			return nil
		}
		t, found, err := describer.SLATarget(ctx, env.OrgID, str(data, "id"))
		if err != nil || !found {
			return err
		}
		data["name"] = t.Name
		if str(data, "ci_id") == "" && t.CIID != "" {
			data["ci_id"] = t.CIID
		}
	case events.CIConfigChanged:
		for _, k := range configContentKeys {
			delete(data, k)
		}
		describer, ok := c.resolver.(AlertDescriber)
		snapshot := firstOf(str(data, "snapshot_id"), str(data, "id"))
		if !ok || snapshot == "" {
			return nil
		}
		cc, found, err := describer.ConfigChange(ctx, env.OrgID, snapshot, str(data, "previous_snapshot_id"))
		if err != nil || !found {
			return err
		}
		if str(data, "collected_at") == "" {
			data["collected_at"] = cc.CollectedAt.UTC().Format(time.RFC3339)
		}
		if cc.HasPrevious {
			data["changed_lines"] = float64(cc.ChangedLines)
			if str(data, "previous_snapshot_id") == "" {
				data["previous_snapshot_id"] = cc.PreviousID
			}
		}
	}
	return nil
}

// RunFailure is what the text of workflow.run_failed names beyond the
// event data: the definition, the failed step and its error, and the
// initiator of the run (its recipient).
type RunFailure struct {
	WorkflowName string
	InitiatedBy  string
	StepIndex    int
	StepAction   string
	Error        string
}

// RunDescriber describes a failed workflow run (workflow.run_failed); the
// PGResolver is one. found is false for a run that no longer exists.
type RunDescriber interface {
	RunFailure(ctx context.Context, orgID, runID string) (f RunFailure, found bool, err error)
}

// maxRunErrorLen bounds the error text of a failed run in a notification.
const maxRunErrorLen = 500

// describeRun adds the definition name, the failed step, the masked error
// and the initiator of a failed run to its data; the audience needs the
// initiator, so it runs before the audience is named.
func (c *EventConsumer) describeRun(ctx context.Context, env *events.Envelope, data map[string]any) error {
	describer, ok := c.resolver.(RunDescriber)
	if !ok || env.Type != events.WorkflowRunFailed {
		return nil
	}
	run := str(data, "run_id")
	if run == "" {
		run = str(data, "id")
	}
	if run == "" {
		return nil
	}
	f, found, err := describer.RunFailure(ctx, env.OrgID, run)
	if err != nil || !found {
		return err
	}
	data["workflow_name"], data["initiated_by"] = f.WorkflowName, f.InitiatedBy
	if f.StepAction != "" {
		data["failed_step"] = fmt.Sprintf("%d (%s)", f.StepIndex+1, f.StepAction)
	}
	data["error"] = runErrorText(f.Error)
	return nil
}

// runErrorText is the error of a failed run as a notification names it:
// one line, every URL masked (a webhook URL carries its token), bounded.
func runErrorText(raw string) string {
	text := maskURLs(strings.Join(strings.Fields(raw), " "))
	if r := []rune(text); len(r) > maxRunErrorLen {
		text = string(r[:maxRunErrorLen]) + "…"
	}
	return text
}

// enrich adds what the texts of an alarm, an incident or another alert
// name beyond the event data: the name of the CI and the link.
func (c *EventConsumer) enrich(ctx context.Context, env *events.Envelope, data map[string]any) error {
	path, ok := alarmLinks[env.Type]
	if !ok {
		return nil
	}
	if c.linkBase != "" && str(data, "id") != "" {
		switch env.Type {
		case events.CIConfigChanged:
			// The diff of the version against its predecessor (WP-1058 AK6).
			if ci := str(data, "ci_id"); ci != "" {
				link := c.linkBase + path + url.PathEscape(ci) + "?config=" +
					url.QueryEscape(firstOf(str(data, "snapshot_id"), str(data, "id")))
				if prev := str(data, "previous_snapshot_id"); prev != "" {
					link += "&against=" + url.QueryEscape(prev)
				}
				data["link"] = link
			}
		case events.WorkflowRunFailed:
			data["link"] = c.linkBase + path + url.PathEscape(firstOf(str(data, "run_id"), str(data, "id")))
		default:
			data["link"] = c.linkBase + path + url.QueryEscape(str(data, "id"))
		}
	}
	if _, ok := data["occurred_at"]; !ok && env.Type == events.CIConfigChanged && !env.OccurredAt.IsZero() {
		data["occurred_at"] = env.OccurredAt.UTC().Format(time.RFC3339)
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

func firstOf(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
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
	if err := c.describeRun(ctx, env, data); err != nil {
		return err
	}
	if err := c.describeAlert(ctx, env, data); err != nil {
		return err
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

// P3Event is one event of the [P3] groups of NTF-05 (Alerts, maintenance
// windows, workflow) and how it is notified: by its subscription in
// Category, or directly by the application path of the work package Direct
// names (no subscription, no template of Templates).
type P3Event struct {
	Type     string
	Category string
	Direct   string
}

// NTF05P3Events are the [P3] events of NTF-05 (WP-1147 AK4). The
// subscribed ones each have a subscription with templates in de-DE and
// en-US; the direct ones name the work package that sends them.
var NTF05P3Events = []P3Event{
	{Type: events.AlarmOpened, Category: CategoryAlarm},
	{Type: events.AlarmResolved, Category: CategoryAlarm},
	{Type: events.IncidentOpened, Category: CategoryAlarm},
	{Type: events.IncidentResolved, Category: CategoryAlarm},
	{Type: events.SLAViolated, Category: CategoryAlarm},
	{Type: events.CIConfigChanged, Category: CategoryConfig},
	{Type: events.MaintenanceScheduled, Category: CategoryMaintenance},
	{Type: events.WorkflowRunFailed, Category: CategoryWorkflow},
	// The approval request reaches the approvers of the step directly.
	{Type: events.WorkflowApprovalRequested, Category: CategoryWorkflow, Direct: "WP-1098"},
	// The action send_notification of a run (template
	// workflow.notification).
	{Type: WorkflowNotification, Category: CategoryWorkflow, Direct: "WP-1095"},
	// The steps of an escalation policy address its targets, outside the
	// subscriptions and without a category of Categories().
	{Type: events.NotificationEscalated, Direct: "WP-1145"},
}

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
