package lifecycle_test

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
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
)

// TestOfflineDetector covers WP-157 (REC-09, LCY-05) against PostgreSQL:
// an observable, discovered CI not seen for three times the interval of its
// covering scope becomes unknown with ci.offline, and active with ci.online
// once seen again. Manual CIs, imported CIs, CIs of non-observable types
// and CIs within their interval stay as they are; without a scope the
// organization's sweep interval applies.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestOfflineDetector(t *testing.T) {
	f := scopetest.Seed(t, "a8")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	typeID := func(key string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = $1 AND organization_id IS NULL`, key).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	server, application := typeID("server"), typeID("application")
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

	collector := f.ID()
	exec(`INSERT INTO collector (id, organization_id, client_id, name, status) VALUES ($1, $2, $3, 'wp157', 'active')`, collector, f.OrgA, f.Client1)
	// Scope 10.30.0.0/24 every hour: offline after three hours.
	exec(`INSERT INTO discovery_scope (organization_id, collector_id, name, kind, target, schedule)
		VALUES ($1, $2, 'net', 'cidr', '10.30.0.0/24', '1h'), ($1, $2, 'range', 'range', '10.31.0.10-10.31.0.20', '2h')`, f.OrgA, collector)

	type ciSpec struct {
		name, typ, source, ip string
		manual                bool
		seen                  time.Duration
		status                string
	}
	specs := map[string]ciSpec{
		"stale-in-scope":    {"stale-in-scope", server, "snmp", "10.30.0.5", false, 4 * time.Hour, "active"},
		"fresh-in-scope":    {"fresh-in-scope", server, "snmp", "10.30.0.6", false, 2 * time.Hour, "active"},
		"stale-in-range":    {"stale-in-range", server, "sweep", "10.31.0.15", false, 7 * time.Hour, "active"},
		"manual":            {"manual", server, "snmp", "10.30.0.7", true, 10 * time.Hour, "active"},
		"imported":          {"imported", server, "import", "10.30.0.8", false, 10 * time.Hour, "active"},
		"not-observable":    {"not-observable", application, "snmp", "10.30.0.9", false, 10 * time.Hour, "active"},
		"no-scope-fresh":    {"no-scope-fresh", server, "redfish", "192.168.9.9", false, 48 * time.Hour, "active"},
		"no-scope-stale":    {"no-scope-stale", server, "redfish", "192.168.9.10", false, 80 * time.Hour, "active"},
		"seen-again":        {"seen-again", server, "snmp", "10.30.0.10", false, 10 * time.Minute, "unknown"},
		"maintenance-stale": {"maintenance-stale", server, "snmp", "10.30.0.11", false, 10 * time.Hour, "maintenance"},
	}
	ids := map[string]string{}
	for key, s := range specs {
		id := f.ID()
		ids[key] = id
		exec(`INSERT INTO ci (id, organization_id, client_id, ci_type_id, name, status, discovery_source, management_ip, is_manual, last_seen_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8::inet, $9, $10)`,
			id, f.OrgA, f.Client1, s.typ, "wp157-"+s.name, s.status, s.source, s.ip, s.manual, now.Add(-s.seen))
	}

	d := lifecycle.NewOfflineDetector(f.App).WithClock(func() time.Time { return now })
	changes, err := d.RunOrg(bg, f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	if changes.Offline != 3 || changes.Online != 1 {
		t.Errorf("changes = %+v, want 3 offline (scope, range, org interval) and 1 online", changes)
	}
	want := map[string]string{
		"stale-in-scope": "unknown", "fresh-in-scope": "active", "stale-in-range": "unknown", "manual": "active",
		"imported": "active", "not-observable": "active", "no-scope-fresh": "active", "no-scope-stale": "unknown",
		"seen-again": "active", "maintenance-stale": "maintenance",
	}
	for key, status := range want {
		var got string
		if err = f.Admin.QueryRow(bg, `SELECT status FROM ci WHERE id = $1`, ids[key]).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != status {
			t.Errorf("%s: status %s, want %s", key, got, status)
		}
	}
	var offline, online int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FILTER (WHERE type = 'ci.offline'), count(*) FILTER (WHERE type = 'ci.online')
		FROM event_outbox WHERE organization_id = $1`, f.OrgA).Scan(&offline, &online); err != nil {
		t.Fatal(err)
	}
	if offline != 3 || online != 1 {
		t.Errorf("events: %d ci.offline, %d ci.online", offline, online)
	}

	// A second run changes nothing; the organization's sweep interval
	// (here 96 h) keeps the CI without scope inside its window.
	if changes, err = d.RunOrg(bg, f.OrgA); err != nil || changes.Offline != 0 || changes.Online != 0 {
		t.Errorf("second run: %+v %v", changes, err)
	}
	exec(`UPDATE organization SET settings = '{"discovery": {"sweep_interval": "96h"}}' WHERE id = $1`, f.OrgA)
	exec(`UPDATE ci SET status = 'active' WHERE id = $1`, ids["no-scope-stale"])
	if changes, err = d.RunOrg(bg, f.OrgA); err != nil || changes.Offline != 0 {
		t.Errorf("with a 96 h organization interval: %+v %v", changes, err)
	}
}

// TestOfflineDetectorOrgIsolation covers WP-383 (REC-09, LCY-05; F-108)
// against PostgreSQL with two organizations, the failing one sorted first.
// A scope whose target PostgreSQL cannot read as inet – an IPv6 zone, which
// the API accepted before, or another malformed row – is skipped and logged
// once per run as discovery.scope.invalid_target with its id; the
// organization's other scopes still apply and its run succeeds. A failing
// organization (here a last_seen_at that cannot be read) does not stop Run:
// the CI of the organization sorted after it becomes unknown with
// ci.offline, Run returns the joined error naming only the failing one, and
// the totals count the organizations that succeeded.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestOfflineDetectorOrgIsolation(t *testing.T) {
	f := scopetest.Seed(t, "7a")
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
	var server string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&server); err != nil {
		t.Fatal(err)
	}
	// Run checks every organization of the database: the clock is the real
	// one, as in production.
	now := time.Now().UTC()
	ci := func(org, name, ip string, seen time.Duration) string {
		t.Helper()
		id := f.ID()
		exec(`INSERT INTO ci (id, organization_id, ci_type_id, name, status, discovery_source, management_ip, is_manual, last_seen_at)
			VALUES ($1, $2, $3, $4, 'active', 'snmp', $5::inet, false, $6)`, id, org, server, "wp383-"+name, ip, now.Add(-seen))
		return id
	}
	status := func(id string) string {
		t.Helper()
		var s string
		if err := f.Admin.QueryRow(bg, `SELECT status FROM ci WHERE id = $1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	// Organization first: every scope runs hourly (offline after 3 h); the
	// organization's default sweep of 24 h makes it 72 h.
	collector := f.ID()
	exec(`INSERT INTO collector (id, organization_id, name, status) VALUES ($1, $2, 'wp383', 'active')`, collector, first)
	scopes := map[string]string{}
	rows, err := f.Admin.Query(bg, `INSERT INTO discovery_scope (organization_id, collector_id, name, kind, target, schedule) VALUES
		($1, $2, 'zoned-range', 'range', 'fe80::1%eth0-fe80::5%eth0', '1h'),
		($1, $2, 'zoned-host', 'host', 'fe80::1%eth0', '1h'),
		($1, $2, 'bad-cidr', 'cidr', '10.70.0.0/33', '1h'),
		($1, $2, 'bad-range', 'range', '10.70.1.300-10.70.1.310', '1h'),
		($1, $2, 'net', 'cidr', '10.70.2.0/24', '1h')
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
	// Applied anyway (the zone dropped, the prefix clamped), the malformed
	// scopes would set these CIs offline after 3 h; skipped, 72 h apply.
	inZonedRange := ci(first, "in-zoned-range", "fe80::3", 10*time.Hour)
	zonedHost := ci(first, "zoned-host", "fe80::1", 10*time.Hour)
	inBadCIDR := ci(first, "in-bad-cidr", "10.70.0.5", 10*time.Hour)
	inScope := ci(first, "in-scope", "10.70.2.5", 4*time.Hour)

	logs := captureLogs(t)
	d := lifecycle.NewOfflineDetector(f.App).WithClock(func() time.Time { return now })
	changes, err := d.RunOrg(bg, first)
	if err != nil {
		t.Fatalf("RunOrg with malformed scope targets: %v", err)
	}
	if changes.Offline != 1 || changes.Online != 0 {
		t.Errorf("changes = %+v, want 1 offline (the valid scope)", changes)
	}
	for id, want := range map[string]string{inZonedRange: "active", zonedHost: "active", inBadCIDR: "active", inScope: "unknown"} {
		if got := status(id); got != want {
			t.Errorf("CI %s: status %s, want %s", id, got, want)
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

	// The failing organization: a CI whose last_seen_at cannot be read.
	unreadable := ci(first, "unreadable", "10.70.2.6", time.Hour)
	exec(`UPDATE ci SET last_seen_at = 'infinity' WHERE id = $1`, unreadable)
	if _, err = d.RunOrg(bg, first); err == nil {
		t.Fatal("control: RunOrg of the organization with the unreadable CI succeeded")
	}
	// Organization second: a CI last seen 400 h ago, overdue after 72 h.
	stale := ci(second, "stale", "10.70.9.9", 400*time.Hour)

	logs.reset()
	changes, err = d.Run(bg)
	if err == nil || !strings.Contains(err.Error(), "organization "+first+":") || strings.Contains(err.Error(), "organization "+second+":") {
		t.Errorf("Run error = %v, want the joined error naming organization %s and not %s", err, first, second)
	}
	if got := status(stale); got != "unknown" {
		t.Errorf("tenant isolation: the CI of organization %s last seen 400 h ago is %s after Run, want unknown", second, got)
	}
	var events int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND type = 'ci.offline' AND entity_id = $2`,
		second, stale).Scan(&events); err != nil || events != 1 {
		t.Errorf("ci.offline events of organization %s: %d (%v), want 1", second, events, err)
	}
	if changes.Offline < 1 {
		t.Errorf("Run totals = %+v, want the CI set offline in organization %s counted", changes, second)
	}
	checkWarnings("Run")
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
// offline detector for scopeID of orgID.
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
			rec["job"] == lifecycle.OfflineDetectorKind && rec["level"] == "WARN" {
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
