// Phase-1 acceptance audit, group T4-collector-load – REGRESSION test (must
// pass).
//
// WP-159 (TOP-02, REL-08). Critical test gap "TOP-02 deadline for cron,
// range and host scopes": the existing TestDiscoveryEdgeCleanup covers cidr
// scopes only. This test pins the deadline max(7 d, 5 × scope interval) for
// host scopes (by IP and by hostname), range scopes, a disabled scope (the
// organization's sweep interval applies), the exact boundary (deadline kept,
// deadline + 1 s removed) and the stale_after value of relationship.removed.
// The cron part of the gap is a defect and lives in
// cleanup_cron_auditdefect_test.go.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -v -p 1 -run 'TestAuditCleanupDeadlinePerScopeKind' ./internal/relationship/
package relationship_test

import (
	"context"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
)

func TestAuditCleanupDeadlinePerScopeKind(t *testing.T) {
	f := scopetest.Seed(t, "d7")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	// PostgreSQL keeps microseconds; the boundary cases need an exact clock.
	now := time.Now().UTC().Truncate(time.Second)
	day := 24 * time.Hour
	collector := f.ID()
	exec(`INSERT INTO collector (id, organization_id, name, status) VALUES ($1, $2, 'audit-deadline', 'active')`, collector, f.OrgA)
	exec(`INSERT INTO discovery_scope (organization_id, collector_id, name, kind, target, schedule, enabled) VALUES
		($1, $2, 'host-ip',   'host',  '10.95.1.1',                 '72h',  true),
		($1, $2, 'host-name', 'host',  'audit-deadline-sw.example', '72h',  true),
		($1, $2, 'range',     'range', '10.95.2.10-10.95.2.20',     '168h', true),
		($1, $2, 'disabled',  'cidr',  '10.95.3.0/24',              '168h', false),
		($1, $2, 'boundary',  'cidr',  '10.95.4.0/24',              '48h',  true)`, f.OrgA, collector)

	source := func(name, ip, hostname string) string {
		id := f.CI(t, f.OrgA, "", "audit-deadline-"+name)
		exec(`UPDATE ci SET management_ip = $2::inet, hostname = NULLIF($3, '') WHERE id = $1`, id, ip, hostname)
		return id
	}
	hostIP := source("host-ip", "10.95.1.1", "")
	hostName := source("host-name", "10.95.9.9", "audit-deadline-sw.example")
	inRange := source("range", "10.95.2.15", "")
	disabled := source("disabled", "10.95.3.1", "")
	boundary := source("boundary", "10.95.4.1", "")

	type edgeCase struct {
		name      string
		src       string
		confirmed time.Duration
		kept      bool
		// staleAfter is the expected stale_after of the removal event.
		staleAfter string
	}
	cases := []edgeCase{
		{"host scope by IP 72h: 14 d < 15 d kept", hostIP, 14 * day, true, ""},
		{"host scope by IP 72h: 16 d > 15 d removed", hostIP, 16 * day, false, "360h0m0s"},
		{"host scope by hostname 72h: 14 d kept", hostName, 14 * day, true, ""},
		{"host scope by hostname 72h: 16 d removed", hostName, 16 * day, false, "360h0m0s"},
		{"range scope 168h: 34 d < 35 d kept", inRange, 34 * day, true, ""},
		{"range scope 168h: 36 d > 35 d removed", inRange, 36 * day, false, "840h0m0s"},
		{"disabled 168h scope ignored, org default 24h: 6 d kept", disabled, 6 * day, true, ""},
		{"disabled 168h scope ignored, org default 24h: 8 d removed", disabled, 8 * day, false, "168h0m0s"},
		{"cidr 48h at exactly the 10 d deadline kept", boundary, 10 * day, true, ""},
		{"cidr 48h at deadline + 1 s removed", boundary, 10*day + time.Second, false, "240h0m0s"},
	}
	ids := make([]string, len(cases))
	for i, tc := range cases {
		peer := f.CI(t, f.OrgA, "", "audit-deadline-peer-"+string(rune('a'+i)))
		if err := f.Admin.QueryRow(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source,
				verification_state, last_seen_at, last_confirmed_at, confidence)
			VALUES ($1, $2, $3, 'connected_to', 'discovery', 'unverified', $4, $4, 1.0) RETURNING id::text`,
			f.OrgA, tc.src, peer, now.Add(-tc.confirmed)).Scan(&ids[i]); err != nil {
			t.Fatal(err)
		}
	}

	res, err := relationship.NewCleanup(f.App).WithClock(func() time.Time { return now }).RunOrg(bg, f.OrgA)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	removed := 0
	for i, tc := range cases {
		var n int
		if qerr := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_relationship WHERE id = $1`, ids[i]).Scan(&n); qerr != nil {
			t.Fatal(qerr)
		}
		if kept := n == 1; kept != tc.kept {
			t.Errorf("%s: kept = %v, want %v", tc.name, kept, tc.kept)
		}
		if tc.kept {
			continue
		}
		removed++
		var staleAfter, reason string
		if qerr := f.Admin.QueryRow(bg, `SELECT COALESCE(data->>'stale_after', ''), COALESCE(data->>'reason', '') FROM event_outbox
			WHERE organization_id = $1 AND type = 'relationship.removed' AND entity_id = $2`, f.OrgA, ids[i]).Scan(&staleAfter, &reason); qerr != nil {
			t.Errorf("%s: relationship.removed event: %v", tc.name, qerr)
			continue
		}
		if staleAfter != tc.staleAfter || reason != "stale" {
			t.Errorf("%s: relationship.removed stale_after=%q reason=%q, want %q/stale", tc.name, staleAfter, reason, tc.staleAfter)
		}
	}
	if res.Stale != removed || res.Suppressed != 0 {
		t.Errorf("cleanup result = %+v, want Stale=%d", res, removed)
	}
}
