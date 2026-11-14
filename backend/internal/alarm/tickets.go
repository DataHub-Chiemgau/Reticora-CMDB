package alarm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// The tickets of an incident (ALR-09 d, TKT-01 Monitoring [P3], WP-1039):
// the open tickets at the incident, at its root CI and at the CIs it
// affects are found and linked instead of opening a second ticket (the
// ticket dedupe of WP-1040). Package alarm does not import package ticket;
// the server wires TicketPort over ticket.Service (incidentTickets in
// server/router.go, pattern reviewTickets). Without a port (--no-db
// without the module ticketing) every lookup is empty.

// Link types of a ticket the incidents read (ticket.LinkIncident,
// ticket.LinkAlarm, ticket.LinkCI).
const (
	TicketLinkIncident = "incident"
	TicketLinkAlarm    = "alarm"
	TicketLinkCI       = "ci"
)

// Relations of an open ticket to an incident (OpenTicket.LinkedVia), in
// the order of OpenTickets.
const (
	LinkedViaIncident   = "incident"
	LinkedViaRootCI     = "root_ci"
	LinkedViaAffectedCI = "affected_ci"
)

// TicketStatusClosed is the status of a closed ticket (ticket.StatusClosed).
const TicketStatusClosed = "closed"

// MaxAffectedTicketCIs bounds the affected CIs whose tickets OpenTickets
// reads; beyond it Truncated is true.
const MaxAffectedTicketCIs = 200

var (
	// ErrTicketNotFound: the ticket does not exist or the caller does not
	// see it (ticket RLS).
	ErrTicketNotFound = errors.New("ticket not found")
	// ErrTicketLinked: the ticket is linked to the incident already.
	ErrTicketLinked = errors.New("the ticket is linked to the incident already")
)

// TicketRef is a ticket as the incidents name it.
type TicketRef struct {
	TicketID     string `json:"ticket_id"`
	TicketNumber int    `json:"ticket_number"`
	Status       string `json:"status"`
}

// TicketPort is the ticket side of the incidents.
type TicketPort interface {
	// LinkedTickets returns per id of the link type the tickets the caller
	// sees, sorted by ticket number, with one query; nil without
	// ticket:read or without the feature ticketing_essential.
	LinkedTickets(ctx context.Context, orgID, linkType string, ids []string) (map[string][]TicketRef, error)
	// LinkIncident links the visible ticket to the incident (link type
	// incident) and writes the internal system comment, in the
	// transaction of ctx: link.Comment when the ticket's client and site
	// lie within the incident's (IncidentTicketLink.WithinScope),
	// link.NeutralComment otherwise. An invisible ticket is
	// ErrTicketNotFound, a ticket linked already ErrTicketLinked.
	LinkIncident(ctx context.Context, orgID, ticketID string, link *IncidentTicketLink) error
}

// IncidentTicketLink is what LinkIncident writes for an incident.
type IncidentTicketLink struct {
	Incident *Incident
	// Comment names the root CI with its name.
	Comment string
	// NeutralComment names the root CI only by its id: the comment follows
	// the scope of the ticket, so the readers of a ticket of a wider scope
	// (an org-wide ticket, another client) must not read the name of a CI
	// of the incident's client or site.
	NeutralComment string
}

// WithinScope reports whether a ticket of client and site (empty: none)
// is read only by principals that see the incident's client and site: the
// incident has no client or the same, and no site or the same.
func (l *IncidentTicketLink) WithinScope(clientID, siteID string) bool {
	inc := l.Incident
	return (inc.ClientID == nil || strings.EqualFold(*inc.ClientID, clientID)) &&
		(inc.SiteID == nil || strings.EqualFold(*inc.SiteID, siteID))
}

// OpenTicket is a ticket that is not closed with its relation to the
// incident: the incident itself, the root CI or an affected CI (CIID).
type OpenTicket struct {
	TicketRef
	LinkedVia string  `json:"linked_via"`
	CIID      *string `json:"ci_id,omitempty"`
}

// OpenTickets is the answer of TicketLookup.OpenTickets.
type OpenTickets struct {
	Tickets []OpenTicket `json:"tickets"`
	// Truncated is true when the incident affects more CIs than
	// MaxAffectedTicketCIs: the tickets of the further CIs are not read.
	Truncated bool `json:"truncated"`
}

// AffectedCIsFunc names the CIs an incident affects for sure, without its
// root CI, at most limit+1 of them (limit+1: more follow). WP-1037 brings
// the impact of the analyzer; until then they are the CIs of the
// incident's member alarms (MemberCIs).
type AffectedCIsFunc func(ctx context.Context, inc *Incident, limit int) ([]string, error)

// TicketLookup finds the open tickets of an incident.
type TicketLookup struct {
	port     TicketPort
	affected AffectedCIsFunc
}

// NewTicketLookup returns the lookup on port; affected nil reads the CIs of
// the incident's alarms in store (MemberCIs).
func NewTicketLookup(port TicketPort, store Store, affected AffectedCIsFunc) *TicketLookup {
	if affected == nil {
		affected = MemberCIs(store)
	}
	return &TicketLookup{port: port, affected: affected}
}

// memberCIPage is the page size MemberCIs reads the members with.
const memberCIPage = 500

// MemberCIs is the AffectedCIsFunc over the alarms of the incident in the
// caller's scope: their CIs without the root CI, in the order of the list.
func MemberCIs(store Store) AffectedCIsFunc {
	return func(ctx context.Context, inc *Incident, limit int) ([]string, error) {
		var out []string
		f := &ListFilter{IncidentID: inc.ID, States: States, Limit: memberCIPage}
		for {
			page, err := store.List(ctx, inc.OrganizationID, f)
			if err != nil {
				return nil, err
			}
			for i := range page.Items {
				c := page.Items[i].CIID
				if c == nil || (inc.RootCIID != nil && strings.EqualFold(*c, *inc.RootCIID)) || slices.Contains(out, *c) {
					continue
				}
				out = append(out, *c)
				if len(out) > limit {
					return out, nil
				}
			}
			if page.Next == nil {
				return out, nil
			}
			f.After = page.Next
		}
	}
}

// OpenTickets returns the tickets that are not closed at the incident, at
// its root CI and at the CIs it affects (at most MaxAffectedTicketCIs,
// beyond Truncated), deduplicated by ticket and sorted by relation
// (incident, root CI, affected CI), then by ticket number. Two reads of
// LinkedTickets: link type incident, link type ci for root and affected
// CIs. Tickets the caller does not see are not named (ticket RLS).
func (l *TicketLookup) OpenTickets(ctx context.Context, inc *Incident) (*OpenTickets, error) {
	out := &OpenTickets{Tickets: []OpenTicket{}}
	if l == nil || l.port == nil || inc == nil {
		return out, nil
	}
	org := inc.OrganizationID
	affected, err := l.affected(ctx, inc, MaxAffectedTicketCIs)
	if err != nil {
		return nil, fmt.Errorf("affected cis of the incident: %w", err)
	}
	if len(affected) > MaxAffectedTicketCIs {
		affected, out.Truncated = affected[:MaxAffectedTicketCIs], true
	}
	byIncident, err := l.port.LinkedTickets(ctx, org, TicketLinkIncident, []string{inc.ID})
	if err != nil {
		return nil, err
	}
	var ciIDs []string
	if inc.RootCIID != nil {
		ciIDs = append(ciIDs, *inc.RootCIID)
	}
	ciIDs = append(ciIDs, affected...)
	var byCI map[string][]TicketRef
	if len(ciIDs) > 0 {
		if byCI, err = l.port.LinkedTickets(ctx, org, TicketLinkCI, ciIDs); err != nil {
			return nil, err
		}
	}
	seen := map[string]bool{}
	add := func(refs []TicketRef, via string, ci *string) {
		group := make([]OpenTicket, 0, len(refs))
		for _, ref := range refs {
			if ref.Status == TicketStatusClosed || seen[ref.TicketID] {
				continue
			}
			seen[ref.TicketID] = true
			group = append(group, OpenTicket{TicketRef: ref, LinkedVia: via, CIID: ci})
		}
		out.Tickets = append(out.Tickets, group...)
	}
	add(byIncident[inc.ID], LinkedViaIncident, nil)
	if inc.RootCIID != nil {
		root := *inc.RootCIID
		add(byCI[root], LinkedViaRootCI, &root)
	}
	start := len(out.Tickets)
	for _, c := range affected {
		ci := c
		add(byCI[c], LinkedViaAffectedCI, &ci)
	}
	// The affected CIs together, by ticket number.
	tail := out.Tickets[start:]
	slices.SortStableFunc(tail, func(a, b OpenTicket) int { return a.TicketNumber - b.TicketNumber })
	return out, nil
}

// incidentTicketLink is the link of a ticket to inc with both comments.
func incidentTicketLink(inc *Incident, rootName string) *IncidentTicketLink {
	return &IncidentTicketLink{Incident: inc, Comment: incidentTicketComment(inc, rootName),
		NeutralComment: incidentTicketComment(inc, "")}
}

// incidentTicketComment is the system comment of a ticket linked to an
// incident: the incident, its root and its cause class; without rootName
// the root CI only by its id.
func incidentTicketComment(inc *Incident, rootName string) string {
	var b strings.Builder
	b.WriteString("Linked to monitoring incident " + inc.ID)
	switch {
	case rootName != "" && inc.RootCIID != nil:
		b.WriteString(", root " + rootName + " (" + *inc.RootCIID + ")")
	case inc.RootCIID != nil:
		b.WriteString(", root CI " + *inc.RootCIID)
	}
	b.WriteString(", cause class " + causeOrUnknown(inc.CauseClass) + ".")
	return b.String()
}

// firstOpenTicket is the first ticket of refs (sorted by number) that is
// not closed; nil for none.
func firstOpenTicket(refs []TicketRef) *TicketRef {
	for i := range refs {
		if refs[i].Status != TicketStatusClosed {
			r := refs[i]
			return &r
		}
	}
	return nil
}

// Tickets from monitoring without a second ticket (TKT-01 Monitoring [P3],
// MON-04, WFL-06, WP-1040, E-205 option 2). An alert rule with the action
// ticket (actions.ticket.enabled) opens a ticket for an alarm it raises:
// after the commit of the raise (database.AfterCommit) OpenAlarmTicket
// runs in the system context of the organization (no principal; the
// reporter is actions.ticket.reporter_id, the user who saved the rule).
// The dedupe key is the root cause (alert_root_cause, WFL-06): the
// incident when the alarm is its root, otherwise the CI of the alarm (the
// alarm without CI): ticket.Service.Create with Exclusive on that target
// under its advisory lock, so two alarms of one CI or two parallel workers
// open one ticket. Before creating, an open ticket at the incident or the
// CI is linked (link alarm) and gets a system comment instead; a race
// (ErrOpenTicketExists) is handled the same way. A symptom alarm of an
// incident opens no ticket: it is linked to and commented at the ticket of
// its incident. The alarm keeps ticket_id and ticket_existing in its
// details. When the alarm resolves, its linked open tickets get a system
// comment, and with close_on_resolve the ticket goes to resolved when its
// state machine allows. Without ticketing_essential no ticket is opened
// (reticora_alarm_ticket_skipped_total{reason="not_entitled"}); a failure
// is logged and counted (reticora_alarm_ticket_errors_total) and never
// rolls back the alarm.

// TicketTarget is an object a ticket is linked to (link type and id).
type TicketTarget struct {
	Type string
	ID   string
}

// TicketRequest is a ticket the monitoring opens.
type TicketRequest struct {
	Title       string
	Description string
	Priority    string
	Category    string
	ReporterID  string
	TeamID      string
	AssigneeID  string
	RelatedCIID string
	Links       []TicketTarget
	// Exclusive is the dedupe target: an open ticket linked to it fails
	// the creation with *TicketExistsError.
	Exclusive *TicketTarget
	// Origin is the object the ticket was created for (audit
	// ticket.create).
	Origin *TicketTarget
	// CheckFieldRights makes the writer check the field rights of the
	// caller's policy on RequestFields before creating (the manual ticket
	// of an incident, like reviewTickets.CreateTicket); the ticket action
	// of a rule runs without principal and checks none.
	CheckFieldRights bool
	// RequestFields are the fields the request body set (priority,
	// team_id, assignee_id).
	RequestFields []string
}

// TicketExistsError: an open ticket is linked to the exclusive target.
type TicketExistsError struct {
	TicketID     string
	TicketNumber int
}

func (e *TicketExistsError) Error() string {
	return fmt.Sprintf("open ticket #%d (%s) exists already", e.TicketNumber, e.TicketID)
}

// TicketWriter is the writing ticket side of the alarms (WP-1040), wired
// over ticket.Service by the server; it works in the context's tenant
// scope and needs no principal.
type TicketWriter interface {
	// Entitled reports whether the organization has ticketing_essential.
	Entitled(ctx context.Context, orgID string) bool
	// Linked returns per id the tickets linked with the link type.
	Linked(ctx context.Context, orgID, linkType string, ids []string) (map[string][]TicketRef, error)
	// CreateTicket opens the ticket with its links (*TicketExistsError for
	// an open ticket at the exclusive target).
	CreateTicket(ctx context.Context, orgID string, req *TicketRequest) (*TicketRef, error)
	// LinkAndComment links the ticket to the targets (an existing link is
	// kept) and writes the internal system comment once.
	LinkAndComment(ctx context.Context, orgID, ticketID string, targets []TicketTarget, comment string) error
	// ResolveTicket writes the system comment and, with resolve, sets the
	// ticket resolved when its state machine allows it.
	ResolveTicket(ctx context.Context, orgID, ticketID, comment string, resolve bool) error
}

// Details keys of the ticket of an alarm.
const (
	DetailsTicketID       = "ticket_id"
	DetailsTicketExisting = "ticket_existing"
)

// Reasons of reticora_alarm_ticket_skipped_total.
const (
	TicketSkippedNotEntitled = "not_entitled"
	TicketSkippedSymptom     = "symptom_without_ticket"
	// TicketSkippedSuppressed: the alarm is suppressed for maintenance; its
	// ticket is decided when the window releases it.
	TicketSkippedSuppressed = "suppressed"
)

var (
	alarmTicketErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "reticora", Name: "alarm_ticket_errors_total",
		Help: "Tickets of alarms that could not be opened, linked, commented or resolved (WP-1040).",
	})
	alarmTicketSkipped = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "reticora", Name: "alarm_ticket_skipped_total",
		Help: "Ticket actions of alarms that opened no ticket, by reason (not_entitled, symptom_without_ticket, suppressed).",
	}, []string{"reason"})
)

// RegisterTicketMetrics registers the metrics of the ticket action.
func RegisterTicketMetrics(reg prometheus.Registerer) {
	reg.MustRegister(alarmTicketErrors, alarmTicketSkipped)
}

// TicketErrors and TicketSkipped return the counters (tests).
func TicketErrors() prometheus.Counter { return alarmTicketErrors }

// TicketSkipped returns the counter of skipped ticket actions of reason.
func TicketSkipped(reason string) prometheus.Counter {
	return alarmTicketSkipped.WithLabelValues(reason)
}

// WithTicketWriter makes the ticket action of the rules open tickets with
// w (WP-1040).
func (s *Service) WithTicketWriter(w TicketWriter) *Service {
	s.tickets = w
	return s
}

// ticketAction returns the enabled ticket action of the alarm's details,
// nil without one.
func ticketAction(details map[string]any) *events.AlarmTicketAction {
	t := actionsOf(details).Ticket
	if t == nil || !t.Enabled {
		return nil
	}
	return t
}

// maintenanceSuppressed reports whether a is suppressed for maintenance:
// it opens no ticket while the window lasts (the notification skips it as
// well); its release decides the ticket (releaseMaintenance).
func maintenanceSuppressed(a *Alarm) bool {
	return deref(a.SuppressedReason) == SuppressedMaintenance
}

// afterOpen registers OpenAlarmTicket for after the commit of the raise
// that opened a, when its ticket action is on.
func (s *Service) afterOpen(ctx context.Context, a *Alarm) {
	if s.tickets == nil || s.pool == nil || ticketAction(a.Details) == nil {
		return
	}
	if maintenanceSuppressed(a) {
		alarmTicketSkipped.WithLabelValues(TicketSkippedSuppressed).Inc()
		return
	}
	org, id := a.OrganizationID, a.ID
	database.AfterCommit(ctx, func(ctx context.Context) {
		if _, err := s.OpenAlarmTicket(context.WithoutCancel(ctx), org, id); err != nil {
			alarmTicketErrors.Inc()
			slog.Warn("alarm ticket not opened", "organization_id", org, "alarm_id", id, "error", err)
		}
	})
}

// afterResolve registers the resolution comment of the tickets of a for
// after the commit of its resolution.
func (s *Service) afterResolve(ctx context.Context, a *Alarm) {
	if s.tickets == nil || s.pool == nil || ticketAction(a.Details) == nil {
		return
	}
	org, id := a.OrganizationID, a.ID
	database.AfterCommit(ctx, func(ctx context.Context) {
		if err := s.ResolveAlarmTickets(context.WithoutCancel(ctx), org, id); err != nil {
			alarmTicketErrors.Inc()
			slog.Warn("alarm ticket not commented on resolution", "organization_id", org, "alarm_id", id, "error", err)
		}
	})
}

// TicketOutcome is what OpenAlarmTicket did: the ticket and whether it
// existed already.
type TicketOutcome struct {
	Ticket   *TicketRef
	Existing bool
}

// systemCtx is ctx in the org-wide scope of the organization (the worker
// after the commit has no principal).
func systemCtx(ctx context.Context, orgID string) context.Context {
	scope := database.OrgWideScope(orgID, "")
	return database.ContextWithTenantScope(database.WithoutTx(ctx), &scope)
}

// ticketPriority maps the severity of an alarm to a ticket priority.
func ticketPriority(severity, configured string) string {
	if configured != "" {
		return configured
	}
	switch severity {
	case SeverityCritical:
		return "critical"
	case SeverityWarning:
		return "medium"
	}
	return "low"
}

// firstOpen returns the first open ticket of the lists.
func firstOpen(lists ...[]TicketRef) *TicketRef {
	for _, refs := range lists {
		if r := firstOpenTicket(refs); r != nil {
			return r
		}
	}
	return nil
}

// OpenAlarmTicket opens or links the ticket of the alarm (see above); nil
// without an enabled ticket action, without the feature or for a symptom
// without ticket of its incident.
func (s *Service) OpenAlarmTicket(ctx context.Context, orgID, alarmID string) (*TicketOutcome, error) {
	if s.tickets == nil {
		return nil, nil
	}
	ctx = systemCtx(ctx, orgID)
	a, err := s.store.Get(ctx, orgID, alarmID)
	if err != nil {
		return nil, err
	}
	action := ticketAction(a.Details)
	if action == nil || maintenanceSuppressed(a) || a.State == StateResolved {
		return nil, nil
	}
	if !s.tickets.Entitled(ctx, orgID) {
		alarmTicketSkipped.WithLabelValues(TicketSkippedNotEntitled).Inc()
		return nil, nil
	}
	alarmTarget := TicketTarget{Type: TicketLinkAlarm, ID: a.ID}
	var inc *Incident
	if a.IncidentID != nil {
		if inc, err = s.store.GetIncident(ctx, orgID, *a.IncidentID); err != nil && !errors.Is(err, ErrIncidentNotFound) {
			return nil, err
		}
	}
	// A symptom of an incident: its alarm goes to the ticket of the
	// incident (or of the incident's root alarm), never a ticket of its
	// own.
	if inc != nil && (inc.RootAlarmID == nil || *inc.RootAlarmID != a.ID) {
		var byIncident map[string][]TicketRef
		byIncident, err = s.tickets.Linked(ctx, orgID, TicketLinkIncident, []string{inc.ID})
		if err != nil {
			return nil, err
		}
		var byRoot map[string][]TicketRef
		if inc.RootAlarmID != nil {
			if byRoot, err = s.tickets.Linked(ctx, orgID, TicketLinkAlarm, []string{*inc.RootAlarmID}); err != nil {
				return nil, err
			}
		}
		var rootRefs []TicketRef
		if inc.RootAlarmID != nil {
			rootRefs = byRoot[*inc.RootAlarmID]
		}
		t := firstOpen(byIncident[inc.ID], rootRefs)
		if t == nil {
			alarmTicketSkipped.WithLabelValues(TicketSkippedSymptom).Inc()
			return nil, nil
		}
		if fmt.Sprint(a.Details[DetailsTicketID]) == t.TicketID {
			// Linked already (a repeated decision after the correlation).
			return &TicketOutcome{Ticket: t, Existing: true}, nil
		}
		if lerr := s.tickets.LinkAndComment(ctx, orgID, t.TicketID, []TicketTarget{alarmTarget},
			"Symptom alarm "+a.ID+" of monitoring incident "+inc.ID+": "+a.Summary+"."); lerr != nil {
			return nil, lerr
		}
		return s.recordTicket(ctx, orgID, a.ID, t, true)
	}
	// The dedupe target: the incident of a root alarm, else the CI, else
	// the alarm (E-205, see the comment above).
	exclusive, origin := alarmTarget, alarmTarget
	switch {
	case inc != nil:
		exclusive = TicketTarget{Type: TicketLinkIncident, ID: inc.ID}
		origin = exclusive
	case a.CIID != nil:
		exclusive = TicketTarget{Type: TicketLinkCI, ID: *a.CIID}
	}
	// An open ticket at the exclusive target is linked and commented; for
	// the root of an incident also one at its root CI or one the root alarm
	// already holds (opened before the correlation formed the incident):
	// it is linked to the incident as well, no second ticket (AK3).
	existing, err := s.tickets.Linked(ctx, orgID, exclusive.Type, []string{exclusive.ID})
	if err != nil {
		return nil, err
	}
	comment := "Monitoring alarm " + a.ID + " (" + a.Severity + "): " + a.Summary + "."
	if t := firstOpen(existing[exclusive.ID]); t != nil {
		if fmt.Sprint(a.Details[DetailsTicketID]) == t.TicketID {
			return &TicketOutcome{Ticket: t, Existing: a.Details[DetailsTicketExisting] == true}, nil
		}
		if lerr := s.tickets.LinkAndComment(ctx, orgID, t.TicketID, []TicketTarget{alarmTarget}, comment); lerr != nil {
			return nil, lerr
		}
		return s.recordTicket(ctx, orgID, a.ID, t, true)
	}
	if inc != nil {
		rootCI := inc.RootCIID
		if rootCI == nil {
			rootCI = a.CIID
		}
		var atCI, atAlarm map[string][]TicketRef
		if rootCI != nil {
			if atCI, err = s.tickets.Linked(ctx, orgID, TicketLinkCI, []string{*rootCI}); err != nil {
				return nil, err
			}
		}
		if atAlarm, err = s.tickets.Linked(ctx, orgID, TicketLinkAlarm, []string{a.ID}); err != nil {
			return nil, err
		}
		var ciRefs []TicketRef
		if rootCI != nil {
			ciRefs = atCI[*rootCI]
		}
		if t := firstOpen(atAlarm[a.ID], ciRefs); t != nil {
			if lerr := s.tickets.LinkAndComment(ctx, orgID, t.TicketID, []TicketTarget{exclusive, alarmTarget},
				comment+" Root alarm of monitoring incident "+inc.ID+"."); lerr != nil {
				return nil, lerr
			}
			own := fmt.Sprint(a.Details[DetailsTicketID]) == t.TicketID && a.Details[DetailsTicketExisting] == false
			return s.recordTicket(ctx, orgID, a.ID, t, !own)
		}
	}
	req := &TicketRequest{Title: ticketTitle(a), Description: ticketDescription(a, inc), Priority: ticketPriority(a.Severity, action.Priority),
		Category: action.Category, ReporterID: action.ReporterID, Links: []TicketTarget{alarmTarget}, Exclusive: &exclusive, Origin: &origin}
	if a.CIID != nil {
		req.RelatedCIID = *a.CIID
		if exclusive.Type != TicketLinkCI {
			req.Links = append(req.Links, TicketTarget{Type: TicketLinkCI, ID: *a.CIID})
		}
	}
	t, err := s.tickets.CreateTicket(ctx, orgID, req)
	var exists *TicketExistsError
	switch {
	case errors.As(err, &exists):
		// A parallel worker opened it first.
		ref := &TicketRef{TicketID: exists.TicketID, TicketNumber: exists.TicketNumber}
		if lerr := s.tickets.LinkAndComment(ctx, orgID, ref.TicketID, []TicketTarget{alarmTarget}, comment); lerr != nil {
			return nil, lerr
		}
		return s.recordTicket(ctx, orgID, a.ID, ref, true)
	case err != nil:
		return nil, err
	}
	return s.recordTicket(ctx, orgID, a.ID, t, false)
}

// recordTicket keeps the ticket of the alarm in its details.
func (s *Service) recordTicket(ctx context.Context, orgID, alarmID string, t *TicketRef, existing bool) (*TicketOutcome, error) {
	err := s.inTx(ctx, orgID, func(ctx context.Context) error {
		a, err := s.store.LockForUpdate(ctx, orgID, alarmID)
		if err != nil {
			return err
		}
		details := make(map[string]any, len(a.Details)+2)
		for k, v := range a.Details {
			details[k] = v
		}
		details[DetailsTicketID] = t.TicketID
		details[DetailsTicketExisting] = existing
		u := stateOf(a)
		u.Details = details
		_, err = s.store.UpdateState(ctx, orgID, alarmID, u)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &TicketOutcome{Ticket: t, Existing: existing}, nil
}

// ResolveAlarmTickets comments the open tickets of the resolved alarm and,
// with close_on_resolve, resolves a ticket the monitoring opened once no
// other alarm on it is still active: a symptom or the second alarm of a CI
// that clears leaves the shared ticket open, the last one (the cause)
// closes it; a ticket a person opened is only commented. A ticket of an
// incident that is not resolved waits for the incident
// (ResolveIncidentTickets).
func (s *Service) ResolveAlarmTickets(ctx context.Context, orgID, alarmID string) error {
	if s.tickets == nil {
		return nil
	}
	ctx = systemCtx(ctx, orgID)
	a, err := s.store.Get(ctx, orgID, alarmID)
	if err != nil {
		return err
	}
	action := ticketAction(a.Details)
	if action == nil || !s.tickets.Entitled(ctx, orgID) {
		return nil
	}
	linked, err := s.tickets.Linked(ctx, orgID, TicketLinkAlarm, []string{a.ID})
	if err != nil {
		return err
	}
	at := "now"
	if a.ResolvedAt != nil {
		at = a.ResolvedAt.UTC().Format(time.RFC3339)
	}
	incidentOpen := false
	if a.IncidentID != nil {
		inc, ierr := s.store.GetIncident(ctx, orgID, *a.IncidentID)
		switch {
		case ierr == nil:
			incidentOpen = inc.State != IncidentResolved
		case !errors.Is(ierr, ErrIncidentNotFound):
			return ierr
		}
	}
	var errs []error
	for _, t := range linked[a.ID] {
		if t.Status == TicketStatusClosed {
			continue
		}
		resolve := false
		if action.CloseOnResolve && !incidentOpen {
			others, opened, uerr := s.store.TicketUse(ctx, orgID, t.TicketID, a.ID)
			if uerr != nil {
				errs = append(errs, uerr)
				continue
			}
			resolve = opened && others == 0
		}
		if err := s.tickets.ResolveTicket(ctx, orgID, t.TicketID, "Monitoring alarm "+a.ID+" resolved at "+at+".", resolve); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Incidents and their tickets (WP-1040 AK2, AK3, AK5): the correlation
// forms an incident after its alarms opened, so their ticket decision runs
// again when alarms join an incident or its root changes (AttachAlarms,
// SetRoot, SetRootManual of the Service, after the commit): the root opens
// or links the incident's ticket (a ticket it holds already, or one at the
// root CI, is linked to the incident), the symptoms are linked to it. When
// the incident resolves (TransitionIncident to resolved, the absorbed
// incident of MergeIncidents) its open tickets get a system comment and,
// with close_on_resolve of the root alarm's ticket action, a ticket the
// monitoring opened is resolved once no alarm on it is active.

// AttachAlarms is Store.AttachAlarms with the ticket decision of the
// alarms after the commit.
func (s *Service) AttachAlarms(ctx context.Context, orgID, incidentID string, alarmIDs []string, role string) (*Incident, error) {
	var out *Incident
	err := s.inTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		if out, err = s.store.AttachAlarms(ctx, orgID, incidentID, alarmIDs, role); err != nil {
			return err
		}
		s.afterIncident(ctx, orgID, incidentID)
		return nil
	})
	return out, err
}

// SetRoot is Store.SetRoot with the ticket decision after the commit.
func (s *Service) SetRoot(ctx context.Context, orgID, incidentID, alarmID string, manual bool) (*Incident, error) {
	var out *Incident
	err := s.inTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		if out, err = s.store.SetRoot(ctx, orgID, incidentID, alarmID, manual); err != nil {
			return err
		}
		s.afterIncident(ctx, orgID, incidentID)
		return nil
	})
	return out, err
}

// SetRootManual is Store.SetRootManual with the ticket decision after the
// commit.
func (s *Service) SetRootManual(ctx context.Context, orgID, incidentID, alarmID string, actor *string, reason string) (*Incident, error) {
	var out *Incident
	err := s.inTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		if out, err = s.store.SetRootManual(ctx, orgID, incidentID, alarmID, actor, reason); err != nil {
			return err
		}
		s.afterIncident(ctx, orgID, incidentID)
		return nil
	})
	return out, err
}

// TransitionIncident is Store.TransitionIncident; a resolved incident
// comments (and closes) its tickets after the commit.
func (s *Service) TransitionIncident(ctx context.Context, orgID, id, to string, actorID *string) (*Incident, error) {
	var out *Incident
	err := s.inTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		if out, err = s.store.TransitionIncident(ctx, orgID, id, to, actorID); err != nil {
			return err
		}
		if out.State == IncidentResolved {
			s.afterIncidentResolved(ctx, orgID, id)
		}
		return nil
	})
	return out, err
}

// MergeIncidents is Store.MergeIncidents; the absorbed incident is resolved
// (its tickets are commented), the kept one decides the tickets of its new
// alarms.
func (s *Service) MergeIncidents(ctx context.Context, orgID, keepID, absorbID, rootAlarmID string) (*Incident, error) {
	var out *Incident
	err := s.inTx(ctx, orgID, func(ctx context.Context) error {
		var err error
		if out, err = s.store.MergeIncidents(ctx, orgID, keepID, absorbID, rootAlarmID); err != nil {
			return err
		}
		s.afterIncidentResolved(ctx, orgID, absorbID)
		s.afterIncident(ctx, orgID, keepID)
		return nil
	})
	return out, err
}

// afterIncident registers IncidentTickets for after the commit.
func (s *Service) afterIncident(ctx context.Context, orgID, incidentID string) {
	if s.tickets == nil || s.pool == nil {
		return
	}
	database.AfterCommit(ctx, func(ctx context.Context) {
		if err := s.IncidentTickets(context.WithoutCancel(ctx), orgID, incidentID); err != nil {
			alarmTicketErrors.Inc()
			slog.Warn("incident tickets not decided", "organization_id", orgID, "incident_id", incidentID, "error", err)
		}
	})
}

// afterIncidentResolved registers ResolveIncidentTickets for after the
// commit.
func (s *Service) afterIncidentResolved(ctx context.Context, orgID, incidentID string) {
	if s.tickets == nil || s.pool == nil {
		return
	}
	database.AfterCommit(ctx, func(ctx context.Context) {
		if err := s.ResolveIncidentTickets(context.WithoutCancel(ctx), orgID, incidentID); err != nil {
			alarmTicketErrors.Inc()
			slog.Warn("incident tickets not commented on resolution", "organization_id", orgID, "incident_id", incidentID, "error", err)
		}
	})
}

// IncidentTickets decides the tickets of the alarms of an incident: the
// root first (it opens or links the incident's ticket), then the symptoms
// (linked to it). Alarms without ticket action are left alone.
func (s *Service) IncidentTickets(ctx context.Context, orgID, incidentID string) error {
	if s.tickets == nil {
		return nil
	}
	sctx := systemCtx(ctx, orgID)
	inc, err := s.store.GetIncident(sctx, orgID, incidentID)
	if err != nil {
		return err
	}
	var ids []string
	if inc.RootAlarmID != nil {
		ids = append(ids, *inc.RootAlarmID)
	}
	f := &ListFilter{IncidentID: incidentID, States: []string{StatePending, StateOpen, StateAcknowledged}, Limit: memberCIPage}
	for {
		page, err := s.store.List(sctx, orgID, f)
		if err != nil {
			return err
		}
		for i := range page.Items {
			if id := page.Items[i].ID; (inc.RootAlarmID == nil || id != *inc.RootAlarmID) && ticketAction(page.Items[i].Details) != nil {
				ids = append(ids, id)
			}
		}
		if page.Next == nil {
			break
		}
		f.After = page.Next
	}
	var errs []error
	for _, id := range ids {
		if _, err := s.OpenAlarmTicket(ctx, orgID, id); err != nil {
			errs = append(errs, fmt.Errorf("alarm %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

// ResolveIncidentTickets comments the open tickets linked to the resolved
// incident (also one opened by hand, POST /monitoring/incidents/{id}/ticket)
// and, with close_on_resolve of the root alarm's ticket action, resolves a
// ticket the monitoring opened once no alarm on it is active.
func (s *Service) ResolveIncidentTickets(ctx context.Context, orgID, incidentID string) error {
	if s.tickets == nil {
		return nil
	}
	ctx = systemCtx(ctx, orgID)
	if !s.tickets.Entitled(ctx, orgID) {
		return nil
	}
	inc, err := s.store.GetIncident(ctx, orgID, incidentID)
	if err != nil {
		return err
	}
	closeOnResolve := false
	if inc.RootAlarmID != nil {
		root, rerr := s.store.Get(ctx, orgID, *inc.RootAlarmID)
		switch {
		case rerr == nil:
			if t := ticketAction(root.Details); t != nil {
				closeOnResolve = t.CloseOnResolve
			}
		case !errors.Is(rerr, ErrNotFound):
			return rerr
		}
	}
	linked, err := s.tickets.Linked(ctx, orgID, TicketLinkIncident, []string{inc.ID})
	if err != nil {
		return err
	}
	at := "now"
	if inc.ResolvedAt != nil {
		at = inc.ResolvedAt.UTC().Format(time.RFC3339)
	}
	var errs []error
	for _, t := range linked[inc.ID] {
		if t.Status == TicketStatusClosed {
			continue
		}
		resolve := false
		if closeOnResolve {
			others, opened, uerr := s.store.TicketUse(ctx, orgID, t.TicketID, "")
			if uerr != nil {
				errs = append(errs, uerr)
				continue
			}
			resolve = opened && others == 0
		}
		if err := s.tickets.ResolveTicket(ctx, orgID, t.TicketID, "Monitoring incident "+inc.ID+" resolved at "+at+".", resolve); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// truncateRunes cuts s to at most n characters, never inside a UTF-8
// sequence.
func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// ticketTitle is the title of the ticket of an alarm.
func ticketTitle(a *Alarm) string {
	return truncateRunes("["+a.Severity+"] "+a.Summary, 200)
}

// ticketDescription names the alarm, its source and the incident.
func ticketDescription(a *Alarm, inc *Incident) string {
	var b strings.Builder
	b.WriteString("Opened by monitoring for alarm " + a.ID + " (source " + a.SourceKind + ", series " + a.SeriesKey + ").")
	if inc != nil {
		b.WriteString(" Root alarm of incident " + inc.ID + ", cause class " + causeOrUnknown(inc.CauseClass) + ".")
	}
	return b.String()
}
