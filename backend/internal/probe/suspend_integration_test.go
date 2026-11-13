package probe_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
)

// Retest (WP-942, PKG-09, ENT-08):
//
//	cd backend && go test -count=1 -p 1 -run 'TestDowngradeSuspends|TestUpgradeResumes|TestReconcileIsIdempotent|TestExpiredAddonSuspends|TestQuotaExcessSuspendsNothing|TestSuspensionNotifiesOrgAdmins|TestReconcileFailingOrganization|TestReconcileWithoutEnforcement|TestPatchEnabledKeepsSuspension|TestMixedRunNamesPausedFeature' ./internal/probe/

// suspendEnv is organization A with an org-wide collector group of one
// active collector and a switchable set of licensed features.
type suspendEnv struct {
	t         *testing.T
	f         *scopetest.Fixture
	group     string
	collector string
	mu        sync.Mutex
	features  map[string]bool
}

func newSuspendEnv(t *testing.T) *suspendEnv {
	t.Helper()
	f := scopetest.Seed(t, "93")
	e := &suspendEnv{t: t, f: f}
	e.group = e.scalar(`INSERT INTO collector_group (organization_id, name) VALUES ($1, 'wp942') RETURNING id::text`, f.OrgA)
	e.collector = e.scalar(`INSERT INTO collector (organization_id, name, status) VALUES ($1, 'wp942', 'active') RETURNING id::text`, f.OrgA)
	e.exec(`UPDATE collector_group_member SET group_id = $1 WHERE collector_id = $2`, e.group, e.collector)
	e.plan(entitlement.FeatureMonitoring, entitlement.FeatureMonitoringNetwork) // pro
	return e
}

func (e *suspendEnv) scalar(sql string, args ...any) string {
	e.t.Helper()
	var out string
	if err := e.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return out
}

func (e *suspendEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.f.Admin.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

// plan sets the licensed features of organization A.
func (e *suspendEnv) plan(features ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.features = map[string]bool{}
	for _, f := range features {
		e.features[f] = true
	}
}

func (e *suspendEnv) licensed(_ context.Context, orgID, feature string) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return orgID == e.f.OrgA && e.features[feature], nil
}

// probe inserts a probe of client 1 in the group.
func (e *suspendEnv) probe(typ, target string, enabled bool) string {
	e.t.Helper()
	return e.scalar(`INSERT INTO probe (organization_id, client_id, collector_group_id, type, target, enabled)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`, e.f.OrgA, e.f.Client1, e.group, typ, target, enabled)
}

// reconcile runs the job for organization A.
func (e *suspendEnv) reconcile() probe.SuspensionChange {
	e.t.Helper()
	c, err := probe.NewSuspender(e.f.App, e.licensed).RunOrg(context.Background(), e.f.OrgA)
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

// assign runs the assignment engine for organization A.
func (e *suspendEnv) assign() {
	e.t.Helper()
	if _, err := probe.NewAssigner(e.f.App).RunOrg(context.Background(), e.f.OrgA); err != nil {
		e.t.Fatal(err)
	}
}

// state reads suspended_reason, enabled and whether the probe is assigned.
func (e *suspendEnv) state(id string) (reason string, enabled, assigned bool) {
	e.t.Helper()
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT COALESCE(suspended_reason, ''), enabled,
			EXISTS (SELECT 1 FROM probe_assignment a WHERE a.probe_id = p.id)
		FROM probe p WHERE id = $1`, id).Scan(&reason, &enabled, &assigned); err != nil {
		e.t.Fatal(err)
	}
	return reason, enabled, assigned
}

func (e *suspendEnv) version() string {
	return e.scalar(`SELECT config_version::text FROM collector_group_member WHERE collector_id = $1`, e.collector)
}

func (e *suspendEnv) events() string {
	return e.scalar(`SELECT count(*)::text FROM event_outbox WHERE organization_id = $1 AND type = $2`,
		e.f.OrgA, events.MonitoringSuspensionChanged)
}

func TestDowngradeSuspendsProbesOfDroppedFeature(t *testing.T) {
	e := newSuspendEnv(t)
	icmp := e.probe("icmp", "10.97.0.1", true)
	snmp := e.probe("snmp_status", "10.97.0.2", true)
	e.assign()
	if c := e.reconcile(); c.Paused+c.Resumed != 0 {
		t.Fatalf("pro: %+v, want no change", c)
	}
	before := e.version()
	e.plan(entitlement.FeatureMonitoring) // standard
	c := e.reconcile()
	if c.Paused != 1 || c.Resumed != 0 || len(c.ProbeTypes) != 1 || c.ProbeTypes[0] != "snmp_status" ||
		len(c.Features) != 1 || c.Features[0] != entitlement.FeatureMonitoringNetwork {
		t.Fatalf("downgrade: %+v", c)
	}
	if reason, enabled, assigned := e.state(snmp); reason != probe.SuspendedEntitlement || !enabled || assigned {
		t.Errorf("snmp_status: reason %q enabled %v assigned %v; want suspended, enabled, unassigned", reason, enabled, assigned)
	}
	if reason, _, assigned := e.state(icmp); reason != "" || !assigned {
		t.Errorf("icmp: reason %q assigned %v; want active and assigned", reason, assigned)
	}
	if after := e.version(); after == before {
		t.Errorf("config_version stayed %s", after)
	}
	// The assignment engine keeps the suspended probe unassigned.
	e.assign()
	if _, _, assigned := e.state(snmp); assigned {
		t.Error("the assignment engine assigned a suspended probe")
	}
}

func TestUpgradeResumesSuspendedProbes(t *testing.T) {
	e := newSuspendEnv(t)
	on := e.probe("snmp_status", "10.97.1.1", true)
	off := e.probe("snmp_status", "10.97.1.2", false)
	e.plan(entitlement.FeatureMonitoring)
	if c := e.reconcile(); c.Paused != 2 {
		t.Fatalf("downgrade: %+v, want 2 paused", c)
	}
	e.plan(entitlement.FeatureMonitoring, entitlement.FeatureMonitoringNetwork)
	if c := e.reconcile(); c.Resumed != 2 || c.Paused != 0 {
		t.Fatalf("upgrade: %+v, want 2 resumed", c)
	}
	if reason, enabled, assigned := e.state(on); reason != "" || !enabled || !assigned {
		t.Errorf("resumed probe: reason %q enabled %v assigned %v", reason, enabled, assigned)
	}
	if reason, enabled, assigned := e.state(off); reason != "" || enabled || assigned {
		t.Errorf("probe the user switched off: reason %q enabled %v assigned %v; want off and unassigned", reason, enabled, assigned)
	}
}

func TestReconcileIsIdempotent(t *testing.T) {
	e := newSuspendEnv(t)
	id := e.probe("snmp_status", "10.97.2.1", true)
	e.plan(entitlement.FeatureMonitoring)
	e.reconcile()
	xmin, n, v := e.scalar(`SELECT xmin::text FROM probe WHERE id = $1`, id), e.events(), e.version()
	if c := e.reconcile(); c.Paused+c.Resumed != 0 {
		t.Errorf("second run: %+v, want nothing", c)
	}
	if e.scalar(`SELECT xmin::text FROM probe WHERE id = $1`, id) != xmin || e.events() != n || e.version() != v {
		t.Error("the second run wrote a row, an event or a version")
	}
}

func TestExpiredAddonSuspends(t *testing.T) {
	e := newSuspendEnv(t)
	ctx := e.f.OrgCtx(e.f.OrgA)
	svc := entitlement.NewService(entitlement.NewPGRepository(e.f.App), entitlement.Options{Enforce: true})
	past := time.Now().Add(-time.Hour)
	for _, ent := range []*entitlement.Entitlement{
		{OrganizationID: e.f.OrgA, FeatureKey: entitlement.FeatureMonitoring, Plan: entitlement.PlanStandard, Enabled: true},
		{OrganizationID: e.f.OrgA, FeatureKey: entitlement.FeatureMonitoringAccess, Plan: entitlement.PlanStandard, Enabled: true,
			ValidUntil: &past},
	} {
		if _, err := svc.Grant(ctx, ent); err != nil {
			t.Fatal(err)
		}
	}
	pon := e.probe("pon", "10.97.3.1", true)
	icmp := e.probe("icmp", "10.97.3.2", true)
	c, err := probe.NewSuspender(e.f.App, probe.LicensedBy(svc)).RunOrg(context.Background(), e.f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	if c.Paused != 1 {
		t.Errorf("expired add-on: %+v, want the pon probe paused", c)
	}
	if reason, _, _ := e.state(pon); reason != probe.SuspendedEntitlement {
		t.Errorf("pon: reason %q", reason)
	}
	if reason, _, _ := e.state(icmp); reason != "" {
		t.Errorf("icmp: reason %q, want active", reason)
	}
}

func TestQuotaExcessSuspendsNothing(t *testing.T) {
	e := newSuspendEnv(t)
	ctx := e.f.OrgCtx(e.f.OrgA)
	svc := entitlement.NewService(entitlement.NewPGRepository(e.f.App), entitlement.Options{Enforce: true})
	grant := func(plan entitlement.Plan, feature string, limits map[string]int64, enabled bool) {
		t.Helper()
		if _, err := svc.Grant(ctx, &entitlement.Entitlement{OrganizationID: e.f.OrgA, FeatureKey: feature,
			Plan: plan, Enabled: enabled, Limits: limits}); err != nil {
			t.Fatal(err)
		}
	}
	// Pro: monitoring and monitoring_network without limits.
	grant(entitlement.PlanPro, entitlement.FeatureMonitoring, map[string]int64{entitlement.LimitMinProbeIntervalS: 10}, true)
	grant(entitlement.PlanPro, entitlement.FeatureMonitoringNetwork, nil, true)
	ids := []string{e.probe("icmp", "10.97.4.1", true), e.probe("icmp", "10.97.4.2", true), e.probe("tcp", "10.97.4.3:22", true)}
	e.exec(`UPDATE probe SET params = '{"interval_s": 10}' WHERE id = ANY ($1::uuid[])`, ids)
	snmp := e.probe("snmp_status", "10.97.4.4", true)
	e.assign()
	run := func() probe.SuspensionChange {
		t.Helper()
		c, err := probe.NewSuspender(e.f.App, probe.LicensedBy(svc)).RunOrg(context.Background(), e.f.OrgA)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if c := run(); c.Paused+c.Resumed != 0 {
		t.Fatalf("pro: %+v, want no change", c)
	}
	params := func(id string) string {
		return e.scalar(`SELECT params::text FROM probe WHERE id = $1`, id)
	}
	before := map[string]string{}
	for _, id := range ids {
		before[id] = params(id)
	}
	// Downgrade to standard: monitoring_network drops, the stock exceeds
	// max_monitored_targets and the interval of 10 s lies below the new
	// min_probe_interval_s.
	grant(entitlement.PlanStandard, entitlement.FeatureMonitoring, map[string]int64{entitlement.LimitMaxMonitoredTargets: 1,
		entitlement.LimitMinProbeIntervalS: 60}, true)
	grant(entitlement.PlanStandard, entitlement.FeatureMonitoringNetwork, nil, false)
	c := run()
	if c.Paused != 1 || c.Resumed != 0 || len(c.ProbeTypes) != 1 || c.ProbeTypes[0] != "snmp_status" {
		t.Errorf("downgrade: %+v, want only the snmp_status probe of the dropped feature paused", c)
	}
	if reason, _, _ := e.state(snmp); reason != probe.SuspendedEntitlement {
		t.Errorf("snmp_status: reason %q, want suspended", reason)
	}
	n := e.events()
	for _, id := range ids {
		if reason, enabled, assigned := e.state(id); reason != "" || !enabled || !assigned {
			t.Errorf("probe %s: reason %q enabled %v assigned %v, want active and assigned", id, reason, enabled, assigned)
		}
		if got := params(id); got != before[id] {
			t.Errorf("probe %s: params %s, want unchanged %s (interval below the new minimum stays)", id, got, before[id])
		}
	}
	// A second run under the quota excess writes nothing.
	if c := run(); c.Paused+c.Resumed != 0 || e.events() != n {
		t.Errorf("quota excess: %+v, want nothing written", c)
	}
}

func TestReconcileWithoutEnforcementSuspendsNothing(t *testing.T) {
	e := newSuspendEnv(t)
	ctx := e.f.OrgCtx(e.f.OrgA)
	svc := entitlement.NewService(entitlement.NewPGRepository(e.f.App), entitlement.Options{Enforce: false})
	past := time.Now().Add(-time.Hour)
	for _, ent := range []*entitlement.Entitlement{
		{OrganizationID: e.f.OrgA, FeatureKey: entitlement.FeatureMonitoring, Plan: entitlement.PlanStandard, Enabled: true},
		{OrganizationID: e.f.OrgA, FeatureKey: entitlement.FeatureMonitoringAccess, Plan: entitlement.PlanStandard, Enabled: true,
			ValidUntil: &past},
	} {
		if _, err := svc.Grant(ctx, ent); err != nil {
			t.Fatal(err)
		}
	}
	pon := e.probe("pon", "10.97.7.1", true)
	snmp := e.probe("snmp_status", "10.97.7.2", true)
	c, err := probe.NewSuspender(e.f.App, probe.LicensedBy(svc)).RunOrg(context.Background(), e.f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	if c.Paused+c.Resumed != 0 {
		t.Errorf("without enforcement: %+v, want nothing paused", c)
	}
	for _, id := range []string{pon, snmp} {
		if reason, _, _ := e.state(id); reason != "" {
			t.Errorf("probe %s: reason %q, want active", id, reason)
		}
	}
}

func TestPatchEnabledKeepsSuspension(t *testing.T) {
	e := newSuspendEnv(t)
	id := e.probe("snmp_status", "10.97.8.1", false)
	e.plan(entitlement.FeatureMonitoring)
	if c := e.reconcile(); c.Paused != 1 {
		t.Fatalf("downgrade: %+v, want 1 paused", c)
	}
	at := e.scalar(`SELECT suspended_at::text FROM probe WHERE id = $1`, id)
	updated, err := probe.NewPGRepository(e.f.App).Update(e.f.OrgCtx(e.f.OrgA), id, func(p *probe.Probe) (*probe.Probe, []string, error) {
		next := *p
		next.Enabled = true
		return &next, []string{"enabled"}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Enabled {
		t.Error("enabled was not set")
	}
	e.assign()
	if reason, enabled, assigned := e.state(id); reason != probe.SuspendedEntitlement || !enabled || assigned {
		t.Errorf("after PATCH enabled=true: reason %q enabled %v assigned %v; want suspended, enabled, unassigned", reason, enabled, assigned)
	}
	if got := e.scalar(`SELECT suspended_at::text FROM probe WHERE id = $1`, id); got != at {
		t.Errorf("suspended_at %s, want %s", got, at)
	}
}

func TestMixedRunNamesPausedFeature(t *testing.T) {
	e := newSuspendEnv(t)
	pon := e.probe("pon", "10.97.9.1", true)
	snmp := e.probe("snmp_status", "10.97.9.2", true)
	if c := e.reconcile(); c.Paused != 1 { // pro: pon has no monitoring_access
		t.Fatalf("pro: %+v, want the pon probe paused", c)
	}
	e.plan(entitlement.FeatureMonitoring, entitlement.FeatureMonitoringAccess)
	if c := e.reconcile(); c.Paused != 1 || c.Resumed != 1 {
		t.Fatalf("swap: %+v, want one paused and one resumed", c)
	}
	var feature string
	var suspended bool
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT data->>'feature_key', (data->>'suspended')::boolean
		FROM event_outbox WHERE organization_id = $1 AND type = $2 ORDER BY sequence DESC LIMIT 1`,
		e.f.OrgA, events.MonitoringSuspensionChanged).Scan(&feature, &suspended); err != nil {
		t.Fatal(err)
	}
	if feature != entitlement.FeatureMonitoringNetwork || !suspended {
		t.Errorf("frame feature_key %q suspended %v, want the paused %s", feature, suspended, entitlement.FeatureMonitoringNetwork)
	}
	if reason, _, _ := e.state(pon); reason != "" {
		t.Errorf("pon: reason %q, want resumed", reason)
	}
	if reason, _, _ := e.state(snmp); reason != probe.SuspendedEntitlement {
		t.Errorf("snmp_status: reason %q, want suspended", reason)
	}
}

func TestSuspensionNotifiesOrgAdmins(t *testing.T) {
	e := newSuspendEnv(t)
	e.probe("snmp_status", "10.97.5.1", true)
	e.probe("snmp_if", "10.97.5.2", true)
	e.plan(entitlement.FeatureMonitoring)
	n := e.events()
	e.reconcile()
	if got := e.events(); got == n {
		t.Fatal("no monitoring.suspension_changed in the outbox")
	}
	var count int
	var data string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*), max(data::text) FROM event_outbox
		WHERE organization_id = $1 AND type = $2`, e.f.OrgA, events.MonitoringSuspensionChanged).Scan(&count, &data); err != nil {
		t.Fatal(err)
	}
	if count != 1 || !strings.Contains(data, `"paused": 2`) && !strings.Contains(data, `"paused":2`) {
		t.Errorf("%d events, data %s; want one with paused 2", count, data)
	}
	sub, ok := notify.Subscriptions[events.MonitoringSuspensionChanged]
	if !ok || sub.Category != "entitlement" || !sub.Critical {
		t.Fatalf("subscription %+v", sub)
	}
	if a := sub.Audience(nil); a == nil || a.Permission != identity.PermUserManage || !a.AllWithPermission {
		t.Errorf("audience %+v, want every holder of user:manage (org_admins)", a)
	}
	deliverSuspension(t, e)
	for _, locale := range []string{notify.LocaleDE, notify.LocaleEN} {
		render, ok := notify.Templates[events.MonitoringSuspensionChanged][locale]
		if !ok {
			t.Fatalf("no %s template", locale)
		}
		text := render(map[string]any{"paused": float64(2), "resumed": float64(0), "features": []any{"monitoring_network"}})
		if !strings.Contains(text.Body, "2 ") {
			t.Errorf("text %v does not name the number of paused probes", text)
		}
	}
}

func TestReconcileFailingOrganizationDoesNotStopOthers(t *testing.T) {
	e := newSuspendEnv(t)
	id := e.probe("snmp_status", "10.97.6.1", true)
	e.plan(entitlement.FeatureMonitoring)
	failing := errors.New("entitlements unavailable")
	s := probe.NewSuspender(e.f.App, func(ctx context.Context, orgID, feature string) (bool, error) {
		if orgID == e.f.OrgB {
			return false, failing
		}
		return e.licensed(ctx, orgID, feature)
	})
	err := s.Run(context.Background())
	if !errors.Is(err, failing) || !strings.Contains(err.Error(), e.f.OrgB) {
		t.Errorf("run: %v, want the failure of organization B", err)
	}
	if reason, _, _ := e.state(id); reason != probe.SuspendedEntitlement {
		t.Errorf("organization A was not reconciled: reason %q", reason)
	}
}

// deliverSuspension hands the monitoring.suspension_changed event of
// organization A from the outbox to the notification consumer and checks
// the delivery: a user holding user:manage (org_admin) gets it in the
// in-app inbox, a user without that permission does not.
func deliverSuspension(t *testing.T, e *suspendEnv) {
	t.Helper()
	bg := context.Background()
	var env events.Envelope
	var data []byte
	if err := e.f.Admin.QueryRow(bg, `SELECT id::text, type, organization_id::text, occurred_at, data FROM event_outbox
		WHERE organization_id = $1 AND type = $2 ORDER BY sequence DESC LIMIT 1`, e.f.OrgA, events.MonitoringSuspensionChanged).
		Scan(&env.ID, &env.Type, &env.OrgID, &env.OccurredAt, &data); err != nil {
		t.Fatal(err)
	}
	env.Data = data
	users := map[string]string{}
	for name, perms := range map[string]string{"admin": `["user:manage"]`, "reader": `["ci:read"]`} {
		role := e.f.ID()
		e.exec(`INSERT INTO role (id, organization_id, name, scope, permissions) VALUES ($1, $2, $3, 'org', $4::jsonb)`,
			role, e.f.OrgA, "wp942-"+name, perms)
		users[name] = e.f.AppUser(t, e.f.OrgA, "wp942-"+name)
		e.exec(`INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, e.f.OrgA, users[name], role)
	}
	key, _ := crypto.GenerateMasterKey()
	enc, _ := crypto.NewEnvelopeEncryptor(key)
	channels, inbox := notify.NewPGRepository(e.f.App, notify.NewSealer(enc)), notify.NewPGInbox(e.f.App)
	if err := channels.Create(bg, &notify.Channel{OrganizationID: e.f.OrgA, Name: "wp942-" + e.f.ID(),
		Provider: notify.ProviderInApp, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	// A clock in the past: the run never claims deliveries of other tests.
	now := time.Date(2020, 1, 7, 10, 0, 0, 0, time.UTC)
	notifier := notify.NewNotifier(channels, notify.NewPGDeliveryStore(e.f.App), notify.NewPGPreferences(e.f.App),
		notify.NewSender(map[string]notify.Provider{notify.ProviderInApp: notify.NewInAppProvider(inbox)})).
		WithClock(func() time.Time { return now })
	consumer := notify.NewEventConsumer(notify.NewPGResolver(e.f.App, permission.NewPGRepository(e.f.App)), notifier)
	if err := consumer.Consume(bg, &env); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := notifier.ProcessDue(bg); err != nil {
			t.Fatal(err)
		}
	}
	for name, want := range map[string]int{"admin": 1, "reader": 0} {
		items, _, err := inbox.List(bg, e.f.OrgA, users[name], false, 50, 0)
		if err != nil {
			t.Fatal(err)
		}
		got := 0
		for i := range items {
			if items[i].Category == "entitlement" && items[i].Data["event_type"] == events.MonitoringSuspensionChanged {
				got++
			}
		}
		if got != want {
			t.Errorf("suspension notifications of the %s: %d, want %d", name, got, want)
		}
	}
}
