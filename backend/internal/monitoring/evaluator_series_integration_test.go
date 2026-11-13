package monitoring_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The evaluator per series and rule scope on PostgreSQL (ALR-05, WP-984):
// LatestBySeries with the scope predicates (types with subtypes, client,
// labels), the alarms of the series through alarm.RuleSink, the pending
// start that survives a new evaluator, and the budget of the series query.

type evalEnv struct {
	t     *testing.T
	f     *scopetest.Fixture
	store *monitoring.PGMetricStore
	svc   *alarm.Service
	now   time.Time
}

func newEvalEnv(t *testing.T) *evalEnv {
	t.Helper()
	f := scopetest.Seed(t, "ef")
	t.Cleanup(func() {
		// metric_sample has no foreign key to the organization.
		if _, err := f.Admin.Exec(context.Background(), `DELETE FROM metric_sample WHERE organization_id = ANY($1::uuid[])`,
			[]string{f.OrgA, f.OrgB}); err != nil {
			t.Errorf("cleanup metric samples: %v", err)
		}
	})
	return &evalEnv{t: t, f: f, store: monitoring.NewPGMetricStore(f.App), svc: alarm.NewPGService(f.App),
		now: time.Now().UTC().Truncate(time.Second)}
}

// rule stores an enabled rule of organization A (gt 10 on metric).
func (e *evalEnv) rule(metric string, duration time.Duration, scope monitoring.RuleScope, groupBy []string) monitoring.AlertRule {
	e.t.Helper()
	r, err := e.store.AlertStore().CreateRule(e.f.OrgCtx(e.f.OrgA), &monitoring.AlertRule{OrgID: e.f.OrgA, Name: "wp984 " + metric,
		MetricName: metric, Condition: "gt", Threshold: 10, Duration: duration, Severity: "critical", Enabled: true,
		Scope: scope, GroupBy: groupBy})
	if err != nil {
		e.t.Fatalf("create rule: %v", err)
	}
	return r
}

func (e *evalEnv) sample(ci, metric string, value float64, labels map[string]string, at time.Time) {
	e.t.Helper()
	if err := e.store.Ingest(e.f.OrgCtx(e.f.OrgA), []monitoring.Metric{{OrgID: e.f.OrgA, CIID: ci, Name: metric, Value: value,
		Labels: labels, Timestamp: at}}); err != nil {
		e.t.Fatalf("ingest: %v", err)
	}
}

// evaluator is a new evaluator at the env's clock.
func (e *evalEnv) evaluator() *monitoring.Evaluator {
	ev := monitoring.NewEvaluator(e.store, e.store.AlertStore(), alarm.NewRuleSink(e.svc))
	ev.Now = func() time.Time { return e.now }
	return ev
}

// alarmsOf lists the alarms of a rule that are not resolved: CI → state.
func (e *evalEnv) alarmsOf(ruleID string) map[string]alarm.Alarm {
	e.t.Helper()
	page, err := e.svc.Store().List(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, &alarm.ListFilter{SourceID: ruleID,
		States: []string{alarm.StatePending, alarm.StateOpen, alarm.StateAcknowledged}, Limit: 1000})
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]alarm.Alarm{}
	for _, a := range page.Items {
		out[*a.CIID] = a
	}
	return out
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sorted(ids ...string) []string {
	out := slices.Clone(ids)
	sort.Strings(out)
	return out
}

func TestEvaluatorScopeCITypeIncludesSubtypes(t *testing.T) {
	e := newEvalEnv(t)
	types := citype.NewPGRepository(e.f.App)
	ctx := tenant.WithTenant(e.f.OrgCtx(e.f.OrgA), tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: e.f.User})
	create := func(key, parent string) string {
		typ := &citype.Type{OrganizationID: e.f.OrgA, Key: key, Name: key, ParentTypeID: parent, IsObservable: true}
		if err := types.Create(ctx, typ); err != nil {
			t.Fatalf("create type %s: %v", key, err)
		}
		return typ.ID
	}
	parent := create("wp984-switch", "")
	child := create("wp984-core-switch", parent)
	grandchild := create("wp984-spine", child)
	other := create("wp984-ups", "")
	ci := func(typeID, name string) string {
		id := e.f.ID()
		if _, err := e.f.Admin.Exec(context.Background(), `INSERT INTO ci (id, organization_id, client_id, ci_type_id, name, status)
			VALUES ($1, $2, $3, $4, $5, 'active')`, id, e.f.OrgA, e.f.Client1, typeID, name); err != nil {
			t.Fatal(err)
		}
		return id
	}
	p, c, g, o := ci(parent, "wp984-p"), ci(child, "wp984-c"), ci(grandchild, "wp984-g"), ci(other, "wp984-o")
	for _, id := range []string{p, c, g, o} {
		e.sample(id, "wp984_errors", 50, nil, e.now.Add(-time.Minute))
	}
	rule := e.rule("wp984_errors", 0, monitoring.RuleScope{CITypeIDs: []string{child}}, nil)
	e.evaluator().Evaluate(context.Background())
	if got, want := keys(e.alarmsOf(rule.ID)), sorted(c, g); !slices.Equal(got, want) {
		t.Errorf("scope type core-switch: alarms of %v, want the type and its subtype %v", got, want)
	}
	// A type of the scope that the organization does not see is 422.
	_, err := e.store.AlertStore().CreateRule(e.f.OrgCtx(e.f.OrgA), &monitoring.AlertRule{OrgID: e.f.OrgA, Name: "wp984 bad",
		MetricName: "x", Condition: "gt", Severity: "warning", Scope: monitoring.RuleScope{CITypeIDs: []string{parent, e.f.ID()}}})
	if err == nil || !containsField(err, "scope.ci_type_ids[1]") {
		t.Errorf("unknown type in the scope: %v, want 422 scope.ci_type_ids[1]", err)
	}
}

func TestEvaluatorScopeClientAndLabels(t *testing.T) {
	e := newEvalEnv(t)
	c1 := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp984-client1")
	c2 := e.f.CI(t, e.f.OrgA, e.f.Client2, "wp984-client2")
	for _, id := range []string{c1, c2} {
		e.sample(id, "wp984_if", 50, map[string]string{"ifIndex": "1", "role": "uplink"}, e.now.Add(-time.Minute))
		e.sample(id, "wp984_if", 50, map[string]string{"ifIndex": "2", "role": "access"}, e.now.Add(-time.Minute))
	}
	byClient := e.rule("wp984_if", 0, monitoring.RuleScope{ClientID: e.f.Client1}, []string{"ifIndex"})
	byLabel := e.rule("wp984_if", 0, monitoring.RuleScope{Labels: map[string]string{"role": "uplink"}}, []string{"ifIndex"})
	e.evaluator().Evaluate(context.Background())
	page, err := e.svc.Store().List(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, &alarm.ListFilter{SourceID: byClient.ID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("client scope: %d alarms, want the two interfaces of the client's CI", len(page.Items))
	}
	for _, a := range page.Items {
		if *a.CIID != c1 || a.DedupKey != monitoring.RuleDedupKey(byClient.ID, monitoring.SeriesKey(c1, []string{"ifIndex"}, a.Labels)) {
			t.Errorf("client scope alarm %s on %s, want CI %s", a.DedupKey, *a.CIID, c1)
		}
	}
	got := e.alarmsOf(byLabel.ID)
	if !slices.Equal(keys(got), sorted(c1, c2)) {
		t.Errorf("label scope: alarms of %v, want both CIs once (uplink only)", keys(got))
	}
	for _, a := range got {
		if a.Labels["ifIndex"] != "1" {
			t.Errorf("label scope alarm with labels %v, want ifIndex 1", a.Labels)
		}
	}
	// A client of the scope that does not exist is 422.
	_, err = e.store.AlertStore().CreateRule(e.f.OrgCtx(e.f.OrgA), &monitoring.AlertRule{OrgID: e.f.OrgA, Name: "wp984 bad",
		MetricName: "x", Condition: "gt", Severity: "warning", Scope: monitoring.RuleScope{ClientID: e.f.ID()}})
	if err == nil || !containsField(err, "scope.client_id") {
		t.Errorf("unknown client in the scope: %v, want 422 scope.client_id", err)
	}
}

func TestEvaluatorRestartKeepsPending(t *testing.T) {
	e := newEvalEnv(t)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp984-restart")
	rule := e.rule("wp984_temp", 5*time.Minute, monitoring.RuleScope{}, nil)
	breach := e.now.Add(-10 * time.Second)
	e.sample(ci, "wp984_temp", 80, nil, breach)
	e.evaluator().Evaluate(context.Background())
	first := e.alarmsOf(rule.ID)[ci]
	if first.State != alarm.StatePending || !first.FirstSeenAt.Equal(breach) {
		t.Fatalf("breach: %s since %v, want pending since %v", first.State, first.FirstSeenAt, breach)
	}
	// A new evaluator (restart) three minutes later: still pending from the
	// original start.
	e.now = e.now.Add(3 * time.Minute)
	e.sample(ci, "wp984_temp", 81, nil, e.now.Add(-time.Second))
	e.evaluator().Evaluate(context.Background())
	again := e.alarmsOf(rule.ID)[ci]
	if again.ID != first.ID || again.State != alarm.StatePending || !again.FirstSeenAt.Equal(breach) {
		t.Fatalf("after the restart: %+v, want the same pending alarm from %v", again, breach)
	}
	// Another new evaluator after the original deadline opens it, once.
	e.now = breach.Add(5*time.Minute + time.Second)
	e.sample(ci, "wp984_temp", 82, nil, e.now.Add(-time.Second))
	e.evaluator().Evaluate(context.Background())
	e.evaluator().Evaluate(context.Background())
	opened := e.alarmsOf(rule.ID)[ci]
	if opened.ID != first.ID || opened.State != alarm.StateOpen {
		t.Fatalf("after the duration: %+v, want the alarm open", opened)
	}
	trs, err := e.svc.Store().Transitions(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, first.ID)
	if err != nil || len(trs) != 2 || trs[1].ToState != alarm.StateOpen {
		t.Errorf("transitions %+v, %v: want pending and one open", trs, err)
	}
	// The return below the threshold resolves it.
	e.now = e.now.Add(time.Minute)
	e.sample(ci, "wp984_temp", 5, nil, e.now.Add(-time.Second))
	e.evaluator().Evaluate(context.Background())
	if len(e.alarmsOf(rule.ID)) != 0 {
		t.Error("the alarm stayed after the return below the threshold")
	}
}

// containsField reports whether err is a 422 of the field.
func containsField(err error, field string) bool {
	var fe *api.FieldError
	return errors.As(err, &fe) && fe.Field == field && fe.Status == http.StatusUnprocessableEntity
}

// benchOrg is the organization of BenchmarkLatestBySeries.
const benchOrg = "e9840000-0000-4000-8000-0000000000be"

// BenchmarkLatestBySeries reads the latest sample of 10,000 series (100 CIs
// with 100 interfaces each, three samples per series) per rule through
// metric_sample_v; the budget is 500 ms for the slowest read. It runs on
// the maintenance pool in the tenant transaction of its own organization.
//
//	cd backend && TEST_DATABASE_URL=… go test -run '^$' -bench BenchmarkLatestBySeries ./internal/monitoring/
func BenchmarkLatestBySeries(b *testing.B) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		b.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL benchmark")
	}
	ctx := context.Background()
	pool, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		b.Fatal(err)
	}
	defer pool.Close()
	cleanup := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM metric_sample WHERE organization_id = $1`, benchOrg)
		_, _ = pool.Exec(ctx, `DELETE FROM organization WHERE id = $1`, benchOrg)
	}
	cleanup()
	b.Cleanup(cleanup)
	if _, err = pool.Exec(ctx, `INSERT INTO organization (id, name, slug) VALUES ($1, 'wp984-bench', 'wp984-bench')`, benchOrg); err != nil {
		b.Fatal(err)
	}
	now := time.Now().UTC()
	var cis []string
	if err = pool.QueryRow(ctx, `WITH t AS (SELECT id FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1),
		ins AS (INSERT INTO ci (organization_id, ci_type_id, name, status)
		        SELECT $1, t.id, 'wp984-bench-' || g, 'active' FROM t, generate_series(1, 100) AS g RETURNING id)
		SELECT array_agg(id::text) FROM ins`, benchOrg).Scan(&cis); err != nil {
		b.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO metric_sample (time, organization_id, ci_id, metric_name, value, labels)
		SELECT $2::timestamptz - make_interval(secs => s * 60), $1, c, 'wp984_bench', random() * 100,
		       jsonb_build_object('ifIndex', i::text)
		  FROM unnest($3::uuid[]) AS c, generate_series(1, 100) AS i, generate_series(0, 2) AS s`,
		benchOrg, now, cis); err != nil {
		b.Fatal(err)
	}
	store := monitoring.NewPGMetricStore(pool)
	scope := database.OrgWideScope(benchOrg, "")
	sctx := database.ContextWithTenantScope(ctx, &scope)
	q := &monitoring.SeriesQuery{OrgID: benchOrg, Name: "wp984_bench", From: now.Add(-15 * time.Minute), To: now,
		GroupBy: []string{"ifIndex"}}
	b.ResetTimer()
	var worst time.Duration
	for range b.N {
		start := time.Now()
		points, err := store.LatestBySeries(sctx, q)
		if err != nil {
			b.Fatal(err)
		}
		if len(points) != 10000 {
			b.Fatalf("%d series, want 10000", len(points))
		}
		worst = max(worst, time.Since(start))
	}
	b.ReportMetric(float64(worst.Milliseconds()), "worst-ms")
	if worst > 500*time.Millisecond {
		b.Errorf("slowest read %v, budget 500ms", worst)
	}
}
