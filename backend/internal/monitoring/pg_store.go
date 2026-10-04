package monitoring

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGMetricStore implements MetricStore against the TimescaleDB metrics. As
// decided in docs/decisions/0001-timescale-rls.md, the application role has
// no privileges on the hypertable metric_sample or the continuous aggregate
// metric_sample_1h; it reads and writes through the security-barrier views
// metric_sample_v and metric_sample_1h_v (migration 000071), which restrict
// every row to the transaction's organization. Statements run in the
// caller's tenant transaction, and samples of a CI count only when the CI is
// visible under the caller's scope.
type PGMetricStore struct {
	pool   *pgxpool.Pool
	alerts *PGAlertStore
	// HourlyThreshold selects the continuous aggregate for step sizes at or
	// above this value; zero keeps the default of one hour.
	HourlyThreshold time.Duration
}

// NewPGMetricStore creates a PostgreSQL/TimescaleDB-backed metric store.
func NewPGMetricStore(pool *pgxpool.Pool) *PGMetricStore {
	return &PGMetricStore{pool: pool, alerts: NewPGAlertStore(pool), HourlyThreshold: time.Hour}
}

// AlertStore returns the PostgreSQL alert rule store paired with this metric
// store; the router uses it so the HTTP layer and the evaluator share one
// persistence for rules.
func (s *PGMetricStore) AlertStore() AlertStore { return s.alerts }

// Ingest inserts the samples through metric_sample_v. Every sample
// must belong to the caller's organization and name a CI visible under its
// scope (or no CI).
func (s *PGMetricStore) Ingest(ctx context.Context, metrics []Metric) error {
	if len(metrics) == 0 {
		return nil
	}
	orgID := metrics[0].OrgID
	return database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		batch := &pgx.Batch{}
		for _, m := range metrics {
			if m.OrgID != orgID {
				return fmt.Errorf("%w: %q", database.ErrTenantMismatch, m.OrgID)
			}
			ts := m.Timestamp
			if ts.IsZero() {
				ts = time.Now().UTC()
			}
			labels := m.Labels
			if labels == nil {
				labels = map[string]string{}
			}
			batch.Queue(
				`INSERT INTO metric_sample_v (time, organization_id, ci_id, metric_name, value, labels)
				 SELECT $1, $2, NULLIF($3, '')::uuid, $4, $5, $6
				 WHERE $3 = '' OR EXISTS (SELECT 1 FROM ci WHERE ci.id = NULLIF($3, '')::uuid)`,
				ts.UTC(), m.OrgID, m.CIID, m.Name, m.Value, labels,
			)
		}
		results := tx.SendBatch(ctx, batch)
		for _, m := range metrics {
			tag, err := results.Exec()
			if err != nil {
				_ = results.Close()
				return fmt.Errorf("ingest metric sample: %w", err)
			}
			if tag.RowsAffected() == 0 {
				_ = results.Close()
				return fmt.Errorf("ingest metric sample: ci %s not found", m.CIID)
			}
		}
		return results.Close()
	})
}

// Query returns raw samples in chronological order, or bucketed averages
// when q.Step is set. Steps at or above HourlyThreshold are served from the
// hourly continuous aggregate instead of scanning raw chunks.
func (s *PGMetricStore) Query(ctx context.Context, q MetricQuery) ([]MetricPoint, error) {
	var points []MetricPoint
	err := database.WithRequestTenant(ctx, s.pool, q.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		switch {
		case q.Step > 0 && s.HourlyThreshold > 0 && q.Step >= s.HourlyThreshold:
			points, err = queryHourly(ctx, tx, &q)
		case q.Step > 0:
			points, err = queryBucketed(ctx, tx, &q)
		default:
			points, err = queryRaw(ctx, tx, &q)
		}
		return err
	})
	return points, err
}

func queryRaw(ctx context.Context, tx pgx.Tx, q *MetricQuery) ([]MetricPoint, error) {
	where, args := metricWhere(*q)
	rows, err := tx.Query(ctx,
		`SELECT time, value FROM metric_sample_v WHERE `+where+` ORDER BY time ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("query metric samples: %w", err)
	}
	defer rows.Close()
	return scanPoints(rows)
}

func queryBucketed(ctx context.Context, tx pgx.Tx, q *MetricQuery) ([]MetricPoint, error) {
	where, args := metricWhere(*q)
	args = append(args, fmt.Sprintf("%f seconds", q.Step.Seconds()))
	rows, err := tx.Query(ctx,
		`SELECT time_bucket($`+fmt.Sprint(len(args))+`::interval, time) AS bucket, avg(value)
		 FROM metric_sample_v WHERE `+where+`
		 GROUP BY bucket ORDER BY bucket ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("query bucketed metric samples: %w", err)
	}
	defer rows.Close()
	return scanPoints(rows)
}

// queryHourly reads the pre-aggregated hourly rollup metric_sample_1h through
// its view. Long-range dashboards therefore touch a fraction of the raw data.
func queryHourly(ctx context.Context, tx pgx.Tx, q *MetricQuery) ([]MetricPoint, error) {
	where, args := metricWhere(*q)
	where = strings.ReplaceAll(where, "time", "bucket")
	args = append(args, fmt.Sprintf("%f seconds", q.Step.Seconds()))
	rows, err := tx.Query(ctx,
		`SELECT time_bucket($`+fmt.Sprint(len(args))+`::interval, bucket) AS rollup,
		        sum(avg_value * sample_count) / sum(sample_count) AS weighted_avg
		 FROM metric_sample_1h_v WHERE `+where+`
		 GROUP BY rollup ORDER BY rollup ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("query hourly metric rollup: %w", err)
	}
	defer rows.Close()
	return scanPoints(rows)
}

func metricWhere(q MetricQuery) (string, []any) {
	// The ci policy filters the subquery, so samples of CIs outside the
	// caller's scope drop out.
	where := []string{"organization_id = $1", "(ci_id IS NULL OR ci_id IN (SELECT id FROM ci))"}
	args := []any{q.OrgID}
	pos := 2
	if q.CIID != "" {
		where = append(where, fmt.Sprintf("ci_id = $%d::uuid", pos))
		args = append(args, q.CIID)
		pos++
	}
	if q.Name != "" {
		where = append(where, fmt.Sprintf("metric_name = $%d", pos))
		args = append(args, q.Name)
		pos++
	}
	if !q.From.IsZero() {
		where = append(where, fmt.Sprintf("time >= $%d", pos))
		args = append(args, q.From.UTC())
		pos++
	}
	if !q.To.IsZero() {
		where = append(where, fmt.Sprintf("time <= $%d", pos))
		args = append(args, q.To.UTC())
		pos++
	}
	return strings.Join(where, " AND "), args
}

func scanPoints(rows pgx.Rows) ([]MetricPoint, error) {
	points := make([]MetricPoint, 0)
	for rows.Next() {
		var p MetricPoint
		if err := rows.Scan(&p.Timestamp, &p.Value); err != nil {
			return nil, fmt.Errorf("scan metric point: %w", err)
		}
		p.Timestamp = p.Timestamp.UTC()
		points = append(points, p)
	}
	return points, rows.Err()
}
