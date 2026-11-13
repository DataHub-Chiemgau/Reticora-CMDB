package alarm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// REST of the alarms (ALR-01, MON-04, WP-983, E-160):
//
//	GET  /api/v1/monitoring/alarms             list (filters state, severity, ci_id, client_id, site_id,
//	                                           source_kind, incident_id, suppressed, from/to on opened_at)
//	GET  /api/v1/monitoring/alarms/{id}        the alarm with its transitions
//	POST /api/v1/monitoring/alarms/{id}/ack    acknowledge (alert:acknowledge)
//	POST /api/v1/monitoring/alarms/{id}/unack  withdraw the acknowledgement (alert:acknowledge)
//
// The routes read in the caller's scope (row level security of alarm and
// alarm_transition): a foreign or invisible alarm is 404. Ack and Un-Ack are
// user actions with audit: on PostgreSQL the handler writes the entry
// alarm.acknowledged or alarm.unacknowledged (resource_type alarm) in the
// transaction of the change, before the event of the lifecycle, which stays
// the last write (Konvention 20).

// Paths of the alarm routes.
const (
	alarmsPath = "/api/v1/monitoring/alarms"
	alarmPath  = alarmsPath + "/{id}"
)

// MaxTransitions is how many transitions the detail of an alarm holds;
// beyond it transitions_truncated is true.
const MaxTransitions = 500

// Audit actions of the user actions (ALR-01).
const (
	AuditAcknowledged   = "alarm.acknowledged"
	AuditUnacknowledged = "alarm.unacknowledged"
	// AuditResourceType is the resource type of the entries; its scope
	// rule is the table alarm (audit/scope.go).
	AuditResourceType = "alarm"
)

// listOrder is the sort of the list and its cursor: newest first on
// opened_at, a pending alarm on its first_seen_at (Alarm.SortAt).
const (
	listSort = "opened_at"
	listDir  = "desc"
)

// ProblemInvalidTransition is the problem type of a refused state change
// (409).
const ProblemInvalidTransition = "urn:reticora:problem:invalid-transition"

// DetailKeys are the members of details an answer carries (Konvention 11):
// the measured value, the threshold and the reference to the rule or probe.
// Every other member, CI attribute values included, is left out.
var DetailKeys = []string{
	"value", "threshold", "clear_threshold", "condition", "operator", "unit", "level",
	"metric_name", "aggregation", "window", "duration",
	"rule_id", "rule_name", "probe_id", "probe_type", "check", "status", "rtt_ms",
	"collector_id", "collector_group_id",
}

// View is an alarm as the list and the user actions answer it.
type View struct {
	Alarm
}

// DetailView is the detail of an alarm: the alarm with its transitions
// (always present, also when none is visible).
type DetailView struct {
	Alarm
	Transitions          []Transition `json:"transitions"`
	TransitionsTruncated bool         `json:"transitions_truncated"`
}

// viewOf filters an alarm for an answer: details keeps only DetailKeys.
func viewOf(a *Alarm) View {
	out := *a
	out.Details = map[string]any{}
	for k, v := range a.Details {
		if slices.Contains(DetailKeys, k) {
			out.Details[k] = v
		}
	}
	if out.Labels == nil {
		out.Labels = map[string]string{}
	}
	return View{Alarm: out}
}

// Handler serves the alarm routes on the lifecycle service.
type Handler struct {
	svc *Service
}

// NewHandler returns the handler on svc. With a database the user actions
// write their audit entry in the transaction of the change.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// RegisterRoutes mounts the routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get(alarmsPath, h.list)
	r.Get(alarmPath, h.get)
	r.Post(alarmPath+"/ack", h.ack)
	r.Post(alarmPath+"/unack", h.unack)
	r.Get(settingsPath, h.getSettings)
	r.Put(settingsPath, h.putSettings)
}

// unprocessable is the 422 of a field.
func unprocessable(field, message string) *api.FieldError {
	return &api.FieldError{Field: field, Message: message, Status: http.StatusUnprocessableEntity}
}

// errCursorOrder is the 400 of a cursor issued for another list.
// validation: a fixed text wrapping api.ErrInvalidCursor (API-03).
var errCursorOrder = fmt.Errorf("%w: not issued for this list", api.ErrInvalidCursor)

// writeError maps the errors of the service.
func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "alarm not found")
	case api.WriteFieldError(w, err):
	case errors.Is(err, ErrInvalidTransitionInput):
		api.WriteFieldError(w, unprocessable("comment", "comment must be at most 1000 characters"))
	default:
		api.WriteRepoError(w, err)
	}
}

// requireOrg returns the organization of the request; without one it
// answers 401.
func requireOrg(w http.ResponseWriter, r *http.Request) (string, bool) {
	org := tenant.FromContext(r.Context()).OrganizationID
	if org == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return "", false
	}
	return org, true
}

// multi reads a repeatable query parameter (also comma-separated) and
// checks every value against allowed.
func multi(q map[string][]string, name string, allowed []string) ([]string, error) {
	var out []string
	for _, raw := range q[name] {
		for _, v := range strings.Split(raw, ",") {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			if !slices.Contains(allowed, v) {
				return nil, unprocessable(name, name+" must be one of "+strings.Join(allowed, ", "))
			}
			if !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
	}
	return out, nil
}

// parseFilter reads the filters of the list.
func parseFilter(r *http.Request) (*ListFilter, error) {
	q := r.URL.Query()
	f := &ListFilter{}
	var err error
	if f.States, err = multi(q, "state", States); err != nil {
		return nil, err
	}
	if len(f.States) == 0 {
		// Without a state the list holds every alarm that is not resolved.
		f.States = []string{StatePending, StateOpen, StateAcknowledged}
	}
	if f.Severities, err = multi(q, "severity", Severities); err != nil {
		return nil, err
	}
	if f.SourceKinds, err = multi(q, "source_kind", SourceKinds); err != nil {
		return nil, err
	}
	for name, dst := range map[string]*string{"ci_id": &f.CIID, "client_id": &f.ClientID, "site_id": &f.SiteID,
		"incident_id": &f.IncidentID} {
		if v := q.Get(name); v != "" {
			if !api.IsUUID(v) {
				return nil, unprocessable(name, name+" must be a UUID")
			}
			*dst = v
		}
	}
	if v := q.Get("suppressed"); v != "" {
		b, perr := strconv.ParseBool(v)
		if perr != nil {
			return nil, unprocessable("suppressed", "suppressed must be true or false")
		}
		f.Suppressed = &b
	}
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
	return f, nil
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	org, ok := requireOrg(w, r)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	if page.CursorError != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", page.CursorError.Error())
		return
	}
	f, err := parseFilter(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if page.Offset > 0 && page.Cursor == nil {
		// The list pages by keyset only; an offset cursor was not issued
		// for it.
		api.WriteError(w, http.StatusBadRequest, "Bad Request", errCursorOrder.Error())
		return
	}
	if page.Cursor != nil {
		at, perr := time.Parse(time.RFC3339Nano, page.Cursor.Value)
		if !page.Cursor.Matches(listSort, listDir) || perr != nil || !api.IsUUID(page.Cursor.ID) {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", errCursorOrder.Error())
			return
		}
		f.After = &Cursor{At: at, ID: page.Cursor.ID}
	}
	f.Limit = page.Limit
	store := h.svc.Store()
	count, err := store.Count(r.Context(), org, f, api.TotalCountLimit)
	if err != nil {
		writeError(w, err)
		return
	}
	res, err := store.List(r.Context(), org, f)
	if err != nil {
		writeError(w, err)
		return
	}
	total, capped := api.CapTotal(count)
	out := api.ListResponse[View]{Data: make([]View, 0, len(res.Items)), Total: total, TotalCapped: capped, Limit: page.Limit,
		HasMore: res.Next != nil}
	for i := range res.Items {
		out.Data = append(out.Data, viewOf(&res.Items[i]))
	}
	if res.Next != nil {
		out.NextCursor = api.EncodeCursor(api.Cursor{Sort: listSort, Dir: listDir,
			Value: res.Next.At.UTC().Format(time.RFC3339Nano), ID: res.Next.ID})
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// alarmID reads the id of the path; one that is no UUID names no alarm.
func alarmID(w http.ResponseWriter, r *http.Request) (string, bool) {
	return api.ParseUUIDParam(w, r, "id", "alarm not found")
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	org, ok := requireOrg(w, r)
	if !ok {
		return
	}
	id, ok := alarmID(w, r)
	if !ok {
		return
	}
	var view DetailView
	err := h.svc.InTx(r.Context(), org, func(ctx context.Context) error {
		a, err := h.svc.Store().Get(ctx, org, id)
		if err != nil {
			return err
		}
		// One more than the detail holds tells whether it is truncated.
		trs, err := h.svc.Store().TransitionsUpTo(ctx, org, id, MaxTransitions+1)
		if err != nil {
			return err
		}
		view.Alarm = viewOf(a).Alarm
		view.TransitionsTruncated = len(trs) > MaxTransitions
		if view.TransitionsTruncated {
			trs = trs[:MaxTransitions]
		}
		view.Transitions = api.NonNilSlice(trs)
		return nil
	})
	if err != nil {
		writeError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, view)
}

// ActionRequest is the body of ack and unack.
type ActionRequest struct {
	Comment string `json:"comment"`
}

// readAction reads the optional body of ack and unack.
func readAction(r *http.Request) (*ActionRequest, error) {
	var req ActionRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		return nil, &api.FieldError{Field: "body", Message: "invalid request body", Status: http.StatusBadRequest}
	}
	req.Comment = strings.TrimSpace(req.Comment)
	if len([]rune(req.Comment)) > CommentMaxLen {
		return nil, unprocessable("comment", "comment must be at most 1000 characters")
	}
	return &req, nil
}

func (h *Handler) ack(w http.ResponseWriter, r *http.Request) {
	h.action(w, r, AuditAcknowledged, StateOpen, h.svc.Acknowledge)
}

func (h *Handler) unack(w http.ResponseWriter, r *http.Request) {
	h.action(w, r, AuditUnacknowledged, StateAcknowledged, h.svc.Unacknowledge)
}

// action runs a user action (ack, unack) and writes its audit entry in the
// transaction of the change; from is the state the action leaves.
func (h *Handler) action(w http.ResponseWriter, r *http.Request, auditAction, from string,
	run func(ctx context.Context, orgID, id, actor, comment string) (*Alarm, error)) {
	org, ok := requireOrg(w, r)
	if !ok {
		return
	}
	id, ok := alarmID(w, r)
	if !ok {
		return
	}
	req, err := readAction(r)
	if err != nil {
		writeError(w, err)
		return
	}
	actor := ""
	if user := tenant.FromContext(r.Context()).UserID; api.IsUUID(user) {
		actor = user
	}
	var out *Alarm
	err = h.svc.InTx(r.Context(), org, func(ctx context.Context) error {
		cur, gerr := h.svc.Store().LockForUpdate(ctx, org, id)
		if gerr != nil {
			return gerr
		}
		if !acknowledgeCovers(ctx, cur) {
			return errAckOutOfScope
		}
		a, rerr := run(ctx, org, id, actor, req.Comment)
		if rerr != nil {
			return rerr
		}
		out = a
		return h.audit(ctx, org, auditAction, from, a, req.Comment != "")
	})
	switch {
	case errors.Is(err, errAckOutOfScope):
		api.WriteError(w, http.StatusForbidden, "Forbidden", "alert:acknowledge does not cover the client and site of the alarm")
		return
	case errors.Is(err, ErrInvalidTransition):
		current := ""
		if a, gerr := h.svc.Store().Get(r.Context(), org, id); gerr == nil {
			current = a.State
		}
		detail := "the alarm cannot be " + strings.TrimPrefix(auditAction, "alarm.")
		if current != "" {
			detail += " in state " + current
		}
		api.WriteProblem(w, http.StatusConflict, ProblemInvalidTransition, "Conflict", detail)
		return
	case err != nil:
		writeError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, viewOf(out))
}

// errAckOutOfScope refuses an ack or un-ack of an alarm the caller reads but
// whose client or site the scope of alert:acknowledge does not cover (403).
var errAckOutOfScope = errors.New("alarm: alert:acknowledge out of scope")

// acknowledgeCovers reports whether the scope the caller holds
// alert:acknowledge in covers the client and site of the alarm. The route
// only checks that the permission is held; the read scope of the session
// (row level security) may be wider than the scope of a role that grants
// alert:acknowledge for some clients or sites only (identity.ResolveAccess,
// Principal.PermissionScopes). An empty client or site of the alarm needs
// that dimension organization-wide (fail-closed, like permission.Covers).
// Without a principal (system) or without a resolved scope and without a
// permission-specific scope the read scope of the request decides.
func acknowledgeCovers(ctx context.Context, a *Alarm) bool {
	p, ok := identity.PrincipalFromContext(ctx)
	if !ok {
		return true
	}
	var scope identity.Scope
	switch s, restricted := p.PermissionScopes[identity.PermAlertAcknowledge]; {
	case restricted:
		scope = s
	case p.Scope == nil:
		return true
	default:
		held := false
		if scope, held = p.ScopeFor(identity.PermAlertAcknowledge); !held {
			return false
		}
	}
	in := func(set identity.ScopeSet, id *string) bool {
		return set.All || (id != nil && *id != "" && slices.Contains(set.IDs, strings.ToLower(*id)))
	}
	return in(scope.Clients, a.ClientID) && in(scope.Sites, a.SiteID)
}

// audit writes the entry of a user action in the transaction of ctx; the
// memory profile has no audit trail (the route audit records the request).
func (h *Handler) audit(ctx context.Context, orgID, action, from string, a *Alarm, commented bool) error {
	if h.svc.pool == nil {
		return nil
	}
	actorID, actorType := "", "system"
	if p, ok := identity.PrincipalFromContext(ctx); ok {
		actorType = "user"
		if p.Type == identity.PrincipalTypeAPIKey {
			actorType = "api_key"
		}
		if audit.IsUUID(p.Subject) {
			actorID = p.Subject
		}
	}
	return database.WithRequestTenant(ctx, h.svc.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{
			OrganizationID: orgID, ActorID: actorID, ActorType: actorType,
			Action: action, ResourceType: AuditResourceType, ResourceID: a.ID,
			Changes: map[string]any{
				"state":     map[string]any{"old": from, "new": a.State},
				"commented": commented,
			},
		})
		return err
	})
}
