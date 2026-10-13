package lifecycle_test

import (
	"context"
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
