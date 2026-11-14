package alarm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// REST of the incidents (ALR-03, ALR-10, WP-1036, E-160):
//
//	GET  /api/v1/monitoring/incidents             list (filters state, client_id, cause_class, from/to, resolved_from; cursor)
//	GET  /api/v1/monitoring/incidents/{id}        the incident with its members, reasons and timeline
//	POST /api/v1/monitoring/incidents/{id}/ack    acknowledge it and its root alarm (alert:acknowledge)
//	POST /api/v1/monitoring/incidents/{id}/root   set the root by hand (monitoring:write)
//	POST /api/v1/monitoring/incidents/{id}/tickets/{ticket_id}
//	                                              link an existing ticket (ticket:write and monitoring:read, ticketing_essential)
//
// The routes read in the caller's scope (row level security of incident,
// incident_transition, alarm and alarm_transition): a foreign or invisible
// incident is 404, members outside the scope are left out. Both POST
// routes write incident.updated in their transaction as the last write
// (Konvention 13 and 20); the route audit records them with the row of the
// incident (snapshot register).

// Paths of the incident routes.
const (
	incidentsPath = "/api/v1/monitoring/incidents"
	incidentPath  = incidentsPath + "/{id}"
)

// MaxTimeline is how many entries the timeline of an incident holds;
// beyond it timeline_truncated is true.
const MaxTimeline = 500

// maxMembers bounds the members an incident detail lists.
const maxMembers = 1000

// Kinds of the change of incident.updated.
const (
	IncidentChangeAcknowledged = "acknowledged"
	IncidentChangeRootChanged  = "root_changed"
)

// incidentListSort is the sort of the list and its cursor.
const (
	incidentListSort = "opened_at"
	incidentListDir  = "desc"
)

// IncidentRoot is the root of an incident in an answer: the alarm, its CI
// and the name of the CI (empty when the caller does not see it).
type IncidentRoot struct {
	AlarmID *string `json:"alarm_id"`
	CIID    *string `json:"ci_id"`
	CIName  *string `json:"ci_name"`
}

// IncidentView is an incident of the list.
type IncidentView struct {
	Incident
	Root         IncidentRoot `json:"root"`
	SymptomCount int          `json:"symptom_count"`
	// Ticket is the first ticket linked to the incident that is not closed
	// (WP-1039); absent without one, without ticket:read or without
	// ticketing_essential.
	Ticket *TicketRef `json:"ticket,omitempty"`
}

// IncidentMember is an alarm of an incident with its role.
type IncidentMember struct {
	View
	Role string `json:"role"`
}

// TimelineEntry is one entry of the timeline of an incident: a transition
// of the incident (kind incident) or of one of its alarms (kind alarm).
type TimelineEntry struct {
	Kind      string         `json:"kind"`
	AlarmID   *string        `json:"alarm_id"`
	FromState *string        `json:"from_state"`
	ToState   string         `json:"to_state"`
	At        time.Time      `json:"at"`
	ActorID   *string        `json:"actor_id"`
	Reason    string         `json:"reason"`
	Comment   *string        `json:"comment"`
	Details   map[string]any `json:"details"`
}

// IncidentDetailView is the detail of an incident.
type IncidentDetailView struct {
	IncidentView
	Members []IncidentMember `json:"members"`
	// MembersTruncated is true when the incident has more members than
	// the detail lists (maxMembers).
	MembersTruncated  bool            `json:"members_truncated"`
	Timeline          []TimelineEntry `json:"timeline"`
	TimelineTruncated bool            `json:"timeline_truncated"`
}

// registerIncidentRoutes mounts the incident routes (RegisterRoutes).
func (h *Handler) registerIncidentRoutes(r interface {
	Get(pattern string, fn http.HandlerFunc)
	Post(pattern string, fn http.HandlerFunc)
}) {
	r.Get(incidentsPath, h.listIncidents)
	r.Get(incidentPath, h.getIncident)
	r.Post(incidentPath+"/ack", h.ackIncident)
	r.Post(incidentPath+"/root", h.setIncidentRoot)
	r.Post(incidentPath+"/tickets/{ticket_id}", h.linkIncidentTicket)
	r.Post(incidentPath+"/ticket", h.createIncidentTicket)
}

// parseIncidentFilter reads the filters of the list.
func parseIncidentFilter(r *http.Request) (*IncidentFilter, error) {
	q := r.URL.Query()
	f := &IncidentFilter{}
	var err error
	if f.States, err = multi(q, "state", IncidentStates); err != nil {
		return nil, err
	}
	if f.CauseClasses, err = multi(q, "cause_class", CauseClasses); err != nil {
		return nil, err
	}
	if v := q.Get("client_id"); v != "" {
		if !api.IsUUID(v) {
			return nil, unprocessable("client_id", "client_id must be a UUID")
		}
		f.ClientID = strings.ToLower(v)
	}
	// from and to apply to opened_at (AK1), as in the alarm list.
	for name, dst := range map[string]**time.Time{"from": &f.OpenedFrom, "to": &f.OpenedTo} {
		if v := q.Get(name); v != "" {
			at, perr := time.Parse(time.RFC3339, v)
			if perr != nil {
				return nil, unprocessable(name, name+" must be an RFC 3339 date-time")
			}
			*dst = &at
		}
	}
	if f.OpenedFrom != nil && f.OpenedTo != nil && !f.OpenedFrom.Before(*f.OpenedTo) {
		return nil, unprocessable("to", "to must be after from")
	}
	// resolved_from applies to resolved_at (WP-1045): an incident resolved
	// before it is not listed, one not resolved is. With state=resolved it
	// is "resolved since", independent of when the incident opened.
	if v := q.Get("resolved_from"); v != "" {
		at, perr := time.Parse(time.RFC3339, v)
		if perr != nil {
			return nil, unprocessable("resolved_from", "resolved_from must be an RFC 3339 date-time")
		}
		f.From = &at
	}
	return f, nil
}

// incidentID reads the id of the path; one that is no UUID names no incident.
func incidentID(w http.ResponseWriter, r *http.Request) (string, bool) {
	return api.ParseUUIDParam(w, r, "id", "incident not found")
}

// writeIncidentError maps the errors of the incident routes.
func writeIncidentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrIncidentNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "incident not found")
	case errors.Is(err, ErrAlarmNotMember):
		api.WriteFieldError(w, unprocessable("alarm_id", "the alarm is no member of the incident"))
	case errors.Is(err, ErrIncidentResolved):
		api.WriteProblem(w, http.StatusConflict, ProblemInvalidTransition, "Conflict", "the incident is resolved")
	case errors.Is(err, ErrInvalidTransition):
		api.WriteProblem(w, http.StatusConflict, ProblemInvalidTransition, "Conflict",
			"the incident is acknowledged or resolved already")
	default:
		writeError(w, err)
	}
}

// views builds the list views of incidents: root CI names and symptom
// counts in the caller's scope.
func (h *Handler) views(ctx context.Context, org string, items []Incident) ([]IncidentView, error) {
	store := h.svc.Store()
	var ciIDs []string
	for i := range items {
		if c := items[i].RootCIID; c != nil && !slices.Contains(ciIDs, *c) {
			ciIDs = append(ciIDs, *c)
		}
	}
	names, err := store.CINames(ctx, org, ciIDs)
	if err != nil {
		return nil, err
	}
	tickets, err := h.incidentTickets(ctx, org, items)
	if err != nil {
		return nil, err
	}
	out := make([]IncidentView, 0, len(items))
	for i := range items {
		inc := items[i]
		v := IncidentView{Incident: inc, Root: IncidentRoot{AlarmID: inc.RootAlarmID, CIID: inc.RootCIID},
			Ticket: firstOpenTicket(tickets[inc.ID])}
		if inc.RootCIID != nil {
			if n, ok := names[*inc.RootCIID]; ok {
				name := n
				v.Root.CIName = &name
			}
		}
		// The symptoms are the visible members without the root, counted
		// directly: an invisible root must not lower the count.
		filter := &ListFilter{IncidentID: inc.ID, States: States}
		if inc.RootAlarmID != nil {
			filter.ExcludeID = *inc.RootAlarmID
		}
		count, cerr := store.Count(ctx, org, filter, 0)
		if cerr != nil {
			return nil, cerr
		}
		v.SymptomCount = count
		if v.Reasons == nil {
			v.Reasons = []string{}
		}
		if v.AffectedCounts == nil {
			v.AffectedCounts = map[string]int{}
		}
		out = append(out, v)
	}
	return out, nil
}

// incidentTickets reads the tickets linked to the incidents (link type
// incident) with one query; none without a ticket port.
func (h *Handler) incidentTickets(ctx context.Context, org string, items []Incident) (map[string][]TicketRef, error) {
	if h.tickets == nil || len(items) == 0 {
		return nil, nil
	}
	ids := make([]string, len(items))
	for i := range items {
		ids[i] = items[i].ID
	}
	return h.tickets.LinkedTickets(ctx, org, TicketLinkIncident, ids)
}

func (h *Handler) listIncidents(w http.ResponseWriter, r *http.Request) {
	org, ok := requireOrg(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	if page.CursorError != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", page.CursorError.Error())
		return
	}
	f, err := parseIncidentFilter(r)
	if err != nil {
		writeIncidentError(w, err)
		return
	}
	if page.Offset > 0 && page.Cursor == nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", errCursorOrder.Error())
		return
	}
	if page.Cursor != nil {
		at, perr := time.Parse(time.RFC3339Nano, page.Cursor.Value)
		if !page.Cursor.Matches(incidentListSort, incidentListDir) || perr != nil || !api.IsUUID(page.Cursor.ID) {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", errCursorOrder.Error())
			return
		}
		f.After = &IncidentCursor{OpenedAt: at, ID: page.Cursor.ID}
	}
	f.Limit = page.Limit
	var out api.ListResponse[IncidentView]
	err = h.svc.InTx(r.Context(), org, func(ctx context.Context) error {
		count, cerr := h.svc.Store().CountIncidents(ctx, org, f, api.TotalCountLimit)
		if cerr != nil {
			return cerr
		}
		res, lerr := h.svc.Store().ListIncidents(ctx, org, f)
		if lerr != nil {
			return lerr
		}
		views, verr := h.views(ctx, org, res.Items)
		if verr != nil {
			return verr
		}
		total, capped := api.CapTotal(count)
		out = api.ListResponse[IncidentView]{Data: views, Total: total, TotalCapped: capped, Limit: page.Limit,
			HasMore: res.Next != nil}
		if res.Next != nil {
			out.NextCursor = api.EncodeCursor(api.Cursor{Sort: incidentListSort, Dir: incidentListDir,
				Value: res.Next.OpenedAt.UTC().Format(time.RFC3339Nano), ID: res.Next.ID})
		}
		return nil
	})
	if err != nil {
		writeIncidentError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// incidentDetail reads the detail of an incident in the transaction of ctx.
func (h *Handler) incidentDetail(ctx context.Context, org, id string) (*IncidentDetailView, error) {
	store := h.svc.Store()
	inc, err := store.GetIncident(ctx, org, id)
	if err != nil {
		return nil, err
	}
	views, err := h.views(ctx, org, []Incident{*inc})
	if err != nil {
		return nil, err
	}
	out := &IncidentDetailView{IncidentView: views[0], Members: []IncidentMember{}}
	members, err := store.List(ctx, org, &ListFilter{IncidentID: id, States: States, Limit: maxMembers})
	if err != nil {
		return nil, err
	}
	// More members than listed: members_truncated (symptom_count counts
	// them all).
	out.MembersTruncated = members.Next != nil
	itrs, err := store.IncidentTransitions(ctx, org, id)
	if err != nil {
		return nil, err
	}
	timeline := make([]TimelineEntry, 0, len(itrs))
	for i := range itrs {
		tr := &itrs[i]
		details := tr.Details
		if details == nil {
			details = map[string]any{}
		}
		entry := TimelineEntry{Kind: "incident", FromState: tr.FromState, ToState: tr.ToState, At: tr.At,
			ActorID: tr.ActorID, Reason: tr.Reason, Details: details}
		if c, ok := details["comment"].(string); ok && c != "" {
			entry.Comment = &c
		}
		timeline = append(timeline, entry)
	}
	for i := range members.Items {
		a := &members.Items[i]
		role := RoleSymptom
		if inc.RootAlarmID != nil && *inc.RootAlarmID == a.ID {
			role = RoleRoot
		}
		out.Members = append(out.Members, IncidentMember{View: viewOf(a), Role: role})
		trs, terr := store.TransitionsUpTo(ctx, org, a.ID, MaxTimeline+1)
		if terr != nil {
			return nil, terr
		}
		for j := range trs {
			tr := &trs[j]
			alarmID := a.ID
			timeline = append(timeline, TimelineEntry{Kind: "alarm", AlarmID: &alarmID, FromState: tr.FromState,
				ToState: tr.ToState, At: tr.At, ActorID: tr.ActorID, Reason: tr.Reason, Comment: tr.Comment,
				Details: map[string]any{}})
		}
	}
	slices.SortStableFunc(timeline, func(a, b TimelineEntry) int { return a.At.Compare(b.At) })
	if len(timeline) > MaxTimeline {
		timeline, out.TimelineTruncated = timeline[:MaxTimeline], true
	}
	out.Timeline = timeline
	return out, nil
}

func (h *Handler) getIncident(w http.ResponseWriter, r *http.Request) {
	org, ok := requireOrg(w, r)
	if !ok {
		return
	}
	id, ok := incidentID(w, r)
	if !ok {
		return
	}
	var out *IncidentDetailView
	err := h.svc.InTx(r.Context(), org, func(ctx context.Context) error {
		var derr error
		out, derr = h.incidentDetail(ctx, org, id)
		return derr
	})
	if err != nil {
		writeIncidentError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// actorOf is the acting user of the request, nil for none.
func actorOf(r *http.Request) *string {
	if user := tenant.FromContext(r.Context()).UserID; api.IsUUID(user) {
		return &user
	}
	return nil
}

// emitIncidentUpdated appends incident.updated of inc in the transaction of
// ctx: the kind of the change, the root and the actor.
func (h *Handler) emitIncidentUpdated(ctx context.Context, org string, inc *Incident, change string, actor *string) error {
	if h.svc.sink == nil {
		return nil
	}
	severity := SeverityInfo
	if inc.RootAlarmID != nil {
		if a, err := h.svc.Store().Get(ctx, org, *inc.RootAlarmID); err == nil {
			severity = a.Severity
		}
	}
	return h.svc.sink.Append(ctx, &events.Event{OrgID: org, Type: events.IncidentUpdated, EntityID: inc.ID,
		Data: events.IncidentData{ID: inc.ID, IncidentID: inc.ID, RootCIID: deref(inc.RootCIID), ClientID: deref(inc.ClientID),
			SiteID: deref(inc.SiteID), Severity: severity, CauseClass: inc.CauseClass, Change: change,
			RootAlarmID: deref(inc.RootAlarmID), ActorID: deref(actor)}})
}

func (h *Handler) ackIncident(w http.ResponseWriter, r *http.Request) {
	org, ok := requireOrg(w, r)
	if !ok {
		return
	}
	id, ok := incidentID(w, r)
	if !ok {
		return
	}
	req, err := readAction(r)
	if err != nil {
		writeError(w, err)
		return
	}
	actor := actorOf(r)
	var out *IncidentDetailView
	err = h.svc.InTx(r.Context(), org, func(ctx context.Context) error {
		// The comment stays with the transition of the incident, also when
		// no open root alarm takes it.
		inc, terr := h.svc.Store().AcknowledgeIncident(ctx, org, id, actor, req.Comment)
		if terr != nil {
			return terr
		}
		// The root alarm is acknowledged with the incident when it is open.
		if inc.RootAlarmID != nil {
			root, gerr := h.svc.Store().Get(ctx, org, *inc.RootAlarmID)
			if gerr != nil && !errors.Is(gerr, ErrNotFound) {
				return gerr
			}
			if gerr == nil && root.State == StateOpen {
				if _, aerr := h.svc.Acknowledge(ctx, org, root.ID, deref(actor), req.Comment); aerr != nil {
					return aerr
				}
			}
		}
		if eerr := h.emitIncidentUpdated(ctx, org, inc, IncidentChangeAcknowledged, actor); eerr != nil {
			return eerr
		}
		var derr error
		out, derr = h.incidentDetail(ctx, org, id)
		return derr
	})
	if err != nil {
		writeIncidentError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// RootRequest is the body of POST /incidents/{id}/root.
type RootRequest struct {
	AlarmID string `json:"alarm_id"`
	Reason  string `json:"reason"`
}

// maxRootReason bounds the reason of a manual root.
const maxRootReason = 1000

func (h *Handler) setIncidentRoot(w http.ResponseWriter, r *http.Request) {
	org, ok := requireOrg(w, r)
	if !ok {
		return
	}
	id, ok := incidentID(w, r)
	if !ok {
		return
	}
	var req RootRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "request body must be a JSON object with alarm_id and reason")
		return
	}
	req.Reason = strings.TrimSpace(req.Reason)
	switch {
	case !api.IsUUID(req.AlarmID):
		api.WriteFieldError(w, unprocessable("alarm_id", "alarm_id must be the UUID of an alarm of the incident"))
		return
	case req.Reason == "":
		api.WriteFieldError(w, unprocessable("reason", "reason is required"))
		return
	case len([]rune(req.Reason)) > maxRootReason:
		api.WriteFieldError(w, unprocessable("reason", "reason must be at most 1000 characters"))
		return
	}
	actor := actorOf(r)
	var out *IncidentDetailView
	err := h.svc.InTx(r.Context(), org, func(ctx context.Context) error {
		inc, serr := h.svc.SetRootManual(ctx, org, id, strings.ToLower(req.AlarmID), actor, req.Reason)
		if serr != nil {
			return serr
		}
		if eerr := h.emitIncidentUpdated(ctx, org, inc, IncidentChangeRootChanged, actor); eerr != nil {
			return eerr
		}
		var derr error
		out, derr = h.incidentDetail(ctx, org, id)
		return derr
	})
	if err != nil {
		writeIncidentError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// linkIncidentTicket links an existing ticket to the incident (WP-1039,
// ALR-09 d): ticket_link of type incident and an internal system comment
// naming the incident, its root and its cause class, in one transaction.
// A ticket linked already is 409, an invisible ticket or incident 404;
// without ticketing_essential 403 feature-not-entitled (Konvention 12).
// The route needs ticket:write (routeRules); the answer is the incident
// detail, so the handler also needs monitoring:read (pattern
// review-items/*/ticket with review:resolve); without it the incident is
// not found (404, no existence oracle).
func (h *Handler) linkIncidentTicket(w http.ResponseWriter, r *http.Request) {
	org, ok := requireOrg(w, r)
	if !ok {
		return
	}
	// Without monitoring:read the caller reads no incident: 404 like an
	// invisible one, so ticket:write alone tells no incident apart.
	if p, found := identity.PrincipalFromContext(r.Context()); !found || !p.Has(identity.PermMonitoringRead) {
		api.WriteError(w, http.StatusNotFound, "Not Found", "incident not found")
		return
	}
	id, ok := incidentID(w, r)
	if !ok {
		return
	}
	ticketID, ok := api.ParseUUIDParam(w, r, "ticket_id", "ticket not found")
	if !ok {
		return
	}
	if h.features != nil && !h.features.IsEnabled(r.Context(), org, entitlement.FeatureTicketingEssential) {
		api.WriteEntitlementError(w, &entitlement.FeatureNotEntitledError{FeatureKey: entitlement.FeatureTicketingEssential})
		return
	}
	if h.tickets == nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "ticket not found")
		return
	}
	var out *IncidentDetailView
	err := h.svc.InTx(r.Context(), org, func(ctx context.Context) error {
		inc, gerr := h.svc.Store().GetIncident(ctx, org, id)
		if gerr != nil {
			return gerr
		}
		var rootName string
		if inc.RootCIID != nil {
			names, nerr := h.svc.Store().CINames(ctx, org, []string{*inc.RootCIID})
			if nerr != nil {
				return nerr
			}
			rootName = names[*inc.RootCIID]
		}
		if lerr := h.tickets.LinkIncident(ctx, org, ticketID, incidentTicketLink(inc, rootName)); lerr != nil {
			return lerr
		}
		var derr error
		out, derr = h.incidentDetail(ctx, org, id)
		return derr
	})
	switch {
	case errors.Is(err, ErrTicketNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "ticket not found")
	case errors.Is(err, ErrTicketLinked):
		api.WriteError(w, http.StatusConflict, "Conflict", ErrTicketLinked.Error())
	case err != nil:
		writeIncidentError(w, err)
	default:
		api.WriteJSON(w, http.StatusCreated, out)
	}
}

// IncidentTicketRequest is the body of POST
// /monitoring/incidents/{id}/ticket (WP-1040): every field is optional,
// title and description default from the incident (root, cause class,
// affected CIs), priority from its severity.
type IncidentTicketRequest struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Priority    string `json:"priority"`
	TeamID      string `json:"team_id"`
	AssigneeID  string `json:"assignee_id"`
}

// IncidentTicketView is the answer of POST /monitoring/incidents/{id}/ticket.
type IncidentTicketView struct {
	TicketID     string `json:"ticket_id"`
	TicketNumber int    `json:"ticket_number"`
	IncidentID   string `json:"incident_id"`
}

// incidentTicketConflict is the 409 of a second ticket for an incident:
// the problem names the open ticket.
type incidentTicketConflict struct {
	Type         string `json:"type"`
	Title        string `json:"title"`
	Status       int    `json:"status"`
	Detail       string `json:"detail"`
	TicketID     string `json:"ticket_id"`
	TicketNumber int    `json:"ticket_number"`
}

// WithTicketWriter sets the writing ticket side of the incidents (manual
// ticket per incident, WP-1040).
func (h *Handler) WithTicketWriter(w TicketWriter) *Handler {
	h.writer = w
	return h
}

// createIncidentTicket handles POST /monitoring/incidents/{id}/ticket: a
// ticket for the incident with the caller as reporter (ticket:write by
// the route rule, the incident read with monitoring:read), 403
// feature-not-entitled without ticketing_essential, 409 naming the open
// ticket of the incident.
func (h *Handler) createIncidentTicket(w http.ResponseWriter, r *http.Request) {
	org, ok := requireOrg(w, r)
	if !ok {
		return
	}
	p, found := identity.PrincipalFromContext(r.Context())
	if !found || !p.Has(identity.PermMonitoringRead) {
		api.WriteError(w, http.StatusNotFound, "Not Found", "incident not found")
		return
	}
	id, ok := incidentID(w, r)
	if !ok {
		return
	}
	var req IncidentTicketRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "invalid ticket request")
			return
		}
	}
	if err := req.validate(); err != nil {
		api.WriteFieldError(w, err)
		return
	}
	if h.features != nil && !h.features.IsEnabled(r.Context(), org, entitlement.FeatureTicketingEssential) {
		api.WriteEntitlementError(w, &entitlement.FeatureNotEntitledError{FeatureKey: entitlement.FeatureTicketingEssential})
		return
	}
	if h.writer == nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "incident not found")
		return
	}
	var out *TicketRef
	err := h.svc.InTx(r.Context(), org, func(ctx context.Context) error {
		inc, gerr := h.svc.Store().GetIncident(ctx, org, id)
		if gerr != nil {
			return gerr
		}
		var rootName string
		if inc.RootCIID != nil {
			names, nerr := h.svc.Store().CINames(ctx, org, []string{*inc.RootCIID})
			if nerr != nil {
				return nerr
			}
			rootName = names[*inc.RootCIID]
		}
		severity := SeverityWarning
		if inc.RootAlarmID != nil {
			if a, aerr := h.svc.Store().Get(ctx, org, *inc.RootAlarmID); aerr == nil {
				severity = a.Severity
			}
		}
		target := TicketTarget{Type: TicketLinkIncident, ID: inc.ID}
		treq := &TicketRequest{Title: req.Title, Description: req.Description, Priority: ticketPriority(severity, req.Priority),
			ReporterID: p.Subject, TeamID: req.TeamID, AssigneeID: req.AssigneeID, Exclusive: &target, Origin: &target,
			CheckFieldRights: true, RequestFields: req.fields()}
		if treq.Title == "" {
			treq.Title = incidentTicketTitle(inc, rootName)
		}
		if treq.Description == "" {
			treq.Description = incidentTicketDescription(inc, rootName)
		}
		if inc.RootCIID != nil {
			treq.RelatedCIID = *inc.RootCIID
			treq.Links = append(treq.Links, TicketTarget{Type: TicketLinkCI, ID: *inc.RootCIID})
		}
		if inc.RootAlarmID != nil {
			treq.Links = append(treq.Links, TicketTarget{Type: TicketLinkAlarm, ID: *inc.RootAlarmID})
		}
		var cerr error
		out, cerr = h.writer.CreateTicket(ctx, org, treq)
		return cerr
	})
	var exists *TicketExistsError
	switch {
	case errors.As(err, &exists):
		api.WriteProblemBody(w, http.StatusConflict, incidentTicketConflict{Type: "about:blank", Title: "Conflict", Status: http.StatusConflict,
			Detail:   fmt.Sprintf("an open ticket is already linked to the incident: #%d (%s)", exists.TicketNumber, exists.TicketID),
			TicketID: exists.TicketID, TicketNumber: exists.TicketNumber})
	case api.WriteFieldError(w, err):
	case err != nil:
		writeIncidentError(w, err)
	default:
		api.WriteJSON(w, http.StatusCreated, IncidentTicketView{TicketID: out.TicketID, TicketNumber: out.TicketNumber, IncidentID: id})
	}
}

// fields are the ticket fields the request body set, for the field rights
// (like reviews.TicketRequest).
func (r *IncidentTicketRequest) fields() []string {
	var out []string
	for _, f := range []struct{ name, value string }{
		{"priority", r.Priority}, {"assignee_id", r.AssigneeID}, {"team_id", r.TeamID},
	} {
		if f.value != "" {
			out = append(out, f.name)
		}
	}
	return out
}

// validate checks the fields of a manual incident ticket (422 on field).
func (r *IncidentTicketRequest) validate() error {
	switch {
	case r.Priority != "" && !slices.Contains(events.TicketPriorities, r.Priority):
		return &api.FieldError{Field: "priority", Status: http.StatusUnprocessableEntity, Message: "must be one of low, medium, high, critical"}
	case utf8.RuneCountInString(r.Title) > 200:
		return &api.FieldError{Field: "title", Status: http.StatusUnprocessableEntity, Message: "must be at most 200 characters"}
	case utf8.RuneCountInString(r.Description) > 10000:
		return &api.FieldError{Field: "description", Status: http.StatusUnprocessableEntity, Message: "must be at most 10000 characters"}
	case r.TeamID != "" && !api.IsUUID(r.TeamID):
		return &api.FieldError{Field: "team_id", Status: http.StatusUnprocessableEntity, Message: "must be a team id"}
	case r.AssigneeID != "" && !api.IsUUID(r.AssigneeID):
		return &api.FieldError{Field: "assignee_id", Status: http.StatusUnprocessableEntity, Message: "must be a user id"}
	}
	for field, v := range map[string]string{"title": r.Title, "description": r.Description} {
		if err := api.RejectNUL(field, v); err != nil {
			return err
		}
	}
	return nil
}

// incidentTicketTitle is the default title of an incident ticket.
func incidentTicketTitle(inc *Incident, rootName string) string {
	root := rootName
	if root == "" && inc.RootCIID != nil {
		root = *inc.RootCIID
	}
	title := "Monitoring incident: " + causeOrUnknown(inc.CauseClass)
	if root != "" {
		title += " at " + root
	}
	return truncateRunes(title, 200)
}

// incidentTicketDescription is the default description: root, cause
// class and the number of affected CIs.
func incidentTicketDescription(inc *Incident, rootName string) string {
	affected := 0
	for _, n := range inc.AffectedCounts {
		affected += n
	}
	var b strings.Builder
	b.WriteString("Monitoring incident " + inc.ID + ", cause class " + causeOrUnknown(inc.CauseClass))
	switch {
	case rootName != "" && inc.RootCIID != nil:
		b.WriteString(", root " + rootName + " (" + *inc.RootCIID + ")")
	case inc.RootCIID != nil:
		b.WriteString(", root CI " + *inc.RootCIID)
	}
	fmt.Fprintf(&b, ", %d affected CIs.", affected)
	return b.String()
}
