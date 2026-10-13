package relationship_test

import (
	"context"
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
