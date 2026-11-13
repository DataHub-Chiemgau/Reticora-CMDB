package probe_test

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// Aggregation tiers of the probe results (PRB-06, NFR-06; WP-972, migration
// 000340_probe_result_rollups, ADR 0001, ADR 0002 section
// "Aggregationsstufen"): the hierarchical continuous aggregates
// probe_result_5m → 1h → 1d without stale-epoch results, sums and counters
// that stay exact across the tiers, refresh windows of at least two days for
// late results, compression and retention policies, the security-barrier
// views with organization, client and site predicates, no direct access of
// the application role and the truncate guard on the materializations.
//
// The refresh runs through refresh_continuous_aggregate on the maintenance
// pool (outside a transaction, in the order of the tiers).
//
// Retest:
//
//	cd backend && go test -count=1 -p 1 -run 'TestProbeRollup' ./internal/probe/

// rollupTiers lists the aggregates in refresh order.
var rollupTiers = []struct {
	name, width    string
	start, end     time.Duration
	schedule       time.Duration
	compressAfter  string
	retentionAfter string
}{
	{"probe_result_5m", "5 minutes", 49 * time.Hour, 5 * time.Minute, 5 * time.Minute, "3 days", "90 days"},
	{"probe_result_1h", "1 hour", 72 * time.Hour, time.Hour, time.Hour, "7 days", "400 days"},
	{"probe_result_1d", "1 day", 120 * time.Hour, 24 * time.Hour, 24 * time.Hour, "30 days", "400 days"},
}

// newRollupEnv is a result environment whose aggregates are rebuilt from the
// remaining raw rows before and after the test, so buckets of earlier runs
// do not count.
func newRollupEnv(t *testing.T) *resultEnv {
	t.Helper()
	e := newResultEnv(t)
	e.refreshAll()
	t.Cleanup(func() {
		for _, tier := range rollupTiers {
			if _, err := e.f.Admin.Exec(context.Background(), `CALL refresh_continuous_aggregate('`+tier.name+`', NULL, NULL)`); err != nil {
				t.Errorf("refresh %s: %v", tier.name, err)
			}
		}
	})
	return e
}

// refreshAll refreshes the three tiers over their whole range.
func (e *resultEnv) refreshAll() {
	e.t.Helper()
	for _, tier := range rollupTiers {
		e.mustExec(`CALL refresh_continuous_aggregate('` + tier.name + `', NULL, NULL)`)
	}
}

// insertResult stores one raw result directly in the hypertable.
func (e *resultEnv) insertResult(orgID, probeID string, seq int, at time.Time, status int16, rtt, loss, value any, stale bool) {
	e.t.Helper()
	e.mustExec(`INSERT INTO probe_result (time, organization_id, probe_id, collector_id, seq, epoch, status, rtt_ms, loss, value, stale_epoch)
		VALUES ($1, $2, $3, $4, $5, 1, $6, $7, $8, $9, $10)`, at, orgID, probeID, e.collector, seq, status, rtt, loss, value, stale)
}

// tierSums are the counters and sums of a probe in one tier (or the raw
// data).
type tierSums struct {
	buckets                     int
	total, ok, fail, errors     int64
	rttCount, lossCount, valCnt int64
	rttSum, lossSum, valSum     float64
}

// rawSums aggregates the raw rows of the probe directly (without stale
// results).
func (e *resultEnv) rawSums(probeID string) tierSums {
	e.t.Helper()
	var s tierSums
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*), count(*) FILTER (WHERE status = 0),
			count(*) FILTER (WHERE status = 1), count(*) FILTER (WHERE status = 2),
			count(rtt_ms), COALESCE(sum(rtt_ms::float8), 0), count(loss), COALESCE(sum(loss::float8), 0),
			count(value), COALESCE(sum(value), 0)
		FROM probe_result WHERE probe_id = $1 AND NOT stale_epoch`, probeID).Scan(&s.total, &s.ok, &s.fail, &s.errors,
		&s.rttCount, &s.rttSum, &s.lossCount, &s.lossSum, &s.valCnt, &s.valSum); err != nil {
		e.t.Fatal(err)
	}
	return s
}

// aggSums sums the buckets of the probe in the aggregate (maintenance pool)
// or, with ctx, in the view of the tier as the application.
func (e *resultEnv) aggSums(relation, probeID string) tierSums {
	e.t.Helper()
	var s tierSums
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*), COALESCE(sum(total), 0), COALESCE(sum(ok), 0),
			COALESCE(sum(fail), 0), COALESCE(sum(error), 0),
			COALESCE(sum(rtt_count), 0), COALESCE(sum(rtt_sum), 0), COALESCE(sum(loss_count), 0), COALESCE(sum(loss_sum), 0),
			COALESCE(sum(value_count), 0), COALESCE(sum(value_sum), 0)
		FROM `+relation+` WHERE probe_id = $1`, probeID).Scan(&s.buckets, &s.total, &s.ok, &s.fail, &s.errors,
		&s.rttCount, &s.rttSum, &s.lossCount, &s.lossSum, &s.valCnt, &s.valSum); err != nil {
		e.t.Fatalf("sums of %s: %v", relation, err)
	}
	return s
}

// viewBuckets counts the buckets of the probes in the view of the tier as
// the application in the scope of ctx.
func (e *resultEnv) viewBuckets(ctx context.Context, orgID, view string, probeIDs ...string) int {
	e.t.Helper()
	var n int
	if err := e.as(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM `+view+` WHERE probe_id = ANY ($1::uuid[])`, probeIDs).Scan(&n)
	}); err != nil {
		e.t.Fatalf("buckets of %s: %v", view, err)
	}
	return n
}

// utcDay is the start of the UTC day of t.
func utcDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func near(a, b float64) bool {
	return math.Abs(a-b) <= 1e-6*math.Max(1, math.Max(math.Abs(a), math.Abs(b)))
}

// TestProbeRollupsSumsMatchAcrossTiers: synthetic results over three full
// UTC days; after the refresh total, ok, fail, error, the RTT, loss and
// value sums and counters agree exactly between the raw data and the three
// tiers, and rtt_avg of the 1d view equals the mean of the raw RTTs.
func TestProbeRollupsSumsMatchAcrossTiers(t *testing.T) {
	e := newRollupEnv(t)
	f := e.f
	p := e.probeOfClient(f.OrgA, f.Client1, "10.72.0.1:22")
	start := utcDay(time.Now()).Add(-4 * 24 * time.Hour)
	// Every 7 minutes over three days: fractional RTTs, a loss on every
	// third result, a value on every fifth, statuses 0/1/2 and some results
	// without RTT (an error has none).
	seq := 0
	for at := start; at.Before(start.Add(72 * time.Hour)); at = at.Add(7 * time.Minute) {
		seq++
		status := int16(seq % 3)
		var rtt, loss, value any
		if status != 2 {
			rtt = float32(1.25 + float64(seq%97)*0.37)
		}
		if seq%3 == 0 {
			loss = float32(seq%10) / 10
		}
		if seq%5 == 0 {
			value = float64(seq) * 0.11
		}
		e.insertResult(f.OrgA, p, seq, at, status, rtt, loss, value, false)
	}
	e.refreshAll()
	raw := e.rawSums(p)
	if raw.total != int64(seq) || raw.ok == 0 || raw.fail == 0 || raw.errors == 0 || raw.lossCount == 0 || raw.valCnt == 0 {
		t.Fatalf("raw data %+v of %d results lacks a status or a column", raw, seq)
	}
	wantBuckets := map[string]int{"probe_result_5m": seq, "probe_result_1h": 72, "probe_result_1d": 3}
	for _, tier := range rollupTiers {
		got := e.aggSums(tier.name, p)
		if got.total != raw.total || got.ok != raw.ok || got.fail != raw.fail || got.errors != raw.errors ||
			got.rttCount != raw.rttCount || got.lossCount != raw.lossCount || got.valCnt != raw.valCnt {
			t.Errorf("%s counters %+v, raw %+v", tier.name, got, raw)
		}
		if !near(got.rttSum, raw.rttSum) || !near(got.lossSum, raw.lossSum) || !near(got.valSum, raw.valSum) {
			t.Errorf("%s sums rtt %v loss %v value %v, raw %v %v %v", tier.name, got.rttSum, got.lossSum, got.valSum,
				raw.rttSum, raw.lossSum, raw.valSum)
		}
		if got.buckets != wantBuckets[tier.name] {
			t.Errorf("%s has %d buckets, want %d", tier.name, got.buckets, wantBuckets[tier.name])
		}
	}
	// The 1d view: the mean over all three days from the sums equals the
	// raw mean; each daily rtt_avg equals the raw mean of that UTC day.
	var viewMean float64
	if err := e.as(f.OrgCtx(f.OrgA), f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT sum(rtt_sum) / sum(rtt_count) FROM probe_result_1d_v WHERE probe_id = $1`, p).Scan(&viewMean)
	}); err != nil {
		t.Fatal(err)
	}
	if rawMean := raw.rttSum / float64(raw.rttCount); !near(viewMean, rawMean) {
		t.Errorf("1d mean %v, raw mean %v", viewMean, rawMean)
	}
	// Each daily rtt_avg equals the raw mean of that UTC day.
	days := 0
	if err := e.as(f.OrgCtx(f.OrgA), f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT bucket, rtt_avg, loss_avg, value_avg, rtt_min, rtt_max FROM probe_result_1d_v
			WHERE probe_id = $1 ORDER BY bucket`, p)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var bucket time.Time
			var rttAvg, lossAvg, valueAvg *float64
			var rttMin, rttMax *float32
			if err := rows.Scan(&bucket, &rttAvg, &lossAvg, &valueAvg, &rttMin, &rttMax); err != nil {
				return err
			}
			if !bucket.Equal(utcDay(bucket)) {
				t.Errorf("1d bucket %s does not start at a UTC midnight", bucket)
			}
			var want float64
			var wantMin, wantMax float32
			if err := f.Admin.QueryRow(context.Background(), `SELECT avg(rtt_ms::float8), min(rtt_ms), max(rtt_ms) FROM probe_result
				WHERE probe_id = $1 AND NOT stale_epoch AND time >= $2 AND time < $2::timestamptz + INTERVAL '1 day'`,
				p, bucket).Scan(&want, &wantMin, &wantMax); err != nil {
				return err
			}
			if rttAvg == nil || !near(*rttAvg, want) {
				t.Errorf("rtt_avg of %s = %v, raw mean %v", bucket, rttAvg, want)
			}
			if rttMin == nil || rttMax == nil || *rttMin != wantMin || *rttMax != wantMax {
				t.Errorf("rtt min/max of %s = %v/%v, raw %v/%v", bucket, rttMin, rttMax, wantMin, wantMax)
			}
			if lossAvg == nil || valueAvg == nil {
				t.Errorf("loss_avg or value_avg of %s is NULL", bucket)
			}
			days++
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if days != 3 {
		t.Errorf("%d days in the 1d view, want 3", days)
	}
}

// TestProbeRollupsIgnoreStaleEpoch: results with stale_epoch are stored raw
// but counted in no tier; a bucket with only stale results does not exist,
// and the means of a bucket without RTTs are NULL.
func TestProbeRollupsIgnoreStaleEpoch(t *testing.T) {
	e := newRollupEnv(t)
	f := e.f
	p := e.probeOfClient(f.OrgA, f.Client1, "10.72.1.1:22")
	day := utcDay(time.Now()).Add(-2 * 24 * time.Hour)
	e.insertResult(f.OrgA, p, 1, day.Add(10*time.Hour), 0, float32(2), nil, nil, false)
	e.insertResult(f.OrgA, p, 2, day.Add(10*time.Hour+time.Minute), 1, float32(500), nil, nil, true)
	// An hour with stale results only.
	e.insertResult(f.OrgA, p, 3, day.Add(12*time.Hour), 2, float32(900), nil, nil, true)
	// A bucket with an error without RTT.
	e.insertResult(f.OrgA, p, 4, day.Add(14*time.Hour), 2, nil, nil, nil, false)
	e.refreshAll()
	want := map[string][2]int64{"probe_result_5m": {2, 2}, "probe_result_1h": {2, 2}, "probe_result_1d": {1, 2}}
	for _, tier := range rollupTiers {
		got := e.aggSums(tier.name, p)
		if int64(got.buckets) != want[tier.name][0] || got.total != want[tier.name][1] || got.fail != 0 || got.rttCount != 1 || got.rttSum != 2 {
			t.Errorf("%s: %+v, want %d buckets with 2 results, no fail and one RTT of 2 ms", tier.name, got, want[tier.name][0])
		}
	}
	var nullAvg bool
	if err := e.as(f.OrgCtx(f.OrgA), f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT rtt_avg IS NULL AND loss_avg IS NULL AND value_avg IS NULL FROM probe_result_5m_v
			WHERE probe_id = $1 AND bucket = $2`, p, day.Add(14*time.Hour)).Scan(&nullAvg)
	}); err != nil || !nullAvg {
		t.Errorf("means of the bucket without values: NULL %v, %v", nullAvg, err)
	}
}

// TestProbeRollupsLateResultWithinWindow: the tiers are materialized, then
// a result arrives late after its measurement time (collector buffer,
// PRB-07): 30 h, and the worst case of the 48 h promise, measured at
// 23:59:30 UTC and delivered 48 h later. With the windows of the policies
// (start_offset, end_offset and schedule_interval read from
// timescaledb_information.jobs) every tier has a run that covers the whole
// bucket of the measurement time, also when it runs as late as possible:
// a full schedule interval after the delivery has reached the tier below
// (or after the bucket is complete). The refresh of that run contains the
// result in every tier.
func TestProbeRollupsLateResultWithinWindow(t *testing.T) {
	e := newRollupEnv(t)
	f := e.f
	type policy struct{ start, end, schedule time.Duration }
	policies := map[string]policy{}
	for _, tier := range rollupTiers {
		var pol policy
		if err := f.Admin.QueryRow(context.Background(), `SELECT
				(extract(epoch FROM (j.config->>'start_offset')::interval) * 1e9)::bigint,
				(extract(epoch FROM (j.config->>'end_offset')::interval) * 1e9)::bigint,
				(extract(epoch FROM j.schedule_interval) * 1e9)::bigint
			FROM timescaledb_information.jobs j
			WHERE j.hypertable_schema = 'public' AND j.hypertable_name = $1 AND j.proc_name = 'policy_refresh_continuous_aggregate'`,
			tier.name).Scan(&pol.start, &pol.end, &pol.schedule); err != nil {
			t.Fatalf("refresh policy of %s: %v", tier.name, err)
		}
		policies[tier.name] = pol
	}
	now := time.Now().UTC()
	cases := []struct {
		name     string
		target   string
		measured time.Time
		delay    time.Duration
	}{
		{"30 h", "10.72.2.1:22", now.Add(-30 * time.Hour).Truncate(time.Second), 30 * time.Hour},
		{"48 h before midnight", "10.72.2.2:22", utcDay(now.Add(-48 * time.Hour)).Add(-30 * time.Second), 48 * time.Hour},
	}
	for _, c := range cases {
		p := e.probeOfClient(f.OrgA, f.Client1, c.target)
		// A result in the same hour that was delivered in time, then the
		// tiers are materialized as the policies would have done.
		e.insertResult(f.OrgA, p, 1, c.measured.Truncate(time.Hour), 0, float32(3), nil, nil, false)
		e.refreshAll()
		if got := e.aggSums("probe_result_1d", p); got.total != 1 {
			t.Fatalf("%s: before the late result the 1d tier holds %d results, want 1", c.name, got.total)
		}
		e.insertResult(f.OrgA, p, 2, c.measured, 0, float32(5), nil, nil, false)
		// ready is when the result has reached the tier below (the raw
		// table: the delivery).
		ready := c.measured.Add(c.delay)
		for _, tier := range rollupTiers {
			pol := policies[tier.name]
			var bucketStart time.Time
			if err := f.Admin.QueryRow(context.Background(), `SELECT time_bucket($1::interval, $2::timestamptz)`, tier.width, c.measured).
				Scan(&bucketStart); err != nil {
				t.Fatal(err)
			}
			width := map[string]time.Duration{"5 minutes": 5 * time.Minute, "1 hour": time.Hour, "1 day": 24 * time.Hour}[tier.width]
			// The latest first run after the result is ready and the
			// bucket is complete: one schedule interval later at most.
			from := ready
			if done := bucketStart.Add(width + pol.end); done.After(from) {
				from = done
			}
			run := from.Add(pol.schedule)
			windowStart, windowEnd := run.Add(-pol.start), run.Add(-pol.end)
			if windowStart.After(bucketStart) {
				t.Errorf("%s: %s: the latest run at %s starts its window at %s, after the bucket %s of the late result",
					c.name, tier.name, run, windowStart, bucketStart)
			}
			if windowEnd.After(now) {
				windowEnd = now
			}
			e.mustExec(`CALL refresh_continuous_aggregate('`+tier.name+`', $1::timestamptz, $2::timestamptz)`, windowStart, windowEnd)
			if got := e.aggSums(tier.name, p); got.total != 2 || got.rttSum != 8 {
				t.Errorf("%s: %s after the late result: %+v, want 2 results with an RTT sum of 8", c.name, tier.name, got)
			}
			ready = run
		}
	}
}

// TestProbeRollupViewsIsolateOrganizations: organization B sees no bucket
// of organization A in any view, also when asking for A's probe by id.
func TestProbeRollupViewsIsolateOrganizations(t *testing.T) {
	e := newRollupEnv(t)
	f := e.f
	clientB := e.scalar(`INSERT INTO client (organization_id, name, slug) VALUES ($1, 'wp972 b', 'wp972-b') RETURNING id::text`, f.OrgB)
	pa := e.probeOfClient(f.OrgA, f.Client1, "10.72.3.1:22")
	pb := e.probeOfClient(f.OrgB, clientB, "10.72.3.2:22")
	day := utcDay(time.Now()).Add(-2 * 24 * time.Hour)
	e.insertResult(f.OrgA, pa, 1, day.Add(time.Hour), 0, float32(1), nil, nil, false)
	e.insertResult(f.OrgA, pa, 2, day.Add(2*time.Hour), 0, float32(1), nil, nil, false)
	e.insertResult(f.OrgB, pb, 1, day.Add(time.Hour), 0, float32(1), nil, nil, false)
	e.refreshAll()
	for _, view := range []string{"probe_result_5m_v", "probe_result_1h_v", "probe_result_1d_v"} {
		if n := e.viewBuckets(f.OrgCtx(f.OrgB), f.OrgB, view, pa); n != 0 {
			t.Errorf("organization B sees %d buckets of organization A in %s", n, view)
		}
		if n := e.viewBuckets(f.OrgCtx(f.OrgB), f.OrgB, view, pa, pb); n != 1 {
			t.Errorf("organization B sees %d buckets in %s, want its own one", n, view)
		}
		if n := e.viewBuckets(f.OrgCtx(f.OrgA), f.OrgA, view, pa, pb); n == 0 {
			t.Errorf("organization A sees no bucket of its probe in %s", view)
		}
		var foreign int
		if err := e.as(f.OrgCtx(f.OrgB), f.OrgB, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM `+view+` WHERE organization_id <> $1`, f.OrgB).Scan(&foreign)
		}); err != nil || foreign != 0 {
			t.Errorf("%s shows %d rows of other organizations to B (%v)", view, foreign, err)
		}
	}
}

// TestProbeRollupViewsHonourScope: a client- or site-restricted principal
// sees only the buckets of the probes of its scope (and of probes without
// client or site); an org-wide one sees all.
func TestProbeRollupViewsHonourScope(t *testing.T) {
	e := newRollupEnv(t)
	f := e.f
	site1 := e.site(f.OrgA, f.Client1, "wp972-site-1")
	other := e.site(f.OrgA, f.Client1, "wp972-site-2")
	p1 := e.probeOfClient(f.OrgA, f.Client1, "10.72.4.1:22")
	p2 := e.probeOfClient(f.OrgA, f.Client2, "10.72.4.2:22")
	pSite := e.scalar(`INSERT INTO probe (organization_id, collector_group_id, client_id, site_id, type, target)
		VALUES ($1, (SELECT g.id FROM collector_group g WHERE g.organization_id = $1 AND g.client_id IS NULL ORDER BY g.id LIMIT 1),
		$2, $3, 'tcp', '10.72.4.3:22') RETURNING id::text`, f.OrgA, f.Client1, site1)
	day := utcDay(time.Now()).Add(-2 * 24 * time.Hour)
	for i, p := range []string{p1, p2, pSite} {
		e.insertResult(f.OrgA, p, 1, day.Add(time.Duration(i+1)*time.Hour), 0, float32(1), nil, nil, false)
	}
	e.refreshAll()
	all := []string{p1, p2, pSite}
	siteScope := func(siteID string) context.Context {
		scope := database.OrgWideScope(f.OrgA, f.User)
		scope.Sites = database.ScopeIDs(siteID)
		return database.ContextWithTenantScope(context.Background(), &scope)
	}
	for _, view := range []string{"probe_result_5m_v", "probe_result_1h_v", "probe_result_1d_v"} {
		for _, c := range []struct {
			who  string
			ctx  context.Context
			want []string
		}{
			{"org-wide", f.OrgCtx(f.OrgA), all},
			{"client 1", f.ClientCtx(f.Client1), []string{p1, pSite}},
			{"client 2", f.ClientCtx(f.Client2), []string{p2}},
			{"site 1", siteScope(site1), all},
			{"site 2", siteScope(other), []string{p1, p2}},
		} {
			for _, p := range all {
				visible := e.viewBuckets(c.ctx, f.OrgA, view, p) > 0
				if visible != contains(c.want, p) {
					t.Errorf("%s: %s sees the probe %s: %v, want %v", view, c.who, p, visible, !visible)
				}
			}
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// TestProbeRollupsNoDirectAccess: the application role has no privilege on
// the three aggregates, their materialization hypertables, chunks,
// compressed chunk relations and partial and direct views; a read ends with
// 42501. The views grant SELECT only.
func TestProbeRollupsNoDirectAccess(t *testing.T) {
	e := newRollupEnv(t)
	f := e.f
	p := e.probeOfClient(f.OrgA, f.Client1, "10.72.5.1:22")
	old := utcDay(time.Now()).Add(-10 * 24 * time.Hour)
	for i := range 4 {
		e.insertResult(f.OrgA, p, i+1, old.Add(time.Duration(i)*time.Hour), 0, float32(1), nil, nil, false)
	}
	e.insertResult(f.OrgA, p, 9, time.Now().UTC().Add(-26*time.Hour), 0, float32(1), nil, nil, false)
	e.refreshAll()
	e.mustExec(`SELECT compress_chunk(c, if_not_compressed => true) FROM show_chunks('probe_result_5m', older_than => INTERVAL '7 days') c`)
	rows, err := f.Admin.Query(context.Background(), `
		WITH agg AS (
			SELECT * FROM _timescaledb_catalog.continuous_agg
			 WHERE user_view_schema = 'public' AND user_view_name IN ('probe_result_5m', 'probe_result_1h', 'probe_result_1d')),
		mat AS (
			SELECT format('%I.%I', h.schema_name, h.table_name)::regclass AS rel
			  FROM agg a JOIN _timescaledb_catalog.hypertable h ON h.id = a.mat_hypertable_id),
		chunk AS (SELECT i.inhrelid::regclass AS rel FROM pg_inherits i JOIN mat m ON i.inhparent = m.rel)
		SELECT format('%I.%I', user_view_schema, user_view_name), 'aggregate' FROM agg
		UNION ALL SELECT rel::text, 'materialization' FROM mat
		UNION ALL SELECT rel::text, 'chunk' FROM chunk
		UNION ALL SELECT to_regclass(rel::text || '_compressed')::text, 'compressed chunk' FROM chunk
		           WHERE to_regclass(rel::text || '_compressed') IS NOT NULL
		UNION ALL SELECT format('%I.%I', partial_view_schema, partial_view_name), 'partial view' FROM agg
		UNION ALL SELECT format('%I.%I', direct_view_schema, direct_view_name), 'direct view' FROM agg`)
	if err != nil {
		t.Fatal(err)
	}
	var rels [][2]string
	for rows.Next() {
		var name, kind string
		if err = rows.Scan(&name, &kind); err != nil {
			t.Fatal(err)
		}
		rels = append(rels, [2]string{name, kind})
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, r := range rels {
		kinds[r[1]]++
		var privileged bool
		if err = f.Admin.QueryRow(context.Background(), `SELECT has_table_privilege('reticora_app', $1::regclass, 'SELECT')
			OR has_table_privilege('reticora_app', $1::regclass, 'INSERT') OR has_table_privilege('reticora_app', $1::regclass, 'DELETE')
			OR has_table_privilege('reticora_app', $1::regclass, 'TRUNCATE')`, r[0]).Scan(&privileged); err != nil {
			t.Fatal(err)
		}
		if privileged {
			t.Errorf("reticora_app has a privilege on %s %s", r[1], r[0])
		}
		err = e.as(f.OrgCtx(f.OrgA), f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
			var n int
			return tx.QueryRow(ctx, `SELECT count(*) FROM `+r[0]).Scan(&n)
		})
		if sqlState(err) != "42501" {
			t.Errorf("read of %s %s as the application: %v, want permission denied (42501)", r[1], r[0], err)
		}
	}
	if kinds["aggregate"] != 3 || kinds["materialization"] != 3 || kinds["chunk"] < 3 || kinds["compressed chunk"] < 1 ||
		kinds["partial view"] != 3 || kinds["direct view"] != 3 {
		t.Fatalf("internal relations %v: want three aggregates with materializations, chunks, a compressed chunk and their views", kinds)
	}
	for _, view := range []string{"probe_result_5m_v", "probe_result_1h_v", "probe_result_1d_v"} {
		got := e.scalar(`SELECT has_table_privilege('reticora_app', $1::regclass, 'SELECT')::text || ',' ||
			(has_table_privilege('reticora_app', $1::regclass, 'INSERT') OR has_table_privilege('reticora_app', $1::regclass, 'UPDATE')
			 OR has_table_privilege('reticora_app', $1::regclass, 'DELETE') OR has_table_privilege('reticora_app', $1::regclass, 'TRUNCATE'))::text`, view)
		if got != "true,false" {
			t.Errorf("privileges of reticora_app on %s (select,write): %s, want true,false", view, got)
		}
	}
	// The view still answers from the compressed chunk.
	if n := e.viewBuckets(f.OrgCtx(f.OrgA), f.OrgA, "probe_result_5m_v", p); n != 5 {
		t.Errorf("the 5m view shows %d buckets, want 5 (also from the compressed chunk)", n)
	}
}

// TestProbeRollupPolicies: refresh, compression and retention job of every
// tier with the values of ADR 0002 in timescaledb_information.jobs, and the
// compression settings of the materializations.
func TestProbeRollupPolicies(t *testing.T) {
	e := newRollupEnv(t)
	for _, tier := range rollupTiers {
		mat := e.scalar(`SELECT materialization_hypertable_name::text FROM timescaledb_information.continuous_aggregates
			WHERE view_schema = 'public' AND view_name = $1`, tier.name)
		check := func(proc, sql string, want string) {
			t.Helper()
			var got string
			if err := e.f.Admin.QueryRow(context.Background(), `SELECT (`+sql+`)::text FROM timescaledb_information.jobs
				WHERE hypertable_schema = 'public' AND hypertable_name = $1 AND proc_name = $2`, tier.name, proc).Scan(&got); err != nil {
				t.Errorf("%s %s: %v", tier.name, proc, err)
				return
			}
			if got != want {
				t.Errorf("%s %s: %s = %s, want %s", tier.name, proc, sql, got, want)
			}
		}
		check("policy_refresh_continuous_aggregate", `(config->>'start_offset')::interval = make_interval(secs => `+
			seconds(tier.start)+`) AND (config->>'end_offset')::interval = make_interval(secs => `+seconds(tier.end)+
			`) AND schedule_interval = make_interval(secs => `+seconds(tier.schedule)+`)`, "true")
		check("policy_compression", `(config->>'compress_after')::interval = INTERVAL '`+tier.compressAfter+`'`, "true")
		check("policy_retention", `(config->>'drop_after')::interval = INTERVAL '`+tier.retentionAfter+`'`, "true")
		settings := e.scalar(`SELECT string_agg(attname || ':' || COALESCE(segmentby_column_index::text, ''), ',' ORDER BY attname)
			FROM timescaledb_information.compression_settings WHERE hypertable_schema = '_timescaledb_internal' AND hypertable_name = $1`, mat)
		for _, want := range []string{"organization_id:1", "probe_id:2"} {
			if !strings.Contains(settings, want) {
				t.Errorf("%s compression settings %s lack segmentby %s", tier.name, settings, want)
			}
		}
		// Compression starts beyond the refresh window.
		if got := e.scalar(`SELECT (INTERVAL '` + tier.compressAfter + `' > make_interval(secs => ` + seconds(tier.start) + `))::text`); got != "true" {
			t.Errorf("%s compresses inside its refresh window", tier.name)
		}
	}
	// The source of the 5m tier excludes the stale results; 1h and 1d build
	// on the tier below.
	def := e.scalar(`SELECT view_definition FROM timescaledb_information.continuous_aggregates
		WHERE view_schema = 'public' AND view_name = 'probe_result_5m'`)
	if !strings.Contains(def, "stale_epoch") {
		t.Errorf("probe_result_5m does not filter stale_epoch: %s", def)
	}
	for parent, child := range map[string]string{"probe_result_5m": "probe_result_1h", "probe_result_1h": "probe_result_1d"} {
		if got := e.scalar(`SELECT count(*)::text FROM _timescaledb_catalog.continuous_agg c
			JOIN _timescaledb_catalog.continuous_agg p ON p.mat_hypertable_id = c.raw_hypertable_id
			WHERE c.user_view_name = $1 AND p.user_view_name = $2`, child, parent); got != "1" {
			t.Errorf("%s is not built on %s", child, parent)
		}
	}
}

func seconds(d time.Duration) string { return fmt.Sprintf("%d", int64(d/time.Second)) }

// TestProbeRollupsTruncateGuard: every materialization carries the trigger
// reticora_truncate_guard; TRUNCATE of the aggregate (TimescaleDB truncates
// the materialization behind it) as reticora_owner or reticora_app ends with
// 42501 even with the privilege, the rows stay.
func TestProbeRollupsTruncateGuard(t *testing.T) {
	e := newRollupEnv(t)
	f := e.f
	p := e.probeOfClient(f.OrgA, f.Client1, "10.72.6.1:22")
	e.insertResult(f.OrgA, p, 1, utcDay(time.Now()).Add(-2*24*time.Hour), 0, float32(1), nil, nil, false)
	e.refreshAll()
	for _, tier := range rollupTiers {
		mat := e.scalar(`SELECT format('%I.%I', materialization_hypertable_schema, materialization_hypertable_name)
			FROM timescaledb_information.continuous_aggregates WHERE view_schema = 'public' AND view_name = $1`, tier.name)
		if got := e.scalar(`SELECT count(*)::text FROM pg_trigger WHERE tgrelid = $1::regclass
			AND tgname = 'reticora_truncate_guard' AND tgenabled = 'O'`, mat); got != "1" {
			t.Errorf("%s (%s) has %s enabled truncate guards, want 1", tier.name, mat, got)
		}
		for _, role := range []string{"reticora_owner", "reticora_app"} {
			err := func() error {
				ctx := context.Background()
				tx, err := f.Admin.Begin(ctx)
				if err != nil {
					return err
				}
				defer func() { _ = tx.Rollback(ctx) }()
				// The privilege is granted inside the rolled-back
				// transaction, so only the trigger can refuse.
				for _, stmt := range []string{
					`GRANT USAGE ON SCHEMA _timescaledb_internal TO ` + role,
					`GRANT TRUNCATE ON ` + tier.name + `, ` + mat + ` TO ` + role,
					`SET LOCAL ROLE ` + role,
				} {
					if _, err = tx.Exec(ctx, stmt); err != nil {
						return err
					}
				}
				// TimescaleDB refuses a TRUNCATE of the materialization
				// itself; TRUNCATE on the aggregate truncates it.
				_, err = tx.Exec(ctx, `TRUNCATE `+tier.name)
				return err
			}()
			if sqlState(err) != "42501" || !strings.Contains(err.Error(), "may not truncate") {
				t.Errorf("TRUNCATE %s (%s) as %s: %v, want the truncate guard (42501)", tier.name, mat, role, err)
			}
		}
		if got := e.aggSums(tier.name, p); got.total != 1 {
			t.Errorf("%s lost its rows: %+v", tier.name, got)
		}
	}
}
