package workflow

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

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
		DedupKey: "workflow:" + run.ID + ":" + strconv.Itoa(index), Recipients: recipients, ChannelIDs: a.channelIDs})
	if err != nil {
		return nil, fmt.Errorf("queue the notification: %w", err)
	}
	return JSONMap{"queued": queued}, nil
}
