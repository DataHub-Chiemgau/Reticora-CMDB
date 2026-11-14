package probe_test

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Retest (WP-948, PRB-11, PRB-01):
//
//	cd backend && go test -count=1 -p 1 -run 'TestDistribution|TestAssignmentChangePushesProbesChanged|TestCredentialBindingChangeRaisesVersion' ./internal/probe/

// distEnv is organization A with an org-wide collector group of one active
// collector and the distribution on PostgreSQL.
type distEnv struct {
	t         *testing.T
	f         *scopetest.Fixture
	group     string
	collector string
	dist      *probe.Distributor
}

func newDistEnv(t *testing.T) *distEnv {
	t.Helper()
	f := scopetest.Seed(t, "93")
	e := &distEnv{t: t, f: f}
	e.group = e.scalar(`INSERT INTO collector_group (organization_id, name) VALUES ($1, 'wp948') RETURNING id::text`, f.OrgA)
	e.collector = e.scalar(`INSERT INTO collector (organization_id, name, status) VALUES ($1, 'wp948', 'active') RETURNING id::text`, f.OrgA)
	e.exec(`UPDATE collector_group_member SET group_id = $1, last_alive_at = now() WHERE collector_id = $2`, e.group, e.collector)
	check := discovery.NewHandler(discovery.NewPGRepository(f.App), nil).CheckCollectorChannel
	e.dist = probe.NewDistributor(probe.NewPGRepository(f.App), check)
	return e
}

func (e *distEnv) scalar(sql string, args ...any) string {
	e.t.Helper()
	var out string
	if err := e.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return out
}

func (e *distEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.f.Admin.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

// assign runs the assignment engine for organization A.
func (e *distEnv) assign(n probe.AssignmentNotifier) {
	e.t.Helper()
	a := probe.NewAssigner(e.f.App)
	if n != nil {
		a.WithNotifier(n)
	}
	if _, err := a.RunOrg(context.Background(), e.f.OrgA); err != nil {
		e.t.Fatal(err)
	}
}

// fetch gets every page of the current set.
func (e *distEnv) fetch() ([]probewire.ProbeSpec, []probewire.SkippedProbe, int64) {
	e.t.Helper()
	var (
		specs   []probewire.ProbeSpec
		skipped []probewire.SkippedProbe
		version int64
	)
	for page := 1; page != 0; {
		got := e.dist.Answer(context.Background(), e.f.OrgA, e.collector,
			[]byte(`{"page":`+itoa(page)+`,"version":`+itoa64(version)+`}`))
		set, ok := got.(probewire.AssignmentSet)
		if !ok || set.VersionChanged {
			e.t.Fatalf("page %d: %#v", page, got)
		}
		version = set.Version
		specs = append(specs, set.Probes...)
		skipped = append(skipped, set.Skipped...)
		page = set.NextPage
	}
	return specs, skipped, version
}

func itoa(n int) string     { return strconv.Itoa(n) }
func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

// statements returns the statements of log since mark without the
// transaction control (BEGIN with the GUCs of the scope, COMMIT, ROLLBACK).
func statements(log *scopetest.StatementLog, mark int) []string {
	var out []string
	for _, st := range log.Since(mark) {
		sql := strings.ToLower(strings.TrimSpace(st.SQL))
		if strings.HasPrefix(sql, "begin") || sql == "commit" || sql == "rollback" {
			continue
		}
		out = append(out, st.SQL)
	}
	return out
}

func TestDistributionTenThousandProbesUnderOneSecond(t *testing.T) {
	e := newDistEnv(t)
	// The production path on a traced pool: collector check, feature
	// monitoring, plan minimum (budget), profiles of every level, CI-bound
	// probes and probe credentials checked against the real bundle.
	trace := scopetest.NewQueryTrace(t, e.f.App)
	svc := entitlement.NewService(entitlement.NewPGRepository(trace.Pool), entitlement.Options{Enforce: true})
	if _, err := svc.Grant(e.f.OrgCtx(e.f.OrgA), &entitlement.Entitlement{OrganizationID: e.f.OrgA, FeatureKey: entitlement.FeatureMonitoring,
		Plan: entitlement.PlanStandard, Enabled: true, Limits: map[string]int64{entitlement.LimitMinProbeIntervalS: 20}}); err != nil {
		t.Fatal(err)
	}
	repo := probe.NewPGRepository(trace.Pool)
	repo.SetBudget(svc)
	check := discovery.NewHandler(discovery.NewPGRepository(trace.Pool), nil).CheckCollectorChannel
	e.dist = probe.NewDistributor(repo, check).WithFeatures(svc).WithBundle(discovery.NewBundleGate(trace.Pool, nil).CredentialIDs)
	cis := make([]string, 10)
	for i := range cis {
		cis[i] = e.f.CI(t, e.f.OrgA, e.f.Client1, "wp948-"+itoa(i))
	}
	e.exec(`INSERT INTO probe (organization_id, client_id, ci_id, collector_group_id, type, target)
		SELECT $1, $2, ($4::uuid[])[1 + g % 10], $3, 'icmp', '10.' || (g / 65536) || '.' || (g / 256 % 256) || '.' || (g % 256)
		FROM generate_series(1, 10000) g`, e.f.OrgA, e.f.Client1, e.group, cis)
	e.exec(`INSERT INTO monitoring_profile (organization_id, level, probe_type, params) VALUES ($1, 'org', 'icmp', '{"interval_s": 60}')`, e.f.OrgA)
	e.exec(`INSERT INTO monitoring_profile (organization_id, level, client_id, probe_type, params) VALUES ($1, 'client', $2, 'icmp', '{"down_count": 4}')`,
		e.f.OrgA, e.f.Client1)
	e.exec(`INSERT INTO monitoring_profile (organization_id, level, ci_id, probe_type, params) VALUES ($1, 'ci', $2, 'icmp', '{"interval_s": 45}')`,
		e.f.OrgA, cis[0])
	cred := e.f.ID()
	e.exec(`INSERT INTO credential (id, organization_id, name, kind, ciphertext) VALUES ($1, $2, 'wp948-10k', 'api_token', '\x00')`, cred, e.f.OrgA)
	e.exec(`INSERT INTO credential_binding (organization_id, credential_id, target_kind, probe_id, namespace, priority)
		SELECT organization_id, $2, 'probe', id, 'api', 1 FROM probe WHERE organization_id = $1 AND collector_group_id = $3 LIMIT 100`,
		e.f.OrgA, cred, e.group)
	e.assign(nil)
	e.exec(`ANALYZE probe, probe_assignment, credential_binding, monitoring_profile`)
	// The entitlements come from the cache of the service (ENT-03, 60 s),
	// read once per organization and minute; warmed here, they are not
	// part of the statements of a page.
	if _, _, err := svc.Status(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, entitlement.FeatureMonitoring); err != nil {
		t.Fatal(err)
	}
	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Skip("TEST_NATS_URL not set")
	}
	nc, err := nats.Connect(url, nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = e.dist.ServeProbeRequests(ctx, nc) }()
	subject := "collector." + e.f.OrgA + "." + e.collector + discovery.ProbesRequestSuffix
	if err = nc.Flush(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	total, credentials, version, pages := 0, 0, int64(0), 0
	perPage := []int{}
	for page := 1; page != 0; pages++ {
		mark := trace.Log.Mark()
		body, _ := json.Marshal(probewire.AssignmentRequest{Page: page, Version: version})
		msg, rerr := nc.Request(subject, body, 5*time.Second)
		if rerr != nil {
			t.Fatal(rerr)
		}
		var set probewire.AssignmentSet
		if uerr := json.Unmarshal(msg.Data, &set); uerr != nil {
			t.Fatal(uerr)
		}
		if len(msg.Data) > probewire.MaxPageBytes {
			t.Errorf("page %d: %d bytes", page, len(msg.Data))
		}
		stmts := statements(trace.Log, mark)
		perPage = append(perPage, len(stmts))
		if len(stmts) > 5 {
			t.Errorf("page %d: %d statements, want at most 5:\n%s", page, len(stmts), strings.Join(stmts, "\n---\n"))
		}
		for i := range set.Probes {
			if len(set.Probes[i].CredentialIDs) > 0 {
				credentials++
			}
		}
		if len(set.Skipped) > 0 {
			t.Errorf("page %d skipped %+v, want every credential in the bundle", page, set.Skipped)
		}
		total += len(set.Probes)
		version, page = set.Version, set.NextPage
	}
	elapsed := time.Since(start)
	if total != 10000 || credentials != 100 {
		t.Fatalf("%d probes (%d with credentials) on %d pages, want 10000 (100)", total, credentials, pages)
	}
	if elapsed >= time.Second {
		t.Errorf("all pages took %v, want < 1 s", elapsed)
	}
	t.Logf("10,000 probes on %d pages in %v, statements per page %v", pages, elapsed, perPage)
	// The same version again: not modified.
	body, _ := json.Marshal(probewire.AssignmentRequest{IfNoneMatch: version, Page: 1})
	msg, err := nc.Request(subject, body, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(msg.Data), `"not_modified":true`) {
		t.Errorf("if_none_match: %s", msg.Data)
	}
}

func TestDistributionCarriesCredentialIDsOnly(t *testing.T) {
	e := newDistEnv(t)
	const canary = "wp948-canary-secret"
	cred := e.f.ID()
	e.exec(`INSERT INTO credential (id, organization_id, name, kind, ciphertext) VALUES ($1, $2, 'wp948', 'api_token', convert_to($3, 'UTF8'))`,
		cred, e.f.OrgA, canary)
	// A vault login never enters a bundle (WP-893).
	missing := e.f.ID()
	e.exec(`INSERT INTO credential (id, organization_id, name, kind, ciphertext) VALUES ($1, $2, 'wp948-missing', 'vault_token', '\x00')`,
		missing, e.f.OrgA)
	with := e.scalar(`INSERT INTO probe (organization_id, client_id, collector_group_id, type, target) VALUES ($1, $2, $3, 'tcp', '10.99.0.1:443')
		RETURNING id::text`, e.f.OrgA, e.f.Client1, e.group)
	lost := e.scalar(`INSERT INTO probe (organization_id, client_id, collector_group_id, type, target) VALUES ($1, $2, $3, 'tcp', '10.99.0.2:443')
		RETURNING id::text`, e.f.OrgA, e.f.Client1, e.group)
	e.exec(`INSERT INTO credential_binding (organization_id, credential_id, target_kind, probe_id, namespace, priority) VALUES ($1, $2, 'probe', $3, 'api', 1)`,
		e.f.OrgA, cred, with)
	e.exec(`INSERT INTO credential_binding (organization_id, credential_id, target_kind, probe_id, namespace, priority) VALUES ($1, $2, 'probe', $3, 'api', 1)`,
		e.f.OrgA, missing, lost)
	e.assign(nil)
	// The bundle source of the worker (workers.go): the ids the bundle of
	// the collector holds, its probe bindings included.
	gate := discovery.NewBundleGate(e.f.App, nil)
	e.dist.WithBundle(gate.CredentialIDs)
	ids, err := gate.CredentialIDs(context.Background(), e.f.OrgA, e.collector)
	if err != nil || len(ids) != 1 || ids[0] != cred {
		t.Fatalf("bundle credentials %v (%v), want the bound api token only", ids, err)
	}
	got := e.dist.Answer(context.Background(), e.f.OrgA, e.collector, nil)
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), canary) || strings.Contains(string(raw), "ciphertext") || strings.Contains(string(raw), "secret") {
		t.Fatalf("the set carries a secret: %s", raw)
	}
	set := got.(probewire.AssignmentSet)
	if len(set.Probes) != 1 || set.Probes[0].ID != with || len(set.Probes[0].CredentialIDs) != 1 || set.Probes[0].CredentialIDs[0] != cred {
		t.Errorf("probes %+v, want the probe with its credential id", set.Probes)
	}
	if len(set.Skipped) != 1 || set.Skipped[0].ProbeID != lost || set.Skipped[0].Reason != probewire.SkipCredentialMissing {
		t.Errorf("skipped %+v, want the probe of the missing credential", set.Skipped)
	}
}

// TestCredentialBindingChangeRaisesVersion: a binding of an assigned probe
// raises the version of its collector (migration 000346), so the set with
// the new credential ids is not answered with not_modified.
func TestCredentialBindingChangeRaisesVersion(t *testing.T) {
	e := newDistEnv(t)
	e.dist.WithBundle(discovery.NewBundleGate(e.f.App, nil).CredentialIDs)
	id := e.scalar(`INSERT INTO probe (organization_id, client_id, collector_group_id, type, target) VALUES ($1, $2, $3, 'tcp', '10.99.4.1:443')
		RETURNING id::text`, e.f.OrgA, e.f.Client1, e.group)
	e.assign(nil)
	specs, _, before := e.fetch()
	if len(specs) != 1 || len(specs[0].CredentialIDs) != 0 {
		t.Fatalf("set %+v", specs)
	}
	cred := e.f.ID()
	e.exec(`INSERT INTO credential (id, organization_id, name, kind, ciphertext) VALUES ($1, $2, 'wp948-v', 'api_token', '\x00')`, cred, e.f.OrgA)
	// Bound by a client-scoped writer: the org-wide collector group is not
	// in its scope, the version rises all the same.
	scope, _ := database.TenantScopeFromContext(e.f.ClientCtx(e.f.Client1))
	if err := database.WithTenant(context.Background(), e.f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO credential_binding (organization_id, credential_id, target_kind, probe_id, namespace, priority)
			VALUES ($1, $2, 'probe', $3, 'api', 1)`, e.f.OrgA, cred, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	got := e.dist.Answer(context.Background(), e.f.OrgA, e.collector, []byte(`{"if_none_match":`+itoa64(before)+`}`))
	set, ok := got.(probewire.AssignmentSet)
	if !ok || set.Version <= before || len(set.Probes) != 1 || len(set.Probes[0].CredentialIDs) != 1 || set.Probes[0].CredentialIDs[0] != cred {
		t.Fatalf("after the binding: %#v, want a new version with the credential id", got)
	}
	// Removing the binding raises the version again.
	e.exec(`DELETE FROM credential_binding WHERE probe_id = $1`, id)
	got = e.dist.Answer(context.Background(), e.f.OrgA, e.collector, []byte(`{"if_none_match":`+itoa64(set.Version)+`}`))
	if again, ok := got.(probewire.AssignmentSet); !ok || again.Version <= set.Version || len(again.Probes[0].CredentialIDs) != 0 {
		t.Errorf("after the delete: %#v, want a new version without credential ids", got)
	}
}

// TestAssignmentSetCarriesTrapConfig covers WP-977 AK3: the set carries
// the trap configuration of the collector's group with the credential ids
// only, never a secret; the credentials enter the collector's bundle (an
// org-wide snmp_v2c credential while enabled), a credential of another kind
// does not.
func TestAssignmentSetCarriesTrapConfig(t *testing.T) {
	e := newDistEnv(t)
	const canary = "wp977-canary-community"
	cred, other := e.f.ID(), e.f.ID()
	e.exec(`INSERT INTO credential (id, organization_id, name, kind, ciphertext) VALUES ($1, $2, 'wp977', 'snmp_v2c', convert_to($3, 'UTF8'))`,
		cred, e.f.OrgA, canary)
	e.exec(`INSERT INTO credential (id, organization_id, name, kind, ciphertext) VALUES ($1, $2, 'wp977-ssh', 'ssh_password', '\x00')`,
		other, e.f.OrgA)
	gate := discovery.NewBundleGate(e.f.App, nil)
	ids, err := gate.CredentialIDs(context.Background(), e.f.OrgA, e.collector)
	if err != nil || len(ids) != 0 {
		t.Fatalf("bundle credentials %v (%v) with the receiver off, want none", ids, err)
	}
	got := e.dist.Answer(context.Background(), e.f.OrgA, e.collector, nil)
	set, ok := got.(probewire.AssignmentSet)
	if !ok || set.TrapConfig != nil {
		t.Fatalf("set of a group never configured (column default): %#v, want no trap_config (AK5)", got)
	}
	e.exec(`UPDATE collector_group SET trap_config = jsonb_build_object('enabled', true, 'port', 1162, 'credential_ids', jsonb_build_array($2::text, $3::text))
		WHERE id = $1`, e.group, cred, other)
	e.exec(`UPDATE collector_group_member SET config_version = config_version + 1 WHERE group_id = $1`, e.group)
	got = e.dist.Answer(context.Background(), e.f.OrgA, e.collector, nil)
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), canary) || strings.Contains(string(raw), "ciphertext") || strings.Contains(string(raw), "secret") {
		t.Fatalf("the set carries a secret: %s", raw)
	}
	set = got.(probewire.AssignmentSet)
	if tc := set.TrapConfig; tc == nil || !tc.Enabled || tc.Port != 1162 || len(tc.CredentialIDs) != 2 {
		t.Fatalf("trap_config %+v", set.TrapConfig)
	}
	ids, err = gate.CredentialIDs(context.Background(), e.f.OrgA, e.collector)
	if err != nil || len(ids) != 1 || ids[0] != cred {
		t.Fatalf("bundle credentials %v (%v), want the snmp_v2c credential of the trap receiver only", ids, err)
	}
}

func TestDistributionOmitsDisabledAndUnassignedProbes(t *testing.T) {
	e := newDistEnv(t)
	on := e.scalar(`INSERT INTO probe (organization_id, client_id, collector_group_id, type, target) VALUES ($1, $2, $3, 'icmp', '10.99.1.1')
		RETURNING id::text`, e.f.OrgA, e.f.Client1, e.group)
	e.exec(`INSERT INTO probe (organization_id, client_id, collector_group_id, type, target, enabled) VALUES ($1, $2, $3, 'icmp', '10.99.1.2', false)`,
		e.f.OrgA, e.f.Client1, e.group)
	e.assign(nil)
	// A probe added after the run has no assignment yet.
	e.exec(`INSERT INTO probe (organization_id, client_id, collector_group_id, type, target) VALUES ($1, $2, $3, 'icmp', '10.99.1.3')`,
		e.f.OrgA, e.f.Client1, e.group)
	specs, _, _ := e.fetch()
	if len(specs) != 1 || specs[0].ID != on {
		t.Errorf("set %+v, want only the enabled assigned probe", specs)
	}
	// A disabled or paused collector is refused before anything is read.
	e.exec(`UPDATE collector SET status = 'paused' WHERE id = $1`, e.collector)
	if r, ok := e.dist.Answer(context.Background(), e.f.OrgA, e.collector, nil).(probe.Reply); !ok || r.Status != 403 {
		t.Errorf("paused collector: %#v, want 403", r)
	}
	if r, ok := e.dist.Answer(context.Background(), e.f.OrgA, e.f.ID(), nil).(probe.Reply); !ok || r.Status != 404 {
		t.Errorf("unknown collector: %#v, want 404", r)
	}
}

func TestAssignmentChangePushesProbesChanged(t *testing.T) {
	e := newDistEnv(t)
	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Skip("TEST_NATS_URL not set")
	}
	nc, err := nats.Connect(url, nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	sub, err := nc.SubscribeSync("collector." + e.f.OrgA + "." + e.collector + discovery.ProbesChangedSuffix)
	if err != nil {
		t.Fatal(err)
	}
	if err = nc.Flush(); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO probe (organization_id, client_id, collector_group_id, type, target) VALUES ($1, $2, $3, 'icmp', '10.99.2.1')`,
		e.f.OrgA, e.f.Client1, e.group)
	e.assign(probe.NewNATSAssignmentNotifier(nc))
	msg, err := sub.NextMsg(5 * time.Second)
	if err != nil {
		t.Fatalf("no .probes.changed: %v", err)
	}
	var changed probewire.ProbesChanged
	if err := json.Unmarshal(msg.Data, &changed); err != nil {
		t.Fatal(err)
	}
	if v := e.scalar(`SELECT config_version::text FROM collector_group_member WHERE collector_id = $1`, e.collector); itoa64(changed.Version) != v {
		t.Errorf(".probes.changed version %d, stored %s", changed.Version, v)
	}
	_, _, version := e.fetch()
	if version != changed.Version {
		t.Errorf("set version %d, pushed %d", version, changed.Version)
	}
}

func TestDistributionIcmpDefaults(t *testing.T) {
	e := newDistEnv(t)
	e.exec(`INSERT INTO probe (organization_id, client_id, collector_group_id, type, target) VALUES ($1, $2, $3, 'icmp', '10.99.3.1')`,
		e.f.OrgA, e.f.Client1, e.group)
	e.assign(nil)
	specs, _, _ := e.fetch()
	if len(specs) != 1 {
		t.Fatalf("set %+v", specs)
	}
	if s := specs[0]; s.IntervalMS != 30000 || s.TimeoutMS != 10000 || s.DownCount != 5 || s.UpCount != 1 || s.Epoch < 1 {
		t.Errorf("spec %+v, want 30 s / 10 s / down count 5 / up count 1 with an epoch", s)
	}
}
