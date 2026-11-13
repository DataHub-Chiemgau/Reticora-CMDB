package alarm_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
)

// The REST routes of the incidents (ALR-03, ALR-10, WP-1036) on the
// MemoryStore, behind the permission of each route as the server wires it.
// The PostgreSQL cases are in incident_handler_integration_test.go.

var incidentRoutes = []struct{ method, pattern string }{
	{http.MethodGet, "/api/v1/monitoring/incidents"},
	{http.MethodGet, "/api/v1/monitoring/incidents/{id}"},
	{http.MethodPost, "/api/v1/monitoring/incidents/{id}/ack"},
	{http.MethodPost, "/api/v1/monitoring/incidents/{id}/root"},
}

// incidentMux mounts the incident routes on svc behind their permissions.
func incidentMux(svc *alarm.Service) http.Handler {
	inner := chi.NewRouter()
	alarm.NewHandler(svc).RegisterRoutes(inner)
	mux := chi.NewRouter()
	for _, rt := range incidentRoutes {
		mux.With(server.AuthorizeRoute(rt.method, rt.pattern)).Method(rt.method, rt.pattern, inner)
	}
	return mux
}

var incidentOperator = []identity.Permission{identity.PermMonitoringRead, identity.PermMonitoringWrite,
	identity.PermAlertAcknowledge}

// memIncidentREST is a memory service with an incident of a root and two
// symptoms.
type memIncidentREST struct {
	svc  *alarm.Service
	sink *alarm.MemorySink
	mux  http.Handler
	env  *incidentEnv
	inc  *alarm.Incident
	ids  []string
}

func newMemIncidentREST(t *testing.T) *memIncidentREST {
	t.Helper()
	store := alarm.NewMemoryStore()
	sink := &alarm.MemorySink{}
	svc := alarm.NewService(store, nil, sink)
	e := memIncidentEnv()
	e.store = store
	e.orgID = memOrg
	// Raised through the lifecycle, so the alarms have transitions.
	var ids []string
	for i := range 3 {
		ci := e.ci("inc-" + string(rune('a'+i)))
		a, err := svc.Raise(e.ctx, memOrg, &alarm.RaiseInput{SourceKind: alarm.SourceCheck, SourceID: ruleID,
			SeriesKey: "inc-" + string(rune('a'+i)), CIID: &ci, Severity: alarm.SeverityCritical, Summary: "down"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, a.ID)
	}
	inc := newIncident(t, e, ids)
	sink.Reset()
	return &memIncidentREST{svc: svc, sink: sink, mux: incidentMux(svc), env: e, inc: inc, ids: ids}
}

func (m *memIncidentREST) call(t *testing.T, perms []identity.Permission, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return call(t, m.mux, memOrg, perms, method, path, body)
}

type incidentDetail struct {
	ID          string `json:"id"`
	State       string `json:"state"`
	RootAlarmID string `json:"root_alarm_id"`
	ManualRoot  bool   `json:"manual_root"`
	Root        struct {
		AlarmID string `json:"alarm_id"`
	} `json:"root"`
	SymptomCount int `json:"symptom_count"`
	Members      []struct {
		ID    string `json:"id"`
		Role  string `json:"role"`
		State string `json:"state"`
	} `json:"members"`
	Timeline []struct {
		Kind    string    `json:"kind"`
		AlarmID *string   `json:"alarm_id"`
		At      time.Time `json:"at"`
		Reason  string    `json:"reason"`
		ActorID *string   `json:"actor_id"`
		Comment *string   `json:"comment"`
		Details map[string]any
	} `json:"timeline"`
	TimelineTruncated bool `json:"timeline_truncated"`
	MembersTruncated  bool `json:"members_truncated"`
}

func decodeIncident(t *testing.T, rec *httptest.ResponseRecorder) incidentDetail {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("incident: %d %s", rec.Code, rec.Body.String())
	}
	var d incidentDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestIncidentListFilters(t *testing.T) {
	m := newMemIncidentREST(t)
	// A second incident of another cause, resolved.
	other, _, err := m.env.store.UpsertIncident(m.env.ctx, memOrg, &alarm.IncidentInput{CauseClass: alarm.CauseCollectorUnreachable})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.env.store.TransitionIncident(m.env.ctx, memOrg, other.ID, alarm.IncidentResolved, nil); err != nil {
		t.Fatal(err)
	}
	ids := func(path string) []string {
		t.Helper()
		rec := m.call(t, incidentOperator, http.MethodGet, path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		var body struct {
			Data []struct {
				ID           string `json:"id"`
				SymptomCount int    `json:"symptom_count"`
			} `json:"data"`
			Total int `json:"total"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, d := range body.Data {
			out = append(out, d.ID)
			if d.ID == m.inc.ID && d.SymptomCount != 2 {
				t.Errorf("symptom_count %d, want 2", d.SymptomCount)
			}
		}
		if body.Total != len(out) {
			t.Errorf("%s: total %d, %d items", path, body.Total, len(out))
		}
		return out
	}
	base := "/api/v1/monitoring/incidents"
	if got := ids(base); len(got) != 2 {
		t.Errorf("all incidents: %v", got)
	}
	if got := ids(base + "?state=open"); len(got) != 1 || got[0] != m.inc.ID {
		t.Errorf("state=open: %v", got)
	}
	if got := ids(base + "?cause_class=collector_unreachable"); len(got) != 1 || got[0] != other.ID {
		t.Errorf("cause_class: %v", got)
	}
	if got := ids(base + "?state=open,resolved&cause_class=topology"); len(got) != 1 || got[0] != m.inc.ID {
		t.Errorf("state and cause_class: %v", got)
	}
	// from and to apply to opened_at (AK1): an incident opened before from
	// that is still open is not listed.
	later := url.QueryEscape(time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	if got := ids(base + "?from=" + later); len(got) != 0 {
		t.Errorf("from after every opening: %v, want none (not the open incident opened before)", got)
	}
	if got := ids(base + "?state=open&to=" + later); len(got) != 1 || got[0] != m.inc.ID {
		t.Errorf("to after the opening: %v", got)
	}
	earlier := url.QueryEscape(m.inc.OpenedAt.Add(-time.Hour).UTC().Format(time.RFC3339))
	if got := ids(base + "?to=" + earlier); len(got) != 0 {
		t.Errorf("to before every opening: %v, want none", got)
	}
	for _, q := range []string{"?state=closed", "?cause_class=x", "?client_id=nope", "?from=yesterday",
		"?from=2026-01-02T00:00:00Z&to=2026-01-01T00:00:00Z"} {
		if rec := m.call(t, incidentOperator, http.MethodGet, base+q, ""); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d, want 422", q, rec.Code)
		}
	}
	// Paging over the signed cursor.
	rec := m.call(t, incidentOperator, http.MethodGet, base+"?limit=1", "")
	var page struct {
		Data       []json.RawMessage `json:"data"`
		HasMore    bool              `json:"has_more"`
		NextCursor string            `json:"next_cursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Data) != 1 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("page 1: %s", rec.Body.String())
	}
	rec = m.call(t, incidentOperator, http.MethodGet, base+"?limit=1&cursor="+page.NextCursor, "")
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Data) != 1 || page.HasMore {
		t.Errorf("page 2: %s", rec.Body.String())
	}
}

func TestIncidentDetailTimelineOrderAndLimit(t *testing.T) {
	m := newMemIncidentREST(t)
	d := decodeIncident(t, m.call(t, incidentOperator, http.MethodGet, "/api/v1/monitoring/incidents/"+m.inc.ID, ""))
	if d.Root.AlarmID != m.ids[0] || len(d.Members) != 3 || d.SymptomCount != 2 {
		t.Fatalf("detail %+v", d)
	}
	roles := map[string]string{}
	for _, mem := range d.Members {
		roles[mem.ID] = mem.Role
	}
	if roles[m.ids[0]] != alarm.RoleRoot || roles[m.ids[1]] != alarm.RoleSymptom || roles[m.ids[2]] != alarm.RoleSymptom {
		t.Errorf("roles %v", roles)
	}
	var kinds = map[string]int{}
	for i, e := range d.Timeline {
		kinds[e.Kind]++
		if i > 0 && e.At.Before(d.Timeline[i-1].At) {
			t.Fatalf("timeline not in time order at %d", i)
		}
	}
	if kinds["incident"] == 0 || kinds["alarm"] != 3 || d.TimelineTruncated {
		t.Errorf("timeline kinds %v truncated %v", kinds, d.TimelineTruncated)
	}
	// Over 500 entries the timeline is cut and marked.
	for i := range 260 {
		if _, err := m.svc.Acknowledge(m.env.ctx, memOrg, m.ids[1], memUser, ""); err != nil {
			t.Fatalf("ack %d: %v", i, err)
		}
		if _, err := m.svc.Unacknowledge(m.env.ctx, memOrg, m.ids[1], memUser, ""); err != nil {
			t.Fatal(err)
		}
	}
	d = decodeIncident(t, m.call(t, incidentOperator, http.MethodGet, "/api/v1/monitoring/incidents/"+m.inc.ID, ""))
	if len(d.Timeline) != alarm.MaxTimeline || !d.TimelineTruncated {
		t.Errorf("timeline of %d entries, truncated %v; want %d, true", len(d.Timeline), d.TimelineTruncated, alarm.MaxTimeline)
	}
	// An unknown incident is 404, also one of another organization.
	if rec := m.call(t, incidentOperator, http.MethodGet, "/api/v1/monitoring/incidents/"+ruleID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown incident: %d", rec.Code)
	}
	if rec := call(t, m.mux, otherOrg, incidentOperator, http.MethodGet, "/api/v1/monitoring/incidents/"+m.inc.ID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("incident of another organization: %d", rec.Code)
	}
}

func TestIncidentAckTwiceConflict(t *testing.T) {
	m := newMemIncidentREST(t)
	path := "/api/v1/monitoring/incidents/" + m.inc.ID + "/ack"
	d := decodeIncident(t, m.call(t, incidentOperator, http.MethodPost, path, `{"comment":"on it"}`))
	if d.State != alarm.IncidentAcknowledged {
		t.Fatalf("state %s", d.State)
	}
	for _, mem := range d.Members {
		if mem.ID == m.ids[0] && mem.State != alarm.StateAcknowledged {
			t.Errorf("root alarm %s, want acknowledged", mem.State)
		}
		if mem.ID != m.ids[0] && mem.State != alarm.StateOpen {
			t.Errorf("symptom %s, want open", mem.State)
		}
	}
	if rec := m.call(t, incidentOperator, http.MethodPost, path, ""); rec.Code != http.StatusConflict {
		t.Errorf("second ack: %d %s, want 409", rec.Code, rec.Body.String())
	}
	if d.MembersTruncated {
		t.Error("members_truncated with three members")
	}
	var commented bool
	for _, tr := range d.Timeline {
		if tr.Kind == "incident" && tr.Reason == alarm.IncidentReasonAcknowledged && tr.Comment != nil && *tr.Comment == "on it" {
			commented = true
		}
	}
	if !commented {
		t.Errorf("the comment of the ack is not in the incident timeline: %+v", d.Timeline)
	}
	updated := 0
	for _, e := range m.sink.Events() {
		if e.Type == events.IncidentUpdated {
			updated++
			if d := e.Data.(events.IncidentData); d.Change != alarm.IncidentChangeAcknowledged || d.IncidentID != m.inc.ID ||
				d.ActorID != memUser || d.RootAlarmID != m.ids[0] {
				t.Errorf("incident.updated %+v", d)
			}
		}
	}
	if updated != 1 {
		t.Errorf("incident.updated events: %d, want 1", updated)
	}
	// A resolved incident is 409 as well; without alert:acknowledge 403.
	if _, err := m.env.store.TransitionIncident(m.env.ctx, memOrg, m.inc.ID, alarm.IncidentResolved, nil); err != nil {
		t.Fatal(err)
	}
	if rec := m.call(t, incidentOperator, http.MethodPost, path, ""); rec.Code != http.StatusConflict {
		t.Errorf("ack of a resolved incident: %d, want 409", rec.Code)
	}
	if rec := m.call(t, []identity.Permission{identity.PermMonitoringRead}, http.MethodPost, path, ""); rec.Code != http.StatusForbidden {
		t.Errorf("ack without alert:acknowledge: %d, want 403", rec.Code)
	}
}

// TestIncidentAckCommentWithoutOpenRoot: with the root alarm acknowledged
// already the comment of the incident's ack is still kept (timeline).
func TestIncidentAckCommentWithoutOpenRoot(t *testing.T) {
	m := newMemIncidentREST(t)
	if _, err := m.svc.Acknowledge(m.env.ctx, memOrg, m.ids[0], memUser, ""); err != nil {
		t.Fatal(err)
	}
	d := decodeIncident(t, m.call(t, incidentOperator, http.MethodPost, "/api/v1/monitoring/incidents/"+m.inc.ID+"/ack",
		`{"comment":"root known"}`))
	for _, tr := range d.Timeline {
		if tr.Kind == "incident" && tr.Comment != nil && *tr.Comment == "root known" {
			return
		}
	}
	t.Errorf("the comment is lost: %+v", d.Timeline)
}

func TestIncidentRootMustBeMember(t *testing.T) {
	m := newMemIncidentREST(t)
	path := "/api/v1/monitoring/incidents/" + m.inc.ID + "/root"
	stray := openAlarms(t, m.env, "stray", 1)[0]
	for body, field := range map[string]string{
		`{"alarm_id":"` + stray + `","reason":"x"}`:   "alarm_id",
		`{"alarm_id":"` + ruleID + `","reason":"x"}`:  "alarm_id",
		`{"alarm_id":"nope","reason":"x"}`:            "alarm_id",
		`{"alarm_id":"` + m.ids[1] + `","reason":""}`: "reason",
	} {
		rec := m.call(t, incidentOperator, http.MethodPost, path, body)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"field":"`+field+`"`) {
			t.Errorf("%s: %d %s, want 422 %s", body, rec.Code, rec.Body.String(), field)
		}
	}
	if n := len(m.sink.Events()); n != 0 {
		t.Errorf("refused calls wrote %d events", n)
	}
	d := decodeIncident(t, m.call(t, incidentOperator, http.MethodPost, path, `{"alarm_id":"`+m.ids[2]+`","reason":"the switch, not the router"}`))
	if d.RootAlarmID != m.ids[2] || !d.ManualRoot {
		t.Fatalf("root %s manual %v", d.RootAlarmID, d.ManualRoot)
	}
	roles := map[string]string{}
	for _, mem := range d.Members {
		roles[mem.ID] = mem.Role
	}
	if roles[m.ids[0]] != alarm.RoleSymptom || roles[m.ids[2]] != alarm.RoleRoot {
		t.Errorf("roles after the change %v", roles)
	}
	var found bool
	for _, e := range d.Timeline {
		if e.Kind == "incident" && e.Reason == alarm.IncidentReasonRootChanged && e.ActorID != nil && *e.ActorID == memUser &&
			e.Details["reason"] == "the switch, not the router" {
			found = true
		}
	}
	if !found {
		t.Error("the transition root_changed names no actor and reason")
	}
	// Without monitoring:write the route is 403.
	reader := []identity.Permission{identity.PermMonitoringRead, identity.PermAlertAcknowledge}
	if rec := m.call(t, reader, http.MethodPost, path, `{"alarm_id":"`+m.ids[1]+`","reason":"x"}`); rec.Code != http.StatusForbidden {
		t.Errorf("root without monitoring:write: %d, want 403", rec.Code)
	}
}
