package maintenance_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/maintenance"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/topology"
)

// Active maintenance windows on PostgreSQL (ALR-11, MGT-04, WP-1033): the
// CIs of an active window over the graph, cancelled windows, and the
// boundary events of the system job maintenance.boundaries.
//
// Retest: cd backend && go test -count=1 -p 1 -run 'TestActive|TestBoundaries' ./internal/maintenance/

// activeEnv is a router of client 1 with a CPE of client 2 behind it
// (uplink_to), a monitor connected_to the router (heuristic) and an
// unrelated server.
type activeEnv struct {
	f                            *scopetest.Fixture
	repo                         *maintenance.PGRepository
	router, cpe, monitor, server string
	analyzer                     *topology.Analyzer
}

func newActiveEnv(t *testing.T) *activeEnv {
	t.Helper()
	f := scopetest.Seed(t, "3b")
	e := &activeEnv{f: f, repo: maintenance.NewPGRepository(f.App)}
	e.router = f.CI(t, f.OrgA, f.Client1, "act-router")
	e.cpe = f.CI(t, f.OrgA, f.Client2, "act-cpe")
	e.monitor = f.CI(t, f.OrgA, f.Client1, "act-monitor")
	e.server = f.CI(t, f.OrgA, f.Client2, "act-server")
	if _, err := f.Admin.Exec(context.Background(), `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES
		($1, $2, $3, 'uplink_to'), ($1, $4, $3, 'connected_to')`, f.OrgA, e.cpe, e.router, e.monitor); err != nil {
		t.Fatal(err)
	}
	e.analyzer = topology.NewAnalyzer(ci.NewPGRepository(f.App), relationship.NewPGRepository(f.App)).
		WithRelationshipTypes(relationshiptype.NewPGRepository(f.App))
	return e
}

func (e *activeEnv) window(t *testing.T, title string, from, to time.Time, cis ...string) *maintenance.Window {
	t.Helper()
	w := &maintenance.Window{OrganizationID: e.f.OrgA, Title: title, StartsAt: from, EndsAt: to, CIIDs: cis}
	if err := e.repo.Create(e.f.OrgCtx(e.f.OrgA), w); err != nil {
		t.Fatal(err)
	}
	return w
}

// TestActiveCIsIncludeCertainImpact: a window on the router; the CPE behind
// it is certainly affected, the monitor connected_to it only heuristically,
// the unrelated server not at all.
func TestActiveCIsIncludeCertainImpact(t *testing.T) {
	e := newActiveEnv(t)
	now := time.Now().UTC()
	w := e.window(t, "Router upgrade", now.Add(-time.Hour), now.Add(time.Hour), e.router)
	active := maintenance.NewActive(e.repo, e.analyzer)
	set, err := active.ActiveCIs(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Windows) != 1 || set.Windows[0].WindowID != w.ID {
		t.Fatalf("active windows %+v, want the router window", set.Windows)
	}
	cis := set.Windows[0]
	if !slices.Contains(cis.Certain, e.router) || !slices.Contains(cis.Certain, e.cpe) || slices.Contains(cis.Certain, e.monitor) {
		t.Errorf("certain CIs %v, want router and CPE", cis.Certain)
	}
	if !slices.Equal(cis.Heuristic, []string{e.monitor}) {
		t.Errorf("heuristic CIs %v, want the monitor", cis.Heuristic)
	}
	for ci, want := range map[string]string{e.cpe: "certain", e.monitor: "heuristic", e.server: "none"} {
		hit, err := active.MaintenanceOf(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, ci, now)
		if err != nil {
			t.Fatal(err)
		}
		got := "none"
		switch {
		case hit.WindowID == w.ID && hit.Heuristic:
			got = "heuristic"
		case hit.WindowID == w.ID:
			got = "certain"
		}
		if got != want {
			t.Errorf("CI %s: %s, want %s", ci, got, want)
		}
	}
	// Before the start and at the end the window is not active.
	for _, at := range []time.Time{now.Add(-2 * time.Hour), now.Add(time.Hour)} {
		set, err := active.ActiveCIs(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, at)
		if err != nil {
			t.Fatal(err)
		}
		if len(set.Windows) != 0 {
			t.Errorf("at %s: active windows %+v, want none", at, set.Windows)
		}
	}
}

// TestActiveWindowsIgnoresCancelled: a cancelled window is never active and
// not between; WindowsBetween returns the overlapping windows.
func TestActiveWindowsIgnoresCancelled(t *testing.T) {
	e := newActiveEnv(t)
	now := time.Now().UTC()
	live := e.window(t, "live", now.Add(-time.Hour), now.Add(time.Hour), e.router)
	gone := e.window(t, "cancelled", now.Add(-time.Hour), now.Add(time.Hour), e.server)
	cancelled := "cancelled"
	if _, err := e.repo.Update(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, gone.ID, maintenance.UpdateWindowRequest{Status: &cancelled}); err != nil {
		t.Fatal(err)
	}
	later := e.window(t, "later", now.Add(3*time.Hour), now.Add(4*time.Hour), e.cpe)
	wins, err := e.repo.ActiveWindows(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(wins) != 1 || wins[0].ID != live.ID || !slices.Equal(wins[0].CIIDs, []string{e.router}) {
		t.Errorf("active windows %+v, want only the live one with its CI", wins)
	}
	between, err := e.repo.WindowsBetween(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, now, now.Add(5*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, w := range between {
		ids = append(ids, w.ID)
	}
	if !slices.Equal(ids, []string{live.ID, later.ID}) {
		t.Errorf("windows between %v, want live and later", ids)
	}
}

// fakeReleaser records the windows whose alarms were released.
type fakeReleaser struct {
	mu      sync.Mutex
	windows []string
	stale   int
}

func (f *fakeReleaser) ReleaseMaintenance(_ context.Context, _, windowID string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.windows = append(f.windows, windowID)
	return 2, nil
}

func (f *fakeReleaser) ReleaseStaleMaintenance(context.Context, string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stale++
	return 0, nil
}

// TestBoundariesEmitOncePerTransition: two runs in the same minute write
// one maintenance.started per started window; after the end two runs write
// one maintenance.ended and release the alarms of the window once.
func TestBoundariesEmitOncePerTransition(t *testing.T) {
	e := newActiveEnv(t)
	now := time.Now().UTC()
	w := e.window(t, "boundaries", now.Add(-10*time.Minute), now.Add(time.Hour), e.router, e.cpe)
	future := e.window(t, "future", now.Add(2*time.Hour), now.Add(3*time.Hour), e.server)
	releaser := &fakeReleaser{}
	clock := now
	b := maintenance.NewBoundaries(e.f.App, e.repo, releaser).WithClock(func() time.Time { return clock })
	count := func(typ, window string) int {
		t.Helper()
		var n int
		if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND type = $2
			AND entity_id = $3`, e.f.OrgA, typ, window).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for range 2 {
		if _, err := b.RunOrg(context.Background(), e.f.OrgA); err != nil {
			t.Fatal(err)
		}
	}
	if count("maintenance.started", w.ID) != 1 || count("maintenance.ended", w.ID) != 0 || count("maintenance.started", future.ID) != 0 {
		t.Fatalf("after the start: started %d, ended %d, future started %d; want 1, 0, 0",
			count("maintenance.started", w.ID), count("maintenance.ended", w.ID), count("maintenance.started", future.ID))
	}
	var data string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT data::text FROM event_outbox WHERE organization_id = $1
		AND type = 'maintenance.started' AND entity_id = $2`, e.f.OrgA, w.ID).Scan(&data); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(data, `"ci_count": 2`) && !strings.Contains(data, `"ci_count":2`) {
		t.Errorf("maintenance.started data %s, want ci_count 2", data)
	}
	if len(releaser.windows) != 0 {
		t.Errorf("alarms released before the end: %v", releaser.windows)
	}
	clock = now.Add(90 * time.Minute)
	for range 2 {
		if _, err := b.RunOrg(context.Background(), e.f.OrgA); err != nil {
			t.Fatal(err)
		}
	}
	if count("maintenance.ended", w.ID) != 1 || count("maintenance.started", w.ID) != 1 {
		t.Errorf("after the end: started %d, ended %d; want one each", count("maintenance.started", w.ID), count("maintenance.ended", w.ID))
	}
	if !slices.Equal(releaser.windows, []string{w.ID}) {
		t.Errorf("released windows %v, want the ended window once", releaser.windows)
	}
}

// TestActiveCIsOrgWideWhateverTheScope: the CIs of a window are computed
// org-wide; a first question in the scope of client 1 (a collector bound to
// it) does not narrow what the cache holds: the CPE of client 2 behind the
// router stays certainly affected for the org-wide question after it.
func TestActiveCIsOrgWideWhateverTheScope(t *testing.T) {
	e := newActiveEnv(t)
	now := time.Now().UTC()
	w := e.window(t, "Router upgrade", now.Add(-time.Hour), now.Add(time.Hour), e.router)
	active := maintenance.NewActive(e.repo, e.analyzer)
	hit, err := active.MaintenanceOf(e.f.ClientCtx(e.f.Client1), e.f.OrgA, e.router, now)
	if err != nil {
		t.Fatal(err)
	}
	if hit.WindowID != w.ID || hit.Heuristic {
		t.Fatalf("router in the scope of client 1: %+v, want the window", hit)
	}
	hit, err = active.MaintenanceOf(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, e.cpe, now)
	if err != nil {
		t.Fatal(err)
	}
	if hit.WindowID != w.ID || hit.Heuristic {
		t.Errorf("CPE of client 2 after a client-bound question: %+v, want certainly in the window", hit)
	}
}

// TestBoundariesReleaseCancelledWindow: a window cancelled while active
// never ends for the job, but its suppressed alarm is released with the next
// run (alarm.Service.ReleaseStaleMaintenance) and notified after the
// maintenance.
func TestBoundariesReleaseCancelledWindow(t *testing.T) {
	e := newActiveEnv(t)
	now := time.Now().UTC()
	w := e.window(t, "cancel me", now.Add(-10*time.Minute), now.Add(time.Hour), e.router)
	svc := alarm.NewPGService(e.f.App).WithMaintenance(maintenance.NewActive(e.repo, e.analyzer))
	ctx := e.f.OrgCtx(e.f.OrgA)
	ci := e.cpe
	a, err := svc.Raise(ctx, e.f.OrgA, &alarm.RaiseInput{SourceKind: alarm.SourceCheck, SourceID: "11111111-1111-4111-8111-0000000010c3",
		SeriesKey: alarm.CheckSeries, CIID: &ci, Severity: alarm.SeverityCritical, Summary: "icmp down"})
	if err != nil {
		t.Fatal(err)
	}
	if a.SuppressedReason == nil || *a.SuppressedReason != alarm.SuppressedMaintenance {
		t.Fatalf("alarm %+v, want suppressed by the window", a)
	}
	b := maintenance.NewBoundaries(e.f.App, e.repo, svc)
	if _, err = b.RunOrg(context.Background(), e.f.OrgA); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Store().Get(ctx, e.f.OrgA, a.ID); got == nil || got.SuppressedReason == nil {
		t.Fatalf("alarm released while its window is active: %+v", got)
	}
	cancelled := "cancelled"
	if _, err = e.repo.Update(ctx, e.f.OrgA, w.ID, maintenance.UpdateWindowRequest{Status: &cancelled}); err != nil {
		t.Fatal(err)
	}
	if _, err = b.RunOrg(context.Background(), e.f.OrgA); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Store().Get(ctx, e.f.OrgA, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SuppressedReason != nil || got.State != alarm.StateOpen {
		t.Errorf("alarm of the cancelled window %+v, want open without suppression", got)
	}
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND type = 'alarm.opened'
		AND entity_id = $2 AND (data->>'after_maintenance')::boolean`, e.f.OrgA, a.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d alarm.opened after_maintenance, want 1", n)
	}
}
