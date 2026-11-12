package monitoring

// Spike of ADR 0002 (WP-943, PRB-06, NFR-11, MON-01, NFR-06, E-171, E-172):
// the storage of the probe results on the TimescaleDB of TEST_DATABASE_URL
// (2.31 in CI and Compose). Every test builds a prototype hypertable
// probe_result_spike with the schema of E-171 in a schema of its own, with
// the security-barrier view after migration 000071, and drops everything
// again; nothing of it is a migration (WP-945, WP-972 and WP-974 hold the
// final ones). The tests pin what docs/decisions/0002-probe-storage.md
// records:
//
//   - write rate of the application role through the view, INSERT … SELECT
//     FROM unnest(…) in blocks of 1,000 rows (TestProbeStorageSpikeWriteRate,
//     1 million rows; -short writes 50,000);
//   - bytes per row before and after compress_chunk with the density of a
//     6-hour chunk (TestProbeStorageSpikeCompressionRatio);
//   - ON CONFLICT DO NOTHING on UNIQUE (probe_id, collector_id, seq, time)
//     on a compressed chunk (TestProbeStorageSpikeOnConflictOnCompressedChunk);
//   - hierarchical continuous aggregates 5 min → 1 h → 1 d created WITH NO
//     DATA in one transaction, refreshed, equal to a direct aggregation, and
//     a retention policy on the 5-minute tier
//     (TestProbeStorageSpikeHierarchicalAggregates);
//   - no direct access of the application role to the hypertable, its
//     chunks, the compressed hypertable, the materializations and the
//     partial and direct views (TestProbeStorageSpikeAppRoleHasNoDirectAccess).

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

const (
	probeSpikeSchema = "p3_probe_spike"
	probeSpikeTable  = probeSpikeSchema + ".probe_result_spike"
	probeSpikeView   = probeSpikeSchema + ".probe_result_spike_v"
	probeSpikeOrg    = "943e0000-0000-4000-8000-00000000000a"
	probeSpikeOrgB   = "943e0000-0000-4000-8000-00000000000b"
	// probeSpikeCollector is the one collector of the synthetic rows.
	probeSpikeCollector = "943e0000-0000-4000-8000-0000000000c1"
)

// probeSpikeSetup creates the prototype table of E-171 (6-hour chunks,
// compression segmented by organization and probe, ordered by time,
// collector and sequence so the unique index is covered), the view of the
// application role after 000071 (organization as a sub-select, CASCADED
// CHECK OPTION) and the grants; the cleanup drops the schema and checks that
// no job is left.
func probeSpikeSetup(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	ctx, admin := spikeAdminPool(t)
	drop := func() {
		if _, err := admin.Exec(ctx, `DROP SCHEMA IF EXISTS `+probeSpikeSchema+` CASCADE`); err != nil {
			t.Errorf("drop probe spike schema: %v", err)
		}
		var jobs int
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM timescaledb_information.jobs WHERE hypertable_schema = $1`, probeSpikeSchema).Scan(&jobs); err != nil {
			t.Errorf("count spike jobs: %v", err)
		} else if jobs != 0 {
			t.Errorf("%d jobs of the spike are left", jobs)
		}
	}
	drop()
	t.Cleanup(drop)
	mustExec(ctx, t, admin,
		`CREATE SCHEMA `+probeSpikeSchema,
		// E-171: the narrow row. status 0 = ok, 1 = fail, 2 = error
		// (probewire.Result.status); loss in per mille.
		`CREATE TABLE `+probeSpikeTable+` (
			time            timestamptz NOT NULL,
			organization_id uuid NOT NULL,
			probe_id        uuid NOT NULL,
			collector_id    uuid NOT NULL,
			seq             bigint NOT NULL,
			status          smallint NOT NULL,
			code            smallint NOT NULL DEFAULT 0,
			rtt_ms          real NULL,
			loss            smallint NULL,
			value           double precision NULL,
			CONSTRAINT probe_result_spike_key UNIQUE (probe_id, collector_id, seq, time))`,
		`SELECT create_hypertable('`+probeSpikeTable+`', 'time', chunk_time_interval => INTERVAL '6 hours')`,
		`CREATE INDEX probe_result_spike_org_time ON `+probeSpikeTable+` (organization_id, time DESC)`,
		`ALTER TABLE `+probeSpikeTable+` SET (timescaledb.compress,
			timescaledb.compress_segmentby = 'organization_id, probe_id',
			timescaledb.compress_orderby = 'time DESC, collector_id, seq')`,
		`CREATE VIEW `+probeSpikeView+` WITH (security_barrier) AS
			SELECT time, organization_id, probe_id, collector_id, seq, status, code, rtt_ms, loss, value
			  FROM `+probeSpikeTable+`
			 WHERE organization_id = (SELECT current_setting('app.org_id')::uuid)
			WITH CASCADED CHECK OPTION`,
		`GRANT USAGE ON SCHEMA `+probeSpikeSchema+` TO reticora_app`,
		`GRANT SELECT, INSERT ON `+probeSpikeView+` TO reticora_app`,
	)
	return ctx, admin
}

// spikeAdminPool opens the maintenance pool of TEST_DATABASE_URL.
func spikeAdminPool(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()
	admin, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatalf("maintenance pool: %v", err)
	}
	t.Cleanup(admin.Close)
	return ctx, admin
}

// probeSpikeApp returns the pool of the application role and a runner for
// tenant transactions of the spike organization.
func probeSpikeApp(ctx context.Context, t *testing.T) func(fn func(context.Context, pgx.Tx) error) error {
	t.Helper()
	pool, err := database.NewPool(ctx, scopetest.AppDSN(t))
	if err != nil {
		t.Fatalf("application pool: %v", err)
	}
	t.Cleanup(pool.Close)
	scope := database.OrgWideScope(probeSpikeOrg, "")
	return func(fn func(context.Context, pgx.Tx) error) error {
		return database.WithTenant(ctx, pool, &scope, fn)
	}
}

// spikeBatch is one block of synthetic results as parallel arrays for
// unnest.
type spikeBatch struct {
	times  []time.Time
	probes []string
	seqs   []int64
	status []int16
	rtt    []float32
}

// spikeProbeID returns the id of synthetic probe n.
func spikeProbeID(n int) string {
	return fmt.Sprintf("943e0000-0000-4000-9000-%012x", n)
}

// spikeInsertSQL writes a block through the view of the application role:
// one statement per block, INSERT … SELECT FROM unnest(…) (E-171, WP-960).
const spikeInsertSQL = `INSERT INTO ` + probeSpikeView + ` (time, organization_id, probe_id, collector_id, seq, status, code, rtt_ms, loss, value)
	SELECT r.t, current_setting('app.org_id')::uuid, r.p, $5::uuid, r.s, r.st, 0, r.rtt, 0, NULL
	  FROM unnest($1::timestamptz[], $2::uuid[], $3::bigint[], $4::smallint[], $6::real[]) AS r(t, p, s, st, rtt)`

func (b *spikeBatch) args(conflict bool) (string, []any) {
	sql := spikeInsertSQL
	if conflict {
		sql += ` ON CONFLICT DO NOTHING`
	}
	return sql, []any{b.times, b.probes, b.seqs, b.status, probeSpikeCollector, b.rtt}
}

// spikeRows builds the results i in [from, to) of probes probes, results
// results each, 30 s apart from base: row i is result i / probes of probe i
// % probes, so a block holds consecutive results of many probes as a
// collector batch does.
func spikeRows(base time.Time, probes, from, to int) *spikeBatch {
	b := &spikeBatch{}
	for i := from; i < to; i++ {
		probe, k := i%probes, i/probes
		b.times = append(b.times, base.Add(time.Duration(k)*30*time.Second))
		b.probes = append(b.probes, spikeProbeID(probe))
		b.seqs = append(b.seqs, int64(i))
		status := int16(0)
		if i%50 == 0 {
			status = 1
		}
		b.status = append(b.status, status)
		b.rtt = append(b.rtt, float32(1+(i*7)%40)+0.25)
	}
	return b
}

// spikeChunkBase is the start of a 6-hour chunk two days back, so that the
// synthetic hours fall into one chunk that is old enough to compress.
func spikeChunkBase() time.Time {
	now := time.Now().UTC().Add(-48 * time.Hour)
	return now.Truncate(6 * time.Hour)
}

// TestProbeStorageSpikeWriteRate covers AK2: the application role writes 1
// million synthetic rows (10,000 probes, 100 results each) through the view
// in blocks of 1,000 rows. The rate must reach 5,000 rows/s on the CI
// database (design target 1,060/s, NFR-11: 353/s with reserve factor 3);
// the WAL per row is measured for the ADR.
func TestProbeStorageSpikeWriteRate(t *testing.T) {
	ctx, admin := probeSpikeSetup(t)
	inTenant := probeSpikeApp(ctx, t)
	const probes, perProbe, block = 10000, 100, 1000
	total := probes * perProbe
	if testing.Short() {
		total = 50 * block
	}
	// Live ingest: the 100 results of a probe span the last 50 minutes, in
	// chunks that no compression touches yet (compression starts after one
	// day; inserting into a compressed chunk is the slow path of late data,
	// see TestProbeStorageSpikeOnConflictOnCompressedChunk).
	base := time.Now().UTC().Add(-perProbe * 30 * time.Second)
	var walBefore, walAfter string
	if err := admin.QueryRow(ctx, `SELECT pg_current_wal_lsn()::text`).Scan(&walBefore); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for from := 0; from < total; from += block {
		b := spikeRows(base, probes, from, from+block)
		sql, args := b.args(false)
		if err := inTenant(func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, sql, args...)
			return err
		}); err != nil {
			t.Fatalf("block at %d: %v", from, err)
		}
	}
	elapsed := time.Since(start)
	if err := admin.QueryRow(ctx, `SELECT pg_current_wal_lsn()::text`).Scan(&walAfter); err != nil {
		t.Fatal(err)
	}
	var walBytes int64
	if err := admin.QueryRow(ctx, `SELECT pg_wal_lsn_diff($1::pg_lsn, $2::pg_lsn)::bigint`, walAfter, walBefore).Scan(&walBytes); err != nil {
		t.Fatal(err)
	}
	var rows int64
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM `+probeSpikeTable).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != int64(total) {
		t.Fatalf("%d rows written, want %d", rows, total)
	}
	rate := float64(total) / elapsed.Seconds()
	walPerRow := float64(walBytes) / float64(total)
	t.Logf("write rate: %d rows in %s = %.0f rows/s (blocks of %d, application role through the view)", total, elapsed.Round(time.Millisecond), rate, block)
	t.Logf("WAL: %d bytes = %.0f B/row; at 353/s %.0f MB/h, at 1,060/s %.0f MB/h",
		walBytes, walPerRow, walPerRow*353*3600/1e6, walPerRow*1060*3600/1e6)
	var tableBytes, indexBytes, totalBytes int64
	if err := admin.QueryRow(ctx, `SELECT COALESCE(sum(table_bytes), 0)::bigint, COALESCE(sum(index_bytes), 0)::bigint, COALESCE(sum(total_bytes), 0)::bigint
		FROM chunks_detailed_size('`+probeSpikeTable+`')`).Scan(&tableBytes, &indexBytes, &totalBytes); err != nil {
		t.Fatal(err)
	}
	t.Logf("uncompressed: heap %.1f B/row, indexes %.1f B/row, total %.1f B/row",
		float64(tableBytes)/float64(total), float64(indexBytes)/float64(total), float64(totalBytes)/float64(total))
	if rate < 5000 {
		t.Errorf("write rate %.0f rows/s, below the limit of 5,000 rows/s (NFR-11 design 1,060/s)", rate)
	}
}

// TestProbeStorageSpikeCompressionRatio covers AK2 (sizes): one 6-hour chunk
// with the density of production, 2,000 probes with a result every 30 s
// (720 per probe), measured before and after compress_chunk.
func TestProbeStorageSpikeCompressionRatio(t *testing.T) {
	ctx, admin := probeSpikeSetup(t)
	probes := 2000
	if testing.Short() {
		probes = 200
	}
	base := spikeChunkBase()
	// Server-side generation; status, code, RTT and loss vary like a
	// mostly healthy fleet (2 % failures, RTT 1–40 ms).
	mustExec(ctx, t, admin, fmt.Sprintf(`INSERT INTO `+probeSpikeTable+`
		SELECT '%s'::timestamptz + make_interval(secs => k * 30 + (p %% 30)),
		       '`+probeSpikeOrg+`'::uuid,
		       ('943e0000-0000-4000-9000-' || lpad(to_hex(p), 12, '0'))::uuid,
		       '`+probeSpikeCollector+`'::uuid,
		       k::bigint * %d + p,
		       CASE WHEN (p + k) %% 50 = 0 THEN 1 ELSE 0 END,
		       0,
		       (1 + ((p * 7 + k) %% 40))::real + random()::real,
		       CASE WHEN (p + k) %% 50 = 0 THEN 1000 ELSE 0 END,
		       NULL
		  FROM generate_series(0, %d) p, generate_series(0, 719) k`,
		base.Format(time.RFC3339), probes, probes-1))
	var chunks int
	var before, after, rows int64
	if err := admin.QueryRow(ctx, `SELECT count(*), sum(total_bytes)::bigint FROM chunks_detailed_size('`+probeSpikeTable+`')`).Scan(&chunks, &before); err != nil {
		t.Fatal(err)
	}
	if chunks != 1 {
		t.Fatalf("%d chunks, want the one 6-hour chunk", chunks)
	}
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM `+probeSpikeTable).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	// Heap and every index of the chunk per row: the unique key, the
	// (organization_id, time) index and the default time index of
	// create_hypertable.
	parts, err := admin.Query(ctx, `SELECT 'heap', pg_relation_size(h.inhrelid) FROM pg_inherits h WHERE h.inhparent = $1::regclass
		UNION ALL SELECT regexp_replace(i.indexrelid::regclass::text, '^.*\.', ''), pg_relation_size(i.indexrelid)
		  FROM pg_index i JOIN pg_inherits h ON h.inhrelid = i.indrelid WHERE h.inhparent = $1::regclass`, probeSpikeTable)
	if err != nil {
		t.Fatal(err)
	}
	for parts.Next() {
		var name string
		var size int64
		if err = parts.Scan(&name, &size); err != nil {
			t.Fatal(err)
		}
		t.Logf("  %s: %.1f B/row", name, float64(size)/float64(rows))
	}
	parts.Close()
	mustExec(ctx, t, admin, `SELECT compress_chunk(c) FROM show_chunks('`+probeSpikeTable+`') c`,
		// The policy of E-171 (compression after one day) is accepted with
		// the unique key and the settings above.
		`SELECT add_compression_policy('`+probeSpikeTable+`', compress_after => INTERVAL '1 day')`)
	var beforeStats int64
	if err := admin.QueryRow(ctx, `SELECT sum(before_compression_total_bytes)::bigint, sum(after_compression_total_bytes)::bigint
		FROM chunk_compression_stats('`+probeSpikeTable+`')`).Scan(&beforeStats, &after); err != nil {
		t.Fatal(err)
	}
	ratio := float64(before) / float64(after)
	perRowBefore, perRowAfter := float64(before)/float64(rows), float64(after)/float64(rows)
	t.Logf("compression: %d rows, before %d B (%.1f B/row; compression stats %d B), after %d B (%.1f B/row), ratio %.1f",
		rows, before, perRowBefore, beforeStats, after, perRowAfter, ratio)
	// Extrapolation to production (E-171): 30.5 million rows a day, a
	// 6-hour chunk of 7.6 million rows.
	t.Logf("extrapolated: %.2f GB/day uncompressed, %.2f GB/day compressed, 6-hour chunk %.2f GB uncompressed",
		perRowBefore*30.5e6/1e9, perRowAfter*30.5e6/1e9, perRowBefore*7.625e6/1e9)
	if after >= before || ratio < 3 {
		t.Errorf("compression ratio %.1f, want at least 3", ratio)
	}
	// The data stays readable through the view after the compression.
	inTenant := probeSpikeApp(ctx, t)
	var n int64
	if err := inTenant(func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM `+probeSpikeView+` WHERE probe_id = $1`, spikeProbeID(7)).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	if n != 720 {
		t.Errorf("view reads %d results of a probe from the compressed chunk, want 720", n)
	}
}

// TestProbeStorageSpikeOnConflictOnCompressedChunk covers AK3: INSERT … ON
// CONFLICT DO NOTHING on UNIQUE (probe_id, collector_id, seq, time) also
// holds on a compressed chunk: the double insert of 1,000 rows changes the
// row count by nothing and raises no error; half new rows add only those.
func TestProbeStorageSpikeOnConflictOnCompressedChunk(t *testing.T) {
	ctx, admin := probeSpikeSetup(t)
	inTenant := probeSpikeApp(ctx, t)
	base := spikeChunkBase()
	b := spikeRows(base, 100, 0, 1000)
	write := func(batch *spikeBatch) (int64, error) {
		var n int64
		sql, args := batch.args(true)
		err := inTenant(func(ctx context.Context, tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, sql, args...)
			n = tag.RowsAffected()
			return err
		})
		return n, err
	}
	if n, err := write(b); err != nil || n != 1000 {
		t.Fatalf("first insert: %d rows, %v", n, err)
	}
	mustExec(ctx, t, admin, `SELECT compress_chunk(c) FROM show_chunks('`+probeSpikeTable+`') c`)
	compressed := func() bool {
		var ok bool
		if err := admin.QueryRow(ctx, `SELECT bool_and(is_compressed) FROM timescaledb_information.chunks
			WHERE hypertable_schema = $1 AND hypertable_name = 'probe_result_spike'`, probeSpikeSchema).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !compressed() {
		t.Fatal("the chunk is not compressed")
	}
	count := func() int64 {
		var n int64
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM `+probeSpikeTable).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	start := time.Now()
	n, err := write(b)
	if err != nil {
		t.Fatalf("double insert into the compressed chunk: %v", err)
	}
	t.Logf("double insert of 1,000 rows into the compressed chunk: %s", time.Since(start).Round(time.Millisecond))
	if n != 0 || count() != 1000 {
		t.Errorf("double insert: %d rows inserted, %d rows in the table, want 0 and 1000", n, count())
	}
	// Half duplicates, half new rows of the same chunk.
	mixed := spikeRows(base, 100, 500, 1500)
	start = time.Now()
	if n, err = write(mixed); err != nil || n != 500 {
		t.Errorf("mixed insert: %d rows, %v, want 500 new", n, err)
	}
	t.Logf("mixed insert of 1,000 rows (500 new) into the compressed chunk: %s", time.Since(start).Round(time.Millisecond))
	if got := count(); got != 1500 {
		t.Errorf("%d rows after the mixed insert, want 1500", got)
	}
	// Without ON CONFLICT the duplicate is refused: the key holds.
	sql, args := b.args(false)
	if err := inTenant(func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, sql, args...)
		return err
	}); err == nil || !strings.Contains(err.Error(), "23505") {
		t.Errorf("duplicate without ON CONFLICT: %v, want 23505", err)
	}
}

// TestProbeStorageSpikeHierarchicalAggregates covers AK4: the continuous
// aggregates 5 min → 1 h → 1 d are created WITH NO DATA in one
// transaction, filled with refresh_continuous_aggregate and equal a direct
// aggregation (count, success ratio, mean, minimum and maximum of the RTT);
// a retention policy on the 5-minute aggregate drops buckets older than its
// period.
func TestProbeStorageSpikeHierarchicalAggregates(t *testing.T) {
	ctx, admin := probeSpikeSetup(t)
	const r5, r1h, r1d = probeSpikeSchema + ".probe_result_spike_5m", probeSpikeSchema + ".probe_result_spike_1h", probeSpikeSchema + ".probe_result_spike_1d"
	// 20 probes, a result every 10 minutes over 3 days ending 2 days ago,
	// and every hour 100 to 97 days back (older than the 90-day retention
	// of the 5-minute tier).
	end := time.Now().UTC().Add(-48 * time.Hour).Truncate(24 * time.Hour)
	mustExec(ctx, t, admin, fmt.Sprintf(`INSERT INTO `+probeSpikeTable+`
		SELECT ts, '`+probeSpikeOrg+`'::uuid, ('943e0000-0000-4000-9000-' || lpad(to_hex(p), 12, '0'))::uuid,
		       '`+probeSpikeCollector+`'::uuid, row_number() OVER (), CASE WHEN (p + extract(epoch FROM ts)::bigint / 600) %% 7 = 0 THEN 1 ELSE 0 END,
		       0, (1 + (p * 13 + extract(epoch FROM ts)::bigint / 600) %% 37)::real + random()::real, 0, NULL
		  FROM generate_series(0, 19) p,
		       (SELECT generate_series('%[1]s'::timestamptz - INTERVAL '3 days', '%[1]s'::timestamptz - INTERVAL '10 minutes', INTERVAL '10 minutes')
		        UNION ALL SELECT generate_series('%[1]s'::timestamptz - INTERVAL '100 days', '%[1]s'::timestamptz - INTERVAL '97 days', INTERVAL '1 hour')) s(ts)`,
		end.Format(time.RFC3339)))

	// One transaction: three tiers WITH NO DATA. Each tier keeps sums so
	// the next one aggregates exactly (the mean of means would be wrong).
	// The RTT sum is double precision: a real sum over fractional RTTs
	// drifts by more than the tolerance already in a 1-day bucket.
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`CREATE MATERIALIZED VIEW ` + r5 + ` WITH (timescaledb.continuous, timescaledb.materialized_only = true) AS
			SELECT time_bucket('5 minutes', time) AS bucket, organization_id, probe_id,
			       count(*) AS total, count(*) FILTER (WHERE status = 0) AS ok,
			       sum(rtt_ms::float8) AS rtt_sum, count(rtt_ms) AS rtt_n, min(rtt_ms) AS rtt_min, max(rtt_ms) AS rtt_max
			  FROM ` + probeSpikeTable + ` GROUP BY 1, 2, 3 WITH NO DATA`,
		`CREATE MATERIALIZED VIEW ` + r1h + ` WITH (timescaledb.continuous, timescaledb.materialized_only = true) AS
			SELECT time_bucket('1 hour', bucket) AS bucket, organization_id, probe_id,
			       sum(total) AS total, sum(ok) AS ok, sum(rtt_sum) AS rtt_sum, sum(rtt_n) AS rtt_n,
			       min(rtt_min) AS rtt_min, max(rtt_max) AS rtt_max
			  FROM ` + r5 + ` GROUP BY 1, 2, 3 WITH NO DATA`,
		`CREATE MATERIALIZED VIEW ` + r1d + ` WITH (timescaledb.continuous, timescaledb.materialized_only = true) AS
			SELECT time_bucket('1 day', bucket) AS bucket, organization_id, probe_id,
			       sum(total) AS total, sum(ok) AS ok, sum(rtt_sum) AS rtt_sum, sum(rtt_n) AS rtt_n,
			       min(rtt_min) AS rtt_min, max(rtt_max) AS rtt_max
			  FROM ` + r1h + ` GROUP BY 1, 2, 3 WITH NO DATA`,
	} {
		if _, err = tx.Exec(ctx, sql); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("create aggregate in one transaction: %v", err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatalf("commit the aggregates: %v", err)
	}
	for _, v := range []string{r5, r1h, r1d} {
		mustExec(ctx, t, admin, `CALL refresh_continuous_aggregate('`+v+`', NULL, NULL)`)
	}

	// The sums are double precision in every tier.
	for _, v := range []string{r5, r1h, r1d} {
		var typ string
		if err = admin.QueryRow(ctx, `SELECT format_type(atttypid, atttypmod) FROM pg_attribute WHERE attrelid = $1::regclass AND attname = 'rtt_sum'`, v).Scan(&typ); err != nil {
			t.Fatal(err)
		}
		if typ != "double precision" {
			t.Errorf("%s: rtt_sum is %s, want double precision", v, typ)
		}
	}

	// Compare every tier with a direct aggregation of the raw rows
	// (fractional RTTs, so the comparison checks the type of the sums).
	for _, tier := range []struct {
		view, width string
	}{{r5, "5 minutes"}, {r1h, "1 hour"}, {r1d, "1 day"}} {
		var buckets, mismatches int
		if err = admin.QueryRow(ctx, `WITH direct AS (
				SELECT time_bucket('`+tier.width+`', time) AS bucket, probe_id, count(*) AS total,
				       count(*) FILTER (WHERE status = 0) AS ok, avg(rtt_ms) AS rtt_avg, min(rtt_ms) AS rtt_min, max(rtt_ms) AS rtt_max
				  FROM `+probeSpikeTable+` GROUP BY 1, 2)
			SELECT count(*), count(*) FILTER (WHERE a.bucket IS NULL OR d.total <> a.total OR d.ok <> a.ok
			           OR abs(d.rtt_avg - a.rtt_sum / a.rtt_n) > 1e-6 OR d.rtt_min <> a.rtt_min OR d.rtt_max <> a.rtt_max)
			  FROM direct d LEFT JOIN `+tier.view+` a ON a.bucket = d.bucket AND a.probe_id = d.probe_id`).Scan(&buckets, &mismatches); err != nil {
			t.Fatalf("%s: %v", tier.view, err)
		}
		if buckets == 0 || mismatches != 0 {
			t.Errorf("%s: %d of %d buckets differ from the direct aggregation", tier.view, mismatches, buckets)
		}
		// Success ratio per probe over the whole range is the same in every tier.
		var ratio float64
		if err = admin.QueryRow(ctx, `SELECT sum(ok)::float8 / sum(total) FROM `+tier.view).Scan(&ratio); err != nil {
			t.Fatal(err)
		}
		var direct float64
		if err = admin.QueryRow(ctx, `SELECT avg((status = 0)::int)::float8 FROM `+probeSpikeTable).Scan(&direct); err != nil {
			t.Fatal(err)
		}
		if math.Abs(ratio-direct) > 1e-9 {
			t.Errorf("%s: success ratio %.6f, direct %.6f", tier.view, ratio, direct)
		}
		t.Logf("%s: %d buckets equal the direct aggregation", tier.view, buckets)
	}

	// Retention of the 5-minute tier: 90 days (E-172). The policy job run
	// by hand drops the buckets 97–100 days back and keeps the recent ones.
	var job int
	if err = admin.QueryRow(ctx, `SELECT add_retention_policy('`+r5+`', drop_after => INTERVAL '90 days')`).Scan(&job); err != nil {
		t.Fatalf("retention policy on the 5-minute aggregate: %v", err)
	}
	mustExec(ctx, t, admin, fmt.Sprintf(`CALL run_job(%d)`, job))
	var old, recent int
	if err = admin.QueryRow(ctx, `SELECT count(*) FILTER (WHERE bucket < now() - INTERVAL '90 days'), count(*) FILTER (WHERE bucket >= now() - INTERVAL '90 days') FROM `+r5).Scan(&old, &recent); err != nil {
		t.Fatal(err)
	}
	if old != 0 || recent == 0 {
		t.Errorf("after the retention job: %d buckets older than 90 days, %d recent; want 0 and some", old, recent)
	}
	// The coarser tiers keep the old days (400 days, E-172).
	var oldHours int
	if err = admin.QueryRow(ctx, `SELECT count(*) FROM `+r1h+` WHERE bucket < now() - INTERVAL '90 days'`).Scan(&oldHours); err != nil {
		t.Fatal(err)
	}
	if oldHours == 0 {
		t.Error("the 1-hour tier lost the days older than 90 days")
	}
}

// TestProbeStorageSpikeAppRoleHasNoDirectAccess covers AK5: after the
// revokes of 000071, applied to the spike's hypertable, chunks, compressed
// hypertable, the materializations and the partial and direct views of the
// aggregates, every direct access of the application role ends with
// permission denied; the view still answers.
func TestProbeStorageSpikeAppRoleHasNoDirectAccess(t *testing.T) {
	ctx, admin := probeSpikeSetup(t)
	agg := probeSpikeSchema + ".probe_result_spike_5m"
	// The production case: probe_result lives in public, where the default
	// privileges of 000056 grant every new table to the application role.
	// The spike schema has no such defaults, so the hypertable gets the
	// grant explicitly before anything is compressed.
	mustExec(ctx, t, admin,
		`GRANT ALL ON `+probeSpikeTable+` TO reticora_app`,
		`INSERT INTO `+probeSpikeTable+` SELECT now() - make_interval(hours => 30 + i), '`+probeSpikeOrg+`'::uuid,
			gen_random_uuid(), '`+probeSpikeCollector+`'::uuid, i, 0, 0, 1, 0, NULL FROM generate_series(1, 48) i`,
		`INSERT INTO `+probeSpikeTable+` SELECT now() - make_interval(hours => 30 + i), '`+probeSpikeOrgB+`'::uuid,
			gen_random_uuid(), '`+probeSpikeCollector+`'::uuid, 100 + i, 0, 0, 1, 0, NULL FROM generate_series(1, 48) i`,
		`SELECT compress_chunk(c) FROM show_chunks('`+probeSpikeTable+`', older_than => INTERVAL '1 day') c`,
	)
	// TimescaleDB 2.31 copies the ACL of the hypertable to <chunk>_compressed
	// when it compresses a chunk, and GRANT/REVOKE on the hypertable keep
	// reaching it: the compressed relations carry the grant now and lose it
	// with the revoke on the hypertable alone.
	compressedPrivileges := func() (privileged, total int) {
		t.Helper()
		if err := admin.QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE has_table_privilege('reticora_app', c.oid, 'SELECT')), count(*)
			  FROM pg_inherits i JOIN pg_class c ON c.oid = to_regclass(i.inhrelid::regclass::text || '_compressed')
			 WHERE i.inhparent = $1::regclass`, probeSpikeTable).Scan(&privileged, &total); err != nil {
			t.Fatal(err)
		}
		return privileged, total
	}
	if granted, total := compressedPrivileges(); total == 0 || granted != total {
		t.Errorf("after compressing a granted hypertable %d of %d compressed relations carry the grant, want all", granted, total)
	}
	mustExec(ctx, t, admin, `REVOKE ALL ON `+probeSpikeTable+` FROM reticora_app`)
	if granted, total := compressedPrivileges(); total == 0 || granted != 0 {
		t.Errorf("after the revoke on the hypertable %d of %d compressed relations keep the grant, want none", granted, total)
	}
	// A chunk compressed after the revoke inherits no grant: the revoke on
	// the hypertable must precede every compression and is never undone.
	_, before := compressedPrivileges()
	mustExec(ctx, t, admin,
		`INSERT INTO `+probeSpikeTable+` VALUES (now() - INTERVAL '5 days', '`+probeSpikeOrgB+`'::uuid,
			gen_random_uuid(), '`+probeSpikeCollector+`'::uuid, 999, 0, 0, 1, 0, NULL)`,
		`SELECT compress_chunk(c, if_not_compressed => true) FROM show_chunks('`+probeSpikeTable+`', older_than => INTERVAL '1 day') c`,
	)
	if granted, total := compressedPrivileges(); total <= before || granted != 0 {
		t.Errorf("after a later compression %d of %d compressed relations carry a grant (before %d), want a new one without", granted, total, before)
	}
	mustExec(ctx, t, admin,
		`CREATE MATERIALIZED VIEW `+agg+` WITH (timescaledb.continuous) AS
			SELECT time_bucket('5 minutes', time) AS bucket, organization_id, probe_id, count(*) AS total
			  FROM `+probeSpikeTable+` GROUP BY 1, 2, 3 WITH NO DATA`,
		`CALL refresh_continuous_aggregate('`+agg+`', NULL, NULL)`,
	)
	// The internal relations, found as 000071 finds them: the hypertables
	// (a compressed hypertable exists only up to TimescaleDB 2.2x; 2.31
	// keeps the compressed data in a relation <chunk>_compressed per chunk,
	// which does not inherit from anything), their chunks through
	// inheritance, the compressed relation of each chunk by name, and the
	// partial and direct views of the aggregate.
	rows, err := admin.Query(ctx, `
		WITH hyper AS (
			SELECT id, format('%I.%I', schema_name, table_name)::regclass AS rel
			  FROM _timescaledb_catalog.hypertable
			 WHERE id IN (SELECT id FROM _timescaledb_catalog.hypertable WHERE schema_name = $1 AND table_name = 'probe_result_spike'
			        UNION SELECT compressed_hypertable_id FROM _timescaledb_catalog.hypertable WHERE schema_name = $1 AND table_name = 'probe_result_spike'
			        UNION SELECT mat_hypertable_id FROM _timescaledb_catalog.continuous_agg WHERE user_view_schema = $1)),
		chunk AS (SELECT i.inhrelid::regclass AS rel FROM pg_inherits i JOIN hyper h ON i.inhparent = h.rel)
		SELECT rel::text, 'hypertable' FROM hyper
		UNION ALL SELECT rel::text, 'chunk' FROM chunk
		UNION ALL SELECT to_regclass(rel::text || '_compressed')::text, 'compressed chunk' FROM chunk
		           WHERE to_regclass(rel::text || '_compressed') IS NOT NULL
		UNION ALL SELECT format('%I.%I', a.partial_view_schema, a.partial_view_name), 'partial view'
		  FROM _timescaledb_catalog.continuous_agg a WHERE a.user_view_schema = $1
		UNION ALL SELECT format('%I.%I', a.direct_view_schema, a.direct_view_name), 'direct view'
		  FROM _timescaledb_catalog.continuous_agg a WHERE a.user_view_schema = $1
		UNION ALL SELECT format('%I.%I', a.user_view_schema, a.user_view_name), 'aggregate'
		  FROM _timescaledb_catalog.continuous_agg a WHERE a.user_view_schema = $1`, probeSpikeSchema)
	if err != nil {
		t.Fatal(err)
	}
	var relations [][2]string
	for rows.Next() {
		var name, kind string
		if err = rows.Scan(&name, &kind); err != nil {
			t.Fatal(err)
		}
		relations = append(relations, [2]string{name, kind})
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, r := range relations {
		kinds[r[1]]++
	}
	// The hypertable and the materialization (and on TimescaleDB up to 2.2x
	// the compressed hypertable); raw and materialized chunks, the
	// compressed relations; partial and direct view.
	if kinds["hypertable"] < 2 || kinds["chunk"] < 2 || kinds["compressed chunk"] < 1 ||
		kinds["partial view"] != 1 || kinds["direct view"] != 1 || kinds["aggregate"] != 1 {
		t.Fatalf("internal relations %v, want hypertables, chunks and the views of the aggregate", kinds)
	}
	// Default privileges (000056) grant new relations in public to the
	// application role; the spike grants every relation explicitly (the
	// worst case), then revokes as 000071 does, the compressed relations
	// included.
	for _, r := range relations {
		mustExec(ctx, t, admin, `GRANT ALL ON `+r[0]+` TO reticora_app`)
	}
	for _, r := range relations {
		mustExec(ctx, t, admin, `REVOKE ALL ON `+r[0]+` FROM reticora_app`)
	}
	inTenant := probeSpikeApp(ctx, t)
	for _, r := range relations {
		stmts := []string{`SELECT 1 FROM ` + r[0] + ` LIMIT 1`}
		if !strings.HasSuffix(r[1], "view") && r[1] != "aggregate" {
			// Views of the aggregate are not updatable; tables are also
			// tried for writing.
			stmts = append(stmts, `DELETE FROM `+r[0])
		}
		for _, stmt := range stmts {
			err := inTenant(func(ctx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(ctx, stmt)
				return err
			})
			if err == nil || !strings.Contains(err.Error(), "permission denied") {
				t.Errorf("%s %s: %v, want permission denied", r[1], stmt, err)
			}
		}
	}
	// The view answers with the own organization only, also from the
	// compressed chunks.
	var n, orgs int
	if err := inTenant(func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*), count(DISTINCT organization_id) FROM `+probeSpikeView).Scan(&n, &orgs)
	}); err != nil {
		t.Fatal(err)
	}
	if n != 48 || orgs != 1 {
		t.Errorf("view returns %d rows of %d organizations, want 48 of one", n, orgs)
	}
	t.Logf("no direct access to %d internal relations: %v", len(relations), kinds)
}
