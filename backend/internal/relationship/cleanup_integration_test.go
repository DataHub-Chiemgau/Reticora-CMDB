package relationship_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
)

// TestDiscoveryEdgeCleanup covers WP-159 (TOP-02, REL-08) against
// PostgreSQL: a re-confirmed discovery edge stays; an unconfirmed one is
// removed after max(7 days, 5 × the scope interval of its source CI) with
// relationship.removed; a suppressed discovery edge goes at once; manual,
// verified and other sources' edges are never removed by the cleanup.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDiscoveryEdgeCleanup(t *testing.T) {
	f := scopetest.Seed(t, "ab")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	now := time.Now().UTC()
	ci := func(name, ip string) string {
		id := f.CI(t, f.OrgA, "", "wp159-"+name)
		if ip != "" {
			exec(`UPDATE ci SET management_ip = $2::inet WHERE id = $1`, id, ip)
		}
		return id
	}
	sw, slowSw, a, b, c, d, e, g := ci("sw", "10.40.0.1"), ci("slow-sw", "10.41.0.1"), ci("a", ""), ci("b", ""), ci("c", ""), ci("d", ""), ci("e", ""), ci("g", "")

	collector := f.ID()
	exec(`INSERT INTO collector (id, organization_id, name, status) VALUES ($1, $2, 'wp159', 'active')`, collector, f.OrgA)
	// 10.41.0.0/24 sweeps every 48 h: its edges stay 10 days (5 × 48 h).
	exec(`INSERT INTO discovery_scope (organization_id, collector_id, name, kind, target, schedule)
		VALUES ($1, $2, 'slow', 'cidr', '10.41.0.0/24', '48h')`, f.OrgA, collector)

	edge := func(src, dst, source, state string, confirmed time.Duration) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source,
				verification_state, last_seen_at, last_confirmed_at, confidence)
			VALUES ($1, $2, $3, 'connected_to', $4, $5, $6, $6, 1.0) RETURNING id::text`,
			f.OrgA, src, dst, source, state, now.Add(-confirmed)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	fresh := edge(sw, a, "discovery", "unverified", 2*24*time.Hour)
	stale := edge(sw, b, "discovery", "unverified", 8*24*time.Hour)
	slowKept := edge(slowSw, c, "discovery", "unverified", 8*24*time.Hour)
	slowStale := edge(slowSw, d, "discovery", "unverified", 11*24*time.Hour)
	verified := edge(sw, e, "discovery", "verified", 30*24*time.Hour)
	manual := edge(sw, g, "manual", "verified", 30*24*time.Hour)
	agent := edge(a, b, "agent", "unverified", 30*24*time.Hour)
	suppressed := edge(c, d, "discovery", "unverified", time.Hour)
	var typeID string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM relationship_type WHERE key = 'connected_to' AND organization_id IS NULL`).Scan(&typeID); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO relationship_suppression (organization_id, source_ci_id, target_ci_id, relationship_type_id) VALUES ($1, $2, $3, $4)`, f.OrgA, d, c, typeID)

	// Discovery sees the stale edge again before the cleanup: confirmed.
	repo := relationship.NewPGRepository(f.App)
	staleSeenAgain := edge(sw, c, "discovery", "unverified", 9*24*time.Hour)
	conf := 0.6
	if err := repo.Confirm(f.OrgCtx(f.OrgA), f.OrgA, staleSeenAgain, &conf); err != nil {
		t.Fatal(err)
	}
	if err := repo.Confirm(f.OrgCtx(f.OrgA), f.OrgA, manual, &conf); err == nil {
		t.Error("discovery re-confirmed a manual edge")
	}

	res, err := relationship.NewCleanup(f.App).WithClock(func() time.Time { return now }).RunOrg(bg, f.OrgA)
	if err != nil || res.Stale != 2 || res.Suppressed != 1 {
		t.Fatalf("cleanup = %+v, %v", res, err)
	}
	exists := func(id string) bool {
		var n int
		if qerr := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_relationship WHERE id = $1`, id).Scan(&n); qerr != nil {
			t.Fatal(qerr)
		}
		return n == 1
	}
	for name, tc := range map[string]struct {
		id   string
		kept bool
	}{
		"fresh": {fresh, true}, "stale": {stale, false}, "slow scope kept": {slowKept, true}, "slow scope stale": {slowStale, false},
		"verified": {verified, true}, "manual": {manual, true}, "agent": {agent, true}, "suppressed": {suppressed, false},
		"seen again": {staleSeenAgain, true},
	} {
		if exists(tc.id) != tc.kept {
			t.Errorf("%s: kept = %v, want %v", name, !tc.kept, tc.kept)
		}
	}
	var removed, suppressedEvents int
	if err = f.Admin.QueryRow(bg, `SELECT count(*), count(*) FILTER (WHERE data->>'reason' = 'suppressed') FROM event_outbox
		WHERE organization_id = $1 AND type = 'relationship.removed'`, f.OrgA).Scan(&removed, &suppressedEvents); err != nil {
		t.Fatal(err)
	}
	if removed != 3 || suppressedEvents != 1 {
		t.Errorf("relationship.removed events: %d (%d suppressed)", removed, suppressedEvents)
	}
	var confirmedConf float64
	if err = f.Admin.QueryRow(bg, `SELECT confidence::float8 FROM ci_relationship WHERE id = $1 AND last_confirmed_at > now() - interval '1 minute'`,
		staleSeenAgain).Scan(&confirmedConf); err != nil || confirmedConf != 0.6 {
		t.Errorf("re-confirmed edge: confidence %v, %v", confirmedConf, err)
	}
}

// TestDiscoveryEdgeCleanupOrgIsolation covers WP-383 (TOP-02; F-108)
// against PostgreSQL with two organizations, the failing one sorted first.
// A scope whose target PostgreSQL cannot read as inet – an IPv6 zone, which
// the API accepted before, or another malformed row – is skipped and logged
// once per run as discovery.scope.invalid_target with its id; the
// organization's other scopes still apply and its cleanup succeeds. A
// failing organization (here a last_confirmed_at that cannot be read) does
// not stop Run: the stale edge of the organization sorted after it is
// removed with relationship.removed, Run returns the joined error naming
// only the failing one, and the totals count the organizations that
// succeeded.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDiscoveryEdgeCleanupOrgIsolation(t *testing.T) {
	f := scopetest.Seed(t, "7b")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	first, second := f.OrgA, f.OrgB
	if second < first {
		first, second = second, first
	}
	// Run cleans every organization of the database: the clock is the real
	// one, as in production.
	now := time.Now().UTC()
	ci := func(org, name, ip string) string {
		t.Helper()
		id := f.CI(t, org, "", "wp383-"+name)
		if ip != "" {
			exec(`UPDATE ci SET management_ip = $2::inet WHERE id = $1`, id, ip)
		}
		return id
	}
	edge := func(org, src, dst string, confirmed time.Duration) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source,
				verification_state, last_seen_at, last_confirmed_at, confidence)
			VALUES ($1, $2, $3, 'connected_to', 'discovery', 'unverified', $4, $4, 1.0) RETURNING id::text`,
			org, src, dst, now.Add(-confirmed)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	exists := func(id string) bool {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_relationship WHERE id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	day := 24 * time.Hour

	// Organization first: every scope sweeps every 72 h, so its edges stay
	// 15 days (5 × 72 h); without a scope the 7 days of the default apply.
	collector := f.ID()
	exec(`INSERT INTO collector (id, organization_id, name, status) VALUES ($1, $2, 'wp383', 'active')`, collector, first)
	scopes := map[string]string{}
	rows, err := f.Admin.Query(bg, `INSERT INTO discovery_scope (organization_id, collector_id, name, kind, target, schedule) VALUES
		($1, $2, 'zoned-range', 'range', 'fe80::1%eth0-fe80::5%eth0', '72h'),
		($1, $2, 'zoned-host', 'host', 'fe80::1%eth0', '72h'),
		($1, $2, 'bad-cidr', 'cidr', '10.71.0.0/33', '72h'),
		($1, $2, 'bad-range', 'range', '10.71.1.300-10.71.1.310', '72h'),
		($1, $2, 'net', 'cidr', '10.71.2.0/24', '72h')
		RETURNING name, id::text`, first, collector)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name, id string
		if err = rows.Scan(&name, &id); err != nil {
			t.Fatal(err)
		}
		scopes[name] = id
	}
	if rows.Close(); rows.Err() != nil || len(scopes) != 5 {
		t.Fatalf("scopes %v: %v", scopes, rows.Err())
	}
	peer := ci(first, "peer", "")
	// Applied anyway (the zone dropped, the prefix clamped), the malformed
	// scopes would keep these edges 15 days; skipped, the default 7 apply.
	inZonedRange := edge(first, ci(first, "in-zoned-range", "fe80::3"), peer, 10*day)
	zonedHost := edge(first, ci(first, "zoned-host", "fe80::1"), peer, 10*day)
	inBadCIDR := edge(first, ci(first, "in-bad-cidr", "10.71.0.5"), peer, 10*day)
	inScope := edge(first, ci(first, "in-scope", "10.71.2.5"), peer, 10*day)

	logs := captureLogs(t)
	cleanup := relationship.NewCleanup(f.App).WithClock(func() time.Time { return now })
	res, err := cleanup.RunOrg(bg, first)
	if err != nil {
		t.Fatalf("RunOrg with malformed scope targets: %v", err)
	}
	if res.Stale != 3 || res.Suppressed != 0 {
		t.Errorf("cleanup = %+v, want 3 stale (the edges of the skipped scopes)", res)
	}
	for id, kept := range map[string]bool{inZonedRange: false, zonedHost: false, inBadCIDR: false, inScope: true} {
		if exists(id) != kept {
			t.Errorf("edge %s: kept = %v, want %v", id, !kept, kept)
		}
	}
	checkWarnings := func(run string) {
		t.Helper()
		for name, id := range scopes {
			want := 1
			if name == "net" {
				want = 0
			}
			if got := logs.invalidTarget(id, first); got != want {
				t.Errorf("%s: scope %s (%s) logged %d times as discovery.scope.invalid_target, want %d", run, name, id, got, want)
			}
		}
	}
	checkWarnings("RunOrg")

	// The failing organization: an edge whose last_confirmed_at cannot be
	// read.
	unreadable := edge(first, ci(first, "unreadable", "10.71.2.6"), peer, day)
	exec(`UPDATE ci_relationship SET last_confirmed_at = 'infinity' WHERE id = $1`, unreadable)
	if _, err = cleanup.RunOrg(bg, first); err == nil {
		t.Fatal("control: RunOrg of the organization with the unreadable edge succeeded")
	}
	// Organization second: a discovery edge last confirmed 30 days ago.
	stale := edge(second, ci(second, "stale", "10.71.9.9"), ci(second, "stale-peer", ""), 30*day)

	logs.reset()
	res, err = cleanup.Run(bg)
	if err == nil || !strings.Contains(err.Error(), "organization "+first+":") || strings.Contains(err.Error(), "organization "+second+":") {
		t.Errorf("Run error = %v, want the joined error naming organization %s and not %s", err, first, second)
	}
	if exists(stale) {
		t.Errorf("tenant isolation: the discovery edge of organization %s last confirmed 30 days ago was kept by Run", second)
	}
	var events int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND type = 'relationship.removed' AND entity_id = $2`,
		second, stale).Scan(&events); err != nil || events != 1 {
		t.Errorf("relationship.removed events of organization %s: %d (%v), want 1", second, events, err)
	}
	if res.Stale < 1 {
		t.Errorf("Run totals = %+v, want the edge removed in organization %s counted", res, second)
	}
	if !exists(inScope) || !exists(unreadable) {
		t.Error("the failed run of the first organization changed its edges")
	}
	checkWarnings("Run")
}

// TestAuditCleanupCronScopeDeadline covers WP-497 (TOP-02; F-111) against
// PostgreSQL: the deadline of a discovery edge is max(7 days, 5 × the scope
// interval), and the interval of a cron scope is the gap between its runs
// (next_run_at − last_run_at), without a run the gap between its next two
// cron slots – not the 24 h default. A weekly cron scope keeps an edge for
// 35 days, a monthly one for at least 140 days. The 168h duration scope is
// the control: same weekly interval, written as a duration.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -v -p 1 -run 'TestAuditCleanupCronScopeDeadline' ./internal/relationship/
func TestAuditCleanupCronScopeDeadline(t *testing.T) {
	f := scopetest.Seed(t, "d8")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	day := 24 * time.Hour
	collector := f.ID()
	exec(`INSERT INTO collector (id, organization_id, name, status) VALUES ($1, $2, 'audit-cron', 'active')`, collector, f.OrgA)
	// The server holds the run state of each scope: weekly scopes ran 3 days
	// ago and run again in 4 days, the monthly scope ran 10 days ago; the
	// "-new" scopes never ran.
	exec(`INSERT INTO discovery_scope (organization_id, collector_id, name, kind, target, schedule, last_run_at, next_run_at) VALUES
		($1, $2, 'weekly-duration',  'cidr', '10.96.1.0/24', '168h',      $3, $4),
		($1, $2, 'weekly-cron',      'cidr', '10.96.2.0/24', '0 3 * * 0', $3, $4),
		($1, $2, 'monthly-cron',     'cidr', '10.96.3.0/24', '0 3 1 * *', $5, $6),
		($1, $2, 'weekly-cron-new',  'cidr', '10.96.4.0/24', '0 3 * * 0', NULL, NULL),
		($1, $2, 'monthly-cron-new', 'cidr', '10.96.5.0/24', '0 3 1 * *', NULL, NULL)`,
		f.OrgA, collector, now.Add(-3*day), now.Add(4*day), now.Add(-10*day), now.Add(21*day))
	source := func(name, ip string) string {
		id := f.CI(t, f.OrgA, "", "audit-cron-"+name)
		exec(`UPDATE ci SET management_ip = $2::inet WHERE id = $1`, id, ip)
		return id
	}
	weeklyDuration, weeklyCron, monthlyCron := source("dur", "10.96.1.1"), source("cron", "10.96.2.1"), source("monthly", "10.96.3.1")
	weeklyNew, monthlyNew := source("cron-new", "10.96.4.1"), source("monthly-new", "10.96.5.1")

	cases := []struct {
		name       string
		src        string
		confirmed  time.Duration
		kept       bool
		staleAfter string
	}{
		{"control: 168h duration scope, edge confirmed 10 d ago kept (deadline 35 d)", weeklyDuration, 10 * day, true, ""},
		{"control: 168h duration scope, edge confirmed 36 d ago removed with stale_after=840h0m0s", weeklyDuration, 36 * day, false, "840h0m0s"},
		{"weekly cron '0 3 * * 0', edge confirmed 10 d ago kept (deadline 35 d)", weeklyCron, 10 * day, true, ""},
		{"weekly cron '0 3 * * 0', edge confirmed 34 d ago kept (deadline 35 d)", weeklyCron, 34 * day, true, ""},
		{"weekly cron '0 3 * * 0', edge confirmed 36 d ago removed with stale_after=840h0m0s", weeklyCron, 36 * day, false, "840h0m0s"},
		{"monthly cron '0 3 1 * *', edge confirmed 36 d ago kept (deadline 5 x 31 d)", monthlyCron, 36 * day, true, ""},
		{"monthly cron '0 3 1 * *', edge confirmed 156 d ago removed with stale_after=3720h0m0s", monthlyCron, 156 * day, false, "3720h0m0s"},
		{"weekly cron never run, edge confirmed 10 d ago kept (next two slots: 7 d)", weeklyNew, 10 * day, true, ""},
		{"monthly cron never run, edge confirmed 139 d ago kept (next two slots: >= 28 d)", monthlyNew, 139 * day, true, ""},
	}
	ids := make([]string, len(cases))
	for i, tc := range cases {
		peer := f.CI(t, f.OrgA, "", "audit-cron-peer-"+string(rune('a'+i)))
		if err := f.Admin.QueryRow(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source,
				verification_state, last_seen_at, last_confirmed_at, confidence)
			VALUES ($1, $2, $3, 'connected_to', 'discovery', 'unverified', $4, $4, 1.0) RETURNING id::text`,
			f.OrgA, tc.src, peer, now.Add(-tc.confirmed)).Scan(&ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := relationship.NewCleanup(f.App).WithClock(func() time.Time { return now }).RunOrg(bg, f.OrgA); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	for i, tc := range cases {
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_relationship WHERE id = $1`, ids[i]).Scan(&n); err != nil {
			t.Fatal(err)
		}
		var staleAfter string
		_ = f.Admin.QueryRow(bg, `SELECT COALESCE(max(data->>'stale_after'), '') FROM event_outbox
			WHERE organization_id = $1 AND type = 'relationship.removed' AND entity_id = $2`, f.OrgA, ids[i]).Scan(&staleAfter)
		if kept := n == 1; kept != tc.kept {
			t.Errorf("TOP-02 violated: %s: kept = %v (relationship.removed stale_after=%q)", tc.name, kept, staleAfter)
			continue
		}
		if !tc.kept && staleAfter != tc.staleAfter {
			t.Errorf("TOP-02 violated: %s: relationship.removed stale_after=%q, want %q", tc.name, staleAfter, tc.staleAfter)
		}
	}
}

// logCapture collects the JSON log records of slog's default logger.
type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *logCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *logCapture) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.buf.Reset()
}

// invalidTarget counts the discovery.scope.invalid_target records of the
// edge cleanup for scopeID of orgID.
func (c *logCapture) invalidTarget(scopeID, orgID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, line := range bytes.Split(c.buf.Bytes(), []byte("\n")) {
		var rec map[string]any
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		if rec["event"] == "discovery.scope.invalid_target" && rec["scope_id"] == scopeID && rec["organization_id"] == orgID &&
			rec["job"] == relationship.CleanupKind && rec["level"] == "WARN" {
			n++
		}
	}
	return n
}

func captureLogs(t *testing.T) *logCapture {
	t.Helper()
	c := &logCapture{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(c, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return c
}

func TestSourcePolicy(t *testing.T) {
	for in, want := range map[string]string{"": "manual", "Discovery": "discovery", "rule": "workflow", "integration": "integration"} {
		if got, ok := relationship.CanonicalSource(in); !ok || got != want {
			t.Errorf("CanonicalSource(%q) = %q, %v", in, got, ok)
		}
	}
	if _, ok := relationship.CanonicalSource("telepathy"); ok {
		t.Error("unknown source accepted")
	}
	if len(relationship.Sources) != 7 {
		t.Errorf("sources = %v", relationship.Sources)
	}
	for _, tc := range []struct {
		by, owner string
		want      bool
	}{
		{"discovery", "discovery", true}, {"discovery", "manual", false}, {"discovery", "agent", false},
		{"manual", "discovery", true}, {"api", "agent", true}, {"import", "manual", false}, {"agent", "agent", true},
	} {
		if got := relationship.MayChange(tc.by, tc.owner); got != tc.want {
			t.Errorf("MayChange(%s, %s) = %v", tc.by, tc.owner, got)
		}
	}
	p := relationship.Provenance("user-1", "user", "manual", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	if p["actor"] != "user-1" || p["at"] != "2026-01-02T03:04:05Z" || p["source"] != "manual" {
		t.Errorf("provenance = %v", p)
	}
}
