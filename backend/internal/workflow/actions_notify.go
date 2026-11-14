package workflow

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
)

// The action send_notification (WFL-02, NTF-05 [P3], WP-1095) notifies
// users, teams, role holders, the contacts of the run's CI and the contacts
// of its client over the notification channels of the organization, in the
// category workflow:
//
//	{"type": "send_notification",
//	 "recipients": {"users": [...], "teams": [...], "roles": [...],
//	                "contact_roles": ["owner", ...],
//	                "client_contacts": {"roles": [...], "organization": false}},
//	 "subject": "text" | {"de": "...", "en": "..."},
//	 "body": "text" | {"de": "...", "en": "..."},
//	 "critical": false, "channel_ids": [...]}
//
// Subject and body may name values of the run context as {{path}} (the dot
// paths of EvaluationValues). Users receive it only with the read
// permission of the object: ci:read in client and site of the run's CI,
// workflow:read without a CI. The delivery is deduplicated per run and
// step, so a repeated step queues nothing twice.

// Fixed texts of a failed send_notification step.
var (
	errNotificationsUnavailable = errors.New("notifications unavailable")
	errNoRecipient              = errors.New("notification has no recipient")
	errNotifyAction             = errors.New("invalid send_notification action")
)

// ciContactRoles are the contact roles of a CI a notification may address
// (NTF-03).
var ciContactRoles = []string{"owner", "escalation", "operator", "responsible", "vendor"}

// placeholder is a {{path}} of a notification text.
var placeholder = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_.\-]+)\s*\}\}`)

// WithNotifier makes send_notification queue its notifications on notifier
// with the recipients of resolver (WP-1095 AK6); without them the step
// fails with "notifications unavailable".
func (e *Executor) WithNotifier(notifier *notify.Notifier, resolver notify.Resolver) *Executor {
	e.notifier, e.recipients = notifier, resolver
	return e
}

// notifyAction is a parsed send_notification action.
type notifyAction struct {
	users, teams, roles, contactRoles []string
	clientContacts                    *notify.ClientContacts
	subject, body                     any
	critical                          bool
	channelIDs                        []string
}

func parseNotifyAction(action JSONMap) (*notifyAction, error) {
	a := &notifyAction{subject: action["subject"], body: action["body"]}
	if v, ok := action["critical"]; ok && v != nil {
		b, isBool := v.(bool)
		if !isBool {
			return nil, errNotifyAction
		}
		a.critical = b
	}
	var err error
	if a.channelIDs, err = stringList(action["channel_ids"]); err != nil {
		return nil, err
	}
	if !validText(a.subject) || (a.body != nil && !validText(a.body)) {
		return nil, errNotifyAction
	}
	rec, _ := action["recipients"].(map[string]any)
	if raw, has := action["recipients"]; has && raw != nil && rec == nil {
		return nil, errNotifyAction
	}
	for key, dst := range map[string]*[]string{"users": &a.users, "teams": &a.teams, "roles": &a.roles, "contact_roles": &a.contactRoles} {
		if *dst, err = stringList(rec[key]); err != nil {
			return nil, err
		}
	}
	for _, r := range a.contactRoles {
		if !slices.Contains(ciContactRoles, r) {
			return nil, errNotifyAction
		}
	}
	if raw, has := rec["client_contacts"]; has && raw != nil {
		cc, ok := raw.(map[string]any)
		if !ok {
			return nil, errNotifyAction
		}
		a.clientContacts = &notify.ClientContacts{}
		if a.clientContacts.ContactRoles, err = stringList(cc["roles"]); err != nil {
			return nil, err
		}
		if v, has := cc["organization"]; has && v != nil {
			b, isBool := v.(bool)
			if !isBool {
				return nil, errNotifyAction
			}
			a.clientContacts.Organization = b
		}
	}
	return a, nil
}

// stringList reads a list of strings (nil: none).
func stringList(v any) ([]string, error) {
	if v == nil {
		return nil, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, errNotifyAction
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		s, isString := item.(string)
		if !isString || strings.TrimSpace(s) == "" {
			return nil, errNotifyAction
		}
		out = append(out, s)
	}
	return out, nil
}

// validText reports whether a text is a non-empty string or an object of
// strings per language.
func validText(v any) bool {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t) != ""
	case map[string]any:
		if len(t) == 0 {
			return false
		}
		for _, s := range t {
			if _, ok := s.(string); !ok {
				return false
			}
		}
		return true
	}
	return false
}

// textIn returns the text of v in the template locale: a plain string in
// every language; of an object the entry of the locale (de-DE or de),
// else the one of the organization's locale, else any.
func textIn(v any, locale, orgLocale string) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		for _, l := range []string{locale, orgLocale} {
			lang, _, _ := strings.Cut(l, "-")
			for _, key := range []string{l, lang} {
				if s, ok := t[key].(string); ok {
					return s
				}
			}
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			if s, ok := t[k].(string); ok {
				return s
			}
		}
	}
	return ""
}

// expand replaces each {{path}} by the value of the run context under that
// dot path; an unknown path becomes empty.
func expand(text string, values map[string]any) string {
	return placeholder.ReplaceAllStringFunc(text, func(m string) string {
		path := placeholder.FindStringSubmatch(m)[1]
		v, ok := values[path]
		if !ok || v == nil {
			return ""
		}
		switch t := v.(type) {
		case string:
			return t
		case map[string]any, JSONMap, []any:
			return ""
		}
		return fmt.Sprint(v)
	})
}

// oneLine collapses every run of white space of a subject, line breaks
// included, into one space: a subject is a mail header, which the SMTP
// provider refuses with a line break (notify/smtp.go), and a placeholder
// value may hold several lines.
func oneLine(text string) string { return strings.Join(strings.Fields(text), " ") }

// sendNotification executes a send_notification action of step index of
// run (WP-1095).
func (e *Executor) sendNotification(ctx context.Context, orgID string, run *Run, index int, action JSONMap) (JSONMap, error) {
	if e.notifier == nil || e.recipients == nil {
		return nil, errNotificationsUnavailable
	}
	a, err := parseNotifyAction(action)
	if err != nil {
		return nil, err
	}
	// A user may be named by a value of the run context ({{requested_by}},
	// WP-1108); one that expands to nothing names no recipient.
	ctxValues := EvaluationValues(run.Context)
	users := make([]string, 0, len(a.users))
	for _, u := range a.users {
		if u = strings.TrimSpace(expand(u, ctxValues)); u != "" {
			users = append(users, u)
		}
	}
	if len(a.users) > 0 && len(users) == 0 {
		return nil, errNoRecipient
	}
	a.users = users
	dedupKey := "workflow:" + run.ID + ":" + strconv.Itoa(index)
	var recipients []notify.Recipient
	if spec, isMaintenance := maintenanceAffected(action); isMaintenance {
		// The recipients of a maintenance window (WP-1109).
		recipients, dedupKey, err = e.maintenanceRecipients(ctx, orgID, run, index, spec)
		if err != nil {
			return nil, err
		}
		if len(recipients) == 0 {
			return JSONMap{"queued": 0}, nil
		}
	} else if recipients, err = e.namedRecipients(ctx, orgID, run, a); err != nil {
		return nil, err
	}
	var brand *notify.Branding
	if source, ok := e.recipients.(notify.BrandingSource); ok {
		if brand, err = source.Branding(ctx, orgID); err != nil {
			return nil, fmt.Errorf("read the branding: %w", err)
		}
	}
	orgLocale := ""
	if brand != nil {
		orgLocale = brand.Locale
	}
	orgTemplate := notify.TemplateLocale("", orgLocale)
	name := ""
	if def, derr := e.repo.GetDefinition(ctx, orgID, run.WorkflowID); derr == nil {
		name = def.Name
	}
	values := EvaluationValues(run.Context)
	texts := map[string]notify.Text{}
	for _, locale := range []string{notify.LocaleDE, notify.LocaleEN} {
		texts[locale] = notify.Render(notify.WorkflowNotification, locale, map[string]any{
			"subject": oneLine(expand(textIn(a.subject, locale, orgTemplate), values)), "body": expand(textIn(a.body, locale, orgTemplate), values),
			"workflow_name": name, "run_id": run.ID,
		})
	}
	queued, err := e.notifier.Notify(ctx, orgID, &notify.Request{Category: notify.CategoryWorkflow, Critical: a.critical,
		Subject: texts[orgTemplate].Subject, Body: texts[orgTemplate].Body, Texts: texts, Branding: brand,
		Data:     map[string]any{"run_id": run.ID, "workflow_id": run.WorkflowID, "step_index": index},
		DedupKey: dedupKey, Recipients: recipients, ChannelIDs: a.channelIDs})
	if err != nil {
		return nil, fmt.Errorf("queue the notification: %w", err)
	}
	return JSONMap{"queued": queued}, nil
}

// namedRecipients resolves the users, teams, roles and contacts a
// send_notification action names (WP-1095).
func (e *Executor) namedRecipients(ctx context.Context, orgID string, run *Run, a *notifyAction) ([]notify.Recipient, error) {
	base := notify.Audience{Permission: identity.PermWorkflowRead}
	if run.CIID != "" && e.cis != nil {
		item, cerr := e.cis.GetByID(ctx, orgID, run.CIID)
		if cerr != nil {
			return nil, fmt.Errorf("read the CI of the run: %w", cerr)
		}
		base = notify.Audience{Permission: identity.PermCIRead, ClientID: item.ClientID, SiteID: item.SiteID}
	}
	// Every named user, team and role must reach someone: an unknown one
	// (or one without the read permission) fails the step.
	for _, named := range []struct {
		ids []string
		set func(a *notify.Audience, id string)
	}{
		{a.users, func(x *notify.Audience, id string) { x.UserIDs = []string{id} }},
		{a.teams, func(x *notify.Audience, id string) { x.TeamIDs = []string{id} }},
		{a.roles, func(x *notify.Audience, id string) { x.Roles = []string{id} }},
	} {
		for _, id := range named.ids {
			single := base
			named.set(&single, id)
			got, rerr := e.recipients.Resolve(ctx, orgID, &single)
			if rerr != nil {
				return nil, fmt.Errorf("resolve recipients: %w", rerr)
			}
			if len(got) == 0 {
				return nil, errNoRecipient
			}
		}
	}
	audience := base
	audience.UserIDs, audience.TeamIDs, audience.Roles = a.users, a.teams, a.roles
	if run.CIID != "" && len(a.contactRoles) > 0 {
		audience.CIID, audience.ContactRoles = run.CIID, a.contactRoles
	}
	audience.ClientContacts = a.clientContacts
	recipients, err := e.recipients.Resolve(ctx, orgID, &audience)
	if err != nil {
		return nil, fmt.Errorf("resolve recipients: %w", err)
	}
	if len(recipients) == 0 {
		return nil, errNoRecipient
	}
	return recipients, nil
}

// The recipient source maintenance_affected of send_notification (WFL-04,
// MGT-04, WP-1109):
//
//	"recipients": {"maintenance_affected": {"window_id": "window.id",
//	               "include_heuristic": false}}
//
// window_id is a path of the run context (default window.id) or the id
// itself. A run of an announcement of POST /maintenance-windows/{id}/notify
// (window.client_id in the context) reaches the contacts of that client in
// the contact roles the operator chose (window.contact_roles; none: every
// contact); a run without client the contacts of every client the window
// affects over the graph (the recipients of WP-1034). Before each delivery
// the window is read again: cancelled, or moved (updated_at newer than
// window.updated_at of the context with another start or end), ends the
// run succeeded with superseded = true and nothing is sent; the run of the
// new announcement takes over. A change that does not move the window
// (title, description, status in_progress) does not supersede the run,
// differing from WP-1109 AK3: the start of the maintenance would otherwise
// end every run before its completion. The delivery is deduplicated per
// schedule and step (maintenance:<window_id>:<starts_at>:<ends_at>:
// <step_index>), so the runs of the several clients of one announcement
// and the run of a re-announcement after such a change send each text to
// each contact once.

// MaintenanceWindow is the state of a window the check reads.
type MaintenanceWindow struct {
	ID        string
	Status    string
	StartsAt  time.Time
	EndsAt    time.Time
	UpdatedAt time.Time
}

// MaintenanceRecipients reads windows and their affected clients
// (maintenance.RecipientFinder; adapter in the router).
type MaintenanceRecipients interface {
	// Window returns the window; ErrMaintenanceWindowNotFound when it is
	// unknown or not visible.
	Window(ctx context.Context, orgID, id string) (*MaintenanceWindow, error)
	// AffectedClients are the clients the window affects over the graph.
	AffectedClients(ctx context.Context, orgID, id string, includeHeuristic bool) ([]string, error)
}

// WithMaintenance sets the source of maintenance_affected (WP-1109).
func (e *Executor) WithMaintenance(m MaintenanceRecipients) *Executor {
	e.maintenance = m
	return e
}

var (
	// ErrMaintenanceWindowNotFound is the fixed text of a step whose window
	// is unknown or not visible (AK1).
	ErrMaintenanceWindowNotFound = errors.New("maintenance window not found")
	// errRunSuperseded ends a run whose window changed or was cancelled.
	errRunSuperseded = errors.New("maintenance window superseded")
)

// maintenanceSpec is a parsed maintenance_affected.
type maintenanceSpec struct {
	windowPath       string
	includeHeuristic bool
}

// maintenanceAffected reads recipients.maintenance_affected of an action.
func maintenanceAffected(action JSONMap) (*maintenanceSpec, bool) {
	rec, _ := action["recipients"].(map[string]any)
	raw, ok := rec["maintenance_affected"].(map[string]any)
	if !ok {
		return nil, false
	}
	s := &maintenanceSpec{windowPath: str(raw, "window_id", "window.id")}
	s.includeHeuristic, _ = raw["include_heuristic"].(bool)
	return s, true
}

// sameInstant compares a context time (RFC 3339) with t to the
// microsecond (the precision of PostgreSQL).
func sameInstant(raw any, t time.Time) bool {
	s, _ := raw.(string)
	parsed, err := time.Parse(time.RFC3339Nano, s)
	return err == nil && parsed.Truncate(time.Microsecond).Equal(t.Truncate(time.Microsecond))
}

// maintenanceRecipients resolves the contacts of the clients a window
// affects for step index of run and its dedup key.
func (e *Executor) maintenanceRecipients(ctx context.Context, orgID string, run *Run, index int, spec *maintenanceSpec) ([]notify.Recipient, string, error) {
	if e.maintenance == nil {
		return nil, "", ErrMaintenanceWindowNotFound
	}
	values := EvaluationValues(run.Context)
	windowID, _ := values[spec.windowPath].(string)
	if windowID == "" && api.IsUUID(spec.windowPath) {
		windowID = spec.windowPath
	}
	if !api.IsUUID(windowID) {
		return nil, "", ErrMaintenanceWindowNotFound
	}
	win, err := e.maintenance.Window(ctx, orgID, windowID)
	if err != nil {
		return nil, "", err
	}
	if win.Status == "cancelled" {
		return nil, "", errRunSuperseded
	}
	if known, ok := values["window.updated_at"].(string); ok && known != "" && !sameInstant(known, win.UpdatedAt) {
		known, _ := time.Parse(time.RFC3339Nano, known)
		moved := !sameInstant(values["window.starts_at"], win.StartsAt) || !sameInstant(values["window.ends_at"], win.EndsAt)
		if win.UpdatedAt.Truncate(time.Microsecond).After(known.Truncate(time.Microsecond)) && moved {
			return nil, "", errRunSuperseded
		}
	}
	var clients, roles []string
	if client, _ := values["window.client_id"].(string); api.IsUUID(client) {
		// The client the operator announced, in the chosen roles.
		clients = []string{client}
		if w, ok := run.Context["window"].(map[string]any); ok {
			// A malformed list (not of an announcement) names no role.
			roles, _ = stringList(w["contact_roles"])
		}
	} else if clients, err = e.maintenance.AffectedClients(ctx, orgID, windowID, spec.includeHeuristic); err != nil {
		return nil, "", err
	}
	var out []notify.Recipient
	seen := map[string]bool{}
	for _, client := range clients {
		got, rerr := e.recipients.Resolve(ctx, orgID, &notify.Audience{ClientID: client, ClientContacts: &notify.ClientContacts{ContactRoles: roles}})
		if rerr != nil {
			return nil, "", fmt.Errorf("resolve recipients: %w", rerr)
		}
		for _, r := range got {
			if key := strings.ToLower(r.Email); key != "" && !seen[key] {
				seen[key] = true
				out = append(out, r)
			}
		}
	}
	schedule := win.StartsAt.UTC().Format(time.RFC3339Nano) + ":" + win.EndsAt.UTC().Format(time.RFC3339Nano)
	return out, "maintenance:" + windowID + ":" + schedule + ":" + strconv.Itoa(index), nil
}
