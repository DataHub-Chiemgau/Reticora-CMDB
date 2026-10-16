//go:build auditdefect

// Phase-1 acceptance audit, group T4-collector-load – DEFECT test (must fail
// until the defect is fixed).
//
// WP-159 (TOP-02). Finding P2 "Cleanup treats cron scope schedules as 24h;
// edges of weekly scopes are deleted after 7 days": scopeInterval
// (cleanup.go) maps every non-duration schedule to the 24 h default and the
// SQL does not read last_run_at/next_run_at, so the deadline of a weekly
// cron scope is max(7 d, 5 × 24 h) = 7 d instead of max(7 d, 5 × 7 d) = 35 d.
// Discovery edges of weekly and monthly scopes are deleted although
// discovery had no chance to re-confirm them (data loss).
//
// The 168h duration scope is the control: same weekly interval, written as a
// duration.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -tags auditdefect -count=1 -v -p 1 -run 'TestAuditCleanupCronScopeDeadline' ./internal/relationship/
package relationship_test

import (
	"context"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
)

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
	// ago and run again in 4 days, the monthly scope ran 10 days ago.
	exec(`INSERT INTO discovery_scope (organization_id, collector_id, name, kind, target, schedule, last_run_at, next_run_at) VALUES
		($1, $2, 'weekly-duration', 'cidr', '10.96.1.0/24', '168h',        $3, $4),
		($1, $2, 'weekly-cron',     'cidr', '10.96.2.0/24', '0 3 * * 0',   $3, $4),
		($1, $2, 'monthly-cron',    'cidr', '10.96.3.0/24', '0 3 1 * *',   $5, $6)`,
		f.OrgA, collector, now.Add(-3*day), now.Add(4*day), now.Add(-10*day), now.Add(21*day))
	source := func(name, ip string) string {
		id := f.CI(t, f.OrgA, "", "audit-cron-"+name)
		exec(`UPDATE ci SET management_ip = $2::inet WHERE id = $1`, id, ip)
		return id
	}
	weeklyDuration, weeklyCron, monthlyCron := source("dur", "10.96.1.1"), source("cron", "10.96.2.1"), source("monthly", "10.96.3.1")

	cases := []struct {
		name       string
		src        string
		confirmed  time.Duration
		kept       bool
		staleAfter string
	}{
		{"control: 168h duration scope, edge confirmed 10 d ago kept (deadline 35 d)", weeklyDuration, 10 * day, true, ""},
		{"weekly cron '0 3 * * 0', edge confirmed 10 d ago kept (deadline 35 d)", weeklyCron, 10 * day, true, ""},
		{"weekly cron '0 3 * * 0', edge confirmed 36 d ago removed with stale_after=840h0m0s", weeklyCron, 36 * day, false, "840h0m0s"},
		{"monthly cron '0 3 1 * *', edge confirmed 36 d ago kept (deadline >= 5 x 28 d)", monthlyCron, 36 * day, true, ""},
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
