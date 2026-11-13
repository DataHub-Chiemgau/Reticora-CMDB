package alarm

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
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
