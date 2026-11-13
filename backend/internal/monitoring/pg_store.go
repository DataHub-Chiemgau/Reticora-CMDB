package monitoring

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
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

// HealthRefresher returns the refresher of ci.health over this store's
// database (DB-05).
func (s *PGMetricStore) HealthRefresher() HealthRefresher { return &PGHealthRefresher{pool: s.pool} }

// HealthWindow is how old the latest sample of a metric may be to count for
// the health of a CI; older CIs become unknown.
const HealthWindow = 15 * time.Minute

// PGHealthRefresher computes ci.health of every organization with the
// database function ci_refresh_health (migration 000090), each in its
// organization's tenant transaction.
type PGHealthRefresher struct {
	pool *pgxpool.Pool
}

// RefreshHealth implements HealthRefresher.
func (h *PGHealthRefresher) RefreshHealth(ctx context.Context) error {
	ids, err := database.OrganizationIDs(ctx, h.pool)
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range ids {
		scope := database.OrgWideScope(id, "")
		err := database.WithTenant(ctx, h.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
			var changed int
			return tx.QueryRow(ctx, `SELECT ci_refresh_health($1)`, HealthWindow).Scan(&changed)
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("refresh health of %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

// Ingest inserts the samples through metric_sample_v. Every sample
// must belong to the caller's organization and name a CI visible under its
// scope (or no CI). The batch is stored whole or not at all: a sample whose
// CI is unknown, of another organization or outside the scope rolls back
// the batch with an *UnknownCIError naming that ci_id (TEN-06, F-394). The
// missing CIs are found with one statement first; the samples go in with
// one bulk statement (InsertSamplesTx, WP-976).
func (s *PGMetricStore) Ingest(ctx context.Context, metrics []Metric) error {
	if len(metrics) == 0 {
		return nil
	}
	orgID := metrics[0].OrgID
	return database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		// The ids in the canonical lower-case form that id::text returns
		// (api.IsUUID also takes upper-case hex); errors name the id as
		// the client sent it.
		ids := make([]string, 0, len(metrics))
		given := map[string]string{}
		for i := range metrics {
			m := &metrics[i]
			if m.OrgID != orgID {
				return fmt.Errorf("%w: %q", database.ErrTenantMismatch, m.OrgID)
			}
			id := strings.ToLower(m.CIID)
			if id == "" || given[id] != "" {
				continue
			}
			if !api.IsUUID(m.CIID) {
				// A malformed ci_id is a bad request (400), as with the
				// cast of the former per-row insert, not an unknown CI.
				return &InvalidMetricsError{Detail: "ci_id must be a UUID"}
			}
			given[id] = m.CIID
			ids = append(ids, id)
		}
		if len(ids) > 0 {
			rows, err := tx.Query(ctx, `SELECT id::text FROM ci WHERE id = ANY ($1::uuid[])`, ids)
			if err != nil {
				return fmt.Errorf("ingest metric sample: %w", err)
			}
			visible, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return fmt.Errorf("ingest metric sample: %w", err)
			}
			found := make(map[string]bool, len(visible))
			for _, id := range visible {
				found[id] = true
			}
			for _, id := range ids {
				if !found[id] {
					return &UnknownCIError{CIID: given[id]}
				}
			}
		}
		n, err := InsertSamplesTx(ctx, tx, metrics)
		if err != nil {
			return err
		}
		if n != len(metrics) {
			return &UnknownCIError{CIID: firstCI(metrics)}
		}
		return nil
	})
}

// firstCI names the first ci_id of the samples (the refusal of a batch a
// concurrent delete shortened).
func firstCI(metrics []Metric) string {
	for i := range metrics {
		if metrics[i].CIID != "" {
			return metrics[i].CIID
		}
	}
	return ""
}

// InsertSamplesTx writes the samples in tx with one statement over
// metric_sample_v (WP-976, MON-02): INSERT … SELECT … FROM unnest(…). A
// sample whose CI the scope of the transaction does not see is not
// written; it returns the number of written rows. Every sample belongs to
// the organization of the first one.
func InsertSamplesTx(ctx context.Context, tx pgx.Tx, metrics []Metric) (int, error) {
	if len(metrics) == 0 {
		return 0, nil
	}
	orgID := metrics[0].OrgID
	times := make([]time.Time, len(metrics))
	cis := make([]*string, len(metrics))
	names := make([]string, len(metrics))
	values := make([]float64, len(metrics))
	labels := make([]map[string]string, len(metrics))
	now := time.Now().UTC()
	for i := range metrics {
		m := &metrics[i]
		if m.OrgID != orgID {
			return 0, fmt.Errorf("%w: %q", database.ErrTenantMismatch, m.OrgID)
		}
		times[i] = m.Timestamp.UTC()
		if m.Timestamp.IsZero() {
			times[i] = now
		}
		if m.CIID != "" {
			ci := m.CIID
			cis[i] = &ci
		}
		names[i], values[i] = m.Name, m.Value
		labels[i] = m.Labels
		if labels[i] == nil {
			labels[i] = map[string]string{}
		}
	}
	tag, err := tx.Exec(ctx, `INSERT INTO metric_sample_v (time, organization_id, ci_id, metric_name, value, labels)
		SELECT u.time, $1, u.ci_id, u.name, u.value, u.labels
		  FROM unnest($2::timestamptz[], $3::uuid[], $4::text[], $5::float8[], $6::jsonb[]) AS u (time, ci_id, name, value, labels)
		 WHERE u.ci_id IS NULL OR EXISTS (SELECT 1 FROM ci WHERE ci.id = u.ci_id)`,
		orgID, times, cis, names, values, labels)
	if err != nil {
		return 0, fmt.Errorf("ingest metric samples: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// Query returns raw samples in chronological order, or bucketed averages
// when q.Step is set. Steps at or above HourlyThreshold are served from the
// hourly continuous aggregate instead of scanning raw chunks.
func (s *PGMetricStore) Query(ctx context.Context, q *MetricQuery) ([]MetricPoint, error) {
	var points []MetricPoint
	err := database.WithRequestTenant(ctx, s.pool, q.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		switch {
		case q.Step > 0 && s.HourlyThreshold > 0 && q.Step >= s.HourlyThreshold:
			points, err = queryHourly(ctx, tx, q)
		case q.Step > 0:
			points, err = queryBucketed(ctx, tx, q)
		default:
			points, err = queryRaw(ctx, tx, q)
		}
		return err
	})
	return points, err
}

func queryRaw(ctx context.Context, tx pgx.Tx, q *MetricQuery) ([]MetricPoint, error) {
	where, args := metricWhere(q)
	rows, err := tx.Query(ctx,
		`SELECT time, value FROM metric_sample_v WHERE `+where+` ORDER BY time ASC, value ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("query metric samples: %w", err)
	}
	defer rows.Close()
	return scanPoints(rows)
}

func queryBucketed(ctx context.Context, tx pgx.Tx, q *MetricQuery) ([]MetricPoint, error) {
	where, args := metricWhere(q)
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
	where, args := metricWhere(q)
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

func metricWhere(q *MetricQuery) (clause string, args []any) {
	// The ci policy filters the subquery, so samples of CIs outside the
	// caller's scope drop out.
	where := []string{"organization_id = $1", "(ci_id IS NULL OR ci_id IN (SELECT id FROM ci))"}
	args = []any{q.OrgID}
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

// maxCIMetricNames bounds the series of one CI.
const maxCIMetricNames = 50

// MetricNames implements MetricNamer.
func (s *PGMetricStore) MetricNames(ctx context.Context, orgID, ciID string, since time.Time) ([]string, error) {
	var names []string
	err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		// The ci policy filters the subquery as in metricWhere, so a CI
		// outside the caller's scope lists no series (TEN-06).
		rows, err := tx.Query(ctx, `SELECT DISTINCT metric_name FROM metric_sample_v
			WHERE organization_id = $1 AND ci_id = $2::uuid AND ci_id IN (SELECT id FROM ci)
			  AND time >= $3 ORDER BY metric_name LIMIT $4`,
			orgID, ciID, since, maxCIMetricNames)
		if err != nil {
			return fmt.Errorf("list metric names: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				return err
			}
			names = append(names, n)
		}
		return rows.Err()
	})
	return names, err
}

// CIVisible implements CIVisibility: the ci policy decides under the
// caller's scope. A malformed id names no CI and never reaches SQL.
func (s *PGMetricStore) CIVisible(ctx context.Context, orgID, ciID string) (bool, error) {
	if !api.IsUUID(ciID) {
		return false, nil
	}
	var visible bool
	err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ci WHERE id = $1::uuid)`, ciID).Scan(&visible)
	})
	return visible, err
}

// LatestBySeries implements SeriesStore (WP-984, WP-985): in one statement
// the latest sample of every series (CI and the values of the group_by
// labels) of the metric in the window and its metric context (value, avg,
// max, min, delta, rate over q.Window back from the latest sample), in the
// tenant transaction of the organization through metric_sample_v. Samples
// without CI form no series; the scope narrows the CIs (ids, types with
// their subtypes, client, site) and the labels (equality), and only CIs
// visible under the scope of the transaction count. The increase of the
// rate adds every rise and, on a drop (counter reset), the new value.
func (s *PGMetricStore) LatestBySeries(ctx context.Context, q *SeriesQuery) ([]SeriesPoint, error) {
	args := []any{q.OrgID, q.Name, q.From.UTC(), q.To.UTC(), groupByOf(q.GroupBy), intervalLiteral(q.Window)}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	where := []string{"s.organization_id = $1", "s.metric_name = $2", "s.time >= $3", "s.time <= $4", "s.ci_id IS NOT NULL"}
	ciWhere := []string{"c.id = s.ci_id"}
	if len(q.Scope.CIIDs) > 0 {
		where = append(where, "s.ci_id = ANY("+arg(q.Scope.CIIDs)+"::uuid[])")
	}
	if len(q.Scope.CITypeIDs) > 0 {
		ciWhere = append(ciWhere, "c.ci_type_id IN (SELECT st.id FROM unnest("+arg(q.Scope.CITypeIDs)+
			"::uuid[]) AS t(id), LATERAL ci_type_subtree(t.id) st)")
	}
	if q.Scope.ClientID != "" {
		ciWhere = append(ciWhere, "c.client_id = "+arg(q.Scope.ClientID)+"::uuid")
	}
	if q.Scope.SiteID != "" {
		ciWhere = append(ciWhere, "c.site_id = "+arg(q.Scope.SiteID)+"::uuid")
	}
	if len(q.Scope.Labels) > 0 {
		where = append(where, "s.labels @> "+arg(q.Scope.Labels)+"::jsonb")
	}
	// The ci policy filters the semi-join, so samples of CIs outside the
	// transaction's scope drop out (as in metricWhere).
	where = append(where, "EXISTS (SELECT 1 FROM ci c WHERE "+strings.Join(ciWhere, " AND ")+")")
	query := `WITH x AS (
		SELECT s.ci_id, s.time, s.value,
		       COALESCE((SELECT jsonb_object_agg(k, s.labels->>k) FROM unnest($5::text[]) AS k WHERE s.labels ? k),
		                '{}'::jsonb) AS series
		  FROM metric_sample_v s
		 WHERE ` + strings.Join(where, " AND ") + `
	), w AS (
		SELECT x.*, max(x.time) OVER (PARTITION BY x.ci_id, x.series) AS last_time FROM x
	), y AS (
		SELECT w.ci_id, w.series, w.time, w.value,
		       lag(w.value) OVER (PARTITION BY w.ci_id, w.series ORDER BY w.time) AS prev
		  FROM w WHERE w.time >= w.last_time - $6::interval
	)
	SELECT y.ci_id::text, y.series, max(y.time),
	       (array_agg(y.value ORDER BY y.time DESC))[1],
	       avg(y.value), max(y.value), min(y.value),
	       (array_agg(y.value ORDER BY y.time DESC))[1] - (array_agg(y.value ORDER BY y.time ASC))[1],
	       COALESCE(sum(CASE WHEN y.prev IS NULL THEN 0 WHEN y.value >= y.prev THEN y.value - y.prev ELSE y.value END), 0),
	       EXTRACT(EPOCH FROM max(y.time) - min(y.time))::float8,
	       count(*)
	  FROM y GROUP BY y.ci_id, y.series ORDER BY y.ci_id, y.series`
	var out []SeriesPoint
	err := database.WithRequestTenant(ctx, s.pool, q.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("latest samples by series: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				p              SeriesPoint
				increase, span float64
			)
			c := &p.Context
			if err := rows.Scan(&p.CIID, &p.Labels, &p.Timestamp, &p.Value, &c.Avg, &c.Max, &c.Min, &c.Delta,
				&increase, &span, &c.Samples); err != nil {
				return fmt.Errorf("scan series sample: %w", err)
			}
			c.Value = p.Value
			if span > 0 {
				rate := increase / span
				c.Rate = &rate
			}
			if p.Labels == nil {
				p.Labels = map[string]string{}
			}
			p.Timestamp = p.Timestamp.UTC()
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}
