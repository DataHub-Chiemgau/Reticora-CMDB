package probe_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
)

// Retest (WP-940, PKG-04):
//
//	cd backend && go test -count=1 -p 1 -run 'TestConcurrentProbeCreateRespectsQuota|TestIntervalChangeRespectsPerMinute|TestResolveClampsToPlanMinimum|TestDisabledProbeIntervalBelowMinimum' ./internal/probe/

// newBudgetEnv is the probe API of organization A (probeEnv) with the
// budget of an enforcing entitlement service whose monitoring row of
// organization A holds limits.
func newBudgetEnv(t *testing.T, limits map[string]int64) (*probeEnv, *probe.PGRepository) {
	t.Helper()
	f := scopetest.Seed(t, "93")
	svc := entitlement.NewService(entitlement.NewPGRepository(f.App), entitlement.Options{Enforce: true})
	if _, err := svc.Grant(f.OrgCtx(f.OrgA), &entitlement.Entitlement{OrganizationID: f.OrgA, FeatureKey: entitlement.FeatureMonitoring,
		Plan: entitlement.PlanStandard, Enabled: true, Limits: limits}); err != nil {
		t.Fatal(err)
	}
	repo := probe.NewPGRepository(f.App)
	inner := chi.NewRouter()
	probe.NewHandler(repo).WithBudget(svc).RegisterRoutes(inner)
	mux := chi.NewRouter()
	auditor := audit.NewPGRouteAuditor(f.App)
	for _, rt := range probeRoutes {
		mode := middleware.AuditIntent
		if rt.method == http.MethodGet {
			mode = middleware.AuditNone
		}
		mux.With(server.AuthorizeRoute(rt.method, rt.pattern), middleware.AuditRoute(rt.method, rt.pattern, auditor, mode, "")).
			Method(rt.method, rt.pattern, inner)
	}
	e := &probeEnv{t: t, f: f, mux: mux}
	e.group1 = e.scalar(`INSERT INTO collector_group (organization_id, client_id, name) VALUES ($1, $2, 'wp940-c1') RETURNING id::text`, f.OrgA, f.Client1)
	return e, repo
}

// icmpBody is the body of an icmp probe of client 1 on target.
func (e *probeEnv) icmpBody(target, params string) string {
	return fmt.Sprintf(`{"type":"icmp","target":%q,"client_id":%q,"collector_group_id":%q,"params":%s}`,
		target, e.f.Client1, e.group1, params)
}

// TestConcurrentProbeCreateRespectsQuota covers AK3 of WP-940 (PKG-04):
// with max_monitored_targets 5, 20 parallel creations of probes on
// distinct targets admit exactly 5; the others are 403 entitlement-limit.
func TestConcurrentProbeCreateRespectsQuota(t *testing.T) {
	e, _ := newBudgetEnv(t, map[string]int64{entitlement.LimitMaxMonitoredTargets: 5})
	codes := make([]int, 20)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := e.do(orgAdmin, http.MethodPost, "/api/v1/monitoring/probes", e.icmpBody(fmt.Sprintf("10.94.0.%d", i+1), `{}`))
			codes[i] = r.Code
		}(i)
	}
	wg.Wait()
	created, refused := 0, 0
	for i, code := range codes {
		switch code {
		case http.StatusCreated:
			created++
		case http.StatusForbidden:
			refused++
		default:
			t.Errorf("create %d: %d", i, code)
		}
	}
	if created != 5 || refused != 15 {
		t.Errorf("created %d, refused %d; want exactly 5 and 15", created, refused)
	}
	var stored int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM probe WHERE organization_id = $1`, e.f.OrgA).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 5 {
		t.Errorf("%d probes stored, want 5", stored)
	}
	// A second probe on a target already counted takes no target; a
	// disabled probe counts nothing.
	counted := e.scalar(`SELECT target FROM probe WHERE organization_id = $1 ORDER BY target LIMIT 1`, e.f.OrgA)
	if r := e.do(orgAdmin, http.MethodPost, "/api/v1/monitoring/probes",
		fmt.Sprintf(`{"type":"tcp","target":"%s:22","client_id":%q,"collector_group_id":%q,"params":{"port":22}}`, counted, e.f.Client1, e.group1)); r.Code != http.StatusCreated {
		t.Errorf("tcp on a counted host: %d %s, want 201", r.Code, r.Body)
	}
	if r := e.do(orgAdmin, http.MethodPost, "/api/v1/monitoring/probes",
		fmt.Sprintf(`{"type":"icmp","target":"10.94.1.1","client_id":%q,"collector_group_id":%q,"enabled":false}`, e.f.Client1, e.group1)); r.Code != http.StatusCreated {
		t.Errorf("disabled probe above the quota: %d %s, want 201", r.Code, r.Body)
	} else if en := e.do(orgAdmin, http.MethodPatch, "/api/v1/monitoring/probes/"+r.Probe.ID, `{"enabled":true}`); en.Code != http.StatusForbidden {
		t.Errorf("enabling above the quota: %d %s, want 403", en.Code, en.Body)
	}
}

// TestIntervalChangeRespectsPerMinute covers AK2 and AK3: a shorter
// interval that raises the checks per minute above max_probes_per_minute
// is 403 and changes nothing of the PATCH; a longer one passes.
func TestIntervalChangeRespectsPerMinute(t *testing.T) {
	e, _ := newBudgetEnv(t, map[string]int64{entitlement.LimitMaxProbesPerMinute: 4})
	a := e.create(orgAdmin, e.icmpBody("10.94.2.1", `{"interval_s":30}`))
	e.create(orgAdmin, e.icmpBody("10.94.2.2", `{"interval_s":30}`))
	r := e.do(orgAdmin, http.MethodPatch, "/api/v1/monitoring/probes/"+a.ID, `{"params":{"interval_s":20},"target":"10.94.2.9"}`)
	if r.Code != http.StatusForbidden {
		t.Fatalf("20 s (5 checks per minute of 4): %d %s, want 403", r.Code, r.Body)
	}
	got := e.do(orgAdmin, http.MethodGet, "/api/v1/monitoring/probes/"+a.ID, "")
	if got.Probe.Target != "10.94.2.1" || string(got.Probe.Params) != `{"interval_s": 30}` && string(got.Probe.Params) != `{"interval_s":30}` {
		t.Errorf("after the refusal: target %s params %s, want unchanged", got.Probe.Target, got.Probe.Params)
	}
	if r := e.do(orgAdmin, http.MethodPatch, "/api/v1/monitoring/probes/"+a.ID, `{"params":{"interval_s":60}}`); r.Code != http.StatusOK {
		t.Errorf("60 s: %d %s, want 200", r.Code, r.Body)
	}
	// 3 checks per minute now: a third probe at 60 s reaches 4 exactly.
	e.create(orgAdmin, e.icmpBody("10.94.2.3", `{"interval_s":60}`))
	if r := e.do(orgAdmin, http.MethodPost, "/api/v1/monitoring/probes", e.icmpBody("10.94.2.4", `{"interval_s":3600}`)); r.Code != http.StatusForbidden {
		t.Errorf("above 4 per minute: %d %s, want 403", r.Code, r.Body)
	}
}

// TestResolveClampsToPlanMinimum covers AK4 and the 422 of AK2: a stored
// interval below min_probe_interval_s (stock after a plan change) resolves
// to the plan minimum with origin plan and clamped; a write below it is
// 422 on interval_s.
func TestResolveClampsToPlanMinimum(t *testing.T) {
	e, repo := newBudgetEnv(t, map[string]int64{entitlement.LimitMinProbeIntervalS: 30})
	if r := e.do(orgAdmin, http.MethodPost, "/api/v1/monitoring/probes", e.icmpBody("10.94.3.1", `{"interval_s":20,"timeout_s":5}`)); r.Code != http.StatusUnprocessableEntity ||
		r.Field != "interval_s" {
		t.Fatalf("10 s below the plan minimum: %d %s, want 422 on interval_s", r.Code, r.Body)
	}
	id := e.scalar(`INSERT INTO probe (organization_id, client_id, collector_group_id, type, target, params)
		VALUES ($1, $2, $3, 'icmp', '10.94.3.2', '{"interval_s": 10, "timeout_s": 5}') RETURNING id::text`, e.f.OrgA, e.f.Client1, e.group1)
	scope := database.OrgWideScope(e.f.OrgA, e.f.User)
	ctx := database.ContextWithTenantScope(context.Background(), &scope)
	resolved, err := repo.ResolveForGroup(ctx, e.group1)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 || resolved[0].Probe.ID != id {
		t.Fatalf("resolved %+v", resolved)
	}
	iv := resolved[0].Effective.IntervalS
	if iv.Int() != 30 || iv.Origin != probe.OriginPlan || !iv.Clamped {
		t.Errorf("interval %+v, want 30 with origin plan, clamped", iv)
	}
	p, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	layers, err := repo.Layers(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if eff := probe.Resolve(p, layers); eff.IntervalS.Origin != probe.OriginPlan || eff.IntervalS.Int() != 30 {
		t.Errorf("Layers: interval %+v, want the plan minimum", eff.IntervalS)
	}
	// The stock stays changeable: a change that leaves the interval passes.
	if r := e.do(orgAdmin, http.MethodPatch, "/api/v1/monitoring/probes/"+id, `{"target":"10.94.3.3"}`); r.Code != http.StatusOK {
		t.Errorf("target change of the stock: %d %s, want 200", r.Code, r.Body)
	}
}

// TestDisabledProbeIntervalBelowMinimum: a disabled probe cannot carry an
// interval below min_probe_interval_s past the check (PKG-04): its create
// and an interval change are 422 on interval_s, and enabling a disabled
// probe stored below the minimum is 422 as well; disabling stock passes.
func TestDisabledProbeIntervalBelowMinimum(t *testing.T) {
	e, _ := newBudgetEnv(t, map[string]int64{entitlement.LimitMinProbeIntervalS: 30})
	body := strings.Replace(e.icmpBody("10.94.4.1", `{"interval_s":10,"timeout_s":5}`), `"type"`, `"enabled":false,"type"`, 1)
	if r := e.do(orgAdmin, http.MethodPost, "/api/v1/monitoring/probes", body); r.Code != http.StatusUnprocessableEntity || r.Field != "interval_s" {
		t.Fatalf("disabled create at 10 s: %d %s, want 422 on interval_s", r.Code, r.Body)
	}
	id := e.scalar(`INSERT INTO probe (organization_id, client_id, collector_group_id, type, target, params, enabled)
		VALUES ($1, $2, $3, 'icmp', '10.94.4.2', '{"interval_s": 60, "timeout_s": 5}', false) RETURNING id::text`, e.f.OrgA, e.f.Client1, e.group1)
	if r := e.do(orgAdmin, http.MethodPatch, "/api/v1/monitoring/probes/"+id, `{"params":{"interval_s":10,"timeout_s":5}}`); r.Code != http.StatusUnprocessableEntity ||
		r.Field != "interval_s" {
		t.Fatalf("interval change of a disabled probe to 10 s: %d %s, want 422 on interval_s", r.Code, r.Body)
	}
	stock := e.scalar(`INSERT INTO probe (organization_id, client_id, collector_group_id, type, target, params, enabled)
		VALUES ($1, $2, $3, 'icmp', '10.94.4.3', '{"interval_s": 10, "timeout_s": 5}', false) RETURNING id::text`, e.f.OrgA, e.f.Client1, e.group1)
	if r := e.do(orgAdmin, http.MethodPatch, "/api/v1/monitoring/probes/"+stock, `{"enabled":true}`); r.Code != http.StatusUnprocessableEntity ||
		r.Field != "interval_s" {
		t.Fatalf("enabling a disabled probe stored at 10 s: %d %s, want 422 on interval_s", r.Code, r.Body)
	}
	running := e.scalar(`INSERT INTO probe (organization_id, client_id, collector_group_id, type, target, params)
		VALUES ($1, $2, $3, 'icmp', '10.94.4.4', '{"interval_s": 10, "timeout_s": 5}') RETURNING id::text`, e.f.OrgA, e.f.Client1, e.group1)
	if r := e.do(orgAdmin, http.MethodPatch, "/api/v1/monitoring/probes/"+running, `{"enabled":false}`); r.Code != http.StatusOK {
		t.Errorf("disabling stock below the minimum: %d %s, want 200", r.Code, r.Body)
	}
}
