package security_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/security"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/vulnfeed"
)

// The version match CI → findings on PostgreSQL (SEC-02, LCY-06, WP-1121,
// E-236): the catalog is written through vulnfeed.PGRepository in the
// system context, the CIs of organization A are matched in its tenant
// context, the findings are written with Upsert.
//
// They run only with TEST_DATABASE_URL (scopetest.Seed).
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestMatcher' ./internal/security/

const matchCSAF = "csaf:https://wp1121.example/provider-metadata.json"

type matchEnv struct {
	t        *testing.T
	f        *scopetest.Fixture
	ctx      context.Context
	catalog  *vulnfeed.PGRepository
	matcher  *security.Matcher
	findings *security.PGRepository
}

func newMatchEnv(t *testing.T) *matchEnv {
	t.Helper()
	f := scopetest.Seed(t, "7b")
	scope := database.OrgWideScope(f.OrgA, "")
	e := &matchEnv{t: t, f: f, catalog: vulnfeed.NewPGRepository(f.App), findings: security.NewPGRepository(f.App),
		ctx: tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope), tenant.TenantInfo{OrganizationID: f.OrgA})}
	e.matcher = security.NewMatcher(f.App, e.catalog, e.findings)
	bg := context.Background()
	cleanup := func() {
		_, _ = f.Admin.Exec(bg, `DELETE FROM vuln_advisory WHERE id LIKE 'wp1121%'`)
		_, _ = f.Admin.Exec(bg, `DELETE FROM vuln_source WHERE id = $1`, matchCSAF)
	}
	cleanup()
	t.Cleanup(cleanup)
	now := time.Now().UTC()
	for _, s := range []*vulnfeed.Source{{ID: matchCSAF, Kind: vulnfeed.KindCSAF, SyncedAt: &now}} {
		if err := e.catalog.UpdateSource(bg, s); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.catalog.GetSource(bg, vulnfeed.KindNVD); err != nil {
		if err = e.catalog.UpdateSource(bg, &vulnfeed.Source{ID: vulnfeed.KindNVD, Kind: vulnfeed.KindNVD}); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

// advisory stores an advisory with one range of vendor/product.
func (e *matchEnv) advisory(id, source string, cves []string, aff vulnfeed.Affected, withdrawn bool) {
	e.t.Helper()
	score := 8.1
	if err := e.catalog.ReplaceAdvisory(context.Background(), &vulnfeed.Advisory{ID: id, SourceID: source, CVEIDs: cves, Title: "advisory " + id,
		Severity: "high", CVSSScore: &score, Withdrawn: withdrawn}, []vulnfeed.Affected{aff}); err != nil {
		e.t.Fatalf("advisory %s: %v", id, err)
	}
}

// device inserts a CI of organization A with the product fields.
func (e *matchEnv) device(name, manufacturer, model, oid, firmware string) string {
	e.t.Helper()
	id := e.f.CI(e.t, e.f.OrgA, e.f.Client1, name)
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE ci SET manufacturer = NULLIF($2, ''), model = NULLIF($3, ''),
		sys_object_id = NULLIF($4, ''), firmware_version = NULLIF($5, '') WHERE id = $1`, id, manufacturer, model, oid, firmware); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// mapping inserts a product mapping of organization org.
func (e *matchEnv) mapping(org, manufacturer, vendor, product, scheme string, priority int) {
	e.t.Helper()
	if _, err := e.f.Admin.Exec(context.Background(), `INSERT INTO cpe_mapping (organization_id, match_kind, manufacturer_pattern, vendor, products,
		version_scheme, priority) VALUES ($1, 'manufacturer_model', $2, $3, ARRAY[$4], $5, $6)`, org, manufacturer, vendor, product, scheme, priority); err != nil {
		e.t.Fatal(err)
	}
}

func (e *matchEnv) run(ciIDs ...string) *security.MatchResult {
	e.t.Helper()
	res, err := e.matcher.Run(e.ctx, e.f.OrgA, ciIDs, "")
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

// findingsOf lists advisory → status/status_reason of the cve_feed findings
// of a CI.
func (e *matchEnv) findingsOf(ciID string) map[string]string {
	e.t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT advisory_id, status || '/' || COALESCE(status_reason, '') FROM security_finding
		WHERE ci_id = $1 AND source = 'cve_feed'`, ciID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var a, s string
		if err := rows.Scan(&a, &s); err != nil {
			e.t.Fatal(err)
		}
		out[a] = s
	}
	return out
}

func (e *matchEnv) findingEvents() int {
	e.t.Helper()
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND type LIKE 'finding.%'`,
		e.f.OrgA).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

var mikrotik = vulnfeed.Affected{Vendor: "wp1121-mikrotik", Product: "rtos", VersionScheme: "generic", VersionEnd: "7.13", FixedVersion: "7.13"}

// TestMatcherCreatesFinding: a CI below the fixed version gets one finding
// of source cve_feed with the fields of the advisory.
func TestMatcherCreatesFinding(t *testing.T) {
	e := newMatchEnv(t)
	e.mapping(e.f.OrgA, "wp1121 mikrotik*", "wp1121-mikrotik", "rtos", "generic", 10)
	e.advisory("wp1121:a1", matchCSAF, []string{"CVE-2026-11211"}, mikrotik, false)
	ci := e.device("wp1121-rb", "WP1121 MikroTik", "RB5009", "", "7.12.1")
	res := e.run()
	if res.CIsChecked < 1 || res.FindingsCreated != 1 {
		t.Fatalf("result %+v, want one finding", res)
	}
	var f struct {
		kind, severity, installed, fixed, advisory, cves string
		cvss                                             float64
	}
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT kind, severity, installed_version, fixed_version, advisory_id, cve_ids::text, cvss_score
		FROM security_finding WHERE ci_id = $1 AND source = 'cve_feed'`, ci).Scan(&f.kind, &f.severity, &f.installed, &f.fixed, &f.advisory, &f.cves, &f.cvss); err != nil {
		t.Fatal(err)
	}
	if f.kind != "vulnerability" || f.severity != "high" || f.installed != "7.12.1" || f.fixed != "7.13" || f.advisory != "wp1121:a1" ||
		!strings.Contains(f.cves, "CVE-2026-11211") || f.cvss != 8.1 {
		t.Errorf("finding %+v", f)
	}
}

// TestMatcherIsIdempotent: a second run creates no finding and no event.
func TestMatcherIsIdempotent(t *testing.T) {
	e := newMatchEnv(t)
	e.mapping(e.f.OrgA, "wp1121 mikrotik*", "wp1121-mikrotik", "rtos", "generic", 10)
	e.advisory("wp1121:a1", matchCSAF, []string{"CVE-2026-11211"}, mikrotik, false)
	e.device("wp1121-rb", "WP1121 MikroTik", "RB5009", "", "7.12.1")
	if res := e.run(); res.FindingsCreated != 1 {
		t.Fatalf("first run %+v", res)
	}
	before := e.findingEvents()
	if res := e.run(); res.FindingsCreated != 0 || res.FindingsResolved != 0 {
		t.Errorf("second run %+v, want nothing created or resolved", res)
	}
	if after := e.findingEvents(); after != before {
		t.Errorf("finding events %d after the second run, want %d", after, before)
	}
}

// TestMatcherResolvesAfterVersionUpdate: after an update to the fixed
// version the finding is resolved with version_fixed.
func TestMatcherResolvesAfterVersionUpdate(t *testing.T) {
	e := newMatchEnv(t)
	e.mapping(e.f.OrgA, "wp1121 mikrotik*", "wp1121-mikrotik", "rtos", "generic", 10)
	e.advisory("wp1121:a1", matchCSAF, []string{"CVE-2026-11211"}, mikrotik, false)
	ci := e.device("wp1121-rb", "WP1121 MikroTik", "RB5009", "", "7.12.1")
	e.run()
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE ci SET firmware_version = '7.13' WHERE id = $1`, ci); err != nil {
		t.Fatal(err)
	}
	if res := e.run(ci); res.FindingsResolved != 1 {
		t.Errorf("run after the update %+v, want one resolved", res)
	}
	if got := e.findingsOf(ci); got["wp1121:a1"] != "resolved/version_fixed" {
		t.Errorf("findings %v, want resolved/version_fixed", got)
	}
}

// TestMatcherResolvesWithdrawnAdvisory: a withdrawn advisory resolves its
// finding with advisory_withdrawn.
func TestMatcherResolvesWithdrawnAdvisory(t *testing.T) {
	e := newMatchEnv(t)
	e.mapping(e.f.OrgA, "wp1121 mikrotik*", "wp1121-mikrotik", "rtos", "generic", 10)
	e.advisory("wp1121:a1", matchCSAF, []string{"CVE-2026-11211"}, mikrotik, false)
	ci := e.device("wp1121-rb", "WP1121 MikroTik", "RB5009", "", "7.12.1")
	e.run()
	e.advisory("wp1121:a1", matchCSAF, []string{"CVE-2026-11211"}, mikrotik, true)
	e.run()
	if got := e.findingsOf(ci); got["wp1121:a1"] != "resolved/advisory_withdrawn" {
		t.Errorf("findings %v, want resolved/advisory_withdrawn", got)
	}
}

// TestMatcherOrgMappingBeforeSystem: a row of the organization maps a
// Cisco device before the system mapping.
func TestMatcherOrgMappingBeforeSystem(t *testing.T) {
	e := newMatchEnv(t)
	e.mapping(e.f.OrgA, "cisco*", "wp1121-cisco", "special_os", "generic", 1)
	e.advisory("wp1121:cisco-own", matchCSAF, nil, vulnfeed.Affected{Vendor: "wp1121-cisco", Product: "special_os", VersionEnd: "2.0"}, false)
	e.advisory("wp1121:cisco-sys", matchCSAF, nil, vulnfeed.Affected{Vendor: "cisco", Product: "ios", VersionScheme: "cisco_ios", VersionEnd: "99.0(1)"}, false)
	ci := e.device("wp1121-cisco", "Cisco Systems", "C2960", "", "1.5")
	e.run()
	got := e.findingsOf(ci)
	if _, own := got["wp1121:cisco-own"]; !own || len(got) != 1 {
		t.Errorf("findings %v, want only the one of the organization mapping", got)
	}
	m := security.ResolveMapping(nil, security.SystemMappings(), &security.CIProduct{Manufacturer: "Cisco Systems", Model: "C9300-48P"})
	if m == nil || m.VersionScheme != security.SchemeCiscoIOSXE {
		t.Errorf("system mapping of a Catalyst 9300: %+v, want IOS-XE", m)
	}
}

// TestMatcherCSAFWinsOverNVD: a CVE of a CSAF and an NVD advisory on the
// same CI yields only the CSAF finding.
func TestMatcherCSAFWinsOverNVD(t *testing.T) {
	e := newMatchEnv(t)
	e.mapping(e.f.OrgA, "wp1121 mikrotik*", "wp1121-mikrotik", "rtos", "generic", 10)
	e.advisory("wp1121:csaf", matchCSAF, []string{"CVE-2026-11212"}, mikrotik, false)
	e.advisory("wp1121:nvd", vulnfeed.KindNVD, []string{"CVE-2026-11212"}, mikrotik, false)
	ci := e.device("wp1121-rb", "WP1121 MikroTik", "RB5009", "", "7.12.1")
	e.run()
	got := e.findingsOf(ci)
	if _, csaf := got["wp1121:csaf"]; !csaf || len(got) != 1 {
		t.Errorf("findings %v, want only the CSAF one", got)
	}
}

// TestMatcherCountsUnmapped: a CI with a manufacturer without mapping is
// unmapped, one without product data and one with an unreadable version
// are counted.
func TestMatcherCountsUnmapped(t *testing.T) {
	e := newMatchEnv(t)
	e.mapping(e.f.OrgA, "wp1121 mikrotik*", "wp1121-mikrotik", "rtos", "cisco_ios", 10)
	unmapped := e.device("wp1121-unknown", "WP1121 Nobody", "X1", "", "1.0")
	bare := e.device("wp1121-bare", "", "", "", "")
	odd := e.device("wp1121-odd", "WP1121 MikroTik", "RB", "", "7.12")
	res := e.run(unmapped, bare, odd)
	if res.CIsUnmapped != 1 || res.CIsWithoutData != 1 || res.VersionsUnparseable != 1 || res.CIsChecked != 0 {
		t.Errorf("result %+v, want one unmapped, one without data, one unparseable", res)
	}
}

// recordingQueue records the submitted match jobs.
type recordingQueue struct {
	mu   sync.Mutex
	keys []string
	ids  [][]string
}

func (q *recordingQueue) Submit(_ context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.keys = append(q.keys, opts.IdempotencyKey)
	p, _ := params.(security.MatchParams)
	q.ids = append(q.ids, p.CIIDs)
	return &jobs.Job{ID: fmt.Sprintf("job-%d", len(q.keys)), OrganizationID: orgID, Kind: kind}, true, nil
}

// TestMatcherIncrementalOnCIChanged: ci.changed with firmware_version
// queues one job for the CI; location_id queues none; ci.created queues
// one.
func TestMatcherIncrementalOnCIChanged(t *testing.T) {
	e := newMatchEnv(t)
	q := &recordingQueue{}
	c := security.NewMatchConsumer(security.NewMatcher(e.f.App, e.catalog, e.findings).WithQueue(q),
		func(context.Context, string) bool { return true })
	ci := e.device("wp1121-inc", "WP1121 MikroTik", "RB", "", "7.12.1")
	env := func(id, typ string, fields ...string) *events.Envelope {
		data, _ := json.Marshal(map[string]any{"id": ci, "changed_fields": fields})
		return &events.Envelope{ID: id, Type: typ, OrgID: e.f.OrgA, Data: data}
	}
	for _, ev := range []*events.Envelope{env("e1", events.CIChanged, "firmware_version"), env("e2", events.CIChanged, "location_id"),
		env("e3", events.CICreated), env("e4", events.TicketUpdated)} {
		if err := c.Consume(context.Background(), ev); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Join(q.keys, ",") != "vulnfeed.match:e1,vulnfeed.match:e3" || len(q.ids[0]) != 1 || q.ids[0][0] != ci {
		t.Errorf("queued %v %v, want the jobs of e1 and e3 for the CI", q.keys, q.ids)
	}
}

// TestMatcherScheduleAfterSync: the schedule queues a full run per
// organization named after the newest sync of the catalog.
func TestMatcherScheduleAfterSync(t *testing.T) {
	e := newMatchEnv(t)
	q := &recordingQueue{}
	m := security.NewMatcher(e.f.App, e.catalog, e.findings).WithQueue(q)
	s := security.NewMatchSchedule(m, e.catalog, func(context.Context) ([]string, error) { return []string{e.f.OrgA, e.f.OrgB}, nil },
		func(_ context.Context, org string) bool { return org == e.f.OrgA })
	n, err := s.Run(context.Background())
	if err != nil || n != 1 || len(q.keys) != 1 || !strings.HasPrefix(q.keys[0], "vulnfeed.match:full:"+e.f.OrgA+":") || q.ids[0] != nil {
		t.Fatalf("schedule %d %v %v, want one full run of organization A", n, q.keys, err)
	}
	if _, err = s.Run(context.Background()); err != nil || len(q.keys) != 2 || q.keys[1] != q.keys[0] {
		t.Errorf("schedule without a new sync: %v %v, want the same idempotency key", q.keys, err)
	}
	later := time.Now().UTC().Add(time.Minute)
	if err = e.catalog.UpdateSource(context.Background(), &vulnfeed.Source{ID: matchCSAF, Kind: vulnfeed.KindCSAF, SyncedAt: &later}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Run(context.Background()); err != nil || len(q.keys) != 3 || q.keys[2] == q.keys[0] {
		t.Errorf("schedule after a new sync: %v %v, want a new key", q.keys, err)
	}
}

// TestMatcherOrgIsolation: the run of organization A neither reads the CIs
// nor the mappings of organization B.
func TestMatcherOrgIsolation(t *testing.T) {
	e := newMatchEnv(t)
	e.mapping(e.f.OrgB, "wp1121 mikrotik*", "wp1121-mikrotik", "rtos", "generic", 10)
	e.advisory("wp1121:a1", matchCSAF, []string{"CVE-2026-11211"}, mikrotik, false)
	ciA := e.device("wp1121-a", "WP1121 MikroTik", "RB", "", "7.12.1")
	ciB := e.f.CI(t, e.f.OrgB, "", "wp1121-b")
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE ci SET manufacturer = 'WP1121 MikroTik', firmware_version = '7.12.1' WHERE id = $1`, ciB); err != nil {
		t.Fatal(err)
	}
	res := e.run()
	if res.FindingsCreated != 0 || len(e.findingsOf(ciA)) != 0 || len(e.findingsOf(ciB)) != 0 {
		t.Errorf("result %+v, want no finding (the mapping of B does not apply to A, the CI of B is not read)", res)
	}
}

// TestMatcherBudget: 10,000 CIs against 2,000 ranges in at most 60 s
// (AK7; the 100,000 CIs follow in the load run WP-1167).
func TestMatcherBudget(t *testing.T) {
	e := newMatchEnv(t)
	bg := context.Background()
	e.mapping(e.f.OrgA, "wp1121budget*", "wp1121-budget", "fw", "generic", 10)
	for i := 0; i < 200; i++ {
		ranges := make([]vulnfeed.Affected, 0, 10)
		for j := 0; j < 10; j++ {
			ranges = append(ranges, vulnfeed.Affected{Vendor: "wp1121-budget", Product: "fw", VersionExact: fmt.Sprintf("9.%d.%d", i, j)})
		}
		if i == 0 {
			ranges[0] = vulnfeed.Affected{Vendor: "wp1121-budget", Product: "fw", VersionExact: "1.0.7"}
		}
		if err := e.catalog.ReplaceAdvisory(bg, &vulnfeed.Advisory{ID: fmt.Sprintf("wp1121:budget-%d", i), SourceID: matchCSAF, Severity: "medium"}, ranges); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.f.Admin.Exec(bg, `INSERT INTO ci (organization_id, ci_type_id, name, status, manufacturer, model, firmware_version)
		SELECT $1, (SELECT id FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1), 'wp1121-budget-' || g, 'active',
		       'WP1121Budget', 'M', '1.0.' || (g % 100)
		  FROM generate_series(1, 10000) g`, e.f.OrgA); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	res := e.run()
	took := time.Since(start)
	if res.CIsChecked < 10000 || res.FindingsCreated != 100 {
		t.Errorf("result %+v, want 10,000 checked and 100 findings", res)
	}
	if took > 60*time.Second {
		t.Errorf("match of 10,000 CIs × 2,000 ranges took %v, budget 60 s", took)
	}
	t.Logf("10,000 CIs × 2,000 ranges: %v", took)
}

// TestMatcherProductWithoutVersion: a range without exact version and
// bounds (a CSAF product without a version) affects every version of the
// product and yields a finding.
func TestMatcherProductWithoutVersion(t *testing.T) {
	e := newMatchEnv(t)
	e.mapping(e.f.OrgA, "wp1121 zyxel*", "wp1121-zyxel", "usg", "generic", 10)
	e.advisory("wp1121:all", matchCSAF, []string{"CVE-2026-11212"}, vulnfeed.Affected{Vendor: "wp1121-zyxel", Product: "usg", VersionScheme: "generic"}, false)
	ci := e.device("wp1121-usg", "WP1121 Zyxel", "USG FLEX 100", "", "5.37")
	if res := e.run(ci); res.FindingsCreated != 1 {
		t.Fatalf("result %+v, want one finding for every version", res)
	}
	if got := e.findingsOf(ci); got["wp1121:all"] != "open/" {
		t.Errorf("findings %v, want an open finding", got)
	}
}

// changedMeanwhile marks a finding false_positive just before the match
// resolves it, as a user between the read and the resolution would.
type changedMeanwhile struct{ *security.PGRepository }

func (c changedMeanwhile) Update(ctx context.Context, orgID, id string, req security.UpdateFindingRequest) (*security.Finding, error) {
	fp, reason := security.StatusFalsePositive, "checked by hand"
	if _, err := c.PGRepository.Update(ctx, orgID, id, security.UpdateFindingRequest{Status: &fp, StatusReason: &reason}); err != nil {
		return nil, err
	}
	return c.PGRepository.Update(ctx, orgID, id, req)
}

// TestMatcherSkipsFindingChangedMeanwhile: a finding that changed status
// between the read and the resolution (refused transition) is skipped,
// the run does not fail.
func TestMatcherSkipsFindingChangedMeanwhile(t *testing.T) {
	e := newMatchEnv(t)
	e.mapping(e.f.OrgA, "wp1121 mikrotik*", "wp1121-mikrotik", "rtos", "generic", 10)
	e.advisory("wp1121:a1", matchCSAF, []string{"CVE-2026-11211"}, mikrotik, false)
	ci := e.device("wp1121-rb", "WP1121 MikroTik", "RB5009", "", "7.12.1")
	e.run()
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE ci SET firmware_version = '7.13' WHERE id = $1`, ci); err != nil {
		t.Fatal(err)
	}
	e.matcher.WithFindingsForTest(changedMeanwhile{e.findings})
	res, err := e.matcher.Run(e.ctx, e.f.OrgA, []string{ci}, "")
	if err != nil {
		t.Fatalf("run with a finding changed meanwhile: %v, want it skipped", err)
	}
	if res.FindingsResolved != 0 {
		t.Errorf("result %+v, want nothing counted as resolved", res)
	}
	if got := e.findingsOf(ci); got["wp1121:a1"] != "false_positive/checked by hand" {
		t.Errorf("findings %v, want the status of the user", got)
	}
}
