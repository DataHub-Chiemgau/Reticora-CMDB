package probe_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
)

// Retest (WP-949, MON-02, PRB-01):
//
//	cd backend && go test -count=1 -p 1 -run 'TestAutocreate|TestDeletedAutoProbeStaysDeleted|TestSuppressionFollowsCIMove' ./internal/probe/

// autoEnv is organization A with a collector of client 1 (and its own
// collector group) and observable CIs of client 1 it discovered.
type autoEnv struct {
	t         *testing.T
	f         *scopetest.Fixture
	repo      *probe.PGRepository
	collector string
	group     string
}

func newAutoEnv(t *testing.T, limits map[string]int64) *autoEnv {
	t.Helper()
	f := scopetest.Seed(t, "93")
	e := &autoEnv{t: t, f: f, repo: probe.NewPGRepository(f.App)}
	if limits != nil {
		svc := entitlement.NewService(entitlement.NewPGRepository(f.App), entitlement.Options{Enforce: true})
		if _, err := svc.Grant(f.OrgCtx(f.OrgA), &entitlement.Entitlement{OrganizationID: f.OrgA, FeatureKey: entitlement.FeatureMonitoring,
			Plan: entitlement.PlanStandard, Enabled: true, Limits: limits}); err != nil {
			t.Fatal(err)
		}
		e.repo.SetBudget(svc)
	}
	e.collector = e.scalar(`INSERT INTO collector (organization_id, client_id, name, status) VALUES ($1, $2, 'wp949', 'active') RETURNING id::text`,
		f.OrgA, f.Client1)
	e.group = e.scalar(`SELECT group_id::text FROM collector_group_member WHERE collector_id = $1`, e.collector)
	return e
}

// newProbeMux serves the probe routes of repo behind the route
// authorization and audit.
func newProbeMux(repo *probe.PGRepository) *chi.Mux {
	inner := chi.NewRouter()
	probe.NewHandler(repo).RegisterRoutes(inner)
	mux := chi.NewRouter()
	for _, rt := range probeRoutes {
		mux.With(server.AuthorizeRoute(rt.method, rt.pattern)).Method(rt.method, rt.pattern, inner)
	}
	return mux
}

func (e *autoEnv) scalar(sql string, args ...any) string {
	e.t.Helper()
	var out string
	if err := e.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return out
}

func (e *autoEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.f.Admin.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

// on switches monitoring.auto_ping on.
func (e *autoEnv) on() {
	e.exec(`UPDATE organization SET settings = COALESCE(settings, '{}'::jsonb) || '{"monitoring":{"auto_ping":true}}' WHERE id = $1`, e.f.OrgA)
}

// ci inserts an observable CI of client 1 with the management IP, found by
// the collector (collector empty: not discovered).
func (e *autoEnv) ci(name, ip, collector string) string {
	e.t.Helper()
	id := e.f.CI(e.t, e.f.OrgA, e.f.Client1, name)
	e.exec(`UPDATE ci SET management_ip = $2 WHERE id = $1`, id, ip)
	if collector != "" {
		e.exec(`INSERT INTO ci_change (organization_id, ci_id, change_type, source, collector_id) VALUES ($1, $2, 'update', 'collector', $3)`,
			e.f.OrgA, id, collector)
	}
	return id
}

func (e *autoEnv) run() *probe.AutocreateReport {
	e.t.Helper()
	r, err := probe.NewAutocreator(e.repo).Run(context.Background(), e.f.OrgA)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func (e *autoEnv) autoProbes() int {
	e.t.Helper()
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM probe WHERE organization_id = $1 AND origin = 'auto'`,
		e.f.OrgA).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestAutocreateOffByDefault(t *testing.T) {
	e := newAutoEnv(t, nil)
	e.ci("wp949-off", "10.98.0.1", e.collector)
	if r := e.run(); r.Created != 0 || e.autoProbes() != 0 {
		t.Fatalf("without auto_ping: %+v, %d auto probes", r, e.autoProbes())
	}
	// Switching it off again deletes nothing.
	e.on()
	e.run()
	e.exec(`UPDATE organization SET settings = settings || '{"monitoring":{"auto_ping":false}}' WHERE id = $1`, e.f.OrgA)
	if r := e.run(); r.Created != 0 || e.autoProbes() != 1 {
		t.Errorf("switched off: %+v, %d auto probes, want the one kept", r, e.autoProbes())
	}
}

func TestAutocreateCreatesIcmpWithDefaults(t *testing.T) {
	e := newAutoEnv(t, nil)
	e.on()
	ci := e.ci("wp949-a", "10.98.1.1", e.collector)
	lost := e.ci("wp949-b", "10.98.1.2", "")
	r := e.run()
	if r.Created != 1 || r.Skipped[probe.SkipNoCollector].Count != 1 || r.Skipped[probe.SkipNoCollector].CIIDs[0] != lost {
		t.Fatalf("report %+v", r)
	}
	id := e.scalar(`SELECT id::text FROM probe WHERE ci_id = $1`, ci)
	var typ, target, group, params string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT type, target, collector_group_id::text, params::text FROM probe WHERE id = $1`,
		id).Scan(&typ, &target, &group, &params); err != nil {
		t.Fatal(err)
	}
	if typ != "icmp" || target != "10.98.1.1" || group != e.group || params != "{}" {
		t.Errorf("auto probe %s %s group %s params %s; want icmp 10.98.1.1 in the collector's group without params", typ, target, group, params)
	}
	p, err := e.repo.Get(e.f.OrgCtx(e.f.OrgA), id)
	if err != nil {
		t.Fatal(err)
	}
	layers, err := e.repo.Layers(e.f.OrgCtx(e.f.OrgA), p)
	if err != nil {
		t.Fatal(err)
	}
	eff := probe.Resolve(p, layers)
	if eff.IntervalS.Int() != 30 || eff.TimeoutS.Int() != 10 || eff.DownCount.Int() != 5 || eff.UpCount.Int() != 1 {
		t.Errorf("effective %d/%d/%d/%d, want 30/10/5/1", eff.IntervalS.Int(), eff.TimeoutS.Int(), eff.DownCount.Int(), eff.UpCount.Int())
	}
	if p.Origin != probe.ProbeOriginAuto {
		t.Errorf("origin %q", p.Origin)
	}
	// A CI of another client than the collector's is skipped.
	other := e.f.CI(t, e.f.OrgA, e.f.Client2, "wp949-c2")
	e.exec(`UPDATE ci SET management_ip = '10.98.1.3' WHERE id = $1`, other)
	e.exec(`INSERT INTO ci_change (organization_id, ci_id, change_type, source, collector_id) VALUES ($1, $2, 'update', 'collector', $3)`,
		e.f.OrgA, other, e.collector)
	if r := e.run(); r.Skipped[probe.SkipClientMismatch].Count != 1 {
		t.Errorf("client mismatch: %+v", r.Skipped[probe.SkipClientMismatch])
	}
}

func TestAutocreateIsIdempotent(t *testing.T) {
	e := newAutoEnv(t, nil)
	e.on()
	e.ci("wp949-i1", "10.98.2.1", e.collector)
	e.ci("wp949-i2", "10.98.2.2", e.collector)
	if r := e.run(); r.Created != 2 {
		t.Fatalf("first run %+v", r)
	}
	if r := e.run(); r.Created != 0 || r.Updated != 0 || e.autoProbes() != 2 {
		t.Errorf("second run %+v with %d auto probes, want nothing new", r, e.autoProbes())
	}
}

func TestAutocreateRespectsQuota(t *testing.T) {
	e := newAutoEnv(t, map[string]int64{entitlement.LimitMaxMonitoredTargets: 3})
	e.on()
	for i := range 5 {
		e.ci(fmt.Sprintf("wp949-q%d", i), fmt.Sprintf("10.98.3.%d", i+1), e.collector)
	}
	r := e.run()
	if r.Created != 3 || r.Skipped[probe.SkipQuota].Count != 2 || len(r.Skipped[probe.SkipQuota].CIIDs) != 2 {
		t.Errorf("report %+v (quota %+v), want 3 created and 2 quota", r, r.Skipped[probe.SkipQuota])
	}
	if n := e.autoProbes(); n != 3 {
		t.Errorf("%d auto probes, want 3", n)
	}
}

func TestDeletedAutoProbeStaysDeleted(t *testing.T) {
	e := newAutoEnv(t, nil)
	e.on()
	ci := e.ci("wp949-d", "10.98.4.1", e.collector)
	e.run()
	id := e.scalar(`SELECT id::text FROM probe WHERE ci_id = $1`, ci)
	env := &probeEnv{t: t, f: e.f}
	env.mux = newProbeMux(e.repo)
	if r := env.do(orgAdmin, http.MethodDelete, "/api/v1/monitoring/probes/"+id, ""); r.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", r.Code, r.Body)
	}
	if n := e.scalar(`SELECT count(*)::text FROM probe_autocreate_suppression WHERE ci_id = $1 AND probe_type = 'icmp'`, ci); n != "1" {
		t.Fatalf("%s suppressions, want 1", n)
	}
	if r := e.run(); r.Created != 0 || r.Skipped[probe.SkipSuppressed].Count != 1 {
		t.Errorf("after the delete: %+v, want the CI suppressed", r)
	}
	// A manual probe's delete writes no suppression.
	other := e.ci("wp949-d2", "10.98.4.2", "")
	m := env.create(orgAdmin, fmt.Sprintf(`{"type":"icmp","target":"10.98.4.2","ci_id":%q,"collector_group_id":%q}`, other, e.group))
	if r := env.do(orgAdmin, http.MethodDelete, "/api/v1/monitoring/probes/"+m.ID, ""); r.Code != http.StatusNoContent {
		t.Fatalf("delete manual: %d %s", r.Code, r.Body)
	}
	if n := e.scalar(`SELECT count(*)::text FROM probe_autocreate_suppression WHERE ci_id = $1`, other); n != "0" {
		t.Errorf("manual delete wrote %s suppressions", n)
	}
}

// TestSuppressionFollowsCIMove: the suppression entry of a deleted auto
// probe carries the scope of its CI and moves with it, so after a move to
// client 2 only client 2 sees and writes it (located-scope, Konvention 5).
func TestSuppressionFollowsCIMove(t *testing.T) {
	e := newAutoEnv(t, nil)
	e.on()
	ci := e.ci("wp949-m", "10.98.6.1", e.collector)
	e.run()
	id := e.scalar(`SELECT id::text FROM probe WHERE ci_id = $1`, ci)
	env := &probeEnv{t: t, f: e.f}
	env.mux = newProbeMux(e.repo)
	if r := env.do(orgAdmin, http.MethodDelete, "/api/v1/monitoring/probes/"+id, ""); r.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", r.Code, r.Body)
	}
	// A stale scope written by hand is replaced by that of the CI.
	e.exec(`UPDATE probe_autocreate_suppression SET client_id = $2 WHERE ci_id = $1`, ci, e.f.Client2)
	if got := e.scalar(`SELECT client_id::text FROM probe_autocreate_suppression WHERE ci_id = $1`, ci); got != e.f.Client1 {
		t.Fatalf("suppression client %s, want the CI's client 1", got)
	}
	e.exec(`UPDATE ci SET client_id = $2 WHERE id = $1`, ci, e.f.Client2)
	if got := e.scalar(`SELECT client_id::text FROM probe_autocreate_suppression WHERE ci_id = $1`, ci); got != e.f.Client2 {
		t.Fatalf("suppression client %s after the move, want client 2", got)
	}
	visible := func(client string) (seen, deleted int64) {
		t.Helper()
		scope, _ := database.TenantScopeFromContext(e.f.ClientCtx(client))
		err := database.WithTenant(context.Background(), e.f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM probe_autocreate_suppression WHERE ci_id = $1`, ci).Scan(&seen); err != nil {
				return err
			}
			tag, err := tx.Exec(ctx, `DELETE FROM probe_autocreate_suppression WHERE ci_id = $1`, ci)
			deleted = tag.RowsAffected()
			if err != nil {
				return err
			}
			return errRollback
		})
		if err != nil && !errors.Is(err, errRollback) {
			t.Fatal(err)
		}
		return seen, deleted
	}
	if seen, deleted := visible(e.f.Client1); seen != 0 || deleted != 0 {
		t.Errorf("client 1 sees %d and deletes %d entries of the moved CI, want 0", seen, deleted)
	}
	if seen, deleted := visible(e.f.Client2); seen != 1 || deleted != 1 {
		t.Errorf("client 2 sees %d and deletes %d entries of the moved CI, want 1", seen, deleted)
	}
}

// errRollback ends a test transaction without committing it.
var errRollback = errors.New("rollback")

func TestAutocreateFollowsManagementIPChange(t *testing.T) {
	e := newAutoEnv(t, nil)
	e.on()
	ci := e.ci("wp949-m", "10.98.5.1", e.collector)
	e.run()
	e.exec(`UPDATE ci SET management_ip = '10.98.5.9' WHERE id = $1`, ci)
	if r := e.run(); r.Updated != 1 || r.Created != 0 {
		t.Fatalf("report %+v, want one updated", r)
	}
	if target := e.scalar(`SELECT target FROM probe WHERE ci_id = $1`, ci); target != "10.98.5.9" {
		t.Errorf("target %s, want 10.98.5.9", target)
	}
}

func TestAutocreateLeavesManualProbes(t *testing.T) {
	e := newAutoEnv(t, nil)
	e.on()
	ci := e.ci("wp949-man", "10.98.6.1", e.collector)
	e.exec(`INSERT INTO probe (organization_id, ci_id, collector_group_id, type, target) VALUES ($1, $2, $3, 'icmp', '10.98.6.50')`,
		e.f.OrgA, ci, e.group)
	if r := e.run(); r.Created != 0 || r.Updated != 0 {
		t.Errorf("report %+v, want the manual probe left alone", r)
	}
	if target := e.scalar(`SELECT target || '/' || origin FROM probe WHERE ci_id = $1`, ci); target != "10.98.6.50/manual" {
		t.Errorf("manual probe %s, want unchanged", target)
	}
}
