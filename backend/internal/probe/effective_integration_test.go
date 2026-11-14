package probe_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
)

// Effective probe values on PostgreSQL (PRB-01, WP-937): the batch
// resolution of a collector's and a collector group's probes with a fixed
// number of statements, and the resolution in the principal's scope (RLS).

// countAll counts the statements of a call and returns those besides
// BEGIN and COMMIT.
func countAll(stmts []scopetest.LoggedStatement) (all int, data []string) {
	for _, s := range stmts {
		all++
		sql := strings.TrimSpace(s.SQL)
		if strings.HasPrefix(sql, "BEGIN") || strings.EqualFold(sql, "commit") {
			continue
		}
		data = append(data, sql)
	}
	return all, data
}

// TestResolveForCollectorBatch: 10,000 probes of one collector group (100
// CIs with 100 probes each, profiles on every level) resolve with at most
// five statements, BEGIN and COMMIT included, and within the time budget
// (AK5).
func TestResolveForCollectorBatch(t *testing.T) {
	e := newSchemaEnv(t)
	org := e.f.OrgA
	collector := e.scalar(`INSERT INTO collector (organization_id, name) VALUES ($1, 'wp937-batch') RETURNING id::text`, org)
	group := e.scalar(`SELECT group_id::text FROM collector_group_member WHERE collector_id = $1`, collector)
	e.mustExec(`INSERT INTO ci (organization_id, client_id, ci_type_id, name, status)
		SELECT $1, $2, (SELECT id FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1), 'wp937-ci-' || i, 'active'
		FROM generate_series(1, 100) i`, org, e.f.Client1)
	mgLow := e.scalar(`INSERT INTO monitoring_group (organization_id, name, priority) VALUES ($1, 'wp937-all', 1) RETURNING id::text`, org)
	mgHigh := e.scalar(`INSERT INTO monitoring_group (organization_id, name, priority) VALUES ($1, 'wp937-half', 2) RETURNING id::text`, org)
	e.mustExec(`INSERT INTO monitoring_group_member (group_id, ci_id, organization_id)
		SELECT $2, id, organization_id FROM ci WHERE organization_id = $1 AND name LIKE 'wp937-ci-%'`, org, mgLow)
	e.mustExec(`INSERT INTO monitoring_group_member (group_id, ci_id, organization_id)
		SELECT $2, id, organization_id FROM ci WHERE organization_id = $1 AND name LIKE 'wp937-ci-%' AND substring(name from 10)::int % 2 = 0`, org, mgHigh)
	ciOne := e.scalar(`SELECT id::text FROM ci WHERE organization_id = $1 AND name = 'wp937-ci-1'`, org)
	ciTwo := e.scalar(`SELECT id::text FROM ci WHERE organization_id = $1 AND name = 'wp937-ci-2'`, org)
	e.mustExec(`INSERT INTO monitoring_profile (organization_id, level, probe_type, params) VALUES ($1, 'org', 'icmp', '{"interval_s": 60, "down_count": 4}')`, org)
	e.mustExec(`INSERT INTO monitoring_profile (organization_id, level, client_id, probe_type, params) VALUES ($1, 'client', $2, 'icmp', '{"count": 4}')`, org, e.f.Client1)
	e.mustExec(`INSERT INTO monitoring_profile (organization_id, level, group_id, probe_type, params) VALUES ($1, 'group', $2, 'icmp', '{"interval_s": 90}')`, org, mgLow)
	e.mustExec(`INSERT INTO monitoring_profile (organization_id, level, group_id, probe_type, params) VALUES ($1, 'group', $2, 'icmp', '{"interval_s": 120}')`, org, mgHigh)
	e.mustExec(`INSERT INTO monitoring_profile (organization_id, level, ci_id, probe_type, params) VALUES ($1, 'ci', $2, 'icmp', '{"interval_s": 300}')`, org, ciOne)
	e.mustExec(`INSERT INTO probe (organization_id, ci_id, collector_group_id, type, target, params)
		SELECT $1, c.id, $2, 'icmp', '10.' || substring(c.name from 10) || '.' || j || '.1',
		       CASE WHEN j = 1 THEN '{"timeout_s": 5}'::jsonb ELSE '{}'::jsonb END
		FROM ci c CROSS JOIN generate_series(1, 100) j
		WHERE c.organization_id = $1 AND c.name LIKE 'wp937-ci-%'`, org, group)
	e.mustExec(`INSERT INTO probe_assignment (probe_id, organization_id, collector_id, epoch)
		SELECT id, organization_id, $2, 3 FROM probe WHERE organization_id = $1 AND collector_group_id = $3`, org, collector, group)
	e.mustExec(`ANALYZE probe, probe_assignment, monitoring_profile, monitoring_group_member, monitoring_group`)

	trace := scopetest.NewQueryTrace(t, e.f.App)
	repo := probe.NewPGRepository(trace.Pool)
	ctx := e.f.OrgCtx(org)

	check := func(name string, got []probe.Resolved, wantEpoch int64) {
		t.Helper()
		if len(got) != 10000 {
			t.Fatalf("%s: %d probes, want 10000", name, len(got))
		}
		for _, r := range got {
			eff := r.Effective
			if r.Epoch != wantEpoch {
				t.Fatalf("%s: epoch %d, want %d", name, r.Epoch, wantEpoch)
			}
			if f := eff.Params["count"]; f.Origin != probe.LevelClient {
				t.Fatalf("%s: count origin %q, want client", name, f.Origin)
			}
			if eff.DownCount.Int() != 4 || eff.DownCount.Origin != probe.LevelOrg {
				t.Fatalf("%s: down_count %+v, want 4 from org", name, eff.DownCount)
			}
			wantInterval, wantOrigin, wantGroup := int64(90), probe.LevelGroup, mgLow
			switch r.Probe.CIID {
			case ciOne:
				wantInterval, wantOrigin, wantGroup = 300, probe.LevelCI, ""
			case ciTwo:
				wantInterval, wantGroup = 120, mgHigh
			}
			if r.Probe.CIID == ciOne || r.Probe.CIID == ciTwo {
				if eff.IntervalS.Int() != wantInterval || eff.IntervalS.Origin != wantOrigin || eff.IntervalS.GroupID != wantGroup {
					t.Fatalf("%s: interval of %s = %+v, want %d from %s %s", name, r.Probe.Target, eff.IntervalS, wantInterval, wantOrigin, wantGroup)
				}
			}
			if strings.HasSuffix(r.Probe.Target, ".1.1") && (eff.TimeoutS.Int() != 5 || eff.TimeoutS.Origin != probe.LevelProbe) {
				t.Fatalf("%s: timeout of %s = %+v, want 5 from probe", name, r.Probe.Target, eff.TimeoutS)
			}
		}
	}

	for _, c := range []struct {
		name  string
		epoch int64
		run   func() ([]probe.Resolved, error)
	}{
		{"collector", 3, func() ([]probe.Resolved, error) { return repo.ResolveForCollector(ctx, collector) }},
		{"group", 0, func() ([]probe.Resolved, error) { return repo.ResolveForGroup(ctx, group) }},
	} {
		// A warm-up call opens the pooled connection, so its setup (SET
		// ROLE) is not counted.
		if _, err := c.run(); err != nil {
			t.Fatal(err)
		}
		var got []probe.Resolved
		stmts := trace.Run(t, func() error {
			var err error
			got, err = c.run()
			return err
		})
		all, data := countAll(stmts)
		if all > 5 {
			t.Errorf("%s: %d statements, want at most 5: %q", c.name, all, data)
		}
		check(c.name, got, c.epoch)
		scopetest.TimeBudget(t, "resolve 10,000 probes for the "+c.name, 3, 500*time.Millisecond, func() error {
			_, err := c.run()
			return err
		})
	}
}

// TestResolveRespectsClientScope: a principal restricted to a client
// resolves only its client's probes and never applies the profiles of
// another client (RLS on probe, monitoring_profile and the memberships).
func TestResolveRespectsClientScope(t *testing.T) {
	e := newSchemaEnv(t)
	org := e.f.OrgA
	group := e.scalar(`INSERT INTO collector_group (organization_id, name) VALUES ($1, 'wp937-shared') RETURNING id::text`, org)
	ci1 := e.f.CI(t, org, e.f.Client1, "wp937-c1")
	ci2 := e.f.CI(t, org, e.f.Client2, "wp937-c2")
	p1 := e.scalar(`INSERT INTO probe (organization_id, ci_id, collector_group_id, type, target) VALUES ($1, $2, $3, 'icmp', '192.0.2.1') RETURNING id::text`, org, ci1, group)
	p2 := e.scalar(`INSERT INTO probe (organization_id, ci_id, collector_group_id, type, target) VALUES ($1, $2, $3, 'icmp', '192.0.2.2') RETURNING id::text`, org, ci2, group)
	mg2 := e.scalar(`INSERT INTO monitoring_group (organization_id, client_id, name, priority) VALUES ($1, $2, 'wp937-client2', 9) RETURNING id::text`, org, e.f.Client2)
	e.mustExec(`INSERT INTO monitoring_group_member (group_id, ci_id, organization_id) VALUES ($1, $2, $3)`, mg2, ci2, org)
	e.mustExec(`INSERT INTO monitoring_profile (organization_id, level, probe_type, params) VALUES ($1, 'org', 'icmp', '{"down_count": 3}')`, org)
	e.mustExec(`INSERT INTO monitoring_profile (organization_id, level, client_id, probe_type, params) VALUES ($1, 'client', $2, 'icmp', '{"interval_s": 60}')`, org, e.f.Client1)
	e.mustExec(`INSERT INTO monitoring_profile (organization_id, level, client_id, probe_type, params) VALUES ($1, 'client', $2, 'icmp', '{"interval_s": 120, "count": 5}')`, org, e.f.Client2)
	e.mustExec(`INSERT INTO monitoring_profile (organization_id, level, group_id, probe_type, params) VALUES ($1, 'group', $2, 'icmp', '{"up_count": 2}')`, org, mg2)
	e.mustExec(`INSERT INTO monitoring_profile (organization_id, level, ci_id, probe_type, params) VALUES ($1, 'ci', $2, 'icmp', '{"timeout_s": 4}')`, org, ci2)

	repo := probe.NewPGRepository(e.f.App)

	// The org-wide principal sees both probes with their own client's values.
	all, err := repo.ResolveForGroup(e.f.OrgCtx(org), group)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("org principal resolved %d probes, want 2", len(all))
	}
	for _, r := range all {
		if r.Probe.ID == p2 && (r.Effective.IntervalS.Int() != 120 || r.Effective.UpCount.Origin != probe.LevelGroup || r.Effective.TimeoutS.Origin != probe.LevelCI) {
			t.Errorf("probe of client 2 = %+v", r.Effective)
		}
	}

	// The client principal resolves its probe only, with its client's
	// profile and the org-wide one.
	c1 := e.f.ClientCtx(e.f.Client1)
	got, err := repo.ResolveForGroup(c1, group)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Probe.ID != p1 {
		t.Fatalf("client principal resolved %v, want only %s", got, p1)
	}
	eff := got[0].Effective
	if eff.IntervalS.Int() != 60 || eff.IntervalS.Origin != probe.LevelClient || eff.DownCount.Origin != probe.LevelOrg {
		t.Errorf("probe of client 1 = %+v", eff)
	}
	if f := eff.Params["count"]; f.Origin != probe.OriginDefault {
		t.Errorf("count of client 1 from %q, want default", f.Origin)
	}

	// Asked for the levels of the other client's probe, the client
	// principal gets none of its profiles: RLS hides them.
	if _, getErr := repo.Get(c1, p2); !errors.Is(getErr, probe.ErrNotFound) {
		t.Errorf("get foreign probe: %v, want ErrNotFound", getErr)
	}
	foreign := &probe.Probe{ID: p2, Type: "icmp", ClientID: e.f.Client2, CIID: ci2}
	layers, err := repo.Layers(c1, foreign)
	if err != nil {
		t.Fatal(err)
	}
	if layers.Client != nil || layers.CI != nil || len(layers.Groups) != 0 || layers.Org == nil {
		t.Errorf("client principal sees foreign levels: %+v", layers)
	}

	// Another organization resolves nothing.
	none, err := repo.ResolveForGroup(e.f.OrgCtx(e.f.OrgB), group)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Errorf("organization B resolved %d probes of A", len(none))
	}
	// Without a tenant scope there is no read.
	if _, err := repo.ResolveForGroup(context.Background(), group); err == nil {
		t.Error("resolution without tenant scope succeeded")
	}
}
