package alarm_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// Client and site scope of the alarms (MON-04, ALR-01, WP-981): the scope
// of an alarm follows its CI, without CI the client the caller set; the
// transitions follow their alarm; a restricted principal reads its clients
// and sites and the rows without, and writes only inside its scope.

type scopeEnv struct {
	t     *testing.T
	f     *scopetest.Fixture
	store *alarm.PGStore
}

func newScopeEnv(t *testing.T) *scopeEnv {
	t.Helper()
	f := scopetest.Seed(t, alarmTag)
	return &scopeEnv{t: t, f: f, store: alarm.NewPGStore(f.App)}
}

// site creates a site of clientID and places the CI there (the site node
// of the location tree has the site's id).
func (e *scopeEnv) placeAtNewSite(ciID, clientID, name string) string {
	e.t.Helper()
	ctx := context.Background()
	var siteID string
	if err := e.f.Admin.QueryRow(ctx, `INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, $3) RETURNING id::text`,
		e.f.OrgA, clientID, name).Scan(&siteID); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.f.Admin.Exec(ctx, `UPDATE ci SET location_id = $2 WHERE id = $1`, ciID, siteID); err != nil {
		e.t.Fatal(err)
	}
	var got *string
	if err := e.f.Admin.QueryRow(ctx, `SELECT site_id::text FROM ci WHERE id = $1`, ciID).Scan(&got); err != nil || got == nil || *got != siteID {
		e.t.Fatalf("CI %s at site %s: site_id %v, %v", ciID, siteID, got, err)
	}
	return siteID
}

func (e *scopeEnv) raise(ctx context.Context, series string, ci, client *string) (*alarm.Alarm, error) {
	a, _, err := e.store.UpsertOpen(ctx, e.f.OrgA, &alarm.UpsertInput{
		SourceKind: alarm.SourceCheck, SourceID: ruleID, SeriesKey: series, CIID: ci, ClientID: client,
		Severity: alarm.SeverityCritical, State: alarm.StateOpen,
	})
	return a, err
}

func (e *scopeEnv) mustRaise(series string, ci, client *string) *alarm.Alarm {
	e.t.Helper()
	a, err := e.raise(e.f.OrgCtx(e.f.OrgA), series, ci, client)
	if err != nil {
		e.t.Fatalf("raise %s: %v", series, err)
	}
	return a
}

func (e *scopeEnv) visibleIDs(ctx context.Context) []string {
	e.t.Helper()
	page, err := e.store.List(ctx, e.f.OrgA, &alarm.ListFilter{Limit: 200})
	if err != nil {
		e.t.Fatal(err)
	}
	var ids []string
	for _, a := range page.Items {
		ids = append(ids, a.ID)
	}
	return ids
}

func ptr(s string) *string { return &s }

func TestAlarmClientScope(t *testing.T) {
	e := newScopeEnv(t)
	ci1 := e.f.CI(t, e.f.OrgA, e.f.Client1, "alarm-c1")
	ci2 := e.f.CI(t, e.f.OrgA, e.f.Client2, "alarm-c2")
	own := e.mustRaise("own", &ci1, nil)
	foreign := e.mustRaise("foreign", &ci2, nil)
	orgWide := e.mustRaise("org-wide", nil, nil)

	client1 := e.f.ClientCtx(e.f.Client1)
	ids := e.visibleIDs(client1)
	if !slices.Contains(ids, own.ID) || !slices.Contains(ids, orgWide.ID) || slices.Contains(ids, foreign.ID) {
		t.Errorf("client principal sees %v; want its client's alarm %s and the org-wide %s, not %s", ids, own.ID, orgWide.ID, foreign.ID)
	}
	if _, err := e.store.Get(client1, e.f.OrgA, foreign.ID); err != alarm.ErrNotFound {
		t.Errorf("foreign alarm: %v, want ErrNotFound", err)
	}
	// Writes stay inside the scope: no alarm of the other client, no
	// org-wide alarm, no state change of an invisible one.
	if _, err := e.raise(client1, "write-foreign", &ci2, nil); err == nil {
		t.Error("a client principal raised an alarm of another client")
	}
	if _, err := e.raise(client1, "write-org-wide", nil, nil); err == nil {
		t.Error("a client principal raised an org-wide alarm")
	}
	if _, err := e.raise(client1, "write-own", &ci1, nil); err != nil {
		t.Errorf("a client principal raised an alarm of its client: %v", err)
	}
	if _, err := e.store.UpdateState(client1, e.f.OrgA, foreign.ID, &alarm.StateUpdate{
		State: alarm.StateAcknowledged, Severity: alarm.SeverityCritical,
	}); err != alarm.ErrNotFound {
		t.Errorf("state change of a foreign alarm: %v, want ErrNotFound", err)
	}
	// Organization B sees none of them.
	if b, err := e.store.Get(e.f.OrgCtx(e.f.OrgB), e.f.OrgB, own.ID); err != alarm.ErrNotFound {
		t.Errorf("organization B reads the alarm: %+v, %v", b, err)
	}
	if page, err := e.store.List(e.f.OrgCtx(e.f.OrgB), e.f.OrgB, &alarm.ListFilter{}); err != nil || len(page.Items) != 0 {
		t.Errorf("organization B sees %d alarms, %v", len(page.Items), err)
	}
	page, err := e.store.List(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, &alarm.ListFilter{ClientID: e.f.Client2})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != foreign.ID {
		t.Errorf("client filter: %+v, %v", page.Items, err)
	}
}

func TestAlarmSiteScope(t *testing.T) {
	e := newScopeEnv(t)
	ciA := e.f.CI(t, e.f.OrgA, e.f.Client1, "alarm-site-a")
	ciB := e.f.CI(t, e.f.OrgA, e.f.Client1, "alarm-site-b")
	siteA := e.placeAtNewSite(ciA, e.f.Client1, "wp981-site-a")
	e.placeAtNewSite(ciB, e.f.Client1, "wp981-site-b")
	atA := e.mustRaise("site-a", &ciA, nil)
	atB := e.mustRaise("site-b", &ciB, nil)
	noSite := e.mustRaise("no-site", nil, &e.f.Client1)

	scope := database.OrgWideScope(e.f.OrgA, e.f.User)
	scope.Sites = database.ScopeIDs(siteA)
	siteCtx := database.ContextWithTenantScope(context.Background(), &scope)
	ids := e.visibleIDs(siteCtx)
	if !slices.Contains(ids, atA.ID) || !slices.Contains(ids, noSite.ID) || slices.Contains(ids, atB.ID) {
		t.Errorf("site principal sees %v; want %s and the alarm without site %s, not %s", ids, atA.ID, noSite.ID, atB.ID)
	}
	if _, err := e.raise(siteCtx, "site-b-write", &ciB, nil); err == nil {
		t.Error("a site principal raised an alarm at another site")
	}
	if _, err := e.raise(siteCtx, "site-a-write", &ciA, nil); err != nil {
		t.Errorf("a site principal raised an alarm at its site: %v", err)
	}
	page, err := e.store.List(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, &alarm.ListFilter{SiteID: siteA})
	if err != nil || len(page.Items) != 2 {
		t.Errorf("site filter: %d alarms, %v; want 2", len(page.Items), err)
	}
}

func TestAlarmScopeDerivedFromCI(t *testing.T) {
	e := newScopeEnv(t)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "alarm-derived")
	site := e.placeAtNewSite(ci, e.f.Client1, "wp981-derived")
	// The client the caller passes does not count with a CI.
	withCI := e.mustRaise("derived", &ci, &e.f.Client2)
	if withCI.ClientID == nil || *withCI.ClientID != e.f.Client1 || withCI.SiteID == nil || *withCI.SiteID != site {
		t.Errorf("alarm with CI: client %v, site %v; want %s and %s from the CI", withCI.ClientID, withCI.SiteID, e.f.Client1, site)
	}
	// Without CI the caller's client stays and the site is cleared.
	without := e.mustRaise("collector", nil, &e.f.Client2)
	if without.ClientID == nil || *without.ClientID != e.f.Client2 || without.SiteID != nil {
		t.Errorf("alarm without CI: client %v, site %v; want %s and none", without.ClientID, without.SiteID, e.f.Client2)
	}
	ctx := context.Background()
	var siteOf *string
	if err := e.f.Admin.QueryRow(ctx, `INSERT INTO alarm (organization_id, client_id, site_id, source_kind, source_id, severity, state)
		VALUES ($1, $2, $3, 'collector', gen_random_uuid(), 'info', 'pending') RETURNING site_id::text`,
		e.f.OrgA, e.f.Client1, site).Scan(&siteOf); err != nil || siteOf != nil {
		t.Errorf("alarm without CI inserted with a site: site %v, %v; want NULL", siteOf, err)
	}
	// A changed CI reference re-derives the scope.
	ci2 := e.f.CI(t, e.f.OrgA, e.f.Client2, "alarm-derived-2")
	var client string
	if err := e.f.Admin.QueryRow(ctx, `UPDATE alarm SET ci_id = $2 WHERE id = $1 RETURNING client_id::text`, withCI.ID, ci2).Scan(&client); err != nil ||
		client != e.f.Client2 {
		t.Errorf("alarm moved to a CI of client 2: client %s, %v", client, err)
	}
	if !time.Now().After(withCI.FirstSeenAt.Add(-time.Minute)) {
		t.Error("first_seen_at in the future")
	}
}

func TestAlarmTransitionFollowsAlarmScope(t *testing.T) {
	e := newScopeEnv(t)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "alarm-transition")
	site := e.placeAtNewSite(ci, e.f.Client1, "wp981-transition")
	a := e.mustRaise("transition", &ci, nil)
	from := alarm.StateOpen
	tr, err := e.store.AppendTransition(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, &alarm.Transition{
		AlarmID: a.ID, FromState: &from, ToState: alarm.StateResolved, Reason: "cleared",
		ToSeverity: ptr(alarm.SeverityCritical),
	})
	if err != nil {
		t.Fatal(err)
	}
	if tr.ClientID == nil || *tr.ClientID != e.f.Client1 || tr.SiteID == nil || *tr.SiteID != site || tr.OrganizationID != e.f.OrgA {
		t.Errorf("transition scope: org %s, client %v, site %v; want the alarm's", tr.OrganizationID, tr.ClientID, tr.SiteID)
	}
	if log, err := e.store.Transitions(e.f.ClientCtx(e.f.Client1), e.f.OrgA, a.ID); err != nil || len(log) != 1 {
		t.Errorf("client 1 reads %d transitions, %v; want 1", len(log), err)
	}
	if log, err := e.store.Transitions(e.f.ClientCtx(e.f.Client2), e.f.OrgA, a.ID); err != nil || len(log) != 0 {
		t.Errorf("client 2 reads %d transitions, %v; want none", len(log), err)
	}
	// A principal of another client appends no transition to the alarm.
	if _, err := e.store.AppendTransition(e.f.ClientCtx(e.f.Client2), e.f.OrgA, &alarm.Transition{
		AlarmID: a.ID, ToState: alarm.StateOpen, Reason: "foreign",
	}); err == nil {
		t.Error("a principal of another client appended a transition")
	}
}

// TestAlarmScopeFollowsMovedCI: a CI moved to another client or site
// carries its alarms and their transitions along (alarm_scope_propagation,
// alarm_transition_scope_propagation); the former client no longer sees
// them, the new one does.
func TestAlarmScopeFollowsMovedCI(t *testing.T) {
	e := newScopeEnv(t)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "alarm-moved")
	a := e.mustRaise("moved", &ci, nil)
	from := alarm.StateOpen
	if _, err := e.store.AppendTransition(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, &alarm.Transition{
		AlarmID: a.ID, FromState: &from, ToState: alarm.StateAcknowledged, Reason: "ack",
	}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := e.f.Admin.Exec(ctx, `UPDATE ci SET client_id = $2 WHERE id = $1`, ci, e.f.Client2); err != nil {
		t.Fatalf("move the CI to client 2: %v", err)
	}
	client1, client2 := e.f.ClientCtx(e.f.Client1), e.f.ClientCtx(e.f.Client2)
	if _, err := e.store.Get(client1, e.f.OrgA, a.ID); err != alarm.ErrNotFound {
		t.Errorf("former client reads the alarm of the moved CI: %v, want ErrNotFound", err)
	}
	if log, err := e.store.Transitions(client1, e.f.OrgA, a.ID); err != nil || len(log) != 0 {
		t.Errorf("former client reads %d transitions, %v; want none", len(log), err)
	}
	got, err := e.store.Get(client2, e.f.OrgA, a.ID)
	if err != nil || got.ClientID == nil || *got.ClientID != e.f.Client2 {
		t.Fatalf("new client reads the alarm: %+v, %v", got, err)
	}
	log, err := e.store.Transitions(client2, e.f.OrgA, a.ID)
	if err != nil || len(log) != 1 || log[0].ClientID == nil || *log[0].ClientID != e.f.Client2 {
		t.Errorf("new client reads the transitions: %+v, %v; want one of client 2", log, err)
	}
	// A move to a site follows as well.
	site := e.placeAtNewSite(ci, e.f.Client2, "wp981-moved")
	log, err = e.store.Transitions(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, a.ID)
	if err != nil || len(log) != 1 || log[0].SiteID == nil || *log[0].SiteID != site {
		t.Errorf("transition after the site move: %+v, %v; want site %s", log, err, site)
	}
	// The log stays append-only for everything but the propagated scope.
	if _, err := e.f.Admin.Exec(ctx, `UPDATE alarm_transition SET client_id = $2 WHERE alarm_id = $1`, a.ID, e.f.Client1); err == nil {
		t.Error("a direct scope change of a transition was accepted")
	}
}
